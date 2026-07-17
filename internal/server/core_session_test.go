package server

import (
	"bytes"
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
