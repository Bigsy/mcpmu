package metrics

import (
	"encoding/json"
	"fmt"
	"log"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Bigsy/mcpmu/internal/process"
)

// StoreVersion is the on-disk schema version of metrics.json.
const StoreVersion = 1

const (
	// recentCap bounds the rolling recent-calls feed.
	recentCap = 200
	// lockTimeout bounds how long a writer waits for the merge lock. A wedged
	// holder costs one skipped flush; the delta is retried on the next tick.
	lockTimeout = 5 * time.Second
)

// MetricsPath returns the metrics file path co-located with the active config.
// Follows the same resolution as config.ToolCachePath (custom config path →
// same directory; default → ~/.config/mcpmu/), plus symlink resolution: the
// daemon canonicalizes its config path while web and TUI use the path as
// given, so a symlinked config (e.g. ~/.config/mcpmu/config.json →
// ~/dotfiles/mcpmu/config.json) would otherwise split writers and readers
// across two metrics.json locations.
func MetricsPath(configPath string) (string, error) {
	path := configPath
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("get home dir: %w", err)
		}
		path = filepath.Join(home, ".config", "mcpmu", "config.json")
	} else if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("get home dir: %w", err)
		}
		path = filepath.Join(home, path[2:])
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	return filepath.Join(filepath.Dir(path), "metrics.json"), nil
}

// Store is the in-memory form of metrics.json.
type Store struct {
	Rows        map[BucketKey]*Counters
	RecentCalls []RecentCall
}

// NewStore returns an empty store.
func NewStore() *Store {
	return &Store{Rows: make(map[BucketKey]*Counters)}
}

// storeFile is the on-disk schema. Rows are a flat array of objects, not
// nested maps keyed by composite strings — tool names come from arbitrary
// upstreams and cannot be trusted not to contain a separator character.
type storeFile struct {
	Version int          `json:"version"`
	Rows    []storeRow   `json:"rows"`
	Recent  []RecentCall `json:"recent,omitempty"`
}

type storeRow struct {
	Date          string             `json:"date"`
	Namespace     string             `json:"namespace,omitempty"`
	Server        string             `json:"server"`
	Tool          string             `json:"tool"`
	Calls         uint64             `json:"calls"`
	Outcomes      map[Outcome]uint64 `json:"outcomes,omitempty"`
	DurationMsSum uint64             `json:"durationMsSum,omitempty"`
	DurationMsMax uint64             `json:"durationMsMax,omitempty"`
	Hist          []uint64           `json:"hist,omitempty"`
}

// Load reads and parses a metrics file. A missing file returns an empty
// store, not an error. No lock is needed for readers — the atomic rename on
// the write path guarantees a consistent file.
func Load(path string) (*Store, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return NewStore(), nil
		}
		return nil, fmt.Errorf("read metrics: %w", err)
	}
	return parseStore(data)
}

func parseStore(data []byte) (*Store, error) {
	var file storeFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("parse metrics: %w", err)
	}
	if file.Version != StoreVersion {
		return nil, fmt.Errorf("unsupported metrics version %d", file.Version)
	}

	store := NewStore()
	for _, row := range file.Rows {
		key := BucketKey{Date: row.Date, Namespace: row.Namespace, Server: row.Server, Tool: row.Tool}
		c := newCounters()
		c.Calls = row.Calls
		maps.Copy(c.Outcomes, row.Outcomes)
		c.DurationMsSum = row.DurationMsSum
		c.DurationMsMax = row.DurationMsMax
		copy(c.Hist[:], row.Hist)
		// Merge rather than overwrite so duplicate keys (hand-edited files)
		// don't silently drop counts.
		if existing, ok := store.Rows[key]; ok {
			existing.merge(c)
		} else {
			store.Rows[key] = c
		}
	}
	store.RecentCalls = file.Recent
	return store, nil
}

