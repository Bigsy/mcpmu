package views

import (
	"testing"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/tui/theme"
	tea "github.com/charmbracelet/bubbletea"
)

func TestShowAddWithDefaults_PrePopulatesFields(t *testing.T) {
	th := theme.New()
	form := NewServerForm(th)

	cmd := form.ShowAddWithDefaults(
		"brave-search",
		"npx",
		"-y @brave/brave-search-mcp-server",
		"BRAVE_API_KEY=<your-BRAVE_API_KEY>",
		"",
		"",
		"",
		"",
	)

	if !form.IsVisible() {
		t.Fatal("form should be visible after ShowAddWithDefaults")
	}
	if form.isEdit {
		t.Error("should not be in edit mode")
	}
	if cmd == nil {
		t.Fatal("ShowAddWithDefaults should return a command from form.Init()")
	}

	// Verify field values were set
	if form.name != "brave-search" {
		t.Errorf("name: got %q, want %q", form.name, "brave-search")
	}
	if form.commandOrURL != "npx" {
		t.Errorf("commandOrURL: got %q, want %q", form.commandOrURL, "npx")
	}
	if form.args != "-y @brave/brave-search-mcp-server" {
		t.Errorf("args: got %q, want %q", form.args, "-y @brave/brave-search-mcp-server")
	}
	if form.env != "BRAVE_API_KEY=<your-BRAVE_API_KEY>" {
		t.Errorf("env: got %q, want %q", form.env, "BRAVE_API_KEY=<your-BRAVE_API_KEY>")
	}
	if form.bearerTokenEnvVar != "" {
		t.Errorf("bearerTokenEnvVar: got %q, want empty", form.bearerTokenEnvVar)
	}
}

func TestShowAddWithDefaults_PrePopulatesHTTPServer(t *testing.T) {
	th := theme.New()
	form := NewServerForm(th)

	cmd := form.ShowAddWithDefaults(
		"smithery-server",
		"https://server.smithery.ai/my-server",
		"",
		"",
		"SMITHERY_API_KEY",
		"",
		"",
		"",
	)

	if !form.IsVisible() {
		t.Fatal("form should be visible")
	}
	if cmd == nil {
		t.Fatal("should return a command")
	}

	if form.commandOrURL != "https://server.smithery.ai/my-server" {
		t.Errorf("commandOrURL: got %q", form.commandOrURL)
	}
	if form.bearerTokenEnvVar != "SMITHERY_API_KEY" {
		t.Errorf("bearerTokenEnvVar: got %q, want %q", form.bearerTokenEnvVar, "SMITHERY_API_KEY")
	}
}

func TestShowAddWithDefaults_SubmitProducesCorrectResult(t *testing.T) {
	th := theme.New()
	form := NewServerForm(th)

	_ = form.ShowAddWithDefaults(
		"brave-search",
		"npx",
		"-y @brave/brave-search-mcp-server",
		"BRAVE_API_KEY=<your-BRAVE_API_KEY>",
		"",
		"",
		"",
		"",
	)

	// Verify buildServerConfig produces the expected config from pre-populated fields
	srv := form.buildServerConfig()
	if srv.Command != "npx" {
		t.Errorf("Command: got %q, want %q", srv.Command, "npx")
	}
	if len(srv.Args) != 2 || srv.Args[0] != "-y" || srv.Args[1] != "@brave/brave-search-mcp-server" {
		t.Errorf("Args: got %v, want [-y @brave/brave-search-mcp-server]", srv.Args)
	}
	if srv.Env["BRAVE_API_KEY"] != "<your-BRAVE_API_KEY>" {
		t.Errorf("Env[BRAVE_API_KEY]: got %q", srv.Env["BRAVE_API_KEY"])
	}
	if srv.URL != "" {
		t.Errorf("URL should be empty for stdio server, got %q", srv.URL)
	}

	name := form.getName()
	if name != "brave-search" {
		t.Errorf("getName: got %q, want %q", name, "brave-search")
	}
}

