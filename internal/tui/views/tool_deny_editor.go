package views

import (
	"fmt"
	"io"
	"strings"

	"github.com/Bigsy/mcpmu/internal/mcp"
	"github.com/Bigsy/mcpmu/internal/tui/theme"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ToolDenyResult is sent when the user finishes editing the deny list.
type ToolDenyResult struct {
	ServerName string
	// DeniedTools is the final set of denied tool names.
	DeniedTools []string
	Submitted   bool
}

// toolDenyItem represents a tool in the deny editor.
type toolDenyItem struct {
	toolName    string
	description string
	denied      bool
}

func (i toolDenyItem) Title() string       { return i.toolName }
func (i toolDenyItem) Description() string { return i.description }
func (i toolDenyItem) FilterValue() string { return i.toolName }

// ToolDenyEditorModel is a modal for editing a server's global deny list.
type ToolDenyEditorModel struct {
	theme      theme.Theme
	visible    bool
	list       list.Model
	width      int
	height     int
	serverName string

	// Current deny state: toolName -> denied
	denied map[string]bool

	// Filter
	filterInput   textinput.Model
	allItems      []list.Item // full unfiltered list, set in Show()
	filterFocused bool

	// Key bindings
	escKey   key.Binding
	enterKey key.Binding
	spaceKey key.Binding
}

// NewToolDenyEditor creates a new tool deny editor.
func NewToolDenyEditor(th theme.Theme) ToolDenyEditorModel {
	delegate := newToolDenyDelegate(th, make(map[string]bool))
	l := list.New([]list.Item{}, delegate, 0, 0)
	l.Title = "Denied Tools"
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(false)
	l.SetShowHelp(false)
	l.Styles.Title = th.Title

	ti := textinput.New()
	ti.Placeholder = "/ to filter..."
	ti.CharLimit = 100

	return ToolDenyEditorModel{
		theme:       th,
		list:        l,
		denied:      make(map[string]bool),
		filterInput: ti,
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
			key.WithHelp("space", "toggle deny"),
		),
	}
}

// Show displays the deny editor for a server's tools.
func (m *ToolDenyEditorModel) Show(serverName string, tools []mcp.Tool, deniedTools []string) {
	m.visible = true
	m.serverName = serverName
	m.denied = make(map[string]bool)

	for _, t := range deniedTools {
		m.denied[t] = true
	}

	var items []list.Item
	for _, tool := range tools {
		items = append(items, toolDenyItem{
			toolName:    tool.Name,
			description: tool.Description,
			denied:      m.denied[tool.Name],
		})
	}

	m.allItems = items
	m.filterInput.SetValue("")
	m.filterInput.Blur()
	m.filterFocused = false
	m.list.SetItems(items)
	m.list.SetDelegate(newToolDenyDelegate(m.theme, m.denied))
}

// IsVisible returns whether the editor is visible.
func (m ToolDenyEditorModel) IsVisible() bool {
	return m.visible
}

// SetSize sets the available size.
func (m *ToolDenyEditorModel) SetSize(width, height int) {
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
	m.list.SetSize(editorWidth-6, editorHeight-8)
}

// applyFilter filters the list items based on the current filter input value.
func (m *ToolDenyEditorModel) applyFilter() {
	query := strings.ToLower(m.filterInput.Value())
	if query == "" {
		m.list.SetItems(m.allItems)
		return
	}
	var filtered []list.Item
	for _, item := range m.allItems {
		ti := item.(toolDenyItem)
		if strings.Contains(strings.ToLower(ti.toolName), query) {
			filtered = append(filtered, item)
		}
	}
	m.list.SetItems(filtered)
}

