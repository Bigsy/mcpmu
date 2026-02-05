package views

import (
	"time"

	"github.com/Bigsy/mcpmu/internal/tui/theme"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ToastLevel represents the severity of a toast notification.
type ToastLevel int

const (
	ToastInfo ToastLevel = iota
	ToastSuccess
	ToastWarn
	ToastError
)

// toastClearMsg is sent when a toast should be cleared.
type toastClearMsg struct {
	id int
}

// ToastModel displays transient notifications.
type ToastModel struct {
	theme   theme.Theme
	message string
	level   ToastLevel
	visible bool
	id      int // Used to identify which toast to clear
	width   int
	height  int
}

// NewToast creates a new toast model.
func NewToast(th theme.Theme) ToastModel {
	return ToastModel{
		theme: th,
	}
}

// Show displays a toast with the given message and level.
// Returns a command that will clear the toast after the appropriate duration.
func (m *ToastModel) Show(message string, level ToastLevel) tea.Cmd {
	m.id++
	m.message = message
	m.level = level
	m.visible = true

	// Determine duration based on level
	duration := 3 * time.Second
	if level == ToastWarn || level == ToastError {
		duration = 5 * time.Second
	}

	currentID := m.id
	return tea.Tick(duration, func(t time.Time) tea.Msg {
		return toastClearMsg{id: currentID}
	})
}

// ShowInfo shows an info toast.
func (m *ToastModel) ShowInfo(message string) tea.Cmd {
	return m.Show(message, ToastInfo)
}

// ShowSuccess shows a success toast.
func (m *ToastModel) ShowSuccess(message string) tea.Cmd {
	return m.Show(message, ToastSuccess)
}

// ShowWarn shows a warning toast.
func (m *ToastModel) ShowWarn(message string) tea.Cmd {
	return m.Show(message, ToastWarn)
}

// ShowError shows an error toast.
func (m *ToastModel) ShowError(message string) tea.Cmd {
	return m.Show(message, ToastError)
}

// Hide hides the toast immediately.
func (m *ToastModel) Hide() {
	m.visible = false
}

// IsVisible returns whether the toast is visible.
func (m ToastModel) IsVisible() bool {
	return m.visible
}

// SetSize sets the available size for positioning.
func (m *ToastModel) SetSize(width, height int) {
	m.width = width
	m.height = height
}

// Update handles messages for the toast.
func (m ToastModel) Update(msg tea.Msg) (ToastModel, tea.Cmd) {
	switch msg := msg.(type) {
	case toastClearMsg:
		// Only clear if this is the current toast
		if msg.id == m.id {
			m.visible = false
		}
	case tea.KeyMsg:
		// Any key dismisses the toast early
		if m.visible {
			m.visible = false
		}
	}
	return m, nil
}

// View renders the toast.
func (m ToastModel) View() string {
	if !m.visible || m.message == "" {
		return ""
	}

	var style lipgloss.Style
	var icon string

	switch m.level {
	case ToastSuccess:
		style = m.theme.ToastInfo // Green/success style
		icon = "✓ "
	case ToastWarn:
		style = m.theme.ToastWarn
		icon = "⚠ "
	case ToastError:
		style = m.theme.ToastErr
		icon = "✖ "
	default: // ToastInfo
		style = m.theme.ToastInfo
		icon = "ℹ "
	}

	return style.Render(icon + m.message)
}

// ViewWithMaxWidth renders the toast, truncating the message if needed so the
// rendered string fits within maxWidth.
func (m ToastModel) ViewWithMaxWidth(maxWidth int) string {
	if !m.visible || m.message == "" {
		return ""
	}

	var style lipgloss.Style
	var icon string

	switch m.level {
	case ToastSuccess:
		style = m.theme.ToastInfo // Green/success style
		icon = "✓ "
	case ToastWarn:
		style = m.theme.ToastWarn
		icon = "⚠ "
	case ToastError:
		style = m.theme.ToastErr
		icon = "✖ "
	default: // ToastInfo
		style = m.theme.ToastInfo
		icon = "ℹ "
	}

	// Fast path: no width constraint.
	if maxWidth <= 0 {
		return style.Render(icon + m.message)
	}

	// Render and check if it already fits.
	rendered := style.Render(icon + m.message)
	if lipgloss.Width(rendered) <= maxWidth {
		return rendered
	}

	// The toast styles include 1-char padding on both sides.
	// We'll conservatively reserve 2 columns for that padding.
	availableText := maxWidth - 2
	if availableText < 0 {
		availableText = 0
	}

	availableMsg := availableText - lipgloss.Width(icon)
	if availableMsg < 0 {
		availableMsg = 0
	}

	msg := m.message
	if lipgloss.Width(msg) > availableMsg {
		if availableMsg <= 3 {
			msg = msg[:min(len(msg), availableMsg)]
		} else {
			msg = msg[:min(len(msg), availableMsg-3)] + "..."
		}
	}

	return style.Render(icon + msg)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// RenderOverlay renders the toast positioned above the status bar.
func (m ToastModel) RenderOverlay(base string, width, height int) string {
	if !m.visible {
		return base
	}

	toast := m.View()
	if toast == "" {
		return base
	}

	// Position toast at bottom-right, just above status bar
	return lipgloss.Place(
		width,
		height-1, // Leave room for status bar
		lipgloss.Right,
		lipgloss.Bottom,
		toast,
		lipgloss.WithWhitespaceChars(" "),
	)
}
