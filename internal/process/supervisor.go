// Package process provides process lifecycle management for MCP servers.
package process

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"maps"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/events"
	"github.com/Bigsy/mcpmu/internal/mcp"
	"github.com/Bigsy/mcpmu/internal/oauth"
)

const (
	// GracefulShutdownTimeout is how long to wait for SIGTERM before SIGKILL.
	GracefulShutdownTimeout = 5 * time.Second

	// MaxInitRetries is the maximum number of MCP initialization attempts.
	MaxInitRetries = 3

	// InitAttemptTimeout is the maximum duration of one MCP initialization attempt.
	InitAttemptTimeout = 30 * time.Second

	// InitRetryBaseDelay is the base delay between retry attempts.
	InitRetryBaseDelay = 500 * time.Millisecond
)

// ErrNeedsLogin indicates that an HTTP MCP server supports OAuth but no usable
// credentials are available. The handle remains available for OAuth metadata,
// but it is not a running MCP connection and may be replaced by the next use.
var ErrNeedsLogin = errors.New("oauth login required")

// Supervisor manages MCP server process lifecycles.
type Supervisor struct {
	bus                     *events.Bus
	handles                 map[InstanceID]*Handle
	pidTracker              *PIDTracker
	credStore               oauth.CredentialStore
	tokenManager            *oauth.TokenManager
	toolCache               *config.ToolCache
	globalOAuthCallbackPort *int
	mu                      sync.RWMutex
	lifecycleMu             sync.Mutex
	lifecycleLocks          map[InstanceID]*sync.Mutex
	generations             map[InstanceID]uint64
	initAttemptTimeout      time.Duration
	initRetryBaseDelay      time.Duration

	// observer receives immutable discovery/lifecycle results and upstream
	// notifications. Set before clients start; read under observerMu.
	observerMu sync.RWMutex
	observer   Observer
}

// SetToolCache sets the tool cache for token counting.
func (s *Supervisor) SetToolCache(tc *config.ToolCache) {
	s.toolCache = tc
}

// SetObserver installs the Core observer. It must be called before Start.
func (s *Supervisor) SetObserver(observer Observer) {
	s.observerMu.Lock()
	s.observer = observer
	s.observerMu.Unlock()
}

// installNotificationHandler wires the observer into a client. The callback
// only hands off immutable data; Core performs refresh work asynchronously.
func (s *Supervisor) installNotificationHandler(handle *Handle, client *mcp.Client) {
	s.observerMu.RLock()
	observer := s.observer
	s.observerMu.RUnlock()
	if observer == nil {
		return
	}
	client.SetNotificationHandler(func(method string, params json.RawMessage) {
		observer.OnUpstreamNotification(UpstreamNotification{
			Instance:   handle.instance,
			Generation: handle.generation,
			Method:     method,
			Params:     append(json.RawMessage(nil), params...),
			Upstream:   true,
		})
	})
}

// SupervisorOptions configures a Supervisor.
type SupervisorOptions struct {
	// CredentialStoreMode specifies the OAuth credential store mode.
	// If empty, defaults to "auto".
	CredentialStoreMode string

	// PIDTrackerDir overrides the directory used for the PID tracking file.
	// If empty, the default ~/.config/mcpmu/ directory is used.
	PIDTrackerDir string

	// PIDFilePrefix labels this owner process's registry file by manager mode.
	// Unique owner identity, not the prefix, prevents concurrent clobbering.
	PIDFilePrefix string

	// GlobalOAuthCallbackPort is the global fallback OAuth callback port.
	// Per-server oauth.callback_port takes precedence over this.
	GlobalOAuthCallbackPort *int

	// InitAttemptTimeout overrides the timeout for each MCP initialization
	// attempt. If non-positive, the package default is used.
	InitAttemptTimeout time.Duration

	// InitRetryBaseDelay overrides the base exponential backoff between MCP
	// initialization attempts. If non-positive, the package default is used.
	InitRetryBaseDelay time.Duration
}

// NewSupervisor creates a new process supervisor.
// It also cleans up any orphan processes from previous runs.
func NewSupervisor(bus *events.Bus) *Supervisor {
	return NewSupervisorWithOptions(bus, SupervisorOptions{})
}

// NewSupervisorWithOptions creates a new process supervisor with options.
func NewSupervisorWithOptions(bus *events.Bus, opts SupervisorOptions) *Supervisor {
	initAttemptTimeout := opts.InitAttemptTimeout
	if initAttemptTimeout <= 0 {
		initAttemptTimeout = InitAttemptTimeout
	}
	initRetryBaseDelay := opts.InitRetryBaseDelay
	if initRetryBaseDelay <= 0 {
		initRetryBaseDelay = InitRetryBaseDelay
	}

	var pidTracker *PIDTracker
	var err error
	if opts.PIDTrackerDir != "" {
		pidTracker, err = NewPIDTrackerInDir(opts.PIDTrackerDir, opts.PIDFilePrefix)
	} else {
		pidTracker, err = NewPIDTrackerInDir("", opts.PIDFilePrefix)
	}
	if err != nil {
		log.Printf("Warning: failed to create PID tracker: %v", err)
	} else {
		// Clean up orphans on startup
		if killed := pidTracker.CleanupOrphans(); killed > 0 {
			log.Printf("Cleaned up %d orphan process(es)", killed)
		}
	}

	// Determine credential store mode
	storeMode := oauth.StoreMode(opts.CredentialStoreMode)
	if storeMode == "" {
		storeMode = oauth.StoreModeAuto
	}

	// Create credential store for OAuth
	credStore, err := oauth.NewCredentialStore(storeMode)
	if err != nil {
		log.Printf("Warning: failed to create credential store: %v", err)
	}

	var tokenManager *oauth.TokenManager
	if credStore != nil {
		tokenManager = oauth.NewTokenManager(credStore)
		// Set up warning handler to surface token storage failures to the user
		tokenManager.SetWarningHandler(func(serverURL string, warning error) {
			bus.Publish(events.NewErrorEvent(serverURL, warning, warning.Error()))
		})
	}

	return &Supervisor{
		bus:                     bus,
		handles:                 make(map[InstanceID]*Handle),
		pidTracker:              pidTracker,
		credStore:               credStore,
		tokenManager:            tokenManager,
		globalOAuthCallbackPort: opts.GlobalOAuthCallbackPort,
		lifecycleLocks:          make(map[InstanceID]*sync.Mutex),
		generations:             make(map[InstanceID]uint64),
		initAttemptTimeout:      initAttemptTimeout,
		initRetryBaseDelay:      initRetryBaseDelay,
	}
}

