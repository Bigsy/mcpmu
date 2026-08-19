// Package httpserve exposes the aggregation endpoint over the MCP Streamable
// HTTP transport (POST + standalone GET SSE stream). It owns the listener,
// the session table, and the transport semantics; nothing here knows about
// aggregation — it only adapts HTTP to server.Session.
package httpserve

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"log"
	"maps"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Bigsy/mcpmu/internal/server"
)

const (
	// maxBodyBytes bounds POST bodies, mirroring the daemon's
	// maxHandshakeLine posture of bounding what a client can make us buffer.
	maxBodyBytes = 1 << 20

	// DefaultAddr is the default listen address (web owns 8080).
	DefaultAddr = "127.0.0.1:8081"

	// DefaultSessionIdleTimeout reaps sessions whose client stopped acting.
	DefaultSessionIdleTimeout = 30 * time.Minute

	// maxSessions bounds the session table. Sessions are cheap while lazy,
	// but each is an upstream fan-out waiting to happen (--eager, or
	// shared:false private instances), and until the idle reaper runs a
	// reconnect-looping client that never DELETEs could mint thousands. Far
	// above any legitimate concurrent-client count; initialize gets 503 past
	// it.
	maxSessions = 256

	// postBodyReadTimeout bounds how long one POST may spend trickling its
	// body in (a slow-loris guard; MaxBytesReader bounds only size). Applied
	// per-request via ResponseController and cleared before dispatch, unlike
	// a server-wide ReadTimeout, which would kill the GET stream and long
	// tool calls.
	postBodyReadTimeout = 30 * time.Second
)

// Tunables, overridable in tests.
var (
	keepaliveInterval = 30 * time.Second
	janitorInterval   = 60 * time.Second
	writeDeadline     = 30 * time.Second
)

// Options configures the HTTP serve listener.
type Options struct {
	Core *server.Core
	Addr string // default DefaultAddr
	// Token is the bearer token required on every request. "" is only
	// allowed on a loopback bind — New refuses a non-loopback Addr without
	// one, because serve-mode tools/call is arbitrary code execution.
	Token          string
	AllowedOrigins []string // extra Origin allowlist entries (loopback is always allowed)
	// SessionIdleTimeout reaps sessions with no client action (POST, DELETE,
	// GET attach) for this long. 0 = never reap. Keepalive writes are
	// deliberately not activity — a write into a black-holed connection
	// "succeeds" into the socket buffer for minutes.
	SessionIdleTimeout time.Duration

	// Per-Session defaults, mirroring the serve flags.
	Namespace          string // default when the URL has no namespace segment
	EagerStart         bool
	ExposeManagerTools bool
	ExposeResources    bool
	ExposePrompts      bool
	ServerVersion      string
}

// Server is the Streamable HTTP listener plus its session table.
type Server struct {
	opts Options
	core *server.Core
	http *http.Server

	// baseCtx parents every session context; baseCancel ends them all.
	baseCtx    context.Context
	baseCancel context.CancelFunc

	mu       sync.Mutex
	closed   bool
	sessions map[string]*httpSession // key: Mcp-Session-Id
}

// httpSession pairs one server.Session with its HTTP-side state.
type httpSession struct {
	sess *server.Session
	hub  *sseHub
	// namespace is the URL segment the session was minted under ("" for
	// /mcp). Enforced on every request: a session ID minted under /mcp/work
	// presented to /mcp/personal is a 404, otherwise one namespace's ID
	// could ride any route while keeping its original toolset.
	namespace  string
	ctx        context.Context
	cancel     context.CancelFunc
	lastActive atomic.Int64 // unix nanos; bumped by client actions only
	inflight   atomic.Int64 // POSTed requests currently being dispatched
	closeOnce  sync.Once
}

func (hs *httpSession) touch() {
	hs.lastActive.Store(time.Now().UnixNano())
}

