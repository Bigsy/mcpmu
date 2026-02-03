package tui

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/events"
	"github.com/Bigsy/mcpmu/internal/mcp"
	"github.com/Bigsy/mcpmu/internal/process"
	"github.com/Bigsy/mcpmu/internal/tui/theme"
	"github.com/Bigsy/mcpmu/internal/tui/views"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Tab represents a tab in the UI.
type Tab int

const (
	TabServers Tab = iota
	TabNamespaces
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
	configPath string // Custom config path, empty for default

	// UI state
	theme       theme.Theme
	keys        KeyBindings
	width       int
	height      int
	activeTab   Tab
	currentView View
	keyContext  KeyContext

	// Server Components
	serverList   views.ServerListModel
	serverDetail views.ServerDetailModel
	serverForm   *views.ServerFormModel // Pointer to preserve huh form's value bindings

	// Namespace Components
	namespaceList   views.NamespaceListModel
	namespaceDetail views.NamespaceDetailModel
	namespaceForm   *views.NamespaceFormModel
	serverPicker    views.ServerPickerModel
	toolPerms       views.ToolPermissionsModel

	// Shared Components
	logPanel    views.LogPanelModel
	helpOverlay views.HelpOverlayModel
	confirmDlg  views.ConfirmModel
	toast       views.ToastModel

	// Server status tracking
	serverStatuses map[string]events.ServerStatus
	serverTools    map[string][]events.McpTool

	// Detail view tracking
	detailServerID    string
	detailNamespaceID string

	// Confirm dialog state (legacy, for quit confirmation)
	showConfirm    bool
	confirmMessage string
	confirmAction  func()

	// Pending delete IDs (for delete confirmation flow)
	pendingDeleteID          string
	pendingDeleteNamespaceID string

	// Tool permission discovery state
	permDiscoveryServers  []string // Servers we're waiting for tools from
	permDiscoveryExpected int      // Number of servers expected to report tools

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

// newNamespaceFormPtr creates a pointer to a NamespaceFormModel.
func newNamespaceFormPtr(th theme.Theme) *views.NamespaceFormModel {
	form := views.NewNamespaceForm(th)
	return &form
}

// NewModel creates a new root model.
func NewModel(cfg *config.Config, supervisor *process.Supervisor, bus *events.Bus, configPath string) Model {
	th := theme.New()
	keys := NewKeyBindings()

	m := Model{
		cfg:             cfg,
		supervisor:      supervisor,
		bus:             bus,
		ctx:             context.Background(),
		configPath:      configPath,
		theme:           th,
		keys:            keys,
		activeTab:       TabServers,
		currentView:     ViewList,
		keyContext:      ContextList,
		serverList:      views.NewServerList(th),
		serverDetail:    views.NewServerDetail(th),
		serverForm:      newServerFormPtr(th),
		namespaceList:   views.NewNamespaceList(th),
		namespaceDetail: views.NewNamespaceDetail(th),
		namespaceForm:   newNamespaceFormPtr(th),
		serverPicker:    views.NewServerPicker(th),
		toolPerms:       views.NewToolPermissions(th),
		logPanel:        views.NewLogPanel(th),
		helpOverlay:     views.NewHelpOverlay(th),
		confirmDlg:      views.NewConfirm(th),
		toast:           views.NewToast(th),
		serverStatuses:  make(map[string]events.ServerStatus),
		serverTools:     make(map[string][]events.McpTool),
		eventCh:         make(chan events.Event, 100),
	}

	// Subscribe to events
	bus.Subscribe(func(e events.Event) {
		select {
		case m.eventCh <- e:
		default:
			log.Printf("Warning: TUI event channel full, dropping event type=%s server=%s", e.Type(), e.ServerID())
		}
	})

	// Initialize lists from config
	m.refreshServerList()
	m.refreshNamespaceList()

	return m
}

// saveConfig saves the config to the appropriate file (custom path or default).
func (m *Model) saveConfig() error {
	if m.configPath != "" {
		return config.SaveTo(m.cfg, m.configPath)
	}
	return config.Save(m.cfg)
}

func (m *Model) switchToTab(tab Tab) {
	m.activeTab = tab
	m.currentView = ViewList
	m.detailServerID = ""
	m.detailNamespaceID = ""

	// Refresh tab-specific lists when switching.
	switch tab {
	case TabServers:
		m.refreshServerList()
	case TabNamespaces:
		m.refreshNamespaceList()
	}
}

func (m *Model) applyFocus() {
	// Reset everything to unfocused, then mark the active pane focused so it
	// picks up the orange accent border.
	m.serverList.SetFocused(false)
	m.serverDetail.SetFocused(false)
	m.namespaceList.SetFocused(false)
	m.namespaceDetail.SetFocused(false)

	switch m.activeTab {
	case TabServers:
		if m.currentView == ViewDetail {
			m.serverDetail.SetFocused(true)
		} else {
			m.serverList.SetFocused(true)
		}
	case TabNamespaces:
		if m.currentView == ViewDetail {
			m.namespaceDetail.SetFocused(true)
		} else {
			m.namespaceList.SetFocused(true)
		}
	}
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
		for _, entry := range m.cfg.ServerEntries() {
			if entry.Config.Autostart && entry.Config.IsEnabled() {
				log.Printf("Autostarting server: %s", entry.Name)
				go func(name string, s config.ServerConfig) {
					_, err := m.supervisor.Start(m.ctx, name, s)
					if err != nil {
						log.Printf("Failed to autostart server %s: %v", name, err)
					}
				}(entry.Name, entry.Config)
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

	// Handle modal forms first - they need ALL messages
	// Server form
	if m.serverForm.IsVisible() {
		return m.updateWithServerForm(msg)
	}

	// Namespace form
	if m.namespaceForm.IsVisible() {
		return m.updateWithNamespaceForm(msg)
	}

	// Server picker modal
	if m.serverPicker.IsVisible() {
		return m.updateWithServerPicker(msg)
	}

	// Tool permissions modal
	if m.toolPerms.IsVisible() {
		return m.updateWithToolPerms(msg)
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

	case views.NamespaceFormResult:
		return m.handleNamespaceFormResult(msg)

	case views.ServerPickerResult:
		return m.handleServerPickerResult(msg)

	case views.ToolPermissionsResult:
		return m.handleToolPermissionsResult(msg)

	case views.ConfirmResult:
		return m.handleConfirmResult(msg)

	case permDiscoveryTimeoutMsg:
		// Handle permission discovery timeout
		if m.toolPerms.IsDiscovering() {
			m.toolPerms.SetDiscoveryTimeout()
			// Try to show whatever tools we have so far
			m.finishPermissionDiscovery()
		}

	case events.Event:
		cmd := m.handleEvent(msg)
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
		cmds = append(cmds, m.waitForEvent())

	case spinner.TickMsg:
		// Handle spinner tick - update server list
		// serverList.Update already schedules the next tick via m.spinner.Update(msg)
		var cmd tea.Cmd
		m.serverList, cmd = m.serverList.Update(msg)
		if cmd != nil {
			// Only keep the tick command if servers are still in transitional state
			if m.serverList.HasTransitionalServers() {
				cmds = append(cmds, cmd)
			}
			// Otherwise drop the tick command to stop the spinner
		}
		// Return early to avoid double-updating serverList in child component section below
		return m, tea.Batch(cmds...)

	default:
		// Handle toast timer messages
		var cmd tea.Cmd
		m.toast, cmd = m.toast.Update(msg)
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
	}

	// Update child components (including for unhandled keys)
	switch m.activeTab {
	case TabServers:
		if m.currentView == ViewList {
			var cmd tea.Cmd
			m.serverList, cmd = m.serverList.Update(msg)
			cmds = append(cmds, cmd)
		} else {
			var cmd tea.Cmd
			m.serverDetail, cmd = m.serverDetail.Update(msg)
			cmds = append(cmds, cmd)
		}
	case TabNamespaces:
		if m.currentView == ViewList {
			var cmd tea.Cmd
			m.namespaceList, cmd = m.namespaceList.Update(msg)
			cmds = append(cmds, cmd)
		} else {
			var cmd tea.Cmd
			m.namespaceDetail, cmd = m.namespaceDetail.Update(msg)
			cmds = append(cmds, cmd)
		}
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
		m.refreshDetailViewIfShowing(evt.ServerID())

		// Show toast for state changes - use the server ID which is now the display name
		serverName := evt.ServerID()
		var cmds []tea.Cmd

		// Start spinner tick when entering transitional state
		if evt.NewState == events.StateStarting || evt.NewState == events.StateStopping {
			cmds = append(cmds, m.serverList.SpinnerTick())
		}

		switch evt.NewState {
		case events.StateRunning:
			cmds = append(cmds, m.toast.ShowSuccess(fmt.Sprintf("Server \"%s\" started", serverName)))
		case events.StateStopped:
			if evt.OldState == events.StateRunning {
				cmds = append(cmds, m.toast.ShowInfo(fmt.Sprintf("Server \"%s\" stopped", serverName)))
			}
		case events.StateError, events.StateCrashed:
			cmds = append(cmds, m.toast.ShowError(fmt.Sprintf("Server \"%s\" failed", serverName)))
		}

		if len(cmds) > 0 {
			return tea.Batch(cmds...)
		}

	case events.ToolsUpdatedEvent:
		m.serverTools[evt.ServerID()] = evt.Tools
		// Update status with tool count
		if status, ok := m.serverStatuses[evt.ServerID()]; ok {
			status.ToolCount = len(evt.Tools)
			m.serverStatuses[evt.ServerID()] = status
		}
		m.refreshServerList()
		m.refreshDetailViewIfShowing(evt.ServerID())

		// Check if we're in discovery mode and this completes it
		if m.toolPerms.IsDiscovering() {
			m.checkPermissionDiscoveryComplete()
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

	case key.Matches(msg, m.keys.TabNext):
		next := Tab((int(m.activeTab) + 1) % 2)
		m.switchToTab(next)
		return true, m, nil

	case key.Matches(msg, m.keys.TabPrev):
		prev := Tab((int(m.activeTab) + 1) % 2) // -1 mod 2
		m.switchToTab(prev)
		return true, m, nil

	case key.Matches(msg, m.keys.Tab1):
		m.switchToTab(TabServers)
		return true, m, nil

	case key.Matches(msg, m.keys.Tab2):
		m.switchToTab(TabNamespaces)
		return true, m, nil

	case key.Matches(msg, m.keys.Escape):
		if m.currentView == ViewDetail {
			m.currentView = ViewList
			m.detailServerID = ""
			m.detailNamespaceID = ""
			return true, m, nil
		}
		if m.logPanel.IsFocused() {
			m.logPanel.SetFocused(false)
			switch m.activeTab {
			case TabServers:
				m.serverList.SetFocused(true)
			case TabNamespaces:
				m.namespaceList.SetFocused(true)
			}
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

	case key.Matches(msg, m.keys.WrapLogs):
		if m.logPanel.IsVisible() {
			m.logPanel.ToggleWrap()
		}
		return true, m, nil
	}

	// Tab and view-specific keys
	switch m.activeTab {
	case TabServers:
		if m.currentView == ViewList {
			return m.handleServerListKey(msg)
		}
		if m.currentView == ViewDetail {
			return m.handleServerDetailKey(msg)
		}
	case TabNamespaces:
		if m.currentView == ViewList {
			return m.handleNamespaceListKey(msg)
		}
		if m.currentView == ViewDetail {
			return m.handleNamespaceDetailKey(msg)
		}
	}

	return false, m, nil
}

func (m *Model) handleServerListKey(msg tea.KeyMsg) (handled bool, model tea.Model, cmd tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Enter):
		if item := m.serverList.SelectedItem(); item != nil {
			m.currentView = ViewDetail
			m.detailServerID = item.Name
			status := m.serverStatuses[item.Name]
			tools := m.convertTools(m.serverTools[item.Name])
			m.serverDetail.SetServer(item.Name, &item.Config, &status, tools)
		}
		return true, m, nil

	case key.Matches(msg, m.keys.Test):
		log.Printf("Test key pressed, selected item: %v", m.serverList.SelectedItem())
		if item := m.serverList.SelectedItem(); item != nil {
			// Toggle: if running, stop; otherwise start
			if item.Status.State == events.StateRunning {
				log.Printf("Stopping server: %s", item.Name)
				go func() { _ = m.supervisor.Stop(item.Name) }()
			} else {
				log.Printf("Starting server: %s", item.Name)
				go m.startServer(item.Name, item.Config)
			}
		}
		return true, m, nil

	case key.Matches(msg, m.keys.ToggleEnabled):
		if item := m.serverList.SelectedItem(); item != nil {
			m.toggleServerEnabled(item.Name)
		}
		return true, m, nil

	case key.Matches(msg, m.keys.Add):
		cmd := m.serverForm.ShowAdd()
		return true, m, cmd

	case key.Matches(msg, m.keys.Edit):
		if item := m.serverList.SelectedItem(); item != nil {
			cmd := m.serverForm.ShowEdit(item.Name, item.Config)
			return true, m, cmd
		}
		return true, m, nil

	case key.Matches(msg, m.keys.Delete):
		if item := m.serverList.SelectedItem(); item != nil {
			m.pendingDeleteID = item.Name
			m.confirmDlg.Show("Delete Server", fmt.Sprintf("Delete server \"%s\"?\nThis cannot be undone.", item.Name), "delete-server")
		}
		return true, m, nil

	case key.Matches(msg, m.keys.Login):
		if item := m.serverList.SelectedItem(); item != nil {
			// Only allow login for servers in needs-auth state
			if item.Status.State == events.StateNeedsAuth {
				go m.loginOAuth(item.Name)
				return true, m, m.toast.ShowInfo("Opening browser for OAuth login...")
			} else if !item.Config.IsHTTP() {
				return true, m, m.toast.ShowError("OAuth login only applies to HTTP servers")
			} else {
				return true, m, m.toast.ShowError("Server doesn't need OAuth login")
			}
		}
		return true, m, nil
	}

	return false, m, nil // Let list handle navigation keys
}

func (m *Model) handleServerDetailKey(msg tea.KeyMsg) (handled bool, model tea.Model, cmd tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Test):
		if item := m.serverList.SelectedItem(); item != nil {
			// Toggle: if running, stop; otherwise start
			if item.Status.State == events.StateRunning {
				go func() { _ = m.supervisor.Stop(item.Name) }()
			} else {
				go m.startServer(item.Name, item.Config)
			}
		}
		return true, m, nil

	case key.Matches(msg, m.keys.ToggleEnabled):
		if m.detailServerID != "" {
			m.toggleServerEnabled(m.detailServerID)
		}
		return true, m, nil

	case key.Matches(msg, m.keys.Login):
		if m.detailServerID != "" {
			status := m.serverStatuses[m.detailServerID]
			if status.State == events.StateNeedsAuth {
				go m.loginOAuth(m.detailServerID)
				return true, m, m.toast.ShowInfo("Opening browser for OAuth login...")
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

	var err error
	if result.IsEdit {
		// Check if name changed (rename)
		if result.OriginalName != "" && result.Name != result.OriginalName {
			if err = m.cfg.RenameServer(result.OriginalName, result.Name); err != nil {
				log.Printf("Failed to rename server: %v", err)
				return m, m.toast.ShowError(fmt.Sprintf("Failed to rename server: %v", err))
			}
		}
		// Update existing server config
		err = m.cfg.UpdateServer(result.Name, result.Server)
		if err != nil {
			log.Printf("Failed to update server: %v", err)
			return m, m.toast.ShowError(fmt.Sprintf("Failed to update server: %v", err))
		}
	} else {
		// Add new server
		err = m.cfg.AddServer(result.Name, result.Server)
		if err != nil {
			log.Printf("Failed to add server: %v", err)
			return m, m.toast.ShowError(fmt.Sprintf("Failed to add server: %v", err))
		}
	}

	// Save config
	if err := m.saveConfig(); err != nil {
		log.Printf("Failed to save config: %v", err)
		return m, m.toast.ShowError(fmt.Sprintf("Failed to save config: %v", err))
	}

	// Refresh the list
	m.refreshServerList()

	// Show success toast
	if result.IsEdit {
		return m, m.toast.ShowSuccess(fmt.Sprintf("Server \"%s\" updated", result.Name))
	}
	return m, m.toast.ShowSuccess(fmt.Sprintf("Server \"%s\" added", result.Name))
}

func (m Model) handleConfirmResult(result views.ConfirmResult) (tea.Model, tea.Cmd) {
	if result.Tag == "delete-server" && result.Confirmed {
		// Server name is the ID now
		serverName := m.pendingDeleteID

		// Stop server if running
		if status, ok := m.serverStatuses[m.pendingDeleteID]; ok {
			if status.State == events.StateRunning || status.State == events.StateStarting {
				_ = m.supervisor.Stop(m.pendingDeleteID)
			}
		}

		// Delete from config
		if err := m.cfg.DeleteServer(m.pendingDeleteID); err != nil {
			log.Printf("Failed to delete server: %v", err)
			m.pendingDeleteID = ""
			return m, m.toast.ShowError(fmt.Sprintf("Failed to delete server: %v", err))
		}

		// Save config
		if err := m.saveConfig(); err != nil {
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

	if result.Tag == "delete-namespace" && result.Confirmed {
		// Namespace name is the ID now
		namespaceName := m.pendingDeleteNamespaceID

		if err := m.cfg.DeleteNamespace(m.pendingDeleteNamespaceID); err != nil {
			log.Printf("Failed to delete namespace: %v", err)
			m.pendingDeleteNamespaceID = ""
			return m, m.toast.ShowError(fmt.Sprintf("Failed to delete namespace: %v", err))
		}

		if err := m.saveConfig(); err != nil {
			log.Printf("Failed to save config: %v", err)
			m.pendingDeleteNamespaceID = ""
			return m, m.toast.ShowError(fmt.Sprintf("Failed to save config: %v", err))
		}

		m.refreshNamespaceList()
		m.refreshServerList() // Update server list badges

		// If we were viewing the deleted namespace, go back to list
		if m.detailNamespaceID == m.pendingDeleteNamespaceID {
			m.currentView = ViewList
			m.detailNamespaceID = ""
		}

		m.pendingDeleteNamespaceID = ""
		return m, m.toast.ShowSuccess(fmt.Sprintf("Namespace \"%s\" deleted", namespaceName))
	}

	m.pendingDeleteID = ""
	m.pendingDeleteNamespaceID = ""
	return m, nil
}

func (m *Model) startServer(name string, srv config.ServerConfig) {
	// Error will be emitted via event bus, no need to handle here
	_, _ = m.supervisor.Start(m.ctx, name, srv)
}

func (m *Model) loginOAuth(name string) {
	// Run OAuth login flow - errors will be emitted via event bus
	if err := m.supervisor.LoginOAuth(m.ctx, name); err != nil {
		log.Printf("OAuth login failed for %s: %v", name, err)
		m.bus.Publish(events.NewErrorEvent(name, err, fmt.Sprintf("OAuth login failed: %v", err)))
	}
}

func (m *Model) toggleServerEnabled(id string) {
	srv, ok := m.cfg.GetServer(id)
	if !ok {
		return
	}

	// Toggle enabled state
	currentlyEnabled := srv.IsEnabled()
	newEnabled := !currentlyEnabled

	// Avoid a contradictory "running + disabled" state by stopping the server
	// when disabling.
	if currentlyEnabled && !newEnabled {
		if status, ok := m.serverStatuses[id]; ok && status.State == events.StateRunning {
			go func() { _ = m.supervisor.Stop(id) }()
		}
	}
	srv.SetEnabled(newEnabled)
	m.cfg.Servers[id] = srv

	// Save config synchronously (fast operation, avoids race conditions)
	if err := m.saveConfig(); err != nil {
		log.Printf("Failed to save config after toggle: %v", err)
	}

	m.refreshServerList()
}

func (m *Model) refreshServerList() {
	entries := m.cfg.ServerEntries()
	items := make([]views.ServerItem, len(entries))
	for i, entry := range entries {
		status := m.serverStatuses[entry.Name]

		// Find namespaces this server belongs to
		var namespaceNames []string
		for nsName, ns := range m.cfg.Namespaces {
			for _, sid := range ns.ServerIDs {
				if sid == entry.Name {
					namespaceNames = append(namespaceNames, nsName)
					break
				}
			}
		}
		sort.Strings(namespaceNames)

		items[i] = views.ServerItem{
			Name:       entry.Name,
			Config:     entry.Config,
			Status:     status,
			Namespaces: namespaceNames,
		}
	}
	m.serverList.SetItems(items)
}

// refreshDetailViewIfShowing updates the detail view if currently showing the specified server.
func (m *Model) refreshDetailViewIfShowing(serverID string) {
	if m.currentView != ViewDetail || m.detailServerID != serverID {
		return
	}
	srv, ok := m.cfg.GetServer(serverID)
	if !ok {
		return
	}
	status := m.serverStatuses[serverID]
	tools := m.convertTools(m.serverTools[serverID])
	m.serverDetail.SetServer(serverID, &srv, &status, tools)
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
	headerHeight := 1 // Tab bar (single line)
	statusHeight := 1 // Status bar
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

	// Set component sizes - servers
	m.serverList.SetSize(contentWidth, contentHeight)
	m.serverDetail.SetSize(contentWidth, contentHeight)

	// Set component sizes - namespaces
	m.namespaceList.SetSize(contentWidth, contentHeight)
	m.namespaceDetail.SetSize(contentWidth, contentHeight)

	// Modal/overlay sizes
	m.serverPicker.SetSize(m.width, m.height)
	m.toolPerms.SetSize(m.width, m.height)

	if m.logPanel.IsVisible() {
		m.logPanel.SetSize(contentWidth, logHeight)
	}
}

// View implements tea.Model.
func (m Model) View() string {
	if m.width == 0 || m.height == 0 {
		return "Loading..."
	}

	m.applyFocus()

	var sections []string

	// Header with tabs
	sections = append(sections, m.renderHeader())

	// Main content based on active tab
	switch m.activeTab {
	case TabServers:
		if m.currentView == ViewList {
			sections = append(sections, m.serverList.View())
		} else {
			sections = append(sections, m.serverDetail.View())
		}
	case TabNamespaces:
		if m.currentView == ViewList {
			sections = append(sections, m.namespaceList.View())
		} else {
			sections = append(sections, m.namespaceDetail.View())
		}
	default:
		sections = append(sections, m.serverList.View())
	}

	// Log panel
	if m.logPanel.IsVisible() {
		sections = append(sections, m.logPanel.View())
	}

	// Status bar
	sections = append(sections, m.renderStatusBar())

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

	// Namespace form overlay
	if m.namespaceForm.IsVisible() {
		content = m.namespaceForm.RenderOverlay(content, m.width, m.height)
	}

	// Server picker overlay
	if m.serverPicker.IsVisible() {
		content = m.serverPicker.RenderOverlay(content, m.width, m.height)
	}

	// Tool permissions overlay
	if m.toolPerms.IsVisible() {
		content = m.toolPerms.RenderOverlay(content, m.width, m.height)
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
		{"Namespaces", true},
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

	appLabel := lipgloss.NewStyle().
		Padding(0, 1).
		Bold(true).
		Background(m.theme.Primary.GetForeground()).
		Foreground(lipgloss.Color("#FFFFFF")).
		Render("mcpmu")
	title := appLabel
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

	// Show context-sensitive key hints based on tab and view
	var keys string
	switch m.activeTab {
	case TabServers:
		enableHint := "E:enable"
		loginHint := ""
		if item := m.serverList.SelectedItem(); item != nil {
			if item.Config.IsEnabled() {
				enableHint = "E:disable"
			}
			// Show login hint for servers needing auth
			if item.Status.State == events.StateNeedsAuth {
				loginHint = "  L:login"
			}
		}

		if m.currentView == ViewList {
			keys = "enter:view  t:test  " + enableHint + loginHint + "  a:add  e:edit  d:delete  l:logs  ?:help"
		} else {
			keys = "esc:back  t:test  " + enableHint + loginHint + "  l:logs  ?:help"
		}
	case TabNamespaces:
		if m.currentView == ViewList {
			keys = "a:add  e:edit  c:copy  d:delete  D:set-default  ?:help"
		} else {
			keys = "esc:back  s:assign-servers  p:permissions  D:set-default  e:edit  ?:help"
		}
	default:
		keys = "?:help"
	}

	// When a toast is visible, render it on the left but keep key hints on the
	// right (so notifications don't hide navigation hints).
	if m.toast.IsVisible() {
		// Ensure the toast doesn't overflow into the key hints area.
		available := m.width - lipgloss.Width(keys) - 4
		if available < 10 {
			available = 10
		}
		if toast := m.toast.ViewWithMaxWidth(available); toast != "" {
			left = toast
		}
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

// ============================================================================
// Modal form update handlers
// ============================================================================

// modalUpdateConfig holds the callbacks for modal-specific behavior.
type modalUpdateConfig struct {
	setSize      func(width, height int)         // Called on WindowSizeMsg to resize the modal
	handleResult func(msg tea.Msg) (bool, Model) // Returns (handled, updatedModel) if msg is the result type
	updateForm   func(msg tea.Msg) tea.Cmd       // Updates the modal form component
}

// updateModal is the common update handler for all modal forms.
// It handles: Ctrl+C quit, window resize, result messages, form updates,
// event handling, and toast updates.
func (m Model) updateModal(msg tea.Msg, cfg modalUpdateConfig) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		if key.Matches(msg, m.keys.CtrlC) {
			return m, tea.Quit
		}
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.updateLayout()
		cfg.setSize(msg.Width, msg.Height)
	default:
		if handled, updatedModel := cfg.handleResult(msg); handled {
			return updatedModel, nil
		}
	}

	if cmd := cfg.updateForm(msg); cmd != nil {
		cmds = append(cmds, cmd)
	}

	if evt, ok := msg.(events.Event); ok {
		if cmd := m.handleEvent(evt); cmd != nil {
			cmds = append(cmds, cmd)
		}
		cmds = append(cmds, m.waitForEvent())
	}

	var toastCmd tea.Cmd
	m.toast, toastCmd = m.toast.Update(msg)
	if toastCmd != nil {
		cmds = append(cmds, toastCmd)
	}

	return m, tea.Batch(cmds...)
}

func (m Model) updateWithServerForm(msg tea.Msg) (tea.Model, tea.Cmd) {
	return m.updateModal(msg, modalUpdateConfig{
		setSize: func(w, h int) {
			m.helpOverlay.SetSize(w, h)
			m.serverForm.SetSize(w, h)
			m.confirmDlg.SetSize(w, h)
			m.toast.SetSize(w, h)
		},
		handleResult: func(msg tea.Msg) (bool, Model) {
			if result, ok := msg.(views.ServerFormResult); ok {
				newModel, _ := m.handleServerFormResult(result)
				return true, newModel.(Model)
			}
			return false, m
		},
		updateForm: func(msg tea.Msg) tea.Cmd {
			return m.serverForm.Update(msg)
		},
	})
}

func (m Model) updateWithNamespaceForm(msg tea.Msg) (tea.Model, tea.Cmd) {
	return m.updateModal(msg, modalUpdateConfig{
		setSize: func(w, h int) {
			m.namespaceForm.SetSize(w, h)
		},
		handleResult: func(msg tea.Msg) (bool, Model) {
			if result, ok := msg.(views.NamespaceFormResult); ok {
				newModel, _ := m.handleNamespaceFormResult(result)
				return true, newModel.(Model)
			}
			return false, m
		},
		updateForm: func(msg tea.Msg) tea.Cmd {
			return m.namespaceForm.Update(msg)
		},
	})
}

func (m Model) updateWithServerPicker(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		if key.Matches(msg, m.keys.CtrlC) {
			return m, tea.Quit
		}
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.updateLayout()
		m.serverPicker.SetSize(msg.Width, msg.Height)
	case views.ServerPickerResult:
		return m.handleServerPickerResult(msg)
	}

	// Update the picker directly on m (not via closure) so visibility changes persist
	if cmd := m.serverPicker.Update(msg); cmd != nil {
		cmds = append(cmds, cmd)
	}

	// Handle events
	if evt, ok := msg.(events.Event); ok {
		if cmd := m.handleEvent(evt); cmd != nil {
			cmds = append(cmds, cmd)
		}
		cmds = append(cmds, m.waitForEvent())
	}

	// Toast updates
	var toastCmd tea.Cmd
	m.toast, toastCmd = m.toast.Update(msg)
	if toastCmd != nil {
		cmds = append(cmds, toastCmd)
	}

	return m, tea.Batch(cmds...)
}

func (m Model) updateWithToolPerms(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		if key.Matches(msg, m.keys.CtrlC) {
			return m, tea.Quit
		}
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.updateLayout()
		m.toolPerms.SetSize(msg.Width, msg.Height)
	case views.ToolPermissionsResult:
		return m.handleToolPermissionsResult(msg)
	}

	// Update the tool perms directly on m (not via closure) so visibility changes persist
	if cmd := m.toolPerms.Update(msg); cmd != nil {
		cmds = append(cmds, cmd)
	}

	// Handle events
	if evt, ok := msg.(events.Event); ok {
		if cmd := m.handleEvent(evt); cmd != nil {
			cmds = append(cmds, cmd)
		}
		cmds = append(cmds, m.waitForEvent())
	}

	// Toast updates
	var toastCmd tea.Cmd
	m.toast, toastCmd = m.toast.Update(msg)
	if toastCmd != nil {
		cmds = append(cmds, toastCmd)
	}

	return m, tea.Batch(cmds...)
}

// ============================================================================
// Namespace key handlers
// ============================================================================

func (m *Model) handleNamespaceListKey(msg tea.KeyMsg) (handled bool, model tea.Model, cmd tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Enter):
		if item := m.namespaceList.SelectedItem(); item != nil {
			m.currentView = ViewDetail
			m.detailNamespaceID = item.Name
			permissions := m.cfg.GetToolPermissionsForNamespace(item.Name)
			m.namespaceDetail.SetNamespace(item.Name, &item.Config, item.IsDefault, m.cfg.ServerEntries(), permissions)
		}
		return true, m, nil

	case key.Matches(msg, m.keys.Add):
		cmd := m.namespaceForm.ShowAdd()
		return true, m, cmd

	case key.Matches(msg, m.keys.Edit):
		if item := m.namespaceList.SelectedItem(); item != nil {
			cmd := m.namespaceForm.ShowEdit(item.Name, item.Config)
			return true, m, cmd
		}
		return true, m, nil

	case key.Matches(msg, m.keys.Delete):
		if item := m.namespaceList.SelectedItem(); item != nil {
			m.pendingDeleteNamespaceID = item.Name
			m.confirmDlg.Show("Delete Namespace", fmt.Sprintf("Delete namespace \"%s\"?\nThis will also remove all associated tool permissions.", item.Name), "delete-namespace")
		}
		return true, m, nil

	case msg.String() == "D": // Set as default
		if item := m.namespaceList.SelectedItem(); item != nil {
			m.cfg.DefaultNamespace = item.Name
			if err := m.saveConfig(); err != nil {
				log.Printf("Failed to save config: %v", err)
				return true, m, m.toast.ShowError(fmt.Sprintf("Failed to save: %v", err))
			}
			m.refreshNamespaceList()
			return true, m, m.toast.ShowSuccess(fmt.Sprintf("Namespace \"%s\" set as default", item.Name))
		}
		return true, m, nil

	case key.Matches(msg, m.keys.Duplicate):
		if item := m.namespaceList.SelectedItem(); item != nil {
			newName := m.uniqueNamespaceCopyName(item.Name)
			newConfig := config.NamespaceConfig{
				Description:   item.Config.Description,
				ServerIDs:     append([]string{}, item.Config.ServerIDs...),
				DenyByDefault: item.Config.DenyByDefault,
			}
			if err := m.cfg.AddNamespace(newName, newConfig); err != nil {
				log.Printf("Failed to duplicate namespace: %v", err)
				return true, m, m.toast.ShowError(fmt.Sprintf("Failed to duplicate: %v", err))
			}
			if err := m.saveConfig(); err != nil {
				log.Printf("Failed to save config: %v", err)
				return true, m, m.toast.ShowError(fmt.Sprintf("Failed to save: %v", err))
			}
			m.refreshNamespaceList()
			return true, m, m.toast.ShowSuccess(fmt.Sprintf("Namespace \"%s\" duplicated", item.Name))
		}
		return true, m, nil
	}

	return false, m, nil
}

// uniqueNamespaceCopyName generates a unique name for a namespace copy.
func (m *Model) uniqueNamespaceCopyName(baseName string) string {
	candidate := baseName + " (copy)"
	if _, exists := m.cfg.Namespaces[candidate]; !exists {
		return candidate
	}
	for i := 2; ; i++ {
		candidate = fmt.Sprintf("%s (copy %d)", baseName, i)
		if _, exists := m.cfg.Namespaces[candidate]; !exists {
			return candidate
		}
	}
}

func (m *Model) handleNamespaceDetailKey(msg tea.KeyMsg) (handled bool, model tea.Model, cmd tea.Cmd) {
	ns, ok := m.cfg.GetNamespace(m.detailNamespaceID)
	if !ok {
		return false, m, nil
	}

	switch {
	case msg.String() == "s": // Assign servers
		m.serverPicker.Show(m.cfg.ServerEntries(), ns.ServerIDs)
		return true, m, nil

	case msg.String() == "p": // Edit permissions
		return m.startToolPermissionEditor(m.detailNamespaceID, &ns)

	case msg.String() == "D": // Set as default
		m.cfg.DefaultNamespace = m.detailNamespaceID
		if err := m.saveConfig(); err != nil {
			log.Printf("Failed to save config: %v", err)
			return true, m, m.toast.ShowError(fmt.Sprintf("Failed to save: %v", err))
		}
		permissions := m.cfg.GetToolPermissionsForNamespace(m.detailNamespaceID)
		m.namespaceDetail.SetNamespace(m.detailNamespaceID, &ns, true, m.cfg.ServerEntries(), permissions)
		m.refreshNamespaceList()
		return true, m, m.toast.ShowSuccess(fmt.Sprintf("Namespace \"%s\" set as default", m.detailNamespaceID))

	case key.Matches(msg, m.keys.Edit):
		cmd := m.namespaceForm.ShowEdit(m.detailNamespaceID, ns)
		return true, m, cmd
	}

	return false, m, nil
}

// serverToStart holds the name and config for a server that needs to be started.
type serverToStart struct {
	name   string
	config config.ServerConfig
}

// startToolPermissionEditor handles the 'p' key to open the permission editor.
// It auto-starts servers if needed and shows a discovery loading state.
func (m *Model) startToolPermissionEditor(nsName string, ns *config.NamespaceConfig) (bool, tea.Model, tea.Cmd) {
	// Collect servers that need to be started and servers already running
	var serversToStart []serverToStart
	var autoStartedIDs []string
	serverTools := make(map[string][]events.McpTool)
	hasDisabledServers := false

	for _, serverName := range ns.ServerIDs {
		srv, ok := m.cfg.GetServer(serverName)
		if !ok {
			continue
		}

		// Check if server is disabled
		if !srv.IsEnabled() {
			hasDisabledServers = true
			continue
		}

		// Check current status
		status, hasStatus := m.serverStatuses[serverName]
		if hasStatus && status.State == events.StateRunning {
			// Already running - use existing tools
			if tools, ok := m.serverTools[serverName]; ok && len(tools) > 0 {
				serverTools[serverName] = tools
			}
		} else if !hasStatus || status.State == events.StateStopped || status.State == events.StateIdle {
			// Not running - need to start
			serversToStart = append(serversToStart, serverToStart{name: serverName, config: srv})
			autoStartedIDs = append(autoStartedIDs, serverName)
		}
	}

	// If no servers assigned, show error
	if len(ns.ServerIDs) == 0 {
		return true, m, m.toast.ShowError("No servers assigned to this namespace.")
	}

	// If all servers are disabled, show error
	if len(serversToStart) == 0 && len(serverTools) == 0 {
		if hasDisabledServers {
			return true, m, m.toast.ShowError("All assigned servers are disabled. Enable them first.")
		}
		return true, m, m.toast.ShowError("No servers available for this namespace.")
	}

	// If all running servers already have tools, show editor immediately
	if len(serversToStart) == 0 && len(serverTools) > 0 {
		m.toolPerms.Show(nsName, serverTools, m.cfg.ServerEntries(), m.cfg.ToolPermissions, ns.DenyByDefault)
		return true, m, nil
	}

	// Need to start servers - show discovery state
	m.permDiscoveryServers = autoStartedIDs
	m.permDiscoveryExpected = len(autoStartedIDs) + len(serverTools)
	m.toolPerms.ShowDiscovering(nsName, autoStartedIDs)

	// Start servers in background
	var cmds []tea.Cmd
	for _, sts := range serversToStart {
		srvName := sts.name
		srvCopy := sts.config
		cmds = append(cmds, func() tea.Msg {
			log.Printf("Auto-starting server %s for permission editor", srvName)
			_, err := m.supervisor.Start(m.ctx, srvName, srvCopy)
			if err != nil {
				log.Printf("Failed to auto-start server %s: %v", srvName, err)
			}
			return nil
		})
	}

	// Add timeout command (15 seconds)
	cmds = append(cmds, tea.Tick(15*time.Second, func(t time.Time) tea.Msg {
		return permDiscoveryTimeoutMsg{}
	}))

	return true, m, tea.Batch(cmds...)
}

// permDiscoveryTimeoutMsg is sent when permission discovery times out.
type permDiscoveryTimeoutMsg struct{}

// checkPermissionDiscoveryComplete checks if all servers have reported tools.
func (m *Model) checkPermissionDiscoveryComplete() {
	ns, ok := m.cfg.GetNamespace(m.detailNamespaceID)
	if !ok {
		return
	}

	// Count how many servers have tools
	toolCount := 0
	for _, serverName := range ns.ServerIDs {
		srv, ok := m.cfg.GetServer(serverName)
		if !ok || !srv.IsEnabled() {
			continue
		}
		if tools, ok := m.serverTools[serverName]; ok && len(tools) > 0 {
			toolCount++
		}
	}

	// If we have tools from all expected servers, finish discovery
	if toolCount >= m.permDiscoveryExpected || toolCount > 0 {
		m.finishPermissionDiscovery()
	}
}

// finishPermissionDiscovery transitions from discovery to editing mode.
func (m *Model) finishPermissionDiscovery() {
	ns, ok := m.cfg.GetNamespace(m.detailNamespaceID)
	if !ok {
		m.toolPerms.Hide()
		return
	}

	// Collect tools from running servers
	serverTools := make(map[string][]events.McpTool)
	for _, serverName := range ns.ServerIDs {
		srv, ok := m.cfg.GetServer(serverName)
		if !ok || !srv.IsEnabled() {
			continue
		}
		if tools, ok := m.serverTools[serverName]; ok && len(tools) > 0 {
			serverTools[serverName] = tools
		}
	}

	if len(serverTools) == 0 {
		// No tools found - hide and show error
		m.toolPerms.Hide()
		// Note: toast will be shown on next tick
		return
	}

	// Transition to editing mode
	m.toolPerms.FinishDiscovery(
		serverTools,
		m.cfg.ServerEntries(),
		m.cfg.ToolPermissions,
		ns.DenyByDefault,
	)
	m.permDiscoveryServers = nil
	m.permDiscoveryExpected = 0
}

// ============================================================================
// Result handlers
// ============================================================================

func (m Model) handleNamespaceFormResult(result views.NamespaceFormResult) (tea.Model, tea.Cmd) {
	if !result.Submitted {
		return m, nil
	}

	var err error
	if result.IsEdit {
		// Check if name changed (rename)
		if result.OriginalName != "" && result.Name != result.OriginalName {
			if err = m.cfg.RenameNamespace(result.OriginalName, result.Name); err != nil {
				log.Printf("Failed to rename namespace: %v", err)
				return m, m.toast.ShowError(fmt.Sprintf("Failed to rename namespace: %v", err))
			}
			// Update detail tracking if we renamed the currently viewed namespace
			if m.detailNamespaceID == result.OriginalName {
				m.detailNamespaceID = result.Name
			}
		}
		err = m.cfg.UpdateNamespace(result.Name, result.Namespace)
		if err != nil {
			log.Printf("Failed to update namespace: %v", err)
			return m, m.toast.ShowError(fmt.Sprintf("Failed to update namespace: %v", err))
		}
	} else {
		err = m.cfg.AddNamespace(result.Name, result.Namespace)
		if err != nil {
			log.Printf("Failed to add namespace: %v", err)
			return m, m.toast.ShowError(fmt.Sprintf("Failed to add namespace: %v", err))
		}
	}

	if err := m.saveConfig(); err != nil {
		log.Printf("Failed to save config: %v", err)
		return m, m.toast.ShowError(fmt.Sprintf("Failed to save config: %v", err))
	}

	m.refreshNamespaceList()
	m.refreshServerList() // Update server list badges (namespace names may have changed)

	// Update detail view if we're editing the currently displayed namespace
	if result.IsEdit && m.currentView == ViewDetail && m.detailNamespaceID == result.Name {
		if ns, ok := m.cfg.GetNamespace(result.Name); ok {
			permissions := m.cfg.GetToolPermissionsForNamespace(result.Name)
			m.namespaceDetail.SetNamespace(result.Name, &ns, result.Name == m.cfg.DefaultNamespace, m.cfg.ServerEntries(), permissions)
		}
	}

	if result.IsEdit {
		return m, m.toast.ShowSuccess(fmt.Sprintf("Namespace \"%s\" updated", result.Name))
	}
	return m, m.toast.ShowSuccess(fmt.Sprintf("Namespace \"%s\" added", result.Name))
}

func (m Model) handleServerPickerResult(result views.ServerPickerResult) (tea.Model, tea.Cmd) {
	if !result.Submitted || m.detailNamespaceID == "" {
		return m, nil
	}

	ns, ok := m.cfg.GetNamespace(m.detailNamespaceID)
	if !ok {
		return m, nil
	}

	// Update server assignments
	ns.ServerIDs = result.SelectedIDs
	m.cfg.Namespaces[m.detailNamespaceID] = ns

	if err := m.saveConfig(); err != nil {
		log.Printf("Failed to save config: %v", err)
		return m, m.toast.ShowError(fmt.Sprintf("Failed to save: %v", err))
	}

	// Refresh detail view
	permissions := m.cfg.GetToolPermissionsForNamespace(m.detailNamespaceID)
	m.namespaceDetail.SetNamespace(m.detailNamespaceID, &ns, m.detailNamespaceID == m.cfg.DefaultNamespace, m.cfg.ServerEntries(), permissions)
	m.refreshNamespaceList()
	m.refreshServerList() // Update server list badges

	return m, m.toast.ShowSuccess("Server assignments updated")
}

func (m Model) handleToolPermissionsResult(result views.ToolPermissionsResult) (tea.Model, tea.Cmd) {
	// Stop auto-started servers regardless of whether changes were submitted
	for _, serverName := range result.AutoStartedServers {
		log.Printf("Stopping auto-started server: %s", serverName)
		go func(name string) { _ = m.supervisor.Stop(name) }(serverName)
	}

	if !result.Submitted || m.detailNamespaceID == "" {
		return m, nil
	}

	// Apply permission changes
	for key, enabled := range result.Changes {
		parts := strings.SplitN(key, ":", 2)
		if len(parts) != 2 {
			continue
		}
		serverName, toolName := parts[0], parts[1]
		if err := m.cfg.SetToolPermission(m.detailNamespaceID, serverName, toolName, enabled); err != nil {
			log.Printf("Failed to set permission: %v", err)
		}
	}

	// Apply permission deletions (revert to default)
	for _, key := range result.Deletions {
		parts := strings.SplitN(key, ":", 2)
		if len(parts) != 2 {
			continue
		}
		serverName, toolName := parts[0], parts[1]
		if err := m.cfg.UnsetToolPermission(m.detailNamespaceID, serverName, toolName); err != nil {
			log.Printf("Failed to unset permission: %v", err)
		}
	}

	if err := m.saveConfig(); err != nil {
		log.Printf("Failed to save config: %v", err)
		return m, m.toast.ShowError(fmt.Sprintf("Failed to save: %v", err))
	}

	// Refresh detail view
	if ns, ok := m.cfg.GetNamespace(m.detailNamespaceID); ok {
		permissions := m.cfg.GetToolPermissionsForNamespace(m.detailNamespaceID)
		m.namespaceDetail.SetNamespace(m.detailNamespaceID, &ns, m.detailNamespaceID == m.cfg.DefaultNamespace, m.cfg.ServerEntries(), permissions)
	}

	return m, m.toast.ShowSuccess("Tool permissions updated")
}

// ============================================================================
// Refresh helpers
// ============================================================================

func (m *Model) refreshNamespaceList() {
	entries := m.cfg.NamespaceEntries()
	items := make([]views.NamespaceItem, len(entries))
	for i, entry := range entries {
		items[i] = views.NamespaceItem{
			Name:      entry.Name,
			Config:    entry.Config,
			IsDefault: entry.Name == m.cfg.DefaultNamespace,
		}
	}
	m.namespaceList.SetItems(items)
}
