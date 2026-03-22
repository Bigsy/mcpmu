package views

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/events"
	"github.com/Bigsy/mcpmu/internal/mcp"
	"github.com/Bigsy/mcpmu/internal/tui/theme"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ServerDetailModel displays detailed information about a server.
type ServerDetailModel struct {
	theme          theme.Theme
	serverName     string // Server name (map key)
	server         *config.ServerConfig
	status         *events.ServerStatus
	tools          []mcp.Tool
	toolTokens     map[string]int // toolName -> token count
	toolsFromCache bool           // true when tools were loaded from cache
	viewport       viewport.Model
	width          int
	height         int
	topPad         int
	focused        bool
}

// NewServerDetail creates a new server detail view.
func NewServerDetail(theme theme.Theme) ServerDetailModel {
	vp := viewport.New(0, 0)
	return ServerDetailModel{
		theme:    theme,
		viewport: vp,
	}
}

// SetServer sets the server to display.
func (m *ServerDetailModel) SetServer(name string, srv *config.ServerConfig, status *events.ServerStatus, tools []mcp.Tool, toolTokens map[string]int, toolsFromCache bool) {
	m.serverName = name
	m.server = srv
	m.status = status
	m.tools = tools
	m.toolTokens = toolTokens
	m.toolsFromCache = toolsFromCache
	m.updateContent()
}

// SetSize sets the dimensions.
func (m *ServerDetailModel) SetSize(width, height int) {
	m.width = width
	m.height = height
	// Viewport gets: width minus borders (2) minus padding (2) = width - 4
	// Height: height minus borders (2) = height - 2
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
func (m *ServerDetailModel) SetFocused(focused bool) {
	m.focused = focused
}

func (m *ServerDetailModel) updateContent() {
	if m.server == nil {
		m.viewport.SetContent("No server selected")
		return
	}

	var content strings.Builder

	// Header with status
	stateStr := "stopped"
	if m.status != nil {
		stateStr = m.status.State.String()
	}
	statusPill := m.theme.StatusPill(stateStr)

	content.WriteString(m.theme.Title.Render(m.serverName))
	content.WriteString("  ")
	content.WriteString(statusPill)
	content.WriteString("\n\n")

	// Server info
	infoStyle := m.theme.Muted
	labelStyle := m.theme.Base.Bold(true)

	if m.status != nil && m.status.PID > 0 {
		content.WriteString(labelStyle.Render("PID: "))
		content.WriteString(infoStyle.Render(fmt.Sprintf("%d", m.status.PID)))
		content.WriteString("   ")
	}

	if m.status != nil && m.status.StartedAt != nil {
		uptime := time.Since(*m.status.StartedAt).Round(time.Second)
		content.WriteString(labelStyle.Render("Uptime: "))
		content.WriteString(infoStyle.Render(formatDuration(uptime)))
	}
	content.WriteString("\n\n")

	// Server type-specific fields
	if m.server.IsHTTP() {
		m.renderHTTPInfo(&content, labelStyle, infoStyle)
	} else {
		m.renderStdioInfo(&content, labelStyle, infoStyle)
	}

	// Configuration
	content.WriteString(labelStyle.Render("Autostart: "))
	if m.server.Autostart {
		content.WriteString(infoStyle.Render("Yes"))
	} else {
		content.WriteString(m.theme.Faint.Render("No"))
	}
	content.WriteString("\n")

	content.WriteString(labelStyle.Render("Startup Timeout: "))
	content.WriteString(infoStyle.Render(fmt.Sprintf("%ds", m.server.StartupTimeout())))
	content.WriteString("\n")

	content.WriteString(labelStyle.Render("Tool Timeout: "))
	content.WriteString(infoStyle.Render(fmt.Sprintf("%ds", m.server.ToolTimeout())))
	content.WriteString("\n")

	// Working directory
	if m.server.Cwd != "" {
		content.WriteString(labelStyle.Render("Working Dir: "))
		content.WriteString(infoStyle.Render(m.server.Cwd))
		content.WriteString("\n")
	}

	// Environment
	if len(m.server.Env) > 0 {
		content.WriteString("\n")
		content.WriteString(labelStyle.Render("Environment:\n"))
		for k, v := range m.server.Env {
			content.WriteString("  ")
			content.WriteString(infoStyle.Render(k + "=" + v))
			content.WriteString("\n")
		}
	}

	// Tools section
	content.WriteString("\n")
	toolsHeader := fmt.Sprintf("Tools (%d)", len(m.tools))
	content.WriteString(m.theme.Title.Render(toolsHeader))
	if m.toolsFromCache {
		content.WriteString("  ")
		content.WriteString(m.theme.Faint.Render("(cached — start server to refresh)"))
	}
	content.WriteString("\n")

	if len(m.tools) == 0 {
		content.WriteString(m.theme.Faint.Render("  No tools discovered"))
		content.WriteString("\n")
	} else {
		toolBox := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.AdaptiveColor{Light: "#D0D7DE", Dark: "#3B4261"}).
			Padding(0, 1).
			Width(m.width - 8)

		var toolsContent strings.Builder
		for i, tool := range m.tools {
			if i > 0 {
				toolsContent.WriteString("\n")
			}
			toolsContent.WriteString(m.theme.Primary.Render(tool.Name))
			if tokens, ok := m.toolTokens[tool.Name]; ok {
				toolsContent.WriteString("  ")
				toolsContent.WriteString(m.theme.Faint.Render(fmt.Sprintf("~%d tokens", tokens)))
			}
			if tool.Description != "" {
				toolsContent.WriteString("\n  ")
				desc := tool.Description
				if len(desc) > 60 {
					desc = desc[:57] + "..."
				}
				toolsContent.WriteString(m.theme.Muted.Render(desc))
			}
		}
		content.WriteString(toolBox.Render(toolsContent.String()))
	}

	// Error info
	if m.status != nil && m.status.Error != "" {
		content.WriteString("\n\n")
		content.WriteString(m.theme.Danger.Bold(true).Render("Error: "))
		content.WriteString(m.theme.Danger.Render(m.status.Error))
	}

	// Last exit info
	if m.status != nil && m.status.LastExit != nil {
		content.WriteString("\n\n")
		content.WriteString(labelStyle.Render("Last Exit: "))
		exitInfo := fmt.Sprintf("code %d", m.status.LastExit.Code)
		if m.status.LastExit.Signal != "" {
			exitInfo += fmt.Sprintf(" (signal: %s)", m.status.LastExit.Signal)
		}
		exitInfo += fmt.Sprintf(" at %s", m.status.LastExit.Timestamp.Format("15:04:05"))
		content.WriteString(infoStyle.Render(exitInfo))
	}

	m.viewport.SetContent(content.String())
}

