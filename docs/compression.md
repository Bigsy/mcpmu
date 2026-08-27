# Tool-surface compression

MCP clients normally receive the full schema for every exposed tool at session
startup. A namespace containing several large servers can therefore consume a
substantial part of the model's context before any work begins.

mcpmu's opt-in compressed tool surface advertises three wrapper tools instead:

| Wrapper | Purpose |
|---|---|
| `list_tools` | Return the compact list of available tools. |
| `get_tool_schema` | Fetch the full schema for one or more tools on demand. |
| `invoke_tool` | Call a real tool through mcpmu's normal routing path. |

A compact tool listing is also embedded in `invoke_tool`'s description. The
agent can see what is available immediately, fetch only the schemas it needs,
and then invoke those tools without loading the entire catalog up front.

## Enable compression

Configure it on a namespace when that namespace should always use compression:

```bash
mcpmu namespace set-compression work medium
```

Or enable it for one serve process:

```bash
mcpmu serve --stdio --compress medium
mcpmu serve --http --compress medium
```

Compression is off by default.

## Choose a level

The level controls how much information each entry in the compact listing
contains. It does not change the full schema returned by `get_tool_schema`.

| Level | Compact listing contains | Best fit |
|---|---|---|
| `low` | Full descriptions | Maximum guidance with modest savings |
| `medium` | First sentence of each description | Recommended starting point |
| `high` | Tool names and argument names | Large, familiar catalogs |
| `max` | Tool names only | Maximum reduction when names are descriptive |

Start with `medium`. Move toward `high` or `max` when the catalog remains too
large and the agent can reliably choose tools by name.

## Namespace settings and flag overrides

Clear a namespace's stored compression setting with:

```bash
mcpmu namespace set-compression work off
```

The effective setting is resolved in this order:

1. An explicit `--compress <level>` selects that level.
2. An explicit `--compress off` disables compression.
3. With no flag, the active namespace's stored setting applies.
4. With neither, compression is off.

This makes it possible for a large `work` namespace to use compression while a
small `dev` namespace exposes normal tools. The stored value is visible in
`mcpmu namespace list` and editable in the terminal and web UIs.

Serve mode resolves the setting from the current configuration on each
request. Namespace or compression changes therefore take effect after a hot
reload on the next `tools/list`.

## Permissions and metrics

Compression does not bypass mcpmu's normal routing:

- The compact listing contains only tools allowed in the active namespace.
- `get_tool_schema` does not return schemas for unknown or denied targets.
- `invoke_tool` applies namespace and server-level permissions to the real
  target before dispatching it.
- Tool calls are recorded in usage metrics under the real server and tool.
- Calls to `list_tools` and `get_tool_schema` are recorded under `mcpmu`.

This pairs well with an allowlisted namespace: permissions reduce what is
available, while compression reduces the schema cost of what remains. See
[Tool permissions](permissions.md) for configuration patterns.

## Client-side permission caveat

While compression is enabled, the connected MCP client sees only
`list_tools`, `get_tool_schema`, and `invoke_tool`. Client-side per-tool rules
therefore cannot distinguish the real target tools called through
`invoke_tool`.

Define security boundaries with mcpmu permissions when using compression. A
client may still choose whether to approve the wrapper call, but it cannot
enforce different policies for individual targets hidden behind that wrapper.

## Operational notes

- Compression behaves the same over stdio and Streamable HTTP.
- The listing follows tool discovery and `tools/list_changed` notifications.
- Manager tools remain directly exposed when `--expose-manager-tools` is set.
- An upstream tool named `invoke_tool` remains addressable by its qualified
  name, such as `server.invoke_tool`.
- Turning compression off invalidates a client's cached wrapper tools. mcpmu
  emits `tools/list_changed`, and the client should request `tools/list` again.

For every compression-related serve flag and namespace command, see the
[CLI reference](CLI.md#serve-mode).
