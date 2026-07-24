#!/usr/bin/env bash
#
# End-to-end smoke tests for mcpmu. Each check exercises one feature against
# real binaries (and, where unavoidable, real network endpoints) — the goal
# is to catch wire-path / config-shape regressions that unit tests can't.
#
# This is NOT wired into `make check` or `make test-integration` because some
# checks make outbound network calls to private endpoints. Run it by hand
# before merging anything that touches a wire path, a config shape, or the
# CLI/TUI/web forms that produce config:
#
#   ./scripts/smoke.sh           # run every check; checks self-skip if prereqs are missing
#   ./scripts/smoke.sh <name>    # run a single check by function name
#
# Adding a new smoke check:
#   1. Write a function `smoke_<feature>` that returns 0 on success, non-zero
#      on failure, and prints "SKIP: <reason>" + returns 0 if its prereqs
#      (env vars, network, etc.) are missing.
#   2. Append the function name to SMOKE_CHECKS below.
#   3. Each check is responsible for its own setup, cleanup, and isolation
#      from the user's real config (use mktemp configs — never write to
#      ~/.config/mcpmu/config.json).
#   4. Secrets come from the environment, never from the script. A check may
#      read a credential out of the real config as a *fallback* when the env
#      var is unset (see cf_access_creds_from_local_config) — read-only, and it
#      must still build its own mktemp config to test against. A check whose
#      prereqs are genuinely absent must SKIP rather than fail, so the script
#      stays useful on machines and in CI that cannot run everything.

set -uo pipefail

cd "$(dirname "$0")/.."

# --- Shared helpers --------------------------------------------------------

# Build once, up front. Individual checks should not rebuild.
build_binary() {
  # Internal package changes do not update the cmd/mcpmu directory mtime, so
  # timestamp checks can silently exercise a stale binary. Always rebuild once.
  echo "==> building binary"
  go build -o mcpmu ./cmd/mcpmu
}

# Make a throwaway config file and echo its path.
new_temp_config() {
  local f
  f=$(mktemp -t mcpmu-smoke.XXXXXX).json
  echo '{"schemaVersion":1,"servers":{},"namespaces":{}}' > "$f"
  echo "$f"
}

# Drive an MCP session through `mcpmu serve --stdio` against the given config.
# Args: config_path, namespace, tool_name, json_arguments
# Echoes the raw NDJSON the server emitted.
mcp_tool_call() {
  local cfg="$1" ns="$2" tool="$3" args="$4"
  {
    printf '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"mcpmu-smoke","version":"0"}}}\n'
    printf '{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}\n'
    printf '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"%s","arguments":%s}}\n' "$tool" "$args"
    sleep 10
  } | ./mcpmu serve --stdio --isolated --config "$cfg" --namespace "$ns" 2>/dev/null
}

# --- Smoke checks ----------------------------------------------------------

# Best-effort discovery of the CF-Access creds from the local mcpmu config, used
# only when they are not already in the environment.
#
# Without this, a plain ./scripts/smoke.sh silently skips the one check that
# exercises a real third-party endpoint, which reads as full coverage when it is
# not. The credential for this exact server already lives in the config, so
# there is no reason to make the developer re-supply it by hand.
#
# This is the one place a check reads the real config, and it is strictly
# read-only: it never writes there, and the check itself still builds its own
# mktemp config. No value is printed. Nothing is hardcoded, so the script stays
# safe to commit.
cf_access_creds_from_local_config() {
  local cfg="${HOME}/.config/mcpmu/config.json"
  [[ -r "$cfg" ]] || return 0
  command -v jq >/dev/null 2>&1 || return 0

  # Match on the headers rather than a server name, so renaming the server does
  # not quietly turn this back into a skip.
  local id secret
  id=$(jq -r 'first(.servers[]? | select((.http_headers["CF-Access-Client-Id"] // "") != "" and (.http_headers["CF-Access-Client-Secret"] // "") != "")) | .http_headers["CF-Access-Client-Id"] // empty' "$cfg" 2>/dev/null)
  secret=$(jq -r 'first(.servers[]? | select((.http_headers["CF-Access-Client-Id"] // "") != "" and (.http_headers["CF-Access-Client-Secret"] // "") != "")) | .http_headers["CF-Access-Client-Secret"] // empty' "$cfg" 2>/dev/null)

  [[ -n "$id" && -n "$secret" ]] || return 0
  export CF_ACCESS_CLIENT_ID="$id"
  export CF_ACCESS_CLIENT_SECRET="$secret"
}

