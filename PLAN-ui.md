# UI Design Specification

This document defines the TUI layout, navigation, and visual design for MCP Studio Go. It translates the original desktop app UI into terminal-native patterns.

## Reference Screenshots

The original MCP-studio desktop app screenshots are at:
- `../mcp-studio-site/public/images/servers.png` - Server management view
- `../mcp-studio-site/public/images/namespaces.png` - Namespace management view
- `../mcp-studio-site/public/images/proxies.png` - Proxy management view
- `../mcp-studio-site/public/images/tools.png` - Tool permissions modal

---

## Design Principles

1. **Keyboard-first**: Every action accessible via keyboard shortcuts
2. **Information density**: Show status at a glance without clutter
3. **Progressive disclosure**: Details on demand, not always visible
4. **Terminal-native**: Use box-drawing, colors that work on dark/light terminals
5. **Responsive**: Adapt to terminal width (min 80 cols, graceful at 120+)
6. **Accessible**: Don't rely on color alone; provide ASCII fallbacks for symbols
7. **Focus clarity**: Strong visual indicators for focused pane and selected item

---

## Global Layout

```
┌─────────────────────────────────────────────────────────────────────────────┐
│ MCP Studio                              [1]Servers [2]Namespaces [3]Proxies │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│                                                                             │
│                            MAIN CONTENT AREA                                │
│                                                                             │
│                                                                             │
│                                                                             │
├─────────────────────────────────────────────────────────────────────────────┤
│ 3/5 servers running │ 2/2 proxies running │ 47 tools exposed    │ ?=help   │
└─────────────────────────────────────────────────────────────────────────────┘
```

### Components

| Component | Position | Content |
|-----------|----------|---------|
| Title bar | Top-left | App name |
| Tab bar | Top-right | Tab names with number shortcuts, active tab highlighted |
| Content | Center | Tab-specific content |
| Status bar | Bottom | Running counts, tool count, help hint |

---

## Tab 1: Servers

### Default View (List Only)

The default view shows just the server list. Forms are accessed via keypress (`a` to add, `e` to edit).

```
┌─ Servers ──────────────────────────────────────────────────────────────────┐
│                                                                            │
│  ● obsidian              Running     npx -y @anthropic/mcp-obsidian        │
│    12 tools              2h 15m      /Users/me/notes                       │
│                                                                            │
│  ○ chrome-dev-tools      Stopped     npx -y @anthropic/chrome-devtools     │
│    27 tools              —           ~/                                    │
│                                                                            │
│  ● context7              Running     npx -y context7-mcp                   │
│    8 tools               45m         ~/projects                            │
│                                                                            │
│  ✖ clippy                Error       clippy-mcp                            │
│    — tools               Exit: 1     ENOENT: command not found             │
│                                                                            │
│                                                                            │
│                                                                            │
├────────────────────────────────────────────────────────────────────────────┤
│  [a]dd  [e]dit  [d]elete  [s]tart  [x]stop  [r]estart  [l]ogs  [?]help    │
└────────────────────────────────────────────────────────────────────────────┘
```

### Add/Edit Server Form (Modal - `a` or `e` key)

Opens as a centered modal overlay:

```
┌─ Add Server ───────────────────────────────────────────────────────────────┐
│                                                                            │
│  Name                                                                      │
│  ┌──────────────────────────────────────────────────────────────────────┐  │
│  │ obsidian                                                             │  │
│  └──────────────────────────────────────────────────────────────────────┘  │
│                                                                            │
│  Type                                                                      │
│  ┌──────────────────────────────────────────────────────────────────────┐  │
│  │ MCP STDIO                                                        ▼   │  │
│  └──────────────────────────────────────────────────────────────────────┘  │
│                                                                            │
│  Command                                                                   │
│  ┌──────────────────────────────────────────────────────────────────────┐  │
│  │ npx -y @anthropic/mcp-obsidian                                       │  │
│  └──────────────────────────────────────────────────────────────────────┘  │
│                                                                            │
│  Arguments                                                                 │
│  ┌──────────────────────────────────────────────────────────────────────┐  │
│  │                                                                      │  │
│  └──────────────────────────────────────────────────────────────────────┘  │
│                                                                            │
│  Working Directory                                                         │
│  ┌──────────────────────────────────────────────────────────────────────┐  │
│  │ /Users/me/notes                                                      │  │
│  └──────────────────────────────────────────────────────────────────────┘  │
│                                                                            │
│  Environment Variables                                                     │
│  ┌──────────────────────────────────────────────────────────────────────┐  │
│  │ OBSIDIAN_VAULT=/Users/me/notes                                       │  │
│  │                                                                      │  │
│  └──────────────────────────────────────────────────────────────────────┘  │
│                                                                            │
│                        [ Cancel ]    [ Save ]                              │
│                                                                            │
│  [Tab] next field    [Shift+Tab] prev    [Enter] save    [Esc] cancel     │
└────────────────────────────────────────────────────────────────────────────┘
```

### Server List with Logs (toggle with `l`)

