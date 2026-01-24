# Phase 5: SSE Client + OAuth

## Objective
Add SSE client transport for connecting TO remote MCP servers (not serving - that's deferred Phase 4), implement OAuth 2.1 browser-based authorization flow, and polish the application for release.

> **Note**: This phase adds SSE as a CLIENT transport - mcp-studio connecting to remote MCP servers that expose SSE endpoints. This is different from Phase 4 (deferred) which would have mcp-studio SERVE via HTTP/SSE.

## Features

### SSE Client Transport
- [ ] SSEClientTransport implementation
- [ ] Connect to remote MCP servers via SSE
- [ ] Server config fields for SSE:
  - URL (required)
  - Custom headers (optional)
  - OAuth enabled flag
- [ ] Reconnection with backoff on disconnect
- [ ] Handle server-sent events parsing

### OAuth 2.1 Flow
- [ ] OAuth config fields per server:
  - Client ID
  - Client Secret (encrypted storage)
  - Scopes
  - Authorization URL
  - Token URL
- [ ] Browser-based authorization:
  - Generate PKCE code verifier and challenge
  - Open system browser to authorization URL
  - Local callback server on `localhost:3333/oauth/callback`
  - Health endpoint at `/health`
- [ ] Token exchange after callback
- [ ] PKCE state expiry (10 minutes)

### Token Storage
- [ ] Encrypted storage using `go-keyring` (OS keychain)
- [ ] Fallback to AES-256-GCM encrypted file if keyring unavailable
- [ ] Machine-derived encryption key (machine ID + app salt)
- [ ] Store: access token, refresh token, expiry, scopes

### Token Refresh
- [ ] Monitor token expiry for connected SSE clients
- [ ] Auto-refresh tokens before expiry (5 minute window)
- [ ] Reconnect SSE transport after refresh
- [ ] Emit "token refreshed" event (success/failure)

### OAuth Status UI
- [ ] Status badges in server list:
  - Green: Authorized
  - Amber: Expiring Soon (< 5 min)
  - Red: Not Authorized / Expired
- [ ] "Authorize" button for unauthorized servers
- [ ] "Revoke" action to clear tokens

### Auth Gate on Server Start
- [ ] Check OAuth status before connecting
- [ ] Attempt token refresh if expired
- [ ] Fallback to full browser auth if refresh fails
- [ ] Block server start until authorized

### OAuth Server Form Fields
- [ ] OAuth toggle (enable/disable)
- [ ] Client ID input
- [ ] Client Secret input (masked, stored encrypted)
- [ ] Scopes input
- [ ] Authorization URL input
- [ ] Token URL input
- [ ] Validate OAuth fields when enabled

---

## Polish & Future Features

### Import/Export Configuration
- [ ] Export config as pretty-printed JSON
- [ ] Import config from JSON file
- [ ] Validation on import:
  - Schema version compatibility
  - Fix orphaned references (servers in namespaces that don't exist)
  - Report warnings/errors
- [ ] Auto-backup before import (keep last 10 backups)
- [ ] Backup location: `~/.config/mcp-studio/backups/`

### Autostart Queue (Enhanced)
- [ ] Track running servers via status events (continuous queue maintenance)
- [ ] On app exit: persist running server IDs, then stop gracefully
- [ ] On app launch: restore servers from autostart queue
- [ ] Handle start failures gracefully (don't block other servers)
- [ ] **Ordering/dependencies**: optional start order for dependent servers
- [ ] **Retry policy**: configurable retries with backoff on autostart failure

### Error Handling Polish
- [ ] Consistent error messages
- [ ] Error categorization (network, auth, config, etc.)
- [ ] Retry suggestions where applicable
- [ ] Error history/log viewer

### Uptime Tracking
- [ ] Track server uptime (time since last start)
- [ ] Display in server detail view
- [ ] Track app session uptime

### Performance Optimizations
- [ ] Lazy tool discovery (on-demand, not on connect)
- [ ] Tool cache invalidation strategy
- [ ] Efficient config file writes (debounce rapid changes)
- [ ] Memory profiling for log buffers

### UX Polish
- [ ] Loading indicators for async operations
- [ ] Keyboard shortcut overlay (?-key)
- [ ] Command palette (if feasible in TUI)
- [ ] Theme support via terminal colors (no custom themes)
- [ ] Mouse support for list navigation
- [ ] Responsive layout for terminal resize

## Dependencies
- Phase 3: Namespaces, tool permissions, TUI components
- Phase 4 (HTTP Proxy) is **NOT required** - that phase is deferred
- See [PLAN-ui.md](PLAN-ui.md) for OAuth badges, toasts, and polish specs

## Unknowns / Questions
1. **Keyring Availability**: What if keyring is unavailable (SSH, containers)? Fallback strategy?
2. **OAuth Discovery**: Should we support OAuth server metadata discovery? (RFC 8414)
3. **Multiple OAuth Providers**: Different servers with different OAuth configs - any shared state?
4. **Browser Opening**: How to open browser cross-platform? `open` (mac), `xdg-open` (linux), `start` (windows)?
5. **Headless Mode**: How to handle OAuth in SSH/tmux sessions? Fall back to manual URL copy?

## Risks
1. **OAuth Complexity**: OAuth flows have many edge cases. Need thorough testing with real providers.
2. **Keyring Compatibility**: OS keyring APIs differ. `go-keyring` may have platform-specific issues.
3. **Browser Callback**: Firewall may block localhost callback. Need clear error messaging.
4. **Token Security**: Encrypted storage is only as good as the key derivation. Machine ID may be predictable.
5. **Auth UX Failures**: Headless, no keyring, revoked refresh, clock skew - need graceful fallbacks.
6. **Config Migration**: Schema changes across phases may break imports. Version + migration strategy essential.

## Success Criteria
- Can connect to SSE MCP servers
- OAuth flow works end-to-end
- Tokens persist securely
- Token refresh works automatically
- Import/export works correctly
- Autostart restores previous state
- App feels polished and responsive

## Files to Create/Modify
```
internal/
  mcp/
    sse_transport.go    # SSE client transport
  oauth/
    oauth.go            # OAuth manager
    callback.go         # Callback server
    storage.go          # Token storage (keyring + file)
    provider.go         # OAuth provider interface
    pkce.go             # PKCE helpers
  config/
    import.go           # Config import logic
    export.go           # Config export logic
    backup.go           # Backup management
    migration.go        # Version migration
  tui/
    server_form.go      # Add OAuth fields
    oauth_badge.go      # Status badge component
    import_export.go    # Import/export dialogs
    loading.go          # Loading indicators
    keyboard_help.go    # Shortcut overlay
```

## Estimated Complexity
- SSE client: Medium
- OAuth flow: High
- Token storage: Medium-High
- Token refresh: Medium
- Import/export: Medium
- Polish items: Medium
- Total: ~3000-4000 lines of Go code (cumulative ~7300-9700, excluding deferred Phase 4)

## Post-Phase 5 / Future Considerations
- [ ] Plugin system for custom transports
- [ ] Multiple config profiles
- [ ] Remote config sync
- [ ] Metrics/telemetry (opt-in)
- [ ] Server health checks
- [ ] Tool invocation testing UI
