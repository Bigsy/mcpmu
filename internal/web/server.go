package web

import (
	"context"
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/events"
	"github.com/Bigsy/mcpmu/internal/process"
	"github.com/Bigsy/mcpmu/internal/registry"
)

//go:embed static
var staticFS embed.FS

//go:embed templates
var templateFS embed.FS

// Server is the HTTP server for the web UI.
type Server struct {
	cfg        *config.Config
	cfgMu      sync.Mutex // protects config read-modify-write cycles
	configPath string
	supervisor *process.Supervisor
	bus        *events.Bus
	toolCache  *config.ToolCache
	registry   *registry.Client
	status     *StatusTracker
	templates  map[string]*template.Template
	httpServer *http.Server
	auth       *auth // nil when auth is disabled
}

// Options configures the web server.
type Options struct {
	Addr       string
	Config     *config.Config
	ConfigPath string
	Supervisor *process.Supervisor
	Bus        *events.Bus
	ToolCache  *config.ToolCache
	Token      string // if set, require token auth for all requests
}

// New creates a new web Server.
func New(opts Options) (*Server, error) {
	a := newAuth(opts.Token)

	tmpl, err := parseTemplates(a != nil)
	if err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}

	s := &Server{
		cfg:        opts.Config,
		configPath: opts.ConfigPath,
		supervisor: opts.Supervisor,
		bus:        opts.Bus,
		toolCache:  opts.ToolCache,
		registry:   registry.NewClient(),
		status:     NewStatusTracker(opts.Bus),
		templates:  tmpl,
		auth:       a,
	}

	mux := http.NewServeMux()
	s.registerRoutes(mux)

	// Auth routes (registered on mux so logging/recovery still apply)
	if a != nil {
		mux.HandleFunc("GET /login", a.handleLoginPage(s))
		mux.HandleFunc("POST /login", a.handleLoginSubmit())
		mux.HandleFunc("POST /logout", a.handleLogout())
	}

	var handler http.Handler = mux
	handler = logging(handler)
	handler = recovery(handler)

	// Auth middleware wraps everything — it exempts /login and /static internally
	if a != nil {
		handler = a.middleware(handler)
	}

	s.httpServer = &http.Server{
		Addr:              opts.Addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	return s, nil
}

// ListenAndServe starts the HTTP server.
func (s *Server) ListenAndServe() error {
	addr := s.httpServer.Addr
	host, _, _ := net.SplitHostPort(addr)
	if host != "127.0.0.1" && host != "localhost" && host != "::1" && host != "" {
		if s.auth == nil {
			log.Printf("WARNING: binding to %s — the web UI has no authentication. Set --token or MCPMU_WEB_TOKEN.", addr)
		} else {
			log.Printf("Binding to %s with token auth enabled", addr)
		}
	}
	log.Printf("mcpmu web listening on http://%s", addr)
	return s.httpServer.ListenAndServe()
}

// Shutdown gracefully shuts down the HTTP server.
func (s *Server) Shutdown(ctx context.Context) error {
	s.status.Close()
	return s.httpServer.Shutdown(ctx)
}