// Init implements tea.Model.
func (m ServerDetailModel) Init() tea.Cmd {
	return nil
}

// Update implements tea.Model.
func (m ServerDetailModel) Update(msg tea.Msg) (ServerDetailModel, tea.Cmd) {
	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	return m, cmd
}

// View implements tea.Model.
func (m ServerDetailModel) View() string {
	title := "Server"
	if m.serverName != "" {
		title = m.serverName
	}
	content := strings.TrimSuffix(m.viewport.View(), "\n")
	if m.topPad > 0 {
		content = strings.Repeat("\n", m.topPad) + content
	}
	return m.theme.RenderPane(title, content, m.width, m.focused)
}

func (m *ServerDetailModel) renderStdioInfo(content *strings.Builder, labelStyle, infoStyle lipgloss.Style) {
	content.WriteString(labelStyle.Render("Command: "))
	cmd := m.server.Command
	if len(m.server.Args) > 0 {
		cmd += " " + strings.Join(m.server.Args, " ")
	}
	content.WriteString(infoStyle.Render(cmd))
	content.WriteString("\n")
}

func (m *ServerDetailModel) renderHTTPInfo(content *strings.Builder, labelStyle, infoStyle lipgloss.Style) {
	// URL
	content.WriteString(labelStyle.Render("URL: "))
	content.WriteString(infoStyle.Render(m.server.URL))
	content.WriteString("\n")

	// Auth mode
	content.WriteString(labelStyle.Render("Auth: "))
	if m.server.BearerTokenEnvVar != "" {
		content.WriteString(infoStyle.Render("Bearer ($" + m.server.BearerTokenEnvVar + ")"))
		content.WriteString("\n")
	} else if m.server.OAuth != nil {
		content.WriteString(infoStyle.Render("OAuth"))
		content.WriteString("\n")
		if m.server.OAuth.ClientID != "" {
			content.WriteString("  ")
			content.WriteString(infoStyle.Render("Client: " + m.server.OAuth.ClientID))
		} else {
			content.WriteString("  ")
			content.WriteString(infoStyle.Render("Client: dynamic"))
		}
		content.WriteString("\n")
		if len(m.server.OAuth.Scopes) > 0 {
			content.WriteString("  ")
			content.WriteString(infoStyle.Render("Scopes: " + strings.Join(m.server.OAuth.Scopes, ", ")))
			content.WriteString("\n")
		}
		if m.server.OAuth.CallbackPort != nil {
			content.WriteString("  ")
			content.WriteString(infoStyle.Render(fmt.Sprintf("Port: %d", *m.server.OAuth.CallbackPort)))
			content.WriteString("\n")
		}
	} else {
		content.WriteString(m.theme.Faint.Render("none"))
		content.WriteString("\n")
	}

	// Static HTTP headers (show keys only, values could be sensitive)
	if len(m.server.HTTPHeaders) > 0 {
		content.WriteString(labelStyle.Render("Headers:"))
		content.WriteString("\n")
		keys := make([]string, 0, len(m.server.HTTPHeaders))
		for k := range m.server.HTTPHeaders {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			content.WriteString("  ")
			content.WriteString(infoStyle.Render(k))
			content.WriteString("\n")
		}
	}

	// Env-backed HTTP headers
	if len(m.server.EnvHTTPHeaders) > 0 {
		content.WriteString(labelStyle.Render("Env Headers:"))
		content.WriteString("\n")
		keys := make([]string, 0, len(m.server.EnvHTTPHeaders))
		for k := range m.server.EnvHTTPHeaders {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			content.WriteString("  ")
			content.WriteString(infoStyle.Render(k + " <- $" + m.server.EnvHTTPHeaders[k]))
			content.WriteString("\n")
		}
	}
}

func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm %ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
}