// CredentialStore returns the OAuth credential store.
func (s *Supervisor) CredentialStore() oauth.CredentialStore {
	return s.credStore
}

// lifecycleLock returns the stable lock for an instance. Lock order is:
// per-instance lifecycle lock -> supervisor map lock -> handle lock.
// lifecycleMu protects only the lock map and is never held while a lifecycle
// lock is acquired or while process I/O is performed.
func (s *Supervisor) lifecycleLock(id InstanceID) *sync.Mutex {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	lock := s.lifecycleLocks[id]
	if lock == nil {
		lock = &sync.Mutex{}
		s.lifecycleLocks[id] = lock
	}
	return lock
}

// Start starts or joins the start of a shared MCP server instance.
func (s *Supervisor) Start(ctx context.Context, name string, srv config.ServerConfig) (*Handle, error) {
	return s.StartInstance(ctx, SharedInstanceID(name), srv, nil)
}

// StartInstance starts or joins one instance under its lifecycle lock. validate
// runs while that lock is held, immediately before inspecting or creating the
// handle; Core uses it as the config-generation barrier.
func (s *Supervisor) StartInstance(
	ctx context.Context,
	id InstanceID,
	srv config.ServerConfig,
	validate func() error,
) (*Handle, error) {
	lock := s.lifecycleLock(id)
	lock.Lock()
	defer lock.Unlock()

	if validate != nil {
		if err := validate(); err != nil {
			return nil, err
		}
	}

	s.mu.RLock()
	existing := s.handles[id]
	s.mu.RUnlock()
	if existing != nil {
		if existing.NeedsLogin() {
			if err := existing.Stop(); err != nil {
				return nil, fmt.Errorf("stop needs-login handle: %w", err)
			}
		} else if existing.IsRunning() {
			return existing, nil
		} else if err := existing.Stop(); err != nil {
			return nil, fmt.Errorf("finish previous server stop: %w", err)
		}
	}

	return s.startInstanceLocked(ctx, id, srv)
}

func (s *Supervisor) startInstanceLocked(ctx context.Context, id InstanceID, srv config.ServerConfig) (*Handle, error) {
	s.mu.Lock()
	s.generations[id]++
	generation := s.generations[id]
	s.mu.Unlock()

	// Dispatch based on server type — initialization runs without the
	// global lock so that multiple servers can start concurrently.
	var (
		handle *Handle
		err    error
	)
	if srv.IsHTTP() {
		handle, err = s.startHTTP(ctx, id, generation, srv)
	} else {
		handle, err = s.startStdio(ctx, id, generation, srv)
	}

	if err != nil {
		// Clean up any partially-registered handle
		s.mu.Lock()
		if h, exists := s.handles[id]; exists && !h.IsRunning() {
			delete(s.handles, id)
		}
		s.mu.Unlock()
	}

	return handle, err
}

// startStdio starts a stdio-based MCP server process.
func (s *Supervisor) startStdio(ctx context.Context, id InstanceID, generation uint64, srv config.ServerConfig) (*Handle, error) {
	name := id.Server
	log.Printf("Starting stdio server: name=%s cmd=%s args=%v", name, srv.Command, srv.Args)

	// Emit starting event
	s.emitStatus(name, events.StateStarting, 0, nil, "")

	// Build command. Don't use exec.CommandContext — process lifecycle is
	// managed by Handle.Stop() (SIGTERM → SIGKILL). Tying the process to
	// a caller context would kill it when short-lived contexts (like the
	// tools/list grace period) expire.
	cmd := exec.Command(srv.Command, srv.Args...)
	configureProcessGroup(cmd)

	// Set working directory
	if srv.Cwd != "" {
		cmd.Dir = srv.Cwd
	}

	// Set environment with PATH augmentation
	cmd.Env = buildEnv(srv.Env)

	// Set up pipes
	stdin, err := cmd.StdinPipe()
	if err != nil {
		s.emitStatus(name, events.StateError, 0, nil, err.Error())
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		s.emitStatus(name, events.StateError, 0, nil, err.Error())
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		s.emitStatus(name, events.StateError, 0, nil, err.Error())
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}

	// Start the process
	if err := cmd.Start(); err != nil {
		s.emitStatus(name, events.StateError, 0, nil, err.Error())
		return nil, fmt.Errorf("start process: %w", err)
	}
	pgid, err := commandProcessGroupID(cmd)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		s.emitStatus(name, events.StateError, 0, nil, err.Error())
		return nil, fmt.Errorf("get process group: %w", err)
	}

	// Track PID for orphan cleanup
	if s.pidTracker != nil {
		if err := s.pidTracker.AddInstance(id, cmd.Process.Pid, pgid, srv.Command, srv.Args); err != nil {
			stopUntrackedCommand(cmd, pgid)
			s.emitStatus(name, events.StateError, 0, nil, err.Error())
			return nil, fmt.Errorf("persist process identity: %w", err)
		}
	}

	// Create transport and client
	transport := mcp.NewStdioTransport(stdin, stdout)
	client := mcp.NewClient(transport)

	// Create handle and register under lock
	handleCtx, handleCancel := context.WithCancel(context.Background())
	handle := &Handle{
		id:             name,
		instance:       id,
		generation:     generation,
		kind:           HandleKindStdio,
		ctx:            handleCtx,
		ctxCancel:      handleCancel,
		cmd:            cmd,
		pgid:           pgid,
		client:         client,
		stdioTransport: transport,
		logs:           make([]string, 0, 1000),
		toolsReady:     make(chan struct{}),
		bus:            s.bus,
		startedAt:      time.Now(),
		done:           make(chan struct{}),
	}
	handle.onStopped = s.notifyInstanceStopped
	if s.pidTracker != nil {
		leaderPID := cmd.Process.Pid
		handle.onGroupRetired = func() {
			if err := s.pidTracker.RemoveInstancePID(id, leaderPID); err != nil {
				log.Printf("Warning: failed to retire PID tracking for %s: %v", id, err)
			}
		}
	}

	s.mu.Lock()
	s.handles[id] = handle
	s.mu.Unlock()

	// Start stderr reader goroutine
	go handle.readStderr(stderr)

	// Start process watcher goroutine
	go handle.watchProcess()

	// Initialize MCP and discover tools in the background.
	// Start() returns immediately so that multiple servers can start concurrently.
	// Callers wait via handle.WaitForTools(), which blocks until init + discovery
	// complete (or the caller's context expires). The process stays alive even if
	// the caller's context expires — only handle.Stop() kills it.
	go s.initAndDiscoverAsync(handle, client, name)

	return handle, nil
}

