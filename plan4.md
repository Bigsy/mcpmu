# Phase 4: Streamable HTTP Servers with OAuth

## Objective
Add support for remote MCP servers using Streamable HTTP transport (SSE is just the event stream), including OAuth 2.1 authentication flows for services like Atlassian, Figma, etc.

**Reference:** [OpenAI Codex MCP configuration](https://developers.openai.com/codex/mcp/)

source code https://github.com/openai/codex

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
```toml
[mcp_servers.context7]
command = "npx"
args = ["-y", "@upstash/context7-mcp"]
```

**Streamable HTTP Servers (new):**
```toml
[mcp_servers.figma]
url = "https://mcp.figma.com/mcp"
bearer_token_env_var = "FIGMA_OAUTH_TOKEN"

[mcp_servers.atlassian]
url = "https://mcp.atlassian.com/mcp"
# OAuth is detected dynamically; login is triggered via CLI/TUI
```

### Authentication Methods

1. **None** - No auth required (public servers)
2. **Bearer Token** - Token from environment variable
3. **Static Headers** - Custom HTTP headers
4. **OAuth 2.1** - Browser-based login flow with token refresh

### OAuth 2.1 Flow

Based on [MCP authorization spec](https://spec.modelcontextprotocol.io/specification/2025-03-26/basic/authorization/):

1. User initiates login (`mcp-studio mcp login <server>` or TUI button)
2. Start local HTTP callback server on configurable port
3. Open browser to server's authorization endpoint
4. User authenticates with the service (e.g., Atlassian)
5. Server redirects to callback with authorization code
6. Exchange code for access token (and refresh token)
7. Store tokens securely
8. Use access token for subsequent requests
9. Auto-refresh when token expires

### Credential Storage

Three modes (matching Codex):
- **auto** (default): Use keyring if available, fall back to file
- **keyring**: System keychain (macOS Keychain, Linux Secret Service)
- **file**: JSON file at `CODEX_HOME/.credentials.json` (or app equivalent)

---

## Config Schema Changes (Codex-compatible)

```go
type ServerConfig struct {
    // Existing fields
    ID        string
    Name      string
    Command   string            // For stdio
    Args      []string
    Env       map[string]string
    Cwd       string
    Enabled   *bool
    Autostart bool

    // New fields for Streamable HTTP servers
    URL              string            // Streamable HTTP server URL (mutually exclusive with Command)
    BearerTokenEnv   string            // Env var containing bearer token
    HTTPHeaders      map[string]string // Static headers
    EnvHTTPHeaders   map[string]string // Headers from env vars
    Scopes           []string          // Optional OAuth scopes

    // Timeouts
    StartupTimeout   int  // seconds, default 10 (map to startup_timeout_sec)
    ToolTimeout      int  // seconds, default 60 (map to tool_timeout_sec)
}

// Top-level config additions
type Config struct {
    // Existing
    Servers            map[string]ServerConfig // maps to [mcp_servers]
    Namespaces         []NamespaceConfig
    // ...

    // New (Codex-compatible names)
    MCPOAuthCredentialStore string // "auto", "keyring", "file" (mcp_oauth_credentials_store)
    MCPOAuthCallbackPort    int    // Fixed port for OAuth callback; unset = random, 0 invalid

    // Studio-specific (optional)
    StreamableHTTPIdleTimeout int  // milliseconds, default 300000
    StreamableHTTPMaxRetries  int  // default 5
}
```

---

## Implementation Plan

### 4.1 Streamable HTTP Transport (`internal/mcp/streamable_http_transport.go`)

- [ ] Implement Streamable HTTP (POST + GET streaming)
- [ ] Handle SSE framing for GET stream (do not rely on `bufio.Scanner` limits)
- [ ] Support `Mcp-Session-Id` + `Last-Event-ID` resume
- [ ] Implement reconnection with exponential backoff
- [ ] Support idle timeout and max retries
- [ ] Handle Content-Type negotiation (application/json vs text/event-stream)

**Streamable HTTP (SSE stream) format:**
```
event: message
data: {"jsonrpc":"2.0","id":1,"result":{...}}

event: message
data: {"jsonrpc":"2.0","method":"notifications/tools/list_changed"}
```

### 4.2 HTTP Headers & Bearer Auth

- [ ] Add `Authorization: Bearer <token>` header when `BearerTokenEnv` is set
- [ ] Add custom headers from `HTTPHeaders` and `EnvHTTPHeaders`
- [ ] Validate token presence before connecting

### 4.3 OAuth 2.1 Implementation (`internal/oauth/`)

- [ ] `server.go` - Local HTTP callback server
- [ ] `flow.go` - OAuth flow orchestration
- [ ] `tokens.go` - Token storage and refresh
- [ ] `keyring.go` - Keyring integration (go-keyring)
- [ ] `discovery.go` - OAuth metadata discovery from MCP server (RFC 8414 + MCP header)

**OAuth discovery:** MCP servers expose `/.well-known/oauth-authorization-server` with:
- `authorization_endpoint`
- `token_endpoint`
- `registration_endpoint` (for dynamic client registration)

Send `MCP-Protocol-Version: 2024-11-05` header during discovery.

### 4.4 CLI Commands

```bash
# Add streamable HTTP server
mcp-studio add figma --url https://mcp.figma.com/mcp --bearer-env FIGMA_TOKEN

# Add OAuth server (OAuth detected dynamically)
mcp-studio add atlassian --url https://mcp.atlassian.com/mcp

# OAuth login/logout
mcp-studio mcp login <server-name>
mcp-studio mcp logout <server-name>

# List with auth status
mcp-studio list
# NAME        TYPE    AUTH         STATUS
# context7    stdio   none         stopped
# figma       http    bearer       ready
# atlassian   http    oauth        logged-in
```

### 4.5 TUI Integration

- [ ] Show server type indicator (stdio vs http)
- [ ] Show auth status (none, bearer, oauth:logged-in, oauth:needs-login)
- [ ] Add "Login" action for OAuth servers (opens browser)
- [ ] Add "Logout" action to revoke tokens
- [ ] Handle OAuth callback while TUI is running (notification)

### 4.6 Process Supervisor Changes

- [ ] `internal/process/supervisor.go` - Detect server type from config
- [ ] For HTTP servers: create Streamable HTTP client instead of spawning process
- [ ] Manage Streamable HTTP connection lifecycle (connect, reconnect, disconnect)
- [ ] Emit same events (StatusChanged, ToolsUpdated, etc.)

---

## Dependencies

```go
// For keyring
"github.com/zalando/go-keyring"

// For OAuth PKCE
"golang.org/x/oauth2"

// Streamable HTTP can use standard library (net/http + bufio/reader)
```

---

## Security Considerations

1. **Token Storage**: Never store tokens in plain text config files
2. **PKCE**: Use Proof Key for Code Exchange for public clients
3. **Scopes**: Request minimal required scopes
4. **Token Refresh**: Handle refresh before expiry
5. **Revocation**: Support logout/token revocation
6. **Callback Server**: Only bind to localhost, use random port by default

---

## Testing

### Unit Tests
- [ ] Streamable HTTP event parsing (SSE framing)
- [ ] OAuth flow state machine
- [ ] Token storage/retrieval
- [ ] Header injection

### Integration Tests
- [ ] Mock Streamable HTTP server
- [ ] Mock OAuth server
- [ ] End-to-end OAuth flow (headless)

### Manual Testing
- [ ] Atlassian Rovo MCP server
- [ ] Figma MCP server
- [ ] Other OAuth-enabled MCP servers

---

## Success Criteria

- [ ] Can add Streamable HTTP servers via CLI and config file
- [ ] Bearer token auth works with env var
- [ ] OAuth login flow opens browser and completes
- [ ] Tokens stored securely in keyring or encrypted file
- [ ] Streamable HTTP connection maintains stable streaming
- [ ] Reconnection handles network interruptions
- [ ] TUI shows auth status and login actions
- [ ] Can call tools on remote Streamable HTTP servers

---

## Out of Scope (Phase 5: Proxies)

- Proxy mode (exposing aggregated servers via stdio)
- Server-to-server authentication
- Custom OAuth providers

---

## References

- [OpenAI Codex MCP Docs](https://developers.openai.com/codex/mcp/)
- [OpenAI Codex Config Reference](https://developers.openai.com/codex/config-reference/)
- [MCP Authorization Spec](https://spec.modelcontextprotocol.io/specification/2025-03-26/basic/authorization/)
- [Atlassian Rovo MCP Server](https://support.atlassian.com/atlassian-rovo-mcp-server/docs/authentication-and-authorization/)
- [Atlassian MCP GitHub](https://github.com/atlassian/atlassian-mcp-server)
