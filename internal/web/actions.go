package web

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/Bigsy/mcpmu/internal/config"
)

// --- Start / Stop ---

// handleServerStart starts a server via the supervisor.
func (s *Server) handleServerStart(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	srv, ok := s.cfg.GetServer(name)
	if !ok {
		http.NotFound(w, r)
		return
	}

	if !srv.IsEnabled() {
		s.redirectBack(w, r, "/servers/"+name)
		return
	}

	// Already running?
	if handle := s.supervisor.Get(name); handle != nil && handle.IsRunning() {
		s.redirectBack(w, r, "/servers/"+name)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(srv.StartupTimeout())*time.Second)
	defer cancel()

	if _, err := s.supervisor.Start(ctx, name, srv); err != nil {
		log.Printf("start server %q: %v", name, err)
	}

	s.redirectBack(w, r, "/servers/"+name)
}

// handleServerStop stops a running server.
func (s *Server) handleServerStop(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	if _, ok := s.cfg.GetServer(name); !ok {
		http.NotFound(w, r)
		return
	}

	if err := s.supervisor.Stop(name); err != nil {
		log.Printf("stop server %q: %v", name, err)
	}

	s.redirectBack(w, r, "/servers/"+name)
}

// handleAPIServerStart is the JSON API for starting a server.
func (s *Server) handleAPIServerStart(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	srv, ok := s.cfg.GetServer(name)
	if !ok {
		jsonError(w, fmt.Sprintf("server %q not found", name), http.StatusNotFound)
		return
	}

	if !srv.IsEnabled() {
		jsonError(w, fmt.Sprintf("server %q is disabled", name), http.StatusUnprocessableEntity)
		return
	}

	if handle := s.supervisor.Get(name); handle != nil && handle.IsRunning() {
		jsonError(w, fmt.Sprintf("server %q is already running", name), http.StatusConflict)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(srv.StartupTimeout())*time.Second)
	defer cancel()

	if _, err := s.supervisor.Start(ctx, name, srv); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	jsonOK(w, map[string]string{"status": "started", "server": name})
}

// handleAPIServerStop is the JSON API for stopping a server.
func (s *Server) handleAPIServerStop(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	if _, ok := s.cfg.GetServer(name); !ok {
		jsonError(w, fmt.Sprintf("server %q not found", name), http.StatusNotFound)
		return
	}

	if err := s.supervisor.Stop(name); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	jsonOK(w, map[string]string{"status": "stopped", "server": name})
}

// --- OAuth Login / Logout ---

