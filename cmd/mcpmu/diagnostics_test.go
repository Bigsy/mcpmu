package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Bigsy/mcpmu/internal/config"
)

func TestDiagnosticsReadOnly(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "md-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	t.Setenv("XDG_RUNTIME_DIR", dir)
	old := configPath
	configPath = filepath.Join(dir, "absent.json")
	defer func() { configPath = old }()
	r, err := collectDiagnostics(context.Background(), true)
	if err != nil || !r.DefaultsUsed || !r.ConfigValid || r.DaemonState != "not_running" {
		t.Fatalf("%+v: %v", r, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("diagnostics wrote files: %v", entries)
	}
	cfg := config.NewConfig()
	cfg.Servers["broken"] = config.ServerConfig{Command: "./missing"}
	if err := config.SaveTo(cfg, configPath); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	r, err = collectDiagnostics(context.Background(), true)
	if err == nil || len(r.Checks) != 2 || r.Checks[1].OK {
		t.Fatalf("missing executable accepted: %+v", r)
	}
	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("config changed")
	}
}

func TestDiagnosticExecutable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fixture")
	if err := os.WriteFile(path, []byte("do not execute"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"./fixture", path, "fixture"} {
		if !diagnosticExecutable(command, dir, dir) {
			t.Errorf("not found: %s", command)
		}
	}
	if diagnosticExecutable("absent", dir, dir) {
		t.Fatal("found absent command")
	}
}

func TestDoctorEnvironmentAndRedaction(t *testing.T) {
	dir := t.TempDir()
	old := configPath
	configPath = filepath.Join(dir, "config.json")
	defer func() { configPath = old }()
	t.Setenv("MCPMU_DOCTOR_FIXTURE", "")
	cfg := config.NewConfig()
	cfg.Servers["remote"] = config.ServerConfig{URL: "https://example.test/mcp", BearerTokenEnvVar: "MCPMU_DOCTOR_FIXTURE", Env: map[string]string{"MCPMU_DOCTOR_FIXTURE": "secret-value"}}
	if err := config.SaveTo(cfg, configPath); err != nil {
		t.Fatal(err)
	}
	report, err := collectDiagnostics(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "secret-value") {
		t.Fatal("secret exposed")
	}
	if len(report.Checks) != 1 || !report.Checks[0].OK {
		t.Fatalf("override ignored: %+v", report.Checks)
	}
	srv := cfg.Servers["remote"]
	srv.Env = nil
	cfg.Servers["remote"] = srv
	if err := config.SaveTo(cfg, configPath); err != nil {
		t.Fatal(err)
	}
	if _, err := collectDiagnostics(context.Background(), true); err == nil {
		t.Fatal("missing variable accepted")
	}
	if err := os.WriteFile(configPath, []byte(`{"servers":"secret-value"}`), 0600); err != nil {
		t.Fatal(err)
	}
	report, err = collectDiagnostics(context.Background(), true)
	if err == nil || report.ConfigValid || strings.Contains(err.Error(), "secret-value") {
		t.Fatalf("invalid config diagnosis: %+v %v", report, err)
	}
}
