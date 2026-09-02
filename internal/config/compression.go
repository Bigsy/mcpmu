package config

import (
	"fmt"
	"strings"
)

// CompressionLevel is the serve-mode compressed tool surface level: how much
// of each tool's metadata survives into the compact listing the wrapper tools
// carry. The zero value means off. Levels follow atlassian-labs/mcp-compressor
// so their docs describe ours: low = full description, medium = first
// sentence, high = args only, max = name only.
//
// This is the one definition shared by --compress flag parsing, namespace
// config validation (NamespaceConfig.Compression stores the string form), and
// the serve session's per-request resolution.
type CompressionLevel string

const (
	CompressionOff    CompressionLevel = ""
	CompressionLow    CompressionLevel = "low"
	CompressionMedium CompressionLevel = "medium"
	CompressionHigh   CompressionLevel = "high"
	CompressionMax    CompressionLevel = "max"
)

// CompressionLevels are the valid non-off values, in ascending order.
var CompressionLevels = []CompressionLevel{CompressionLow, CompressionMedium, CompressionHigh, CompressionMax}

// ParseCompressionLevel accepts "", "off", or a level, case-insensitively and
// whitespace-trimmed. Empty and "off" both mean disabled. This is the one
// parser; NormalizeCompressionLevel and ValidateCompression are thin wrappers
// kept for the string-typed config field.
func ParseCompressionLevel(s string) (CompressionLevel, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "off":
		return CompressionOff, nil
	case "low":
		return CompressionLow, nil
	case "medium":
		return CompressionMedium, nil
	case "high":
		return CompressionHigh, nil
	case "max":
		return CompressionMax, nil
	default:
		return CompressionOff, fmt.Errorf("invalid compression level %q (valid: off, low, medium, high, max)", s)
	}
}

// Enabled reports whether the level turns the compressed surface on.
func (l CompressionLevel) Enabled() bool {
	return l != CompressionOff
}

// NormalizeCompressionLevel canonicalizes a compression value for storage:
// whitespace trimmed, lowercased, and "off" mapped to "" — so a stored level
// is always either absent or an exact CompressionLevels member, and every
// display/edit surface can compare it literally. Unknown values are returned
// (trimmed/lowercased) rather than dropped, so validation still sees them.
func NormalizeCompressionLevel(level string) string {
	if parsed, err := ParseCompressionLevel(level); err == nil {
		return string(parsed)
	}
	return strings.ToLower(strings.TrimSpace(level))
}

// ValidateCompression checks a namespace compression value. Empty and "off"
// both mean disabled; mutation paths normalize before storing so only "" and
// CompressionLevels members reach the config.
func ValidateCompression(level string) error {
	_, err := ParseCompressionLevel(level)
	return err
}

// CompressionOverride is the tri-state --compress flag: unset (the active
// namespace's configured level decides), an explicit off, or an explicit
// level. Its text form is "" / "off" / "<level>", which is also how it travels
// in the daemon handshake.
type CompressionOverride struct {
	set   bool
	level CompressionLevel
}

// CompressionUnset is the zero override: no flag was given, so the active
// namespace's configured level decides.
func CompressionUnset() CompressionOverride { return CompressionOverride{} }

// CompressionForce is an explicit --compress value. The level may be
// CompressionOff, which forces compression off even when the active namespace
// configures a level.
func CompressionForce(level CompressionLevel) CompressionOverride {
	return CompressionOverride{set: true, level: level}
}

// Set reports whether the flag was given at all.
func (o CompressionOverride) Set() bool { return o.set }

// Resolve applies the override to a namespace-configured value. An explicit
// flag wins in both directions; otherwise the stored level applies. A stored
// value that does not parse degrades to off: mutation paths and load-time
// validation reject bad levels, so anything that slips through (a hand-edited
// config) should not fail every tools/list.
func (o CompressionOverride) Resolve(configured string) CompressionLevel {
	if o.set {
		return o.level
	}
	level, err := ParseCompressionLevel(configured)
	if err != nil {
		return CompressionOff
	}
	return level
}

// ParseCompressionOverride builds an override from a flag value and whether
// the flag was given at all. A bad level is an error. The `given` bool is what
// distinguishes an explicitly empty flag value (which forces compression off,
// like `--compress off`) from an absent flag — the text encoding has no such
// bool, which is why UnmarshalText reads an empty value as unset instead.
func ParseCompressionOverride(s string, given bool) (CompressionOverride, error) {
	level, err := ParseCompressionLevel(s)
	if err != nil {
		return CompressionOverride{}, err
	}
	if !given {
		return CompressionUnset(), nil
	}
	return CompressionForce(level), nil
}

// MarshalText renders the wire form: "" when unset, "off" when forced off,
// otherwise the level.
func (o CompressionOverride) MarshalText() ([]byte, error) {
	if !o.set {
		return nil, nil
	}
	if o.level == CompressionOff {
		return []byte("off"), nil
	}
	return []byte(o.level), nil
}

// UnmarshalText is MarshalText's inverse. An empty (or whitespace-only) value
// decodes to unset — which is also how an absent handshake key decodes, so an
// older shim that never sent the field is read correctly. Anything else must
// be "off" or a valid level.
func (o *CompressionOverride) UnmarshalText(b []byte) error {
	if strings.TrimSpace(string(b)) == "" {
		*o = CompressionUnset()
		return nil
	}
	level, err := ParseCompressionLevel(string(b))
	if err != nil {
		return err
	}
	*o = CompressionForce(level)
	return nil
}
