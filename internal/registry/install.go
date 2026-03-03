package registry

import (
	"fmt"
	"strings"
)

// InstallSpec contains the pre-populated values for the server add form.
type InstallSpec struct {
	Name              string            // Derived short name for the server
	CommandOrURL      string            // Command (stdio) or URL (HTTP)
	Args              string            // Space-separated arguments
	Env               map[string]string // Environment variables with placeholders
	BearerTokenEnvVar string            // Bearer token env var for HTTP servers
}

// SelectBestPackage chooses the best installable option from a server.
// Returns a package for local install, or a remote for hosted endpoints.
// Returns nil, nil if no supported option is found.
func SelectBestPackage(srv Server) (*Package, *Remote) {
	// Prefer stdio packages
	var stdioNPM, stdioPyPI, stdioOther *Package
	for i := range srv.Packages {
		pkg := &srv.Packages[i]
		if pkg.Transport.Type != "stdio" {
			continue
		}
		switch pkg.RegistryType {
		case "npm":
			if stdioNPM == nil {
				stdioNPM = pkg
			}
		case "pypi":
			if stdioPyPI == nil {
				stdioPyPI = pkg
			}
		default:
			if stdioOther == nil {
				stdioOther = pkg
			}
		}
	}

	if stdioNPM != nil {
		return stdioNPM, nil
	}
	if stdioPyPI != nil {
		return stdioPyPI, nil
	}
	if stdioOther != nil {
		return stdioOther, nil
	}

	// Fall back to first remote with streamable-http or sse
	for i := range srv.Remotes {
		remote := &srv.Remotes[i]
		if remote.Type == "streamable-http" || remote.Type == "sse" {
			return nil, remote
		}
	}

	return nil, nil
}

// BuildInstallSpec converts a server and selected package into form defaults.
// If pkg is non-nil, builds a local install spec. If remote is non-nil, builds an HTTP spec.
// Returns an empty InstallSpec (CommandOrURL == "") if neither is provided.
func BuildInstallSpec(srv Server, pkg *Package, remote *Remote) InstallSpec {
	name := DeriveName(srv)

	if pkg != nil {
		return buildPackageSpec(name, pkg)
	}
	if remote != nil {
		return buildRemoteSpec(name, remote)
	}
	return InstallSpec{}
}

func buildPackageSpec(name string, pkg *Package) InstallSpec {
	spec := InstallSpec{Name: name}

	// Determine command from runtime hint or registry type defaults
	switch pkg.RegistryType {
	case "npm":
		spec.CommandOrURL = defaultIfEmpty(pkg.RuntimeHint, "npx")
		spec.Args = buildNPMArgs(pkg)
	case "pypi":
		spec.CommandOrURL = defaultIfEmpty(pkg.RuntimeHint, "uvx")
		spec.Args = buildPyPIArgs(pkg)
	default:
		spec.CommandOrURL = defaultIfEmpty(pkg.RuntimeHint, pkg.Identifier)
		spec.Args = buildGenericArgs(pkg)
	}

	spec.Env = buildEnvMap(pkg.EnvironmentVariables)
	return spec
}

func buildNPMArgs(pkg *Package) string {
	parts := []string{"-y", pkg.Identifier}
	parts = append(parts, buildArgStrings(pkg.PackageArguments)...)
	return strings.Join(parts, " ")
}

func buildPyPIArgs(pkg *Package) string {
	parts := []string{pkg.Identifier}
	parts = append(parts, buildArgStrings(pkg.PackageArguments)...)
	return strings.Join(parts, " ")
}

func buildGenericArgs(pkg *Package) string {
	parts := buildArgStrings(pkg.PackageArguments)
	return strings.Join(parts, " ")
}

func buildArgStrings(args []PackageArgument) []string {
	var parts []string
	for _, arg := range args {
		if !arg.IsRequired {
			continue
		}
		value := arg.Default
		if value == "" {
			value = fmt.Sprintf("<%s>", arg.Name)
		}
		if arg.Type == "named" {
			parts = append(parts, fmt.Sprintf("--%s", arg.Name), value)
		} else {
			parts = append(parts, value)
		}
	}
	return parts
}

func buildEnvMap(vars []EnvironmentVar) map[string]string {
	if len(vars) == 0 {
		return nil
	}
	env := make(map[string]string, len(vars))
	for _, v := range vars {
		env[v.Name] = fmt.Sprintf("<your-%s>", v.Name)
	}
	return env
}

func buildRemoteSpec(name string, remote *Remote) InstallSpec {
	spec := InstallSpec{
		Name:         name,
		CommandOrURL: remote.URL,
	}

	// Check for Authorization header with bearer token pattern
	for _, h := range remote.Headers {
		if strings.EqualFold(h.Name, "Authorization") && strings.HasPrefix(h.Value, "Bearer ") {
			// Extract env var name from template like "Bearer {smithery_api_key}"
			token := strings.TrimPrefix(h.Value, "Bearer ")
			token = strings.Trim(token, "{}")
			if token != "" {
				spec.BearerTokenEnvVar = strings.ToUpper(token)
			}
			continue
		}
	}

	// Collect non-Authorization headers as env vars for user to fill
	env := make(map[string]string)
	for _, h := range remote.Headers {
		if strings.EqualFold(h.Name, "Authorization") {
			continue
		}
		if h.IsRequired {
			env[h.Name] = fmt.Sprintf("<your-%s>", h.Name)
		}
	}
	if len(env) > 0 {
		spec.Env = env
	}

	return spec
}

// DeriveName extracts a short, usable name from the server's registry name or title.
func DeriveName(srv Server) string {
	// If title is set and short enough, prefer it
	if srv.Title != "" && len(srv.Title) <= 40 {
		name := strings.ToLower(srv.Title)
		name = strings.ReplaceAll(name, " ", "-")
		// Strip common suffixes
		name = strings.TrimSuffix(name, "-mcp-server")
		name = strings.TrimSuffix(name, "-mcp")
		return name
	}

	// Fall back to last path component of the registry name
	name := srv.Name
	if idx := strings.LastIndex(name, "/"); idx >= 0 {
		name = name[idx+1:]
	}

	// Strip common prefixes/suffixes
	name = strings.ToLower(name)
	name = strings.TrimPrefix(name, "mcp-server-")
	name = strings.TrimPrefix(name, "server-")
	name = strings.TrimSuffix(name, "-mcp-server")
	name = strings.TrimSuffix(name, "-mcp")
	return name
}

func defaultIfEmpty(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
