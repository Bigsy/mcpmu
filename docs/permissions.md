# Tool permissions

mcpmu enforces tool access at the gateway, so the same rules apply to every
connected client. Permissions affect which tools are advertised and whether a
tool call is dispatched upstream.

Use namespace rules to give each agent the tools it needs. Use server-level
global denies as a non-overridable safety boundary.

## Permission resolution

Rules are evaluated from highest to lowest priority:

1. **Server global deny** — a hard block in every namespace and even when no
   namespace is selected.
2. **Explicit tool permission** — an allow or deny for one tool in one
   namespace.
3. **Per-server default** — the fallback for one server in one namespace.
4. **Namespace default** — the fallback for every assigned server.
5. **Allow** — the result when no rule matches.

An explicit namespace allow cannot override a server global deny. Remove the
global deny before the tool can be used again.

## Common patterns

### Globally block dangerous operations

Use this for a tool that no connected agent should be able to call:

```bash
mcpmu server deny-tool filesystem delete_file move_file
mcpmu server denied-tools filesystem
```

Re-enable a tool by removing it from the global deny list:

```bash
mcpmu server allow-tool filesystem move_file
```

### Allowlist a minimal namespace

Set the namespace to deny by default, then explicitly expose the tools the
agent needs:

```bash
mcpmu namespace add minimal --description "Read-only everyday tools"
mcpmu namespace assign minimal context7
mcpmu namespace assign minimal github
mcpmu namespace set-deny-default minimal true

mcpmu permission set minimal context7 resolve allow
mcpmu permission set minimal github search_code allow
```

### Restrict one broad server

Keep the namespace's normal allow-by-default behaviour, but allowlist tools
from one tool-heavy or sensitive server:

```bash
mcpmu permission set-server-default work grafana deny
mcpmu permission set work grafana query_loki_logs allow
```

Remove the per-server default to fall back to the namespace default again:

```bash
mcpmu permission unset-server-default work grafana
```

### Deny one tool in a permissive namespace

```bash
mcpmu permission set work atlassian confluence_delete deny
```

Remove the explicit rule to restore inherited behaviour:

```bash
mcpmu permission unset work atlassian confluence_delete
```

## Inspect permissions

```bash
mcpmu permission list work
mcpmu permission list work --json
mcpmu server denied-tools filesystem --json
```

Permissions can also be edited from the terminal UI and browser UI. Serve mode
hot-reloads configuration changes, so clients do not need an mcpmu restart.

## Permissions with compression

mcpmu applies the same permission filter before building a compressed tool
listing. Denied tools do not appear in `list_tools`, in the listing embedded in
`invoke_tool`, or through `get_tool_schema`.

When `invoke_tool` is called, mcpmu resolves the real target and applies its
permissions again before dispatch. Metrics are also recorded against that real
server and tool.

A client's own per-tool rules see only the three wrapper tools while
compression is enabled. If access must be restricted by real tool name, define
that policy in mcpmu rather than relying on the client. See
[Tool-surface compression](compression.md) for the complete model.

## Resources and prompts

The permission system applies to tools. Upstream resources and prompts do not
have a permission layer; they are read-only and user-initiated. Control their
passthrough with the serve-mode resource and prompt flags described in the
[CLI reference](CLI.md#serve-flags).
