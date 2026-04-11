package views

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Bigsy/mcpmu/internal/registry"
	"github.com/Bigsy/mcpmu/internal/tui/theme"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// RegistryBrowserResult is sent when the user completes or cancels the registry browser.
type RegistryBrowserResult struct {
	Spec      registry.InstallSpec
	Submitted bool
}

// registryBrowserState tracks the current navigation state.
type registryBrowserState int

const (
	stateSearchList registryBrowserState = iota
	stateDetail
)

// debounceMsg is sent after the debounce delay to trigger a search.
type debounceMsg struct{ query string }

// registrySearchResultMsg carries async search results back to the browser.
type registrySearchResultMsg struct {
	query   string
	servers []registry.Server
	err     error
}

// registryBrowserItem represents a server in the search result list.
type registryBrowserItem struct {
	server  registry.Server
	display string // Derived short name
	pkg     *registry.Package
	remote  *registry.Remote
}

func (i registryBrowserItem) Title() string       { return i.display }
func (i registryBrowserItem) Description() string { return "" }
func (i registryBrowserItem) FilterValue() string { return i.display }

// RegistryBrowserModel is a registry browser overlay with search and detail views.
type RegistryBrowserModel struct {
	theme   theme.Theme
	visible bool
	state   registryBrowserState
	width   int
	height  int

	// Search state
	searchInput       textinput.Model
	prevInputValue    string
	lastSearchedQuery string
	searching         bool
	searchErr         error
	servers           []registry.Server

	// List
	list list.Model

	// Detail state
	detailServer   *registry.Server
	detailPkg      *registry.Package
	detailRemote   *registry.Remote
	detailSpec     registry.InstallSpec
	detailViewport viewport.Model
	detailReady    bool

	// Spinner for search
	spinner spinner.Model

	// Registry client
	client *registry.Client

	// Key bindings
	navUp      key.Binding
	navDown    key.Binding
	enterKey   key.Binding
	escKey     key.Binding
	installKey key.Binding
}

// NewRegistryBrowser creates a new registry browser.
func NewRegistryBrowser(th theme.Theme) RegistryBrowserModel {
	ti := textinput.New()
	ti.Placeholder = "Search MCP servers..."
	ti.CharLimit = 100

	delegate := newRegistryBrowserDelegate(th)
	l := list.New([]list.Item{}, delegate, 0, 0)
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(false)
	l.SetShowHelp(false)
	l.SetShowTitle(false)

	s := spinner.New()
	s.Spinner = spinner.Dot

	return RegistryBrowserModel{
		theme:       th,
		searchInput: ti,
		list:        l,
		spinner:     s,
		client:      registry.NewClient(),
		navUp: key.NewBinding(
			key.WithKeys("up"),
		),
		navDown: key.NewBinding(
			key.WithKeys("down"),
		),
		enterKey: key.NewBinding(
			key.WithKeys("enter"),
		),
		escKey: key.NewBinding(
			key.WithKeys("esc"),
		),
		installKey: key.NewBinding(
			key.WithKeys("i"),
		),
	}
}

// NewRegistryBrowserWithClient creates a browser with a custom client (for testing).
func NewRegistryBrowserWithClient(th theme.Theme, client *registry.Client) RegistryBrowserModel {
	m := NewRegistryBrowser(th)
	m.client = client
	return m
}

// SetTestServers injects servers and updates the list (for testing from other packages).
func (m *RegistryBrowserModel) SetTestServers(query string, servers []registry.Server) {
	m.lastSearchedQuery = query
	m.servers = servers
	m.searching = false
	m.searchErr = nil
	m.updateListItems()
}

// Show makes the browser visible and focuses the search input.
func (m *RegistryBrowserModel) Show() {
	m.visible = true
	m.state = stateSearchList
	m.searchInput.SetValue("")
	m.searchInput.Focus()
	m.prevInputValue = ""
	m.lastSearchedQuery = ""
	m.searching = false
	m.searchErr = nil
	m.servers = nil
	m.list.SetItems(nil)
	m.detailServer = nil
	m.detailPkg = nil
	m.detailRemote = nil
	m.detailReady = false
}

