# Phase 4: HTTP Proxies (DEFERRED)

> **Status: DEFERRED** - This phase is preserved for future implementation. The stdio-only approach (Phase 1.5) covers the primary use case of local Claude Code/Codex workflows. HTTP proxy adds value for web clients, remote access, and shared team servers - implement when those needs arise.

## When to Revisit This Phase
- Need to expose MCP tools to web browser clients
- Need remote access from another machine
- Need a shared team server with multiple concurrent clients
- Need enterprise features (auth, audit, rate limits)

---

## Objective
Implement the HTTP proxy layer that exposes MCP servers to external clients. Support multiple transports (SSE and Streamable-HTTP), namespace binding, and dynamic port assignment.

## Features

### Proxy CRUD
- [ ] Create proxy form:
  - ID (auto-generated or user-provided)
  - Name (display name)
  - Path segment (URL path component)
  - Host (default: localhost)
  - Port (0 for auto-assign)
  - Transport type (SSE or Streamable-HTTP)
- [ ] Edit proxy
- [ ] Delete proxy (with running check)
- [ ] Proxy list view (new tab)

### HTTP Server Infrastructure
- [ ] Base HTTP server using `net/http`
- [ ] Configurable host/port binding
- [ ] Auto-port assignment (port=0 → OS assigns)
- [ ] Graceful shutdown
- [ ] CORS headers: permissive (`*`), allow `mcp-session-id`

### SSE Transport
- [ ] `GET /mcp/{path}` - SSE event stream
- [ ] `POST /mcp/{path}/message` - JSON-RPC messages
- [ ] Session management via query param or `mcp-session-id` header
- [ ] Session lifecycle: create on GET, destroy on disconnect/timeout
- [ ] Keep-alive pings

### Streamable-HTTP Transport
- [ ] `POST /mcp/{path}` - JSON-RPC messages (request/response)
- [ ] `DELETE /mcp/{path}` - Session cleanup
- [ ] `mcp-session-id` header in responses
- [ ] Stateless message handling with optional session context

### Proxy Lifecycle
- [ ] Start proxy (bind HTTP server)
- [ ] Stop proxy (graceful shutdown, close sessions)
- [ ] Proxy status tracking (running/stopped)
- [ ] Port collision handling

### Tool Aggregation
- [ ] Aggregate tools from all namespaces bound to proxy
- [ ] Apply namespace tool permissions (filter disabled tools)
- [ ] Prefix tool descriptions with server name: `[ServerName] Original description`
- [ ] **Tool identity**: tools keyed by `(serverId, toolName)` to avoid collisions
- [ ] **Collision handling**: if two servers expose same tool name, use first-match or error (configurable)
- [ ] Handle `list_tools` MCP request
- [ ] Route `call_tool` to correct upstream server based on tool identity

### Namespace Binding
- [ ] Assign namespaces to proxy (multi-select)
- [ ] Proxy exposes tools from bound namespaces only
- [ ] Live update when namespace membership changes
- [ ] "Manage Namespaces" action from proxy detail
- [ ] **Optional**: Direct server binding (bypass namespace) for early testing

### Proxy Detail View
- [ ] Show proxy metadata
- [ ] Display bound URL (copy to clipboard)
- [ ] List bound namespaces
- [ ] Show upstream count and total tools
- [ ] Session count (if tracking)

### Proxy Analytics
- [ ] Upstream server count
- [ ] Total exposed tools count
- [ ] Active session count (for SSE)

### TUI Enhancements
- [ ] Proxies tab
- [ ] Proxy status indicators (green/gray)
- [ ] Keyboard shortcuts:
  - Enter: view details
  - a: add proxy
  - e: edit proxy
  - d: delete proxy
  - s: start proxy
  - x: stop proxy
  - c: copy URL to clipboard
  - n: manage namespaces
- [ ] Status bar: "X/Y proxies running, N tools exposed"

### Proxy Autostart
- [ ] Persist running state to config
- [ ] Restore running proxies on app launch
- [ ] Handle startup failures gracefully

## Dependencies
- Phase 3: Namespaces, tool permissions
- See [PLAN-ui.md](PLAN-ui.md) for proxy list and detail view specs

## Unknowns / Questions
1. **Session Storage**: In-memory only or persist across restarts? (Design: in-memory)
2. **Concurrent Sessions**: Max sessions per proxy? Memory limits?
3. **Tool Routing**: How to handle tool name collisions across servers in same namespace?
4. **Streamable-HTTP Spec**: Verify exact spec compliance for session handling

## Risks
1. **HTTP Server Stability**: Long-running SSE connections need proper cleanup. Connection leaks are easy to introduce.
2. **Tool Name Collisions**: If two servers expose same tool name, routing is ambiguous. Need clear collision handling (first-match or error).
3. **Session Memory**: Many concurrent sessions could exhaust memory. Need limits and eviction.
4. **Port Conflicts**: Auto-assigned ports may conflict on restart. Need retry logic.
5. **Spec/Interop Drift**: SSE/Streamable-HTTP details (framing, timeouts, error semantics) can differ by client. Isolate behind transport interface.
6. **Security Footguns**: Aggregation can accidentally widen access. "Least privilege" harder with combined namespaces.

## Success Criteria
- Can create/edit/delete proxies
- Proxy starts/stops reliably
- SSE transport works with MCP clients
- Streamable-HTTP transport works
- Tool aggregation respects permissions
- URL copy-to-clipboard works
- Autostart restores proxy state

## Files to Create/Modify
```
internal/
  config/
    config.go       # Add proxy config
  proxy/
    proxy.go        # Proxy manager
    server.go       # HTTP server
    sse.go          # SSE transport handler
    streamable.go   # Streamable-HTTP handler
    session.go      # Session management
    aggregator.go   # Tool aggregation
    cors.go         # CORS middleware
  tui/
    model.go        # Add proxies tab
    proxy_list.go   # Proxy list view
    proxy_form.go   # Proxy form
    proxy_detail.go # Proxy detail view
    namespace_picker.go # Multi-select for namespaces
```

## Estimated Complexity
- Proxy CRUD: Low
- HTTP server: Medium
- SSE transport: High
- Streamable-HTTP: Medium-High
- Tool aggregation: Medium
- TUI components: Medium
- Total: ~2500-3500 lines of Go code (cumulative ~6800-9200)
