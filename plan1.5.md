# Phase 1.5: stdio Server Mode

## Objective
Enable mcp-studio to be spawned by Claude Code/Codex as an MCP server via `--stdio` mode. This eliminates the need for auto-start daemons - the client simply spawns mcp-studio on-demand. This phase also establishes stdio mode as the **primary integration testing vehicle** for the entire project.

> **This is THE primary way to use mcp-studio.** HTTP proxy mode (Phase 4) is deferred - stdio covers 90%+ of local Claude Code/Codex workflows. Different configs can be used for different contexts (work, personal, project-specific).

---

## Design Decisions (Locked In)

### 1. Single Binary, Multiple Entrypoints
**Decision**: One binary with subcommands, not separate binaries.
```bash
mcp-studio serve --stdio   # MCP server mode (spawned by Claude Code)
mcp-studio tui             # Interactive TUI (Phase 2+)
mcp-studio status          # Quick CLI status check
```
- Simplifies distribution (one binary to install)
- Core logic shared between serve and TUI modes
- TUI code isolated in its own package, never imported by stdio mode
- HTTP mode (`--http`) can be added later if needed (Phase 4, deferred)

### 2. Namespace Selection (Toolset Selection)
**Decision**: stdio mode exposes exactly one toolset per process, selected by namespace at startup.

Selection rules:
- If `--namespace <id>` is provided: use it (error if unknown)
- Else if `config.defaultNamespaceId` is set: use it (error if unknown)
- Else if there is exactly 1 namespace configured: use it
- Else if there are 0 namespaces configured: expose all servers (backward compatible)
- Else (2+ namespaces, none selected): fail `initialize` with an actionable error

Phase interaction:
- Phase 1.5 implements selection + filtering by server membership
- Phase 3 adds tool permission enforcement within the active namespace

Rationale:
- Stdio MCP servers are naturally single-tenant per spawned process
- "Work vs personal" maps to different MCP server entries with different `--namespace` values
- This avoids accidentally exposing “all tools” when multiple toolsets exist

### 3. Tool Namespacing Strategy
**Decision**: Aggregated tools with `serverId.toolName` format.
```
filesystem.read_file
filesystem.write_file
github.create_issue
github.list_repos
```
- Claude sees one flat tool list from mcp-studio
- mcp-studio routes calls to the correct upstream server
- Collision-free: server ID guarantees uniqueness
- Human-readable: easy to understand tool provenance

**Server ID constraints:**
- `serverId` is a stable internal identifier, not the display name
- Auto-generate a short 4-char `[a-z0-9]` id by default; regenerate on collision
- Disallow `.` in `serverId` (tool name parsing safety)

