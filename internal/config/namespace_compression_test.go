package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseCompressionLevel(t *testing.T) {
	valid := map[string]CompressionLevel{
		"":         CompressionOff,
		"off":      CompressionOff,
		" Off ":    CompressionOff,
		"low":      CompressionLow,
		"medium":   CompressionMedium,
		"HIGH":     CompressionHigh,
		"Max":      CompressionMax,
		" medium ": CompressionMedium,
	}
	for in, want := range valid {
		got, err := ParseCompressionLevel(in)
		if err != nil {
			t.Errorf("ParseCompressionLevel(%q) error: %v", in, err)
		}
		if got != want {
			t.Errorf("ParseCompressionLevel(%q) = %q, want %q", in, got, want)
		}
	}
	for _, in := range []string{"maximum", "on", "true", "1", "bogus"} {
		if _, err := ParseCompressionLevel(in); err == nil {
			t.Errorf("ParseCompressionLevel(%q) should fail", in)
		}
	}
	if CompressionOff.Enabled() {
		t.Error("off should not be enabled")
	}
	// Coverage follows the slice, not the hand-maintained map above: a level
	// added to CompressionLevels without a parser case fails here.
	for _, level := range CompressionLevels {
		if !level.Enabled() {
			t.Errorf("%q should be enabled", level)
		}
		got, err := ParseCompressionLevel(string(level))
		if err != nil {
			t.Errorf("ParseCompressionLevel(%q) error: %v", level, err)
		}
		if got != level {
			t.Errorf("ParseCompressionLevel(%q) = %q, want round trip", level, got)
		}
	}
	if len(CompressionLevels) != 4 {
		t.Errorf("CompressionLevels has %d entries, want 4", len(CompressionLevels))
	}
}

