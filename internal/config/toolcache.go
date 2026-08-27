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
//
// Several processes share one file (each `serve` namespace, the web UI, the
// TUI), so every mutation is a read-merge-write under the cross-process lock:
// the on-disk file is authoritative and the in-memory copy is a snapshot of
// it. Writing the whole in-memory map would let one process's stale snapshot
// clobber tools another process had just discovered.
type ToolCache struct {
	path  string
	cache toolCacheFile
	// modTime is the mtime of the file the snapshot came from; Get reloads
	// when the file on disk has moved past it.
	modTime time.Time
	mu      sync.RWMutex
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
	return tc.mutate(func(file *toolCacheFile) {
		file.Servers[serverID] = ServerToolCache{
			Tools:     cached,
			UpdatedAt: time.Now(),
		}
	})
}

// Get retrieves cached tools for a server.
func (tc *ToolCache) Get(serverID string) ([]CachedTool, bool) {
	tc.refresh()
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

	return tc.mutate(func(file *toolCacheFile) {
		delete(file.Servers, serverID)
	})
}

// Rename migrates a cache entry to a new key and recomputes token counts
// (aggregated format includes server name).
func (tc *ToolCache) Rename(oldID, newID string) error {
	tc.mu.Lock()
	defer tc.mu.Unlock()

	return tc.mutate(func(file *toolCacheFile) {
		entry, ok := file.Servers[oldID]
		if !ok {
			return
		}
		// Recompute token counts with new server name
		for i, t := range entry.Tools {
			entry.Tools[i].TokenCount = CountAggregatedToolTokens(newID, t.input())
		}
		entry.UpdatedAt = time.Now()
		delete(file.Servers, oldID)
		file.Servers[newID] = entry
	})
}

func (tc *ToolCache) load() {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	if file, modTime, ok := tc.readFile(); ok {
		tc.cache, tc.modTime = file, modTime
	}
}

// readFile parses the on-disk cache. ok is false when the file is missing,
// unreadable, corrupt, or at a different version — all cases where the disk
// contents should be treated as empty and rewritten from scratch.
func (tc *ToolCache) readFile() (file toolCacheFile, modTime time.Time, ok bool) {
	info, err := os.Stat(tc.path)
	if err != nil {
		return file, modTime, false
	}
	data, err := os.ReadFile(tc.path)
	if err != nil {
		return file, modTime, false
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return file, modTime, false
	}
	// Version mismatch — discard stale cache
	if file.Version != ToolCacheVersion {
		return file, modTime, false
	}
	if file.Servers == nil {
		file.Servers = make(map[string]ServerToolCache)
	}
	return file, info.ModTime(), true
}

// refresh reloads the snapshot when another process has rewritten the file
// since it was last read, so long-lived readers (web UI, TUI) see discovery
// done by serve processes without restarting.
func (tc *ToolCache) refresh() {
	info, err := os.Stat(tc.path)
	if err != nil {
		return
	}
	tc.mu.RLock()
	stale := info.ModTime().After(tc.modTime)
	tc.mu.RUnlock()
	if !stale {
		return
	}
	tc.load()
}

// mutate applies fn to the latest on-disk contents and writes the result
// back, all under the cross-process lock, then adopts the result as the
// in-memory snapshot. Cross-process writers (TUI, web, daemon share this
// file) serialise on a dedicated lock file; the in-process tc.mu only orders
// goroutines. Callers must hold tc.mu for writing.
func (tc *ToolCache) mutate(fn func(file *toolCacheFile)) error {
	return flock.WithLock(tc.path, func() error {
		file, _, ok := tc.readFile()
		if !ok {
			file = toolCacheFile{
				Version: ToolCacheVersion,
				Servers: make(map[string]ServerToolCache),
			}
		}
		fn(&file)

		// Resolve symlinks so the atomic rename targets the real file
		path := tc.path
		if resolved, err := filepath.EvalSymlinks(path); err == nil {
			path = resolved
		}

		data, err := json.MarshalIndent(file, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal tool cache: %w", err)
		}
		if err := flock.WriteAtomic(path, data); err != nil {
			return fmt.Errorf("write cache: %w", err)
		}
		tc.cache = file
		if info, err := os.Stat(tc.path); err == nil {
			tc.modTime = info.ModTime()
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
