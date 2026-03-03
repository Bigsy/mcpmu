package registry

// SearchResponse is the top-level API response from the MCP registry.
type SearchResponse struct {
	Servers  []ServerEntry `json:"servers"`
	Metadata Metadata      `json:"metadata"`
}

// Metadata contains pagination/count info.
type Metadata struct {
	Count int `json:"count"`
}

// ServerEntry wraps a server with its registry metadata.
type ServerEntry struct {
	Server Server         `json:"server"`
	Meta   map[string]any `json:"_meta,omitempty"`
}

// Server represents an MCP server in the registry.
type Server struct {
	Name        string     `json:"name"`
	Title       string     `json:"title,omitempty"`
	Description string     `json:"description"`
	Version     string     `json:"version"`
	Repository  Repository `json:"repository"`
	Packages    []Package  `json:"packages,omitempty"`
	Remotes     []Remote   `json:"remotes,omitempty"`
}

// Repository contains source code information.
type Repository struct {
	URL       string `json:"url"`
	Source    string `json:"source"`
	Subfolder string `json:"subfolder,omitempty"`
}

// Package represents a locally-installable package for an MCP server.
type Package struct {
	RegistryType         string            `json:"registryType"`
	RegistryBaseURL      string            `json:"registryBaseUrl,omitempty"`
	Identifier           string            `json:"identifier"`
	Version              string            `json:"version"`
	RuntimeHint          string            `json:"runtimeHint,omitempty"`
	Transport            Transport         `json:"transport"`
	EnvironmentVariables []EnvironmentVar  `json:"environmentVariables,omitempty"`
	PackageArguments     []PackageArgument `json:"packageArguments,omitempty"`
}

// Transport describes how to connect to the MCP server.
type Transport struct {
	Type string `json:"type"` // "stdio", "sse", "streamable-http"
	URL  string `json:"url,omitempty"`
}

// EnvironmentVar describes an environment variable needed by the server.
type EnvironmentVar struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	IsRequired  bool   `json:"isRequired,omitempty"`
	IsSecret    bool   `json:"isSecret,omitempty"`
	Format      string `json:"format,omitempty"`
}

// PackageArgument describes a command-line argument for the package.
type PackageArgument struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	IsRequired  bool   `json:"isRequired,omitempty"`
	Format      string `json:"format,omitempty"`
	Type        string `json:"type,omitempty"` // "named" or empty (positional)
	Default     string `json:"default,omitempty"`
}

// Remote represents a hosted endpoint for an MCP server.
type Remote struct {
	Type    string         `json:"type"` // "streamable-http", "sse"
	URL     string         `json:"url"`
	Headers []RemoteHeader `json:"headers,omitempty"`
}

// RemoteHeader describes an HTTP header needed for a remote endpoint.
type RemoteHeader struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Value       string `json:"value"`
	IsRequired  bool   `json:"isRequired,omitempty"`
	IsSecret    bool   `json:"isSecret,omitempty"`
}
