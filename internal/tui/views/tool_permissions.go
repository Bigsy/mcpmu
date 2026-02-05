package views

import (
	"fmt"
	"io"
	"strings"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/events"
	"github.com/Bigsy/mcpmu/internal/server"
	"github.com/Bigsy/mcpmu/internal/tui/theme"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ToolPermissionsResult is sent when the user finishes editing permissions.
type ToolPermissionsResult struct {
	// Changes contains permission changes (tool name -> enabled)
	// Map key is "serverID:toolName"
	Changes map[string]bool
	// Deletions contains permissions to remove (revert to default)
	// Map key is "serverID:toolName"
	Deletions []string
	Submitted bool
	// AutoStartedServers contains IDs of servers that were auto-started for this session
	// The caller should stop these when the modal closes
	AutoStartedServers []string
}

// toolPermItem represents a tool in the permission editor.
type toolPermItem struct {
	serverID    string
	serverName  string
	toolName    string
	description string
	enabled     bool
	isHeader    bool // True for server headers
}

func (i toolPermItem) Title() string {
	if i.isHeader {
		return i.serverName
	}
	return i.toolName
}
func (i toolPermItem) Description() string { return i.description }
func (i toolPermItem) FilterValue() string { return i.toolName }

// ToolPermissionsModel is a modal for editing tool permissions.
type ToolPermissionsModel struct {
	theme       theme.Theme
	visible     bool
	list        list.Model
	width       int
	height      int
	namespaceID string

	// Original permissions for detecting changes
	originalPerms map[string]bool // "serverID:toolName" -> enabled
	currentPerms  map[string]bool

	// If true, unconfigured tools default to denied
	denyByDefault bool

	// Auto-start tracking
	autoStartedServers []string // Servers we started for this session
	discovering        bool     // True while waiting for tools
	discoveryTimeout   bool     // True if discovery timed out

	// Key bindings
	escKey        key.Binding
	enterKey      key.Binding
	spaceKey      key.Binding
	enableSafeKey key.Binding
	denyAllKey    key.Binding
}

// NewToolPermissions creates a new tool permissions editor.
func NewToolPermissions(th theme.Theme) ToolPermissionsModel {
	delegate := newToolPermDelegate(th, make(map[string]bool), false)
	l := list.New([]list.Item{}, delegate, 0, 0)
	l.Title = "Tool Permissions"
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(true)
	l.SetShowHelp(false)
	l.Styles.Title = th.Title
	l.FilterInput.PromptStyle = th.Primary
	l.FilterInput.Cursor.Style = th.Primary

	return ToolPermissionsModel{
		theme:         th,
		list:          l,
		originalPerms: make(map[string]bool),
		currentPerms:  make(map[string]bool),
		escKey: key.NewBinding(
			key.WithKeys("esc"),
			key.WithHelp("esc", "cancel"),
		),
		enterKey: key.NewBinding(
			key.WithKeys("enter"),
			key.WithHelp("enter", "save"),
		),
		spaceKey: key.NewBinding(
			key.WithKeys(" "),
			key.WithHelp("space", "toggle"),
		),
		enableSafeKey: key.NewBinding(
			key.WithKeys("a"),
			key.WithHelp("a", "enable-safe"),
		),
		denyAllKey: key.NewBinding(
			key.WithKeys("d"),
			key.WithHelp("d", "deny-all"),
		),
	}
}

// Show displays the editor with tools from running servers.
// serverTools maps serverName -> list of tools
// permissions are the current explicit permissions
func (m *ToolPermissionsModel) Show(
	namespaceName string,
	serverTools map[string][]events.McpTool,
	servers []config.ServerEntry,
	permissions []config.ToolPermission,
	denyByDefault bool,
) {
	m.visible = true
	m.namespaceID = namespaceName
	m.denyByDefault = denyByDefault
	m.originalPerms = make(map[string]bool)
	m.currentPerms = make(map[string]bool)

	// Build permission lookup
	for _, perm := range permissions {
		if perm.Namespace == namespaceName {
			key := perm.Server + ":" + perm.ToolName
			m.originalPerms[key] = perm.Enabled
			m.currentPerms[key] = perm.Enabled
		}
	}

	// Build list items
	var items []list.Item
	for serverName, tools := range serverTools {
		if len(tools) == 0 {
			continue
		}

		// Add server header
		items = append(items, toolPermItem{
			serverID:   serverName,
			serverName: serverName,
			isHeader:   true,
		})

		// Add tools
		for _, tool := range tools {
			key := serverName + ":" + tool.Name
			enabled, hasExplicit := m.currentPerms[key]
			if !hasExplicit {
				// Use namespace default
				enabled = !denyByDefault
			}

			items = append(items, toolPermItem{
				serverID:    serverName,
				serverName:  serverName,
				toolName:    tool.Name,
				description: tool.Description,
				enabled:     enabled,
				isHeader:    false,
			})
		}
	}

	m.list.SetItems(items)
	m.list.SetDelegate(newToolPermDelegate(m.theme, m.currentPerms, m.denyByDefault))
}

// Hide hides the editor.
func (m *ToolPermissionsModel) Hide() {
	m.visible = false
	m.discovering = false
	m.discoveryTimeout = false
}

// IsVisible returns whether the editor is visible.
func (m ToolPermissionsModel) IsVisible() bool {
	return m.visible
}

// ShowDiscovering shows the discovering tools state.
// autoStartedServers contains IDs of servers that were started for this session.
func (m *ToolPermissionsModel) ShowDiscovering(namespaceID string, autoStartedServers []string) {
	m.visible = true
	m.discovering = true
	m.discoveryTimeout = false
	m.namespaceID = namespaceID
	m.autoStartedServers = autoStartedServers
	m.originalPerms = make(map[string]bool)
	m.currentPerms = make(map[string]bool)
	m.list.SetItems([]list.Item{})
}

// IsDiscovering returns whether the editor is in discovery mode.
func (m ToolPermissionsModel) IsDiscovering() bool {
	return m.discovering
}

// SetDiscoveryTimeout marks that discovery timed out.
func (m *ToolPermissionsModel) SetDiscoveryTimeout() {
	m.discoveryTimeout = true
}

// GetAutoStartedServers returns the list of servers that were auto-started.
func (m ToolPermissionsModel) GetAutoStartedServers() []string {
	return m.autoStartedServers
}

// ClearAutoStartedServers clears the list of auto-started servers.
func (m *ToolPermissionsModel) ClearAutoStartedServers() {
	m.autoStartedServers = nil
}

// FinishDiscovery transitions from discovery to editing mode.
func (m *ToolPermissionsModel) FinishDiscovery(
	serverTools map[string][]events.McpTool,
	servers []config.ServerEntry,
	permissions []config.ToolPermission,
	denyByDefault bool,
) {
	m.discovering = false
	m.discoveryTimeout = false
	m.denyByDefault = denyByDefault

	// Build permission lookup
	for _, perm := range permissions {
		if perm.Namespace == m.namespaceID {
			key := perm.Server + ":" + perm.ToolName
			m.originalPerms[key] = perm.Enabled
			m.currentPerms[key] = perm.Enabled
		}
	}

	// Build list items
	var items []list.Item
	for serverName, tools := range serverTools {
		if len(tools) == 0 {
			continue
		}

		// Add server header
		items = append(items, toolPermItem{
			serverID:   serverName,
			serverName: serverName,
			isHeader:   true,
		})

		// Add tools
		for _, tool := range tools {
			key := serverName + ":" + tool.Name
			enabled, hasExplicit := m.currentPerms[key]
			if !hasExplicit {
				enabled = !denyByDefault
			}

			items = append(items, toolPermItem{
				serverID:    serverName,
				serverName:  serverName,
				toolName:    tool.Name,
				description: tool.Description,
				enabled:     enabled,
				isHeader:    false,
			})
		}
	}

	m.list.SetItems(items)
	m.list.SetDelegate(newToolPermDelegate(m.theme, m.currentPerms, m.denyByDefault))
}

// SetSize sets the available size.
func (m *ToolPermissionsModel) SetSize(width, height int) {
	m.width = width
	m.height = height
	editorWidth := 70
	if width < 80 {
		editorWidth = width - 10
	}
	editorHeight := 25
	if height < 30 {
		editorHeight = height - 5
	}
	m.list.SetSize(editorWidth-6, editorHeight-6)
}

// Update handles messages.
func (m *ToolPermissionsModel) Update(msg tea.Msg) tea.Cmd {
	if !m.visible {
		return nil
	}

	// In discovering mode, only allow escape
	if m.discovering {
		if msg, ok := msg.(tea.KeyMsg); ok && key.Matches(msg, m.escKey) {
			autoStarted := m.autoStartedServers
			m.visible = false
			m.discovering = false
			m.autoStartedServers = nil
			return func() tea.Msg {
				return ToolPermissionsResult{
					Submitted:          false,
					AutoStartedServers: autoStarted,
				}
			}
		}
		return nil
	}

	// When filtering is active, let the list handle most keys
	// Only intercept escape when not filtering to close the modal
	if m.list.FilterState() == list.Filtering {
		var cmd tea.Cmd
		m.list, cmd = m.list.Update(msg)
		return cmd
	}

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch {
		case key.Matches(msg, m.escKey):
			// If filter is shown (even if not actively filtering), clear it first
			if m.list.FilterState() == list.FilterApplied {
				m.list.ResetFilter()
				return nil
			}
			autoStarted := m.autoStartedServers
			m.visible = false
			m.autoStartedServers = nil
			return func() tea.Msg {
				return ToolPermissionsResult{
					Submitted:          false,
					AutoStartedServers: autoStarted,
				}
			}
		case key.Matches(msg, m.enterKey):
			autoStarted := m.autoStartedServers
			m.visible = false
			m.autoStartedServers = nil
			// Calculate changes and deletions
			changes := make(map[string]bool)
			var deletions []string

			// Check for new or changed permissions
			for key, enabled := range m.currentPerms {
				orig, hadOrig := m.originalPerms[key]
				if !hadOrig || orig != enabled {
					changes[key] = enabled
				}
			}

			// Check for permissions that were removed (reverted to default)
			for key := range m.originalPerms {
				if _, stillExists := m.currentPerms[key]; !stillExists {
					deletions = append(deletions, key)
				}
			}

			return func() tea.Msg {
				return ToolPermissionsResult{
					Changes:            changes,
					Deletions:          deletions,
					Submitted:          true,
					AutoStartedServers: autoStarted,
				}
			}
		case key.Matches(msg, m.spaceKey):
			// Toggle permission
			if item := m.list.SelectedItem(); item != nil {
				ti := item.(toolPermItem)
				if !ti.isHeader {
					key := ti.serverID + ":" + ti.toolName
					current, has := m.currentPerms[key]
					if !has {
						// No explicit permission, use inverse of default
						current = !m.denyByDefault
					}
					newValue := !current
					// If new value matches default, remove explicit permission (revert to default)
					defaultValue := !m.denyByDefault
					if newValue == defaultValue {
						delete(m.currentPerms, key)
					} else {
						m.currentPerms[key] = newValue
					}
					// Update delegate
					m.list.SetDelegate(newToolPermDelegate(m.theme, m.currentPerms, m.denyByDefault))
				}
			}
			return nil
		case key.Matches(msg, m.enableSafeKey):
			// Enable all safe tools (those classified as ToolSafe)
			m.applyBulkEnableSafe()
			m.list.SetDelegate(newToolPermDelegate(m.theme, m.currentPerms, m.denyByDefault))
			return nil
		case key.Matches(msg, m.denyAllKey):
			// Deny all tools
			m.applyBulkDenyAll()
			m.list.SetDelegate(newToolPermDelegate(m.theme, m.currentPerms, m.denyByDefault))
			return nil
		}
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return cmd
}

// applyBulkEnableSafe enables all tools classified as safe (read-only).
// Unknown tools are left unchanged. Unsafe tools are not modified.
func (m *ToolPermissionsModel) applyBulkEnableSafe() {
	for _, item := range m.list.Items() {
		ti, ok := item.(toolPermItem)
		if !ok || ti.isHeader {
			continue
		}

		// Only modify tools classified as safe
		classification := server.ClassifyTool(ti.toolName)
		if classification != server.ToolSafe {
			continue
		}

		key := ti.serverID + ":" + ti.toolName
		// If the default is allow (denyByDefault=false), we need to explicitly set
		// only if it's currently denied. If default is deny, we need to explicitly allow.
		defaultValue := !m.denyByDefault
		if defaultValue {
			// Default is allow - remove any explicit deny
			delete(m.currentPerms, key)
		} else {
			// Default is deny - explicitly allow
			m.currentPerms[key] = true
		}
	}
}

// applyBulkDenyAll denies all tools.
func (m *ToolPermissionsModel) applyBulkDenyAll() {
	for _, item := range m.list.Items() {
		ti, ok := item.(toolPermItem)
		if !ok || ti.isHeader {
			continue
		}

		key := ti.serverID + ":" + ti.toolName
		// If default is deny (denyByDefault=true), we can remove explicit permissions
		// If default is allow, we need to explicitly deny
		defaultValue := !m.denyByDefault
		if !defaultValue {
			// Default is deny - remove any explicit allow
			delete(m.currentPerms, key)
		} else {
			// Default is allow - explicitly deny
			m.currentPerms[key] = false
		}
	}
}

// View renders the editor.
func (m ToolPermissionsModel) View() string {
	if !m.visible {
		return ""
	}
	return m.list.View()
}

// RenderOverlay renders the editor as a centered overlay.
func (m ToolPermissionsModel) RenderOverlay(base string, width, height int) string {
	if !m.visible {
		return base
	}

	editorWidth := 70
	if width < 80 {
		editorWidth = width - 10
	}

	var contentStr string

	if m.discovering {
		// Show discovering state
		var msg string
		if m.discoveryTimeout {
			msg = m.theme.Warn.Render("Timeout waiting for tools.") + "\n\n" +
				m.theme.Muted.Render("Some servers may not have responded.\nPress esc to cancel.")
		} else {
			msg = m.theme.Primary.Render("Discovering tools...") + "\n\n" +
				m.theme.Muted.Render("Starting servers and waiting for tool discovery.\nThis may take a few seconds.\n\nPress esc to cancel.")
		}
		contentStr = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(m.theme.Primary.GetForeground()).
			Padding(2, 4).
			Width(editorWidth).
			Render(m.theme.Title.Render("Tool Permissions") + "\n\n" + msg)
	} else {
		// Normal editing state
		var footer strings.Builder

		// Show selected tool description
		if item := m.list.SelectedItem(); item != nil {
			if ti, ok := item.(toolPermItem); ok && !ti.isHeader {
				desc := ti.description
				if desc == "" {
					desc = "(no description)"
				}
				// Wrap long descriptions
				maxDescWidth := editorWidth - 8
				if len(desc) > maxDescWidth {
					desc = desc[:maxDescWidth-3] + "..."
				}
				footer.WriteString(m.theme.Muted.Render(desc))
				footer.WriteString("\n")
			}
		}

		footer.WriteString(m.theme.Faint.Render("space=toggle  /=filter  a=enable-safe  d=deny-all  enter=save  esc=cancel"))
		if m.denyByDefault {
			footer.WriteString("\n")
			footer.WriteString(m.theme.Warn.Render("Namespace policy: deny unconfigured tools"))
		} else {
			footer.WriteString("\n")
			footer.WriteString(m.theme.Muted.Render("Namespace policy: allow unconfigured tools"))
		}

		contentStr = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(m.theme.Primary.GetForeground()).
			Padding(1, 2).
			Width(editorWidth).
			Render(m.View() + "\n\n" + footer.String())
	}

	return lipgloss.Place(
		width,
		height,
		lipgloss.Center,
		lipgloss.Center,
		contentStr,
		lipgloss.WithWhitespaceChars(" "),
		lipgloss.WithWhitespaceForeground(lipgloss.AdaptiveColor{Light: "#E5E7EB", Dark: "#1F2937"}),
	)
}

// toolPermDelegate renders items in the tool permissions editor.
type toolPermDelegate struct {
	theme         theme.Theme
	perms         map[string]bool
	denyByDefault bool
}

func newToolPermDelegate(th theme.Theme, perms map[string]bool, denyByDefault bool) toolPermDelegate {
	return toolPermDelegate{theme: th, perms: perms, denyByDefault: denyByDefault}
}

func (d toolPermDelegate) Height() int  { return 1 }
func (d toolPermDelegate) Spacing() int { return 0 }
func (d toolPermDelegate) Update(_ tea.Msg, _ *list.Model) tea.Cmd {
	return nil
}

func (d toolPermDelegate) Render(w io.Writer, m list.Model, index int, item list.Item) {
	ti, ok := item.(toolPermItem)
	if !ok {
		return
	}

	selected := index == m.Index()

	if ti.isHeader {
		// Server header
		header := d.theme.Title.Render("─── " + ti.serverName + " ───")
		_, _ = fmt.Fprint(w, header)
		return
	}

	cursor := "  "
	if selected {
		cursor = "> "
	}

	// Determine current state
	key := ti.serverID + ":" + ti.toolName
	enabled, hasExplicit := d.perms[key]
	if !hasExplicit {
		enabled = !d.denyByDefault
	}

	var checkbox string
	if enabled {
		checkbox = d.theme.Success.Render("[+]")
	} else {
		checkbox = d.theme.Danger.Render("[-]")
	}

	name := ti.toolName
	if selected {
		name = d.theme.ItemSelected.Render(name)
	} else if enabled {
		name = d.theme.Item.Render(name)
	} else {
		name = d.theme.Muted.Render(name)
	}

	// Show if explicitly configured vs default
	suffix := ""
	if !hasExplicit {
		suffix = d.theme.Faint.Render(" (default)")
	}

	_, _ = fmt.Fprintf(w, "%s%s %s%s", cursor, checkbox, name, suffix)
}
