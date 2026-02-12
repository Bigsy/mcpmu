// Package process provides process lifecycle management for MCP servers.
package process

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
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

	// InitRetryBaseDelay is the base delay between retry attempts.
	InitRetryBaseDelay = 500 * time.Millisecond
)

// Supervisor manages MCP server process lifecycles.
type Supervisor struct {
	bus          *events.Bus
	handles      map[string]*Handle
	pidTracker   *PIDTracker
	credStore    oauth.CredentialStore
	tokenManager *oauth.TokenManager
	mu           sync.RWMutex
}

// SupervisorOptions configures a Supervisor.
type SupervisorOptions struct {
	// CredentialStoreMode specifies the OAuth credential store mode.
	// If empty, defaults to "auto".
	CredentialStoreMode string

	// PIDTrackerDir overrides the directory used for the PID tracking file.
	// If empty, the default ~/.config/mcpmu/ directory is used.
	PIDTrackerDir string
}

// NewSupervisor creates a new process supervisor.
// It also cleans up any orphan processes from previous runs.
func NewSupervisor(bus *events.Bus) *Supervisor {
	return NewSupervisorWithOptions(bus, SupervisorOptions{})
}

// NewSupervisorWithOptions creates a new process supervisor with options.
func NewSupervisorWithOptions(bus *events.Bus, opts SupervisorOptions) *Supervisor {
	var pidTracker *PIDTracker
	var err error
	if opts.PIDTrackerDir != "" {
		pidTracker, err = NewPIDTrackerWithDir(opts.PIDTrackerDir)
	} else {
		pidTracker, err = NewPIDTracker()
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
		bus:          bus,
		handles:      make(map[string]*Handle),
		pidTracker:   pidTracker,
		credStore:    credStore,
		tokenManager: tokenManager,
	}
}

// CredentialStore returns the OAuth credential store.
func (s *Supervisor) CredentialStore() oauth.CredentialStore {
	return s.credStore
}

// Start starts an MCP server (stdio process or HTTP connection).
// The name parameter is used as the identifier for the server.
func (s *Supervisor) Start(ctx context.Context, name string, srv config.ServerConfig) (*Handle, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Check if already running
	if h, exists := s.handles[name]; exists && h.IsRunning() {
		return nil, fmt.Errorf("server %s is already running", name)
	}

	// Dispatch based on server type
	if srv.IsHTTP() {
		return s.startHTTP(ctx, name, srv)
	}
	return s.startStdio(ctx, name, srv)
}

// startStdio starts a stdio-based MCP server process.
func (s *Supervisor) startStdio(ctx context.Context, name string, srv config.ServerConfig) (*Handle, error) {
	log.Printf("Starting stdio server: name=%s cmd=%s args=%v", name, srv.Command, srv.Args)

	// Emit starting event
	s.emitStatus(name, events.StateStarting, 0, nil, "")

	// Build command
	cmd := exec.CommandContext(ctx, srv.Command, srv.Args...)

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

	// Track PID for orphan cleanup
	if s.pidTracker != nil {
		if err := s.pidTracker.Add(name, cmd.Process.Pid, srv.Command, srv.Args); err != nil {
			log.Printf("Warning: failed to track PID: %v", err)
		}
	}

	// Create transport and client
	transport := mcp.NewStdioTransport(stdin, stdout)
	client := mcp.NewClient(transport)

	// Create handle
	handleCtx, handleCancel := context.WithCancel(context.Background())
	handle := &Handle{
		id:             name,
		kind:           HandleKindStdio,
		ctx:            handleCtx,
		ctxCancel:      handleCancel,
		cmd:            cmd,
		client:         client,
		stdioTransport: transport,
		logs:           make([]string, 0, 1000),
		toolsReady:     make(chan struct{}),
		bus:            s.bus,
		startedAt:      time.Now(),
		done:           make(chan struct{}),
	}

	s.handles[name] = handle

	// Start stderr reader goroutine
	go handle.readStderr(stderr)

	// Start process watcher goroutine
	go handle.watchProcess()

	// Initialize MCP connection with retry and exponential backoff
	var initErr error
	for attempt := 1; attempt <= MaxInitRetries; attempt++ {
		initCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		initErr = client.Initialize(initCtx)
		cancel()

		if initErr == nil {
			break
		}

		log.Printf("MCP init attempt %d/%d failed: %v", attempt, MaxInitRetries, initErr)

		if attempt < MaxInitRetries {
			// Exponential backoff: 500ms, 1s, 2s...
			delay := InitRetryBaseDelay * time.Duration(1<<(attempt-1))
			log.Printf("Retrying in %v", delay)
			time.Sleep(delay)
		}
	}

	if initErr != nil {
		_ = handle.Stop()
		s.emitStatus(name, events.StateError, cmd.Process.Pid, nil, fmt.Sprintf("MCP init failed after %d attempts: %v", MaxInitRetries, initErr))
		return nil, fmt.Errorf("initialize mcp: %w", initErr)
	}

	// Emit running event immediately (tool discovery happens in background)
	s.emitStatus(name, events.StateRunning, cmd.Process.Pid, nil, "")

	// Discover tools in background (non-blocking)
	go s.discoverToolsAsync(handle, client, name)

	return handle, nil
}

