package views

import (
	"strings"
	"testing"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/testutil"
	"github.com/Bigsy/mcpmu/internal/tui/theme"
)

func newTestDetailModel(t *testing.T) ServerDetailModel {
	t.Helper()
	th := theme.New()
	detail := NewServerDetail(th)
	detail.SetSize(80, 40)
	return detail
}

func detailContent(t *testing.T, detail ServerDetailModel) string {
	t.Helper()
	return testutil.StripANSI(detail.viewport.View())
}

func assertContains(t *testing.T, content, substr string) {
	t.Helper()
	if !strings.Contains(content, substr) {
		t.Errorf("expected content to contain %q, got:\n%s", substr, content)
	}
}

func assertNotContains(t *testing.T, content, substr string) {
	t.Helper()
	if strings.Contains(content, substr) {
		t.Errorf("expected content NOT to contain %q, got:\n%s", substr, content)
	}
}

func TestServerDetail_HTTPBearer(t *testing.T) {
	detail := newTestDetailModel(t)
	srv := &config.ServerConfig{
		URL:               "https://mcp.figma.com/mcp",
		BearerTokenEnvVar: "FIGMA_TOKEN",
	}
	detail.SetServer("figma", srv, nil, nil, nil, false)
	content := detailContent(t, detail)

	assertContains(t, content, "https://mcp.figma.com/mcp")
	assertContains(t, content, "Bearer")
	assertContains(t, content, "$FIGMA_TOKEN")
	assertNotContains(t, content, "Command:")
}

func TestServerDetail_HTTPOAuthFull(t *testing.T) {
	port := 3118
	detail := newTestDetailModel(t)
	srv := &config.ServerConfig{
		URL: "https://mcp.atlassian.com/mcp",
		OAuth: &config.OAuthConfig{
			ClientID:     "abc123",
			ClientSecret: "topsecret",
			Scopes:       []string{"read", "write"},
			CallbackPort: &port,
		},
	}
	detail.SetServer("atlassian", srv, nil, nil, nil, false)
	content := detailContent(t, detail)

	assertContains(t, content, "https://mcp.atlassian.com/mcp")
	assertContains(t, content, "OAuth")
	assertContains(t, content, "abc123")
	assertContains(t, content, "read, write")
	assertContains(t, content, "3118")
	assertNotContains(t, content, "Command:")
	assertNotContains(t, content, "topsecret")
}

func TestServerDetail_HTTPOAuthDynamic(t *testing.T) {
	detail := newTestDetailModel(t)
	srv := &config.ServerConfig{
		URL:   "https://mcp.example.com/v1",
		OAuth: &config.OAuthConfig{},
	}
	detail.SetServer("example", srv, nil, nil, nil, false)
	content := detailContent(t, detail)

	assertContains(t, content, "OAuth")
	assertContains(t, content, "dynamic")
}

func TestServerDetail_HTTPOAuthWithSecret(t *testing.T) {
	detail := newTestDetailModel(t)
	srv := &config.ServerConfig{
		URL: "https://mcp.example.com/v1",
		OAuth: &config.OAuthConfig{
			ClientID:     "myid",
			ClientSecret: "supersecret",
		},
	}
	detail.SetServer("example", srv, nil, nil, nil, false)
	content := detailContent(t, detail)

	assertContains(t, content, "myid")
	assertNotContains(t, content, "supersecret")
}

func TestServerDetail_HTTPHeaders(t *testing.T) {
	detail := newTestDetailModel(t)
	srv := &config.ServerConfig{
		URL: "https://mcp.example.com/v1",
		HTTPHeaders: map[string]string{
			"X-Custom": "secretval",
		},
	}
	detail.SetServer("example", srv, nil, nil, nil, false)
	content := detailContent(t, detail)

	assertContains(t, content, "X-Custom")
	assertNotContains(t, content, "secretval")
}

func TestServerDetail_HTTPEnvHeaders(t *testing.T) {
	detail := newTestDetailModel(t)
	srv := &config.ServerConfig{
		URL: "https://mcp.example.com/v1",
		EnvHTTPHeaders: map[string]string{
			"Authorization": "AUTH_VAR",
		},
	}
	detail.SetServer("example", srv, nil, nil, nil, false)
	content := detailContent(t, detail)

	assertContains(t, content, "Authorization")
	assertContains(t, content, "$AUTH_VAR")
}

func TestServerDetail_HTTPNoAuth(t *testing.T) {
	detail := newTestDetailModel(t)
	srv := &config.ServerConfig{
		URL: "https://mcp.example.com/v1",
	}
	detail.SetServer("example", srv, nil, nil, nil, false)
	content := detailContent(t, detail)

	assertContains(t, content, "URL:")
	assertContains(t, content, "none")
	assertNotContains(t, content, "Command:")
}

func TestServerDetail_StdioUnchanged(t *testing.T) {
	detail := newTestDetailModel(t)
	srv := &config.ServerConfig{
		Command: "echo",
		Args:    []string{"hello", "world"},
	}
	detail.SetServer("echo-server", srv, nil, nil, nil, false)
	content := detailContent(t, detail)

	assertContains(t, content, "Command:")
	assertContains(t, content, "echo hello world")
	assertNotContains(t, content, "URL:")
	assertNotContains(t, content, "Auth:")
}

func TestServerDetail_StdioWithCwdAndEnv(t *testing.T) {
	detail := newTestDetailModel(t)
	srv := &config.ServerConfig{
		Command: "node",
		Args:    []string{"server.js"},
		Cwd:     "/home/user/project",
		Env:     map[string]string{"NODE_ENV": "production"},
	}
	detail.SetServer("node-server", srv, nil, nil, nil, false)
	content := detailContent(t, detail)

	assertContains(t, content, "Command:")
	assertContains(t, content, "Working Dir:")
	assertContains(t, content, "/home/user/project")
	assertContains(t, content, "NODE_ENV=production")
	assertNotContains(t, content, "URL:")
}

func TestServerDetail_HTTPWithCwdAndEnv(t *testing.T) {
	detail := newTestDetailModel(t)
	srv := &config.ServerConfig{
		URL: "https://mcp.example.com/v1",
		Cwd: "/tmp/workdir",
		Env: map[string]string{"DEBUG": "true"},
	}
	detail.SetServer("http-with-env", srv, nil, nil, nil, false)
	content := detailContent(t, detail)

	assertContains(t, content, "URL:")
	assertContains(t, content, "Working Dir:")
	assertContains(t, content, "/tmp/workdir")
	assertContains(t, content, "DEBUG=true")
	assertNotContains(t, content, "Command:")
}

func TestServerDetail_HTTPHeadersSorted(t *testing.T) {
	detail := newTestDetailModel(t)
	srv := &config.ServerConfig{
		URL: "https://mcp.example.com/v1",
		HTTPHeaders: map[string]string{
			"Z-Header": "z",
			"A-Header": "a",
			"M-Header": "m",
		},
	}
	detail.SetServer("example", srv, nil, nil, nil, false)
	content := detailContent(t, detail)

	// All header names should be present
	assertContains(t, content, "A-Header")
	assertContains(t, content, "M-Header")
	assertContains(t, content, "Z-Header")

	// A-Header should appear before Z-Header
	aIdx := strings.Index(content, "A-Header")
	zIdx := strings.Index(content, "Z-Header")
	if aIdx >= zIdx {
		t.Errorf("expected A-Header (pos %d) to appear before Z-Header (pos %d)", aIdx, zIdx)
	}
}
