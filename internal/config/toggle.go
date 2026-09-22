package config

import (
	"fmt"
	"strings"
)

// Toggle is a tri-state override for a boolean setting: unset defers to the
// config, "on" and "off" win in their direction. Its text form ("", "on",
// "off") is also how it travels in the daemon handshake, and decoding rejects
// anything else, so a malformed handshake cannot silently mean "off".
type Toggle string

const (
	ToggleUnset Toggle = ""
	ToggleOn    Toggle = "on"
	ToggleOff   Toggle = "off"
)

// ParseToggle reads a flag value: on/off (also true/false, yes/no, 1/0), or
// empty for unset.
func ParseToggle(s string) (Toggle, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "":
		return ToggleUnset, nil
	case "on", "true", "yes", "1":
		return ToggleOn, nil
	case "off", "false", "no", "0":
		return ToggleOff, nil
	}
	return ToggleUnset, fmt.Errorf("invalid value %q: expected on or off", s)
}

// Resolve applies the override to the configured value.
func (t Toggle) Resolve(configured bool) bool {
	switch t {
	case ToggleOn:
		return true
	case ToggleOff:
		return false
	}
	return configured
}

// UnmarshalText validates the wire form.
func (t *Toggle) UnmarshalText(text []byte) error {
	switch Toggle(text) {
	case ToggleUnset, ToggleOn, ToggleOff:
		*t = Toggle(text)
		return nil
	}
	return fmt.Errorf("invalid toggle %q: expected \"\", \"on\" or \"off\"", string(text))
}

// SingleCallerHeuristic enables the single-caller routing heuristic, per
// downstream transport: a server-to-client request from a shared instance
// with exactly one call in flight is relayed to that call's session. Nothing
// distinguishes such a request from one left over from an earlier call, so a
// wrong route is possible; that is why it is off unless enabled, and never
// used for sampling. stdio covers embedded and daemon sessions; http covers
// serve --http sessions, which may belong to different people.
type SingleCallerHeuristic struct {
	Stdio bool `json:"stdio,omitempty"`
	HTTP  bool `json:"http,omitempty"`
}

// SingleCallerHeuristicEnabled reports the configured switch for a transport
// ("http" or anything else for stdio/daemon).
func (c *Config) SingleCallerHeuristicEnabled(http bool) bool {
	if c.ElicitationSingleCallerHeuristic == nil {
		return false
	}
	if http {
		return c.ElicitationSingleCallerHeuristic.HTTP
	}
	return c.ElicitationSingleCallerHeuristic.Stdio
}