// startHTTP starts an HTTP-based MCP server connection.
func (s *Supervisor) startHTTP(ctx context.Context, name string, srv config.ServerConfig) (*Handle, error) {
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
	for k, v := range srv.HTTPHeaders {
		headers[k] = v
	}
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

	// Create handle
	handleCtx, handleCancel := context.WithCancel(context.Background())
	handle := &Handle{
		id:            name,
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

	s.handles[name] = handle

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
				// Server supports OAuth - put handle in "needs login" state
				handle.authStatus = mcp.AuthStatusOAuthNeeds
				handle.oauthMeta = oauthMeta
				handle.client = nil // No client until authenticated
				_ = httpTransport.Close()
				handle.httpTransport = nil

				s.emitStatus(name, events.StateNeedsAuth, 0, nil, "OAuth login required")
				log.Printf("Server %s requires OAuth login", name)
				return handle, nil
			}
		}

		_ = handle.Stop()
		s.emitStatus(name, events.StateError, 0, nil, fmt.Sprintf("MCP init failed: %v", err))
		return nil, fmt.Errorf("initialize mcp: %w", err)
	}

	// Emit running event immediately (tool discovery happens in background)
	s.emitStatus(name, events.StateRunning, 0, nil, "")

	// Discover tools in background (non-blocking)
	go s.discoverToolsAsync(handle, client, name)

	return handle, nil
}

// Stop stops a running MCP server process.
func (s *Supervisor) Stop(id string) error {
	s.mu.Lock()
	handle, exists := s.handles[id]
	s.mu.Unlock()

	if !exists {
		return fmt.Errorf("server %s not found", id)
	}

	err := handle.Stop()

	// Remove PID tracking after stop
	if s.pidTracker != nil {
		if removeErr := s.pidTracker.Remove(id); removeErr != nil {
			log.Printf("Warning: failed to remove PID tracking: %v", removeErr)
		}
	}

	return err
}

