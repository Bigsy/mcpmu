package tui

// Modal dispatch: every modal (forms, pickers, editors, browser) is fed
// through updateModal so the event pump stays armed while it is open.

import (
	"strings"

	"github.com/Bigsy/mcpmu/internal/events"
	"github.com/Bigsy/mcpmu/internal/tui/views"
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
)

// modalUpdateConfig describes one modal to updateModal: how to resize it, how
// to feed it messages, and how to recognise and handle its result message.
// Every modal goes through updateModal — there is deliberately no per-modal
// update loop, because the event-pump re-arm in updateModal is what keeps
// status/log/toast delivery alive while a modal is open
// (TestEventPump_ReArmedWithEveryModalOpen).
type modalUpdateConfig struct {
	setSize      func(width, height int)                      // Called on WindowSizeMsg to resize the modal
	handleResult func(msg tea.Msg) (bool, tea.Model, tea.Cmd) // Returns (handled, model, cmd) if msg is the result type
	updateForm   func(msg tea.Msg) tea.Cmd                    // Updates the modal form component
}

// resultHandler adapts a typed handle*Result method to modalUpdateConfig.handleResult.
func resultHandler[T any](handle func(T) (tea.Model, tea.Cmd)) func(tea.Msg) (bool, tea.Model, tea.Cmd) {
	return func(msg tea.Msg) (bool, tea.Model, tea.Cmd) {
		if result, ok := msg.(T); ok {
			model, cmd := handle(result)
			return true, model, cmd
		}
		return false, nil, nil
	}
}

// modalVisible reports whether any modal is open.
func (m Model) modalVisible() bool {
	_, ok := m.activeModal()
	return ok
}

// activeModal returns the update config for the visible modal, if any. Order
// matters only when two are visible at once (e.g. the server form opened from
// the add-method selector), and mirrors the historical precedence.
//
// The closures are bound to *this* m, so the caller must build the config on
// the same copy of the model it mutates and returns — several views are held
// by value, and a config built on another copy would update the wrong one.
func (m *Model) activeModal() (modalUpdateConfig, bool) {
	switch {
	case m.serverForm.IsVisible():
		return modalUpdateConfig{
			setSize: func(w, h int) {
				m.helpOverlay.SetSize(w, h)
				m.serverForm.SetSize(w, h)
				m.confirmDlg.SetSize(w, h)
				m.toast.SetSize(w, h)
			},
			handleResult: resultHandler(m.handleServerFormResult),
			updateForm:   m.serverForm.Update,
		}, true
	case m.namespaceForm.IsVisible():
		return modalUpdateConfig{
			setSize:      m.namespaceForm.SetSize,
			handleResult: resultHandler(m.handleNamespaceFormResult),
			updateForm:   m.namespaceForm.Update,
		}, true
	case m.serverPicker.IsVisible():
		return modalUpdateConfig{
			setSize:      m.serverPicker.SetSize,
			handleResult: resultHandler(m.handleServerPickerResult),
			updateForm:   m.serverPicker.Update,
		}, true
	case m.toolPerms.IsVisible():
		return modalUpdateConfig{
			setSize:      m.toolPerms.SetSize,
			handleResult: resultHandler(m.handleToolPermissionsResult),
			updateForm:   m.toolPerms.Update,
		}, true
	case m.toolDenyEditor.IsVisible():
		return modalUpdateConfig{
			setSize:      m.toolDenyEditor.SetSize,
			handleResult: resultHandler(m.handleToolDenyResult),
			updateForm:   m.toolDenyEditor.Update,
		}, true
	case m.addMethod.IsVisible():
		return modalUpdateConfig{
			setSize:      m.addMethod.SetSize,
			handleResult: resultHandler(m.handleAddMethodResult),
			updateForm: func(msg tea.Msg) tea.Cmd {
				var cmd tea.Cmd
				m.addMethod, cmd = m.addMethod.Update(msg)
				return cmd
			},
		}, true
	case m.registryBrowser.IsVisible():
		return modalUpdateConfig{
			setSize:      m.registryBrowser.SetSize,
			handleResult: resultHandler(m.handleRegistryBrowserResult),
			updateForm:   m.registryBrowser.Update,
		}, true
	}
	return modalUpdateConfig{}, false
}

// updateModal is the common update handler for all modal forms.
// It handles: Ctrl+C quit, window resize, result messages, form updates,
// event handling (with event-pump re-arm), and toast updates.
func (m Model) updateModal(msg tea.Msg) (tea.Model, tea.Cmd) {
	cfg, ok := m.activeModal()
	if !ok {
		return m, nil
	}
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		if key.Matches(msg, m.keys.CtrlC) {
			return m, tea.Quit
		}
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.updateLayout()
		cfg.setSize(msg.Width, msg.Height)
	default:
		if handled, model, cmd := cfg.handleResult(msg); handled {
			return model, cmd
		}
	}

	if cmd := cfg.updateForm(msg); cmd != nil {
		cmds = append(cmds, cmd)
	}

	if evt, ok := msg.(events.Event); ok {
		if cmd := m.handleEvent(evt); cmd != nil {
			cmds = append(cmds, cmd)
		}
		cmds = append(cmds, m.waitForEvent())
	}

	var toastCmd tea.Cmd
	m.toast, toastCmd = m.toast.Update(msg)
	if toastCmd != nil {
		cmds = append(cmds, toastCmd)
	}

	return m, tea.Batch(cmds...)
}

func (m Model) handleAddMethodResult(result views.AddMethodResult) (tea.Model, tea.Cmd) {
	m.addMethod.Hide()
	if result.Submitted {
		switch result.Method {
		case "manual":
			return m, m.serverForm.ShowAdd()
		case "registry":
			m.registryBrowser.Show()
		}
	}
	return m, nil
}

func (m Model) handleRegistryBrowserResult(result views.RegistryBrowserResult) (tea.Model, tea.Cmd) {
	if result.Submitted {
		m.pendingRegistryInstall = &result.Spec
	}
	m.registryBrowser.Hide()
	return m, nil
}

// formatEnvMap converts a map of env vars to the "KEY=value\nKEY2=value2" format.
func formatEnvMap(env map[string]string) string {
	if len(env) == 0 {
		return ""
	}
	var parts []string
	for k, v := range env {
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, "\n")
}
