package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/process"
)

type synchronizedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *synchronizedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *synchronizedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

func bareSubscriptionSession() *Session {
	return &Session{subs: make(map[string]process.InstanceID), resourceMap: make(map[string]process.InstanceID)}
}

func TestResourceSubscriptionsTransitionRefcounts(t *testing.T) {
	t.Parallel()
	registry := newResourceSubscriptions()
	first := bareSubscriptionSession()
	second := bareSubscriptionSession()
	key := resourceSubscriptionKey{Instance: process.SharedInstanceID("files"), URI: "file:///shared.txt"}

	var subscribes atomic.Int32
	subscribe := func() error {
		subscribes.Add(1)
		return nil
	}
	if _, err := registry.subscribe(first, key, 1, subscribe); err != nil {
		t.Fatalf("first subscribe: %v", err)
	}
	if _, err := registry.subscribe(first, key, 1, subscribe); err != nil {
		t.Fatalf("duplicate subscribe: %v", err)
	}
	if _, err := registry.subscribe(second, key, 1, subscribe); err != nil {
		t.Fatalf("second-session subscribe: %v", err)
	}
	if got := subscribes.Load(); got != 1 {
		t.Fatalf("0→1→1→2 should call upstream subscribe once, got %d", got)
	}

	var unsubscribes atomic.Int32
	unsubscribe := func(uint64) error {
		unsubscribes.Add(1)
		return nil
	}
	if err := registry.unsubscribe(first, key, unsubscribe); err != nil {
		t.Fatalf("first unsubscribe: %v", err)
	}
	if got := unsubscribes.Load(); got != 0 {
		t.Fatalf("2→1 should not call upstream unsubscribe, got %d calls", got)
	}
	if err := registry.unsubscribe(second, key, unsubscribe); err != nil {
		t.Fatalf("last unsubscribe: %v", err)
	}
	if got := unsubscribes.Load(); got != 1 {
		t.Fatalf("1→0 should call upstream unsubscribe once, got %d", got)
	}
	if registry.hasSubscribers(key) {
		t.Fatal("subscription remained after last session unsubscribed")
	}
}

func TestResourceSubscriptionsFailureAndReplay(t *testing.T) {
	t.Parallel()
	registry := newResourceSubscriptions()
	first := bareSubscriptionSession()
	second := bareSubscriptionSession()
	key := resourceSubscriptionKey{Instance: process.SharedInstanceID("files"), URI: "file:///shared.txt"}

	wantErr := errors.New("subscribe rejected")
	if _, err := registry.subscribe(first, key, 1, func() error { return wantErr }); !errors.Is(err, wantErr) {
		t.Fatalf("initial failure = %v, want %v", err, wantErr)
	}
	if registry.hasSubscribers(key) || len(first.subscriptionSnapshot()) != 0 {
		t.Fatal("failed initial subscribe left phantom state")
	}

	if _, err := registry.subscribe(first, key, 1, func() error { return nil }); err != nil {
		t.Fatalf("subscribe first: %v", err)
	}
	if _, err := registry.subscribe(second, key, 1, func() error { return nil }); err != nil {
		t.Fatalf("subscribe second: %v", err)
	}
	var replays atomic.Int32
	if dropped, err := registry.replay(key, 2, func() error {
		replays.Add(1)
		return nil
	}); err != nil || len(dropped) != 0 {
		t.Fatalf("successful replay: dropped=%d err=%v", len(dropped), err)
	}
	if got := replays.Load(); got != 1 {
		t.Fatalf("replay calls = %d, want 1", got)
	}
	if _, err := registry.replay(key, 2, func() error {
		replays.Add(1)
		return nil
	}); err != nil {
		t.Fatalf("same-generation replay: %v", err)
	}
	if got := replays.Load(); got != 1 {
		t.Fatalf("same generation replayed twice, calls=%d", got)
	}

	dropped, err := registry.replay(key, 3, func() error { return wantErr })
	if !errors.Is(err, wantErr) {
		t.Fatalf("failed replay = %v, want %v", err, wantErr)
	}
	if len(dropped) != 2 {
		t.Fatalf("failed replay dropped %d sessions, want 2", len(dropped))
	}
	if registry.hasSubscribers(key) || len(first.subscriptionSnapshot()) != 0 || len(second.subscriptionSnapshot()) != 0 {
		t.Fatal("failed replay retained subscription intent")
	}
}