// Hide hides the browser.
func (m *RegistryBrowserModel) Hide() {
	m.visible = false
	m.searchInput.Blur()
}

// IsVisible returns whether the browser is visible.
func (m RegistryBrowserModel) IsVisible() bool {
	return m.visible
}

// SetSize sets the available dimensions for the browser overlay.
func (m *RegistryBrowserModel) SetSize(width, height int) {
	m.width = width
	m.height = height

	innerWidth := m.overlayWidth() - 6 // border(2) + padding(4)
	listHeight := m.listHeight()
	m.list.SetSize(innerWidth, listHeight)
}

// Update handles messages for the browser.
func (m *RegistryBrowserModel) Update(msg tea.Msg) tea.Cmd {
	if !m.visible {
		return nil
	}

	switch msg := msg.(type) {
	case debounceMsg:
		if msg.query == m.searchInput.Value() && msg.query != "" {
			m.searching = true
			m.searchErr = nil
			m.lastSearchedQuery = msg.query
			return tea.Batch(m.doSearch(msg.query), m.spinner.Tick)
		}
		return nil

	case registrySearchResultMsg:
		// Discard stale results
		if msg.query != m.lastSearchedQuery || !m.visible {
			return nil
		}
		m.searching = false
		if msg.err != nil {
			m.searchErr = msg.err
			m.servers = nil
			m.list.SetItems(nil)
			return nil
		}
		m.searchErr = nil
		m.servers = msg.servers
		m.updateListItems()
		return nil

	case spinner.TickMsg:
		if m.searching {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			return cmd
		}
		return nil

	case tea.KeyMsg:
		if m.state == stateDetail {
			return m.updateDetail(msg)
		}
		return m.updateSearchList(msg)
	}

	return nil
}

func (m *RegistryBrowserModel) updateSearchList(msg tea.KeyMsg) tea.Cmd {
	switch {
	case key.Matches(msg, m.escKey):
		m.visible = false
		return func() tea.Msg {
			return RegistryBrowserResult{Submitted: false}
		}

	case key.Matches(msg, m.navDown):
		m.list.CursorDown()
		return nil

	case key.Matches(msg, m.navUp):
		m.list.CursorUp()
		return nil

	case key.Matches(msg, m.enterKey):
		return m.enterDetail()

	default:
		// All other keys go to text input
		var cmd tea.Cmd
		m.searchInput, cmd = m.searchInput.Update(msg)

		if m.searchInput.Value() != m.prevInputValue {
			m.prevInputValue = m.searchInput.Value()
			if m.prevInputValue == "" {
				m.clearResults()
				return cmd
			}
			return tea.Batch(cmd, m.scheduleDebounce(m.prevInputValue))
		}
		return cmd
	}
}

func (m *RegistryBrowserModel) updateDetail(msg tea.KeyMsg) tea.Cmd {
	switch {
	case key.Matches(msg, m.escKey):
		m.state = stateSearchList
		m.detailServer = nil
		m.detailPkg = nil
		m.detailRemote = nil
		m.detailReady = false
		return nil

	case key.Matches(msg, m.enterKey), key.Matches(msg, m.installKey):
		if m.detailServer == nil {
			return nil
		}
		spec := m.detailSpec
		m.visible = false
		return func() tea.Msg {
			return RegistryBrowserResult{
				Spec:      spec,
				Submitted: true,
			}
		}

	default:
		// Scroll the detail viewport with arrow keys
		if m.detailReady {
			var cmd tea.Cmd
			m.detailViewport, cmd = m.detailViewport.Update(msg)
			return cmd
		}
		return nil
	}
}

func (m *RegistryBrowserModel) enterDetail() tea.Cmd {
	item := m.list.SelectedItem()
	if item == nil {
		return nil
	}
	bi := item.(registryBrowserItem)
	m.state = stateDetail
	m.detailServer = &bi.server
	m.detailPkg = bi.pkg
	m.detailRemote = bi.remote
	m.detailSpec = registry.BuildInstallSpec(bi.server, bi.pkg, bi.remote)
	m.prepareDetailViewport()
	return nil
}