// mergeAndSave folds a delta into metrics.json under an exclusive file lock.
// Each flush contributes only its own delta since the last flush, so
// concurrent writers (daemon + embedded serves) never clobber each other's
// counts.
func mergeAndSave(path string, delta map[BucketKey]*Counters, recent []RecentCall, retentionDays int) error {
	release, err := process.LockFileBlocking(path+".lock", lockTimeout)
	if err != nil {
		return fmt.Errorf("acquire metrics lock: %w", err)
	}
	defer release()

	store := loadForMerge(path)

	for key, c := range delta {
		if existing, ok := store.Rows[key]; ok {
			existing.merge(c)
		} else {
			store.Rows[key] = c
		}
	}

	store.RecentCalls = append(store.RecentCalls, recent...)
	slices.SortStableFunc(store.RecentCalls, func(a, b RecentCall) int {
		return b.Time.Compare(a.Time) // newest first
	})
	if len(store.RecentCalls) > recentCap {
		store.RecentCalls = store.RecentCalls[:recentCap]
	}

	store.prune(retentionDays, time.Now())

	return store.saveAtomic(path)
}

// loadForMerge reads the current file for the read-merge-write cycle. An
// unparseable or wrong-version file is renamed to metrics.json.corrupt and
// replaced with a fresh store — never crash a serve process over a metrics
// file.
func loadForMerge(path string) *Store {
	data, err := os.ReadFile(path)
	if err != nil {
		return NewStore()
	}
	store, err := parseStore(data)
	if err != nil {
		log.Printf("Warning: metrics file %s is unusable (%v); moving aside and starting fresh", path, err)
		if renameErr := os.Rename(path, path+".corrupt"); renameErr != nil {
			log.Printf("Warning: failed to move corrupt metrics file: %v", renameErr)
		}
		return NewStore()
	}
	return store
}

// prune drops rows and recent entries older than the retention window.
// ISO date strings sort lexicographically, so a string compare suffices.
func (s *Store) prune(retentionDays int, now time.Time) {
	cutoffTime := now.AddDate(0, 0, -retentionDays)
	cutoff := cutoffTime.Format(dateLayout)
	for key := range s.Rows {
		if key.Date < cutoff {
			delete(s.Rows, key)
		}
	}
	s.RecentCalls = slices.DeleteFunc(s.RecentCalls, func(rc RecentCall) bool {
		return rc.Time.Before(cutoffTime)
	})
}

// saveAtomic writes the store to a pid-suffixed temp file in the same
// directory, fsyncs, then renames over the target.
func (s *Store) saveAtomic(path string) error {
	file := storeFile{Version: StoreVersion, Rows: make([]storeRow, 0, len(s.Rows)), Recent: s.RecentCalls}
	for key, c := range s.Rows {
		file.Rows = append(file.Rows, storeRow{
			Date:          key.Date,
			Namespace:     key.Namespace,
			Server:        key.Server,
			Tool:          key.Tool,
			Calls:         c.Calls,
			Outcomes:      c.Outcomes,
			DurationMsSum: c.DurationMsSum,
			DurationMsMax: c.DurationMsMax,
			Hist:          c.Hist[:],
		})
	}
	// Stable output ordering keeps diffs and tests sane.
	slices.SortFunc(file.Rows, func(a, b storeRow) int {
		if v := strings.Compare(a.Date, b.Date); v != 0 {
			return v
		}
		if v := strings.Compare(a.Namespace, b.Namespace); v != 0 {
			return v
		}
		if v := strings.Compare(a.Server, b.Server); v != 0 {
			return v
		}
		return strings.Compare(a.Tool, b.Tool)
	})

	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal metrics: %w", err)
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create metrics dir: %w", err)
	}

	tmpPath := fmt.Sprintf("%s.tmp.%d", path, os.Getpid())
	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("create temp metrics: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("write temp metrics: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("sync temp metrics: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close temp metrics: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename metrics: %w", err)
	}
	return nil
}
