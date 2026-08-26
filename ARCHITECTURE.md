# mcpmu Architecture

## Overview

mcpmu is an MCP server aggregator that manages multiple MCP servers and exposes their tools through a unified interface.

```
┌─────────────────────────────────────────────────────────────┐
│                      Claude Code / Codex                     │
│                         (MCP Client)                         │
└─────────────────────────────┬───────────────────────────────┘
                              │ spawns via stdin/stdout
                              ▼
┌─────────────────────────────────────────────────────────────┐
│                        mcpmu                            │
│                                                              │
│  ┌──────────────────────────────────────────────────────┐   │
│  │                    stdio Server                       │   │
│  │              (MCP JSON-RPC protocol)                  │   │
│  └──────────────────────────┬───────────────────────────┘   │
│                              │                               │
│  ┌──────────────────────────┴───────────────────────────┐   │
│  │                  Tool Aggregator                      │   │
│  │         (collects tools from managed servers)         │   │
│  │         (routes tool calls to correct server)         │   │
│  └──────────────────────────┬───────────────────────────┘   │
│                              │                               │
│  ┌──────────────────────────┴───────────────────────────┐   │
│  │                    Supervisor                         │   │
│  │           (manages server process lifecycle)          │   │
│  └──────────────────────────┬───────────────────────────┘   │
│                              │                               │
│  ┌──────────────────────────┴───────────────────────────┐   │
│  │                      Config                           │   │
│  │            (~/.config/mcpmu/config.json)         │   │
│  └──────────────────────────────────────────────────────┘   │
└─────────────────────────────┬───────────────────────────────┘
                              │ spawns & manages
                              ▼
┌─────────────────────────────────────────────────────────────┐
│                    Managed MCP Servers                       │
│                                                              │
│   ┌─────────────┐  ┌─────────────┐  ┌─────────────┐         │
│   │ filesystem  │  │   github    │  │   sqlite    │  ...    │
│   │   server    │  │   server    │  │   server    │         │
│   └─────────────┘  └─────────────┘  └─────────────┘         │
└─────────────────────────────────────────────────────────────┘
```

## Primary Usage: stdio Mode

Claude Code/Codex spawns mcpmu as a subprocess. On Unix, the default `serve`
path is a stdio-to-Unix-socket shim connected to one shared daemon per canonical
config path. The daemon owns one Core and attaches one Session per shim.
Top-level `daemonMode: false` disables daemon mode globally, `--isolated`
forces embedded behavior for one process, and every daemon setup failure falls
back to embedded serve. Windows remains embedded. `mcpmu serve --http` is a
third mode — a dedicated foreground process exposing the same endpoint over
Streamable HTTP (see "HTTP Serve Mode" below).

```json
// ~/.claude/mcp_servers.json
{
  "mcpmu": {
    "command": "mcpmu",
    "args": ["serve", "--stdio", "--config", "~/.config/mcpmu/config.json", "--namespace", "default"]
  }
}
```

### Serve Core and Session Layers

Serve mode is split into two layers inside `internal/server`:

- `Core` owns the configuration, hot-reload watcher, process supervisor,
  upstream tool aggregator, the common lazy-start/readiness path, and the
  upstream notification broadcaster and resource-subscription refcounter.
- `Session` owns one downstream MCP connection: initialize state and
  negotiated capabilities, namespace selection, permission context, resource
  URI routing and its local subscription view, and the JSON-RPC read/write
  loop. It also owns discovery state for servers configured with
  `shared: false`.

The embedded stdio path uses `server.New` to construct one
`Core` and attaches exactly one stdio `Session` in the same process. Upstream
notifications enter through a Core-owned worker-backed broadcaster, so the
Supervisor does not depend on a particular client connection and never runs a
refresh request on its MCP client's response-reader goroutine. The broadcaster
supports multiple independently buffered Session subscribers; embedded mode
uses one. All lazy tool, resource, prompt, and discovery acquisition uses the
same Core helper.
That helper enforces each server's `startup_timeout_sec` (10 seconds by
default) across startup, initialization, and initial tool discovery.

Embedded serve keeps the same stdio wire protocol and process ownership. The
same boundary also lets the internal daemon attach multiple Sessions to one
Core without duplicating upstream processes.

Server instance identity is explicit throughout the Supervisor, PID registry,
subscriptions, notifications, and manager tools. Shared servers use the server
name as their identity. A server with `shared: false` uses `(server,
sessionID)`, is discovered in that Session's private catalog, and is stopped
and forgotten when the Session disconnects. Private upstream notifications
are delivered only to their owner, and manager operations resolve the caller's
private instance. Browser automation and interpreter/REPL servers are the
primary candidates for this isolation escape hatch.

### Shared Daemon and Shim

`internal/daemon` provides the Unix listener and shared Core;
`internal/shim` implements the stdio bridge and connect-or-spawn protocol.
An absent or true top-level `daemonMode` selects it; explicit false keeps every
serve embedded. The hidden commands are available for development and
diagnostics:

```bash
mcpmu --config /absolute/or/relative/config.json daemon run --foreground
mcpmu --config /absolute/or/relative/config.json daemon status
mcpmu --config /absolute/or/relative/config.json daemon stop
```

There is one daemon identity per canonical config path. Canonicalization
expands `~`, makes the path absolute, resolves symlinks, and preserves a
not-yet-created suffix below the nearest existing ancestor. A short hash of
that path names the Unix socket, run lock, pidfile, and log in a user-owned
runtime directory; the complete canonical path remains authoritative in every
handshake and pidfile, so a short-hash collision cannot select another config.
Runtime directories are mode `0700`, sockets and pidfiles are `0600`, and
Linux/macOS connections additionally require a matching peer UID.

