<p align="center">
  <img src="docs/mcp-icon.png" alt="mcpmu" width="120">
</p>

<h1 align="center">mcpmu (μ)</h1>

<p align="center"><strong>A multiplexing MCP gateway that exposes multiple MCP servers through unified stdio or Streamable HTTP endpoints.</strong></p>

Unlike typical MCP setups where each coding agent needs its own server configurations, mcpmu acts as a meta-server: you configure all your MCP servers once, then expose them as a unified endpoint to any agent that supports the Model Context Protocol. Add one entry to Claude Code, Cursor, Windsurf, or any MCP-compatible tool and instantly gain access to your entire MCP ecosystem.

Key differentiators:
- **Single configuration, universal access** — Define servers once, use everywhere
- **Namespace profiles** — Group servers by context (work, personal, project) with per-namespace tool permissions
- **Flexible upstreams** — Manage both local stdio processes and remote Streamable HTTP/SSE servers
- **Flexible endpoints** — Connect clients over stdio or Streamable HTTP, with one endpoint per namespace
- **Registry browser** — Search the official MCP registry and install servers with pre-populated config
- **Interactive TUI** — Monitor, test, and manage servers with a terminal interface
- **Tool permissions** — Block unused tools per-namespace or globally deny dangerous tools at the server level
- **Defense-in-depth** — Server-level global deny list that overrides all namespace permissions
- **Resource & prompt passthrough** — Optionally expose upstream resources and prompts via `--resources` and `--prompts` flags
- **Metadata-faithful** — Tool annotations, output schemas, and result `structuredContent` survive the proxy hop, so `readOnlyHint`-based auto-approve keeps working


### TUI

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

### Web

<table>
  <tr>
    <td><img width="467" height="313" alt="Web UI servers" src="https://github.com/user-attachments/assets/6a9d7ea3-29db-4ab9-b14f-21524e05ded1" /></td>
    <td><img width="467" height="313" alt="Web UI server detail" src="https://github.com/user-attachments/assets/7ec8f3b1-7bab-4433-a21d-9b720baaf737" /></td>
  </tr>
  <tr>
    <td><img width="467" height="313" alt="Web UI namespaces" src="https://github.com/user-attachments/assets/a3b1a8eb-be48-47cc-8fe8-eaa37a22e9d8" /></td>
    <td><img width="467" height="313" alt="Web UI registry" src="https://github.com/user-attachments/assets/e62457c7-70a1-4674-9640-1e6c0136f53d" /></td>
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

### Let your agent set it up

Install the mcpmu skill, then ask your coding agent to do the rest — it can read your existing MCP config, import your servers into mcpmu, and register mcpmu as your single MCP endpoint:

```bash
mcpmu skill install
```

Then tell your agent: *"Read my current MCP config, add all my servers to mcpmu, and register mcpmu as an MCP server"*

### Or set it up manually with the TUI, web UI, or CLI

**1. Add your MCP servers:**

```bash
# Start TUI
mcpmu

# Start web
mcpmu web

# Or use the CLI

# Add a stdio server
mcpmu add context7 -- npx -y @upstash/context7-mcp

# Add an HTTP server
mcpmu add atlassian https://mcp.atlassian.com/mcp --scopes read,write

# Add an HTTP server fronted by Cloudflare Access (custom headers on every request)
mcpmu add searxng https://searxng-mcp.example.com/mcp \
  --header "CF-Access-Client-Id: <id>" \
  --env-header "CF-Access-Client-Secret: CF_ACCESS_CLIENT_SECRET"
```

**2. Choose how your clients connect:**

Use stdio for local agent integrations. Each agent launches an mcpmu shim, and
on Unix those shims share one background daemon by default:

```bash
# Claude Code
claude mcp add mcpmu -- mcpmu serve --stdio

# Codex
codex mcp add mcpmu -- mcpmu serve --stdio
```

Or add directly to any MCP config JSON (Claude Code, Cursor, Windsurf, etc.):
```json
{
  "mcpmu": {
    "command": "mcpmu",
    "args": ["serve", "--stdio"]
  }
}
```

Or run one persistent Streamable HTTP endpoint for multiple HTTP-capable clients:

```bash
mcpmu serve --http
```

Then point clients at `http://127.0.0.1:8081/mcp` for the default namespace,
or `/mcp/{namespace}` for a specific namespace. For example:

```json
{
  "mcpmu": {
    "url": "http://127.0.0.1:8081/mcp"
  }
}
```

That's it. Your clients now have access to all configured MCP servers through
the transport that fits their setup.

## Namespaces

Namespaces let you create different server profiles — one for work, one for personal projects, a minimal one for keeping context length down.