func TestResourceSubscriptionsUnsubscribeFailureStillClearsLocalState(t *testing.T) {
	t.Parallel()
	registry := newResourceSubscriptions()
	session := bareSubscriptionSession()
	key := resourceSubscriptionKey{Instance: process.SharedInstanceID("files"), URI: "file:///shared.txt"}
	if _, err := registry.subscribe(session, key, 1, func() error { return nil }); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	wantErr := errors.New("upstream unavailable")
	if err := registry.unsubscribe(session, key, func(uint64) error { return wantErr }); !errors.Is(err, wantErr) {
		t.Fatalf("unsubscribe error = %v, want %v", err, wantErr)
	}
	if registry.hasSubscribers(key) || len(session.subscriptionSnapshot()) != 0 {
		t.Fatal("upstream unsubscribe failure retained local state")
	}
}

func newDirectResourceSession(t *testing.T, core *Core, cfg *config.Config, namespace string) (*Session, *synchronizedBuffer) {
	t.Helper()
	out := &synchronizedBuffer{}
	session, err := NewSession(core, Options{
		SessionOptions: SessionOptions{
			Namespace:       namespace,
			ExposeResources: true,
		},
		Config:        cfg,
		Stdin:         strings.NewReader(""),
		Stdout:        out,
		ServerName:    "mcpmu-test",
		ServerVersion: "1.0.0",
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	params := json.RawMessage(`{"protocolVersion":"2024-11-05","clientInfo":{"name":"test","version":"1.0"}}`)
	if _, rpcErr := session.handleInitialize(context.Background(), params); rpcErr != nil {
		session.Close()
		t.Fatalf("initialize: %v", rpcErr)
	}
	return session, out
}

func countLoggedMethod(t *testing.T, path, method string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatalf("read request log: %v", err)
	}
	count := 0
	for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		if line == method {
			count++
		}
	}
	return count
}

func waitForLoggedMethodCount(t *testing.T, path, method string, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if countLoggedMethod(t, path, method) >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d %s calls; got %d", want, method, countLoggedMethod(t, path, method))
}

func TestCoreSubscriptionsSharedRefcountAndDisconnectCleanup(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping subprocess subscription test in short mode")
	}
	requestLog := filepath.Join(t.TempDir(), "requests.log")
	cfg := &config.Config{SchemaVersion: 1, Servers: map[string]config.ServerConfig{
		"files": fakeServerConfig(t, map[string]any{
			"tools":              []any{},
			"resources":          []any{map[string]any{"uri": "file:///shared.txt", "name": "shared"}},
			"resourcesSubscribe": true,
			"requestLogPath":     requestLog,
		}),
	}}
	core, err := NewCore(Options{Config: cfg, PIDTrackerDir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewCore: %v", err)
	}
	defer core.Close()
	first, _ := newDirectResourceSession(t, core, cfg, "")
	second, _ := newDirectResourceSession(t, core, cfg, "")

	if _, rpcErr := first.handleResourcesList(context.Background()); rpcErr != nil {
		t.Fatalf("first resources/list: %v", rpcErr)
	}
	if _, rpcErr := second.handleResourcesList(context.Background()); rpcErr != nil {
		t.Fatalf("second resources/list: %v", rpcErr)
	}
	params := json.RawMessage(`{"uri":"file:///shared.txt"}`)
	if _, rpcErr := first.handleResourcesSubscribe(context.Background(), params); rpcErr != nil {
		t.Fatalf("first subscribe: %v", rpcErr)
	}
	if _, rpcErr := first.handleResourcesSubscribe(context.Background(), params); rpcErr != nil {
		t.Fatalf("duplicate subscribe: %v", rpcErr)
	}
	if _, rpcErr := second.handleResourcesSubscribe(context.Background(), params); rpcErr != nil {
		t.Fatalf("second subscribe: %v", rpcErr)
	}
	if got := countLoggedMethod(t, requestLog, "resources/subscribe"); got != 1 {
		t.Fatalf("upstream subscribe calls = %d, want 1", got)
	}

	first.Close()
	if got := countLoggedMethod(t, requestLog, "resources/unsubscribe"); got != 0 {
		t.Fatalf("disconnect at refcount 2→1 sent %d upstream unsubscribe calls", got)
	}
	second.Close()
	waitForLoggedMethodCount(t, requestLog, "resources/unsubscribe", 1)
	if got := countLoggedMethod(t, requestLog, "resources/unsubscribe"); got != 1 {
		t.Fatalf("last disconnect upstream unsubscribe calls = %d, want 1", got)
	}
}

