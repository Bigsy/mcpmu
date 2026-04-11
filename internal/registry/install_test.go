package registry

import (
	"testing"
)

func TestBuildInstallSpec_NPMStdio(t *testing.T) {
	srv := Server{
		Name:  "io.github.brave/brave-search-mcp-server",
		Title: "Brave Search MCP Server",
	}
	pkg := &Package{
		RegistryType: "npm",
		Identifier:   "@brave/brave-search-mcp-server",
		RuntimeHint:  "npx",
		Transport:    Transport{Type: "stdio"},
		EnvironmentVariables: []EnvironmentVar{
			{Name: "BRAVE_API_KEY", Description: "API key", IsRequired: true, IsSecret: true},
		},
	}

	spec := BuildInstallSpec(srv, pkg, nil)

	if spec.CommandOrURL != "npx" {
		t.Errorf("command: got %q, want %q", spec.CommandOrURL, "npx")
	}
	if spec.Args != "-y @brave/brave-search-mcp-server" {
		t.Errorf("args: got %q, want %q", spec.Args, "-y @brave/brave-search-mcp-server")
	}
	if spec.Env["BRAVE_API_KEY"] != "<your-BRAVE_API_KEY>" {
		t.Errorf("env: got %q", spec.Env["BRAVE_API_KEY"])
	}
	if spec.Name != "brave-search" {
		t.Errorf("name: got %q, want %q", spec.Name, "brave-search")
	}
}

func TestBuildInstallSpec_NPMNoRuntimeHint(t *testing.T) {
	pkg := &Package{
		RegistryType: "npm",
		Identifier:   "@some/package",
		Transport:    Transport{Type: "stdio"},
	}

	spec := BuildInstallSpec(Server{Name: "test/pkg"}, pkg, nil)

	if spec.CommandOrURL != "npx" {
		t.Errorf("command: got %q, want %q", spec.CommandOrURL, "npx")
	}
}

func TestBuildInstallSpec_PyPIStdio(t *testing.T) {
	srv := Server{Name: "io.github.example/mcp-server-python"}
	pkg := &Package{
		RegistryType: "pypi",
		Identifier:   "mcp-server-python",
		RuntimeHint:  "uvx",
		Transport:    Transport{Type: "stdio"},
	}

	spec := BuildInstallSpec(srv, pkg, nil)

	if spec.CommandOrURL != "uvx" {
		t.Errorf("command: got %q, want %q", spec.CommandOrURL, "uvx")
	}
	if spec.Args != "mcp-server-python" {
		t.Errorf("args: got %q, want %q", spec.Args, "mcp-server-python")
	}
}

func TestBuildInstallSpec_PyPINoRuntimeHint(t *testing.T) {
	pkg := &Package{
		RegistryType: "pypi",
		Identifier:   "some-package",
		Transport:    Transport{Type: "stdio"},
	}

	spec := BuildInstallSpec(Server{Name: "test/pkg"}, pkg, nil)

	if spec.CommandOrURL != "uvx" {
		t.Errorf("command: got %q, want %q", spec.CommandOrURL, "uvx")
	}
}

func TestSelectBestPackage_PrefersStdioNPM(t *testing.T) {
	srv := Server{
		Packages: []Package{
			{RegistryType: "pypi", Identifier: "pypi-pkg", Transport: Transport{Type: "stdio"}},
			{RegistryType: "npm", Identifier: "npm-pkg", Transport: Transport{Type: "stdio"}},
		},
	}

	pkg, remote := SelectBestPackage(srv)

	if remote != nil {
		t.Error("expected no remote")
	}
	if pkg == nil {
		t.Fatal("expected a package")
	}
	if pkg.Identifier != "npm-pkg" {
		t.Errorf("expected npm-pkg, got %q", pkg.Identifier)
	}
}

func TestSelectBestPackage_FallsToPyPI(t *testing.T) {
	srv := Server{
		Packages: []Package{
			{RegistryType: "pypi", Identifier: "pypi-pkg", Transport: Transport{Type: "stdio"}},
		},
	}

	pkg, remote := SelectBestPackage(srv)

	if remote != nil {
		t.Error("expected no remote")
	}
	if pkg == nil {
		t.Fatal("expected a package")
	}
	if pkg.Identifier != "pypi-pkg" {
		t.Errorf("expected pypi-pkg, got %q", pkg.Identifier)
	}
}

