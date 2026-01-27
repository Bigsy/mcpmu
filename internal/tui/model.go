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
	helpOverlay  views.HelpOverlayModel
	serverForm   *views.ServerFormModel // Pointer to preserve huh form's value bindings
	confirmDlg   views.ConfirmModel
	toast        views.ToastModel

	// Server status tracking
	serverStatuses map[string]events.ServerStatus
	serverTools    map[string][]events.McpTool

	// Detail view tracking
	detailServerID string

	// Confirm dialog state (legacy, for quit confirmation)
	showConfirm    bool
	confirmMessage string
	confirmAction  func()

	// Pending delete server ID (for delete confirmation flow)
	pendingDeleteID string

	// Event channel for Bubble Tea integration
	eventCh chan events.Event
}

// newServerFormPtr creates a pointer to a ServerFormModel.
// This is needed because huh forms store pointers to field values,
// and we need the form to persist across Bubble Tea's value-based updates.
func newServerFormPtr(th theme.Theme) *views.ServerFormModel {
	form := views.NewServerForm(th)
	return &form
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
		helpOverlay:    views.NewHelpOverlay(th),
		serverForm:     newServerFormPtr(th),
		confirmDlg:     views.NewConfirm(th),
		toast:          views.NewToast(th),
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
	// Start autostart servers and wait for events
	return tea.Batch(
		m.startAutostartServers(),
		m.waitForEvent(),
	)
}

