# Phase 2: Multi-Server TUI

## Objective
Extend the TUI to manage multiple servers with full CRUD operations, start/stop controls, and real-time log streaming. This phase makes the Servers tab fully functional.

## Features

### Server CRUD Operations
- [ ] Add Server form using `charmbracelet/huh`:
  - ID (auto-generated UUID or user-provided)
  - Name (display name)
  - Command (required)
  - Arguments (space-separated or array)
  - Working directory (optional)
  - Environment variables (key=value pairs)
- [ ] Edit Server form (pre-populated with existing values)
- [ ] Delete Server confirmation dialog
- [ ] Server list updates reactively after CRUD operations

### Server Lifecycle Management
- [ ] Start server (spawn process, establish MCP connection)
- [ ] Stop server (graceful shutdown with timeout)
- [ ] Restart server (stop + start)
- [ ] Server status polling/events (running, stopped, error)
- [ ] PID tracking for running servers
- [ ] Last exit metadata display (exit code, signal, timestamp)

### Log Streaming
- [ ] Real-time stderr capture from server processes
- [ ] Log buffer with configurable size (last N lines, default 1000)
- [ ] Log deduplication (collapse repeated lines)
- [ ] Log viewer panel (scrollable, auto-scroll to bottom)
- [ ] Toggle log viewer visibility (l key)
- [ ] Log timestamps

### Server Detail View
- [ ] Split view: server list | detail pane
- [ ] Detail shows: status, PID, tools count, last error
- [ ] Tools list in detail view (name, description)
- [ ] Log viewer embedded in detail view

### Multi-Server Registry
- [ ] ServerRegistry manages multiple McpClient instances
- [ ] Concurrent connection support
- [ ] Status events broadcast to TUI via event bus
- [ ] Tool cache per server (persists across reconnects)
- [ ] Tool discovery on connect + periodic refresh
- [ ] Offline tool representation (for Phase 3 permissions when server is stopped)

### Basic Autostart
- [ ] Server config flag: `autostart: bool`
- [ ] On app launch: start servers with autostart=true
- [ ] Simple implementation (no queue/ordering, just concurrent start)
- [ ] Handle startup failures gracefully (don't block other servers)
- [ ] Log autostart results to TUI

### TUI Enhancements
- [ ] Server list with status indicators (icons/colors)
  - Green: running
  - Red: error
  - Gray: stopped
- [ ] Keyboard shortcuts:
  - Enter: view details
  - a: add server
  - e: edit server
  - d: delete server
  - s: start server
  - x: stop server
  - r: restart server
  - l: toggle logs
- [ ] Status bar: "X/Y servers running"
- [ ] Confirmation dialogs for destructive actions

## Dependencies
- Phase 1: Config schema, McpClient, basic TUI
- See [PLAN-ui.md](PLAN-ui.md) for server list, detail view, and log viewer specs

## Unknowns / Questions
1. **Log Buffer Strategy**: Ring buffer vs. slice with truncation? Memory impact with many servers?
2. **Concurrent Starts**: Should server starts be queued or parallel? What about start-all?
3. **Form Validation**: How strict should validation be? Allow empty args? Validate command exists?
4. **Detail View Layout**: Fixed split or adjustable? Tabs within detail?
5. **Tool Cache Staleness**: How long to keep cached tools when server is stopped? Until explicit refresh?

## Risks
1. **Memory Pressure**: Many servers with verbose logs could consume significant memory. Need buffer limits and potential disk spill.
2. **UI Responsiveness**: Long-running operations (server start) must not block the UI. Need proper async handling with Bubble Tea messages.
3. **State Synchronization**: Multiple servers changing state simultaneously requires careful event handling to avoid race conditions.
4. **Backpressure**: Log streaming + concurrent servers can overwhelm TUI. Need bounded buffers, truncation, per-server retention limits.

## Success Criteria
- Can manage 5+ servers through the TUI
- All CRUD operations work correctly
- Server start/stop works reliably
- Logs stream in real-time
- Status updates are immediate and accurate
- Config persists all server changes

## Files to Create/Modify
```
internal/
  config/
    config.go       # Add server CRUD methods
  mcp/
    registry.go     # Multi-server registry
    events.go       # Event types and dispatcher
  tui/
    server_list.go  # Enhanced list with status
    server_form.go  # Add/edit form using huh
    server_detail.go # Detail view component
    log_viewer.go   # Log viewer component
    confirm.go      # Confirmation dialog
    messages.go     # Bubble Tea messages
```

## Estimated Complexity
- Server CRUD: Low-Medium
- Lifecycle management: Medium
- Log streaming: Medium
- TUI components: Medium
- Total: ~1500-2000 lines of Go code (cumulative ~2500-3200)