func TestCoreSubscriptionsUpstreamFailureLeavesNoRefcount(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping subprocess subscription test in short mode")
	}
	requestLog := filepath.Join(t.TempDir(), "requests.log")
	cfg := &config.Config{SchemaVersion: 1, Servers: map[string]config.ServerConfig{
		"files": fakeServerConfig(t, map[string]any{
			"tools":              []any{},
			"resources":          []any{map[string]any{"uri": "file:///failed", "name": "failed"}},
			"resourcesSubscribe": true,
			"requestLogPath":     requestLog,
			"errors": map[string]any{
				"resources/subscribe": map[string]any{"code": -32000, "message": "rejected"},
			},
		}),
	}}
	core, err := NewCore(Options{Config: cfg, PIDTrackerDir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewCore: %v", err)
	}
	defer core.Close()
	session, _ := newDirectResourceSession(t, core, cfg, "")
	defer session.Close()
	if _, rpcErr := session.handleResourcesList(context.Background()); rpcErr != nil {
		t.Fatalf("resources/list: %v", rpcErr)
	}
	params := json.RawMessage(`{"uri":"file:///failed"}`)
	for attempt := range 2 {
		if _, rpcErr := session.handleResourcesSubscribe(context.Background(), params); rpcErr == nil {
			t.Fatalf("subscribe attempt %d unexpectedly succeeded", attempt+1)
		}
	}
	if got := countLoggedMethod(t, requestLog, "resources/subscribe"); got != 2 {
		t.Fatalf("failed subscribes were cached; upstream calls = %d, want 2", got)
	}
	key := resourceSubscriptionKey{Instance: process.SharedInstanceID("files"), URI: "file:///failed"}
	if core.subscriptions.hasSubscribers(key) || len(session.subscriptionSnapshot()) != 0 {
		t.Fatal("failed upstream subscribe left phantom refcount")
	}
}

func TestCoreSubscriptionsInstanceScopedRouting(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping subprocess subscription test in short mode")
	}
	logA := filepath.Join(t.TempDir(), "a.log")
	logB := filepath.Join(t.TempDir(), "b.log")
	cfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"a": fakeServerConfig(t, map[string]any{
				"tools": []any{}, "resources": []any{map[string]any{"uri": "file:///same", "name": "a"}},
				"resourcesSubscribe": true, "requestLogPath": logA,
			}),
			"b": fakeServerConfig(t, map[string]any{
				"tools": []any{}, "resources": []any{map[string]any{"uri": "file:///same", "name": "b"}},
				"resourcesSubscribe": true, "requestLogPath": logB,
			}),
		},
		Namespaces: map[string]config.NamespaceConfig{
			"a-first": {ServerIDs: []string{"a", "b"}},
			"b-first": {ServerIDs: []string{"b", "a"}},
		},
	}
	core, err := NewCore(Options{Config: cfg, PIDTrackerDir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewCore: %v", err)
	}
	defer core.Close()
	first, firstOut := newDirectResourceSession(t, core, cfg, "a-first")
	defer first.Close()
	second, secondOut := newDirectResourceSession(t, core, cfg, "b-first")
	defer second.Close()

	if _, rpcErr := first.handleResourcesList(context.Background()); rpcErr != nil {
		t.Fatalf("a-first resources/list: %v", rpcErr)
	}
	if _, rpcErr := second.handleResourcesList(context.Background()); rpcErr != nil {
		t.Fatalf("b-first resources/list: %v", rpcErr)
	}
	params := json.RawMessage(`{"uri":"file:///same"}`)
	if _, rpcErr := first.handleResourcesSubscribe(context.Background(), params); rpcErr != nil {
		t.Fatalf("a-first subscribe: %v", rpcErr)
	}
	if _, rpcErr := second.handleResourcesSubscribe(context.Background(), params); rpcErr != nil {
		t.Fatalf("b-first subscribe: %v", rpcErr)
	}
	if got := countLoggedMethod(t, logA, "resources/subscribe"); got != 1 {
		t.Fatalf("server a subscribe calls = %d, want 1", got)
	}
	if got := countLoggedMethod(t, logB, "resources/subscribe"); got != 1 {
		t.Fatalf("server b subscribe calls = %d, want 1", got)
	}

	paramsJSON := json.RawMessage(`{"uri":"file:///same"}`)
	core.notifications.Publish(process.UpstreamNotification{
		Instance: process.SharedInstanceID("a"), Method: "notifications/resources/updated", Params: paramsJSON,
	})
	core.notifications.Publish(process.UpstreamNotification{
		Instance: process.SharedInstanceID("b"), Method: "notifications/resources/updated", Params: paramsJSON,
	})
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(collectUpdatedURIs(t, firstOut.String())) == 1 && len(collectUpdatedURIs(t, secondOut.String())) == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := collectUpdatedURIs(t, firstOut.String()); len(got) != 1 {
		t.Fatalf("a-first updates = %v, want one instance-scoped update", got)
	}
	if got := collectUpdatedURIs(t, secondOut.String()); len(got) != 1 {
		t.Fatalf("b-first updates = %v, want one instance-scoped update", got)
	}
}

