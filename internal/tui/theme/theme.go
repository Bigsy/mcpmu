// Package theme provides the visual theme for the TUI.
package theme

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Theme holds all the styles used in the TUI.
type Theme struct {
	// Text styles
	Base  lipgloss.Style
	Muted lipgloss.Style
	Faint lipgloss.Style
	Title lipgloss.Style

	// Accent colors
	Primary lipgloss.Style
	Success lipgloss.Style
	Warn    lipgloss.Style
	Danger  lipgloss.Style

	// Layout chrome
	App         lipgloss.Style
	Pane        lipgloss.Style
	PaneFocused lipgloss.Style

	// Tabs
	Tabs      lipgloss.Style
	Tab       lipgloss.Style
	TabActive lipgloss.Style

	// Lists
	Item         lipgloss.Style
	ItemSelected lipgloss.Style
	ItemDim      lipgloss.Style

	// Status bar
	StatusBar lipgloss.Style

	// Toasts (Phase 2)
	ToastInfo lipgloss.Style
	ToastWarn lipgloss.Style
	ToastErr  lipgloss.Style
}

// New creates the default theme (orange accent).
func New() Theme {
	primary := lipgloss.AdaptiveColor{Light: "#EA580C", Dark: "#FB923C"} // Orange
	success := lipgloss.AdaptiveColor{Light: "#0F7B0F", Dark: "#9ECE6A"}
	warn := lipgloss.AdaptiveColor{Light: "#B45309", Dark: "#FBBF24"}
	danger := lipgloss.AdaptiveColor{Light: "#B00020", Dark: "#F7768E"}
	border := lipgloss.AdaptiveColor{Light: "#D0D7DE", Dark: "#3B4261"}
	muted := lipgloss.AdaptiveColor{Light: "#6B7280", Dark: "#A9B1D6"}
	faint := lipgloss.AdaptiveColor{Light: "#9CA3AF", Dark: "#565F89"}

	return Theme{
		Base:  lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#111827", Dark: "#C0CAF5"}),
		Muted: lipgloss.NewStyle().Foreground(muted),
		Faint: lipgloss.NewStyle().Foreground(faint),
		Title: lipgloss.NewStyle().Bold(true),

		Primary: lipgloss.NewStyle().Foreground(primary),
		Success: lipgloss.NewStyle().Foreground(success),
		Warn:    lipgloss.NewStyle().Foreground(warn),
		Danger:  lipgloss.NewStyle().Foreground(danger),

		App: lipgloss.NewStyle().Padding(0, 1),

		Pane: lipgloss.NewStyle().
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(border).
			Padding(0, 1),

		PaneFocused: lipgloss.NewStyle().
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(primary).
			Padding(0, 1),

		Tabs: lipgloss.NewStyle().Padding(0, 1),
		Tab: lipgloss.NewStyle().
			Padding(0, 1).
			Foreground(lipgloss.AdaptiveColor{Light: "#374151", Dark: "#A9B1D6"}),
		TabActive: lipgloss.NewStyle().
			Padding(0, 1).
			Bold(true).
			Foreground(primary).
			Underline(true),

		Item:         lipgloss.NewStyle().Padding(0, 1),
		ItemSelected: lipgloss.NewStyle().Padding(0, 1).Bold(true).Foreground(primary),
		ItemDim:      lipgloss.NewStyle().Padding(0, 1).Foreground(faint),

		StatusBar: lipgloss.NewStyle().
			Padding(0, 1).
			Foreground(muted),

		ToastInfo: lipgloss.NewStyle().Padding(0, 1).Foreground(lipgloss.Color("#FFFFFF")).Background(lipgloss.Color("#EA580C")),
		ToastWarn: lipgloss.NewStyle().Padding(0, 1).Foreground(lipgloss.Color("#111827")).Background(lipgloss.Color("#F59E0B")),
		ToastErr:  lipgloss.NewStyle().Padding(0, 1).Foreground(lipgloss.Color("#FFFFFF")).Background(lipgloss.Color("#DC2626")),
	}
}

// Warning returns the warning style (alias for Warn).
func (t Theme) Warning() lipgloss.Style {
	return t.Warn
}

// Error returns the error style (alias for Danger).
func (t Theme) Error() lipgloss.Style {
	return t.Danger
}

