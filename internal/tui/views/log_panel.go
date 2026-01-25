package views

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/hedworth/mcp-studio-go/internal/tui/theme"
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
	visible  bool
	width    int
	height   int
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
	// Height: height minus borders (2) minus header line (1) = height - 3
	m.viewport.Width = width - 4
	m.viewport.Height = height - 3
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

		// Timestamp
		ts := entry.Timestamp.Format("15:04:05")
		content.WriteString(m.theme.Faint.Render(ts))
		content.WriteString(" ")

		// Server ID
		serverTag := fmt.Sprintf("[%s]", entry.ServerID)
		content.WriteString(m.theme.Primary.Render(serverTag))
		content.WriteString(" ")

		// Log line (colorize errors)
		line := entry.Line
		if strings.Contains(strings.ToLower(line), "error") ||
			strings.Contains(strings.ToLower(line), "err:") {
			content.WriteString(m.theme.Danger.Render(line))
		} else if strings.Contains(strings.ToLower(line), "warn") {
			content.WriteString(m.theme.Warn.Render(line))
		} else {
			content.WriteString(m.theme.Base.Render(line))
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

	style := m.theme.Pane
	if m.focused {
		style = m.theme.PaneFocused
	}

	// Title with follow indicator
	title := "Logs"
	if m.follow {
		title += " " + m.theme.Success.Render("[f]ollow")
	} else {
		title += " " + m.theme.Faint.Render("[f]ollow")
	}

	header := m.theme.Title.Render(title) + "\n"
	content := header + m.viewport.View()

	// Width is content width; borders are outside this
	return style.Width(m.width - 2).Render(content)
}