# Verifies the --header / --env-header CLI flow and the end-to-end CF-Access
# wire path (PLAN-headers.md's "Required test before merging" non-negotiable).
#
# Needs the Cloudflare Access creds for the bigsy.uk searxng MCP. The
# environment wins, so CI and other machines can supply them however they like;
# otherwise they are read from the local mcpmu config. Either way they are never
# stored in the repo.
smoke_cf_access_headers() {
  if [[ -z "${CF_ACCESS_CLIENT_ID:-}" || -z "${CF_ACCESS_CLIENT_SECRET:-}" ]]; then
    cf_access_creds_from_local_config
  fi
  if [[ -z "${CF_ACCESS_CLIENT_ID:-}" || -z "${CF_ACCESS_CLIENT_SECRET:-}" ]]; then
    echo "SKIP: no CF-Access creds; set CF_ACCESS_CLIENT_ID and CF_ACCESS_CLIENT_SECRET" \
      "or configure a server with CF-Access http_headers"
    return 0
  fi

  local cfg rc=0
  cfg=$(new_temp_config)

  _smoke_cf_access_headers_inner "$cfg"
  rc=$?
  rm -f "$cfg"
  return "$rc"
}

_smoke_cf_access_headers_inner() {
  local cfg="$1"

  ./mcpmu --config "$cfg" add searxng-test https://searxng-mcp.bigsy.uk/mcp \
    --header "CF-Access-Client-Id: $CF_ACCESS_CLIENT_ID" \
    --header "CF-Access-Client-Secret: $CF_ACCESS_CLIENT_SECRET" >/dev/null

  local stored
  stored=$(jq -r '.servers["searxng-test"].http_headers["CF-Access-Client-Id"]' "$cfg")
  if [[ "$stored" != "$CF_ACCESS_CLIENT_ID" ]]; then
    echo "FAIL: --header did not round-trip into config http_headers (got $stored)"
    return 1
  fi

  ./mcpmu --config "$cfg" namespace add work >/dev/null
  ./mcpmu --config "$cfg" namespace assign work searxng-test >/dev/null
  ./mcpmu --config "$cfg" namespace default work >/dev/null

  local response result
  response=$(mcp_tool_call "$cfg" work "searxng-test.web_url_read" '{"url":"https://example.com","maxLength":200}')
  result=$(printf '%s\n' "$response" | jq -c 'select(.id == 2)' | head -1)

  if [[ -z "$result" ]]; then
    echo "FAIL: no response to tools/call"
    printf '%s\n' "$response"
    return 1
  fi
  if printf '%s' "$result" | jq -e '.error' >/dev/null; then
    echo "FAIL: tools/call returned an error:"
    printf '%s\n' "$result" | jq .
    return 1
  fi
  if [[ -z "$(printf '%s' "$result" | jq -r '.result.content[0].text[0:120] // empty')" ]]; then
    echo "FAIL: tools/call result has no text content"
    printf '%s\n' "$result" | jq .
    return 1
  fi

  return 0
}

# Verifies that the real serve binary starts stdio upstreams in their own
# process group and reaps a wrapper-launched worker when the MCP session exits.
smoke_process_group_cleanup() {
  local tmp cfg fake child_file response child_pid rc=0
  tmp=$(mktemp -d -t mcpmu-smoke-group.XXXXXX)
  cfg="$tmp/config.json"
  fake="$tmp/fake-mcp.sh"
  child_file="$tmp/child.pid"

  cat > "$fake" <<'EOF'
#!/usr/bin/env bash
sleep 300 &
echo "$!" > "$CHILD_PID_FILE"
while IFS= read -r line; do
  method=$(printf '%s' "$line" | jq -r '.method // empty')
  id=$(printf '%s' "$line" | jq -r '.id // empty')
  case "$method" in
    initialize)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2025-06-18","capabilities":{"tools":{}},"serverInfo":{"name":"smoke-wrapper","version":"1"}}}\n' "$id"
      ;;
    tools/list)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"tools":[]}}\n' "$id"
      ;;
  esac
