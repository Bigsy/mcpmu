package server

import (
	"context"
	"io"
	"testing"

	"github.com/Bigsy/mcpmu/internal/config"
)

func TestMetadataReloadClassification(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*config.Config)
		want   bool
	}{
		{"compression", func(c *config.Config) { n := c.Namespaces["work"]; n.Compression = "max"; c.Namespaces["work"] = n }, true},
		{"permission", func(c *config.Config) {
			c.ToolPermissions = []config.ToolPermission{{Namespace: "work", Server: "srv1", ToolName: "read_file", Enabled: false}}
		}, true},
		{"denied", func(c *config.Config) {
			s := c.Servers["srv1"]
			s.DeniedTools = []string{"read_file"}
			c.Servers["srv1"] = s
		}, true},
		{"command", func(c *config.Config) { s := c.Servers["srv1"]; s.Command = "changed"; c.Servers["srv1"] = s }, false},
		{"membership", func(c *config.Config) { n := c.Namespaces["work"]; n.ServerIDs = nil; c.Namespaces["work"] = n }, false},
		{"metrics", func(c *config.Config) { c.Metrics = &config.MetricsConfig{Enabled: new(false)} }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			old, next := nsCompressConfig(""), nsCompressConfig("")
			tc.change(next)
			if got := metadataOnlyReload(old, next); got != tc.want {
				t.Fatalf("got %t", got)
			}
		})
	}
}

func TestMetadataReloadRetainsInstanceAndRevokesTool(t *testing.T) {
	cfg := nsCompressConfig("")
	session, err := New(Options{Config: cfg, PIDTrackerDir: t.TempDir(), Stdout: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	ctx := context.Background()
	if _, e := session.handleInitialize(ctx, nil); e != nil {
		t.Fatal(e)
	}
	handle, _, err := session.getOrStartHandle(ctx, "srv1")
	if err != nil {
		t.Fatal(err)
	}
	next := nsCompressConfig("medium")
	next.ToolPermissions = []config.ToolPermission{{Namespace: "work", Server: "srv1", ToolName: "read_file", Enabled: false}}
	session.applyReload(ctx, next)
	retained, _, err := session.getOrStartHandle(ctx, "srv1")
	if err != nil {
		t.Fatal(err)
	}
	if retained != handle || retained.PID() != handle.PID() {
		t.Fatal("metadata restarted upstream")
	}
	if session.compressionLevel() != config.CompressionMedium {
		t.Fatal("compression did not update")
	}
	if _, e := session.handleToolsCall(ctx, []byte(`{"name":"srv1.read_file","arguments":{}}`)); e == nil {
		t.Fatal("revoked tool remained callable")
	}
}
