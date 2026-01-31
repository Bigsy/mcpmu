# mcpmu (μ)

**A multiplexing MCP server that aggregates multiple MCP servers behind a single endpoint.**

Unlike typical MCP setups where each coding agent needs its own server configurations, mcpmu acts as a meta-server: you configure all your MCP servers once, then expose them as a unified endpoint to any agent that supports the Model Context Protocol. Add one entry to Claude Code, Cursor, Windsurf, or any MCP-compatible tool and instantly gain access to your entire MCP ecosystem.

Key differentiators:
- **Single configuration, universal access** — Define servers once, use everywhere
- **Namespace profiles** — Group servers by context (work, personal, project) with per-namespace tool permissions
- **Multi-transport** — Manage both local stdio processes and remote HTTP/SSE endpoints
- **Interactive TUI** — Monitor, test, and manage servers with a terminal interface

## Features

- **Bearer token auth** — Authenticate to HTTP servers via environment variables
- **Custom headers** — Add static or env-sourced HTTP headers
- **OAuth support** — Built-in OAuth flow with configurable scopes
- **Tool permissions** — Allow/deny specific tools per namespace

<img width="798" height="560" alt="image" src="https://github.com/user-attachments/assets/d7eb8ef0-6249-43e6-9019-8b4ee07a23d7" />


<img width="797" height="561" alt="image" src="https://github.com/user-attachments/assets/a7cab86e-e006-4a17-bd7e-4c9d2bd7a9ed" />


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

Keybindings:
- `t` start/stop server (test toggle)
- `e` enable/disable server
- `l` toggle log panel
- `f` toggle follow mode (logs)
- `?` help overlay
- `Enter` view details
- `Esc` go back

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

**List and remove:**
```bash
mcpmu list
mcpmu list --json

mcpmu remove <name>
mcpmu remove <name> --yes  # skip confirmation
```

### OAuth authentication

```bash
mcpmu mcp login <server>              # start OAuth flow in browser
mcpmu mcp login atlassian --scopes read,write
mcpmu mcp logout <server>             # remove stored credentials
```

### Namespaces

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

Example entry for Claude Code:
```json
{
  "mcpmu": {
    "command": "mcpmu",
    "args": ["serve", "--stdio", "--config", "~/.config/mcpmu/config.json", "--namespace", "default"]
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
      "id": "filesystem",
      "name": "Filesystem",
      "kind": "stdio",
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"],
      "cwd": "",
      "env": {},
      "autostart": false
    }
  }
}
```

### HTTP server (Streamable HTTP)
```json
{
  "servers": {
    "figma": {
      "id": "figma",
      "name": "Figma",
      "kind": "streamable_http",
      "url": "https://mcp.figma.com/mcp",
      "bearer_token_env_var": "FIGMA_TOKEN",
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
| `bearer_token_env_var` | Env var containing bearer token |
| `http_headers` | Static headers to include in all requests |
| `env_http_headers` | Headers sourced from env vars (header name → env var name) |
| `scopes` | OAuth scopes to request |
| `startup_timeout_sec` | Connection timeout (default: 10) |
| `tool_timeout_sec` | Tool call timeout (default: 60) |

## Testing

```bash
go test ./...
```
