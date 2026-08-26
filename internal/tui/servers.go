package tui

// Servers tab: list/detail key handling, server form result, start/stop,
// OAuth login/logout, enable toggle and the server-side refresh helpers.

import (
	"fmt"
	"log"
	"slices"
	"sort"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/events"
	"github.com/Bigsy/mcpmu/internal/mcp"
	"github.com/Bigsy/mcpmu/internal/tui/views"
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
)

func (m *Model) handleServerListKey(msg tea.KeyMsg) (handled bool, model tea.Model, cmd tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Enter):
		if item := m.serverList.SelectedItem(); item != nil {
			m.currentView = ViewDetail
			m.detailServerID = item.Name
			status := m.serverStatuses[item.Name]
			tools, toolTokens, fromCache := m.getServerToolsForDetail(item.Name)
			m.serverDetail.SetServer(item.Name, &item.Config, &status, tools, toolTokens, fromCache)
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
		m.addMethod.Show()
		return true, m, nil

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
			if item.Config.IsHTTP() && item.Config.BearerTokenEnvVar == "" {
				if item.Status.State == events.StateNeedsAuth {
					go m.loginOAuth(item.Name)
					return true, m, m.toast.ShowInfo("Opening browser for OAuth login...")
				}
				return true, m, m.toast.ShowError(oauthLoginHint(item.Status.State))
			}
			return true, m, m.toast.ShowError("OAuth login only applies to OAuth HTTP servers")
		}
		return true, m, nil

	case key.Matches(msg, m.keys.Logout):
		if item := m.serverList.SelectedItem(); item != nil {
			if item.Config.IsHTTP() && item.Config.BearerTokenEnvVar == "" {
				if err := m.logoutOAuth(item.Name); err != nil {
					return true, m, m.toast.ShowError(fmt.Sprintf("OAuth logout failed: %v", err))
				}
				return true, m, m.toast.ShowSuccess(fmt.Sprintf("Logged out from \"%s\"", item.Name))
			}
			return true, m, m.toast.ShowError("OAuth logout only applies to OAuth HTTP servers")
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
			srv, ok := m.cfg.GetServer(m.detailServerID)
			if ok && srv.IsHTTP() && srv.BearerTokenEnvVar == "" {
				status := m.serverStatuses[m.detailServerID]
				if status.State == events.StateNeedsAuth {
					go m.loginOAuth(m.detailServerID)
					return true, m, m.toast.ShowInfo("Opening browser for OAuth login...")
				}
				return true, m, m.toast.ShowError(oauthLoginHint(status.State))
			}
			return true, m, m.toast.ShowError("OAuth login only applies to OAuth HTTP servers")
		}
		return true, m, nil

	case key.Matches(msg, m.keys.Logout):
		if m.detailServerID != "" {
			srv, ok := m.cfg.GetServer(m.detailServerID)
			if ok && srv.IsHTTP() && srv.BearerTokenEnvVar == "" {
				if err := m.logoutOAuth(m.detailServerID); err != nil {
					return true, m, m.toast.ShowError(fmt.Sprintf("OAuth logout failed: %v", err))
				}
				return true, m, m.toast.ShowSuccess(fmt.Sprintf("Logged out from \"%s\"", m.detailServerID))
			}
			return true, m, m.toast.ShowError("OAuth logout only applies to OAuth HTTP servers")
		}
		return true, m, nil

	case msg.String() == "p": // Edit denied tools
		if m.detailServerID != "" {
			tools, _, _ := m.getServerToolsForDetail(m.detailServerID)
			if len(tools) == 0 {
				return true, m, m.toast.ShowError("No tools discovered — start the server first")
			}
			srv, _ := m.cfg.GetServer(m.detailServerID)
			m.toolDenyEditor.Show(m.detailServerID, tools, srv.DeniedTools)
			return true, m, nil
		}
		return true, m, nil
	}

	return false, m, nil
}