func (s *Server) registerRoutes(mux *http.ServeMux) {
	// Static files
	staticSub, _ := fs.Sub(staticFS, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticSub))))

	// Pages — read-only
	mux.HandleFunc("GET /{$}", s.handleServersPage)
	mux.HandleFunc("GET /servers", s.handleServersPage)
	mux.HandleFunc("GET /servers/{name}", s.handleServerDetailPage)
	mux.HandleFunc("GET /namespaces", s.handleNamespacesPage)
	mux.HandleFunc("GET /namespaces/{name}", s.handleNamespaceDetailPage)

	// Pages — forms (Phase 2)
	mux.HandleFunc("GET /servers/add", s.handleServerAddPage)
	mux.HandleFunc("GET /servers/{name}/edit", s.handleServerEditPage)
	mux.HandleFunc("GET /namespaces/add", s.handleNamespaceAddPage)
	mux.HandleFunc("GET /namespaces/{name}/edit", s.handleNamespaceEditPage)
	mux.HandleFunc("GET /config/import", s.handleConfigImportPage)
	mux.HandleFunc("GET /registry", s.handleRegistryPage)

	// Form submissions (Phase 2)
	mux.HandleFunc("POST /servers", s.handleServerCreate)
	mux.HandleFunc("POST /servers/{name}/edit", s.handleServerUpdate)
	mux.HandleFunc("POST /servers/{name}/delete", s.handleServerDelete)
	mux.HandleFunc("POST /servers/{name}/toggle", s.handleServerToggle)
	mux.HandleFunc("POST /namespaces", s.handleNamespaceCreate)
	mux.HandleFunc("POST /namespaces/{name}/edit", s.handleNamespaceUpdate)
	mux.HandleFunc("POST /namespaces/{name}/delete", s.handleNamespaceDelete)
	mux.HandleFunc("POST /namespaces/{name}/assign", s.handleNamespaceAssign)
	mux.HandleFunc("POST /namespaces/{name}/unassign", s.handleNamespaceUnassign)

	// Live actions (Phase 3)
	mux.HandleFunc("POST /servers/{name}/start", s.handleServerStart)
	mux.HandleFunc("POST /servers/{name}/stop", s.handleServerStop)
	mux.HandleFunc("POST /servers/{name}/login", s.handleServerLogin)
	mux.HandleFunc("POST /servers/{name}/logout", s.handleServerLogout)
	mux.HandleFunc("POST /servers/{name}/denied-tools", s.handleServerDeniedTools)
	mux.HandleFunc("POST /namespaces/{name}/set-default", s.handleNamespaceSetDefault)
	mux.HandleFunc("POST /namespaces/{name}/permission", s.handleNamespacePermission)
	mux.HandleFunc("POST /namespaces/{name}/server-default", s.handleNamespaceServerDefault)

	// JSON API (Phase 2)
	mux.HandleFunc("GET /api/servers", s.handleAPIListServers)
	mux.HandleFunc("POST /api/servers", s.handleAPICreateServer)
	mux.HandleFunc("GET /api/servers/{name}", s.handleAPIGetServer)
	mux.HandleFunc("PUT /api/servers/{name}", s.handleAPIUpdateServer)
	mux.HandleFunc("DELETE /api/servers/{name}", s.handleAPIDeleteServer)
	mux.HandleFunc("GET /api/namespaces", s.handleAPIListNamespaces)
	mux.HandleFunc("POST /api/namespaces", s.handleAPICreateNamespace)
	mux.HandleFunc("GET /api/namespaces/{name}", s.handleAPIGetNamespace)
	mux.HandleFunc("PUT /api/namespaces/{name}", s.handleAPIUpdateNamespace)
	mux.HandleFunc("DELETE /api/namespaces/{name}", s.handleAPIDeleteNamespace)
	mux.HandleFunc("POST /api/servers/{name}/start", s.handleAPIServerStart)
	mux.HandleFunc("POST /api/servers/{name}/stop", s.handleAPIServerStop)
	mux.HandleFunc("POST /api/servers/{name}/login", s.handleAPIServerLogin)
	mux.HandleFunc("POST /api/servers/{name}/logout", s.handleAPIServerLogout)
	mux.HandleFunc("GET /api/config/export", s.handleAPIExportConfig)
	mux.HandleFunc("POST /api/config/import", s.handleAPIImportConfig)
	mux.HandleFunc("POST /api/config/import/apply", s.handleAPIImportApply)
	mux.HandleFunc("GET /api/registry/search", s.handleAPIRegistrySearch)

	// Fragments (HTML partials for htmx swaps)
	mux.HandleFunc("GET /fragments/servers/table", s.handleFragmentServerTable)
	mux.HandleFunc("GET /fragments/servers/{name}/status", s.handleFragmentServerStatus)
	mux.HandleFunc("GET /fragments/registry/results", s.handleFragmentRegistryResults)

	// SSE
	mux.HandleFunc("GET /servers/{name}/logs/stream", s.handleSSELogs)
}

// pageTemplates lists the page templates that each get their own clone of the layout.
var pageTemplates = []string{
	"templates/servers.html",
	"templates/server_detail.html",
	"templates/server_form.html",
	"templates/namespaces.html",
	"templates/namespace_detail.html",
	"templates/namespace_form.html",
	"templates/config_import.html",
	"templates/registry.html",
}

