package views

import (
	"fmt"
	"io"
	"strings"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/tui/theme"
	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
)

// NamespaceItem represents a namespace in the list.
type NamespaceItem struct {
	Name       string // Namespace name (map key)
	Config     config.NamespaceConfig
	IsDefault  bool
	TokenCount int  // Estimated token count for enabled tools
	HasCache   bool // Whether any tool cache data exists
}

func (i NamespaceItem) Title() string       { return i.Name }
func (i NamespaceItem) Description() string { return i.Config.Description }
func (i NamespaceItem) FilterValue() string { return i.Name }

// NamespaceListModel is the namespace list view component.
type NamespaceListModel struct {
	list     list.Model
	theme    theme.Theme
	emptyMsg string
	width    int
	height   int
	topPad   int
	focused  bool
}

// NewNamespaceList creates a new namespace list view.
func NewNamespaceList(th theme.Theme) NamespaceListModel {
	delegate := newNamespaceDelegate(th)
	l := list.New([]list.Item{}, delegate, 0, 0)
	l.SetShowTitle(false)
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(false)
	l.SetShowHelp(false)

	emptyMsg := th.Faint.Render("    ○\n  No namespaces configured\n\n  Press 'a' to create a namespace")

	return NamespaceListModel{
		list:     l,
		theme:    th,
		emptyMsg: emptyMsg,
		focused:  true,
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
	m.topPad = paneTopPaddingLines(height)
	listHeight := height - 2 - m.topPad
	if listWidth < 10 {
		listWidth = 10
	}
	if listHeight < 3 {
		// Preserve a minimum usable list height by reducing top padding first.
		m.topPad = height - 2 - 3
		if m.topPad < 0 {
			m.topPad = 0
		}
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
	content := m.list.View()
	if len(m.list.Items()) == 0 && m.emptyMsg != "" {
		content = m.emptyMsg
	}
	content = strings.TrimSuffix(content, "\n")
	if m.topPad > 0 {
		content = strings.Repeat("\n", m.topPad) + content
	}
	return m.theme.RenderPane("Namespaces", content, m.width, m.focused)
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

	name := ni.Name
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

	// Token count
	if ni.HasCache {
		line2 += "  " + d.theme.Muted.Render(formatTokenCount(ni.TokenCount))
	} else if serverCount > 0 {
		line2 += "  " + d.theme.Faint.Render("(tokens unknown)")
	}

	_, _ = fmt.Fprint(w, line1+"\n"+line2)
}

func formatTokenCount(tokens int) string {
	if tokens >= 1000 {
		return fmt.Sprintf("~%.1fk tokens", float64(tokens)/1000)
	}
	return fmt.Sprintf("~%d tokens", tokens)
}
