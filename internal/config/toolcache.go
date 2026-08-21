package config

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Bigsy/mcpmu/internal/flock"
	"github.com/tiktoken-go/tokenizer"
)

// ToolCacheVersion is bumped whenever CachedTool's shape changes. load()
// discards a cache written at any other version, so the migration is
// self-healing: the next discovery repopulates it.
//
//	1 → 2: tools carry title/outputSchema/annotations/icons/_meta and the
//	       unknown-field catch-all, and token counts include them.
const ToolCacheVersion = 2

// ToolCache stores tool definitions and token counts for servers.
// It is persisted alongside the active config file.
type ToolCache struct {
	path  string
	cache toolCacheFile
	mu    sync.RWMutex
}

type toolCacheFile struct {
	Version int                        `json:"version"`
	Servers map[string]ServerToolCache `json:"servers"`
}

// ServerToolCache stores cached tool data for a single server.
type ServerToolCache struct {
	Tools     []CachedTool `json:"tools"`
	UpdatedAt time.Time    `json:"updatedAt"`
}

// CachedTool stores a tool definition with its precomputed token count. The
// field set mirrors what mcpmu actually sends downstream, so TokenCount stays
// an honest measure of the tool's context cost.
type CachedTool struct {
	Name         string                     `json:"name"`
	Title        string                     `json:"title,omitempty"`
	Description  string                     `json:"description,omitempty"`
	InputSchema  json.RawMessage            `json:"inputSchema,omitempty"`
	OutputSchema json.RawMessage            `json:"outputSchema,omitempty"`
	Annotations  json.RawMessage            `json:"annotations,omitempty"`
	Icons        json.RawMessage            `json:"icons,omitempty"`
	Meta         json.RawMessage            `json:"_meta,omitempty"`
	Extra        map[string]json.RawMessage `json:"extra,omitempty"`
	TokenCount   int                        `json:"tokenCount"`
}

// ToolCachePath returns the cache file path co-located with the active config.
func ToolCachePath(configPath string) (string, error) {
	if configPath != "" {
		expanded := configPath
		if strings.HasPrefix(expanded, "~/") {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", fmt.Errorf("get home dir: %w", err)
			}
			expanded = filepath.Join(home, expanded[2:])
		}
		return filepath.Join(filepath.Dir(expanded), "toolcache.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home dir: %w", err)
	}
	return filepath.Join(home, ".config", "mcpmu", "toolcache.json"), nil
}

// NewToolCache creates or loads a tool cache for the given config path.
func NewToolCache(configPath string) (*ToolCache, error) {
	path, err := ToolCachePath(configPath)
	if err != nil {
		return nil, err
	}
	tc := &ToolCache{
		path: path,
		cache: toolCacheFile{
			Version: ToolCacheVersion,
			Servers: make(map[string]ServerToolCache),
		},
	}
	tc.load()
	return tc, nil
}

// CachedToolInput is the input for updating cached tools (avoids importing
// events in config). It mirrors mcp.Tool minus the fields mcpmu strips.
type CachedToolInput struct {
	Name         string
	Title        string
	Description  string
	InputSchema  json.RawMessage
	OutputSchema json.RawMessage
	Annotations  json.RawMessage
	Icons        json.RawMessage
	Meta         json.RawMessage
	Extra        map[string]json.RawMessage
}

// Update caches tools for a server, computing token counts in aggregated format.
func (tc *ToolCache) Update(serverID string, tools []CachedToolInput) error {
	tc.mu.Lock()
	defer tc.mu.Unlock()

	cached := make([]CachedTool, len(tools))
	for i, t := range tools {
		cached[i] = CachedTool{
			Name:         t.Name,
			Title:        t.Title,
			Description:  t.Description,
			InputSchema:  t.InputSchema,
			OutputSchema: t.OutputSchema,
			Annotations:  t.Annotations,
			Icons:        t.Icons,
			Meta:         t.Meta,
			Extra:        maps.Clone(t.Extra),
			TokenCount:   CountAggregatedToolTokens(serverID, t),
		}
	}
	tc.cache.Servers[serverID] = ServerToolCache{
		Tools:     cached,
		UpdatedAt: time.Now(),
	}
	return tc.save()
}

// Get retrieves cached tools for a server.
func (tc *ToolCache) Get(serverID string) ([]CachedTool, bool) {
	tc.mu.RLock()
	defer tc.mu.RUnlock()

	entry, ok := tc.cache.Servers[serverID]
	if !ok {
		return nil, false
	}
	return entry.Tools, true
}

