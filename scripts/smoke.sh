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

# Verifies the usage-metrics write path end to end: a tool call routed through
# a real serve process must land in metrics.json (co-located with the config)
# after the shutdown flush, with names/timings only — never arguments.
#
# This crosses two process boundaries (serve subprocess, fake upstream) and
# exercises the flock merge + atomic rename against a real filesystem, which
# unit tests approximate but cannot fully prove.
smoke_usage_metrics() {
  local tmp cfg fake response rc=0
  tmp=$(mktemp -d -t mcpmu-smoke-metrics.XXXXXX)
  cfg="$tmp/config.json"
  fake="$tmp/fake-mcp.sh"

  cat > "$fake" <<'EOF'
#!/usr/bin/env bash
while IFS= read -r line; do
  method=$(printf '%s' "$line" | jq -r '.method // empty')
  id=$(printf '%s' "$line" | jq -r '.id // empty')
  case "$method" in
    initialize)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2025-06-18","capabilities":{"tools":{}},"serverInfo":{"name":"smoke-metrics","version":"1"}}}\n' "$id"
      ;;
    tools/list)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"tools":[{"name":"ping","description":"ping","inputSchema":{"type":"object"}}]}}\n' "$id"
      ;;
    tools/call)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"content":[{"type":"text","text":"pong"}]}}\n' "$id"
      ;;
  esac
done
EOF
  chmod 0700 "$fake"

  jq -n --arg command "$fake" '{
    schemaVersion: 1,
    servers: {
      pinger: {
        command: $command,
        startup_timeout_sec: 5
      }
    },
    namespaces: {}
  }' > "$cfg"

  response=$({
    printf '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"mcpmu-smoke","version":"0"}}}\n'
    printf '{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}\n'
    printf '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"pinger.ping","arguments":{"smoke_secret_arg":"do-not-store"}}}\n'
    sleep 5
  } | ./mcpmu serve --stdio --isolated --config "$cfg" 2>/dev/null)

  if ! printf '%s\n' "$response" \
    | jq -e 'select(.id == 2) | select(.result.content[0].text == "pong")' >/dev/null; then
    echo "FAIL: tool call through serve did not succeed"
    printf '%s\n' "$response" | head -5
    rc=1
  fi

  # The final flush on shutdown must have written the sidecar next to the config.
  if [ ! -f "$tmp/metrics.json" ]; then
    echo "FAIL: metrics.json was not written next to the config"
    rc=1
  elif ! jq -e '.rows[] | select(.server == "pinger" and .tool == "ping" and .calls >= 1 and .outcomes.ok >= 1)' \
      "$tmp/metrics.json" >/dev/null; then
    echo "FAIL: metrics.json is missing the pinger.ping ok row"
    jq . "$tmp/metrics.json" | head -20
    rc=1
  fi

  # Privacy: tool arguments must never land in the metrics file.
  if [ -f "$tmp/metrics.json" ] && grep -q "do-not-store" "$tmp/metrics.json"; then
    echo "FAIL: tool arguments leaked into metrics.json"
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

# Verifies that a tool definition's 2025-11-25 metadata survives the proxy hop,
# that the result envelope keeps `structuredContent`, and that `execution` is
# stripped.
#
# Unit tests cover each hop in isolation; only a real `serve` process proves the
# whole wire path, which is where every one of these fields used to be discarded
# at unmarshal time.
smoke_tool_metadata_fidelity() {
  local tmp cfg fake response tool rc=0

  command -v jq >/dev/null 2>&1 || { echo "SKIP: jq not available"; return 0; }

  tmp=$(mktemp -d -t mcpmu-smoke-fidelity.XXXXXX)
  cfg="$tmp/config.json"
  fake="$tmp/fake-mcp.sh"

  cat > "$fake" <<'EOF'
#!/usr/bin/env bash
# Serves one tool carrying every field the 2025-11-25 tools spec defines, plus
# an unknown member standing in for a future revision.
while IFS= read -r line; do
  method=$(printf '%s' "$line" | jq -r '.method // empty')
  id=$(printf '%s' "$line" | jq -r '.id // empty')
  case "$method" in
    initialize)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2025-11-25","capabilities":{"tools":{}},"serverInfo":{"name":"smoke-fidelity","version":"1"}}}\n' "$id"
      ;;
    tools/list)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"tools":[{' "$id"
      printf '"name":"read_thing","title":"Read Thing","description":"reads a thing",'
      printf '"inputSchema":{"type":"object","properties":{"id":{"type":"integer","maximum":9007199254740993}}},'
      printf '"outputSchema":{"type":"object","properties":{"thing":{"type":"string"}}},'
      printf '"annotations":{"readOnlyHint":true,"idempotentHint":true},'
      printf '"icons":[{"src":"https://example.test/i.png","mimeType":"image/png"}],'
      printf '"execution":{"taskSupport":"required"},'
      printf '"_meta":{"vendor.example/tier":"gold"},'
      printf '"futureField":{"revision":"2027-01-01"}'
      printf '}]}}\n'
      ;;
    tools/call)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"content":[{"type":"text","text":"ok"}],"structuredContent":{"thing":"value"},"_meta":{"vendor.example/ms":7}}}\n' "$id"
      ;;
  esac
