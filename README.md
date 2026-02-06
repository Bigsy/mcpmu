# mcpmu (μ)

**A multiplexing MCP server that aggregates multiple MCP servers behind a single stdio MCP server.**

Unlike typical MCP setups where each coding agent needs its own server configurations, mcpmu acts as a meta-server: you configure all your MCP servers once, then expose them as a unified endpoint to any agent that supports the Model Context Protocol. Add one entry to Claude Code, Cursor, Windsurf, or any MCP-compatible tool and instantly gain access to your entire MCP ecosystem.

Key differentiators:
- **Single configuration, universal access** — Define servers once, use everywhere
- **Namespace profiles** — Group servers by context (work, personal, project) with per-namespace tool permissions
- **Multi-transport** — Manage both local stdio processes and remote HTTP/SSE endpoints
- **Interactive TUI** — Monitor, test, and manage servers with a terminal interface

<table>
  <tr>
    <td><img width="467" height="360" alt="image" src="https://github.com/user-attachments/assets/481cebb2-c3de-4c4b-8b01-81f43ab06c54" /></td>
    <td><img width="467" height="359" alt="image" src="https://github.com/user-attachments/assets/127f1ccd-4882-4676-876a-4f7cb067769e" /></td>
  </tr>
  <tr>
    <td><img width="467" height="359" alt="image" src="https://github.com/user-attachments/assets/9378dff4-14b1-49a1-bfe6-4c5a00d73bfc" /></td>
    <td><img width="466" height="358" alt="image" src="https://github.com/user-attachments/assets/763e8be4-e84c-45c9-9795-792769be7504" /></td>
  </tr>
</table>

## Installation

### Homebrew (macOS/Linux)

```bash
brew tap Bigsy/tap
brew install mcpmu
```

### From source

```bash
go install github.com/Bigsy/mcpmu/cmd/mcpmu@latest
```

## Quick Start

### 1. Add your MCP servers to mcpmu

```bash
# Add a stdio server
mcpmu add context7 -- npx -y @upstash/context7-mcp

# Add an HTTP server
mcpmu add figma https://mcp.figma.com/mcp --bearer-env FIGMA_TOKEN
```

### 2. Add mcpmu to your agent

**Claude Code:**
```bash
claude mcp add mcpmu -- mcpmu serve --stdio
```

**Codex:**
```bash
codex mcp add mcpmu -- mcpmu serve --stdio
```

**Or add directly to any MCP config JSON (Claude Code, Cursor, Windsurf, etc.):**
```json
{
  "mcpmu": {
    "command": "mcpmu",
    "args": ["serve", "--stdio"]
  }
}
```

That's it. Your agent now has access to all your configured MCP servers through a single endpoint.

## Namespaces

Namespaces let you create different server profiles — one for work, one for personal projects, a minimal one for keeping context length down.

```bash
# Create namespaces
mcpmu namespace add work --description "Work servers"
mcpmu namespace add personal --description "Personal projects"

# Assign servers to namespaces
mcpmu namespace assign work atlassian
mcpmu namespace assign work figma
mcpmu namespace assign personal context7
```

Then point each agent at the namespace it needs:

**Claude Code:**
```bash
claude mcp add work -- mcpmu serve --stdio --namespace work
```

**Codex:**
```bash
codex mcp add personal -- mcpmu serve --stdio --namespace personal
```

If no namespace is specified, mcpmu uses the default namespace (usually the first namespace created).

## Tool Permissions

Control which tools are exposed per namespace — useful for keeping context lean or restricting access:

```bash
# Allow/deny specific tools
mcpmu permission set work figma get_file allow
mcpmu permission set work figma delete_file deny

# Deny all tools by default, then allowlist what you need
mcpmu namespace set-deny-default minimal true
mcpmu permission set minimal context7 resolve allow
```

A common pattern: keep a lean namespace with only your most-used tools for everyday work, and an "extra" namespace with the full suite that you add as a second MCP server when needed.

## Features

- **Stdio process management** — Spawn and supervise local MCP servers (npx, binaries, scripts)
- **Streamable HTTP/SSE** — Connect to remote MCP endpoints with full SSE support
- **MCP aggregation** — Expose all managed servers as a single MCP endpoint via `mcpmu serve --stdio`
- **OAuth support** — Full OAuth 2.1 with PKCE, dynamic client registration, and token management
- **Hot-reload** — Serve mode watches the config file and automatically applies changes without restart
- **Lazy or eager startup** — Start servers on-demand or pre-start everything with `--eager`
- **Interactive TUI** — Real-time logs, server status, start/stop controls, and namespace switching

---

## CLI Reference

All commands support `--config` / `-c` to specify a custom config file path.

### Server management

```bash
# Add stdio server
mcpmu add <name> -- <command> [args...]
mcpmu add context7 -- npx -y @upstash/context7-mcp
mcpmu add my-server --env FOO=bar --cwd /path -- ./server --flag
mcpmu add auto-server --autostart -- ./server  # start on app launch

# Add HTTP server (Streamable HTTP / SSE)
mcpmu add figma https://mcp.figma.com/mcp --bearer-env FIGMA_TOKEN
mcpmu add atlassian https://mcp.atlassian.com/mcp --scopes read,write
mcpmu add my-api https://example.com/mcp

# List, remove, rename
mcpmu list
mcpmu list --json
mcpmu remove <name> [--yes]
mcpmu rename <old-name> <new-name>
```

### OAuth authentication

```bash
mcpmu mcp login <server>              # start OAuth flow in browser
mcpmu mcp login atlassian --scopes read,write
mcpmu mcp logout <server>             # remove stored credentials
```

### Namespace commands (alias: `ns`)

```bash
mcpmu namespace list [--json]
mcpmu namespace add <name> --description "desc"
mcpmu namespace remove <name> [--yes]
mcpmu namespace assign <namespace> <server>
mcpmu namespace unassign <namespace> <server>
mcpmu namespace default <name>
mcpmu namespace set-deny-default <namespace> <true|false>
mcpmu namespace rename <old-name> <new-name>
```

### Permission commands

```bash
mcpmu permission list <namespace> [--json]
mcpmu permission set <namespace> <server> <tool> <allow|deny>
mcpmu permission unset <namespace> <server> <tool>
```

### Serve mode

```bash
mcpmu serve --stdio --namespace default
mcpmu serve -n work --log-level debug --eager
mcpmu serve --stdio --expose-manager-tools  # include mcpmu.* tools
```

**Flags:**
- `-n, --namespace` — namespace to expose (default: auto-select)
- `-l, --log-level` — debug, info, warn, error (default: info)
- `--eager` — pre-start all servers on init (default: lazy start)
- `--expose-manager-tools` — include mcpmu.* management tools in tools/list

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

## Building from source

```bash
git clone https://github.com/Bigsy/mcpmu.git
cd mcpmu
go build -o mcpmu ./cmd/mcpmu
./mcpmu
```

## Testing

```bash
go test ./...
make check            # lint + tests
make test-integration # integration tests
```