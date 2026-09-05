// Package process provides process lifecycle management for MCP servers.
package process

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"
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

// StopMatching retires selected identities, including starts already waiting on
// a lifecycle lock. The caller must reject new acquisitions before calling it.
// retire runs under the identity's lifecycle lock, before transport shutdown.
func (s *Supervisor) StopMatching(matches func(InstanceID) bool, retire func(InstanceID, uint64)) {
	s.lifecycleMu.Lock()
	ids := make([]InstanceID, 0, len(s.lifecycleLocks))
	for id := range s.lifecycleLocks {
		if matches(id) {
			ids = append(ids, id)
		}
	}
	s.lifecycleMu.Unlock()
	for _, id := range ids {
		lock := s.lifecycleLock(id)
		lock.Lock()
		if handle := s.GetInstance(id); handle != nil {
			retire(id, handle.Generation())
			if err := s.stopInstanceLocked(id); err == nil {
				s.mu.Lock()
				delete(s.handles, id)
				s.mu.Unlock()
			} else {
				log.Printf("Warning: failed to retire server %s: %v", id, err)
			}
		}
		lock.Unlock()
	}
}