done
EOF
  chmod 0700 "$fake"

  jq -n --arg command "$fake" '{
    schemaVersion: 1,
    servers: { fidelity: { command: $command, startup_timeout_sec: 5 } },
    namespaces: {}
  }' > "$cfg"

  response=$({
    printf '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"mcpmu-smoke","version":"0"}}}\n'
    printf '{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}\n'
    printf '{"jsonrpc":"2.0","id":2,"method":"tools/list"}\n'
    sleep 2
    printf '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"fidelity.read_thing","arguments":{"id":1}}}\n'
    sleep 3
  } | ./mcpmu serve --stdio --isolated --config "$cfg" 2>/dev/null)

  tool=$(printf '%s\n' "$response" | jq -c 'select(.id == 2) | .result.tools[0]' 2>/dev/null)
  if [[ -z "$tool" ]]; then
    echo "FAIL: tools/list returned nothing"
    printf '%s\n' "$response" | head -5
    rm -rf "$tmp"
    return 1
  fi

  local field
  for field in title outputSchema annotations icons _meta futureField; do
    if ! printf '%s' "$tool" | jq -e --arg f "$field" 'has($f)' >/dev/null; then
      echo "FAIL: tools/list dropped $field"
      printf '%s\n' "$tool"
      rc=1
    fi
  done
  if ! printf '%s' "$tool" | jq -e '.annotations.readOnlyHint == true' >/dev/null; then
    echo "FAIL: annotations.readOnlyHint did not survive"
    rc=1
  fi
  if printf '%s' "$tool" | jq -e 'has("execution")' >/dev/null; then
    echo "FAIL: execution was forwarded downstream (mcpmu implements no tasks/*)"
    rc=1
  fi
  # A float64 round trip would turn this into 9007199254740992.
  if ! printf '%s' "$tool" | grep -q 9007199254740993; then
    echo "FAIL: a large integer in inputSchema was mangled by a round trip"
    printf '%s\n' "$tool"
    rc=1
  fi
  if ! printf '%s\n' "$response" \
    | jq -e 'select(.id == 3) | select(.result.structuredContent.thing == "value")' >/dev/null; then
    echo "FAIL: structuredContent did not survive tools/call"
    printf '%s\n' "$response" | head -5
    rc=1
  fi
  if ! printf '%s\n' "$response" | jq -e 'select(.id == 3) | .result | has("_meta")' >/dev/null; then
    echo "FAIL: result _meta did not survive tools/call"
    rc=1
  fi

  rm -rf "$tmp"
  return "$rc"
}