// beginRequest/endRequest bracket a POST being handled. lastActive is stamped
// when a request *arrives*, so a tool call that runs longer than
// SessionIdleTimeout would otherwise have its session reaped mid-flight —
// reapIdle skips a session with work in flight. beginRequest runs only under
// Server.mu (see admitPost); endRequest stamps before it decrements so a
// reaper that observes the count back at zero is guaranteed to see the fresh
// timestamp too, and the idle clock then runs from the response rather than
// from the request that preceded it.
func (hs *httpSession) beginRequest() {
	hs.inflight.Add(1)
	hs.touch()
}

func (hs *httpSession) endRequest() {
	hs.touch()
	hs.inflight.Add(-1)
}

func (hs *httpSession) busy() bool {
	return hs.inflight.Load() > 0
}

// New builds the server. It refuses a non-loopback bind without a token.
func New(opts Options) (*Server, error) {
	if opts.Core == nil {
		return nil, fmt.Errorf("httpserve: Core is required")
	}
	if opts.Addr == "" {
		opts.Addr = DefaultAddr
	}
	if opts.Token == "" {
		if !isLoopbackAddr(opts.Addr) {
			return nil, fmt.Errorf(
				"refusing to bind %s without a token: serve-mode tools/call is arbitrary code execution. "+
					"Set --token or MCPMU_SERVE_TOKEN, or bind a loopback address", opts.Addr)
		}
		log.Printf("WARNING: serving MCP on %s without authentication. Set --token or MCPMU_SERVE_TOKEN.", opts.Addr)
	}

	baseCtx, baseCancel := context.WithCancel(context.Background())
	s := &Server{
		opts:       opts,
		core:       opts.Core,
		baseCtx:    baseCtx,
		baseCancel: baseCancel,
		sessions:   make(map[string]*httpSession),
	}

	mcpMux := http.NewServeMux()
	mcpMux.HandleFunc("POST /mcp", s.handlePost)
	mcpMux.HandleFunc("POST /mcp/{namespace}", s.handlePost)
	mcpMux.HandleFunc("GET /mcp", s.handleGet)
	mcpMux.HandleFunc("GET /mcp/{namespace}", s.handleGet)
	mcpMux.HandleFunc("DELETE /mcp", s.handleDelete)
	mcpMux.HandleFunc("DELETE /mcp/{namespace}", s.handleDelete)

	// /healthz is exempt from Origin/auth — readiness probe, serves nothing
	// sensitive. Everything else passes Origin check, then bearer auth.
	outer := http.NewServeMux()
	outer.HandleFunc("GET /healthz", s.handleHealthz)
	outer.Handle("/", s.checkOrigin(s.requireAuth(mcpMux)))

	s.http = &http.Server{
		Addr:    opts.Addr,
		Handler: outer,
		// No WriteTimeout/ReadTimeout: they would kill long tool calls and
		// the GET stream. Per-request deadlines are the tool-timeout's job.
		// IdleTimeout applies only between requests on idle keep-alive
		// connections, never to an active SSE response.
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	go s.janitor()
	return s, nil
}

// ListenAndServe serves until Shutdown or a listener error.
func (s *Server) ListenAndServe() error {
	log.Printf("mcpmu serve listening on http://%s/mcp", s.http.Addr)
	err := s.http.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

// Serve serves on an existing listener (tests).
func (s *Server) Serve(l net.Listener) error {
	err := s.http.Serve(l)
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

// Shutdown drains active requests, then tears down every session. The Core
// is the caller's to close (it may outlive this listener in tests).
func (s *Server) Shutdown(ctx context.Context) error {
	err := s.http.Shutdown(ctx)

	s.mu.Lock()
	s.closed = true
	sessions := make(map[string]*httpSession, len(s.sessions))
	maps.Copy(sessions, s.sessions)
	s.mu.Unlock()

	for id, hs := range sessions {
		s.teardown(id, hs)
	}
	s.baseCancel()
	return err
}

// teardown is the single exit path for a session: DELETE, idle reap, and
// shutdown all funnel here.
func (s *Server) teardown(id string, hs *httpSession) {
	hs.closeOnce.Do(func() {
		hs.cancel()
		hs.sess.Close() // unsubscribes, refcounts, stops private instances
		hs.hub.close()
	})
	s.mu.Lock()
	delete(s.sessions, id)
	s.mu.Unlock()
}

// register stores a new session, refusing after shutdown began or past the
// session cap.
func (s *Server) register(id string, hs *httpSession) (ok bool, full bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false, false
	}
	if len(s.sessions) >= maxSessions {
		return false, true
	}
	s.sessions[id] = hs
	return true, false
}

// lookup returns the session for an Mcp-Session-Id, enforcing that it is
// presented on the namespace route it was minted under.
func (s *Server) lookup(id, routeNamespace string) *httpSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	hs, ok := s.sessions[id]
	if !ok || hs.namespace != routeNamespace {
		return nil
	}
	return hs
}

// admitPost looks the session up and marks it busy in one step under s.mu —
// the same lock reapIdle scans with — so the reaper can never tear a session
// down between a handler finding it and the handler starting work on it.
// Callers must pair a non-nil return with endRequest.
func (s *Server) admitPost(sessionID, routeNamespace string) *httpSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	hs, ok := s.sessions[sessionID]
	if !ok || hs.namespace != routeNamespace {
		return nil
	}
	hs.beginRequest()
	return hs
}

