package views

import (
	"fmt"
	"io"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/tui/theme"
)

// NamespaceItem represents a namespace in the list.
type NamespaceItem struct {
	Config    config.NamespaceConfig
	IsDefault bool
}

func (i NamespaceItem) Title() string       { return i.Config.Name }
func (i NamespaceItem) Description() string { return i.Config.Description }
func (i NamespaceItem) FilterValue() string { return i.Config.Name }

// NamespaceListModel is the namespace list view component.
type NamespaceListModel struct {
	list    list.Model
	theme   theme.Theme
	width   int
	height  int
	focused bool
}

// NewNamespaceList creates a new namespace list view.
func NewNamespaceList(th theme.Theme) NamespaceListModel {
	delegate := newNamespaceDelegate(th)
	l := list.New([]list.Item{}, delegate, 0, 0)
	l.Title = "Namespaces"
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(false)
	l.SetShowHelp(false)
	l.Styles.Title = th.Title

	return NamespaceListModel{
		list:    l,
		theme:   th,
		focused: true,
	}
}

// SetItems updates the namespace list items.
func (m *NamespaceListModel) SetItems(items []NamespaceItem) {
	listItems := make([]list.Item, len(items))
	for i, item := range items {
		listItems[i] = item
	}
	m.list.SetItems(listItems)
}

// SetSize sets the dimensions of the list.
func (m *NamespaceListModel) SetSize(width, height int) {
	m.width = width
	m.height = height
	listWidth := width - 4
	listHeight := height - 2
	if listWidth < 10 {
		listWidth = 10
	}
	if listHeight < 3 {
		listHeight = 3
	}
	m.list.SetSize(listWidth, listHeight)
}

// SetFocused sets whether the list is focused.
func (m *NamespaceListModel) SetFocused(focused bool) {
	m.focused = focused
}

// SelectedItem returns the currently selected namespace.
func (m *NamespaceListModel) SelectedItem() *NamespaceItem {
	item := m.list.SelectedItem()
	if item == nil {
		return nil
	}
	ni := item.(NamespaceItem)
	return &ni
}

// SelectedIndex returns the index of the selected item.
func (m NamespaceListModel) SelectedIndex() int {
	return m.list.Index()
}

// Init implements tea.Model.
func (m NamespaceListModel) Init() tea.Cmd {
	return nil
}

// Update implements tea.Model.
func (m NamespaceListModel) Update(msg tea.Msg) (NamespaceListModel, tea.Cmd) {
	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

// View implements tea.Model.
func (m NamespaceListModel) View() string {
	style := m.theme.Pane
	if m.focused {
		style = m.theme.PaneFocused
	}
	return style.Width(m.width - 2).Render(m.list.View())
}

// namespaceDelegate is a custom delegate for rendering namespace items.
type namespaceDelegate struct {
	theme theme.Theme
}

func newNamespaceDelegate(th theme.Theme) namespaceDelegate {
	return namespaceDelegate{theme: th}
}

func (d namespaceDelegate) Height() int                             { return 2 }
func (d namespaceDelegate) Spacing() int                            { return 1 }
func (d namespaceDelegate) Update(_ tea.Msg, _ *list.Model) tea.Cmd { return nil }

func (d namespaceDelegate) Render(w io.Writer, m list.Model, index int, item list.Item) {
	ni, ok := item.(NamespaceItem)
	if !ok {
		return
	}

	selected := index == m.Index()

	// First line: name, default indicator, deny-by-default indicator
	var line1 string

	name := ni.Config.Name
	if selected {
		name = d.theme.ItemSelected.Render(name)
	} else {
		name = d.theme.Item.Render(name)
	}

	if selected {
		line1 = d.theme.Primary.Render(">") + " "
	} else {
		line1 = "  "
	}

	line1 += name

	if ni.IsDefault {
		line1 += "  " + d.theme.Primary.Render("[default]")
	}

	if ni.Config.DenyByDefault {
		line1 += "  " + d.theme.Warn.Render("[deny-by-default]")
	}

	// Second line: description or server count
	var line2 string
	line2 = "    "
	if ni.Config.Description != "" {
		desc := ni.Config.Description
		if len(desc) > 50 {
			desc = desc[:47] + "..."
		}
		line2 += d.theme.Muted.Render(desc)
	}

	serverCount := len(ni.Config.ServerIDs)
	if serverCount > 0 {
		if ni.Config.Description != "" {
			line2 += "  "
		}
		line2 += d.theme.Faint.Render(fmt.Sprintf("%d server(s)", serverCount))
	} else if ni.Config.Description == "" {
		line2 += d.theme.Faint.Render("No servers assigned")
	}

	fmt.Fprint(w, line1+"\n"+line2)
}
