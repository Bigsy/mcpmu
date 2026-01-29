package views

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/hedworth/mcp-studio-go/internal/config"
	"github.com/hedworth/mcp-studio-go/internal/tui/theme"
)

// NamespaceDetailModel displays detailed information about a namespace.
type NamespaceDetailModel struct {
	theme     theme.Theme
	namespace *config.NamespaceConfig
	isDefault bool
	// All servers for assignment display
	allServers []config.ServerConfig
	// Tool permissions for this namespace
	permissions []config.ToolPermission
	viewport    viewport.Model
	width       int
	height      int
	focused     bool
}

// NewNamespaceDetail creates a new namespace detail view.
func NewNamespaceDetail(th theme.Theme) NamespaceDetailModel {
	vp := viewport.New(0, 0)
	return NamespaceDetailModel{
		theme:    th,
		viewport: vp,
	}
}

// SetNamespace sets the namespace to display.
func (m *NamespaceDetailModel) SetNamespace(ns *config.NamespaceConfig, isDefault bool, allServers []config.ServerConfig, permissions []config.ToolPermission) {
	m.namespace = ns
	m.isDefault = isDefault
	m.allServers = allServers
	m.permissions = permissions
	m.updateContent()
}

// SetSize sets the dimensions.
func (m *NamespaceDetailModel) SetSize(width, height int) {
	m.width = width
	m.height = height
	m.viewport.Width = width - 4
	m.viewport.Height = height - 2
	if m.viewport.Width < 10 {
		m.viewport.Width = 10
	}
	if m.viewport.Height < 1 {
		m.viewport.Height = 1
	}
	m.updateContent()
}

// SetFocused sets whether the view is focused.
func (m *NamespaceDetailModel) SetFocused(focused bool) {
	m.focused = focused
}

func (m *NamespaceDetailModel) updateContent() {
	if m.namespace == nil {
		m.viewport.SetContent("No namespace selected")
		return
	}

	var content strings.Builder

	// Header
	content.WriteString(m.theme.Title.Render(m.namespace.Name))
	if m.isDefault {
		content.WriteString("  ")
		content.WriteString(m.theme.Primary.Render("[default]"))
	}
	content.WriteString("\n\n")

	infoStyle := m.theme.Muted
	labelStyle := m.theme.Base.Bold(true)

	// Description
	if m.namespace.Description != "" {
		content.WriteString(labelStyle.Render("Description: "))
		content.WriteString(infoStyle.Render(m.namespace.Description))
		content.WriteString("\n")
	}

	// Deny by default status
	content.WriteString(labelStyle.Render("Default Policy: "))
	if m.namespace.DenyByDefault {
		content.WriteString(m.theme.Warn.Render("Deny unconfigured tools"))
	} else {
		content.WriteString(m.theme.Success.Render("Allow unconfigured tools"))
	}
	content.WriteString("\n\n")

	// Assigned servers section
	content.WriteString(m.theme.Title.Render(fmt.Sprintf("Assigned Servers (%d)", len(m.namespace.ServerIDs))))
	content.WriteString("\n")

	if len(m.namespace.ServerIDs) == 0 {
		content.WriteString(m.theme.Faint.Render("  No servers assigned"))
		content.WriteString("\n")
		content.WriteString(m.theme.Faint.Render("  Press 's' to assign servers"))
		content.WriteString("\n")
	} else {
		serverBox := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.AdaptiveColor{Light: "#D0D7DE", Dark: "#3B4261"}).
			Padding(0, 1).
			Width(m.width - 8)

		var serversContent strings.Builder
		for i, serverID := range m.namespace.ServerIDs {
			if i > 0 {
				serversContent.WriteString("\n")
			}
			// Find server name
			serverName := serverID
			for _, srv := range m.allServers {
				if srv.ID == serverID {
					if srv.Name != "" {
						serverName = srv.Name
					}
					break
				}
			}
			serversContent.WriteString(m.theme.Primary.Render(serverName))
			serversContent.WriteString(m.theme.Faint.Render(fmt.Sprintf(" (%s)", serverID)))
		}
		content.WriteString(serverBox.Render(serversContent.String()))
	}

	// Tool permissions section
	content.WriteString("\n\n")
	content.WriteString(m.theme.Title.Render(fmt.Sprintf("Tool Permissions (%d configured)", len(m.permissions))))
	content.WriteString("\n")

	if len(m.permissions) == 0 {
		if m.namespace.DenyByDefault {
			content.WriteString(m.theme.Warn.Render("  No tools explicitly allowed"))
			content.WriteString("\n")
			content.WriteString(m.theme.Faint.Render("  All tools will be blocked. Press 'p' to configure permissions."))
		} else {
			content.WriteString(m.theme.Faint.Render("  No explicit permissions set"))
			content.WriteString("\n")
			content.WriteString(m.theme.Faint.Render("  All tools allowed by default. Press 'p' to configure permissions."))
		}
		content.WriteString("\n")
	} else {
		permBox := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.AdaptiveColor{Light: "#D0D7DE", Dark: "#3B4261"}).
			Padding(0, 1).
			Width(m.width - 8)

		var permsContent strings.Builder
		// Group by server
		serverPerms := make(map[string][]config.ToolPermission)
		for _, perm := range m.permissions {
			serverPerms[perm.ServerID] = append(serverPerms[perm.ServerID], perm)
		}

		first := true
		for serverID, perms := range serverPerms {
			if !first {
				permsContent.WriteString("\n\n")
			}
			first = false

			// Find server name
			serverName := serverID
			for _, srv := range m.allServers {
				if srv.ID == serverID {
					if srv.Name != "" {
						serverName = srv.Name
					}
					break
				}
			}
			permsContent.WriteString(m.theme.Item.Bold(true).Render(serverName))
			permsContent.WriteString("\n")

			for _, perm := range perms {
				if perm.Enabled {
					permsContent.WriteString("  ")
					permsContent.WriteString(m.theme.Success.Render("[+] "))
					permsContent.WriteString(m.theme.Primary.Render(perm.ToolName))
				} else {
					permsContent.WriteString("  ")
					permsContent.WriteString(m.theme.Danger.Render("[-] "))
					permsContent.WriteString(m.theme.Muted.Render(perm.ToolName))
				}
				permsContent.WriteString("\n")
			}
		}
		content.WriteString(permBox.Render(strings.TrimRight(permsContent.String(), "\n")))
	}

	m.viewport.SetContent(content.String())
}

// Init implements tea.Model.
func (m NamespaceDetailModel) Init() tea.Cmd {
	return nil
}

// Update implements tea.Model.
func (m NamespaceDetailModel) Update(msg tea.Msg) (NamespaceDetailModel, tea.Cmd) {
	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	return m, cmd
}

// View implements tea.Model.
func (m NamespaceDetailModel) View() string {
	style := m.theme.Pane
	if m.focused {
		style = m.theme.PaneFocused
	}
	return style.Width(m.width - 2).Render(m.viewport.View())
}
