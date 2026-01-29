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

### ✅ Phase 3 Scope (Completed)
- [x] Permission evaluation in `tools/call`
- [x] Safe tool classification (pattern-based, for future bulk actions)
- [x] CLI namespace management (`namespace add/list/remove/assign`)
- [x] CLI permission management (`permission set/unset/list`)

### ✅ Phase 3.1 - TUI Namespaces (Completed)
- [x] TUI Namespaces tab (Tab 2)
- [x] TUI namespace list/detail/form views
- [x] TUI tool permission editor (modal, shows tools from running servers)
- [x] Server picker for namespace assignment
- [x] Visual namespace indicators in server list
- [x] Duplicate namespace name validation on edit
- [x] Permission revert-to-default (toggle back removes explicit permission)

### ✅ Phase 3.2 (Completed)
- [x] Auto-start servers in test mode when opening permission editor
- [x] Bulk permission actions (`enable-safe`, `deny-all`) using safe tool classification
- [x] Tool description tooltips in permission editor
- [x] Search/filter tools by name in permission editor

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

**Note:** CLI bulk actions (`enable-safe`, `deny-all`) are deferred until cached tool lists are implemented. TUI bulk actions are available in the permission editor modal.

---

## Completed Features

### TUI Namespaces Tab ✅
- Tab navigation (1=Servers, 2=Namespaces)
- Namespace list view with server counts, default indicator, deny-by-default indicator
- Namespace detail view showing assigned servers and configured permissions
- Create/edit/delete forms with dirty checking
- Server picker modal for assignment
- Visual namespace badges on server list

### TUI Tool Permission Editor ✅
- Modal view opened from namespace detail (`p` key)
- Tools grouped by server
- Toggle enable/disable per tool
- Shows current state and whether explicit or default
- Revert-to-default by toggling back to namespace default value
- **Approach:** On-demand - auto-starts servers to discover tools, auto-stops on modal close (no caching needed)

## Implementation Details (Phase 3.2) ✅

### Auto-start for Permission Editing ✅
Auto-starts assigned servers when `p` is pressed, then shows the permission editor once tools are discovered.
- [x] Track which servers were auto-started for this modal, and auto-stop them on exit (pre-running servers are not stopped).
- [x] Skip auto-start for disabled servers; show a toast explaining they must be enabled first.
- [x] Show a "Discovering tools…" state while waiting, with 15s timeout.
- [x] On timeout: show partial tools with warning message, or close if no tools found.

### Bulk Permission Actions ✅
- [x] `enable-safe` (`a` key) - Allow all tools classified as safe (read-only)
- [x] `deny-all` (`d` key) - Deny all tools (then selectively allow)
- [x] Uses existing `ClassifyTool()` from `safe_tools.go`
- [x] Keybindings shown in footer/help.

**Behavior notes:**
- Both actions **override** existing explicit permissions (not additive). User can undo with space toggle.
- `enable-safe`: Only affects tools classified as `ToolSafe`. Unknown and unsafe tools are unchanged.
- `deny-all`: Denies ALL tools regardless of classification.

### UX Improvements ✅
- [x] Tool description shown in modal footer (truncated if too long; "(no description)" if empty).
- [x] Search/filter tools by name using `/` key.
- [x] Filter can be cleared with Esc (first Esc clears filter, second closes modal).

**Filter behavior:** Uses bubbles list built-in filtering (name-only match). Server headers are hidden when filtering; only matching tools are shown.

### Tests (Phase 3.2) ✅
- [x] Auto-start flow: `TestToolPermissions_ShowDiscovering`, `TestToolPermissions_FinishDiscovery`
- [x] Bulk actions: `TestToolPermissions_BulkEnableSafe`, `TestToolPermissions_BulkDenyAll`
- [x] Timeout handling: `TestToolPermissions_DiscoveryTimeout`
- [x] Auto-started servers tracking: `TestToolPermissions_AutoStartedServersInResult`

---

## Files Created/Modified

