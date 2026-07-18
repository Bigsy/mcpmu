//go:build !windows

package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Bigsy/mcpmu/internal/daemon"
)

func TestDaemonCLI_RunStatusStop(t *testing.T) {
	runtimeRoot, err := os.MkdirTemp("/tmp", "mu-cli-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtimeRoot) })
	t.Setenv("XDG_RUNTIME_DIR", runtimeRoot)
	configPath := setupTestConfig(t)
	canonical, err := daemon.CanonicalConfigPath(configPath)
	if err != nil {
		t.Fatal(err)
	}
	paths, err := daemon.RuntimePaths(canonical)
	if err != nil {
		t.Fatal(err)
	}

	run := exec.Command(testBinary, "--config", configPath, "daemon", "run")
	run.Env = append(os.Environ(), "XDG_RUNTIME_DIR="+runtimeRoot)
	var runStderr strings.Builder
	run.Stderr = &runStderr
	if err := run.Start(); err != nil {
		t.Fatal(err)
	}
	finished := false
	t.Cleanup(func() {
		if finished || run.Process == nil {
			return
		}
		_ = run.Process.Signal(syscall.SIGTERM)
		_ = run.Wait()
	})

	deadline := time.Now().Add(5 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		_, _, inspectErr := daemon.Inspect(ctx, canonical)
		cancel()
		if inspectErr == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("daemon did not become ready: %v; stderr=%s", inspectErr, runStderr.String())
		}
		time.Sleep(20 * time.Millisecond)
	}

	status := exec.Command(testBinary, "--config", configPath, "daemon", "status", "--json")
	status.Env = append(os.Environ(), "XDG_RUNTIME_DIR="+runtimeRoot)
	statusOutput, err := status.CombinedOutput()
	if err != nil {
		t.Fatalf("daemon status failed: %v\n%s", err, statusOutput)
	}
	var payload struct {
		daemon.StatusResponse
		PIDFileFallback bool `json:"pidfileFallback"`
	}
	if err := json.Unmarshal(statusOutput, &payload); err != nil {
		t.Fatalf("parse status %q: %v", statusOutput, err)
	}
	if payload.ConfigPath != canonical || payload.PID != run.Process.Pid || payload.PIDFileFallback {
		t.Fatalf("unexpected status: %+v", payload)
	}

	stop := exec.Command(testBinary, "--config", configPath, "daemon", "stop")
	stop.Env = append(os.Environ(), "XDG_RUNTIME_DIR="+runtimeRoot)
	stopOutput, err := stop.CombinedOutput()
	if err != nil {
		t.Fatalf("daemon stop failed: %v\n%s", err, stopOutput)
	}
	if !strings.Contains(string(stopOutput), "Daemon stopping") {
		t.Fatalf("unexpected stop output: %s", stopOutput)
	}

	done := make(chan error, 1)
	go func() { done <- run.Wait() }()
	select {
	case err := <-done:
		finished = true
		if err != nil {
			t.Fatalf("daemon run exited with error: %v; stderr=%s", err, runStderr.String())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("daemon run did not exit after stop")
	}

	logData, err := os.ReadFile(paths.LogFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logData), "mcpmu daemon listening") {
		t.Fatalf("per-config daemon log missing listener message: %s", logData)
	}
	if info, err := os.Stat(paths.LogFile); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != 0600 {
		t.Fatalf("daemon log mode = %o, want 600", info.Mode().Perm())
	}
	if _, err := os.Stat(paths.Socket); !os.IsNotExist(err) {
		t.Fatalf("socket remains after daemon exit: %v", err)
	}
	if _, err := os.Stat(paths.PIDFile); !os.IsNotExist(err) {
		t.Fatalf("pidfile remains after daemon exit: %v", err)
	}
}

func TestDaemonCLI_RunRequiresConfig(t *testing.T) {
	command := exec.Command(testBinary, "daemon", "run", "--foreground")
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "daemon run requires --config") {
		t.Fatalf("daemon run error = %v, output = %s", err, output)
	}
}

func TestDaemonCLI_InvalidLogLevel(t *testing.T) {
	configPath := setupTestConfig(t)
	command := exec.Command(testBinary, "--config", filepath.Clean(configPath), "daemon", "run", "--foreground", "--log-level", "loud")
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "invalid log level") {
		t.Fatalf("daemon run error = %v, output = %s", err, output)
	}
}

func TestDaemonErrorLogLevelRetainsDiagnostics(t *testing.T) {
	previousOutput := log.Writer()
	previousFlags := log.Flags()
	defer func() {
		log.SetOutput(previousOutput)
		log.SetFlags(previousFlags)
	}()
	var output strings.Builder
	if err := configureDaemonLogging("error", &output); err != nil {
		t.Fatal(err)
	}
	log.Print("daemon diagnostic sentinel")
	if !strings.Contains(output.String(), "daemon diagnostic sentinel") {
		t.Fatalf("error-level daemon logging discarded diagnostics: %q", output.String())
	}
}