func stopUntrackedCommand(cmd *exec.Cmd, pgid int) {
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	_ = terminateProcessGroupGracefully(pgid)
	select {
	case <-done:
	case <-time.After(GracefulShutdownTimeout):
		_ = killProcessGroup(pgid)
		<-done
	}
	_ = terminateProcessGroup(pgid, GracefulShutdownTimeout)
}

// initAndDiscoverAsync initializes the MCP connection (with retries) and then
// discovers tools. It signals handle.toolsReady when done (success or failure).
// Uses handle.ctx so the init is not tied to any caller's short-lived context
// (e.g. the tools/list grace period).
func (s *Supervisor) initAndDiscoverAsync(handle *Handle, client *mcp.Client, name string) {
	defer handle.signalToolsReady()

	// Initialize MCP connection with retry and exponential backoff
	var initErr error
initLoop:
	for attempt := 1; attempt <= MaxInitRetries; attempt++ {
		initCtx, cancel := context.WithTimeout(handle.ctx, s.initAttemptTimeout)
		initErr = client.Initialize(initCtx)
		cancel()

		if initErr == nil {
			break
		}

		log.Printf("MCP init attempt %d/%d failed: %v", attempt, MaxInitRetries, initErr)

		if attempt < MaxInitRetries {
			// Exponential backoff: 500ms, 1s, 2s... (context-aware)
			delay := s.initRetryBaseDelay * time.Duration(1<<(attempt-1))
			log.Printf("Retrying in %v", delay)
			select {
			case <-handle.ctx.Done():
				initErr = handle.ctx.Err()
				break initLoop
			case <-time.After(delay):
			}
		}
	}

	if initErr != nil {
		handle.setInitError(initErr)
		s.publishDiscovery(DiscoveryResult{
			Instance: handle.instance, Generation: handle.generation,
			Sequence: handle.NextDiscoverySequence(), Err: initErr,
		})
		_ = handle.Stop()
		s.emitStatus(name, events.StateError, handle.PID(), nil, fmt.Sprintf("MCP init failed after %d attempts: %v", MaxInitRetries, initErr))
		return
	}

	// Install notification handler now that initialization succeeded.
	s.installNotificationHandler(handle, client)

	// Emit running event
	s.emitStatus(name, events.StateRunning, handle.PID(), nil, "")

	s.discoverInitialTools(handle, client, name)
}