done
EOF
  chmod 0700 "$fake"

  jq -n --arg command "$fake" --arg child_file "$child_file" '{
    schemaVersion: 1,
    servers: {
      wrapper: {
        command: $command,
        env: {CHILD_PID_FILE: $child_file},
        startup_timeout_sec: 3
      }
    },
    namespaces: {}
  }' > "$cfg"

  response=$({
    printf '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"mcpmu-smoke","version":"0"}}}\n'
    printf '{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}\n'
    printf '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}\n'
  } | ./mcpmu serve --stdio --isolated --config "$cfg" 2>/dev/null)

  if ! printf '%s\n' "$response" | jq -e 'select(.id == 2 and .result.tools)' >/dev/null; then
    echo "FAIL: real serve session did not complete tools/list"
    rc=1
  elif [[ ! -s "$child_file" ]]; then
    echo "FAIL: wrapper did not record its worker PID"
    rc=1
  else
    child_pid=$(cat "$child_file")
    for _ in {1..50}; do
      if ! kill -0 "$child_pid" 2>/dev/null; then
        break
      fi
      sleep 0.02
    done
    if kill -0 "$child_pid" 2>/dev/null; then
      echo "FAIL: wrapper worker PID $child_pid survived serve shutdown"
      kill "$child_pid" 2>/dev/null || true
      rc=1
    fi
  fi

  rm -rf "$tmp"
  return "$rc"
}

# Verifies the real daemon binary's per-config rendezvous, status protocol,
# graceful stop, and runtime-artifact cleanup without touching the user config.
smoke_daemon_control() {
  local tmp cfg runtime daemon_pid status socket pidfile rc=0
  tmp=$(mktemp -d -t mcpmu-smoke-daemon.XXXXXX)
  cfg="$tmp/config.json"
  runtime=$(mktemp -d /tmp/mu-daemon.XXXXXX)
  chmod 0700 "$runtime"
  echo '{"schemaVersion":1,"servers":{},"namespaces":{}}' > "$cfg"

  XDG_RUNTIME_DIR="$runtime" ./mcpmu --config "$cfg" daemon run &
  daemon_pid=$!

  status=""
  for _ in {1..100}; do
    status=$(XDG_RUNTIME_DIR="$runtime" ./mcpmu --config "$cfg" daemon status --json 2>/dev/null || true)
    if [[ -n "$status" ]] && printf '%s' "$status" | jq -e '.pidfileFallback == false' >/dev/null 2>&1; then
      break
    fi
    sleep 0.02
  done

  if [[ -z "$status" ]] || ! printf '%s' "$status" | jq -e --argjson pid "$daemon_pid" '.pid == $pid and .pidfileFallback == false' >/dev/null 2>&1; then
    echo "FAIL: daemon status did not report the live daemon"
    printf '%s\n' "$status"
    rc=1
  else
    socket=$(printf '%s' "$status" | jq -r '.socket')
    pidfile="${socket%.sock}.pid"
    if ! XDG_RUNTIME_DIR="$runtime" ./mcpmu --config "$cfg" daemon stop >/dev/null; then
      echo "FAIL: daemon stop command failed"
      rc=1
    fi

    for _ in {1..100}; do
      if [[ ! -e "$socket" && ! -e "$pidfile" ]]; then
        break
      fi
      sleep 0.02
    done
    if [[ -e "$socket" || -e "$pidfile" ]]; then
      echo "FAIL: daemon runtime artifacts remain after graceful stop"
      rc=1
    fi
  fi

  if kill -0 "$daemon_pid" 2>/dev/null; then
    kill -TERM "$daemon_pid" 2>/dev/null || true
  fi
  wait "$daemon_pid" 2>/dev/null || true
  rm -rf "$runtime"
  rm -rf "$tmp"
  return "$rc"
}