Session connections use a versioned pre-MCP handshake carrying the executable
content hash, canonical config path, namespace, eager setting, manager-tool
visibility, and resource/prompt passthrough flags. Once accepted, the socket is
ordinary NDJSON MCP. Each Session has one bounded outbound queue; a client that
cannot drain is disconnected instead of blocking the shared Core. Control
connections have a separately frozen protocol and tolerate executable-build
mismatch so a newly installed CLI can still inspect or stop an older daemon.

The daemon owns config watching and applies each reload once at Core scope:
subscriptions and URI maps are cleared, upstreams are stopped, every attached
Session re-resolves its namespace and permissions, eager Sessions restart
their selected shared and private instances, and capability-scoped list-change
notifications are sent to each Session. Embedded serve retains its
single-Session reload consumer.

A spawn lock serializes concurrent cold starts. The winning shim rechecks the
socket, proves staleness only when the daemon run lock is free, removes a stale
socket, and starts the current executable in a detached session with the full
canonical config path on its command line. All contenders then perform the
normal build/protocol/config handshake. Rejection or any setup timeout returns
that serve process to embedded mode without stopping a daemon that may still be
serving compatible clients.

Once connected, the shim copies MCP bytes without interpreting them. Daemon
EOF ends the shim even while client stdin is open; client stdin EOF half-closes
the socket so queued responses can drain for at most five seconds. The daemon
writer uses an ordered flush marker before closing such a completed Session.

The daemon inherits the environment and working directory of the shim that won
the spawn race. Server configs should therefore use absolute `cwd` values and
explicit `env` entries; `env_http_headers` also resolve in that inherited daemon
environment. Environment is deliberately not part of daemon identity because
that would fragment sharing per caller.

Shared upstreams also share server-side login state, OAuth/token use, and rate
limits. Manager actions resolve through the calling Session: for a shared
instance, `mcpmu.servers_stop` stops it for every Session and the next use
starts it again; for `shared: false`, stop/restart affects only the caller's
private instance.

A run lock serializes daemon ownership. The first and last Session transitions
control a 60-second linger timer; `daemon stop` rejects new Sessions, drains
existing ones for at most 30 seconds, then cancels them. SIGTERM follows the
same path. Normal exit closes the shared Core (and therefore every upstream
process group), removes the socket and pidfile, and retains the per-config log.
The pidfile records the full config path, PID/start identity, and executable;
control fallback validates all of that identity before signalling anything.

**Platform support (2026-08-26, D2)**: macOS and Linux are the supported
platforms. Windows builds and runs in embedded mode but is *best-effort*: there
is no Windows CI job, process-tree termination uses `Kill()` on the leader only,
and the PID tracker/orphan cleanup are disabled there. Windows-only defects are
tracked at P3 in `PLAN.md` until a CI job exists.

### HTTP Serve Mode

`mcpmu serve --http` exposes the same aggregation endpoint over the MCP
Streamable HTTP transport (POST + standalone GET SSE stream), implemented in
`internal/httpserve`. It is a deliberate, foreground, long-lived process (like
`mcpmu web`) that owns one `Core` directly and attaches one `server.Session`
per HTTP client session. It never rendezvouses with the Unix daemon: the
daemon is spawned implicitly by whichever client shim wins the race, and a
network port opened as a side effect of a client spawn would be a security
surprise; the daemon's linger-driven lifetime also could not outlive its
sessions to accept the next connection. A dedicated process additionally gives
HTTP mode to Windows for free.

**Routes and namespace selection.** One URL per namespace replaces stdio's
per-client `--namespace` flag:

```
POST   /mcp                  → default namespace (same auto-select as stdio)
POST   /mcp/{namespace}      → that namespace
GET    /mcp[/{namespace}]    → standalone SSE stream for the session
DELETE /mcp[/{namespace}]    → terminate the session
GET    /healthz              → unauthenticated readiness probe
```

The namespace is resolved when the session is created (initialize) and
enforced on every request: a session ID minted under `/mcp/work` presented to
`/mcp/personal` is a 404. Session IDs are 128-bit `crypto/rand` hex — they are
bearer-ish within an authenticated origin.

**Session lifecycle.** Sessions are created only by an `initialize` POST
(which returns the `Mcp-Session-Id` header) and end via DELETE, idle reap, or
shutdown — all funnelled through one teardown that runs the same cleanup a
stdio EOF runs (unsubscribes, refcounts, stops private instances). The idle
reaper (default 30m, `--session-idle-timeout`) counts only client actions —
POST, DELETE, GET attach — never keepalive writes, because a small write into
a black-holed TCP connection "succeeds" into the socket buffer for minutes.
A request counts as activity for as long as it is being dispatched, not just
when it arrives: admission (lookup + busy-marking) and reaping run under the
same lock, so a session with work in flight is skipped and a request can
never begin dispatch against a session the reaper is tearing down. A tool
call slower than the idle timeout is therefore never reaped out from under
itself, and the idle clock restarts from the response.
The reaper is the HTTP replacement for stdio's EOF and is what makes
`shared: false` (one private upstream instance per session) safe here: session
count is process count.

**Dispatch.** Each POST-carried request is dispatched synchronously on its
handler goroutine via `Session.Dispatch` (the transport-neutral half of the
stdio loop). The dispatch context is a child of the session context,
additionally cancelled when the POST's request context ends, so a vanished
client stops burning upstream tool-call time; such cancellations record the
`cancelled` metrics outcome, which stays out of the error rate.
`http.Server.Shutdown` draining active requests is therefore also the handler
drain mechanism — and since `ListenAndServe` returns the moment the listener
closes, the command waits for `Shutdown` to return before closing the `Core`,
which would otherwise stop upstream processes and write the final metrics
flush mid-drain. Notifications return 202, as do client responses (an id with
no method — mcpmu issues no server→client requests, so there is nothing to
correlate them with); parse errors and batch arrays (removed from the spec in
2025-06-18) return 400.