// startAutostartServers starts all servers with autostart=true.
func (m Model) startAutostartServers() tea.Cmd {
	return func() tea.Msg {
		for _, srv := range m.cfg.ServerList() {
			if srv.Autostart && srv.IsEnabled() {
				log.Printf("Autostarting server: %s", srv.ID)
				go func(s config.ServerConfig) {
					_, err := m.supervisor.Start(m.ctx, s)
					if err != nil {
						log.Printf("Failed to autostart server %s: %v", s.ID, err)
					}
				}(srv)
			}
		}
		return nil
	}
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

	// Server form needs ALL messages (including internal huh messages like cursor blink)
	// Handle this first, before the type switch
	if m.serverForm.IsVisible() {
		var cmds []tea.Cmd
		switch msg := msg.(type) {
		case tea.KeyMsg:
			// Always handle Ctrl+C even in form
			if key.Matches(msg, m.keys.CtrlC) {
				return m, tea.Quit
			}
		case tea.WindowSizeMsg:
			m.width = msg.Width
			m.height = msg.Height
			m.updateLayout()
			m.helpOverlay.SetSize(msg.Width, msg.Height)
			m.serverForm.SetSize(msg.Width, msg.Height)
			m.confirmDlg.SetSize(msg.Width, msg.Height)
			m.toast.SetSize(msg.Width, msg.Height)
		case views.ServerFormResult:
			return m.handleServerFormResult(msg)
		}
		// Pass all messages to the form (pointer receiver to preserve huh's value bindings)
		if cmd := m.serverForm.Update(msg); cmd != nil {
			cmds = append(cmds, cmd)
		}

		// Keep server events flowing while the modal is open.
		if evt, ok := msg.(events.Event); ok {
			if cmd := m.handleEvent(evt); cmd != nil {
				cmds = append(cmds, cmd)
			}
			cmds = append(cmds, m.waitForEvent())
		}

		// Allow toast timers and key-dismiss to work while the form is visible.
		var toastCmd tea.Cmd
		m.toast, toastCmd = m.toast.Update(msg)
		if toastCmd != nil {
			cmds = append(cmds, toastCmd)
		}

		return m, tea.Batch(cmds...)
	}

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.updateLayout()
		m.helpOverlay.SetSize(msg.Width, msg.Height)
		m.serverForm.SetSize(msg.Width, msg.Height)
		m.confirmDlg.SetSize(msg.Width, msg.Height)
		m.toast.SetSize(msg.Width, msg.Height)

	case tea.KeyMsg:
		// Always handle Ctrl+C
		if key.Matches(msg, m.keys.CtrlC) {
			return m, tea.Quit
		}

		// Dismiss toast on any key press
		if m.toast.IsVisible() {
			m.toast.Hide()
		}

		// Handle confirm dialog
		if m.confirmDlg.IsVisible() {
			var cmd tea.Cmd
			m.confirmDlg, cmd = m.confirmDlg.Update(msg)
			return m, cmd
		}

		// Handle legacy confirm dialog (quit)
		if m.showConfirm {
			return m.handleConfirmKey(msg)
		}

		// Handle help overlay
		if m.helpOverlay.IsVisible() {
			if key.Matches(msg, m.keys.Help) || key.Matches(msg, m.keys.Escape) {
				m.helpOverlay.SetVisible(false)
				return m, nil
			}
			// Forward scroll keys to help overlay
			var cmd tea.Cmd
			m.helpOverlay, cmd = m.helpOverlay.Update(msg)
			return m, cmd
		}

		// Handle our custom keys first
		if handled, newModel, cmd := m.handleKey(msg); handled {
			return newModel, cmd
		}

	case views.ServerFormResult:
		return m.handleServerFormResult(msg)

	case views.ConfirmResult:
		return m.handleConfirmResult(msg)

	case events.Event:
		cmd := m.handleEvent(msg)
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
		cmds = append(cmds, m.waitForEvent())

	default:
		// Handle toast timer messages
		var cmd tea.Cmd
		m.toast, cmd = m.toast.Update(msg)
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
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

func (m *Model) handleEvent(e events.Event) tea.Cmd {
	switch evt := e.(type) {
	case events.StatusChangedEvent:
		m.serverStatuses[evt.ServerID()] = evt.Status
		m.refreshServerList()

		// Refresh detail view if showing this server
		if m.currentView == ViewDetail && m.detailServerID == evt.ServerID() {
			if srv := m.cfg.GetServer(evt.ServerID()); srv != nil {
				tools := m.convertTools(m.serverTools[evt.ServerID()])
				m.serverDetail.SetServer(srv, &evt.Status, tools)
			}
		}

		// Show toast for state changes
		serverName := evt.ServerID()
		if srv := m.cfg.GetServer(evt.ServerID()); srv != nil && srv.Name != "" {
			serverName = srv.Name
		}

		switch evt.NewState {
		case events.StateRunning:
			return m.toast.ShowSuccess(fmt.Sprintf("Server \"%s\" started", serverName))
		case events.StateStopped:
			if evt.OldState == events.StateRunning {
				return m.toast.ShowInfo(fmt.Sprintf("Server \"%s\" stopped", serverName))
			}
		case events.StateError, events.StateCrashed:
			return m.toast.ShowError(fmt.Sprintf("Server \"%s\" failed", serverName))
		}

	case events.ToolsUpdatedEvent:
		m.serverTools[evt.ServerID()] = evt.Tools
		// Update status with tool count
		if status, ok := m.serverStatuses[evt.ServerID()]; ok {
			status.ToolCount = len(evt.Tools)
			m.serverStatuses[evt.ServerID()] = status
		}
		m.refreshServerList()

		// Refresh detail view if showing this server
		if m.currentView == ViewDetail && m.detailServerID == evt.ServerID() {
			if srv := m.cfg.GetServer(evt.ServerID()); srv != nil {
				status := m.serverStatuses[evt.ServerID()]
				tools := m.convertTools(evt.Tools)
				m.serverDetail.SetServer(srv, &status, tools)
			}
		}

	case events.LogReceivedEvent:
		m.logPanel.AppendLog(evt.ServerID(), evt.Line)

	case events.ErrorEvent:
		return m.toast.ShowError(evt.Message)
	}
	return nil
}

func (m *Model) handleKey(msg tea.KeyMsg) (handled bool, model tea.Model, cmd tea.Cmd) {
	// Global keys
	switch {
	case key.Matches(msg, m.keys.Help):
		m.helpOverlay.Toggle()
		return true, m, nil

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
			m.detailServerID = ""
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
	if m.currentView == ViewDetail {
		return m.handleDetailKey(msg)
	}

	return false, m, nil
}

func (m *Model) handleListKey(msg tea.KeyMsg) (handled bool, model tea.Model, cmd tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Enter):
		if item := m.serverList.SelectedItem(); item != nil {
			m.currentView = ViewDetail
			m.detailServerID = item.Config.ID
			status := m.serverStatuses[item.Config.ID]
			tools := m.convertTools(m.serverTools[item.Config.ID])
			m.serverDetail.SetServer(&item.Config, &status, tools)
		}
		return true, m, nil

	case key.Matches(msg, m.keys.Test):
		log.Printf("Test key pressed, selected item: %v", m.serverList.SelectedItem())
		if item := m.serverList.SelectedItem(); item != nil {
			// Toggle: if running, stop; otherwise start
			if item.Status.State == events.StateRunning {
				log.Printf("Stopping server: %s", item.Config.ID)
				go m.supervisor.Stop(item.Config.ID)
			} else {
				log.Printf("Starting server: %s", item.Config.ID)
				go m.startServer(item.Config)
			}
		}
		return true, m, nil

	case key.Matches(msg, m.keys.ToggleEnabled):
		if item := m.serverList.SelectedItem(); item != nil {
			m.toggleServerEnabled(item.Config.ID)
		}
		return true, m, nil

	case key.Matches(msg, m.keys.Add):
		cmd := m.serverForm.ShowAdd()
		return true, m, cmd

	case key.Matches(msg, m.keys.Edit):
		if item := m.serverList.SelectedItem(); item != nil {
			cmd := m.serverForm.ShowEdit(item.Config)
			return true, m, cmd
		}
		return true, m, nil

	case key.Matches(msg, m.keys.Delete):
		if item := m.serverList.SelectedItem(); item != nil {
			m.pendingDeleteID = item.Config.ID
			name := item.Config.Name
			if name == "" {
				name = item.Config.ID
			}
			m.confirmDlg.Show("Delete Server", fmt.Sprintf("Delete server \"%s\"?\nThis cannot be undone.", name), "delete-server")
		}
		return true, m, nil
	}

	return false, m, nil // Let list handle navigation keys
}

