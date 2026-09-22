package server

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/mcptest/fakeserver"
)

var askRoots = map[string]fakeserver.ServerRequestScript{"roots": {Method: "roots/list"}}

// rootsServer is a server whose "roots" tool asks mcpmu for its roots.
func rootsServer(t *testing.T, shared bool, roots []string, relay bool, logPath, capsLog string) config.ServerConfig {
	t.Helper()
	srv := elicitServer(t, shared, false, fakeserver.Config{
		Tools:                     []fakeserver.Tool{{Name: "roots"}},
		ToolServerRequests:        askRoots,
		RequestLogPath:            logPath,
		ClientCapabilitiesLogPath: capsLog,
	})
	srv.Roots = roots
	if relay {
		srv.ClientFeatures = &config.ClientFeatures{Roots: true}
	}
	return srv
}

func waitForLog(t *testing.T, path, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(strings.Join(readLines(t, path), "\n"), want) {
		if time.Now().After(deadline) {
			t.Fatalf("%q never appeared in the upstream log:\n%s", want, strings.Join(readLines(t, path), "\n"))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func countLines(t *testing.T, path, line string) int {
	t.Helper()
	n := 0
	for _, l := range readLines(t, path) {
		if l == line {
			n++
		}
	}
	return n
}

// TestRoots_AnsweredFromConfig: a server with configured roots is told it
// has them — capability declared, roots/list answered locally — shared or
// not, and the client is never asked.
func TestRoots_AnsweredFromConfig(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("subprocess test")
	}
	for _, shared := range []bool{true, false} {
		capsLog := filepath.Join(t.TempDir(), "caps.log")
		cfg := &config.Config{SchemaVersion: 1, Servers: map[string]config.ServerConfig{
			"srv": rootsServer(t, shared, []string{"file:///work/app", "file:///work/lib"}, false, "", capsLog),
		}}
		h := startRelayHarness(t, cfg, `{"roots":{"listChanged":true}}`, Options{})
		h.write(callTool(2, "srv.roots"))
		outcome := h.outcome(h.response("2"))
		const want = `{"roots":[{"uri":"file:///work/app","name":"app"},{"uri":"file:///work/lib","name":"lib"}]}`
		if !outcome.Answered || string(outcome.Result) != want {
			t.Errorf("shared=%v: upstream saw %+v, want %s", shared, outcome, want)
		}
		h.noFrame("roots/list relayed to the client", 0, func(f rpcFrame) bool { return f.Method == "roots/list" })
		if caps := readLines(t, capsLog)[0]; !strings.Contains(caps, `"roots":{"listChanged":true}`) {
			t.Errorf("shared=%v: declared %s", shared, caps)
		}
	}
}

// TestRoots_NotDeclaredWithoutConfig: with no roots and no relay opt-in,
// nothing is declared and roots/list gets method-not-found as before.
func TestRoots_NotDeclaredWithoutConfig(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("subprocess test")
	}
	capsLog := filepath.Join(t.TempDir(), "caps.log")
	cfg := &config.Config{SchemaVersion: 1, Servers: map[string]config.ServerConfig{
		"srv": rootsServer(t, true, nil, false, "", capsLog),
	}}
	h := startRelayHarness(t, cfg, `{"roots":{}}`, Options{})
	h.write(callTool(2, "srv.roots"))
	if outcome := h.outcome(h.response("2")); outcome.Error == nil || outcome.Error.Code != -32601 {
		t.Errorf("upstream saw %+v, want method not found", outcome)
	}
	if caps := readLines(t, capsLog)[0]; caps != "{}" {
		t.Errorf("declared %s, want {}", caps)
	}
}

// TestRoots_EditNotifiesWithoutRestart: editing a non-empty roots list sends
// notifications/roots/list_changed to the running instance, which is not
// restarted and sees the new list when it asks again.
func TestRoots_EditNotifiesWithoutRestart(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("subprocess test")
	}
	logPath := filepath.Join(t.TempDir(), "requests.log")
	cfg := &config.Config{SchemaVersion: 1, Servers: map[string]config.ServerConfig{
		"srv": rootsServer(t, true, []string{"file:///before"}, false, logPath, ""),
	}}
	h := startRelayHarness(t, cfg, `{}`, Options{})
	h.write(callTool(2, "srv.roots"))
	h.response("2")

	reloaded := *cfg
	reloaded.Servers = map[string]config.ServerConfig{"srv": cfg.Servers["srv"]}
	edited := reloaded.Servers["srv"]
	edited.Roots = []string{"file:///after"}
	reloaded.Servers["srv"] = edited
	h.srv.applyReload(context.Background(), &reloaded)

	waitForLog(t, logPath, "notifications/roots/list_changed")
	h.write(callTool(3, "srv.roots"))
	if outcome := h.outcome(h.response("3")); !strings.Contains(string(outcome.Result), "file:///after") {
		t.Errorf("after the edit the upstream saw %s", outcome.Result)
	}
	if n := countLines(t, logPath, "initialize"); n != 1 {
		t.Errorf("the instance was initialized %d times; a roots edit must not restart it", n)
	}
}

