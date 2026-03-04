package views

import (
	"strings"

	"github.com/Bigsy/mcpmu/internal/tui/theme"
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// AddMethodResult is sent when the user selects an add method or cancels.
type AddMethodResult struct {
	Method    string // "manual" or "registry"
	Submitted bool
}

// AddMethodModel is a selector overlay for choosing how to add a server.
type AddMethodModel struct {
	theme    theme.Theme
	visible  bool
	selected int // 0=manual, 1=registry
	width    int
	height   int

	upKey    key.Binding
	downKey  key.Binding
	enterKey key.Binding
	escKey   key.Binding
}

// NewAddMethod creates a new add method selector.
func NewAddMethod(th theme.Theme) AddMethodModel {
	return AddMethodModel{
		theme: th,
		upKey: key.NewBinding(
			key.WithKeys("up", "k"),
		),
		downKey: key.NewBinding(
			key.WithKeys("down", "j"),
		),
		enterKey: key.NewBinding(
			key.WithKeys("enter"),
		),
		escKey: key.NewBinding(
			key.WithKeys("esc"),
		),
	}
}

// Show displays the add method selector, resetting selection to Manual.
func (m *AddMethodModel) Show() {
	m.visible = true
	m.selected = 0
}

// Hide hides the add method selector.
func (m *AddMethodModel) Hide() {
	m.visible = false
}

// IsVisible returns whether the selector is visible.
func (m AddMethodModel) IsVisible() bool {
	return m.visible
}

// SetSize sets the available dimensions for centering.
func (m *AddMethodModel) SetSize(width, height int) {
	m.width = width
	m.height = height
}

// Update handles key events for the add method selector.
func (m AddMethodModel) Update(msg tea.Msg) (AddMethodModel, tea.Cmd) {
	if !m.visible {
		return m, nil
	}

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch {
		case key.Matches(msg, m.upKey):
			if m.selected > 0 {
				m.selected--
			}
		case key.Matches(msg, m.downKey):
			if m.selected < 1 {
				m.selected++
			}
		case key.Matches(msg, m.enterKey):
			m.visible = false
			method := "manual"
			if m.selected == 1 {
				method = "registry"
			}
			return m, func() tea.Msg {
				return AddMethodResult{Method: method, Submitted: true}
			}
		case key.Matches(msg, m.escKey):
			m.visible = false
			return m, func() tea.Msg {
				return AddMethodResult{Submitted: false}
			}
		}
	}

	return m, nil
}

// RenderOverlay renders the selector as a centered overlay on top of the base content.
func (m AddMethodModel) RenderOverlay(base string, width, height int) string {
	if !m.visible {
		return base
	}

	dialogWidth := 58

	title := m.theme.Title.Render("Add Server")

	// Render options
	options := []struct {
		label string
		desc  string
	}{
		{"Manual", "Enter command, URL, and env"},
		{"Official Registry", "Search & install from the MCP server registry"},
	}

	var optionContent strings.Builder
	for i, opt := range options {
		var labelStyle, descStyle lipgloss.Style
		if i == m.selected {
			labelStyle = m.theme.Primary.Bold(true)
			descStyle = m.theme.Muted
			optionContent.WriteString("  " + m.theme.Primary.Render("\u25b8") + " " + labelStyle.Render(opt.label) + "\n")
		} else {
			labelStyle = m.theme.Base
			descStyle = m.theme.Muted
			optionContent.WriteString("    " + labelStyle.Render(opt.label) + "\n")
		}
		optionContent.WriteString("    " + descStyle.Render(opt.desc) + "\n")
		if i < len(options)-1 {
			optionContent.WriteString("\n")
		}
	}

	footer := m.theme.Faint.Render("\u2191\u2193 select  enter confirm  esc \u00d7")

	content := title + "\n\n" + optionContent.String() + "\n" + footer

	dialog := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(m.theme.Primary.GetForeground()).
		Padding(1, 2).
		Width(dialogWidth).
		Render(content)

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
