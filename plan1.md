# Phase 1: Foundation

## Objective
Establish the core infrastructure: config schema, domain model, single stdio server connection, and minimal TUI shell. This phase delivers a working proof-of-concept that can connect to one MCP server and list its tools.

---

## Design Decisions (Locked In)

These decisions are made upfront to avoid rework in later phases.

### 1. Tool List Location
**Decision**: Detail view only. Press `Enter` on a server to see its tools.
- Server list shows: name, status, command, tool count
- Detail view shows: full server info + scrollable tool list
- Validates MCP `tools/list` flow end-to-end in Phase 1

### 2. Form Library
**Decision**: Use `huh` from Phase 1 for Add/Edit Server forms.
- No throwaway text input code
- Consistent form UX from the start
- `huh` is small, well-documented, and already planned for Phase 2+

### 3. Process Ownership Model
**Decision**: Supervisor owns `exec.Cmd`, MCP client receives pipes.
```
process.Supervisor
  └── spawns exec.Cmd
  └── captures stdin/stdout/stderr pipes
  └── creates mcp.StdioTransport(stdin, stdout)
  └── creates mcp.Client(transport)
  └── returns ServerHandle{Client, Stop(), Logs()}

mcp.Client
  └── pure protocol implementation
  └── receives io.ReadWriter, doesn't know about processes
  └── easy to test with fake server (just pipes)
```
Clean separation: Supervisor handles lifecycle, Client handles protocol.

### 4. Log Panel
**Decision**: Implement log panel in Phase 1 with `l` toggle.
- Bottom panel showing stderr from all running servers
- Essential for debugging MCP connections
- Simple viewport component, immediately useful
- `f` for follow mode (auto-scroll)

### 5. Feedback Primitives
**Decision**: Establish these mechanisms in Phase 1:

| Primitive | Phase 1 Scope |
|-----------|---------------|
| Toast | Full implementation (server started/stopped/error) |
| Confirm dialog | Full implementation (quit with running servers) |
| Modal | Full implementation (Add/Edit Server form) |
| Help overlay | Stub (shows `?` pressed, minimal content) |

### 6. Keybinding Architecture
**Decision**: Establish dispatch pattern, don't hardcode.
```go
type KeyContext int
const (
    ContextList KeyContext = iota
    ContextModal
    ContextLogPanel
    ContextHelp
)

type Keymap struct {
    Global map[string]Action  // q, ?, 1/2/3
    List   map[string]Action  // j, k, Enter, a, e, d, s, x
    Modal  map[string]Action  // Tab, Esc, Enter
    Log    map[string]Action  // f, Esc
}
```
Context-aware routing prevents spaghetti, makes Phase 2-5 keys trivial to add.

### 7. Responsive Layout
**Decision**: Single layout for Phase 1. Keep it simple.
- Handle `WindowSizeMsg` to track terminal size
- Truncate content if narrow, but same layout structure
- Iterate on responsive breakpoints in later phases if needed

