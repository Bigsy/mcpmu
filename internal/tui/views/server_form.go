package views

import (
	"strings"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/tui/theme"
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
)

// ServerFormResult is sent when the user completes or cancels the form.
type ServerFormResult struct {
	Name         string // Server name (map key)
	OriginalName string // Original name (for rename detection in edit mode)
	Server       config.ServerConfig
	Submitted    bool
	IsEdit       bool // true if editing an existing server
}

// ServerFormModel is a form for adding/editing servers.
type ServerFormModel struct {
	theme   theme.Theme
	visible bool
	isEdit  bool // true when editing existing server
	width   int
	height  int

	// Form state
	form *huh.Form

	// Original server config (for edit mode - preserves fields not in form)
	originalServer *config.ServerConfig
	originalName   string // Original name for edit mode (to detect rename)

	// Form field values
	name              string
	commandOrURL      string // Auto-detected: http(s):// = HTTP server, else = stdio command
	args              string // Only used for stdio
	cwd               string
	env               string
	bearerTokenEnvVar string // Only used for HTTP

	// Initial values for dirty checking
	initialName              string
	initialCommandOrURL      string
	initialArgs              string
	initialCwd               string
	initialEnv               string
	initialBearerTokenEnvVar string

	// Confirm discard state
	showConfirmDiscard bool

	// Key bindings
	escKey key.Binding
}

// NewServerForm creates a new server form.
func NewServerForm(th theme.Theme) ServerFormModel {
	return ServerFormModel{
		theme: th,
		escKey: key.NewBinding(
			key.WithKeys("esc"),
			key.WithHelp("esc", "cancel"),
		),
	}
}

// ShowAdd displays the form for adding a new server.
// Returns a tea.Cmd to initialize the form.
func (m *ServerFormModel) ShowAdd() tea.Cmd {
	m.visible = true
	m.isEdit = false
	m.showConfirmDiscard = false
	m.originalServer = nil
	m.originalName = ""
	m.name = ""
	m.commandOrURL = ""
	m.args = ""
	m.cwd = ""
	m.env = ""
	m.bearerTokenEnvVar = ""
	// Save initial values for dirty checking
	m.initialName = ""
	m.initialCommandOrURL = ""
	m.initialArgs = ""
	m.initialCwd = ""
	m.initialEnv = ""
	m.initialBearerTokenEnvVar = ""
	m.buildForm()
	return m.form.Init()
}

// ShowEdit displays the form for editing an existing server.
// Returns a tea.Cmd to initialize the form.
func (m *ServerFormModel) ShowEdit(name string, srv config.ServerConfig) tea.Cmd {
	m.visible = true
	m.isEdit = true
	m.showConfirmDiscard = false
	m.originalServer = &srv // Preserve original for non-form fields
	m.originalName = name   // Remember original name for rename detection
	m.name = name

	// Determine if this is an HTTP or stdio server and populate accordingly
	if srv.URL != "" {
		m.commandOrURL = srv.URL
		m.args = ""
		m.bearerTokenEnvVar = srv.BearerTokenEnvVar
	} else {
		m.commandOrURL = srv.Command
		m.args = formatArgs(srv.Args) // Properly quote args with spaces
		m.bearerTokenEnvVar = ""
	}
	m.cwd = srv.Cwd
	m.env = formatEnvVars(srv.Env)

	// Save initial values for dirty checking
	m.initialName = m.name
	m.initialCommandOrURL = m.commandOrURL
	m.initialArgs = m.args
	m.initialCwd = m.cwd
	m.initialEnv = m.env
	m.initialBearerTokenEnvVar = m.bearerTokenEnvVar
	m.buildForm()
	return m.form.Init()
}

