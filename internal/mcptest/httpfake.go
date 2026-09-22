package mcptest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Bigsy/mcpmu/internal/mcptest/fakeserver"
)

// HTTPFakeConfig configures an HTTP (Streamable HTTP) fake MCP upstream whose
// tools make server-to-client requests mid-call.
type HTTPFakeConfig struct {
	// Tools lists tool names. Each tool answers with a ServerRequestOutcome
	// as JSON text, or plain text "ok" if it makes no request.
	Tools []string
	// OnPOSTStream maps tools that send their request on the tools/call
	// POST's own response stream (upgraded to SSE) to that request.
	OnPOSTStream map[string]fakeserver.ServerRequestScript
	// OnGETStream maps tools that send their request on the standalone GET
	// stream, answering the POST with plain JSON once the reply arrives.
	OnGETStream map[string]fakeserver.ServerRequestScript
	// Hold keeps a tool's call open this long before answering (plain JSON),
	// so tests can have calls in flight concurrently.
	Hold map[string]time.Duration
}

// HTTPFake is a running HTTP fake upstream.
type HTTPFake struct {
	URL string

	cfg     HTTPFakeConfig
	next    atomic.Int64
	mu      sync.Mutex
	pending map[string]chan json.RawMessage // server request id → reply
	stream  chan []byte                     // frames for the GET stream
	// Capabilities records the capabilities object of the last initialize.
	capabilities atomic.Value
}

// StartHTTPFake serves cfg on a loopback httptest server for the test's life.
func StartHTTPFake(t *testing.T, cfg HTTPFakeConfig) *HTTPFake {
	t.Helper()
	fake := &HTTPFake{cfg: cfg, pending: make(map[string]chan json.RawMessage), stream: make(chan []byte, 16)}
	srv := httptest.NewServer(http.HandlerFunc(fake.serve))
	t.Cleanup(srv.Close)
	fake.URL = srv.URL
	return fake
}

// DeclaredCapabilities returns the capabilities from the last initialize.
func (f *HTTPFake) DeclaredCapabilities() json.RawMessage {
	caps, _ := f.capabilities.Load().(json.RawMessage)
	return caps
}

func (f *HTTPFake) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" && r.URL.Path != "" {
		// OAuth discovery probes /.well-known/...; this server has none.
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		f.serveStream(w, r)
		return
	case http.MethodDelete:
		w.WriteHeader(http.StatusOK)
		return
	case http.MethodPost:
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	body, _ := io.ReadAll(r.Body)
	var msg struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(body, &msg); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	w.Header().Set("Mcp-Session-Id", "fake-session")
	switch {
	case msg.Method == "" && msg.ID != nil:
		// A reply to one of our requests.
		answer := msg.Result
		if len(msg.Error) > 0 {
			answer = json.RawMessage(`{"error":` + string(msg.Error) + `}`)
		}
		f.mu.Lock()
		ch := f.pending[string(msg.ID)]
		delete(f.pending, string(msg.ID))
		f.mu.Unlock()
		if ch != nil {
			ch <- answer
		}
		w.WriteHeader(http.StatusAccepted)
	case msg.ID == nil:
		w.WriteHeader(http.StatusAccepted)
	case msg.Method == "initialize":
		var p struct {
			ProtocolVersion string          `json:"protocolVersion"`
			Capabilities    json.RawMessage `json:"capabilities"`
		}
		_ = json.Unmarshal(msg.Params, &p)
		f.capabilities.Store(p.Capabilities)
		f.writeJSON(w, msg.ID, map[string]any{
			"protocolVersion": p.ProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "http-fake", "version": "1"},
		})
	case msg.Method == "tools/list":
		tools := make([]map[string]any, 0, len(f.cfg.Tools))
		for _, name := range f.cfg.Tools {
			tools = append(tools, map[string]any{"name": name, "inputSchema": map[string]any{"type": "object"}})
		}
		f.writeJSON(w, msg.ID, map[string]any{"tools": tools})
	case msg.Method == "tools/call":
		var p struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(msg.Params, &p)
		f.callTool(w, r, msg.ID, p.Name)
	default:
		f.writeFrame(w, map[string]any{"jsonrpc": "2.0", "id": msg.ID,
			"error": map[string]any{"code": -32601, "message": "Method not found"}})
	}
}

func (f *HTTPFake) callTool(w http.ResponseWriter, r *http.Request, id json.RawMessage, name string) {
	if hold := f.cfg.Hold[name]; hold > 0 {
		select {
		case <-time.After(hold):
		case <-r.Context().Done():
			return
		}
		f.writeJSON(w, id, textToolResult("ok"))
		return
	}
	if script, ok := f.cfg.OnPOSTStream[name]; ok {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		reqID, reply := f.register()
		f.writeEvent(w, map[string]any{"jsonrpc": "2.0", "id": reqID, "method": script.Method, "params": script.Params})
		outcome := f.await(r.Context(), reqID, reply)
		f.writeEvent(w, map[string]any{"jsonrpc": "2.0", "id": id, "result": textToolResult(outcome)})
		return
	}
	if script, ok := f.cfg.OnGETStream[name]; ok {
		reqID, reply := f.register()
		frame, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": reqID, "method": script.Method, "params": script.Params})
		f.stream <- frame
		outcome := f.await(r.Context(), reqID, reply)
		f.writeJSON(w, id, textToolResult(outcome))
		return
	}
	f.writeJSON(w, id, textToolResult("ok"))
}

func (f *HTTPFake) register() (string, chan json.RawMessage) {
	reqID := fmt.Sprintf("srv-%d", f.next.Add(1))
	ch := make(chan json.RawMessage, 1)
	f.mu.Lock()
	f.pending[`"`+reqID+`"`] = ch
	f.mu.Unlock()
	return reqID, ch
}

// await waits for the reply and renders a ServerRequestOutcome as JSON.
func (f *HTTPFake) await(ctx context.Context, reqID string, reply chan json.RawMessage) string {
	outcome := fakeserver.ServerRequestOutcome{}
	select {
	case answer := <-reply:
		outcome.Answered = true
		outcome.Result = answer
	case <-time.After(10 * time.Second):
	case <-ctx.Done():
	}
	encoded, _ := json.Marshal(outcome)
	return string(encoded)
}

func (f *HTTPFake) serveStream(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	w.(http.Flusher).Flush()
	for {
		select {
		case frame := <-f.stream:
			_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", frame)
			w.(http.Flusher).Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func textToolResult(text string) map[string]any {
	return map[string]any{"content": []map[string]any{{"type": "text", "text": text}}}
}

func (f *HTTPFake) writeJSON(w http.ResponseWriter, id json.RawMessage, result any) {
	f.writeFrame(w, map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func (f *HTTPFake) writeFrame(w http.ResponseWriter, frame any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(frame)
}

func (f *HTTPFake) writeEvent(w http.ResponseWriter, frame any) {
	data, _ := json.Marshal(frame)
	_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", data)
	w.(http.Flusher).Flush()
}
