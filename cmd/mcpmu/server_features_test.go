package main

import (
	"strings"
	"testing"

	"github.com/Bigsy/mcpmu/internal/config"
)

func loadServer(t *testing.T, configPath, name string) config.ServerConfig {
	t.Helper()
	cfg, err := config.LoadFrom(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	srv, ok := cfg.GetServer(name)
	if !ok {
		t.Fatalf("server %q missing", name)
	}
	return srv
}

func TestCLI_AddElicitation(t *testing.T) {
	t.Parallel()
	configPath := setupTestConfig(t)
	if stdout, stderr, err := runCLI(testBinary, configPath, "add", "browser", "--shared=false", "--elicitation", "--", "browser-mcp"); err != nil {
		t.Fatalf("add: %v\n%s%s", err, stdout, stderr)
	}
	if srv := loadServer(t, configPath, "browser"); !srv.ElicitationEnabled() || srv.IsShared() {
		t.Errorf("server = %+v, want private with elicitation", srv)
	}
	if stdout, stderr, err := runCLI(testBinary, configPath, "add", "api", "https://example.test/mcp", "--elicitation"); err != nil {
		t.Fatalf("add http: %v\n%s%s", err, stdout, stderr)
	}
	if !loadServer(t, configPath, "api").ElicitationEnabled() {
		t.Error("HTTP add dropped --elicitation")
	}
}

func TestCLI_SetClientFeature(t *testing.T) {
	t.Parallel()
	configPath := setupTestConfig(t)
	if _, stderr, err := runCLI(testBinary, configPath, "add", "browser", "--", "browser-mcp"); err != nil {
		t.Fatalf("add: %v %s", err, stderr)
	}
	if stdout, stderr, err := runCLI(testBinary, configPath, "server", "set-client-feature", "browser", "elicitation", "on"); err != nil {
		t.Fatalf("set on: %v\n%s%s", err, stdout, stderr)
	}
	if !loadServer(t, configPath, "browser").ElicitationEnabled() {
		t.Fatal("elicitation not enabled")
	}
	if _, stderr, err := runCLI(testBinary, configPath, "server", "set-client-feature", "browser", "elicitation", "off"); err != nil {
		t.Fatalf("set off: %v %s", err, stderr)
	}
	if srv := loadServer(t, configPath, "browser"); srv.ClientFeatures != nil {
		t.Errorf("clientFeatures = %+v, want the block dropped once every feature is off", srv.ClientFeatures)
	}

	_, stderr, err := runCLI(testBinary, configPath, "server", "set-client-feature", "browser", "telepathy", "on")
	if err == nil || !strings.Contains(stderr, "unknown client feature") {
		t.Errorf("unknown feature: err %v stderr %q", err, stderr)
	}
	if _, _, err := runCLI(testBinary, configPath, "server", "set-client-feature", "missing", "elicitation", "on"); err == nil {
		t.Error("setting a feature on a missing server succeeded")
	}
}
