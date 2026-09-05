package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/Bigsy/mcpmu/internal/config"
)

// compressionLevel resolves the effective compression level for this session
// right now. An explicit --compress flag wins in both directions: a level
// forces compression on, `--compress off` forces it off. Otherwise the active
// namespace's configured "compression" applies. Resolved per request, not at
// session construction, because a hot reload can change both the active
// namespace and its configured level.
func (s *Session) compressionLevel() config.CompressionLevel {
	s.mu.RLock()
	activeNamespaceName := s.activeNamespaceName
	cfg := s.currentConfig()
	s.mu.RUnlock()
	ns, ok := cfg.GetNamespace(activeNamespaceName)
	if !ok {
		return s.opts.Compression.Resolve("")
	}
	return s.opts.Compression.Resolve(ns.Compression)
}

// handleToolsList handles the tools/list request.
func (s *Session) handleToolsList(ctx context.Context) (any, *RPCError) {
	s.mu.RLock()
	if !s.initialized {
		s.mu.RUnlock()
		return nil, ErrInvalidRequest("not initialized")
	}
	s.mu.RUnlock()

	// Resolve the level once so the branch and the listing agree even if a
	// reload lands mid-request.
	compression := s.compressionLevel()

	// The listing runs under the session lifetime, not the request: tools/list
	// is handled synchronously on the main loop, and shutdown must cut the
	// discovery grace period short while a client cancelling its own tools/list
	// must not abort a server start other requests are waiting on.
	tools := s.visibleTools(s.lifetime)

	// Compressed surface: the client sees only the wrapper tools; the full
	// listing rides inside invoke_tool's description (and list_tools). Manager
	// tools stay real — they are already opt-in and tiny.
	if compression.Enabled() {
		wrappers := wrapperTools(formatListing(compression, tools))
		if s.opts.ExposeManagerTools {
			wrappers = append(wrappers, s.currentAggregator().ManagerTools()...)
		}
		return toolsListResult{Tools: wrappers}, nil
	}

	if s.opts.ExposeManagerTools {
		tools = append(tools, s.currentAggregator().ManagerTools()...)
	}
	return toolsListResult{Tools: tools}, nil
}

// visibleTools returns this session's permission-filtered upstream tools in
// stable order (namespace server order, then upstream order), waiting up to
// the grace period for discovery and continuing stragglers in the background.
// Manager tools are not included — exposure is the caller's choice. ctx bounds
// the grace period: handleToolsList passes the session lifetime, the async
// list_tools wrapper the request context (like any other tools/call).
func (s *Session) visibleTools(ctx context.Context) []AggregatedTool {
	s.mu.RLock()
	activeNamespaceName := s.activeNamespaceName
	activeServerNames := s.activeServerNames
	cfg := s.currentConfig()
	s.mu.RUnlock()

	// Discover tools with a grace period. ListTools starts servers
	// concurrently and returns whatever succeeds within the deadline.
	// Already-running servers with tools return instantly.
	gracePeriod := s.listToolsGracePeriod
	if gracePeriod == 0 {
		gracePeriod = ListToolsGracePeriod
	}
	graceCtx, cancel := context.WithTimeout(ctx, gracePeriod)
	defer cancel()
	tools := s.listTools(graceCtx, activeServerNames)

	// If any servers didn't finish in time, continue in the background.
	// Pass the caller's snapshot of activeServerNames so the goroutine
	// doesn't re-read state that a concurrent reload could change.
	stillPending := s.pendingServers(activeServerNames)
	if len(stillPending) > 0 && s.bgDiscovering.CompareAndSwap(false, true) {
		s.spawn("background discovery", func(ctx context.Context) error {
			s.discoverAndNotify(ctx, stillPending)
			return nil
		})
	}

	// Filter tools based on permissions (always runs — IsToolAllowed handles
	// global deny even without a namespace, and returns true for everything
	// else when namespace is empty)
	filtered := make([]AggregatedTool, 0, len(tools))
	for _, tool := range tools {
		serverName, toolName, isManager := ParseToolName(tool.Name)
		// Manager tools are always shown
		if isManager {
			filtered = append(filtered, tool)
			continue
		}
		// Check permission for regular tools
		allowed, _ := IsToolAllowed(cfg, activeNamespaceName, serverName, toolName)
		if allowed {
			filtered = append(filtered, tool)
		}
	}
	return filtered
}

