package views

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/events"
	"github.com/Bigsy/mcpmu/internal/mcp"
	"github.com/Bigsy/mcpmu/internal/tui/theme"
)

// ServerItem represents a server in the list.
type ServerItem struct {
	Name       string // Server name (map key)
	Config     config.ServerConfig
	Status     events.ServerStatus
	Namespaces []string       // Names of namespaces this server belongs to
	AuthStatus mcp.AuthStatus // Authentication status for HTTP servers
}

func (i ServerItem) Title() string { return i.Name }
func (i ServerItem) Description() string {
	if i.Config.IsHTTP() {
		return i.Config.URL
	}
	return i.Config.Command
}
func (i ServerItem) FilterValue() string { return i.Name }

// ServerListModel is the server list view component.
type ServerListModel struct {
	list     list.Model
	theme    theme.Theme
	spinner  spinner.Model
	emptyMsg string
	width    int
	height   int
	focused  bool
}

// NewServerList creates a new server list view.
func NewServerList(th theme.Theme) ServerListModel {
	delegate := newServerDelegate(th, "")
	l := list.New([]list.Item{}, delegate, 0, 0)
	l.SetShowTitle(false)
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(false)
	l.SetShowHelp(false)

	// Better empty state
	emptyMsg := th.Faint.Render("    ○\n  No servers configured\n\n  Press 'a' to add your first server")
	l.SetStatusBarItemName("server", "servers")
	l.Styles.NoItems = lipgloss.NewStyle().Padding(2, 0).MarginLeft(2)
	l.SetShowStatusBar(false)
	l.SetDelegate(delegate)

	s := spinner.New()
	s.Spinner = spinner.MiniDot

	m := ServerListModel{
		list:    l,
		theme:   th,
		spinner: s,
		focused: true,
	}
	m.list.SetStatusBarItemName("server", "servers")
	// Set custom empty message via styles
	m.emptyMsg = emptyMsg

	return m
}

// SetItems updates the server list items.
func (m *ServerListModel) SetItems(items []ServerItem) {
	listItems := make([]list.Item, len(items))
	for i, item := range items {
		listItems[i] = item
	}
	m.list.SetItems(listItems)
	// Update delegate with current spinner frame
	m.list.SetDelegate(newServerDelegate(m.theme, m.spinner.View()))
}

// HasTransitionalServers returns true if any server is in a transitional state.
func (m ServerListModel) HasTransitionalServers() bool {
	for _, item := range m.list.Items() {
		si, ok := item.(ServerItem)
		if ok && (si.Status.State == events.StateStarting || si.Status.State == events.StateStopping) {
			return true
		}
	}
	return false
}

// SpinnerTick returns a command to tick the spinner.
func (m ServerListModel) SpinnerTick() tea.Cmd {
	return m.spinner.Tick
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
	var cmds []tea.Cmd

	// Handle spinner tick messages
	if _, ok := msg.(spinner.TickMsg); ok {
		var spinnerCmd tea.Cmd
		m.spinner, spinnerCmd = m.spinner.Update(msg)
		if spinnerCmd != nil {
			cmds = append(cmds, spinnerCmd)
		}
		// Update the delegate with new spinner frame
		m.list.SetDelegate(newServerDelegate(m.theme, m.spinner.View()))
	}

	var listCmd tea.Cmd
	m.list, listCmd = m.list.Update(msg)
	if listCmd != nil {
		cmds = append(cmds, listCmd)
	}

	return m, tea.Batch(cmds...)
}

// View implements tea.Model.
func (m ServerListModel) View() string {
	content := m.list.View()
	if len(m.list.Items()) == 0 && m.emptyMsg != "" {
		content = m.emptyMsg
	}
	return m.theme.RenderPane("Servers", content, m.width, m.focused)
}

// serverDelegate is a custom delegate for rendering server items.
type serverDelegate struct {
	theme        theme.Theme
	spinnerFrame string
}

