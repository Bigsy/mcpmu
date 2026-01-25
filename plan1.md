# Phase 1: Foundation

## Implementation Status: ✅ COMPLETE (2026-01-25)

**Summary**: Phase 1 core functionality is working. Servers can be started/stopped, tools are discovered, logs are captured.

**Key learnings during implementation:**
- MCP stdio uses NDJSON framing (not Content-Length as originally planned)
- Didn't need `mark3labs/mcp-go` SDK - custom JSON-RPC client was simpler
- Key routing needed careful handling to not block bubbles/list navigation

**Remaining items for polish:**
- [ ] Connection retry with backoff
- [ ] Signal handling for graceful app shutdown
- [ ] Orphan process cleanup
- [ ] Per-server enabled/disabled toggle (persisted; config-only, not lifecycle)
- [ ] Replace manual start/stop with a "Test" toggle (temporary start → initialize → tools/list; press again stops)
- [ ] Help overlay (`?` key)

---

## Objective
Establish the core infrastructure (config schema, domain model, process supervisor, stdio MCP client, event bus) and a minimal TUI shell that can:
- Load servers from config (manual editing in this phase)
- Start/stop a server
- Show a server’s tools (after connect)
- Tail stderr logs in a toggleable log panel

---

## Phase 1 Scope (to keep momentum)

**In scope**
- Read/validate config; optional save helpers (no in-TUI editing)
- Process lifecycle + stderr log capture
- MCP stdio client + `tools/list`
- Minimal Bubble Tea app shell (Servers tab only) with list → detail navigation
- Log panel (`l`) + follow mode (`f`)
- Minimal “quit with running servers” confirm

**Out of scope (defer to Phase 2)**
- Add/Edit/Delete server forms (`huh`)
- Toasts and “polish” feedback primitives (beyond quit confirm)
- Rich multi-pane layout (list+detail split, filtering, etc.)

## Design Decisions (Locked In)

These decisions are made upfront to avoid rework in later phases.

### 1. Tool List Location
**Decision**: Detail view only. Press `Enter` on a server to see its tools.
- Server list shows: name, status, command, tool count
- Detail view shows: full server info + scrollable tool list
- Validates MCP `tools/list` flow end-to-end in Phase 1

### 2. Form Library
**Decision**: Use `huh` for Add/Edit Server forms in **Phase 2**.
- Phase 1 stays config-file-driven to keep the core loop (start/connect/list tools/logs) small and testable
- Avoids building “temporary” text inputs while still keeping forms as the clear next step

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
**Decision**: Keep Phase 1 feedback minimal to avoid UI churn.

| Primitive | Phase 1 Scope |
|-----------|---------------|
| Toast | Deferred (Phase 2) |
| Confirm dialog | Minimal implementation (quit with running servers) |
| Modal | Deferred (Phase 2) |
| Help overlay | Deferred or stub (optional) |

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
    List   map[string]Action  // j, k, Enter, s, x, l
    Modal  map[string]Action  // Esc
    Log    map[string]Action  // f, Esc
}
```
Context-aware routing prevents spaghetti, makes Phase 2-5 keys trivial to add.

### 7. Responsive Layout
**Decision**: Single layout for Phase 1. Keep it simple.
- Handle `WindowSizeMsg` to track terminal size
- Truncate content if narrow, but same layout structure
- Iterate on responsive breakpoints in later phases if needed

### 8. MCP SDK & Wire Format
**Decision**: Start with `mark3labs/mcp-go`, own the framing layer.

**SDK Choice:**
- Use `github.com/mark3labs/mcp-go` for Phase 1
- Has working stdio client examples, pipes-friendly
- Our `McpClient` interface abstracts it - can swap to official SDK later if needed
- Key requirement: SDK must accept `io.ReadWriter` (not spawn processes itself)

**Framing Strategy** ("be liberal in what you accept, conservative in what you send"):
- **Read**: Accept both Content-Length (LSP-style) and NDJSON (newline-delimited)
- **Write**: Always use Content-Length (spec-compliant)
- Own the framing layer (~50-100 LOC), don't rely on SDK for this

```go
// StdioTransport framing

// Read: detect format, handle both
func (t *StdioTransport) Read() ([]byte, error) {
    // Peek first bytes
    // If starts with "Content-Length:", use LSP framing
    // Otherwise, assume NDJSON (read until newline)
}