```bash
# Create namespaces
mcpmu namespace add work --description "Work servers"
mcpmu namespace add personal --description "Personal projects"

# Assign servers to namespaces
mcpmu namespace assign work atlassian
mcpmu namespace assign work context7
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

**Streamable HTTP:**

With `mcpmu serve --http` running, use the namespace URL directly:

```text
http://127.0.0.1:8081/mcp/work
http://127.0.0.1:8081/mcp/personal
```

If no namespace is specified, mcpmu uses the default namespace (usually the first namespace created).

## Tool Permissions

Control which tools are exposed per namespace — useful for keeping context lean or restricting access. Can also be all configured from the TUI (probably easier there):

```bash
# Allow/deny specific tools
mcpmu permission set work atlassian jira_search allow
mcpmu permission set work atlassian confluence_delete deny

# Deny all tools by default, then allowlist what you need
mcpmu namespace set-deny-default minimal true
mcpmu permission set minimal context7 resolve allow

# Per-server deny-default — deny a tool-heavy server, allow the rest
mcpmu permission set-server-default work grafana deny
mcpmu permission set work grafana query_loki_logs allow
```

### Server-level global deny

For defense-in-depth, you can deny tools at the server level. Globally denied tools are blocked regardless of namespace permissions — even a namespace explicit allow cannot override a server global deny:

```bash
mcpmu server deny-tool filesystem delete_file move_file
mcpmu server allow-tool filesystem move_file   # re-enable
mcpmu server denied-tools filesystem           # list denied tools
```

Permission resolution order: **server global deny > explicit tool permission > server default > namespace default > allow**.

A common pattern: keep a lean namespace with only your most-used tools for everyday work, and an "extra" namespace with the full suite that you add as a second MCP server when needed.

## Features

- **Stdio process management** — Spawn and supervise local MCP servers (npx, binaries, scripts)
- **Process-tree cleanup** — Stop wrapper-launched workers with their parent and retain identity-validated crash recovery records
- **Remote upstreams** — Connect to Streamable HTTP/SSE MCP servers with full SSE support
- **MCP aggregation** — Expose all managed servers over stdio or Streamable HTTP, with one endpoint per namespace
- **Faithful proxying** — Tool definitions arrive with `title`, `annotations`, `outputSchema`, `icons`, and `_meta` intact, and results keep `structuredContent`; a tool's `readOnlyHint` reaches your agent instead of being discarded, so auto-approve still works through mcpmu
- **Protocol revisions up to 2025-11-25** — Negotiated per client connection rather than pinned, with cancellation and `progressToken` progress relayed in both directions
- **Shared daemon** — Concurrent `serve` clients share one set of upstream processes by default on Unix
- **OAuth support** — Full OAuth 2.1 with PKCE, dynamic client registration, token management, and automatic scope discovery
- **Hot-reload** — Serve mode watches the config file and automatically applies changes without restart
- **Lazy or eager startup** — Start servers on-demand or pre-start everything with `--eager`
- **Per-server startup bounds** — Apply `startup_timeout_sec` to connection, initialization, and initial discovery
- **Registry browser** — Search the official MCP server registry from the TUI and install with pre-populated config (`a` → Official Registry)
- **Interactive TUI** — Real-time logs, server status, start/stop controls, and namespace switching
- **Web UI** — Browser-based management via `mcpmu web` with live log streaming, CRUD operations, and registry browser
- **Usage metrics** — Per-tool call counts, error rates, and latency collected in serve mode, with an unused-tools view answering "am I actually using all the tools I've assigned?" (web UI **Metrics** page, or `GET /api/metrics` for scripting)
- **Compressed tool surface** — Opt-in `--compress` replaces the full `tools/list` with three lazy wrapper tools, so agents only spend context on the tool schemas they actually use

## Serve Mode

Expose the same managed servers and namespaces through either client transport:

| Client transport | Best fit | Namespace selection | Process model |
|------------------|----------|---------------------|---------------|
| stdio | Local agent and editor integrations | `--namespace work` | Per-client shim with a shared daemon by default on Unix |
| Streamable HTTP | Persistent or networked endpoints shared by multiple clients | `/mcp/work` | One dedicated, long-running HTTP process |

### Stdio serve mode

Use stdio when the MCP client launches its configured server process:

```bash
mcpmu serve --stdio                          # default namespace
mcpmu serve --stdio --namespace work         # specific namespace
mcpmu serve --stdio -n work --eager          # pre-start all servers
mcpmu serve --stdio --expose-manager-tools   # include mcpmu.* management tools
mcpmu serve --stdio --log-level debug        # verbose logging
```

On Unix, the first `serve` process for a config starts a detached daemon and
becomes a stdio shim; concurrent clients then share the daemon's upstream
processes. Any connect, spawn, or identity-handshake failure emits one warning
and falls back to embedded serve. Use `--isolated` for one private embedded
client, or set top-level `"daemonMode": false` as a global kill switch.
Windows remains embedded.

mcpmu supports macOS and Linux. Windows builds and runs in embedded mode on a
best-effort basis: it is not covered by CI, and stopping a server there kills
only the leader process, not its children.

The daemon inherits the environment and working directory of its first
spawner. Prefer absolute server `cwd` values and explicit config `env` entries.
Servers are shared by default. Stateful servers such as browser automation,
REPLs, and interpreter sessions should opt out per server so each connected
agent gets its own instance:

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

Private instances have session-scoped tools, notifications, logs, and manager
actions, and are stopped when that serve session disconnects. Shared instances
also share authentication sessions and upstream rate limits. Calling
`mcpmu.servers_stop` for a shared server stops it for every client; the next
use starts it again. For `shared: false`, that action affects only the caller.
See [docs/CLI.md](docs/CLI.md#shared-daemon-mode) for configuration and
diagnostic commands.

### HTTP serve mode

`mcpmu serve --http` exposes the same endpoint over the MCP Streamable HTTP
transport (POST + SSE) instead of stdio — one long-running process that any
number of HTTP MCP clients can connect to:

```bash
mcpmu serve --http                                    # 127.0.0.1:8081
mcpmu serve --http --addr 127.0.0.1:9090              # custom port
mcpmu serve --http --addr 0.0.0.0:8081 --token $TOK   # token mandatory off-loopback
mcpmu serve --http --session-idle-timeout 1h --allow-origin https://myapp.example
```

Each namespace gets its own URL — one running process, many toolsets:

```json
{
  "mcpmu": {
    "url": "http://127.0.0.1:8081/mcp/work",
    "headers": { "Authorization": "Bearer <token>" }
  }
}
```

`POST /mcp` uses the default namespace (same auto-select as stdio);
`POST /mcp/{namespace}` selects that namespace. Sessions idle for longer than
`--session-idle-timeout` (default 30m) are reaped, which is what keeps
`shared: false` servers safe here too — each HTTP session gets its own private
instance, so session count is process count.

### Compressed tool surface

A namespace with several large servers (Atlassian + GitHub + Grafana) can ship
tens of thousands of tokens of tool schemas on every session start. Opt-in
`--compress` replaces that with three wrapper tools, so the client only pays
for the schemas it actually fetches:

```bash
mcpmu serve --stdio --compress medium      # works with --http too
```

`tools/list` then returns just `list_tools`, `get_tool_schema`, and
`invoke_tool`, with a compact one-line-per-tool listing embedded in
`invoke_tool`'s description — the agent sees every available tool up front,
fetches full schemas on demand with `get_tool_schema`, and calls tools through
`invoke_tool`. The level sets how much each listing line carries: `low` (full
description), `medium` (first sentence — recommended), `high` (argument names
only), `max` (tool names only), modelled on
[mcp-compressor](https://github.com/atlassian-labs/mcp-compressor).

Namespace permissions and usage metrics are enforced and recorded against the
real target tool, and denied tools never appear in the listing. One caveat:
client-side per-tool allow/deny rules only ever see `invoke_tool`, so use
mcpmu's tool permissions to restrict tools in this mode.

Security: pass a bearer token via `--token` or `MCPMU_SERVE_TOKEN`; loopback
binds may run tokenless (the unauthenticated endpoint is then loopback-only,
plus an Origin check against browsers). Binding a
non-loopback address without a token refuses to start — serve-mode
`tools/call` is arbitrary code execution. TLS termination is out of scope;
put a reverse proxy in front for network deployments. `--isolated` does not
combine with `--http` (there is no daemon to skip; use per-server
`"shared": false` for per-session instances). An HTTP serve running beside
stdio clients duplicates shared upstream instances across the two processes —
tolerated, but worth knowing.

## Shell Completions

Tab-completion for server names, namespace names, and subcommand arguments. If installed via Homebrew:

```bash
mcpmu completion zsh > "$(brew --prefix)/share/zsh/site-functions/_mcpmu"
```

For bash, fish, and PowerShell setup see [docs/completions.md](docs/completions.md).

## Full CLI Reference

For the complete list of commands, flags, config schema, and HTTP server fields see [docs/CLI.md](docs/CLI.md).

## Agent Skill

mcpmu ships with a built-in [agent skill](https://agentskills.io) that teaches AI coding agents how to use the mcpmu CLI. Install it with a single command:

```bash
mcpmu skill install
```

This auto-detects which agents you have installed and copies the skill to the right locations:

| Agent | Path |
|-------|------|
| Claude Code | `~/.claude/skills/mcpmu/SKILL.md` |
| Codex CLI | `~/.codex/skills/mcpmu/SKILL.md` |
| Cross-agent | `~/.agents/skills/mcpmu/SKILL.md` |

The cross-agent path (`~/.agents/`) is always created as it's the emerging standard.

To remove the skill from all locations:

```bash
mcpmu skill uninstall
```

Once installed, your agent will automatically know how to use mcpmu commands when you ask about MCP server management.

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
