package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValidateCompression(t *testing.T) {
	for _, valid := range []string{"", "off", "OFF", "low", "medium", "high", "max", "Medium"} {
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

func TestNamespaceCompression_MutationsValidate(t *testing.T) {
	cfg := NewConfig()
	if err := cfg.AddNamespace("work", NamespaceConfig{Compression: "bogus"}); err == nil {
		t.Error("AddNamespace accepted invalid compression level")
	}
	if err := cfg.AddNamespace("work", NamespaceConfig{}); err != nil {
		t.Fatalf("AddNamespace: %v", err)
	}
	if err := cfg.UpdateNamespace("work", NamespaceConfig{Compression: "bogus"}); err == nil {
		t.Error("UpdateNamespace accepted invalid compression level")
	}
}

func TestConfigValidate_RejectsBadNamespaceCompression(t *testing.T) {
	cfg := NewConfig()
	cfg.Namespaces["work"] = NamespaceConfig{Compression: "bogus"}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate accepted invalid namespace compression")
	}
	if !strings.Contains(err.Error(), `namespace "work"`) {
		t.Errorf("error should name the namespace, got: %v", err)
	}
}
