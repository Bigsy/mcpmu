package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/process"
)

func TestRequestKey_DistinguishesIDShapes(t *testing.T) {
	t.Parallel()
	// JSON-RPC ids may be numbers or strings, and 1 is not "1".
	if requestKey(json.RawMessage(`1`)) == requestKey(json.RawMessage(`"1"`)) {
		t.Error("numeric and string ids collide in the in-flight table")
	}
	// Whitespace is not identity.
	if requestKey(json.RawMessage(` 7 `)) != requestKey(json.RawMessage(`7`)) {
		t.Error("id 7 did not canonicalise")
	}
	// An absent or null id can never be named by a cancellation.
	if requestKey(nil) != "" || requestKey(json.RawMessage(`null`)) != "" {
		t.Error("null id produced a usable key")
	}
}

func TestInflightCalls_CancelByID(t *testing.T) {
	t.Parallel()
	calls := newInflightCalls()

	first, releaseFirst := calls.track(context.Background(), json.RawMessage(`1`))
	defer releaseFirst()
	second, releaseSecond := calls.track(context.Background(), json.RawMessage(`2`))
	defer releaseSecond()

	if !calls.cancel(json.RawMessage(`1`), &cancelledError{reason: "user cancelled"}) {
		t.Fatal("cancel reported no such request")
	}
	if first.Err() == nil {
		t.Error("cancelled request context is still live")
	}
	if cause := context.Cause(first); cause == nil || !strings.Contains(cause.Error(), "user cancelled") {
		t.Errorf("cancellation cause = %v, want the client's reason", context.Cause(first))
	}
	if second.Err() != nil {
		t.Error("cancelling one request took down another")
	}
	if calls.cancel(json.RawMessage(`1`), nil) {
		t.Error("a second cancellation for the same id reported success")
	}
}

// Retired mappings must not accumulate: the table is swept whenever a new
// token is minted.
func TestProgressRoutes_SweepsExpiredEntries(t *testing.T) {
	t.Parallel()
	routes := newProgressRoutes()
	clock := time.Now()
	routes.now = func() time.Time { return clock }

	for range 5 {
		_, release := routes.mint("session-1", json.RawMessage(`1`))
		release()
	}
	clock = clock.Add(progressRouteGrace + time.Second)

	_, release := routes.mint("session-1", json.RawMessage(`2`))
	defer release()

	routes.mu.Lock()
	remaining := len(routes.tokens)
	routes.mu.Unlock()
	if remaining != 1 {
		t.Errorf("progress table holds %d entries after a sweep, want 1", remaining)
	}
}

func TestInflightCalls_ReleaseDropsEntry(t *testing.T) {
	t.Parallel()
	calls := newInflightCalls()
	_, release := calls.track(context.Background(), json.RawMessage(`1`))
	if calls.len() != 1 {
		t.Fatalf("in-flight table holds %d entries, want 1", calls.len())
	}
	release()
	if calls.len() != 0 {
		t.Errorf("release left %d entries behind", calls.len())
	}
}

