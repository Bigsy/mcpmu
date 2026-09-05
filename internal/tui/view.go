package tui

// Rendering: View, header, status bar and the confirm overlay.

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

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

	// Tool deny editor overlay
	if m.toolDenyEditor.IsVisible() {
		content = m.toolDenyEditor.RenderOverlay(content, m.width, m.height)
	}

	// Add method selector overlay
	if m.addMethod.IsVisible() {
		content = m.addMethod.RenderOverlay(content, m.width, m.height)
	}

	// Registry browser overlay
	if m.registryBrowser.IsVisible() {
		content = m.registryBrowser.RenderOverlay(content, m.width, m.height)
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
	padding := max(m.width-lipgloss.Width(title)-lipgloss.Width(tabBar)-4, 1)

	return title + strings.Repeat(" ", padding) + tabBar
}

func (m Model) renderStatusBar() string {
	runningCount := m.supervisor.RunningCount()
	totalCount := len(m.cfg.Servers)

	left := fmt.Sprintf("%d/%d servers running (management session)", runningCount, totalCount)

	// Show context-sensitive key hints based on tab and view
	var keys string
	switch m.activeTab {
	case TabServers:
		enableHint := "E:enable"
		oauthHint := ""
		if item := m.serverList.SelectedItem(); item != nil {
			if item.Config.IsEnabled() {
				enableHint = "E:disable"
			}
			// Show OAuth hints for OAuth-capable servers (HTTP without bearer token)
			if item.Config.IsHTTP() && item.Config.BearerTokenEnvVar == "" {
				oauthHint = "  L:login  O:logout"
			}
		}

		if m.currentView == ViewList {
			keys = "enter:view  t:test  " + enableHint + oauthHint + "  a:add  e:edit  d:delete  l:logs  ?:help"
		} else {
			keys = "esc:back  t:test  " + enableHint + oauthHint + "  p:deny-tools  l:logs  ?:help"
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
		available := max(m.width-lipgloss.Width(keys)-4, 10)
		if toast := m.toast.ViewWithMaxWidth(available); toast != "" {
			left = toast
		}
	}

	padding := max(m.width-lipgloss.Width(left)-lipgloss.Width(keys)-4, 1)

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
