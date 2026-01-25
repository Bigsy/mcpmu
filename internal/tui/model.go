package tui

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/hedworth/mcp-studio-go/internal/config"
	"github.com/hedworth/mcp-studio-go/internal/events"
	"github.com/hedworth/mcp-studio-go/internal/mcp"
	"github.com/hedworth/mcp-studio-go/internal/process"
	"github.com/hedworth/mcp-studio-go/internal/tui/theme"
	"github.com/hedworth/mcp-studio-go/internal/tui/views"
)

// Tab represents a tab in the UI.
type Tab int

const (
	TabServers Tab = iota
	TabNamespaces
	TabProxies
)

// View represents the current view mode.
type View int

const (
	ViewList View = iota
	ViewDetail
)

// Model is the root Bubble Tea model.
type Model struct {
	// Dependencies
	cfg        *config.Config
	supervisor *process.Supervisor
	bus        *events.Bus
	ctx        context.Context

	// UI state
	theme       theme.Theme
	keys        KeyBindings
	width       int
	height      int
	activeTab   Tab
	currentView View
	keyContext  KeyContext

	// Components
	serverList   views.ServerListModel
	serverDetail views.ServerDetailModel
	logPanel     views.LogPanelModel

	// Server status tracking
	serverStatuses map[string]events.ServerStatus
	serverTools    map[string][]events.McpTool

	// Confirm dialog state
	showConfirm    bool
	confirmMessage string
	confirmAction  func()

	// Event channel for Bubble Tea integration
	eventCh chan events.Event
}

// NewModel creates a new root model.
func NewModel(cfg *config.Config, supervisor *process.Supervisor, bus *events.Bus) Model {
	th := theme.New()
	keys := NewKeyBindings()

	m := Model{
		cfg:            cfg,
		supervisor:     supervisor,
		bus:            bus,
		ctx:            context.Background(),
		theme:          th,
		keys:           keys,
		activeTab:      TabServers,
		currentView:    ViewList,
		keyContext:     ContextList,
		serverList:     views.NewServerList(th),
		serverDetail:   views.NewServerDetail(th),
		logPanel:       views.NewLogPanel(th),
		serverStatuses: make(map[string]events.ServerStatus),
		serverTools:    make(map[string][]events.McpTool),
		eventCh:        make(chan events.Event, 100),
	}

	// Subscribe to events
	bus.Subscribe(func(e events.Event) {
		select {
		case m.eventCh <- e:
		default:
			// Channel full, drop event
		}
	})

	// Initialize server list from config
	m.refreshServerList()

	return m
}

// Init implements tea.Model.
func (m Model) Init() tea.Cmd {
	return m.waitForEvent()
}

// waitForEvent returns a command that waits for the next event.
func (m Model) waitForEvent() tea.Cmd {
	return func() tea.Msg {
		return <-m.eventCh
	}
}

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.updateLayout()

	case tea.KeyMsg:
		// Always handle Ctrl+C
		if key.Matches(msg, m.keys.CtrlC) {
			return m, tea.Quit
		}

		// Handle confirm dialog
		if m.showConfirm {
			return m.handleConfirmKey(msg)
		}

		// Handle our custom keys first
		if handled, newModel, cmd := m.handleKey(msg); handled {
			return newModel, cmd
		}

	case events.Event:
		m.handleEvent(msg)
		cmds = append(cmds, m.waitForEvent())
	}

	// Update child components (including for unhandled keys)
	if m.currentView == ViewList {
		var cmd tea.Cmd
		m.serverList, cmd = m.serverList.Update(msg)
		cmds = append(cmds, cmd)
	} else {
		var cmd tea.Cmd
		m.serverDetail, cmd = m.serverDetail.Update(msg)
		cmds = append(cmds, cmd)
	}

	if m.logPanel.IsVisible() {
		var cmd tea.Cmd
		m.logPanel, cmd = m.logPanel.Update(msg)
		cmds = append(cmds, cmd)
	}

	return m, tea.Batch(cmds...)
}

