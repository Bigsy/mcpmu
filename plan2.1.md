# Phase 2.1: CLI Server Management

## Objective
Add CLI commands to create, list, and remove MCP server entries, writing into mcp-studio's JSON config.

---

## Scope
- Add top-level `add`, `list`, and `remove` commands.
- Support stdio server creation via a `--` separator.
- Enforce unique server names.
- IDs are internal only - never shown to users.
- Persist changes into `~/.config/mcp-studio/config.json` using existing config helpers.

---

## CLI Behavior

### Add command
```
mcp-studio add <server-name> \
  --env VAR1=VALUE1 --env VAR2=VALUE2 \
  [--cwd /path] [--autostart] \
  -- <stdio server-command> [args...]
```

### Examples
```bash
mcp-studio add context7 -- npx -y @upstash/context7-mcp
mcp-studio add my-server --env FOO=bar --env BAZ=qux -- ./server --flag
```

### List command
```bash
mcp-studio list
mcp-studio list --json
```

**Default output:**
```
NAME         COMMAND                           ENABLED
filesystem   npx -y @anthropics/mcp-fs         yes
context7     npx -y @upstash/context7-mcp      yes
my-server    ./server --flag                   no
```

### Remove command
```bash
mcp-studio remove <name>
mcp-studio remove <name> --yes
```

### Field mapping
- `<server-name>` → `ServerConfig.Name` (display name, must be unique)
- `--` command list → `ServerConfig.Command` + `ServerConfig.Args`
- `--env KEY=VALUE` → `ServerConfig.Env`
- `--cwd` → `ServerConfig.Cwd`
- `--autostart` → `ServerConfig.Autostart`
- `Kind` defaults to `stdio`
- `Enabled` defaults to `true`
- `ID` auto-generated (internal only)

### Output
On success:
```
Added server "context7"
```

---

## Implementation Plan

### 1) Config helpers
- Add `FindServerByName(name string) (*ServerConfig, error)` to config package.
- Add `DeleteServerByName(name string) error` to config package.
- Modify `AddServer()` to enforce name uniqueness.

### 2) Cobra command wiring
- Add `cmd/mcp-studio/add.go` for the `add` command.
- Add `cmd/mcp-studio/list.go` for the `list` command.
- Add `cmd/mcp-studio/remove.go` for the `remove` command.

### 3) Argument parsing (add command)
- Use `cmd.ArgsLenAtDash()` to split flags from the stdio command.
- Validate:
  - `<server-name>` is present.
  - `--` separator is present.
  - At least one command token after `--`.
- Parse `--env` entries with `strings.SplitN(kv, "=", 2)`; error if missing `=` or empty key.

### 4) UX + error handling
- Clear errors for: missing `--`, invalid env, duplicate name.
- List: tabular output by default, JSON with `--json`.
- Remove: error if name not found; `--yes` skips confirmation.

---

## Tests
- Unit test env parsing helper.
- Unit test name uniqueness enforcement.
- CLI integration test with isolated `$HOME`:
  - Run `mcp-studio add ...`, verify config contains new server.
  - Run `mcp-studio add ...` with same name, verify error.
  - Run `mcp-studio list`, verify name appears.
  - Run `mcp-studio remove ...`, verify server removed.

---

## Success Criteria
- `mcp-studio add <name> -- <cmd>` writes a valid server entry.
- Duplicate names are rejected with a clear error.
- `mcp-studio list` shows servers without exposing IDs.
- `mcp-studio remove <name>` removes the server.
- New server appears in the TUI and can be started.
