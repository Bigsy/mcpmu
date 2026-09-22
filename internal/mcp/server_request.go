package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"runtime/debug"
)

// CallOwner names the downstream request an upstream request is being made on
// behalf of. Serve mode attaches one to the context of every upstream call it
// dispatches for a client (tools/call, resources/read, prompts/get), and
// Client.call registers it against the upstream JSON-RPC id it allocates —
// the only layer that knows that id.
//
// That registration is what lets a server-to-client request be traced back to
// its cause: an HTTP upstream that sends elicitation/create on the response
// stream of a POST names that POST's request (ServerRequest.Origin), and the
// in-flight table tells how many callers an instance is serving right now
// (InFlightOwners). Owners are compared by pointer.
type CallOwner struct {
	// Session is the downstream session ID.
	Session string
	// RequestID is the downstream client's JSON-RPC id for the request.
	RequestID json.RawMessage
	// State is opaque to this package; serve mode keeps its per-call record
	// here so a routed server request can reach the call it belongs to.
	State any
}

type callOwnerKey struct{}

// WithCallOwner returns ctx carrying owner. Every upstream request sent under
// the returned context is registered as owned by it, including a resend after
// HTTP session recovery and a retry on a fresh client.
func WithCallOwner(ctx context.Context, owner *CallOwner) context.Context {
	return context.WithValue(ctx, callOwnerKey{}, owner)
}

// CallOwnerFromContext returns the owner attached by WithCallOwner, or nil.
func CallOwnerFromContext(ctx context.Context) *CallOwner {
	owner, _ := ctx.Value(callOwnerKey{}).(*CallOwner)
	return owner
}

// ServerRequest is one request an upstream server sent to mcpmu
// (elicitation/create, sampling/createMessage, roots/list, ping, ...).
type ServerRequest struct {
	// ID is the server's JSON-RPC id, verbatim.
	ID     json.RawMessage
	Method string
	Params json.RawMessage
	// OriginRequestID is the upstream id of mcpmu's own request whose HTTP
	// response stream carried this message, or 0 when it arrived some other
	// way (stdio, the standalone GET stream). The spec says such a message
	// SHOULD relate to that request, which is strong evidence but not proof.
	OriginRequestID int64
	// Origin is the owner registered for OriginRequestID, when that request
	// is still in flight and was made on a client's behalf.
	Origin *CallOwner
	// InFlight are the distinct owners of this client's requests that were
	// awaiting a response when this request was read. It is taken on the
	// reader goroutine, so it is exact with respect to message order: a call
	// whose response arrived before this request is not in it.
	InFlight []*CallOwner
}

// ServerRequestHandler answers one server-to-client request. It runs on its
// own goroutine, never the transport's reader, so it may block — on a human,
// if it relays the request to a client. ctx is cancelled when the server
// cancels its request (notifications/cancelled) or the client shuts down; in
// both cases nothing is sent back, whatever the handler returns, because the
// spec says the receiver of a cancellation SHOULD NOT answer the request.
type ServerRequestHandler func(ctx context.Context, req ServerRequest) (json.RawMessage, *RPCError)

// DefaultServerRequestHandler is what a client answers with when nothing else
// is installed. `ping` needs no capability, and a server that pings its client
// as a liveness check would otherwise conclude the connection is dead.
// Everything else gets "Method not found" rather than silence: a JSON-RPC
// request that never gets a response leaves the server blocked until its own
// timeout, and an explicit error lets it fail the operation cleanly instead.
func DefaultServerRequestHandler(_ context.Context, req ServerRequest) (json.RawMessage, *RPCError) {
	if req.Method == "ping" {
		return json.RawMessage(`{}`), nil
	}
	if DebugLogging {
		log.Printf("MCP Recv: server->client request %s (id=%s) answered with method not found", req.Method, string(req.ID))
	}
	return nil, &RPCError{
		Code:    rpcCodeMethodNotFound,
		Message: fmt.Sprintf("Method not found: %s (mcpmu does not relay this server-to-client request)", req.Method),
	}
}

// errServerCancelledRequest is the handler context's cause when the server
// withdrew its own request.
var errServerCancelledRequest = errors.New("server cancelled its request")

// errClientClosed is the handler context's cause when the client shut down.
var errClientClosed = errors.New("mcp client closed")

// serverRequestEntry is one server request being answered, addressed by
// pointer so cleanup can tell its own entry from a later request that reused
// the id.
type serverRequestEntry struct {
	cancel context.CancelCauseFunc
}

// SetServerRequestHandler installs the handler for server-to-client requests.
// Pass nil to restore DefaultServerRequestHandler. Install it before
// Initialize: a server may send requests as soon as it has seen
// notifications/initialized.
func (c *Client) SetServerRequestHandler(h ServerRequestHandler) {
	if h == nil {
		c.serverReqHandler.Store(nil)
		return
	}
	c.serverReqHandler.Store(&h)
}

// SetClientCapabilities sets the capabilities object Initialize declares
// upstream. nil (the default) declares none. Only declare what the installed
// server request handler can actually answer.
func (c *Client) SetClientCapabilities(caps map[string]any) {
	if caps == nil {
		c.clientCaps.Store(nil)
		return
	}
	c.clientCaps.Store(&caps)
}

func (c *Client) clientCapabilities() map[string]any {
	if caps := c.clientCaps.Load(); caps != nil {
		return *caps
	}
	return map[string]any{}
}

// InFlightOwners returns the distinct owners of this client's upstream
// requests that are awaiting a response right now. Requests made on nobody's
// behalf (discovery, subscription replay) are not included.
func (c *Client) InFlightOwners() []*CallOwner {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.inFlightOwnersLocked()
}

