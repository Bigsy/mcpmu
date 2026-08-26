package tui

// Confirm dialog: the quit prompt and every destructive-action confirmation.

import (
	"fmt"
	"log"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/events"
	"github.com/Bigsy/mcpmu/internal/tui/views"
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
)

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

		// Delete from config (the ToolCache entry goes with it)
		if err := m.mutate(func(cfg *config.Config) error { return cfg.DeleteServer(serverName) }); err != nil {
			log.Printf("Failed to delete server: %v", err)
			m.pendingDeleteID = ""
			return m, m.toast.ShowError(fmt.Sprintf("Failed to delete server: %v", err))
		}

		// Clear status tracking
		delete(m.serverStatuses, m.pendingDeleteID)
		delete(m.serverTools, m.pendingDeleteID)

		// Refresh list
		m.refreshServerList()
		m.refreshNamespaceList()

		m.pendingDeleteID = ""
		return m, m.toast.ShowSuccess(fmt.Sprintf("Server \"%s\" deleted", serverName))
	}

	if result.Tag == "delete-namespace" && result.Confirmed {
		// Namespace name is the ID now
		namespaceName := m.pendingDeleteNamespaceID

		if err := m.mutate(func(cfg *config.Config) error { return cfg.DeleteNamespace(namespaceName) }); err != nil {
			log.Printf("Failed to delete namespace: %v", err)
			m.pendingDeleteNamespaceID = ""
			return m, m.toast.ShowError(fmt.Sprintf("Failed to delete namespace: %v", err))
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
