package web

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/Bigsy/mcpmu/internal/registry"
)

// registryPageData is the template data for the registry browser page.
type registryPageData struct {
	Page       string
	ConfigPath string
	Query      string
	Results    []registryResult
	Error      string
}

// registryResult is a single search result for template rendering.
type registryResult struct {
	Name           string
	Version        string
	Description    string
	RegistryType   string // npm, pypi, etc.
	Transport      string // stdio, sse, streamable-http
	IsOfficial     bool
	EnvVars        []registry.EnvironmentVar
	InstallPreview bool // true if we have an install spec to show
	Spec           registryInstallSpec
	InstallURL     string // /servers/add?... with pre-populated form fields
}

// registryInstallSpec is the install spec data for template rendering.
type registryInstallSpec struct {
	CommandOrURL      string
	Args              string
	Env               map[string]string
	BearerTokenEnvVar string
	Headers           map[string]string // required HTTP headers (non-bearer) — informational only
	IsHTTP            bool
}

// handleRegistryPage renders the registry browser page.
// With no query param, shows the empty search state.
// With ?q=..., performs a search and renders results.
func (s *Server) handleRegistryPage(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))

	data := registryPageData{
		Page:       "registry",
		ConfigPath: s.configPathDisplay(),
		Query:      query,
	}

	if query != "" {
		results, err := s.registry.Search(r.Context(), query)
		if err != nil {
			data.Error = err.Error()
		} else {
			data.Results = buildRegistryResults(results)
		}
	}

	s.render(w, "registry.html", data)
}

// handleFragmentRegistryResults returns the registry results HTML fragment for htmx.
func (s *Server) handleFragmentRegistryResults(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))

	data := registryPageData{
		Query: query,
	}

	if query != "" {
		results, err := s.registry.Search(r.Context(), query)
		if err != nil {
			data.Error = err.Error()
		} else {
			data.Results = buildRegistryResults(results)
		}
	}

	s.renderFragment(w, "registry_results", data)
}

// handleAPIRegistrySearch is the JSON API for searching the registry.
func (s *Server) handleAPIRegistrySearch(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" {
		jsonError(w, "query parameter 'q' is required", http.StatusBadRequest)
		return
	}

	results, err := s.registry.Search(r.Context(), query)
	if err != nil {
		jsonError(w, err.Error(), http.StatusBadGateway)
		return
	}

	type apiResult struct {
		Name        string                `json:"name"`
		Title       string                `json:"title,omitempty"`
		Description string                `json:"description"`
		Version     string                `json:"version"`
		InstallSpec *registry.InstallSpec `json:"installSpec,omitempty"`
	}

	apiResults := make([]apiResult, 0, len(results))
	for _, srv := range results {
		ar := apiResult{
			Name:        srv.Name,
			Title:       srv.Title,
			Description: srv.Description,
			Version:     srv.Version,
		}
		pkg, remote := registry.SelectBestPackage(srv)
		if pkg != nil || remote != nil {
			spec := registry.BuildInstallSpec(srv, pkg, remote)
			ar.InstallSpec = &spec
		}
		apiResults = append(apiResults, ar)
	}

	jsonOK(w, apiResults)
}

// buildRegistryResults converts raw registry search results to template-friendly structs.
func buildRegistryResults(servers []registry.Server) []registryResult {
	results := make([]registryResult, 0, len(servers))
	for _, srv := range servers {
		pkg, remote := registry.SelectBestPackage(srv)

		r := registryResult{
			Name:        srv.Name,
			Version:     srv.Version,
			Description: srv.Description,
			IsOfficial:  strings.HasPrefix(srv.Name, "@modelcontextprotocol/"),
		}

		if pkg != nil {
			r.RegistryType = pkg.RegistryType
			r.Transport = pkg.Transport.Type
			r.EnvVars = pkg.EnvironmentVariables
		} else if remote != nil {
			r.Transport = remote.Type
		}

		if pkg != nil || remote != nil {
			spec := registry.BuildInstallSpec(srv, pkg, remote)
			r.InstallPreview = spec.CommandOrURL != ""
			r.Spec = registryInstallSpec{
				CommandOrURL:      spec.CommandOrURL,
				Args:              spec.Args,
				Env:               spec.Env,
				BearerTokenEnvVar: spec.BearerTokenEnvVar,
				Headers:           spec.Headers,
				IsHTTP:            remote != nil,
			}
			r.InstallURL = buildInstallURL(spec, remote != nil)
		} else {
			// No install spec — link to manual add
			r.InstallURL = "/servers/add"
		}

		results = append(results, r)
	}
	return results
}

// buildInstallURL constructs /servers/add?... with query params to pre-populate the form.
func buildInstallURL(spec registry.InstallSpec, isHTTP bool) string {
	params := url.Values{}
	params.Set("from", "registry")
	if spec.Name != "" {
		params.Set("name", spec.Name)
	}
	if isHTTP {
		params.Set("kind", "http")
		params.Set("url", spec.CommandOrURL)
		if spec.BearerTokenEnvVar != "" {
			params.Set("bearer_env", spec.BearerTokenEnvVar)
		}
	} else {
		params.Set("kind", "stdio")
		params.Set("command", spec.CommandOrURL)
		if spec.Args != "" {
			params.Set("args", spec.Args)
		}
	}
	// Encode env vars as key=val pairs
	for k, v := range spec.Env {
		params.Add("env", fmt.Sprintf("%s=%s", k, v))
	}
	return "/servers/add?" + params.Encode()
}
