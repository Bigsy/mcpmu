package config

import (
	"path/filepath"
	"testing"
)

func TestMetricsConfig_Defaults(t *testing.T) {
	t.Parallel()
	cfg := NewConfig()
	if !cfg.MetricsEnabled() {
		t.Error("absent metrics block should default to enabled")
	}
	if got := cfg.MetricsRetentionDays(); got != DefaultMetricsRetentionDays {
		t.Errorf("retention = %d, want %d", got, DefaultMetricsRetentionDays)
	}

	cfg.Metrics = &MetricsConfig{}
	if !cfg.MetricsEnabled() {
		t.Error("empty metrics block should default to enabled")
	}
	if got := cfg.MetricsRetentionDays(); got != DefaultMetricsRetentionDays {
		t.Errorf("retention = %d, want %d", got, DefaultMetricsRetentionDays)
	}

	disabled := false
	cfg.Metrics = &MetricsConfig{Enabled: &disabled, RetentionDays: 14}
	if cfg.MetricsEnabled() {
		t.Error("explicit enabled=false should disable metrics")
	}
	if got := cfg.MetricsRetentionDays(); got != 14 {
		t.Errorf("retention = %d, want 14", got)
	}

	enabled := true
	cfg.Metrics = &MetricsConfig{Enabled: &enabled, RetentionDays: -5}
	if !cfg.MetricsEnabled() {
		t.Error("explicit enabled=true should enable metrics")
	}
	if got := cfg.MetricsRetentionDays(); got != DefaultMetricsRetentionDays {
		t.Errorf("non-positive retention should fall back to default, got %d", got)
	}
}

func TestMetricsConfig_RoundTrip(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.json")

	disabled := false
	cfg := NewConfig()
	cfg.Metrics = &MetricsConfig{Enabled: &disabled, RetentionDays: 30}
	if err := SaveTo(cfg, path); err != nil {
		t.Fatalf("SaveTo: %v", err)
	}

	loaded, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if loaded.Metrics == nil {
		t.Fatal("metrics block lost in round-trip")
	}
	if loaded.MetricsEnabled() {
		t.Error("enabled=false lost in round-trip")
	}
	if got := loaded.MetricsRetentionDays(); got != 30 {
		t.Errorf("retention = %d, want 30", got)
	}

	// Absent block stays absent (no spurious key written).
	cfg2 := NewConfig()
	if err := SaveTo(cfg2, path); err != nil {
		t.Fatalf("SaveTo: %v", err)
	}
	loaded2, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if loaded2.Metrics != nil {
		t.Error("absent metrics block should stay absent after round-trip")
	}
}