**sseHub.** Each session's `Session.writer` is an `sseHub`: every notification
arrives as one frame (single-write `send`), is queued under a short mutex, and
is delivered by the currently attached GET stream's own goroutine as
`event: message` SSE events with 30s `: ping` keepalives under per-write
deadlines. Queued frames coalesce — `*/list_changed` per method,
`resources/updated` per URI, progress per token — so the 256-frame backlog cap
is practically unreachable; a second GET replaces the first (clients reconnect
faster than dead connections are detected) and draining is ownership-checked —
an evicted handler that wins the shared wake signal takes nothing, so frames
never land on the abandoned connection. Stream disconnect never ends
the session. Unlike the daemon's `queuedWriter`, overflow never kills the
session: responses do not flow through the hub.

**Security posture** (stricter than the web UI, because serve-mode
`tools/call` is arbitrary code execution): when a token is configured
(`--token` / `MCPMU_SERVE_TOKEN`, constant-time compare) it is required on
every `/mcp` request — only `/healthz`, which serves nothing sensitive, is
exempt. A tokenless bind is permitted on loopback only; a non-loopback bind
without a token refuses to start rather than warn. Origin
headers are validated against loopback plus `--allow-origin` entries
(DNS-rebinding protection; absent Origin is allowed — rebinding attacks come
from browsers, which always send it). POST requires
`Content-Type: application/json` (415 otherwise) — with Origin checking, that
is what stops a browser form on a tokenless loopback bind. Bodies are bounded
at 1 MiB and, via a per-request read deadline that is cleared before dispatch,
at 30s of trickle (a server-wide `ReadTimeout` would kill the GET stream and
long tool calls). The session table is capped at 256 — initialize gets 503
past it — so a reconnect-looping client cannot fan out unbounded upstream
instances before the idle reaper catches up. Frames are validated beyond
syntax: a wrong `jsonrpc` version or a message that is neither request,
notification, nor response is a 400 `-32600` (echoing the request id when one
was decodable, `"id": null` otherwise). TLS is out of scope: put a reverse proxy in front for non-loopback
deployments.