// handleServerLogin triggers OAuth login for an HTTP server.
func (s *Server) handleServerLogin(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	srv, ok := s.cfg.GetServer(name)
	if !ok {
		http.NotFound(w, r)
		return
	}

	if !srv.IsHTTP() || !srv.IsEnabled() {
		s.redirectBack(w, r, "/servers/"+name)
		return
	}

	// If no handle exists or it's not in NeedsAuth state, start the server first
	// so that the supervisor can discover OAuth requirements and create the handle.
	if s.supervisor.Get(name) == nil {
		ctx, cancel := context.WithTimeout(r.Context(), time.Duration(srv.StartupTimeout())*time.Second)
		defer cancel()
		if _, err := s.supervisor.Start(ctx, name, srv); err != nil {
			log.Printf("oauth login %q: start failed: %v", name, err)
			s.redirectBack(w, r, "/servers/"+name)
			return
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()

	if err := s.supervisor.LoginOAuth(ctx, name); err != nil {
		log.Printf("oauth login %q: %v", name, err)
	}

	s.redirectBack(w, r, "/servers/"+name)
}

// handleServerLogout clears OAuth credentials for an HTTP server.
func (s *Server) handleServerLogout(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	srv, ok := s.cfg.GetServer(name)
	if !ok {
		http.NotFound(w, r)
		return
	}

	if !srv.IsHTTP() {
		s.redirectBack(w, r, "/servers/"+name)
		return
	}

	// Delete stored credentials
	credStore := s.supervisor.CredentialStore()
	if credStore != nil {
		if err := credStore.Delete(srv.URL); err != nil {
			log.Printf("oauth logout %q: delete creds: %v", name, err)
		}
	}

	// Stop the server if running so it reconnects fresh
	if handle := s.supervisor.Get(name); handle != nil && handle.IsRunning() {
		if err := s.supervisor.Stop(name); err != nil {
			log.Printf("oauth logout %q: stop: %v", name, err)
		}
	}

	s.redirectBack(w, r, "/servers/"+name)
}

// handleAPIServerLogin is the JSON API for OAuth login.
func (s *Server) handleAPIServerLogin(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	srv, ok := s.cfg.GetServer(name)
	if !ok {
		jsonError(w, fmt.Sprintf("server %q not found", name), http.StatusNotFound)
		return
	}

	if !srv.IsHTTP() {
		jsonError(w, fmt.Sprintf("server %q is not an HTTP server — OAuth login is only for HTTP servers", name), http.StatusUnprocessableEntity)
		return
	}

	if !srv.IsEnabled() {
		jsonError(w, fmt.Sprintf("server %q is disabled", name), http.StatusUnprocessableEntity)
		return
	}

	// Ensure the server has a handle. If not started, start it so the supervisor
	// can discover OAuth requirements and create the NeedsAuth handle.
	if s.supervisor.Get(name) == nil {
		ctx, cancel := context.WithTimeout(r.Context(), time.Duration(srv.StartupTimeout())*time.Second)
		defer cancel()
		if _, err := s.supervisor.Start(ctx, name, srv); err != nil {
			jsonError(w, fmt.Sprintf("failed to start server for OAuth: %v", err), http.StatusInternalServerError)
			return
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()

	if err := s.supervisor.LoginOAuth(ctx, name); err != nil {
		code := http.StatusInternalServerError
		if strings.Contains(err.Error(), "doesn't need OAuth login") {
			code = http.StatusConflict
		}
		jsonError(w, err.Error(), code)
		return
	}

	jsonOK(w, map[string]string{"status": "logged_in", "server": name})
}

// handleAPIServerLogout is the JSON API for OAuth logout.
func (s *Server) handleAPIServerLogout(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	srv, ok := s.cfg.GetServer(name)
	if !ok {
		jsonError(w, fmt.Sprintf("server %q not found", name), http.StatusNotFound)
		return
	}

	if !srv.IsHTTP() {
		jsonError(w, fmt.Sprintf("server %q is not an HTTP server", name), http.StatusUnprocessableEntity)
		return
	}

	credStore := s.supervisor.CredentialStore()
	if credStore == nil {
		jsonError(w, "no credential store available", http.StatusInternalServerError)
		return
	}

	if err := credStore.Delete(srv.URL); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Stop if running
	if handle := s.supervisor.Get(name); handle != nil && handle.IsRunning() {
		if err := s.supervisor.Stop(name); err != nil {
			log.Printf("oauth logout %q: stop: %v", name, err)
		}
	}

	jsonOK(w, map[string]string{"status": "logged_out", "server": name})
}

// --- Denied Tools ---

// handleServerDeniedTools adds or removes a tool from the server's denied list.
// Form fields: action ("add" or "remove"), tool (tool name).
func (s *Server) handleServerDeniedTools(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	action := r.FormValue("action")
	tool := strings.TrimSpace(r.FormValue("tool"))
	if tool == "" {
		s.redirectBack(w, r, "/servers/"+name)
		return
	}

	err := s.mutateConfig(func(cfg *config.Config) error {
		if _, ok := cfg.GetServer(name); !ok {
			return fmt.Errorf("server %q not found", name)
		}
		switch action {
		case "add":
			return cfg.DenyTool(name, tool)
		case "remove":
			return cfg.AllowTool(name, tool)
		default:
			return fmt.Errorf("unknown action %q", action)
		}
	})

	if err != nil {
		log.Printf("denied-tools %q: %v", name, err)
	}

	s.redirectBack(w, r, "/servers/"+name)
}

// --- Set Default Namespace ---

// handleNamespaceSetDefault sets or unsets a namespace as the default.
func (s *Server) handleNamespaceSetDefault(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	err := s.mutateConfig(func(cfg *config.Config) error {
		if _, ok := cfg.GetNamespace(name); !ok {
			return fmt.Errorf("namespace %q not found", name)
		}
		if cfg.DefaultNamespace == name {
			cfg.DefaultNamespace = "" // toggle off
		} else {
			cfg.DefaultNamespace = name
		}
		return nil
	})

	if err != nil {
		log.Printf("set-default namespace %q: %v", name, err)
	}

	s.redirectBack(w, r, "/namespaces/"+name)
}

// --- Tool Permissions ---

// handleNamespacePermission sets or unsets a tool permission for a namespace.
// Form fields: action ("set" or "unset"), server, tool, enabled ("true"/"false" for set).
func (s *Server) handleNamespacePermission(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	action := r.FormValue("action")
	server := r.FormValue("server")
	tool := strings.TrimSpace(r.FormValue("tool"))

	err := s.mutateConfig(func(cfg *config.Config) error {
		if _, ok := cfg.GetNamespace(name); !ok {
			return fmt.Errorf("namespace %q not found", name)
		}
		switch action {
		case "set":
			enabled := r.FormValue("enabled") == "true"
			return cfg.SetToolPermission(name, server, tool, enabled)
		case "unset":
			return cfg.UnsetToolPermission(name, server, tool)
		default:
			return fmt.Errorf("unknown action %q", action)
		}
	})

	if err != nil {
		log.Printf("permission %q: %v", name, err)
	}

	s.redirectBack(w, r, "/namespaces/"+name)
}

// --- Server Default Permissions ---

// handleNamespaceServerDefault sets or unsets the per-server deny default in a namespace.
// Form fields: action ("set" or "unset"), server, deny ("true"/"false" for set).
func (s *Server) handleNamespaceServerDefault(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	action := r.FormValue("action")
	server := r.FormValue("server")

	err := s.mutateConfig(func(cfg *config.Config) error {
		if _, ok := cfg.GetNamespace(name); !ok {
			return fmt.Errorf("namespace %q not found", name)
		}
		switch action {
		case "set":
			deny := r.FormValue("deny") == "true"
			return cfg.SetServerDefault(name, server, deny)
		case "unset":
			return cfg.UnsetServerDefault(name, server)
		default:
			return fmt.Errorf("unknown action %q", action)
		}
	})

	if err != nil {
		log.Printf("server-default %q: %v", name, err)
	}

	s.redirectBack(w, r, "/namespaces/"+name)
}

// --- Helpers ---

// redirectBack sends an HX-Redirect for htmx requests or a 303 redirect otherwise.
func (s *Server) redirectBack(w http.ResponseWriter, r *http.Request, fallback string) {
	if r.Header.Get("HX-Request") == "true" {
		referer := r.Header.Get("HX-Current-URL")
		if referer == "" {
			referer = fallback
		}
		w.Header().Set("HX-Redirect", referer)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, fallback, http.StatusSeeOther)
}
