// Package process provides process lifecycle management for MCP servers.
package process

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/hedworth/mcp-studio-go/internal/config"
	"github.com/hedworth/mcp-studio-go/internal/events"
	"github.com/hedworth/mcp-studio-go/internal/mcp"
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
	bus        *events.Bus
	handles    map[string]*Handle
	pidTracker *PIDTracker
	mu         sync.RWMutex
}

// NewSupervisor creates a new process supervisor.
// It also cleans up any orphan processes from previous runs.
func NewSupervisor(bus *events.Bus) *Supervisor {
	pidTracker, err := NewPIDTracker()
	if err != nil {
		log.Printf("Warning: failed to create PID tracker: %v", err)
	} else {
		// Clean up orphans on startup
		if killed := pidTracker.CleanupOrphans(); killed > 0 {
			log.Printf("Cleaned up %d orphan process(es)", killed)
		}
	}

	return &Supervisor{
		bus:        bus,
		handles:    make(map[string]*Handle),
		pidTracker: pidTracker,
	}
}

// Start starts an MCP server process.
func (s *Supervisor) Start(ctx context.Context, srv config.ServerConfig) (*Handle, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	log.Printf("Starting server: id=%s cmd=%s args=%v", srv.ID, srv.Command, srv.Args)

	// Check if already running
	if h, exists := s.handles[srv.ID]; exists && h.IsRunning() {
		return nil, fmt.Errorf("server %s is already running", srv.ID)
	}

	// Emit starting event
	s.emitStatus(srv.ID, events.StateStarting, 0, nil, "")

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
		s.emitStatus(srv.ID, events.StateError, 0, nil, err.Error())
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		s.emitStatus(srv.ID, events.StateError, 0, nil, err.Error())
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		s.emitStatus(srv.ID, events.StateError, 0, nil, err.Error())
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}

	// Start the process
	if err := cmd.Start(); err != nil {
		s.emitStatus(srv.ID, events.StateError, 0, nil, err.Error())
		return nil, fmt.Errorf("start process: %w", err)
	}

	// Track PID for orphan cleanup
	if s.pidTracker != nil {
		if err := s.pidTracker.Add(srv.ID, cmd.Process.Pid, srv.Command, srv.Args); err != nil {
			log.Printf("Warning: failed to track PID: %v", err)
		}
	}

	// Create transport and client
	transport := mcp.NewStdioTransport(stdin, stdout)
	client := mcp.NewClient(transport)

	// Create handle
	handle := &Handle{
		id:        srv.ID,
		cmd:       cmd,
		client:    client,
		transport: transport,
		logs:      make([]string, 0, 1000),
		bus:       s.bus,
		startedAt: time.Now(),
		done:      make(chan struct{}),
	}

	s.handles[srv.ID] = handle

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
		handle.Stop()
		s.emitStatus(srv.ID, events.StateError, cmd.Process.Pid, nil, fmt.Sprintf("MCP init failed after %d attempts: %v", MaxInitRetries, initErr))
		return nil, fmt.Errorf("initialize mcp: %w", initErr)
	}

	// Discover tools
	toolsCtx, toolsCancel := context.WithTimeout(ctx, 30*time.Second)
	defer toolsCancel()
	tools, err := client.ListTools(toolsCtx)
	if err != nil {
		// Non-fatal - server is still running
		s.bus.Publish(events.NewErrorEvent(srv.ID, err, "Failed to list tools"))
	} else {
		handle.tools = tools
		mcpTools := make([]events.McpTool, len(tools))
		for i, t := range tools {
			mcpTools[i] = events.McpTool{
				Name:        t.Name,
				Description: t.Description,
			}
		}
		s.bus.Publish(events.NewToolsUpdatedEvent(srv.ID, mcpTools))
	}

	// Emit running event
	s.emitStatus(srv.ID, events.StateRunning, cmd.Process.Pid, nil, "")

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
			s.Stop(id)
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

// Handle represents a running server process.
type Handle struct {
	id        string
	cmd       *exec.Cmd
	client    *mcp.Client
	transport *mcp.StdioTransport
	tools     []mcp.Tool
	logs      []string
	logsMu    sync.RWMutex
	bus       *events.Bus
	startedAt time.Time
	stopped   bool
	stopMu    sync.Mutex
	done      chan struct{} // closed when process exits
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
	return h.tools
}

// Logs returns the captured stderr logs.
func (h *Handle) Logs() []string {
	h.logsMu.RLock()
	defer h.logsMu.RUnlock()
	logs := make([]string, len(h.logs))
	copy(logs, h.logs)
	return logs
}

// PID returns the process ID.
func (h *Handle) PID() int {
	if h.cmd.Process == nil {
		return 0
	}
	return h.cmd.Process.Pid
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

// Stop gracefully stops the server process.
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

	// Close MCP client first
	h.client.Close()

	// Send SIGTERM - watchProcess goroutine handles Wait()
	if h.cmd.Process != nil {
		h.cmd.Process.Signal(syscall.SIGTERM)

		// Wait for watchProcess to signal completion with timeout
		select {
		case <-h.done:
			// Process exited gracefully
		case <-time.After(GracefulShutdownTimeout):
			// Force kill
			h.cmd.Process.Signal(syscall.SIGKILL)
			<-h.done
		}
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
