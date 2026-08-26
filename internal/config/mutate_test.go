package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func writeTestConfig(t *testing.T, cfg *Config) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := SaveTo(cfg, path); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	return path
}

func TestMutate_ConcurrentWritersBothSurvive(t *testing.T) {
	cfg := NewConfig()
	cfg.Servers["a"] = ServerConfig{Command: "a"}
	cfg.Servers["b"] = ServerConfig{Command: "b"}
	path := writeTestConfig(t, cfg)

	const rounds = 20
	var wg sync.WaitGroup
	errs := make(chan error, 2*rounds)
	for i := range rounds {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, err := Mutate(path, func(c *Config) error {
				return c.DenyTool("a", "tool-a-"+string(rune('a'+i)))
			})
			errs <- err
		}()
		go func() {
			defer wg.Done()
			_, err := Mutate(path, func(c *Config) error {
				return c.DenyTool("b", "tool-b-"+string(rune('a'+i)))
			})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("mutate: %v", err)
		}
	}

	got, err := LoadFrom(path)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(got.Servers["a"].DeniedTools); n != rounds {
		t.Errorf("server a: %d denied tools, want %d (lost updates)", n, rounds)
	}
	if n := len(got.Servers["b"].DeniedTools); n != rounds {
		t.Errorf("server b: %d denied tools, want %d (lost updates)", n, rounds)
	}
}

func TestMutate_FnErrorLeavesFileUntouched(t *testing.T) {
	cfg := NewConfig()
	cfg.Servers["a"] = ServerConfig{Command: "a"}
	path := writeTestConfig(t, cfg)
	before, _ := os.ReadFile(path)

	_, err := Mutate(path, func(c *Config) error {
		c.Servers["b"] = ServerConfig{Command: "b"}
		return os.ErrInvalid
	})
	if err == nil {
		t.Fatal("expected fn error to propagate")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Error("file changed although fn returned an error")
	}
}

func TestMutate_InvalidResultIsRejected(t *testing.T) {
	path := writeTestConfig(t, NewConfig())
	_, err := Mutate(path, func(c *Config) error {
		// Bypass AddServer's validation deliberately.
		c.Servers["bad"] = ServerConfig{Command: "x", URL: "https://x"}
		return nil
	})
	if err == nil {
		t.Fatal("expected validation error")
	}
}

func TestMutate_CreatesMissingConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.json")
	saved, err := Mutate(path, func(c *Config) error {
		return c.AddServer("a", ServerConfig{Command: "a"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := saved.Servers["a"]; !ok {
		t.Error("returned config lacks the added server")
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("config not written: %v", err)
	}
}

func seedToolCache(t *testing.T, configPath string, servers ...string) *ToolCache {
	t.Helper()
	tc, err := NewToolCache(configPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range servers {
		if err := tc.Update(s, []CachedToolInput{{Name: "t", InputSchema: json.RawMessage(`{}`)}}); err != nil {
			t.Fatal(err)
		}
	}
	return tc
}

func TestMutate_RenameMovesToolCacheEntry(t *testing.T) {
	cfg := NewConfig()
	cfg.Servers["old"] = ServerConfig{Command: "x"}
	path := writeTestConfig(t, cfg)
	seedToolCache(t, path, "old")

	if _, err := Mutate(path, func(c *Config) error {
		return c.RenameServer("old", "new")
	}); err != nil {
		t.Fatal(err)
	}

	tc, _ := NewToolCache(path)
	if _, ok := tc.Get("old"); ok {
		t.Error("toolcache still has entry under old name")
	}
	if _, ok := tc.Get("new"); !ok {
		t.Error("toolcache has no entry under new name")
	}
}

func TestMutateWithCache_DeleteAndToolSetChangeEvictInCallerCache(t *testing.T) {
	cfg := NewConfig()
	cfg.Servers["gone"] = ServerConfig{Command: "x"}
	cfg.Servers["edited"] = ServerConfig{Command: "x"}
	cfg.Servers["same"] = ServerConfig{Command: "x"}
	path := writeTestConfig(t, cfg)
	tc := seedToolCache(t, path, "gone", "edited", "same")

	_, err := MutateWithCache(path, tc, func(c *Config) error {
		if err := c.DeleteServer("gone"); err != nil {
			return err
		}
		if err := c.UpdateServer("edited", ServerConfig{Command: "y"}); err != nil {
			return err
		}
		return c.UpdateServer("same", ServerConfig{Command: "x", Autostart: true})
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, ok := tc.Get("gone"); ok {
		t.Error("deleted server still cached in caller's ToolCache")
	}
	if _, ok := tc.Get("edited"); ok {
		t.Error("server with changed command still cached")
	}
	if _, ok := tc.Get("same"); !ok {
		t.Error("server with unrelated edit lost its cache entry")
	}
}

func TestMutate_ReturnedConfigCarriesNoPendingOps(t *testing.T) {
	cfg := NewConfig()
	cfg.Servers["a"] = ServerConfig{Command: "x"}
	path := writeTestConfig(t, cfg)

	saved, err := Mutate(path, func(c *Config) error { return c.DeleteServer("a") })
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.toolCacheOps) != 0 {
		t.Error("ops not drained after Mutate")
	}
}
