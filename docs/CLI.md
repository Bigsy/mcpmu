# CLI Reference

All commands support `--config` / `-c` to specify a custom config file path.

## Server management

```bash
# Add stdio server
mcpmu add <name> -- <command> [args...]
mcpmu add context7 -- npx -y @upstash/context7-mcp
mcpmu add my-server --env FOO=bar --cwd /path -- ./server --flag
mcpmu add auto-server --autostart -- ./server  # start on app launch

# Add HTTP server (Streamable HTTP / SSE)
mcpmu add atlassian https://mcp.atlassian.com/mcp --scopes read,write
mcpmu add my-api https://example.com/mcp --bearer-env API_TOKEN
mcpmu add slack https://mcp.slack.com/mcp --oauth-client-id 1601185624273.8899143856786 --oauth-callback-port 3118

# List, remove, rename
mcpmu list
mcpmu list --json
mcpmu remove <name> [--yes]
mcpmu rename <old-name> <new-name>
```

### Add flags

**HTTP-specific:**
- `--bearer-env` — env var containing bearer token
- `--scopes` — OAuth scopes (comma-separated; auto-discovered from server if omitted)
- `--oauth-client-id` — pre-registered OAuth client ID (skips dynamic registration)
- `--oauth-callback-port` — OAuth callback port (1-65535)

Note: `--bearer-env` and OAuth flags are mutually exclusive.

**General (stdio and HTTP):**
- `--autostart` — start server automatically on app launch
- `--startup-timeout` — startup timeout in seconds (default: 10)
- `--tool-timeout` — tool call timeout in seconds (default: 60)

## OAuth authentication

```bash
mcpmu mcp login <server>              # start OAuth flow in browser
mcpmu mcp login atlassian --scopes read,write  # explicit scopes
mcpmu mcp login slack                 # scopes auto-discovered from server metadata
mcpmu mcp logout <server>             # remove stored credentials
```

## Serve mode

```bash
mcpmu serve --stdio --namespace default
mcpmu serve --stdio -n work --log-level debug --eager
mcpmu serve --stdio --expose-manager-tools
mcpmu serve --stdio --resources --prompts
```

### Serve flags

- `--namespace` / `-n` — namespace to expose (default: auto-select)
- `--log-level` / `-l` — log level: debug, info, warn, error (default: info)
- `--eager` — pre-start all servers on init (default: lazy start)
- `--expose-manager-tools` — include mcpmu.* tools in tools/list (default: hidden)
- `--resources` — passthrough resources/* from upstream servers (default: off)
- `--prompts` — passthrough prompts/* from upstream servers (default: off)

When `--resources` is enabled, resource URIs are qualified as `serverName:originalUri`. When `--prompts` is enabled, prompt names are qualified as `serverName.promptName`.

## Namespace commands (alias: `ns`)

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

## Server-level global deny list

Deny tools at the server level for defense-in-depth. Globally denied tools are blocked regardless of namespace permissions.

```bash
mcpmu server deny-tool <server> <tool> [<tool>...]
mcpmu server allow-tool <server> <tool> [<tool>...]
mcpmu server denied-tools <server> [--json]
```

Permission resolution order: **server global deny > explicit tool permission > server default > namespace default > allow**.

## Permission commands

```bash
mcpmu permission list <namespace> [--json]
mcpmu permission set <namespace> <server> <tool> <allow|deny>
mcpmu permission unset <namespace> <server> <tool>
mcpmu permission set-server-default <namespace> <server> <deny|allow>
mcpmu permission unset-server-default <namespace> <server>
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
      "enabled": false,
      "deniedTools": ["delete_file", "move_file"]
    }
  }
}
```

### HTTP server (Streamable HTTP)
```json
{
  "servers": {
    "atlassian": {
      "url": "https://mcp.atlassian.com/mcp",
      "oauth": {
        "scopes": ["read", "write"]
      }
    }
  }
}
```

With pre-registered OAuth client (e.g. Slack — scopes auto-discovered from server):
```json
{
  "servers": {
    "slack": {
      "url": "https://mcp.slack.com/mcp",
      "oauth": {
        "client_id": "1601185624273.8899143856786",
        "callback_port": 3118
      }
    }
  }
}
```

With bearer token auth:
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
      }
    }
  }
}
```

### Config fields for HTTP servers

| Field | Description |
|-------|-------------|
| `url` | Server endpoint URL |
| `bearer_token_env_var` | Env var containing bearer token (mutually exclusive with `oauth`) |
| `http_headers` | Static headers to include in all requests |
| `env_http_headers` | Headers sourced from env vars (header name -> env var name) |
| `oauth.client_id` | Pre-registered OAuth client ID (skips dynamic registration) |
| `oauth.client_secret` | OAuth client secret (for confidential clients) |
| `oauth.callback_port` | Per-server OAuth callback port (overrides global) |
| `oauth.scopes` | OAuth scopes to request (auto-discovered from server if omitted) |
| `startup_timeout_sec` | Connection timeout (default: 10) |
| `tool_timeout_sec` | Tool call timeout (default: 60) |

### Global config fields

| Field | Description |
|-------|-------------|
| `mcp_oauth_credentials_store` | Where to store OAuth tokens: `"auto"`, `"keyring"`, or `"file"` (default: auto) |
| `mcp_oauth_callback_port` | Port for the OAuth callback server (default: auto-assigned) |