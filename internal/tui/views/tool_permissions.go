package views

import (
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/hedworth/mcp-studio-go/internal/config"
	"github.com/hedworth/mcp-studio-go/internal/events"
	"github.com/hedworth/mcp-studio-go/internal/tui/theme"
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

	// Key bindings
	escKey   key.Binding
	enterKey key.Binding
	spaceKey key.Binding
}

// NewToolPermissions creates a new tool permissions editor.
func NewToolPermissions(th theme.Theme) ToolPermissionsModel {
	delegate := newToolPermDelegate(th, make(map[string]bool), false)
	l := list.New([]list.Item{}, delegate, 0, 0)
	l.Title = "Tool Permissions"
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(false)
	l.SetShowHelp(false)
	l.Styles.Title = th.Title

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
	}
}

// Show displays the editor with tools from running servers.
// serverTools maps serverID -> list of tools
// permissions are the current explicit permissions
func (m *ToolPermissionsModel) Show(
	namespaceID string,
	serverTools map[string][]events.McpTool,
	servers []config.ServerConfig,
	permissions []config.ToolPermission,
	denyByDefault bool,
) {
	m.visible = true
	m.namespaceID = namespaceID
	m.denyByDefault = denyByDefault
	m.originalPerms = make(map[string]bool)
	m.currentPerms = make(map[string]bool)

	// Build permission lookup
	for _, perm := range permissions {
		if perm.NamespaceID == namespaceID {
			key := perm.ServerID + ":" + perm.ToolName
			m.originalPerms[key] = perm.Enabled
			m.currentPerms[key] = perm.Enabled
		}
	}

	// Build server name lookup
	serverNames := make(map[string]string)
	for _, srv := range servers {
		name := srv.Name
		if name == "" {
			name = srv.Command
		}
		serverNames[srv.ID] = name
	}

	// Build list items
	var items []list.Item
	for serverID, tools := range serverTools {
		if len(tools) == 0 {
			continue
		}

		serverName := serverNames[serverID]
		if serverName == "" {
			serverName = serverID
		}

		// Add server header
		items = append(items, toolPermItem{
			serverID:   serverID,
			serverName: serverName,
			isHeader:   true,
		})

		// Add tools
		for _, tool := range tools {
			key := serverID + ":" + tool.Name
			enabled, hasExplicit := m.currentPerms[key]
			if !hasExplicit {
				// Use namespace default
				enabled = !denyByDefault
			}

			items = append(items, toolPermItem{
				serverID:    serverID,
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
}

// IsVisible returns whether the editor is visible.
func (m ToolPermissionsModel) IsVisible() bool {
	return m.visible
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

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch {
		case key.Matches(msg, m.escKey):
			m.visible = false
			return func() tea.Msg {
				return ToolPermissionsResult{Submitted: false}
			}
		case key.Matches(msg, m.enterKey):
			m.visible = false
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
					Changes:   changes,
					Deletions: deletions,
					Submitted: true,
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
		}
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return cmd
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

	var footer strings.Builder
	footer.WriteString(m.theme.Faint.Render("space=toggle  enter=save  esc=cancel"))
	if m.denyByDefault {
		footer.WriteString("\n")
		footer.WriteString(m.theme.Warn.Render("Namespace policy: deny unconfigured tools"))
	} else {
		footer.WriteString("\n")
		footer.WriteString(m.theme.Muted.Render("Namespace policy: allow unconfigured tools"))
	}

	content := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(m.theme.Primary.GetForeground()).
		Padding(1, 2).
		Width(editorWidth).
		Render(m.View() + "\n\n" + footer.String())

	return lipgloss.Place(
		width,
		height,
		lipgloss.Center,
		lipgloss.Center,
		content,
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
		fmt.Fprint(w, header)
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

	fmt.Fprintf(w, "%s%s %s%s", cursor, checkbox, name, suffix)
}