func (m *Model) handleEvent(e events.Event) {
	switch evt := e.(type) {
	case events.StatusChangedEvent:
		m.serverStatuses[evt.ServerID()] = evt.Status
		m.refreshServerList()

	case events.ToolsUpdatedEvent:
		m.serverTools[evt.ServerID()] = evt.Tools
		// Update status with tool count
		if status, ok := m.serverStatuses[evt.ServerID()]; ok {
			status.ToolCount = len(evt.Tools)
			m.serverStatuses[evt.ServerID()] = status
		}
		m.refreshServerList()

	case events.LogReceivedEvent:
		m.logPanel.AppendLog(evt.ServerID(), evt.Line)

	case events.ErrorEvent:
		// Could show toast here in Phase 2
	}
}

func (m *Model) handleKey(msg tea.KeyMsg) (handled bool, model tea.Model, cmd tea.Cmd) {
	// Global keys
	switch {
	case key.Matches(msg, m.keys.Quit):
		if m.supervisor.RunningCount() > 0 {
			m.showConfirmQuit()
			return true, m, nil
		}
		return true, m, tea.Quit

	case key.Matches(msg, m.keys.Tab1):
		m.activeTab = TabServers
		m.currentView = ViewList
		return true, m, nil

	case key.Matches(msg, m.keys.Tab2):
		// Namespaces - disabled in Phase 1
		return true, m, nil

	case key.Matches(msg, m.keys.Tab3):
		// Proxies - disabled in Phase 1
		return true, m, nil

	case key.Matches(msg, m.keys.Escape):
		if m.currentView == ViewDetail {
			m.currentView = ViewList
			return true, m, nil
		}
		if m.logPanel.IsFocused() {
			m.logPanel.SetFocused(false)
			m.serverList.SetFocused(true)
			return true, m, nil
		}
		return false, m, nil // Let child handle Esc

	case key.Matches(msg, m.keys.ToggleLogs):
		if m.logPanel.IsVisible() {
			m.logPanel.SetVisible(false)
			m.logPanel.SetFocused(false)
		} else {
			m.logPanel.SetVisible(true)
		}
		m.updateLayout()
		return true, m, nil

	case key.Matches(msg, m.keys.FollowLogs):
		if m.logPanel.IsVisible() {
			m.logPanel.ToggleFollow()
		}
		return true, m, nil
	}

	// View-specific keys
	if m.currentView == ViewList {
		return m.handleListKey(msg)
	}

	return false, m, nil
}

func (m *Model) handleListKey(msg tea.KeyMsg) (handled bool, model tea.Model, cmd tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Enter):
		if item := m.serverList.SelectedItem(); item != nil {
			m.currentView = ViewDetail
			status := m.serverStatuses[item.Config.ID]
			tools := m.convertTools(m.serverTools[item.Config.ID])
			m.serverDetail.SetServer(&item.Config, &status, tools)
		}
		return true, m, nil

	case key.Matches(msg, m.keys.Start):
		log.Printf("Start key pressed, selected item: %v", m.serverList.SelectedItem())
		if item := m.serverList.SelectedItem(); item != nil {
			log.Printf("Starting server: %s", item.Config.ID)
			go m.startServer(item.Config)
		}
		return true, m, nil

	case key.Matches(msg, m.keys.Stop):
		if item := m.serverList.SelectedItem(); item != nil {
			go m.supervisor.Stop(item.Config.ID)
		}
		return true, m, nil
	}

	return false, m, nil // Let list handle navigation keys
}

func (m *Model) handleConfirmKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Yes):
		m.showConfirm = false
		if m.confirmAction != nil {
			m.confirmAction()
		}
		return m, tea.Quit

	case key.Matches(msg, m.keys.No), key.Matches(msg, m.keys.Escape):
		m.showConfirm = false
		return m, nil
	}
	return m, nil
}

func (m *Model) showConfirmQuit() {
	count := m.supervisor.RunningCount()
	m.confirmMessage = fmt.Sprintf("%d server(s) still running. Stop all and quit?", count)
	m.confirmAction = func() {
		m.supervisor.StopAll()
	}
	m.showConfirm = true
}

func (m *Model) startServer(srv config.ServerConfig) {
	_, err := m.supervisor.Start(m.ctx, srv)
	if err != nil {
		// Error will be emitted via event bus
	}
}

func (m *Model) refreshServerList() {
	servers := m.cfg.ServerList()
	items := make([]views.ServerItem, len(servers))
	for i, srv := range servers {
		status := m.serverStatuses[srv.ID]
		items[i] = views.ServerItem{
			Config: srv,
			Status: status,
		}
	}
	m.serverList.SetItems(items)
}

