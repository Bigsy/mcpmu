# Phase 4: Streamable HTTP Servers with OAuth

## Objective
Add support for remote MCP servers using Streamable HTTP transport (SSE is just the event stream), including OAuth 2.1 authentication flows for services like Atlassian, Figma, etc.

**Reference:** [OpenAI Codex MCP configuration](https://developers.openai.com/codex/mcp/)

Source code: https://github.com/openai/codex

---

## Current State

MCP Studio currently only supports **stdio servers** (local processes). This phase adds:
- Streamable HTTP transport (POST + GET streaming; SSE is a sub-mechanism)
- Bearer token authentication
- OAuth 2.1 authentication with browser-based login flow (Codex-compatible)
- Secure credential storage

---

## Design

### Server Types

**Stdio Servers (existing):**
```json
{
  "servers": {
    "context7": {
      "command": "npx",
      "args": ["-y", "@upstash/context7-mcp"]
    }
  }
}
```

**Streamable HTTP Servers (new):**
```json
{
  "servers": {
    "figma": {
      "url": "https://mcp.figma.com/mcp",
      "bearer_token_env_var": "FIGMA_OAUTH_TOKEN"
    },
    "atlassian": {
      "url": "https://mcp.atlassian.com/mcp"
    }
  }
}
```

### Authentication Methods & Precedence

Auth is determined in this order (Codex-compatible):

1. **Bearer Token** - If `bearer_token_env_var` is set, use it. OAuth is NOT considered. Missing env var = **fail immediately** (no fallback).
2. **Stored OAuth** - If OAuth credentials exist in credential store, use them.
3. **OAuth Discovery** - Probe `/.well-known/oauth-authorization-server`. If supported, report "needs login".
4. **None** - No auth required (public servers).

### OAuth 2.1 Flow

Based on [MCP authorization spec](https://spec.modelcontextprotocol.io/specification/2025-03-26/basic/authorization/):

1. User initiates login (`mcp-studio mcp login <server>` or TUI button)
2. Discover OAuth metadata via RFC 8414 (`/.well-known/oauth-authorization-server`)
3. Start local HTTP callback server (random port by default, configurable)
4. Perform dynamic client registration if `registration_endpoint` available
5. Open browser to `authorization_endpoint` with PKCE
6. User authenticates with the service (e.g., Atlassian)
7. Server redirects to callback with authorization code
8. Exchange code for access token (and refresh token)
9. Store tokens + `client_id` securely (client_id from dynamic registration)
10. Use access token for subsequent requests
11. Auto-refresh 30 seconds before token expires

### Credential Storage

Three modes (matching Codex):
- **auto** (default): Use keyring if available, fall back to file
- **keyring**: System keychain (macOS Keychain, Linux Secret Service)
- **file**: JSON file at `~/.config/mcp-studio/.credentials.json`

**Credential entry format:**
```json
{
  "server_name": "atlassian",
  "server_url": "https://mcp.atlassian.com/mcp",
  "client_id": "dynamically-registered-client-id",
  "access_token": "...",
  "refresh_token": "...",
  "expires_at": 1234567890000,
  "scopes": ["read", "write"]
}
```

Note: `client_id` is stored because it comes from dynamic registration, not config.

---

## Config Schema Changes (Codex-compatible)

```go
// ServerKind represents the transport type for an MCP server.
type ServerKind string

const (
    ServerKindStdio          ServerKind = "stdio"
    ServerKindStreamableHTTP ServerKind = "streamable_http"
)

type ServerConfig struct {
    // Existing fields
    ID        string            `json:"id"`
    Name      string            `json:"name"`
    Kind      ServerKind        `json:"kind"`
    Command   string            `json:"command,omitempty"`   // stdio only
    Args      []string          `json:"args,omitempty"`      // stdio only
    Env       map[string]string `json:"env,omitempty"`
    Cwd       string            `json:"cwd,omitempty"`
    Enabled   *bool             `json:"enabled,omitempty"`
    Autostart bool              `json:"autostart,omitempty"`

    // Streamable HTTP fields (mutually exclusive with Command)
    URL               string            `json:"url,omitempty"`
    BearerTokenEnvVar string            `json:"bearer_token_env_var,omitempty"`
    HTTPHeaders       map[string]string `json:"http_headers,omitempty"`
    EnvHTTPHeaders    map[string]string `json:"env_http_headers,omitempty"`
    Scopes            []string          `json:"scopes,omitempty"` // OAuth scopes to request

    // Timeouts (seconds)
    StartupTimeoutSec int `json:"startup_timeout_sec,omitempty"` // default 10
    ToolTimeoutSec    int `json:"tool_timeout_sec,omitempty"`    // default 60
}

// Top-level config additions
type Config struct {
    // Existing
    SchemaVersion   int                       `json:"schemaVersion"`
    Servers         map[string]ServerConfig   `json:"servers"`
    Namespaces      []NamespaceConfig         `json:"namespaces,omitempty"`
    // ...

    // New (Codex-compatible names)
    MCPOAuthCredentialStore string `json:"mcp_oauth_credentials_store,omitempty"` // "auto", "keyring", "file"
    MCPOAuthCallbackPort    *int   `json:"mcp_oauth_callback_port,omitempty"`     // nil = random, 0 invalid
}
```

**Removed fields** (discovered dynamically via RFC 8414, not configured):
- ~~OAuthClientID~~
- ~~OAuthAuthURL~~
- ~~OAuthTokenURL~~
- ~~OAuthClientSecret~~

---

## Implementation Plan

### 4.1 Streamable HTTP Transport (`internal/mcp/streamable_http_transport.go`)

- [ ] Implement Streamable HTTP (POST for requests, GET for SSE stream)
- [ ] SSE parsing with proper handling:
  - Comment lines (`:`) - ignore
  - `id:` field - track for `Last-Event-ID` resume
  - `event:` field - typically "message"
  - `data:` field - can be multi-line (concat with `\n`)
  - Dispatch on blank line
  - **Do NOT use `bufio.Scanner` with default limits** - use incremental line parsing
  - Enforce max event size to prevent unbounded buffering
- [ ] Session resumption:
  - Persist `Mcp-Session-Id` from server response header
  - Send `Mcp-Session-Id` + `Last-Event-ID` on reconnect
  - If server rejects, drop state and re-initialize fresh
- [ ] Reconnection with exponential backoff
- [ ] Handle Content-Type negotiation (`application/json` vs `text/event-stream`)

**SSE format:**
```
id: 1
event: message
data: {"jsonrpc":"2.0","id":1,"result":{...}}

id: 2
event: message
data: {"jsonrpc":"2.0","method":"notifications/tools/list_changed"}
```

### 4.2 HTTP Headers & Bearer Auth

- [ ] Add `Authorization: Bearer <token>` when `bearer_token_env_var` is set
- [ ] Validate env var exists before connecting - **fail immediately if missing**
- [ ] Add static headers from `http_headers`
- [ ] Add headers from env vars via `env_http_headers`

### 4.3 OAuth 2.1 Implementation (`internal/oauth/`)

- [ ] `discovery.go` - RFC 8414 OAuth metadata discovery
  - Try paths: `/.well-known/oauth-authorization-server/<path>`, `/<path>/.well-known/oauth-authorization-server`, `/.well-known/oauth-authorization-server`
  - Send `MCP-Protocol-Version: 2024-11-05` header
  - Timeout: 5 seconds
  - Required fields: `authorization_endpoint`, `token_endpoint`
  - Optional: `registration_endpoint` (for dynamic client registration)
- [ ] `registration.go` - Dynamic client registration (RFC 7591)
- [ ] `server.go` - Local HTTP callback server
  - Bind to `127.0.0.1:0` by default (random port)
  - Use bound port in redirect URI
  - Reject explicit `0` in config
- [ ] `flow.go` - OAuth flow orchestration with PKCE
- [ ] `tokens.go` - Token storage and refresh
  - Refresh 30 seconds before expiry
  - Store `client_id` alongside tokens
- [ ] `keyring.go` - Keyring integration (`github.com/zalando/go-keyring`)
- [ ] `store.go` - Credential store abstraction (auto/keyring/file modes)

### 4.4 CLI Commands

```bash
# Add streamable HTTP server with bearer token
mcp-studio add figma --url https://mcp.figma.com/mcp --bearer-env FIGMA_TOKEN

# Add OAuth server (OAuth detected dynamically)
mcp-studio add atlassian --url https://mcp.atlassian.com/mcp

# Add with custom scopes
mcp-studio add atlassian --url https://mcp.atlassian.com/mcp --scopes read,write

# OAuth login/logout
mcp-studio mcp login <server-name>
mcp-studio mcp login <server-name> --scopes read,write
mcp-studio mcp logout <server-name>

# List with auth status
mcp-studio list
# NAME        TYPE              AUTH              STATUS
# context7    stdio             -                 stopped
# figma       streamable_http   bearer            ready
# atlassian   streamable_http   oauth:logged-in   ready
# github      streamable_http   oauth:needs-login disconnected
```

### 4.5 TUI Integration

- [ ] Show server type indicator (stdio vs streamable_http)
- [ ] Show auth status:
  - `-` (stdio, no auth)
  - `bearer` (bearer token configured)
  - `oauth:logged-in` (OAuth tokens present)
  - `oauth:needs-login` (OAuth supported but not logged in)
  - `oauth:expired` (refresh failed)
- [ ] Add "Login" action (`l` key) for OAuth servers - opens browser
- [ ] Add "Logout" action for OAuth servers
- [ ] Handle OAuth callback notification while TUI is running

### 4.6 Supervisor Changes (`internal/process/supervisor.go`)

- [ ] Detect server type from config (`url` present = streamable_http, else stdio)
- [ ] For HTTP servers: create `StreamableHTTPTransport` instead of spawning process
- [ ] Abstract `Handle` to work with both transport types (no PID for HTTP)
- [ ] Manage HTTP connection lifecycle (connect, reconnect, disconnect)
- [ ] Emit same events (StatusChanged, ToolsUpdated, etc.)

---

## Dependencies

```go
// For keyring
"github.com/zalando/go-keyring"

// Standard library for everything else
"net/http"
"crypto/rand" // for PKCE
"encoding/base64"
```

Note: Not using `golang.org/x/oauth2` - implementing PKCE flow directly for better control.

---

## Security Considerations

1. **Token Storage**: Never store tokens in config files - use keyring or separate credentials file
2. **PKCE**: Always use Proof Key for Code Exchange (S256 method)
3. **Scopes**: Request only scopes specified in config, or minimal defaults
4. **Token Refresh**: Refresh 30 seconds before expiry to avoid request failures
5. **Callback Server**: Only bind to `127.0.0.1`, random port by default
6. **Credential File**: Set 0600 permissions on Unix

---

## Testing

### Unit Tests
- [ ] SSE event parsing (multi-line data, id tracking, comments)
- [ ] OAuth discovery path resolution
- [ ] Token refresh timing logic
- [ ] Auth precedence (bearer > stored OAuth > discovery)
- [ ] Credential store (keyring mock, file)

### Integration Tests
- [ ] Mock Streamable HTTP server with SSE
- [ ] Mock OAuth authorization server
- [ ] Session resumption with `Last-Event-ID`

### Manual Testing
- [ ] Atlassian Rovo MCP server
- [ ] Figma MCP server (if available)
- [ ] Test bearer token + missing env var (should fail)
- [ ] Test OAuth login → logout → login cycle

---

## Success Criteria

- [ ] Can add Streamable HTTP servers via CLI and config file
- [ ] Bearer token auth works; missing env var fails immediately
- [ ] OAuth discovery detects supported servers
- [ ] OAuth login flow completes with browser
- [ ] Dynamic client registration works
- [ ] Tokens stored securely with `client_id`
- [ ] Token refresh happens automatically before expiry
- [ ] Session resumption works after disconnect
- [ ] TUI shows correct auth status
- [ ] `mcp login`/`mcp logout` commands work

---

## Out of Scope (Future)

- Proxy mode (exposing aggregated servers via stdio)
- Pre-registered OAuth clients (config-based `client_id`)
- Custom OAuth providers (non-MCP discovery)
- Token revocation endpoint

---

## References

- [OpenAI Codex MCP Docs](https://developers.openai.com/codex/mcp/)
- [OpenAI Codex Source](https://github.com/openai/codex) - specifically `codex-rs/rmcp-client/src/`
- [MCP Authorization Spec](https://spec.modelcontextprotocol.io/specification/2025-03-26/basic/authorization/)
- [RFC 8414 - OAuth Authorization Server Metadata](https://tools.ietf.org/html/rfc8414)
- [RFC 7591 - Dynamic Client Registration](https://tools.ietf.org/html/rfc7591)
- [Atlassian Rovo MCP Server](https://support.atlassian.com/atlassian-rovo-mcp-server/docs/authentication-and-authorization/)