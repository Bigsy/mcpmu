package config

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/Bigsy/mcpmu/internal/flock"
)

// toolCacheOp is a ToolCache side effect implied by a Config mutation: a
// renamed server must keep its cached tools under the new key, and a removed
// server (or one whose command/URL changed, so its tool set is suspect) must
// lose them. Config methods record these; Mutate applies them after the save.
type toolCacheOp struct {
	oldName string // empty for a delete
	newName string // empty for a delete
	name    string // set for a delete
}

func (c *Config) noteToolCacheRename(oldName, newName string) {
	c.toolCacheOps = append(c.toolCacheOps, toolCacheOp{oldName: oldName, newName: newName})
}

func (c *Config) noteToolCacheDelete(name string) {
	c.toolCacheOps = append(c.toolCacheOps, toolCacheOp{name: name})
}

// toolSetChanged reports whether an edit to a server makes its cached tool
// list untrustworthy: a different program or endpoint almost certainly
// exposes different tools.
func toolSetChanged(old, updated ServerConfig) bool {
	return old.Command != updated.Command ||
		old.URL != updated.URL ||
		!slices.Equal(old.Args, updated.Args)
}

// Mutate is the one way to change the config file. It runs fn against a
// freshly loaded copy of the file at path (the default path when path is
// empty) while holding the cross-process lock, validates, saves, and returns
// the saved config for the caller to adopt as its in-memory state.
//
// Every surface — CLI, TUI, web — writes through here. A load→edit→save
// sequence anywhere else loses concurrent updates: two processes each load,
// each save, and the second save silently discards the first's change.
//
// ToolCache maintenance implied by fn (see toolCacheOp) is applied to the
// cache co-located with the config. Callers that already hold an open
// ToolCache must use MutateWithCache so their instance stays current.
func Mutate(path string, fn func(*Config) error) (*Config, error) {
	return MutateWithCache(path, nil, fn)
}

// MutateWithCache is Mutate for callers that hold a long-lived *ToolCache
// (TUI, web): rename/delete side effects are applied to tc rather than to a
// throwaway instance loaded from disk, so the caller's in-memory view matches
// the file. A nil tc behaves like Mutate.
func MutateWithCache(path string, tc *ToolCache, fn func(*Config) error) (*Config, error) {
	if path == "" {
		var err error
		path, err = ConfigPath()
		if err != nil {
			return nil, err
		}
	}

	// Lock on the same resolved name SaveTo writes to, so every process —
	// however it spelled the path — contends on one lock file.
	lockPath, err := ResolvePath(path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(lockPath), 0700); err != nil {
		return nil, fmt.Errorf("create config dir: %w", err)
	}

	var saved *Config
	err = flock.WithLock(lockPath, func() error {
		cfg, err := LoadFrom(path)
		if err != nil {
			return fmt.Errorf("reload config: %w", err)
		}
		cfg.toolCacheOps = nil
		if err := fn(cfg); err != nil {
			return err
		}
		if err := cfg.Validate(); err != nil {
			return fmt.Errorf("invalid config: %w", err)
		}
		if err := SaveTo(cfg, path); err != nil {
			return fmt.Errorf("save config: %w", err)
		}
		saved = cfg
		return nil
	})
	if err != nil {
		return nil, err
	}

	ops := saved.toolCacheOps
	saved.toolCacheOps = nil
	if len(ops) > 0 {
		if tc == nil {
			tc, err = NewToolCache(path)
			if err != nil {
				// The config is saved; a missing cache only means stale
				// token counts until the next discovery.
				return saved, nil
			}
		}
		for _, op := range ops {
			if op.name != "" {
				_ = tc.Delete(op.name)
			} else {
				_ = tc.Rename(op.oldName, op.newName)
			}
		}
	}

	return saved, nil
}
