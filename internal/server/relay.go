package server

import (
	"context"
	"encoding/json"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/mcp"
	"github.com/Bigsy/mcpmu/internal/process"
)

// ClientCapabilities implements process.ServerRequestObserver: the
// capabilities declared upstream for an instance about to initialize.
func (c *Core) ClientCapabilities(process.InstanceID, config.ServerConfig) map[string]any {
	return nil
}

// OnServerRequest implements process.ServerRequestObserver. It runs on its
// own goroutine per request and may block until the relayed interaction
// settles; ctx ends when the upstream cancels the request or the instance
// stops.
func (c *Core) OnServerRequest(ctx context.Context, req process.UpstreamRequest) (json.RawMessage, *mcp.RPCError) {
	return mcp.DefaultServerRequestHandler(ctx, req.ServerRequest)
}

var _ process.ServerRequestObserver = (*Core)(nil)
