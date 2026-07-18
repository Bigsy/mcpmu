//go:build !windows

package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Bigsy/mcpmu/internal/daemon"
)

const testInitialize = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}` + "\n"

const testInitializeAndList = testInitialize +
	`{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}` + "\n" +
	`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}` + "\n"

func writeServeConfig(t *testing.T, daemonMode bool) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	payload := map[string]any{
		"schemaVersion": 1,
		"daemonMode":    daemonMode,
		"servers":       map[string]any{},
		"namespaces":    map[string]any{},
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeDefaultServeConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	payload := map[string]any{
		"schemaVersion": 1,
		"servers":       map[string]any{},
		"namespaces":    map[string]any{},
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func runServeProcess(t *testing.T, runtimeRoot string, args ...string) (string, string, error) {
	return runServeProcessWithInput(t, runtimeRoot, testInitialize, args...)
}

func runServeProcessWithInput(t *testing.T, runtimeRoot, input string, args ...string) (string, string, error) {
	t.Helper()
	command := exec.Command(testBinary, args...)
	command.Env = append(os.Environ(), "XDG_RUNTIME_DIR="+runtimeRoot)
	command.Stdin = strings.NewReader(input)
	var stdout, stderr strings.Builder
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	return stdout.String(), stderr.String(), err
}

func TestServeDaemonModeSharesUpstreamAcrossTenConcurrentSessions(t *testing.T) {
	runtimeRoot, err := os.MkdirTemp("/tmp", "mu-share-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtimeRoot) })
	if err := os.Chmod(runtimeRoot, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_RUNTIME_DIR", runtimeRoot)
	tempDir := t.TempDir()
	pidLog := filepath.Join(tempDir, "pids")
	serverScript := filepath.Join(tempDir, "fake-mcp.sh")
	script := `#!/bin/sh
echo $$ >> "$PID_LOG"
while IFS= read -r line; do
  id=$(printf '%s' "$line" | sed -n 's/.*"id":\([0-9][0-9]*\).*/\1/p')
  case "$line" in
    *'"method":"initialize"'*)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2024-11-05","capabilities":{"tools":{}},"serverInfo":{"name":"shared-test","version":"1"}}}\n' "$id"
      ;;
    *'"method":"tools/list"'*)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"tools":[]}}\n' "$id"
      ;;
  esac