// janitor reaps idle sessions. It is the HTTP replacement for stdio's EOF
// and what makes shared:false (per-session private upstream instances) safe
// here.
func (s *Server) janitor() {
	if s.opts.SessionIdleTimeout <= 0 {
		return
	}
	ticker := time.NewTicker(janitorInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.baseCtx.Done():
			return
		case now := <-ticker.C:
			s.reapIdle(now)
		}
	}
}

func (s *Server) reapIdle(now time.Time) {
	cutoff := now.Add(-s.opts.SessionIdleTimeout).UnixNano()
	s.mu.Lock()
	stale := make(map[string]*httpSession)
	for id, hs := range s.sessions {
		// busy() is checked under the same lock admitPost admits with, so a
		// session is either already busy here (skipped) or cannot become
		// busy after this pass removes it from the table.
		if hs.busy() {
			continue
		}
		if hs.lastActive.Load() < cutoff {
			stale[id] = hs
			delete(s.sessions, id)
		}
	}
	s.mu.Unlock()
	for id, hs := range stale {
		log.Printf("httpserve: reaping idle session %s", id)
		s.teardown(id, hs)
	}
}

// requireAuth gates every request behind the bearer token. Comparison is
// constant-time; failures get WWW-Authenticate per RFC 6750.
func (s *Server) requireAuth(next http.Handler) http.Handler {
	if s.opts.Token == "" {
		return next
	}
	token := []byte(s.opts.Token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const prefix = "Bearer "
		header := r.Header.Get("Authorization")
		if !strings.HasPrefix(header, prefix) ||
			subtle.ConstantTimeCompare([]byte(header[len(prefix):]), token) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// checkOrigin rejects browser cross-origin requests (DNS-rebinding
// protection, spec-mandated). An absent Origin (curl, MCP clients,
// same-machine agents) is allowed — rebinding attacks come from browsers,
// which always send it.
func (s *Server) checkOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && !s.originAllowed(origin) {
			http.Error(w, "origin not allowed", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) originAllowed(origin string) bool {
	for _, allowed := range s.opts.AllowedOrigins {
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
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// isLoopbackAddr reports whether a listen address can only be reached from
// this machine. An empty host (":8081") binds every interface and is not
// loopback.
func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// newSessionID mints an unguessable session ID — 128 bits, hex. Session IDs
// are bearer-ish within an authenticated origin.
func newSessionID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
