package oauth

import (
	"net/url"
	"testing"
)

func TestBuildDiscoveryPaths_WithPath(t *testing.T) {
	parsed, _ := url.Parse("https://mcp.example.com/api/mcp")
	paths := buildDiscoveryPaths(parsed)

	expected := []string{
		"https://mcp.example.com/.well-known/oauth-authorization-server/api/mcp",
		"https://mcp.example.com/api/mcp/.well-known/oauth-authorization-server",
		"https://mcp.example.com/.well-known/oauth-authorization-server",
	}

	if len(paths) != len(expected) {
		t.Fatalf("expected %d paths, got %d: %v", len(expected), len(paths), paths)
	}

	for i, e := range expected {
		if paths[i] != e {
			t.Errorf("path[%d]: expected %q, got %q", i, e, paths[i])
		}
	}
}

func TestBuildDiscoveryPaths_RootPath(t *testing.T) {
	parsed, _ := url.Parse("https://mcp.example.com/")
	paths := buildDiscoveryPaths(parsed)

	// With root path, only root discovery should be tried
	expected := []string{
		"https://mcp.example.com/.well-known/oauth-authorization-server",
	}

	if len(paths) != len(expected) {
		t.Fatalf("expected %d paths, got %d: %v", len(expected), len(paths), paths)
	}

	if paths[0] != expected[0] {
		t.Errorf("expected %q, got %q", expected[0], paths[0])
	}
}

func TestBuildDiscoveryPaths_NoPath(t *testing.T) {
	parsed, _ := url.Parse("https://mcp.example.com")
	paths := buildDiscoveryPaths(parsed)

	expected := []string{
		"https://mcp.example.com/.well-known/oauth-authorization-server",
	}

	if len(paths) != len(expected) {
		t.Fatalf("expected %d paths, got %d: %v", len(expected), len(paths), paths)
	}

	if paths[0] != expected[0] {
		t.Errorf("expected %q, got %q", expected[0], paths[0])
	}
}

func TestBuildDiscoveryPaths_TrailingSlash(t *testing.T) {
	parsed, _ := url.Parse("https://mcp.example.com/mcp/")
	paths := buildDiscoveryPaths(parsed)

	// Trailing slash should be stripped
	expected := []string{
		"https://mcp.example.com/.well-known/oauth-authorization-server/mcp",
		"https://mcp.example.com/mcp/.well-known/oauth-authorization-server",
		"https://mcp.example.com/.well-known/oauth-authorization-server",
	}

	if len(paths) != len(expected) {
		t.Fatalf("expected %d paths, got %d: %v", len(expected), len(paths), paths)
	}

	for i, e := range expected {
		if paths[i] != e {
			t.Errorf("path[%d]: expected %q, got %q", i, e, paths[i])
		}
	}
}

func TestAuthorizationServerMetadata_SupportsS256(t *testing.T) {
	tests := []struct {
		name    string
		methods []string
		want    bool
	}{
		{
			name:    "supports S256",
			methods: []string{"plain", "S256"},
			want:    true,
		},
		{
			name:    "only S256",
			methods: []string{"S256"},
			want:    true,
		},
		{
			name:    "only plain",
			methods: []string{"plain"},
			want:    false,
		},
		{
			name:    "empty",
			methods: nil,
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := AuthorizationServerMetadata{
				CodeChallengeMethodsSupported: tt.methods,
			}
			if got := m.SupportsS256(); got != tt.want {
				t.Errorf("SupportsS256() = %v, want %v", got, tt.want)
			}
		})
	}
}
