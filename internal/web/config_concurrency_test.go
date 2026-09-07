package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Bigsy/mcpmu/internal/config"
)

func TestConfigPublicationConcurrentReads(t *testing.T) {
	s := newTestServer(t)
	start := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		<-start
		for range 30 {
			for _, route := range []string{"/api/servers", "/api/namespaces", "/api/metrics", "/api/config/export", "/servers", "/servers/test-stdio", "/namespaces/default", "/metrics", "/fragments/servers/table"} {
				rec := httptest.NewRecorder()
				s.httpServer.Handler.ServeHTTP(rec, newRequest("GET", route, nil))
				if rec.Code != 200 {
					t.Errorf("%s: status %d: %s", route, rec.Code, rec.Body.String())
					continue
				}
				if strings.HasPrefix(route, "/api/") {
					if !json.Valid(rec.Body.Bytes()) {
						t.Errorf("%s: invalid JSON", route)
					}
				} else if rec.Body.Len() == 0 {
					t.Errorf("%s: empty HTML", route)
				}
			}
		}
	}()
	close(start)
	for i := range 30 {
		if err := s.mutateConfig(func(c *config.Config) error {
			srv, _ := c.GetServer("test-stdio")
			srv.Args = []string{fmt.Sprint(i)}
			return c.UpdateServer("test-stdio", srv)
		}); err != nil {
			t.Error(err)
		}
	}
	<-done
}

func TestWatcherPublicationConcurrentReads(t *testing.T) {
	s := newTestServer(t)
	original := s.configSnapshot()
	originalJSON, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	watcherDone := make(chan struct{})
	go func() { defer close(watcherDone); s.WatchConfig(ctx) }()
	defer func() { cancel(); <-watcherDone }()
	changed, unsubscribe := s.configBcast.Subscribe()
	defer unsubscribe()

	reading := make(chan struct{})
	stopReads := make(chan struct{})
	readsDone := make(chan struct{})
	go func() {
		defer close(readsDone)
		close(reading)
		for {
			select {
			case <-stopReads:
				return
			default:
			}
			for _, route := range []string{"/api/config/export", "/api/metrics", "/metrics", "/namespaces/default", "/servers/test-stdio/edit"} {
				rec := httptest.NewRecorder()
				s.httpServer.Handler.ServeHTTP(rec, newRequest("GET", route, nil))
				if rec.Code != 200 {
					t.Errorf("%s: status %d", route, rec.Code)
					continue
				}
				if strings.HasPrefix(route, "/api/") && !json.Valid(rec.Body.Bytes()) {
					t.Errorf("%s: invalid JSON", route)
				}
			}
		}
	}()
	defer func() { close(stopReads); <-readsDone }()
	<-reading

	// Watch has no readiness hook. Retry an external atomic save until the first
	// broadcast, so setup is coordinated by observed publication rather than sleeps.
	retry := time.NewTicker(300 * time.Millisecond)
	defer retry.Stop()
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()
	for i := range 3 {
		fresh, err := config.LoadFrom(s.configPath)
		if err != nil {
			t.Fatal(err)
		}
		srv, _ := fresh.GetServer("test-stdio")
		srv.Args = []string{fmt.Sprintf("external-%d", i)}
		if err := fresh.UpdateServer("test-stdio", srv); err != nil {
			t.Fatal(err)
		}
		if err := config.SaveTo(fresh, s.configPath); err != nil {
			t.Fatal(err)
		}
	waitPublication:
		for {
			select {
			case <-changed:
				got, _ := s.configSnapshot().GetServer("test-stdio")
				if slices.Equal(got.Args, srv.Args) {
					break waitPublication
				}
			case <-retry.C:
				if err := config.SaveTo(fresh, s.configPath); err != nil {
					t.Fatal(err)
				}
			case <-timeout.C:
				t.Fatal("watcher did not publish external edits")
			}
		}
	}
	after, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(originalJSON, after) {
		t.Error("publication modified an older snapshot")
	}
}
