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

// NamespaceFormResult is sent when the user completes or cancels the form.
type NamespaceFormResult struct {
	Name         string // Namespace name (map key)
	OriginalName string // Original name (for rename detection in edit mode)
	Namespace    config.NamespaceConfig
	Submitted    bool
	IsEdit       bool
}

// NamespaceFormModel is a form for adding/editing namespaces.
type NamespaceFormModel struct {
	theme   theme.Theme
	visible bool
	isEdit  bool
	width   int
	height  int

	// Form state
	form *huh.Form

	// Original namespace config (for edit mode)
	originalNamespace *config.NamespaceConfig
	originalName      string // Original name for edit mode (to detect rename)

	// Form field values
	name          string
	description   string
	denyByDefault bool
	compression   string

	// Initial values for dirty checking
	initialName          string
	initialDescription   string
	initialDenyByDefault bool
	initialCompression   string

	// Confirm discard state
	showConfirmDiscard bool

	// Key bindings
	escKey key.Binding
}

// NewNamespaceForm creates a new namespace form.
func NewNamespaceForm(th theme.Theme) NamespaceFormModel {
	return NamespaceFormModel{
		theme: th,
		escKey: key.NewBinding(
			key.WithKeys("esc"),
			key.WithHelp("esc", "cancel"),
		),
	}
}

// ShowAdd displays the form for adding a new namespace.
func (m *NamespaceFormModel) ShowAdd() tea.Cmd {
	m.visible = true
	m.isEdit = false
	m.showConfirmDiscard = false
	m.originalNamespace = nil
	m.originalName = ""
	m.name = ""
	m.description = ""
	m.denyByDefault = false
	m.compression = ""
	// Save initial values
	m.initialName = ""
	m.initialDescription = ""
	m.initialDenyByDefault = false
	m.initialCompression = ""
	m.buildForm()
	return m.form.Init()
}

// ShowEdit displays the form for editing an existing namespace.
func (m *NamespaceFormModel) ShowEdit(name string, ns config.NamespaceConfig) tea.Cmd {
	m.visible = true
	m.isEdit = true
	m.showConfirmDiscard = false
	m.originalNamespace = &ns
	m.originalName = name
	m.name = name
	m.description = ns.Description
	m.denyByDefault = ns.DenyByDefault
	m.compression = ns.Compression
	// Save initial values
	m.initialName = m.name
	m.initialDescription = m.description
	m.initialDenyByDefault = m.denyByDefault
	m.initialCompression = m.compression
	m.buildForm()
	return m.form.Init()
}

func (m *NamespaceFormModel) buildForm() {
	keymap := huh.NewDefaultKeyMap()
	keymap.Input.Prev.SetKeys("up", "shift+tab")
	keymap.Input.Next.SetKeys("down", "tab")
	keymap.Text.Prev.SetKeys("up", "shift+tab")
	keymap.Text.Next.SetKeys("down", "tab")
	keymap.Confirm.Prev.SetKeys("up", "shift+tab")
	keymap.Confirm.Next.SetKeys("down", "tab")
	// The compression select is inline (left/right cycles values), which
	// disables its own up/down bindings — reuse them for field navigation like
	// every other field.
	keymap.Select.Prev.SetKeys("up", "shift+tab")
	keymap.Select.Next.SetKeys("down", "tab", "enter")

	// Custom theme with orange titles
	formTheme := huh.ThemeBase16()
	orange := lipgloss.AdaptiveColor{Light: "#EA580C", Dark: "#FB923C"}
	formTheme.Focused.Title = formTheme.Focused.Title.Foreground(orange)
	formTheme.Blurred.Title = formTheme.Blurred.Title.Foreground(orange)

	m.form = huh.NewForm(
		huh.NewGroup(
			huh.NewInput().
				Title("Name").
				Description("Display name for the namespace (required)").
				Value(&m.name).
				Validate(huh.ValidateNotEmpty()),

			huh.NewText().
				Title("Description").
				Description("Optional description").
				Value(&m.description).
				CharLimit(200).
				Lines(2),

			huh.NewConfirm().
				Title("Deny by Default").
				Description("Block tools without explicit permission").
				Value(&m.denyByDefault),

			huh.NewSelect[string]().
				Title("Serve Compression").
				Description("Compress serve-mode tools/list into wrapper tools (--compress overrides)").
				Options(
					huh.NewOption("off", ""),
					huh.NewOption("low", "low"),
					huh.NewOption("medium", "medium"),
					huh.NewOption("high", "high"),
					huh.NewOption("max", "max"),
				).
				Inline(true).
				Value(&m.compression),
		),
	).WithTheme(formTheme).
		WithWidth(60).
		WithShowHelp(true).
		WithShowErrors(true).
		WithKeyMap(keymap)
}

