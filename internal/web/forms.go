package web

import (
	"fmt"
	"log"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/events"
)

// envPair is a key-value pair for template rendering of environment variables.
type envPair struct {
	Key string
	Val string
}

// serverFormData holds the pre-filled values for the server form.
type serverFormData struct {
	Name              string
	IsHTTP            bool
	Command           string
	Args              string // space-separated
	Cwd               string
	URL               string
	AuthMode          string // "none", "bearer", "oauth"
	BearerEnv         string
	OAuthClientID     string
	OAuthCallbackPort string
	OAuthScopes       string
	EnvPairs          []envPair
	Enabled           bool
	Autostart         bool
	StartupTimeout    int
	ToolTimeout       int
}

// serverFormPageData is the template data for the server add/edit form page.
type serverFormPageData struct {
	Page       string
	ConfigPath string
	IsEdit     bool
	Name       string
	Action     string
	Form       serverFormData
	Error      string
	Notice     string
}

// newServerFormData creates default form data for a new server.
func newServerFormData() serverFormData {
	return serverFormData{
		AuthMode:       "none",
		Enabled:        true,
		StartupTimeout: 10,
		ToolTimeout:    60,
	}
}

// serverFormDataFromConfig populates form data from an existing server config.
func serverFormDataFromConfig(name string, srv config.ServerConfig) serverFormData {
	fd := serverFormData{
		Name:           name,
		IsHTTP:         srv.IsHTTP(),
		Command:        srv.Command,
		Args:           strings.Join(srv.Args, " "),
		Cwd:            srv.Cwd,
		URL:            srv.URL,
		Enabled:        srv.IsEnabled(),
		Autostart:      srv.Autostart,
		StartupTimeout: srv.StartupTimeout(),
		ToolTimeout:    srv.ToolTimeout(),
	}

	// Auth mode
	if srv.OAuth != nil {
		fd.AuthMode = "oauth"
		fd.OAuthClientID = srv.OAuth.ClientID
		fd.OAuthScopes = strings.Join(srv.OAuth.Scopes, ",")
		if srv.OAuth.CallbackPort != nil {
			fd.OAuthCallbackPort = strconv.Itoa(*srv.OAuth.CallbackPort)
		}
	} else if srv.BearerTokenEnvVar != "" {
		fd.AuthMode = "bearer"
		fd.BearerEnv = srv.BearerTokenEnvVar
	} else {
		fd.AuthMode = "none"
	}

	// Env vars
	for k, v := range srv.Env {
		fd.EnvPairs = append(fd.EnvPairs, envPair{Key: k, Val: v})
	}
	slices.SortFunc(fd.EnvPairs, func(a, b envPair) int {
		return strings.Compare(a.Key, b.Key)
	})

	return fd
}

// parseServerForm extracts form values from the request and builds a serverFormData.
func parseServerForm(r *http.Request) serverFormData {
	return serverFormData{
		Name:              r.FormValue("name"),
		IsHTTP:            r.FormValue("kind") == "http",
		Command:           strings.TrimSpace(r.FormValue("command")),
		Args:              strings.TrimSpace(r.FormValue("args")),
		Cwd:               strings.TrimSpace(r.FormValue("cwd")),
		URL:               strings.TrimSpace(r.FormValue("url")),
		AuthMode:          r.FormValue("auth_mode"),
		BearerEnv:         strings.TrimSpace(r.FormValue("bearer_env")),
		OAuthClientID:     strings.TrimSpace(r.FormValue("oauth_client_id")),
		OAuthCallbackPort: strings.TrimSpace(r.FormValue("oauth_callback_port")),
		OAuthScopes:       strings.TrimSpace(r.FormValue("oauth_scopes")),
		Enabled:           formChecked(r, "enabled"),
		Autostart:         formChecked(r, "autostart"),
		StartupTimeout:    atoi(r.FormValue("startup_timeout"), 10),
		ToolTimeout:       atoi(r.FormValue("tool_timeout"), 60),
		EnvPairs:          parseEnvPairs(r),
	}
}

// formChecked returns true if "true" appears in any of the form values for key.
// This handles the hidden-input-before-checkbox pattern where the browser sends
// both "false" (hidden) and "true" (checkbox) when checked, but only "false" when unchecked.
// Callers must call r.ParseForm() before calling this.
func formChecked(r *http.Request, key string) bool {
	return slices.Contains(r.Form[key], "true")
}

