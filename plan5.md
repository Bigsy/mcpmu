# Phase 5: Proxies

## Objective
Implement proxy mode that exposes aggregated MCP servers via a single stdio interface, allowing MCP Studio to act as a unified gateway for multiple backend servers.

---

## Status

**Not Started** - Deferred from original Phase 4 to prioritize SSE server support.

---

## Design Overview

### Proxy Mode Concept

MCP Studio can run as a proxy server that:
1. Accepts MCP requests via stdio (like a normal MCP server)
2. Routes tool calls to appropriate backend servers
3. Aggregates tools from all enabled servers
4. Handles namespace-based filtering and permissions

```
┌─────────────────────────────────────────────────────────────┐
│  Claude / LLM Client                                        │
└─────────────────────┬───────────────────────────────────────┘
                      │ stdio (MCP)
                      ▼
┌─────────────────────────────────────────────────────────────┐
│  MCP Studio (Proxy Mode)                                    │
│  - Aggregates tools from all servers                        │
│  - Applies namespace permissions                            │
│  - Routes calls to correct backend                          │
└───────┬─────────────────┬─────────────────┬─────────────────┘
        │ stdio           │ stdio           │ SSE
        ▼                 ▼                 ▼
   ┌─────────┐      ┌─────────┐      ┌─────────────┐
   │ Server1 │      │ Server2 │      │ Remote SSE  │
   │ (local) │      │ (local) │      │ Server      │
   └─────────┘      └─────────┘      └─────────────┘
```

### Use Cases

1. **Claude Desktop Integration** - Run `mcp-studio --stdio` as a single MCP server in Claude Desktop config
2. **Tool Aggregation** - Expose tools from multiple servers under one interface
3. **Permission Enforcement** - Apply namespace-based permissions before forwarding
4. **Unified Logging** - Central logging for all tool calls

---

## Implementation Plan

### 5.1 Stdio Server Mode (`cmd/mcp-studio/stdio.go`)

- [ ] `mcp-studio --stdio` flag to run as MCP server
- [ ] Accept JSON-RPC messages on stdin
- [ ] Write responses to stdout
- [ ] Log to stderr (or file when in proxy mode)

### 5.2 Tool Aggregation

- [ ] Collect `tools/list` from all enabled servers
- [ ] Prefix tool names with server ID to avoid collisions: `serverid.toolname`
- [ ] Handle `tools/list_changed` notifications from backends
- [ ] Re-emit aggregated `tools/list_changed` to client

### 5.3 Request Routing (`internal/server/router.go` enhancements)

- [ ] Parse tool name to extract server ID prefix
- [ ] Forward `tools/call` to correct backend server
- [ ] Handle backend errors and timeouts
- [ ] Return results to client

### 5.4 Permission Integration

- [ ] Apply active namespace's permissions before forwarding
- [ ] Block denied tools with appropriate error
- [ ] Support `--namespace` flag for proxy mode

### 5.5 Lifecycle Management

- [ ] Start backend servers on proxy startup
- [ ] Handle backend server crashes (reconnect or report error)
- [ ] Graceful shutdown of all backends

### 5.6 TUI "Proxies" Tab

- [ ] Enable Tab 3 (currently disabled)
- [ ] Show proxy configurations
- [ ] Create/edit proxy profiles (which servers to include)
- [ ] Start/stop proxy mode from TUI

---

## Config Schema

```go
type ProxyConfig struct {
    ID          string   `json:"id"`
    Name        string   `json:"name"`
    ServerIDs   []string `json:"serverIds"`   // Servers to aggregate
    NamespaceID string   `json:"namespaceId"` // Permissions to apply
    AutoStart   bool     `json:"autostart"`   // Start servers on proxy start
}

type Config struct {
    // ... existing fields
    Proxies []ProxyConfig `json:"proxies"`
}
```

---

## CLI Commands

```bash
# Run as stdio proxy (all enabled servers)
mcp-studio --stdio

# Run with specific namespace
mcp-studio --stdio --namespace production

# Run specific proxy profile
mcp-studio --stdio --proxy my-proxy-profile

# Manage proxy profiles
mcp-studio proxy add <name> --servers s1,s2,s3 --namespace ns1
mcp-studio proxy list
mcp-studio proxy remove <name>
```

---

## Success Criteria

- [ ] `mcp-studio --stdio` works as MCP server
- [ ] Tools from multiple backends are aggregated
- [ ] Tool calls route to correct backend
- [ ] Namespace permissions are enforced
- [ ] Can be used as MCP server in Claude Desktop
- [ ] TUI Proxies tab allows profile management

---

## Dependencies

- Phase 4 (SSE servers) - Proxy should work with both stdio and SSE backends

---

## References

- [MCP Specification](https://spec.modelcontextprotocol.io/)
- Current `internal/server/` router implementation