// startHTTP starts an HTTP-based MCP server connection.
func (s *Supervisor) startHTTP(ctx context.Context, id InstanceID, generation uint64, srv config.ServerConfig) (*Handle, error) {
	name := id.Server
	log.Printf("Starting HTTP server: name=%s url=%s", name, srv.URL)

	// Emit starting event
	s.emitStatus(name, events.StateStarting, 0, nil, "")

	// Determine authentication
	var bearerToken string
	var bearerTokenProvider func(context.Context) (string, error)
	authStatus := mcp.AuthStatusNone

	// Check bearer token first (highest priority)
	if srv.BearerTokenEnvVar != "" {
		token := os.Getenv(srv.BearerTokenEnvVar)
		if token == "" {
			err := fmt.Errorf("bearer token env var %s is not set", srv.BearerTokenEnvVar)
			s.emitStatus(name, events.StateError, 0, nil, err.Error())
			return nil, err
		}
		bearerToken = token
		authStatus = mcp.AuthStatusBearer
	} else if s.tokenManager != nil {
		// Check for OAuth credentials
		log.Printf("Looking up OAuth token for URL: %s", srv.URL)
		token, err := s.tokenManager.GetAccessToken(ctx, srv.URL)
		if err == nil && token != "" {
			log.Printf("Found OAuth token for %s (len=%d)", name, len(token))
			bearerToken = token
			bearerTokenProvider = func(callCtx context.Context) (string, error) {
				return s.tokenManager.GetAccessToken(callCtx, srv.URL)
			}
			authStatus = mcp.AuthStatusOAuthOK
		} else {
			log.Printf("No OAuth token found for %s: err=%v", name, err)
			// Try to discover OAuth support
			metadata, _ := oauth.SupportsOAuth(ctx, srv.URL)
			if metadata != nil {
				authStatus = mcp.AuthStatusOAuthNeeds
				// Don't fail - server might work without auth, or user can login later
				log.Printf("Server %s supports OAuth but needs login", name)
			}
		}
	}

	// Build HTTP headers
	headers := make(map[string]string)
	maps.Copy(headers, srv.HTTPHeaders)
	for headerName, envVarName := range srv.EnvHTTPHeaders {
		if value := os.Getenv(envVarName); value != "" {
			headers[headerName] = value
		}
	}

	// Create HTTP transport
	transportConfig := mcp.StreamableHTTPConfig{
		URL:                 srv.URL,
		BearerToken:         bearerToken,
		BearerTokenProvider: bearerTokenProvider,
		HTTPHeaders:         headers,
	}
	httpTransport := mcp.NewStreamableHTTPTransport(transportConfig)

	// Connect SSE stream
	if err := httpTransport.Connect(ctx); err != nil {
		// Check if it's an auth error
		if authStatus == mcp.AuthStatusOAuthNeeds {
			log.Printf("Server %s requires OAuth login", name)
		}
		s.emitStatus(name, events.StateError, 0, nil, err.Error())
		return nil, fmt.Errorf("connect HTTP transport: %w", err)
	}

	// Create client
	client := mcp.NewClient(httpTransport)

	// Create handle and register under lock
	handleCtx, handleCancel := context.WithCancel(context.Background())
	handle := &Handle{
		id:            name,
		instance:      id,
		generation:    generation,
		kind:          HandleKindHTTP,
		ctx:           handleCtx,
		ctxCancel:     handleCancel,
		client:        client,
		httpTransport: httpTransport,
		authStatus:    authStatus,
		serverURL:     srv.URL,
		serverConfig:  srv,
		logs:          make([]string, 0, 1000),
		toolsReady:    make(chan struct{}),
		bus:           s.bus,
		startedAt:     time.Now(),
		done:          make(chan struct{}),
	}
	handle.onStopped = s.notifyInstanceStopped

	s.mu.Lock()
	s.handles[id] = handle
	s.mu.Unlock()

	// Initialize MCP connection
	initCtx, cancel := context.WithTimeout(ctx, time.Duration(srv.StartupTimeout())*time.Second)
	defer cancel()

	if err := client.Initialize(initCtx); err != nil {
		// Check if it's an auth error - we can handle this gracefully
		var unauthErr *mcp.UnauthorizedError
		if errors.As(err, &unauthErr) {
			log.Printf("Server %s returned 401, checking for OAuth support", name)

			// Try to discover OAuth via the challenge
			var oauthMeta *oauth.AuthorizationServerMetadata
			if unauthErr.Challenge != nil && unauthErr.Challenge.ResourceMetadata != "" {
				// Challenge is now *oauth.BearerChallenge, can use directly
				result, discErr := oauth.DiscoverFromChallenge(ctx, unauthErr.Challenge)
				if discErr == nil && result != nil {
					oauthMeta = result.Metadata
					log.Printf("Discovered OAuth via resource_metadata for %s", name)
				} else {
					log.Printf("Failed to discover OAuth from challenge: %v", discErr)
				}
			}

			// Fallback: try standard discovery
			if oauthMeta == nil {
				oauthMeta, _ = oauth.SupportsOAuth(ctx, srv.URL)
			}

			if oauthMeta != nil {
				// Server supports OAuth - put handle in "needs login" state.
				// The handle is already published, so publish the whole state
				// change atomically and close the transport afterwards.
				_ = handle.setNeedsLogin(oauthMeta, unauthErr.Challenge)
				_ = httpTransport.Close()
				needsLoginErr := fmt.Errorf("%w for server %s", ErrNeedsLogin, name)
				handle.setInitError(needsLoginErr)
				s.publishDiscovery(DiscoveryResult{
					Instance: handle.instance, Generation: handle.generation,
					Sequence: handle.NextDiscoverySequence(), Err: needsLoginErr,
				})
				handle.signalToolsReady()

				s.emitStatus(name, events.StateNeedsAuth, 0, nil, "OAuth login required")
				log.Printf("Server %s requires OAuth login", name)
				return handle, nil
			}
		}

		s.publishDiscovery(DiscoveryResult{
			Instance: handle.instance, Generation: handle.generation,
			Sequence: handle.NextDiscoverySequence(), Err: err,
		})
		_ = handle.Stop()
		s.emitStatus(name, events.StateError, 0, nil, fmt.Sprintf("MCP init failed: %v", err))
		return nil, fmt.Errorf("initialize mcp: %w", err)
	}

	// Install notification handler now that initialization succeeded.
	s.installNotificationHandler(handle, client)

	// Emit running event immediately (tool discovery happens in background)
	s.emitStatus(name, events.StateRunning, 0, nil, "")

	// Discover tools in background (non-blocking)
	go s.discoverToolsAsync(handle, client, name)

	return handle, nil
}

// Stop stops a running MCP server process.
func (s *Supervisor) Stop(id string) error {
	return s.StopInstance(SharedInstanceID(id))
}

// StopInstance stops one instance under the same lifecycle lock used by start.
func (s *Supervisor) StopInstance(id InstanceID) error {
	lock := s.lifecycleLock(id)
	lock.Lock()
	defer lock.Unlock()
	return s.stopInstanceLocked(id)
}

func (s *Supervisor) stopInstanceLocked(id InstanceID) error {
	s.mu.RLock()
	handle, exists := s.handles[id]
	s.mu.RUnlock()

	if !exists {
		return fmt.Errorf("server %s not found", id)
	}

	err := handle.Stop()

	// Remove PID tracking after stop
	if err == nil && s.pidTracker != nil {
		if removeErr := s.pidTracker.RemoveInstance(id); removeErr != nil {
			log.Printf("Warning: failed to remove PID tracking: %v", removeErr)
		}
	}

	return err
}

// Restart stops and starts a shared instance as one serialized lifecycle operation.
func (s *Supervisor) Restart(ctx context.Context, name string, srv config.ServerConfig) (*Handle, error) {
	return s.RestartInstance(ctx, SharedInstanceID(name), srv)
}

// RestartInstance stops and starts one instance as one serialized lifecycle operation.
func (s *Supervisor) RestartInstance(ctx context.Context, id InstanceID, srv config.ServerConfig) (*Handle, error) {
	return s.RestartInstanceValidated(ctx, id, srv, nil)
}

// RestartInstanceValidated stops and starts one instance only after validate
// succeeds while holding the same lifecycle lock used by start and stop.
func (s *Supervisor) RestartInstanceValidated(
	ctx context.Context,
	id InstanceID,
	srv config.ServerConfig,
	validate func() error,
) (*Handle, error) {
	lock := s.lifecycleLock(id)
	lock.Lock()
	defer lock.Unlock()
	if validate != nil {
		if err := validate(); err != nil {
			return nil, err
		}
	}

	s.mu.RLock()
	_, exists := s.handles[id]
	s.mu.RUnlock()
	if exists {
		if err := s.stopInstanceLocked(id); err != nil {
			return nil, err
		}
	}
	return s.startInstanceLocked(ctx, id, srv)
}

