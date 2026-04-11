package web

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/Bigsy/mcpmu/internal/config"
)

// --- JSON helpers ---

func jsonError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = fmt.Fprintf(w, `{"error":%q}`, msg)
}

func jsonOK(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(data); err != nil {
		log.Printf("json encode: %v", err)
	}
}

// --- Server API ---

// apiServer is the JSON representation of a server for API responses.
type apiServer struct {
	Name   string              `json:"name"`
	Config config.ServerConfig `json:"config"`
	Status string              `json:"status,omitempty"`
}

func (s *Server) handleAPIListServers(w http.ResponseWriter, r *http.Request) {
	entries := s.cfg.ServerEntries()
	statuses := s.status.All()

	servers := make([]apiServer, 0, len(entries))
	for _, e := range entries {
		as := apiServer{Name: e.Name, Config: e.Config}
		if st, ok := statuses[e.Name]; ok {
			as.Status = st.State.String()
		}
		servers = append(servers, as)
	}

	jsonOK(w, servers)
}

func (s *Server) handleAPIGetServer(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	srv, ok := s.cfg.GetServer(name)
	if !ok {
		jsonError(w, fmt.Sprintf("server %q not found", name), http.StatusNotFound)
		return
	}

	as := apiServer{Name: name, Config: srv}
	if st, ok := s.status.Get(name); ok {
		as.Status = st.State.String()
	}

	jsonOK(w, as)
}

// apiCreateServerRequest is the JSON body for creating a server.
type apiCreateServerRequest struct {
	Name   string              `json:"name"`
	Config config.ServerConfig `json:"config"`
}

func (s *Server) handleAPICreateServer(w http.ResponseWriter, r *http.Request) {
	var req apiCreateServerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		jsonError(w, "name is required", http.StatusBadRequest)
		return
	}

	err := s.mutateConfig(func(cfg *config.Config) error {
		return cfg.AddServer(name, req.Config)
	})

	if err != nil {
		code := http.StatusUnprocessableEntity
		if strings.Contains(err.Error(), "already exists") {
			code = http.StatusConflict
		}
		jsonError(w, err.Error(), code)
		return
	}

	w.WriteHeader(http.StatusCreated)
	jsonOK(w, apiServer{Name: name, Config: req.Config})
}

// apiUpdateServerRequest is the JSON body for updating a server.
// Only included fields are applied (partial update). Uses json.RawMessage
// to distinguish absent fields from zero values.
type apiUpdateServerRequest struct {
	Name        *string   `json:"name,omitempty"`        // rename
	Enabled     *bool     `json:"enabled,omitempty"`     // enable/disable
	DeniedTools *[]string `json:"deniedTools,omitempty"` // replace denied tools
	Autostart   *bool     `json:"autostart,omitempty"`
	Command     *string   `json:"command,omitempty"`
	Args        *[]string `json:"args,omitempty"`
	Cwd         *string   `json:"cwd,omitempty"`
	URL         *string   `json:"url,omitempty"`
}

func (s *Server) handleAPIUpdateServer(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	var req apiUpdateServerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	err := s.mutateConfig(func(cfg *config.Config) error {
		srv, ok := cfg.GetServer(name)
		if !ok {
			return fmt.Errorf("server %q not found", name)
		}

		// Apply only the fields that are present in the request
		if req.Enabled != nil {
			srv.SetEnabled(*req.Enabled)
		}
		if req.DeniedTools != nil {
			srv.DeniedTools = *req.DeniedTools
		}
		if req.Autostart != nil {
			srv.Autostart = *req.Autostart
		}
		if req.Command != nil {
			srv.Command = *req.Command
		}
		if req.Args != nil {
			srv.Args = *req.Args
		}
		if req.Cwd != nil {
			srv.Cwd = *req.Cwd
		}
		if req.URL != nil {
			srv.URL = *req.URL
		}

		// Validate and save the merged config
		if err := srv.Validate(); err != nil {
			return fmt.Errorf("invalid server config: %w", err)
		}
		cfg.Servers[name] = srv

		// Rename last (changes the key)
		if req.Name != nil && *req.Name != name {
			return cfg.RenameServer(name, *req.Name)
		}

		return nil
	})

	if err != nil {
		code := http.StatusUnprocessableEntity
		if strings.Contains(err.Error(), "not found") {
			code = http.StatusNotFound
		} else if strings.Contains(err.Error(), "already exists") {
			code = http.StatusConflict
		}
		jsonError(w, err.Error(), code)
		return
	}

	// Return the updated server
	finalName := name
	if req.Name != nil {
		finalName = *req.Name
	}
	srv, _ := s.cfg.GetServer(finalName)
	jsonOK(w, apiServer{Name: finalName, Config: srv})
}

