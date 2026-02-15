package views

import (
	"fmt"
	"strings"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/tui/theme"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// NamespaceDetailModel displays detailed information about a namespace.
type NamespaceDetailModel struct {
	theme         theme.Theme
	namespaceName string // Namespace name (map key)
	namespace     *config.NamespaceConfig
	isDefault     bool
	// All servers for assignment display
	allServers []config.ServerEntry
	// Tool permissions for this namespace
	permissions  []config.ToolPermission
	serverTokens map[string]int // serverID -> enabled token count
	totalTokens  int
	hasCache     bool
	viewport     viewport.Model
	width        int
	height       int
	topPad       int
	focused      bool
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
func (m *NamespaceDetailModel) SetNamespace(name string, ns *config.NamespaceConfig, isDefault bool, allServers []config.ServerEntry, permissions []config.ToolPermission, serverTokens map[string]int) {
	m.namespaceName = name
	m.namespace = ns
	m.isDefault = isDefault
	m.allServers = allServers
	m.permissions = permissions
	m.serverTokens = serverTokens
	m.totalTokens = 0
	for _, n := range serverTokens {
		m.totalTokens += n
	}
	m.hasCache = len(serverTokens) > 0
	m.updateContent()
}

// SetSize sets the dimensions.
func (m *NamespaceDetailModel) SetSize(width, height int) {
	m.width = width
	m.height = height
	m.viewport.Width = width - 4
	m.topPad = paneTopPaddingLines(height)
	m.viewport.Height = height - 2 - m.topPad
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
	content.WriteString(m.theme.Title.Render(m.namespaceName))
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
	content.WriteString("\n")

	// Estimated tokens
	content.WriteString(labelStyle.Render("Estimated Tokens: "))
	if m.hasCache {
		content.WriteString(m.theme.Primary.Render(formatTokenCount(m.totalTokens)))
		content.WriteString(m.theme.Faint.Render(" (enabled tools)"))
	} else {
		content.WriteString(m.theme.Faint.Render("Unknown - start servers to discover tools"))
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
		for i, serverName := range m.namespace.ServerIDs {
			if i > 0 {
				serversContent.WriteString("\n")
			}
			serversContent.WriteString(m.theme.Primary.Render(serverName))
			if tokens, ok := m.serverTokens[serverName]; ok {
				serversContent.WriteString("  ")
				serversContent.WriteString(m.theme.Muted.Render(fmt.Sprintf("(%d tokens)", tokens)))
			} else {
				serversContent.WriteString("  ")
				serversContent.WriteString(m.theme.Faint.Render("(tokens unknown)"))
			}
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
			serverPerms[perm.Server] = append(serverPerms[perm.Server], perm)
		}

		first := true
		for serverName, perms := range serverPerms {
			if !first {
				permsContent.WriteString("\n\n")
			}
			first = false

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
	title := "Namespace"
	if m.namespaceName != "" {
		title = m.namespaceName
	}
	content := strings.TrimSuffix(m.viewport.View(), "\n")
	if m.topPad > 0 {
		content = strings.Repeat("\n", m.topPad) + content
	}
	return m.theme.RenderPane(title, content, m.width, m.focused)
}