```
┌─ Servers ──────────────────────────────────────────────────────────────────┐
│                                                                            │
│  ● obsidian              Running     npx -y @anthropic/mcp-obsidian        │
│    12 tools              2h 15m      /Users/me/notes                       │
│                                                                            │
│  ○ chrome-dev-tools      Stopped     npx -y @anthropic/chrome-devtools     │
│    27 tools              —           ~/                                    │
│                                                                            │
│  ● context7              Running     npx -y context7-mcp                   │
│    8 tools               45m         ~/projects                            │
│                                                                            │
├─ Logs ─────────────────────────────────────────────────────────────────────┤
│ [obsidian] 10:42:15 MCP server started on stdio                            │
│ [obsidian] 10:42:15 Registered 12 tools                                    │
│ [context7] 10:42:18 Connected to context7 API                              │
│ [clippy] 10:42:20 Error: ENOENT: command not found                         │
├────────────────────────────────────────────────────────────────────────────┤
│  [a]dd  [e]dit  [d]elete  [s]tart  [x]stop  [l]ogs  [f]ollow  [?]help     │
└────────────────────────────────────────────────────────────────────────────┘
```

### Server List Item States

| State | Icon | ASCII Fallback | Color | Description |
|-------|------|----------------|-------|-------------|
| Running | ● | [+] | Green | Server is connected and healthy |
| Stopped | ○ | [-] | Gray | Server is not running |
| Error | ✖ | [!] | Red | Server crashed or failed to connect |
| Starting | ◐ | [~] | Yellow | Server is starting up |
| Stopping | ◑ | [~] | Yellow | Server is shutting down |

**Note**: Use `MCP_ASCII=1` env var to force ASCII-only rendering for compatibility.

### Server Detail View (Enter on server)

```
┌─ Server: obsidian ──────────────────────────────────────────────────────────┐
│                                                                             │
│  Status: ● Running          PID: 12847          Uptime: 2h 15m              │
│  Command: npx -y @anthropic/mcp-obsidian                                    │
│  Working Dir: /Users/me/notes                                               │
│                                                                             │
│  ┌─ Tools (12) ─────────────────────────────────────────────────────────┐   │
│  │  read_note          Read a note from the vault                       │   │
│  │  write_note         Create or update a note                          │   │
│  │  search_notes       Search notes by content                          │   │
│  │  list_notes         List all notes in a folder                       │   │
│  │  delete_note        Delete a note                                    │   │
│  │  ...                                                                 │   │
│  └──────────────────────────────────────────────────────────────────────┘   │
│                                                                             │
│  ┌─ Logs ───────────────────────────────────────────────────────────────┐   │
│  │ 10:42:15 [INFO] MCP server started on stdio                          │   │
│  │ 10:42:15 [INFO] Registered 12 tools                                  │   │
│  │ 10:44:22 [DEBUG] Tool invoked: read_note                             │   │
│  └──────────────────────────────────────────────────────────────────────┘   │
│                                                                             │
│  [s]tart  [x]stop  [r]estart  [e]dit  [d]elete  [Esc]back                   │
└─────────────────────────────────────────────────────────────────────────────┘
```

---

## Tab 2: Namespaces

### Default View (List Only)

The default view shows just the namespace list. Forms are accessed via keypress (`a` to add, `e` to edit).

```
┌─ Namespaces ───────────────────────────────────────────────────────────────┐
│                                                                            │
│  chrome                                                                    │
│    Servers: chrome-dev-tools                                               │
│    Tools: 24/27 enabled                                                    │
│                                                                            │
│  test                                                                      │
│    Servers: obsidian, context7                                             │
│    Tools: 15/15 enabled                                                    │
│                                                                            │
│  work                                                                      │
│    Servers: obsidian, context7, clippy                                     │
│    Tools: 8/24 enabled                                                     │
│                                                                            │
│                                                                            │
│                                                                            │
├────────────────────────────────────────────────────────────────────────────┤
│  [a]dd  [e]dit  [d]elete  [t]ools  [Enter] details  [?]help               │
└────────────────────────────────────────────────────────────────────────────┘
```

### Add/Edit Namespace Form (Modal - `a` or `e` key)

Opens as a centered modal overlay:

```
┌─ Create Namespace ─────────────────────────────────────────────────────────┐
│                                                                            │
│  Name                                                                      │
│  ┌──────────────────────────────────────────────────────────────────────┐  │
│  │ work                                                                 │  │
│  └──────────────────────────────────────────────────────────────────────┘  │
│                                                                            │
│  Description                                                               │
│  ┌──────────────────────────────────────────────────────────────────────┐  │
│  │ Work-related MCP servers and tools                                   │  │
│  └──────────────────────────────────────────────────────────────────────┘  │
│                                                                            │
│  ─────────────────────────────────────────────────────────────────────     │
│  Assign Servers                                                            │
│                                                                            │
│  [●] obsidian            ● Running      12 tools                           │
│  [●] chrome-dev-tools    ○ Stopped      27 tools                           │
│  [ ] context7            ● Running       8 tools                           │
│  [ ] clippy              ✖ Error         — tools                           │
│                                                                            │
│                        [ Cancel ]    [ Save ]                              │
│                                                                            │
│  [Space] toggle server    [Enter] save    [Esc] cancel                    │
└────────────────────────────────────────────────────────────────────────────┘
```

