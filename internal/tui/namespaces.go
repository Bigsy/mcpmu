package tui

// Namespaces tab: list/detail key handling, tool-permission editor and its
// discovery wait, namespace/picker/permissions results, refresh helpers.

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/events"
	"github.com/Bigsy/mcpmu/internal/server"
	"github.com/Bigsy/mcpmu/internal/tui/views"
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
)

func (m *Model) handleNamespaceListKey(msg tea.KeyMsg) (handled bool, model tea.Model, cmd tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Enter):
		if item := m.namespaceList.SelectedItem(); item != nil {
			m.currentView = ViewDetail
			m.detailNamespaceID = item.Name
			permissions := m.cfg.GetToolPermissionsForNamespace(item.Name)
			serverTokens := m.getServerTokensForNamespace(item.Name)
			m.namespaceDetail.SetNamespace(item.Name, &item.Config, item.IsDefault, m.cfg.ServerEntries(), permissions, serverTokens)
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
			if err := m.mutate(func(cfg *config.Config) error { cfg.DefaultNamespace = item.Name; return nil }); err != nil {
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
			if err := m.mutate(func(cfg *config.Config) error { return cfg.DuplicateNamespace(item.Name, newName) }); err != nil {
				log.Printf("Failed to duplicate namespace: %v", err)
				return true, m, m.toast.ShowError(fmt.Sprintf("Failed to duplicate: %v", err))
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
		if err := m.mutate(func(cfg *config.Config) error { cfg.DefaultNamespace = m.detailNamespaceID; return nil }); err != nil {
			log.Printf("Failed to save config: %v", err)
			return true, m, m.toast.ShowError(fmt.Sprintf("Failed to save: %v", err))
		}
		permissions := m.cfg.GetToolPermissionsForNamespace(m.detailNamespaceID)
		serverTokens := m.getServerTokensForNamespace(m.detailNamespaceID)
		m.namespaceDetail.SetNamespace(m.detailNamespaceID, &ns, true, m.cfg.ServerEntries(), permissions, serverTokens)
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
		m.toolPerms.Show(nsName, serverTools, m.cfg.ServerEntries(), m.cfg.ToolPermissions, ns.DenyByDefault, ns.ServerDefaults, buildGlobalDenied(m.cfg))
		return true, m, nil
	}

	// Need to start servers - show discovery state
	// We expect ALL servers being started to report tools before finishing
	m.permDiscoveryServers = autoStartedIDs
	m.permDiscoveryExpected = len(autoStartedIDs)
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

// checkPermissionDiscoveryComplete checks if all servers have reported tools or failed.
func (m *Model) checkPermissionDiscoveryComplete() {
	if len(m.permDiscoveryServers) == 0 {
		return
	}

	// Count servers that are "done" - either have tools or have failed
	doneCount := 0
	for _, serverName := range m.permDiscoveryServers {
		// Server has reported tools
		if tools, ok := m.serverTools[serverName]; ok && len(tools) > 0 {
			doneCount++
			continue
		}
		// Server has failed/errored - won't report tools, count as done
		if status, ok := m.serverStatuses[serverName]; ok {
			switch status.State {
			case events.StateError, events.StateCrashed, events.StateStopped:
				doneCount++
			}
		}
	}

	// Finish when all servers we started are done (have tools or failed)
	if doneCount >= m.permDiscoveryExpected {
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
		ns.ServerDefaults,
		buildGlobalDenied(m.cfg),
	)
	m.permDiscoveryServers = nil
	m.permDiscoveryExpected = 0
}

// buildGlobalDenied builds a map of serverName -> denied tool names from the config.
func buildGlobalDenied(cfg *config.Config) map[string][]string {
	result := make(map[string][]string)
	for name, srv := range cfg.Servers {
		if len(srv.DeniedTools) > 0 {
			result[name] = srv.DeniedTools
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func (m Model) handleNamespaceFormResult(result views.NamespaceFormResult) (tea.Model, tea.Cmd) {
	if !result.Submitted {
		return m, nil
	}

	renamed := result.IsEdit && result.OriginalName != "" && result.Name != result.OriginalName
	err := m.mutate(func(cfg *config.Config) error {
		if !result.IsEdit {
			return cfg.AddNamespace(result.Name, result.Namespace)
		}
		if renamed {
			if err := cfg.RenameNamespace(result.OriginalName, result.Name); err != nil {
				return fmt.Errorf("rename namespace: %w", err)
			}
		}
		return cfg.UpdateNamespace(result.Name, result.Namespace)
	})
	if err != nil {
		log.Printf("Failed to save namespace: %v", err)
		return m, m.toast.ShowError(fmt.Sprintf("Failed to save namespace: %v", err))
	}

	// Update detail tracking if we renamed the currently viewed namespace
	if renamed && m.detailNamespaceID == result.OriginalName {
		m.detailNamespaceID = result.Name
	}

	m.refreshNamespaceList()
	m.refreshServerList() // Update server list badges (namespace names may have changed)

	// Update detail view if we're editing the currently displayed namespace
	if result.IsEdit && m.currentView == ViewDetail && m.detailNamespaceID == result.Name {
		if ns, ok := m.cfg.GetNamespace(result.Name); ok {
			permissions := m.cfg.GetToolPermissionsForNamespace(result.Name)
			serverTokens := m.getServerTokensForNamespace(result.Name)
			m.namespaceDetail.SetNamespace(result.Name, &ns, result.Name == m.cfg.DefaultNamespace, m.cfg.ServerEntries(), permissions, serverTokens)
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

	// Update server assignments
	err := m.mutate(func(cfg *config.Config) error {
		ns, ok := cfg.GetNamespace(m.detailNamespaceID)
		if !ok {
			return fmt.Errorf("namespace %q not found", m.detailNamespaceID)
		}
		ns.ServerIDs = result.SelectedIDs
		cfg.Namespaces[m.detailNamespaceID] = ns
		return nil
	})
	if err != nil {
		log.Printf("Failed to save config: %v", err)
		return m, m.toast.ShowError(fmt.Sprintf("Failed to save: %v", err))
	}
	ns, ok := m.cfg.GetNamespace(m.detailNamespaceID)
	if !ok {
		return m, nil
	}

	// Refresh detail view
	permissions := m.cfg.GetToolPermissionsForNamespace(m.detailNamespaceID)
	serverTokens := m.getServerTokensForNamespace(m.detailNamespaceID)
	m.namespaceDetail.SetNamespace(m.detailNamespaceID, &ns, m.detailNamespaceID == m.cfg.DefaultNamespace, m.cfg.ServerEntries(), permissions, serverTokens)
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

	err := m.mutate(func(cfg *config.Config) error {
		// Apply permission changes
		for key, enabled := range result.Changes {
			parts := strings.SplitN(key, ":", 2)
			if len(parts) != 2 {
				continue
			}
			serverName, toolName := parts[0], parts[1]
			if err := cfg.SetToolPermission(m.detailNamespaceID, serverName, toolName, enabled); err != nil {
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
			if err := cfg.UnsetToolPermission(m.detailNamespaceID, serverName, toolName); err != nil {
				log.Printf("Failed to unset permission: %v", err)
			}
		}
		return nil
	})
	if err != nil {
		log.Printf("Failed to save config: %v", err)
		return m, m.toast.ShowError(fmt.Sprintf("Failed to save: %v", err))
	}

	// Refresh detail view
	if ns, ok := m.cfg.GetNamespace(m.detailNamespaceID); ok {
		permissions := m.cfg.GetToolPermissionsForNamespace(m.detailNamespaceID)
		serverTokens := m.getServerTokensForNamespace(m.detailNamespaceID)
		m.namespaceDetail.SetNamespace(m.detailNamespaceID, &ns, m.detailNamespaceID == m.cfg.DefaultNamespace, m.cfg.ServerEntries(), permissions, serverTokens)
	}
	m.refreshNamespaceList()

	return m, m.toast.ShowSuccess("Tool permissions updated")
}

func (m *Model) refreshNamespaceList() {
	entries := m.cfg.NamespaceEntries()
	items := make([]views.NamespaceItem, len(entries))
	for i, entry := range entries {
		tokenCount, hasCache := m.getNamespaceTokenCount(entry.Name)
		items[i] = views.NamespaceItem{
			Name:       entry.Name,
			Config:     entry.Config,
			IsDefault:  entry.Name == m.cfg.DefaultNamespace,
			TokenCount: tokenCount,
			HasCache:   hasCache,
		}
	}
	m.namespaceList.SetItems(items)
}

// getNamespaceTokenCount calculates total tokens for enabled tools in a namespace.
func (m *Model) getNamespaceTokenCount(nsName string) (total int, hasAnyCache bool) {
	if m.toolCache == nil {
		return 0, false
	}
	ns, ok := m.cfg.GetNamespace(nsName)
	if !ok {
		return 0, false
	}

	for _, serverID := range ns.ServerIDs {
		srv, ok := m.cfg.GetServer(serverID)
		if !ok || !srv.IsEnabled() {
			continue
		}

		cachedTools, ok := m.toolCache.Get(serverID)
		if !ok {
			continue
		}
		hasAnyCache = true

		for _, tool := range cachedTools {
			allowed, _ := server.IsToolAllowed(m.cfg, nsName, serverID, tool.Name)
			if allowed {
				total += tool.TokenCount
			}
		}
	}
	return total, hasAnyCache
}

// getServerTokensForNamespace returns per-server token counts for enabled tools.
func (m *Model) getServerTokensForNamespace(nsName string) map[string]int {
	result := make(map[string]int)
	if m.toolCache == nil {
		return result
	}
	ns, ok := m.cfg.GetNamespace(nsName)
	if !ok {
		return result
	}
	for _, serverID := range ns.ServerIDs {
		srv, ok := m.cfg.GetServer(serverID)
		if !ok || !srv.IsEnabled() {
			continue
		}
		cachedTools, ok := m.toolCache.Get(serverID)
		if !ok {
			continue
		}
		total := 0
		for _, tool := range cachedTools {
			allowed, _ := server.IsToolAllowed(m.cfg, nsName, serverID, tool.Name)
			if allowed {
				total += tool.TokenCount
			}
		}
		result[serverID] = total
	}
	return result
}

// refreshNamespaceDetailIfShowing refreshes the namespace detail view if currently showing.
func (m *Model) refreshNamespaceDetailIfShowing() {
	if m.activeTab != TabNamespaces || m.currentView != ViewDetail || m.detailNamespaceID == "" {
		return
	}
	ns, ok := m.cfg.GetNamespace(m.detailNamespaceID)
	if !ok {
		return
	}
	serverTokens := m.getServerTokensForNamespace(m.detailNamespaceID)
	permissions := m.cfg.GetToolPermissionsForNamespace(m.detailNamespaceID)
	m.namespaceDetail.SetNamespace(
		m.detailNamespaceID,
		&ns,
		m.detailNamespaceID == m.cfg.DefaultNamespace,
		m.cfg.ServerEntries(),
		permissions,
		serverTokens,
	)
}
