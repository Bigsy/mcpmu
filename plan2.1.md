# Phase 2.1: CLI MCP Add

## Objective
Add a CLI workflow to create MCP server entries via the command line, mirroring the Codex CLI experience (e.g., `codex mcp add ...`) while writing into mcp-studio’s JSON config.

---

## Scope
- Add a new top-level `mcp` command with `add`, `list`, and `remove` subcommands.
- Support stdio server creation via a `--` separator.
- Persist changes into `~/.config/mcp-studio/config.json` using existing config helpers.
- Keep current ID constraints (4-char `[a-z0-9]`) and auto-generate IDs.

---

## CLI Behavior (Proposed)

### Command form
```
mcp-studio mcp add <server-name> \
  --env VAR1=VALUE1 --env VAR2=VALUE2 \
  [--cwd /path] [--autostart] [--id ab12] \
  -- <stdio server-command> [args...]
```

### Examples
```
mcp-studio mcp add context7 -- npx -y @upstash/context7-mcp
mcp-studio mcp add my-server --env FOO=bar --env BAZ=qux -- ./server --flag
```

### List command
```
mcp-studio mcp list
mcp-studio mcp list --json
```

### Remove command
```
mcp-studio mcp remove <id>
mcp-studio mcp remove <id> --yes
```

### Field mapping
- `<server-name>` → `ServerConfig.Name` (display name)
- `--` command list → `ServerConfig.Command` + `ServerConfig.Args`
- `--env KEY=VALUE` → `ServerConfig.Env`
- `--cwd` → `ServerConfig.Cwd`
- `--autostart` → `ServerConfig.Autostart`
- `--id` (optional) → `ServerConfig.ID` (validated)
- `Kind` defaults to `stdio`
- `Enabled` left unset (defaults to enabled)

### Output
On success, print a short confirmation including the assigned ID:
```
Added server "context7" (id: ab12)
```

---

## Implementation Plan

### 1) Cobra command wiring
- Add `cmd/mcp-studio/mcp.go` to register `mcp` under `rootCmd`.
- Add `cmd/mcp-studio/mcp_add.go` for the `add` subcommand.
- Add `cmd/mcp-studio/mcp_list.go` for the `list` subcommand.
- Add `cmd/mcp-studio/mcp_remove.go` for the `remove` subcommand.

### 2) Argument parsing
- Use `cmd.ArgsLenAtDash()` to split flags from the stdio command.
- Validate:
  - `<server-name>` is present.
  - `--` separator is present.
  - At least one command token after `--`.
- Parse `--env` entries with `strings.SplitN(kv, "=", 2)`; error if missing `=` or empty key.

### 3) Config mutation
- Load config with `config.Load()` or `config.LoadFrom()` (add a `--config` flag to `mcp` for parity with `serve`).
- Build `ServerConfig` and call `cfg.AddServer()`.
- Persist with `config.Save()`.

### 4) UX + error handling
- Errors should be human-readable (missing `--`, invalid env, invalid ID, etc.).
- Print the new server ID so users can locate it in the TUI.
- List output (default): one server per line, include ID, name, enabled, kind, command.
- List output (`--json`): emit JSON array of `ServerConfig` objects.
- Remove: error if ID not found; support `--yes` to skip confirmation.

---

## Tests
- Unit test env parsing helper (valid/invalid cases).
- CLI integration test with isolated `$HOME`:
  - Run `mcp-studio mcp add ...`
  - Verify config JSON contains new server with expected fields.
- CLI integration test for `mcp list`:
  - Add a server, run list, verify ID appears in text output.
- CLI integration test for `mcp remove`:
  - Add then remove; verify config no longer contains server.

---

## Docs / Help Updates
- Update `cmd/mcp-studio/root.go` or a README section to mention `mcp` commands.
- Add a short usage block in `PLAN.md` or a new CLI section in `ARCHITECTURE.md`.

---

## Open Questions
1. Should we allow `--id` or keep IDs strictly auto-generated?
2. Should we accept `--enabled=false` or leave enable/disable to the TUI?
3. Should `mcp remove` accept name as well as ID (risk of ambiguity)?
4. Do we want to support non-stdio servers (`--url`) in a later phase?

---

## Risks
- **Name vs ID confusion**: Users might expect `<server-name>` to be the tool prefix; we should surface the generated ID clearly.
- **Env value quoting**: Values with spaces rely on shell quoting; errors should point users to quote values.

---

## Success Criteria
- `mcp-studio mcp add ... -- <cmd>` writes a valid server entry to config.
- New server appears in the TUI and can be started successfully.
- Error messages are clear for missing `--`, invalid envs, or invalid IDs.
