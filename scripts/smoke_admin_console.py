#!/usr/bin/env python3
"""End-to-end smoke for the superadmin console surfaces (docs/admin-console-guide.md).

Every command and status code the manual documents is exercised here against a
REAL center instance started from the current source. The manual's examples are
therefore backed by something re-runnable rather than by a one-off manual test:

    python3 scripts/smoke_admin_console.py

It builds the binary, starts a center on a free port with a throwaway GODEX_HOME,
runs the whole flow (registration, tenant administration, join codes, the node
allowance, visibility, operating-access grants, metering reconciliation, and the
authorization boundaries), prints each result, and always tears the server down.

Exit code 0 means every call behaved as documented.

Why a separate scenario script rather than an entry in `scripts/smoke.sh`: that
wrapper discovers `smoke_*.sh` only, and this check is naturally HTTP-shaped
(python's urllib is used rather than curl so it runs in minimal environments too).
`scripts/` already holds standalone python helpers, so the shape is not novel.
"""

import json
import os
import shutil
import signal
import socket
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request

REPO = os.path.abspath(os.path.join(os.path.dirname(__file__), ".."))
WEB_TOKEN = "smoke-admin-console-token"

failures = []


def free_port() -> int:
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


class Center:
    def __init__(self, port: int, home: str, binary: str):
        self.port = port
        self.base = f"http://127.0.0.1:{port}/api"
        self.home = home
        self.binary = binary
        self.proc = None
        self.log_path = os.path.join(home, "serve.log")

    def start(self) -> bool:
        env = dict(os.environ, GODEX_HOME=self.home, GODEX_WEB_TOKEN=WEB_TOKEN)
        self.log = open(self.log_path, "w")
        self.proc = subprocess.Popen(
            [self.binary, "serve", "--addr", f"127.0.0.1:{self.port}"],
            env=env, stdout=self.log, stderr=subprocess.STDOUT, cwd=REPO,
        )
        for _ in range(60):
            if self.proc.poll() is not None:
                return False
            try:
                with urllib.request.urlopen(self.base + "/meta", timeout=2) as resp:
                    if resp.status == 200:
                        return True
            except Exception:
                time.sleep(0.5)
        return False

    def stop(self):
        if self.proc is None:
            return
        self.proc.send_signal(signal.SIGTERM)
        try:
            self.proc.wait(timeout=10)
        except subprocess.TimeoutExpired:
            self.proc.kill()
        self.log.close()

    def call(self, method, path, body=None, token=None, expect=None, note=""):
        data = json.dumps(body).encode() if body is not None else None
        req = urllib.request.Request(self.base + path, data=data, method=method)
        if data is not None:
            req.add_header("Content-Type", "application/json")
        if token:
            req.add_header("Authorization", "Bearer " + token)
        try:
            with urllib.request.urlopen(req, timeout=20) as resp:
                raw, status = resp.read().decode(), resp.status
        except urllib.error.HTTPError as err:
            raw, status = err.read().decode(), err.code
        try:
            parsed = json.loads(raw) if raw.strip() else {}
        except json.JSONDecodeError:
            parsed = {"_raw": raw[:200]}
        ok = expect is None or status == expect
        if not ok:
            failures.append(f"{method} {path} -> {status}, want {expect}: {raw[:200]}")
        print(f"{'OK ' if ok else '!! '}{method:6} {path:52} {status:3}  "
              f"{json.dumps(parsed, ensure_ascii=False)[:110]}")
        if note:
            print(f"       └─ {note}")
        return parsed