func (m *ServerFormModel) buildForm() {
	// Custom keymap to add arrow key navigation
	keymap := huh.NewDefaultKeyMap()
	// Add up/down arrow navigation to Input fields
	keymap.Input.Prev.SetKeys("up", "shift+tab")
	keymap.Input.Next.SetKeys("down", "tab")
	// Add up/down arrow navigation to Text fields
	keymap.Text.Prev.SetKeys("up", "shift+tab")
	keymap.Text.Next.SetKeys("down", "tab")
	// Add up/down arrow navigation to Confirm fields
	keymap.Confirm.Prev.SetKeys("up", "shift+tab")
	keymap.Confirm.Next.SetKeys("down", "tab")

	// Custom theme with orange titles
	formTheme := huh.ThemeBase16()
	orange := lipgloss.AdaptiveColor{Light: "#EA580C", Dark: "#FB923C"}
	formTheme.Focused.Title = formTheme.Focused.Title.Foreground(orange)
	formTheme.Blurred.Title = formTheme.Blurred.Title.Foreground(orange)

	m.form = huh.NewForm(
		huh.NewGroup(
			huh.NewInput().
				Title("Name").
				Description("Display name for the server").
				Value(&m.name).
				Validate(func(s string) error {
					return nil // Name is optional
				}),

			huh.NewInput().
				Title("Command or URL").
				Description("Command to run, or https:// URL for HTTP server").
				Value(&m.commandOrURL).
				Validate(huh.ValidateNotEmpty()),

			huh.NewInput().
				Title("Arguments").
				Description("Space-separated args (for commands only)").
				Value(&m.args),

			huh.NewInput().
				Title("Working Directory").
				Description("Directory to run the command in").
				Value(&m.cwd),

			huh.NewText().
				Title("Environment Variables").
				Description("One per line: KEY=value").
				Value(&m.env).
				CharLimit(1000).
				Lines(2),

			huh.NewInput().
				Title("Bearer Token Env Var").
				Description("Env var name for bearer auth (HTTP only, optional)").
				Value(&m.bearerTokenEnvVar),
		),
	).WithTheme(formTheme).
		WithWidth(60).
		WithShowHelp(true).
		WithShowErrors(true).
		WithKeyMap(keymap)
}

// isDirty returns true if any form values have changed from their initial values.
func (m *ServerFormModel) isDirty() bool {
	return m.name != m.initialName ||
		m.commandOrURL != m.initialCommandOrURL ||
		m.args != m.initialArgs ||
		m.cwd != m.initialCwd ||
		m.env != m.initialEnv ||
		m.bearerTokenEnvVar != m.initialBearerTokenEnvVar
}

// Hide hides the form.
func (m *ServerFormModel) Hide() {
	m.visible = false
	m.form = nil
}

// IsVisible returns whether the form is visible.
func (m ServerFormModel) IsVisible() bool {
	return m.visible
}

// SetSize sets the available size for the form.
func (m *ServerFormModel) SetSize(width, height int) {
	m.width = width
	m.height = height
}

