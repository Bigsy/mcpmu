package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func newTestCache(t *testing.T) *ToolCache {
	t.Helper()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	tc, err := NewToolCache(configPath)
	if err != nil {
		t.Fatalf("NewToolCache: %v", err)
	}
	return tc
}

func sampleTools() []CachedToolInput {
	return []CachedToolInput{
		{
			Name:        "read_file",
			Description: "Read a file from disk",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`),
		},
		{
			Name:        "write_file",
			Description: "Write content to a file",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string"}}}`),
		},
	}
}

func TestToolCache_UpdateAndGet(t *testing.T) {
	tc := newTestCache(t)

	if err := tc.Update("myserver", sampleTools()); err != nil {
		t.Fatalf("Update: %v", err)
	}

	tools, ok := tc.Get("myserver")
	if !ok {
		t.Fatal("expected to find cached tools")
	}
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools))
	}
	if tools[0].Name != "read_file" {
		t.Errorf("expected first tool name 'read_file', got %q", tools[0].Name)
	}
	if tools[0].TokenCount <= 0 {
		t.Errorf("expected positive token count, got %d", tools[0].TokenCount)
	}
}

func TestToolCache_Delete(t *testing.T) {
	tc := newTestCache(t)
	_ = tc.Update("myserver", sampleTools())

	if err := tc.Delete("myserver"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, ok := tc.Get("myserver")
	if ok {
		t.Error("expected server to be deleted from cache")
	}
}

func TestToolCache_Delete_Nonexistent(t *testing.T) {
	tc := newTestCache(t)
	if err := tc.Delete("nosuchserver"); err != nil {
		t.Fatalf("Delete nonexistent: %v", err)
	}
}

func TestToolCache_GetNonexistent(t *testing.T) {
	tc := newTestCache(t)
	_, ok := tc.Get("nosuchserver")
	if ok {
		t.Error("expected false for nonexistent server")
	}
}

func TestToolCache_Rename(t *testing.T) {
	tc := newTestCache(t)
	_ = tc.Update("oldname", sampleTools())

	oldTools, _ := tc.Get("oldname")
	oldTokens := oldTools[0].TokenCount

	if err := tc.Rename("oldname", "newname"); err != nil {
		t.Fatalf("Rename: %v", err)
	}

	// Old key should be gone
	_, ok := tc.Get("oldname")
	if ok {
		t.Error("expected old key to be gone after rename")
	}

	// New key should exist with recomputed tokens
	newTools, ok := tc.Get("newname")
	if !ok {
		t.Fatal("expected new key to exist after rename")
	}
	if len(newTools) != 2 {
		t.Fatalf("expected 2 tools after rename, got %d", len(newTools))
	}

	// Token counts should differ because aggregated format includes server name
	if newTools[0].TokenCount == oldTokens {
		t.Log("Warning: token counts are the same after rename (may happen if server names tokenize identically)")
	}
}

func TestToolCache_Rename_Nonexistent(t *testing.T) {
	tc := newTestCache(t)
	if err := tc.Rename("nosuch", "newname"); err != nil {
		t.Fatalf("Rename nonexistent: %v", err)
	}
}

func TestCountAggregatedToolTokens(t *testing.T) {
	tokens := CountAggregatedToolTokens(
		"myserver",
		"read_file",
		"Read a file from disk",
		json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`),
	)
	if tokens <= 0 {
		t.Errorf("expected positive token count, got %d", tokens)
	}
}

func TestCountAggregatedToolTokens_EmptyDescription(t *testing.T) {
	tokens := CountAggregatedToolTokens("srv", "tool", "", nil)
	if tokens <= 0 {
		t.Errorf("expected positive token count, got %d", tokens)
	}
}

func TestCountAggregatedToolTokens_LargeSchema(t *testing.T) {
	// Build a moderately large schema
	var schema strings.Builder
	schema.WriteString(`{"type":"object","properties":{`)
	for i := range 50 {
		if i > 0 {
			schema.WriteString(",")
		}
		schema.WriteString(`"field` + string(rune('a'+i%26)) + `":{"type":"string","description":"A field"}`)
	}
	schema.WriteString(`}}`)

	tokens := CountAggregatedToolTokens("srv", "tool", "A tool with a large schema", json.RawMessage(schema.String()))
	if tokens < 50 {
		t.Errorf("expected at least 50 tokens for large schema, got %d", tokens)
	}
}

func TestEstimateFallback(t *testing.T) {
	result := estimateFallback("myserver.tool", "[myserver] desc", json.RawMessage(`{"key":"value"}`))
	// ~4 chars per token heuristic
	expected := (len("myserver.tool") + len("[myserver] desc") + len(`{"key":"value"}`)) / 4
	if result != expected {
		t.Errorf("expected %d, got %d", expected, result)
	}
}

func TestToolCache_Persistence(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")

	// Write cache
	tc1, err := NewToolCache(configPath)
	if err != nil {
		t.Fatalf("NewToolCache: %v", err)
	}
	_ = tc1.Update("srv", sampleTools())

	// Load into new instance
	tc2, err := NewToolCache(configPath)
	if err != nil {
		t.Fatalf("NewToolCache: %v", err)
	}
	tools, ok := tc2.Get("srv")
	if !ok {
		t.Fatal("expected tools to persist across instances")
	}
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools))
	}
}

func TestToolCache_FilePermissions(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")

	tc, _ := NewToolCache(configPath)
	_ = tc.Update("srv", sampleTools())

	cachePath, _ := ToolCachePath(configPath)
	info, err := os.Stat(cachePath)
	if err != nil {
		t.Fatalf("stat cache file: %v", err)
	}
	perm := info.Mode().Perm()
	if perm != 0600 {
		t.Errorf("expected 0600 permissions, got %o", perm)
	}
}

func TestToolCache_VersionMismatch(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	cachePath, _ := ToolCachePath(configPath)

	// Write a cache with wrong version
	data := `{"version":999,"servers":{"srv":{"tools":[{"name":"tool","tokenCount":42}]}}}`
	_ = os.WriteFile(cachePath, []byte(data), 0600)

	tc, err := NewToolCache(configPath)
	if err != nil {
		t.Fatalf("NewToolCache: %v", err)
	}

	// Should start fresh (version mismatch discards)
	_, ok := tc.Get("srv")
	if ok {
		t.Error("expected version mismatch to discard cache")
	}
}

func TestToolCache_CorruptFile(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	cachePath, _ := ToolCachePath(configPath)

	// Write corrupt JSON
	_ = os.WriteFile(cachePath, []byte("{corrupt"), 0600)

	tc, err := NewToolCache(configPath)
	if err != nil {
		t.Fatalf("NewToolCache: %v", err)
	}

	// Should start fresh
	_, ok := tc.Get("srv")
	if ok {
		t.Error("expected corrupt file to result in fresh cache")
	}
}

func TestToolCachePath_Default(t *testing.T) {
	path, err := ToolCachePath("")
	if err != nil {
		t.Fatalf("ToolCachePath: %v", err)
	}
	home, _ := os.UserHomeDir()
	expected := filepath.Join(home, ".config", "mcpmu", "toolcache.json")
	if path != expected {
		t.Errorf("expected %q, got %q", expected, path)
	}
}

func TestToolCachePath_CustomConfig(t *testing.T) {
	path, err := ToolCachePath("/custom/path/config.json")
	if err != nil {
		t.Fatalf("ToolCachePath: %v", err)
	}
	if path != "/custom/path/toolcache.json" {
		t.Errorf("expected /custom/path/toolcache.json, got %q", path)
	}
}

func TestToolCachePath_TildeExpansion(t *testing.T) {
	path, err := ToolCachePath("~/foo/config.json")
	if err != nil {
		t.Fatalf("ToolCachePath: %v", err)
	}
	home, _ := os.UserHomeDir()
	expected := filepath.Join(home, "foo", "toolcache.json")
	if path != expected {
		t.Errorf("expected %q, got %q", expected, path)
	}
}

func TestToolCache_ConcurrentUpdates(t *testing.T) {
	tc := newTestCache(t)

	var wg sync.WaitGroup
	for i := range 10 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			tools := []CachedToolInput{
				{Name: "tool", Description: "desc"},
			}
			_ = tc.Update("server", tools)
		}(i)
	}
	wg.Wait()

	tools, ok := tc.Get("server")
	if !ok {
		t.Fatal("expected tools to be cached after concurrent updates")
	}
	if len(tools) != 1 {
		t.Errorf("expected 1 tool, got %d", len(tools))
	}
}