// Get returns the handle for a server, or nil if not running.
func (s *Supervisor) Get(id string) *Handle {
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
	ids := make([]string, 0, len(s.handles))
	for id := range s.handles {
		ids = append(ids, id)
	}
	s.mu.RUnlock()

	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			if err := s.Stop(id); err != nil {
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
			ids = append(ids, id)
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

// discoverToolsAsync discovers tools from an MCP server in the background.
// It signals the handle when discovery is complete (whether successful or not).
func (s *Supervisor) discoverToolsAsync(handle *Handle, client *mcp.Client, name string) {
	defer handle.signalToolsReady()

	ctx, cancel := context.WithTimeout(handle.ctx, 30*time.Second)
	defer cancel()

	tools, err := client.ListTools(ctx)
	if err != nil {
		s.bus.Publish(events.NewErrorEvent(name, err, "Failed to list tools"))
		return
	}

	handle.SetTools(tools)

	mcpTools := make([]events.McpTool, len(tools))
	for i, t := range tools {
		mcpTools[i] = events.McpTool{
			Name:        t.Name,
			Description: t.Description,
		}
	}
	s.bus.Publish(events.NewToolsUpdatedEvent(name, mcpTools))
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
		if strings.HasPrefix(e, "PATH=") {
			currentPath := strings.TrimPrefix(e, "PATH=")
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
	id   string
	kind HandleKind

	// Stdio-specific fields
	cmd            *exec.Cmd
	stdioTransport *mcp.StdioTransport

	// HTTP-specific fields
	httpTransport *mcp.StreamableHTTPTransport
	authStatus    mcp.AuthStatus
	serverURL     string
	serverConfig  config.ServerConfig                // Cached for retry after OAuth
	oauthMeta     *oauth.AuthorizationServerMetadata // Cached OAuth metadata for login

	// Common fields
	ctx          context.Context    // cancelled when server stops
	ctxCancel    context.CancelFunc // cancels ctx
	client       *mcp.Client
	tools        []mcp.Tool
	toolsMu      sync.RWMutex
	toolsReady   chan struct{} // closed when tools are discovered
	toolsReadyMu sync.Mutex    // protects toolsReady close
	logs         []string
	logsMu       sync.RWMutex
	bus          *events.Bus
	startedAt    time.Time
	stopped      bool
	stopMu       sync.Mutex
	done         chan struct{} // closed when server stops
}

// ID returns the server ID.
func (h *Handle) ID() string {
	return h.id
}

// Client returns the MCP client.
func (h *Handle) Client() *mcp.Client {
	return h.client
}

// Tools returns the discovered tools.
func (h *Handle) Tools() []mcp.Tool {
	h.toolsMu.RLock()
	defer h.toolsMu.RUnlock()
	return h.tools
}

// SetTools sets the discovered tools (thread-safe).
func (h *Handle) SetTools(tools []mcp.Tool) {
	h.toolsMu.Lock()
	defer h.toolsMu.Unlock()
	h.tools = tools
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

// WaitForTools waits for tool discovery to complete or context to be cancelled.
func (h *Handle) WaitForTools(ctx context.Context) error {
	select {
	case <-h.toolsReady:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
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

// Stop gracefully stops the server (process or HTTP connection).
func (h *Handle) Stop() error {
	h.stopMu.Lock()
	if h.stopped {
		h.stopMu.Unlock()
		return nil
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

	// Close MCP client first (may be nil for needs-auth state)
	if h.client != nil {
		_ = h.client.Close()
	}

	if h.kind == HandleKindStdio {
		// Stdio: send SIGTERM to process
		if h.cmd != nil && h.cmd.Process != nil {
			_ = h.cmd.Process.Signal(syscall.SIGTERM)

			// Wait for watchProcess to signal completion with timeout
			select {
			case <-h.done:
				// Process exited gracefully
			case <-time.After(GracefulShutdownTimeout):
				// Force kill
				_ = h.cmd.Process.Signal(syscall.SIGKILL)
				<-h.done
			}
		}
	} else {
		// HTTP: close transport
		if h.httpTransport != nil {
			_ = h.httpTransport.Close()
		}
		// Signal done
		close(h.done)

		h.bus.Publish(events.NewStatusChangedEvent(h.id, events.StateStopping, events.StateStopped, events.ServerStatus{
			ID:    h.id,
			State: events.StateStopped,
		}))
	}

	return nil
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

	// Signal that process has exited
	close(h.done)

	h.stopMu.Lock()
	wasStopped := h.stopped
	h.stopped = true
	h.stopMu.Unlock()

	exitCode := 0
	signal := ""
	if h.cmd.ProcessState != nil {
		exitCode = h.cmd.ProcessState.ExitCode()
		if ws, ok := h.cmd.ProcessState.Sys().(syscall.WaitStatus); ok {
			if ws.Signaled() {
				signal = ws.Signal().String()
			}
		}
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
}

// OAuthMeta returns the cached OAuth metadata for servers needing login.
func (h *Handle) OAuthMeta() *oauth.AuthorizationServerMetadata {
	return h.oauthMeta
}

// LoginOAuth triggers the OAuth login flow for a server that needs authentication.
// It opens a browser for the user to authenticate, then reconnects.
func (s *Supervisor) LoginOAuth(ctx context.Context, name string) error {
	s.mu.Lock()
	handle, exists := s.handles[name]
	if !exists {
		s.mu.Unlock()
		return fmt.Errorf("server %s not found", name)
	}
	s.mu.Unlock()

	if handle.authStatus != mcp.AuthStatusOAuthNeeds {
		return fmt.Errorf("server %s doesn't need OAuth login (status: %s)", name, handle.authStatus)
	}

	if s.credStore == nil {
		return fmt.Errorf("no credential store available")
	}

	// Build OAuth flow config
	flowConfig := oauth.FlowConfig{
		ServerURL:  handle.serverURL,
		ServerName: name,
		Store:      s.credStore,
		ClientID:   handle.serverConfig.OAuthClientID, // Pre-registered client ID (if configured)
	}

	// Add scopes from config or metadata
	if len(handle.serverConfig.Scopes) > 0 {
		flowConfig.Scopes = handle.serverConfig.Scopes
	} else if handle.oauthMeta != nil && len(handle.oauthMeta.ScopesSupported) > 0 {
		flowConfig.Scopes = handle.oauthMeta.ScopesSupported
	}

	// Run OAuth flow
	flow := oauth.NewFlow(flowConfig)
	if err := flow.Run(ctx); err != nil {
		return fmt.Errorf("oauth login: %w", err)
	}

	// Retry connection with new tokens
	return s.retryHTTPConnection(ctx, name)
}

// retryHTTPConnection attempts to reconnect an HTTP server after OAuth completes.
func (s *Supervisor) retryHTTPConnection(ctx context.Context, name string) error {
	s.mu.Lock()
	handle, exists := s.handles[name]
	s.mu.Unlock()

	if !exists {
		return fmt.Errorf("server %s not found", name)
	}

	// Use cached server config from handle
	cfg := handle.serverConfig

	// Emit starting event
	s.emitStatus(name, events.StateStarting, 0, nil, "")

	// Get OAuth token
	token, err := s.tokenManager.GetAccessToken(ctx, handle.serverURL)
	if err != nil {
		s.emitStatus(name, events.StateError, 0, nil, fmt.Sprintf("Failed to get OAuth token: %v", err))
		return fmt.Errorf("get oauth token: %w", err)
	}

	// Build HTTP headers
	headers := make(map[string]string)
	for k, v := range cfg.HTTPHeaders {
		headers[k] = v
	}
	for headerName, envVarName := range cfg.EnvHTTPHeaders {
		if value := os.Getenv(envVarName); value != "" {
			headers[headerName] = value
		}
	}

	// Create transport with token
	transportConfig := mcp.StreamableHTTPConfig{
		URL:         handle.serverURL,
		BearerToken: token,
		BearerTokenProvider: func(callCtx context.Context) (string, error) {
			return s.tokenManager.GetAccessToken(callCtx, handle.serverURL)
		},
		HTTPHeaders: headers,
	}
	httpTransport := mcp.NewStreamableHTTPTransport(transportConfig)

	// Connect
	if err := httpTransport.Connect(ctx); err != nil {
		s.emitStatus(name, events.StateError, 0, nil, fmt.Sprintf("Connect failed: %v", err))
		return fmt.Errorf("connect: %w", err)
	}

	// Create client and initialize
	client := mcp.NewClient(httpTransport)

	initCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	if err := client.Initialize(initCtx); err != nil {
		_ = httpTransport.Close()
		s.emitStatus(name, events.StateError, 0, nil, fmt.Sprintf("MCP init failed: %v", err))
		return fmt.Errorf("initialize: %w", err)
	}

	// Update handle
	handle.ctx, handle.ctxCancel = context.WithCancel(context.Background())
	handle.client = client
	handle.httpTransport = httpTransport
	handle.authStatus = mcp.AuthStatusOAuthOK
	handle.done = make(chan struct{}) // Reset done channel
	handle.startedAt = time.Now()

	s.emitStatus(name, events.StateRunning, 0, nil, "")

	// Discover tools in background
	go s.discoverToolsAsync(handle, client, name)

	return nil
}