### 4. Lazy Server Start
**Decision**: Servers start on first tool call, not on mcp-studio init.
- Faster startup: mcp-studio initializes instantly
- Resource efficient: only running servers that are actually used
- Config flag `eager: true` available for servers that should pre-start
- Failed lazy-start returns MCP error to client (doesn't crash mcp-studio)
Note: `tools/list` triggers tool discovery and may start servers as needed (still not on `initialize`).
- Use bounded concurrency (e.g. 4 servers at a time) and per-server timeouts (e.g. 3–5s) so the client sees tools quickly
- Cache discovered tools in-memory for the stdio session; refresh on reconnect/restart

### 5. Output Discipline
**Decision**: Strict stdout/stderr separation.
- **stdout**: MCP JSON-RPC protocol only (Content-Length framed)
- **stderr**: All logs, debug output, progress messages
- Never mix - any stdout pollution breaks the protocol
- Log level configurable via `--log-level` flag or env var

### 6. Graceful Degradation
**Decision**: Partial failures don't break the whole server.
- If one managed server fails to start, others still work
- `tools/list` returns tools from healthy servers only (within the active namespace/toolset)
- `tools/call` to failed server returns structured MCP error
- Status of all servers available via manager tools

### 7. Manager Tools (Meta-Tools)
**Decision**: Expose server management as MCP tools themselves.
```
mcp-studio.servers_list      # List configured servers and their status
mcp-studio.servers_start     # Start a specific server
mcp-studio.servers_stop      # Stop a specific server
mcp-studio.servers_restart   # Restart a specific server
mcp-studio.server_logs       # Get recent logs from a server
mcp-studio.namespaces_list   # List namespaces and show the active namespace
```
- Claude can manage servers without TUI
- Enables autonomous workflows (Claude starts servers it needs)
- Prefixed with `mcp-studio.` to avoid collision with managed tools

---

## Features

### CLI Entrypoint
- [ ] Cobra CLI with `serve` subcommand
- [ ] `--stdio` flag for stdio transport (default for `serve`)
- [ ] `--config` flag to specify config file path
- [ ] `--namespace` flag to select active namespace/toolset
- [ ] Server entries in config remain **`mcpServers`-compatible** (`command`, `args`, `cwd`, `env`) so users can edit manually or copy/paste from MCP client configs
- [ ] `--log-level` flag (debug, info, warn, error)
- [ ] `--eager` flag to pre-start all servers on init
- [ ] Version flag (`--version`)
- [ ] Clean shutdown on SIGINT/SIGTERM
- [ ] Ensure Cobra never writes help/usage to stdout in `--stdio` mode (stdout is protocol-only)

### MCP Server Protocol (stdio)
- [ ] Content-Length framed JSON-RPC on stdout
- [ ] Accept both Content-Length and NDJSON on stdin (liberal reader)
- [ ] `initialize` request/response with capabilities
- [ ] `initialized` notification handling
- [ ] `tools/list` returns aggregated tools from servers in the active namespace (or all servers if no namespaces configured)
- [ ] `tools/call` routes to correct upstream server
- [ ] `ping` for keepalive
- [ ] Proper error responses (JSON-RPC error codes)

### Server Capabilities Declaration
- [ ] Declare `tools` capability
- [ ] Server info: name="mcp-studio", version from build
- [ ] Protocol version negotiation

### Tool Aggregation
- [ ] On init: load config, select active namespace, discover servers assigned to it
- [ ] Tool naming: `{serverId}.{toolName}`
- [ ] Tool description: `[{serverName}] {originalDescription}`
- [ ] Maintain tool→server routing map
- [ ] Handle tool schema passthrough (inputSchema)
- [ ] Refresh tools when server reconnects

### Tool Call Routing
- [ ] Parse qualified tool name to extract serverId
- [ ] Lazy-start server if not running (unless `eager: false, lazy: false`)
- [ ] Forward call to upstream server's MCP client
- [ ] Timeout propagation (use client's timeout or default 30s)
- [ ] Cancellation propagation (if client sends cancel)
- [ ] Return upstream response or structured error

### Manager Tools
- [ ] `mcp-studio.servers_list` - returns JSON array of server status
- [ ] `mcp-studio.servers_start` - start server by ID, returns success/error
- [ ] `mcp-studio.servers_stop` - stop server by ID, returns success/error
- [ ] `mcp-studio.servers_restart` - restart server by ID
- [ ] `mcp-studio.server_logs` - returns last N log lines for server
- [ ] `mcp-studio.config_reload` - reload config without restart
- [ ] `mcp-studio.namespaces_list` - list namespaces and indicate active namespace

### Error Handling
- [ ] Structured MCP errors with codes:
  - `-32600` Invalid Request
  - `-32601` Method not found
  - `-32602` Invalid params
  - `-32603` Internal error
  - Custom: `-32000` Server not found
  - Custom: `-32001` Server failed to start
  - Custom: `-32002` Tool call timeout
  - Custom: `-32003` Server not running
- [ ] Error details include server ID and upstream error when relevant
- [ ] Never crash on upstream failures - always return error response

### Logging
- [ ] Structured logging to stderr (JSON or human-readable)
- [ ] Log levels: debug, info, warn, error
- [ ] Request/response logging at debug level
- [ ] Server lifecycle events at info level
- [ ] Errors include context (server ID, tool name, etc.)

### Shutdown Handling
- [ ] Graceful shutdown on SIGINT/SIGTERM
- [ ] Stop all managed servers (SIGTERM → timeout → SIGKILL)
- [ ] Drain in-flight requests (short timeout)
- [ ] Exit code 0 on clean shutdown, non-zero on error

---

## Testing Strategy

This phase establishes the **primary testing infrastructure** for the project.

### Unit Tests (internal packages)
- [ ] Config parsing and validation
- [ ] Tool name qualification/parsing
- [ ] Error code mapping
- [ ] Supervisor state machine

### Protocol Tests (MCP compliance)
- [ ] Spawn `mcp-studio serve --stdio` as subprocess
- [ ] Send MCP requests via stdin, read responses from stdout
- [ ] Test `initialize` handshake
- [ ] Test `tools/list` response schema
- [ ] Test `tools/call` routing
- [ ] Test error responses for invalid requests
- [ ] Test graceful handling of malformed input

### Integration Tests (with fake servers)
- [ ] Create minimal fake MCP server (Go test binary or in-process)
- [ ] Test tool discovery from fake server
- [ ] Test tool call forwarding
- [ ] Test lazy server start
- [ ] Test server failure handling
- [ ] Test timeout propagation
- [ ] Test concurrent tool calls

### Manual Testing
- [ ] Configure in Claude Code's `mcp_servers.json`
- [ ] Verify tools appear in Claude's tool list
- [ ] Verify tool calls work end-to-end
- [ ] Test with real MCP servers (filesystem, GitHub, etc.)

### Test Fixtures
- [ ] Sample config files for tests
- [ ] Fake MCP server binary (minimal, for integration tests)
- [ ] MCP request/response fixtures (JSON files)

---

## Claude Code Integration

### Configuration Example
```json
// ~/.claude/mcp_servers.json
{
  "mcp-studio": {
    "command": "/usr/local/bin/mcp-studio",
    "args": ["serve", "--stdio", "--config", "~/.config/mcp-studio/config.json", "--namespace", "work"]
  }
}
```

### What Claude Sees
```
Available tools:
- filesystem.read_file: [Filesystem] Read contents of a file
- filesystem.write_file: [Filesystem] Write contents to a file
- github.create_issue: [GitHub] Create a new issue
- mcp-studio.servers_list: List all configured MCP servers
- mcp-studio.servers_start: Start a specific server
...
```

---

## Dependencies
- Phase 1: Config schema, MCP client, process supervisor, event bus
- Reuses: `StdioTransport` (framing layer), `McpClient` interface, `Supervisor`

## Unknowns / Questions
1. **Tool Refresh**: When should we re-fetch tools from managed servers? On reconnect? Periodically? On-demand via manager tool?
2. **Concurrent Calls**: How many concurrent tool calls per server? Queue or reject excess?
3. **Config Hot-Reload**: Should config changes trigger automatic server restarts? Or require explicit reload?
4. **Startup Timeout**: How long to wait for lazy server start before returning error?

## Risks
1. **Protocol Compliance**: MCP spec nuances may cause interop issues with Claude Code. Need thorough protocol testing.
2. **Stdout Pollution**: Any accidental stdout writes break the protocol. Need strict discipline and testing.
3. **Deadlock**: Synchronous tool calls to slow servers could block the event loop. Need timeout and async handling.
4. **Resource Leaks**: Long-running servers accumulating memory/handles. Need lifecycle management.

## Success Criteria
- [ ] `mcp-studio serve --stdio` starts and responds to MCP protocol
- [ ] Can configure in Claude Code and see aggregated tools
- [ ] Tool calls route correctly to managed servers
- [ ] Manager tools work (list, start, stop servers)
- [ ] Graceful error handling (no crashes on failures)
- [ ] Integration tests pass reliably
- [ ] Can dogfood: use mcp-studio to manage other MCP servers from Claude Code

## Files to Create/Modify
```
cmd/
  mcp-studio/
    main.go           # Cobra root command
    serve.go          # serve subcommand (--stdio, --sse future)
    version.go        # Version info
internal/
  server/
    server.go         # MCP server implementation
    handler.go        # Request handler (tools/list, tools/call)
    aggregator.go     # Tool aggregation from managed servers
    router.go         # Tool call routing
    manager_tools.go  # Meta-tools (servers_list, etc.)
    errors.go         # MCP error codes and formatting
  protocol/
    messages.go       # MCP message types (request, response, notification)
    capabilities.go   # Server capabilities declaration
  testutil/
    fake_server.go    # Fake MCP server for testing
    mcp_client.go     # Test MCP client (for protocol tests)
    fixtures/         # JSON test fixtures
```

## Estimated Complexity
- CLI entrypoint: Low (~100 LOC)
- MCP server protocol: Medium (~300 LOC)
- Tool aggregation: Medium (~200 LOC)
- Tool routing: Medium (~200 LOC)
- Manager tools: Low (~150 LOC)
- Error handling: Low (~100 LOC)
- Test infrastructure: Medium (~400 LOC)
- **Total: ~1450-1800 lines of Go code**

## Relationship to Other Phases

### What This Enables
- **Primary testing method**: All future phases tested via stdio mode
- **Dogfooding**: Use mcp-studio to develop mcp-studio
- **Zero-friction usage**: No daemons, no manual startup
- **Multiple configs**: Different configs for work/personal/project contexts

### What This Defers
- TUI (Phase 2) - optional management interface
- Namespace filtering and permissions (Phase 3) - Phase 1.5 exposes all configured servers, no filtering
- HTTP proxy serving (Phase 4) - **DEFERRED**, add only if web/remote/team needs arise
- SSE client transport (Phase 5) - for connecting TO remote MCP servers

### Future Enhancements (Post-Phase 1.5)
- Namespace-aware tool filtering in stdio mode
- Permission enforcement in stdio mode
- HTTP server mode (`--http`) for web clients (Phase 4, if needed)
