package tui

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/events"
	"github.com/Bigsy/mcpmu/internal/process"
	"github.com/Bigsy/mcpmu/internal/registry"
	"github.com/Bigsy/mcpmu/internal/tui/theme"
	"github.com/Bigsy/mcpmu/internal/tui/views"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
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
	configPath string // Resolved config path (always set)
	toolCache  *config.ToolCache

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
	toolDenyEditor  views.ToolDenyEditorModel
	registryBrowser views.RegistryBrowserModel

	// Shared Components
	logPanel    views.LogPanelModel
	helpOverlay views.HelpOverlayModel
	confirmDlg  views.ConfirmModel
	addMethod   views.AddMethodModel
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

	// Pending registry install (deferred form opening)
	pendingRegistryInstall *registry.InstallSpec

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
func NewModel(cfg *config.Config, supervisor *process.Supervisor, bus *events.Bus, configPath string, toolCache *config.ToolCache) Model {
	th := theme.New()
	keys := NewKeyBindings()

	m := Model{
		cfg:             cfg,
		supervisor:      supervisor,
		bus:             bus,
		ctx:             context.Background(),
		configPath:      configPath,
		toolCache:       toolCache,
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
		toolDenyEditor:  views.NewToolDenyEditor(th),
		registryBrowser: views.NewRegistryBrowser(th),
		logPanel:        views.NewLogPanel(th),
		helpOverlay:     views.NewHelpOverlay(th),
		confirmDlg:      views.NewConfirm(th),
		addMethod:       views.NewAddMethod(th),
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

// mutate applies fn to the config file through config.MutateWithCache and
// adopts the saved result as the model's config. Every TUI config change goes
// through here: the in-memory copy is never written back wholesale, so a
// concurrent CLI or web edit is merged instead of overwritten, and ToolCache
// upkeep for renames/removals happens inside the helper.
func (m *Model) mutate(fn func(*config.Config) error) error {
	if m.configPath == "" {
		// cmd/mcpmu always resolves the path before building the model; an
		// empty one must not fall through to config.Mutate's default path.
		return errors.New("config path not set")
	}
	fresh, err := config.MutateWithCache(m.configPath, m.toolCache, fn)
	if err != nil {
		return err
	}
	m.cfg = fresh
	return nil
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
	if m.modalVisible() {
		return m.updateModal(msg)
	}

	// Handle pending registry install (deferred form opening after browser closes)
	if m.pendingRegistryInstall != nil {
		spec := m.pendingRegistryInstall
		m.pendingRegistryInstall = nil
		if spec.CommandOrURL == "" {
			return m, m.toast.ShowError("Server has no installable packages")
		}
		cmd := m.serverForm.ShowAddWithDefaults(spec.Name, spec.CommandOrURL, spec.Args, formatEnvMap(spec.Env), spec.BearerTokenEnvVar, "", "", "")
		return m, cmd
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

	case views.ToolDenyResult:
		return m.handleToolDenyResult(msg)

	case views.AddMethodResult:
		return m.handleAddMethodResult(msg)

	case views.RegistryBrowserResult:
		return m.handleRegistryBrowserResult(msg)

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
			// Check if we're waiting for this server during permission discovery
			if m.toolPerms.IsDiscovering() {
				m.checkPermissionDiscoveryComplete()
			}
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

		// Refresh namespace views (token counts may have changed)
		m.refreshNamespaceList()
		m.refreshNamespaceDetailIfShowing()

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

func (m *Model) updateLayout() {
	// Calculate heights more carefully
	headerHeight := 1 // Tab bar (single line)
	statusHeight := 1 // Status bar
	logHeight := 0
	if m.logPanel.IsVisible() {
		logHeight = 10 // Log panel height when visible (including border)
	}

	// Available height for main content
	contentHeight := max(m.height-headerHeight-statusHeight-logHeight,
		// Minimum content height
		5)

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
	m.toolDenyEditor.SetSize(m.width, m.height)
	m.addMethod.SetSize(m.width, m.height)
	m.registryBrowser.SetSize(m.width, m.height)

	if m.logPanel.IsVisible() {
		m.logPanel.SetSize(contentWidth, logHeight)
	}
}