func (m *Model) handleDetailKey(msg tea.KeyMsg) (handled bool, model tea.Model, cmd tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Test):
		if item := m.serverList.SelectedItem(); item != nil {
			// Toggle: if running, stop; otherwise start
			if item.Status.State == events.StateRunning {
				go m.supervisor.Stop(item.Config.ID)
			} else {
				go m.startServer(item.Config)
			}
		}
		return true, m, nil
	}

	return false, m, nil
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

func (m Model) handleServerFormResult(result views.ServerFormResult) (tea.Model, tea.Cmd) {
	if !result.Submitted {
		// Form was cancelled
		return m, nil
	}

	serverName := result.Server.Name
	if serverName == "" {
		serverName = result.Server.Command
	}

	var err error
	if result.IsEdit {
		// Update existing server
		err = m.cfg.UpdateServer(result.Server)
		if err != nil {
			log.Printf("Failed to update server: %v", err)
			return m, m.toast.ShowError(fmt.Sprintf("Failed to update server: %v", err))
		}
	} else {
		// Add new server
		_, err = m.cfg.AddServer(result.Server)
		if err != nil {
			log.Printf("Failed to add server: %v", err)
			return m, m.toast.ShowError(fmt.Sprintf("Failed to add server: %v", err))
		}
	}

	// Save config
	if err := config.Save(m.cfg); err != nil {
		log.Printf("Failed to save config: %v", err)
		return m, m.toast.ShowError(fmt.Sprintf("Failed to save config: %v", err))
	}

	// Refresh the list
	m.refreshServerList()

	// Show success toast
	if result.IsEdit {
		return m, m.toast.ShowSuccess(fmt.Sprintf("Server \"%s\" updated", serverName))
	}
	return m, m.toast.ShowSuccess(fmt.Sprintf("Server \"%s\" added", serverName))
}