**Client recovery.** A POST presenting an unknown or expired session ID gets
404, and spec-compliant clients reinitialize. mcpmu's own
`StreamableHTTPTransport` returns a typed session-expired error on such a 404,
clears its session state, and `mcp.Client` reinitializes and retries once —
reopening the standalone GET stream for the fresh session. Recovery is
single-flight: one reinitialize serves every caller that raced into the
window. The transport latches the expired session ID until a replacement is
minted and fails non-initialize sends locally with the typed error while
latched — necessary, not just tidy, because a request sent after the session
was cleared would carry no session header and come back 400 ("missing
Mcp-Session-Id"), which nothing recognises as expiry. The latch is also what
lets an *idle* client recover: a GET-stream reconnect that 404s runs the same
expiry handling, and the next call — with no session left to present — still
routes into reinitialization instead of failing forever.

**Coexistence caveat.** If stdio clients (via the daemon) and an HTTP serve
run at the same time against the same config, shared upstream instances are
duplicated across the two processes. This topology is already tolerated
(embedded fallback, `--isolated`, and the metrics multi-writer design all
assume it); PID tracking and the `metrics.json` file lock keep it safe.
`--isolated` is rejected with `--http` — its only effect is skipping the
daemon rendezvous, and there is no daemon to skip; per-session privacy is the
per-server `shared: false` config property.

### Compressed Tool Surface

`mcpmu serve --compress <level>` (opt-in; `low`/`medium`/`high`/`max`) shrinks
what `tools/list` costs a client: instead of every tool's full schema, the
session exposes three wrapper tools and the client fetches schemas on demand.
Modelled on atlassian-labs/mcp-compressor, but implemented natively inside the
serve-mode Session — there is one wrapper set, not one per backend server,
because tools are already namespaced as `{server}.{tool}`.

| Wrapper | Behaviour |
|---|---|
| `list_tools` | Returns the compact listing (below) as text |
| `get_tool_schema` | `{"tool": "a.b"}` or `{"tools": [...]}` — returns the full `AggregatedTool` (description, `inputSchema`, `outputSchema`, annotations) as text + `structuredContent`; the multi form reports unknown/denied names per entry without failing the call |
| `invoke_tool` | `{"tool": "a.b", "input": {…}}` — rewrites the request and falls through to the normal `tools/call` path |

The listing is one line per tool —
`<tool>server.name(required, optional?): description</tool>` — and is embedded
in `invoke_tool`'s description at every level, so the model sees the available
tools on the first `tools/list` without an extra call. The level sets what
each line carries: `low` full description, `medium` first sentence, `high`
argument names only, `max` name only. Argument names come from
`inputSchema.properties` walked with `json.Decoder` tokens to preserve the
author's key order (a map would shuffle them between calls); invalid or
non-object schemas render as `name()`.

Where it plugs in (`internal/server`):

- `compress.go` holds the pure pieces: `CompressionLevel`, the listing
  formatter, and the wrapper tool definitions.
- `handleToolsList` branches after the shared `visibleTools` helper (grace
  period, background discovery, and permission filter — identical to the
  uncompressed path), so denied tools never appear in the listing.
- `handleToolsCall` intercepts the wrapper names before `ParseToolName`.
  `invoke_tool` rewrites `req.Name`/`req.Arguments` and falls through —
  namespace enforcement, `_meta` rewriting, `Router.CallTool` permission
  checks, the stale-session retry, and metrics recording all run on the
  *target* tool with zero changes. `get_tool_schema` applies the same
  namespace/enabled/permission checks the direct path does, and falls back to
  `Aggregator.DiscoverServer` for servers not discovered yet — the compressed
  analogue of lazy startup. With compression off, wrapper names fall through
  and get the same error any unknown dotless name does.
- Wrapper names contain no dot, so they cannot collide with `{server}.{tool}`;
  an upstream tool literally named `invoke_tool` stays callable as
  `{server}.invoke_tool`.

Interactions: metrics for `invoke_tool` record against the target server/tool;
`list_tools`/`get_tool_schema` are meta-calls recorded under `server="mcpmu"`
like manager tools. Manager tools stay real tools when
`--expose-manager-tools` is set. The listing is rebuilt per call from the
current config and catalog, so hot reload needs no extra plumbing, and
straggler discovery works unchanged — the client re-lists on
`tools/list_changed` and gets a wrapper whose description now includes the
late server's tools. Compression is a Session option, not a Core one: two
daemon-attached serves can run different levels against the same daemon (the
level travels in the shim handshake). One trade-off to know: a client's own
per-tool allow/deny rules only ever see `invoke_tool`; mcpmu's permissions are
unaffected because they run on the real target, which is the reason this mode
is opt-in rather than the default.

**Per-namespace configuration.** `NamespaceConfig.Compression`
(`mcpmu namespace set-compression <ns> <level|off>`, also editable in the TUI
and web namespace forms) stores a level on the namespace, so a large "work"
namespace compresses while a three-tool "dev" one doesn't. Resolution happens
per request in `Session.compressionLevel()`, not at session construction: an
explicit `--compress` flag wins in both directions (`Options.Compression`
carries a level, `Options.CompressionForceOff` carries an explicit
`--compress off`), otherwise the *active* namespace's configured level from the
*current* config applies — a hot reload that changes the namespace or its level
takes effect on the next `tools/list`. The handlers resolve the level once per
request (the intercept in `handleToolsCall` and the listing a wrapper renders
must agree) and snapshot the namespace name under `s.mu`, mirroring the
`router` snapshot pattern. The flag's tri-state travels in the daemon
handshake as a string: `""` = flag absent (namespace config decides), `"off"` =
explicit off, otherwise the level.

### Verified Upstream Catalog

The Supervisor is the single owner of upstream initialization and initial
`tools/list`. Each actual start receives a monotonically increasing generation
for its `InstanceID`; after initialization, the Supervisor publishes one
immutable result containing that identity, generation, advertised capabilities,
tools, and any discovery error. Core consumes the result into a catalog with
`unknown`, `discovering`, `verified`, and `failed` states.

Cold discovery is singleflight per instance. A verified empty result is distinct
from an unknown server. Initialization verifies resources-only or prompts-only
servers without issuing an unsupported `tools/list`; failure from a server that
advertises tools stays retryable and retains the last verified tool set. Stops
and config reloads invalidate verification, restarts publish a fresh generation,
and late results from older generations are ignored.

Generation alone cannot order results *within* one generation, and more than one
producer describes generation N: the Supervisor's initial discovery and every
subsequent refresh. Each result therefore also carries a `Sequence`, stamped
from a per-handle counter at the moment the upstream response lands, and the
catalog rejects a result whose sequence is not newer than the one already
applied for that generation. That makes an already-applied result idempotent —
`ensureCatalog` re-applies the handle's stored initial result, which the
Observer path has usually applied already — and stops a snapshot whose carrier
goroutine was descheduled from overwriting newer tools. A new generation starts
a fresh sequence space.

An upstream `notifications/tools/list_changed` callback only enters the
broadcaster queue. Its worker refreshes that generation's catalog entry before
fanning the notification out to Sessions, avoiding the reader-goroutine
request/response deadlock. Resources and prompts list fan-out uses recorded
capabilities to skip verified, stopped upstreams that lack the relevant
capability while still probing unknown entries.

### Resource Subscription Ownership

Resource subscription intent is owned by Core and keyed by `(InstanceID,
URI)`, not by URI alone. Each entry contains the set of subscribed Sessions.
Only the 0→1 transition calls upstream `resources/subscribe`, and only 1→0
calls upstream `resources/unsubscribe`; repeated subscribe requests from one
Session and subscriptions from additional Sessions change local membership
without duplicating the upstream request. Session shutdown walks its local
view through the same transition logic. An upstream unsubscribe failure is
logged but never prevents local client cleanup, while a failed subscribe does
not create a phantom refcount.

Intent remains after an upstream process stops. Discovery of a newer process
generation replays each retained upstream subscription once. Replay failure
drops the entry from every affected Session and sends those Sessions
`notifications/resources/list_changed` so they can rebuild URI ownership and
subscribe again. Config reload is deliberately different: it clears all
subscription intent and every attached Session's URI map before stopping the
old transports.

Subscribe, unsubscribe, replay, and `resources/updated` dispatch are
serialized per `(InstanceID, URI)` across the upstream response and local
state transition. This ensures an update emitted immediately after a
successful subscribe is not lost, while the upstream client's response-reader
goroutine still only enqueues notifications and never waits on Core work.

Those per-key mutexes are reference counted and dropped when the last operation
on a key completes, so the map tracks in-flight operations rather than every URI
ever seen — the keys are client-supplied, and `hasSubscribers` takes one for each
notification URI too. The count is what makes recycling a mutex safe: it is
incremented before the mutex is acquired, so an entry any operation can still
reach is never removed, and two operations on one key can never end up
serializing on different mutexes.

### HTTP Transport Streams

`StreamableHTTPTransport` reads server messages from two places, and needs both:

- **POST responses.** A reply may be `application/json` or an SSE stream. SSE
  responses are drained on a background goroutine, because the spec allows a
  server to hold that stream open after the response event, and reading it
  inline would block `Send` — which `Client.call` holds `sendMu` across, so every
  other RPC on the transport would queue behind it. The stream is tied to the
  request's context and ends with it.
- **The standalone GET stream.** Opened once, after the first successful POST has
  settled the protocol version and session ID, carrying `Accept:
  text/event-stream` plus the session ID. This is the only channel for messages
  the server originates rather than sends in reply: `notifications/
  resources/updated` for a subscribed resource, and `notifications/tools/
  list_changed`. Without it, `resources/subscribe` succeeds against an HTTP
  upstream and then silently never delivers anything.

  Drops are reconnected with exponential backoff between
  `SSEReconnectBaseDelay` and `SSEReconnectMaxDelay`, resuming from
  `Last-Event-ID`. A stream that delivered nothing and lasted less than
  `SSEReconnectMinUptime` does not reset the backoff, so a server that accepts
  the GET and closes it immediately is not reconnected forever at the base delay.
  `405` and `501` mean the server has no stream to give and are not retried;
  so are `401`/`403`, since the POST path owns authentication. A `404`
  carrying a session ID means something else entirely — the session is gone —
  so it runs the same expiry handling the POST path does rather than
  abandoning the stream (against mcpmu's own server this is exactly what a
  reconnect after a reap or DELETE hits).

`Close` cancels the transport-wide context, which aborts in-flight GETs and
closes any POST response body a server is holding open, then waits for both
kinds of reader to exit.

### Process Lifecycle Foundations

Every upstream is keyed internally by a stable `InstanceID`; current embedded
mode constructs the shared identity from the server name, while the type can
also carry the session discriminator needed by future private instances.
Start, stop, and restart use one lock per instance. The enforced lock order is
`instance lifecycle lock → Supervisor map lock → Handle lock`, and process
exit is never awaited while holding the map lock. A config reload advances a
Core generation before stopping upstreams. A get-or-start operation holding an
older snapshot must revalidate the canonical JSON encoding of the complete
`ServerConfig` under the lifecycle lock, so a removed or changed definition
cannot be started after the reload barrier.

Stdio servers run in their own Unix process group. Normal stop sends SIGTERM
to the group, escalates to SIGKILL after five seconds, and does not retire the
group identity until the leader is reaped and the group is empty. The leader
watcher performs the same cleanup immediately when a wrapper exits before its
workers, preventing a later restart from leaking those workers. Windows keeps
the direct-child behavior; shared-daemon transport remains Unix-only.

Crash recovery uses one atomic PID registry file per owner process rather than
one shared read-modify-write file. Owner identity is PID + OS process-start
identity + a random nonce; each entry records `InstanceID`, leader PID/start
identity, PGID, and command metadata. Live-owner files are skipped. Dead-owner
groups are signalled only while the recorded leader identity still matches;
reused leaders are never signalled, and an unverifiable leaderless group is
retained with a warning for manual cleanup. A newly spawned stdio process is
stopped and its start fails if its identity cannot be persisted atomically.

### Config Compatibility (mcpServers-style)

The mcpmu config is designed so server entries remain compatible with the common `mcpServers` object shape used by MCP clients. In practice that means the server config uses the familiar field names:
- `command`
- `args`
- `cwd`
- `env`

This keeps manual editing easy (copy/paste a server definition from a client config into mcpmu, then add the namespace assignments/permissions as needed).

### Multiple Toolsets (Namespaces) for Different Contexts

The stdio server exposes a *single toolset* per process, selected by namespace at startup. Configure multiple MCP entries that run the same binary with different `--namespace` values.

If multiple namespaces exist and none is selected (and no default is set), mcpmu fails `initialize` with an actionable error rather than accidentally exposing all tools.

```json
// Work context
{
  "mcpmu-work": {
    "command": "mcpmu",
    "args": ["serve", "--stdio", "--config", "~/.config/mcpmu/config.json", "--namespace", "work"]
  }
}

// Personal context
{
  "mcpmu-personal": {
    "command": "mcpmu",
    "args": ["serve", "--stdio", "--config", "~/.config/mcpmu/config.json", "--namespace", "personal"]
  }
}
```

### Optional: Separate Config Files

Namespaces are the preferred mechanism for selecting toolsets, but separate config files are still supported when you want fully isolated settings.

```json
{
  "mcpmu-project-x": {
    "command": "mcpmu",
    "args": ["serve", "--stdio", "--config", "~/.config/mcpmu/project-x.json", "--namespace", "default"]
  }
}
```

## Progressive Tool Discovery

Serve mode uses a two-phase `tools/list` flow so clients are not blocked behind slow upstream discovery.

1. On `initialize`, `mcpmu` advertises `tools.listChanged: true`.
2. On the first `tools/list`, `mcpmu` starts or probes all selected servers concurrently.
3. It waits up to an 8 second grace period and returns the tools that are already ready.
4. Any remaining discovery continues in the background with the normal per-server timeout.
5. Each time a background straggler completes discovery, `mcpmu` sends `notifications/tools/list_changed` so the client can refresh without missing tools from servers that finish after an earlier notification.
6. Config reloads that may change the visible tool set also send `notifications/tools/list_changed`.

The catalog retains the last verified tools across a transient refresh failure,
but keeps the entry failed/retryable. Upstream list-change notifications refresh
the catalog before they are relayed downstream.

This keeps `tools/list` responsive for clients with tight request timeouts while still converging to the full aggregated tool set.

Change detection compares **every field `mcpmu` exposes downstream**, not just
`name`/`description`/`inputSchema`: `title`, `outputSchema`, `annotations`,
`icons`, `_meta`, and any member of a future revision captured by the
unknown-field catch-all. A server that changes only its `annotations` (say, from
`readOnlyHint: true` to `false`) still fires
`notifications/tools/list_changed` — otherwise an agent would hold a stale
definition indefinitely and keep auto-approving a tool that is no longer
read-only.

## Protocol Revision Negotiation

`mcpmu` negotiates independently on each side of the proxy.

- **Upstream**, `internal/mcp` offers `SupportedProtocolVersions` newest-first
  (`2025-11-25` … `2024-11-05`) and takes the first revision a server accepts.
- **Downstream**, `handleInitialize` echoes the client's requested revision when
  it appears in `server.DownstreamProtocolVersions`, and otherwise answers with
  `mcpmu`'s newest, per the lifecycle spec. The result is stored per **Session**,
  not per process: in daemon mode two clients on one Core may legitimately settle
  on different revisions.

Field passthrough is **permissive**: `mcpmu` forwards every field an upstream
sent regardless of the revision the downstream session negotiated. Unknown
object members are ignored by clients in practice, whereas a field→revision
strip table would need maintaining for every future revision. If a real client
is ever found that chokes, the strict path belongs in
`negotiateProtocolVersion`'s neighbourhood in `internal/server/protocol_version.go`.

### What mcpmu preserves and what it strips

Preserved verbatim on tools: `title`, `inputSchema` (as raw bytes, so large
integers in a schema are not mangled by a float64 round trip), `outputSchema`,
`annotations`, `icons`, `_meta`, and unknown members. On a `tools/call` result:
content blocks, `structuredContent`, `isError`, and `_meta`. On the request:
`_meta`, so a client that asks for progress actually gets it.

Stripped deliberately: **`execution`** (`execution.taskSupport`). It advertises
that a tool supports task-augmented execution; forwarding it while `mcpmu`
implements no `tasks/*` methods would invite an agent to make a call `mcpmu`
cannot service. Forward it when tasks are supported. The same reasoning applies
to any future field that promises behaviour the proxy must itself provide —
passthrough is the default, but not for capability-implying fields.

Only `annotations` is interpreted rather than merely carried: `readOnlyHint` and
`destructiveHint` feed tool safety classification
(`ClassifyToolWithAnnotations`), where the server's declaration outranks the
name-substring heuristic. The heuristic remains as the fallback for servers that
declare nothing.

## Request Lifecycle: Cancellation and Progress

Both are tracked per **Session**, keyed so that several sessions sharing one
upstream instance cannot interfere with each other.

- **Cancellation.** Each request that dispatches upstream is registered in the
  Session's in-flight table under its JSON-RPC id (canonicalised, so `1` and
  `"1"` stay distinct). `notifications/cancelled` cancels that context with the
  client's stated `reason` as the cause; `mcp.Client` then emits its own
  `notifications/cancelled` upstream, so the server stops too rather than
  finishing work nobody will read. The same upstream withdrawal covers deadline
  expiry, which the cancellation spec also asks for. Closing a session cancels
  that session's calls and leaves another session's calls on the same shared
  instance running.
