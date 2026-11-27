package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"testing"

	"github.com/Bigsy/mcpmu/internal/config"
)

func TestStdioSlotCoversResponseWrite(t *testing.T) {
	started, finish := make(chan struct{}), make(chan struct{})
	session := &Session{
		requestSlots: make(chan struct{}, 1),
		writer: frameWriterFunc(func(p []byte) (int, error) {
			close(started)
			select {
			case <-finish:
			case <-t.Context().Done():
			}
			return len(p), nil
		}),
	}
	done := make(chan error, 1)
	go func() { done <- session.handleMessage(t.Context(), []byte(`{"jsonrpc":"2.0","id":1,"method":"ping"}`)) }()
	<-started
	if got := len(session.requestSlots); got != 1 {
		t.Errorf("occupied slots during write = %d", got)
	}
	close(finish)
	if err := <-done; err != nil || len(session.requestSlots) != 0 {
		t.Fatalf("after write: err = %v, slots = %d", err, len(session.requestSlots))
	}
}

func TestStdioSlotReleasedAfterPanicResponse(t *testing.T) {
	out := &panicOnceWriter{trigger: `"id":1`}
	s := &Session{requestSlots: make(chan struct{}, 1), writer: out}
	for _, id := range []string{"1", "2"} {
		if err := s.handleMessage(t.Context(), []byte(`{"jsonrpc":"2.0","method":"ping","id":`+id+`}`)); err != nil {
			t.Fatal(err)
		}
	}
	got := responsesByID(t, out.String())
	if len(got) != 2 || got["1"].Error == nil || got["1"].Error.Code != ErrCodeInternalError || got["2"].Error != nil || len(s.requestSlots) != 0 {
		t.Fatalf("responses after panic: %s", out.String())
	}
}

func TestStdioControlFramesBypassFullSession(t *testing.T) {
	var out bytes.Buffer
	s := &Session{requestSlots: make(chan struct{}, 1), writer: &out, inflight: newInflightCalls(), outbound: newOutboundRequests()}
	s.requestSlots <- struct{}{}
	ctx, release := s.TrackRequest(t.Context(), json.RawMessage("1"))
	defer release()
	if err := s.handleMessage(t.Context(), []byte(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":1}}`)); err != nil || ctx.Err() != context.Canceled {
		t.Fatalf("cancel: %v, %v", err, ctx.Err())
	}
	id, _, entry, err := s.outbound.register()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.handleMessage(t.Context(), []byte(`{"jsonrpc":"2.0","result":{},"id":`+string(id)+`}`)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entry.done:
	default:
		t.Fatal("client reply was not routed")
	}
	if out.Len() != 0 || len(s.requestSlots) != 1 {
		t.Fatalf("unexpected output %q or slot consumption", out.String())
	}
}

func TestStdioLimitUsesCoreStartupConfig(t *testing.T) {
	opts := Options{Config: &config.Config{MaxConcurrentRequestsPerSession: 2}, Stdout: io.Discard}
	core, err := NewCore(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer core.Close()
	next := *opts.Config
	next.MaxConcurrentRequestsPerSession = 3
	if !metadataOnlyReload(opts.Config, &next) {
		t.Fatal("changing the stdio limit requires a full reload")
	}
	core.applyReload(t.Context(), &next, nil)
	s, err := NewSession(core, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if cap(s.requestSlots) != 2 {
		t.Fatalf("session after reload: limit = %d", cap(s.requestSlots))
	}
}
