---
name: mcpmu
disable-model-invocation: true
description: Install, set up, and manage MCP servers using the mcpmu CLI. Use when the user wants to install mcpmu, register it as an MCP server, add/remove/list MCP servers, manage namespaces, set tool permissions, manage server-level denied tools, expose servers via serve mode (stdio or HTTP), run the web UI, or inspect usage metrics and failed tool calls.
allowed-tools: Bash(mcpmu *), Bash(brew *), Bash(go install *), Bash(claude mcp *), Bash(codex mcp *), Bash(which mcpmu), Bash(command -v mcpmu), Bash(curl http://127.0.0.1:*), Bash(curl http://localhost:*)
---

# mcpmu — MCP Server Manager

mcpmu is a multiplexing MCP server manager. You configure MCP servers once in mcpmu, then expose them as a single unified MCP endpoint to any agent (Claude Code, Codex, Cursor, Windsurf, etc.).

## Installing mcpmu

First check if mcpmu is already installed:
```bash
which mcpmu
```

If not installed, install via Homebrew (preferred) or Go:

**Homebrew (macOS/Linux):**
```bash
brew tap Bigsy/tap && brew install mcpmu
```

**From source (requires Go):**
```bash
go install github.com/Bigsy/mcpmu/cmd/mcpmu@latest
```

### Installing / updating this skill

The skill ships inside the mcpmu binary. Re-run install after upgrading mcpmu
to pick up the latest version:
```bash
mcpmu skill install     # ~/.agents/skills/mcpmu/ plus detected agents (~/.claude/skills, ~/.codex/skills)
mcpmu skill uninstall   # remove it from everywhere
```

## Registering mcpmu as an MCP Server

After installing mcpmu, register it so your agent can use all mcpmu-managed servers through a single endpoint.

**Claude Code:**
```bash
claude mcp add mcpmu -- mcpmu serve --stdio
```

**Codex:**
```bash
codex mcp add mcpmu -- mcpmu serve --stdio
```

**OpenCode** (global config `~/.config/opencode/config.json`, or project-level `opencode.json`):
```json
{
  "mcp": {
    "mcpmu": {
      "type": "local",
      "command": ["mcpmu", "serve", "--stdio"]
    }
  }
}
```

**Any MCP config JSON (Cursor, Windsurf, etc.):**
```json
{
  "mcpmu": {
    "command": "mcpmu",
    "args": ["serve", "--stdio"]
  }
}
```

**With a specific namespace:**
```bash
claude mcp add work -- mcpmu serve --stdio --namespace work
codex mcp add work -- mcpmu serve --stdio --namespace work
```

**OpenCode** (namespace-specific):
```json
{
  "mcp": {
    "work": {
      "type": "local",
      "command": ["mcpmu", "serve", "--stdio", "--namespace", "work"]
    }
  }
}
```

**With management tools (lets the agent inspect and start/stop servers via MCP):**
```bash
claude mcp add mcpmu -- mcpmu serve --stdio --expose-manager-tools
```

This adds `mcpmu.servers_list`, `mcpmu.servers_start`, `mcpmu.servers_stop`,
`mcpmu.servers_restart`, `mcpmu.server_logs` and `mcpmu.namespaces_list`. They
are runtime controls only — they cannot add, remove or edit servers,
namespaces or permissions; use the CLI for that.

**With a compressed tool surface (saves context on large namespaces):**
```bash
claude mcp add mcpmu -- mcpmu serve --stdio --compress medium
```

With `--compress`, `tools/list` returns only three wrapper tools —
`list_tools`, `get_tool_schema`, and `invoke_tool` — with a compact
one-line-per-tool listing embedded in `invoke_tool`'s description. The agent
fetches full schemas on demand and calls tools through `invoke_tool`, so it
only pays context for the schemas it actually uses. Levels: `low` (full
descriptions), `medium` (first sentence — recommended), `high` (argument names
only), `max` (tool names only). Works with `--http` too. mcpmu tool
permissions still apply to the real target tool; note that client-side
per-tool rules only ever see `invoke_tool`, so prefer mcpmu permissions when
compression is on.

Compression can also be stored on a namespace, so serve sessions compress
without the flag (an explicit `--compress` flag — including `--compress off` —
overrides it):
```bash
mcpmu namespace set-compression work medium
mcpmu namespace set-compression work off     # clear
```

You can verify the registration:
```bash
claude mcp list
codex mcp list
opencode mcp list
```

To remove mcpmu from an agent:
```bash
claude mcp remove mcpmu
codex mcp remove mcpmu
```
For OpenCode, remove the entry from the config JSON file.

### Scoped registration (Claude Code)

Claude Code supports different scopes for MCP server registration:

```bash
claude mcp add mcpmu --scope user -- mcpmu serve --stdio       # available in all projects
claude mcp add mcpmu --scope project -- mcpmu serve --stdio    # this project only
```

## Full Setup Walkthrough

To go from zero to a working mcpmu setup:

1. Install mcpmu: `brew tap Bigsy/tap && brew install mcpmu`
2. Add some MCP servers: `mcpmu add context7 -- npx -y @upstash/context7-mcp`
3. Optionally create a namespace: `mcpmu namespace add work --description "Work tools"`
4. Assign servers to it: `mcpmu namespace assign work context7`
5. Register with your agent:
   - Claude Code: `claude mcp add mcpmu -- mcpmu serve --stdio`
   - Codex: `codex mcp add mcpmu -- mcpmu serve --stdio`
   - OpenCode: add to `~/.config/opencode/config.json` (global) or `opencode.json` (project)
   - Others: add the JSON config entry shown above
6. Restart your agent — all mcpmu-managed tools are now available

## Adding Servers

### Stdio servers (local processes)
```bash
mcpmu add <name> -- <command> [args...]
```

Examples:
```bash
mcpmu add context7 -- npx -y @upstash/context7-mcp
mcpmu add filesystem -- npx -y @modelcontextprotocol/server-filesystem /tmp
mcpmu add my-server --env FOO=bar --cwd /path -- ./server --flag
mcpmu add auto-server --autostart -- ./server  # start on app launch
```

### HTTP servers (remote endpoints)
```bash
mcpmu add <name> <url> [flags]
```

Examples:
```bash
mcpmu add atlassian https://mcp.atlassian.com/mcp --scopes read,write
mcpmu add figma https://mcp.figma.com/mcp --bearer-env FIGMA_TOKEN
mcpmu add slack https://mcp.slack.com/mcp --oauth-client-id 1601185624273.8899143856786 --oauth-callback-port 3118  # scopes auto-discovered

# HTTP server fronted by Cloudflare Access — custom headers stack on top of any auth mode
mcpmu add searxng https://searxng-mcp.example.com/mcp \
  --header "CF-Access-Client-Id: <id>" \
  --env-header "CF-Access-Client-Secret: CF_ACCESS_CLIENT_SECRET"
```

Flags for HTTP servers:
- `--scopes` — OAuth scopes (comma-separated; auto-discovered from server if omitted)
- `--bearer-env` — env var containing bearer token
- `--oauth-client-id` — pre-registered OAuth client ID (skips dynamic registration)
- `--oauth-callback-port` — OAuth callback port (1-65535)
- `--header` — custom HTTP header in `Name: Value` form, repeatable. Sent on every request. Stored verbatim in config.
- `--env-header` — HTTP header sourced from an env var, `Name: ENV_VAR` form, repeatable. Value read at request time — use this for secrets so they stay out of the config file.

The URL can also be given as `--url <url>` instead of a positional argument.

General flags (stdio and HTTP):
- `--env KEY=VALUE` / `-e` — environment variable, repeatable
- `--cwd` — working directory (stdio; prefer absolute paths, see daemon notes)
- `--autostart` — start server automatically on app launch
- `--shared=<bool>` — share between agent connections (default: true); use `--shared=false` for a private instance per connection
- `--startup-timeout` — startup timeout in seconds (default: 10)
- `--tool-timeout` — tool call timeout in seconds (default: 60)
- `--elicitation` — relay the server's elicitation requests (forms, sign-in URLs) to the agent's client in serve mode; best with `--shared=false`. Toggle later with `mcpmu server set-client-feature <server> elicitation on|off`

Note: `--bearer-env` and OAuth flags (`--oauth-client-id`, `--scopes`, `--oauth-callback-port`) are mutually exclusive.
Note: `--header` / `--env-header` are orthogonal to auth mode — they stack on top of bearer or OAuth, useful for gateways like Cloudflare Access. A header name cannot appear in both flags.
Note: Most OAuth servers advertise supported scopes via metadata — `--scopes` is only needed when the server doesn't or you want to restrict the requested set.

### Finding servers in the official registry

The TUI and web UI can search `registry.modelcontextprotocol.io` and
pre-fill the add form with the install spec: in the TUI press `a` on the
server list and choose **Official Registry**; in the web UI open the
**Registry** page. There is no CLI search — use `mcpmu add` directly when you
already know the command or URL.

### OAuth login (for HTTP servers that need it)
```bash
mcpmu mcp login <server>
mcpmu mcp login atlassian --scopes read,write
mcpmu mcp login slack  # uses pre-registered client ID from config
mcpmu mcp logout <server>
```

## Listing and Managing Servers

```bash
mcpmu list              # human-readable list
mcpmu list --json       # JSON output
mcpmu remove <name>     # remove (prompts for confirmation)
mcpmu remove <name> --yes  # skip confirmation
mcpmu rename <old> <new>   # rename (updates namespace/permission refs)
```

### Disabling a server without removing it

Set `"enabled": false` on the server in the config (or press `E` in the TUI,
or use the web edit form). Disabled servers are never started and their
tools are hidden from serve mode. There is no CLI command for this.

## Namespaces

Namespaces group servers into profiles — e.g. work, personal, minimal. The `namespace` subcommand can also be shortened to `ns`.

```bash
mcpmu namespace add <name> --description "desc"
mcpmu namespace list [--json]
mcpmu namespace remove <name> [--yes]
mcpmu namespace assign <namespace> <server>
mcpmu namespace unassign <namespace> <server>
mcpmu namespace default <name>
mcpmu namespace rename <old> <new>
mcpmu namespace set-deny-default <namespace> <true|false>
mcpmu namespace set-compression <namespace> <level|off>
```

### Common namespace patterns

Create separate profiles:
```bash
mcpmu namespace add work --description "Work servers"
mcpmu namespace add personal --description "Personal projects"
mcpmu namespace assign work atlassian
mcpmu namespace assign work context7
mcpmu namespace assign personal context7
```

Create a minimal namespace that denies all tools by default, then allowlist:
```bash
mcpmu namespace add minimal --description "Lean toolset"
mcpmu namespace set-deny-default minimal true
mcpmu permission set minimal context7 resolve allow
```

## Tool Permissions

Control which tools each server exposes per namespace:

```bash
mcpmu permission list <namespace> [--json]
mcpmu permission set <namespace> <server> <tool> <allow|deny>
mcpmu permission unset <namespace> <server> <tool>
```

Examples:
```bash
mcpmu permission set work atlassian jira_search allow
mcpmu permission set work atlassian confluence_delete deny
```

### Per-server default within a namespace

Override the namespace's deny-by-default setting for one server's tools.
Explicit tool permissions still win:

```bash
mcpmu permission set-server-default <namespace> <server> <deny|allow>
mcpmu permission unset-server-default <namespace> <server>
```

Example — allow everything from a trusted server but deny a noisy one by
default, then allowlist one of its tools:
```bash
mcpmu permission set-server-default work context7 allow
mcpmu permission set-server-default work atlassian deny
mcpmu permission set work atlassian jira_search allow
```

### Server-level global deny list

For defense-in-depth, deny tools at the server level. Globally denied tools are blocked regardless of namespace permissions — even a namespace explicit allow cannot override a server global deny:

```bash
mcpmu server deny-tool <server> <tool> [<tool>...]
mcpmu server allow-tool <server> <tool> [<tool>...]
mcpmu server denied-tools <server> [--json]
```

Examples:
```bash
mcpmu server deny-tool filesystem delete_file move_file
mcpmu server allow-tool filesystem move_file   # re-enable
mcpmu server denied-tools filesystem           # list denied tools
```

Permission resolution order: **server global deny > explicit tool permission > server default > namespace default > allow**.

In the TUI, press `p` on the server detail pane to open an interactive deny list editor.

## Serve Mode

Expose managed servers as a single MCP endpoint:

```bash
mcpmu serve --stdio                          # default namespace
mcpmu serve --stdio --namespace work         # specific namespace
mcpmu serve --stdio -n work --eager          # pre-start all servers
mcpmu serve --stdio --expose-manager-tools   # include mcpmu.* management tools
mcpmu serve --stdio --log-level debug        # verbose logging
mcpmu serve --stdio --isolated               # private embedded serve
mcpmu serve --stdio --resources=false --prompts=false  # tools only
mcpmu serve --http                           # same endpoint over Streamable HTTP (see below)
```

Flags:
- `-n, --namespace` — namespace to expose
- `--eager` — pre-start all servers (default: lazy/on-demand)
- `--expose-manager-tools` — include mcpmu.* tools in tools/list
- `-l, --log-level` — debug, info, warn, error
- `--isolated` — bypass the shared daemon for this serve process (stdio only)
- `--resources` / `--prompts` — pass through upstream `resources/*` and
  `prompts/*` (both default on; set `=false` to expose tools only)
- `--compress <level|off>` — compressed tool surface (see above)

### What agents see from upstream servers

- **Instructions:** the `instructions` string each upstream returns from
  `initialize` is combined into mcpmu's own initialize result, one section per
  server, headed by the server name that prefixes its tools. Only shared
  upstreams that are *already running* at initialize contribute (initialize
  starts nothing), so a cold first session sees none — use `--eager` or
  `autostart` if an agent should always get a server's instructions.
- **Server-to-client requests:** mcpmu answers upstream `ping`. Elicitation
  (`elicitation/create`) is relayed to the client for servers opted in with
  `--elicitation` / `"clientFeatures": {"elicitation": true}`, prefixed with
  `[server] ` so the user sees who is asking. Routing is certain for
  `shared: false` servers and strong for HTTP upstreams that send the request
  on the call's own response stream. For other shared servers it is only
  relayed when exactly one call is in flight *and* the opt-in
  `elicitationSingleCallerHeuristic` (`{"stdio": true}` / `{"http": true}`, or
  `mcpmu serve --elicitation-heuristic on`) allows it. A request that cannot be
  routed, or that needs a mode the client did not declare, is answered
  `{"action":"cancel"}`. A tool's
  timeout pauses while it waits on the user (bounded by
  `interaction_timeout_sec`, default 600, and `interaction_budget_sec`,
  default 1800). Other server requests get method-not-found.
- **Upstream errors** pass through with their code and data intact, so a
  `URLElicitationRequiredError` (`-32042`) reaches the client with its
  sign-in URLs.
- Cancellation and progress notifications are relayed in both directions.

### HTTP serve mode

`mcpmu serve --http` exposes the same endpoint over the MCP Streamable HTTP
transport (POST + SSE) instead of stdio — one long-running foreground process
that any number of HTTP MCP clients can connect to. Each namespace gets its
own URL from the one process: `POST /mcp` is the default namespace,
`POST /mcp/{namespace}` selects one.

```bash
mcpmu serve --http                                    # 127.0.0.1:8081
mcpmu serve --http --addr 127.0.0.1:9090              # custom port
mcpmu serve --http --addr 0.0.0.0:8081 --token $TOK   # token mandatory off-loopback
mcpmu serve --http --session-idle-timeout 1h --allow-origin https://myapp.example
mcpmu serve --http --namespace work --eager           # stdio serve flags apply too
```

HTTP-only flags (each requires `--http`):
- `--addr` — listen address (default: `127.0.0.1:8081`; the web UI owns 8080)
- `--token` — bearer token required on every request (falls back to the
  `MCPMU_SERVE_TOKEN` env var; the flag wins). Mandatory for a non-loopback
  `--addr` — binding one without a token refuses to start. Loopback binds may
  run tokenless.
- `--allow-origin` — extra allowed `Origin`, repeatable (loopback origins are
  always allowed)
- `--session-idle-timeout` — reap sessions idle for this long (default `30m`,
  `0` = never); an in-flight tool call counts as activity, so long calls are
  never cut off

Notes:
- The process must stay running — launch it yourself (or via a process
  manager); agents connect to it rather than spawning it.
- `--isolated` is rejected with `--http` (there is no daemon to skip). For
  per-session upstream instances use `"shared": false` on individual servers.
- Health check: `curl http://127.0.0.1:8081/healthz` answers
  `ok mcpmu <version>` without authentication.
- TLS termination is out of scope — put a reverse proxy in front for network
  deployments.

Register the HTTP endpoint with an agent:

**Claude Code:**
```bash
claude mcp add --transport http mcpmu http://127.0.0.1:8081/mcp
claude mcp add --transport http work http://127.0.0.1:8081/mcp/work
# with a token:
claude mcp add --transport http mcpmu http://127.0.0.1:8081/mcp \
  --header "Authorization: Bearer <token>"
```

**Any MCP config JSON that supports HTTP servers:**
```json
{
  "mcpmu": {
    "url": "http://127.0.0.1:8081/mcp/work",
    "headers": { "Authorization": "Bearer <token>" }
  }
}
```

### Shared daemon behavior

On Unix, concurrent serves for the same config share one daemon and one
instance of each upstream server by default. Windows stays embedded. Set
top-level `"daemonMode": false` to disable the daemon globally, or use
`--isolated` for one private serve.

The daemon inherits the first spawner's working directory and environment, so
prefer absolute server `cwd` values and explicit `env` config. Shared servers
also share login state and upstream rate limits. Stateful servers such as
browser automation, REPLs, and interpreter sessions should use
`"shared": false` in that server's config; they then get one instance per
connected serve session.

`mcpmu.servers_stop` stops a shared instance for every connected client and
the next use starts it again. For `shared: false`, manager start/stop/restart
actions affect only the caller's instance.

To mark a server as stateful, use `mcpmu add --shared=false ...`, or untick
**Share between agent connections** in the TUI/web server form.

The `daemon` command is hidden from `--help`; it is for diagnostics only
(prefer `mcpmu status`). `daemon stop` drains connected sessions for up to 30
seconds, then cancels them and stops every shared upstream — agents using it
lose their connection until they restart their serve. The daemon also exits
on its own 60 seconds after the last session disconnects:

```bash
mcpmu daemon status
mcpmu daemon stop
```

## Interactive TUI

Run `mcpmu` (or `mcpmu tui`) to open the terminal UI for server management,
log monitoring, namespaces, permissions and the registry browser. `--debug`
logs to `/tmp/mcpmu-debug.log`.

## Web UI

`mcpmu web` serves a browser UI for the same management tasks, plus live log
streaming, the registry browser and the Metrics page.

```bash
mcpmu web                                    # http://127.0.0.1:8080
mcpmu web --addr 127.0.0.1:3000              # custom port
mcpmu web --addr 0.0.0.0:8080 --token $TOK   # token mandatory off-loopback
mcpmu web --allow-origin https://mcpmu.example  # behind a reverse proxy
```

- `--token` falls back to `MCPMU_WEB_TOKEN`. A non-loopback `--addr` without a
  token refuses to start.
- The TUI and web UI are mutually exclusive managers (a `manager.lock` next to
  the config prevents running both). `mcpmu serve` is unaffected by the lock.
- Their start/stop/test controls use their own supervisor, separate from the
  daemon that serves agents: a server stopped in the UI may still be running
  for an agent.

## Usage Metrics and Failed Tool Calls

Serve mode records one sample per tool call — server, tool, namespace,
outcome and latency; never arguments or results by default. Samples flush
every ~30s to `metrics.json` next to the config. View them on the web UI's
**Metrics** page (call counts, error rates, latency, unused tools) or as JSON:

```bash
curl http://127.0.0.1:8080/api/metrics    # while `mcpmu web` is running
```

Click a tool's error count on the Metrics page to see failed responses
(structured MCP errors and transport diagnostics), filterable by namespace and
time window. Error responses are kept for 60 days.

Config knobs:
```json
{
  "metrics": { "enabled": true, "retentionDays": 60 },
  "servers": {
    "my-server": { "recordErrorInputs": true }
  }
}
```

- `metrics.enabled` — on by default; set `false` to stop collection.
- `metrics.retentionDays` — how long daily counters are kept (default 60).
- `recordErrorInputs` (per server, default off) — also store the *inputs* of
  failed calls, with common secret fields redacted recursively. Free text is
  not redacted, so leave it off for servers that handle sensitive data. Also
  available as **Record inputs for failed tool calls** in the edit forms.

## Config

Config lives at `~/.config/mcpmu/config.json`. All commands support `--config` / `-c` to use a custom config path.

Config edits (CLI, UI or by hand) hot-reload into running serves. Permission
and compression changes keep upstream instances running; runtime changes
(command, env, URL, headers…) restart only the affected servers; metrics and
other global settings trigger a full reload. An invalid hand edit is ignored
and the last valid config stays in effect (the web UI shows a warning until
it is fixed).

## Shell Completions

```bash
# zsh (Homebrew)
mcpmu completion zsh > "$(brew --prefix)/share/zsh/site-functions/_mcpmu"

# bash
mcpmu completion bash > /etc/bash_completion.d/mcpmu

# fish
mcpmu completion fish > ~/.config/fish/completions/mcpmu.fish
```


## Diagnostics

```bash
mcpmu status [--json]   # resolved config, daemon state, running instances
mcpmu doctor [--json]   # config validity, executables, cwd, env vars referenced by HTTP headers/auth
```

Both are read-only: they never start servers, log in, or start the daemon.
`doctor` exits 1 on invalid config or a failed prerequisite; env checks show
variable names and outcomes, never values. Checks reflect the current shell's
environment, which may differ from what a running daemon inherited.