func (m Model) handleConfirmResult(result views.ConfirmResult) (tea.Model, tea.Cmd) {
	if result.Tag == "delete-server" && result.Confirmed {
		// Get server name for toast
		serverName := m.pendingDeleteID
		if srv := m.cfg.GetServer(m.pendingDeleteID); srv != nil && srv.Name != "" {
			serverName = srv.Name
		}

		// Stop server if running
		if status, ok := m.serverStatuses[m.pendingDeleteID]; ok {
			if status.State == events.StateRunning || status.State == events.StateStarting {
				m.supervisor.Stop(m.pendingDeleteID)
			}
		}

		// Delete from config
		if err := m.cfg.DeleteServer(m.pendingDeleteID); err != nil {
			log.Printf("Failed to delete server: %v", err)
			m.pendingDeleteID = ""
			return m, m.toast.ShowError(fmt.Sprintf("Failed to delete server: %v", err))
		}

		// Save config
		if err := config.Save(m.cfg); err != nil {
			log.Printf("Failed to save config: %v", err)
			m.pendingDeleteID = ""
			return m, m.toast.ShowError(fmt.Sprintf("Failed to save config: %v", err))
		}

		// Clear status tracking
		delete(m.serverStatuses, m.pendingDeleteID)
		delete(m.serverTools, m.pendingDeleteID)

		// Refresh list
		m.refreshServerList()

		m.pendingDeleteID = ""
		return m, m.toast.ShowSuccess(fmt.Sprintf("Server \"%s\" deleted", serverName))
	}

	m.pendingDeleteID = ""
	return m, nil
}

func (m *Model) startServer(srv config.ServerConfig) {
	_, err := m.supervisor.Start(m.ctx, srv)
	if err != nil {
		// Error will be emitted via event bus
	}
}

func (m *Model) toggleServerEnabled(id string) {
	srv := m.cfg.GetServer(id)
	if srv == nil {
		return
	}

	// Toggle enabled state
	newEnabled := !srv.IsEnabled()
	srv.SetEnabled(newEnabled)
	m.cfg.Servers[id] = *srv

	// Save config synchronously (fast operation, avoids race conditions)
	if err := config.Save(m.cfg); err != nil {
		log.Printf("Failed to save config after toggle: %v", err)
	}

	m.refreshServerList()
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
	// Calculate heights more carefully
	headerHeight := 1  // Tab bar (single line)
	statusHeight := 1  // Status bar
	logHeight := 0
	if m.logPanel.IsVisible() {
		logHeight = 10 // Log panel height when visible (including border)
	}

	// Available height for main content
	contentHeight := m.height - headerHeight - statusHeight - logHeight
	if contentHeight < 5 {
		contentHeight = 5 // Minimum content height
	}

	// Available width: total width minus App padding (2)
	contentWidth := m.width - 4

	// Set component sizes
	m.serverList.SetSize(contentWidth, contentHeight)
	m.serverDetail.SetSize(contentWidth, contentHeight)
	if m.logPanel.IsVisible() {
		m.logPanel.SetSize(contentWidth, logHeight)
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

	// Status bar (replaced by toast when visible)
	if m.toast.IsVisible() {
		sections = append(sections, m.toast.View())
	} else {
		sections = append(sections, m.renderStatusBar())
	}

	// Build base content
	content := lipgloss.JoinVertical(lipgloss.Left, sections...)

	// Legacy confirm dialog overlay (quit)
	if m.showConfirm {
		content = m.renderConfirmOverlay(content)
	}

	// Server form overlay
	if m.serverForm.IsVisible() {
		content = m.serverForm.RenderOverlay(content, m.width, m.height)
	}

	// Confirm dialog overlay (delete, etc.)
	if m.confirmDlg.IsVisible() {
		content = m.confirmDlg.RenderOverlay(content, m.width, m.height)
	}

	// Help overlay
	if m.helpOverlay.IsVisible() {
		content = m.helpOverlay.RenderOverlay(content, m.width, m.height)
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

	left := fmt.Sprintf("%d/%d servers running", runningCount, totalCount)

	// Show context-sensitive key hints
	var keys string
	if m.currentView == ViewList {
		keys = "t:test  E:enable  a:add  e:edit  d:delete  l:logs  ?:help"
	} else {
		keys = "esc:back  t:test  l:logs  ?:help"
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