### Phase 3 (CLI + Server) ✅
```
internal/server/
    permissions.go          # Permission evaluation logic ✅
    safe_tools.go           # Tool classification ✅
    router.go               # Permission check in tools/call ✅
internal/config/
    schema.go               # DenyByDefault in NamespaceConfig ✅
    config.go               # Namespace CRUD, UpdateNamespace duplicate check ✅
cmd/mcp-studio/
    namespace.go            # namespace command group ✅
    permission.go           # permission command group ✅
```

### Phase 3.1 (TUI Namespaces) ✅
```
internal/tui/views/
    namespace_list.go       # Namespace list view ✅
    namespace_form.go       # Create/edit form ✅
    namespace_detail.go     # Detail view ✅
    tool_permissions.go     # Permission editor modal ✅
    server_picker.go        # Multi-select for assignment ✅
    server_list.go          # Updated with namespace badges ✅
internal/tui/
    model.go                # Tab 2 wiring, handlers ✅
    model_test.go           # Namespace tests ✅
```

### Phase 3.2 ✅
```
internal/tui/
    model.go                # Auto-start servers for permission editing ✅
    views/tool_permissions.go # Bulk actions, tooltips, search, discovery state ✅
    views/tool_permissions_test.go # Phase 3.2 tests ✅
```

---

## Tests

### Unit Tests ✅
- `permissions_test.go`: Permission evaluation (allow/deny/default, no-namespace bypass) ✅
- `safe_tools_test.go`: Tool classification patterns (with prefix stripping) ✅
- `config_test.go`: Namespace CRUD helpers, name uniqueness, UpdateNamespace duplicate check ✅

### TUI Tests ✅
- `views/namespace_list_test.go`: Namespace list component ✅
- `views/namespace_form_test.go`: Form show/hide, dirty checking, config building ✅
- `views/server_picker_test.go`: Server picker show/hide, selection state ✅
- `views/tool_permissions_test.go`: Permission editor show/hide, denyByDefault ✅
- `model_test.go`: Namespace tab navigation, key handlers, result handlers ✅

### Server Integration Tests ✅
- `TestServer_NamespaceToolPermissions_EndToEnd`: Full flow with 2 servers, namespace, permissions ✅
- Deny-by-default behavior blocks unconfigured tools ✅
- Explicitly denied tools return ErrCodeToolDenied ✅
- Allowed tools can be called ✅
- No-namespace mode allows all tools (permission check bypass) ✅

---

## Success Criteria (Phase 3 Minimal) ✅
- [x] `tools/call` respects permission settings (allow/deny)
- [x] Denied tools return clear error message
- [x] `DenyByDefault` namespace setting blocks unconfigured tools
- [x] No-namespace mode (selection=all) bypasses permission checks
- [x] Safe tool classification correctly categorizes common patterns
- [x] CLI can fully manage namespaces (add/list/remove/assign/default)
- [x] CLI can fully manage permissions (set/unset/list)
- [x] Namespace names are unique (enforced on add)
- [x] All changes persist to config

## Success Criteria (Phase 3.1 TUI) ✅
- [x] Tab 2 shows namespace list with indicators
- [x] Can add/edit/delete namespaces via TUI
- [x] Can assign/unassign servers via picker modal
- [x] Can edit tool permissions when servers are running
- [x] Permissions can be reverted to default
- [x] Server list shows namespace badges
- [x] Namespace names are unique (enforced on edit too)

## Success Criteria (Phase 3.2) ✅
- [x] Permission editor auto-starts servers if not running
- [x] Bulk "enable safe" / "deny all" actions available
- [x] Tool descriptions visible in permission editor
- [x] Can search/filter tools by name

---

## Risks
1. **Permission bypass:** Must ensure ALL tool calls (including manager tools) go through permission check
2. **Safe tool false positives:** Pattern matching may miscategorize tools; override via explicit permission
3. **Namespace deletion:** Need to clean up orphaned permissions and server assignments
4. **Name→ID lookup:** Must be consistent across all CLI commands and internal lookups

---