# Verifies downstream protocol version negotiation: the revision the client
# asked for comes back, not a hard-coded one.
#
# mcpmu negotiates up to 2025-11-25 upstream, so presenting a 2024 face to the
# agent left every field above technically present and practically unused.
smoke_protocol_negotiation() {
  local cfg negotiated rc=0

  command -v jq >/dev/null 2>&1 || { echo "SKIP: jq not available"; return 0; }

  cfg=$(new_temp_config)

  ask_version() {
    local requested="$1"
    {
      printf '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"%s","capabilities":{},"clientInfo":{"name":"mcpmu-smoke","version":"0"}}}\n' "$requested"
      sleep 1
    } | ./mcpmu serve --stdio --isolated --config "$cfg" 2>/dev/null \
      | jq -r 'select(.id == 1) | .result.protocolVersion' 2>/dev/null
  }

  local requested
  for requested in 2025-11-25 2025-06-18 2024-11-05; do
    negotiated=$(ask_version "$requested")
    if [[ "$negotiated" != "$requested" ]]; then
      echo "FAIL: client asked for $requested, mcpmu answered ${negotiated:-<nothing>}"
      rc=1
    fi
  done

  # An unknown future revision must get mcpmu's own newest, not an echo.
  negotiated=$(ask_version 2099-01-01)
  if [[ "$negotiated" != "2025-11-25" ]]; then
    echo "FAIL: unknown revision answered ${negotiated:-<nothing>}, want 2025-11-25"
    rc=1
  fi

  rm -f "$cfg"
  return "$rc"
}

# Verifies the request lifecycle end to end: a cancelled tool call is withdrawn
# from the upstream server, and progress notifications reach the client carrying
# the client's own progressToken.
#
# Both used to be dead ends — cancellation was a log line, and the request
# `_meta` that carries progressToken was dropped before it reached upstream, so
# no server was ever asked for progress at all.
smoke_cancel_and_progress() {
  local tmp cfg fake upstream_log response rc=0

  command -v jq >/dev/null 2>&1 || { echo "SKIP: jq not available"; return 0; }

  tmp=$(mktemp -d -t mcpmu-smoke-lifecycle.XXXXXX)
  cfg="$tmp/config.json"
  fake="$tmp/fake-mcp.sh"
  upstream_log="$tmp/upstream.log"

  cat > "$fake" <<'EOF'
#!/usr/bin/env bash
# Logs every method it is sent, emits progress for any call that carries a
# progressToken, and takes 5s to answer tools/call so there is time to cancel.
log="$UPSTREAM_LOG"
while IFS= read -r line; do
  method=$(printf '%s' "$line" | jq -r '.method // empty')
  id=$(printf '%s' "$line" | jq -r '.id // empty')
  printf '%s\n' "$method" >> "$log"
  case "$method" in
    initialize)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2025-11-25","capabilities":{"tools":{}},"serverInfo":{"name":"smoke-lifecycle","version":"1"}}}\n' "$id"
      ;;
    tools/list)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"tools":[{"name":"slow","description":"slow","inputSchema":{"type":"object"}}]}}\n' "$id"
      ;;
    tools/call)
      token=$(printf '%s' "$line" | jq -c '.params._meta.progressToken // empty')
      if [[ -n "$token" ]]; then
        printf '{"jsonrpc":"2.0","method":"notifications/progress","params":{"progressToken":%s,"progress":1,"total":2}}\n' "$token"
      fi
      sleep 5
      printf '{"jsonrpc":"2.0","id":%s,"result":{"content":[{"type":"text","text":"done"}]}}\n' "$id"
      ;;
  esac
done
EOF
  chmod 0700 "$fake"

  jq -n --arg command "$fake" --arg log "$upstream_log" '{
    schemaVersion: 1,
    servers: {
      lifecycle: {
        command: $command,
        startup_timeout_sec: 10,
        env: { UPSTREAM_LOG: $log }
      }
    },
    namespaces: {}
  }' > "$cfg"

  response=$({
    printf '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"mcpmu-smoke","version":"0"}}}\n'
    printf '{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}\n'
    printf '{"jsonrpc":"2.0","id":2,"method":"tools/list"}\n'
    sleep 3
    # A call that asks for progress and is then withdrawn.
    printf '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"lifecycle.slow","arguments":{},"_meta":{"progressToken":"smoke-token"}}}\n'
    sleep 2
    printf '{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":3,"reason":"smoke test"}}\n'
    sleep 8
  } | ./mcpmu serve --stdio --isolated --config "$cfg" 2>/dev/null)

  if ! printf '%s\n' "$response" \
    | jq -e 'select(.method == "notifications/progress") | select(.params.progressToken == "smoke-token")' >/dev/null; then
    echo "FAIL: no progress notification carrying the client's own token reached the client"
    printf '%s\n' "$response" | head -8
    rc=1
  fi
  if printf '%s\n' "$response" | grep -q '"progressToken":"mcpmu/'; then
    echo "FAIL: mcpmu's internal progress token leaked to the client"
    rc=1
  fi
  if ! printf '%s\n' "$response" | jq -e 'select(.id == 3) | has("error")' >/dev/null; then
    echo "FAIL: a cancelled tools/call did not return an error to the client"
    printf '%s\n' "$response" | head -8
    rc=1
  fi
  if ! grep -q 'notifications/cancelled' "$upstream_log" 2>/dev/null; then
    echo "FAIL: the upstream server was never told the call was cancelled"
    echo "upstream methods seen:"
    cat "$upstream_log" 2>/dev/null | head -10
    rc=1
  fi

  rm -rf "$tmp"
  return "$rc"
}

