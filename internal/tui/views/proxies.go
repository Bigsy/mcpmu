package views

import (
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/Bigsy/mcpmu/internal/tui/theme"
)

// ProxiesModel is a placeholder view for the Proxies tab (Phase 5).
type ProxiesModel struct {
	theme    theme.Theme
	viewport viewport.Model
	width    int
	height   int
	focused  bool
}

// NewProxies creates the Proxies placeholder view.
func NewProxies(th theme.Theme) ProxiesModel {
	vp := viewport.New(0, 0)
	return ProxiesModel{
		theme:    th,
		viewport: vp,
	}
}

// SetSize sets the dimensions.
func (m *ProxiesModel) SetSize(width, height int) {
	m.width = width
	m.height = height
	// Viewport gets: width minus borders (2) minus padding (2) = width - 4
	// Height: height minus borders (2) = height - 2
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
func (m *ProxiesModel) SetFocused(focused bool) {
	m.focused = focused
}

func (m *ProxiesModel) updateContent() {
	var content strings.Builder
	content.WriteString(m.theme.Title.Render("Proxies"))
	content.WriteString("\n\n")
	content.WriteString(m.theme.Muted.Render("Coming soon: proxy configurations for MCP servers."))
	content.WriteString("\n")
	content.WriteString(m.theme.Faint.Render("Planned for "))
	content.WriteString(m.theme.Primary.Render("Phase 5"))
	content.WriteString(m.theme.Faint.Render("."))
	content.WriteString("\n\n")
	content.WriteString(m.theme.Faint.Render("For now, use Servers and Namespaces to manage access."))
	m.viewport.SetContent(content.String())
}

// Init implements tea.Model.
func (m ProxiesModel) Init() tea.Cmd {
	return nil
}

// Update implements tea.Model.
func (m ProxiesModel) Update(msg tea.Msg) (ProxiesModel, tea.Cmd) {
	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	return m, cmd
}

// View implements tea.Model.
func (m ProxiesModel) View() string {
	style := m.theme.Pane
	if m.focused {
		style = m.theme.PaneFocused
	}
	return style.Width(m.width - 2).Render(m.viewport.View())
}

