# UI Touchup Analysis

Hands-on exploration of the TUI via tmux. Focus on navigation consistency and visual polish.

---

## Navigation Issues

### 1. Redundant Key Hints in Namespace Detail View
**Priority: High**

The namespace detail view shows keys in TWO places:

**Content area (line 207-208):**
```
Keys: s=assign servers  p=edit permissions  D=set default  esc=back
```

**Status bar:**
```
esc:back  s:assign-servers  p:permissions  D:set-default  e:edit  ?:help
```

**Fix:** Remove the inline keys from namespace_detail.go. The status bar is the consistent location for key hints across all views.

**File:** `internal/tui/views/namespace_detail.go:206-208`
```go
// DELETE these lines:
content.WriteString("\n\n")
content.WriteString(m.theme.Faint.Render("Keys: "))
content.WriteString(m.theme.Muted.Render("s=assign servers  p=edit permissions  D=set default  esc=back"))
```

---

### 2. Help Overlay Shows "(coming)" for Working Tab
**Priority: Medium**

Help overlay says:
```
2  Namespaces tab (coming)
3  Proxies tab (coming)
```

But Namespaces (tab 2) is fully working.

**File:** `internal/tui/views/help_overlay.go:110-114`

**Fix:**
```go
m.renderSection("Tabs", [][]string{
    {"1", "Servers tab"},
    {"2", "Namespaces tab"},        // Remove "(coming)"
    {"3", "Proxies tab (planned)"}, // Keep indicator
}),
```

---

### 3. Missing Enter Hint in Server List
**Priority: Low**

Server list status bar: `t:test  E:enable  a:add  e:edit  d:delete  l:logs  ?:help`

Users may not realize Enter opens server details.

**File:** `internal/tui/model.go:936`

**Fix:** Add `enter:view` at the start:
```go
keys = "enter:view  t:test  E:enable  a:add  e:edit  d:delete  l:logs  ?:help"
```

---

### 4. Tab Key Doesn't Cycle Tabs
**Priority: Low (intentional?)**

Pressing Tab doesn't switch between tabs. Only number keys (1/2/3) work.

This may be intentional, but some users expect Tab to cycle through tabs. Consider adding Tab/Shift+Tab for tab cycling.

---

## Visual Issues

### 1. Double Status Indicators on Server List
**Priority: Medium**

Each server shows BOTH:
- Status icon: `●` or `○`
- Status pill: `● RUN` or `○ STOP`

This is redundant.

**File:** `internal/tui/views/server_list.go:143-174`

**Options:**
1. Remove icon, keep pill (recommended - more informative)
2. Use icon for enabled/disabled, pill for running/stopped
3. Keep both (current)

**Recommendation:** Remove icon from lines 146-149:
```go
// Remove this:
icon := d.theme.StatusIcon(
    si.Status.State == events.StateRunning,
    si.Status.State == events.StateError || si.Status.State == events.StateCrashed,
)
```

And adjust line 167:
```go
// Change from:
line1.WriteString(icon)
line1.WriteString(" ")
// To just the name directly
```

---

### 2. Key Hint Separator Inconsistency
**Priority: Low**

- Status bar uses double-space: `t:test  E:enable  a:add`
- Modal footers use bullet: `shift+tab back • enter next`
- Namespace inline uses double-space: `s=assign servers  p=edit permissions`

**Recommendation:** Standardize on double-space everywhere.

---

### 3. Logs Panel Inline Hint
**Priority: Low**

Logs header shows: `Logs [f]ollow`

The `[f]` is helpful but breaks pattern. Other features show hints in status bar.

**Options:**
1. Keep it (it's helpful and compact)
2. Remove it, ensure status bar shows `f:follow` when logs visible

---

## Not Bugs (Verified)

### Server Names Look Truncated
Observed: "Filesystemsds", "Memoryf", "Sequential Thinkin"

Verified: These ARE the names in config.json. Not a display bug.

```bash
$ jq '.servers | to_entries[] | .value.name' ~/.config/mcp-studio/config.json
"Filesystemsds"
"Memoryf"
"Sequential Thinkin"
```

---

## Summary of Changes

| Priority | Issue | File | Action |
|----------|-------|------|--------|
| High | Redundant keys in namespace detail | namespace_detail.go:206-208 | Delete inline keys |
| Medium | Help shows "(coming)" for working tab | help_overlay.go:110-114 | Update text |
| Medium | Redundant status icon | server_list.go:146-167 | Remove icon |
| Low | Missing Enter hint | model.go:936 | Add `enter:view` |
| Low | Separator inconsistency | Various | Standardize on double-space |
| Low | Tab key doesn't cycle | keymap.go | Consider adding Tab binding |

---

## Additional Findings (Jan 29, 2026)

### 1. Proxies Tab Key is a No-op
**Priority: High**

Pressing `3` does nothing, but the tab label is present and help lists it.

**File:** `internal/tui/model.go` (Tab3 handling)

**Fix:** Either render a “Proxies (coming soon)” empty state or hide/disable the tab until implemented.

---

### 2. Server List Ordering Changes After View Transitions
**Priority: Medium**

Server ordering is not stable after entering/exiting detail view. This feels like the list is being rebuilt without a stable sort.

**File:** `internal/tui/model.go` (list refresh) and `internal/config/schema.go` (ServerList)

**Fix:** Sort the list by name (and secondary key by ID) when refreshing the view.

---

### 3. Disabled + Running State Conflict
**Priority: High**

It’s possible to toggle `E` to disabled while a server is running, yielding a contradictory state (RUN + [disabled]).

**File:** `internal/tui/model.go` (toggle enabled) and `internal/tui/views/server_list.go` (status render)

**Fix (pick one):**
1) Prevent disabling while running and show a toast.
2) Auto-stop on disable.
3) Allow but update status pill to reflect “running (disabled)” clearly.