func (m *RegistryBrowserModel) prepareDetailViewport() {
	innerWidth := m.overlayWidth() - 6
	vpHeight := m.detailViewportHeight()

	content := m.renderDetailContent(innerWidth)
	m.detailViewport = viewport.New(innerWidth, vpHeight)
	m.detailViewport.SetContent(content)
	m.detailReady = true
}

func (m *RegistryBrowserModel) scheduleDebounce(query string) tea.Cmd {
	return tea.Tick(300*time.Millisecond, func(time.Time) tea.Msg {
		return debounceMsg{query: query}
	})
}

func (m *RegistryBrowserModel) doSearch(query string) tea.Cmd {
	client := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		servers, err := client.Search(ctx, query)
		return registrySearchResultMsg{query: query, servers: servers, err: err}
	}
}

func (m *RegistryBrowserModel) clearResults() {
	m.servers = nil
	m.searchErr = nil
	m.searching = false
	m.lastSearchedQuery = ""
	m.list.SetItems(nil)
}

func (m *RegistryBrowserModel) updateListItems() {
	items := make([]list.Item, 0, len(m.servers))
	for _, srv := range m.servers {
		pkg, remote := registry.SelectBestPackage(srv)
		display := registry.DeriveName(srv)
		items = append(items, registryBrowserItem{
			server:  srv,
			display: display,
			pkg:     pkg,
			remote:  remote,
		})
	}
	m.list.SetItems(items)
}

// overlayWidth returns the width of the overlay dialog.
func (m RegistryBrowserModel) overlayWidth() int {
	w := 70
	if m.width < 80 {
		w = m.width - 10
	}
	if w < 30 {
		w = 30
	}
	return w
}

// innerContentHeight returns the usable content height inside the overlay box
// (after subtracting border, padding, title, and title gap).
func (m RegistryBrowserModel) innerContentHeight() int {
	// overlayHeight includes border(2). Padding(1,2) adds 2 vertical lines.
	// Title line(1) + title gap(1) = 2 more.
	h := max(
		// border, padding, title+gap
		m.overlayHeight()-2-2-2, 6)
	return h
}

// listHeight returns the available height for the list within the overlay.
func (m RegistryBrowserModel) listHeight() int {
	// From innerContentHeight, subtract: search(1) + gap(1) + footer gap(1) + footer(1) = 4
	h := max(m.innerContentHeight()-4, 3)
	return h
}

// detailViewportHeight returns the available height for the detail viewport.
func (m RegistryBrowserModel) detailViewportHeight() int {
	// From innerContentHeight, subtract: footer gap(1) + footer(1) = 2
	h := max(m.innerContentHeight()-2, 5)
	return h
}

// View renders the browser content (used inside RenderOverlay).
func (m RegistryBrowserModel) View() string {
	if !m.visible {
		return ""
	}
	if m.state == stateDetail {
		return m.viewDetail()
	}
	return m.viewSearchList()
}

func (m RegistryBrowserModel) viewSearchList() string {
	var b strings.Builder

	// Search input
	b.WriteString(m.theme.Faint.Render("Search: "))
	b.WriteString(m.searchInput.View())
	b.WriteString("\n\n")

	// Content area
	if m.searching {
		b.WriteString(m.spinner.View())
		b.WriteString(" Searching...")
	} else if m.searchErr != nil {
		b.WriteString(m.theme.Danger.Render(m.searchErr.Error()))
	} else if m.lastSearchedQuery != "" && len(m.servers) == 0 {
		b.WriteString(m.theme.Muted.Render(fmt.Sprintf("No servers found for %q", m.lastSearchedQuery)))
	} else if len(m.servers) > 0 {
		b.WriteString(m.list.View())
	}

	b.WriteString("\n\n")

	// Footer
	if len(m.servers) > 0 {
		b.WriteString(m.theme.Faint.Render("↑↓ navigate  enter select  esc close"))
	} else {
		b.WriteString(m.theme.Faint.Render("esc close"))
	}

	return b.String()
}