func newServerDelegate(th theme.Theme, spinnerFrame string) serverDelegate {
	return serverDelegate{theme: th, spinnerFrame: spinnerFrame}
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

	// Column widths
	const nameWidth = 18

	// First line: cursor + name + status + tools + uptime + badges
	var line1 strings.Builder

	// Cursor
	if selected {
		line1.WriteString(d.theme.Primary.Render(">"))
		line1.WriteString(" ")
	} else {
		line1.WriteString("  ")
	}

	// Name (fixed width)
	name := truncateString(si.Name, nameWidth)
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
	line1.WriteString(padRight(styledName, nameWidth))

	// Status pill
	var statePill string
	if d.spinnerFrame != "" && (si.Status.State == events.StateStarting || si.Status.State == events.StateStopping) {
		statePill = d.theme.StatusPillAnimated(si.Status.State.String(), d.spinnerFrame)
	} else {
		statePill = d.theme.StatusPill(si.Status.State.String())
	}
	line1.WriteString(" ")
	line1.WriteString(statePill)

	// Tool count (compact)
	if si.Status.ToolCount > 0 {
		line1.WriteString(" ")
		line1.WriteString(d.theme.Faint.Render(fmt.Sprintf("◉%d", si.Status.ToolCount)))
	}

	// Uptime for running servers
	if si.Status.State == events.StateRunning && si.Status.StartedAt != nil {
		uptime := time.Since(*si.Status.StartedAt)
		line1.WriteString(" ")
		line1.WriteString(d.theme.Faint.Render("↑" + formatUptime(uptime)))
	}

	// Disabled indicator
	if !enabled {
		line1.WriteString(" ")
		line1.WriteString(d.theme.Faint.Render("[disabled]"))
	}

	// HTTP badge
	if si.Config.IsHTTP() {
		line1.WriteString(" ")
		line1.WriteString(d.theme.Faint.Render("[http]"))
		line1.WriteString(" ")
		line1.WriteString(formatAuthBadge(si, d.theme))
	}

	// Second line: command/URL + namespace badges
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
	maxCmdLen := 45
	if len(cmdOrURL) > maxCmdLen {
		cmdOrURL = cmdOrURL[:maxCmdLen-3] + "..."
	}
	line2.WriteString(d.theme.Muted.Render(cmdOrURL))

	// Namespace badges on second line
	if len(si.Namespaces) > 0 {
		line2.WriteString("  ")
		for i, ns := range si.Namespaces {
			if i > 0 {
				line2.WriteString(" ")
			}
			line2.WriteString(d.theme.Faint.Render("[" + ns + "]"))
		}
	}

	_, _ = fmt.Fprint(w, line1.String()+"\n"+line2.String())
}

// truncateString truncates a string to maxLen visual width, adding "…" if needed.
func truncateString(s string, maxLen int) string {
	if lipgloss.Width(s) <= maxLen {
		return s
	}
	// Truncate rune by rune until we fit
	runes := []rune(s)
	for i := len(runes) - 1; i >= 0; i-- {
		truncated := string(runes[:i]) + "…"
		if lipgloss.Width(truncated) <= maxLen {
			return truncated
		}
	}
	// Fallback: just return ellipsis if nothing fits
	if maxLen >= 1 {
		return "…"
	}
	return ""
}

// padRight pads a string with spaces to reach the target width.
func padRight(s string, width int) string {
	currentWidth := lipgloss.Width(s)
	if currentWidth >= width {
		return s
	}
	return s + strings.Repeat(" ", width-currentWidth)
}

// formatUptime formats a duration as a compact uptime string.
func formatUptime(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	hours := int(d.Hours())
	mins := int(d.Minutes()) % 60
	if hours >= 24 {
		days := hours / 24
		hours = hours % 24
		return fmt.Sprintf("%dd%dh", days, hours)
	}
	return fmt.Sprintf("%dh%dm", hours, mins)
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
