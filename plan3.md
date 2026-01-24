# Phase 3: Namespaces

## Objective
Implement namespace management for grouping servers and controlling tool permissions. This enables fine-grained access control for which tools are exposed through proxies.

## Features

### Namespace CRUD
- [ ] Create namespace form:
  - ID (required, unique)
  - Name (display name)
  - Description (optional)
- [ ] Edit namespace
- [ ] Delete namespace (with orphan check)
- [ ] Namespace list view (new tab in TUI)

### Server Assignment
- [ ] Assign servers to namespaces (multi-select)
- [ ] Server can belong to multiple namespaces
- [ ] Visual indicator of namespace membership in server list
- [ ] "Manage Servers" action from namespace detail view

### Tool Permissions
- [ ] Tool permission grid: namespace × server × tool
- [ ] Permission states: enabled, disabled, prompt (future)
- [ ] Default behavior: unconfigured tools enabled until server has any explicit permissions
- [ ] **Deny by default option**: namespace-level setting to deny unspecified tools
- [ ] Bulk actions:
  - "Enable All" - enable all tools for a server
  - "Disable All" - disable all tools for a server
  - "Enable Safe Tools" - rule-based classification
- [ ] **Safe tool classification rules** (not just heuristic):
  - Pattern matching on tool name prefixes/suffixes
  - Safe (read-only): read, get, list, search, view, show, describe, fetch, query
  - Unsafe (mutating): write, update, delete, execute, run, create, set, modify, remove, post, put, patch
  - Unknown: tools that don't match either pattern → follow namespace default
  - User can override classification per-tool

### Tool Permission Persistence
- [ ] Store permissions in config: `namespaceId:serverId:toolName → enabled`
- [ ] Efficient lookup structure (map of maps)
- [ ] Serialize/deserialize with config

### Namespace Detail View
- [ ] Show namespace metadata
- [ ] List assigned servers with status
- [ ] Tool count summary (enabled/total)
- [ ] "Manage Tools" action opens permission editor

### Tool Permission Editor
- [ ] Modal or dedicated view
- [ ] Grouped by server
- [ ] Checkbox per tool
- [ ] Server-level bulk toggles
- [ ] Tool description tooltips
- [ ] Search/filter tools by name

### TUI Tab Navigation
- [ ] Servers tab (from Phase 2)
- [ ] Namespaces tab (new)
- [ ] Tab switching with number keys (1, 2) or Tab key
- [ ] Visual indicator of active tab

### Tool Name Namespacing
- [ ] Qualified tool names: `{namespaceId}:{serverId}:{toolName}`
- [ ] Used internally for proxy tool aggregation
- [ ] Prevents collision when same server in multiple namespaces

## Dependencies
- Phase 2: Multi-server management, TUI framework
- See [PLAN-ui.md](PLAN-ui.md) for namespace list and tool permissions modal specs

## Unknowns / Questions
1. **Permission Granularity**: Per-namespace or global tool permissions? (Design: per-namespace)
2. **Default Namespace**: Should there be a default namespace? Or require explicit assignment?
3. **Orphan Handling**: What happens to servers when their namespace is deleted?
4. **Tool Discovery Timing**: When to refresh tool list? On server connect? On demand?
5. **Tool Identity**: How to handle tool name collisions? Key by `(serverId, toolName)` or qualified alias?
6. **Offline Permissions**: Allow editing permissions for servers that aren't running (using cached tools)?

## Risks
1. **Stale Tool Lists**: If server's tools change, cached permissions may reference non-existent tools. Need graceful handling.
2. **Complex UI State**: Tool permission editor has many interacting elements. Need careful state management.
3. **Safe Tool Heuristic**: Pattern matching may have false positives/negatives. Should be overridable.
4. **Tool Metadata Quality**: Many MCP tools won't clearly declare mutability. Fallback behavior needed.
5. **Permissions Pending**: What if tool hasn't been discovered yet? Mark as "pending" until server runs.

## Success Criteria
- Can create/edit/delete namespaces
- Can assign servers to namespaces
- Tool permission editor works correctly
- "Enable Safe Tools" applies correct permissions
- Permissions persist across restarts
- Tab navigation works smoothly

## Files to Create/Modify
```
internal/
  config/
    config.go       # Add namespace and permission types
    permissions.go  # Permission evaluation logic
  namespace/
    namespace.go    # Namespace management
    permissions.go  # Tool permission logic
    safe_tools.go   # Safe tool heuristic
  tui/
    model.go        # Add tab navigation
    tabs.go         # Tab bar component
    namespace_list.go    # Namespace list view
    namespace_form.go    # Namespace form
    namespace_detail.go  # Namespace detail view
    tool_permissions.go  # Permission editor
    server_picker.go     # Multi-select server picker
```

## Estimated Complexity
- Namespace CRUD: Low
- Server assignment: Low-Medium
- Tool permissions: Medium-High
- Permission editor UI: High
- Total: ~1800-2500 lines of Go code (cumulative ~4300-5700)
