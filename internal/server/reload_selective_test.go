package server

import (
	"context"
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/process"
)

func TestSelectiveReloadRestartsOnlyChangedServer(t *testing.T) {
	ctx := context.Background()
	makeCfg := func() *config.Config {
		cfg := nsCompressConfig("")
		cfg.Servers["b"] = cfg.Servers["srv1"]
		ns := cfg.Namespaces["work"]
		ns.ServerIDs = append(ns.ServerIDs, "b")
		cfg.Namespaces["work"] = ns
		return cfg
	}
	cfg := makeCfg()
	core, err := NewCore(Options{Config: cfg, PIDTrackerDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer core.Close()
	s, _ := newDirectResourceSession(t, core, cfg, "work")
	defer s.Close()
	a, _, err := s.getOrStartHandle(ctx, "srv1")
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := s.getOrStartHandle(ctx, "b")
	if err != nil {
		t.Fatal(err)
	}
	next := makeCfg()
	changed := next.Servers["srv1"]
	changed.Env = maps.Clone(changed.Env)
	changed.Env["RELOAD_FIXTURE"] = "changed"
	next.Servers["srv1"] = changed
	core.applyReload(ctx, next, nil)
	aa, _, err := s.getOrStartHandle(ctx, "srv1")
	if err != nil {
		t.Fatal(err)
	}
	bb, _, err := s.getOrStartHandle(ctx, "b")
	if err != nil {
		t.Fatal(err)
	}
	if aa == a || aa.PID() == a.PID() || a.IsRunning() {
		t.Fatal("changed upstream not retired")
	}
	if bb != b || !b.IsRunning() {
		t.Fatal("unrelated upstream restarted")
	}
	if _, e := s.handleToolsCall(ctx, []byte(`{"name":"b.read_file","arguments":{}}`)); e != nil {
		t.Fatal(e)
	}
	// The old generation must never repopulate the shared catalog.
	if result, ok := a.DiscoveryResult(); ok {
		core.OnDiscoveryResult(result)
	}
	if entry := core.currentAggregator().catalog.snapshot(process.SharedInstanceID("srv1")); entry.generation != aa.Generation() {
		t.Fatal("stale discovery replaced new catalog")
	}
}

func TestSelectiveReloadSharingAndMembership(t *testing.T) {
	ctx := context.Background()
	cfg := nsCompressConfig("")
	core, err := NewCore(Options{Config: cfg, PIDTrackerDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer core.Close()
	first, _ := newDirectResourceSession(t, core, cfg, "work")
	defer first.Close()
	second, _ := newDirectResourceSession(t, core, cfg, "work")
	defer second.Close()
	shared, _, err := first.getOrStartHandle(ctx, "srv1")
	if err != nil {
		t.Fatal(err)
	}
	next := nsCompressConfig("")
	srv := next.Servers["srv1"]
	srv.Shared = new(false)
	next.Servers["srv1"] = srv
	core.applyReload(ctx, next, nil)
	a, _, err := first.getOrStartHandle(ctx, "srv1")
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := second.getOrStartHandle(ctx, "srv1")
	if err != nil {
		t.Fatal(err)
	}
	if shared.IsRunning() || a == b || a.PID() == b.PID() {
		t.Fatal("sharing transition leaked identity")
	}
	removed := *next
	removed.Namespaces = map[string]config.NamespaceConfig{"work": {ServerIDs: []string{}}}
	core.applyReload(ctx, &removed, nil)
	if core.supervisor.GetInstance(a.InstanceID()) != nil || core.supervisor.GetInstance(b.InstanceID()) != nil {
		t.Fatal("excluded private instances were not forgotten")
	}
	if a.IsRunning() || b.IsRunning() {
		t.Fatal("excluded private instances survived")
	}
	if _, _, e := first.getOrStartHandle(ctx, "srv1"); e == nil {
		t.Fatal("excluded private instance revived")
	}
}

func TestSelectiveReloadRetainsEligibleSubscriptions(t *testing.T) {
	ctx := context.Background()
	cfg := config.NewConfig()
	cfg.Servers["files"] = fakeServerConfig(t, map[string]any{"tools": []any{}, "resources": []any{map[string]any{"uri": "file:///test", "name": "test"}}, "resourcesSubscribe": true, "resourceContents": map[string]any{"file:///test": []any{map[string]any{"uri": "file:///test", "text": "fixture"}}}})
	cfg.Namespaces = map[string]config.NamespaceConfig{"a": {ServerIDs: []string{"files"}}, "b": {ServerIDs: []string{"files"}}}
	core, err := NewCore(Options{Config: cfg, PIDTrackerDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer core.Close()
	first, firstOut := newDirectResourceSession(t, core, cfg, "a")
	defer first.Close()
	second, secondOut := newDirectResourceSession(t, core, cfg, "b")
	defer second.Close()
	for _, s := range []*Session{first, second} {
		if _, e := s.handleResourcesList(ctx); e != nil {
			t.Fatal(e)
		}
		if _, e := s.handleResourcesSubscribe(ctx, []byte(`{"uri":"file:///test"}`)); e != nil {
			t.Fatal(e)
		}
	}
	handle := core.supervisor.Get("files")
	next := *cfg
	next.Namespaces = map[string]config.NamespaceConfig{"a": {ServerIDs: []string{}}, "b": {ServerIDs: []string{"files"}}}
	core.applyReload(ctx, &next, nil)
	if core.supervisor.Get("files") != handle || !handle.IsRunning() {
		t.Fatal("retained shared upstream restarted")
	}
	if len(first.subscriptionSnapshot()) != 0 || len(second.subscriptionSnapshot()) != 1 {
		t.Fatal("incorrect subscription pruning")
	}
	if _, e := first.handleResourcesRead(ctx, []byte(`{"uri":"file:///test"}`)); e == nil {
		t.Fatal("revoked resource readable")
	}
	if _, e := second.handleResourcesRead(ctx, []byte(`{"uri":"file:///test"}`)); e != nil {
		t.Fatal(e)
	}

	core.OnUpstreamNotification(process.UpstreamNotification{Instance: handle.InstanceID(), Generation: handle.Generation(), Upstream: true, Method: "notifications/resources/updated", Params: []byte(`{"uri":"file:///test"}`)})
	deadline := time.Now().Add(time.Second)
	for !strings.Contains(secondOut.String(), "notifications/resources/updated") && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !strings.Contains(secondOut.String(), "notifications/resources/updated") {
		t.Fatal("retained subscription lost notification")
	}
	if strings.Contains(firstOut.String(), "notifications/resources/updated") {
		t.Fatal("revoked session received notification")
	}
}

func TestSelectiveReloadKeepsBarrierUntilResourceRoutesPruned(t *testing.T) {
	ctx := context.Background()
	cfg := nsCompressConfig("")
	core, err := NewCore(Options{Config: cfg, PIDTrackerDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer core.Close()
	session, _ := newDirectResourceSession(t, core, cfg, "work")
	defer session.Close()
	old, _, err := session.getOrStartHandle(ctx, "srv1")
	if err != nil {
		t.Fatal(err)
	}
	next := nsCompressConfig("")
	srv := next.Servers["srv1"]
	srv.Env["RELOAD_FIXTURE"] = "changed"
	next.Servers["srv1"] = srv
	// Hold route cleanup after retirement. Until this lock is released, a new
	// acquisition could publish fresh routes that cleanup would wrongly delete.
	session.resourceMapMu.Lock()
	done := make(chan struct{})
	defer func() {
		session.resourceMapMu.Unlock()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("reload did not finish")
		}
	}()
	go func() { defer close(done); core.applyReload(ctx, next, nil) }()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for old.IsRunning() {
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatal("old instance was not retired")
		}
	}
	for range 10 {
		if _, _, err := session.getOrStartHandle(ctx, "srv1"); err == nil {
			t.Fatal("replacement started before resource-route cleanup finished")
		}
		<-ticker.C
	}
}