- **Progress.** Tokens must be unique across active requests, and two sessions
  are free to pick the same one, so `mcpmu` substitutes a token of its own
  (`mcpmu/{session}/{n}`) on the way up and reverses the substitution on the way
  down. Every other member of `_meta` is forwarded untouched. Delivery is exact
  rather than a guess: a `notifications/progress` whose token is not in the
  session's table did not come from that session's request and is dropped.
  Filtering happens at the notification sink rather than in the broadcaster, so
  the existing fan-out ordering is preserved. A mapping outlives its call by a
  short grace window because progress and the result travel different paths
  downstream — without it, a progress frame already in flight would lose a race
  it should never have been in.

## HTTP Server Custom Headers

For `streamable_http` servers, two map fields on `ServerConfig` flow from the CLI (`--header`/`--env-header`), TUI form, and web form through `internal/config` (parsed and validated by `ParseHeaderLines`) into `supervisor.go`'s `StreamableHTTPConfig.HTTPHeaders` and out through `streamable_http_transport.Send` on every request. Static headers (`http_headers`) are stored verbatim in `~/.config/mcpmu/config.json`; env-backed headers (`env_http_headers`) are resolved from the named env var at request time so secrets stay out of the file. The two maps are merged at request build time with env-backed entries taking precedence on name collision; missing env vars are silently omitted (a no-op when the header is optional). Used in practice for Cloudflare Access (`CF-Access-Client-Id` / `CF-Access-Client-Secret`) on top of any auth mode.