// Update handles messages.
func (m *ToolDenyEditorModel) Update(msg tea.Msg) tea.Cmd {
	if !m.visible {
		return nil
	}

	kmsg, isKey := msg.(tea.KeyMsg)
	if !isKey {
		var cmd tea.Cmd
		m.list, cmd = m.list.Update(msg)
		return cmd
	}

	if m.filterFocused {
		switch {
		case key.Matches(kmsg, m.escKey):
			// Exit filter mode, keep filter text
			m.filterFocused = false
			m.filterInput.Blur()
			return nil
		case key.Matches(kmsg, m.enterKey):
			m.visible = false
			var denied []string
			for toolName, isDenied := range m.denied {
				if isDenied {
					denied = append(denied, toolName)
				}
			}
			return func() tea.Msg {
				return ToolDenyResult{
					ServerName:  m.serverName,
					DeniedTools: denied,
					Submitted:   true,
				}
			}
		case key.Matches(kmsg, m.spaceKey):
			if item := m.list.SelectedItem(); item != nil {
				ti := item.(toolDenyItem)
				m.denied[ti.toolName] = !m.denied[ti.toolName]
				m.list.SetDelegate(newToolDenyDelegate(m.theme, m.denied))
			}
			return nil
		case kmsg.Type == tea.KeyUp || kmsg.Type == tea.KeyDown:
			var cmd tea.Cmd
			m.list, cmd = m.list.Update(msg)
			return cmd
		default:
			// Send to textinput, then apply filter
			var cmd tea.Cmd
			m.filterInput, cmd = m.filterInput.Update(msg)
			m.applyFilter()
			return cmd
		}
	}

	// Action mode
	switch {
	case kmsg.Type == tea.KeyRunes && string(kmsg.Runes) == "/":
		m.filterFocused = true
		m.filterInput.Focus()
		return nil
	case key.Matches(kmsg, m.escKey):
		if m.filterInput.Value() != "" {
			m.filterInput.SetValue("")
			m.applyFilter()
			return nil
		}
		m.visible = false
		return func() tea.Msg {
			return ToolDenyResult{ServerName: m.serverName, Submitted: false}
		}
	case key.Matches(kmsg, m.enterKey):
		m.visible = false
		var denied []string
		for toolName, isDenied := range m.denied {
			if isDenied {
				denied = append(denied, toolName)
			}
		}
		return func() tea.Msg {
			return ToolDenyResult{
				ServerName:  m.serverName,
				DeniedTools: denied,
				Submitted:   true,
			}
		}
	case key.Matches(kmsg, m.spaceKey):
		if item := m.list.SelectedItem(); item != nil {
			ti := item.(toolDenyItem)
			m.denied[ti.toolName] = !m.denied[ti.toolName]
			m.list.SetDelegate(newToolDenyDelegate(m.theme, m.denied))
		}
		return nil
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return cmd
}

// View renders the editor list.
func (m ToolDenyEditorModel) View() string {
	if !m.visible {
		return ""
	}

	filterLabel := m.theme.Faint.Render("Filter: ")
	filterView := m.filterInput.View()
	filterBar := filterLabel + filterView

	listView := m.list.View()
	if len(m.list.Items()) == 0 && m.filterInput.Value() != "" {
		listView = "\n" + m.theme.Faint.Render("  No matching tools") + "\n"
	}

	return filterBar + "\n\n" + listView
}

// RenderOverlay renders the editor as a centered overlay.
func (m ToolDenyEditorModel) RenderOverlay(base string, width, height int) string {
	if !m.visible {
		return base
	}

	editorWidth := 70
	if width < 80 {
		editorWidth = width - 10
	}

	var footer strings.Builder

	// Show selected tool description
	if item := m.list.SelectedItem(); item != nil {
		if ti, ok := item.(toolDenyItem); ok {
			desc := ti.description
			if desc == "" {
				desc = "(no description)"
			}
			maxDescWidth := editorWidth - 8
			if len(desc) > maxDescWidth {
				desc = desc[:maxDescWidth-3] + "..."
			}
			footer.WriteString(m.theme.Muted.Render(desc))
			footer.WriteString("\n")
		}
	}

	footer.WriteString(m.theme.Faint.Render("space=toggle deny  /=filter  enter=save  esc=cancel"))

	contentStr := lipgloss.NewStyle().
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
		contentStr,
		lipgloss.WithWhitespaceChars(" "),
		lipgloss.WithWhitespaceForeground(lipgloss.AdaptiveColor{Light: "#E5E7EB", Dark: "#1F2937"}),
	)
}

// toolDenyDelegate renders items in the deny editor.
type toolDenyDelegate struct {
	theme  theme.Theme
	denied map[string]bool
}

func newToolDenyDelegate(th theme.Theme, denied map[string]bool) toolDenyDelegate {
	return toolDenyDelegate{theme: th, denied: denied}
}

func (d toolDenyDelegate) Height() int                             { return 1 }
func (d toolDenyDelegate) Spacing() int                            { return 0 }
func (d toolDenyDelegate) Update(_ tea.Msg, _ *list.Model) tea.Cmd { return nil }

func (d toolDenyDelegate) Render(w io.Writer, m list.Model, index int, item list.Item) {
	ti, ok := item.(toolDenyItem)
	if !ok {
		return
	}

	selected := index == m.Index()

	cursor := "  "
	if selected {
		cursor = "> "
	}

	isDenied := d.denied[ti.toolName]

	var checkbox string
	if isDenied {
		checkbox = d.theme.Danger.Render("[X]")
	} else {
		checkbox = d.theme.Success.Render("[ ]")
	}

	name := ti.toolName
	if selected {
		name = d.theme.ItemSelected.Render(name)
	} else if isDenied {
		name = d.theme.Faint.Render(name)
	} else {
		name = d.theme.Item.Render(name)
	}

	_, _ = fmt.Fprintf(w, "%s%s %s", cursor, checkbox, name)
}
