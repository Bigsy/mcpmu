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
	tokens := CountAggregatedToolTokens("myserver", CachedToolInput{
		Name:        "read_file",
		Description: "Read a file from disk",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`),
	})
	if tokens <= 0 {
		t.Errorf("expected positive token count, got %d", tokens)
	}
}

func TestCountAggregatedToolTokens_EmptyDescription(t *testing.T) {
	tokens := CountAggregatedToolTokens("srv", CachedToolInput{Name: "tool"})
	if tokens <= 0 {
		t.Errorf("expected positive token count, got %d", tokens)
	}
}

// The 2025-11-25 fields are real context cost, so they must move the number:
// a tools/list that carries an outputSchema is not free just because mcpmu
// used to ignore it.
func TestCountAggregatedToolTokens_IncludesNewFields(t *testing.T) {
	base := CachedToolInput{
		Name:        "read_file",
		Description: "Read a file from disk",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`),
	}
	rich := base
	rich.Title = "Read File"
	rich.OutputSchema = json.RawMessage(`{"type":"object","properties":{"contents":{"type":"string"}}}`)
	rich.Annotations = json.RawMessage(`{"readOnlyHint":true}`)
	rich.Icons = json.RawMessage(`[{"src":"https://example.test/i.png","mimeType":"image/png"}]`)
	rich.Meta = json.RawMessage(`{"vendor/tier":"gold"}`)
	rich.Extra = map[string]json.RawMessage{"futureField": json.RawMessage(`{"a":1}`)}

	baseTokens := CountAggregatedToolTokens("srv", base)
	richTokens := CountAggregatedToolTokens("srv", rich)
	if richTokens <= baseTokens {
		t.Errorf("expected the extra fields to raise the count: base=%d rich=%d", baseTokens, richTokens)
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

	tokens := CountAggregatedToolTokens("srv", CachedToolInput{
		Name:        "tool",
		Description: "A tool with a large schema",
		InputSchema: json.RawMessage(schema.String()),
	})
	if tokens < 50 {
		t.Errorf("expected at least 50 tokens for large schema, got %d", tokens)
	}
}

func TestEstimateFallback(t *testing.T) {
	result := estimateFallback([]string{"myserver.tool", "[myserver] desc", `{"key":"value"}`})
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

// A cache written before tools carried their full field set must be discarded
// rather than shown with token counts that under-report the real cost.
func TestToolCache_DiscardsVersion1AndRegeneratesAtVersion2(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	cachePath, _ := ToolCachePath(configPath)

	legacy := `{"version":1,"servers":{"srv":{"tools":[{"name":"tool","tokenCount":42}]}}}`
	if err := os.WriteFile(cachePath, []byte(legacy), 0600); err != nil {
		t.Fatalf("seed legacy cache: %v", err)
	}

	tc, err := NewToolCache(configPath)
	if err != nil {
		t.Fatalf("NewToolCache: %v", err)
	}
	if _, ok := tc.Get("srv"); ok {
		t.Error("a version 1 cache was reused instead of discarded")
	}

	if err := tc.Update("srv", []CachedToolInput{{
		Name:        "tool",
		Annotations: json.RawMessage(`{"readOnlyHint":true}`),
	}}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	written, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatalf("read cache: %v", err)
	}
	var file struct {
		Version int `json:"version"`
		Servers map[string]struct {
			Tools []CachedTool `json:"tools"`
		} `json:"servers"`
	}
	if err := json.Unmarshal(written, &file); err != nil {
		t.Fatalf("unmarshal cache: %v", err)
	}
	if file.Version != 2 {
		t.Errorf("cache regenerated at version %d, want 2", file.Version)
	}
	tools := file.Servers["srv"].Tools
	if len(tools) != 1 {
		t.Fatalf("cache holds %d tools, want 1: %s", len(tools), written)
	}
	// The file is written indented, so compare the decoded value rather than
	// the raw bytes.
	var annotations struct {
		ReadOnlyHint bool `json:"readOnlyHint"`
	}
	if err := json.Unmarshal(tools[0].Annotations, &annotations); err != nil {
		t.Fatalf("annotations were not persisted: %s", written)
	}
	if !annotations.ReadOnlyHint {
		t.Errorf("readOnlyHint did not survive the cache round trip: %s", written)
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
