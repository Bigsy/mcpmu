# mcp-studio Architecture

## Overview

mcp-studio is an MCP server aggregator that manages multiple MCP servers and exposes their tools through a unified interface.

```
┌─────────────────────────────────────────────────────────────┐
│                      Claude Code / Codex                     │
│                         (MCP Client)                         │
└─────────────────────────────┬───────────────────────────────┘
                              │ spawns via stdin/stdout
                              ▼
┌─────────────────────────────────────────────────────────────┐
│                        mcp-studio                            │
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
│  │            (~/.config/mcp-studio/config.json)         │   │
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

Claude Code/Codex spawns mcp-studio as a subprocess. No daemons, no manual startup.

```json
// ~/.claude/mcp_servers.json
{
  "mcp-studio": {
    "command": "mcp-studio",
    "args": ["serve", "--stdio", "--config", "~/.config/mcp-studio/config.json", "--namespace", "default"]
  }
}
```

### Config Compatibility (mcpServers-style)

The mcp-studio config is designed so server entries remain compatible with the common `mcpServers` object shape used by MCP clients. In practice that means the server config uses the familiar field names:
- `command`
- `args`
- `cwd`
- `env`

This keeps manual editing easy (copy/paste a server definition from a client config into mcp-studio, then add the namespace assignments/permissions as needed).

### Multiple Toolsets (Namespaces) for Different Contexts

The stdio server exposes a *single toolset* per process, selected by namespace at startup. Configure multiple MCP entries that run the same binary with different `--namespace` values.

If multiple namespaces exist and none is selected (and no default is set), mcp-studio fails `initialize` with an actionable error rather than accidentally exposing all tools.

```json
// Work context
{
  "mcp-studio-work": {
    "command": "mcp-studio",
    "args": ["serve", "--stdio", "--config", "~/.config/mcp-studio/config.json", "--namespace", "work"]
  }
}

// Personal context
{
  "mcp-studio-personal": {
    "command": "mcp-studio",
    "args": ["serve", "--stdio", "--config", "~/.config/mcp-studio/config.json", "--namespace", "personal"]
  }
}
```

### Optional: Separate Config Files

Namespaces are the preferred mechanism for selecting toolsets, but separate config files are still supported when you want fully isolated settings.

```json
{
  "mcp-studio-project-x": {
    "command": "mcp-studio",
    "args": ["serve", "--stdio", "--config", "~/.config/mcp-studio/project-x.json", "--namespace", "default"]
  }
}
```

## Tool Namespacing

Tools from managed servers are exposed with `serverId.toolName` format:

```
filesystem.read_file
filesystem.write_file
github.create_issue
github.list_repos
mcp-studio.servers_list    # Manager tools
mcp-studio.servers_start
mcp-studio.servers_stop
mcp-studio.namespaces_list
```

`serverId` is a stable internal identifier (auto-generated short `[a-z0-9]`, no `.`), not the human display name.

## Phase Overview

| Phase | Description | Status |
|-------|-------------|--------|
| 1 | Core: config, MCP client, supervisor, event bus | Planned |
| 1.1 | Testing infrastructure | Planned |
| 1.5 | **stdio server mode** (primary feature) | Planned |
| 2 | TUI for config management | Planned |
| 3 | Namespaces and tool permissions | Planned |
| 4 | HTTP proxy (SSE, Streamable-HTTP) | **DEFERRED** |
| 5 | SSE client + OAuth | Planned |

## Why stdio-Only (Phase 4 Deferred)

HTTP proxy mode adds complexity for use cases most users don't need:
- Web browser clients
- Remote access from another machine
- Shared team servers
- Enterprise features (auth, audit)

**stdio mode covers 90%+ of local Claude Code/Codex workflows.**

The core is designed to be transport-agnostic, so HTTP can be added later without a rewrite.

## Key Design Principles

1. **Non-blocking initialize**: Return immediately; optionally start `eager` servers in background (otherwise start on-demand)
2. **Lazy server start**: Servers start on first tool call (configurable)
3. **Graceful degradation**: If one server fails, others still work
4. **Strict output discipline**: stdout = MCP protocol only, stderr = logs
5. **Transport-agnostic core**: Easy to add HTTP later if needed