### Tool Permissions Modal (t on namespace)

```
┌─ Tool Permissions: chrome ──────────────────────────────────────────────────┐
│                                                                             │
│  Control which tools are available in this namespace.                       │
│                                                                             │
│  ┌─ Bulk Actions ───────────────────────────────────────────────────────┐   │
│  │  [S] Enable Safe    [A] Enable All    [N] Disable All                │   │
│  └──────────────────────────────────────────────────────────────────────┘   │
│                                                                             │
│  ┌─ chrome-dev-tools (27 tools) ────────────────────────────────────────┐   │
│  │                                                                      │   │
│  │  [●] click                 Click an element by selector              │   │
│  │  [●] close_page            Close the active browser page             │   │
│  │  [●] drag                  Drag element to another element           │   │
│  │  [●] emulate_network       Emulate network conditions                │   │
│  │  [ ] evaluate              Execute JavaScript in page context        │   │
│  │  [●] get_console_logs      Get console log messages                  │   │
│  │  [ ] navigate              Navigate to a URL                         │   │
│  │  [●] screenshot            Take a screenshot of the page             │   │
│  │  ...                                                                 │   │
│  │                                                                      │   │
│  └──────────────────────────────────────────────────────────────────────┘   │
│                                                                             │
│  [Space] toggle    [Enter] save    [Esc] cancel    [/] search               │
└─────────────────────────────────────────────────────────────────────────────┘
```

---

## Tab 3: Proxies

### Default View (List Only)

The default view shows just the proxy list. Forms are accessed via keypress (`a` to add, `e` to edit).

```
┌─ Proxies ──────────────────────────────────────────────────────────────────┐
│                                                                            │
│  ● Wildcat                                                                 │
│    http://localhost:4200/mcp/wildcat                                       │
│    Namespaces: 3    Tools: 29    Transport: SSE                            │
│                                                                            │
│  ○ BubbleCore                                                              │
│    http://localhost:4388/mcp/bubble                                        │
│    Namespaces: 4    Tools: 12    Transport: Streamable                     │
│                                                                            │
│                                                                            │
│                                                                            │
│                                                                            │
│                                                                            │
├────────────────────────────────────────────────────────────────────────────┤
│  [a]dd  [e]dit  [d]elete  [s]tart  [x]stop  [c]opy URL  [Enter] details   │
└────────────────────────────────────────────────────────────────────────────┘
```

### Add/Edit Proxy Form (Modal - `a` or `e` key)

Opens as a centered modal overlay:

```
┌─ Create Proxy ─────────────────────────────────────────────────────────────┐
│                                                                            │
│  Name                                                                      │
│  ┌──────────────────────────────────────────────────────────────────────┐  │
│  │ Wildcat                                                              │  │
│  └──────────────────────────────────────────────────────────────────────┘  │
│                                                                            │
│  Path Segment                                                              │
│  ┌──────────────────────────────────────────────────────────────────────┐  │
│  │ wildcat                                                              │  │
│  └──────────────────────────────────────────────────────────────────────┘  │
│                                                                            │
│  Host                                                                      │
│  ┌──────────────────────────────────────────────────────────────────────┐  │
│  │ localhost                                                            │  │
│  └──────────────────────────────────────────────────────────────────────┘  │
│                                                                            │
│  Port                                                                      │
│  ┌──────────────────────────────────────────────────────────────────────┐  │
│  │ 0 (auto-assign)                                                      │  │
│  └──────────────────────────────────────────────────────────────────────┘  │
│                                                                            │
│  Transport                                                                 │
│  ┌──────────────────────────────────────────────────────────────────────┐  │
│  │ SSE                                                              ▼   │  │
│  └──────────────────────────────────────────────────────────────────────┘  │
│                                                                            │
│                        [ Cancel ]    [ Save ]                              │
│                                                                            │
│  [Tab] next field    [Enter] save    [Esc] cancel                         │
└────────────────────────────────────────────────────────────────────────────┘
```

### Proxy Detail View

```
┌─ Proxy: Wildcat ────────────────────────────────────────────────────────────┐
│                                                                             │
│  Status: ● Running          Transport: SSE                                  │
│  URL: http://localhost:4200/mcp/wildcat                          [c]opy    │
│                                                                             │
│  ┌─ Bound Namespaces ───────────────────────────────────────────────────┐   │
│  │  [●] chrome         24 tools                                         │   │
│  │  [●] test           15 tools                                         │   │
│  │  [ ] work            8 tools                                         │   │
│  └──────────────────────────────────────────────────────────────────────┘   │
│                                                                             │
│  ┌─ Exposed Tools (39 total) ───────────────────────────────────────────┐   │
│  │  [chrome-dev-tools] click           Click an element by selector     │   │
│  │  [chrome-dev-tools] screenshot      Take a screenshot                │   │
│  │  [obsidian] read_note               Read a note from vault           │   │
│  │  [obsidian] search_notes            Search notes by content          │   │
│  │  ...                                                                 │   │
│  └──────────────────────────────────────────────────────────────────────┘   │
│                                                                             │
│  [s]tart  [x]stop  [n]amespaces  [e]dit  [d]elete  [c]opy URL  [Esc]back   │
└─────────────────────────────────────────────────────────────────────────────┘
```

