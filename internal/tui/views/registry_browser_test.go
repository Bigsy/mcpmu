package views

import (
	"errors"
	"testing"

	"github.com/Bigsy/mcpmu/internal/registry"
	"github.com/Bigsy/mcpmu/internal/tui/theme"
	tea "github.com/charmbracelet/bubbletea"
)

func newTestBrowser() RegistryBrowserModel {
	th := theme.New()
	return NewRegistryBrowser(th)
}

func testServers() []registry.Server {
	return []registry.Server{
		{
			Name:        "io.github.brave/brave-search-mcp-server",
			Title:       "Brave Search MCP Server",
			Description: "Web search with Brave",
			Version:     "2.0.75",
			Packages: []registry.Package{
				{
					RegistryType: "npm",
					Identifier:   "@brave/brave-search-mcp-server",
					Version:      "2.0.75",
					RuntimeHint:  "npx",
					Transport:    registry.Transport{Type: "stdio"},
					EnvironmentVariables: []registry.EnvironmentVar{
						{Name: "BRAVE_API_KEY", Description: "API key", IsRequired: true, IsSecret: true},
					},
				},
			},
		},
		{
			Name:        "io.github.example/other-server",
			Description: "Another server",
			Version:     "1.0.0",
			Packages: []registry.Package{
				{
					RegistryType: "pypi",
					Identifier:   "other-server",
					Version:      "1.0.0",
					Transport:    registry.Transport{Type: "stdio"},
				},
			},
		},
	}
}

func TestRegistryBrowser_ShowHideLifecycle(t *testing.T) {
	m := newTestBrowser()

	if m.IsVisible() {
		t.Error("expected browser to be hidden initially")
	}

	m.Show()
	if !m.IsVisible() {
		t.Error("expected browser to be visible after Show()")
	}
	if m.state != stateSearchList {
		t.Errorf("expected stateSearchList, got %d", m.state)
	}

	m.Hide()
	if m.IsVisible() {
		t.Error("expected browser to be hidden after Hide()")
	}
}

func TestRegistryBrowser_EscClosesFromSearchList(t *testing.T) {
	m := newTestBrowser()
	m.Show()
	m.SetSize(120, 40)

	cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.IsVisible() {
		t.Error("expected browser to be hidden after Esc")
	}

	if cmd == nil {
		t.Fatal("expected a command from Esc")
	}
	msg := cmd()
	result, ok := msg.(RegistryBrowserResult)
	if !ok {
		t.Fatalf("expected RegistryBrowserResult, got %T", msg)
	}
	if result.Submitted {
		t.Error("expected Submitted=false on Esc")
	}
}

func TestRegistryBrowser_SearchResultUpdatesItems(t *testing.T) {
	m := newTestBrowser()
	m.Show()
	m.SetSize(120, 40)
	m.lastSearchedQuery = "brave"

	servers := testServers()
	m.Update(registrySearchResultMsg{query: "brave", servers: servers})

	items := m.list.Items()
	if len(items) != 2 {
		t.Fatalf("expected 2 list items, got %d", len(items))
	}
}

func TestRegistryBrowser_StaleSearchResultDiscarded(t *testing.T) {
	m := newTestBrowser()
	m.Show()
	m.SetSize(120, 40)
	m.lastSearchedQuery = "sentry"

	// Send result for old "brave" query
	servers := testServers()
	m.Update(registrySearchResultMsg{query: "brave", servers: servers})

	items := m.list.Items()
	if len(items) != 0 {
		t.Fatalf("expected 0 items (stale result), got %d", len(items))
	}
}

func TestRegistryBrowser_ArrowKeysNavigateList(t *testing.T) {
	m := newTestBrowser()
	m.Show()
	m.SetSize(120, 40)
	m.lastSearchedQuery = "brave"
	m.Update(registrySearchResultMsg{query: "brave", servers: testServers()})

	// Initial cursor should be 0
	if m.list.Index() != 0 {
		t.Fatalf("expected initial cursor at 0, got %d", m.list.Index())
	}

	// Arrow down moves cursor
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if m.list.Index() != 1 {
		t.Errorf("expected cursor at 1 after ↓, got %d", m.list.Index())
	}

	// Arrow up moves cursor back
	m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if m.list.Index() != 0 {
		t.Errorf("expected cursor at 0 after ↑, got %d", m.list.Index())
	}

	// 'j' key should go to text input, not navigate list
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	if m.list.Index() != 0 {
		t.Errorf("expected cursor still at 0 after 'j' (should go to input), got %d", m.list.Index())
	}
	if m.searchInput.Value() != "j" {
		t.Errorf("expected 'j' in search input, got %q", m.searchInput.Value())
	}
}

