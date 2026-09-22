package server

import (
	"context"
	"encoding/json"
	"time"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/mcp"
	"github.com/Bigsy/mcpmu/internal/metrics"
	"github.com/Bigsy/mcpmu/internal/process"
)

// ClientCapabilities implements process.ServerRequestObserver: the
// capabilities declared upstream for an instance about to initialize. Only
// features the server opted in to are declared. Elicitation is declared for
// shared instances too: a request that cannot be tied to one session is
// answered "cancel", which every server must handle.
func (c *Core) ClientCapabilities(id process.InstanceID, srv config.ServerConfig) map[string]any {
	caps := map[string]any{}
	if srv.ElicitationEnabled() {
		caps[featureElicitation] = map[string]any{modeForm: map[string]any{}, modeURL: map[string]any{}}
	}
	if roots := c.rootsDeclaration(id, srv); roots != nil {
		caps[featureRoots] = roots
	}
	return caps
}

// OnServerRequest implements process.ServerRequestObserver. It runs on its
// own goroutine per request and may block until the relayed interaction
// settles; ctx ends when the upstream cancels the request or the instance
// stops.
func (c *Core) OnServerRequest(ctx context.Context, req process.UpstreamRequest) (json.RawMessage, *mcp.RPCError) {
	switch req.Method {
	case "elicitation/create":
		if srv, ok := c.currentConfig().GetServer(req.Instance.Server); ok && srv.ElicitationEnabled() {
			return c.relayElicitation(ctx, req)
		}
	case "roots/list":
		return c.answerRoots(ctx, req)
	}
	return mcp.DefaultServerRequestHandler(ctx, req.ServerRequest)
}

// recordInteraction counts one relayed server-to-client request by outcome.
// Names and outcomes only, never content — the same rule as call metrics.
func (c *Core) recordInteraction(namespace, server, method, outcome string) {
	c.currentRecorder().RecordInteraction(metrics.InteractionSample{
		Time:      time.Now(),
		Namespace: namespace,
		Server:    server,
		Method:    method,
		Outcome:   outcome,
	})
}

var _ process.ServerRequestObserver = (*Core)(nil)