## Resource and Prompt Passthrough

Serve mode passes through `resources/*` and `prompts/*` MCP methods from upstream servers (enabled by default, disable with `--resources=false` or `--prompts=false`).

- **Resources**: URIs are passed through unmodified from upstream servers. A per-Session reverse map (URI → `InstanceID`) is rebuilt atomically during `resources/list` and used to route `resources/read`, subscribe, and unsubscribe calls to the correct upstream instance. Results are merged in stable namespace server order; if two upstreams expose the same raw URI, the first owner wins and later duplicates are omitted and logged. Different Sessions may therefore resolve the same URI to different upstreams without sharing subscription state. All MCP resource fields are preserved, including `annotations`, `title`, `size`, `icons`, and `_meta`. `resources/templates/list` is also supported (returns an empty list if no upstream servers provide templates).
- **Prompts**: Names are qualified as `serverName.promptName` (same as tools). Descriptions are prefixed with `[serverName]`. On `prompts/get`, the prefix is stripped before forwarding upstream.
- **No caching**: Resource and prompt payloads are fetched on demand. Their fan-out consults initialize-time catalog capabilities so a verified stopped server that lacks the relevant capability can be skipped without starting it.
- **No permissions**: Unlike tools, resources and prompts have no permission layer — they are read-only and user-initiated.

## Permission Resolution

Tool access follows a four-tier resolution. The server-level global deny applies even without a namespace:

1. **Server global deny** (`server deny-tool`) — hard block, no override. Applies even without a namespace.
2. **Explicit tool permission** (`permission set`) — highest namespace-level priority
3. **Per-server default** (`permission set-server-default`) — overrides namespace default for one server
4. **Namespace default** (`namespace set-deny-default`) — fallback for all servers
5. **Allow** — if nothing else applies