// Update handles messages for the form.
// IMPORTANT: Uses pointer receiver because huh forms store pointers to our field values.
func (m *ServerFormModel) Update(msg tea.Msg) tea.Cmd {
	if !m.visible || m.form == nil {
		return nil
	}

	switch msg := msg.(type) {
	case tea.KeyMsg:
		// Handle confirm discard dialog
		if m.showConfirmDiscard {
			switch msg.String() {
			case "y", "Y", "enter":
				// Save and close
				m.visible = false
				m.showConfirmDiscard = false
				srv := m.buildServerConfig()
				name := m.getName()
				originalName := m.originalName
				return func() tea.Msg {
					return ServerFormResult{
						Name:         name,
						OriginalName: originalName,
						Server:       srv,
						Submitted:    true,
						IsEdit:       m.isEdit,
					}
				}
			case "n", "N":
				// Discard and close
				m.visible = false
				m.showConfirmDiscard = false
				return func() tea.Msg {
					return ServerFormResult{Submitted: false}
				}
			case "esc", "c", "C":
				// Cancel - go back to form
				m.showConfirmDiscard = false
				return nil
			}
			return nil
		}

		// Handle escape to cancel
		if key.Matches(msg, m.escKey) {
			if m.isDirty() {
				// Show confirm dialog
				m.showConfirmDiscard = true
				return nil
			}
			// Not dirty, just close
			m.visible = false
			return func() tea.Msg {
				return ServerFormResult{Submitted: false}
			}
		}
	}

	// Don't update form while showing confirm dialog
	if m.showConfirmDiscard {
		return nil
	}

	// Update the form
	form, cmd := m.form.Update(msg)
	if f, ok := form.(*huh.Form); ok {
		m.form = f
	}

	// Check if form was completed
	if m.form.State == huh.StateCompleted {
		m.visible = false
		srv := m.buildServerConfig()
		name := m.getName()
		originalName := m.originalName
		isEdit := m.isEdit
		return func() tea.Msg {
			return ServerFormResult{
				Name:         name,
				OriginalName: originalName,
				Server:       srv,
				Submitted:    true,
				IsEdit:       isEdit,
			}
		}
	}

	// Check if form was aborted
	if m.form.State == huh.StateAborted {
		m.visible = false
		return func() tea.Msg {
			return ServerFormResult{Submitted: false}
		}
	}

	return cmd
}

