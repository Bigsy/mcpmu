# Phase 1: Foundation

## Objective
Establish the core infrastructure: config schema, domain model, single stdio server connection, and minimal TUI shell. This phase delivers a working proof-of-concept that can connect to one MCP server and list its tools.

## Features

### Config Schema & Persistence
- [ ] Define JSON config schema for servers, namespaces, proxies
- [ ] `ServerConfig` struct: id, name, command, args, cwd, env, kind (stdio/sse), url, headers, OAuth fields
- [ ] `NamespaceConfig` struct: id, name, description, serverIds
- [ ] `ProxyConfig` struct: id, name, pathSegment, host, port, runningState, transportType
- [ ] `ToolPermission` struct: namespaceId, serverId, toolName, enabled
- [ ] Config file location: `~/.config/mcp-studio/config.json`
- [ ] Atomic writes with temp file + rename
- [ ] Restrictive file permissions (0600)
- [ ] Load/save config functions with validation
- [ ] **Config versioning**: schema version field for future migrations
- [ ] **Secret references**: placeholder pattern for OAuth tokens (stored separately, not in config JSON)

### Domain Model & Types
- [ ] `RuntimeState` enum: idle, running, stopped, error
- [ ] `LastExit` struct: code, signal, timestamp
- [ ] `ServerStatus` struct: id, state, pid, lastExit
- [ ] `McpTool` struct: name, description, inputSchema
- [ ] Event types for status changes, log output, tool updates

### Transport Abstraction
- [ ] Define `Transport` interface (Connect, Close, Send, Receive)
- [ ] Define `McpClient` interface (ListTools, InvokeTool, Status)
- [ ] This abstraction allows Phase 4/5 transports without refactoring

### MCP Client (stdio only)
- [ ] Integrate `github.com/mark3labs/mcp-go` or evaluate `github.com/modelcontextprotocol/go-sdk`
- [ ] `StdioMcpClient` implementing McpClient interface
- [ ] `StdioTransport` implementing Transport interface
- [ ] PATH augmentation for Homebrew locations (`/opt/homebrew/bin`, `/usr/local/bin`)
- [ ] stderr capture for log streaming
- [ ] Connection retry with backoff (3 attempts, exponential delay)

### Process Supervisor Primitives
- [ ] Process states: idle, starting, running, stopping, stopped, error, crashed
- [ ] Graceful process termination (SIGTERM → SIGKILL after timeout)
- [ ] Signal handling (SIGINT, SIGTERM for app shutdown)
- [ ] Orphan process cleanup on app crash/restart
- [ ] Exit code/signal capture for debugging

### Event Bus Pattern
- [ ] Define event types: StatusChanged, LogReceived, ToolsUpdated, Error
- [ ] Goroutine-safe event dispatch (channels)
- [ ] Bubble Tea integration: convert events to Bubble Tea messages
- [ ] Prevents race conditions from concurrent goroutines updating state

### Minimal TUI Shell
- [ ] Bubble Tea main model with basic layout
- [ ] Tab bar placeholder (Servers tab active, others disabled)
- [ ] Server list view (using bubbles/list)
- [ ] Basic keybindings: q=quit, ?=help, tab=switch tabs
- [ ] Status bar showing app name and connection count
- [ ] Help overlay (modal or bottom panel)

### Single Server Connection
- [ ] "Add Server" command (hardcoded for testing, or simple text input)
- [ ] Start/stop server with keyboard shortcut
- [ ] Display server status (idle/running/stopped/error)
- [ ] Display tool count when connected
- [ ] Basic error display on connection failure

## Dependencies
- None (this is the foundation phase)
- See [PLAN-ui.md](PLAN-ui.md) for UI design specifications

## Unknowns / Questions
1. **MCP SDK Choice**: Which Go MCP SDK to use? `mark3labs/mcp-go` seems more mature, but verify feature parity with official SDK
2. **Config Migration**: Do we need to support importing existing MCP-studio JSON configs? (Defer to Phase 5)
3. **Tool Schema**: How complex is the inputSchema field? Do we need full JSON Schema validation?
4. **Bubble Tea State**: Should we use a single monolithic model or component-based models?
5. **Event Bus Pattern**: How to safely dispatch events from goroutines to Bubble Tea model?

## Risks
1. **MCP SDK Gaps**: The Go MCP SDKs may have missing features compared to the TypeScript SDK. May need to contribute upstream or implement workarounds.
2. **Process Management**: Child process lifecycle on macOS can be tricky. Need to handle orphan processes and ensure cleanup on app crash.
3. **PATH Issues**: The original app had issues with binaries not being found when launched from Finder. Need thorough testing of PATH handling.
4. **Race Conditions**: Multiple goroutines (process watcher, MCP reader, log tailer) updating state. Need strict event bus pattern.

## Success Criteria
- App launches and displays TUI
- Can add a server config (even hardcoded)
- Can start/stop an MCP server
- Can see the list of tools exposed by the server
- Config persists across app restarts

## Files to Create
```
internal/
  config/
    config.go       # Config schema and load/save
    schema.go       # Type definitions
    version.go      # Schema versioning
  mcp/
    client.go       # McpClient interface
    stdio.go        # StdioMcpClient implementation
    transport.go    # Transport interface + StdioTransport
    types.go        # MCP protocol types
  process/
    supervisor.go   # Process state management
    signals.go      # Signal handling
  events/
    bus.go          # Event bus implementation
    types.go        # Event type definitions
  tui/
    model.go        # Root Bubble Tea model
    server_list.go  # Server list component
    styles.go       # Lipgloss styles
    keybindings.go  # Key handling
cmd/
  mcp-studio/
    main.go         # Entry point
```

## Estimated Complexity
- Config layer: Low
- MCP client: Medium (SDK integration, process management)
- TUI shell: Low-Medium (Bubble Tea basics)
- Total: ~800-1200 lines of Go code