func TestNormalizeCompressionLevel(t *testing.T) {
	tests := map[string]string{
		"":         "",
		"off":      "",
		"OFF":      "",
		" Off ":    "",
		"medium":   "medium",
		"MEDIUM":   "medium",
		" medium ": "medium",
		"ultra":    "ultra", // unknown values pass through for validation to see
	}
	for in, want := range tests {
		if got := NormalizeCompressionLevel(in); got != want {
			t.Errorf("NormalizeCompressionLevel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidateCompression(t *testing.T) {
	for _, valid := range []string{"", "off", "OFF", "low", "medium", "high", "max", "Medium", " medium "} {
		if err := ValidateCompression(valid); err != nil {
			t.Errorf("ValidateCompression(%q) = %v, want nil", valid, err)
		}
	}
	for _, invalid := range []string{"bogus", "med", "0", "minimum"} {
		if err := ValidateCompression(invalid); err == nil {
			t.Errorf("ValidateCompression(%q) = nil, want error", invalid)
		}
	}
}

func TestNamespaceCompression_RoundTrip(t *testing.T) {
	cfg := NewConfig()
	if err := cfg.AddNamespace("work", NamespaceConfig{Compression: "medium"}); err != nil {
		t.Fatalf("AddNamespace: %v", err)
	}

	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var loaded Config
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if ns, _ := loaded.GetNamespace("work"); ns.Compression != "medium" {
		t.Errorf("round-tripped compression = %q, want %q", ns.Compression, "medium")
	}

	// An empty level stays absent from the JSON.
	if err := cfg.UpdateNamespace("work", NamespaceConfig{}); err != nil {
		t.Fatalf("UpdateNamespace: %v", err)
	}
	data, err = json.Marshal(cfg)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(data), `"compression"`) {
		t.Errorf("empty compression serialized: %s", data)
	}
}

// TestNamespaceCompression_MutationsNormalizeAndValidate pins that every
// mutation surface stores the canonical form ("" for off, lowercase levels)
// and rejects unknown levels.
func TestNamespaceCompression_MutationsNormalizeAndValidate(t *testing.T) {
	cfg := NewConfig()
	if err := cfg.AddNamespace("work", NamespaceConfig{Compression: "bogus"}); err == nil {
		t.Error("AddNamespace accepted invalid compression level")
	}
	if err := cfg.AddNamespace("work", NamespaceConfig{Compression: "MEDIUM"}); err != nil {
		t.Fatalf("AddNamespace: %v", err)
	}
	if ns, _ := cfg.GetNamespace("work"); ns.Compression != "medium" {
		t.Errorf("AddNamespace stored %q, want normalized %q", ns.Compression, "medium")
	}
	if err := cfg.UpdateNamespace("work", NamespaceConfig{Compression: "bogus"}); err == nil {
		t.Error("UpdateNamespace accepted invalid compression level")
	}
	if err := cfg.UpdateNamespace("work", NamespaceConfig{Compression: " Off "}); err != nil {
		t.Fatalf("UpdateNamespace: %v", err)
	}
	if ns, _ := cfg.GetNamespace("work"); ns.Compression != "" {
		t.Errorf("UpdateNamespace stored %q, want %q (off normalizes away)", ns.Compression, "")
	}
}

// TestLoadFrom_ToleratesAndNormalizesCompression pins the load-time contract:
// a hand-edited or future-version compression level must never brick the
// config (serve degrades it to off at runtime), and recognized-but-
// non-canonical spellings ("OFF", " MEDIUM ") are canonicalized so display and
// edit surfaces can compare literally.
func TestLoadFrom_ToleratesAndNormalizesCompression(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	raw := `{"schemaVersion":1,"servers":{},"namespaces":{
		"future":{"serverIds":[],"compression":"ultra"},
		"shouty":{"serverIds":[],"compression":" MEDIUM "},
		"literal-off":{"serverIds":[],"compression":"off"}}}`
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom must tolerate unknown compression levels, got: %v", err)
	}
	if ns, _ := cfg.GetNamespace("future"); ns.Compression != "ultra" {
		t.Errorf("unknown level = %q, want kept as %q", ns.Compression, "ultra")
	}
	if ns, _ := cfg.GetNamespace("shouty"); ns.Compression != "medium" {
		t.Errorf("mixed-case level = %q, want normalized %q", ns.Compression, "medium")
	}
	if ns, _ := cfg.GetNamespace("literal-off"); ns.Compression != "" {
		t.Errorf("literal off = %q, want normalized %q", ns.Compression, "")
	}
}

// TestDuplicateNamespace_PreservesAllFields guards the copy in
// DuplicateNamespace against silently dropping a field — exactly how the
// Compression field went missing when it was a field-by-field literal.
func TestDuplicateNamespace_PreservesAllFields(t *testing.T) {
	cfg := NewConfig()
	original := NamespaceConfig{
		Description:    "the original",
		ServerIDs:      []string{"a", "b"},
		DenyByDefault:  true,
		ServerDefaults: map[string]bool{"a": true},
		Compression:    "medium",
	}
	cfg.Namespaces["work"] = original

	if err := cfg.DuplicateNamespace("work", "work-copy"); err != nil {
		t.Fatalf("DuplicateNamespace: %v", err)
	}
	dup, ok := cfg.GetNamespace("work-copy")
	if !ok {
		t.Fatal("duplicate not found")
	}

	origJSON, _ := json.Marshal(original)
	dupJSON, _ := json.Marshal(dup)
	if string(origJSON) != string(dupJSON) {
		t.Errorf("duplicate differs from original:\noriginal: %s\nduplicate: %s", origJSON, dupJSON)
	}

	// And it is a deep copy: mutating the duplicate's reference fields must
	// not touch the original.
	dup.ServerIDs[0] = "mutated"
	dup.ServerDefaults["a"] = false
	if ns, _ := cfg.GetNamespace("work"); ns.ServerIDs[0] != "a" || !ns.ServerDefaults["a"] {
		t.Error("duplicate shares reference fields with the original")
	}
}