// TestRoots_AddingRootsRestarts: adding roots where there were none changes
// the declared capability, so the instance restarts.
func TestRoots_AddingRootsRestarts(t *testing.T) {
	t.Parallel()
	old := &config.Config{Servers: map[string]config.ServerConfig{"srv": {Command: "x"}, "other": {Command: "y", Roots: []string{"file:///a"}}}}
	next := &config.Config{Servers: map[string]config.ServerConfig{"srv": {Command: "x", Roots: []string{"file:///a"}}, "other": {Command: "y", Roots: []string{"file:///b"}}}}
	if metadataOnlyReload(old, next) {
		t.Fatal("adding roots classified as metadata-only")
	}
	changed, ok := selectiveReload(old, next)
	if !ok || !changed["srv"] || changed["other"] {
		t.Fatalf("changed = %v, %v; want only srv (adding roots) restarted", changed, ok)
	}
	if edits := rootsListEdits(old, next); len(edits) != 1 || edits[0] != "other" {
		t.Errorf("rootsListEdits = %v, want [other]", edits)
	}
	editOnly := &config.Config{Servers: map[string]config.ServerConfig{"srv": {Command: "x"}, "other": {Command: "y", Roots: []string{"file:///b"}}}}
	if !metadataOnlyReload(old, editOnly) {
		t.Error("editing a non-empty roots list is not metadata-only")
	}
}

// TestRoots_RelayedForPrivateInstance: a private instance opted in to the
// roots relay, with no configured roots, gets its owning client's roots, and
// the client's roots/list_changed is forwarded to it.
func TestRoots_RelayedForPrivateInstance(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("subprocess test")
	}
	logPath := filepath.Join(t.TempDir(), "requests.log")
	capsLog := filepath.Join(t.TempDir(), "caps.log")
	cfg := &config.Config{SchemaVersion: 1, Servers: map[string]config.ServerConfig{
		"srv": rootsServer(t, false, nil, true, logPath, capsLog),
	}}
	h := startRelayHarness(t, cfg, `{"roots":{"listChanged":true}}`, Options{})
	h.write(callTool(2, "srv.roots"))
	req := h.request("roots/list")
	h.write(`{"jsonrpc":"2.0","id":` + string(req.ID) + `,"result":{"roots":[{"uri":"file:///client/ws","name":"ws"}]}}`)
	if outcome := h.outcome(h.response("2")); string(outcome.Result) != `{"roots":[{"uri":"file:///client/ws","name":"ws"}]}` {
		t.Errorf("upstream saw %+v", outcome)
	}
	if caps := readLines(t, capsLog)[0]; !strings.Contains(caps, `"roots":{"listChanged":true}`) {
		t.Errorf("declared %s", caps)
	}

	h.write(`{"jsonrpc":"2.0","method":"notifications/roots/list_changed"}`)
	waitForLog(t, logPath, "notifications/roots/list_changed")
}

// TestRoots_ConfigWinsOverRelay: configured roots are the answer even when
// the relay is also opted in.
func TestRoots_ConfigWinsOverRelay(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("subprocess test")
	}
	cfg := &config.Config{SchemaVersion: 1, Servers: map[string]config.ServerConfig{
		"srv": rootsServer(t, false, []string{"file:///configured"}, true, "", ""),
	}}
	h := startRelayHarness(t, cfg, `{"roots":{}}`, Options{})
	h.write(callTool(2, "srv.roots"))
	if outcome := h.outcome(h.response("2")); !strings.Contains(string(outcome.Result), "file:///configured") {
		t.Errorf("upstream saw %s", outcome.Result)
	}
	h.noFrame("roots/list relayed despite configured roots", 0, func(f rpcFrame) bool { return f.Method == "roots/list" })
}

func TestRootsResultShape(t *testing.T) {
	t.Parallel()
	var got struct {
		Roots []map[string]string `json:"roots"`
	}
	if err := json.Unmarshal(rootsResult([]string{"file:///", "file:///a/b%20c"}), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Roots) != 2 || got.Roots[0]["name"] != "" || got.Roots[1]["name"] != "b c" {
		t.Errorf("roots = %+v", got.Roots)
	}
}
