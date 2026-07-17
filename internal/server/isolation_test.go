package server

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/process"
	"github.com/Bigsy/mcpmu/internal/testutil"
)

func TestPrivateServerInstancesAreSessionScoped(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess test")
	}
	testutil.SetupTestHome(t)
	shared := false
	cfg := &config.Config{SchemaVersion: 1, Servers: map[string]config.ServerConfig{
		"browser": fakeServerConfig(t, map[string]any{
			"tools": []any{map[string]any{"name": "navigate"}},
		}),
	}}
	private := cfg.Servers["browser"]
	private.Shared = &shared
	cfg.Servers["browser"] = private

	pidDir := t.TempDir()
	core, err := NewCore(Options{Config: cfg, PIDTrackerDir: pidDir})
	if err != nil {
		t.Fatalf("NewCore: %v", err)
	}
	t.Cleanup(core.Close)

	firstOut := &synchronizedBuffer{}
	first, err := NewSession(core, Options{Config: cfg, ExposePrompts: true, Stdin: strings.NewReader(""), Stdout: firstOut})
	if err != nil {
		t.Fatalf("NewSession first: %v", err)
	}
	secondOut := &synchronizedBuffer{}
	second, err := NewSession(core, Options{Config: cfg, ExposePrompts: true, Stdin: strings.NewReader(""), Stdout: secondOut})
	if err != nil {
		t.Fatalf("NewSession second: %v", err)
	}
	defer second.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	firstHandle, _, err := first.getOrStartHandle(ctx, "browser")
	if err != nil {
		t.Fatalf("start first private instance: %v", err)
	}
	secondHandle, _, err := second.getOrStartHandle(ctx, "browser")
	if err != nil {
		t.Fatalf("start second private instance: %v", err)
	}
	if firstHandle == secondHandle || firstHandle.PID() == secondHandle.PID() {
		t.Fatalf("private sessions shared a process: first=%d second=%d", firstHandle.PID(), secondHandle.PID())
	}
	firstID := process.PrivateInstanceID("browser", first.id)
	secondID := process.PrivateInstanceID("browser", second.id)
	if firstHandle.InstanceID() != firstID || secondHandle.InstanceID() != secondID {
		t.Fatalf("instance IDs = %v, %v; want %v, %v", firstHandle.InstanceID(), secondHandle.InstanceID(), firstID, secondID)
	}
	registryFiles, err := filepath.Glob(filepath.Join(pidDir, "pids-owner-*.json"))
	if err != nil || len(registryFiles) != 1 {
		t.Fatalf("PID registry files = %v, err=%v", registryFiles, err)
	}
	registryData, err := os.ReadFile(registryFiles[0])
	if err != nil {
		t.Fatal(err)
	}
	var registry struct {
		Entries []json.RawMessage `json:"entries"`
	}
	if err := json.Unmarshal(registryData, &registry); err != nil {
		t.Fatal(err)
	}
	if len(registry.Entries) != 2 {
		t.Fatalf("PID registry entries = %d, want both private instances", len(registry.Entries))
	}
	if core.currentAggregator().catalog.snapshot(process.SharedInstanceID("browser")).state != catalogUnknown {
		t.Fatal("private discovery leaked into the shared catalog")
	}
	if first.privateAggregatorSnapshot().catalog.snapshot(firstID).state != catalogVerified ||
		second.privateAggregatorSnapshot().catalog.snapshot(secondID).state != catalogVerified {
		t.Fatal("private discovery was not recorded in each session catalog")
	}

	// Manager operations resolve the caller's private identity.
	if _, rpcErr := first.router.handleServersStop(ctx, json.RawMessage(`{"server_id":"browser"}`)); rpcErr != nil {
		t.Fatalf("first manager stop: %v", rpcErr)
	}
	if firstHandle.IsRunning() {
		t.Fatal("first private instance remained running after caller stop")
	}
	if !secondHandle.IsRunning() {
		t.Fatal("first session manager stop affected second private instance")
	}

	// Private upstream notifications reach only the owning session.
	core.notifications.Publish(process.UpstreamNotification{
		Instance: secondID, Method: "notifications/prompts/list_changed",
	})
	deadline := time.Now().Add(time.Second)
	for !strings.Contains(secondOut.String(), "notifications/prompts/list_changed") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(secondOut.String(), "notifications/prompts/list_changed") {
		t.Fatal("owning session did not receive private notification")
	}
	if strings.Contains(firstOut.String(), "notifications/prompts/list_changed") {
		t.Fatal("private notification leaked to another session")
	}

	first.Close()
	if core.supervisor.GetInstance(firstID) != nil {
		t.Fatal("closed session retained its private handle")
	}
	if !secondHandle.IsRunning() {
		t.Fatal("closing first session stopped second session's private instance")
	}
}

