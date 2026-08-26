package mcp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestUpstreamHTTPError_IsVersionRejection(t *testing.T) {
	cases := []struct {
		name string
		code int
		body string
		want bool
	}{
		{"jsonrpc error message", 400, `{"jsonrpc":"2.0","error":{"code":-32000,"message":"Unsupported MCP-Protocol-Version: 2025-11-25"}}`, true},
		{"bare error string", 400, `{"error":"Unsupported protocol version: 2025-11-25"}`, true},
		{"supportedVersions field", 400, `{"error":"bad request","supportedVersions":["2024-11-05"]}`, true},
		{"unrelated json error", 400, `{"jsonrpc":"2.0","error":{"code":-32600,"message":"Invalid Request"}}`, false},
		{"plain text fallback", 400, `Unsupported MCP-Protocol-Version header`, true},
		{"plain text unrelated", 400, `missing session`, false},
		{"not a 400", 403, `{"error":"unsupported version"}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := &UpstreamHTTPError{Code: tc.code, Body: tc.body}
			if got := e.IsVersionRejection(); got != tc.want {
				t.Fatalf("IsVersionRejection()=%v, want %v", got, tc.want)
			}
		})
	}
}

func TestIsProtocolVersionError_Structured(t *testing.T) {
	if !isProtocolVersionError(&ProtocolVersionError{Version: "x", Cause: &UpstreamHTTPError{Code: 400, Body: "nope"}}) {
		t.Fatal("ProtocolVersionError must classify as version error regardless of body text")
	}
	if isProtocolVersionError(&UpstreamHTTPError{Code: 403, Status: "403 Forbidden", Body: "no"}) {
		t.Fatal("403 must not classify as version error")
	}
}

func TestSend_JSONResponseTooLarge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(make([]byte, MaxJSONResponseSize+1))
	}))
	defer srv.Close()

	tr := NewStreamableHTTPTransport(StreamableHTTPConfig{URL: srv.URL})
	defer func() { _ = tr.Close() }()

	err := tr.Send(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	if !errors.Is(err, ErrBodyTooLarge) {
		t.Fatalf("expected ErrBodyTooLarge, got %v", err)
	}
}

func TestSend_CloseAbortsInFlightPOST(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)

	tr := NewStreamableHTTPTransport(StreamableHTTPConfig{URL: srv.URL})
	errCh := make(chan error, 1)
	go func() {
		errCh <- tr.Send(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	}()

	time.Sleep(100 * time.Millisecond)
	_ = tr.Close()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected Send to fail after Close")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Send did not return after Close; POST not bound to baseCtx")
	}
}

func TestTransport_RefusesCrossOriginRedirect(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("Mcp-Session-Id") != "" {
			t.Errorf("credentials leaked to redirect target: %v", r.Header)
		}
	}))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
	}))
	defer srv.Close()

	tr := NewStreamableHTTPTransport(StreamableHTTPConfig{URL: srv.URL, BearerToken: "secret"})
	defer func() { _ = tr.Close() }()
	if err := tr.Send(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"ping"}`)); err == nil {
		t.Fatal("expected redirect refusal")
	}
}