def main() -> int:
    work = tempfile.mkdtemp(prefix="godex-admin-smoke-")
    try:
        binary = os.path.join(work, "godex")
        print("[admin-console-smoke] building godex")
        build_env = dict(os.environ)
        build_env["PATH"] = "/usr/local/go/bin:" + build_env.get("PATH", "")
        if subprocess.run(["go", "build", "-o", binary, "./cmd/godex"],
                          cwd=REPO, env=build_env).returncode != 0:
            print("build failed", file=sys.stderr)
            return 1

        home = os.path.join(work, "center")
        os.makedirs(home, exist_ok=True)
        center = Center(free_port(), home, binary)
        if not center.start():
            print("center did not become ready; log follows:", file=sys.stderr)
            print(open(center.log_path).read()[-3000:], file=sys.stderr)
            return 1
        try:
            run(center)
        finally:
            center.stop()
    finally:
        shutil.rmtree(work, ignore_errors=True)

    print("=" * 74)
    if failures:
        print(f"{len(failures)} EXPECTATION(S) NOT MET:")
        for item in failures:
            print("  - " + item)
        return 1
    print("ALL CALLS BEHAVED AS docs/admin-console-guide.md DESCRIBES")
    return 0


def run(center: Center) -> None:
    # --- 1. Self-service signup: one tenant per signup, caller becomes owner.
    a = center.call("POST", "/auth/register",
                    {"email": "a@example.com", "password": "correct-horse", "tenant_name": "Acme"},
                    expect=201)
    center.call("POST", "/auth/register",
                {"email": "b@example.com", "password": "correct-horse", "tenant_name": "Globex"},
                expect=201)
    tenant_a, user_a, session_a = a["tenant"]["id"], a["principal"]["user_id"], a["token"]

    # --- 2. Email+password login, and the two principal shapes.
    center.call("POST", "/auth/login", {"email": "a@example.com", "password": "correct-horse"}, expect=200)
    center.call("GET", "/auth/me", token=session_a, expect=200,
                note="tenant member: relay_only, no platform role")
    center.call("GET", "/auth/me", token=WEB_TOKEN, expect=200,
                note="web token holder IS the platform admin (local-operator)")

    # --- 3. Tenant administration.
    center.call("GET", "/admin/tenants", token=WEB_TOKEN, expect=200)
    center.call("GET", "/admin/policy", token=WEB_TOKEN, expect=200)
    center.call("PUT", f"/admin/tenants/{tenant_a}/sharing", {"enabled": False}, token=WEB_TOKEN, expect=200)
    center.call("PUT", f"/admin/tenants/{tenant_a}/sharing", {"enabled": True}, token=WEB_TOKEN, expect=200)
    center.call("PUT", f"/admin/tenants/{tenant_a}/max-nodes", {"max_nodes": 1}, token=WEB_TOKEN, expect=200)
    center.call("PUT", f"/admin/tenants/{tenant_a}/max-nodes", {"max_nodes": 2}, token=WEB_TOKEN, expect=200)
    listed = center.call("GET", "/admin/tenants", token=WEB_TOKEN, expect=200)
    row = next((t for t in listed["tenants"] if t["id"] == tenant_a), {})
    if row.get("max_nodes") != 2:
        failures.append(f"tenant list max_nodes = {row.get('max_nodes')}, want 2")
    if "node_count" not in row:
        failures.append("tenant list is missing node_count")
    center.call("GET", f"/admin/tenants/{tenant_a}/members", token=WEB_TOKEN, expect=200)

    # --- 4. Join codes: two enrolments allowed (allowance = 2), the third refused.
    tok1 = center.call("POST", f"/admin/tenants/{tenant_a}/join-tokens",
                       {"label": "车间笔记本", "ttl_hours": 24}, token=WEB_TOKEN, expect=201)
    first = center.call("POST", "/control/join",
                        {"token": tok1["token"], "node_id": "node-a1", "name": "车间笔记本"},
                        expect=201, note="ownership comes from the CODE, not the body")
    for key in ("credential", "node_proxy_token", "tenant_id", "owner_user_id"):
        if key not in first:
            failures.append(f"join response missing {key}")
    if first.get("visibility") != "private":
        failures.append(f"new node visibility = {first.get('visibility')}, want private (the safe default)")

    tok2 = center.call("POST", f"/admin/tenants/{tenant_a}/join-tokens",
                       {"label": "备用机", "ttl_hours": 24}, token=WEB_TOKEN, expect=201)
    center.call("POST", "/control/join", {"token": tok2["token"], "node_id": "node-a2"}, expect=201)

    # A one-time code cannot be reused (the second attempt is refused even though
    # the allowance is now full, so this is also the quota path).
    center.call("POST", "/control/join", {"token": tok1["token"], "node_id": "node-a9"}, expect=401)

    tok3 = center.call("POST", f"/admin/tenants/{tenant_a}/join-tokens",
                       {"label": "第三台", "ttl_hours": 24}, token=WEB_TOKEN, expect=201)
    center.call("POST", "/control/join", {"token": tok3["token"], "node_id": "node-a3"},
                expect=409, note="allowance reached: 2 of 2")
    # The anti-phantom property: the refused node was never registered at all.
    # A token is required because this endpoint is behind nodeProxyRead — without one
    # the answer is 401, which would say nothing about the node.
    center.call("GET", "/control/nodes/node-a3", token=WEB_TOKEN, expect=404,
                note="a refused enrolment leaves NOTHING behind")

    # --- 5. Visibility: the owner may flip their own node.
    center.call("GET", "/control/nodes", token=session_a, expect=200)
    center.call("POST", "/control/nodes/node-a1/visibility", {"visibility": "tenant"},
                token=session_a, expect=200)

    # --- 6. Admin repair path is NOT blocked by the allowance.
    nodes = center.call("GET", "/admin/nodes", token=WEB_TOKEN, expect=200)
    center.call("POST", "/admin/nodes/node-a3/claim",
                {"tenant_id": tenant_a, "owner_user_id": user_a, "visibility": "private"},
                token=WEB_TOKEN, expect=404, note="nonexistent node (see above)")
    unowned = next((n["id"] for n in nodes.get("nodes", []) if not n.get("tenant_id")), "")
    if unowned:
        center.call("POST", f"/admin/nodes/{unowned}/claim",
                    {"tenant_id": tenant_a, "owner_user_id": user_a, "visibility": "private"},
                    token=WEB_TOKEN, expect=200,
                    note="succeeds even though the tenant is at 2/2: repairs are not quota-gated")

    # --- 7. Operating-access grants: tenant-side, time-bounded, audited.
    center.call("POST", "/tenant/delegations",
                {"operator_user_id": "local-operator", "scope": "read_nodes",
                 "reason": "排查节点离线", "ttl_hours": 2}, token=session_a, expect=201)
    grants = center.call("GET", "/tenant/delegations", token=session_a, expect=200)
    if grants.get("max_ttl_hours") != 24:
        failures.append(f"max_ttl_hours = {grants.get('max_ttl_hours')}, want 24")
    center.call("GET", "/tenant/delegations/audit", token=session_a, expect=200)
    center.call("GET", "/delegations/mine", token=WEB_TOKEN, expect=200,
                note="operator side: only the operator's OWN grants")
    center.call("GET", "/tenant/consent", token=session_a, expect=200,
                note="signup already left a record")
    center.call("POST", "/tenant/consent", {"accepted": True}, token=session_a, expect=201)

    # --- 8. Relay metering: tenant view and operator reconciliation.
    center.call("GET", "/usage/relay?hours=24", token=session_a, expect=200)
    center.call("GET", "/admin/usage/relay?hours=24", token=WEB_TOKEN, expect=200)

    # --- 9. Boundaries the manual calls out explicitly.
    center.call("GET", "/admin/tenants", token=session_a, expect=403,
                note="a tenant is not an operator")
    center.call("GET", "/usage/relay", token=WEB_TOKEN, expect=200,
                note="operator holds no tenant -> unscoped bucket, not 400")
    center.call("GET", "/usage/relay?hours=abc", token=session_a, expect=400,
                note="an unusable window is refused, never silently replaced")
    center.call("PUT", "/admin/tenants/x/max-nodes", {}, token=WEB_TOKEN, expect=400,
                note="0 means unlimited, so an omitted field must not default to it")
    center.call("PUT", "/admin/tenants/x/max-nodes", {"max_nodes": -1}, token=WEB_TOKEN, expect=400)
    center.call("PUT", "/admin/tenants/x/sharing", {}, token=WEB_TOKEN, expect=400)


if __name__ == "__main__":
    sys.exit(main())