# Verifies real-binary shim auto-spawn, absent-config canonicalization, and
# embedded fallback without touching the user's config or runtime directory.
smoke_daemon_shim_fallback() {
  local tmp runtime cfg missing long_runtime stderr_file response status rc=0
  tmp=$(mktemp -d -t mcpmu-smoke-shim.XXXXXX)
  runtime=$(mktemp -d /tmp/mu-shim.XXXXXX)
  chmod 0700 "$runtime"
  cfg="$tmp/config.json"
  missing="$tmp/not-created/config.json"
  stderr_file="$tmp/serve.stderr"
  echo '{"schemaVersion":1,"servers":{},"namespaces":{}}' > "$cfg"

  response=$(printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"mcpmu-smoke","version":"0"}}}' |
    XDG_RUNTIME_DIR="$runtime" ./mcpmu serve --stdio --config "$cfg" 2>"$stderr_file")
  if ! printf '%s\n' "$response" | jq -e 'select(.id == 1 and .result)' >/dev/null; then
    echo "FAIL: daemon shim did not return initialize"
    rc=1
  fi
  status=$(XDG_RUNTIME_DIR="$runtime" ./mcpmu --config "$cfg" daemon status --json 2>/dev/null || true)
  if [[ -z "$status" ]] || ! printf '%s' "$status" | jq -e '.pidfileFallback == false' >/dev/null 2>&1; then
    echo "FAIL: shim did not auto-spawn a live daemon"
    rc=1
  fi
  XDG_RUNTIME_DIR="$runtime" ./mcpmu --config "$cfg" daemon stop >/dev/null 2>&1 || true

  response=$(printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"mcpmu-smoke","version":"0"}}}' |
    XDG_RUNTIME_DIR="$runtime" ./mcpmu serve --stdio --isolated --config "$missing" 2>"$stderr_file")
  if ! printf '%s\n' "$response" | jq -e 'select(.id == 1 and .result)' >/dev/null; then
    echo "FAIL: absent config with absent parent did not serve embedded"
    rc=1
  fi

  long_runtime="$tmp/$(printf 'long-runtime-%.0s' {1..10})"
  mkdir -p "$long_runtime"
  response=$(printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"mcpmu-smoke","version":"0"}}}' |
    XDG_RUNTIME_DIR="$long_runtime" ./mcpmu serve --stdio --config "$cfg" 2>"$stderr_file")
  if ! printf '%s\n' "$response" | jq -e 'select(.id == 1 and .result)' >/dev/null; then
    echo "FAIL: daemon-path failure did not fall back to embedded serve"
    rc=1
  elif [[ $(grep -c 'shared daemon unavailable; falling back to embedded serve' "$stderr_file") -ne 1 ]]; then
    echo "FAIL: fallback did not emit exactly one warning"
    rc=1
  fi

  rm -rf "$runtime"
  rm -rf "$tmp"
  return "$rc"
}

# Verifies the real daemon/shim path gives shared:false servers one upstream
# process per live session and tears each process down with its owner.
smoke_daemon_private_instances() {
  local tmp runtime cfg fake pid_file input_one input_two output_one output_two
  local serve_one serve_two pid_one pid_two live rc=0
  tmp=$(mktemp -d -t mcpmu-smoke-private.XXXXXX)
  runtime=$(mktemp -d /tmp/mu-private.XXXXXX)
  chmod 0700 "$runtime"
  cfg="$tmp/config.json"
  fake="$tmp/fake-mcp.sh"
  pid_file="$tmp/upstreams.pid"
  input_one="$tmp/session-one.in"
  input_two="$tmp/session-two.in"
  output_one="$tmp/session-one.out"
  output_two="$tmp/session-two.out"

  cat > "$fake" <<'EOF'
#!/usr/bin/env bash
echo "$$" >> "$UPSTREAM_PID_FILE"
while IFS= read -r line; do
  method=$(printf '%s' "$line" | jq -r '.method // empty')
  id=$(printf '%s' "$line" | jq -r '.id // empty')
  case "$method" in
    initialize)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2025-06-18","capabilities":{"tools":{}},"serverInfo":{"name":"private-smoke","version":"1"}}}\n' "$id"
      ;;
    tools/list)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"tools":[]}}\n' "$id"
      ;;
  esac