func (m RegistryBrowserModel) viewDetail() string {
	if !m.detailReady {
		return ""
	}

	var b strings.Builder
	b.WriteString(m.detailViewport.View())
	b.WriteString("\n\n")
	b.WriteString(m.theme.Faint.Render("enter install  esc back"))
	return b.String()
}

func (m RegistryBrowserModel) renderDetailContent(width int) string {
	if m.detailServer == nil {
		return ""
	}
	srv := m.detailServer
	spec := m.detailSpec

	var b strings.Builder

	// Header: name + version
	name := registry.DeriveName(*srv)
	version := srv.Version
	nameStr := m.theme.Title.Render(name)
	versionStr := m.theme.Faint.Render(version)
	gap := max(width-lipgloss.Width(nameStr)-lipgloss.Width(versionStr), 1)
	b.WriteString(nameStr + strings.Repeat(" ", gap) + versionStr)
	b.WriteString("\n")

	// Full registry name
	b.WriteString(m.theme.Faint.Render(srv.Name))
	b.WriteString("\n\n")

	// Description
	if srv.Description != "" {
		b.WriteString(wordWrap(srv.Description, width))
		b.WriteString("\n\n")
	}

	// Package info
	if m.detailPkg != nil {
		pkg := m.detailPkg
		badges := m.theme.Primary.Render(pkg.RegistryType) + "  " + m.theme.Primary.Render(pkg.Transport.Type)
		b.WriteString(m.theme.Title.Render("Package") + "  " + badges)
		b.WriteString("\n")
		b.WriteString(m.theme.Muted.Render(pkg.Identifier))
		b.WriteString("\n\n")
	} else if m.detailRemote != nil {
		remote := m.detailRemote
		b.WriteString(m.theme.Title.Render("Remote") + "  " + m.theme.Primary.Render(remote.Type))
		b.WriteString("\n")
		b.WriteString(m.theme.Muted.Render(remote.URL))
		b.WriteString("\n\n")
	}

	// Environment Variables
	var envVars []registry.EnvironmentVar
	if m.detailPkg != nil {
		envVars = m.detailPkg.EnvironmentVariables
	}
	if len(envVars) > 0 {
		b.WriteString(m.theme.Title.Render("Environment Variables"))
		b.WriteString("\n")
		for _, v := range envVars {
			line := m.theme.Base.Render(v.Name)
			if v.IsRequired {
				line += "  " + m.theme.Warn.Render("required")
			}
			if v.IsSecret {
				line += "  " + m.theme.Faint.Render("secret")
			}
			b.WriteString(line)
			b.WriteString("\n")
			if v.Description != "" {
				b.WriteString("  " + m.theme.Muted.Render(v.Description))
				b.WriteString("\n")
			}
		}
		b.WriteString("\n")
	}

	// Package Arguments
	var pkgArgs []registry.PackageArgument
	if m.detailPkg != nil {
		pkgArgs = m.detailPkg.PackageArguments
	}
	if len(pkgArgs) > 0 {
		b.WriteString(m.theme.Title.Render("Package Arguments"))
		b.WriteString("\n")
		for _, a := range pkgArgs {
			argName := a.Name
			if a.Type == "named" {
				argName = "--" + argName
			}
			line := m.theme.Base.Render(argName)
			if a.IsRequired {
				line += "  " + m.theme.Warn.Render("required")
			}
			b.WriteString(line)
			b.WriteString("\n")
			if a.Description != "" {
				b.WriteString("  " + m.theme.Muted.Render(a.Description))
				b.WriteString("\n")
			}
		}
		b.WriteString("\n")
	}

	// Install Preview
	b.WriteString(m.theme.Title.Render("Install Preview"))
	b.WriteString("\n")
	if spec.CommandOrURL != "" {
		preview := spec.CommandOrURL
		if spec.Args != "" {
			preview += " " + spec.Args
		}
		b.WriteString(m.theme.Primary.Render(preview))
		b.WriteString("\n")
		if spec.Env != nil {
			for k, v := range spec.Env {
				b.WriteString(m.theme.Muted.Render(k + "=" + v))
				b.WriteString("\n")
			}
		}
		if spec.Headers != nil {
			for k, v := range spec.Headers {
				b.WriteString(m.theme.Muted.Render("Header: " + k + ": " + v))
				b.WriteString("\n")
			}
		}
		if spec.BearerTokenEnvVar != "" {
			b.WriteString(m.theme.Muted.Render("Bearer token: " + spec.BearerTokenEnvVar))
			b.WriteString("\n")
		}
	} else {
		b.WriteString(m.theme.Danger.Render("No installable packages"))
		b.WriteString("\n")
	}

	return b.String()
}

