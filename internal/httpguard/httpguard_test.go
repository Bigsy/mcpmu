package httpguard

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func serve(opts Options, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	Middleware(opts, okHandler()).ServeHTTP(rec, req)
	return rec
}

func TestRefuseUnsafeBind(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:8081", ":8081", "192.168.1.10:8081", "bogus"} {
		if err := RefuseUnsafeBind(addr, "", "--token"); err == nil {
			t.Errorf("addr %q without token: want error, got nil", addr)
		}
	}
	for _, addr := range []string{"127.0.0.1:8081", "localhost:8081", "[::1]:8081"} {
		if err := RefuseUnsafeBind(addr, "", "--token"); err != nil {
			t.Errorf("loopback addr %q without token: %v", addr, err)
		}
	}
	for _, addr := range []string{"192.168.1.10:8081", ":8081"} {
		if err := RefuseUnsafeBind(addr, "sekrit", "--token"); err != nil {
			t.Errorf("addr %q with token: %v", addr, err)
		}
	}
}

func TestHostAllowlist(t *testing.T) {
	loopback := Options{Addr: "127.0.0.1:8081"}
	cases := []struct {
		name string
		opts Options
		host string
		want bool
	}{
		{"empty host refused (HTTP/1.0)", loopback, "", false},
		{"empty host refused on wildcard bind too", Options{Addr: ":8081"}, "", false},
		{"loopback ipv4 with port", loopback, "127.0.0.1:8081", true},
		{"localhost with port", loopback, "localhost:8081", true},
		{"ipv6 loopback", loopback, "[::1]:8081", true},
		{"rebound attacker domain", loopback, "evil.com", false},
		{"attacker domain with port", loopback, "evil.com:80", false},
		{"other lan ip", loopback, "192.168.1.5:8081", false},
		{"case-insensitive localhost", loopback, "LOCALHOST:8081", true},

		{"own bind address allowed", Options{Addr: "192.168.1.10:8080"}, "192.168.1.10:8080", true},
		{"other address still refused", Options{Addr: "192.168.1.10:8080"}, "192.168.1.11:8080", false},

		{"wildcard bind accepts ip literal", Options{Addr: ":8081"}, "192.168.1.5:8081", true},
		{"wildcard bind refuses name", Options{Addr: ":8081"}, "myserver.lan", false},
		{"wildcard bind refuses attacker domain", Options{Addr: ":8081"}, "evil.com", false},
		{"0.0.0.0 bind accepts ip literal", Options{Addr: "0.0.0.0:8081"}, "192.168.1.5:8081", true},
		{"0.0.0.0 bind accepts ipv6 literal", Options{Addr: "0.0.0.0:8081"}, "[fd00::7]:8081", true},
		{"0.0.0.0 bind refuses name", Options{Addr: "0.0.0.0:8081"}, "myserver.lan:8081", false},
		{"[::] bind accepts ip literal", Options{Addr: "[::]:8081"}, "10.0.0.7:8081", true},
		{"[::] bind refuses attacker domain", Options{Addr: "[::]:8081"}, "evil.com", false},
		{"specific ipv6 bind allows itself", Options{Addr: "[fd00::7]:8081"}, "[fd00::7]:8081", true},

		{"proxied host matching allowlisted origin", Options{Addr: "127.0.0.1:8081", AllowedOrigins: []string{"https://mcpmu.corp.example"}}, "mcpmu.corp.example", true},
		{"proxied host with forwarded port", Options{Addr: "127.0.0.1:8081", AllowedOrigins: []string{"https://mcpmu.corp.example"}}, "mcpmu.corp.example:8443", true},
		{"bare hostname allowlist entry", Options{Addr: "127.0.0.1:8081", AllowedOrigins: []string{"mcpmu.corp.example"}}, "mcpmu.corp.example", true},
		{"unrelated name still refused", Options{Addr: "127.0.0.1:8081", AllowedOrigins: []string{"https://mcpmu.corp.example"}}, "other.example", false},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
		req.Host = tc.host
		if got := hostAllowed(tc.opts, tc.host); got != tc.want {
			t.Errorf("%s: hostAllowed(%q) = %v, want %v", tc.name, tc.host, got, tc.want)
		}
	}
}

func TestMiddlewareHostRejection(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "evil.com"
	rec := serve(Options{Addr: "127.0.0.1:8081"}, req)
	if rec.Code != http.StatusMisdirectedRequest {
		t.Fatalf("rebound Host: status %d, want 421", rec.Code)
	}

	// HTTP/1.0 carries no Host; it must not slip past the allowlist.
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = ""
	if got := serve(Options{Addr: "127.0.0.1:8081"}, req).Code; got != http.StatusMisdirectedRequest {
		t.Fatalf("missing Host: status %d, want 421", got)
	}
}

func TestIsWildcardAddr(t *testing.T) {
	for _, addr := range []string{":8081", "0.0.0.0:8081", "[::]:8081"} {
		if !isWildcardAddr(addr) {
			t.Errorf("isWildcardAddr(%q) = false, want true", addr)
		}
	}
	for _, addr := range []string{"127.0.0.1:8081", "[::1]:8081", "192.168.1.10:8081", "localhost:8081", "bogus"} {
		if isWildcardAddr(addr) {
			t.Errorf("isWildcardAddr(%q) = true, want false", addr)
		}
	}
}