func (s *Session) splitServersBySharing(serverNames []string) (shared, private []string) {
	for _, name := range serverNames {
		if s.instanceID(name).IsShared() {
			shared = append(shared, name)
		} else {
			private = append(private, name)
		}
	}
	return shared, private
}

func (s *Session) listTools(ctx context.Context, serverNames []string) []AggregatedTool {
	shared, private := s.splitServersBySharing(serverNames)
	var sharedTools, privateTools []AggregatedTool
	var wg sync.WaitGroup
	if len(shared) > 0 {
		wg.Go(func() { sharedTools, _ = s.currentAggregator().ListTools(ctx, shared) })
	}
	if len(private) > 0 {
		wg.Go(func() { privateTools, _ = s.privateAggregatorSnapshot().ListTools(ctx, private) })
	}
	wg.Wait()

	byServer := make(map[string][]AggregatedTool, len(serverNames))
	for _, tool := range append(sharedTools, privateTools...) {
		byServer[tool.serverName] = append(byServer[tool.serverName], tool)
	}
	result := make([]AggregatedTool, 0, len(sharedTools)+len(privateTools))
	for _, name := range serverNames {
		result = append(result, byServer[name]...)
	}
	return result
}

// callableServer applies the checks every tool-addressed request performs
// before touching an upstream: the server is in the active namespace, exists
// in the current config, and is enabled. cfg and activeServerNames are the
// caller's snapshots, so a caller that goes on to use the same cfg (see
// resolveToolSchema, which feeds it to IsToolAllowed) cannot have a
// concurrent reload disagree with the check it already passed.
//
// Router.CallTool and getOrStartServer deliberately keep their own lookups:
// they run after permission checks or on a different entry path (manager
// tools, lazy start from resources/prompts), and getOrStartServer re-checks
// because config can reload between this check and the start.
//
// A plain function, not a Session method: everything it needs is passed in,
// which is what makes the snapshot guarantee above hold.
func callableServer(cfg *config.Config, activeServerNames []string, serverName string) *RPCError {
	if !slices.Contains(activeServerNames, serverName) {
		return ErrServerNotFound(serverName)
	}
	srv, ok := cfg.GetServer(serverName)
	if !ok {
		return ErrServerNotFound(serverName)
	}
	if !srv.IsEnabled() {
		return NewRPCError(ErrCodeServerNotRunning, "server is disabled: "+serverName, nil)
	}
	return nil
}