A namespace-level explicit allow **cannot** override a server global deny. To re-enable a globally denied tool, remove it from `deniedTools` via `server allow-tool`.

This enables defense-in-depth: globally deny dangerous tools at the server level, then use namespace permissions for fine-grained control over the rest.

## Tool Namespacing

Tools from managed servers are exposed with `serverId.toolName` format:

```
filesystem.read_file
filesystem.write_file
github.create_issue
github.list_repos
mcpmu.servers_list    # Manager tools
mcpmu.servers_start
mcpmu.servers_stop
mcpmu.namespaces_list
```

`serverId` is a stable internal identifier (auto-generated short `[a-z0-9]`, no `.`), not the human display name.

Because the `mcpmu.` prefix is claimed by the manager tools, `mcpmu` is a
reserved server name: `ValidateServerName` rejects it in `AddServer` and
`RenameServer`, which every CLI, TUI and web add/rename path goes through.
Namespaces are unaffected — a namespace name never appears in a qualified tool
name — so they still use `ValidateName`.

Loading a config that already contains such a server deliberately does *not*
fail; refusing the whole file would take every other server down over one bad
name. Instead `Config.ReservedNameConflicts()` reports them and `mcpmu serve`
warns on stderr with the `mcpmu rename` command that fixes it. Left unrenamed,
that server's tools are listed but skip the permission filter and always fail to
call with "tool not found".

## Registry Browser

The TUI includes a registry browser for discovering and installing servers from the official MCP registry (`registry.modelcontextprotocol.io`). Press `a` on the server list to open an add-method selector:

- **Manual** — opens the blank add-server form
- **Official Registry** — opens a searchable browser with debounced live search, detail view with install preview, and pre-populates the add-server form with the selected server's command/args/env

The registry client (`internal/registry/`) handles API calls, type mapping, and install spec generation (package selection, runtime hints, env var placeholders).

## Embedded Agent Skill

mcpmu embeds a `SKILL.md` file in the binary (`cmd/mcpmu/skill_data/SKILL.md` via `//go:embed`). The `mcpmu skill install` command writes this to agent-specific skill directories (`~/.claude/skills/mcpmu/`, `~/.codex/skills/mcpmu/`, `~/.agents/skills/mcpmu/`). A checked-in mirror at `.claude/skills/mcpmu/SKILL.md` is kept in sync by a test assertion.

## Web UI

`mcpmu web` starts a browser-based management UI on `127.0.0.1:8080` (configurable via `--addr`). The web UI and TUI are mutually exclusive managers — a `manager.lock` file prevents concurrent instances.

**Stack**: Go `net/http` (stdlib, Go 1.22+ route patterns) + htmx + Go `html/template`, all embedded via `go:embed`. Single binary, no build step.

**Architecture** (`internal/web/`):
- `server.go` — HTTP server setup, route registration, template parsing, config mutation helper
- `pages.go` — Full-page handlers (server list, server detail, namespace list, namespace detail)
- `forms.go` — Form page handlers and CRUD operations (add/edit/delete servers and namespaces)
- `actions.go` — Live action handlers (start/stop, OAuth login/logout, denied tools, permissions, server defaults, set default namespace)
- `handlers.go` — JSON API endpoints (`/api/servers`, `/api/namespaces`, `/api/config/export|import`)
- `registry.go` — Registry browser page, htmx fragment, and JSON API (`/api/registry/search`)
- `metrics.go` — Usage-metrics page, htmx fragments, and JSON API (`/metrics`, `/api/metrics`); see Usage Metrics below
- `fragments.go` — htmx fragment handlers (server table, status pill, registry results)
- `sse.go` — Server-Sent Events for live log streaming
- `status.go` — `StatusTracker` subscribes to event bus, maintains last-known status per server
- `middleware.go` — Request logging, panic recovery

**Data flow**: Browser requests go through middleware to handlers, which read/write config (same `internal/config` package as TUI), interact with the supervisor for start/stop, and subscribe to the event bus for live status and logs.

**Scope decision (2026-08-26, D1)**: `internal/web/` is kept, read-mostly. Pages, metrics, SSE and the unused-tools panel stay; config mutation happens only through `config.Mutate`; no new forms or write endpoints are added until the control-plane direction below is settled. Hardening work on the existing surface (data races, template bugs, secret redaction in exports) is in scope; new features are not.

**Direction (F4): TUI and web as daemon clients.** Today `cmd/mcpmu/manager_startup.go` (`startManager`, shared by `mcpmu tui` and `mcpmu web`) builds a private `Supervisor`, so in daemon mode the managers show and control a *different* set of upstream processes than the one agents are using — the root cause of "status is wrong" reports and of a manager spawning a duplicate upstream. The intended end state is that both managers are thin clients of the daemon's control socket and own no supervision of their own; every change to `web/` or `tui/` from here on should move toward that and not deepen their private supervision. Control-protocol additions this needs (not yet built): a `status` request returning per-server runtime state, PID, session count and last error; a `subscribe` stream forwarding `events.Bus` events (status, log lines, tool discovery) over the socket; `start`/`stop`/`restart` requests resolved the same way manager tools are (shared vs private instance); a `reload` request so a manager that has just written config can ask the daemon to pick it up immediately; and a `tools` request serving the verified catalog so the managers stop reading `toolcache.json` directly. When no daemon is running the managers fall back to today's embedded `Supervisor`.

**Config mutations**: every write goes through `config.Mutate` / `config.MutateWithCache` (`internal/config/mutate.go`): take the cross-process lock (`config.json.lock`), reload from disk, apply the caller's function, validate, save atomically, and return the saved config for the caller to adopt. CLI, TUI and web all use it — a load→edit→save anywhere else would lose a concurrent writer's update. `RenameServer`, `DeleteServer` and `UpdateServer` (when command/args/URL change) record ToolCache side effects on the `Config`, which `Mutate` applies after the save, so a rename keeps the server's cached tools and a removal drops them regardless of which surface did it. The web `mutateConfig` method is a thin adapter that also serialises on `cfgMu` and swaps `s.cfg`. The form-to-`ServerConfig` merge rules (what an edit preserves — `OAuth.ClientSecret`, `Shared`, `DeniedTools` — and what switching transport clears) live once in `config.BuildServerConfig`, shared by the TUI and web forms.

