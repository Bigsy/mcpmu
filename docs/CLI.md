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

# Add HTTP server with custom headers (Cloudflare Access, custom gateways, etc.)
mcpmu add searxng https://searxng-mcp.example.com/mcp \
  --header "CF-Access-Client-Id: <id>" \
  --header "CF-Access-Client-Secret: <secret>"

# Same, with the secret read from an env var instead of stored in config
mcpmu add searxng https://searxng-mcp.example.com/mcp \
  --header "CF-Access-Client-Id: <id>" \
  --env-header "CF-Access-Client-Secret: CF_ACCESS_CLIENT_SECRET"

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
- `--header` — custom HTTP header in `Name: Value` form, repeatable. Applied to every request. Stored verbatim in config.
- `--env-header` — HTTP header sourced from an env var, `Name: ENV_VAR` form, repeatable. Value is read from the named env var at request time, so secrets stay out of the config file.

Note: `--bearer-env` and OAuth flags are mutually exclusive. `--header` and `--env-header` are orthogonal — they stack on top of any auth mode (useful for Cloudflare Access in front of an OAuth server). A header name cannot appear in both flags.

**General (stdio and HTTP):**
- `--autostart` — start server automatically on app launch
- `--startup-timeout` — connection, initialization, and initial-discovery timeout in seconds (default: 10)
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
mcpmu serve --stdio --isolated
```

### Serve flags

- `--namespace` / `-n` — namespace to expose (default: auto-select)
- `--log-level` / `-l` — log level: debug, info, warn, error (default: info). Each
  level drops the lines below it: `error` keeps only failures and panics, `debug`
  adds `file:line` prefixes and MCP payload dumps.
- `--eager` — pre-start all servers on init (default: lazy start)
- `--expose-manager-tools` — include mcpmu.* tools in tools/list (default: hidden)
- `--resources` — passthrough resources/* from upstream servers (default: on)
- `--prompts` — passthrough prompts/* from upstream servers (default: on)
- `--isolated` — bypass shared-daemon mode for this process and run embedded (stdio only)

Resource URIs are passed through unmodified from upstream servers. Prompt names are qualified as `serverName.promptName`.

## HTTP serve mode

The same aggregation endpoint over the MCP Streamable HTTP transport (POST +
GET SSE) instead of stdio — one long-running foreground process that any number
of HTTP MCP clients can connect to. It never uses the shared daemon.

```bash
mcpmu serve --http                                    # 127.0.0.1:8081
mcpmu serve --http --addr 127.0.0.1:9090              # custom port
mcpmu serve --http --addr 0.0.0.0:8081 --token $TOK   # token mandatory off-loopback
mcpmu serve --http --session-idle-timeout 1h --allow-origin https://myapp.example
mcpmu serve --http --namespace work --eager           # the stdio flags apply too
```

`POST /mcp` uses the default namespace (the same auto-select as stdio);
`POST /mcp/{namespace}` selects one. `GET` on the same URL attaches the
standalone SSE stream that carries server-initiated notifications; `DELETE`
ends the session.

### HTTP serve flags

- `--http` — expose the endpoint over Streamable HTTP instead of stdio
- `--addr` — listen address (default: `127.0.0.1:8081`). Requests whose
  `Host` header does not name this machine are refused (DNS-rebinding
  defence): loopback and the bind address itself always work, and a wildcard
  bind accepts IP-literal hosts — connect by IP, or bind the specific
  address if clients must use a DNS name.
- `--token` — bearer token required on every request. Falls back to
  `MCPMU_SERVE_TOKEN` when the flag is absent; the flag wins when both are set.
  Mandatory for a non-loopback `--addr` — binding one without a token refuses to
  start, because serve-mode `tools/call` is arbitrary code execution.
- `--allow-origin` — extra allowed `Origin`, repeatable. Loopback and
  `localhost` origins are always allowed, as is an origin equal to the bind
  address itself (what a browser at this server sends on same-origin POSTs);
  everything else is rejected unless listed here. Behind a reverse proxy that
  forwards the client's original `Host` through, list the public origin here
  and both the `Host` and `Origin` checks accept it — a proxy that rewrites
  `Host` to the upstream address needs nothing extra.
- `--session-idle-timeout` — reap sessions with no client activity for this long
  (default: `30m`, `0` = never). A request being dispatched counts as activity
  for as long as it runs, so a long tool call is not reaped mid-flight.
  SSE keepalive writes deliberately do not count.

The four flags above require `--http`; passing one without it is an error.
`--isolated` is rejected with `--http` — there is no daemon to skip. For
per-session upstream instances use `"shared": false` on individual servers,
which is what session reaping keeps safe here (session count is process count).

TLS termination is out of scope; put a reverse proxy in front for network
deployments. If the proxy forwards the client's original `Host` header
instead of rewriting it to the upstream address, pass the public origin via
`--allow-origin` — the `Host` check refuses names it has not been told about.

## Shared daemon mode

On Unix, shared serve is enabled by default. The first `mcpmu serve` for a
canonical config path starts a detached daemon and connects its stdio through a
Unix-socket shim. Later serves for that config share the daemon's Core and
upstream processes. The pointer-shaped setting preserves absent versus explicit
false; use false as the global kill switch:

```json
{
  "daemonMode": false,
  "servers": {},
  "namespaces": {}
}
```

An absent or true `daemonMode` uses the shared daemon. `--isolated` always runs
only the calling serve embedded. Windows remains embedded.

The connect protocol verifies the executable content hash, protocol version,
and full canonical config path. Any connect, spawn, startup, or handshake
failure prints one warning and falls back to a working embedded serve. Relative,
symlinked, and not-yet-created config paths use the same canonicalization.

The daemon inherits the first spawner's working directory and environment.
Use absolute upstream `cwd` values and explicit `env` config; environment-backed
HTTP headers are likewise resolved from the daemon environment.

Servers share one upstream instance by default. Set `"shared": false` on a
stateful server—especially browser automation, a REPL, or an interpreter-style
server—to create one private instance per connected serve session:

```json
{
  "servers": {
    "playwright": {
      "command": "npx",
      "args": ["@playwright/mcp"],
      "shared": false
    }
  }
}
```

Private-instance discovery, notifications, logs, and `mcpmu.servers_*`
manager actions are scoped to the caller. The instance is stopped on session
disconnect. `shared` is optional and absent means shared. The TUI and web edit
forms preserve this config field but do not expose a control for it yet.

Shared servers also share their upstream login/authentication state and rate
limits. `mcpmu.servers_stop` on a shared server stops that instance for every
connected client; the next use lazily starts a fresh instance. With
`shared: false`, stop and restart manager actions affect only the caller's
private instance.

The daemon diagnostic control surface is hidden from casual command help:

```bash
mcpmu --config /path/to/config.json daemon run --foreground
mcpmu --config /path/to/config.json daemon status [--json]
mcpmu --config /path/to/config.json daemon stop
```

Without `--foreground`, daemon output goes to its per-config runtime log.
`status` and `stop` use the Unix control socket; if it is unavailable, they
accept pidfile state only after validating the full config path, process start
identity, and executable path. Windows continues to use embedded serve.

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
| `startup_timeout_sec` | Connection, initialization, and initial-discovery timeout (default: 10) |
| `tool_timeout_sec` | Tool call timeout (default: 60) |

### General server config fields

| Field | Description |
|-------|-------------|
| `shared` | Share one daemon upstream across serve sessions; absent/true is shared, false creates a private per-session instance |

### Global config fields

| Field | Description |
|-------|-------------|
| `daemonMode` | Unix shared-daemon serve mode; absent/true enables it, false is the global embedded-mode kill switch |
| `mcp_oauth_credentials_store` | Where to store OAuth tokens: `"auto"`, `"keyring"`, or `"file"` (default: auto) |
| `mcp_oauth_callback_port` | Port for the OAuth callback server (default: auto-assigned) |
