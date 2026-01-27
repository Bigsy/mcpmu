# Phase 2: Multi-Server TUI

## Objective
Extend the TUI to manage multiple servers with full CRUD operations, start/stop controls, and real-time log streaming. This phase makes the Servers tab fully functional.

## Deferred from Phase 1.5
These items were deferred from stdio server mode and should be addressed here or later:
- [ ] `mcp-studio.config_reload` tool - only matters for long-lived sessions (could skip entirely)
- [ ] `--log-level` actual filtering - currently parses flag but doesn't filter (polish item)
- [ ] `notifications/cancelled` propagation to upstream servers - add when needed

## Architecture Notes
**Existing primitives cover most needs - extend, don't recreate:**
- `process.Supervisor` + `events.Bus` = multi-server registry (no need for new wrapper)
- Log streaming already wired: stderr → events → views
- Views exist (`server_list`, `server_detail`, `log_panel`) - extend these, don't create new components

**Phase 2 should focus on:**
- CRUD forms using `huh` + confirm dialogs
- `autostart` config field
- UX polish in existing views

## Features

### Server CRUD Operations ✅
- [x] Add Server form using `charmbracelet/huh`:
  - ID (auto-generated short 4-char `[a-z0-9]` or user-provided)
  - Name (display name)
  - Command (required)
  - Arguments (space-separated or array)
  - Working directory (optional)
  - Environment variables (key=value pairs)
  - Autostart toggle
- [x] Edit Server form (pre-populated with existing values)
- [x] Delete Server confirmation dialog
- [x] Server list updates reactively after CRUD operations

### Server Lifecycle Management ✅
- [x] Start server (spawn process, establish MCP connection)
- [x] Stop server (graceful shutdown with timeout)
- [x] Restart server (stop + start)
- [x] Server status polling/events (running, stopped, error)
- [x] PID tracking for running servers
- [x] Last exit metadata display (exit code, signal, timestamp)

### Log Streaming ✅
**Note: Basic log streaming already wired (stderr → events → log_panel):**
- [x] Real-time stderr capture from server processes → Already implemented
- [x] Log buffer with configurable size (last N lines, default 1000) → Already implemented
- [x] Log viewer panel (scrollable, auto-scroll to bottom) → `log_panel.go` exists
- [x] Toggle log viewer visibility (l key) → Already bound
- [ ] Log deduplication (collapse repeated lines) - deferred (polish)
- [x] Log timestamps

### Server Detail View ✅
- [x] Split view: server list | detail pane (press Enter for detail)
- [x] Detail shows: status, PID, tools count, last error
- [x] Tools list in detail view (name, description)
- [x] Log panel visible in both views (toggle with l)

### Multi-Server Registry ✅
**Note: `process.Supervisor` + `events.Bus` already provide this - extend rather than create new:**
- [x] ServerRegistry manages multiple McpClient instances → `process.Supervisor` already does this
- [x] Concurrent connection support → Already implemented
- [x] Status events broadcast to TUI via event bus → `events.Bus` already wired
- [x] Tool cache per server (in serverTools map)
- [x] Tool discovery on connect
- [ ] Offline tool representation (for Phase 3 permissions when server is stopped) - deferred

### Basic Autostart ✅
- [x] Server config flag: `autostart: bool`
- [x] On app launch: start servers with autostart=true
- [x] Simple implementation (no queue/ordering, just concurrent start)
- [x] Handle startup failures gracefully (don't block other servers)
- [x] Log autostart results to TUI (via events/log panel)

### TUI Enhancements ✅
- [x] Server list with status indicators (icons/colors)
  - Green: running
  - Red: error
  - Gray: stopped
- [x] Keyboard shortcuts:
  - Enter: view details
  - t: test server (start/stop toggle)
  - E: enable/disable (for stdio serve mode, does NOT start/stop)
  - a: add server
  - e: edit server
  - d: delete server
  - l: toggle logs
- [x] Status bar: "X/Y servers running"
- [x] Confirmation dialogs for destructive actions
- [x] Toast notifications for server events and CRUD operations

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
    config.go           # Add server CRUD methods, autostart field
  process/
    supervisor.go       # Already exists - extend if needed
  events/
    bus.go              # Already exists - extend if needed
  tui/
    views/
      server_list.go    # Already exists - extend with status indicators
      server_detail.go  # Already exists - extend with tools list
      log_panel.go      # Already exists - extend with timestamps, dedup
    server_form.go      # NEW: Add/edit form using huh
    confirm.go          # NEW: Confirmation dialog
    messages.go         # Already exists - add new message types
```

## Estimated Complexity
- Server CRUD: Low-Medium
- Lifecycle management: Medium
- Log streaming: Medium
- TUI components: Medium
- Total: ~1500-2000 lines of Go code (cumulative ~2500-3200)