## Usage Metrics

Every tool call routed through serve mode is counted into daily usage buckets and surfaced in the web UI's **Metrics** page (plus a compact block on each server detail page). The headline question the feature answers: *"Am I actually using all the tools I've assigned?"* — hence the dedicated unused-tools view.

**Write path** (`internal/metrics`): tool calls are dispatched in serve-mode processes (the shared daemon or an embedded serve), never in the web process, so metrics flow through a file on disk. `Router.CallTool` records one `CallSample` per call at exit — outcome `ok`, `tool_error` (upstream returned `isError`), `error` (transport/startup failure), `timeout`, `cancelled` (the client abandoned the call — a cancellation notification, or an HTTP client that dropped its POST; not counted as an error), or `denied` (permission refusal; duration 0). Manager tools record under server `mcpmu`. Misaddressed calls (server not found) are not tool usage and are not recorded; the internal 4xx-reinit retry records only the final outcome. The `Recorder` lives on `Core` (one per process, reached via the Session), accumulates a delta in memory (mutex-only, no I/O on the call path), and flushes every ~30s plus once on `Core.Close`. A nil `*Recorder` is valid and a no-op, so call sites are unconditional.

**Multi-writer story**: a daemon and one or more embedded serves can run concurrently against the same config. Each flush is a read-merge-write of `metrics.json` under an exclusive file lock (`metrics.json.lock`, via `process.LockFileBlocking` on the same flock/LockFileEx primitive as `manager.lock`; the lock file is never deleted, for the same inode-race reason). Because each writer contributes only its own delta since its last flush, no writer clobbers another's counts; a crash costs at most one flush interval of data. Writes land in a temp file, fsync, then atomic rename — so readers (the web process) never need the lock. On flush failure the delta is merged back and retried on the next tick.

**Storage**: `metrics.json` sits next to the active config file, exactly like `toolcache.json` (custom config path → same directory; default → `~/.config/mcpmu/`). Rows are a flat array keyed by `(date, namespace, server, tool)` — daily buckets, local-time dates — holding call counts, per-outcome tallies, duration sum/max, and a fixed-boundary latency histogram (p50/p95 are estimated by interpolating the histogram on the read side). A denied call counts toward `calls` and the outcome tally but stays out of the latency aggregates — it never reached an upstream, and folding its zero duration in would drag p50/p95 toward zero for a mostly-refused tool. The read side therefore reports latency over `TimedCalls` (calls minus denials) and renders an em dash when that is zero. A rolling `recent` feed keeps the last 200 calls. An unparseable file is renamed to `metrics.json.corrupt` and collection restarts fresh — a metrics file never takes serve down.

**Retention**: rows older than `metrics.retentionDays` (default 60) are pruned by each flush that writes; a flush with an empty delta returns without touching the file, so an idle process leaves the sidecar alone. Config: `metrics.enabled` (default true) and `metrics.retentionDays` under a `metrics` block; hot-reload flips the recorder on/off live.

**Privacy rule (hard)**: buckets and the recent feed carry *names, timestamps, durations, and outcomes only* — never tool arguments, results, or error message bodies. Arguments routinely contain secrets and personal data; they must not land in a sidecar file.

**Read path** (`internal/web/metrics.go`): the web process re-reads `metrics.json` per request, cached by file mtime+size so htmx polling stays cheap. The page shows summary tiles, a server-rendered SVG calls-per-day chart, a sortable per-tool table with sparklines, the unused-tools panel, and a recent-calls feed (5s htmx poll); `/api/metrics` returns the same view as JSON. Charts are inline SVG built from precomputed view-models — no JS chart dependencies. The unused-tools computation lives in the web package because it needs config + toolcache + permissions: per namespace, each assigned server's discovered tools (from the shared `ToolCache`) are filtered through `server.IsToolAllowed` and diffed against the called set; "exposed" therefore reflects the last discovery snapshot, and a never-started server reports "no discovery data" rather than "all unused". The same pass produces the coverage tile: `toolsUsed` is the exposed∩called intersection, not every tool with a row, because a denied call records against a tool that is by definition not exposed (`toolsCalled` in the JSON keeps the row-based count). When the config has no namespaces at all, serve exposes every enabled server under the empty namespace — the `(none)` filter reproduces that grouping so rows recorded before the first namespace was created still diff against real exposure. Data lags live activity by up to one flush interval.

**Not in scope (v1)**: OpenTelemetry export (`Recorder.Record` is the seam an exporter would sit behind), a TUI metrics view, per-client dimensions, hourly buckets, and a CLI command (`/api/metrics` + `jq` covers scripting).

## Key Design Principles

1. **Non-blocking initialize**: Return immediately; optionally start `eager` servers in background (otherwise start on-demand)
2. **Lazy server start**: Servers start on first tool call (configurable)
3. **Progressive tool discovery**: `tools/list` returns ready tools within a grace window, then notifies clients when stragglers finish
4. **Graceful degradation**: If one server fails, others still work
5. **Strict output discipline**: stdout = MCP protocol only, stderr = logs
6. **Transport-agnostic core**: Easy to add HTTP later if needed
7. **Shared daemon ownership**: On Unix, thin stdio shims share one per-config
   Core and upstream process set. This deliberately supersedes the earlier
   no-daemon/single-process simplicity so concurrent agents do not duplicate
   every upstream; embedded mode remains the failure fallback and explicit
   isolation escape hatch.
