package server

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/mcp"
)

func TestNegotiateProtocolVersion(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		requested string
		want      string
	}{
		{"current revision is echoed", "2025-11-25", "2025-11-25"},
		{"older supported revision is echoed", "2024-11-05", "2024-11-05"},
		{"middle revision is echoed", "2025-06-18", "2025-06-18"},
		{"unknown future revision falls back to ours", "2099-01-01", LatestDownstreamProtocolVersion()},
		{"absent revision falls back to ours", "", LatestDownstreamProtocolVersion()},
		{"nonsense falls back to ours", "banana", LatestDownstreamProtocolVersion()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := negotiateProtocolVersion(tt.requested); got != tt.want {
				t.Errorf("negotiateProtocolVersion(%q) = %q, want %q", tt.requested, got, tt.want)
			}
		})
	}
}

// The downstream list may legitimately lag the upstream one, but a revision we
// serve downstream and cannot negotiate upstream would be a lie.
func TestDownstreamVersionsAreASubsetOfUpstream(t *testing.T) {
	t.Parallel()
	upstream := make(map[string]bool, len(mcp.SupportedProtocolVersions))
	for _, version := range mcp.SupportedProtocolVersions {
		upstream[version] = true
	}
	for _, version := range DownstreamProtocolVersions {
		if !upstream[version] {
			t.Errorf("downstream advertises %q but upstream negotiation never offers it", version)
		}
	}
}

func TestSession_InitializeEchoesRequestedVersion(t *testing.T) {
	t.Parallel()
	for _, requested := range []string{"2025-11-25", "2024-11-05"} {
		t.Run(requested, func(t *testing.T) {
			t.Parallel()
			got, session := initializeWithVersion(t, requested)
			if got != requested {
				t.Errorf("initialize response protocolVersion = %q, want %q", got, requested)
			}
			if recorded := session.NegotiatedProtocolVersion(); recorded != requested {
				t.Errorf("session recorded %q, want %q", recorded, requested)
			}
		})
	}
}

func TestSession_InitializeUnknownVersionFallsBack(t *testing.T) {
	t.Parallel()
	got, _ := initializeWithVersion(t, "2099-01-01")
	if want := LatestDownstreamProtocolVersion(); got != want {
		t.Errorf("initialize response protocolVersion = %q, want %q", got, want)
	}
}

// Two sessions on one Core may speak different revisions; the negotiated value
// is per-session state, not a process-wide setting.
func TestCore_SessionsNegotiateIndependently(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{SchemaVersion: 1, Servers: map[string]config.ServerConfig{}}
	core, err := NewCore(Options{Config: cfg, PIDTrackerDir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewCore: %v", err)
	}
	t.Cleanup(core.Close)

	newSession := func(version string) *Session {
		t.Helper()
		session, err := NewSession(core, Options{
			Config: cfg, Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{},
		})
		if err != nil {
			t.Fatalf("NewSession: %v", err)
		}
		t.Cleanup(session.Close)
		params := json.RawMessage(`{"protocolVersion":"` + version + `","clientInfo":{"name":"test","version":"1"}}`)
		if _, rpcErr := session.handleInitialize(context.Background(), params); rpcErr != nil {
			t.Fatalf("handleInitialize: %v", rpcErr)
		}
		return session
	}

	modern := newSession("2025-11-25")
	legacy := newSession("2024-11-05")

	if got := modern.NegotiatedProtocolVersion(); got != "2025-11-25" {
		t.Errorf("modern session negotiated %q, want 2025-11-25", got)
	}
	if got := legacy.NegotiatedProtocolVersion(); got != "2024-11-05" {
		t.Errorf("legacy session negotiated %q, want 2024-11-05", got)
	}
}

func initializeWithVersion(t *testing.T, requested string) (string, *Session) {
	t.Helper()
	cfg := &config.Config{SchemaVersion: 1, Servers: map[string]config.ServerConfig{}}
	session, err := New(Options{
		Config: cfg, PIDTrackerDir: t.TempDir(),
		Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{},
		ServerName: "mcpmu-test", ServerVersion: "1.0.0", LogLevel: "error",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() {
		session.Close()
		session.Core.Close()
	})

	params := json.RawMessage(`{"protocolVersion":"` + requested + `","clientInfo":{"name":"test","version":"1"}}`)
	result, rpcErr := session.handleInitialize(context.Background(), params)
	if rpcErr != nil {
		t.Fatalf("handleInitialize: %v", rpcErr)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal initialize result: %v", err)
	}
	var decoded struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal initialize result: %v", err)
	}
	return decoded.ProtocolVersion, session
}
