// Package httpguard carries the HTTP security wrappers shared by mcpmu's two
// listeners — the web management UI and the serve-mode MCP endpoint:
//
//   - Host allowlisting (DNS-rebinding defence),
//   - Origin validation,
//   - constant-time bearer-token enforcement,
//   - refusal of tokenless non-loopback binds.
//
// The wrappers must be installed unconditionally and outermost, independent
// of whether auth happens to be enabled: a page at http://evil.com can
// rebind DNS to 127.0.0.1 and speak same-origin to any loopback service, so
// a server whose protections are conditional on a token defaulting to empty
// has none.
package httpguard

import (
	"crypto/subtle"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// Options configures Middleware and RefuseUnsafeBind.
type Options struct {
	// Addr is the configured listen address ("host:port"). A specific host
	// part joins the Host allowlist so a deliberate non-loopback bind stays
	// reachable by its own address; a wildcard bind falls back to accepting
	// any IP-literal Host (an attacker domain is a name, never an IP).
	Addr string

	// Token is the bearer token required by Middleware. "" disables token
	// enforcement — the Host and Origin checks above it still apply.
	Token string

	// AllowedOrigins are extra Origin allowlist entries beyond loopback.
	AllowedOrigins []string
}

// RefuseUnsafeBind rejects a tokenless bind to a non-loopback address. Serve
// mode exposes tools/call and the web UI exposes configuration mutation and
// server registration — either way, an unauthenticated network-reachable
// listener is arbitrary execution one DNS-rebind away. tokenHint names the
// ways to configure the token, e.g. "--token or MCPMU_SERVE_TOKEN".
func RefuseUnsafeBind(addr, token, tokenHint string) error {
	if token != "" || isLoopbackAddr(addr) {
		return nil
	}
	return fmt.Errorf(
		"refusing to bind %s without a token: this endpoint manages configuration and can execute commands. "+
			"Set %s, or bind a loopback address", addr, tokenHint)
}

// Middleware nests Host validation, Origin validation, and bearer-token
// enforcement around next, in that order. Install it outermost — outside any
// logging, recovery, or auth middleware — so the checks hold for every route
// and every auth posture. This is the right shape for pure-bearer endpoints
// like serve mode; a server with its own richer authentication (cookies plus
// bearer) should compose HostAndOrigin around its existing auth middleware
// instead.
func Middleware(opts Options, next http.Handler) http.Handler {
	return HostAndOrigin(opts, RequireToken(opts.Token, next))
}

// HostAndOrigin applies just the unconditional request-hygiene wrappers:
// Host allowlisting, then Origin validation. Authentication stays the inner
// middleware's business.
func HostAndOrigin(opts Options, next http.Handler) http.Handler {
	return checkHost(opts, checkOrigin(opts, next))
}

// RequireToken gates every request behind the bearer token. Comparison is
// constant-time; failures get WWW-Authenticate per RFC 6750. An empty token
// disables enforcement (callers must have refused a tokenless non-loopback
// bind via RefuseUnsafeBind).
func RequireToken(token string, next http.Handler) http.Handler {
	return requireToken(token, next)
}

// checkHost rejects requests whose Host header does not name this server.
//
// A DNS-rebinding attack makes a victim browser resolve an attacker domain to
// 127.0.0.1; the browser then connects to our loopback listener carrying
// Host: evil.com — same-origin by the browser's bookkeeping, invisible to
// Origin checks because rebinded pages are "same-origin" with themselves.
// Only hosts that genuinely name this machine pass.
func checkHost(opts Options, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !hostAllowed(opts, r.Host) {
			http.Error(w, "host not allowed", http.StatusMisdirectedRequest)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// hostAllowed reports whether a Host header value names this listener. An
// empty value cannot occur through a real listener (HTTP/1.1 requires Host;
// HTTP/2 requires :authority) — it appears only in direct handler invocation,
// and is accepted for exactly that case.
func hostAllowed(opts Options, host string) bool {
	if host == "" {
		return true
	}
	name := host
	if h, _, err := net.SplitHostPort(host); err == nil {
		name = h
	}
	name = strings.ToLower(strings.Trim(name, "[]"))
	if name == "" {
		return false
	}
	switch name {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	bindHost := ""
	if h, _, err := net.SplitHostPort(opts.Addr); err == nil {
		bindHost = strings.ToLower(h)
	} else {
		bindHost = strings.ToLower(opts.Addr)
	}
	if bindHost != "" && bindHost == name {
		return true
	}
	// A reverse proxy configured to forward the client's original Host
	// presents the public name — the same one --allow-origin admits at the
	// Origin layer. Accept it here too, so the flag is sufficient on its own
	// for proxied setups. Safe by construction: browsers always send the
	// visited URL's authority as Host, so an attacker page can never present
	// a name the admin explicitly allowlisted.
	for _, allowed := range opts.AllowedOrigins {
		if allowedHostname(allowed) == name {
			return true
		}
	}
	// Wildcard bind: accept any IP-literal host. Rebinding needs a resolvable
	// attacker-controlled name, which an IP literal never is.
	if ip := net.ParseIP(name); ip != nil && isWildcardAddr(opts.Addr) {
		return true
	}
	return false
}

// allowedHostname extracts the hostname from an AllowedOrigins entry, which
// may be a full origin ("https://mcpmu.corp.example") or a bare hostname.
func allowedHostname(origin string) string {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		if h, _, err := net.SplitHostPort(origin); err == nil {
			return strings.ToLower(h)
		}
		return strings.ToLower(strings.Trim(origin, "[]"))
	}
	return strings.ToLower(u.Hostname())
}

// checkOrigin rejects browser cross-origin requests. An absent Origin (curl,
// MCP clients, same-machine agents) is allowed — rebinding-style attacks come
// from browsers, which always send it.
func checkOrigin(opts Options, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && !originAllowed(opts, origin) {
			http.Error(w, "origin not allowed", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func originAllowed(opts Options, origin string) bool {
	for _, allowed := range opts.AllowedOrigins {
		if strings.EqualFold(origin, allowed) {
			return true
		}
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	host := u.Hostname()
	// An Origin equal to this server's own address is same-origin by
	// definition — a browser talking to a tokened non-loopback bind sends it
	// on every form POST and fetch, and refusing it would make mutations
	// impossible while pages loaded fine.
	if originMatchesBind(opts.Addr, host, u.Port(), u.Scheme) {
		return true
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// originMatchesBind reports whether hostname:originPort over scheme is
// exactly this listener's address. On a wildcard bind any IP-literal host is
// accepted (a rebinding attacker always presents a resolvable name, never an
// IP); a specific bind must match exactly. An omitted port means the
// scheme's default.
func originMatchesBind(addr, hostname, originPort, scheme string) bool {
	bindHost, bindPort, err := net.SplitHostPort(addr)
	if err != nil {
		bindHost, bindPort = addr, ""
	}
	bindHost = strings.ToLower(strings.Trim(bindHost, "[]"))
	if !portMatches(originPort, scheme, bindPort) {
		return false
	}
	if bindHost == "" {
		return net.ParseIP(hostname) != nil
	}
	return bindHost == strings.ToLower(hostname)
}

func portMatches(originPort, scheme, bindPort string) bool {
	if originPort == "" {
		switch scheme {
		case "http":
			originPort = "80"
		case "https":
			originPort = "443"
		default:
			return bindPort == ""
		}
	}
	return originPort == bindPort
}

// requireToken gates every request behind the bearer token. Comparison is
// constant-time; failures get WWW-Authenticate per RFC 6750. An empty token
// disables enforcement (callers must have refused a tokenless non-loopback
// bind via RefuseUnsafeBind).
func requireToken(token string, next http.Handler) http.Handler {
	if token == "" {
		return next
	}
	tokenBytes := []byte(token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const prefix = "Bearer "
		header := r.Header.Get("Authorization")
		if !strings.HasPrefix(header, prefix) ||
			subtle.ConstantTimeCompare([]byte(header[len(prefix):]), tokenBytes) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// isLoopbackAddr reports whether a listen address can only be reached from
// this machine. An empty host (":8081") binds every interface and is not
// loopback.
func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") || host == "::1" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func isWildcardAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	return host == ""
}
