//go:build integration

package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Bigsy/mcpmu/internal/config"
)

func TestTmux_HTTPServerDetail(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}

	// Build binary
	tmpDir := t.TempDir()
	binary := filepath.Join(tmpDir, "mcpmu")
	build := exec.Command("go", "build", "-o", binary, "./cmd/mcpmu")
	build.Dir = findRepoRoot(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build failed: %v\n%s", err, out)
	}

	// Write test config with an HTTP bearer server and a stdio server.
	// Servers are sorted alphabetically, so "alpha-http" comes before "beta-stdio".
	cfg := config.NewConfig()
	cfg.Servers["alpha-http"] = config.ServerConfig{
		URL:               "https://mcp.example.com/v1",
		BearerTokenEnvVar: "TEST_TOKEN",
	}
	cfg.Servers["beta-stdio"] = config.ServerConfig{
		Command: "echo",
		Args:    []string{"hello", "world"},
	}

	cfgData, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	cfgPath := filepath.Join(tmpDir, "config.json")
	if err := os.WriteFile(cfgPath, cfgData, 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	session := fmt.Sprintf("mcpmu-test-%d", os.Getpid())
	t.Cleanup(func() {
		_ = exec.Command("tmux", "kill-session", "-t", session).Run()
	})

	// Start tmux session
	tmuxCmd := exec.Command("tmux", "new-session", "-d",
		"-s", session,
		"-x", "120", "-y", "40",
		binary, "--config", cfgPath,
	)
	if out, err := tmuxCmd.CombinedOutput(); err != nil {
		t.Fatalf("tmux new-session failed: %v\n%s", err, out)
	}

	// Wait for TUI to render
	time.Sleep(2 * time.Second)

	// The first server alphabetically is "alpha-http".
	// It should be selected by default. Press Enter to open detail.
	sendKeys(t, session, "Enter")
	time.Sleep(500 * time.Millisecond)

	content := capturePane(t, session)

	// HTTP detail should show URL and Bearer info
	if !strings.Contains(content, "https://mcp.example.com/v1") {
		t.Errorf("HTTP detail missing URL.\nCaptured pane:\n%s", content)
	}
	if !strings.Contains(content, "Bearer") {
		t.Errorf("HTTP detail missing Bearer auth.\nCaptured pane:\n%s", content)
	}
	if !strings.Contains(content, "$TEST_TOKEN") {
		t.Errorf("HTTP detail missing $TEST_TOKEN.\nCaptured pane:\n%s", content)
	}
	if strings.Contains(content, "Command:") {
		t.Errorf("HTTP detail should not show 'Command:'.\nCaptured pane:\n%s", content)
	}

	// Go back to list, navigate down to stdio server, open detail
	sendKeys(t, session, "Escape")
	time.Sleep(300 * time.Millisecond)
	sendKeys(t, session, "j") // move down to beta-stdio
	time.Sleep(300 * time.Millisecond)
	sendKeys(t, session, "Enter")
	time.Sleep(500 * time.Millisecond)

	content = capturePane(t, session)

	// Stdio detail should show Command
	if !strings.Contains(content, "Command:") {
		t.Errorf("Stdio detail missing 'Command:'.\nCaptured pane:\n%s", content)
	}
	if !strings.Contains(content, "echo hello world") {
		t.Errorf("Stdio detail missing command text.\nCaptured pane:\n%s", content)
	}
	if strings.Contains(content, "URL:") {
		t.Errorf("Stdio detail should not show 'URL:'.\nCaptured pane:\n%s", content)
	}

	// Quit
	sendKeys(t, session, "Escape")
	time.Sleep(200 * time.Millisecond)
	sendKeys(t, session, "q")
}

func sendKeys(t *testing.T, session, keys string) {
	t.Helper()
	if out, err := exec.Command("tmux", "send-keys", "-t", session, keys).CombinedOutput(); err != nil {
		t.Fatalf("tmux send-keys %q failed: %v\n%s", keys, err, out)
	}
}

func capturePane(t *testing.T, session string) string {
	t.Helper()
	out, err := exec.Command("tmux", "capture-pane", "-t", session, "-p").Output()
	if err != nil {
		t.Fatalf("tmux capture-pane failed: %v", err)
	}
	return string(out)
}

func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find repo root (no go.mod found)")
		}
		dir = parent
	}
}