# Verifies `mcpmu serve --http` end to end on the real wire: a Streamable
# HTTP session against the real binary (401 without token → initialize with
# session-ID capture → tools/list → tools/call), then the dogfood twist — a
# second mcpmu config points at that endpoint as an HTTP upstream with a
# bearer env var and calls the same tool through it, proving
# mcpmu-behind-mcpmu works on the real wire.
smoke_serve_http() {
  local tmp cfg cfg2 fake port token base sid code resp rc=0 serve_pid=""

  command -v jq >/dev/null 2>&1 || { echo "SKIP: jq not available"; return 0; }
  command -v curl >/dev/null 2>&1 || { echo "SKIP: curl not available"; return 0; }
  command -v python3 >/dev/null 2>&1 || { echo "SKIP: python3 not available (port picking)"; return 0; }

  tmp=$(mktemp -d -t mcpmu-smoke-servehttp.XXXXXX)
  cfg="$tmp/config.json"
  cfg2="$tmp/client-config.json"
  fake="$tmp/fake-mcp.sh"

  cat > "$fake" <<'EOF'
#!/usr/bin/env bash
while IFS= read -r line; do
  method=$(printf '%s' "$line" | jq -r '.method // empty')
  id=$(printf '%s' "$line" | jq -r '.id // empty')
  case "$method" in
    initialize)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2025-06-18","capabilities":{"tools":{}},"serverInfo":{"name":"smoke-http","version":"1"}}}\n' "$id"
      ;;
    tools/list)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"tools":[{"name":"ping","description":"ping","inputSchema":{"type":"object"}}]}}\n' "$id"
      ;;
    tools/call)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"content":[{"type":"text","text":"pong-http"}]}}\n' "$id"
      ;;
  esac