// Write: always Content-Length (spec-compliant)
func (t *StdioTransport) Write(msg []byte) error {
    fmt.Fprintf(t.w, "Content-Length: %d\r\n\r\n%s", len(msg), msg)
}
```

This maximizes compatibility and removes "which spec is it today?" risk.

---

## Features

### Config Schema & Persistence
- [x] Define JSON config schema for servers, namespaces, proxies
- [x] `ServerConfig` struct: id, name, command, args, cwd, env, kind (stdio/sse), url, headers, OAuth fields
- [x] `NamespaceConfig` struct: id, name, description, serverIds
- [x] `ProxyConfig` struct: id, name, pathSegment, host, port, runningState, transportType (Phase 4, deferred - include in schema for forward compatibility)
- [x] `ToolPermission` struct: namespaceId, serverId, toolName, enabled
- [x] `defaultNamespaceId` config field for stdio toolset selection (Phase 1.5)
- [x] **`mcpServers`-compatible server entries**: keep JSON field names aligned (`command`, `args`, `cwd`, `env`) so users can copy/paste from common MCP client configs
- [x] **Servers map format**: support `servers` (or `mcpServers`) as an object map `serverId → {command,args,cwd,env,...}` for easy manual editing
- [x] Server ID rules: auto-generate short 4-char `[a-z0-9]` id by default; regenerate on collision; disallow `.` in ids
- [x] Config file location: `~/.config/mcp-studio/config.json`
- [x] Atomic writes with temp file + rename
- [x] Restrictive file permissions (0600)
- [x] Load/save config functions with validation
- [x] **Config versioning**: schema version field for future migrations
- [ ] **Secret references**: placeholder pattern for OAuth tokens (stored separately, not in config JSON)

### Domain Model & Types
- [x] `RuntimeState` enum: idle, running, stopped, error
- [x] `LastExit` struct: code, signal, timestamp
- [x] `ServerStatus` struct: id, state, pid, lastExit
- [x] `McpTool` struct: name, description, inputSchema
- [x] Event types for status changes, log output, tool updates

### Transport Abstraction
- [x] Define `Transport` interface (Connect, Close, Send, Receive)
- [x] Define `McpClient` interface (ListTools, InvokeTool, Status)
- [x] This abstraction allows Phase 4/5 transports without refactoring
- [x] Abstracts SDK choice - can swap `mark3labs/mcp-go` for official SDK later

### Stdio Framing Layer (owned, not from SDK)
- [x] `StdioTransport` struct wrapping `io.ReadWriter`
- [x] **NDJSON framing** (newline-delimited JSON) - this is what MCP stdio actually uses
- **NOTE**: Original plan said Content-Length, but MCP stdio uses NDJSON. Fixed during implementation.

### MCP Client (stdio only)
- [x] Custom JSON-RPC client (didn't need mark3labs/mcp-go SDK)
- [x] `Client` struct implementing McpClient interface
- [x] **Client receives `io.ReadWriter` pipes** (doesn't spawn processes)
- [x] Wire through our `StdioTransport` framing layer
- [x] Initialize handshake (`initialize` request/response)
- [x] `ListTools()` method calling `tools/list`
- [ ] Connection retry with backoff (3 attempts, exponential delay) - not implemented yet

### Process Supervisor
- [x] **Supervisor owns `exec.Cmd` lifecycle** (spawns, stops, monitors)
- [x] Process states: idle, starting, running, stopping, stopped, error, crashed
- [x] PATH augmentation for Homebrew locations (`/opt/homebrew/bin`, `/usr/local/bin`)
- [x] Capture stdin/stdout pipes for MCP transport
- [x] Capture stderr for log streaming
- [x] Graceful process termination (SIGTERM → SIGKILL after timeout)
- [ ] Signal handling (SIGINT, SIGTERM for app shutdown) - partial, Ctrl+C works
- [ ] Orphan process cleanup on app crash/restart - not implemented
- [x] Exit code/signal capture for debugging
- [x] Returns `ServerHandle{Client, Stop(), Logs()}` to TUI

### Event Bus Pattern
- [x] Define event types: StatusChanged, LogReceived, ToolsUpdated, Error
- [x] Goroutine-safe event dispatch (channels)
- [x] Bubble Tea integration: convert events to Bubble Tea messages
- [x] Prevents race conditions from concurrent goroutines updating state

### TUI Shell & Layout
- [x] Bubble Tea main model with basic layout
- [x] Tab bar (Servers tab active; Namespaces/Proxies visible but disabled)
- [x] Server list view (using `bubbles/list` with custom delegate)
- [x] Server detail view (Enter on server → tools list, Esc to return)
- [x] Status bar showing running count and help hint
- [x] **Single layout** - handle `WindowSizeMsg`, truncate if narrow
- [x] Theme struct with Lipgloss styles (see PLAN-ui.md patterns)

### Keybinding System
- [x] `Keymap` struct with context-aware routing
- [x] `KeyContext` enum: List, Modal, LogPanel, Help, Confirm
- [x] Global keys: `q` (quit), `?` (help - not implemented), `1/2/3` (tabs)
- [x] List keys (Phase 1): `j/k` (navigate), `Enter` (detail), `s` (start), `x` (stop), `l` (toggle logs)
- [x] Modal keys (Phase 1): `Esc` (cancel/close confirm)
- [x] Log keys: `l` (toggle), `f` (follow), `Esc` (close)

### Feedback Primitives
- [x] **Confirm dialog**: modal with Yes/No (quit-with-running only)
- [ ] Toast, modal overlay, help overlay: deferred to Phase 2 (see PLAN-ui.md)

### Log Panel
- [x] Bottom panel using `bubbles/viewport`
- [x] Toggle with `l` key
- [x] Shows stderr from all running servers with `[server]` prefix
- [x] Follow mode (`f`) auto-scrolls to bottom
- [x] Timestamp prefix on each line

### Server List & Actions (config-file-driven)
- [x] List shows: status icon, name, command (truncated), tool count
- [x] Start server (`s`): spawns process, connects MCP client
- [x] Stop server (`x`): graceful shutdown
- [ ] Remove `s`/`x` UI actions (keep stop-all-on-exit); use test toggle for lifecycle instead
- [ ] Toggle enabled/disabled (persist to config; does not start/stop; later gates exposure in `serve --stdio`)
- [ ] Test toggle (`t`): start+validate (init + tools/list), press again stops; allowed even if disabled
- [x] Detail view (`Enter`): full info + scrollable tool list

## Dependencies
- None (this is the foundation phase)
- See [PLAN-ui.md](PLAN-ui.md) for UI design specifications
- See [plan1.1.md](plan1.1.md) for testing strategy

## Unknowns / Questions
1. **Config Migration**: Do we need to support importing existing MCP-studio JSON configs? (Defer to Phase 5)
2. **Tool Schema**: How complex is the inputSchema field? Do we need full JSON Schema validation? (Likely just store as `json.RawMessage` for now)

**Resolved in Design Decisions:**
- ~~MCP SDK Choice~~: Use `mark3labs/mcp-go`, own framing layer, abstract via interface
- ~~Bubble Tea State~~: Component-based with root model routing
- ~~Event Bus Pattern~~: Channels + Bubble Tea messages (see Event Bus section)
- ~~Form approach~~: Use `huh` from Phase 1
- ~~Process ownership~~: Supervisor owns exec.Cmd, Client receives pipes
- ~~Log visibility~~: Log panel with `l` toggle
- ~~Responsive layout~~: Single layout, iterate later
- ~~Wire format~~: Accept Content-Length + NDJSON on read, write Content-Length

## Risks
1. **MCP SDK Gaps**: The Go MCP SDKs may have missing features compared to the TypeScript SDK. May need to contribute upstream or implement workarounds.
2. **Process Management**: Child process lifecycle on macOS can be tricky. Need to handle orphan processes and ensure cleanup on app crash.
3. **PATH Issues**: The original app had issues with binaries not being found when launched from Finder. Need thorough testing of PATH handling.
4. **Race Conditions**: Multiple goroutines (process watcher, MCP reader, log tailer) updating state. Need strict event bus pattern.

## Success Criteria
- App launches and displays TUI
- Can load server configs from `~/.config/mcp-studio/config.json` (manual editing)
- Can start/stop an MCP server
- Can see the list of tools exposed by the server
- Can view stderr logs in the log panel
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
    transport.go    # Transport interface
    framing.go      # StdioTransport with Content-Length + NDJSON support
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
    feedback/
      confirm.go        # Confirm dialog (quit-with-running)
cmd/
  mcp-studio/
    main.go         # Entry point (Phase 1: TUI; Phase 1.5 adds Cobra)
```

## Estimated Complexity
- Config layer: Low (~200 LOC)
- Stdio framing layer: Low (~100 LOC)
- MCP client + transport: Medium (~250 LOC)
- Process supervisor: Medium (~250 LOC)
- Event bus: Low (~100 LOC)
- TUI shell + keybindings: Medium (~400 LOC)
- Feedback primitives (toast, confirm, modal): Medium (~300 LOC)
- Server form (huh): Low (~150 LOC)
- Log panel: Low (~100 LOC)
- **Total: ~1850-2250 lines of Go code**

Note: Increased from original estimate due to upfront investment in feedback primitives, keybinding architecture, huh forms, and owned framing layer. This pays off in Phase 2+ by avoiding rework and maximizing compatibility.
