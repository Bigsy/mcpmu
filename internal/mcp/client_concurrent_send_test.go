package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// TestConcurrentSendsNotHeadOfLineBlocked pins the removal of the client-side
// send lock: one caller's in-flight HTTP POST round trip must not serialize
// another caller's send. Holding a mutex across transport.Send queued every
// RPC behind a single long tools/call — up to a whole tool timeout — even
// though Streamable HTTP POSTs are independent requests. Frame integrity on
// stdio is the stdio transport's own mutex, so nothing here needs a lock.
func TestConcurrentSendsNotHeadOfLineBlocked(t *testing.T) {
	slowArrived := make(chan struct{})
	var slowRelease atomic.Value // chan struct{}
	release := make(chan struct{})
	slowRelease.Store(release)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			ID     *json.RawMessage `json:"id"`
			Method string           `json:"method"`
			Params struct {
				Name string `json:"name"`
			} `json:"params"`
		}
		_ = json.Unmarshal(body, &req)

		switch req.Method {
		case "initialize":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2025-06-18",`+
				`"capabilities":{},"serverInfo":{"name":"t","version":"1"}}}`, *req.ID)
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/call":
			if req.Params.Name == "slow" {
				close(slowArrived)
				<-slowRelease.Load().(chan struct{}) // hold the POST open
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"content":[]}}`, *req.ID)
		default:
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{}}`, *req.ID)
		}
	}))
	defer upstream.Close()

	client := NewClient(NewStreamableHTTPTransport(StreamableHTTPConfig{URL: upstream.URL + "/mcp"}))
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	// Caller A starts a long tool call and stays in flight throughout.
	callErr := make(chan error, 1)
	go func() {
		_, err := client.CallTool(context.Background(), "slow", json.RawMessage(`{}`))
		callErr <- err
	}()
	select {
	case <-slowArrived:
	case <-time.After(3 * time.Second):
		t.Fatal("slow tools/call never reached the upstream")
	}

	// Caller B's independent call must go out now, not after A's round trip.
	start := time.Now()
	if _, err := client.CallTool(context.Background(), "quick", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("quick tools/call: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 1200*time.Millisecond {
		t.Fatalf("quick call took %s — it queued behind the slow POST round trip", elapsed)
	}

	close(release)
	select {
	case <-callErr:
	case <-time.After(3 * time.Second):
		t.Fatal("slow call never returned")
	}
}
