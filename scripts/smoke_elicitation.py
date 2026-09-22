#!/usr/bin/env python3
"""Real-binary elicitation relay smoke; no user config or network.

Adds a private (shared: false) server through the real CLI with --elicitation,
then drives it through `serve --stdio` and `serve --http`:
  - elicitation/create reaches the client, names the requester, and the
    client's answer reaches the upstream tool;
  - on --http with no GET stream, the request rides the tools/call POST's
    response stream (SSE upgrade) and the final response follows on it;
  - a URLElicitationRequiredError (-32042) passes through with its
    elicitation id rewritten, and notifications/elicitation/complete arrives
    under the rewritten id;
  - roots added with --root are declared upstream and answer roots/list.
"""
import json
import os
from pathlib import Path
import queue
import socket
import subprocess
import sys
import tempfile
import threading
import time
import urllib.request

binary, fake = map(os.path.abspath, sys.argv[1:])

FAKE = {
    "tools": [{"name": "confirm", "inputSchema": {"type": "object"}},
              {"name": "files", "inputSchema": {"type": "object"}},
              {"name": "roots", "inputSchema": {"type": "object"}}],
    "toolServerRequests": {"confirm": {
        "method": "elicitation/create",
        "params": {"mode": "form", "message": "Proceed?",
                   "requestedSchema": {"type": "object", "properties": {"ok": {"type": "boolean"}}}}},
        "roots": {"method": "roots/list"}},
    "toolURLElicitations": {"files": {
        "elicitationId": "upstream-7", "url": "https://auth.example.invalid/x",
        "message": "Authorize", "completeAfterMs": 300}},
}
CAPS = {"elicitation": {"form": {}, "url": {}}}


def fail(message):
    print("FAIL: " + message)
    sys.exit(1)


def tool_outcome(result):
    return json.loads(result["content"][0]["text"])


def check_stdio(binary, path, env, root):
    with (root / "serve.log").open("w") as log:
        proc = subprocess.Popen([binary, "serve", "--stdio", "--isolated", "--config", str(path)],
                                env=env, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                stderr=log, text=True, bufsize=1)
        incoming = queue.Queue()

        def read():
            for line in proc.stdout:
                incoming.put(json.loads(line))
            incoming.put(None)

        threading.Thread(target=read, daemon=True).start()
        pending = []

        def send(message):
            proc.stdin.write(json.dumps(message) + "\n")
            proc.stdin.flush()

        def wait(predicate, what):
            for i, message in enumerate(pending):
                if predicate(message):
                    return pending.pop(i)
            deadline = time.monotonic() + 10
            while time.monotonic() < deadline:
                try:
                    message = incoming.get(timeout=max(.01, deadline - time.monotonic()))
                except queue.Empty:
                    break
                if message is None:
                    fail("serve exited while waiting for " + what)
                if predicate(message):
                    return message
                pending.append(message)
            fail("timed out waiting for " + what)

        try:
            send({"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {
                "protocolVersion": "2025-11-25", "capabilities": CAPS,
                "clientInfo": {"name": "mcpmu-smoke", "version": "0"}}})
            wait(lambda m: m.get("id") == 1, "initialize")
            send({"jsonrpc": "2.0", "method": "notifications/initialized"})

            send({"jsonrpc": "2.0", "id": 2, "method": "tools/call",
                  "params": {"name": "confirmer.confirm", "arguments": {}}})
            request = wait(lambda m: m.get("method") == "elicitation/create", "elicitation/create")
            if not str(request.get("id", "")).startswith("mcpmu-"):
                fail("relayed request id %r is not an mcpmu id" % request.get("id"))
            if request["params"]["message"] != "[confirmer] Proceed?":
                fail("requester not named: %r" % request["params"]["message"])
            send({"jsonrpc": "2.0", "id": request["id"],
                  "result": {"action": "accept", "content": {"ok": True}}})
            outcome = tool_outcome(wait(lambda m: m.get("id") == 2, "tools/call response")["result"])
            if outcome.get("result") != {"action": "accept", "content": {"ok": True}}:
                fail("upstream saw %r" % outcome)

            send({"jsonrpc": "2.0", "id": 3, "method": "tools/call",
                  "params": {"name": "confirmer.files", "arguments": {}}})
            response = wait(lambda m: m.get("id") == 3, "files response")
            error = response.get("error") or {}
            if error.get("code") != -32042:
                fail("files did not return -32042: %r" % response)
            minted = error["data"]["elicitations"][0]["elicitationId"]
            if minted == "upstream-7" or not minted.startswith("mcpmu/"):
                fail("elicitation id not rewritten: %r" % minted)
            done = wait(lambda m: m.get("method") == "notifications/elicitation/complete",
                        "notifications/elicitation/complete")
            if done["params"]["elicitationId"] != minted:
                fail("completion names %r, want %r" % (done["params"]["elicitationId"], minted))

            send({"jsonrpc": "2.0", "id": 4, "method": "tools/call",
                  "params": {"name": "confirmer.roots", "arguments": {}}})
            outcome = tool_outcome(wait(lambda m: m.get("id") == 4, "roots response")["result"])
            if outcome.get("result") != {"roots": [{"uri": "file:///smoke/workspace", "name": "workspace"}]}:
                fail("roots/list answered %r" % outcome)
        finally:
            proc.stdin.close()
            proc.wait(timeout=10)


