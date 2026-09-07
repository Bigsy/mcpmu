#!/usr/bin/env python3
"""Exercise web config transactions and cache persistence using only temp files."""

import json
from pathlib import Path
import socket
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request


def run(binary):
    with tempfile.TemporaryDirectory(prefix="mcpmu-smoke-web-config-") as directory:
        root = Path(directory)
        config = root / "config.json"
        cache = root / "toolcache.json"
        config.write_text(json.dumps({
            "schemaVersion": 1,
            "servers": {name: {"command": "echo"} for name in ("alpha", "beta", "removed")},
            "namespaces": {},
        }))
        cache.write_text(json.dumps({
            "version": 2,
            "servers": {name: {"tools": [{"name": "ping", "tokenCount": 1}],
                               "updatedAt": "2026-01-01T00:00:00Z"}
                        for name in ("alpha", "beta", "removed")},
        }))
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            port = sock.getsockname()[1]
        base = f"http://127.0.0.1:{port}"
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))

        def request(method, path, body=None, status=200):
            data = None if body is None else json.dumps(body).encode()
            req = urllib.request.Request(base + path, data=data, method=method, headers={
                "Content-Type": "application/json", "Authorization": "Bearer smoke-config-token",
            })
            try:
                response = opener.open(req, timeout=2)
            except urllib.error.HTTPError as error:
                response = error
            with response:
                payload = response.read()
                assert response.status == status, (path, response.status, payload)
                return json.loads(payload) if payload else None

        def cache_names():
            return set(json.loads(cache.read_text())["servers"])

        with (root / "web.log").open("w+") as log:
            process = subprocess.Popen([
                binary, "--config", str(config), "web", "--addr", f"127.0.0.1:{port}",
                "--token", "smoke-config-token",
            ], stdout=log, stderr=log)
            try:
                deadline = time.monotonic() + 10
                while True:
                    assert process.poll() is None, "web exited during startup"
                    try:
                        request("GET", "/api/servers")
                        break
                    except (urllib.error.URLError, TimeoutError):
                        if time.monotonic() >= deadline:
                            raise AssertionError("web did not become ready")
                        time.sleep(0.05)

                request("PUT", "/api/servers/alpha", {"enabled": False})
                assert cache_names() == {"alpha", "beta", "removed"}
                before = config.read_bytes(), cache.read_bytes()
                request("PUT", "/api/servers/alpha", {"command": ""}, status=422)
                assert before == (config.read_bytes(), cache.read_bytes()), "invalid update saved state"
                request("PUT", "/api/servers/alpha", {"command": "other", "name": "changed"})
                assert cache_names() == {"beta", "removed"}, "command + rename retained stale tools"
                request("PUT", "/api/servers/beta", {"name": "kept"})
                assert cache_names() == {"kept", "removed"}, "rename lost tools"
                request("POST", "/api/config/import/apply", {
                    "schemaVersion": 1, "servers": {"kept": {"command": "echo"}},
                })
                assert cache_names() == {"kept"}, "import cache cleanup failed"
                assert set(json.loads(config.read_text())["servers"]) == {"kept"}
                request("POST", "/api/config/import/apply", {"servers": {}})
                assert cache_names() == set(), "empty import left cached tools"
                request("POST", "/api/servers", {"name": "after", "config": {"command": "echo"}}, status=201)
                assert "after" in json.loads(config.read_text())["servers"], "empty import left unusable maps"
                print("web updates/imports maintain config and tool cache")
            finally:
                process.terminate()
                try:
                    process.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait(timeout=5)


if __name__ == "__main__":
    run(str(Path(sys.argv[1]).resolve()))