func TestSelectBestPackage_FallsToRemote(t *testing.T) {
	srv := Server{
		Packages: []Package{
			{RegistryType: "npm", Identifier: "npm-pkg", Transport: Transport{Type: "sse"}}, // not stdio
		},
		Remotes: []Remote{
			{Type: "streamable-http", URL: "https://example.com/mcp"},
		},
	}

	pkg, remote := SelectBestPackage(srv)

	if pkg != nil {
		t.Error("expected no package")
	}
	if remote == nil {
		t.Fatal("expected a remote")
	}
	if remote.URL != "https://example.com/mcp" {
		t.Errorf("unexpected URL: %s", remote.URL)
	}
}

func TestSelectBestPackage_NoneAvailable(t *testing.T) {
	srv := Server{} // no packages or remotes

	pkg, remote := SelectBestPackage(srv)

	if pkg != nil || remote != nil {
		t.Error("expected nil, nil")
	}
}

func TestBuildInstallSpec_NamedPackageArgs(t *testing.T) {
	srv := Server{Name: "io.github.bytedance/mcp-server-filesystem"}
	pkg := &Package{
		RegistryType: "npm",
		Identifier:   "@agent-infra/mcp-server-filesystem",
		RuntimeHint:  "npx",
		Transport:    Transport{Type: "stdio"},
		PackageArguments: []PackageArgument{
			{Name: "allowed-directories", Description: "dirs", IsRequired: true, Type: "named"},
		},
	}

	spec := BuildInstallSpec(srv, pkg, nil)

	want := "-y @agent-infra/mcp-server-filesystem --allowed-directories <allowed-directories>"
	if spec.Args != want {
		t.Errorf("args:\n  got:  %q\n  want: %q", spec.Args, want)
	}
}

func TestBuildInstallSpec_NamedArgWithDefault(t *testing.T) {
	pkg := &Package{
		RegistryType: "npm",
		Identifier:   "@some/pkg",
		Transport:    Transport{Type: "stdio"},
		PackageArguments: []PackageArgument{
			{Name: "port", Description: "port", IsRequired: true, Type: "named", Default: "3000"},
		},
	}

	spec := BuildInstallSpec(Server{Name: "test/pkg"}, pkg, nil)

	want := "-y @some/pkg --port 3000"
	if spec.Args != want {
		t.Errorf("args: got %q, want %q", spec.Args, want)
	}
}

func TestBuildInstallSpec_PositionalArg(t *testing.T) {
	pkg := &Package{
		RegistryType: "npm",
		Identifier:   "@some/pkg",
		Transport:    Transport{Type: "stdio"},
		PackageArguments: []PackageArgument{
			{Name: "path", Description: "path", IsRequired: true}, // no type = positional
		},
	}

	spec := BuildInstallSpec(Server{Name: "test/pkg"}, pkg, nil)

	want := "-y @some/pkg <path>"
	if spec.Args != want {
		t.Errorf("args: got %q, want %q", spec.Args, want)
	}
}

func TestBuildInstallSpec_OptionalArgsOmitted(t *testing.T) {
	pkg := &Package{
		RegistryType: "npm",
		Identifier:   "@some/pkg",
		Transport:    Transport{Type: "stdio"},
		PackageArguments: []PackageArgument{
			{Name: "optional-flag", Description: "optional", IsRequired: false, Type: "named"},
		},
	}

	spec := BuildInstallSpec(Server{Name: "test/pkg"}, pkg, nil)

	want := "-y @some/pkg"
	if spec.Args != want {
		t.Errorf("args: got %q, want %q", spec.Args, want)
	}
}

func TestBuildInstallSpec_Remote(t *testing.T) {
	srv := Server{Name: "io.github.example/my-server"}
	remote := &Remote{
		Type: "streamable-http",
		URL:  "https://server.smithery.ai/my-server",
		Headers: []RemoteHeader{
			{
				Name:       "Authorization",
				Value:      "Bearer {smithery_api_key}",
				IsRequired: true,
				IsSecret:   true,
			},
		},
	}

	spec := BuildInstallSpec(srv, nil, remote)

	if spec.CommandOrURL != "https://server.smithery.ai/my-server" {
		t.Errorf("url: got %q", spec.CommandOrURL)
	}
	if spec.BearerTokenEnvVar != "SMITHERY_API_KEY" {
		t.Errorf("bearer: got %q, want %q", spec.BearerTokenEnvVar, "SMITHERY_API_KEY")
	}
	if spec.Args != "" {
		t.Errorf("args should be empty: got %q", spec.Args)
	}
}