func (m *NamespaceFormModel) isDirty() bool {
	return m.name != m.initialName ||
		m.description != m.initialDescription ||
		m.denyByDefault != m.initialDenyByDefault ||
		m.compression != m.initialCompression
}

// Hide hides the form.
func (m *NamespaceFormModel) Hide() {
	m.visible = false
	m.form = nil
}

// IsVisible returns whether the form is visible.
func (m NamespaceFormModel) IsVisible() bool {
	return m.visible
}

// SetSize sets the available size for the form.
func (m *NamespaceFormModel) SetSize(width, height int) {
	m.width = width
	m.height = height
}

// Update handles messages for the form.
func (m *NamespaceFormModel) Update(msg tea.Msg) tea.Cmd {
	if !m.visible || m.form == nil {
		return nil
	}

	switch msg := msg.(type) {
	case tea.KeyMsg:
		// Handle confirm discard dialog
		if m.showConfirmDiscard {
			switch msg.String() {
			case "y", "Y", "enter":
				m.visible = false
				m.showConfirmDiscard = false
				ns := m.buildNamespaceConfig()
				name := strings.TrimSpace(m.name)
				originalName := m.originalName
				isEdit := m.isEdit
				return func() tea.Msg {
					return NamespaceFormResult{
						Name:         name,
						OriginalName: originalName,
						Namespace:    ns,
						Submitted:    true,
						IsEdit:       isEdit,
					}
				}
			case "n", "N":
				m.visible = false
				m.showConfirmDiscard = false
				return func() tea.Msg {
					return NamespaceFormResult{Submitted: false}
				}
			case "esc", "c", "C":
				m.showConfirmDiscard = false
				return nil
			}
			return nil
		}

		// Handle escape to cancel
		if key.Matches(msg, m.escKey) {
			if m.isDirty() {
				m.showConfirmDiscard = true
				return nil
			}
			m.visible = false
			return func() tea.Msg {
				return NamespaceFormResult{Submitted: false}
			}
		}
	}

	if m.showConfirmDiscard {
		return nil
	}

	form, cmd := m.form.Update(msg)
	if f, ok := form.(*huh.Form); ok {
		m.form = f
	}

	if m.form.State == huh.StateCompleted {
		m.visible = false
		ns := m.buildNamespaceConfig()
		name := strings.TrimSpace(m.name)
		originalName := m.originalName
		isEdit := m.isEdit
		return func() tea.Msg {
			return NamespaceFormResult{
				Name:         name,
				OriginalName: originalName,
				Namespace:    ns,
				Submitted:    true,
				IsEdit:       isEdit,
			}
		}
	}

	if m.form.State == huh.StateAborted {
		m.visible = false
		return func() tea.Msg {
			return NamespaceFormResult{Submitted: false}
		}
	}

	return cmd
}

func (m NamespaceFormModel) buildNamespaceConfig() config.NamespaceConfig {
	var ns config.NamespaceConfig

	if m.isEdit && m.originalNamespace != nil {
		ns = *m.originalNamespace
	}

	ns.Description = strings.TrimSpace(m.description)
	ns.DenyByDefault = m.denyByDefault
	ns.Compression = m.compression

	return ns
}

// View renders the form.
func (m NamespaceFormModel) View() string {
	if !m.visible || m.form == nil {
		return ""
	}
	return m.form.View()
}

// RenderOverlay renders the form as a centered overlay.
func (m NamespaceFormModel) RenderOverlay(base string, width, height int) string {
	if !m.visible {
		return base
	}

	dialogWidth := 70
	if width > 0 && width < 80 {
		dialogWidth = width - 10
	}

	var content string
	if m.showConfirmDiscard {
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
