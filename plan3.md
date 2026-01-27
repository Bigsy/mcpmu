# Phase 3: Namespaces & Permissions

## Objective
Implement namespace management and tool permission enforcement. This enables fine-grained access control for which tools are exposed via stdio mode.

---

## Status

### ✅ Already Implemented (Phase 2)
- [x] Namespace selection for stdio mode (`--namespace` flag)
- [x] Auto-selection rules (default → only → all → error)
- [x] Server filtering by active namespace
- [x] `mcp-studio.namespaces_list` tool
- [x] Config schema for namespaces and tool permissions

### 🎯 Phase 3 Scope (Minimal)
- [ ] Permission evaluation in `tools/call`
- [ ] Safe tool classification (pattern-based, for future bulk actions)
- [ ] CLI namespace management (`namespace add/list/remove/assign`)
- [ ] CLI permission management (`permission set/unset/list`)

### 🔮 Deferred to Future Phase
- [ ] TUI Namespaces tab
- [ ] TUI tool permission editor (modal/grid)
- [ ] Cached tool lists for offline permission editing
- [ ] Visual namespace indicators in server list
- [ ] Bulk permission actions (`enable-safe`, `deny-all`) - requires cached tools
- [ ] Tool description tooltips
- [ ] Search/filter tools by name

---

## Design Decisions

### Namespace Identity: Name vs ID
**Names are user-facing, IDs are internal** (same as servers).

- Users interact via namespace name (unique, human-readable)
- IDs are auto-generated 4-char strings (internal only)
- CLI commands accept names, lookup by name → ID internally
- Config stores ID references (`defaultNamespaceId`, `ToolPermission.NamespaceID`)

### tools/list Behavior
**Show all tools, block on call.**

- `tools/list` returns ALL tools regardless of permissions
- Permission enforcement happens only in `tools/call`
- Rationale: Simpler, more transparent, avoids cache invalidation issues

### Safe Tool Classification
**Strip server prefix before matching.**

- Input: `filesystem.read_file` → classify `read_file`
- Patterns match against unqualified tool name only

### No Namespace (selection=all) Behavior
**Skip permission checks entirely.**

When no namespaces are configured and `selectionMethod=all`:
- No active namespace to check permissions against
- All tool calls are allowed
- Rationale: If user hasn't set up namespaces, they haven't set up permissions

---

## Phase 3 Implementation Plan

### 1. Permission Evaluation (`internal/server/permissions.go`)

Add permission checking to `tools/call` handler:

```go
type PermissionResult int
const (
    PermissionAllow PermissionResult = iota
    PermissionDeny
    PermissionDefault  // No explicit rule, use namespace default
)

func (r *Router) CheckPermission(namespaceID, serverID, toolName string) PermissionResult
```

**Evaluation order:**
1. Check explicit `ToolPermission` entry → return Allow/Deny
2. No explicit entry → check namespace `DenyByDefault` setting
3. If `DenyByDefault=true` → Deny
4. Otherwise → Allow

**Config changes:**
- Add `DenyByDefault bool` to `NamespaceConfig`

### 2. Safe Tool Classification (`internal/server/safe_tools.go`)

Pattern-based classification for bulk permission assignment:

```go
type ToolClassification int
const (
    ToolSafe    ToolClassification = iota  // Read-only operations
    ToolUnsafe                              // Mutating operations
    ToolUnknown                             // Can't determine
)

func ClassifyTool(toolName string) ToolClassification
```

**Safe patterns** (read-only):
- `read`, `get`, `list`, `search`, `view`, `show`, `describe`, `fetch`, `query`, `find`, `lookup`

**Unsafe patterns** (mutating):
- `write`, `update`, `delete`, `execute`, `run`, `create`, `set`, `modify`, `remove`, `post`, `put`, `patch`, `send`, `invoke`

**Matching:** Check if tool name contains pattern (case-insensitive).

### 3. CLI Namespace Management

All commands use namespace **name** (not ID). Names must be unique.

```bash
# Create namespace (name must be unique)
mcp-studio namespace add <name> [--description "..."]

# List namespaces
mcp-studio namespace list [--json]

# Remove namespace
mcp-studio namespace remove <name> [--yes]

# Assign server to namespace (both by name)
mcp-studio namespace assign <namespace-name> <server-name>

# Unassign server from namespace
mcp-studio namespace unassign <namespace-name> <server-name>

# Set as default namespace
mcp-studio namespace default <name>

# Set deny-by-default for namespace
mcp-studio namespace set-deny-default <name> true|false
```

### 4. CLI Permission Management

All commands use **names** for namespace and server.

