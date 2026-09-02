package httpserve

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/Bigsy/mcpmu/internal/server"
)

// resolveRoute validates the URL's namespace segment ("" for the bare /mcp
// route). Unknown namespace → 404, reported by the caller.
func (s *Server) resolveRoute(r *http.Request) (routeNamespace string, ok bool) {
	ns := r.PathValue("namespace")
	if ns != "" && !s.core.HasNamespace(ns) {
		return "", false
	}
	return ns, true
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	// Unauthenticated, so it says nothing about the build: a bare "ok".
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, "ok\n")
}

// handlePost implements the client→server half of the Streamable HTTP
// contract: initialize creates a session, requests get their response on the
// POST that carried them, notifications get 202.
func (s *Server) handlePost(w http.ResponseWriter, r *http.Request) {
	routeNS, ok := s.resolveRoute(r)
	if !ok {
		http.Error(w, "unknown namespace: "+r.PathValue("namespace"), http.StatusNotFound)
		return
	}

	// Content-Type is a security control, not a parsing detail: on a
	// tokenless loopback bind this check (plus Origin) is what stops a
	// browser form from calling tools — HTML forms cannot send
	// application/json and a cross-origin fetch with it triggers a CORS
	// preflight we never approve.
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
		return
	}

	// Bound body-read *time* as well as size. The deadline must be cleared
	// before dispatch: it lives on the connection, and the server's idle
	// background read would trip over it mid tool call otherwise.
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Now().Add(postBodyReadTimeout))
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	_ = rc.SetReadDeadline(time.Time{})
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}

	trimmed := bytes.TrimSpace(body)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		// JSON-RPC batching was removed from the MCP spec in 2025-06-18.
		writeRPCError(w, http.StatusBadRequest, nil,
			server.ErrInvalidRequest("JSON-RPC batch arrays are not supported"))
		return
	}
	msg, parseErr := server.ParseMessage(trimmed)
	if parseErr != nil {
		// Shape errors echo the request id when one was decodable; syntax
		// errors carry id null.
		writeRPCError(w, http.StatusBadRequest, msg.ID, parseErr)
		return
	}

	sessionID := r.Header.Get("Mcp-Session-Id")
	if msg.Method == "initialize" && msg.ID != nil {
		if sessionID != "" {
			// The spec forbids a session header on initialize (and our
			// handleInitialize would answer "already initialized" anyway).
			// Note: mcpmu's own client may also append a legacy ?sessionId=
			// query param to the POST URL — that is invisible to ServeMux
			// path routing and deliberately ignored here; do not "fix" it.
			http.Error(w, "initialize must not carry Mcp-Session-Id", http.StatusBadRequest)
			return
		}
		s.handleInitialize(w, r, routeNS, msg)
		return
	}

	if sessionID == "" {
		http.Error(w, "missing Mcp-Session-Id", http.StatusBadRequest)
		return
	}
	// Admission and idle-reaping are one synchronized transition: admitPost
	// marks the session busy under the same lock the reaper scans with, so a
	// request can never begin dispatch against a session the reaper is about
	// to tear down (a bare lookup would leave a window between returning the
	// session and marking it busy).
	hs := s.admitPost(sessionID, routeNS)
	if hs == nil {
		// Unknown or expired. Spec-compliant clients MUST reinitialize.
		http.Error(w, "unknown or expired session", http.StatusNotFound)
		return
	}
	defer hs.endRequest()
	s.logVersionMismatch(r, hs)

	if msg.IsResponse() {
		// A client's response to a server→client request. mcpmu never issues
		// one, so there is nothing to correlate this with — but it is a
		// response, not a request, so it gets 202 and no body rather than
		// being dispatched as a call to the empty method.
		w.WriteHeader(http.StatusAccepted)
		return
	}

	if msg.ID == nil {
		// Notification: dispatch against the session's own lifetime (an eager
		// start kicked off by notifications/initialized must outlive this
		// POST) and acknowledge.
		hs.sess.Dispatch(hs.ctx, msg)
		w.WriteHeader(http.StatusAccepted)
		return
	}

	// A request. Its context is a child of the session context, additionally
	// cancelled when this POST's context ends — a vanished client stops
	// burning upstream tool-call time.
	dispatchCtx, cancel := context.WithCancel(hs.ctx)
	defer cancel()
	stop := context.AfterFunc(r.Context(), cancel)
	defer stop()

	// Register with the in-flight table so a notifications/cancelled POST
	// naming this id can cancel it.
	callCtx, release := hs.sess.TrackRequest(dispatchCtx, msg.ID)
	defer release()

	resp, hasResponse := hs.sess.Dispatch(callCtx, msg)
	if !hasResponse {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleInitialize creates a Session bound to the route's namespace with an
// sseHub as its writer. On RPC error the reply is still 200 (JSON-RPC error
// object) but the session is not retained.
func (s *Server) handleInitialize(w http.ResponseWriter, r *http.Request, routeNS string, msg server.RPCMessage) {
	namespace := routeNS
	if namespace == "" {
		namespace = s.opts.Namespace
	}

	hub := newSSEHub()
	sessCtx, cancel := context.WithCancel(s.baseCtx)
	// NewSession wraps Stdin in a bufio.Reader — give it an empty reader; it
	// is never read because HTTP sessions never call Run (hot reload and the
	// notification fan-out run at Core scope).
	sess, err := server.NewSession(s.core, server.Options{
		Namespace:          namespace,
		EagerStart:         s.opts.EagerStart,
		ExposeManagerTools: s.opts.ExposeManagerTools,
		ExposeResources:    s.opts.ExposeResources,
		ExposePrompts:      s.opts.ExposePrompts,
		Compression:        s.opts.Compression,
		Stdin:              strings.NewReader(""),
		Stdout:             hub,
		Stderr:             io.Discard,
		ServerName:         "mcpmu",
		ServerVersion:      s.opts.ServerVersion,
	})
	if err != nil {
		cancel()
		http.Error(w, "create session: "+err.Error(), http.StatusInternalServerError)
		return
	}

	resp, _ := sess.Dispatch(sessCtx, msg)
	if resp.Error != nil {
		sess.Close()
		cancel()
		writeJSON(w, http.StatusOK, resp)
		return
	}

	id, err := newSessionID()
	if err != nil {
		sess.Close()
		cancel()
		http.Error(w, "mint session id: "+err.Error(), http.StatusInternalServerError)
		return
	}
	hs := &httpSession{
		sess:      sess,
		hub:       hub,
		namespace: routeNS,
		ctx:       sessCtx,
		cancel:    cancel,
	}
	hs.touch()
	if ok, full := s.register(id, hs); !ok {
		s.teardown(id, hs)
		if full {
			log.Printf("httpserve: refusing initialize, all %d sessions busy", maxSessions)
			http.Error(w, "too many sessions", http.StatusServiceUnavailable)
		} else {
			http.Error(w, "server is shutting down", http.StatusServiceUnavailable)
		}
		return
	}
	w.Header().Set("Mcp-Session-Id", id)
	writeJSON(w, http.StatusOK, resp)
}

// handleGet attaches the request as the session's standalone SSE stream —
// the only channel for server-initiated messages (resources/updated,
// tools/list_changed). A second GET replaces the first. Stream disconnect ≠
// session end.
func (s *Server) handleGet(w http.ResponseWriter, r *http.Request) {
	routeNS, ok := s.resolveRoute(r)
	if !ok {
		http.Error(w, "unknown namespace: "+r.PathValue("namespace"), http.StatusNotFound)
		return
	}
	if !acceptsEventStream(r) {
		http.Error(w, "Accept must include text/event-stream", http.StatusNotAcceptable)
		return
	}
	sessionID := r.Header.Get("Mcp-Session-Id")
	if sessionID == "" {
		http.Error(w, "missing Mcp-Session-Id", http.StatusBadRequest)
		return
	}
	hs := s.lookup(sessionID, routeNS)
	if hs == nil {
		http.Error(w, "unknown or expired session", http.StatusNotFound)
		return
	}

	replaced, drain, ok := hs.hub.attach()
	if !ok {
		http.Error(w, "unknown or expired session", http.StatusNotFound)
		return
	}
	hs.touch()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	_ = rc.Flush()

	// All frame delivery happens on this goroutine. Each write gets a
	// deadline so a genuinely dead peer eventually errors the write —
	// without one, a ping into a black-holed TCP connection "succeeds" into
	// the socket buffer for many minutes.
	writeChunk := func(chunk []byte) bool {
		_ = rc.SetWriteDeadline(time.Now().Add(writeDeadline))
		if _, err := w.Write(chunk); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	flushFrames := func() bool {
		for _, frame := range hs.hub.takeAll(replaced) {
			var buf bytes.Buffer
			buf.WriteString("event: message\n")
			for line := range bytes.Lines(frame.data) {
				buf.WriteString("data: ")
				buf.Write(bytes.TrimRight(line, "\n"))
				buf.WriteByte('\n')
			}
			buf.WriteByte('\n')
			if !writeChunk(buf.Bytes()) {
				return false
			}
		}
		return true
	}

	if !flushFrames() { // drain the backlog accumulated before attach
		hs.hub.detach(replaced)
		return
	}

	ticker := time.NewTicker(keepaliveInterval)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			hs.hub.detach(replaced)
			return
		case <-replaced:
			// A newer GET owns the stream (or the session closed) — exit
			// without detaching.
			return
		case <-hs.ctx.Done():
			return
		case <-drain:
			if !flushFrames() {
				hs.hub.detach(replaced)
				return
			}
		case <-ticker.C:
			// Keepalive success is deliberately not session activity.
			if !writeChunk([]byte(": ping\n\n")) {
				hs.hub.detach(replaced)
				return
			}
		}
	}
}

