package views

import (
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/hedworth/mcp-studio-go/internal/config"
	"github.com/hedworth/mcp-studio-go/internal/events"
	"github.com/hedworth/mcp-studio-go/internal/mcp"
	"github.com/hedworth/mcp-studio-go/internal/tui/theme"
)

// ServerItem represents a server in the list.
type ServerItem struct {
	Config     config.ServerConfig
	Status     events.ServerStatus
	Namespaces []string // Names of namespaces this server belongs to
	AuthStatus mcp.AuthStatus // Authentication status for HTTP servers
}

func (i ServerItem) Title() string       { return i.Config.Name }
func (i ServerItem) Description() string {
	if i.Config.IsHTTP() {
		return i.Config.URL
	}
	return i.Config.Command
}
func (i ServerItem) FilterValue() string { return i.Config.Name }

// ServerListModel is the server list view component.
type ServerListModel struct {
	list     list.Model
	theme    theme.Theme
	width    int
	height   int
	focused  bool
}

// NewServerList creates a new server list view.
func NewServerList(theme theme.Theme) ServerListModel {
	delegate := newServerDelegate(theme)
	l := list.New([]list.Item{}, delegate, 0, 0)
	l.Title = "Servers"
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(false)
	l.SetShowHelp(false)
	l.Styles.Title = theme.Title

	return ServerListModel{
		list:    l,
		theme:   theme,
		focused: true,
	}
}

// SetItems updates the server list items.
func (m *ServerListModel) SetItems(items []ServerItem) {
	listItems := make([]list.Item, len(items))
	for i, item := range items {
		listItems[i] = item
	}
	m.list.SetItems(listItems)
}

// SetSize sets the dimensions of the list.
func (m *ServerListModel) SetSize(width, height int) {
	m.width = width
	m.height = height
	// List gets: width minus borders (2) minus padding (2) = width - 4
	// Height: height minus borders (2) = height - 2
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
func (m *ServerListModel) SetFocused(focused bool) {
	m.focused = focused
}

// SelectedItem returns the currently selected server.
func (m *ServerListModel) SelectedItem() *ServerItem {
	item := m.list.SelectedItem()
	if item == nil {
		return nil
	}
	si := item.(ServerItem)
	return &si
}

// SelectedIndex returns the index of the selected item.
func (m ServerListModel) SelectedIndex() int {
	return m.list.Index()
}

// Init implements tea.Model.
func (m ServerListModel) Init() tea.Cmd {
	return nil
}

// Update implements tea.Model.
func (m ServerListModel) Update(msg tea.Msg) (ServerListModel, tea.Cmd) {
	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

// View implements tea.Model.
func (m ServerListModel) View() string {
	style := m.theme.Pane
	if m.focused {
		style = m.theme.PaneFocused
	}
	// Width is content width; borders are outside this
	return style.Width(m.width - 2).Render(m.list.View())
}

// serverDelegate is a custom delegate for rendering server items.
type serverDelegate struct {
	theme theme.Theme
}

func newServerDelegate(theme theme.Theme) serverDelegate {
	return serverDelegate{theme: theme}
}

func (d serverDelegate) Height() int                             { return 2 }
func (d serverDelegate) Spacing() int                            { return 1 }
func (d serverDelegate) Update(_ tea.Msg, _ *list.Model) tea.Cmd { return nil }

func (d serverDelegate) Render(w io.Writer, m list.Model, index int, item list.Item) {
	si, ok := item.(ServerItem)
	if !ok {
		return
	}

	selected := index == m.Index()
	enabled := si.Config.IsEnabled()

	// First line: name, status pill
	var line1 strings.Builder

	name := si.Config.Name
	var styledName string
	switch {
	case !enabled && selected:
		styledName = d.theme.ItemDim.Bold(true).Render(name)
	case !enabled:
		styledName = d.theme.ItemDim.Render(name)
	case selected:
		styledName = d.theme.ItemSelected.Render(name)
	default:
		styledName = d.theme.Item.Render(name)
	}

	if selected {
		line1.WriteString(d.theme.Primary.Render(">"))
		line1.WriteString(" ")
	} else {
		line1.WriteString("  ")
	}

	line1.WriteString(styledName)

	// Server type indicator (http badge for HTTP servers)
	if si.Config.IsHTTP() {
		line1.WriteString("  ")
		line1.WriteString(d.theme.Faint.Render("[http]"))
	}

	// Status pill - always show runtime state, add disabled indicator if applicable
	statePill := d.theme.StatusPill(si.Status.State.String())
	line1.WriteString("  ")
	line1.WriteString(statePill)
	if !enabled {
		line1.WriteString(" ")
		line1.WriteString(d.theme.Faint.Render("[disabled]"))
	}

	// Auth status for HTTP servers
	if si.Config.IsHTTP() {
		line1.WriteString(" ")
		authBadge := formatAuthBadge(si, d.theme)
		line1.WriteString(authBadge)
	}

	// Namespace badges on first line
	if len(si.Namespaces) > 0 {
		line1.WriteString("  ")
		for i, ns := range si.Namespaces {
			if i > 0 {
				line1.WriteString(" ")
			}
			// Short namespace indicator
			line1.WriteString(d.theme.Faint.Render("[" + ns + "]"))
		}
	}

	// Second line: command/URL (truncated), tool count
	var line2 strings.Builder
	line2.WriteString("   ")

	var cmdOrURL string
	if si.Config.IsHTTP() {
		cmdOrURL = si.Config.URL
	} else {
		cmdOrURL = si.Config.Command
		if len(si.Config.Args) > 0 {
			cmdOrURL += " " + strings.Join(si.Config.Args, " ")
		}
	}
	maxCmdLen := 40
	if len(cmdOrURL) > maxCmdLen {
		cmdOrURL = cmdOrURL[:maxCmdLen-3] + "..."
	}
	line2.WriteString(d.theme.Muted.Render(cmdOrURL))

	if si.Status.ToolCount > 0 {
		line2.WriteString("  ")
		line2.WriteString(d.theme.Faint.Render(fmt.Sprintf("%d tools", si.Status.ToolCount)))
	}

	fmt.Fprint(w, line1.String()+"\n"+line2.String())
}

// formatAuthBadge returns a styled auth status badge for HTTP servers.
func formatAuthBadge(si ServerItem, t theme.Theme) string {
	// Check config first
	if si.Config.BearerTokenEnvVar != "" {
		return t.Faint.Render("[bearer]")
	}

	// Check runtime auth status
	switch si.AuthStatus {
	case mcp.AuthStatusOAuthOK:
		return t.Success.Render("[oauth:ok]")
	case mcp.AuthStatusOAuthNeeds:
		return t.Warn.Render("[oauth:login]")
	case mcp.AuthStatusOAuthExp:
		return t.Danger.Render("[oauth:expired]")
	default:
		return t.Faint.Render("[oauth]")
	}
}