def free_port():
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def post(base, body, session=None):
    headers = {"Content-Type": "application/json", "Accept": "application/json, text/event-stream"}
    if session:
        headers["Mcp-Session-Id"] = session
    request = urllib.request.Request(base, data=json.dumps(body).encode(), headers=headers, method="POST")
    return urllib.request.urlopen(request, timeout=15)


def sse_events(response):
    data = []
    for raw in response:
        line = raw.decode().rstrip("\r\n")
        if line.startswith("data:"):
            data.append(line[5:].lstrip(" "))
        elif line == "" and data:
            yield json.loads("\n".join(data))
            data = []


def check_http(binary, path, env, root):
    port = free_port()
    base = "http://127.0.0.1:%d/mcp" % port
    with (root / "serve-http.log").open("w") as log:
        proc = subprocess.Popen([binary, "serve", "--http", "--addr", "127.0.0.1:%d" % port,
                                 "--config", str(path)], env=env, stdout=log, stderr=log)
        try:
            deadline = time.monotonic() + 10
            while True:
                try:
                    urllib.request.urlopen("http://127.0.0.1:%d/healthz" % port, timeout=1).close()
                    break
                except OSError:
                    if time.monotonic() > deadline:
                        fail("serve --http never became healthy")
                    time.sleep(.05)

            response = post(base, {"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {
                "protocolVersion": "2025-11-25", "capabilities": CAPS,
                "clientInfo": {"name": "mcpmu-smoke", "version": "0"}}})
            session = response.headers["Mcp-Session-Id"]
            response.read()
            post(base, {"jsonrpc": "2.0", "method": "notifications/initialized"}, session).read()

            call = post(base, {"jsonrpc": "2.0", "id": 2, "method": "tools/call",
                               "params": {"name": "confirmer.confirm", "arguments": {}}}, session)
            if not call.headers.get("Content-Type", "").startswith("text/event-stream"):
                fail("tools/call POST was not upgraded to SSE: %r" % call.headers.get("Content-Type"))
            events = sse_events(call)
            request = next(events)
            if request.get("method") != "elicitation/create":
                fail("first POST-stream event is not the elicitation: %r" % request)
            post(base, {"jsonrpc": "2.0", "id": request["id"],
                        "result": {"action": "decline"}}, session).read()
            final = next(events)
            if final.get("id") != 2 or tool_outcome(final["result"]).get("result") != {"action": "decline"}:
                fail("final POST-stream event: %r" % final)
        finally:
            proc.terminate()
            proc.wait(timeout=10)


with tempfile.TemporaryDirectory(prefix="mu-elicit-", dir="/tmp") as directory:
    root = Path(directory)
    path = root / "config.json"
    path.write_text(json.dumps({"schemaVersion": 1, "daemonMode": False,
                                "metrics": {"enabled": False}, "servers": {}}))
    env = dict(os.environ, XDG_RUNTIME_DIR=directory)
    subprocess.run([binary, "--config", str(path), "add", "confirmer", "--shared=false", "--elicitation",
                    "--root", "/smoke/workspace",
                    "--env", "GO_WANT_HELPER_PROCESS=1", "--env", "FAKE_MCP_CFG=" + json.dumps(FAKE),
                    "--", fake, "-test.run=TestHelperProcess", "--"],
                   env=env, check=True, capture_output=True, timeout=10)
    server = json.loads(path.read_text())["servers"]["confirmer"]
    if (server.get("clientFeatures") != {"elicitation": True} or server.get("shared") is not False
            or server.get("roots") != ["file:///smoke/workspace"]):
        fail("CLI wrote %r" % server)
    check_stdio(binary, path, env, root)
    check_http(binary, path, env, root)
