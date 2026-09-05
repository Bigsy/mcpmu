package process

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/events"
	"github.com/Bigsy/mcpmu/internal/mcp"
	"github.com/Bigsy/mcpmu/internal/oauth"
)

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

	// Signal the stdio process group before closing the MCP client. A child
	// that stopped reading stdin fills its pipe; Send then blocks forever
	// holding the framing mutex, and client.Close needs that same mutex —
	// closing first would hang teardown exactly when it matters. SIGTERM
	// settles the child (or its writer) before Close runs.
	if h.kind == HandleKindStdio && h.cmd != nil && h.cmd.Process != nil && h.pgid > 0 {
		_ = terminateProcessGroupGracefully(h.pgid)
	}

	// Close MCP client (may be nil for needs-auth state). Snapshot both under
	// authMu and close outside it, so a slow Close cannot stall readers.
	h.authMu.RLock()
	client, httpTransport := h.client, h.httpTransport
	h.authMu.RUnlock()

	if client != nil {
		_ = client.Close()
	}

	if h.kind == HandleKindStdio {
		// The watcher retires the PGID only after the leader is reaped and
		// any surviving workers are gone.
		if h.cmd != nil && h.cmd.Process != nil && h.pgid > 0 {
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
