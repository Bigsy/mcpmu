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
	"time"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/events"
	"github.com/Bigsy/mcpmu/internal/process"
)

//go:embed static
var staticFS embed.FS

//go:embed templates
var templateFS embed.FS

// Server is the HTTP server for the web UI.
type Server struct {
	cfg        *config.Config
	configPath string
	supervisor *process.Supervisor
	bus        *events.Bus
	toolCache  *config.ToolCache
	status     *StatusTracker
	templates  map[string]*template.Template
	httpServer *http.Server
}

// Options configures the web server.
type Options struct {
	Addr       string
	Config     *config.Config
	ConfigPath string
	Supervisor *process.Supervisor
	Bus        *events.Bus
	ToolCache  *config.ToolCache
}

// New creates a new web Server.
func New(opts Options) (*Server, error) {
	tmpl, err := parseTemplates()
	if err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}

	s := &Server{
		cfg:        opts.Config,
		configPath: opts.ConfigPath,
		supervisor: opts.Supervisor,
		bus:        opts.Bus,
		toolCache:  opts.ToolCache,
		status:     NewStatusTracker(opts.Bus),
		templates:  tmpl,
	}

	mux := http.NewServeMux()
	s.registerRoutes(mux)

	var handler http.Handler = mux
	handler = logging(handler)
	handler = recovery(handler)

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
		log.Printf("WARNING: binding to %s — the web UI has no authentication. Anyone on the network can access it.", addr)
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

	// Pages
	mux.HandleFunc("GET /{$}", s.handleServersPage)
	mux.HandleFunc("GET /servers", s.handleServersPage)
	mux.HandleFunc("GET /servers/{name}", s.handleServerDetailPage)
	mux.HandleFunc("GET /namespaces", s.handleNamespacesPage)
	mux.HandleFunc("GET /namespaces/{name}", s.handleNamespaceDetailPage)

	// Fragments (HTML partials for htmx swaps)
	mux.HandleFunc("GET /fragments/servers/table", s.handleFragmentServerTable)
	mux.HandleFunc("GET /fragments/servers/{name}/status", s.handleFragmentServerStatus)

	// SSE
	mux.HandleFunc("GET /servers/{name}/logs/stream", s.handleSSELogs)
}

// pageTemplates lists the page templates that each get their own clone of the layout.
var pageTemplates = []string{
	"templates/servers.html",
	"templates/server_detail.html",
	"templates/namespaces.html",
	"templates/namespace_detail.html",
}

func parseTemplates() (map[string]*template.Template, error) {
	funcMap := template.FuncMap{
		"stateClass": stateClass,
		"stateDot":   stateDot,
		"stateLabel": stateLabel,
		"kindBadge":  kindBadge,
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
