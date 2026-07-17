package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/mcp"
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
	if _, err := NewSession(core, opts); !errors.Is(err, errNotificationSubscriberExists) {
		t.Fatalf("second NewSession error = %v, want single-subscriber error", err)
	}

	first.Close()
	second, err := NewSession(core, opts)
	if err != nil {
		t.Fatalf("NewSession after close: %v", err)
	}
	second.Close()
}

type recordingNotificationSink struct {
	server string
	method string
	params json.RawMessage
}

func (s *recordingNotificationSink) OnUpstreamNotification(serverName, method string, params json.RawMessage) {
	s.server = serverName
	s.method = method
	s.params = append(s.params[:0], params...)
}

var _ mcp.NotificationSink = (*recordingNotificationSink)(nil)

func TestSingleSubscriberBroadcasterForwardsNotifications(t *testing.T) {
	broadcaster := &singleSubscriberBroadcaster{}
	sink := &recordingNotificationSink{}
	unsubscribe, err := broadcaster.Subscribe(sink)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	params := json.RawMessage(`{"uri":"file:///example"}`)
	broadcaster.OnUpstreamNotification("files", "notifications/resources/updated", params)
	if sink.server != "files" || sink.method != "notifications/resources/updated" {
		t.Fatalf("forwarded notification = (%q, %q), want files/resources-updated", sink.server, sink.method)
	}
	if string(sink.params) != string(params) {
		t.Fatalf("forwarded params = %s, want %s", sink.params, params)
	}

	unsubscribe()
	if _, err := broadcaster.Subscribe(&recordingNotificationSink{}); err != nil {
		t.Fatalf("Subscribe after unsubscribe: %v", err)
	}
}