done
EOF
  chmod 0700 "$fake"

  jq -n --arg command "$fake" '{
    schemaVersion: 1,
    servers: { pinger: { command: $command, startup_timeout_sec: 5 } },
    namespaces: {}
  }' > "$cfg"

  port=$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')
  token="smoke-token-$$"
  base="http://127.0.0.1:$port/mcp"

  ./mcpmu serve --http --addr "127.0.0.1:$port" --token "$token" --config "$cfg" 2>"$tmp/serve.log" &
  serve_pid=$!

  # Readiness: poll the unauthenticated /healthz probe.
  local ready=""
  for _ in $(seq 1 50); do
    if curl -fsS --max-time 2 "http://127.0.0.1:$port/healthz" >/dev/null 2>&1; then
      ready=1
      break
    fi
    if ! kill -0 "$serve_pid" 2>/dev/null; then
      break
    fi
    sleep 0.1
  done
  if [[ -z "$ready" ]]; then
    echo "SKIP: mcpmu serve --http did not come up on 127.0.0.1:$port"
    head -5 "$tmp/serve.log" 2>/dev/null
    kill "$serve_pid" 2>/dev/null || true
    rm -rf "$tmp"
    return 0
  fi

  # Security posture: no token → 401.
  code=$(curl -s -o /dev/null -w '%{http_code}' -X POST -H 'Content-Type: application/json' \
    -d '{"jsonrpc":"2.0","id":1,"method":"ping"}' "$base")
  if [[ "$code" != "401" ]]; then
    echo "FAIL: POST without token returned $code, want 401"
    rc=1
  fi

  # Initialize, capturing the Mcp-Session-Id response header.
  resp=$(curl -sS -D "$tmp/headers" -H "Authorization: Bearer $token" -H 'Content-Type: application/json' \
    -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"mcpmu-smoke","version":"0"}}}' \
    "$base")
  sid=$(awk 'tolower($1) == "mcp-session-id:" {print $2}' "$tmp/headers" | tr -d '\r')
  if [[ -z "$sid" ]]; then
    echo "FAIL: initialize issued no Mcp-Session-Id"
    printf '%s\n' "$resp" | head -3
    rc=1
  fi
  if ! printf '%s\n' "$resp" | jq -e '.result.serverInfo.name == "mcpmu"' >/dev/null; then
    echo "FAIL: initialize result is not from mcpmu"
    printf '%s\n' "$resp" | head -3
    rc=1
  fi

  curl -s -o /dev/null -H "Authorization: Bearer $token" -H 'Content-Type: application/json' \
    -H "Mcp-Session-Id: $sid" \
    -d '{"jsonrpc":"2.0","method":"notifications/initialized"}' "$base"

  # tools/list must show the upstream's tool; tools/call must reach it.
  resp=$(curl -sS -H "Authorization: Bearer $token" -H 'Content-Type: application/json' \
    -H "Mcp-Session-Id: $sid" \
    -d '{"jsonrpc":"2.0","id":2,"method":"tools/list"}' "$base")
  if ! printf '%s\n' "$resp" | jq -e '.result.tools[] | select(.name == "pinger.ping")' >/dev/null; then
    echo "FAIL: tools/list over HTTP is missing pinger.ping"
    printf '%s\n' "$resp" | head -3
    rc=1
  fi
  resp=$(curl -sS -H "Authorization: Bearer $token" -H 'Content-Type: application/json' \
    -H "Mcp-Session-Id: $sid" \
    -d '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"pinger.ping","arguments":{}}}' "$base")
  if ! printf '%s\n' "$resp" | jq -e '.result.content[0].text == "pong-http"' >/dev/null; then
    echo "FAIL: tools/call over HTTP did not reach the upstream"
    printf '%s\n' "$resp" | head -3
    rc=1
  fi

  # Dogfood: a second mcpmu (stdio serve) using the HTTP endpoint as an
  # upstream, authenticated via --bearer-env-style config.
  jq -n --arg url "$base" '{
    schemaVersion: 1,
    servers: { mcpmux: { url: $url, bearer_token_env_var: "MCPMU_SMOKE_HTTP_TOKEN", startup_timeout_sec: 10 } },
    namespaces: {}
  }' > "$cfg2"

  resp=$({
    printf '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"mcpmu-smoke","version":"0"}}}\n'
    printf '{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}\n'
    printf '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"mcpmux.pinger.ping","arguments":{}}}\n'
    sleep 5
  } | MCPMU_SMOKE_HTTP_TOKEN="$token" ./mcpmu serve --stdio --isolated --config "$cfg2" 2>/dev/null)
  if ! printf '%s\n' "$resp" \
    | jq -e 'select(.id == 2) | .result.content[0].text == "pong-http"' >/dev/null; then
    echo "FAIL: mcpmu-behind-mcpmu tool call did not round-trip"
    printf '%s\n' "$resp" | head -5
    rc=1
  fi

  kill "$serve_pid" 2>/dev/null || true
  wait "$serve_pid" 2>/dev/null || true
  rm -rf "$tmp"
  return "$rc"
}

