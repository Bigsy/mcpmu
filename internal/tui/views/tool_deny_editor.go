package views

import (
	"fmt"
	"io"
	"strings"

	"github.com/Bigsy/mcpmu/internal/mcp"
	"github.com/Bigsy/mcpmu/internal/tui/theme"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
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
	l.SetFilteringEnabled(true)
	l.SetShowHelp(false)
	l.Styles.Title = th.Title
	l.FilterInput.PromptStyle = th.Primary
	l.FilterInput.Cursor.Style = th.Primary

	return ToolDenyEditorModel{
		theme:  th,
		list:   l,
		denied: make(map[string]bool),
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
	m.list.SetSize(editorWidth-6, editorHeight-6)
}

// Update handles messages.
func (m *ToolDenyEditorModel) Update(msg tea.Msg) tea.Cmd {
	if !m.visible {
		return nil
	}

	// When filtering is active, let the list handle most keys
	if m.list.FilterState() == list.Filtering {
		var cmd tea.Cmd
		m.list, cmd = m.list.Update(msg)
		return cmd
	}

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch {
		case key.Matches(msg, m.escKey):
			if m.list.FilterState() == list.FilterApplied {
				m.list.ResetFilter()
				return nil
			}
			m.visible = false
			return func() tea.Msg {
				return ToolDenyResult{ServerName: m.serverName, Submitted: false}
			}
		case key.Matches(msg, m.enterKey):
			m.visible = false
			// Collect denied tools
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
		case key.Matches(msg, m.spaceKey):
			if item := m.list.SelectedItem(); item != nil {
				ti := item.(toolDenyItem)
				m.denied[ti.toolName] = !m.denied[ti.toolName]
				m.list.SetDelegate(newToolDenyDelegate(m.theme, m.denied))
			}
			return nil
		}
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
	return m.list.View()
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
