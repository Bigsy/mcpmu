package mcp

import (
	"context"
	"encoding/json"
	"strconv"
	"sync"
	"testing"
	"time"
)

// TestNegotiatedStateRaceDuringInitialize guards the negotiated-state fields
// (serverName, serverVersion, protocolVersion, capabilities) against concurrent
// access while Initialize is still in flight.
//
// The window is real in production, not just in tests: Supervisor.Start
// publishes the Handle into s.handles and only then runs the handshake on a
// separate goroutine (see supervisor.go, "go s.initAndDiscoverAsync"), so any
// caller holding the handle can reach Capabilities()/ServerInfo() while
// Initialize is writing them. Live readers include Core.processNotification,
// Aggregator.shouldQueryCapability and Router.handleServersList.
//
// Before the fix, Initialize wrote the four fields bare and the accessors read
// them bare; this test fails under -race with "WARNING: DATA RACE" between
// Initialize and Capabilities.
func TestNegotiatedStateRaceDuringInitialize(t *testing.T) {
	transport := newSyntheticTransport()
	client := NewClient(transport)
	defer func() { _ = client.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Readers spin on the accessors for the whole handshake, mirroring the
	// callers that can hold a published-but-uninitialized Handle.
	stop := make(chan struct{})
	var readers sync.WaitGroup
	for range 4 {
		readers.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
					_ = client.Capabilities()
					_, _ = client.ServerInfo()
					_ = client.ProtocolVersion()
				}
			}
		})
	}

	// Respond to initialize only after the readers have had time to get going,
	// so the write lands in the middle of concurrent reads rather than before
	// them.
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		frame := transport.nextSent(t, 2*time.Second)
		var req struct {
			ID     int64 `json:"id"`
			Params struct {
				ProtocolVersion string `json:"protocolVersion"`
			} `json:"params"`
		}
		if err := json.Unmarshal(frame, &req); err != nil {
			t.Errorf("unmarshal initialize request: %v", err)
			return
		}
		time.Sleep(50 * time.Millisecond)
		transport.inject([]byte(`{"jsonrpc":"2.0","id":` + strconv.FormatInt(req.ID, 10) + `,"result":{` +
			`"protocolVersion":"` + req.Params.ProtocolVersion + `",` +
			`"capabilities":{"tools":{"listChanged":true},"resources":{"subscribe":true}},` +
			`"serverInfo":{"name":"probe-server","version":"1.2.3"}}}`))
	}()

	if err := client.Initialize(ctx); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	close(stop)
	readers.Wait()
	<-serverDone

	// The negotiated values must be visible to readers after Initialize returns.
	name, version := client.ServerInfo()
	if name != "probe-server" || version != "1.2.3" {
		t.Errorf("ServerInfo() = (%q, %q), want (%q, %q)", name, version, "probe-server", "1.2.3")
	}
	// The client records the version it successfully offered, which is the first
	// preference since this fake accepts it.
	if got, want := client.ProtocolVersion(), SupportedProtocolVersions[0]; got != want {
		t.Errorf("ProtocolVersion() = %q, want %q", got, want)
	}
	caps := client.Capabilities()
	if caps.Resources == nil || !caps.Resources.Subscribe {
		t.Errorf("Capabilities().Resources.Subscribe = %v, want true", caps.Resources)
	}
	if caps.Tools == nil || !caps.Tools.ListChanged {
		t.Errorf("Capabilities().Tools.ListChanged = %v, want true", caps.Tools)
	}
}