// parseEnvPairs extracts env key/value pairs from form data.
func parseEnvPairs(r *http.Request) []envPair {
	keys := r.Form["env_key"]
	vals := r.Form["env_val"]
	var pairs []envPair
	for i := range keys {
		k := strings.TrimSpace(keys[i])
		if k == "" {
			continue
		}
		v := ""
		if i < len(vals) {
			v = vals[i]
		}
		pairs = append(pairs, envPair{Key: k, Val: v})
	}
	return pairs
}

// buildServerConfig constructs a ServerConfig from form data.
// For edit mode, it merges into the existing config to preserve fields not exposed in the form.
func buildServerConfig(fd serverFormData, existing *config.ServerConfig) config.ServerConfig {
	var srv config.ServerConfig
	if existing != nil {
		srv = *existing // start from existing to preserve unexposed fields
	}

	srv.SetEnabled(fd.Enabled)
	srv.Autostart = fd.Autostart
	srv.StartupTimeoutSec = fd.StartupTimeout
	srv.ToolTimeoutSec = fd.ToolTimeout

	// Env vars
	if len(fd.EnvPairs) > 0 {
		srv.Env = make(map[string]string, len(fd.EnvPairs))
		for _, kv := range fd.EnvPairs {
			srv.Env[kv.Key] = kv.Val
		}
	} else {
		srv.Env = nil
	}

	if fd.IsHTTP {
		// HTTP server
		srv.Command = ""
		srv.Args = nil
		srv.Cwd = ""
		srv.URL = fd.URL

		switch fd.AuthMode {
		case "bearer":
			srv.BearerTokenEnvVar = fd.BearerEnv
			srv.OAuth = nil
		case "oauth":
			srv.BearerTokenEnvVar = ""
			oauth := &config.OAuthConfig{
				ClientID: fd.OAuthClientID,
			}
			if fd.OAuthScopes != "" {
				oauth.Scopes = strings.Split(fd.OAuthScopes, ",")
				for i := range oauth.Scopes {
					oauth.Scopes[i] = strings.TrimSpace(oauth.Scopes[i])
				}
			}
			if fd.OAuthCallbackPort != "" {
				if port, err := strconv.Atoi(fd.OAuthCallbackPort); err == nil {
					oauth.CallbackPort = &port
				}
			}
			srv.OAuth = oauth
		default:
			srv.BearerTokenEnvVar = ""
			srv.OAuth = nil
		}
	} else {
		// Stdio server
		srv.URL = ""
		srv.BearerTokenEnvVar = ""
		srv.OAuth = nil
		srv.Command = fd.Command
		srv.Cwd = fd.Cwd
		if fd.Args != "" {
			srv.Args = strings.Fields(fd.Args)
		} else {
			srv.Args = nil
		}
	}

	return srv
}

// handleServerAddPage renders the add server form.
func (s *Server) handleServerAddPage(w http.ResponseWriter, r *http.Request) {
	data := serverFormPageData{
		Page:       "servers",
		ConfigPath: s.configPathDisplay(),
		Action:     "/servers",
		Form:       newServerFormData(),
	}
	s.render(w, "server_form.html", data)
}

// handleServerEditPage renders the edit server form.
func (s *Server) handleServerEditPage(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	srv, ok := s.cfg.GetServer(name)
	if !ok {
		http.NotFound(w, r)
		return
	}

	isRunning := false
	if st, ok := s.status.Get(name); ok {
		isRunning = st.State == events.StateRunning
	}

	data := serverFormPageData{
		Page:       "servers",
		ConfigPath: s.configPathDisplay(),
		IsEdit:     true,
		Name:       name,
		Action:     "/servers/" + name + "/edit",
		Form:       serverFormDataFromConfig(name, srv),
	}

	if isRunning {
		data.Notice = "This server is running. Restart it to apply changes."
	}

	s.render(w, "server_form.html", data)
}

// handleServerCreate processes the add server form submission.
func (s *Server) handleServerCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	fd := parseServerForm(r)
	name := strings.TrimSpace(fd.Name)

	if name == "" {
		s.renderServerFormError(w, fd, false, "", "Server name is required.")
		return
	}

	srv := buildServerConfig(fd, nil)

	err := s.mutateConfig(func(cfg *config.Config) error {
		return cfg.AddServer(name, srv)
	})

	if err != nil {
		s.renderServerFormError(w, fd, false, "", err.Error())
		return
	}

	http.Redirect(w, r, "/servers/"+name, http.StatusSeeOther)
}

