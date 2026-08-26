---
name: mcpmu
disable-model-invocation: true
description: Install, set up, and manage MCP servers using the mcpmu CLI. Use when the user wants to install mcpmu, register it as an MCP server, add/remove/list MCP servers, manage namespaces, set tool permissions, manage server-level denied tools, or expose servers via serve mode.
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

**With management tools (lets the agent add/remove servers via MCP):**
```bash
claude mcp add mcpmu -- mcpmu serve --stdio --expose-manager-tools
```

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

General flags (stdio and HTTP):
- `--autostart` — start server automatically on app launch
- `--startup-timeout` — startup timeout in seconds (default: 10)
- `--tool-timeout` — tool call timeout in seconds (default: 60)

Note: `--bearer-env` and OAuth flags (`--oauth-client-id`, `--scopes`, `--oauth-callback-port`) are mutually exclusive.
Note: `--header` / `--env-header` are orthogonal to auth mode — they stack on top of bearer or OAuth, useful for gateways like Cloudflare Access. A header name cannot appear in both flags.
Note: Most OAuth servers advertise supported scopes via metadata — `--scopes` is only needed when the server doesn't or you want to restrict the requested set.

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
mcpmu serve --http                           # same endpoint over Streamable HTTP (see below)
```

Flags:
- `-n, --namespace` — namespace to expose
- `--eager` — pre-start all servers (default: lazy/on-demand)
- `--expose-manager-tools` — include mcpmu.* tools in tools/list
- `-l, --log-level` — debug, info, warn, error
- `--isolated` — bypass the shared daemon for this serve process (stdio only)

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

Daemon diagnostics are available when needed:

```bash
mcpmu daemon status
mcpmu daemon stop
```

## Connecting mcpmu to Other Agents

For Claude Code registration, see "Registering mcpmu as an MCP Server" above.

For other agents:

**Codex:**
```bash
codex mcp add mcpmu -- mcpmu serve --stdio
```

**OpenCode** (global `~/.config/opencode/config.json` or project `opencode.json`):
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

For a namespace-specific entry:

**Codex:**
```bash
codex mcp add work -- mcpmu serve --stdio --namespace work
```

**OpenCode:**
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

**Cursor, Windsurf, etc.:**
```json
{
  "work": {
    "command": "mcpmu",
    "args": ["serve", "--stdio", "--namespace", "work"]
  }
}
```

## Interactive TUI

Run `mcpmu` with no arguments to open the terminal UI for visual server management, log monitoring, and namespace switching.

## Config

Config lives at `~/.config/mcpmu/config.json`. All commands support `--config` / `-c` to use a custom config path.

## Shell Completions

```bash
# zsh (Homebrew)
mcpmu completion zsh > "$(brew --prefix)/share/zsh/site-functions/_mcpmu"

# bash
mcpmu completion bash > /etc/bash_completion.d/mcpmu

# fish
mcpmu completion fish > ~/.config/fish/completions/mcpmu.fish
```