func (m Model) handleServerFormResult(result views.ServerFormResult) (tea.Model, tea.Cmd) {
	if !result.Submitted {
		if result.Err != nil {
			return m, m.toast.ShowError(fmt.Sprintf("Invalid server: %v", result.Err))
		}
		// Form was cancelled
		return m, nil
	}

	err := m.mutate(func(cfg *config.Config) error {
		if !result.IsEdit {
			return cfg.AddServer(result.Name, result.Server)
		}
		if result.OriginalName != "" && result.Name != result.OriginalName {
			if err := cfg.RenameServer(result.OriginalName, result.Name); err != nil {
				return fmt.Errorf("rename server: %w", err)
			}
		}
		return cfg.UpdateServer(result.Name, result.Server)
	})
	if err != nil {
		log.Printf("Failed to save server: %v", err)
		return m, m.toast.ShowError(fmt.Sprintf("Failed to save server: %v", err))
	}

	// Refresh the list
	m.refreshServerList()

	// Show success toast
	if result.IsEdit {
		return m, m.toast.ShowSuccess(fmt.Sprintf("Server \"%s\" updated", result.Name))
	}
	return m, m.toast.ShowSuccess(fmt.Sprintf("Server \"%s\" added", result.Name))
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

func (m *Model) logoutOAuth(name string) error {
	srv, ok := m.cfg.GetServer(name)
	if !ok {
		return fmt.Errorf("server not found")
	}
	store := m.supervisor.CredentialStore()
	if store == nil {
		return fmt.Errorf("no credential store available")
	}
	if err := store.Delete(srv.URL); err != nil {
		return fmt.Errorf("failed to remove credentials: %w", err)
	}
	// Stop server if running to avoid stale auth in-memory
	status := m.serverStatuses[name]
	if status.State == events.StateRunning || status.State == events.StateStarting {
		_ = m.supervisor.Stop(name)
	}
	return nil
}

// oauthLoginHint returns a user-facing message explaining why L didn't trigger,
// tailored to the server's current state.
func oauthLoginHint(state events.RuntimeState) string {
	switch state {
	case events.StateRunning:
		return "Server already authenticated — use O to logout first, then restart with t"
	case events.StateStarting:
		return "Server is starting — wait for it to reach the auth prompt"
	case events.StateIdle, events.StateStopped:
		return "Start the server first with t to begin OAuth login"
	default:
		return "Server is not awaiting OAuth login"
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
	err := m.mutate(func(cfg *config.Config) error {
		srv, ok := cfg.GetServer(id)
		if !ok {
			return fmt.Errorf("server %q not found", id)
		}
		srv.SetEnabled(newEnabled)
		cfg.Servers[id] = srv
		return nil
	})
	if err != nil {
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
			if slices.Contains(ns.ServerIDs, entry.Name) {
				namespaceNames = append(namespaceNames, nsName)
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
	tools, toolTokens, fromCache := m.getServerToolsForDetail(serverID)
	m.serverDetail.SetServer(serverID, &srv, &status, tools, toolTokens, fromCache)
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

func (m Model) handleToolDenyResult(result views.ToolDenyResult) (tea.Model, tea.Cmd) {
	if !result.Submitted || result.ServerName == "" {
		return m, nil
	}

	err := m.mutate(func(cfg *config.Config) error {
		srv, ok := cfg.GetServer(result.ServerName)
		if !ok {
			return fmt.Errorf("server %q not found", result.ServerName)
		}
		// Replace the deny list
		if len(result.DeniedTools) == 0 {
			srv.DeniedTools = nil
		} else {
			srv.DeniedTools = slices.Sorted(slices.Values(result.DeniedTools))
		}
		cfg.Servers[result.ServerName] = srv
		return nil
	})
	if err != nil {
		log.Printf("Failed to save denied tools: %v", err)
		return m, m.toast.ShowError(fmt.Sprintf("Failed to save: %v", err))
	}

	// Refresh server detail view
	if srv, ok := m.cfg.GetServer(result.ServerName); ok && m.detailServerID == result.ServerName {
		status := m.serverStatuses[result.ServerName]
		tools, toolTokens, fromCache := m.getServerToolsForDetail(result.ServerName)
		m.serverDetail.SetServer(result.ServerName, &srv, &status, tools, toolTokens, fromCache)
	}
	m.refreshServerList()

	return m, m.toast.ShowSuccess("Denied tools updated")
}

// getServerToolsForDetail returns tools and token info for the server detail view.
// Prefers live tools; falls back to cache when server is not running.
func (m *Model) getServerToolsForDetail(serverID string) (tools []mcp.Tool, toolTokens map[string]int, fromCache bool) {
	toolTokens = make(map[string]int)

	// Check for live tools first — presence in the map means the server has
	// reported tools (even if the list is empty, that's authoritative).
	if liveTools, ok := m.serverTools[serverID]; ok {
		tools = m.convertTools(liveTools)
		// Get token counts from cache if available
		if m.toolCache != nil {
			if cachedTools, ok := m.toolCache.Get(serverID); ok {
				for _, ct := range cachedTools {
					toolTokens[ct.Name] = ct.TokenCount
				}
			}
		}
		return tools, toolTokens, false
	}

	// Fall back to cache
	if m.toolCache != nil {
		if cachedTools, ok := m.toolCache.Get(serverID); ok {
			tools = make([]mcp.Tool, len(cachedTools))
			for i, ct := range cachedTools {
				tools[i] = mcp.Tool{
					Name:        ct.Name,
					Description: ct.Description,
				}
				toolTokens[ct.Name] = ct.TokenCount
			}
			return tools, toolTokens, true
		}
	}

	return nil, toolTokens, false
}