func (m *Model) convertTools(mcpTools []events.McpTool) []mcp.Tool {
	result := make([]mcp.Tool, len(mcpTools))
	for i, t := range mcpTools {
		result[i] = mcp.Tool{
			Name:        t.Name,
			Description: t.Description,
		}
	}
	return result
}

func (m *Model) updateLayout() {
	// Calculate heights
	headerHeight := 3  // Tab bar + border
	statusHeight := 1  // Status bar
	logHeight := 0
	if m.logPanel.IsVisible() {
		logHeight = 8 // Log panel height when visible
	}

	contentHeight := m.height - headerHeight - statusHeight - logHeight

	// Set component sizes
	m.serverList.SetSize(m.width-2, contentHeight)
	m.serverDetail.SetSize(m.width-2, contentHeight)
	if m.logPanel.IsVisible() {
		m.logPanel.SetSize(m.width-2, logHeight)
	}
}

// View implements tea.Model.
func (m Model) View() string {
	if m.width == 0 || m.height == 0 {
		return "Loading..."
	}

	var sections []string

	// Header with tabs
	sections = append(sections, m.renderHeader())

	// Main content
	if m.currentView == ViewList {
		sections = append(sections, m.serverList.View())
	} else {
		sections = append(sections, m.serverDetail.View())
	}

	// Log panel
	if m.logPanel.IsVisible() {
		sections = append(sections, m.logPanel.View())
	}

	// Status bar
	sections = append(sections, m.renderStatusBar())

	// Confirm dialog overlay
	content := lipgloss.JoinVertical(lipgloss.Left, sections...)
	if m.showConfirm {
		content = m.renderConfirmOverlay(content)
	}

	return m.theme.App.Render(content)
}

func (m Model) renderHeader() string {
	tabs := []struct {
		name    string
		enabled bool
	}{
		{"Servers", true},
		{"Namespaces", false}, // Phase 3
		{"Proxies", false},    // Phase 4
	}

	var tabViews []string
	for i, tab := range tabs {
		label := fmt.Sprintf("[%d]%s", i+1, tab.name)
		if i == int(m.activeTab) {
			tabViews = append(tabViews, m.theme.TabActive.Render(label))
		} else if tab.enabled {
			tabViews = append(tabViews, m.theme.Tab.Render(label))
		} else {
			tabViews = append(tabViews, m.theme.Faint.Render(label))
		}
	}

	title := m.theme.Title.Render("MCP Studio")
	tabBar := lipgloss.JoinHorizontal(lipgloss.Top, tabViews...)

	// Align title left, tabs right
	padding := m.width - lipgloss.Width(title) - lipgloss.Width(tabBar) - 4
	if padding < 1 {
		padding = 1
	}

	return title + strings.Repeat(" ", padding) + tabBar
}

func (m Model) renderStatusBar() string {
	runningCount := m.supervisor.RunningCount()
	totalCount := len(m.cfg.Servers)

	left := fmt.Sprintf("%d/%d running", runningCount, totalCount)

	// Show context-sensitive key hints
	var keys string
	if m.currentView == ViewList {
		keys = "j/k:nav  s:start  x:stop  enter:details  l:logs  q:quit"
	} else {
		keys = "esc:back  s:start  x:stop  l:logs  q:quit"
	}

	padding := m.width - lipgloss.Width(left) - lipgloss.Width(keys) - 4
	if padding < 1 {
		padding = 1
	}

	return m.theme.StatusBar.Render(left + strings.Repeat(" ", padding) + keys)
}

func (m Model) renderConfirmOverlay(base string) string {
	// Simple confirm dialog
	dialog := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(m.theme.Warn.GetForeground()).
		Padding(1, 2).
		Width(50).
		Render(
			m.theme.Warn.Bold(true).Render("Confirm") + "\n\n" +
				m.confirmMessage + "\n\n" +
				m.theme.Muted.Render("[y]es  [n]o"),
		)

	// Center the dialog
	return lipgloss.Place(
		m.width,
		m.height,
		lipgloss.Center,
		lipgloss.Center,
		dialog,
		lipgloss.WithWhitespaceChars(" "),
		lipgloss.WithWhitespaceForeground(lipgloss.AdaptiveColor{Light: "#E5E7EB", Dark: "#1F2937"}),
	)
}
