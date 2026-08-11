package server

import (
	"log"
	"slices"
)

// DownstreamProtocolVersions lists the MCP revisions mcpmu will serve to a
// downstream client, newest first.
//
// Kept separate from mcp.SupportedProtocolVersions (which governs *upstream*
// negotiation) so downstream support can lag upstream support if a revision
// ever adds a server-side obligation mcpmu cannot meet. The two lists are
// identical today.
var DownstreamProtocolVersions = []string{
	"2025-11-25", // current
	"2025-06-18",
	"2025-03-26",
	"2024-11-05", // legacy fallback
}

// LatestDownstreamProtocolVersion is the newest revision mcpmu serves.
func LatestDownstreamProtocolVersion() string {
	return DownstreamProtocolVersions[0]
}

// negotiateProtocolVersion implements the lifecycle spec's downstream half: if
// the client asked for a revision mcpmu supports, echo it back; otherwise
// respond with mcpmu's newest revision and let the client decide whether it can
// proceed. An absent version is treated the same as an unsupported one.
//
// Field passthrough is deliberately *permissive*: mcpmu emits every field an
// upstream server sent regardless of the revision this session negotiated. Per
// JSON-RPC and every MCP client seen in practice, unknown object members are
// ignored, so a 2024-11-05 client is unharmed by an `outputSchema` it does not
// recognise — whereas a field→revision strip table is a maintenance burden that
// has to be updated for every future revision. If a real client is ever found
// that chokes on this, the strict path belongs here, not at each call site.
func negotiateProtocolVersion(requested string) string {
	if slices.Contains(DownstreamProtocolVersions, requested) {
		return requested
	}
	latest := LatestDownstreamProtocolVersion()
	if requested != "" {
		log.Printf("Client requested unsupported protocol version %q, responding with %q",
			requested, latest)
	}
	return latest
}
