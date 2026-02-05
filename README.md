# mcpmu (μ)

**A multiplexing MCP server that aggregates multiple MCP servers behind a single endpoint.**

Unlike typical MCP setups where each coding agent needs its own server configurations, mcpmu acts as a meta-server: you configure all your MCP servers once, then expose them as a unified endpoint to any agent that supports the Model Context Protocol. Add one entry to Claude Code, Cursor, Windsurf, or any MCP-compatible tool and instantly gain access to your entire MCP ecosystem.

Key differentiators:
- **Single configuration, universal access** — Define servers once, use everywhere
- **Namespace profiles** — Group servers by context (work, personal, project) with per-namespace tool permissions
- **Multi-transport** — Manage both local stdio processes and remote HTTP/SSE endpoints
- **Interactive TUI** — Monitor, test, and manage servers with a terminal interface

## Features

- **Stdio process management** — Spawn and supervise local MCP servers (npx, binaries, scripts)
- **Streamable HTTP/SSE** — Connect to remote MCP endpoints with full SSE support
- **MCP aggregation** — Expose all managed servers as a single MCP endpoint via `mcpmu serve --stdio`
- **Namespace profiles** — Create isolated server groups (work, personal, project) with independent configurations
- **Tool permissions** — Allow/deny specific tools per namespace; deny-by-default mode available
- **Bearer token auth** — Authenticate to HTTP servers via environment variables
- **Custom headers** — Add static or env-sourced HTTP headers
- **OAuth support** — Full OAuth 2.1 with PKCE, dynamic client registration (RFC 7591), authorization server discovery (RFC 8414), protected resource metadata (RFC 9728), and token revocation (RFC 7009). Credentials stored in system keyring (preferred) or file. `bearer_token_env_var` takes precedence over OAuth when both are configured.
- **Hot-reload** — Serve mode watches the config file and automatically applies changes without restart
- **Interactive TUI** — Real-time logs, server status, start/stop controls, and namespace switching
- **Lazy or eager startup** — Start servers on-demand or pre-start everything at init

<img width="935" height="720" alt="image" src="https://github.com/user-attachments/assets/481cebb2-c3de-4c4b-8b01-81f43ab06c54" />
<img width="935" height="718" alt="image" src="https://github.com/user-attachments/assets/127f1ccd-4882-4676-876a-4f7cb067769e" />
<img width="933" height="718" alt="image" src="https://github.com/user-attachments/assets/9378dff4-14b1-49a1-bfe6-4c5a00d73bfc" />
<img width="932" height="715" alt="image" src="https://github.com/user-attachments/assets/763e8be4-e84c-45c9-9795-792769be7504" />


## Installation

### Homebrew (macOS/Linux)

```bash
brew tap Bigsy/mcpmu https://github.com/Bigsy/mcpmu
brew install mcpmu
```

### From source

```bash
go install github.com/Bigsy/mcpmu/cmd/mcpmu@latest
```

### Manual build

```bash
git clone https://github.com/Bigsy/mcpmu.git
cd mcpmu
go build -o mcpmu ./cmd/mcpmu
./mcpmu
```

## TUI Usage

**Global:** `q` quit, `?` help, `tab`/`shift+tab` cycle tabs, `1` servers, `2` namespaces, `esc` back, `ctrl+c` force quit

**Navigation:** `j`/`↓` down, `k`/`↑` up, `g` top, `G` bottom, `enter` select

**Server/Namespace actions:** `t` test (start/stop), `a` add, `e` edit, `d` delete, `c` copy, `E` enable/disable, `L` OAuth login

**Log panel:** `l` toggle, `f` follow, `w` wrap

**Namespace detail:** `s` assign servers, `p` permissions, `D` set default

## CLI Usage

All commands support `--config` / `-c` to specify a custom config file path.

### Server management

**Add stdio server:**
```bash
mcpmu add <name> -- <command> [args...]
mcpmu add context7 -- npx -y @upstash/context7-mcp
mcpmu add my-server --env FOO=bar --cwd /path -- ./server --flag
mcpmu add auto-server --autostart -- ./server  # start on app launch
```

**Add HTTP server (Streamable HTTP / SSE):**
```bash
# With bearer token from environment variable
mcpmu add figma https://mcp.figma.com/mcp --bearer-env FIGMA_TOKEN

# With OAuth scopes (login separately with `mcp login`)
mcpmu add atlassian https://mcp.atlassian.com/mcp --scopes read,write

# Plain HTTP (no auth)
mcpmu add my-api https://example.com/mcp
```

