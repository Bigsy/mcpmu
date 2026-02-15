package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/tiktoken-go/tokenizer"
)

const ToolCacheVersion = 1

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

// CachedTool stores a tool definition with its precomputed token count.
type CachedTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
	TokenCount  int             `json:"tokenCount"`
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

// CachedToolInput is the input for updating cached tools (avoids importing events in config).
type CachedToolInput struct {
	Name        string
	Description string
	InputSchema json.RawMessage
}

// Update caches tools for a server, computing token counts in aggregated format.
func (tc *ToolCache) Update(serverID string, tools []CachedToolInput) error {
	tc.mu.Lock()
	defer tc.mu.Unlock()

	cached := make([]CachedTool, len(tools))
	for i, t := range tools {
		cached[i] = CachedTool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
			TokenCount:  CountAggregatedToolTokens(serverID, t.Name, t.Description, t.InputSchema),
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
		entry.Tools[i].TokenCount = CountAggregatedToolTokens(newID, t.Name, t.Description, t.InputSchema)
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
	dir := filepath.Dir(tc.path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create cache dir: %w", err)
	}

	data, err := json.MarshalIndent(tc.cache, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal tool cache: %w", err)
	}

	tmpFile := tc.path + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0600); err != nil {
		return fmt.Errorf("write temp cache: %w", err)
	}

	if err := os.Rename(tmpFile, tc.path); err != nil {
		_ = os.Remove(tmpFile)
		return fmt.Errorf("rename cache: %w", err)
	}

	return nil
}

// CountAggregatedToolTokens counts tokens for a tool in aggregated format
// (matches what tools/list returns to clients via aggregator.go).
func CountAggregatedToolTokens(serverID, toolName, toolDescription string, inputSchema json.RawMessage) int {
	qualifiedName := serverID + "." + toolName

	aggregatedDesc := "[" + serverID + "]"
	if toolDescription != "" {
		aggregatedDesc = "[" + serverID + "] " + toolDescription
	}

	codec, err := tokenizer.Get(tokenizer.Cl100kBase)
	if err != nil {
		return estimateFallback(qualifiedName, aggregatedDesc, inputSchema)
	}

	total := 0
	total += countOrZero(codec, qualifiedName)
	total += countOrZero(codec, aggregatedDesc)
	if len(inputSchema) > 0 {
		total += countOrZero(codec, string(inputSchema))
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

func estimateFallback(name, desc string, schema json.RawMessage) int {
	total := len(name) + len(desc)
	if len(schema) > 0 {
		total += len(schema)
	}
	return total / 4
}