---

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
- [ ] `Client` struct implementing McpClient interface
- [ ] `StdioTransport` implementing Transport interface
- [ ] **Client receives `io.ReadWriter` pipes** (doesn't spawn processes)
- [ ] Initialize handshake (`initialize` request/response)
- [ ] `ListTools()` method calling `tools/list`
- [ ] Connection retry with backoff (3 attempts, exponential delay)

### Process Supervisor
- [ ] **Supervisor owns `exec.Cmd` lifecycle** (spawns, stops, monitors)
- [ ] Process states: idle, starting, running, stopping, stopped, error, crashed
- [ ] PATH augmentation for Homebrew locations (`/opt/homebrew/bin`, `/usr/local/bin`)
- [ ] Capture stdin/stdout pipes for MCP transport
- [ ] Capture stderr for log streaming
- [ ] Graceful process termination (SIGTERM → SIGKILL after timeout)
- [ ] Signal handling (SIGINT, SIGTERM for app shutdown)
- [ ] Orphan process cleanup on app crash/restart
- [ ] Exit code/signal capture for debugging
- [ ] Returns `ServerHandle{Client, Stop(), Logs()}` to TUI

### Event Bus Pattern
- [ ] Define event types: StatusChanged, LogReceived, ToolsUpdated, Error
- [ ] Goroutine-safe event dispatch (channels)
- [ ] Bubble Tea integration: convert events to Bubble Tea messages
- [ ] Prevents race conditions from concurrent goroutines updating state

### TUI Shell & Layout
- [ ] Bubble Tea main model with basic layout
- [ ] Tab bar (Servers tab active, Namespaces/Proxies disabled but visible)
- [ ] Server list view (using `bubbles/list` with custom delegate)
- [ ] Server detail view (Enter on server → tools list, Esc to return)
- [ ] Status bar showing running count and help hint
- [ ] **Single layout** - handle `WindowSizeMsg`, truncate if narrow
- [ ] Theme struct with Lipgloss styles (see PLAN-ui.md patterns)

### Keybinding System
- [ ] `Keymap` struct with context-aware routing
- [ ] `KeyContext` enum: List, Modal, LogPanel, Help, Confirm
- [ ] Global keys: `q` (quit), `?` (help), `1/2/3` (tabs)
- [ ] List keys: `j/k` (navigate), `Enter` (detail), `a` (add), `e` (edit), `d` (delete), `s` (start), `x` (stop)
- [ ] Modal keys: `Tab` (next field), `Esc` (cancel), `Enter` (submit)
- [ ] Log keys: `l` (toggle), `f` (follow), `Esc` (close)

### Feedback Primitives
- [ ] **Toast component**: auto-dismiss messages (success/info/warning/error)
- [ ] **Confirm dialog**: modal with Yes/No (used for quit-with-running, delete)
- [ ] **Modal overlay**: centered form container with backdrop dimming
- [ ] **Help overlay**: stub implementation (shows keybindings, minimal content)

### Log Panel
- [ ] Bottom panel using `bubbles/viewport`
- [ ] Toggle with `l` key
- [ ] Shows stderr from all running servers with `[server]` prefix
- [ ] Follow mode (`f`) auto-scrolls to bottom
- [ ] Timestamp prefix on each line

### Add/Edit Server Form
- [ ] **Use `huh` library** for form (not throwaway textinput)
- [ ] Fields: Name, Type (dropdown), Command, Arguments, Working Dir, Env Vars
- [ ] Validation: name required, command required
- [ ] Opens as centered modal overlay
- [ ] `Esc` cancels, `Enter` on Save button submits

### Server List & Actions
- [ ] List shows: status icon, name, command (truncated), tool count
- [ ] Start server (`s`): spawns process, connects MCP client
- [ ] Stop server (`x`): graceful shutdown
- [ ] Delete server (`d`): confirm dialog, then remove from config
- [ ] Edit server (`e`): opens form with existing values
- [ ] Detail view (`Enter`): full info + scrollable tool list

## Dependencies
- None (this is the foundation phase)
- See [PLAN-ui.md](PLAN-ui.md) for UI design specifications
- See [plan1.1.md](plan1.1.md) for testing strategy

## Unknowns / Questions
1. **MCP SDK Choice**: Which Go MCP SDK to use? `mark3labs/mcp-go` seems more mature, but verify feature parity with official SDK. Evaluate early in implementation.
2. **Config Migration**: Do we need to support importing existing MCP-studio JSON configs? (Defer to Phase 5)
3. **Tool Schema**: How complex is the inputSchema field? Do we need full JSON Schema validation? (Likely just store as `json.RawMessage` for now)

**Resolved in Design Decisions:**
- ~~Bubble Tea State~~: Component-based with root model routing
- ~~Event Bus Pattern~~: Channels + Bubble Tea messages (see Event Bus section)
- ~~Form approach~~: Use `huh` from Phase 1
- ~~Process ownership~~: Supervisor owns exec.Cmd, Client receives pipes
- ~~Log visibility~~: Log panel with `l` toggle
- ~~Responsive layout~~: Single layout, iterate later

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
    client.go       # McpClient interface + Client implementation
    transport.go    # Transport interface + StdioTransport
    types.go        # MCP protocol types (Tool, etc.)
  process/
    supervisor.go   # Process lifecycle management
    handle.go       # ServerHandle struct (Client, Stop, Logs)
    signals.go      # Signal handling (SIGINT, SIGTERM)
  events/
    bus.go          # Event bus implementation
    types.go        # Event type definitions
  tui/
    model.go        # Root Bubble Tea model
    keymap.go       # Keymap struct + context routing
    theme.go        # Theme struct with Lipgloss styles
    views/
      server_list.go    # Server list component (bubbles/list delegate)
      server_detail.go  # Server detail view (tools list)
      log_panel.go      # Log panel component (bubbles/viewport)
    forms/
      server_form.go    # Add/Edit Server form (huh)
    feedback/
      toast.go          # Toast component
      confirm.go        # Confirm dialog
      modal.go          # Modal overlay container
      help.go           # Help overlay (stub)
cmd/
  mcp-studio-go/
    main.go         # Entry point
```

## Estimated Complexity
- Config layer: Low (~200 LOC)
- MCP client + transport: Medium (~300 LOC)
- Process supervisor: Medium (~250 LOC)
- Event bus: Low (~100 LOC)
- TUI shell + keybindings: Medium (~400 LOC)
- Feedback primitives (toast, confirm, modal): Medium (~300 LOC)
- Server form (huh): Low (~150 LOC)
- Log panel: Low (~100 LOC)
- **Total: ~1800-2200 lines of Go code**

Note: Increased from original estimate due to upfront investment in feedback primitives, keybinding architecture, and huh forms. This pays off in Phase 2+ by avoiding rework.