// isHTTPURL returns true if the string looks like an HTTP(S) URL.
func isHTTPURL(s string) bool {
	s = strings.TrimSpace(strings.ToLower(s))
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

func (m ServerFormModel) buildServerConfig() config.ServerConfig {
	var srv config.ServerConfig

	// For edit mode, start with the original to preserve non-form fields
	// (Enabled, Kind, Headers, OAuth fields, Scopes, etc.)
	if m.isEdit && m.originalServer != nil {
		srv = *m.originalServer
	}

	commandOrURL := strings.TrimSpace(m.commandOrURL)

	// Auto-detect server type based on input
	if isHTTPURL(commandOrURL) {
		// HTTP server
		srv.URL = commandOrURL
		srv.Command = ""
		srv.Args = nil
		srv.BearerTokenEnvVar = strings.TrimSpace(m.bearerTokenEnvVar)
		srv.Kind = config.ServerKindStreamableHTTP
	} else {
		// Stdio server
		srv.Command = commandOrURL
		srv.Args = parseArgs(m.args)
		srv.URL = ""
		srv.BearerTokenEnvVar = ""
		srv.Kind = config.ServerKindStdio
	}

	srv.Cwd = strings.TrimSpace(m.cwd)
	srv.Env = parseEnvVars(m.env)
	// Autostart preserved from original in edit mode, defaults to false for new servers

	return srv
}

// getName returns the server name, using command/URL as fallback for new servers
func (m ServerFormModel) getName() string {
	name := strings.TrimSpace(m.name)
	if name != "" {
		return name
	}

	// Fallback: derive name from command or URL
	commandOrURL := strings.TrimSpace(m.commandOrURL)
	if isHTTPURL(commandOrURL) {
		// Extract hostname from URL as name
		// e.g., "https://mcp.sentry.dev/mcp" -> "sentry"
		s := strings.TrimPrefix(commandOrURL, "https://")
		s = strings.TrimPrefix(s, "http://")
		// Get hostname part
		if idx := strings.Index(s, "/"); idx > 0 {
			s = s[:idx]
		}
		// Try to extract a meaningful name from subdomain
		// "mcp.sentry.dev" -> "sentry"
		parts := strings.Split(s, ".")
		if len(parts) >= 2 {
			// Skip "mcp" prefix if present
			if parts[0] == "mcp" && len(parts) >= 3 {
				return parts[1]
			}
			return parts[0]
		}
		return s
	}

	// For commands, use the command itself
	return commandOrURL
}

// View renders the form.
func (m ServerFormModel) View() string {
	if !m.visible || m.form == nil {
		return ""
	}

	return m.form.View()
}

// RenderOverlay renders the form as a centered overlay.
func (m ServerFormModel) RenderOverlay(base string, width, height int) string {
	if !m.visible {
		return base
	}

	// Wrap in a styled box
	dialogWidth := 70
	if width > 0 && width < 80 {
		dialogWidth = width - 10
	}

	var content string
	if m.showConfirmDiscard {
		// Show confirm discard dialog
		confirmStyle := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(m.theme.Warn.GetForeground()).
			Padding(1, 2).
			Width(50)

		confirmContent := m.theme.Warn.Bold(true).Render("Unsaved Changes") + "\n\n" +
			"You have unsaved changes. What would you like to do?\n\n" +
			m.theme.Primary.Render("[Y]") + " Save changes\n" +
			m.theme.Danger.Render("[N]") + " Discard changes\n" +
			m.theme.Muted.Render("[C/Esc]") + " Cancel (continue editing)"

		content = confirmStyle.Render(confirmContent)
	} else {
		formView := m.View()
		content = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(m.theme.Primary.GetForeground()).
			Padding(1, 2).
			Width(dialogWidth).
			Render(formView)
	}

	return lipgloss.Place(
		width,
		height,
		lipgloss.Center,
		lipgloss.Center,
		content,
		lipgloss.WithWhitespaceChars(" "),
		lipgloss.WithWhitespaceForeground(lipgloss.AdaptiveColor{Light: "#E5E7EB", Dark: "#1F2937"}),
	)
}

// formatArgs converts args slice to space-separated string, quoting args with spaces.
func formatArgs(args []string) string {
	if len(args) == 0 {
		return ""
	}

	var parts []string
	for _, arg := range args {
		escaped := strings.ReplaceAll(arg, "\\", "\\\\")
		escaped = strings.ReplaceAll(escaped, "\"", "\\\"")
		if strings.Contains(arg, " ") || strings.Contains(arg, "'") || strings.Contains(arg, "\"") {
			// Quote args containing spaces or quotes
			// Use double quotes and escape any existing double quotes
			parts = append(parts, "\""+escaped+"\"")
		} else {
			parts = append(parts, escaped)
		}
	}
	return strings.Join(parts, " ")
}

// parseArgs splits space-separated arguments, respecting quoted strings and escapes.
func parseArgs(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}

	var args []string
	var current strings.Builder
	inQuote := false
	quoteChar := rune(0)
	escaped := false

	for _, r := range s {
		if escaped {
			// Previous char was backslash - add this char literally
			current.WriteRune(r)
			escaped = false
			continue
		}

		switch {
		case r == '\\':
			// Start escape sequence
			escaped = true
		case (r == '"' || r == '\'') && !inQuote:
			inQuote = true
			quoteChar = r
		case r == quoteChar && inQuote:
			inQuote = false
			quoteChar = 0
		case r == ' ' && !inQuote:
			if current.Len() > 0 {
				args = append(args, current.String())
				current.Reset()
			}
		default:
			current.WriteRune(r)
		}
	}

	if current.Len() > 0 {
		args = append(args, current.String())
	}

	return args
}

// parseEnvVars parses KEY=value lines into a map.
func parseEnvVars(s string) map[string]string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}

	env := make(map[string]string)
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.Contains(line, "=") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		key := strings.TrimSpace(parts[0])
		value := ""
		if len(parts) > 1 {
			value = strings.TrimSpace(parts[1])
		}
		if key != "" {
			env[key] = value
		}
	}

	if len(env) == 0 {
		return nil
	}
	return env
}

// formatEnvVars converts a map to KEY=value lines.
func formatEnvVars(env map[string]string) string {
	if len(env) == 0 {
		return ""
	}

	var lines []string
	for k, v := range env {
		lines = append(lines, k+"="+v)
	}
	return strings.Join(lines, "\n")
}