---

## Navigation & Keybindings

### Global Keys

| Key | Action |
|-----|--------|
| `1` | Switch to Servers tab |
| `2` | Switch to Namespaces tab |
| `3` | Switch to Proxies tab |
| `Tab` | Next field (in forms) / Toggle log panel focus |
| `Shift+Tab` | Previous field (in forms) |
| `Esc` | Close modal / Go back / Clear filter |
| `?` | Show help overlay (context-aware) |
| `q` | Quit application (confirm if servers running) |
| `Ctrl+C` | Force quit (always quits, no repurposing) |

### List Navigation

| Key | Action |
|-----|--------|
| `j` / `↓` | Move down |
| `k` / `↑` | Move up |
| `g` | Go to first item |
| `G` | Go to last item |
| `Enter` | View details / select |
| `/` | Search/filter |
| `n` | Next search match |
| `N` / `p` | Previous search match |
| `Esc` | Clear filter / go back |

### Server Actions

| Key | Action |
|-----|--------|
| `a` | Add new server |
| `e` | Edit selected server |
| `d` | Delete selected server (with confirm + highlight) |
| `s` | Start selected server |
| `x` | Stop selected server |
| `r` | Restart selected server |
| `l` | Toggle log panel (bottom pane) |
| `L` | Full-screen log viewer |
| `f` | Follow/tail logs (auto-scroll) |
| `Enter` | View server details |

### Namespace Actions

| Key | Action |
|-----|--------|
| `a` | Add new namespace |
| `e` | Edit selected namespace |
| `d` | Delete selected namespace |
| `t` | Open tool permissions modal |
| `Enter` | View namespace details |

### Proxy Actions

| Key | Action |
|-----|--------|
| `a` | Add new proxy |
| `e` | Edit selected proxy |
| `d` | Delete selected proxy |
| `s` | Start selected proxy |
| `x` | Stop selected proxy |
| `n` | Manage namespace bindings |
| `c` | Copy URL to clipboard |
| `Enter` | View proxy details |

### Form Keys

| Key | Action |
|-----|--------|
| `Tab` | Next field |
| `Shift+Tab` | Previous field |
| `Enter` | Submit form (when on button) |
| `Esc` | Cancel / close form |
| `Space` | Toggle checkbox |

### Modal Keys

| Key | Action |
|-----|--------|
| `Enter` | Confirm / save |
| `Esc` | Cancel / close |
| `Space` | Toggle selected item |

---

## Color Scheme

Use terminal's default colors with semantic meaning:

| Element | Color | Usage |
|---------|-------|-------|
| Running/Success | Green | Running status, enabled items |
| Error | Red | Error status, delete actions |
| Warning | Yellow | Starting/stopping, expiring soon |
| Stopped/Disabled | Gray/Dim | Stopped status, disabled items |
| Selected | Reverse/Bold | Currently selected list item |
| Active Tab | Bold/Underline | Current tab |
| Borders | Default | Box drawing characters |
| Accent | Cyan | URLs, links, highlights |

---

## Responsive Behavior

### Layout Philosophy

**List-first design**: Default view shows only the list. Forms/modals appear on demand via keypress.

- `a` → Add form (modal overlay)
- `e` → Edit form (modal overlay)
- `Enter` → Detail view (replaces list, `Esc` to return)
- `l` → Toggle log panel (bottom split)

### Narrow Terminal (< 100 cols)

- List items show condensed info (name, status only)
- Modals take full screen width with small margins
- Detail views replace the list entirely
- Truncate long names/paths with `...`
- Breadcrumb shows location: `Servers › obsidian`

### Standard Terminal (100-140 cols)

- List items show full info (name, status, command, metadata)
- Modals centered with comfortable margins (~80% width)
- Log panel toggleable at bottom (when visible, list shrinks)
- Detail views can show side-by-side info

### Wide Terminal (> 140 cols)

- List items show extended info (timestamps, full paths)
- Detail view with logs: list + detail + logs visible simultaneously
- Modals remain centered, don't stretch too wide (max ~100 cols)

### Minimum Size

- 80 columns × 24 rows minimum
- Show warning if terminal too small
- Graceful degradation: hide secondary info, not functionality

---

## Transient Status Messages (Toasts)

Short-lived messages that don't require modal confirmation:

```
┌─────────────────────────────────────────────────────────────────────────────┐
│ MCP Studio                              [1]Servers [2]Namespaces [3]Proxies │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│  ... content ...                                                            │
│                                                                             │
├─────────────────────────────────────────────────────────────────────────────┤
│ ✓ Server "obsidian" started                                      (2s)      │
├─────────────────────────────────────────────────────────────────────────────┤
│ 4/5 servers running │ 2/2 proxies running │ 47 tools             │ ?=help  │
└─────────────────────────────────────────────────────────────────────────────┘
```

| Type | Icon | Color | Duration | Examples |
|------|------|-------|----------|----------|
| Success | ✓ | Green | 3s | "Saved", "Started", "Copied to clipboard" |
| Info | ℹ | Cyan | 3s | "Refreshing...", "12 tools discovered" |
| Warning | ⚠ | Yellow | 5s | "Token expiring soon", "Server slow to respond" |
| Error | ✖ | Red | 5s+ | "Failed to connect", "Permission denied" |

Toasts appear above status bar and auto-dismiss. Press any key to dismiss early.

---

## Focus Indicators

Strong visual cues for which pane/element has focus:

### Pane Focus
- **Focused pane**: Bold border, highlighted title
- **Unfocused pane**: Dim border, normal title

```
Focused:                          Unfocused:
┏━ Servers (focused) ━━━━━━━━━┓   ┌─ Details ─────────────────┐
┃  ● obsidian         Running ┃   │  Status: Running          │
┃  ○ chrome-dev       Stopped ┃   │  PID: 12847               │
┗━━━━━━━━━━━━━━━━━━━━━━━━━━━━━┛   └────────────────────────────┘
```

### List Item Selection
- **Selected row**: Reverse video (inverted colors) + `>` marker
- **Hover** (if mouse enabled): Underline

```
  ● obsidian            Running
> ○ chrome-dev-tools    Stopped   ← selected (reverse video)
  ● context7            Running
```

### Form Field Focus
- **Focused field**: Highlighted label, visible cursor, box highlight
- **Unfocused field**: Normal rendering

---

## Breadcrumbs (Narrow Mode)

When in focus mode or nested views, show location:

```
┌─ Servers › obsidian › Edit ────────────────────────────────────────────────┐
│                                                                            │
│  Name: [obsidian                    ]                                      │
│  Command: [npx -y @anthropic/mcp-obsidian]                                 │
│  ...                                                                       │
│                                                                            │
│  [Esc] back    [Enter] save                                                │
└────────────────────────────────────────────────────────────────────────────┘
```

---

## Log Viewer Modes

### Collapsed (default)
- Hidden until toggled with `l`
- Shows toast on new errors: "Error in obsidian: ENOENT"

### Bottom Panel (toggle with `l`)
- 5-10 lines visible
- Auto-scrolls (follow mode)
- Shows all servers' logs interleaved with `[server]` prefix

```
┌─ Process Logs ──────────────────────────────────────────────────────────────┐
│ [obsidian] 10:42:15 MCP server started                                     │
│ [obsidian] 10:42:15 Registered 12 tools                                    │
│ [context7] 10:42:18 Connected to API                                       │
│ [clippy] 10:42:20 ERROR: ENOENT: command not found                         │
└─────────────────────────────────────────────────────────────────────── [f] ┘
```

### Full-Screen (toggle with `L`)
- Dedicated log view with full scrollback
- Filter by server, log level
- Search with `/`
- Copy selection with `y`
- Export to file with `E`

```
┌─ Logs: All Servers ─────────────────────────────────────────────────────────┐
│ Filter: [all servers ▼]  Level: [all ▼]  Search: [          ]              │
├─────────────────────────────────────────────────────────────────────────────┤
│ 10:42:15 [obsidian] [INFO] MCP server started on stdio                     │
│ 10:42:15 [obsidian] [INFO] Registered 12 tools                             │
│ 10:42:18 [context7] [INFO] Connected to context7 API                       │
│ 10:42:20 [clippy] [ERROR] ENOENT: command not found                        │
│ 10:44:22 [obsidian] [DEBUG] Tool invoked: read_note                        │
│ ...                                                                        │
├─────────────────────────────────────────────────────────────────────────────┤
│ [f]ollow  [/]search  [c]opy  [E]xport  [Esc]close             Line 42/1337 │
└─────────────────────────────────────────────────────────────────────────────┘
```

---

## TUI Component Library (Bubbles)

| Component | Bubbles Component | Usage |
|-----------|-------------------|-------|
| Server list | `list.Model` | Main lists with filtering |
| Form fields | `huh` library | Multi-field forms |
| Text input | `textinput.Model` | Single line inputs |
| Text area | `textarea.Model` | Multi-line (env vars, description) |
| Checkbox | Custom or `huh` | Server selection, tool toggles |
| Dropdown | `huh` select | Server type, transport type |
| Tabs | Custom | Tab bar navigation |
| Table | `table.Model` | Tool lists, log viewer |
| Help | `help.Model` | Keyboard shortcut hints |
| Spinner | `spinner.Model` | Loading states |
| Viewport | `viewport.Model` | Scrollable log viewer |