// overlayHeight returns the fixed outer height of the overlay box.
func (m RegistryBrowserModel) overlayHeight() int {
	// Use most of the terminal but leave room for app chrome
	h := max(m.height-6, 12)
	return h
}

// RenderOverlay renders the browser as a centered overlay.
func (m RegistryBrowserModel) RenderOverlay(base string, width, height int) string {
	if !m.visible {
		return base
	}

	overlayWidth := m.overlayWidth()
	overlayHeight := m.overlayHeight()

	title := m.theme.Title.Render(" Install from Registry ")
	inner := title + "\n\n" + m.View()

	// Pad inner content to fill the fixed height so the box never jumps.
	// Subtract border(2) + padding(2) from the target height to get inner lines.
	targetInner := overlayHeight - 4
	innerLines := strings.Count(inner, "\n") + 1
	if innerLines < targetInner {
		inner += strings.Repeat("\n", targetInner-innerLines)
	}

	content := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(m.theme.Primary.GetForeground()).
		Padding(1, 2).
		Width(overlayWidth).
		Height(overlayHeight - 2). // subtract border top+bottom
		Render(inner)

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

// wordWrap wraps text to the given width at word boundaries.
func wordWrap(text string, width int) string {
	if width <= 0 {
		return text
	}
	words := strings.Fields(text)
	if len(words) == 0 {
		return ""
	}
	var lines []string
	current := words[0]
	for _, word := range words[1:] {
		if len(current)+1+len(word) > width {
			lines = append(lines, current)
			current = word
		} else {
			current += " " + word
		}
	}
	lines = append(lines, current)
	return strings.Join(lines, "\n")
}

// registryBrowserDelegate renders items in the registry browser list.
type registryBrowserDelegate struct {
	theme theme.Theme
}

func newRegistryBrowserDelegate(th theme.Theme) registryBrowserDelegate {
	return registryBrowserDelegate{theme: th}
}

func (d registryBrowserDelegate) Height() int                             { return 2 }
func (d registryBrowserDelegate) Spacing() int                            { return 0 }
func (d registryBrowserDelegate) Update(_ tea.Msg, _ *list.Model) tea.Cmd { return nil }

func (d registryBrowserDelegate) Render(w io.Writer, m list.Model, index int, item list.Item) {
	bi, ok := item.(registryBrowserItem)
	if !ok {
		return
	}

	isSelected := index == m.Index()

	// Line 1: cursor + name + version + badges
	cursor := "  "
	if isSelected {
		cursor = m.Styles.Title.Render("▸ ")
	}

	name := bi.display
	if isSelected {
		name = d.theme.ItemSelected.Render(name)
	} else {
		name = d.theme.Item.Render(name)
	}

	version := d.theme.Faint.Render("v" + bi.server.Version)

	var badges string
	if bi.pkg != nil {
		badges = d.theme.Muted.Render(bi.pkg.RegistryType + "  " + bi.pkg.Transport.Type)
	} else if bi.remote != nil {
		badges = d.theme.Muted.Render(bi.remote.Type)
	}

	line1 := cursor + name + "  " + version + "  " + badges

	// Line 2: description (truncated)
	desc := bi.server.Description
	maxDesc := m.Width() - 4
	if maxDesc > 0 && len(desc) > maxDesc {
		desc = desc[:maxDesc-1] + "…"
	}
	line2 := "    " + d.theme.Faint.Render(desc)

	_, _ = fmt.Fprintf(w, "%s\n%s", line1, line2)
}
