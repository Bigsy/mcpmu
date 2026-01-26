# MCP Studio Go

A TUI-based MCP (Model Context Protocol) server manager. Rewrite of [MCP-studio](/Users/hedworth/workspace/mystuff/mac/MCP-studio) to eliminate Tauri/Node complexity in favor of a single Go binary.

## Goals

- Simple TUI for managing MCP server configurations
- Enable/disable servers and tools with keyboard-driven interface
- Single binary, no runtime dependencies
- Human-editable JSON config file

## Tech Stack

| Component | Choice | Notes |
|-----------|--------|-------|
| Language | Go | |
| TUI Framework | [Bubble Tea](https://github.com/charmbracelet/bubbletea) | MVU architecture |
| TUI Components | [Bubbles](https://github.com/charmbracelet/bubbles) | List, table, textinput, help |
| TUI Styling | [Lipgloss](https://github.com/charmbracelet/lipgloss) | |
| Forms | [Huh](https://github.com/charmbracelet/huh) | For config editing |
| MCP SDK | [mark3labs/mcp-go](https://github.com/mark3labs/mcp-go) or official SDK | Evaluate both |
| Secrets | [go-keyring](https://github.com/zalando/go-keyring) | OS keychain for OAuth tokens |
| Config | `encoding/json` | JSON file with atomic writes |

---

## Documentation

- **[UI Design Specification](PLAN-ui.md)** - TUI layout, navigation, keybindings, visual design

---

## Implementation Phases

### [Phase 1: Foundation](plan1.md) ✅
Config schema, domain model, single stdio server connection, minimal TUI shell.
- Config persistence with atomic writes
- MCP client for stdio transport
- Basic Bubble Tea TUI shell (config-file-driven)
- Start/stop a server, list tools, view stderr logs (log panel)
- No in-TUI CRUD forms yet (Phase 2)

### [Phase 1.1: Testing Strategy](plan1.1.md) ✅
Testing infrastructure and fixtures for validating Phase 1–2 work.
- Fake MCP server (stdio) for tests/CI
- Integration tests (start, handshake, list tools, stop, crash handling)
- TUI unit tests (Update/View logic)
- CI configuration (GitHub Actions, OS matrix)

### [Phase 1.5: stdio Server Mode](plan1.5.md) ✅
Enable mcp-studio to be spawned by Claude Code as an MCP server.
- Cobra CLI with serve subcommand
- MCP server protocol (NDJSON)
- Tool aggregation from managed servers
- Tool call routing with lazy start
- Manager tools (servers_list, servers_start, etc.)
- Namespace selection and enforcement

### [Phase 2: Multi-Server TUI](plan2.md) ⬜
Server list view, CRUD operations, start/stop, log streaming.
- Server add/edit/delete forms
- Multi-server registry
- Real-time log viewer
- Server detail view with tools list

### [Phase 3: Namespaces](plan3.md) ⬜
Namespace CRUD, server assignment, tool permissions UI.
- Namespace management
- Server-to-namespace assignment
- Tool permission editor with bulk actions
- "Enable Safe Tools" heuristic

### [Phase 4: Proxies](plan4.md) ⬜
HTTP proxy layer, SSE + Streamable-HTTP transports, namespace binding.
- Proxy CRUD and lifecycle
- SSE transport implementation
- Streamable-HTTP transport
- Tool aggregation with permissions
- Namespace binding

### [Phase 5: SSE + OAuth](plan5.md) ⬜
SSE client transport, OAuth browser flow, token storage/refresh, polish.
- SSE client for remote servers
- OAuth 2.1 with PKCE
- Secure token storage (keyring)
- Import/export configuration
- Autostart queue
- UX polish

---

## Progress Tracking

| Phase | Status | Features | Notes |
|-------|--------|----------|-------|
| 1 - Foundation | ✅ Complete | 5/5 | |
| 1.1 - Testing | ✅ Complete | 5/5 | Fake MCP server, integration tests |
| 1.5 - stdio Server | ✅ Complete | 6/6 | MCP server mode for Claude Code |
| 2 - Multi-Server | ⬜ Not Started | 0/6 | |
| 3 - Namespaces | ⬜ Not Started | 0/7 | |
| 4 - Proxies | ⬜ Not Started | 0/10 | |
| 5 - SSE + OAuth | ⬜ Not Started | 0/12 | |

**Legend:** ⬜ Not Started | 🟡 In Progress | ✅ Complete

---

## Core Features (from original)

### Server Management

- [ ] Register MCP servers via **stdio** or **SSE** transport
- [ ] Server config fields: id, name, command, args, cwd, env vars
- [ ] SSE server config: url, custom headers
- [ ] Start/stop/restart servers
- [ ] Remove/delete servers
- [ ] View server status (running, stopped, error)
- [ ] Stream stdout/stderr logs in real-time
- [ ] View last exit metadata (exit code, signal, timestamp)
- [ ] Autostart queue - remember running servers across app restarts
  - Continuously maintained via status events (running→enqueue, stopped→dequeue)
  - On exit: persist running servers, then stop them gracefully
  - On launch: restore servers from queue

### Namespaces

- [ ] Create namespaces with id, name, description
- [ ] Assign servers to namespaces
- [ ] Tool permissions per namespace (allow-list)
  - "Enable Safe Tools" heuristic (read/get/list/search/view/show = safe; write/update/delete/execute/run/create/set/modify = unsafe)
  - "Enable All" / "Disable All" bulk actions
  - Default behavior: unconfigured tools enabled until server has any explicit permissions, then unspecified tools default to disabled
- [ ] Update/delete namespaces
- [ ] Tool-name namespacing: `{namespaceId}:{serverId}` to avoid cross-namespace collisions

### Proxies

- [ ] Create multiple HTTP proxy surfaces with custom host/port/path segments
- [ ] Auto-assign ports at runtime (port=0)
- [ ] Transport types:
  - **SSE**: `GET /mcp/{path}` + `POST /mcp/{path}/message`, session via query param or `mcp-session-id` header
  - **Streamable-HTTP**: `POST /mcp/{path}` for messages, `DELETE` for session cleanup, `mcp-session-id` in response headers
- [ ] Start/stop proxies independently
- [ ] View proxy analytics (upstream count, total tools exposed)
- [ ] Manage namespace membership per proxy (toggle which namespaces are exposed)
- [ ] Copy proxy URL to clipboard
- [ ] Proxy autostart (restore running state on launch)
- [ ] CORS: permissive (`*`), allows `mcp-session-id` header
- [ ] Tool aggregation: prefix tool descriptions with upstream server name

### OAuth Support (SSE Servers)

- [ ] OAuth 2.1 flow with browser-based authorization
- [ ] Local callback server on `localhost:3333/oauth/callback` (+ `/health` endpoint)
- [ ] OAuth config: client ID, client secret, scopes, authorization URL, token URL
- [ ] Status badges: Authorized (green), Expiring Soon (amber), Not Authorized (red)
- [ ] Authorize button in server list for unauthorized servers
- [ ] Encrypted token storage (AES-256-GCM with machine-derived key)
- [ ] Token refresh monitoring for connected clients
  - Only refreshes tokens for connected SSE clients nearing expiry
  - Reconnects SSE transport after refresh
  - Emits "token refreshed" event with success/failure
- [ ] Auth gate on server start: check status → attempt refresh → fallback to full browser auth
- [ ] Revoke tokens
- [ ] PKCE state and code verifiers expire after 10 minutes

### Configuration

- [ ] Export config as pretty-printed JSON
- [ ] Import config from JSON with validation
  - Auto-fix orphaned references (servers referenced by namespaces that don't exist)
  - Version compatibility check
- [ ] Auto-backup before import (keeps last 10 backups)
- [ ] Atomic config writes with restrictive file permissions
- [ ] Split storage:
  - **JSON config file**: servers, namespaces, proxies, tool permissions (source of truth, human-editable)
  - **Runtime state**: autostart queue, proxy running state, OAuth tokens/metadata, preferences

### App / UI

- [ ] Tab navigation: Servers, Namespaces, Proxies
- [ ] Status bar showing counts (running/total servers, proxies)
- [ ] Uptime tracking display
- [ ] Log viewer with buffered output and deduplication
- [ ] Server detail view (tools list, logs, status)
- [ ] Proxy detail view (URL, upstreams, tools)
- [ ] Namespace detail view with tool permission grid

### Runtime / Reliability

- [ ] Force sane PATH for stdio transport (include Homebrew locations) to avoid "works in terminal, fails when launched" issues
- [ ] Retry/backoff for transient connection failures
- [ ] Tool discovery triggered automatically after MCP connect (cached, emits events)
- [ ] Graceful shutdown with proper cleanup

## Out of Scope (TUI simplification)

These features from the original are intentionally omitted for the TUI version:

- [ ] System tray / menubar UI (macOS-specific)
- [ ] Launch at login toggle (use OS mechanisms directly)
- [ ] Start minimized option
- [ ] Window show/hide behavior
- [ ] Theme support (TUI will use terminal colors)
