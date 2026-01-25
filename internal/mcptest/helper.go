// Package mcptest provides test infrastructure for MCP client testing.
package mcptest

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/hedworth/mcp-studio-go/internal/mcptest/fakeserver"
)

// FakeServerConfig is an alias for fakeserver.Config for convenience.
type FakeServerConfig = fakeserver.Config

// Tool is an alias for fakeserver.Tool for convenience.
type Tool = fakeserver.Tool

// JSONRPCError is an alias for fakeserver.JSONRPCError for convenience.
type JSONRPCError = fakeserver.JSONRPCError

// StartFakeServer spawns a fake MCP server as a subprocess using the test helper pattern.
// Returns stdin (write to server), stdout (read from server), and a stop function.
// The stop function is also registered as a t.Cleanup.
func StartFakeServer(t *testing.T, cfg FakeServerConfig) (stdin io.WriteCloser, stdout io.ReadCloser, stop func()) {
	t.Helper()

	cfgJSON, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal fake server config: %v", err)
	}

	// Re-exec the test binary with the helper process marker
	cmd := exec.Command(os.Args[0], "-test.run=TestHelperProcess", "--")
	cmd.Env = append(os.Environ(),
		"GO_WANT_HELPER_PROCESS=1",
		"FAKE_MCP_CFG="+string(cfgJSON),
	)

	stdin, err = cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}

	stdout, err = cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatalf("stderr pipe: %v", err)
	}

	if err := cmd.Start(); err != nil {
		t.Fatalf("start fake server: %v", err)
	}

	// Drain stderr to prevent deadlock
	go io.Copy(io.Discard, stderr)

	stop = func() {
		// Close stdin to signal graceful shutdown
		_ = stdin.Close()

		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()

		select {
		case <-time.After(2 * time.Second):
			// Force kill if it doesn't exit gracefully
			_ = cmd.Process.Kill()
			<-done
		case <-done:
			// Process exited
		}
	}

	t.Cleanup(stop)
	return stdin, stdout, stop
}

// RunHelperProcess implements the fake MCP server when invoked as a subprocess.
// Other packages call this from their own TestHelperProcess:
//
//	func TestHelperProcess(t *testing.T) {
//	    mcptest.RunHelperProcess(t)
//	}
//
// The test re-exec pattern uses os.Args[0] with -test.run=TestHelperProcess to
// spawn the fake server process. This allows integration tests to run real subprocess
// communication without external dependencies.
func RunHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}

	cfgJSON := os.Getenv("FAKE_MCP_CFG")
	if cfgJSON == "" {
		os.Exit(2)
	}

	var cfg fakeserver.Config
	if err := json.Unmarshal([]byte(cfgJSON), &cfg); err != nil {
		os.Exit(2)
	}

	if err := fakeserver.Serve(context.Background(), os.Stdin, os.Stdout, cfg); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}
