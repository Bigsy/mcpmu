package server

import (
	"fmt"
	"strings"
)

// aggregateInstructions composes the `instructions` member of the downstream
// initialize result from the upstreams in the active namespace.
//
// Each upstream may return its own `instructions` from initialize: guidance to
// the model on how its tools are meant to be used. A proxy that drops them
// silently strips that guidance, so they are concatenated here as one section
// per server, headed by the server name that also prefixes its qualified
// `server.tool` names, and led by a line that explains the layout.
//
// Only shared upstreams that are already running contribute. initialize is
// non-blocking and upstreams start lazily, so a cold first session sees none;
// in daemon mode later sessions see whatever earlier ones started, and --eager
// converges quickly. Private (shared: false) instances are per-session and do
// not exist yet at initialize. The spec offers no notification for a changed
// instructions string, so this is a one-off snapshot either way.
//
// Callers hold s.mu; the supervisor lookup takes only the supervisor's own lock.
func (s *Session) aggregateInstructions() string {
	var b strings.Builder
	for _, name := range s.activeServerNames {
		handle := s.supervisor.Get(name)
		if handle == nil {
			continue
		}
		text := strings.TrimSpace(handle.Instructions())
		if text == "" {
			continue
		}
		if b.Len() == 0 {
			fmt.Fprintf(&b, "Instructions from the MCP servers behind %s. Each section is one server; its tools are exposed as `<section>.<tool>`.",
				s.opts.ServerName)
		}
		fmt.Fprintf(&b, "\n\n## %s\n\n%s", name, text)
	}
	return b.String()
}