done
EOF
  chmod 0700 "$fake"
  jq -n --arg command "$fake" --arg pid_file "$pid_file" '{
    schemaVersion: 1,
    servers: {
      browser: {
        command: $command,
        env: {UPSTREAM_PID_FILE: $pid_file},
        shared: false,
        startup_timeout_sec: 3
      }
    },
    namespaces: {}
  }' > "$cfg"

  mkfifo "$input_one" "$input_two"
  XDG_RUNTIME_DIR="$runtime" ./mcpmu serve --stdio --config "$cfg" < "$input_one" > "$output_one" 2>/dev/null &
  serve_one=$!
  exec 3>"$input_one"
  XDG_RUNTIME_DIR="$runtime" ./mcpmu serve --stdio --config "$cfg" < "$input_two" > "$output_two" 2>/dev/null 3>&- &
  serve_two=$!
  exec 4>"$input_two"

  printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"private-one","version":"0"}}}' >&3
  printf '%s\n' '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}' >&3
  printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"private-two","version":"0"}}}' >&4
  printf '%s\n' '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}' >&4

  for _ in {1..150}; do
    if [[ -s "$pid_file" ]] && [[ $(sort -u "$pid_file" | wc -l | tr -d ' ') -eq 2 ]]; then
      break
    fi
    sleep 0.02
  done
  if [[ ! -s "$pid_file" ]] || [[ $(sort -u "$pid_file" | wc -l | tr -d ' ') -ne 2 ]]; then
    echo "FAIL: shared:false sessions did not start two upstream processes"
    rc=1
  else
    pid_one=$(sort -u "$pid_file" | sed -n '1p')
    pid_two=$(sort -u "$pid_file" | sed -n '2p')

    exec 3>&-
    wait "$serve_one" 2>/dev/null || true
    for _ in {1..100}; do
      live=0
      kill -0 "$pid_one" 2>/dev/null && live=$((live + 1))
      kill -0 "$pid_two" 2>/dev/null && live=$((live + 1))
      [[ $live -eq 1 ]] && break
      sleep 0.02
    done
    if [[ $live -ne 1 ]]; then
      echo "FAIL: closing one session did not leave exactly one private upstream"
      rc=1
    fi

    exec 4>&-
    wait "$serve_two" 2>/dev/null || true
    for _ in {1..100}; do
      live=0
      kill -0 "$pid_one" 2>/dev/null && live=$((live + 1))
      kill -0 "$pid_two" 2>/dev/null && live=$((live + 1))
      [[ $live -eq 0 ]] && break
      sleep 0.02
    done
    if [[ $live -ne 0 ]]; then
      echo "FAIL: private upstream survived its session disconnect"
      rc=1
    fi
  fi

  exec 3>&- 2>/dev/null || true
  exec 4>&- 2>/dev/null || true
  kill "$serve_one" "$serve_two" 2>/dev/null || true
  XDG_RUNTIME_DIR="$runtime" ./mcpmu --config "$cfg" daemon stop >/dev/null 2>&1 || true
  rm -rf "$runtime"
  rm -rf "$tmp"
  return "$rc"
}