// StopSessionInstances stops and forgets every private instance owned by a
// downstream session. Shared instances are deliberately untouched.
func (s *Supervisor) StopSessionInstances(session string) {
	s.mu.RLock()
	ids := make([]InstanceID, 0)
	for id := range s.handles {
		if id.Session == session {
			ids = append(ids, id)
		}
	}
	s.mu.RUnlock()

	for _, id := range ids {
		if err := s.StopInstance(id); err != nil {
			log.Printf("Warning: failed to stop private server %q: %v", id, err)
			continue
		}
		s.mu.Lock()
		delete(s.handles, id)
		s.mu.Unlock()
	}
}

// Get returns the handle for a server, or nil if not running.
func (s *Supervisor) Get(id string) *Handle {
	return s.GetInstance(SharedInstanceID(id))
}

// GetInstance returns the handle for one stable instance identity.
func (s *Supervisor) GetInstance(id InstanceID) *Handle {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.handles[id]
}

// StopAll stops all running servers gracefully.
// Logs any errors that occur during shutdown but does not return them,
// as this is typically called during application shutdown where we want
// to attempt stopping all servers regardless of individual failures.
func (s *Supervisor) StopAll() {
	s.mu.RLock()
	ids := make([]InstanceID, 0, len(s.handles))
	for id := range s.handles {
		ids = append(ids, id)
	}
	s.mu.RUnlock()

	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func(id InstanceID) {
			defer wg.Done()
			if err := s.StopInstance(id); err != nil {
				log.Printf("Warning: failed to stop server %q: %v", id, err)
			}
		}(id)
	}
	wg.Wait()
}

// RunningCount returns the number of running servers.
func (s *Supervisor) RunningCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	count := 0
	for _, h := range s.handles {
		if h.IsRunning() {
			count++
		}
	}
	return count
}

// RunningServers returns the IDs of running servers.
func (s *Supervisor) RunningServers() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := make([]string, 0, len(s.handles))
	for id, h := range s.handles {
		if h.IsRunning() {
			ids = append(ids, id.Server)
		}
	}
	return ids
}

func (s *Supervisor) emitStatus(id string, state events.RuntimeState, pid int, lastExit *events.LastExit, errMsg string) {
	status := events.ServerStatus{
		ID:       id,
		State:    state,
		PID:      pid,
		LastExit: lastExit,
		Error:    errMsg,
	}
	s.bus.Publish(events.NewStatusChangedEvent(id, events.StateIdle, state, status))
}

// discoverToolsAsync discovers tools from an already-initialized MCP server in the background.
// Used by startHTTP (which does its own sync init) and retryHTTPConnection.
func (s *Supervisor) discoverToolsAsync(handle *Handle, client *mcp.Client, name string) {
	defer handle.signalToolsReady()
	s.discoverInitialTools(handle, client, name)
}

// discoverInitialTools is the Supervisor's single owner of initial upstream
// discovery. Initialization without a tools capability verifies an empty tool
// catalog and deliberately does not issue tools/list.
func (s *Supervisor) discoverInitialTools(handle *Handle, client *mcp.Client, name string) {
	capabilities := client.Capabilities()
	if capabilities.Tools == nil {
		handle.SetTools([]mcp.Tool{})
		s.publishDiscovery(DiscoveryResult{
			Instance: handle.instance, Generation: handle.generation,
			Sequence: handle.NextDiscoverySequence(), Initialized: true,
			Capabilities: capabilities, Tools: []mcp.Tool{},
		})
		return
	}

	ctx, cancel := context.WithTimeout(handle.ctx, 30*time.Second)
	defer cancel()

	tools, err := client.ListTools(ctx)
	// Stamped here, next to the response: everything below — publishing, the
	// observer's apply, a caller re-applying the stored result later — can be
	// descheduled past a list_changed refresh of the same generation, and the
	// catalog needs to know this snapshot is the older of the two.
	sequence := handle.NextDiscoverySequence()
	if err != nil {
		s.bus.Publish(events.NewErrorEvent(name, err, "Failed to list tools"))
		s.publishDiscovery(DiscoveryResult{
			Instance: handle.instance, Generation: handle.generation,
			Sequence: sequence, Initialized: true,
			Capabilities: capabilities, Err: err,
		})
		return
	}

	handle.SetTools(tools)
	s.publishDiscovery(DiscoveryResult{
		Instance: handle.instance, Generation: handle.generation,
		Sequence: sequence, Initialized: true,
		Capabilities: capabilities, Tools: tools,
	})
	s.publishToolsUpdated(name, tools)
}

func (s *Supervisor) publishToolsUpdated(name string, tools []mcp.Tool) {
	mcpTools := make([]events.McpTool, len(tools))
	for i, t := range tools {
		mcpTools[i] = events.McpTool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
			Annotations: t.Annotations,
		}
	}
	if s.toolCache != nil {
		cacheInputs := make([]config.CachedToolInput, len(tools))
		for i, t := range tools {
			cacheInputs[i] = config.CachedToolInput{
				Name:         t.Name,
				Title:        t.Title,
				Description:  t.Description,
				InputSchema:  t.InputSchema,
				OutputSchema: t.OutputSchema,
				Annotations:  t.Annotations,
				Icons:        t.Icons,
				Meta:         t.Meta,
				Extra:        t.Extra,
			}
		}
		if err := s.toolCache.Update(name, cacheInputs); err != nil {
			log.Printf("Warning: failed to update tool cache for %s: %v", name, err)
		}
	}

	s.bus.Publish(events.NewToolsUpdatedEvent(name, mcpTools))
}

func (s *Supervisor) publishDiscovery(result DiscoveryResult) {
	result = result.Clone()
	if handle := s.GetInstance(result.Instance); handle != nil && handle.Generation() == result.Generation {
		handle.setDiscoveryResult(result)
	}
	s.observerMu.RLock()
	observer := s.observer
	s.observerMu.RUnlock()
	if observer != nil {
		observer.OnDiscoveryResult(result.Clone())
	}
}

