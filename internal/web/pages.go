package web

import (
	"log"
	"net/http"
	"slices"
	"time"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/events"
)

// serverRow is the template data for a single server in the list.
type serverRow struct {
	Name       string
	Config     config.ServerConfig
	Status     events.ServerStatus
	ToolCount  int
	Cached     bool
	Uptime     time.Duration
	Namespaces []string
}

// serversPageData is the template data for the servers list page.
type serversPageData struct {
	Page         string
	ConfigPath   string
	Servers      []serverRow
	TotalCount   int
	RunningCount int
	TotalTools   int
}

// buildServerRow builds a serverRow with status, tool counts, and uptime.
// This is the single source of truth for server row data across all pages.
func (s *Server) buildServerRow(name string, srv config.ServerConfig, statuses map[string]events.ServerStatus) serverRow {
	row := serverRow{
		Name:   name,
		Config: srv,
	}

	// Get runtime status
	if st, ok := statuses[name]; ok {
		row.Status = st
		row.ToolCount = st.ToolCount
		if st.State == events.StateRunning && st.StartedAt != nil {
			row.Uptime = time.Since(*st.StartedAt)
		}
	}

	// If not running, check tool cache for cached tools
	if row.Status.State != events.StateRunning && s.toolCache != nil {
		if cached, ok := s.toolCache.Get(name); ok {
			row.ToolCount = len(cached)
			row.Cached = true
		}
	}

	// Live tools from status tracker override cache count
	if liveTools, ok := s.status.Tools(name); ok && len(liveTools) > 0 {
		row.ToolCount = len(liveTools)
		row.Cached = false
	}

	return row
}

func (s *Server) handleServersPage(w http.ResponseWriter, r *http.Request) {
	entries := s.cfg.ServerEntries()
	statuses := s.status.All()

	var rows []serverRow
	var runningCount, totalTools int

	for _, entry := range entries {
		row := s.buildServerRow(entry.Name, entry.Config, statuses)

		if row.Status.State == events.StateRunning {
			runningCount++
		}
		totalTools += row.ToolCount

		// Find which namespaces this server belongs to
		for nsName, nsCfg := range s.cfg.Namespaces {
			if slices.Contains(nsCfg.ServerIDs, entry.Name) {
				row.Namespaces = append(row.Namespaces, nsName)
			}
		}

		rows = append(rows, row)
	}

	data := serversPageData{
		Page:         "servers",
		ConfigPath:   s.configPathDisplay(),
		Servers:      rows,
		TotalCount:   len(entries),
		RunningCount: runningCount,
		TotalTools:   totalTools,
	}

	s.render(w, "servers.html", data)
}

// serverDetailData is the template data for the server detail page.
type serverDetailData struct {
	Page       string
	ConfigPath string
	Name       string
	Config     config.ServerConfig
	Status     events.ServerStatus
	Tools      []toolDisplay
	DeniedMap  map[string]bool
	Uptime     time.Duration
	Namespaces []string
	Cached     bool
}

type toolDisplay struct {
	Name        string
	Description string
	Denied      bool
	TokenCount  int
}

func (s *Server) handleServerDetailPage(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	srv, ok := s.cfg.GetServer(name)
	if !ok {
		http.NotFound(w, r)
		return
	}

	data := serverDetailData{
		Page:       "servers",
		ConfigPath: s.configPathDisplay(),
		Name:       name,
		Config:     srv,
		DeniedMap:  make(map[string]bool),
	}

	// Build denied tools map
	for _, t := range srv.DeniedTools {
		data.DeniedMap[t] = true
	}

	// Get status
	if st, ok := s.status.Get(name); ok {
		data.Status = st
		if st.State == events.StateRunning && st.StartedAt != nil {
			data.Uptime = time.Since(*st.StartedAt)
		}
	}

	// Get tools — prefer live, fall back to cache
	if liveTools, ok := s.status.Tools(name); ok && len(liveTools) > 0 {
		for _, t := range liveTools {
			data.Tools = append(data.Tools, toolDisplay{
				Name:        t.Name,
				Description: t.Description,
				Denied:      data.DeniedMap[t.Name],
			})
		}
	} else if s.toolCache != nil {
		if cached, ok := s.toolCache.Get(name); ok {
			data.Cached = true
			for _, t := range cached {
				data.Tools = append(data.Tools, toolDisplay{
					Name:        t.Name,
					Description: t.Description,
					Denied:      data.DeniedMap[t.Name],
					TokenCount:  t.TokenCount,
				})
			}
		}
	}

	// Namespaces
	for nsName, nsCfg := range s.cfg.Namespaces {
		if slices.Contains(nsCfg.ServerIDs, name) {
			data.Namespaces = append(data.Namespaces, nsName)
		}
	}

	s.render(w, "server_detail.html", data)
}