**List, remove, and rename:**
```bash
mcpmu list
mcpmu list --json

mcpmu remove <name>
mcpmu remove <name> --yes  # skip confirmation

mcpmu rename <old-name> <new-name>  # updates namespace and permission references
```

### OAuth authentication

```bash
mcpmu mcp login <server>              # start OAuth flow in browser
mcpmu mcp login atlassian --scopes read,write
mcpmu mcp logout <server>             # remove stored credentials
```

### Namespaces (alias: `ns`)

```bash
mcpmu namespace list
mcpmu namespace list --json
mcpmu namespace add <name> --description "My namespace"
mcpmu namespace remove <name>
mcpmu namespace remove <name> --yes

mcpmu namespace assign <namespace> <server>
mcpmu namespace unassign <namespace> <server>
mcpmu namespace default <name>
mcpmu namespace set-deny-default <namespace> <true|false>  # deny unconfigured tools
mcpmu namespace rename <old-name> <new-name>
```

### Tool permissions

```bash
mcpmu permission list <namespace>
mcpmu permission set <namespace> <server> <tool> <allow|deny>
mcpmu permission unset <namespace> <server> <tool>
```

## MCP Server Mode (stdio)

Run mcpmu as an MCP server so Claude/Codex can call tools from your managed servers:
```bash
mcpmu serve --stdio --namespace default
mcpmu serve -n work --log-level debug --eager  # pre-start all servers
```

**Serve flags:**
- `-n, --namespace` - namespace to expose (default: auto-select)
- `-l, --log-level` - log level: debug, info, warn, error (default: info)
- `--eager` - pre-start all servers on init (default: lazy start)
- `--expose-manager-tools` - include mcpmu.* management tools in tools/list

Serve mode watches the config file and hot-reloads on changes — add, remove, or reconfigure servers without restarting.

Example entry for Claude Code (minimal):
```json
{
  "mcpmu": {
    "command": "mcpmu",
    "args": ["serve", "--stdio"]
  }
}
```

With explicit namespace:
```json
{
  "mcpmu": {
    "command": "mcpmu",
    "args": ["serve", "--stdio", "-n", "work"]
  }
}
```

## Configuration

Default config path: `~/.config/mcpmu/config.json`

### Stdio server
```json
{
  "servers": {
    "filesystem": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"]
    }
  }
}
```

With optional fields:
```json
{
  "servers": {
    "myserver": {
      "command": "./server",
      "args": ["--flag"],
      "cwd": "/path/to/dir",
      "env": {"FOO": "bar"},
      "autostart": true,
      "enabled": false
    }
  }
}
```

### HTTP server (Streamable HTTP)
```json
{
  "servers": {
    "figma": {
      "url": "https://mcp.figma.com/mcp",
      "bearer_token_env_var": "FIGMA_TOKEN"
    }
  }
}
```

With optional fields:
```json
{
  "servers": {
    "myapi": {
      "url": "https://example.com/mcp",
      "bearer_token_env_var": "API_TOKEN",
      "http_headers": {
        "X-Custom-Header": "value"
      },
      "env_http_headers": {
        "X-Api-Key": "MY_API_KEY_ENV"
      },
      "scopes": ["read", "write"]
    }
  }
}
```

### Config fields for HTTP servers

| Field | Description |
|-------|-------------|
| `url` | Server endpoint URL |
| `bearer_token_env_var` | Env var containing bearer token (takes precedence over OAuth) |
| `http_headers` | Static headers to include in all requests |
| `env_http_headers` | Headers sourced from env vars (header name → env var name) |
| `scopes` | OAuth scopes to request |
| `oauth_client_id` | Override the OAuth client ID (skips dynamic registration) |
| `startup_timeout_sec` | Connection timeout (default: 10) |
| `tool_timeout_sec` | Tool call timeout (default: 60) |

### Global config fields

| Field | Description |
|-------|-------------|
| `mcp_oauth_credentials_store` | Where to store OAuth tokens: `"auto"`, `"keyring"`, or `"file"` (default: auto) |
| `mcp_oauth_callback_port` | Port for the OAuth callback server (default: auto-assigned) |

## Testing

```bash
go test ./...
make check            # lint + tests
make test-integration # integration tests
```
