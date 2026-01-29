package tui

import (
	"github.com/charmbracelet/bubbles/key"
)

// KeyContext identifies which UI context is active for key routing.
type KeyContext int

const (
	ContextList KeyContext = iota
	ContextDetail
	ContextModal
	ContextLogPanel
	ContextHelp
	ContextConfirm
)

// KeyBindings holds all keybindings organized by context.
type KeyBindings struct {
	// Global keys (always active)
	Quit    key.Binding
	Help    key.Binding
	TabNext key.Binding
	TabPrev key.Binding
	Tab1    key.Binding
	Tab2    key.Binding
	Tab3    key.Binding
	Escape  key.Binding
	CtrlC   key.Binding

	// List navigation
	Up     key.Binding
	Down   key.Binding
	Top    key.Binding
	Bottom key.Binding
	Enter  key.Binding

	// Server actions
	Test          key.Binding // Toggle start/stop for testing
	Add           key.Binding
	Edit          key.Binding
	Delete        key.Binding
	ToggleLogs    key.Binding
	FollowLogs    key.Binding
	ToggleEnabled key.Binding

	// Confirm dialog
	Yes key.Binding
	No  key.Binding
}

// NewKeyBindings creates the default keybindings.
func NewKeyBindings() KeyBindings {
	return KeyBindings{
		// Global
		Quit: key.NewBinding(
			key.WithKeys("q"),
			key.WithHelp("q", "quit"),
		),
		Help: key.NewBinding(
			key.WithKeys("?"),
			key.WithHelp("?", "help"),
		),
		TabNext: key.NewBinding(
			key.WithKeys("tab"),
			key.WithHelp("tab", "next tab"),
		),
		TabPrev: key.NewBinding(
			key.WithKeys("shift+tab"),
			key.WithHelp("shift+tab", "prev tab"),
		),
		Tab1: key.NewBinding(
			key.WithKeys("1"),
			key.WithHelp("1", "servers"),
		),
		Tab2: key.NewBinding(
			key.WithKeys("2"),
			key.WithHelp("2", "namespaces"),
		),
		Tab3: key.NewBinding(
			key.WithKeys("3"),
			key.WithHelp("3", "proxies"),
		),
		Escape: key.NewBinding(
			key.WithKeys("esc"),
			key.WithHelp("esc", "back/close"),
		),
		CtrlC: key.NewBinding(
			key.WithKeys("ctrl+c"),
			key.WithHelp("ctrl+c", "force quit"),
		),

		// List navigation
		Up: key.NewBinding(
			key.WithKeys("up", "k"),
			key.WithHelp("↑/k", "up"),
		),
		Down: key.NewBinding(
			key.WithKeys("down", "j"),
			key.WithHelp("↓/j", "down"),
		),
		Top: key.NewBinding(
			key.WithKeys("g"),
			key.WithHelp("g", "top"),
		),
		Bottom: key.NewBinding(
			key.WithKeys("G"),
			key.WithHelp("G", "bottom"),
		),
		Enter: key.NewBinding(
			key.WithKeys("enter"),
			key.WithHelp("enter", "select"),
		),

		// Server actions
		Test: key.NewBinding(
			key.WithKeys("t"),
			key.WithHelp("t", "test (start/stop)"),
		),
		Add: key.NewBinding(
			key.WithKeys("a"),
			key.WithHelp("a", "add"),
		),
		Edit: key.NewBinding(
			key.WithKeys("e"),
			key.WithHelp("e", "edit"),
		),
		Delete: key.NewBinding(
			key.WithKeys("d"),
			key.WithHelp("d", "delete"),
		),
		ToggleLogs: key.NewBinding(
			key.WithKeys("l"),
			key.WithHelp("l", "logs"),
		),
		FollowLogs: key.NewBinding(
			key.WithKeys("f"),
			key.WithHelp("f", "follow"),
		),
		ToggleEnabled: key.NewBinding(
			key.WithKeys("E"),
			key.WithHelp("E", "enable/disable"),
		),

		// Confirm dialog
		Yes: key.NewBinding(
			key.WithKeys("y", "Y"),
			key.WithHelp("y", "yes"),
		),
		No: key.NewBinding(
			key.WithKeys("n", "N"),
			key.WithHelp("n", "no"),
		),
	}
}

// ShortHelp returns keybindings for the short help view.
func (k KeyBindings) ShortHelp() []key.Binding {
	return []key.Binding{k.Test, k.ToggleEnabled, k.ToggleLogs, k.Help, k.Quit}
}

// FullHelp returns keybindings for the full help view.
func (k KeyBindings) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Up, k.Down, k.Top, k.Bottom, k.Enter, k.Escape},
		{k.Test, k.ToggleEnabled, k.Add, k.Edit, k.Delete},
		{k.ToggleLogs, k.FollowLogs, k.TabPrev, k.TabNext, k.Tab1, k.Tab2, k.Tab3},
		{k.Help, k.Quit, k.CtrlC},
	}
}
