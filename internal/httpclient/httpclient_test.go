package httpclient

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNew_FollowsSameOriginRedirect(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/a" {
			http.Redirect(w, r, srv.URL+"/b", http.StatusFound)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	resp, err := New(nil).Get(srv.URL + "/a")
	if err != nil {
		t.Fatalf("same-origin redirect: %v", err)
	}
	body, err := ReadBody(resp, 1024)
	if err != nil || string(body) != "ok" {
		t.Fatalf("body=%q err=%v", body, err)
	}
}

func TestNew_RefusesCrossOriginRedirect(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("cross-origin target was reached with headers %v", r.Header)
	}))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/steal", http.StatusFound)
	}))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	req.Header.Set("Authorization", "Bearer secret")
	resp, err := New(nil).Do(req)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("expected cross-origin redirect to be refused")
	}
	if !errors.Is(err, ErrCrossOriginRedirect) {
		t.Fatalf("expected ErrCrossOriginRedirect, got %v", err)
	}
}

func TestNew_LimitsRedirectHops(t *testing.T) {
	var srv *httptest.Server
	hops := 0
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hops++
		http.Redirect(w, r, srv.URL+"/loop", http.StatusFound)
	}))
	defer srv.Close()

	resp, err := New(nil).Get(srv.URL)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("expected redirect loop to fail")
	}
	if !strings.Contains(err.Error(), "redirects") {
		t.Fatalf("unexpected error: %v", err)
	}
	if hops > MaxRedirects+1 {
		t.Fatalf("followed %d hops, want at most %d", hops, MaxRedirects+1)
	}
}

func TestNew_ClearsClientTimeoutAndKeepsTransport(t *testing.T) {
	base := &http.Client{Timeout: 5, Transport: &http.Transport{}}
	c := New(base)
	if c.Timeout != 0 {
		t.Fatalf("Timeout=%v, want 0", c.Timeout)
	}
	tt, ok := c.Transport.(*http.Transport)
	if !ok || tt == base.Transport {
		t.Fatal("expected a cloned *http.Transport")
	}
	if tt.ResponseHeaderTimeout != DefaultConnectTimeout {
		t.Fatalf("ResponseHeaderTimeout=%v", tt.ResponseHeaderTimeout)
	}
	if base.CheckRedirect != nil {
		t.Fatal("base client must not be mutated")
	}
}

func TestReadBody_TooLarge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(make([]byte, 100))
	}))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, err = ReadBody(resp, 99)
	if !errors.Is(err, ErrBodyTooLarge) {
		t.Fatalf("expected ErrBodyTooLarge, got %v", err)
	}

	resp, _ = http.Get(srv.URL)
	data, err := ReadBody(resp, 100)
	if err != nil || len(data) != 100 {
		t.Fatalf("exact-limit read: len=%d err=%v", len(data), err)
	}
}