// StatusIcon returns the appropriate icon for a server state.
func (t Theme) StatusIcon(running bool, hasError bool) string {
	if hasError {
		return t.Danger.Render("✖")
	}
	if running {
		return t.Success.Render("●")
	}
	return t.Faint.Render("○")
}

// StatusPill renders a status pill with background color.
func (t Theme) StatusPill(state string) string {
	pill := lipgloss.NewStyle().Padding(0, 1).Bold(true)
	switch state {
	case "running":
		return pill.Background(lipgloss.Color("#14532D")).
			Foreground(lipgloss.Color("#DCFCE7")).Render("● RUN")
	case "idle", "stopped":
		return pill.Background(lipgloss.Color("#374151")).
			Foreground(lipgloss.Color("#E5E7EB")).Render("○ STOP")
	case "starting", "stopping":
		return pill.Background(lipgloss.Color("#713F12")).
			Foreground(lipgloss.Color("#FEF3C7")).Render("◐ ...")
	case "error", "crashed":
		return pill.Background(lipgloss.Color("#7F1D1D")).
			Foreground(lipgloss.Color("#FEE2E2")).Render("✖ ERR")
	default:
		return pill.Background(lipgloss.Color("#374151")).
			Foreground(lipgloss.Color("#E5E7EB")).Render("○ " + state)
	}
}

// StatusPillAnimated renders a status pill with animated spinner for transitional states.
func (t Theme) StatusPillAnimated(state string, spinnerFrame string) string {
	pill := lipgloss.NewStyle().Padding(0, 1).Bold(true)
	switch state {
	case "starting":
		return pill.Background(lipgloss.Color("#713F12")).
			Foreground(lipgloss.Color("#FEF3C7")).Render(spinnerFrame + " ...")
	case "stopping":
		return pill.Background(lipgloss.Color("#713F12")).
			Foreground(lipgloss.Color("#FEF3C7")).Render(spinnerFrame + " ...")
	default:
		return t.StatusPill(state)
	}
}

// RenderPane renders content in a pane with btop-style header (title embedded in border).
// Example output:
//
//	╭─┤ Servers ├──────────────────────────────╮
//	│ content here                             │
//	╰──────────────────────────────────────────╯
func (t Theme) RenderPane(title, content string, width int, focused bool) string {
	// Guard against very small widths that would cause panics
	if width < 10 {
		width = 10
	}

	borderColor := lipgloss.AdaptiveColor{Light: "#D0D7DE", Dark: "#3B4261"}
	if focused {
		borderColor = lipgloss.AdaptiveColor{Light: "#EA580C", Dark: "#FB923C"}
	}

	borderStyle := lipgloss.NewStyle().Foreground(borderColor)
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(borderColor)

	// Calculate widths (account for box drawing characters and padding)
	contentWidth := width - 4 // 2 for borders, 2 for padding
	if contentWidth < 1 {
		contentWidth = 1
	}

	// Build header: ╭─┤ Title ├───...───╮
	titleText := titleStyle.Render(title)
	titleWidth := lipgloss.Width(titleText)
	// Header structure: "╭─┤ " + title + " ├" + "─" * rest + "╮"
	// Total = 4 + titleWidth + 2 + rest + 1 = width
	// rest = width - titleWidth - 7
	restWidth := width - titleWidth - 7
	if restWidth < 0 {
		restWidth = 0
	}

	header := borderStyle.Render("╭─┤ ") + titleText + borderStyle.Render(" ├"+strings.Repeat("─", restWidth)+"╮")

	// Build content with side borders
	lines := strings.Split(content, "\n")
	var body strings.Builder
	for _, line := range lines {
		lineWidth := lipgloss.Width(line)
		padding := contentWidth - lineWidth
		if padding < 0 {
			padding = 0
		}
		body.WriteString(borderStyle.Render("│ "))
		body.WriteString(line)
		body.WriteString(strings.Repeat(" ", padding))
		body.WriteString(borderStyle.Render(" │"))
		body.WriteString("\n")
	}

	// Build footer: ╰───...───╯
	footerWidth := width - 2
	if footerWidth < 0 {
		footerWidth = 0
	}
	footer := borderStyle.Render("╰" + strings.Repeat("─", footerWidth) + "╯")

	return header + "\n" + body.String() + footer
}
