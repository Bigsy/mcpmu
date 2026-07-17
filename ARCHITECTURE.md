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
back to embedded serve. Windows remains embedded.

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
the socket so queued responses can drain. The daemon writer uses an ordered
flush marker before closing such a completed Session.

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
5. If background discovery makes progress, `mcpmu` sends `notifications/tools/list_changed` so the client can refresh with another `tools/list`.
6. Config reloads that may change the visible tool set also send `notifications/tools/list_changed`.

The catalog retains the last verified tools across a transient refresh failure,
but keeps the entry failed/retryable. Upstream list-change notifications refresh
the catalog before they are relayed downstream.

This keeps `tools/list` responsive for clients with tight request timeouts while still converging to the full aggregated tool set.

## HTTP Server Custom Headers

For `streamable_http` servers, two map fields on `ServerConfig` flow from the CLI (`--header`/`--env-header`), TUI form, and web form through `internal/config` (parsed and validated by `ParseHeaderLines`) into `supervisor.go`'s `StreamableHTTPConfig.HTTPHeaders` and out through `streamable_http_transport.Send` on every request. Static headers (`http_headers`) are stored verbatim in `~/.config/mcpmu/config.json`; env-backed headers (`env_http_headers`) are resolved from the named env var at request time so secrets stay out of the file. The two maps are merged at request build time with env-backed entries taking precedence on name collision; missing env vars are silently omitted (a no-op when the header is optional). Used in practice for Cloudflare Access (`CF-Access-Client-Id` / `CF-Access-Client-Secret`) on top of any auth mode.

## Resource and Prompt Passthrough

Serve mode passes through `resources/*` and `prompts/*` MCP methods from upstream servers (enabled by default, disable with `--resources=false` or `--prompts=false`).

- **Resources**: URIs are passed through unmodified from upstream servers. A per-Session reverse map (URI → `InstanceID`) is rebuilt atomically during `resources/list` and used to route `resources/read`, subscribe, and unsubscribe calls to the correct upstream instance. Results are merged in stable namespace server order; if two upstreams expose the same raw URI, the first owner wins and later duplicates are omitted and logged. Different Sessions may therefore resolve the same URI to different upstreams without sharing subscription state. All MCP resource fields are preserved, including `annotations`, `title`, and `size`. `resources/templates/list` is also supported (returns an empty list if no upstream servers provide templates).
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
- `fragments.go` — htmx fragment handlers (server table, status pill, registry results)
- `sse.go` — Server-Sent Events for live log streaming
- `status.go` — `StatusTracker` subscribes to event bus, maintains last-known status per server
- `middleware.go` — Request logging, panic recovery

**Data flow**: Browser requests go through middleware to handlers, which read/write config (same `internal/config` package as TUI), interact with the supervisor for start/stop, and subscribe to the event bus for live status and logs.

**Config mutations**: The `mutateConfig` helper reloads config from disk, applies the mutation, and atomically saves — safe for the single-manager design.

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