---

## Inspirations

TUI apps with similar patterns to reference:

- **lazygit** - Split panels, keyboard-driven, modal dialogs
- **k9s** - Status indicators, resource lists, detail views
- **htop** - Real-time updates, status bar, color coding
- **charm/gum** - Form patterns, styling approach

---

## Phase Implementation Notes

### Phase 1
- Basic layout with tab bar (tabs disabled except Servers)
- Server list component (no form yet)
- Status bar with help hint

### Phase 2
- Server form panel
- Server detail view
- Log viewer component
- All server keybindings

### Phase 3
- Namespaces tab enabled
- Namespace form and list
- Tool permissions modal
- Server picker component

### Phase 4
- Proxies tab enabled
- Proxy form and list
- Proxy detail view
- Namespace binding modal
- URL copy to clipboard

### Phase 5
- OAuth status badges in server list
- OAuth authorization flow UI
- Import/export dialogs
- Help overlay polish
- Loading spinners throughout

---

## Bubble Tea / Lipgloss Implementation Patterns

Reference patterns from popular TUI apps in the Bubble Tea ecosystem:
- **[charmbracelet/glow](https://github.com/charmbracelet/glow)** - List/detail navigation, viewport, consistent theming
- **[charmbracelet/soft-serve](https://github.com/charmbracelet/soft-serve)** - Complex multi-pane UI, tabs, "app shell" structure
- **[dlvhdr/gh-dash](https://github.com/dlvhdr/gh-dash)** - Split panes, list+detail, key help, searching/filtering

**Note:** lazygit and k9s are excellent UX references but use different TUI stacks. Borrow their interaction design (dense tables, clear focus, fast filtering, strong keybind discoverability).

---

### Theme-First Approach

Centralize semantic styles in a `Theme` struct so the app asks for "FocusedBorder" / "MutedText" / "Danger" rather than hardcoding colors everywhere.

```go
type Theme struct {
    // Text
    Base, Muted, Faint, Title lipgloss.Style

    // Accents
    Primary, Success, Warn, Danger lipgloss.Style

    // Chrome
    App, Pane, PaneFocused, Tabs, Tab, TabActive lipgloss.Style

    // Lists
    Item, ItemSelected, ItemDim lipgloss.Style

    // Toasts
    ToastInfo, ToastWarn, ToastErr lipgloss.Style
}

func NewTheme() Theme {
    primary := lipgloss.AdaptiveColor{Light: "#0B5FFF", Dark: "#7AA2F7"}
    success := lipgloss.AdaptiveColor{Light: "#0F7B0F", Dark: "#9ECE6A"}
    warn := lipgloss.AdaptiveColor{Light: "#8A5A00", Dark: "#E0AF68"}
    danger := lipgloss.AdaptiveColor{Light: "#B00020", Dark: "#F7768E"}
    border := lipgloss.AdaptiveColor{Light: "#D0D7DE", Dark: "#3B4261"}

    return Theme{
        Base:  lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#111827", Dark: "#C0CAF5"}),
        Muted: lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#6B7280", Dark: "#A9B1D6"}),
        Faint: lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#9CA3AF", Dark: "#565F89"}),
        Title: lipgloss.NewStyle().Bold(true),

        Primary: lipgloss.NewStyle().Foreground(primary),
        Success: lipgloss.NewStyle().Foreground(success),
        Warn:    lipgloss.NewStyle().Foreground(warn),
        Danger:  lipgloss.NewStyle().Foreground(danger),

        App: lipgloss.NewStyle().Padding(1, 2),

        Pane: lipgloss.NewStyle().
            BorderStyle(lipgloss.RoundedBorder()).
            BorderForeground(border).
            Padding(0, 1),

        PaneFocused: lipgloss.NewStyle().
            BorderStyle(lipgloss.RoundedBorder()).
            BorderForeground(primary).
            Padding(0, 1),

        Tabs: lipgloss.NewStyle().Padding(0, 1),
        Tab: lipgloss.NewStyle().
            Padding(0, 1).
            Foreground(lipgloss.AdaptiveColor{Light: "#374151", Dark: "#A9B1D6"}),
        TabActive: lipgloss.NewStyle().
            Padding(0, 1).
            Bold(true).
            Foreground(primary).
            BorderStyle(lipgloss.NormalBorder()).
            BorderBottom(true).
            BorderForeground(primary),

        Item:         lipgloss.NewStyle().Padding(0, 1),
        ItemSelected: lipgloss.NewStyle().Padding(0, 1).Bold(true).Foreground(primary),
        ItemDim:      lipgloss.NewStyle().Padding(0, 1).Foreground(lipgloss.AdaptiveColor{Light: "#9CA3AF", Dark: "#565F89"}),

        ToastInfo: lipgloss.NewStyle().Padding(0, 1).Foreground(lipgloss.Color("#FFFFFF")).Background(lipgloss.Color("#2563EB")),
        ToastWarn: lipgloss.NewStyle().Padding(0, 1).Foreground(lipgloss.Color("#111827")).Background(lipgloss.Color("#F59E0B")),
        ToastErr:  lipgloss.NewStyle().Padding(0, 1).Foreground(lipgloss.Color("#FFFFFF")).Background(lipgloss.Color("#DC2626")),
    }
}
```

---

### Status Indicators (Pill Style)

Pill-style status reads instantly in dense server lists:

```go
type Status int
const (
    StatusRunning Status = iota
    StatusStopped
    StatusError
    StatusStarting
    StatusStopping
)

func StatusPill(t Theme, s Status) string {
    pill := lipgloss.NewStyle().Padding(0, 1).Bold(true)
    switch s {
    case StatusRunning:
        return pill.Background(lipgloss.Color("#14532D")).
            Foreground(lipgloss.Color("#DCFCE7")).Render("● RUN")
    case StatusStopped:
        return pill.Background(lipgloss.Color("#374151")).
            Foreground(lipgloss.Color("#E5E7EB")).Render("○ STOP")
    case StatusStarting, StatusStopping:
        return pill.Background(lipgloss.Color("#713F12")).
            Foreground(lipgloss.Color("#FEF3C7")).Render("◐ ...")
    default:
        return pill.Background(lipgloss.Color("#7F1D1D")).
            Foreground(lipgloss.Color("#FEE2E2")).Render("✖ ERR")
    }
}
```

---

### Focused vs Unfocused Panes

The "pro" feel: only the active pane is bright, unfocused panes dim:

```go
func RenderPane(t Theme, focused bool, title, body string) string {
    chrome := t.Pane
    titleStyle := t.Title
    if focused {
        chrome = t.PaneFocused
    } else {
        titleStyle = titleStyle.Foreground(lipgloss.AdaptiveColor{Light: "#6B7280", Dark: "#565F89"})
        body = t.Muted.Render(body)
    }
    return chrome.Render(titleStyle.Render(title) + "\n" + body)
}
```

---

### Selected List Items (lazygit/k9s Style)

Left "bar" + bold makes selection obvious without heavy backgrounds:

```go
func SelectedRow(t Theme, content string) string {
    bar := lipgloss.NewStyle().
        Foreground(lipgloss.AdaptiveColor{Light: "#0B5FFF", Dark: "#7AA2F7"}).
        Render("▌")
    return bar + t.ItemSelected.Render(content)
}

func NormalRow(t Theme, content string) string {
    return " " + t.Item.Render(content)
}
```

For the server list, use `bubbles/list` with a custom `ItemDelegate`:
- Leading status glyph/pill
- Secondary muted metadata line (command, namespace)
- Right-aligned stats (pid, uptime)

---

### Tab Bar Rendering

Simple, stable-width, underline active:

```go
func RenderTabs(t Theme, labels []string, active int) string {
    var out []string
    for i, lab := range labels {
        label := fmt.Sprintf("[%d]%s", i+1, lab)
        if i == active {
            out = append(out, t.TabActive.Render(label))
        } else {
            out = append(out, t.Tab.Render(label))
        }
    }
    return t.Tabs.Render(lipgloss.JoinHorizontal(lipgloss.Top, out...))
}
```

---

### Huh Forms Integration

For Add/Edit Server forms, leverage Huh's built-in themes and validators:

```go
form := huh.NewForm(
    huh.NewGroup(
        huh.NewInput().
            Title("Server name").
            Value(&name).
            Validate(huh.ValidateNotEmpty()),
        huh.NewSelect[string]().
            Title("Type").
            Options(
                huh.NewOption("MCP STDIO", "stdio"),
                huh.NewOption("MCP SSE", "sse"),
            ).
            Value(&serverType),
        huh.NewInput().
            Title("Command").
            Value(&cmd).
            Validate(huh.ValidateMinLength(2)),
        huh.NewInput().
            Title("Arguments").
            Value(&args),
        huh.NewInput().
            Title("Working Dir").
            Value(&cwd),
        huh.NewText().
            Title("Env Variables").
            Value(&envVars).
            CharLimit(500),
    ),
).WithTheme(huh.ThemeBase16())
```

Available Huh themes: `ThemeCharm()`, `ThemeDracula()`, `ThemeCatppuccin()`, `ThemeBase16()`, `ThemeBase()`

---

### Toast/Notification Pattern

Non-blocking, auto-dismiss notifications:

```go
type Toast struct {
    Msg   string
    Level string // "info" | "warn" | "err"
    Alive bool
}

type clearToastMsg struct{}

func (m Model) renderToast() string {
    if !m.toast.Alive || m.toast.Msg == "" {
        return ""
    }
    var s lipgloss.Style
    switch m.toast.Level {
    case "warn":
        s = m.theme.ToastWarn
    case "err":
        s = m.theme.ToastErr
    default:
        s = m.theme.ToastInfo
    }
    box := s.Render(m.toast.Msg)
    return lipgloss.Place(m.w, m.h, lipgloss.Right, lipgloss.Bottom, box,
        lipgloss.WithWhitespaceForeground(lipgloss.NoColor{}))
}

// Trigger toast with auto-clear (in Update)
m.toast = Toast{Msg: "Server started", Level: "info", Alive: true}
return m, tea.Tick(3*time.Second, func(time.Time) tea.Msg { return clearToastMsg{} })

// Handle clear (in Update)
case clearToastMsg:
    m.toast.Alive = false
```

---

### Log Viewer Pattern

Use `bubbles/viewport` with filter and follow mode:

```go
type LogViewer struct {
    viewport viewport.Model
    filter   textinput.Model
    lines    []string
    follow   bool // auto-scroll to bottom on new logs
}

func NewLogViewer(width, height int) LogViewer {
    vp := viewport.New(width, height)
    fi := textinput.New()
    fi.Placeholder = "Filter..."
    return LogViewer{viewport: vp, filter: fi, follow: true}
}

func (l *LogViewer) AppendLog(line string) {
    l.lines = append(l.lines, line)
    l.rebuildContent()
    if l.follow {
        l.viewport.GotoBottom()
    }
}

func (l *LogViewer) rebuildContent() {
    pattern := l.filter.Value()
    var filtered []string
    for _, line := range l.lines {
        if pattern == "" || strings.Contains(line, pattern) {
            filtered = append(filtered, line)
        }
    }
    l.viewport.SetContent(strings.Join(filtered, "\n"))
}

func (l *LogViewer) ToggleFollow() {
    l.follow = !l.follow
    if l.follow {
        l.viewport.GotoBottom()
    }
}
```

---

### Modal Overlay Pattern

Root model swallows input when modal is open, renders overlay centered:

```go
type Focus int
const (
    FocusLeft Focus = iota
    FocusRight
)

type Model struct {
    w, h   int
    theme  Theme
    focus  Focus
    tabs   int

    servers list.Model
    logs    viewport.Model

    modal tea.Model // nil when closed
    toast Toast
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
    switch msg := msg.(type) {
    case tea.WindowSizeMsg:
        m.w, m.h = msg.Width, msg.Height
        // recompute child sizes here

    case tea.KeyMsg:
        // Modal captures all input when open
        if m.modal != nil {
            if msg.String() == "esc" {
                m.modal = nil
                return m, nil
            }
            var cmd tea.Cmd
            m.modal, cmd = m.modal.Update(msg)
            return m, cmd
        }

        switch msg.String() {
        case "tab":
            if m.focus == FocusLeft {
                m.focus = FocusRight
            } else {
                m.focus = FocusLeft
            }
        case "t": // Open tool permissions modal
            m.modal = NewToolPermissionsModal(m.theme, m.w, m.h)
        }
    }

    // Route to focused child when no modal
    if m.modal == nil {
        if m.focus == FocusLeft {
            var cmd tea.Cmd
            m.servers, cmd = m.servers.Update(msg)
            return m, cmd
        }
        var cmd tea.Cmd
        m.logs, cmd = m.logs.Update(msg)
        return m, cmd
    }

    return m, nil
}

func (m Model) View() string {
    base := m.renderMainUI()

    if m.modal != nil {
        modalView := m.modal.View()
        // Semi-transparent overlay effect via whitespace styling
        return lipgloss.Place(m.w, m.h, lipgloss.Center, lipgloss.Center, modalView,
            lipgloss.WithWhitespaceChars(" "),
            lipgloss.WithWhitespaceForeground(lipgloss.AdaptiveColor{Light: "#E5E7EB", Dark: "#1F2937"}))
    }

    // Overlay toast if active
    if m.toast.Alive {
        toast := m.renderToast()
        return lipgloss.JoinVertical(lipgloss.Left, base, toast)
    }

    return base
}
```

---

### Component Mapping

| UI Spec Component | Bubbles/Pattern | Notes |
|-------------------|-----------------|-------|
| Server list | `bubbles/list` + custom `ItemDelegate` | Status pill, metadata line, right-aligned stats |
| Add Server form | `huh.Form` | Left panel or modal, use `ThemeBase16()` |
| Server detail | `bubbles/viewport` for tools list | Scrollable content area |
| Process Logs | `bubbles/viewport` + `textinput` | Follow mode, filter, level colors |
| Tab bar | Custom + `lipgloss.JoinHorizontal` | Number shortcuts, underline active |
| Tool permissions modal | Overlay model + `bubbles/list` | Checkbox-style items, bulk actions |
| Status bar | `lipgloss.JoinHorizontal` | Fixed at bottom, counts + help hint |
| Toasts | Overlay + `lipgloss.Place` + `tea.Tick` | Auto-dismiss after 3-5s |
| Help overlay | `bubbles/help` or custom modal | Context-aware keybindings |
| Dropdowns | `huh.Select` | Server type, transport type |
| Text areas | `huh.Text` or `bubbles/textarea` | Env vars, description fields |
| Spinners | `bubbles/spinner` | Starting/stopping states |