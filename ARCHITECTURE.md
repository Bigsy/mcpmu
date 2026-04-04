# mcpmu Architecture

## Overview

mcpmu is an MCP server aggregator that manages multiple MCP servers and exposes their tools through a unified interface.

```
┌─────────────────────────────────────────────────────────────┐
│                      Claude Code / Codex                     │
│                         (MCP Client)                         │
└─────────────────────────────┬───────────────────────────────┘
                              │ spawns via stdin/stdout
                              ▼
┌─────────────────────────────────────────────────────────────┐
│                        mcpmu                            │
│                                                              │
│  ┌──────────────────────────────────────────────────────┐   │
│  │                    stdio Server                       │   │
│  │              (MCP JSON-RPC protocol)                  │   │
│  └──────────────────────────┬───────────────────────────┘   │
│                              │                               │
│  ┌──────────────────────────┴───────────────────────────┐   │
│  │                  Tool Aggregator                      │   │
│  │         (collects tools from managed servers)         │   │
│  │         (routes tool calls to correct server)         │   │
│  └──────────────────────────┬───────────────────────────┘   │
│                              │                               │
│  ┌──────────────────────────┴───────────────────────────┐   │
│  │                    Supervisor                         │   │
│  │           (manages server process lifecycle)          │   │
│  └──────────────────────────┬───────────────────────────┘   │
│                              │                               │
│  ┌──────────────────────────┴───────────────────────────┐   │
│  │                      Config                           │   │
│  │            (~/.config/mcpmu/config.json)         │   │
│  └──────────────────────────────────────────────────────┘   │
└─────────────────────────────┬───────────────────────────────┘
                              │ spawns & manages
                              ▼
┌─────────────────────────────────────────────────────────────┐
│                    Managed MCP Servers                       │
│                                                              │
│   ┌─────────────┐  ┌─────────────┐  ┌─────────────┐         │
│   │ filesystem  │  │   github    │  │   sqlite    │  ...    │
│   │   server    │  │   server    │  │   server    │         │
│   └─────────────┘  └─────────────┘  └─────────────┘         │
└─────────────────────────────────────────────────────────────┘
```

## Primary Usage: stdio Mode

Claude Code/Codex spawns mcpmu as a subprocess. No daemons, no manual startup.

```json
// ~/.claude/mcp_servers.json
{
  "mcpmu": {
    "command": "mcpmu",
    "args": ["serve", "--stdio", "--config", "~/.config/mcpmu/config.json", "--namespace", "default"]
  }
}
```

### Config Compatibility (mcpServers-style)

The mcpmu config is designed so server entries remain compatible with the common `mcpServers` object shape used by MCP clients. In practice that means the server config uses the familiar field names:
- `command`
- `args`
- `cwd`
- `env`

This keeps manual editing easy (copy/paste a server definition from a client config into mcpmu, then add the namespace assignments/permissions as needed).

### Multiple Toolsets (Namespaces) for Different Contexts

The stdio server exposes a *single toolset* per process, selected by namespace at startup. Configure multiple MCP entries that run the same binary with different `--namespace` values.

If multiple namespaces exist and none is selected (and no default is set), mcpmu fails `initialize` with an actionable error rather than accidentally exposing all tools.

```json
// Work context
{
  "mcpmu-work": {
    "command": "mcpmu",
    "args": ["serve", "--stdio", "--config", "~/.config/mcpmu/config.json", "--namespace", "work"]
  }
}

// Personal context
{
  "mcpmu-personal": {
    "command": "mcpmu",
    "args": ["serve", "--stdio", "--config", "~/.config/mcpmu/config.json", "--namespace", "personal"]
  }
}
```

### Optional: Separate Config Files

Namespaces are the preferred mechanism for selecting toolsets, but separate config files are still supported when you want fully isolated settings.

```json
{
  "mcpmu-project-x": {
    "command": "mcpmu",
    "args": ["serve", "--stdio", "--config", "~/.config/mcpmu/project-x.json", "--namespace", "default"]
  }
}
```

## Progressive Tool Discovery

Serve mode uses a two-phase `tools/list` flow so clients are not blocked behind slow upstream discovery.