# Verifies that an upstream stdio server's final frame is still delivered when
# it is written without a trailing newline and the process then exits.
#
# bufio.ReadBytes hands back the bytes it read alongside io.EOF. The stdio
# transport used to check the error first and discard those bytes, so this exact
# shape — last response, no newline, immediate exit — lost the reply and the
# tool call failed. Only reproducible across a real process boundary.
smoke_stdio_trailing_frame() {
  local tmp cfg fake response rc=0
  tmp=$(mktemp -d -t mcpmu-smoke-trailing.XXXXXX)
  cfg="$tmp/config.json"
  fake="$tmp/fake-mcp.sh"

  cat > "$fake" <<'EOF'
#!/usr/bin/env bash
# Answers initialize and tools/list normally, then writes the tools/call reply
# with NO trailing newline and exits immediately.
while IFS= read -r line; do
  method=$(printf '%s' "$line" | jq -r '.method // empty')
  id=$(printf '%s' "$line" | jq -r '.id // empty')
  case "$method" in
    initialize)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2025-06-18","capabilities":{"tools":{}},"serverInfo":{"name":"smoke-trailing","version":"1"}}}\n' "$id"
      ;;
    tools/list)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"tools":[{"name":"ping","description":"ping","inputSchema":{"type":"object"}}]}}\n' "$id"
      ;;
    tools/call)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"content":[{"type":"text","text":"pong-no-newline"}]}}' "$id"
      exit 0
      ;;
  esac
done
EOF
  chmod 0700 "$fake"

  jq -n --arg command "$fake" '{
    schemaVersion: 1,
    servers: {
      trailing: {
        command: $command,
        startup_timeout_sec: 5
      }
    },
    namespaces: {}
  }' > "$cfg"

  response=$({
    printf '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"mcpmu-smoke","version":"0"}}}\n'
    printf '{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}\n'
    printf '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"trailing.ping","arguments":{}}}\n'
    sleep 5
  } | ./mcpmu serve --stdio --isolated --config "$cfg" 2>/dev/null)

  if ! printf '%s\n' "$response" \
    | jq -e 'select(.id == 2) | select(.result.content[0].text == "pong-no-newline")' >/dev/null; then
    echo "FAIL: final frame without a trailing newline was not delivered"
    printf '%s\n' "$response" | head -5
    rc=1
  fi

  rm -rf "$tmp"
  return "$rc"
}

