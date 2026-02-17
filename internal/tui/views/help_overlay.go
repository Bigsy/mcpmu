package views

import (
	"fmt"
	"strings"

	"github.com/Bigsy/mcpmu/internal/tui/theme"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// HelpOverlayModel is the help overlay component.
type HelpOverlayModel struct {
	theme    theme.Theme
	viewport viewport.Model
	visible  bool
	width    int
	height   int
	ready    bool
}

// NewHelpOverlay creates a new help overlay.
func NewHelpOverlay(th theme.Theme) HelpOverlayModel {
	return HelpOverlayModel{
		theme: th,
	}
}

// SetSize sets the dimensions for the overlay.
func (m *HelpOverlayModel) SetSize(width, height int) {
	m.width = width
	m.height = height

	// Calculate viewport size (dialog is ~60 wide, leave room for borders)
	vpWidth := 54
	vpHeight := max(
		// Leave room for title, footer, borders
		height-10, 5)

	if !m.ready {
		m.viewport = viewport.New(vpWidth, vpHeight)
		m.viewport.SetContent(m.helpContent())
		m.ready = true
	} else {
		m.viewport.Width = vpWidth
		m.viewport.Height = vpHeight
	}
}

// Toggle toggles the visibility of the help overlay.
func (m *HelpOverlayModel) Toggle() {
	m.visible = !m.visible
	if m.visible {
		m.viewport.GotoTop()
	}
}

// IsVisible returns whether the help overlay is visible.
func (m HelpOverlayModel) IsVisible() bool {
	return m.visible
}

// SetVisible sets the visibility of the help overlay.
func (m *HelpOverlayModel) SetVisible(visible bool) {
	m.visible = visible
	if visible {
		m.viewport.GotoTop()
	}
}

// Init implements tea.Model.
func (m HelpOverlayModel) Init() tea.Cmd {
	return nil
}

// Update implements tea.Model.
func (m HelpOverlayModel) Update(msg tea.Msg) (HelpOverlayModel, tea.Cmd) {
	if !m.visible {
		return m, nil
	}

	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	return m, cmd
}

func (m HelpOverlayModel) helpContent() string {
	sections := []string{
		m.renderSection("Navigation", [][]string{
			{"j / ↓", "Move down"},
			{"k / ↑", "Move up"},
			{"g", "Go to top"},
			{"G", "Go to bottom"},
			{"Enter", "View details"},
			{"Esc", "Go back / close"},
		}),
		m.renderSection("Server Actions", [][]string{
			{"t", "Test server (start/stop)"},
			{"E", "Enable/disable server"},
			{"a", "Add new server"},
			{"e", "Edit server"},
			{"d", "Delete server"},
			{"L", "OAuth login (HTTP servers)"},
			{"O", "OAuth logout (HTTP servers)"},
		}),
		m.renderSection("Logs", [][]string{
			{"l", "Toggle log panel"},
			{"f", "Toggle follow mode"},
		}),
		m.renderSection("Tabs", [][]string{
			{"Tab", "Next tab"},
			{"Shift+Tab", "Previous tab"},
			{"1", "Servers tab"},
			{"2", "Namespaces tab"},
			{"3", "Proxies tab (planned)"},
		}),
		m.renderSection("General", [][]string{
			{"?", "Toggle this help"},
			{"q", "Quit"},
			{"Ctrl+C", "Force quit"},
		}),
	}

	return strings.Join(sections, "\n")
}

func (m HelpOverlayModel) renderSection(title string, bindings [][]string) string {
	var sb strings.Builder

	sb.WriteString(m.theme.Primary.Bold(true).Render(title))
	sb.WriteString("\n")

	for _, binding := range bindings {
		key := m.theme.Primary.Render(binding[0])
		desc := m.theme.Base.Render(binding[1])

		// Right-align keys in a 12-char column
		keyWidth := 12
		padding := max(keyWidth-len(binding[0]), 0)

		sb.WriteString(strings.Repeat(" ", padding))
		sb.WriteString(key)
		sb.WriteString("  ")
		sb.WriteString(desc)
		sb.WriteString("\n")
	}

	return sb.String()
}

// RenderOverlay renders the help overlay on top of the base content.
func (m HelpOverlayModel) RenderOverlay(base string, width, height int) string {
	if !m.visible {
		return base
	}

	// Make sure viewport is initialized
	if !m.ready {
		return base
	}

	title := m.theme.Title.Render("Keyboard Shortcuts")

	// Scroll indicator
	scrollInfo := ""
	if m.viewport.TotalLineCount() > m.viewport.Height {
		pct := m.viewport.ScrollPercent() * 100
		scrollInfo = m.theme.Faint.Render(fmt.Sprintf(" (j/k to scroll, %.0f%%)", pct))
	}

	footer := m.theme.Faint.Render("Press ? or Esc to close")

	content := title + scrollInfo + "\n\n" + m.viewport.View() + "\n\n" + footer

	// Create dialog box
	dialogStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(m.theme.Primary.GetForeground()).
		Padding(1, 2).
		Width(60)

	dialog := dialogStyle.Render(content)

	// Center the dialog
	return lipgloss.Place(
		width,
		height,
		lipgloss.Center,
		lipgloss.Center,
		dialog,
		lipgloss.WithWhitespaceChars(" "),
		lipgloss.WithWhitespaceForeground(lipgloss.AdaptiveColor{Light: "#E5E7EB", Dark: "#1F2937"}),
	)
}
