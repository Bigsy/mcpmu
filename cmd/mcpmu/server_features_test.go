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

func TestCLI_Roots(t *testing.T) {
	t.Parallel()
	configPath := setupTestConfig(t)
	if stdout, stderr, err := runCLI(testBinary, configPath, "add", "fs", "--root", "/work/app", "--root", "file:///work/lib", "--", "fs-mcp"); err != nil {
		t.Fatalf("add: %v\n%s%s", err, stdout, stderr)
	}
	if got := loadServer(t, configPath, "fs").Roots; len(got) != 2 || got[0] != "file:///work/app" || got[1] != "file:///work/lib" {
		t.Fatalf("roots after add = %v", got)
	}
	if _, stderr, err := runCLI(testBinary, configPath, "server", "set-roots", "fs", "/other"); err != nil {
		t.Fatalf("set-roots: %v %s", err, stderr)
	}
	if got := loadServer(t, configPath, "fs").Roots; len(got) != 1 || got[0] != "file:///other" {
		t.Fatalf("roots after set-roots = %v", got)
	}
	if _, stderr, err := runCLI(testBinary, configPath, "server", "set-roots", "fs"); err != nil {
		t.Fatalf("clear: %v %s", err, stderr)
	}
	if got := loadServer(t, configPath, "fs").Roots; got != nil {
		t.Fatalf("roots after clearing = %v", got)
	}
	_, stderr, err := runCLI(testBinary, configPath, "server", "set-roots", "fs", "relative/dir")
	if err == nil || !strings.Contains(stderr, "absolute path") {
		t.Errorf("relative root: err %v stderr %q", err, stderr)
	}
	if _, stderr, err := runCLI(testBinary, configPath, "server", "set-client-feature", "fs", "roots", "on"); err != nil {
		t.Fatalf("roots relay opt-in: %v %s", err, stderr)
	}
	if !loadServer(t, configPath, "fs").RootsRelayEnabled() {
		t.Error("roots relay not enabled")
	}
}
