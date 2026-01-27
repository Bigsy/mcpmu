package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// buildBinary builds the mcp-studio binary for testing.
// Returns the path to the binary.
func buildBinary(t *testing.T) string {
	t.Helper()

	binary := filepath.Join(t.TempDir(), "mcp-studio")
	cmd := exec.Command("go", "build", "-o", binary, ".")
	cmd.Dir = filepath.Join(getModuleRoot(t), "cmd", "mcp-studio")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to build binary: %v\n%s", err, out)
	}
	return binary
}

// getModuleRoot returns the root of the Go module.
func getModuleRoot(t *testing.T) string {
	t.Helper()

	// Walk up from current directory to find go.mod
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}

	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find module root")
		}
		dir = parent
	}
}

// setupTestConfig creates an empty config file and returns its path.
func setupTestConfig(t *testing.T) string {
	t.Helper()

	configPath := filepath.Join(t.TempDir(), "config.json")
	config := `{"schemaVersion": 1, "servers": {}, "namespaces": []}`
	if err := os.WriteFile(configPath, []byte(config), 0644); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}
	return configPath
}

// runCLI runs the mcp-studio binary with the given args.
// Returns stdout, stderr, and any error.
func runCLI(binary, configPath string, args ...string) (string, string, error) {
	fullArgs := append([]string{"--config", configPath}, args...)
	cmd := exec.Command(binary, fullArgs...)

	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

func TestCLI_Add(t *testing.T) {
	binary := buildBinary(t)
	configPath := setupTestConfig(t)

	// Add a server
	stdout, stderr, err := runCLI(binary, configPath, "add", "my-server", "--", "echo", "hello")
	if err != nil {
		t.Fatalf("add failed: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}

	if !strings.Contains(stdout, `Added server "my-server"`) {
		t.Errorf("expected success message, got: %s", stdout)
	}

	// Verify config file
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("failed to read config: %v", err)
	}

	var config map[string]interface{}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatalf("failed to parse config: %v", err)
	}

	servers := config["servers"].(map[string]interface{})
	if len(servers) != 1 {
		t.Errorf("expected 1 server, got %d", len(servers))
	}

	// Find the server (ID is auto-generated)
	var srv map[string]interface{}
	for _, s := range servers {
		srv = s.(map[string]interface{})
		break
	}

	if srv["name"] != "my-server" {
		t.Errorf("expected name 'my-server', got %v", srv["name"])
	}
	if srv["command"] != "echo" {
		t.Errorf("expected command 'echo', got %v", srv["command"])
	}
}

func TestCLI_Add_WithEnv(t *testing.T) {
	binary := buildBinary(t)
	configPath := setupTestConfig(t)

	stdout, stderr, err := runCLI(binary, configPath,
		"add", "my-server",
		"--env", "FOO=bar",
		"--env", "BAZ=qux",
		"--", "echo", "hello")
	if err != nil {
		t.Fatalf("add failed: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}

	// Verify env in config
	data, _ := os.ReadFile(configPath)
	var config map[string]interface{}
	json.Unmarshal(data, &config)

	servers := config["servers"].(map[string]interface{})
	var srv map[string]interface{}
	for _, s := range servers {
		srv = s.(map[string]interface{})
		break
	}

	env := srv["env"].(map[string]interface{})
	if env["FOO"] != "bar" || env["BAZ"] != "qux" {
		t.Errorf("expected env FOO=bar BAZ=qux, got %v", env)
	}
}

func TestCLI_Add_DuplicateName(t *testing.T) {
	binary := buildBinary(t)
	configPath := setupTestConfig(t)

	// Add first server
	_, _, err := runCLI(binary, configPath, "add", "my-server", "--", "echo", "hello")
	if err != nil {
		t.Fatalf("first add failed: %v", err)
	}

	// Try to add duplicate
	stdout, stderr, err := runCLI(binary, configPath, "add", "my-server", "--", "cat")
	if err == nil {
		t.Fatal("expected error for duplicate name")
	}

	output := stdout + stderr
	if !strings.Contains(output, "already exists") {
		t.Errorf("expected 'already exists' error, got: %s", output)
	}
}

func TestCLI_Add_MissingSeparator(t *testing.T) {
	binary := buildBinary(t)
	configPath := setupTestConfig(t)

	stdout, stderr, err := runCLI(binary, configPath, "add", "my-server", "echo", "hello")
	if err == nil {
		t.Fatal("expected error for missing separator")
	}

	output := stdout + stderr
	if !strings.Contains(output, "missing --") {
		t.Errorf("expected 'missing --' error, got: %s", output)
	}
}

func TestCLI_Add_MissingCommand(t *testing.T) {
	binary := buildBinary(t)
	configPath := setupTestConfig(t)

	stdout, stderr, err := runCLI(binary, configPath, "add", "my-server", "--")
	if err == nil {
		t.Fatal("expected error for missing command")
	}

	output := stdout + stderr
	if !strings.Contains(output, "missing command") {
		t.Errorf("expected 'missing command' error, got: %s", output)
	}
}

func TestCLI_Add_InvalidEnv(t *testing.T) {
	binary := buildBinary(t)
	configPath := setupTestConfig(t)

	stdout, stderr, err := runCLI(binary, configPath, "add", "my-server", "--env", "INVALID", "--", "echo")
	if err == nil {
		t.Fatal("expected error for invalid env")
	}

	output := stdout + stderr
	if !strings.Contains(output, "KEY=VALUE") {
		t.Errorf("expected KEY=VALUE error, got: %s", output)
	}
}

func TestCLI_List(t *testing.T) {
	binary := buildBinary(t)
	configPath := setupTestConfig(t)

	// Add servers
	runCLI(binary, configPath, "add", "alpha", "--", "echo", "a")
	runCLI(binary, configPath, "add", "beta", "--", "echo", "b")

	// List
	stdout, stderr, err := runCLI(binary, configPath, "list")
	if err != nil {
		t.Fatalf("list failed: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}

	if !strings.Contains(stdout, "alpha") || !strings.Contains(stdout, "beta") {
		t.Errorf("expected both servers in output, got: %s", stdout)
	}

	if !strings.Contains(stdout, "NAME") || !strings.Contains(stdout, "COMMAND") {
		t.Errorf("expected table headers, got: %s", stdout)
	}
}

func TestCLI_List_JSON(t *testing.T) {
	binary := buildBinary(t)
	configPath := setupTestConfig(t)

	// Add a server
	runCLI(binary, configPath, "add", "my-server", "--", "echo", "hello")

	// List as JSON
	stdout, stderr, err := runCLI(binary, configPath, "list", "--json")
	if err != nil {
		t.Fatalf("list --json failed: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}

	var servers []map[string]interface{}
	if err := json.Unmarshal([]byte(stdout), &servers); err != nil {
		t.Fatalf("failed to parse JSON: %v\noutput: %s", err, stdout)
	}

	if len(servers) != 1 {
		t.Errorf("expected 1 server, got %d", len(servers))
	}

	if servers[0]["name"] != "my-server" {
		t.Errorf("expected name 'my-server', got %v", servers[0]["name"])
	}

	// Verify ID is not in JSON output
	if _, hasID := servers[0]["id"]; hasID {
		t.Error("ID should not be exposed in JSON output")
	}
}

func TestCLI_List_Empty(t *testing.T) {
	binary := buildBinary(t)
	configPath := setupTestConfig(t)

	stdout, stderr, err := runCLI(binary, configPath, "list")
	if err != nil {
		t.Fatalf("list failed: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}

	if !strings.Contains(stdout, "No servers configured") {
		t.Errorf("expected 'No servers configured', got: %s", stdout)
	}
}

func TestCLI_Remove(t *testing.T) {
	binary := buildBinary(t)
	configPath := setupTestConfig(t)

	// Add a server
	runCLI(binary, configPath, "add", "my-server", "--", "echo", "hello")

	// Remove with --yes
	stdout, stderr, err := runCLI(binary, configPath, "remove", "my-server", "--yes")
	if err != nil {
		t.Fatalf("remove failed: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}

	if !strings.Contains(stdout, `Removed server "my-server"`) {
		t.Errorf("expected success message, got: %s", stdout)
	}

	// Verify server is gone
	listOut, _, _ := runCLI(binary, configPath, "list")
	if strings.Contains(listOut, "my-server") {
		t.Error("server should have been removed")
	}
}

func TestCLI_Remove_NotFound(t *testing.T) {
	binary := buildBinary(t)
	configPath := setupTestConfig(t)

	stdout, stderr, err := runCLI(binary, configPath, "remove", "nonexistent", "--yes")
	if err == nil {
		t.Fatal("expected error for non-existent server")
	}

	output := stdout + stderr
	if !strings.Contains(output, "not found") {
		t.Errorf("expected 'not found' error, got: %s", output)
	}
}
