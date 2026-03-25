# mcpmu (μ)

**A multiplexing MCP server that aggregates multiple MCP servers behind a single stdio MCP server.**

Unlike typical MCP setups where each coding agent needs its own server configurations, mcpmu acts as a meta-server: you configure all your MCP servers once, then expose them as a unified endpoint to any agent that supports the Model Context Protocol. Add one entry to Claude Code, Cursor, Windsurf, or any MCP-compatible tool and instantly gain access to your entire MCP ecosystem.

Key differentiators:
- **Single configuration, universal access** — Define servers once, use everywhere
- **Namespace profiles** — Group servers by context (work, personal, project) with per-namespace tool permissions
- **Multi-transport** — Manage both local stdio processes and remote HTTP/SSE endpoints
- **Registry browser** — Search the official MCP registry and install servers with pre-populated config
- **Interactive TUI** — Monitor, test, and manage servers with a terminal interface
- **Tool permissions** — Block unused tools per-namespace or globally deny dangerous tools at the server level
- **Defense-in-depth** — Server-level global deny list that overrides all namespace permissions


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
mcpmu add atlassian https://mcp.atlassian.com/mcp --scopes read,write
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
- **Streamable HTTP/SSE** — Connect to remote MCP endpoints with full SSE support
- **MCP aggregation** — Expose all managed servers as a single MCP endpoint via `mcpmu serve --stdio`
- **OAuth support** — Full OAuth 2.1 with PKCE, dynamic client registration, token management, and automatic scope discovery
- **Hot-reload** — Serve mode watches the config file and automatically applies changes without restart
- **Lazy or eager startup** — Start servers on-demand or pre-start everything with `--eager`
- **Registry browser** — Search the official MCP server registry from the TUI and install with pre-populated config (`a` → Official Registry)
- **Interactive TUI** — Real-time logs, server status, start/stop controls, and namespace switching

## Serve Mode

Expose managed servers as a single MCP endpoint:

```bash
mcpmu serve --stdio                          # default namespace
mcpmu serve --stdio --namespace work         # specific namespace
mcpmu serve --stdio -n work --eager          # pre-start all servers
mcpmu serve --stdio --expose-manager-tools   # include mcpmu.* management tools
mcpmu serve --stdio --log-level debug        # verbose logging
```

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
