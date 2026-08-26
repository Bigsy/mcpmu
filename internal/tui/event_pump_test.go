package tui

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Bigsy/mcpmu/internal/events"
)

// pumpSentinel is what a re-armed waitForEvent() will return once the test
// drops it into m.eventCh.
var pumpSentinel = events.NewLogReceivedEvent("pump-probe", "sentinel")

// reArmsEventPump reports whether running cmd (recursively, through
// tea.BatchMsg) yields a command that reads m.eventCh — i.e. whether the
// Update that produced cmd re-armed the event pump. Every command is run on
// its own goroutine with the sentinel already queued; commands that block
// (tickers, blink timers) are abandoned after a short grace period.
func reArmsEventPump(t *testing.T, m Model, cmd tea.Cmd) bool {
	t.Helper()
	if cmd == nil {
		return false
	}

	// One sentinel is enough: at most one leaf should be reading eventCh, and
	// if none is, the sentinel just stays queued and is drained below.
	m.eventCh <- pumpSentinel
	defer func() {
		select {
		case <-m.eventCh:
		default:
		}
	}()

	results := make(chan tea.Msg, 64)
	pending := 0
	run := func(c tea.Cmd) {
		pending++
		go func() { results <- c() }()
	}
	run(cmd)

	deadline := time.After(500 * time.Millisecond)
	for pending > 0 {
		select {
		case msg := <-results:
			pending--
			switch v := msg.(type) {
			case tea.BatchMsg:
				for _, c := range v {
					if c != nil {
						run(c)
					}
				}
			case events.LogReceivedEvent:
				if v.ServerID() == "pump-probe" {
					return true
				}
			}
		case <-deadline:
			return false
		}
	}
	return false
}

// TestEventPump_ReArmedWithEveryModalOpen is the continuity guarantee for the
// TUI's event pump: whatever modal is open, delivering an events.Event through
// Update must produce a command that waits for the next one. If any modal's
// handler forgets, every status/log/toast update stops for the rest of the
// session the moment that modal is opened.
func TestEventPump_ReArmedWithEveryModalOpen(t *testing.T) {
	modals := []struct {
		name string
		open func(m *Model)
	}{
		{"none", func(m *Model) {}},
		{"serverForm", func(m *Model) { _ = m.serverForm.ShowAdd() }},
		{"namespaceForm", func(m *Model) { _ = m.namespaceForm.ShowAdd() }},
		{"serverPicker", func(m *Model) { m.serverPicker.Show(nil, nil) }},
		{"toolPerms", func(m *Model) { m.toolPerms.Show("ns", nil, nil, nil, false, nil, nil) }},
		{"toolDenyEditor", func(m *Model) { m.toolDenyEditor.Show("srv", nil, nil) }},
		{"addMethod", func(m *Model) { m.addMethod.Show() }},
		{"registryBrowser", func(m *Model) { m.registryBrowser.Show() }},
	}

	for _, tc := range modals {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel(t)
			tc.open(&m)

			evt := events.NewLogReceivedEvent("some-server", "hello")
			_, cmd := updateModel(m, evt)

			if !reArmsEventPump(t, m, cmd) {
				t.Fatalf("event pump not re-armed with %s open: Update(events.Event) returned no waitForEvent()", tc.name)
			}
		})
	}
}

// TestEventPump_BaselineNonEventDoesNotReArm guards the probe itself: a key
// press must not look like a re-arm, or the test above proves nothing.
func TestEventPump_BaselineNonEventDoesNotReArm(t *testing.T) {
	m := newTestModel(t)
	_, cmd := updateModel(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if reArmsEventPump(t, m, cmd) {
		t.Fatal("probe reported a re-arm for a plain key press")
	}
}
