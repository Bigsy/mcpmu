#!/usr/bin/env python3
"""Self-contained real-binary reload/diagnostic smoke; no user config or network."""
import json
import os
from pathlib import Path
import queue
import subprocess
import sys
import tempfile
import threading
import time

binary, fake = map(os.path.abspath, sys.argv[1:])
with tempfile.TemporaryDirectory(prefix="mu-reload-", dir="/tmp") as directory:
    root = Path(directory)
    path = root / "config.json"
    env = dict(os.environ, XDG_RUNTIME_DIR=directory)
    cfg = {"schemaVersion": 1, "daemonMode": False, "metrics": {"enabled": False},
           "servers": {}, "namespaces": {"work": {"serverIds": ["a", "b"]}}}
    for name in ["a", "b"]:
        cfg["servers"][name] = {
            "command": fake, "args": ["-test.run=TestHelperProcess", "--"],
            "env": {"GO_WANT_HELPER_PROCESS": "1", "FAKE_MCP_CFG": json.dumps({
                "tools": [{"name": "ping", "description": "Ping. Return a result.", "inputSchema": {"type": "object"}}],
                "echoToolCalls": True, "requestLogPath": str(root / (name + ".log"))})}}

    def save():
        temporary = path.with_suffix(".tmp")
        temporary.write_text(json.dumps(cfg))
        temporary.replace(path)

    def starts(name):
        return (root / (name + ".log")).read_text().splitlines().count("initialize")

    save()
    before = path.read_bytes()
    for command in ["status", "doctor"]:
        result = subprocess.run([binary, command, "--json", "--config", str(path)], env=env,
                                text=True, capture_output=True, timeout=6, check=True)
        report = json.loads(result.stdout)
        assert report["configValid"] and report["daemonState"] == "disabled", report
    assert path.read_bytes() == before
    assert not (root / "mcpmu").exists(), "diagnostics created runtime state"
    with (root / "serve.log").open("w") as log:
        proc = subprocess.Popen([binary, "serve", "--stdio", "--isolated", "--config", str(path)],
                                env=env, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                stderr=log, text=True, bufsize=1)
        incoming = queue.Queue()

        def read():
            for line in proc.stdout:
                incoming.put(json.loads(line))
            incoming.put(None)

        reader = threading.Thread(target=read, daemon=True)
        reader.start()
        request_id = 0

        def call(method, params=None):
            global request_id
            request_id += 1
            proc.stdin.write(json.dumps({"jsonrpc": "2.0", "id": request_id,
                                         "method": method, "params": params or {}}) + "\n")
            proc.stdin.flush()
            deadline = time.monotonic() + 8
            while True:
                reply = incoming.get(timeout=max(.01, deadline - time.monotonic()))
                assert reply is not None, "serve exited"
                if reply.get("id") == request_id:
                    return reply

        def until(operation, predicate):
            deadline = time.monotonic() + 8
            while time.monotonic() < deadline:
                reply = operation()
                if predicate(reply):
                    return reply
                time.sleep(.03)
            raise AssertionError("reload did not converge")

        try:
            assert "result" in call("initialize", {"protocolVersion": "2025-06-18", "capabilities": {},
                                                   "clientInfo": {"name": "smoke", "version": "1"}})
            for name in ["a", "b"]:
                assert "result" in call("tools/call", {"name": name + ".ping"})
            assert starts("a") == starts("b") == 1
            cfg["namespaces"]["work"]["compression"] = "medium"
            cfg["servers"]["a"]["deniedTools"] = ["ping"]
            save()
            until(lambda: call("tools/list"), lambda r: any(t["name"] == "invoke_tool" for t in r.get("result", {}).get("tools", [])))
            assert "error" in call("tools/call", {"name": "invoke_tool", "arguments": {"tool": "a.ping", "input": {}}})
            assert starts("a") == starts("b") == 1, "metadata edit restarted an upstream"
            cfg["servers"]["a"]["env"]["SMOKE_REVISION"] = "2"
            cfg["servers"]["a"].pop("deniedTools")
            cfg["namespaces"]["work"].pop("compression")
            save()
            until(lambda: call("tools/list"), lambda r: any(t["name"] == "a.ping" for t in r.get("result", {}).get("tools", [])))
            until(lambda: call("tools/call", {"name": "a.ping"}), lambda r: "result" in r)
            assert "result" in call("tools/call", {"name": "b.ping"})
            assert starts("a") == 2 and starts("b") == 1, "selective restart counts incorrect"
        finally:
            proc.stdin.close()
            try:
                proc.wait(timeout=8)
            except subprocess.TimeoutExpired:
                proc.terminate()
                proc.wait(timeout=8)
            reader.join(timeout=1)
            proc.stdout.close()
print("metadata retained both upstreams; runtime edit restarted only A; diagnostics were read-only")