# Graceful shutdown: SIGTERM during an in-flight tool call must drain the call
# before the Core is closed. ListenAndServe returns the instant the listener
# closes, so a serve that stops waiting there tears down upstream processes
# (and writes the final metrics flush) underneath calls that are still running.
smoke_serve_http_graceful_shutdown() {
  local tmp cfg fake port token base sid rc=0 serve_pid="" call_pid="" alive=""

  command -v jq >/dev/null 2>&1 || { echo "SKIP: jq not available"; return 0; }
  command -v curl >/dev/null 2>&1 || { echo "SKIP: curl not available"; return 0; }
  command -v python3 >/dev/null 2>&1 || { echo "SKIP: python3 not available (port picking)"; return 0; }

  tmp=$(mktemp -d -t mcpmu-smoke-servehttp-term.XXXXXX)
  cfg="$tmp/config.json"
  fake="$tmp/fake-mcp.sh"

  cat > "$fake" <<'EOF'
#!/usr/bin/env bash
while IFS= read -r line; do
  method=$(printf '%s' "$line" | jq -r '.method // empty')
  id=$(printf '%s' "$line" | jq -r '.id // empty')
  case "$method" in
    initialize)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2025-06-18","capabilities":{"tools":{}},"serverInfo":{"name":"smoke-slow","version":"1"}}}\n' "$id"
      ;;
    tools/list)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"tools":[{"name":"slow","description":"slow","inputSchema":{"type":"object"}}]}}\n' "$id"
      ;;
    tools/call)
      sleep 2
      printf '{"jsonrpc":"2.0","id":%s,"result":{"content":[{"type":"text","text":"pong-slow"}]}}\n' "$id"
      ;;
  esac
done
EOF
  chmod 0700 "$fake"

  jq -n --arg command "$fake" '{
    schemaVersion: 1,
    servers: { slowpoke: { command: $command, startup_timeout_sec: 5, tool_timeout_sec: 30 } },
    namespaces: {}
  }' > "$cfg"

  port=$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')
  token="smoke-token-$$"
  base="http://127.0.0.1:$port/mcp"

  ./mcpmu serve --http --addr "127.0.0.1:$port" --token "$token" --config "$cfg" 2>"$tmp/serve.log" &
  serve_pid=$!

  local ready=""
  for _ in $(seq 1 50); do
    if curl -fsS --max-time 2 "http://127.0.0.1:$port/healthz" >/dev/null 2>&1; then
      ready=1
      break
    fi
    kill -0 "$serve_pid" 2>/dev/null || break
    sleep 0.1
  done
  if [[ -z "$ready" ]]; then
    echo "SKIP: mcpmu serve --http did not come up on 127.0.0.1:$port"
    head -5 "$tmp/serve.log" 2>/dev/null
    kill "$serve_pid" 2>/dev/null || true
    rm -rf "$tmp"
    return 0
  fi

  curl -sS -D "$tmp/headers" -o /dev/null -H "Authorization: Bearer $token" -H 'Content-Type: application/json' \
    -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"mcpmu-smoke","version":"0"}}}' \
    "$base"
  sid=$(awk 'tolower($1) == "mcp-session-id:" {print $2}' "$tmp/headers" | tr -d '\r')
  if [[ -z "$sid" ]]; then
    echo "FAIL: initialize issued no Mcp-Session-Id"
    kill "$serve_pid" 2>/dev/null || true
    rm -rf "$tmp"
    return 1
  fi
  curl -s -o /dev/null -H "Authorization: Bearer $token" -H 'Content-Type: application/json' \
    -H "Mcp-Session-Id: $sid" \
    -d '{"jsonrpc":"2.0","method":"notifications/initialized"}' "$base"
  # Warm the upstream so the timing below measures the call, not the spawn.
  curl -s -o /dev/null -H "Authorization: Bearer $token" -H 'Content-Type: application/json' \
    -H "Mcp-Session-Id: $sid" \
    -d '{"jsonrpc":"2.0","id":2,"method":"tools/list"}' "$base"

  # A 2s call, SIGTERM'd a second in.
  curl -sS --max-time 20 -H "Authorization: Bearer $token" -H 'Content-Type: application/json' \
    -H "Mcp-Session-Id: $sid" \
    -d '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"slowpoke.slow","arguments":{}}}' \
    "$base" > "$tmp/call.json" 2>/dev/null &
  call_pid=$!
  sleep 1
  kill -TERM "$serve_pid" 2>/dev/null || true
  wait "$call_pid" 2>/dev/null || true

  if ! jq -e '.result.content[0].text == "pong-slow"' < "$tmp/call.json" >/dev/null 2>&1; then
    echo "FAIL: in-flight tool call did not survive SIGTERM"
    head -3 "$tmp/call.json" 2>/dev/null
    rc=1
  fi

  for _ in $(seq 1 50); do
    if ! kill -0 "$serve_pid" 2>/dev/null; then
      alive=""
      break
    fi
    alive=1
    sleep 0.2
  done
  if [[ -n "$alive" ]]; then
    echo "FAIL: serve --http did not exit within 10s of SIGTERM"
    kill -9 "$serve_pid" 2>/dev/null || true
    rc=1
  fi
  wait "$serve_pid" 2>/dev/null || true
  rm -rf "$tmp"
  return "$rc"
}