func TestAbsentSharedFieldUsesOneSharedInstance(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess test")
	}
	testutil.SetupTestHome(t)
	cfg := &config.Config{SchemaVersion: 1, Servers: map[string]config.ServerConfig{
		"files": fakeServerConfig(t, map[string]any{"tools": []any{map[string]any{"name": "read"}}}),
	}}
	core, err := NewCore(Options{Config: cfg, PIDTrackerDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(core.Close)
	first, _ := NewSession(core, Options{Config: cfg, Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{}})
	second, _ := NewSession(core, Options{Config: cfg, Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{}})
	defer first.Close()
	defer second.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	one, _, err := first.getOrStartHandle(ctx, "files")
	if err != nil {
		t.Fatal(err)
	}
	two, _, err := second.getOrStartHandle(ctx, "files")
	if err != nil {
		t.Fatal(err)
	}
	if one != two || !one.InstanceID().IsShared() {
		t.Fatal("absent shared field did not use one shared instance")
	}
}

func TestPrivateStartRacingDisconnectCannotLeak(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess test")
	}
	testutil.SetupTestHome(t)
	shared := false
	srv := fakeServerConfig(t, map[string]any{
		"tools":  []any{map[string]any{"name": "navigate"}},
		"delays": map[string]int64{"initialize": int64(100 * time.Millisecond)},
	})
	srv.Shared = &shared
	cfg := &config.Config{SchemaVersion: 1, Servers: map[string]config.ServerConfig{"browser": srv}}
	core, err := NewCore(Options{Config: cfg, PIDTrackerDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(core.Close)
	session, err := NewSession(core, Options{Config: cfg, Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{}})
	if err != nil {
		t.Fatal(err)
	}
	id := process.PrivateInstanceID("browser", session.id)

	started := make(chan struct{})
	done := make(chan struct{})
	go func() {
		close(started)
		_, _, _ = session.getOrStartHandle(context.Background(), "browser")
		close(done)
	}()
	<-started
	time.Sleep(20 * time.Millisecond)
	session.Close()
	<-done

	if handle := core.supervisor.GetInstance(id); handle != nil {
		t.Fatalf("disconnect racing start retained private handle: running=%t", handle.IsRunning())
	}
	if got := core.supervisor.RunningCount(); got != 0 {
		t.Fatalf("running instances after disconnect = %d, want 0", got)
	}
}

func TestStalePrivateDiscoveryCannotStartSharedInstanceAfterReload(t *testing.T) {
	testutil.SetupTestHome(t)
	privateFlag := false
	privateConfig := config.ServerConfig{Command: "echo", Shared: &privateFlag}
	initial := &config.Config{SchemaVersion: 1, Servers: map[string]config.ServerConfig{"browser": privateConfig}}
	core, err := NewCore(Options{Config: initial, PIDTrackerDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(core.Close)
	session, err := NewSession(core, Options{Config: initial, Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{}})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	stalePrivateCatalog := session.privateAggregatorSnapshot()

	sharedConfig := config.ServerConfig{Command: "echo"}
	reloaded := &config.Config{SchemaVersion: 1, Servers: map[string]config.ServerConfig{"browser": sharedConfig}}
	core.applyReload(context.Background(), reloaded, nil)

	if _, err := stalePrivateCatalog.DiscoverServer(context.Background(), "browser"); err == nil ||
		!strings.Contains(err.Error(), "sharing mode changed") {
		t.Fatalf("stale private discovery error = %v, want sharing-mode barrier", err)
	}
	if handle := core.supervisor.GetInstance(process.PrivateInstanceID("browser", session.id)); handle != nil {
		t.Fatal("stale private discovery started an instance after shared-mode reload")
	}
}