```bash
# Set permission for a tool (tool name is unqualified, e.g., "read_file" not "fs.read_file")
mcp-studio permission set <namespace> <server> <tool> allow|deny

# Remove explicit permission (revert to namespace default)
mcp-studio permission unset <namespace> <server> <tool>

# List permissions for namespace
mcp-studio permission list <namespace> [--json]
```

**Note:** Bulk actions (`enable-safe`, `deny-all`) are deferred until cached tool lists are implemented.

---

## Deferred Features (Future Phase)

### TUI Namespaces Tab
- Tab navigation (1=Servers, 2=Namespaces)
- Namespace list view with server counts
- Namespace detail view
- Create/edit/delete forms

### TUI Tool Permission Editor
- Modal or dedicated view
- Grouped by server
- Checkbox per tool
- Bulk toggles
- Requires either:
  - **On-demand:** Only edit when server is running (simpler)
  - **Cached:** Persist discovered tool lists (better UX, more complexity)

**Why deferred:** High UI complexity. CLI provides equivalent functionality for power users. TUI can be added later when the backend is stable.

### Cached Tool Lists
- Persist `tools/list` results to config or separate file
- Enables offline permission editing
- Requires cache invalidation strategy

**Why deferred:** Adds complexity (staleness, invalidation). On-demand discovery is sufficient for MVP.

---

## Files to Create/Modify

### Phase 3 (Minimal)
```
internal/
  server/
    permissions.go      # Permission evaluation logic
    safe_tools.go       # Tool classification (for future bulk actions)
    router.go           # Add permission check to tools/call
  config/
    schema.go           # Add DenyByDefault to NamespaceConfig
    config.go           # Add FindNamespaceByName, AddNamespace, DeleteNamespaceByName

cmd/mcp-studio/
    namespace.go        # namespace command group
    namespace_add.go    # Create namespace
    namespace_list.go   # List namespaces
    namespace_remove.go # Remove namespace
    namespace_assign.go # Assign/unassign server
    namespace_default.go # Set default namespace
    permission.go       # permission command group
    permission_set.go   # Set/unset permission
    permission_list.go  # List permissions
```

### Deferred (Future)
```
internal/
  tui/
    tabs.go                 # Tab bar component
    namespace_list.go       # Namespace list view
    namespace_form.go       # Create/edit form
    namespace_detail.go     # Detail view
    tool_permissions.go     # Permission editor modal
    server_picker.go        # Multi-select for assignment

cmd/mcp-studio/
    permission_bulk.go      # enable-safe, deny-all (requires cached tools)
```

---

## Tests

### Unit Tests
- `permissions_test.go`: Permission evaluation (allow/deny/default, no-namespace bypass)
- `safe_tools_test.go`: Tool classification patterns (with prefix stripping)
- `config_test.go`: Namespace CRUD helpers, name uniqueness

### CLI Integration Tests
- `namespace add/list/remove` workflow
- `namespace assign/unassign` workflow
- `permission set/unset/list` workflow
- Duplicate namespace name rejection

### Server Integration Tests
- Create namespace → assign server → set permission → verify `tools/call` enforcement
- Deny-by-default behavior blocks unconfigured tools
- No-namespace mode allows all tools (permission check bypass)

---

## Success Criteria (Phase 3 Minimal)
- [ ] `tools/call` respects permission settings (allow/deny)
- [ ] Denied tools return clear error message
- [ ] `DenyByDefault` namespace setting blocks unconfigured tools
- [ ] No-namespace mode (selection=all) bypasses permission checks
- [ ] Safe tool classification correctly categorizes common patterns
- [ ] CLI can fully manage namespaces (add/list/remove/assign/default)
- [ ] CLI can fully manage permissions (set/unset/list)
- [ ] Namespace names are unique (enforced on add)
- [ ] All changes persist to config

---

## Risks
1. **Permission bypass:** Must ensure ALL tool calls (including manager tools) go through permission check
2. **Safe tool false positives:** Pattern matching may miscategorize tools; override via explicit permission
3. **Namespace deletion:** Need to clean up orphaned permissions and server assignments
4. **Name→ID lookup:** Must be consistent across all CLI commands and internal lookups

---

## Estimated Complexity
- Permission evaluation: Low (~150 lines)
- Safe tool classification: Low (~80 lines)
- Config helpers (namespace CRUD): Low (~100 lines)
- CLI namespace commands: Medium (~350 lines)
- CLI permission commands: Medium (~200 lines)
- Tests: Medium (~400 lines)
- **Total: ~1280 lines**

Deferred TUI work would add ~1500-2000 lines.
Deferred bulk permission commands would add ~150 lines (after cached tools).