# Verifies the compressed tool surface (`serve --compress`): tools/list
# returns only the three wrapper tools, the embedded listing names the real
# tool, get_tool_schema resolves the full definition, and invoke_tool
# round-trips to the upstream. Real binary, real flag parsing — catches
# CLI-to-Session plumbing regressions unit tests can't.
smoke_serve_compress() {
  local tmp cfg fake response rc=0
  tmp=$(mktemp -d -t mcpmu-smoke-compress.XXXXXX)
  cfg="$tmp/config.json"
  fake="$tmp/fake-mcp.sh"

  cat > "$fake" <<'EOF'
#!/usr/bin/env bash
while IFS= read -r line; do
  method=$(printf '%s' "$line" | jq -r '.method // empty')
  id=$(printf '%s' "$line" | jq -r '.id // empty')
  case "$method" in
    initialize)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2025-06-18","capabilities":{"tools":{}},"serverInfo":{"name":"smoke-compress","version":"1"}}}\n' "$id"
      ;;
    tools/list)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"tools":[{"name":"ping","description":"Ping the smoke server. Returns pong.","inputSchema":{"type":"object","properties":{"target":{"type":"string"}},"required":["target"]}}]}}\n' "$id"
      ;;
    tools/call)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"content":[{"type":"text","text":"pong"}]}}\n' "$id"
      ;;
  esac
done
EOF
  chmod 0700 "$fake"

  jq -n --arg command "$fake" '{
    schemaVersion: 1,
    servers: {
      pinger: {
        command: $command,
        startup_timeout_sec: 5
      }
    },
    namespaces: {}
  }' > "$cfg"

  response=$({
    printf '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"mcpmu-smoke","version":"0"}}}\n'
    printf '{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}\n'
    printf '{"jsonrpc":"2.0","id":2,"method":"tools/list"}\n'
    printf '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"get_tool_schema","arguments":{"tool":"pinger.ping"}}}\n'
    printf '{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"invoke_tool","arguments":{"tool":"pinger.ping","input":{"target":"x"}}}}\n'
    sleep 3
  } | ./mcpmu serve --stdio --isolated --config "$cfg" --compress medium 2>/dev/null)

  # tools/list must return exactly the three wrappers, with the listing
  # embedded in invoke_tool's description.
  if ! printf '%s\n' "$response" \
    | jq -e 'select(.id == 2) | .result.tools | map(.name) | sort == ["get_tool_schema","invoke_tool","list_tools"]' >/dev/null; then
    echo "FAIL: tools/list did not return exactly the three wrapper tools"
    printf '%s\n' "$response" | head -5
    rc=1
  fi
  if ! printf '%s\n' "$response" \
    | jq -e 'select(.id == 2) | .result.tools[] | select(.name == "invoke_tool") | .description | contains("<tool>pinger.ping(target)")' >/dev/null; then
    echo "FAIL: invoke_tool description does not embed the compact listing"
    rc=1
  fi

  # get_tool_schema resolves the full definition.
  if ! printf '%s\n' "$response" \
    | jq -e 'select(.id == 3) | .result.structuredContent | .name == "pinger.ping" and (.inputSchema.properties | has("target"))' >/dev/null; then
    echo "FAIL: get_tool_schema did not return the full tool definition"
    rc=1
  fi

  # invoke_tool round-trips to the upstream.
  if ! printf '%s\n' "$response" \
    | jq -e 'select(.id == 4) | select(.result.content[0].text == "pong")' >/dev/null; then
    echo "FAIL: invoke_tool did not reach the upstream tool"
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
  smoke_usage_metrics
  smoke_serve_compress
  smoke_http_sse_notification
  smoke_tool_metadata_fidelity
  smoke_protocol_negotiation
  smoke_cancel_and_progress
  smoke_serve_http
  smoke_serve_http_graceful_shutdown
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