func (s *Supervisor) notifyInstanceStopped(id InstanceID, generation uint64) {
	s.observerMu.RLock()
	observer := s.observer
	s.observerMu.RUnlock()
	if observer != nil {
		observer.OnInstanceStopped(id, generation)
	}
}

// buildEnv creates the environment for a subprocess with PATH augmentation.
func buildEnv(customEnv map[string]string) []string {
	// Start with current environment
	env := os.Environ()

	// Augment PATH with common binary locations
	pathDirs := []string{
		"/opt/homebrew/bin",
		"/usr/local/bin",
		"/usr/bin",
		"/bin",
	}

	// Find and update PATH
	for i, e := range env {
		if after, ok := strings.CutPrefix(e, "PATH="); ok {
			currentPath := after
			// Prepend additional paths
			newPath := strings.Join(pathDirs, ":") + ":" + currentPath
			env[i] = "PATH=" + newPath
			break
		}
	}

	// Add custom environment variables
	for k, v := range customEnv {
		found := false
		prefix := k + "="
		for i, e := range env {
			if strings.HasPrefix(e, prefix) {
				env[i] = k + "=" + v
				found = true
				break
			}
		}
		if !found {
			env = append(env, k+"="+v)
		}
	}

	return env
}

// HandleKind represents the type of server handle.
type HandleKind int

const (
	HandleKindStdio HandleKind = iota
	HandleKindHTTP
)

// Handle represents a running server (process or HTTP connection).
type Handle struct {
	id         string
	instance   InstanceID
	generation uint64
	kind       HandleKind

	// Stdio-specific fields
	cmd            *exec.Cmd
	pgid           int
	stdioTransport *mcp.StdioTransport
	onGroupRetired func()

	// HTTP-specific fields
	serverURL    string
	serverConfig config.ServerConfig // Cached for retry after OAuth

	// authMu guards the fields below plus client. startHTTP publishes the handle
	// into Supervisor.handles before running the handshake, then rewrites all of
	// them on the OAuth-401 path, so readers on other goroutines (web's
	// AuthStatus(), serve mode's Client()) can be mid-read while it does.
	// Snapshot under the lock and do any I/O — client.Close(), transport.Close()
	// — after releasing it.
	authMu        sync.RWMutex
	httpTransport *mcp.StreamableHTTPTransport
	authStatus    mcp.AuthStatus
	oauthMeta     *oauth.AuthorizationServerMetadata // Cached OAuth metadata for login
	authChallenge *oauth.BearerChallenge             // Cached WWW-Authenticate challenge

	// Common fields
	ctx       context.Context    // cancelled when server stops
	ctxCancel context.CancelFunc // cancels ctx
	client    *mcp.Client        // guarded by authMu; startHTTP clears it on the OAuth path

	tools        []mcp.Tool
	toolsMu      sync.RWMutex
	toolsReady   chan struct{} // closed when init + tool discovery complete
	toolsReadyMu sync.Mutex    // protects toolsReady close
	initErr      error         // set if MCP init fails (checked by WaitForTools)
	initErrMu    sync.Mutex
	discovery    DiscoveryResult
	discoverySet bool
	discoveryMu  sync.RWMutex
	discoverySeq atomic.Uint64 // stamps DiscoveryResult.Sequence
	logs         []string
	logsMu       sync.RWMutex
	bus          *events.Bus
	startedAt    time.Time
	stopped      bool
	stopMu       sync.Mutex
	done         chan struct{} // closed when server stops
	groupErr     error
	groupErrMu   sync.Mutex
	onStopped    func(InstanceID, uint64)
	stoppedOnce  sync.Once
}

// ID returns the server ID.
func (h *Handle) ID() string {
	return h.id
}

// InstanceID returns the stable identity used by the Supervisor and PID registry.
func (h *Handle) InstanceID() InstanceID {
	return h.instance
}

// Generation identifies this exact process/transport generation.
func (h *Handle) Generation() uint64 {
	return h.generation
}

// NextDiscoverySequence stamps a DiscoveryResult produced for this handle.
// Call it where the catalog data is obtained (right after the upstream
// responds), so that a snapshot taken earlier always carries a lower sequence
// than a later one even if the goroutine carrying it is descheduled before it
// reaches the catalog. See DiscoveryResult.Sequence.
func (h *Handle) NextDiscoverySequence() uint64 {
	return h.discoverySeq.Add(1)
}

// Client returns the MCP client, or nil if the handle has no usable connection
// (needs-auth HTTP handles clear it).
func (h *Handle) Client() *mcp.Client {
	h.authMu.RLock()
	defer h.authMu.RUnlock()
	return h.client
}

// Capabilities returns the capabilities advertised by the upstream server at
// initialize time. Returns the zero value if the handle has no client yet
// (e.g., before initialization completes or for needs-auth HTTP handles).
func (h *Handle) Capabilities() mcp.ServerCapabilities {
	client := h.Client()
	if client == nil {
		return mcp.ServerCapabilities{}
	}
	return client.Capabilities()
}

// Tools returns the discovered tools.
func (h *Handle) Tools() []mcp.Tool {
	h.toolsMu.RLock()
	defer h.toolsMu.RUnlock()
	return cloneTools(h.tools)
}

// SetTools sets the discovered tools (thread-safe).
func (h *Handle) SetTools(tools []mcp.Tool) {
	h.toolsMu.Lock()
	defer h.toolsMu.Unlock()
	h.tools = cloneTools(tools)
}

func (h *Handle) setDiscoveryResult(result DiscoveryResult) {
	h.discoveryMu.Lock()
	h.discovery = result.Clone()
	h.discoverySet = true
	h.discoveryMu.Unlock()
}

// DiscoveryResult returns the Supervisor-owned initial discovery result.
func (h *Handle) DiscoveryResult() (DiscoveryResult, bool) {
	h.discoveryMu.RLock()
	defer h.discoveryMu.RUnlock()
	return h.discovery.Clone(), h.discoverySet
}

func (h *Handle) notifyStopped() {
	h.stoppedOnce.Do(func() {
		if h.onStopped != nil {
			h.onStopped(h.instance, h.generation)
		}
	})
}