func TestBuildInstallSpec_RemoteWithHeaders(t *testing.T) {
	srv := Server{Name: "io.github.example/custom-server"}
	remote := &Remote{
		Type: "streamable-http",
		URL:  "https://example.com/mcp",
		Headers: []RemoteHeader{
			{Name: "Authorization", Value: "Bearer {api_key}", IsRequired: true, IsSecret: true},
			{Name: "X-Custom-Header", Value: "some-value", IsRequired: true},
			{Name: "X-Optional", Value: "optional", IsRequired: false},
		},
	}

	spec := BuildInstallSpec(srv, nil, remote)

	if spec.BearerTokenEnvVar != "API_KEY" {
		t.Errorf("bearer: got %q, want %q", spec.BearerTokenEnvVar, "API_KEY")
	}
	// Non-bearer required headers go into Headers, NOT Env
	if spec.Headers == nil || spec.Headers["X-Custom-Header"] == "" {
		t.Error("expected X-Custom-Header in Headers")
	}
	if spec.Env != nil {
		t.Errorf("Env should be nil for remote specs with only headers, got %v", spec.Env)
	}
	// Optional headers are not included
	if _, ok := spec.Headers["X-Optional"]; ok {
		t.Error("optional header should not be in Headers")
	}
}

func TestBuildInstallSpec_RemoteNoAuth(t *testing.T) {
	srv := Server{Name: "test/server"}
	remote := &Remote{
		Type: "streamable-http",
		URL:  "https://example.com/mcp",
	}

	spec := BuildInstallSpec(srv, nil, remote)

	if spec.CommandOrURL != "https://example.com/mcp" {
		t.Errorf("url: got %q", spec.CommandOrURL)
	}
	if spec.BearerTokenEnvVar != "" {
		t.Errorf("bearer should be empty: got %q", spec.BearerTokenEnvVar)
	}
}

func TestBuildInstallSpec_NoPkgNoRemote(t *testing.T) {
	srv := Server{Name: "test/server"}
	spec := BuildInstallSpec(srv, nil, nil)

	if spec.CommandOrURL != "" {
		t.Errorf("expected empty CommandOrURL, got %q", spec.CommandOrURL)
	}
	if spec.Name != "" {
		t.Errorf("expected empty Name, got %q", spec.Name)
	}
}

func TestDeriveName_FromTitle(t *testing.T) {
	tests := []struct {
		title string
		want  string
	}{
		{"Brave Search MCP Server", "brave-search"},
		{"Sentry", "sentry"},
		{"My Cool Tool", "my-cool-tool"},
	}
	for _, tt := range tests {
		srv := Server{Name: "io.github.test/test-server", Title: tt.title}
		got := DeriveName(srv)
		if got != tt.want {
			t.Errorf("DeriveName(title=%q): got %q, want %q", tt.title, got, tt.want)
		}
	}
}

func TestDeriveName_FromName(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"io.github.brave/brave-search-mcp-server", "brave-search"},
		{"io.github.example/mcp-server-filesystem", "filesystem"},
		{"io.github.example/server-tools", "tools"},
		{"simple-name", "simple-name"},
		{"io.github.example/my-tool-mcp", "my-tool"},
	}
	for _, tt := range tests {
		srv := Server{Name: tt.name} // no title
		got := DeriveName(srv)
		if got != tt.want {
			t.Errorf("DeriveName(name=%q): got %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestDeriveName_LongTitleFallsBackToName(t *testing.T) {
	srv := Server{
		Name:  "io.github.example/my-tool-mcp-server",
		Title: "This Is A Very Long Title That Exceeds Forty Characters Limit",
	}
	got := DeriveName(srv)
	// Should fall back to name-based derivation since title > 40 chars
	if got != "my-tool" {
		t.Errorf("got %q, want %q", got, "my-tool")
	}
}
