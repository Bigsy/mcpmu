package process

import "fmt"

// InstanceID is the stable identity of one upstream MCP server instance.
// Shared instances have only Server set. Session is reserved for the private
// per-session instances introduced by the daemon isolation phase.
type InstanceID struct {
	Server  string `json:"server"`
	Session string `json:"session,omitempty"`
}

// SharedInstanceID returns the daemon-wide identity for a shared server.
func SharedInstanceID(server string) InstanceID {
	return InstanceID{Server: server}
}

// IsShared reports whether the instance is shared across sessions.
func (id InstanceID) IsShared() bool {
	return id.Session == ""
}

// String returns a stable, human-readable identity.
func (id InstanceID) String() string {
	if id.Session == "" {
		return id.Server
	}
	return fmt.Sprintf("%s@%s", id.Server, id.Session)
}