func TestShowAddWithDefaults_SubmitHTTPProducesCorrectResult(t *testing.T) {
	th := theme.New()
	form := NewServerForm(th)

	_ = form.ShowAddWithDefaults(
		"smithery-server",
		"https://server.smithery.ai/my-server",
		"",
		"",
		"SMITHERY_API_KEY",
		"",
		"",
		"",
	)

	srv := form.buildServerConfig()
	if srv.URL != "https://server.smithery.ai/my-server" {
		t.Errorf("URL: got %q", srv.URL)
	}
	if srv.BearerTokenEnvVar != "SMITHERY_API_KEY" {
		t.Errorf("BearerTokenEnvVar: got %q, want %q", srv.BearerTokenEnvVar, "SMITHERY_API_KEY")
	}
	if srv.Command != "" {
		t.Errorf("Command should be empty for HTTP server, got %q", srv.Command)
	}
}

func TestShowAddWithDefaults_NotDirtyOnOpen(t *testing.T) {
	th := theme.New()
	form := NewServerForm(th)

	_ = form.ShowAddWithDefaults(
		"brave-search",
		"npx",
		"-y @brave/brave-search-mcp-server",
		"BRAVE_API_KEY=<your-key>",
		"",
		"",
		"",
		"",
	)

	if form.isDirty() {
		t.Error("form should not be dirty immediately after ShowAddWithDefaults")
	}

	// Esc should close immediately without confirm dialog (since not dirty)
	cmd := form.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd == nil {
		t.Fatal("Esc should return a command")
	}

	if form.IsVisible() {
		t.Error("form should be hidden after Esc (not dirty, no confirm dialog)")
	}
	if form.showConfirmDiscard {
		t.Error("should not show confirm discard dialog")
	}

	msg := cmd()
	result, ok := msg.(ServerFormResult)
	if !ok {
		t.Fatalf("expected ServerFormResult, got %T", msg)
	}
	if result.Submitted {
		t.Error("result should have Submitted=false for cancel")
	}
}

func TestShowAddWithDefaults_PrePopulatesOAuthFields(t *testing.T) {
	th := theme.New()
	form := NewServerForm(th)

	cmd := form.ShowAddWithDefaults(
		"slack",
		"https://mcp.slack.com/mcp",
		"",
		"",
		"",
		"12345.67890",
		"3118",
		"",
	)

	if cmd == nil {
		t.Fatal("should return a command")
	}

	if form.oauthClientID != "12345.67890" {
		t.Errorf("oauthClientID: got %q, want %q", form.oauthClientID, "12345.67890")
	}
	if form.oauthCallbackPort != "3118" {
		t.Errorf("oauthCallbackPort: got %q, want %q", form.oauthCallbackPort, "3118")
	}

	// Form should not be dirty (initial values match)
	if form.isDirty() {
		t.Error("form should not be dirty immediately after ShowAddWithDefaults with OAuth")
	}

	// Verify buildServerConfig produces correct OAuth config
	srv := form.buildServerConfig()
	if srv.OAuth == nil {
		t.Fatal("expected OAuth config in built server")
	}
	if srv.OAuth.ClientID != "12345.67890" {
		t.Errorf("OAuth.ClientID: got %q", srv.OAuth.ClientID)
	}
	if srv.OAuth.CallbackPort == nil || *srv.OAuth.CallbackPort != 3118 {
		t.Errorf("OAuth.CallbackPort: got %v", srv.OAuth.CallbackPort)
	}
}

func TestBuildServerConfig_BearerClearsOAuth(t *testing.T) {
	th := theme.New()
	form := NewServerForm(th)

	// Simulate editing an existing OAuth server
	port := 3118
	_ = form.ShowEdit("slack", config.ServerConfig{
		URL: "https://mcp.slack.com/mcp",
		OAuth: &config.OAuthConfig{
			ClientID:     "12345.67890",
			ClientSecret: "secret",
			CallbackPort: &port,
			Scopes:       []string{"channels:read"},
		},
	})

	// User switches to bearer auth by entering a bearer token env var
	form.bearerTokenEnvVar = "SLACK_TOKEN"

	srv := form.buildServerConfig()
	if srv.OAuth != nil {
		t.Error("expected OAuth to be nil when bearer token is set")
	}
	if srv.BearerTokenEnvVar != "SLACK_TOKEN" {
		t.Errorf("expected BearerTokenEnvVar 'SLACK_TOKEN', got %q", srv.BearerTokenEnvVar)
	}
}