---

### 4. Footer Hints Don’t Reflect State
**Priority: Medium**

Footer always shows `E:enable` even when the selected server is disabled. Should dynamically show `E:disable` or `E:enable`.

**File:** `internal/tui/model.go` (status bar key hints)

---

### 5. Follow Mode Has No Visible State
**Priority: Medium**

Toggling `f` doesn’t change the log panel header or UI. The action is invisible.

**File:** `internal/tui/views/log_panel.go` (header render)

**Fix:** Render “Follow: on/off” or `[follow]` highlight when active.

---

### 6. Toasts Hide Key Hints
**Priority: Low**

Toasts replace the entire status bar, hiding key hints.

**File:** `internal/tui/views/toast.go` and `internal/tui/model.go` (status bar render)

**Fix:** Render toasts above the status bar, or overlay the toast without removing key hints.

---

### 7. Tool Permissions Discovery Escape Not Cancelling
**Priority: High**

“Press esc to cancel” doesn’t exit discovery mode.

**File:** `internal/tui/views/tool_permissions.go` (discovering mode Update)

**Fix:** Verify `esc` reaches the modal during discovery; ensure the modal is visible and `Update` is called. If this is a focus issue, ensure discovery mode receives KeyMsg events.

---

### 8. Status Row Alignment in Server List
**Priority: Low**

Multiple badges (`RUN/STOP`, `[disabled]`, namespace tags) make rows jagged and hard to scan.

**File:** `internal/tui/views/server_list.go`

**Fix:** Use a fixed layout: name column + status pill + tags, aligned via padding.

---

## Complete Plan

### Phase 1 — Consistency & Accuracy (Docs + Key Hints)
**Goal:** Make the UI hints match behavior.

**Tasks**
1. Update help overlay tabs text: remove “Namespaces (coming)” and clarify Proxies state.
2. Decide Proxies tab behavior: hidden/disabled or show “coming soon” view.
3. Add `enter:view` to server list hint.
4. Remove redundant inline key hints in namespace detail.
5. Standardize key hint separators (double-space) across modals and inline hints.

**Acceptance**
- Help overlay mirrors actual tabs.
- All key hints appear in one consistent location (status bar).

---

### Phase 2 — State Correctness
**Goal:** Eliminate contradictory or invisible states.

**Tasks**
1. Prevent disable on running server OR auto-stop on disable (choose behavior).
2. Dynamic footer hint for enable/disable.
3. Add visual indicator for follow mode in log panel header.
4. Fix discovery `esc` cancel behavior in tool permissions modal.

**Acceptance**
- No RUN + [disabled] combo unless explicitly designed.
- Follow mode visibly toggles.
- `esc` cancels discovery immediately.

---

### Phase 3 — Layout & Visual Polish
**Goal:** Improve scanability and visual balance.

**Tasks**
1. Remove redundant status icon in server list or reassign icon meaning.
2. Normalize server list layout with stable columns/padding.
3. Keep toast visible without hiding key hints (toast above status bar).

**Acceptance**
- Server rows align cleanly.
- Toasts don’t suppress key help.

---

### Phase 4 — Behavior & Navigation Enhancements
**Goal:** Improve navigation expectations.

**Tasks**
1. Add stable sorting in server list refresh.
2. Add Tab / Shift+Tab to cycle between tabs (optional).

**Acceptance**
- Order doesn’t shift when entering/exiting detail.
- Tab cycling works or is explicitly documented as not supported.

---

## Suggested Execution Order
1) Phase 1 (docs + hints), low risk and quick wins.  
2) Phase 2 (state correctness), fixes user-facing confusion.  
3) Phase 3 (visual cleanup), improves scanability.  
4) Phase 4 (navigation polish), optional ergonomics.