func TestCoreSubscriptionsReplayAfterRestart(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping subprocess subscription test in short mode")
	}
	requestLog := filepath.Join(t.TempDir(), "requests.log")
	cfg := &config.Config{SchemaVersion: 1, Servers: map[string]config.ServerConfig{
		"files": fakeServerConfig(t, map[string]any{
			"tools":              []any{},
			"resources":          []any{map[string]any{"uri": "file:///restart", "name": "restart"}},
			"resourcesSubscribe": true,
			"requestLogPath":     requestLog,
		}),
	}}
	core, err := NewCore(Options{Config: cfg, PIDTrackerDir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewCore: %v", err)
	}
	defer core.Close()
	session, out := newDirectResourceSession(t, core, cfg, "")
	defer session.Close()
	if _, rpcErr := session.handleResourcesList(context.Background()); rpcErr != nil {
		t.Fatalf("resources/list: %v", rpcErr)
	}
	if _, rpcErr := session.handleResourcesSubscribe(context.Background(), json.RawMessage(`{"uri":"file:///restart"}`)); rpcErr != nil {
		t.Fatalf("subscribe: %v", rpcErr)
	}
	waitForLoggedMethodCount(t, requestLog, "resources/subscribe", 1)
	if err := core.supervisor.StopInstance(process.SharedInstanceID("files")); err != nil {
		t.Fatalf("stop upstream: %v", err)
	}
	if _, _, err := core.getOrStartHandle(context.Background(), "files"); err != nil {
		t.Fatalf("restart upstream: %v", err)
	}
	waitForLoggedMethodCount(t, requestLog, "resources/subscribe", 2)
	handle := core.supervisor.GetInstance(process.SharedInstanceID("files"))
	if handle == nil {
		t.Fatal("restarted handle missing")
	}
	core.OnUpstreamNotification(process.UpstreamNotification{
		Instance:   handle.InstanceID(),
		Generation: handle.Generation(),
		Method:     "notifications/resources/updated",
		Params:     json.RawMessage(`{"uri":"file:///restart"}`),
		Upstream:   true,
	})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(collectUpdatedURIs(t, out.String())) == 1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("updates did not resume after replay; got %v", collectUpdatedURIs(t, out.String()))
}

func TestCoreSubscriptionsReplayFailureNotifiesAndDrops(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{SchemaVersion: 1, Servers: map[string]config.ServerConfig{}}
	core, err := NewCore(Options{Config: cfg, PIDTrackerDir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewCore: %v", err)
	}
	defer core.Close()
	session, out := newDirectResourceSession(t, core, cfg, "")
	defer session.Close()
	key := resourceSubscriptionKey{Instance: process.SharedInstanceID("gone"), URI: "file:///gone"}
	if _, err := core.subscriptions.subscribe(session, key, 1, func() error { return nil }); err != nil {
		t.Fatalf("seed subscription: %v", err)
	}
	dropped, replayErr := core.subscriptions.replay(key, 2, func() error { return errors.New("URI removed") })
	if replayErr == nil {
		t.Fatal("expected replay failure")
	}
	core.notifyDroppedSubscriptions(dropped)
	if len(session.subscriptionSnapshot()) != 0 || core.subscriptions.hasSubscribers(key) {
		t.Fatal("failed replay retained local or Core state")
	}
	if !strings.Contains(out.String(), `"method":"notifications/resources/list_changed"`) {
		t.Fatalf("affected session was not notified after replay failure: %s", out.String())
	}
}

func TestCoreSubscriptionsReloadClearsEverySession(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{SchemaVersion: 1, Servers: map[string]config.ServerConfig{}}
	core, err := NewCore(Options{Config: cfg, PIDTrackerDir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewCore: %v", err)
	}
	defer core.Close()
	first, _ := newDirectResourceSession(t, core, cfg, "")
	defer first.Close()
	second, _ := newDirectResourceSession(t, core, cfg, "")
	defer second.Close()
	key := resourceSubscriptionKey{Instance: process.SharedInstanceID("files"), URI: "file:///reload"}
	for _, session := range []*Session{first, second} {
		session.resourceMapMu.Lock()
		session.resourceMap[key.URI] = key.Instance
		session.resourceMapMu.Unlock()
		if _, err := core.subscriptions.subscribe(session, key, 1, func() error { return nil }); err != nil {
			t.Fatalf("seed subscription: %v", err)
		}
	}

	first.applyReload(context.Background(), cfg)
	if core.subscriptions.hasSubscribers(key) {
		t.Fatal("reload retained Core subscription state")
	}
	for index, session := range []*Session{first, second} {
		if len(session.subscriptionSnapshot()) != 0 {
			t.Fatalf("session %d retained subscription state", index)
		}
		session.resourceMapMu.RLock()
		remainingURIs := len(session.resourceMap)
		session.resourceMapMu.RUnlock()
		if remainingURIs != 0 {
			t.Fatalf("session %d retained %d URI mappings", index, remainingURIs)
		}
	}
}