// namespacesPageData is the template data for the namespaces list page.
type namespacesPageData struct {
	Page             string
	ConfigPath       string
	Namespaces       []namespaceRow
	TotalCount       int
	TotalAssigned    int
	DefaultNamespace string
}

type namespaceRow struct {
	Name        string
	Config      config.NamespaceConfig
	ServerCount int
	PermCount   int
	IsDefault   bool
}

func (s *Server) handleNamespacesPage(w http.ResponseWriter, r *http.Request) {
	entries := s.cfg.NamespaceEntries()

	var rows []namespaceRow
	var totalAssigned int

	for _, entry := range entries {
		perms := s.cfg.GetToolPermissionsForNamespace(entry.Name)
		row := namespaceRow{
			Name:        entry.Name,
			Config:      entry.Config,
			ServerCount: len(entry.Config.ServerIDs),
			PermCount:   len(perms),
			IsDefault:   entry.Name == s.cfg.DefaultNamespace,
		}
		totalAssigned += row.ServerCount
		rows = append(rows, row)
	}

	data := namespacesPageData{
		Page:             "namespaces",
		ConfigPath:       s.configPathDisplay(),
		Namespaces:       rows,
		TotalCount:       len(entries),
		TotalAssigned:    totalAssigned,
		DefaultNamespace: s.cfg.DefaultNamespace,
	}

	s.render(w, "namespaces.html", data)
}

// namespaceDetailData is the template data for the namespace detail page.
type namespaceDetailData struct {
	Page             string
	ConfigPath       string
	Name             string
	Config           config.NamespaceConfig
	IsDefault        bool
	DefaultNamespace string
	Servers          []serverRow
	Permissions      []config.ToolPermission
}

func (s *Server) handleNamespaceDetailPage(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	ns, ok := s.cfg.GetNamespace(name)
	if !ok {
		http.NotFound(w, r)
		return
	}

	statuses := s.status.All()

	var servers []serverRow
	for _, sid := range ns.ServerIDs {
		srv, ok := s.cfg.GetServer(sid)
		if !ok {
			continue
		}
		servers = append(servers, s.buildServerRow(sid, srv, statuses))
	}

	data := namespaceDetailData{
		Page:             "namespaces",
		ConfigPath:       s.configPathDisplay(),
		Name:             name,
		Config:           ns,
		IsDefault:        name == s.cfg.DefaultNamespace,
		DefaultNamespace: s.cfg.DefaultNamespace,
		Servers:          servers,
		Permissions:      s.cfg.GetToolPermissionsForNamespace(name),
	}

	s.render(w, "namespace_detail.html", data)
}

// render executes a page template by name (e.g., "servers.html").
// Each page template has its own clone of the layout, so block definitions
// don't collide across pages.
func (s *Server) render(w http.ResponseWriter, name string, data any) {
	tmpl, ok := s.templates[name]
	if !ok {
		log.Printf("template %q not found", name)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "layout", data); err != nil {
		log.Printf("template %s: %v", name, err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

// handleConfigImportPage renders the config import page.
func (s *Server) handleConfigImportPage(w http.ResponseWriter, _ *http.Request) {
	data := struct {
		Page       string
		ConfigPath string
	}{
		Page:       "",
		ConfigPath: s.configPathDisplay(),
	}
	s.render(w, "config_import.html", data)
}

// configPathDisplay returns a display-friendly config path.
func (s *Server) configPathDisplay() string {
	if s.configPath != "" {
		return s.configPath
	}
	return "~/.config/mcpmu/config.json"
}