1. On `initialize`, `mcpmu` advertises `tools.listChanged: true`.
2. On the first `tools/list`, `mcpmu` starts or probes all selected servers concurrently.
3. It waits up to an 8 second grace period and returns the tools that are already ready.
4. Any remaining discovery continues in the background with the normal per-server timeout.
5. If background discovery makes progress, `mcpmu` sends `notifications/tools/list_changed` so the client can refresh with another `tools/list`.
6. Config reloads that may change the visible tool set also send `notifications/tools/list_changed`.

This keeps `tools/list` responsive for clients with tight request timeouts while still converging to the full aggregated tool set.

## Resource and Prompt Passthrough

When enabled via `--resources` and/or `--prompts`, serve mode passes through `resources/*` and `prompts/*` MCP methods from upstream servers. These are off by default to avoid context bloat.

- **Resources**: URIs are qualified with the server name prefix (`serverName:originalUri`). On `resources/read`, the prefix is stripped before forwarding upstream. The `:` separator is safe because server names cannot contain `:`.
- **Prompts**: Names are qualified as `serverName.promptName` (same as tools). Descriptions are prefixed with `[serverName]`. On `prompts/get`, the prefix is stripped before forwarding upstream.
- **No caching**: Resources and prompts are fetched on demand from upstream servers, not cached or discovered at startup.
- **No permissions**: Unlike tools, resources and prompts have no permission layer — they are read-only and user-initiated.

## Permission Resolution

Tool access follows a four-tier resolution. The server-level global deny applies even without a namespace:

1. **Server global deny** (`server deny-tool`) — hard block, no override. Applies even without a namespace.
2. **Explicit tool permission** (`permission set`) — highest namespace-level priority
3. **Per-server default** (`permission set-server-default`) — overrides namespace default for one server
4. **Namespace default** (`namespace set-deny-default`) — fallback for all servers
5. **Allow** — if nothing else applies

A namespace-level explicit allow **cannot** override a server global deny. To re-enable a globally denied tool, remove it from `deniedTools` via `server allow-tool`.

This enables defense-in-depth: globally deny dangerous tools at the server level, then use namespace permissions for fine-grained control over the rest.

## Tool Namespacing

Tools from managed servers are exposed with `serverId.toolName` format:

```
filesystem.read_file
filesystem.write_file
github.create_issue
github.list_repos
mcpmu.servers_list    # Manager tools
mcpmu.servers_start
mcpmu.servers_stop
mcpmu.namespaces_list
```

`serverId` is a stable internal identifier (auto-generated short `[a-z0-9]`, no `.`), not the human display name.

## Registry Browser

The TUI includes a registry browser for discovering and installing servers from the official MCP registry (`registry.modelcontextprotocol.io`). Press `a` on the server list to open an add-method selector:

- **Manual** — opens the blank add-server form
- **Official Registry** — opens a searchable browser with debounced live search, detail view with install preview, and pre-populates the add-server form with the selected server's command/args/env

The registry client (`internal/registry/`) handles API calls, type mapping, and install spec generation (package selection, runtime hints, env var placeholders).

## Embedded Agent Skill

mcpmu embeds a `SKILL.md` file in the binary (`cmd/mcpmu/skill_data/SKILL.md` via `//go:embed`). The `mcpmu skill install` command writes this to agent-specific skill directories (`~/.claude/skills/mcpmu/`, `~/.codex/skills/mcpmu/`, `~/.agents/skills/mcpmu/`). A checked-in mirror at `.claude/skills/mcpmu/SKILL.md` is kept in sync by a test assertion.

## Key Design Principles

1. **Non-blocking initialize**: Return immediately; optionally start `eager` servers in background (otherwise start on-demand)
2. **Lazy server start**: Servers start on first tool call (configurable)
3. **Progressive tool discovery**: `tools/list` returns ready tools within a grace window, then notifies clients when stragglers finish
4. **Graceful degradation**: If one server fails, others still work
5. **Strict output discipline**: stdout = MCP protocol only, stderr = logs
6. **Transport-agnostic core**: Easy to add HTTP later if needed
