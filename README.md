# MCP Studio Go

A TUI application and stdio MCP server for managing local MCP (Model Context Protocol) servers.

## Status

Implemented so far:
- Stdio MCP server management (start/stop, logs, tool discovery)
- TUI for servers, logs, and namespaces
- Namespace-based tool permissions
- CLI for server CRUD + namespace/permission management
- `serve --stdio` mode to expose an aggregated MCP toolset to Claude/Codex

Planned next (Phase 4):
- Streamable HTTP client transport
- OAuth 2.1 login flow and token storage

## Build & Run

```bash
go build -o mcp-studio ./cmd/mcp-studio
./mcp-studio
./mcp-studio --debug  # logs to /tmp/mcp-studio-debug.log
```

## TUI Usage

Keybindings:
- `t` start/stop server (test toggle)
- `e` enable/disable server
- `l` toggle log panel
- `f` toggle follow mode (logs)
- `?` help overlay
- `Enter` view details
- `Esc` go back

## CLI Usage

Server management:
```bash
mcp-studio add <name> -- <command> [args...]
mcp-studio add context7 -- npx -y @upstash/context7-mcp
mcp-studio add my-server --env FOO=bar --cwd /path -- ./server --flag

mcp-studio list
mcp-studio list --json

mcp-studio remove <name>
mcp-studio remove <name> --yes
```

Namespaces:
```bash
mcp-studio namespace list
mcp-studio namespace add <name> --description "My namespace"
mcp-studio namespace remove <name>
mcp-studio namespace assign <namespace> <server>
mcp-studio namespace unassign <namespace> <server>
mcp-studio namespace default <name>
```

Tool permissions:
```bash
mcp-studio permission list <namespace>
mcp-studio permission set <namespace> <server> <tool> <allow|deny>
mcp-studio permission unset <namespace> <server> <tool>
```

## MCP Server Mode (stdio)

Run mcp-studio as an MCP server so Claude/Codex can call tools from your managed servers:
```bash
mcp-studio serve --stdio --config ~/.config/mcp-studio/config.json --namespace default
```

Example entry for Claude Code:
```json
{
  "mcp-studio": {
    "command": "mcp-studio",
    "args": ["serve", "--stdio", "--config", "~/.config/mcp-studio/config.json", "--namespace", "default"]
  }
}
```

## Configuration

Default config path: `~/.config/mcp-studio/config.json`

Minimal example (also see `example-config.json`):
```json
{
  "schemaVersion": 1,
  "servers": {
    "fs": {
      "id": "fs",
      "name": "Filesystem",
      "kind": "stdio",
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"],
      "cwd": ""
    }
  },
  "namespaces": [],
  "proxies": [],
  "toolPermissions": []
}
```

## Notes

- Transport is stdio-only today. Streamable HTTP + OAuth is planned next.
- Token storage and remote HTTP transport are not yet implemented.

## Testing

```bash
go test ./...
```
