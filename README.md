<p align="center">
  <img src="docs/mcp-icon.png" alt="mcpmu" width="120">
</p>

<h1 align="center">mcpmu (μ)</h1>

<p align="center"><strong>One secure, context-efficient MCP gateway for all your agents.</strong></p>

mcpmu lets you configure MCP servers once and expose them through a single
stdio or Streamable HTTP endpoint. Claude Code, Codex, Cursor, Windsurf, and
other MCP clients can share the same servers without duplicating configuration.

- **Control tools at the gateway** — Allowlist tools by namespace and globally
  block dangerous operations, regardless of which agent connects.
- **Reduce tool-schema context costs** — Replace a large `tools/list` response
  with three discovery and invocation tools, configurable per namespace.
- **Create focused namespaces** — Give work, personal, and project agents only
  the servers and tools they need.
- **Connect any upstream** — Manage local stdio processes and remote Streamable
  HTTP/SSE servers.
- **Serve any client** — Use local stdio shims or one persistent Streamable HTTP
  endpoint with a URL for each namespace.
- **Manage everything visually** — Use the terminal UI or browser UI to add,
  test, monitor, and configure servers.
- **Keep MCP metadata intact** — Tool annotations, output schemas, icons, and
  structured results survive the proxy hop.

### Terminal UI

<table>
  <tr>
    <td><img width="467" height="360" alt="Terminal UI server list" src="https://github.com/user-attachments/assets/481cebb2-c3de-4c4b-8b01-81f43ab06c54" /></td>
    <td><img width="467" height="359" alt="Terminal UI server detail" src="https://github.com/user-attachments/assets/127f1ccd-4882-4676-876a-4f7cb067769e" /></td>
  </tr>
  <tr>
    <td><img width="467" height="359" alt="Terminal UI namespace view" src="https://github.com/user-attachments/assets/9378dff4-14b1-49a1-bfe6-4c5a00d73bfc" /></td>
    <td><img width="466" height="358" alt="Terminal UI permissions editor" src="https://github.com/user-attachments/assets/763e8be4-e84c-45c9-9795-792769be7504" /></td>
  </tr>
</table>

### Web UI

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

## Quick start

Add an MCP server:

```bash
# Local stdio server
mcpmu add context7 -- npx -y @upstash/context7-mcp

# Remote HTTP server with OAuth
mcpmu add atlassian https://mcp.atlassian.com/mcp --scopes read,write
```

Then register mcpmu with a local agent:

```bash
claude mcp add mcpmu -- mcpmu serve --stdio
# or
codex mcp add mcpmu -- mcpmu serve --stdio
```

The equivalent MCP configuration is:

```json
{
  "mcpmu": {
    "command": "mcpmu",
    "args": ["serve", "--stdio"]
  }
}
```

Run `mcpmu` for the terminal UI or `mcpmu web` for the browser UI.

Alternatively, let your coding agent perform the setup. Install the bundled
agent skill, then ask it to import your existing MCP configuration:

```bash
mcpmu skill install
```

## Give each agent the right tools

Namespaces create separate tool profiles from the same server configuration:

```bash
mcpmu namespace add work --description "Work tools"
mcpmu namespace assign work atlassian
mcpmu namespace assign work context7

claude mcp add work -- mcpmu serve --stdio --namespace work
```

Permissions are enforced inside mcpmu, not delegated to the connecting client.
You can deny dangerous tools everywhere, create an allowlist for a namespace,
or choose defaults for one particularly broad server:

```bash
# This cannot be overridden by a namespace
mcpmu server deny-tool filesystem delete_file

# Expose only selected tools in this namespace
mcpmu namespace set-deny-default work true
mcpmu permission set work context7 resolve allow
```

The precedence is: **server global deny > explicit tool rule > server default >
namespace default > allow**. See [Tool permissions](docs/permissions.md) for
recipes, the full resolution model, and the interaction with compression.

## Reduce tool-schema context costs

Large MCP setups can send thousands of tokens of tool schemas when a session
starts. Compression replaces that surface with `list_tools`,
`get_tool_schema`, and `invoke_tool`, allowing an agent to fetch full schemas
only when it needs them:

```bash
# Recommended starting point for a large namespace
mcpmu namespace set-compression work medium
```