# Verifies that a resource-update notification from an HTTP upstream reaches a
# downstream client through `mcpmu serve`.
#
# This is the end-to-end path that used to be a silent dead end: the transport
# only ever issued POSTs, so although serve advertised subscribe:true and
# resources/subscribe succeeded, notifications/resources/updated had no channel
# to arrive on. It only works if the transport opens the standalone GET SSE
# stream, which needs a real HTTP server and a real serve process to exercise.
smoke_http_sse_notification() {
  local tmp cfg upstream port response rc=0 server_pid=""

  if ! command -v python3 >/dev/null 2>&1; then
    echo "SKIP: python3 not available for smoke_http_sse_notification"
    return 0
  fi

  tmp=$(mktemp -d -t mcpmu-smoke-sse.XXXXXX)
  cfg="$tmp/config.json"
  upstream="$tmp/upstream.py"

  cat > "$upstream" <<'PYEOF'
import json, sys, threading, time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

RESOURCE = "file:///watched.txt"

class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, *args):
        pass

    def do_GET(self):
        # Only /mcp is the SSE stream. Everything else must 404 — in particular
        # the OAuth discovery probes (/.well-known/...), which would otherwise be
        # answered with an unterminated event stream and wedge the keep-alive
        # connection the following POST reuses.
        if self.path != "/mcp":
            self.send_response(404)
            self.send_header("Content-Length", "0")
            self.end_headers()
            return

        # A stream has no Content-Length, so it must close the connection rather
        # than be reused for a later request.
        self.close_connection = True
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Cache-Control", "no-cache")
        self.send_header("Connection", "close")
        self.end_headers()

        note = json.dumps({
            "jsonrpc": "2.0",
            "method": "notifications/resources/updated",
            "params": {"uri": RESOURCE},
        })
        try:
            # Keep-alives while serve finishes subscribing, then one event.
            for _ in range(10):
                time.sleep(0.1)
                self.wfile.write(b": keep-alive\n\n")
                self.wfile.flush()
            self.wfile.write(("id: ev-1\ndata: %s\n\n" % note).encode())
            self.wfile.flush()
            # Hold the stream open so the transport does not reconnect.
            time.sleep(10)
        except Exception:
            pass

    def do_POST(self):
        length = int(self.headers.get("Content-Length") or 0)
        req = json.loads(self.rfile.read(length) or b"{}")
        method = req.get("method")
        rid = req.get("id")

        if rid is None:
            self.send_response(202)
            self.send_header("Content-Length", "0")
            self.end_headers()
            return

        if method == "initialize":
            result = {
                "protocolVersion": "2025-06-18",
                "capabilities": {
                    "resources": {"subscribe": True, "listChanged": True},
                    "tools": {},
                },
                "serverInfo": {"name": "smoke-sse-upstream", "version": "1"},
            }
        elif method == "resources/list":
            result = {"resources": [{"uri": RESOURCE, "name": "watched"}]}
        elif method == "tools/list":
            result = {"tools": []}
        elif method in ("resources/subscribe", "resources/unsubscribe"):
            result = {}
        else:
            result = {}

        body = json.dumps({"jsonrpc": "2.0", "id": rid, "result": result}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Mcp-Session-Id", "smoke-session")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
server.daemon_threads = True
print(server.server_address[1], flush=True)
server.serve_forever()
PYEOF

  # Start the upstream and learn its port.
  exec 3< <(python3 "$upstream" 2>/dev/null)
  read -r port <&3 || true
  if [[ -z "${port:-}" ]]; then
    echo "FAIL: upstream HTTP server did not start"
    exec 3<&-
    rm -rf "$tmp"
    return 1
  fi

  jq -n --arg url "http://127.0.0.1:$port/mcp" '{
    schemaVersion: 1,
    servers: { watcher: { url: $url, startup_timeout_sec: 10 } },
    namespaces: {}
  }' > "$cfg"

  response=$({
    printf '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"mcpmu-smoke","version":"0"}}}\n'
    printf '{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}\n'
    printf '{"jsonrpc":"2.0","id":2,"method":"resources/list","params":{}}\n'
    sleep 1
    printf '{"jsonrpc":"2.0","id":3,"method":"resources/subscribe","params":{"uri":"file:///watched.txt"}}\n'
    sleep 4
  } | ./mcpmu serve --stdio --isolated --resources --config "$cfg" 2>/dev/null)

  # Stop the upstream.
  exec 3<&-
  pkill -f "$upstream" 2>/dev/null || true

  if ! printf '%s\n' "$response" | jq -e 'select(.id == 3 and .result)' >/dev/null; then
    echo "FAIL: resources/subscribe did not succeed"
    printf '%s\n' "$response" | head -5
    rc=1
  elif ! printf '%s\n' "$response" \
    | jq -e 'select(.method == "notifications/resources/updated")' >/dev/null; then
    echo "FAIL: resources/updated from the HTTP upstream never reached the client"
    printf '%s\n' "$response" | head -5
    rc=1
  fi

  rm -rf "$tmp"
  return "$rc"
}

# Register new smoke checks here.
SMOKE_CHECKS=(
  smoke_cf_access_headers
  smoke_process_group_cleanup
  smoke_stdio_trailing_frame
  smoke_http_sse_notification
  smoke_daemon_control
  smoke_daemon_shim_fallback
  smoke_daemon_private_instances
)

# --- Runner ---------------------------------------------------------------

run_check() {
  local name="$1"
  echo
  echo "==> $name"
  if "$name"; then
    echo "    OK"
    return 0
  else
    echo "    FAIL"
    return 1
  fi
}

main() {
  build_binary

  local checks=()
  if [[ $# -gt 0 ]]; then
    checks=("$@")
  else
    checks=("${SMOKE_CHECKS[@]}")
  fi

  local failures=0
  for c in "${checks[@]}"; do
    if ! run_check "$c"; then
      failures=$((failures + 1))
    fi
  done

  echo
  if (( failures > 0 )); then
    echo "==> $failures check(s) failed"
    exit 1
  fi
  echo "==> all checks passed"
}

main "$@"