// handleToolsCall handles the tools/call request.
func (s *Session) handleToolsCall(ctx context.Context, params json.RawMessage) (any, *RPCError) {
	s.mu.RLock()
	if !s.initialized {
		s.mu.RUnlock()
		return nil, ErrInvalidRequest("not initialized")
	}
	activeServerNames := s.activeServerNames
	router := s.router
	s.mu.RUnlock()

	var req toolsCallRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, ErrInvalidParams(err.Error())
	}

	// Compressed surface: intercept the wrapper tools before ParseToolName.
	// Resolved once per request so the intercept and the listing a wrapper
	// renders agree.
	compression := s.compressionLevel()
	if compression.Enabled() {
		switch req.Name {
		case wrapperListTools:
			return s.handleListToolsWrapper(ctx, router, compression)
		case wrapperGetToolSchema:
			return s.handleGetToolSchemaWrapper(ctx, router, req.Arguments)
		case wrapperInvokeTool:
			var args invokeToolArgs
			if len(req.Arguments) > 0 {
				if err := json.Unmarshal(req.Arguments, &args); err != nil {
					return nil, ErrInvalidParams(wrapperInvokeTool + ": " + err.Error())
				}
			}
			if args.Tool == "" {
				return nil, ErrInvalidParams(wrapperInvokeTool + `: "tool" is required (qualified name like "server.tool_name")`)
			}
			// The client can no longer schema-validate before sending, so
			// catch a non-object input here with an error the model can act
			// on, instead of forwarding it into an opaque upstream failure.
			if input := bytes.TrimSpace(args.Input); len(input) > 0 &&
				input[0] != '{' && !bytes.Equal(input, []byte("null")) {
				return nil, ErrInvalidParams(wrapperInvokeTool + `: "input" must be a JSON object holding the tool's arguments`)
			}
			// Rewrite to the target and fall through: namespace enforcement,
			// permission checks, the retry path, and metrics recording all run
			// on the target tool with zero changes.
			req.Name = args.Tool
			req.Arguments = args.Input
		}
	}

	// Parse tool name to check namespace enforcement
	serverName, _, isManager := ParseToolName(req.Name)

	// A dotless non-manager name can never route — aggregated tools are
	// always qualified as "server.tool" — and letting it fall through used to
	// produce `Server not found: ""`, useless to a model reading the error.
	// Name the actual problem instead (matching resolveToolSchema's
	// deliberate divergence). A wrapper name reaching this point means
	// compression is off — typically a client cache made stale by a reload
	// that disabled it — so that error additionally says how to recover.
	if !isManager && serverName == "" {
		msg := fmt.Sprintf("Tool not found: %s (tool names are qualified as \"server.tool\")", req.Name)
		switch req.Name {
		case wrapperListTools, wrapperGetToolSchema, wrapperInvokeTool:
			msg = fmt.Sprintf("Tool not found: %s (compression is off on this session; call tools/list for the available tools)", req.Name)
		}
		return nil, NewRPCError(ErrCodeToolNotFound, msg, map[string]string{"toolName": req.Name})
	}

	// Manager tools are always allowed
	if !isManager {
		if rpcErr := callableServer(s.currentConfig(), activeServerNames, serverName); rpcErr != nil {
			return nil, rpcErr
		}
	}

	// Forward the client's `_meta` upstream, substituting a unique token for
	// any progressToken it carried. Without this the server is never asked for
	// progress at all, because the whole `_meta` object used to be dropped
	// here at unmarshal time.
	meta, releaseProgress := s.rewriteRequestMeta(req.Meta)
	defer releaseProgress()

	// Route the call through the router
	result, rpcErr := router.CallTool(ctx, req.Name, req.Arguments, meta)
	if rpcErr != nil {
		return nil, rpcErr
	}

	return result, nil
}

// handleListToolsWrapper serves the list_tools wrapper: the compact listing as
// a text result, rebuilt per call from the current config and catalog. router
// and level are the caller's snapshots — a reload can swap s.router and change
// the effective compression concurrently, so neither is re-read here.
func (s *Session) handleListToolsWrapper(ctx context.Context, router *Router, level config.CompressionLevel) (any, *RPCError) {
	start := time.Now()
	listing := formatListing(level, s.visibleTools(ctx))
	router.recordMeta(wrapperListTools, start, nil)
	return textResult(listing), nil
}

