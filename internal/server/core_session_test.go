package server

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/process"
	"github.com/Bigsy/mcpmu/internal/testutil"
)

func TestCoreSessionLifecycle(t *testing.T) {
	testutil.SetupTestHome(t)
	opts := Options{
		Config:        &config.Config{SchemaVersion: 1, Servers: map[string]config.ServerConfig{}},
		PIDTrackerDir: t.TempDir(),
		Stdin:         strings.NewReader(""),
		Stdout:        &bytes.Buffer{},
	}
	core, err := NewCore(opts)
	if err != nil {
		t.Fatalf("NewCore: %v", err)
	}
	t.Cleanup(core.Close)

	first, err := NewSession(core, opts)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if first.Core != core {
		t.Fatal("session is not attached to the requested core")
	}
	concurrent, err := NewSession(core, opts)
	if err != nil {
		t.Fatalf("second concurrent NewSession: %v", err)
	}
	concurrent.Close()

	first.Close()
	second, err := NewSession(core, opts)
	if err != nil {
		t.Fatalf("NewSession after close: %v", err)
	}
	second.Close()
}

func TestCoreReloadUpdatesEverySession(t *testing.T) {
	testutil.SetupTestHome(t)
	initial := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"a": {Command: "echo"}, "b": {Command: "echo"},
		},
		Namespaces: map[string]config.NamespaceConfig{
			"first":  {ServerIDs: []string{"a"}},
			"second": {ServerIDs: []string{"b"}},
		},
	}
	core, err := NewCore(Options{Config: initial, PIDTrackerDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(core.Close)

	firstOutput := &bytes.Buffer{}
	first, err := NewSession(core, Options{
		SessionOptions: SessionOptions{
			Namespace:       "first",
			ExposeResources: true,
		},
		Config: initial,
		Stdin:  strings.NewReader(""), Stdout: firstOutput,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	secondOutput := &bytes.Buffer{}
	second, err := NewSession(core, Options{
		SessionOptions: SessionOptions{
			Namespace:     "second",
			ExposePrompts: true,
		},
		Config: initial,
		Stdin:  strings.NewReader(""), Stdout: secondOutput,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	initialize := json.RawMessage(`{"protocolVersion":"2024-11-05","clientInfo":{"name":"test","version":"1"}}`)
	if _, rpcErr := first.handleInitialize(context.Background(), initialize); rpcErr != nil {
		t.Fatal(rpcErr)
	}
	if _, rpcErr := second.handleInitialize(context.Background(), initialize); rpcErr != nil {
		t.Fatal(rpcErr)
	}

	reloaded := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"a": {Command: "echo"}, "b": {Command: "echo"},
		},
		Namespaces: map[string]config.NamespaceConfig{
			"first":  {ServerIDs: []string{"b"}},
			"second": {ServerIDs: []string{"a"}},
		},
	}
	core.applyReload(context.Background(), reloaded, nil)

	if got := first.activeServerNames; len(got) != 1 || got[0] != "b" {
		t.Fatalf("first session servers = %v, want [b]", got)
	}
	if got := second.activeServerNames; len(got) != 1 || got[0] != "a" {
		t.Fatalf("second session servers = %v, want [a]", got)
	}
	if output := firstOutput.String(); !strings.Contains(output, "notifications/tools/list_changed") || !strings.Contains(output, "notifications/resources/list_changed") || strings.Contains(output, "notifications/prompts/list_changed") {
		t.Fatalf("first session reload notifications = %s", output)
	}
	if output := secondOutput.String(); !strings.Contains(output, "notifications/tools/list_changed") || strings.Contains(output, "notifications/resources/list_changed") || !strings.Contains(output, "notifications/prompts/list_changed") {
		t.Fatalf("second session reload notifications = %s", output)
	}
}

type recordingNotificationSink struct {
	notifications chan process.UpstreamNotification
}

func (s *recordingNotificationSink) OnUpstreamNotification(notification process.UpstreamNotification) {
	s.notifications <- notification.Clone()
}

var _ NotificationSink = (*recordingNotificationSink)(nil)

func TestNotificationBroadcasterForwardsToMultipleSubscribers(t *testing.T) {
	broadcaster := newNotificationBroadcaster(nil)
	t.Cleanup(broadcaster.Close)
	sink := &recordingNotificationSink{notifications: make(chan process.UpstreamNotification, 1)}
	unsubscribe, err := broadcaster.Subscribe(sink)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	second := &recordingNotificationSink{notifications: make(chan process.UpstreamNotification, 1)}
	if _, err := broadcaster.Subscribe(second); err != nil {
		t.Fatalf("Subscribe second: %v", err)
	}

	params := json.RawMessage(`{"uri":"file:///example"}`)
	broadcaster.OnUpstreamNotification(process.UpstreamNotification{
		Instance: process.SharedInstanceID("files"), Method: "notifications/resources/updated", Params: params,
	})
	for index, receiver := range []<-chan process.UpstreamNotification{sink.notifications, second.notifications} {
		select {
		case got := <-receiver:
			if got.Instance.Server != "files" || got.Method != "notifications/resources/updated" || string(got.Params) != string(params) {
				t.Fatalf("subscriber %d got %+v", index, got)
			}
		case <-time.After(time.Second):
			t.Fatalf("subscriber %d did not receive notification", index)
		}
	}

	unsubscribe()
	if _, err := broadcaster.Subscribe(&recordingNotificationSink{notifications: make(chan process.UpstreamNotification, 1)}); err != nil {
		t.Fatalf("Subscribe after unsubscribe: %v", err)
	}
}