// handleServerUpdate processes the edit server form submission.
func (s *Server) handleServerUpdate(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	fd := parseServerForm(r)
	fd.Name = name // preserve original name

	err := s.mutateConfig(func(cfg *config.Config) error {
		existing, ok := cfg.GetServer(name)
		if !ok {
			return fmt.Errorf("server %q not found", name)
		}
		srv := buildServerConfig(fd, &existing)
		return cfg.UpdateServer(name, srv)
	})

	if err != nil {
		s.renderServerFormError(w, fd, true, name, err.Error())
		return
	}

	http.Redirect(w, r, "/servers/"+name, http.StatusSeeOther)
}

// handleServerDelete processes a server deletion request.
func (s *Server) handleServerDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	// Stop the server if running
	if handle := s.supervisor.Get(name); handle != nil {
		if err := s.supervisor.Stop(name); err != nil {
			log.Printf("stop server %q before delete: %v", name, err)
		}
	}

	err := s.mutateConfig(func(cfg *config.Config) error {
		return cfg.DeleteServer(name)
	})

	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Support htmx and regular requests
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", "/servers")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/servers", http.StatusSeeOther)
}

// handleServerToggle toggles a server's enabled state.
func (s *Server) handleServerToggle(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	err := s.mutateConfig(func(cfg *config.Config) error {
		srv, ok := cfg.GetServer(name)
		if !ok {
			return fmt.Errorf("server %q not found", name)
		}
		srv.SetEnabled(!srv.IsEnabled())
		cfg.Servers[name] = srv
		return nil
	})

	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Redirect back to where they came from
	if r.Header.Get("HX-Request") == "true" {
		// For htmx, redirect to trigger a full page refresh
		referer := r.Header.Get("HX-Current-URL")
		if referer == "" {
			referer = "/servers"
		}
		w.Header().Set("HX-Redirect", referer)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/servers", http.StatusSeeOther)
}

// renderServerFormError re-renders the server form with an error message.
func (s *Server) renderServerFormError(w http.ResponseWriter, fd serverFormData, isEdit bool, name string, errMsg string) {
	action := "/servers"
	if isEdit {
		action = "/servers/" + name + "/edit"
	}
	data := serverFormPageData{
		Page:       "servers",
		ConfigPath: s.configPathDisplay(),
		IsEdit:     isEdit,
		Name:       name,
		Action:     action,
		Form:       fd,
		Error:      errMsg,
	}
	w.WriteHeader(http.StatusUnprocessableEntity)
	s.render(w, "server_form.html", data)
}

// --- Namespace form ---

// namespaceFormData holds the pre-filled values for the namespace form.
type namespaceFormData struct {
	Name          string
	Description   string
	DenyByDefault bool
}

// namespaceFormPageData is the template data for the namespace add/edit form page.
type namespaceFormPageData struct {
	Page             string
	ConfigPath       string
	IsEdit           bool
	Name             string
	Action           string
	Form             namespaceFormData
	Error            string
	AssignedServers  []string
	AvailableServers []string
}

// handleNamespaceAddPage renders the add namespace form.
func (s *Server) handleNamespaceAddPage(w http.ResponseWriter, r *http.Request) {
	data := namespaceFormPageData{
		Page:       "namespaces",
		ConfigPath: s.configPathDisplay(),
		Action:     "/namespaces",
	}
	s.render(w, "namespace_form.html", data)
}

// handleNamespaceEditPage renders the edit namespace form.
func (s *Server) handleNamespaceEditPage(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ns, ok := s.cfg.GetNamespace(name)
	if !ok {
		http.NotFound(w, r)
		return
	}

	// Build lists of assigned and available servers
	assignedSet := make(map[string]bool, len(ns.ServerIDs))
	for _, sid := range ns.ServerIDs {
		assignedSet[sid] = true
	}

	var available []string
	for _, entry := range s.cfg.ServerEntries() {
		if !assignedSet[entry.Name] {
			available = append(available, entry.Name)
		}
	}

	data := namespaceFormPageData{
		Page:       "namespaces",
		ConfigPath: s.configPathDisplay(),
		IsEdit:     true,
		Name:       name,
		Action:     "/namespaces/" + name + "/edit",
		Form: namespaceFormData{
			Name:          name,
			Description:   ns.Description,
			DenyByDefault: ns.DenyByDefault,
		},
		AssignedServers:  ns.ServerIDs,
		AvailableServers: available,
	}

	s.render(w, "namespace_form.html", data)
}

// handleNamespaceCreate processes the add namespace form.
func (s *Server) handleNamespaceCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		s.renderNamespaceFormError(w, namespaceFormData{}, false, "", "Namespace name is required.")
		return
	}

	ns := config.NamespaceConfig{
		Description:   strings.TrimSpace(r.FormValue("description")),
		DenyByDefault: formChecked(r, "deny_by_default"),
		ServerIDs:     []string{},
	}

	err := s.mutateConfig(func(cfg *config.Config) error {
		return cfg.AddNamespace(name, ns)
	})

	if err != nil {
		fd := namespaceFormData{
			Name:          name,
			Description:   ns.Description,
			DenyByDefault: ns.DenyByDefault,
		}
		s.renderNamespaceFormError(w, fd, false, "", err.Error())
		return
	}

	http.Redirect(w, r, "/namespaces/"+name, http.StatusSeeOther)
}

