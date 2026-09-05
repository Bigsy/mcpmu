package server

import (
	"context"
	"fmt"
	"io"
	"log"
	"testing"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/mcp"
	"github.com/Bigsy/mcpmu/internal/process"
)

func benchmarkCatalog(n int) (*verifiedCatalog, *config.Config, []string) {
	catalog := newVerifiedCatalog()
	cfg := config.NewConfig()
	names := []string{}
	for server := range 10 {
		name := fmt.Sprintf("server%d", server)
		names = append(names, name)
		cfg.Servers[name] = config.ServerConfig{Command: "synthetic"}
		tools := make([]mcp.Tool, n/10)
		for i := range tools {
			tools[i] = mcp.Tool{Name: fmt.Sprintf("read_%05d", i), Description: "Read a synthetic record. Returns the requested record.", InputSchema: []byte(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}`)}
		}
		catalog.apply(process.DiscoveryResult{Instance: process.SharedInstanceID(name), Generation: 1, Initialized: true, Tools: tools})
	}
	cfg.Namespaces["work"] = config.NamespaceConfig{ServerIDs: names, DenyByDefault: true, ServerDefaults: map[string]bool{"server0": false, "server2": false, "server4": false}}
	return catalog, cfg, names
}

func BenchmarkToolListing(b *testing.B) {
	for _, n := range []int{100, 1000, 10000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			catalog, cfg, names := benchmarkCatalog(n)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				tools := catalog.toolsForInstances(names, process.SharedInstanceID)
				for _, tool := range tools {
					_, _ = IsToolAllowed(cfg, "work", tool.serverName, tool.origName)
				}
			}
		})
	}
}
func BenchmarkCompression(b *testing.B) {
	for _, n := range []int{100, 1000, 10000} {
		for _, level := range []config.CompressionLevel{config.CompressionLow, config.CompressionMedium, config.CompressionHigh, config.CompressionMax} {
			b.Run(fmt.Sprintf("%d/%s", n, level), func(b *testing.B) {
				catalog, _, names := benchmarkCatalog(n)
				tools := catalog.toolsForInstances(names, process.SharedInstanceID)
				b.ReportAllocs()
				b.ResetTimer()
				var output string
				for b.Loop() {
					output = formatListing(level, tools)
				}
				b.ReportMetric(float64(len(output)), "output-B")
			})
		}
	}
}

// Direct transport and Router cases share a warmed subprocess with zero
// configured latency. Their difference estimates routing overhead, not startup.
func BenchmarkRouting(b *testing.B) {
	oldLog := log.Writer()
	log.SetOutput(io.Discard)
	defer log.SetOutput(oldLog)
	cfg := nsCompressConfig("")
	s, err := New(Options{Config: cfg, PIDTrackerDir: b.TempDir(), Stdout: io.Discard})
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if _, e := s.handleInitialize(ctx, nil); e != nil {
		b.Fatal(e)
	}
	h, _, err := s.getOrStartHandle(ctx, "srv1")
	if err != nil {
		b.Fatal(err)
	}
	b.Run("direct", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, err := h.Client().CallTool(ctx, "read_file", nil); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("router", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, err := s.router.CallTool(ctx, "srv1.read_file", nil, nil); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("parallel", func(b *testing.B) {
		b.ReportAllocs()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				if _, err := s.router.CallTool(ctx, "srv1.read_file", nil, nil); err != nil {
					b.Error(err)
					return
				}
			}
		})
	})
}
