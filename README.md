# mcpmu

A TUI application and MCP server for managing MCP (Model Context Protocol) servers - both local stdio processes and remote HTTP endpoints.

## Features

- **Multi-transport support**: Stdio processes and Streamable HTTP (SSE) servers
- **TUI interface**: Interactive server management, logs, and namespaces
- **Namespace-based permissions**: Group servers and control tool access per namespace
- **MCP aggregation**: Expose all managed servers as a single MCP endpoint via `serve --stdio`
- **Bearer token auth**: Authenticate to HTTP servers via environment variables
- **Custom headers**: Add static or env-sourced HTTP headers
- **OAuth scopes**: Configure OAuth scopes for servers that support it

## Build & Run

```bash
go build -o mcpmu ./cmd/mcpmu
./mcpmu
./mcpmu --debug  # logs to /tmp/mcpmu-debug.log
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

### Server management

**Stdio servers:**
```bash
mcpmu add <name> -- <command> [args...]
mcpmu add context7 -- npx -y @upstash/context7-mcp
mcpmu add my-server --env FOO=bar --cwd /path -- ./server --flag
```

**HTTP servers (Streamable HTTP / SSE):**
```bash
# With bearer token from environment variable
mcpmu add figma --url https://mcp.figma.com/mcp --bearer-env FIGMA_TOKEN

# With OAuth scopes
mcpmu add atlassian --url https://mcp.atlassian.com/mcp --scopes read,write

# Plain HTTP (no auth)
mcpmu add my-api --url https://example.com/mcp
```

**Common commands:**
```bash
mcpmu list
mcpmu list --json

mcpmu remove <name>
mcpmu remove <name> --yes
```

Namespaces:
```bash
mcpmu namespace list
mcpmu namespace add <name> --description "My namespace"
mcpmu namespace remove <name>
mcpmu namespace assign <namespace> <server>
mcpmu namespace unassign <namespace> <server>
mcpmu namespace default <name>
```

Tool permissions:
```bash
mcpmu permission list <namespace>
mcpmu permission set <namespace> <server> <tool> <allow|deny>
mcpmu permission unset <namespace> <server> <tool>
```

## MCP Server Mode (stdio)

Run mcpmu as an MCP server so Claude/Codex can call tools from your managed servers:
```bash
mcpmu serve --stdio --config ~/.config/mcpmu/config.json --namespace default
```

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
