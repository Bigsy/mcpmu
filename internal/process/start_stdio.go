package process

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/events"
	"github.com/Bigsy/mcpmu/internal/mcp"
)

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