// handleGetToolSchemaWrapper serves the get_tool_schema wrapper. A single
// "tool" returns the full AggregatedTool, or the same RPC error the direct
// tools/call path would produce. A "tools" array returns one entry per name,
// reporting unknown or denied names per entry without failing the call.
// router is the caller's snapshot (see handleListToolsWrapper).
func (s *Session) handleGetToolSchemaWrapper(ctx context.Context, router *Router, arguments json.RawMessage) (any, *RPCError) {
	start := time.Now()
	fail := func(rpcErr *RPCError) (any, *RPCError) {
		router.recordMeta(wrapperGetToolSchema, start, rpcErr)
		return nil, rpcErr
	}
	var args getToolSchemaArgs
	if len(arguments) > 0 {
		if err := json.Unmarshal(arguments, &args); err != nil {
			return fail(ErrInvalidParams(wrapperGetToolSchema + ": " + err.Error()))
		}
	}
	if (args.Tool != "") == (len(args.Tools) > 0) {
		return fail(ErrInvalidParams(wrapperGetToolSchema + `: pass exactly one of "tool" or "tools"`))
	}
	if args.Tool != "" {
		tool, rpcErr := s.resolveToolSchema(ctx, args.Tool)
		if rpcErr != nil {
			return fail(rpcErr)
		}
		result, rpcErr := schemaResult(tool)
		if rpcErr != nil {
			return fail(rpcErr)
		}
		router.recordMeta(wrapperGetToolSchema, start, nil)
		return result, nil
	}
	type schemaError struct {
		Tool  string    `json:"tool"`
		Error *RPCError `json:"error"`
	}
	entries := make([]any, 0, len(args.Tools))
	for _, name := range args.Tools {
		tool, rpcErr := s.resolveToolSchema(ctx, name)
		if rpcErr != nil {
			entries = append(entries, schemaError{Tool: name, Error: rpcErr})
			continue
		}
		entries = append(entries, tool)
	}
	result, rpcErr := schemaResult(struct {
		Tools []any `json:"tools"`
	}{Tools: entries})
	if rpcErr != nil {
		return fail(rpcErr)
	}
	router.recordMeta(wrapperGetToolSchema, start, nil)
	return result, nil
}

// schemaResult renders v as an indented JSON text block plus structuredContent.
func schemaResult(v any) (any, *RPCError) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, ErrInternalError("marshal tool schema: " + err.Error())
	}
	result := textResult(string(data))
	result.StructuredContent = data
	return result, nil
}

// resolveToolSchema looks up one qualified tool for get_tool_schema, applying
// the same namespace, enabled, and permission checks the direct tools/call
// path performs — denied tools are refused so the model never learns about
// tools it cannot call. A server not discovered yet (lazy start, straggler)
// is discovered under a timeout instead of returning not-found — the
// compressed analogue of lazy startup.
func (s *Session) resolveToolSchema(ctx context.Context, qualifiedName string) (AggregatedTool, *RPCError) {
	serverName, toolName, isManager := ParseToolName(qualifiedName)
	if isManager || serverName == "" {
		// Deliberate divergence from the direct path, which answers a dotless
		// name with ErrServerNotFound("") — tool-not-found names the actual
		// problem for a model reading the error. Manager tools are excluded by
		// design: they stay real tools with full schemas in tools/list.
		return AggregatedTool{}, ErrToolNotFound(qualifiedName)
	}
	s.mu.RLock()
	activeNamespaceName := s.activeNamespaceName
	activeServerNames := s.activeServerNames
	s.mu.RUnlock()
	cfg := s.currentConfig()
	if rpcErr := callableServer(cfg, activeServerNames, serverName); rpcErr != nil {
		return AggregatedTool{}, rpcErr
	}
	if allowed, reason := IsToolAllowed(cfg, activeNamespaceName, serverName, toolName); !allowed {
		return AggregatedTool{}, ErrToolDenied(qualifiedName, reason)
	}
	agg := s.aggregatorForServer(serverName)
	if tool, ok := agg.GetTool(qualifiedName); ok {
		return tool, nil
	}
	// Discovery runs under the request context, exactly like the direct
	// tools/call lazy-start path — the client can cancel its own call.
	discoverCtx, cancel := context.WithTimeout(ctx, DefaultToolDiscoveryTimeout)
	defer cancel()
	if _, err := agg.DiscoverServer(discoverCtx, serverName); err != nil {
		return AggregatedTool{}, ErrServerFailedToStart(serverName, err.Error())
	}
	if tool, ok := agg.GetTool(qualifiedName); ok {
		return tool, nil
	}
	return AggregatedTool{}, ErrToolNotFound(qualifiedName)
}
