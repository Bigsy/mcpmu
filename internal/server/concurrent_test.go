package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"testing"
	"time"

	"github.com/Bigsy/mcpmu/internal/config"
)

// TestServer_ConcurrentToolCalls_SlowUpstreamDoesNotBlockOthers verifies that
// a slow tools/call on one upstream server does not block tools/call on
// another upstream. Regression for the serve-mode hang where the main JSON-RPC
// loop dispatched each request synchronously, so one stuck upstream froze
// every subsequent call (including calls to healthy servers).
func TestServer_ConcurrentToolCalls_SlowUpstreamDoesNotBlockOthers(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("Skipping concurrent test in short mode")
	}

	enabled := true
	slowDelay := 500 * time.Millisecond

	cfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"slow_srv": {
				Kind:    config.ServerKindStdio,
				Enabled: &enabled,
				Command: os.Args[0],
				Args:    []string{"-test.run=TestHelperProcess", "--"},
				Env: map[string]string{
					"GO_WANT_HELPER_PROCESS": "1",
					"FAKE_MCP_CFG": fmt.Sprintf(
						`{"tools":[{"name":"slow_op"}],"echoToolCalls":true,"delays":{"tools/call":%d}}`,
						slowDelay.Nanoseconds(),
					),
				},
			},
			"fast_srv": {
				Kind:    config.ServerKindStdio,
				Enabled: &enabled,
				Command: os.Args[0],
				Args:    []string{"-test.run=TestHelperProcess", "--"},
				Env: map[string]string{
					"GO_WANT_HELPER_PROCESS": "1",
					"FAKE_MCP_CFG":           `{"tools":[{"name":"fast_op"}],"echoToolCalls":true}`,
				},
			},
		},
		Namespaces: map[string]config.NamespaceConfig{
			"default": {ServerIDs: []string{"slow_srv", "fast_srv"}},
		},
	}

	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()

	srv, err := New(Options{
		Config:          cfg,
		PIDTrackerDir:   t.TempDir(),
		Namespace:       "default",
		EagerStart:      true, // pre-start both upstreams so only tools/call timing matters
		Stdin:           stdinR,
		Stdout:          stdoutW,
		ServerName:      "mcpmu-test",
		ServerVersion:   "1.0.0",
		ProtocolVersion: "2024-11-05",
		LogLevel:        "error",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	runDone := make(chan struct{})
	go func() {
		_ = srv.Run(ctx)
		close(runDone)
	}()
	t.Cleanup(func() {
		cancel()
		_ = stdinW.Close()
		_ = stdoutW.Close()
		<-runDone
	})

	respCh := make(chan concurrentResp, 16)
	go func() {
		reader := bufio.NewReader(stdoutR)
		for {
			line, err := reader.ReadBytes('\n')
			if len(line) > 0 {
				var msg struct {
					ID     int             `json:"id"`
					Method string          `json:"method"`
					Params json.RawMessage `json:"params,omitempty"`
				}
				_ = json.Unmarshal(line, &msg)
				// Skip notifications (no id or method set).
				if msg.ID != 0 {
					respCh <- concurrentResp{id: msg.ID, receivedAt: time.Now()}
				}
			}
			if err != nil {
				return
			}
		}
	}()

	write := func(line string) {
		t.Helper()
		if _, err := stdinW.Write([]byte(line + "\n")); err != nil {
			t.Fatalf("write stdin: %v", err)
		}
	}

	received := map[int]concurrentResp{}
	collect := func(ids []int, budget time.Duration) bool {
		t.Helper()
		deadline := time.After(budget)
		for {
			have := true
			for _, id := range ids {
				if _, ok := received[id]; !ok {
					have = false
					break
				}
			}
			if have {
				return true
			}
			select {
			case r := <-respCh:
				if _, dup := received[r.id]; !dup {
					received[r.id] = r
				}
			case <-deadline:
				return false
			}
		}
	}

	// Handshake.
	write(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"test","version":"1.0"}}}`)
	if !collect([]int{1}, 5*time.Second) {
		t.Fatal("initialize response not received")
	}

	// Dispatch slow call FIRST, then immediately the fast call. We never wait
	// for the slow response before issuing the fast one.
	write(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"slow_srv.slow_op","arguments":{}}}`)
	fastSentAt := time.Now()
	write(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"fast_srv.fast_op","arguments":{}}}`)

	totalBudget := slowDelay + 5*time.Second
	if !collect([]int{2, 3}, totalBudget) {
		t.Fatalf("did not receive both tools/call responses within %v (have ids: %v)", totalBudget, keys(received))
	}
	fastResp := received[3]
	slowResp := received[2]

	fastElapsed := fastResp.receivedAt.Sub(fastSentAt)
	slowElapsed := slowResp.receivedAt.Sub(fastSentAt)
	t.Logf("fast arrived after %v; slow arrived after %v", fastElapsed, slowElapsed)

	// Key assertion: fast must come back BEFORE slow. If the serve loop
	// serializes requests, the FIFO'd slow response arrives first and this
	// fails. With concurrent dispatch, fast wins easily.
	if !fastResp.receivedAt.Before(slowResp.receivedAt) {
		t.Errorf("fast tools/call response arrived at/after slow one (fast after %v, slow after %v); requests are being serialized",
			fastElapsed, slowElapsed)
	}

	// And fast should have finished well before the slow delay elapsed —
	// otherwise something is still blocking it on the slow upstream.
	if fastElapsed >= slowDelay {
		t.Errorf("fast tools/call took %v, which meets or exceeds the slow delay %v; it was blocked",
			fastElapsed, slowDelay)
	}
}

type concurrentResp struct {
	id         int
	receivedAt time.Time
}

func keys(m map[int]concurrentResp) []int {
	ks := make([]int, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}