// handleDelete terminates the session.
func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	routeNS, ok := s.resolveRoute(r)
	if !ok {
		http.Error(w, "unknown namespace: "+r.PathValue("namespace"), http.StatusNotFound)
		return
	}
	sessionID := r.Header.Get("Mcp-Session-Id")
	if sessionID == "" {
		http.Error(w, "missing Mcp-Session-Id", http.StatusBadRequest)
		return
	}
	hs := s.lookup(sessionID, routeNS)
	if hs == nil {
		http.Error(w, "unknown or expired session", http.StatusNotFound)
		return
	}
	s.teardown(sessionID, hs)
	w.WriteHeader(http.StatusNoContent)
}

// logVersionMismatch implements the lenient MCP-Protocol-Version policy:
// accept and log mismatches, default to the negotiated version when absent.
// Strict 400 rejection buys nothing against real clients.
func (s *Server) logVersionMismatch(r *http.Request, hs *httpSession) {
	header := r.Header.Get("MCP-Protocol-Version")
	if header == "" {
		return
	}
	if negotiated := hs.sess.NegotiatedProtocolVersion(); negotiated != "" && header != negotiated {
		log.Printf("httpserve: client sent MCP-Protocol-Version %q, session negotiated %q (accepting)",
			header, negotiated)
	}
}

func acceptsEventStream(r *http.Request) bool {
	for _, accept := range r.Header.Values("Accept") {
		for part := range strings.SplitSeq(accept, ",") {
			media, _, err := mime.ParseMediaType(strings.TrimSpace(part))
			if err != nil {
				continue
			}
			if media == "text/event-stream" || media == "*/*" || media == "text/*" {
				return true
			}
		}
	}
	return false
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		http.Error(w, "marshal response: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// Bound the write like the GET stream's per-write deadline. Into a client
	// that POSTs and never reads, this write "succeeds" into socket buffers
	// for minutes while inflight stays ≥1 and reapIdle skips the session — a
	// wedged goroutine plus one of the 256 slots per occurrence, permanent at
	// the cap. The response is already computed, so a bounded deadline cannot
	// truncate a legitimate slow tool call.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(writeDeadline))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(data)
}

// writeRPCError replies with a JSON-RPC error object body (null id when the
// request's id could not be parsed).
func writeRPCError(w http.ResponseWriter, status int, id json.RawMessage, rpcErr *server.RPCError) {
	writeJSON(w, status, server.RPCResponse{JSONRPC: "2.0", ID: id, Error: rpcErr})
}