func TestOriginPolicy(t *testing.T) {
	opts := Options{Addr: "127.0.0.1:8081", AllowedOrigins: []string{"https://myapp.example"}}
	cases := []struct {
		origin string
		want   int
	}{
		{"", http.StatusOK}, // absent Origin passes
		{"http://localhost:3000", http.StatusOK},
		{"http://127.0.0.1", http.StatusOK},
		{"https://myapp.example", http.StatusOK},
		{"https://evil.example", http.StatusForbidden},
		{"null", http.StatusForbidden},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Host = "127.0.0.1:8081"
		if tc.origin != "" {
			req.Header.Set("Origin", tc.origin)
		}
		if got := serve(opts, req).Code; got != tc.want {
			t.Errorf("Origin %q: status %d, want %d", tc.origin, got, tc.want)
		}
	}
}

// TestOriginMatchesBind pins the same-origin-with-bind rule: a tokened
// non-loopback deployment must accept the Origin its own browser clients
// send on form POSTs, while everything else stays rejected.
func TestOriginMatchesBind(t *testing.T) {
	bound := Options{Addr: "192.168.1.5:8080"}
	cases := []struct {
		name   string
		opts   Options
		origin string
		want   bool
	}{
		{"exact bind origin", bound, "http://192.168.1.5:8080", true},
		{"bind host wrong port", bound, "http://192.168.1.5:9999", false},
		{"bind host other ip", bound, "http://192.168.1.6:8080", false},
		{"attacker name never matches", bound, "http://evil.com:8080", false},

		{"default http port omitted", Options{Addr: "192.168.1.5:80"}, "http://192.168.1.5", true},
		{"default https port omitted", Options{Addr: "192.168.1.5:443"}, "https://192.168.1.5", true},
		{"omitted port vs non-default bind", bound, "http://192.168.1.5", false},

		{"wildcard accepts ip literal origin", Options{Addr: ":8081"}, "http://10.0.0.7:8081", true},
		{"wildcard refuses name origin", Options{Addr: ":8081"}, "http://myhost.internal:8081", false},
		{"wildcard port mismatch", Options{Addr: ":8081"}, "http://10.0.0.7:9999", false},
		{"0.0.0.0 accepts ip literal origin", Options{Addr: "0.0.0.0:8081"}, "http://10.0.0.7:8081", true},
		{"0.0.0.0 refuses name origin", Options{Addr: "0.0.0.0:8081"}, "http://myhost.internal:8081", false},
		{"[::] accepts ip literal origin", Options{Addr: "[::]:8081"}, "http://10.0.0.7:8081", true},
	}
	for _, tc := range cases {
		if got := originAllowed(tc.opts, tc.origin); got != tc.want {
			t.Errorf("%s: originAllowed(%q) = %v, want %v", tc.name, tc.origin, got, tc.want)
		}
	}
}

func TestTokenEnforcement(t *testing.T) {
	opts := Options{Addr: "127.0.0.1:8081", Token: "sekrit"}

	t.Run("missing token is 401 with WWW-Authenticate", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Host = "127.0.0.1:8081"
		rec := serve(opts, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status %d, want 401", rec.Code)
		}
		if rec.Header().Get("WWW-Authenticate") != "Bearer" {
			t.Fatalf("WWW-Authenticate = %q, want Bearer", rec.Header().Get("WWW-Authenticate"))
		}
	})
	t.Run("wrong token is 401", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Host = "127.0.0.1:8081"
		req.Header.Set("Authorization", "Bearer wrong")
		if got := serve(opts, req).Code; got != http.StatusUnauthorized {
			t.Fatalf("status %d, want 401", got)
		}
	})
	t.Run("right token passes", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Host = "127.0.0.1:8081"
		req.Header.Set("Authorization", "Bearer sekrit")
		if got := serve(opts, req).Code; got != http.StatusOK {
			t.Fatalf("status %d, want 200", got)
		}
	})
}

// TestProxiedForwardedHostAdmitted runs the full middleware stack against
// the deployment shape where a reverse proxy forwards the client's original
// Host: with --allow-origin listing the public origin, both the Host check
// and the Origin check pass and the request reaches the handler.
func TestProxiedForwardedHostAdmitted(t *testing.T) {
	opts := Options{
		Addr:           "127.0.0.1:8081",
		Token:          "proxy-secret",
		AllowedOrigins: []string{"https://mcpmu.corp.example"},
	}

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Host = "mcpmu.corp.example"
	req.Header.Set("Origin", "https://mcpmu.corp.example")
	req.Header.Set("Authorization", "Bearer proxy-secret")
	if got := serve(opts, req).Code; got != http.StatusOK {
		t.Fatalf("proxied request status = %d, want 200 (421 means Host died before Origin)", got)
	}

	// The allowlisted name must not open the door to everything else.
	req = httptest.NewRequest(http.MethodPost, "/", nil)
	req.Host = "other.example"
	req.Header.Set("Origin", "https://other.example")
	req.Header.Set("Authorization", "Bearer proxy-secret")
	if got := serve(opts, req).Code; got != http.StatusMisdirectedRequest {
		t.Fatalf("unproxied foreign Host status = %d, want 421", got)
	}
}

// TestGuardsHoldWithoutToken pins the Phase-0 posture: Host and Origin checks
// apply even when no token is configured — the state newAuth("") used to
// leave entirely unprotected.
func TestGuardsHoldWithoutToken(t *testing.T) {
	opts := Options{Addr: "127.0.0.1:8081"}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "evil.com"
	if got := serve(opts, req).Code; got != http.StatusMisdirectedRequest {
		t.Fatalf("Host check skipped without token: status %d, want 421", got)
	}

	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "127.0.0.1:8081"
	req.Header.Set("Origin", "https://evil.example")
	if got := serve(opts, req).Code; got != http.StatusForbidden {
		t.Fatalf("Origin check skipped without token: status %d, want 403", got)
	}
}