// Delete removes a server from the cache.
func (tc *ToolCache) Delete(serverID string) error {
	tc.mu.Lock()
	defer tc.mu.Unlock()

	if _, ok := tc.cache.Servers[serverID]; !ok {
		return nil
	}
	delete(tc.cache.Servers, serverID)
	return tc.save()
}

// Rename migrates a cache entry to a new key and recomputes token counts
// (aggregated format includes server name).
func (tc *ToolCache) Rename(oldID, newID string) error {
	tc.mu.Lock()
	defer tc.mu.Unlock()

	entry, ok := tc.cache.Servers[oldID]
	if !ok {
		return nil
	}

	// Recompute token counts with new server name
	for i, t := range entry.Tools {
		entry.Tools[i].TokenCount = CountAggregatedToolTokens(newID, t.input())
	}
	entry.UpdatedAt = time.Now()

	delete(tc.cache.Servers, oldID)
	tc.cache.Servers[newID] = entry
	return tc.save()
}

func (tc *ToolCache) load() {
	data, err := os.ReadFile(tc.path)
	if err != nil {
		return
	}

	var file toolCacheFile
	if err := json.Unmarshal(data, &file); err != nil {
		return
	}

	// Version mismatch — discard stale cache
	if file.Version != ToolCacheVersion {
		return
	}

	if file.Servers == nil {
		file.Servers = make(map[string]ServerToolCache)
	}
	tc.cache = file
}

func (tc *ToolCache) save() error {
	// Cross-process writers (TUI, web, daemon share this file) serialise on a
	// dedicated lock file. The in-process tc.mu only orders goroutines; the
	// fixed .tmp path + rename this once used could interleave across
	// processes and tear the file.
	return flock.WithLock(tc.path, func() error {
		// Resolve symlinks so the atomic rename targets the real file
		path := tc.path
		if resolved, err := filepath.EvalSymlinks(path); err == nil {
			path = resolved
		}

		data, err := json.MarshalIndent(tc.cache, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal tool cache: %w", err)
		}
		if err := flock.WriteAtomic(path, data); err != nil {
			return fmt.Errorf("write cache: %w", err)
		}
		return nil
	})
}

// input reconstructs the counting input for an already-cached tool.
func (t CachedTool) input() CachedToolInput {
	return CachedToolInput{
		Name:         t.Name,
		Title:        t.Title,
		Description:  t.Description,
		InputSchema:  t.InputSchema,
		OutputSchema: t.OutputSchema,
		Annotations:  t.Annotations,
		Icons:        t.Icons,
		Meta:         t.Meta,
		Extra:        t.Extra,
	}
}

// CountAggregatedToolTokens counts tokens for a tool in aggregated format
// (matches what tools/list returns to clients via aggregator.go). Every field
// mcpmu forwards is counted — an outputSchema or an icons array is real
// context cost, and omitting it would make the TUI's per-server figures
// under-report exactly the servers that cost the most.
func CountAggregatedToolTokens(serverID string, tool CachedToolInput) int {
	qualifiedName := serverID + "." + tool.Name

	aggregatedDesc := "[" + serverID + "]"
	if tool.Description != "" {
		aggregatedDesc = "[" + serverID + "] " + tool.Description
	}

	texts := make([]string, 0, 8)
	texts = append(texts, qualifiedName, aggregatedDesc)
	if tool.Title != "" {
		texts = append(texts, tool.Title)
	}
	for _, raw := range []json.RawMessage{
		tool.InputSchema, tool.OutputSchema, tool.Annotations, tool.Icons, tool.Meta,
	} {
		if len(raw) > 0 {
			texts = append(texts, string(raw))
		}
	}
	for _, key := range slices.Sorted(maps.Keys(tool.Extra)) {
		texts = append(texts, key, string(tool.Extra[key]))
	}

	codec, err := tokenizer.Get(tokenizer.Cl100kBase)
	if err != nil {
		return estimateFallback(texts)
	}

	total := 0
	for _, text := range texts {
		total += countOrZero(codec, text)
	}
	return total
}

func countOrZero(codec tokenizer.Codec, text string) int {
	tokens, _, err := codec.Encode(text)
	if err != nil {
		return len(text) / 4
	}
	return len(tokens)
}

func estimateFallback(texts []string) int {
	total := 0
	for _, text := range texts {
		total += len(text)
	}
	return total / 4
}
