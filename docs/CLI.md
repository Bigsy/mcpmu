# CLI Reference

All commands support `--config` / `-c` to specify a custom config file path.

For task-focused guidance, see [Tool permissions](permissions.md) and
[Tool-surface compression](compression.md).

## Read-only diagnostics

```bash
mcpmu status [--json]
mcpmu doctor [--json]
```

Status reports the resolved config path, whether missing-file defaults were used,
the daemon setting, availability, build compatibility when known, and the daemon's
running instance names. It does not inspect isolated or dedicated HTTP serves.
Doctor additionally checks local stdio executables, working directories and
referenced bearer/header environment variables, respecting server overrides.
Neither command starts a daemon/upstream, writes config, repairs runtime files,
queries credentials or performs OAuth. The daemon may have inherited a different
environment. TUI/web statuses refer only to their own management session.

JSON contains `configPath`, `defaultsUsed`, `configValid`, `scope`, `daemonEnabled`,
`daemonState`, `checks`, and optional `buildCompatible`/`daemon`/`clientFeatures`
(servers opted in to relayed client features, with the feature names and whether
the server is shared — see [Client features](#client-features-elicitation)). Each check has
`server`, `kind`, optional environment-variable `name`, and boolean `ok`.
Values of environment variables and headers are never printed. Exit 0 includes
an absent daemon and contextual availability warnings; invalid/unreadable config
or definite doctor prerequisite failures exit 1. An absent config uses empty defaults.

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
- `--shared=<bool>` — share between agent connections (default: true); use `--shared=false` for a private instance per connection
- `--startup-timeout` — connection, initialization, and initial-discovery timeout in seconds (default: 10)
- `--tool-timeout` — tool call timeout in seconds (default: 60)
- `--elicitation` — relay the server's elicitation requests to the client in serve mode (see [Client features](#client-features-elicitation)); best with `--shared=false`
- `--sampling` — relay the server's sampling requests to the client, spending its model tokens (see [Sampling](#sampling))
- `--root <path|uri>` — root reported to the server as its client's roots (absolute path or `file://` URI, repeatable; see [Roots](#roots))

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
mcpmu serve --stdio --compress medium
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
- `--compress` — compressed tool surface: `tools/list` returns only three wrapper
  tools (`list_tools`, `get_tool_schema`, `invoke_tool`) with a compact
  one-line-per-tool listing embedded in `invoke_tool`'s description, so the
  client only pays context for the schemas it actually fetches. Levels control
  how much each listing line carries: `low` (full description), `medium` (first
  sentence — recommended), `high` (argument names only), `max` (tool names only).
  Off by default. Applies to `--stdio` and `--http` identically. mcpmu-side
  permissions and usage metrics are enforced/recorded against the real target
  tool; note that client-side per-tool allow/deny rules only ever see
  `invoke_tool`, so use mcpmu permissions to restrict tools in this mode.
  Compression can also be configured per namespace with
  `mcpmu namespace set-compression` (see Namespaces below); the flag overrides
  that setting in both directions — a level replaces the configured one, and an
  explicit `--compress off` forces compression off. With no flag, the session
  follows its active namespace's configured level, including across hot config
  reloads.
- `--elicitation-heuristic on|off` — for this session, override the config's
  `elicitationSingleCallerHeuristic` switch for its transport (see
  [Client features](#client-features-elicitation)). Travels to the shared
  daemon in the session handshake.

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
mcpmu namespace set-compression <namespace> <level|off>
mcpmu namespace rename <old-name> <new-name>
```

`set-compression` stores a serve-mode compressed-tool-surface level on the
namespace (`low`, `medium`, `high`, `max`; `off` clears it) — serve sessions on
that namespace compress without needing `--compress`, and an explicit
`--compress` flag overrides the stored level. The level shows in
`namespace list` (COMPRESS column, `compression` JSON field).

## Server-level global deny list

Deny tools at the server level for defense-in-depth. Globally denied tools are blocked regardless of namespace permissions.

```bash
mcpmu server deny-tool <server> <tool> [<tool>...]
mcpmu server allow-tool <server> <tool> [<tool>...]
mcpmu server denied-tools <server> [--json]
```

Permission resolution order: **server global deny > explicit tool permission > server default > namespace default > allow**.

## Client features (elicitation)

An upstream server can ask the client something mid-call — an
`elicitation/create` form ("Proceed?"), or a URL to open for sign-in. mcpmu
relays these only for servers that opt in, and only to the client it can prove
the request belongs to:

```bash
mcpmu add browser --shared=false --elicitation -- browser-mcp
mcpmu server set-client-feature <server> elicitation <on|off>
```

- **Opt in per server** (`clientFeatures.elicitation`, the `--elicitation` add
  flag, or the TUI/web server form). mcpmu then declares `elicitation`
  (form and URL mode) to that server at initialize. Changing it restarts the
  instance.
- **Routing.** mcpmu relays a request only to a session it has evidence for,
  tried in this order:
  1. **Private instance** (`"shared": false`): exactly one owning session.
     Certain — set `"shared": false` when you need determinism.
  2. **HTTP upstream, request on a POST response stream**: the request that
     opened the stream is the cause. Strong evidence (the spec says such
     messages *should* relate to that request).
  3. **Single caller** (opt-in): a shared instance with exactly one call in
     flight. Only a heuristic — a request left over from an earlier call looks
     the same, and a wrong route hands one agent's question (and its user's
     answer) to another. Off by default, with separate switches for
     stdio/daemon sessions and for `serve --http` sessions (which may belong to
     different people): `"elicitationSingleCallerHeuristic": {"stdio": true,
     "http": false}`, or `--elicitation-heuristic on|off` per serve process.

  Anything else — several callers on a shared stdio instance, no caller at all
  — is not routed. A request that cannot be routed, or whose client did not
  declare the mode it needs (URL mode for a client that only declared form), is
  answered `{"action": "cancel"}` — a normal result every server must handle.
- **Identification.** The client only knows it is talking to mcpmu, so the
  request's `message` is prefixed with `[server] `. URL-mode elicitation ids are
  rewritten into mcpmu's own id space (the URL itself is untouched), and the
  server's later `notifications/elicitation/complete` reaches only the client
  that was shown the elicitation, under the rewritten id. The same applies to
  the elicitations listed in a `URLElicitationRequiredError` (`-32042`), which
  passes through with its code and data intact.
- **Timeouts.** A call's tool timeout pauses while it waits on the user. Each
  interaction is bounded by `interaction_timeout_sec` (default 600), the total
  time one call may spend paused by `interaction_budget_sec` (default 1800),
  and a call can never outlive its tool timeout plus that budget. Both are
  global and overridable per server; editing them restarts nothing.
- **Cancellation.** If the server withdraws its request, the client's copy is
  withdrawn with `notifications/cancelled`. If the client cancels its
  `tools/call`, the elicitation is withdrawn and the server gets `cancel`. To
  refuse an elicitation, the client answers `action: "cancel"`.

Outcomes (accepted, declined, cancelled, or why the fallback was sent) are
counted per server in the usage metrics; the request's content and the user's
answer are never recorded.

## Sampling

`sampling/createMessage` lets a server use the client's model — and spend its
tokens — so it is opt-in per server and routed only on strong evidence:

```bash
mcpmu add agent --shared=false --sampling -- agent-mcp
mcpmu server set-client-feature agent sampling on
mcpmu server set-client-feature agent sampling-tools on   # also allow tool use
```

- Declared upstream only for opted-in servers (`clientFeatures.sampling`);
  `sampling.tools` only with `clientFeatures.samplingTools`.
- Relayed only from a private instance or when an HTTP upstream sends the
  request on the call's own response stream — never by the single-caller
  heuristic, whatever its setting.
- Never sent to a client that did not declare `sampling`; a request that
  offers the model tools additionally needs the client's `sampling.tools`.
- The requesting server is named in the request's `_meta`
  (`"mcpmu/server": "<name>"`); the messages and system prompt the model sees
  are forwarded untouched.
- Sampling has no "cancel" action, so a request that cannot be relayed or
  answered gets a JSON-RPC error saying why.

## Roots

Roots tell a server which directories it may work in. They are per-client
state, so mcpmu does not relay one client's roots to an instance several
clients share; instead a server's roots are configured on the server and mcpmu
answers its `roots/list` itself:

```bash
mcpmu add filesystem --root ~/src/app --root /srv/data -- npx -y @modelcontextprotocol/server-filesystem
mcpmu server set-roots filesystem ~/src/app       # replace the list
mcpmu server set-roots filesystem                 # clear it
```

- With roots set, mcpmu declares `roots: {listChanged: true}` to every
  instance of the server, shared or private.
- Editing a non-empty list sends `notifications/roots/list_changed` to running
  instances, which ask again; nothing restarts. Adding or clearing the list
  changes the declared capability, so running instances restart.
- Roots are server-level only: shared instances serve every namespace, so a
  namespace-level list could not be authoritative for them.
- For a private (`"shared": false`) server with no configured roots, opting in
  to `clientFeatures.roots` (`mcpmu server set-client-feature <server> roots on`)
  relays `roots/list` to the instance's owning client instead, and forwards
  that client's `notifications/roots/list_changed`. Configured roots always win.

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
| `clientFeatures` | Relayed client features the server opts in to: `elicitation` (see [Client features](#client-features-elicitation)), `sampling` and `samplingTools` (see [Sampling](#sampling)), and `roots` (relay a private instance's owning client's roots; see [Roots](#roots)) |
| `roots` | `file://` URIs reported to the server as its roots; mcpmu answers `roots/list` (see [Roots](#roots)) |
| `interaction_timeout_sec` | Per-server override of the global relayed-interaction timeout |
| `interaction_budget_sec` | Per-server override of the global per-call interaction budget |

### Global config fields

| Field | Description |
|-------|-------------|
| `daemonMode` | Unix shared-daemon serve mode; absent/true enables it, false is the global embedded-mode kill switch |
| `interaction_timeout_sec` | How long one relayed interaction (an elicitation waiting on the user) may take (default: 600) |
| `interaction_budget_sec` | Total time one call may spend paused on relayed interactions; its hard lifetime is the tool timeout plus this (default: 1800) |
| `elicitationSingleCallerHeuristic` | `{"stdio": bool, "http": bool}` — route a shared server's elicitation to the session of its only in-flight call, per downstream transport (default: both off) |
| `mcp_oauth_credentials_store` | Where to store OAuth tokens: `"auto"`, `"keyring"`, or `"file"` (default: auto) |
| `mcp_oauth_callback_port` | Port for the OAuth callback server (default: auto-assigned) |