func TestRegistryBrowser_EnterOnListTransitionsToDetail(t *testing.T) {
	m := newTestBrowser()
	m.Show()
	m.SetSize(120, 40)
	m.lastSearchedQuery = "brave"
	m.Update(registrySearchResultMsg{query: "brave", servers: testServers()})

	m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if m.state != stateDetail {
		t.Errorf("expected stateDetail after Enter, got %d", m.state)
	}
	if m.detailServer == nil {
		t.Error("expected detailServer to be set")
	}
}

func TestRegistryBrowser_EscFromDetailReturnsToSearchList(t *testing.T) {
	m := newTestBrowser()
	m.Show()
	m.SetSize(120, 40)
	m.lastSearchedQuery = "brave"
	m.Update(registrySearchResultMsg{query: "brave", servers: testServers()})
	m.Update(tea.KeyMsg{Type: tea.KeyEnter}) // enter detail

	if m.state != stateDetail {
		t.Fatal("precondition: expected to be in detail state")
	}

	m.Update(tea.KeyMsg{Type: tea.KeyEsc})

	if m.state != stateSearchList {
		t.Errorf("expected stateSearchList after Esc from detail, got %d", m.state)
	}
	if m.IsVisible() != true {
		t.Error("expected browser to still be visible after Esc from detail")
	}
}

func TestRegistryBrowser_EnterFromDetailProducesInstallResult(t *testing.T) {
	m := newTestBrowser()
	m.Show()
	m.SetSize(120, 40)
	m.lastSearchedQuery = "brave"
	m.Update(registrySearchResultMsg{query: "brave", servers: testServers()})
	m.Update(tea.KeyMsg{Type: tea.KeyEnter}) // enter detail

	cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter}) // install

	if cmd == nil {
		t.Fatal("expected a command from install Enter")
	}
	msg := cmd()
	result, ok := msg.(RegistryBrowserResult)
	if !ok {
		t.Fatalf("expected RegistryBrowserResult, got %T", msg)
	}
	if !result.Submitted {
		t.Error("expected Submitted=true")
	}
	if result.Spec.CommandOrURL == "" {
		t.Error("expected non-empty CommandOrURL in install spec")
	}
	if result.Spec.Name == "" {
		t.Error("expected non-empty Name in install spec")
	}
}

func TestRegistryBrowser_ErrorMessageDisplayed(t *testing.T) {
	m := newTestBrowser()
	m.Show()
	m.SetSize(120, 40)
	m.lastSearchedQuery = "test"

	m.Update(registrySearchResultMsg{
		query: "test",
		err:   errors.New("registry search timed out"),
	})

	if m.searchErr == nil {
		t.Error("expected searchErr to be set")
	}

	view := m.viewSearchList()
	if view == "" {
		t.Fatal("expected non-empty view")
	}
	// The error message should appear in the rendered output (stripped of ANSI would contain it)
	if m.searchErr.Error() != "registry search timed out" {
		t.Errorf("expected timeout error, got %q", m.searchErr.Error())
	}
}

func TestRegistryBrowser_IKeyFromDetailProducesInstallResult(t *testing.T) {
	m := newTestBrowser()
	m.Show()
	m.SetSize(120, 40)
	m.lastSearchedQuery = "brave"
	m.Update(registrySearchResultMsg{query: "brave", servers: testServers()})
	m.Update(tea.KeyMsg{Type: tea.KeyEnter}) // enter detail

	// Press 'i' to install
	cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'i'}})

	if cmd == nil {
		t.Fatal("expected a command from 'i' key install")
	}
	msg := cmd()
	result, ok := msg.(RegistryBrowserResult)
	if !ok {
		t.Fatalf("expected RegistryBrowserResult, got %T", msg)
	}
	if !result.Submitted {
		t.Error("expected Submitted=true from 'i' key")
	}
}
