package views

import (
	"fmt"
	"io"
	"strings"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/tui/theme"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ServerPickerResult is sent when the user completes server selection.
type ServerPickerResult struct {
	SelectedIDs []string
	Submitted   bool
}

// serverPickerItem represents a server in the picker list.
type serverPickerItem struct {
	name   string // Server name (map key)
	config config.ServerConfig
}

func (i serverPickerItem) Title() string       { return i.name }
func (i serverPickerItem) Description() string { return i.config.Command }
func (i serverPickerItem) FilterValue() string { return i.name }

// ServerPickerModel is a multi-select server picker.
type ServerPickerModel struct {
	theme   theme.Theme
	visible bool
	list    list.Model
	width   int
	height  int

	// Selected server IDs
	selected map[string]bool

	// Key bindings
	escKey   key.Binding
	enterKey key.Binding
	spaceKey key.Binding
}

// NewServerPicker creates a new server picker.
func NewServerPicker(th theme.Theme) ServerPickerModel {
	delegate := newServerPickerDelegate(th)
	l := list.New([]list.Item{}, delegate, 0, 0)
	l.Title = "Select Servers"
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(false)
	l.SetShowHelp(false)
	l.Styles.Title = th.Title

	return ServerPickerModel{
		theme:    th,
		list:     l,
		selected: make(map[string]bool),
		escKey: key.NewBinding(
			key.WithKeys("esc"),
			key.WithHelp("esc", "cancel"),
		),
		enterKey: key.NewBinding(
			key.WithKeys("enter"),
			key.WithHelp("enter", "confirm"),
		),
		spaceKey: key.NewBinding(
			key.WithKeys(" "),
			key.WithHelp("space", "toggle"),
		),
	}
}

// Show displays the picker with the given servers and initial selection.
func (m *ServerPickerModel) Show(servers []config.ServerEntry, selectedNames []string) {
	m.visible = true
	m.selected = make(map[string]bool)
	for _, name := range selectedNames {
		m.selected[name] = true
	}

	items := make([]list.Item, len(servers))
	for i, entry := range servers {
		items[i] = serverPickerItem{
			name:   entry.Name,
			config: entry.Config,
		}
	}
	m.list.SetItems(items)

	// Update delegate to access selected state
	m.list.SetDelegate(newServerPickerDelegateWithSelection(m.theme, m.selected))
}

// Hide hides the picker.
func (m *ServerPickerModel) Hide() {
	m.visible = false
}

// IsVisible returns whether the picker is visible.
func (m ServerPickerModel) IsVisible() bool {
	return m.visible
}

// SetSize sets the available size for the picker.
func (m *ServerPickerModel) SetSize(width, height int) {
	m.width = width
	m.height = height
	pickerWidth := 60
	if width < 70 {
		pickerWidth = width - 10
	}
	pickerHeight := 20
	if height < 25 {
		pickerHeight = height - 5
	}
	m.list.SetSize(pickerWidth-4, pickerHeight-4)
}

// Update handles messages for the picker.
func (m *ServerPickerModel) Update(msg tea.Msg) tea.Cmd {
	if !m.visible {
		return nil
	}

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch {
		case key.Matches(msg, m.escKey):
			m.visible = false
			return func() tea.Msg {
				return ServerPickerResult{Submitted: false}
			}
		case key.Matches(msg, m.enterKey):
			m.visible = false
			var selectedIDs []string
			for id, sel := range m.selected {
				if sel {
					selectedIDs = append(selectedIDs, id)
				}
			}
			return func() tea.Msg {
				return ServerPickerResult{
					SelectedIDs: selectedIDs,
					Submitted:   true,
				}
			}
		case key.Matches(msg, m.spaceKey):
			// Toggle selection
			if item := m.list.SelectedItem(); item != nil {
				si := item.(serverPickerItem)
				m.selected[si.name] = !m.selected[si.name]
				// Update delegate
				m.list.SetDelegate(newServerPickerDelegateWithSelection(m.theme, m.selected))
			}
			return nil
		}
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return cmd
}

// View renders the picker.
func (m ServerPickerModel) View() string {
	if !m.visible {
		return ""
	}
	return m.list.View()
}

// RenderOverlay renders the picker as a centered overlay.
func (m ServerPickerModel) RenderOverlay(base string, width, height int) string {
	if !m.visible {
		return base
	}

	pickerWidth := 60
	if width < 70 {
		pickerWidth = width - 10
	}

	helpText := m.theme.Faint.Render("space=toggle  enter=confirm  esc=cancel")

	content := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(m.theme.Primary.GetForeground()).
		Padding(1, 2).
		Width(pickerWidth).
		Render(m.View() + "\n\n" + helpText)

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

// serverPickerDelegate renders items in the server picker.
type serverPickerDelegate struct {
	theme    theme.Theme
	selected map[string]bool
}

func newServerPickerDelegate(th theme.Theme) serverPickerDelegate {
	return serverPickerDelegate{theme: th, selected: make(map[string]bool)}
}

func newServerPickerDelegateWithSelection(th theme.Theme, selected map[string]bool) serverPickerDelegate {
	return serverPickerDelegate{theme: th, selected: selected}
}

func (d serverPickerDelegate) Height() int                             { return 1 }
func (d serverPickerDelegate) Spacing() int                            { return 0 }
func (d serverPickerDelegate) Update(_ tea.Msg, _ *list.Model) tea.Cmd { return nil }

func (d serverPickerDelegate) Render(w io.Writer, m list.Model, index int, item list.Item) {
	si, ok := item.(serverPickerItem)
	if !ok {
		return
	}

	cursor := "  "
	if index == m.Index() {
		cursor = "> "
	}

	checkbox := "[ ]"
	if d.selected[si.name] {
		checkbox = "[x]"
	}

	var line strings.Builder
	line.WriteString(cursor)
	if d.selected[si.name] {
		line.WriteString(d.theme.Success.Render(checkbox))
	} else {
		line.WriteString(d.theme.Muted.Render(checkbox))
	}
	line.WriteString(" ")
	if index == m.Index() {
		line.WriteString(d.theme.ItemSelected.Render(si.name))
	} else {
		line.WriteString(d.theme.Item.Render(si.name))
	}

	_, _ = fmt.Fprint(w, line.String())
}