func TestShowEdit_AutostartPrePopulated(t *testing.T) {
	th := theme.New()
	form := NewServerForm(th)
	_ = form.ShowEdit("my-server", config.ServerConfig{
		Command:   "echo",
		Autostart: true,
	})
	if !form.autostart {
		t.Error("expected autostart to be true in edit mode")
	}
	srv := form.buildServerConfig()
	if !srv.Autostart {
		t.Error("expected built config to have Autostart=true")
	}
}

func TestShowEdit_TimeoutsPrePopulated(t *testing.T) {
	th := theme.New()
	form := NewServerForm(th)
	_ = form.ShowEdit("my-server", config.ServerConfig{
		Command:           "echo",
		StartupTimeoutSec: 30,
		ToolTimeoutSec:    120,
	})
	if form.startupTimeout != "30" {
		t.Errorf("startupTimeout: got %q, want %q", form.startupTimeout, "30")
	}
	if form.toolTimeout != "120" {
		t.Errorf("toolTimeout: got %q, want %q", form.toolTimeout, "120")
	}
	srv := form.buildServerConfig()
	if srv.StartupTimeoutSec != 30 {
		t.Errorf("StartupTimeoutSec: got %d, want 30", srv.StartupTimeoutSec)
	}
	if srv.ToolTimeoutSec != 120 {
		t.Errorf("ToolTimeoutSec: got %d, want 120", srv.ToolTimeoutSec)
	}
}

func TestBuildServerConfig_EmptyTimeoutsAreZero(t *testing.T) {
	th := theme.New()
	form := NewServerForm(th)
	_ = form.ShowAdd()
	form.commandOrURL = "echo"
	form.startupTimeout = ""
	form.toolTimeout = ""
	srv := form.buildServerConfig()
	if srv.StartupTimeoutSec != 0 {
		t.Errorf("expected StartupTimeoutSec 0, got %d", srv.StartupTimeoutSec)
	}
	if srv.ToolTimeoutSec != 0 {
		t.Errorf("expected ToolTimeoutSec 0, got %d", srv.ToolTimeoutSec)
	}
}

func TestShowEdit_OAuthScopesRoundTrip(t *testing.T) {
	th := theme.New()
	form := NewServerForm(th)
	_ = form.ShowEdit("slack", config.ServerConfig{
		URL: "https://mcp.slack.com/mcp",
		OAuth: &config.OAuthConfig{
			Scopes: []string{"channels:read", "channels:write"},
		},
	})
	if form.oauthScopes != "channels:read, channels:write" {
		t.Errorf("oauthScopes: got %q", form.oauthScopes)
	}
	srv := form.buildServerConfig()
	if srv.OAuth == nil {
		t.Fatal("expected OAuth config")
	}
	if len(srv.OAuth.Scopes) != 2 || srv.OAuth.Scopes[0] != "channels:read" || srv.OAuth.Scopes[1] != "channels:write" {
		t.Errorf("OAuth.Scopes: got %v", srv.OAuth.Scopes)
	}
}

func TestIsDirty_NewFields(t *testing.T) {
	th := theme.New()
	form := NewServerForm(th)
	_ = form.ShowAdd()
	if form.isDirty() {
		t.Error("should not be dirty on open")
	}

	form.autostart = true
	if !form.isDirty() {
		t.Error("should be dirty after changing autostart")
	}
	form.autostart = false

	form.startupTimeout = "20"
	if !form.isDirty() {
		t.Error("should be dirty after changing startupTimeout")
	}
	form.startupTimeout = ""

	form.toolTimeout = "30"
	if !form.isDirty() {
		t.Error("should be dirty after changing toolTimeout")
	}
	form.toolTimeout = ""

	form.oauthScopes = "read"
	if !form.isDirty() {
		t.Error("should be dirty after changing oauthScopes")
	}
}
