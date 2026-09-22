package server

import (
	"context"
	"encoding/json"
	"log"
	"net/url"
	"path"
	"slices"
	"time"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/mcp"
	"github.com/Bigsy/mcpmu/internal/process"
)

// rootsNotifyTimeout bounds one notifications/roots/list_changed write.
const rootsNotifyTimeout = 5 * time.Second

// rootsFallback answers a relayed roots/list that got no answer: an empty
// list is a valid result, and says nothing false about the client.
var rootsFallback = json.RawMessage(`{"roots":[]}`)

// rootsDeclaration is the roots capability declared upstream for an instance,
// or nil. Configured roots are declared for every instance (they are
// server-level, answered by mcpmu); a relay of the owning client's roots only
// for a private instance whose client declared roots itself.
func (c *Core) rootsDeclaration(id process.InstanceID, srv config.ServerConfig) map[string]any {
	if len(srv.Roots) > 0 {
		return map[string]any{modeListChanged: true}
	}
	if !srv.RootsRelayEnabled() || id.IsShared() {
		return nil
	}
	session := c.sessionForID(id.Session)
	if session == nil || !session.clientSupports(featureRoots, "") {
		return nil
	}
	return map[string]any{modeListChanged: session.clientSupports(featureRoots, modeListChanged)}
}

// answerRoots answers roots/list. Roots are per-client state, so relaying the
// request to one of several sessions sharing an instance would be arbitrary;
// a server's configured roots are the one authoritative answer and are given
// locally. Only a private instance that opted in (clientFeatures.roots) with
// no configured roots gets its owning client's roots, relayed.
func (c *Core) answerRoots(ctx context.Context, req process.UpstreamRequest) (json.RawMessage, *mcp.RPCError) {
	srv, ok := c.currentConfig().GetServer(req.Instance.Server)
	if !ok {
		return mcp.DefaultServerRequestHandler(ctx, req.ServerRequest)
	}
	if len(srv.Roots) > 0 {
		return rootsResult(srv.Roots), nil
	}
	if srv.RootsRelayEnabled() && !req.Instance.IsShared() {
		return c.relayRoots(ctx, req, srv)
	}
	return mcp.DefaultServerRequestHandler(ctx, req.ServerRequest)
}

// rootsResult renders configured roots as a roots/list result, naming each by
// its last path element.
func rootsResult(roots []string) json.RawMessage {
	type root struct {
		URI  string `json:"uri"`
		Name string `json:"name,omitempty"`
	}
	out := make([]root, 0, len(roots))
	for _, uri := range roots {
		r := root{URI: uri}
		if u, err := url.Parse(uri); err == nil {
			if name := path.Base(u.Path); name != "/" && name != "." {
				r.Name = name
			}
		}
		out = append(out, r)
	}
	encoded, _ := json.Marshal(struct {
		Roots []root `json:"roots"`
	}{Roots: out})
	return encoded
}

// relayRoots relays roots/list from a private instance to its owning client.
func (c *Core) relayRoots(ctx context.Context, req process.UpstreamRequest, srv config.ServerConfig) (json.RawMessage, *mcp.RPCError) {
	target, _, ok := c.routeServerRequest(req, false)
	if !ok || !target.session.clientSupports(featureRoots, "") {
		return rootsFallback, nil
	}
	result, rpcErr, err := target.interact(ctx, req.Method, nil, c.currentConfig().InteractionTimeout(srv))
	switch {
	case err != nil:
		if ctx.Err() == nil {
			log.Printf("roots/list from %s not answered by its client: %v", req.Instance, err)
		}
		return rootsFallback, nil
	case rpcErr != nil:
		return nil, &mcp.RPCError{Code: rpcErr.Code, Message: rpcErr.Message, Data: rpcErr.Data}
	}
	return result, nil
}

// rootsListEdits returns the servers whose roots list changed between two
// configs while staying non-empty: those instances keep running (the declared
// capability is unchanged) and are told with notifications/roots/list_changed.
// Adding or removing roots entirely is a runtime change that restarts the
// instance instead (see runtimeServerConfig).
func rootsListEdits(old, next *config.Config) []string {
	var edited []string
	for name, srv := range next.Servers {
		prev, ok := old.Servers[name]
		if ok && len(prev.Roots) > 0 && len(srv.Roots) > 0 && !slices.Equal(prev.Roots, srv.Roots) {
			edited = append(edited, name)
		}
	}
	slices.Sort(edited)
	return edited
}

// notifyRootsChanged tells every running instance of the named servers that
// their roots changed.
func (c *Core) notifyRootsChanged(servers []string) {
	for _, name := range servers {
		for _, handle := range c.supervisor.InstancesOf(name) {
			c.sendRootsChanged(handle)
		}
	}
}

// forwardRootsChanged forwards this session's client's
// notifications/roots/list_changed to its private instances that relay the
// client's roots (clientFeatures.roots, no configured roots).
func (s *Session) forwardRootsChanged() {
	cfg := s.currentConfig()
	for name, srv := range cfg.Servers {
		if srv.IsShared() || !srv.RootsRelayEnabled() || len(srv.Roots) > 0 {
			continue
		}
		if handle := s.supervisor.GetInstance(process.PrivateInstanceID(name, s.id)); handle != nil && handle.IsRunning() {
			s.sendRootsChanged(handle)
		}
	}
}

func (c *Core) sendRootsChanged(handle *process.Handle) {
	client := handle.Client()
	if client == nil {
		return
	}
	c.spawn("roots/list_changed to "+handle.InstanceID().String(), func(lifetime context.Context) error {
		ctx, cancel := context.WithTimeout(lifetime, rootsNotifyTimeout)
		defer cancel()
		return client.Notify(ctx, "notifications/roots/list_changed", nil)
	})
}