done
`
	if err := os.WriteFile(serverScript, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(tempDir, "config.json")
	payload := map[string]any{
		"schemaVersion": 1,
		"servers": map[string]any{
			"fake": map[string]any{"command": serverScript, "env": map[string]string{"PID_LOG": pidLog}},
		},
		"namespaces": map[string]any{},
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = daemon.Stop(ctx, configPath)
	})

	type result struct {
		session        int
		stdout, stderr string
		err            error
	}
	const sessions = 10
	start := make(chan struct{})
	results := make(chan result, sessions)
	var wg sync.WaitGroup
	for session := 1; session <= sessions; session++ {
		wg.Go(func() {
			<-start
			stdout, stderr, runErr := runServeProcessWithInput(t, runtimeRoot, testInitializeAndList, "serve", "--config", configPath)
			results <- result{session: session, stdout: stdout, stderr: stderr, err: runErr}
		})
	}
	close(start)
	wg.Wait()
	close(results)
	for result := range results {
		if result.err != nil {
			t.Fatalf("serve session %d failed: %v\nstderr=%s", result.session, result.err, result.stderr)
		}
		requireInitializeResult(t, result.stdout)
		if !strings.Contains(result.stdout, `"id":2`) {
			t.Fatalf("serve session %d tools/list response missing: %s", result.session, result.stdout)
		}
		if strings.Contains(result.stderr, "falling back") {
			t.Fatalf("serve session %d unexpectedly fell back: %s", result.session, result.stderr)
		}
	}
	pids, err := os.ReadFile(pidLog)
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Fields(string(pids)); len(lines) != 1 {
		t.Fatalf("upstream starts = %d (%q), want one shared process", len(lines), pids)
	}
}

func TestServeRecoversAfterDaemonCrash(t *testing.T) {
	runtimeRoot, err := os.MkdirTemp("/tmp", "mu-crash-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtimeRoot) })
	if err := os.Chmod(runtimeRoot, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_RUNTIME_DIR", runtimeRoot)
	configPath := writeServeConfig(t, true)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = daemon.Stop(ctx, configPath)
	})

	stdinReader, stdinWriter := io.Pipe()
	command := exec.Command(testBinary, "serve", "--config", configPath)
	command.Env = append(os.Environ(), "XDG_RUNTIME_DIR="+runtimeRoot)
	command.Stdin = stdinReader
	var stdout, stderr strings.Builder
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	waitStarted := false
	t.Cleanup(func() {
		_ = stdinWriter.Close()
		if !waited && command.Process != nil {
			_ = command.Process.Kill()
			if !waitStarted {
				_ = command.Wait()
			}
		}
	})
	if _, err := io.WriteString(stdinWriter, testInitialize); err != nil {
		t.Fatal(err)
	}

	var crashedPID int
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		status, _, inspectErr := daemon.Inspect(ctx, configPath)
		cancel()
		if inspectErr == nil && status.PID != 0 && status.Sessions == 1 {
			crashedPID = status.PID
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if crashedPID == 0 {
		t.Fatal("daemon session did not become ready")
	}
	daemonProcess, err := os.FindProcess(crashedPID)
	if err != nil {
		t.Fatal(err)
	}
	if err := daemonProcess.Kill(); err != nil {
		t.Fatalf("crash daemon: %v", err)
	}
	_ = stdinWriter.Close()
	waitDone := make(chan error, 1)
	waitStarted = true
	go func() { waitDone <- command.Wait() }()
	select {
	case <-waitDone:
		waited = true
	case <-time.After(3 * time.Second):
		t.Fatal("serve shim did not exit after daemon crash")
	}

	secondStdout, secondStderr, err := runServeProcess(t, runtimeRoot, "serve", "--config", configPath)
	if err != nil {
		t.Fatalf("replacement serve failed: %v\nstderr=%s", err, secondStderr)
	}
	requireInitializeResult(t, secondStdout)
	if strings.Contains(secondStderr, "falling back") {
		t.Fatalf("replacement serve fell back instead of resurrecting daemon: %s", secondStderr)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	status, fallback, err := daemon.Inspect(ctx, configPath)
	if err != nil {
		t.Fatalf("replacement daemon unavailable: %v", err)
	}
	if fallback || status.PID == 0 || status.PID == crashedPID {
		t.Fatalf("unexpected replacement daemon: fallback=%t status=%+v crashedPID=%d", fallback, status, crashedPID)
	}
}

func requireInitializeResult(t *testing.T, output string) {
	t.Helper()
	for line := range strings.SplitSeq(output, "\n") {
		if line == "" {
			continue
		}
		var message map[string]any
		if json.Unmarshal([]byte(line), &message) == nil && message["id"] == float64(1) && message["result"] != nil {
			return
		}
	}
	t.Fatalf("initialize result missing from %q", output)
}

func TestServeDefaultDaemonModeAutoSpawnsDetachedShim(t *testing.T) {
	runtimeRoot, err := os.MkdirTemp("/tmp", "mu-serve-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtimeRoot) })
	if err := os.Chmod(runtimeRoot, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_RUNTIME_DIR", runtimeRoot)
	configPath := writeDefaultServeConfig(t)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = daemon.Stop(ctx, configPath)
	})

	stdout, stderr, err := runServeProcess(t, runtimeRoot, "serve", "--config", configPath)
	if err != nil {
		t.Fatalf("serve failed: %v\nstderr=%s", err, stderr)
	}
	requireInitializeResult(t, stdout)
	if strings.Contains(stderr, "falling back") {
		t.Fatalf("serve unexpectedly fell back: %s", stderr)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	status, fallback, err := daemon.Inspect(ctx, configPath)
	if err != nil {
		t.Fatalf("auto-spawned daemon unavailable: %v\nstderr=%s", err, stderr)
	}
	if fallback || status.Sessions != 0 || status.PID == 0 {
		t.Fatalf("unexpected auto-spawned daemon status: fallback=%t status=%+v", fallback, status)
	}
}

func TestServeIsolatedBypassesEnabledDaemonMode(t *testing.T) {
	runtimeRoot, err := os.MkdirTemp("/tmp", "mu-isolated-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtimeRoot) })
	if err := os.Chmod(runtimeRoot, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_RUNTIME_DIR", runtimeRoot)
	configPath := writeServeConfig(t, true)
	canonical, err := daemon.CanonicalConfigPath(configPath)
	if err != nil {
		t.Fatal(err)
	}
	paths, err := daemon.RuntimePaths(canonical)
	if err != nil {
		t.Fatal(err)
	}

	stdout, stderr, err := runServeProcess(t, runtimeRoot, "serve", "--config", configPath, "--isolated")
	if err != nil {
		t.Fatalf("isolated serve failed: %v\nstderr=%s", err, stderr)
	}
	requireInitializeResult(t, stdout)
	if _, err := os.Stat(paths.Socket); !os.IsNotExist(err) {
		t.Fatalf("isolated serve touched daemon socket: %v", err)
	}
}

func TestServeDaemonModeFalseBypassesSharedDaemon(t *testing.T) {
	runtimeRoot, err := os.MkdirTemp("/tmp", "mu-disabled-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtimeRoot) })
	if err := os.Chmod(runtimeRoot, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_RUNTIME_DIR", runtimeRoot)
	configPath := writeServeConfig(t, false)
	canonical, err := daemon.CanonicalConfigPath(configPath)
	if err != nil {
		t.Fatal(err)
	}
	paths, err := daemon.RuntimePaths(canonical)
	if err != nil {
		t.Fatal(err)
	}

	stdout, stderr, err := runServeProcess(t, runtimeRoot, "serve", "--config", configPath)
	if err != nil {
		t.Fatalf("daemon-disabled serve failed: %v\nstderr=%s", err, stderr)
	}
	requireInitializeResult(t, stdout)
	if _, err := os.Stat(paths.Socket); !os.IsNotExist(err) {
		t.Fatalf("daemonMode:false serve touched daemon socket: %v", err)
	}
}

func TestServeDaemonFailureFallsBackToEmbedded(t *testing.T) {
	longRuntime := filepath.Join(t.TempDir(), strings.Repeat("runtime-segment-", 8))
	if err := os.MkdirAll(longRuntime, 0700); err != nil {
		t.Fatal(err)
	}
	configPath := writeServeConfig(t, true)

	stdout, stderr, err := runServeProcess(t, longRuntime, "serve", "--config", configPath)
	if err != nil {
		t.Fatalf("fallback serve failed: %v\nstderr=%s", err, stderr)
	}
	requireInitializeResult(t, stdout)
	if strings.Count(stderr, "shared daemon unavailable; falling back to embedded serve") != 1 {
		t.Fatalf("fallback warning count != 1: %s", stderr)
	}
}

func TestServeRejectsInvalidLogLevelBeforeDaemonStartup(t *testing.T) {
	command := exec.Command(testBinary, "serve", "--log-level", "verbose")
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), `invalid log level "verbose"`) {
		t.Fatalf("serve error = %v, output = %s", err, output)
	}
}

func TestServeAbsentConfigWithAbsentParent(t *testing.T) {
	runtimeRoot, err := os.MkdirTemp("/tmp", "mu-absent-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtimeRoot) })
	t.Setenv("XDG_RUNTIME_DIR", runtimeRoot)
	missing := filepath.Join(t.TempDir(), "not-created", "config.json")
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = daemon.Stop(ctx, missing)
	})

	stdout, stderr, err := runServeProcess(t, runtimeRoot, "serve", "--config", missing)
	if err != nil {
		t.Fatalf("absent-config serve failed: %v\nstderr=%s", err, stderr)
	}
	requireInitializeResult(t, stdout)
}