Compression is opt-in and works with stdio and HTTP. Denied tools stay out of
the compact listing, and calls through `invoke_tool` still use the real target
tool for mcpmu permissions and metrics.

See [Tool-surface compression](docs/compression.md) for level selection,
flag overrides, client-side permission caveats, and operational details.

## Choose a client transport

| Transport | Best fit | Namespace | Process model |
|---|---|---|---|
| stdio | Local agents and editor integrations | `--namespace work` | Per-client shim; shared daemon by default on Unix |
| Streamable HTTP | Persistent or networked endpoints | `/mcp/work` | One long-running HTTP process |

For HTTP clients, start the endpoint and point the client at the namespace URL:

```bash
mcpmu serve --http
```

```json
{
  "mcpmu": {
    "url": "http://127.0.0.1:8081/mcp/work"
  }
}
```

`/mcp` selects the default namespace; `/mcp/{namespace}` selects a named one.
A non-loopback bind requires a bearer token:

```bash
mcpmu serve --http --addr 0.0.0.0:8081 --token "$MCPMU_TOKEN"
```

Servers are shared by default. For stateful servers such as browser automation
or REPLs, turn off **Share between agent connections** in the TUI or web server
form (or set `"shared": false` in the config) to give each client session a private instance.
Changing sharing takes effect when serving processes reload the config. The [CLI reference](docs/CLI.md) covers
daemon controls, HTTP security, OAuth, custom headers, and all serve flags.

## Runtime status and diagnostics

TUI and web statuses describe **this management session**. Their start/stop
controls use a separate supervisor from the daemon serving agent connections.
A stopped server in a manager may still be running for an agent.

```bash
mcpmu status           # resolved config, daemon availability and running instances
mcpmu status --json
mcpmu doctor          # config, executable, working-directory and env checks
mcpmu doctor --json
```

Both commands are read-only and support `--config`. They never start upstreams,
perform OAuth login, or start a daemon. Doctor checks the current CLI environment
plus server overrides; a running daemon may have inherited a different environment.
Missing configs use empty defaults. Status succeeds for a determined absent daemon;
doctor exits 1 for invalid config or failed local prerequisite checks. Environment
checks show variable names and outcomes, never values.

Serve reloads keep unaffected upstreams running. Permission/compression edits
retain instances; runtime edits restart affected servers. Invalid external edits
retain the previous valid config, and the web UI shows a warning until recovery.
Global settings such as metrics and OAuth still use a full reload.

## More features

- Search and install servers from the official MCP registry.
- Authenticate remote servers with OAuth 2.1, PKCE, dynamic client
  registration, bearer tokens, or custom headers.
- Hot-reload configuration without restarting serve mode.
- Start servers lazily or pre-start them with `--eager`.
- Pass through upstream resources and prompts.
- Track per-tool call counts, error rates, latency, and unused tools without
  recording arguments or results.
- Share upstream processes across concurrent stdio clients while allowing
  private instances per server.
- Negotiate MCP protocol revisions through 2025-11-25 and relay cancellation
  and progress in both directions.

## Documentation

- [Tool permissions](docs/permissions.md) — access rules, precedence, and
  practical allowlist and denylist patterns
- [Tool-surface compression](docs/compression.md) — reduce tool-schema context
  while preserving gateway enforcement
- [CLI reference](docs/CLI.md) — commands, flags, configuration fields, HTTP,
  and shared-daemon operation
- [Shell completions](docs/completions.md) — zsh, bash, fish, and PowerShell
- [Architecture](ARCHITECTURE.md) — implementation and protocol details

## Building and testing

```bash
git clone https://github.com/Bigsy/mcpmu.git
cd mcpmu
go build -o mcpmu ./cmd/mcpmu
./mcpmu
```

```bash
go test ./...
make check
make test-integration
```

Local linting and CI use [golangci-lint v2.12.2](https://github.com/golangci/golangci-lint/releases/tag/v2.12.2),
built with Go 1.26. Run `make check` and `make test-integration` sequentially:
a legacy OAuth callback fixture uses a fixed port. Self-contained reload and
diagnostic smoke coverage: `./scripts/smoke.sh smoke_selective_reload`.
Performance fixtures and measured baselines are in [docs/performance.md](docs/performance.md).