// signalToolsReady signals that tool discovery is complete.
func (h *Handle) signalToolsReady() {
	h.toolsReadyMu.Lock()
	defer h.toolsReadyMu.Unlock()
	select {
	case <-h.toolsReady:
		// Already closed
	default:
		close(h.toolsReady)
	}
}

// ToolsReady returns true if tool discovery has completed (non-blocking).
func (h *Handle) ToolsReady() bool {
	select {
	case <-h.toolsReady:
		return true
	default:
		return false
	}
}

// WaitForTools waits for init + tool discovery to complete or context to be cancelled.
// Returns initErr if MCP initialization failed.
func (h *Handle) WaitForTools(ctx context.Context) error {
	select {
	case <-h.toolsReady:
		if err := h.InitError(); err != nil {
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// setInitError records an MCP initialization error.
func (h *Handle) setInitError(err error) {
	h.initErrMu.Lock()
	defer h.initErrMu.Unlock()
	h.initErr = err
}

// InitError returns the MCP initialization error, if any.
func (h *Handle) InitError() error {
	h.initErrMu.Lock()
	defer h.initErrMu.Unlock()
	return h.initErr
}

// Logs returns the captured stderr logs.
func (h *Handle) Logs() []string {
	h.logsMu.RLock()
	defer h.logsMu.RUnlock()
	logs := make([]string, len(h.logs))
	copy(logs, h.logs)
	return logs
}

// PID returns the process ID (0 for HTTP handles).
func (h *Handle) PID() int {
	if h.kind != HandleKindStdio || h.cmd == nil || h.cmd.Process == nil {
		return 0
	}
	return h.cmd.Process.Pid
}

// Kind returns the handle type (stdio or HTTP).
func (h *Handle) Kind() HandleKind {
	return h.kind
}

// AuthStatus returns the authentication status (for HTTP handles).
func (h *Handle) AuthStatus() mcp.AuthStatus {
	h.authMu.RLock()
	defer h.authMu.RUnlock()
	return h.authStatus
}

// ServerURL returns the server URL (for HTTP handles).
func (h *Handle) ServerURL() string {
	return h.serverURL
}

// StartedAt returns when the process started.
func (h *Handle) StartedAt() time.Time {
	return h.startedAt
}

// Uptime returns how long the process has been running.
func (h *Handle) Uptime() time.Duration {
	return time.Since(h.startedAt)
}

// IsRunning returns true if the process is still running.
func (h *Handle) IsRunning() bool {
	if h.NeedsLogin() {
		return false
	}

	h.stopMu.Lock()
	stopped := h.stopped
	h.stopMu.Unlock()

	if stopped {
		return false
	}

	// Check if done channel is closed (non-blocking)
	select {
	case <-h.done:
		return false
	default:
		return true
	}
}

// NeedsLogin reports whether initialization stopped at the OAuth login gate.
func (h *Handle) NeedsLogin() bool {
	return errors.Is(h.InitError(), ErrNeedsLogin)
}

// Stop gracefully stops the server (process or HTTP connection).
func (h *Handle) Stop() error {
	h.stopMu.Lock()
	if h.stopped {
		h.stopMu.Unlock()
		<-h.done
		return h.processGroupError()
	}
	h.stopped = true
	h.stopMu.Unlock()

	h.bus.Publish(events.NewStatusChangedEvent(h.id, events.StateRunning, events.StateStopping, events.ServerStatus{
		ID:    h.id,
		State: events.StateStopping,
		PID:   h.PID(),
	}))

	// Cancel handle context to abort any in-flight operations (e.g. tool discovery)
	if h.ctxCancel != nil {
		h.ctxCancel()
	}

	// Close MCP client first (may be nil for needs-auth state). Snapshot both
	// under authMu and close outside it, so a slow Close cannot stall readers.
	h.authMu.RLock()
	client, httpTransport := h.client, h.httpTransport
	h.authMu.RUnlock()

	if client != nil {
		_ = client.Close()
	}

	if h.kind == HandleKindStdio {
		// Stdio: signal the entire process group. The watcher retires the PGID
		// only after the leader is reaped and any surviving workers are gone.
		if h.cmd != nil && h.cmd.Process != nil && h.pgid > 0 {
			_ = terminateProcessGroupGracefully(h.pgid)

			// Wait for watchProcess to signal completion with timeout
			select {
			case <-h.done:
				// Process exited gracefully
			case <-time.After(GracefulShutdownTimeout):
				// Force kill
				_ = killProcessGroup(h.pgid)
				<-h.done
			}
		}
	} else {
		// HTTP: close transport
		if httpTransport != nil {
			_ = httpTransport.Close()
		}
		// Signal done
		close(h.done)

		h.bus.Publish(events.NewStatusChangedEvent(h.id, events.StateStopping, events.StateStopped, events.ServerStatus{
			ID:    h.id,
			State: events.StateStopped,
		}))
	}
	h.notifyStopped()

	return h.processGroupError()
}

func (h *Handle) setProcessGroupError(err error) {
	h.groupErrMu.Lock()
	h.groupErr = err
	h.groupErrMu.Unlock()
}

func (h *Handle) processGroupError() error {
	h.groupErrMu.Lock()
	defer h.groupErrMu.Unlock()
	return h.groupErr
}

// readStderr reads stderr and publishes log events.
func (h *Handle) readStderr(stderr io.ReadCloser) {
	scanner := bufio.NewScanner(stderr)
	for scanner.Scan() {
		line := scanner.Text()

		h.logsMu.Lock()
		h.logs = append(h.logs, line)
		// Keep only last 1000 lines
		if len(h.logs) > 1000 {
			h.logs = h.logs[len(h.logs)-1000:]
		}
		h.logsMu.Unlock()

		h.bus.Publish(events.NewLogReceivedEvent(h.id, line))
	}
}

// watchProcess monitors the process for exit.
func (h *Handle) watchProcess() {
	err := h.cmd.Wait()

	// A wrapper may exit while leaving workers behind. The leader's watcher
	// owns immediate group cleanup so a later restart cannot discard the PGID.
	var cleanupErr error
	if h.pgid > 0 {
		cleanupErr = terminateProcessGroup(h.pgid, GracefulShutdownTimeout)
		if cleanupErr != nil {
			h.setProcessGroupError(fmt.Errorf("retire process group %d: %w", h.pgid, cleanupErr))
			log.Printf("Failed to retire process group %d for %s: %v", h.pgid, h.instance, cleanupErr)
		}
	}
	if cleanupErr == nil && h.onGroupRetired != nil {
		h.onGroupRetired()
	}

	// Signal completion only after the process group and registry entry retire.
	close(h.done)

	h.stopMu.Lock()
	wasStopped := h.stopped
	h.stopped = true
	h.stopMu.Unlock()

	exitCode := 0
	signal := ""
	if h.cmd.ProcessState != nil {
		exitCode = h.cmd.ProcessState.ExitCode()
		signal = processExitSignal(h.cmd.ProcessState)
	}

	lastExit := &events.LastExit{
		Code:      exitCode,
		Signal:    signal,
		Timestamp: time.Now(),
	}

	var newState events.RuntimeState
	if wasStopped {
		newState = events.StateStopped
	} else if err != nil || exitCode != 0 {
		newState = events.StateCrashed
	} else {
		newState = events.StateStopped
	}

	h.bus.Publish(events.NewStatusChangedEvent(h.id, events.StateRunning, newState, events.ServerStatus{
		ID:       h.id,
		State:    newState,
		LastExit: lastExit,
	}))
	h.notifyStopped()
}

// OAuthMeta returns the cached OAuth metadata for servers needing login.
func (h *Handle) OAuthMeta() *oauth.AuthorizationServerMetadata {
	h.authMu.RLock()
	defer h.authMu.RUnlock()
	return h.oauthMeta
}

// setNeedsLogin records the needs-OAuth-login state discovered during startHTTP
// and returns the transport the caller must close. The client and transport are
// dropped because neither is usable until the user authenticates; closing is
// left to the caller so it happens outside the lock.
func (h *Handle) setNeedsLogin(
	meta *oauth.AuthorizationServerMetadata,
	challenge *oauth.BearerChallenge,
) *mcp.StreamableHTTPTransport {
	h.authMu.Lock()
	defer h.authMu.Unlock()
	h.authStatus = mcp.AuthStatusOAuthNeeds
	h.oauthMeta = meta
	h.authChallenge = challenge
	h.client = nil
	transport := h.httpTransport
	h.httpTransport = nil
	return transport
}

// loginState snapshots the fields LoginOAuth needs in one critical section, so
// it cannot mix a stale status with a newer challenge.
func (h *Handle) loginState() (mcp.AuthStatus, *oauth.BearerChallenge, *oauth.AuthorizationServerMetadata) {
	h.authMu.RLock()
	defer h.authMu.RUnlock()
	return h.authStatus, h.authChallenge, h.oauthMeta
}

// LoginOAuth triggers the OAuth login flow for a server that needs authentication.
// It opens a browser for the user to authenticate, then reconnects.
func (s *Supervisor) LoginOAuth(ctx context.Context, name string) error {
	s.mu.Lock()
	handle, exists := s.handles[SharedInstanceID(name)]
	if !exists {
		s.mu.Unlock()
		return fmt.Errorf("server %s not found", name)
	}
	s.mu.Unlock()

	authStatus, authChallenge, oauthMeta := handle.loginState()
	if authStatus != mcp.AuthStatusOAuthNeeds {
		return fmt.Errorf("server %s doesn't need OAuth login (status: %s)", name, authStatus)
	}

	if s.credStore == nil {
		return fmt.Errorf("no credential store available")
	}

	// Build and resolve OAuth flow config
	flowConfig := resolveOAuthFlowConfig(
		handle.serverURL, name, s.credStore,
		handle.serverConfig.OAuth, s.globalOAuthCallbackPort,
		authChallenge, oauthMeta,
	)

	// Run OAuth flow
	flow := oauth.NewFlow(flowConfig)
	if err := flow.Run(ctx); err != nil {
		return fmt.Errorf("oauth login: %w", err)
	}

	// Retry connection with new tokens
	return s.retryHTTPConnection(ctx, name)
}

// resolveOAuthFlowConfig builds an OAuth FlowConfig with the correct resolution
// priority for callback port and scopes:
//   - Callback port: per-server oauth.callback_port → global → nil
//   - Scopes: per-server config → WWW-Authenticate challenge → metadata
func resolveOAuthFlowConfig(
	serverURL, serverName string,
	store oauth.CredentialStore,
	oauthCfg *config.OAuthConfig,
	globalCallbackPort *int,
	challenge *oauth.BearerChallenge,
	meta *oauth.AuthorizationServerMetadata,
) oauth.FlowConfig {
	fc := oauth.FlowConfig{
		ServerURL:  serverURL,
		ServerName: serverName,
		Store:      store,
	}

	// Apply per-server OAuth config if present
	if oauthCfg != nil {
		fc.ClientID = oauthCfg.ClientID
		fc.ClientSecret = oauthCfg.ClientSecret
		if len(oauthCfg.Scopes) > 0 {
			fc.Scopes = oauthCfg.Scopes
		}
		fc.CallbackPort = oauthCfg.CallbackPort
	}

	// Callback port fallback: per-server → global → nil
	if fc.CallbackPort == nil {
		fc.CallbackPort = globalCallbackPort
	}

	// Scope fallback: config → challenge → metadata
	if len(fc.Scopes) == 0 && challenge != nil && challenge.Scope != "" {
		fc.Scopes = strings.Split(challenge.Scope, " ")
	}
	if len(fc.Scopes) == 0 && meta != nil && len(meta.ScopesSupported) > 0 {
		fc.Scopes = meta.ScopesSupported
	}

	return fc
}

// retryHTTPConnection attempts to reconnect an HTTP server after OAuth completes.
func (s *Supervisor) retryHTTPConnection(ctx context.Context, name string) error {
	s.mu.RLock()
	handle, exists := s.handles[SharedInstanceID(name)]
	s.mu.RUnlock()

	if !exists {
		return fmt.Errorf("server %s not found", name)
	}

	_, err := s.Start(ctx, name, handle.serverConfig)
	return err
}