// Closing a session must cancel that session's calls and only that session's
// calls — the cross-agent bleed a shared upstream instance invites.
func TestSessionClose_CancelsOnlyItsOwnCalls(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{SchemaVersion: 1, Servers: map[string]config.ServerConfig{}}
	core, err := NewCore(Options{Config: cfg, PIDTrackerDir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewCore: %v", err)
	}
	t.Cleanup(core.Close)

	newSession := func() *Session {
		t.Helper()
		session, err := NewSession(core, Options{
			Config: cfg, Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{},
		})
		if err != nil {
			t.Fatalf("NewSession: %v", err)
		}
		return session
	}

	leaving := newSession()
	staying := newSession()
	t.Cleanup(staying.Close)

	leavingCall, releaseLeaving := leaving.inflight.track(context.Background(), json.RawMessage(`1`))
	defer releaseLeaving()
	stayingCall, releaseStaying := staying.inflight.track(context.Background(), json.RawMessage(`1`))
	defer releaseStaying()

	leaving.Close()

	if leavingCall.Err() == nil {
		t.Error("closing a session left its own call running")
	}
	if stayingCall.Err() != nil {
		t.Error("closing one session cancelled another session's call on the same shared instance")
	}
}

// Two sessions may pick the same progressToken; each must see only its own
// progress, carrying the token it chose.
func TestProgress_SessionsWithIdenticalTokensStayIsolated(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{SchemaVersion: 1, Servers: map[string]config.ServerConfig{}}
	core, err := NewCore(Options{Config: cfg, PIDTrackerDir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewCore: %v", err)
	}
	t.Cleanup(core.Close)

	type client struct {
		session *Session
		output  *lockedBuffer
	}
	newClient := func() client {
		t.Helper()
		output := &lockedBuffer{}
		session, err := NewSession(core, Options{
			Config: cfg, Stdin: strings.NewReader(""), Stdout: output,
		})
		if err != nil {
			t.Fatalf("NewSession: %v", err)
		}
		t.Cleanup(session.Close)
		return client{session: session, output: output}
	}

	first := newClient()
	second := newClient()

	// Both clients choose the same token — nothing in the protocol stops them.
	firstMeta, releaseFirst := first.session.rewriteRequestMeta(json.RawMessage(`{"progressToken":1,"vendor/trace":"abc"}`))
	defer releaseFirst()
	secondMeta, releaseSecond := second.session.rewriteRequestMeta(json.RawMessage(`{"progressToken":1}`))
	defer releaseSecond()

	firstToken := progressTokenOf(t, firstMeta)
	secondToken := progressTokenOf(t, secondMeta)
	if firstToken == secondToken {
		t.Fatalf("both sessions were given the same upstream token %q", firstToken)
	}
	if !strings.Contains(string(firstMeta), "vendor/trace") {
		t.Errorf("rewriting progressToken dropped the rest of _meta: %s", firstMeta)
	}

	// The upstream reports progress against the first session's token; the
	// broadcaster hands it to every session, and only one must act on it.
	notification := process.UpstreamNotification{
		Instance: process.SharedInstanceID("srv"), Generation: 1,
		Method: "notifications/progress",
		Params: json.RawMessage(`{"progressToken":"` + firstToken + `","progress":3,"total":10,"message":"halfway"}`),
	}
	first.session.OnUpstreamNotification(notification)
	second.session.OnUpstreamNotification(notification)
	first.session.handlersWG.Wait()
	second.session.handlersWG.Wait()

	firstOut := first.output.String()
	if !strings.Contains(firstOut, "notifications/progress") {
		t.Fatalf("the requesting session received no progress: %q", firstOut)
	}
	if !strings.Contains(firstOut, `"progressToken":1`) {
		t.Errorf("progress reached the client with the wrong token: %s", firstOut)
	}
	if !strings.Contains(firstOut, `"message":"halfway"`) {
		t.Errorf("rewriting the token dropped the rest of the notification: %s", firstOut)
	}
	if out := second.output.String(); strings.Contains(out, "notifications/progress") {
		t.Errorf("another session's progress leaked to this client: %s", out)
	}
}

// A call that ends stops matching progress, and a token that was never minted
// never matches at all.
func TestProgress_UnknownTokensAreDropped(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{SchemaVersion: 1, Servers: map[string]config.ServerConfig{}}
	session, err := New(Options{
		Config: cfg, PIDTrackerDir: t.TempDir(),
		Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() {
		session.Close()
		session.Core.Close()
	})

	// Drive the clock so the post-completion grace window can be observed
	// without sleeping through it.
	clock := time.Now()
	session.progress.now = func() time.Time { return clock }

	meta, release := session.rewriteRequestMeta(json.RawMessage(`{"progressToken":"client-token"}`))
	token := progressTokenOf(t, meta)

	if _, ok := session.progressNotificationForSession(
		json.RawMessage(`{"progressToken":"` + token + `","progress":1}`)); !ok {
		t.Fatal("a live token did not match")
	}
	release()
	// Still inside the grace window: a progress frame already in flight when
	// the result came back must not be dropped.
	if _, ok := session.progressNotificationForSession(
		json.RawMessage(`{"progressToken":"` + token + `","progress":2}`)); !ok {
		t.Error("progress racing the result was dropped")
	}
	clock = clock.Add(progressRouteGrace + time.Second)
	if _, ok := session.progressNotificationForSession(
		json.RawMessage(`{"progressToken":"` + token + `","progress":3}`)); ok {
		t.Error("a long-finished call still matched progress notifications")
	}
	for _, params := range []string{
		`{"progressToken":"never-minted","progress":1}`,
		`{"progressToken":7,"progress":1}`,
		`{"progress":1}`,
		`not json`,
	} {
		if _, ok := session.progressNotificationForSession(json.RawMessage(params)); ok {
			t.Errorf("params %s matched a session that never requested progress", params)
		}
	}
}

// A request without a progressToken must reach the upstream unchanged: mcpmu
// has no business inventing progress the client never asked for.
func TestProgress_MetaWithoutTokenPassesThrough(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{SchemaVersion: 1, Servers: map[string]config.ServerConfig{}}
	session, err := New(Options{
		Config: cfg, PIDTrackerDir: t.TempDir(),
		Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() {
		session.Close()
		session.Core.Close()
	})

	for _, meta := range []string{
		`{"vendor/trace":"abc"}`,
		`{"progressToken":null}`,
		`{"malformed`,
	} {
		got, release := session.rewriteRequestMeta(json.RawMessage(meta))
		release()
		if string(got) != meta {
			t.Errorf("_meta %s was rewritten to %s", meta, got)
		}
	}

	got, release := session.rewriteRequestMeta(nil)
	release()
	if got != nil {
		t.Errorf("absent _meta became %s", got)
	}
}

// End-to-end: a client that asks for progress gets it, with its own token,
// from a real upstream process.
func TestServer_ProgressReachesClientEndToEnd(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("Skipping end-to-end test in short mode")
	}

	enabled := true
	cfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"srv": {
				Kind: config.ServerKindStdio, Enabled: &enabled,
				Command: os.Args[0],
				Args:    []string{"-test.run=TestHelperProcess", "--"},
				Env: map[string]string{
					"GO_WANT_HELPER_PROCESS": "1",
					"FAKE_MCP_CFG":           `{"tools":[{"name":"slow"}],"echoToolCalls":true,"progressUpdatesPerCall":2}`,
				},
			},
		},
	}

	var stdout bytes.Buffer
	stdin := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","clientInfo":{"name":"test","version":"1.0"}}}` + "\n" +
			`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"srv.slow","arguments":{},"_meta":{"progressToken":"client-chosen"}}}` + "\n",
	)

	srv, err := New(Options{
		Config: cfg, PIDTrackerDir: t.TempDir(),
		Stdin: stdin, Stdout: &stdout,
		ServerName: "mcpmu-test", ServerVersion: "1.0.0", LogLevel: "error",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_ = srv.Run(ctx)

	output := stdout.String()
	if !strings.Contains(output, "notifications/progress") {
		t.Fatalf("no progress reached the client:\n%s", output)
	}
	if !strings.Contains(output, `"progressToken":"client-chosen"`) {
		t.Errorf("progress did not carry the client's own token:\n%s", output)
	}
	if strings.Contains(output, `"progressToken":"mcpmu/`) {
		t.Errorf("mcpmu's internal token leaked to the client:\n%s", output)
	}
}

// A client cancelling a tool call must not be left waiting for the upstream to
// finish work it no longer wants.
func TestServer_CancellationEndsToolCall(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("Skipping end-to-end test in short mode")
	}

	requestLog := t.TempDir() + "/requests.log"
	enabled := true
	cfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"srv": {
				Kind: config.ServerKindStdio, Enabled: &enabled,
				Command: os.Args[0],
				Args:    []string{"-test.run=TestHelperProcess", "--"},
				Env: map[string]string{
					"GO_WANT_HELPER_PROCESS": "1",
					"FAKE_MCP_CFG": `{"tools":[{"name":"slow"}],"echoToolCalls":true,` +
						`"delays":{"tools/call":1500000000},"requestLogPath":"` + requestLog + `"}`,
				},
			},
		},
	}

	// A pipe rather than a string reader: the cancellation has to be written
	// after the tools/call is already in flight.
	clientIn, clientOut := io.Pipe()
	output := &lockedBuffer{}

	srv, err := New(Options{
		Config: cfg, PIDTrackerDir: t.TempDir(),
		Stdin: clientIn, Stdout: output,
		ServerName: "mcpmu-test", ServerVersion: "1.0.0", LogLevel: "error",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		_ = srv.Run(ctx)
	}()

	write := func(line string) {
		t.Helper()
		if _, err := clientOut.Write([]byte(line + "\n")); err != nil {
			t.Errorf("write to server: %v", err)
		}
	}
	write(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","clientInfo":{"name":"test","version":"1.0"}}}`)
	write(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"srv.slow","arguments":{}}}`)

	// Let the call reach the upstream, then withdraw it.
	waitFor(t, 10*time.Second, func() bool {
		return strings.Contains(readFileString(t, requestLog), "tools/call")
	}, "upstream never received the tools/call")
	write(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":2,"reason":"user pressed escape"}}`)

	waitFor(t, 10*time.Second, func() bool {
		return strings.Contains(output.String(), `"id":2`)
	}, "cancelled tools/call never produced a response")

	if !strings.Contains(output.String(), `"error"`) {
		t.Errorf("a cancelled call returned a success result: %s", output.String())
	}
	waitFor(t, 10*time.Second, func() bool {
		return strings.Contains(readFileString(t, requestLog), "notifications/cancelled")
	}, "the upstream server was never told the call was cancelled")

	_ = clientOut.Close()
	select {
	case <-runDone:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after the client disconnected")
	}
}

// lockedBuffer is a bytes.Buffer safe for a test goroutine to read while the
// server writes.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func progressTokenOf(t *testing.T, meta json.RawMessage) string {
	t.Helper()
	var parsed struct {
		ProgressToken string `json:"progressToken"`
	}
	if err := json.Unmarshal(meta, &parsed); err != nil {
		t.Fatalf("unmarshal rewritten _meta %s: %v", meta, err)
	}
	if parsed.ProgressToken == "" {
		t.Fatalf("_meta %s carries no upstream progress token", meta)
	}
	return parsed.ProgressToken
}

func waitFor(t *testing.T, limit time.Duration, condition func() bool, message string) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(message)
}

func readFileString(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}
