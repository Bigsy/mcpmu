package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Bigsy/mcpmu/internal/config"
)

// buildBinary builds the mcpmu binary for testing.
// Returns the path to the binary.
func buildBinary(t *testing.T) string {
	t.Helper()

	binary := filepath.Join(t.TempDir(), "mcpmu")
	cmd := exec.Command("go", "build", "-o", binary, ".")
	cmd.Dir = filepath.Join(getModuleRoot(t), "cmd", "mcpmu")
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
	config := `{"schemaVersion": 1, "servers": {}, "namespaces": {}}`
	if err := os.WriteFile(configPath, []byte(config), 0644); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}
	return configPath
}

// runCLI runs the mcpmu binary with the given args.
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

// getServerName verifies the server exists and returns its name (which is also the ID now)
func getServerName(t *testing.T, configPath, name string) string {
	t.Helper()
	cfg, err := config.LoadFrom(configPath)
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}
	if cfg.GetServer(name) == nil {
		t.Fatalf("server %q not found in config", name)
	}
	return name
}

func TestParseEnvFlags(t *testing.T) {
	tests := []struct {
		name    string
		input   []string
		want    map[string]string
		wantErr bool
	}{
		{
			name:  "nil input",
			input: nil,
			want:  nil,
		},
		{
			name:  "empty input",
			input: []string{},
			want:  nil,
		},
		{
			name:  "single valid",
			input: []string{"FOO=bar"},
			want:  map[string]string{"FOO": "bar"},
		},
		{
			name:  "multiple valid",
			input: []string{"FOO=bar", "BAZ=qux"},
			want:  map[string]string{"FOO": "bar", "BAZ": "qux"},
		},
		{
			name:  "empty value",
			input: []string{"FOO="},
			want:  map[string]string{"FOO": ""},
		},
		{
			name:  "value with equals",
			input: []string{"FOO=bar=baz"},
			want:  map[string]string{"FOO": "bar=baz"},
		},
		{
			name:    "missing equals",
			input:   []string{"INVALID"},
			wantErr: true,
		},
		{
			name:    "empty key",
			input:   []string{"=value"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseEnvFlags(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseEnvFlags() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if len(got) != len(tt.want) {
					t.Errorf("parseEnvFlags() got %v, want %v", got, tt.want)
					return
				}
				for k, v := range tt.want {
					if got[k] != v {
						t.Errorf("parseEnvFlags()[%q] = %q, want %q", k, got[k], v)
					}
				}
			}
		})
	}
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

	// Server name is now the map key
	srv, exists := servers["my-server"].(map[string]interface{})
	if !exists {
		t.Fatal("expected server 'my-server' to exist")
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
	_ = json.Unmarshal(data, &config)

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

func TestCLI_Add_HTTP_PositionalURL(t *testing.T) {
	binary := buildBinary(t)
	configPath := setupTestConfig(t)

	// Add HTTP server with URL as positional arg (no --url flag)
	stdout, stderr, err := runCLI(binary, configPath, "add", "my-api", "https://example.com/mcp")
	if err != nil {
		t.Fatalf("add failed: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}

	if !strings.Contains(stdout, `Added HTTP server "my-api"`) {
		t.Errorf("expected success message, got: %s", stdout)
	}

	// Verify config file
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("failed to read config: %v", err)
	}

	var cfg map[string]interface{}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("failed to parse config: %v", err)
	}

	servers := cfg["servers"].(map[string]interface{})
	if len(servers) != 1 {
		t.Errorf("expected 1 server, got %d", len(servers))
	}

	// Server name is now the map key
	srv, exists := servers["my-api"].(map[string]interface{})
	if !exists {
		t.Fatal("expected server 'my-api' to exist")
	}

	// Kind may be omitted when inferred from URL
	if srv["url"] != "https://example.com/mcp" {
		t.Errorf("expected url 'https://example.com/mcp', got %v", srv["url"])
	}
}

func TestCLI_Add_HTTP_PositionalURL_WithBearerEnv(t *testing.T) {
	binary := buildBinary(t)
	configPath := setupTestConfig(t)

	stdout, stderr, err := runCLI(binary, configPath, "add", "figma", "https://mcp.figma.com/mcp", "--bearer-env", "FIGMA_TOKEN")
	if err != nil {
		t.Fatalf("add failed: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}

	if !strings.Contains(stdout, `Added HTTP server "figma"`) {
		t.Errorf("expected success message, got: %s", stdout)
	}

	// Verify bearer env in config
	data, _ := os.ReadFile(configPath)
	var cfg map[string]interface{}
	_ = json.Unmarshal(data, &cfg)

	servers := cfg["servers"].(map[string]interface{})
	var srv map[string]interface{}
	for _, s := range servers {
		srv = s.(map[string]interface{})
		break
	}

	if srv["bearer_token_env_var"] != "FIGMA_TOKEN" {
		t.Errorf("expected bearer_token_env_var 'FIGMA_TOKEN', got %v", srv["bearer_token_env_var"])
	}
}

func TestCLI_List(t *testing.T) {
	binary := buildBinary(t)
	configPath := setupTestConfig(t)

	// Add servers
	_, _, _ = runCLI(binary, configPath, "add", "alpha", "--", "echo", "a")
	_, _, _ = runCLI(binary, configPath, "add", "beta", "--", "echo", "b")

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
	_, _, _ = runCLI(binary, configPath, "add", "my-server", "--", "echo", "hello")

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
	_, _, _ = runCLI(binary, configPath, "add", "my-server", "--", "echo", "hello")

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

// ============================================================================
// Namespace CLI Tests
// ============================================================================

func TestCLI_Namespace_Add(t *testing.T) {
	binary := buildBinary(t)
	configPath := setupTestConfig(t)

	stdout, stderr, err := runCLI(binary, configPath, "namespace", "add", "dev", "--description", "Development")
	if err != nil {
		t.Fatalf("namespace add failed: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}

	if !strings.Contains(stdout, `Added namespace "dev"`) {
		t.Errorf("expected success message, got: %s", stdout)
	}

	// Verify in config
	data, _ := os.ReadFile(configPath)
	var config map[string]interface{}
	_ = json.Unmarshal(data, &config)

	namespaces := config["namespaces"].(map[string]interface{})
	if len(namespaces) != 1 {
		t.Errorf("expected 1 namespace, got %d", len(namespaces))
	}

	// Namespace name is now the map key
	ns, exists := namespaces["dev"].(map[string]interface{})
	if !exists {
		t.Fatal("expected namespace 'dev' to exist")
	}
	if ns["description"] != "Development" {
		t.Errorf("expected description 'Development', got %v", ns["description"])
	}
}

func TestCLI_Namespace_Add_DuplicateName(t *testing.T) {
	binary := buildBinary(t)
	configPath := setupTestConfig(t)

	_, _, _ = runCLI(binary, configPath, "namespace", "add", "dev")

	stdout, stderr, err := runCLI(binary, configPath, "namespace", "add", "dev")
	if err == nil {
		t.Fatal("expected error for duplicate name")
	}

	output := stdout + stderr
	if !strings.Contains(output, "already exists") {
		t.Errorf("expected 'already exists' error, got: %s", output)
	}
}

func TestCLI_Namespace_List(t *testing.T) {
	binary := buildBinary(t)
	configPath := setupTestConfig(t)

	_, _, _ = runCLI(binary, configPath, "namespace", "add", "dev", "--description", "Development")
	_, _, _ = runCLI(binary, configPath, "namespace", "add", "prod", "--description", "Production")

	stdout, stderr, err := runCLI(binary, configPath, "namespace", "list")
	if err != nil {
		t.Fatalf("namespace list failed: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}

	if !strings.Contains(stdout, "dev") || !strings.Contains(stdout, "prod") {
		t.Errorf("expected both namespaces in output, got: %s", stdout)
	}

	if !strings.Contains(stdout, "NAME") || !strings.Contains(stdout, "DESCRIPTION") {
		t.Errorf("expected table headers, got: %s", stdout)
	}
}

func TestCLI_Namespace_List_JSON(t *testing.T) {
	binary := buildBinary(t)
	configPath := setupTestConfig(t)

	_, _, _ = runCLI(binary, configPath, "namespace", "add", "dev")

	stdout, stderr, err := runCLI(binary, configPath, "namespace", "list", "--json")
	if err != nil {
		t.Fatalf("namespace list --json failed: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}

	var namespaces []map[string]interface{}
	if err := json.Unmarshal([]byte(stdout), &namespaces); err != nil {
		t.Fatalf("failed to parse JSON: %v\noutput: %s", err, stdout)
	}

	if len(namespaces) != 1 {
		t.Errorf("expected 1 namespace, got %d", len(namespaces))
	}

	if namespaces[0]["name"] != "dev" {
		t.Errorf("expected name 'dev', got %v", namespaces[0]["name"])
	}
}

func TestCLI_Namespace_List_Empty(t *testing.T) {
	binary := buildBinary(t)
	configPath := setupTestConfig(t)

	stdout, stderr, err := runCLI(binary, configPath, "namespace", "list")
	if err != nil {
		t.Fatalf("namespace list failed: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}

	if !strings.Contains(stdout, "No namespaces configured") {
		t.Errorf("expected 'No namespaces configured', got: %s", stdout)
	}
}

func TestCLI_Namespace_Remove(t *testing.T) {
	binary := buildBinary(t)
	configPath := setupTestConfig(t)

	_, _, _ = runCLI(binary, configPath, "namespace", "add", "dev")

	stdout, stderr, err := runCLI(binary, configPath, "namespace", "remove", "dev", "--yes")
	if err != nil {
		t.Fatalf("namespace remove failed: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}

	if !strings.Contains(stdout, `Removed namespace "dev"`) {
		t.Errorf("expected success message, got: %s", stdout)
	}

	// Verify namespace is gone
	listOut, _, _ := runCLI(binary, configPath, "namespace", "list")
	if strings.Contains(listOut, "dev") && !strings.Contains(listOut, "No namespaces") {
		t.Error("namespace should have been removed")
	}
}

func TestCLI_Namespace_Remove_NotFound(t *testing.T) {
	binary := buildBinary(t)
	configPath := setupTestConfig(t)

	stdout, stderr, err := runCLI(binary, configPath, "namespace", "remove", "nonexistent", "--yes")
	if err == nil {
		t.Fatal("expected error for non-existent namespace")
	}

	output := stdout + stderr
	if !strings.Contains(output, "not found") {
		t.Errorf("expected 'not found' error, got: %s", output)
	}
}

func TestCLI_Namespace_Assign(t *testing.T) {
	binary := buildBinary(t)
	configPath := setupTestConfig(t)

	// Add server and namespace
	_, _, _ = runCLI(binary, configPath, "add", "my-server", "--", "echo", "hello")
	_, _, _ = runCLI(binary, configPath, "namespace", "add", "dev")

	stdout, stderr, err := runCLI(binary, configPath, "namespace", "assign", "dev", "my-server")
	if err != nil {
		t.Fatalf("namespace assign failed: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}

	if !strings.Contains(stdout, `Assigned server "my-server" to namespace "dev"`) {
		t.Errorf("expected success message, got: %s", stdout)
	}

	// Verify in list
	listOut, _, _ := runCLI(binary, configPath, "namespace", "list")
	if !strings.Contains(listOut, "1") { // 1 server assigned
		t.Errorf("expected 1 server count, got: %s", listOut)
	}
}

func TestCLI_Namespace_Assign_NotFound(t *testing.T) {
	binary := buildBinary(t)
	configPath := setupTestConfig(t)

	_, _, _ = runCLI(binary, configPath, "namespace", "add", "dev")

	stdout, stderr, err := runCLI(binary, configPath, "namespace", "assign", "dev", "nonexistent")
	if err == nil {
		t.Fatal("expected error for non-existent server")
	}

	output := stdout + stderr
	if !strings.Contains(output, "not found") {
		t.Errorf("expected 'not found' error, got: %s", output)
	}
}

func TestCLI_Namespace_Unassign(t *testing.T) {
	binary := buildBinary(t)
	configPath := setupTestConfig(t)

	_, _, _ = runCLI(binary, configPath, "add", "my-server", "--", "echo", "hello")
	_, _, _ = runCLI(binary, configPath, "namespace", "add", "dev")
	_, _, _ = runCLI(binary, configPath, "namespace", "assign", "dev", "my-server")

	stdout, stderr, err := runCLI(binary, configPath, "namespace", "unassign", "dev", "my-server")
	if err != nil {
		t.Fatalf("namespace unassign failed: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}

	if !strings.Contains(stdout, `Unassigned server "my-server" from namespace "dev"`) {
		t.Errorf("expected success message, got: %s", stdout)
	}
}

func TestCLI_Namespace_Default(t *testing.T) {
	binary := buildBinary(t)
	configPath := setupTestConfig(t)

	_, _, _ = runCLI(binary, configPath, "namespace", "add", "dev")

	stdout, stderr, err := runCLI(binary, configPath, "namespace", "default", "dev")
	if err != nil {
		t.Fatalf("namespace default failed: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}

	if !strings.Contains(stdout, `Set default namespace to "dev"`) {
		t.Errorf("expected success message, got: %s", stdout)
	}

	// Verify in list output (should show *)
	listOut, _, _ := runCLI(binary, configPath, "namespace", "list")
	if !strings.Contains(listOut, "*") {
		t.Errorf("expected default indicator (*), got: %s", listOut)
	}
}

func TestCLI_Namespace_SetDenyDefault(t *testing.T) {
	binary := buildBinary(t)
	configPath := setupTestConfig(t)

	_, _, _ = runCLI(binary, configPath, "namespace", "add", "prod")

	stdout, stderr, err := runCLI(binary, configPath, "namespace", "set-deny-default", "prod", "true")
	if err != nil {
		t.Fatalf("namespace set-deny-default failed: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}

	if !strings.Contains(stdout, `Deny-by-default enabled for namespace "prod"`) {
		t.Errorf("expected success message, got: %s", stdout)
	}

	// Verify in list
	listOut, _, _ := runCLI(binary, configPath, "namespace", "list")
	if !strings.Contains(listOut, "yes") { // deny-default column shows "yes"
		t.Errorf("expected deny-default 'yes', got: %s", listOut)
	}
}

// ============================================================================
// Permission CLI Tests
// ============================================================================

func TestCLI_Permission_Set(t *testing.T) {
	binary := buildBinary(t)
	configPath := setupTestConfig(t)

	_, _, _ = runCLI(binary, configPath, "add", "api-server", "--", "echo", "hello")
	_, _, _ = runCLI(binary, configPath, "namespace", "add", "prod")

	stdout, stderr, err := runCLI(binary, configPath, "permission", "set", "prod", "api-server", "create_user", "deny")
	if err != nil {
		t.Fatalf("permission set failed: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}

	if !strings.Contains(stdout, "denied") {
		t.Errorf("expected 'denied' in output, got: %s", stdout)
	}
}

func TestCLI_Permission_Set_NotFound(t *testing.T) {
	binary := buildBinary(t)
	configPath := setupTestConfig(t)

	_, _, _ = runCLI(binary, configPath, "namespace", "add", "prod")

	stdout, stderr, err := runCLI(binary, configPath, "permission", "set", "prod", "nonexistent", "tool", "allow")
	if err == nil {
		t.Fatal("expected error for non-existent server")
	}

	output := stdout + stderr
	if !strings.Contains(output, "not found") {
		t.Errorf("expected 'not found' error, got: %s", output)
	}
}

func TestCLI_Permission_List(t *testing.T) {
	binary := buildBinary(t)
	configPath := setupTestConfig(t)

	_, _, _ = runCLI(binary, configPath, "add", "api-server", "--", "echo", "hello")
	_, _, _ = runCLI(binary, configPath, "namespace", "add", "prod")
	_, _, _ = runCLI(binary, configPath, "permission", "set", "prod", "api-server", "create_user", "deny")
	_, _, _ = runCLI(binary, configPath, "permission", "set", "prod", "api-server", "read_user", "allow")

	stdout, stderr, err := runCLI(binary, configPath, "permission", "list", "prod")
	if err != nil {
		t.Fatalf("permission list failed: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}

	if !strings.Contains(stdout, "create_user") || !strings.Contains(stdout, "read_user") {
		t.Errorf("expected both permissions in output, got: %s", stdout)
	}

	if !strings.Contains(stdout, "deny") || !strings.Contains(stdout, "allow") {
		t.Errorf("expected permission values, got: %s", stdout)
	}
}

func TestCLI_Permission_List_JSON(t *testing.T) {
	binary := buildBinary(t)
	configPath := setupTestConfig(t)

	_, _, _ = runCLI(binary, configPath, "add", "api-server", "--", "echo", "hello")
	_, _, _ = runCLI(binary, configPath, "namespace", "add", "prod")
	_, _, _ = runCLI(binary, configPath, "permission", "set", "prod", "api-server", "create_user", "deny")

	stdout, stderr, err := runCLI(binary, configPath, "permission", "list", "prod", "--json")
	if err != nil {
		t.Fatalf("permission list --json failed: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}

	var result map[string]interface{}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("failed to parse JSON: %v\noutput: %s", err, stdout)
	}

	if result["namespace"] != "prod" {
		t.Errorf("expected namespace 'prod', got %v", result["namespace"])
	}

	permissions := result["permissions"].([]interface{})
	if len(permissions) != 1 {
		t.Errorf("expected 1 permission, got %d", len(permissions))
	}
}

func TestCLI_Permission_List_Empty(t *testing.T) {
	binary := buildBinary(t)
	configPath := setupTestConfig(t)

	_, _, _ = runCLI(binary, configPath, "namespace", "add", "prod")

	stdout, stderr, err := runCLI(binary, configPath, "permission", "list", "prod")
	if err != nil {
		t.Fatalf("permission list failed: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}

	if !strings.Contains(stdout, "No explicit permissions") {
		t.Errorf("expected 'No explicit permissions', got: %s", stdout)
	}
}

func TestCLI_Permission_Unset(t *testing.T) {
	binary := buildBinary(t)
	configPath := setupTestConfig(t)

	_, _, _ = runCLI(binary, configPath, "add", "api-server", "--", "echo", "hello")
	_, _, _ = runCLI(binary, configPath, "namespace", "add", "prod")
	_, _, _ = runCLI(binary, configPath, "permission", "set", "prod", "api-server", "create_user", "deny")

	stdout, stderr, err := runCLI(binary, configPath, "permission", "unset", "prod", "api-server", "create_user")
	if err != nil {
		t.Fatalf("permission unset failed: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}

	if !strings.Contains(stdout, "Removed permission") {
		t.Errorf("expected success message, got: %s", stdout)
	}

	// Verify permission is gone
	listOut, _, _ := runCLI(binary, configPath, "permission", "list", "prod")
	if strings.Contains(listOut, "create_user") {
		t.Error("permission should have been removed")
	}
}

func TestCLI_Permission_Set_NormalizesQualifiedName(t *testing.T) {
	binary := buildBinary(t)
	configPath := setupTestConfig(t)

	_, _, _ = runCLI(binary, configPath, "add", "api-server", "--", "echo", "hello")
	_, _, _ = runCLI(binary, configPath, "namespace", "add", "prod")

	serverID := getServerName(t, configPath, "api-server")

	// Use a qualified name like "<serverID>.create_user" - should be normalized to "create_user"
	stdout, stderr, err := runCLI(binary, configPath, "permission", "set", "prod", "api-server", serverID+".create_user", "deny")
	if err != nil {
		t.Fatalf("permission set failed: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}

	// List should show "create_user" not "srv.create_user"
	listOut, _, _ := runCLI(binary, configPath, "permission", "list", "prod")
	if strings.Contains(listOut, "srv.create_user") {
		t.Error("qualified name should have been normalized")
	}
	if !strings.Contains(listOut, "create_user") {
		t.Error("expected normalized tool name 'create_user'")
	}
}

func TestCLI_Permission_Unset_NormalizesQualifiedName(t *testing.T) {
	binary := buildBinary(t)
	configPath := setupTestConfig(t)

	_, _, _ = runCLI(binary, configPath, "add", "api-server", "--", "echo", "hello")
	_, _, _ = runCLI(binary, configPath, "namespace", "add", "prod")
	_, _, _ = runCLI(binary, configPath, "permission", "set", "prod", "api-server", "create_user", "deny")

	serverID := getServerName(t, configPath, "api-server")

	// Unset with a qualified name - should still work
	stdout, stderr, err := runCLI(binary, configPath, "permission", "unset", "prod", "api-server", serverID+".create_user")
	if err != nil {
		t.Fatalf("permission unset failed: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}

	if !strings.Contains(stdout, "Removed permission") {
		t.Errorf("expected success message, got: %s", stdout)
	}

	// Verify permission is gone
	listOut, _, _ := runCLI(binary, configPath, "permission", "list", "prod")
	if strings.Contains(listOut, "create_user") {
		t.Error("permission should have been removed")
	}
}

func TestCLI_Permission_DoesNotStripDotsInToolNames(t *testing.T) {
	binary := buildBinary(t)
	configPath := setupTestConfig(t)

	_, _, _ = runCLI(binary, configPath, "add", "api-server", "--", "echo", "hello")
	_, _, _ = runCLI(binary, configPath, "namespace", "add", "prod")

	stdout, stderr, err := runCLI(binary, configPath, "permission", "set", "prod", "api-server", "fs.read_file", "deny")
	if err != nil {
		t.Fatalf("permission set failed: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}

	listOut, _, _ := runCLI(binary, configPath, "permission", "list", "prod")
	if !strings.Contains(listOut, "fs.read_file") {
		t.Error("expected tool name with dots to be preserved")
	}
	if strings.Contains(stdout, "Removed permission") || strings.Contains(stderr, "invalid") {
		t.Logf("unexpected output while setting permission: stdout=%q stderr=%q", stdout, stderr)
	}
}
