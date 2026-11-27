package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/mcptest"
)

func TestStdioSessionConcurrency(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	var mu sync.Mutex
	var executed []string
	started := make(chan string, 4)
	release := map[string]chan struct{}{"a": make(chan struct{}), "b": make(chan struct{})}
	up := mcptest.StartHTTPFake(t, mcptest.HTTPFakeConfig{
		Tools: []string{"a", "b", "c", "d", "e"},
		OnToolCall: func(callCtx context.Context, name string) {
			mu.Lock()
			executed = append(executed, name)
			mu.Unlock()
			if gate := release[name]; gate != nil {
				started <- name
				select {
				case <-gate:
				case <-ctx.Done():
				case <-callCtx.Done():
				}
			}
		},
	})
	core, err := NewCore(Options{Config: &config.Config{
		MaxConcurrentRequestsPerSession: 2,
		Servers:                         map[string]config.ServerConfig{"probe": {URL: up.URL}},
	}, PIDTrackerDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(core.Close)
	left := connectStdioPeer(t, core)
	right := connectStdioPeer(t, core)

	for _, name := range []string{"a", "b"} {
		left.call(t, name, name)
		select {
		case got := <-started:
			if got != name {
				t.Fatalf("upstream started %q, want %q", got, name)
			}
		case <-ctx.Done():
			t.Fatal("upstream did not start", name)
		}
	}
	left.call(t, "c", "c")
	denied := left.response(t, "c")
	if denied.Error == nil || denied.Error.Code != -32007 {
		t.Fatalf("request at capacity: %+v", denied)
	}
	var detail struct {
		Reason    string
		Execution string
		RetrySafe bool
		Limit     int
	}
	if err := json.Unmarshal(denied.Error.Data, &detail); err != nil ||
		detail.Reason != "session_concurrency_limit" || detail.Execution != "not_dispatched" || !detail.RetrySafe || detail.Limit != 2 {
		t.Fatalf("overload details: %s (%v)", denied.Error.Data, err)
	}

	// Read just the first byte so the synchronous pipe writer is blocked on
	// the rest of B. Another session can reuse B's ID on the same upstream.
	close(release["b"])
	first := make([]byte, 1)
	if _, err := left.conn.Read(first); err != nil || first[0] != '{' {
		t.Fatalf("start of response B: %q, %v", first, err)
	}
	right.call(t, "b", "d")
	right.requireOK(t, "b")
	rest, err := left.reader.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var b RPCResponse
	if err := json.Unmarshal(append(first, rest...), &b); err != nil || string(b.ID) != `"b"` || !bytes.Contains(b.Result, []byte(`"ok"`)) {
		t.Fatalf("response B: %+v, %v", b, err)
	}
	close(release["a"])
	left.requireOK(t, "a")
	left.call(t, "d", "d")
	left.requireOK(t, "d")

	// E has already run when its first response byte reaches the client.
	// Disconnect mid-frame and verify neither the side effect nor C is replayed.
	left.call(t, "e", "e")
	if _, err := left.conn.Read(first); err != nil {
		t.Fatal(err)
	}
	if err := left.conn.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-left.done:
	case <-ctx.Done():
		t.Fatal("session did not stop after a partial terminal write")
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(executed, []string{"a", "b", "d", "d", "e"}) {
		t.Fatalf("upstream calls = %v", executed)
	}
}

func TestStdioShutdownInterruptsBlockedWrite(t *testing.T) {
	core, err := NewCore(Options{Config: &config.Config{}, PIDTrackerDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(core.Close)
	p := connectStdioPeer(t, core)
	if _, err := fmt.Fprintln(p.conn, `{"jsonrpc":"2.0","method":"ping","id":7}`); err != nil {
		t.Fatal(err)
	}
	// net.Pipe cannot finish the frame until we read the remaining bytes.
	if _, err := p.conn.Read(make([]byte, 1)); err != nil {
		t.Fatal(err)
	}
	p.cancel()
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		t.Fatal("session waited for the client to drain during shutdown")
	}
}

type stdioPeer struct {
	cancel context.CancelFunc
	conn   net.Conn
	reader *bufio.Reader
	done   chan struct{}
}

func connectStdioPeer(t *testing.T, core *Core) *stdioPeer {
	t.Helper()
	serverConn, client := net.Pipe()
	if err := client.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	session, err := NewSession(core, Options{Stdin: serverConn, Stdout: serverConn})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	p := &stdioPeer{cancel: cancel, conn: client, reader: bufio.NewReader(client), done: make(chan struct{})}
	go func() {
		defer close(p.done)
		defer func() { _ = serverConn.Close() }()
		_ = session.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		_ = client.Close()
		select {
		case <-p.done:
		case <-time.After(10 * time.Second):
			t.Error("stdio session did not exit")
		}
	})
	if _, err := fmt.Fprintln(client, `{"jsonrpc":"2.0","id":"init","method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`); err != nil {
		t.Fatal(err)
	}
	if resp := p.response(t, "init"); resp.Error != nil {
		t.Fatal(resp.Error)
	}
	if _, err := fmt.Fprintln(client, `{"jsonrpc":"2.0","method":"notifications/initialized"}`); err != nil {
		t.Fatal(err)
	}
	return p
}

func (p *stdioPeer) call(t *testing.T, id, tool string) {
	t.Helper()
	if _, err := fmt.Fprintf(p.conn, `{"jsonrpc":"2.0","id":%q,"method":"tools/call","params":{"name":%q,"arguments":{}}}`+"\n", id, "probe."+tool); err != nil {
		t.Fatal(err)
	}
}

func (p *stdioPeer) response(t *testing.T, id string) RPCResponse {
	t.Helper()
	for {
		line, err := p.reader.ReadBytes('\n')
		if err != nil {
			t.Fatal(err)
		}
		var msg RPCResponse
		if err := json.Unmarshal(line, &msg); err != nil {
			t.Fatal(err)
		}
		if msg.ID == nil {
			continue
		}
		if string(msg.ID) != fmt.Sprintf("%q", id) {
			t.Fatalf("unexpected response: %s", line)
		}
		return msg
	}
}

func (p *stdioPeer) requireOK(t *testing.T, id string) {
	t.Helper()
	msg := p.response(t, id)
	if msg.Error != nil || !bytes.Contains(msg.Result, []byte(`"ok"`)) {
		t.Fatalf("response %s: %+v", id, msg)
	}
}
