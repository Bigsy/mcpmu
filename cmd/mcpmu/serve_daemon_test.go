//go:build !windows

package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

func TestServeDaemonModeSharesUpstreamAcrossSessions(t *testing.T) {
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
		"schemaVersion": 1, "daemonMode": true,
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

	for session := 1; session <= 2; session++ {
		stdout, stderr, err := runServeProcessWithInput(t, runtimeRoot, testInitializeAndList, "serve", "--config", configPath)
		if err != nil {
			t.Fatalf("serve session %d failed: %v\nstderr=%s", session, err, stderr)
		}
		requireInitializeResult(t, stdout)
		if !strings.Contains(stdout, `"id":2`) {
			t.Fatalf("serve session %d tools/list response missing: %s", session, stdout)
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

func TestServeDaemonModeAutoSpawnsDetachedShim(t *testing.T) {
	runtimeRoot, err := os.MkdirTemp("/tmp", "mu-serve-")
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

func TestServeAbsentConfigWithAbsentParent(t *testing.T) {
	runtimeRoot, err := os.MkdirTemp("/tmp", "mu-absent-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtimeRoot) })
	missing := filepath.Join(t.TempDir(), "not-created", "config.json")

	stdout, stderr, err := runServeProcess(t, runtimeRoot, "serve", "--config", missing)
	if err != nil {
		t.Fatalf("absent-config serve failed: %v\nstderr=%s", err, stderr)
	}
	requireInitializeResult(t, stdout)
}
