package views

import (
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/hedworth/mcp-studio-go/internal/tui/theme"
)

// ConfirmResult is sent when the user responds to the confirmation dialog.
type ConfirmResult struct {
	Confirmed bool
	Tag       string // Optional tag to identify which action was confirmed
}

// ConfirmModel is a reusable confirmation dialog component.
type ConfirmModel struct {
	theme   theme.Theme
	visible bool
	title   string
	message string
	tag     string // Optional tag to identify the action
	width   int
	height  int

	// Keys
	yesKey key.Binding
	noKey  key.Binding
	escKey key.Binding
}

// NewConfirm creates a new confirmation dialog.
func NewConfirm(th theme.Theme) ConfirmModel {
	return ConfirmModel{
		theme: th,
		yesKey: key.NewBinding(
			key.WithKeys("y", "Y"),
			key.WithHelp("y", "yes"),
		),
		noKey: key.NewBinding(
			key.WithKeys("n", "N"),
			key.WithHelp("n", "no"),
		),
		escKey: key.NewBinding(
			key.WithKeys("esc"),
			key.WithHelp("esc", "cancel"),
		),
	}
}

// Show displays the confirmation dialog with the given title and message.
func (m *ConfirmModel) Show(title, message, tag string) {
	m.visible = true
	m.title = title
	m.message = message
	m.tag = tag
}

// Hide hides the confirmation dialog.
func (m *ConfirmModel) Hide() {
	m.visible = false
}

// IsVisible returns whether the dialog is visible.
func (m ConfirmModel) IsVisible() bool {
	return m.visible
}

// SetSize sets the size of the dialog (for centering).
func (m *ConfirmModel) SetSize(width, height int) {
	m.width = width
	m.height = height
}

// Update handles key events for the confirmation dialog.
func (m ConfirmModel) Update(msg tea.Msg) (ConfirmModel, tea.Cmd) {
	if !m.visible {
		return m, nil
	}

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch {
		case key.Matches(msg, m.yesKey):
			m.visible = false
			return m, func() tea.Msg {
				return ConfirmResult{Confirmed: true, Tag: m.tag}
			}
		case key.Matches(msg, m.noKey), key.Matches(msg, m.escKey):
			m.visible = false
			return m, func() tea.Msg {
				return ConfirmResult{Confirmed: false, Tag: m.tag}
			}
		}
	}

	return m, nil
}

// View renders the confirmation dialog.
func (m ConfirmModel) View() string {
	if !m.visible {
		return ""
	}

	dialogWidth := 50
	if m.width > 0 && m.width < 60 {
		dialogWidth = m.width - 10
	}

	titleStyle := m.theme.Danger.Bold(true)
	if m.title == "" {
		m.title = "Confirm"
	}

	content := titleStyle.Render(m.title) + "\n\n" +
		m.message + "\n\n" +
		m.theme.Muted.Render("[y]es  [n]o")

	dialog := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(m.theme.Danger.GetForeground()).
		Padding(1, 2).
		Width(dialogWidth).
		Render(content)

	return dialog
}

// RenderOverlay renders the dialog as a centered overlay on top of the base content.
func (m ConfirmModel) RenderOverlay(base string, width, height int) string {
	if !m.visible {
		return base
	}

	dialog := m.View()

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
