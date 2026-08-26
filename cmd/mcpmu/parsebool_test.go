package main

import (
	"strings"
	"testing"

	"github.com/Bigsy/mcpmu/internal/config"
)

func TestParseBool(t *testing.T) {
	for _, s := range []string{"true", "TRUE", "yes", "1", "allow", "on", " Allow "} {
		if v, err := parseBool(s); err != nil || !v {
			t.Errorf("parseBool(%q) = %v, %v; want true", s, v, err)
		}
	}
	for _, s := range []string{"false", "No", "0", "deny", "off"} {
		if v, err := parseBool(s); err != nil || v {
			t.Errorf("parseBool(%q) = %v, %v; want false", s, v, err)
		}
	}
	for _, s := range []string{"", "maybe", "2", "enabled"} {
		if _, err := parseBool(s); err == nil {
			t.Errorf("parseBool(%q) accepted; want error", s)
		}
	}
}

// TestCLI_BoolConventions pins what the literal strings "true" and "false"
// mean to each of the three commands that take a boolean positional:
//
//	permission set            <allow|deny>  → true = tool allowed
//	permission set-server-default <deny|allow> → true = allow (deny-default off)
//	namespace set-deny-default <true|false>  → true = deny-by-default on
func TestCLI_BoolConventions(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		args  []string
		value string
		check func(t *testing.T, cfg *config.Config)
	}{
		{"permission set true", []string{"permission", "set", "prod", "api", "tool"}, "true",
			func(t *testing.T, cfg *config.Config) { wantToolEnabled(t, cfg, true) }},
		{"permission set false", []string{"permission", "set", "prod", "api", "tool"}, "false",
			func(t *testing.T, cfg *config.Config) { wantToolEnabled(t, cfg, false) }},
		{"permission set allow", []string{"permission", "set", "prod", "api", "tool"}, "allow",
			func(t *testing.T, cfg *config.Config) { wantToolEnabled(t, cfg, true) }},
		{"set-server-default true", []string{"permission", "set-server-default", "prod", "api"}, "true",
			func(t *testing.T, cfg *config.Config) { wantServerDenyDefault(t, cfg, false) }},
		{"set-server-default false", []string{"permission", "set-server-default", "prod", "api"}, "false",
			func(t *testing.T, cfg *config.Config) { wantServerDenyDefault(t, cfg, true) }},
		{"set-server-default deny", []string{"permission", "set-server-default", "prod", "api"}, "deny",
			func(t *testing.T, cfg *config.Config) { wantServerDenyDefault(t, cfg, true) }},
		{"set-deny-default true", []string{"namespace", "set-deny-default", "prod"}, "true",
			func(t *testing.T, cfg *config.Config) { wantNamespaceDenyDefault(t, cfg, true) }},
		{"set-deny-default false", []string{"namespace", "set-deny-default", "prod"}, "false",
			func(t *testing.T, cfg *config.Config) { wantNamespaceDenyDefault(t, cfg, false) }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			configPath := setupTestConfig(t)
			mustCLI(t, configPath, "add", "api", "--", "echo", "hello")
			mustCLI(t, configPath, "namespace", "add", "prod")
			mustCLI(t, configPath, append(tc.args, tc.value)...)

			cfg, err := config.LoadFrom(configPath)
			if err != nil {
				t.Fatal(err)
			}
			tc.check(t, cfg)
		})
	}

	t.Run("garbage rejected", func(t *testing.T) {
		t.Parallel()
		configPath := setupTestConfig(t)
		mustCLI(t, configPath, "namespace", "add", "prod")
		_, stderr, err := runCLI(testBinary, configPath, "namespace", "set-deny-default", "prod", "maybe")
		if err == nil || !strings.Contains(stderr, "invalid value") {
			t.Fatalf("expected invalid value error, got err=%v stderr=%s", err, stderr)
		}
	})
}

func mustCLI(t *testing.T, configPath string, args ...string) {
	t.Helper()
	stdout, stderr, err := runCLI(testBinary, configPath, args...)
	if err != nil {
		t.Fatalf("%v failed: %v\nstdout: %s\nstderr: %s", args, err, stdout, stderr)
	}
}

func wantToolEnabled(t *testing.T, cfg *config.Config, want bool) {
	t.Helper()
	for _, tp := range cfg.ToolPermissions {
		if tp.Namespace == "prod" && tp.Server == "api" {
			if tp.Enabled != want {
				t.Fatalf("tool Enabled=%v, want %v", tp.Enabled, want)
			}
			return
		}
	}
	t.Fatal("no tool permission recorded")
}

func wantServerDenyDefault(t *testing.T, cfg *config.Config, want bool) {
	t.Helper()
	ns, ok := cfg.GetNamespace("prod")
	if !ok {
		t.Fatal("namespace missing")
	}
	got, ok := ns.ServerDefaults["api"]
	if !ok {
		t.Fatal("no server default recorded")
	}
	if got != want {
		t.Fatalf("ServerDefaults[api]=%v, want %v", got, want)
	}
}

func wantNamespaceDenyDefault(t *testing.T, cfg *config.Config, want bool) {
	t.Helper()
	ns, ok := cfg.GetNamespace("prod")
	if !ok {
		t.Fatal("namespace missing")
	}
	if ns.DenyByDefault != want {
		t.Fatalf("DenyByDefault=%v, want %v", ns.DenyByDefault, want)
	}
}