func parseTemplates(authEnabled bool) (map[string]*template.Template, error) {
	funcMap := template.FuncMap{
		"authEnabled": func() bool { return authEnabled },
		"stateClass":  stateClass,
		"stateDot":    stateDot,
		"stateLabel":  stateLabel,
		"kindBadge":   kindBadge,
		"isRunning":   func(s events.RuntimeState) bool { return s == events.StateRunning },
		"isStarting":  func(s events.RuntimeState) bool { return s == events.StateStarting },
		"isStopping":  func(s events.RuntimeState) bool { return s == events.StateStopping },
		"isNeedsAuth": func(s events.RuntimeState) bool { return s == events.StateNeedsAuth },
		"canStart": func(s events.RuntimeState) bool {
			return s == events.StateIdle || s == events.StateStopped || s == events.StateError || s == events.StateCrashed
		},
		"permKey": func(server, tool string) string { return server + ":" + tool },
		"mapGet": func(m map[string]bool, key string) bool {
			return m[key]
		},
		"mapHas": func(m map[string]bool, key string) bool {
			_, ok := m[key]
			return ok
		},
		"formatUptime": func(d time.Duration) string {
			if d <= 0 {
				return "\u2014"
			}
			h := int(d.Hours())
			m := int(d.Minutes()) % 60
			if h > 0 {
				return fmt.Sprintf("%dh %dm", h, m)
			}
			s := int(d.Seconds()) % 60
			if m > 0 {
				return fmt.Sprintf("%dm %ds", m, s)
			}
			return fmt.Sprintf("%ds", s)
		},
		"serverCommand": func(srv config.ServerConfig) string {
			if srv.IsHTTP() {
				return srv.URL
			}
			parts := append([]string{srv.Command}, srv.Args...)
			return strings.Join(parts, " ")
		},
		"add": func(a, b int) int { return a + b },
		"sub": func(a, b int) int { return a - b },
	}

	// Parse the shared layout + partials as a base template
	base, err := template.New("base").Funcs(funcMap).ParseFS(templateFS,
		"templates/layout.html",
		"templates/partials/*.html",
	)
	if err != nil {
		return nil, fmt.Errorf("parse layout: %w", err)
	}

	// For each page, clone the base and parse the page template into the clone.
	// This ensures each page has its own "content" and "title" block definitions.
	templates := make(map[string]*template.Template, len(pageTemplates)+1)
	for _, pagePath := range pageTemplates {
		clone, err := base.Clone()
		if err != nil {
			return nil, fmt.Errorf("clone base for %s: %w", pagePath, err)
		}
		t, err := clone.ParseFS(templateFS, pagePath)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", pagePath, err)
		}
		// Key by the filename (e.g., "servers.html")
		name := pagePath[len("templates/"):]
		templates[name] = t
	}

	// Store the base template (with partials) for fragment rendering.
	// Fragments execute named partial definitions without the layout wrapper.
	templates["_fragments"] = base

	// Login page is standalone (no layout wrapper)
	login, err := template.New("login.html").ParseFS(templateFS, "templates/login.html")
	if err != nil {
		return nil, fmt.Errorf("parse login.html: %w", err)
	}
	templates["login.html"] = login

	return templates, nil
}

// Template helper functions

func stateClass(s events.RuntimeState) string {
	switch s {
	case events.StateRunning:
		return "running"
	case events.StateStarting:
		return "starting"
	case events.StateStopping:
		return "stopping"
	case events.StateError, events.StateCrashed:
		return "error"
	case events.StateNeedsAuth:
		return "auth"
	default:
		return "stopped"
	}
}

func stateDot(s events.RuntimeState) string {
	return "dot-" + stateClass(s)
}

func stateLabel(s events.RuntimeState) string {
	switch s {
	case events.StateRunning:
		return "\u25cf Running"
	case events.StateStarting:
		return "\u25cf Starting"
	case events.StateStopping:
		return "\u25cf Stopping"
	case events.StateError:
		return "\u2716 Error"
	case events.StateCrashed:
		return "\u2716 Crashed"
	case events.StateNeedsAuth:
		return "\U0001F512 Login"
	default:
		return "\u25cb Stopped"
	}
}

func kindBadge(srv config.ServerConfig) string {
	if srv.IsHTTP() {
		return "http"
	}
	return "stdio"
}

// mutateConfig safely applies a config mutation using a read-modify-write cycle.
// It reloads config from disk, applies the mutation function, saves back to disk,
// and updates the in-memory config pointer. The mutex ensures atomicity.
func (s *Server) mutateConfig(fn func(cfg *config.Config) error) error {
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()

	fresh, err := config.LoadFrom(s.configPath)
	if err != nil {
		return fmt.Errorf("reload config: %w", err)
	}

	if err := fn(fresh); err != nil {
		return err
	}

	if err := config.SaveTo(fresh, s.configPath); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	s.cfg = fresh
	return nil
}
