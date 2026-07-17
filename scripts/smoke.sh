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
  } | ./mcpmu serve --stdio --config "$cfg" --namespace "$ns" 2>/dev/null
}

# --- Smoke checks ----------------------------------------------------------

# Verifies the --header / --env-header CLI flow and the end-to-end CF-Access
# wire path (PLAN-headers.md's "Required test before merging" non-negotiable).
#
# Requires the Cloudflare Access creds for the bigsy.uk searxng MCP. Reads
# them from the environment so they never land in the repo.
smoke_cf_access_headers() {
  if [[ -z "${CF_ACCESS_CLIENT_ID:-}" || -z "${CF_ACCESS_CLIENT_SECRET:-}" ]]; then
    echo "SKIP: set CF_ACCESS_CLIENT_ID and CF_ACCESS_CLIENT_SECRET to run smoke_cf_access_headers"
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
  } | ./mcpmu serve --stdio --config "$cfg" 2>/dev/null)

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
  echo '{"schemaVersion":1,"daemonMode":true,"servers":{},"namespaces":{}}' > "$cfg"

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

# Register new smoke checks here.
SMOKE_CHECKS=(
  smoke_cf_access_headers
  smoke_process_group_cleanup
  smoke_daemon_control
  smoke_daemon_shim_fallback
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
