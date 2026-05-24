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
  if [[ ! -x ./mcpmu || ./cmd/mcpmu -nt ./mcpmu ]]; then
    echo "==> building binary"
    go build -o mcpmu ./cmd/mcpmu
  fi
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

# Register new smoke checks here.
SMOKE_CHECKS=(
  smoke_cf_access_headers
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
