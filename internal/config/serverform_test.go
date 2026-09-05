package config

import (
	"slices"
	"testing"
)

func TestBuildServerConfig_EditPreservesClientSecretAndUnexposedFields(t *testing.T) {
	port := 3118
	shared := false
	existing := ServerConfig{
		URL:         "https://mcp.slack.com/mcp",
		Shared:      &shared,
		DeniedTools: []string{"danger"},
		OAuth: &OAuthConfig{
			ClientID:     "id",
			ClientSecret: "s3cret",
			CallbackPort: &port,
			Scopes:       []string{"a"},
		},
	}

	// Web-style edit of an unrelated field: explicit oauth mode, form carries
	// the visible OAuth values back, plus a new autostart flag.
	got, err := BuildServerConfig(ServerFormData{
		IsHTTP:            true,
		URL:               existing.URL,
		AuthMode:          AuthModeOAuth,
		OAuthClientID:     "id",
		OAuthCallbackPort: "3118",
		OAuthScopes:       "a",
		Autostart:         true,
		Enabled:           new(true),
	}, &existing)
	if err != nil {
		t.Fatal(err)
	}
	if got.OAuth == nil || got.OAuth.ClientSecret != "s3cret" {
		t.Fatalf("ClientSecret lost on edit: %+v", got.OAuth)
	}
	if got.Shared == nil || *got.Shared {
		t.Error("Shared=false not preserved")
	}
	if !slices.Equal(got.DeniedTools, []string{"danger"}) {
		t.Error("DeniedTools not preserved")
	}
	if !got.Autostart {
		t.Error("Autostart not applied")
	}
	if got.Kind != ServerKindStreamableHTTP {
		t.Errorf("Kind = %q", got.Kind)
	}
	if err := got.Validate(); err != nil {
		t.Error(err)
	}
}

func TestBuildServerConfig_InferredAuthMode(t *testing.T) {
	// TUI passes AuthMode "" and relies on inference.
	existingOAuth := ServerConfig{URL: "https://x/mcp", OAuth: &OAuthConfig{ClientSecret: "s"}}

	got, err := BuildServerConfig(ServerFormData{IsHTTP: true, URL: "https://x/mcp"}, &existingOAuth)
	if err != nil {
		t.Fatal(err)
	}
	if got.OAuth == nil || got.OAuth.ClientSecret != "s" {
		t.Error("existing OAuth should imply oauth mode and keep the secret")
	}

	got, err = BuildServerConfig(ServerFormData{IsHTTP: true, URL: "https://x/mcp", BearerEnv: "TOK"}, &existingOAuth)
	if err != nil {
		t.Fatal(err)
	}
	if got.OAuth != nil || got.BearerTokenEnvVar != "TOK" {
		t.Error("bearer env should win over existing OAuth")
	}

	got, err = BuildServerConfig(ServerFormData{IsHTTP: true, URL: "https://x/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.OAuth != nil || got.BearerTokenEnvVar != "" {
		t.Error("no auth input and no existing → none")
	}

	got, err = BuildServerConfig(ServerFormData{IsHTTP: true, URL: "https://x/mcp", OAuthScopes: "read, write,"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.OAuth == nil || !slices.Equal(got.OAuth.Scopes, []string{"read", "write"}) {
		t.Errorf("scopes: %+v", got.OAuth)
	}
}

func TestBuildServerConfig_SwitchingTransportClearsOtherSide(t *testing.T) {
	existing := ServerConfig{
		URL:               "https://x/mcp",
		BearerTokenEnvVar: "TOK",
		HTTPHeaders:       map[string]string{"A": "b"},
		Kind:              ServerKindStreamableHTTP,
	}
	got, err := BuildServerConfig(ServerFormData{Command: "npx", Args: "-y pkg"}, &existing)
	if err != nil {
		t.Fatal(err)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("stdio result invalid: %v", err)
	}
	if got.URL != "" || got.BearerTokenEnvVar != "" || got.HTTPHeaders != nil || got.Kind != ServerKindStdio {
		t.Errorf("HTTP leftovers: %+v", got)
	}

	stdio := ServerConfig{Command: "npx", Args: []string{"x"}, Cwd: "/tmp", Kind: ServerKindStdio}
	got, err = BuildServerConfig(ServerFormData{IsHTTP: true, URL: "https://x/mcp"}, &stdio)
	if err != nil {
		t.Fatal(err)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("http result invalid: %v", err)
	}
	if got.Command != "" || got.Args != nil || got.Cwd != "" {
		t.Errorf("stdio leftovers: %+v", got)
	}
}

func TestBuildServerConfig_QuotedArgsRoundTrip(t *testing.T) {
	want := []string{"-y", "@scope/pkg", "--name", "hello world", `say "hi"`, ""}
	got, err := BuildServerConfig(ServerFormData{Command: "npx", Args: JoinArgs(want)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Args, want) {
		t.Errorf("Args = %q, want %q", got.Args, want)
	}
}

func TestBuildServerConfig_Errors(t *testing.T) {
	cases := []struct {
		name string
		form ServerFormData
	}{
		{"unterminated quote", ServerFormData{Command: "x", Args: `"oops`}},
		{"bad header", ServerFormData{IsHTTP: true, URL: "https://x", HTTPHeaders: "nocolon"}},
		{"dup header", ServerFormData{IsHTTP: true, URL: "https://x", HTTPHeaders: "A: b", EnvHTTPHeaders: "A: ENV"}},
		{"bad port", ServerFormData{IsHTTP: true, URL: "https://x", AuthMode: AuthModeOAuth, OAuthCallbackPort: "70000"}},
	}
	for _, tc := range cases {
		if _, err := BuildServerConfig(tc.form, nil); err == nil {
			t.Errorf("%s: expected error", tc.name)
		}
	}
}

func TestBuildServerConfig_EnabledNilKeepsExisting(t *testing.T) {
	existing := ServerConfig{Command: "x", Enabled: new(false)}
	got, err := BuildServerConfig(ServerFormData{Command: "x"}, &existing)
	if err != nil {
		t.Fatal(err)
	}
	if got.IsEnabled() {
		t.Error("Enabled=false should survive when the form has no enabled field")
	}
	got, err = BuildServerConfig(ServerFormData{Command: "x", Enabled: new(true)}, &existing)
	if err != nil {
		t.Fatal(err)
	}
	if !got.IsEnabled() {
		t.Error("explicit Enabled=true not applied")
	}
}

func TestBuildServerConfigSharing(t *testing.T) {
	for _, initial := range []*bool{nil, new(false), new(true)} {
		for _, input := range []*bool{nil, new(false), new(true)} {
			existing := ServerConfig{Command: "fixture", Shared: initial, DeniedTools: []string{"danger"}}
			got, err := BuildServerConfig(ServerFormData{Command: "edited", Shared: input}, &existing)
			if err != nil {
				t.Fatal(err)
			}
			want := existing.IsShared()
			if input != nil {
				want = *input
			}
			if got.IsShared() != want || len(got.DeniedTools) != 1 {
				t.Fatalf("sharing or denied tools lost: %+v", got)
			}
		}
	}
	got, err := BuildServerConfig(ServerFormData{Command: "fixture"}, nil)
	if err != nil || !got.IsShared() {
		t.Fatalf("new default: %+v %v", got, err)
	}
}