func (c *Client) inFlightOwnersLocked() []*CallOwner {
	seen := make(map[*CallOwner]struct{}, len(c.owners))
	owners := make([]*CallOwner, 0, len(c.owners))
	for _, owner := range c.owners {
		if _, dup := seen[owner]; dup {
			continue
		}
		seen[owner] = struct{}{}
		owners = append(owners, owner)
	}
	return owners
}

// OwnerOf returns the owner registered for an in-flight upstream request id,
// or nil when the request has finished or was made on nobody's behalf.
func (c *Client) OwnerOf(id int64) *CallOwner {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.owners[id]
}

// handleServerRequest dispatches one server-to-client request to the installed
// handler on a goroutine of its own. This runs on the reader goroutine; the
// handler may block on a human, and the reader must keep delivering responses
// and notifications meanwhile.
func (c *Client) handleServerRequest(id json.RawMessage, method string, params json.RawMessage, origin int64) {
	key := canonicalID(id)
	req := ServerRequest{
		ID:              append(json.RawMessage(nil), id...),
		Method:          method,
		Params:          append(json.RawMessage(nil), params...),
		OriginRequestID: origin,
	}

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	if origin != 0 {
		req.Origin = c.owners[origin]
	}
	req.InFlight = c.inFlightOwnersLocked()
	ctx, cancel := context.WithCancelCause(c.lifetime)
	entry := &serverRequestEntry{cancel: cancel}
	if key != "" {
		c.serverReqs[key] = entry
	}
	c.mu.Unlock()

	handler := DefaultServerRequestHandler
	if h := c.serverReqHandler.Load(); h != nil && *h != nil {
		handler = *h
	}

	go func() {
		defer func() {
			c.mu.Lock()
			if c.serverReqs[key] == entry {
				delete(c.serverReqs, key)
			}
			c.mu.Unlock()
			cancel(context.Canceled)
		}()

		result, rpcErr := runServerRequestHandler(ctx, handler, req)
		if ctx.Err() != nil {
			// The server withdrew the request, or the connection is going
			// away. Either way no reply: the spec says a cancelled request
			// SHOULD NOT be answered, and a closed client has no one to
			// answer.
			if DebugLogging {
				log.Printf("MCP: not answering server request %s (id=%s): %v", method, string(id), context.Cause(ctx))
			}
			return
		}
		c.sendServerReply(req, result, rpcErr)
	}()
}

// runServerRequestHandler calls handler, converting a panic into an internal
// error reply so a bug in one relay cannot take down the process — and cannot
// leave the server waiting on a request nobody will answer.
func runServerRequestHandler(ctx context.Context, handler ServerRequestHandler, req ServerRequest) (result json.RawMessage, rpcErr *RPCError) {
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Printf("PANIC in server request handler %s: %v\n%s", req.Method, recovered, debug.Stack())
			result = nil
			rpcErr = &RPCError{Code: rpcCodeInternalError, Message: fmt.Sprintf("Internal error: handler panicked: %v", recovered)}
		}
	}()
	return handler(ctx, req)
}

// sendServerReply writes the answer to a server request under a deadline of
// its own, because no downstream request is waiting on it.
func (c *Client) sendServerReply(req ServerRequest, result json.RawMessage, rpcErr *RPCError) {
	reply := rpcReply{JSONRPC: "2.0", ID: req.ID}
	switch {
	case rpcErr != nil:
		reply.Error = rpcErr
	case len(result) == 0:
		reply.Result = json.RawMessage(`{}`)
	default:
		reply.Result = result
	}
	data, err := json.Marshal(reply)
	if err != nil {
		if DebugLogging {
			log.Printf("MCP Recv: marshal reply to %s: %v", req.Method, err)
		}
		return
	}

	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), serverReplyTimeout)
	defer cancel()
	// Plain Send, not sendWithSessionRecovery: a reply to a dead HTTP
	// session is worthless, and reinitializing on its account would be a
	// side effect the server never asked for.
	if err := c.transport.Send(ctx, data); err != nil && DebugLogging {
		log.Printf("MCP Send: reply to server request %s not delivered: %v", req.Method, err)
	}
}

// handleServerCancellation acts on notifications/cancelled from the server,
// reporting whether it named a server request being answered. A server may
// only cancel requests it issued, so the id is looked up among those — never
// among mcpmu's own outstanding calls.
func (c *Client) handleServerCancellation(params json.RawMessage) bool {
	var req struct {
		RequestID json.RawMessage `json:"requestId"`
	}
	if err := json.Unmarshal(params, &req); err != nil {
		return false
	}
	key := canonicalID(req.RequestID)
	if key == "" {
		return false
	}
	c.mu.Lock()
	entry := c.serverReqs[key]
	c.mu.Unlock()
	if entry == nil {
		return false
	}
	entry.cancel(errServerCancelledRequest)
	return true
}

// canonicalID normalises a JSON-RPC id for map use: `1` and `"1"` stay
// distinct, whitespace does not matter, and null or absent yields "".
func canonicalID(id json.RawMessage) string {
	if len(id) == 0 {
		return ""
	}
	// UseNumber so a large numeric id is not rounded through float64 into
	// colliding with a neighbour.
	decoder := json.NewDecoder(bytes.NewReader(id))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return string(id)
	}
	if decoded == nil {
		return ""
	}
	canonical, err := json.Marshal(decoded)
	if err != nil {
		return string(id)
	}
	return string(canonical)
}
