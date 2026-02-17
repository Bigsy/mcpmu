package views

import (
	"fmt"
	"strings"
	"time"

	"github.com/Bigsy/mcpmu/internal/tui/theme"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
)

// LogEntry represents a single log line.
type LogEntry struct {
	ServerID  string
	Line      string
	Timestamp time.Time
}

// LogPanelModel displays server logs.
type LogPanelModel struct {
	theme    theme.Theme
	viewport viewport.Model
	entries  []LogEntry
	follow   bool
	wrap     bool
	visible  bool
	width    int
	height   int
	topPad   int
	focused  bool
}

// NewLogPanel creates a new log panel.
func NewLogPanel(theme theme.Theme) LogPanelModel {
	vp := viewport.New(0, 0)
	return LogPanelModel{
		theme:    theme,
		viewport: vp,
		entries:  make([]LogEntry, 0, 1000),
		follow:   true,
	}
}

// SetSize sets the dimensions.
func (m *LogPanelModel) SetSize(width, height int) {
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

// SetVisible sets whether the panel is visible.
func (m *LogPanelModel) SetVisible(visible bool) {
	m.visible = visible
}

// IsVisible returns whether the panel is visible.
func (m LogPanelModel) IsVisible() bool {
	return m.visible
}

// SetFocused sets whether the panel is focused.
func (m *LogPanelModel) SetFocused(focused bool) {
	m.focused = focused
}

// IsFocused returns whether the panel is focused.
func (m LogPanelModel) IsFocused() bool {
	return m.focused
}

// ToggleFollow toggles follow mode.
func (m *LogPanelModel) ToggleFollow() {
	m.follow = !m.follow
	if m.follow {
		m.viewport.GotoBottom()
	}
}

// IsFollowing returns whether follow mode is active.
func (m LogPanelModel) IsFollowing() bool {
	return m.follow
}

// ToggleWrap toggles line wrapping.
func (m *LogPanelModel) ToggleWrap() {
	m.wrap = !m.wrap
	m.updateContent()
}

// IsWrapping returns whether wrap mode is active.
func (m LogPanelModel) IsWrapping() bool {
	return m.wrap
}

// AppendLog adds a log entry.
func (m *LogPanelModel) AppendLog(serverID, line string) {
	entry := LogEntry{
		ServerID:  serverID,
		Line:      line,
		Timestamp: time.Now(),
	}
	m.entries = append(m.entries, entry)

	// Keep only last 1000 entries
	if len(m.entries) > 1000 {
		m.entries = m.entries[len(m.entries)-1000:]
	}

	m.updateContent()

	if m.follow {
		m.viewport.GotoBottom()
	}
}

// Clear clears all log entries.
func (m *LogPanelModel) Clear() {
	m.entries = m.entries[:0]
	m.updateContent()
}

// wrapText wraps text to fit within the specified width, breaking at word
// boundaries when possible and hard-breaking very long words.
func wrapText(text string, width int) []string {
	if width <= 0 {
		return []string{text}
	}
	if len(text) == 0 {
		return []string{""}
	}

	var lines []string
	words := strings.Fields(text)
	if len(words) == 0 {
		return []string{""}
	}

	var currentLine strings.Builder
	currentLen := 0

	for _, word := range words {
		wordLen := len(word)

		// If word itself is longer than width, hard-break it
		if wordLen > width {
			// Flush current line if not empty
			if currentLen > 0 {
				lines = append(lines, currentLine.String())
				currentLine.Reset()
				currentLen = 0
			}
			// Break the long word into chunks
			for len(word) > width {
				lines = append(lines, word[:width])
				word = word[width:]
			}
			if len(word) > 0 {
				currentLine.WriteString(word)
				currentLen = len(word)
			}
			continue
		}

		// Check if word fits on current line
		if currentLen == 0 {
			currentLine.WriteString(word)
			currentLen = wordLen
		} else if currentLen+1+wordLen <= width {
			currentLine.WriteString(" ")
			currentLine.WriteString(word)
			currentLen += 1 + wordLen
		} else {
			// Start new line
			lines = append(lines, currentLine.String())
			currentLine.Reset()
			currentLine.WriteString(word)
			currentLen = wordLen
		}
	}

	// Flush remaining content
	if currentLen > 0 {
		lines = append(lines, currentLine.String())
	}

	return lines
}

func (m *LogPanelModel) updateContent() {
	if len(m.entries) == 0 {
		m.viewport.SetContent(m.theme.Faint.Render("No logs yet..."))
		return
	}

	var content strings.Builder
	for i, entry := range m.entries {
		if i > 0 {
			content.WriteString("\n")
		}

		// Build the prefix: "HH:MM:SS [serverID] "
		ts := entry.Timestamp.Format("15:04:05")
		serverTag := fmt.Sprintf("[%s]", entry.ServerID)

		// Determine style based on log content
		line := entry.Line
		isError := strings.Contains(strings.ToLower(line), "error") ||
			strings.Contains(strings.ToLower(line), "err:")
		isWarn := strings.Contains(strings.ToLower(line), "warn")

		if m.wrap {
			// Calculate prefix width (timestamp + space + serverTag + space)
			prefixWidth := len(ts) + 1 + len(serverTag) + 1

			// Calculate available width for log content
			contentWidth := max(m.viewport.Width-prefixWidth, 10)

			// Wrap the log line
			wrappedLines := wrapText(entry.Line, contentWidth)
			indent := strings.Repeat(" ", prefixWidth)

			for j, wrappedLine := range wrappedLines {
				if j > 0 {
					content.WriteString("\n")
				}

				if j == 0 {
					// First line gets the prefix
					content.WriteString(m.theme.Faint.Render(ts))
					content.WriteString(" ")
					content.WriteString(m.theme.Primary.Render(serverTag))
					content.WriteString(" ")
				} else {
					// Continuation lines get indent
					content.WriteString(indent)
				}

				// Apply appropriate style to the log content
				if isError {
					content.WriteString(m.theme.Danger.Render(wrappedLine))
				} else if isWarn {
					content.WriteString(m.theme.Warn.Render(wrappedLine))
				} else {
					content.WriteString(m.theme.Base.Render(wrappedLine))
				}
			}
		} else {
			// No wrapping - render single line
			content.WriteString(m.theme.Faint.Render(ts))
			content.WriteString(" ")
			content.WriteString(m.theme.Primary.Render(serverTag))
			content.WriteString(" ")

			if isError {
				content.WriteString(m.theme.Danger.Render(line))
			} else if isWarn {
				content.WriteString(m.theme.Warn.Render(line))
			} else {
				content.WriteString(m.theme.Base.Render(line))
			}
		}
	}

	m.viewport.SetContent(content.String())
}

// Init implements tea.Model.
func (m LogPanelModel) Init() tea.Cmd {
	return nil
}

// Update implements tea.Model.
func (m LogPanelModel) Update(msg tea.Msg) (LogPanelModel, tea.Cmd) {
	if !m.visible {
		return m, nil
	}

	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	return m, cmd
}

// View implements tea.Model.
func (m LogPanelModel) View() string {
	if !m.visible {
		return ""
	}

	// Title with status and keybinding hints
	title := "Logs"

	// Show current states
	if m.follow {
		title += " [FOLLOW]"
	}
	if m.wrap {
		title += " [WRAP]"
	}

	// Show keybinding hints
	title += "  f:follow w:wrap"

	content := strings.TrimSuffix(m.viewport.View(), "\n")
	if m.topPad > 0 {
		content = strings.Repeat("\n", m.topPad) + content
	}
	return m.theme.RenderPane(title, content, m.width, m.focused)
}