// handleNamespaceUpdate processes the edit namespace form.
func (s *Server) handleNamespaceUpdate(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	err := s.mutateConfig(func(cfg *config.Config) error {
		ns, ok := cfg.GetNamespace(name)
		if !ok {
			return fmt.Errorf("namespace %q not found", name)
		}
		ns.Description = strings.TrimSpace(r.FormValue("description"))
		ns.DenyByDefault = formChecked(r, "deny_by_default")
		return cfg.UpdateNamespace(name, ns)
	})

	if err != nil {
		fd := namespaceFormData{
			Name:          name,
			Description:   r.FormValue("description"),
			DenyByDefault: formChecked(r, "deny_by_default"),
		}
		s.renderNamespaceFormError(w, fd, true, name, err.Error())
		return
	}

	http.Redirect(w, r, "/namespaces/"+name, http.StatusSeeOther)
}

// handleNamespaceDelete processes a namespace deletion request.
func (s *Server) handleNamespaceDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	err := s.mutateConfig(func(cfg *config.Config) error {
		return cfg.DeleteNamespace(name)
	})

	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", "/namespaces")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/namespaces", http.StatusSeeOther)
}

// handleNamespaceAssign assigns a server to a namespace.
func (s *Server) handleNamespaceAssign(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	serverName := r.FormValue("assign_server")
	if serverName == "" {
		http.Error(w, "No server selected", http.StatusBadRequest)
		return
	}

	err := s.mutateConfig(func(cfg *config.Config) error {
		return cfg.AssignServerToNamespace(name, serverName)
	})

	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", "/namespaces/"+name+"/edit")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/namespaces/"+name+"/edit", http.StatusSeeOther)
}

// handleNamespaceUnassign removes a server from a namespace.
func (s *Server) handleNamespaceUnassign(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	serverName := r.FormValue("server")
	if serverName == "" {
		http.Error(w, "No server specified", http.StatusBadRequest)
		return
	}

	err := s.mutateConfig(func(cfg *config.Config) error {
		return cfg.UnassignServerFromNamespace(name, serverName)
	})

	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", "/namespaces/"+name+"/edit")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/namespaces/"+name+"/edit", http.StatusSeeOther)
}

// renderNamespaceFormError re-renders the namespace form with an error message.
func (s *Server) renderNamespaceFormError(w http.ResponseWriter, fd namespaceFormData, isEdit bool, name string, errMsg string) {
	action := "/namespaces"
	if isEdit {
		action = "/namespaces/" + name + "/edit"
	}
	data := namespaceFormPageData{
		Page:       "namespaces",
		ConfigPath: s.configPathDisplay(),
		IsEdit:     isEdit,
		Name:       name,
		Action:     action,
		Form:       fd,
		Error:      errMsg,
	}
	w.WriteHeader(http.StatusUnprocessableEntity)
	s.render(w, "namespace_form.html", data)
}

// atoi converts a string to int with a default fallback.
func atoi(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}