func (s *Server) handleAPIDeleteServer(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	// Stop if running
	if handle := s.supervisor.Get(name); handle != nil {
		if err := s.supervisor.Stop(name); err != nil {
			log.Printf("stop server %q before delete: %v", name, err)
		}
	}

	err := s.mutateConfig(func(cfg *config.Config) error {
		return cfg.DeleteServer(name)
	})

	if err != nil {
		code := http.StatusInternalServerError
		if strings.Contains(err.Error(), "not found") {
			code = http.StatusNotFound
		}
		jsonError(w, err.Error(), code)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// --- Namespace API ---

// apiNamespace is the JSON representation of a namespace for API responses.
type apiNamespace struct {
	Name      string                 `json:"name"`
	Config    config.NamespaceConfig `json:"config"`
	IsDefault bool                   `json:"isDefault"`
}

func (s *Server) handleAPIListNamespaces(w http.ResponseWriter, r *http.Request) {
	entries := s.cfg.NamespaceEntries()
	namespaces := make([]apiNamespace, 0, len(entries))
	for _, e := range entries {
		namespaces = append(namespaces, apiNamespace{
			Name:      e.Name,
			Config:    e.Config,
			IsDefault: e.Name == s.cfg.DefaultNamespace,
		})
	}
	jsonOK(w, namespaces)
}

func (s *Server) handleAPIGetNamespace(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ns, ok := s.cfg.GetNamespace(name)
	if !ok {
		jsonError(w, fmt.Sprintf("namespace %q not found", name), http.StatusNotFound)
		return
	}

	result := apiNamespace{
		Name:      name,
		Config:    ns,
		IsDefault: name == s.cfg.DefaultNamespace,
	}

	jsonOK(w, result)
}

// apiCreateNamespaceRequest is the JSON body for creating a namespace.
type apiCreateNamespaceRequest struct {
	Name   string                 `json:"name"`
	Config config.NamespaceConfig `json:"config"`
}

func (s *Server) handleAPICreateNamespace(w http.ResponseWriter, r *http.Request) {
	var req apiCreateNamespaceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		jsonError(w, "name is required", http.StatusBadRequest)
		return
	}

	err := s.mutateConfig(func(cfg *config.Config) error {
		return cfg.AddNamespace(name, req.Config)
	})

	if err != nil {
		code := http.StatusUnprocessableEntity
		if strings.Contains(err.Error(), "already exists") {
			code = http.StatusConflict
		}
		jsonError(w, err.Error(), code)
		return
	}

	w.WriteHeader(http.StatusCreated)
	jsonOK(w, apiNamespace{Name: name, Config: req.Config})
}

// apiUpdateNamespaceRequest is the JSON body for updating a namespace.
type apiUpdateNamespaceRequest struct {
	Name           *string                  `json:"name,omitempty"`           // rename
	Default        *bool                    `json:"default,omitempty"`        // set as default
	ServerIDs      *[]string                `json:"serverIds,omitempty"`      // replace servers
	Permissions    *[]config.ToolPermission `json:"permissions,omitempty"`    // replace permissions
	ServerDefaults map[string]bool          `json:"serverDefaults,omitempty"` // replace server defaults
	Description    *string                  `json:"description,omitempty"`
	DenyByDefault  *bool                    `json:"denyByDefault,omitempty"`
}

func (s *Server) handleAPIUpdateNamespace(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	var req apiUpdateNamespaceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	err := s.mutateConfig(func(cfg *config.Config) error {
		ns, ok := cfg.GetNamespace(name)
		if !ok {
			return fmt.Errorf("namespace %q not found", name)
		}

		if req.Description != nil {
			ns.Description = *req.Description
		}
		if req.DenyByDefault != nil {
			ns.DenyByDefault = *req.DenyByDefault
		}
		if req.ServerIDs != nil {
			ns.ServerIDs = *req.ServerIDs
		}
		if req.ServerDefaults != nil {
			ns.ServerDefaults = req.ServerDefaults
		}
		cfg.Namespaces[name] = ns

		// Replace permissions for this namespace
		if req.Permissions != nil {
			// Remove existing permissions for this namespace
			filtered := make([]config.ToolPermission, 0, len(cfg.ToolPermissions))
			for _, tp := range cfg.ToolPermissions {
				if tp.Namespace != name {
					filtered = append(filtered, tp)
				}
			}
			// Add new permissions
			for _, tp := range *req.Permissions {
				tp.Namespace = name
				filtered = append(filtered, tp)
			}
			cfg.ToolPermissions = filtered
		}

		// Set as default namespace
		if req.Default != nil {
			if *req.Default {
				cfg.DefaultNamespace = name
			} else if cfg.DefaultNamespace == name {
				cfg.DefaultNamespace = ""
			}
		}

		// Rename last
		if req.Name != nil && *req.Name != name {
			return cfg.RenameNamespace(name, *req.Name)
		}

		return nil
	})

	if err != nil {
		code := http.StatusUnprocessableEntity
		if strings.Contains(err.Error(), "not found") {
			code = http.StatusNotFound
		} else if strings.Contains(err.Error(), "already exists") {
			code = http.StatusConflict
		}
		jsonError(w, err.Error(), code)
		return
	}

	finalName := name
	if req.Name != nil {
		finalName = *req.Name
	}
	ns, _ := s.cfg.GetNamespace(finalName)
	jsonOK(w, apiNamespace{
		Name:      finalName,
		Config:    ns,
		IsDefault: finalName == s.cfg.DefaultNamespace,
	})
}

func (s *Server) handleAPIDeleteNamespace(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	err := s.mutateConfig(func(cfg *config.Config) error {
		return cfg.DeleteNamespace(name)
	})

	if err != nil {
		code := http.StatusInternalServerError
		if strings.Contains(err.Error(), "not found") {
			code = http.StatusNotFound
		}
		jsonError(w, err.Error(), code)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// --- Config export/import ---

func (s *Server) handleAPIExportConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", "attachment; filename=mcpmu-config.json")
	if err := json.NewEncoder(w).Encode(s.cfg); err != nil {
		log.Printf("export config: %v", err)
	}
}

// importPreview holds the result of a config import preview.
type importPreview struct {
	Servers    importDiff `json:"servers"`
	Namespaces importDiff `json:"namespaces"`
}

type importDiff struct {
	Added   []string `json:"added,omitempty"`
	Removed []string `json:"removed,omitempty"`
	Changed []string `json:"changed,omitempty"`
}

func (s *Server) handleAPIImportConfig(w http.ResponseWriter, r *http.Request) {
	var incoming config.Config
	if err := json.NewDecoder(r.Body).Decode(&incoming); err != nil {
		jsonError(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	if err := incoming.Validate(); err != nil {
		jsonError(w, "invalid config: "+err.Error(), http.StatusUnprocessableEntity)
		return
	}

	current := s.cfg
	preview := importPreview{}

	// Diff servers
	for name := range incoming.Servers {
		if _, ok := current.Servers[name]; !ok {
			preview.Servers.Added = append(preview.Servers.Added, name)
		} else {
			preview.Servers.Changed = append(preview.Servers.Changed, name)
		}
	}
	for name := range current.Servers {
		if _, ok := incoming.Servers[name]; !ok {
			preview.Servers.Removed = append(preview.Servers.Removed, name)
		}
	}

	// Diff namespaces
	for name := range incoming.Namespaces {
		if _, ok := current.Namespaces[name]; !ok {
			preview.Namespaces.Added = append(preview.Namespaces.Added, name)
		} else {
			preview.Namespaces.Changed = append(preview.Namespaces.Changed, name)
		}
	}
	for name := range current.Namespaces {
		if _, ok := incoming.Namespaces[name]; !ok {
			preview.Namespaces.Removed = append(preview.Namespaces.Removed, name)
		}
	}

	jsonOK(w, preview)
}

func (s *Server) handleAPIImportApply(w http.ResponseWriter, r *http.Request) {
	var incoming config.Config
	if err := json.NewDecoder(r.Body).Decode(&incoming); err != nil {
		jsonError(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	if err := incoming.Validate(); err != nil {
		jsonError(w, "invalid config: "+err.Error(), http.StatusUnprocessableEntity)
		return
	}

	err := s.mutateConfig(func(cfg *config.Config) error {
		// Replace the entire config with the imported one.
		// Preserve nothing from the current config — the import is a full replacement.
		// SaveTo will update LastModified automatically.
		*cfg = incoming
		return nil
	})

	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	jsonOK(w, map[string]string{"status": "ok"})
}
