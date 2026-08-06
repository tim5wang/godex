#!/usr/bin/env bash
# 节点接入产品化（join）端到端冒烟
#
# 场景（模拟 Web UI「接入新节点」→ 内网节点一键粘贴）：
#   1. 起中心 godex（127.0.0.1:3921）
#   2. 中心侧注册节点 + 签发 ck_ 凭证（即 Web UI JoinNodeCard 编排的两步）
#   3. 内网节点上执行 `godex node join <center_url> --id my-laptop --credential ck_... --trust trusted`
#      → 写 home godex.yaml（control.center_url / node_id / trust_level）+ home .env（credential）
#   4. 重启节点 serve：按指定 node_id 注册并连上中心 relay
#   5. 中心侧 `godex node exec --node my-laptop 'echo smoke-join-ok'` 用指定 id 命中
#   6. 中心侧不传 --node（配置 default_node）也能命中
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

CENTER_PORT=3921
NODE_PORT=3922
CENTER_URL="http://127.0.0.1:${CENTER_PORT}"

WORK="$(mktemp -d)"
BIN="$WORK/godex"
CENTER_HOME="$WORK/center"
NODE_HOME="$WORK/node"
mkdir -p "$CENTER_HOME" "$NODE_HOME"

CENTER_PID=""
NODE_PID=""

cleanup() {
  [ -n "$NODE_PID" ] && kill "$NODE_PID" 2>/dev/null || true
  [ -n "$CENTER_PID" ] && kill "$CENTER_PID" 2>/dev/null || true
  wait "$NODE_PID" 2>/dev/null || true
  wait "$CENTER_PID" 2>/dev/null || true
  rm -rf "$WORK"
}
trap cleanup EXIT

echo "[join-smoke] build binary"
export PATH="/usr/local/go/bin:$PATH"
go build -o "$BIN" ./cmd/godex

echo "[join-smoke] start center on :${CENTER_PORT}"
GODEX_HOME="$CENTER_HOME" "$BIN" serve --addr "127.0.0.1:${CENTER_PORT}" >"$WORK/center.log" 2>&1 &
CENTER_PID=$!

echo "[join-smoke] wait for center /api/meta"
for i in $(seq 1 60); do
  if curl -sf "$CENTER_URL/api/meta" >/dev/null 2>&1; then
    break
  fi
  sleep 0.5
done
curl -sf "$CENTER_URL/api/meta" >/dev/null || { echo "center did not come up"; cat "$WORK/center.log"; exit 1; }

NODE_ID="my-laptop"

echo "[join-smoke] 1/6 register node + issue credential (Web UI flow)"
curl -sf -X POST "$CENTER_URL/api/control/nodes/register" \
  -H 'Content-Type: application/json' \
  -d "{\"id\":\"$NODE_ID\",\"name\":\"smoke-node\",\"version\":\"dev\",\"trust_level\":\"trusted\"}" >/dev/null
CRED_RESP="$(curl -sf -X POST "$CENTER_URL/api/control/nodes/${NODE_ID}/credential")"
CREDENTIAL="$(printf '%s' "$CRED_RESP" | grep -o '"credential"[^,}]*' | head -1 | sed 's/.*: *"//; s/"//')"
if [ -z "$CREDENTIAL" ]; then
  echo "failed to issue credential: $CRED_RESP"; exit 1
fi
echo "  ok (credential ${CREDENTIAL:0:12}...)"

echo "[join-smoke] 2/6 run 'godex node join' on the intranet node"
GODEX_HOME="$NODE_HOME" "$BIN" node join "$CENTER_URL" \
  --id "$NODE_ID" --credential "$CREDENTIAL" --trust trusted --name "smoke-node" >"$WORK/join.log" 2>&1
cat "$WORK/join.log"

echo "[join-smoke] 3/6 verify config written to node home"
grep -q "center_url: $CENTER_URL" "$NODE_HOME/godex.yaml" || { echo "center_url missing in godex.yaml"; cat "$NODE_HOME/godex.yaml"; exit 1; }
grep -q "node_id: $NODE_ID" "$NODE_HOME/godex.yaml" || { echo "node_id missing in godex.yaml"; exit 1; }
grep -q "trust_level: trusted" "$NODE_HOME/godex.yaml" || { echo "trust_level missing in godex.yaml"; exit 1; }
grep -q "GODEX_CONTROL_CREDENTIAL=$CREDENTIAL" "$NODE_HOME/.env" || { echo "credential missing in .env"; cat "$NODE_HOME/.env"; exit 1; }
echo "  ok"

echo "[join-smoke] 4/6 restart node, wait for relay connected under specified id"
GODEX_HOME="$NODE_HOME" "$BIN" serve --addr "127.0.0.1:${NODE_PORT}" >"$WORK/node.log" 2>&1 &
NODE_PID=$!
CONNECTED=""
for i in $(seq 1 60); do
  NODES="$(curl -sf "$CENTER_URL/api/control/nodes" || true)"
  if printf '%s' "$NODES" | grep -q "\"id\":\"$NODE_ID\"" && printf '%s' "$NODES" | grep -q '"relay_status":"connected"'; then
    CONNECTED=1
    break
  fi
  sleep 0.5
done
if [ -z "$CONNECTED" ]; then
  echo "node never became connected"
  echo "--- node log ---"; cat "$WORK/node.log"
  echo "--- center log ---"; cat "$WORK/center.log"
  exit 1
fi
echo "  ok"

echo "[join-smoke] 5/6 center-side exec with explicit --node"
OUT="$(GODEX_HOME="$CENTER_HOME" "$BIN" node exec --center "$CENTER_URL" --node "$NODE_ID" 'echo smoke-join-ok' 2>&1)"
printf '%s' "$OUT" | grep -q "smoke-join-ok" || { echo "expected smoke-join-ok in exec output: $OUT"; exit 1; }
echo "  ok"

echo "[join-smoke] 6/6 center-side exec without --node uses default_node"
GODEX_HOME="$CENTER_HOME" "$BIN" config set control.default_node "$NODE_ID" >/dev/null 2>&1 || true
# Fall back to env override if the config CLI is unavailable.
OUT2="$(GODEX_HOME="$CENTER_HOME" GODEX_CONTROL_DEFAULT_NODE="$NODE_ID" "$BIN" node exec --center "$CENTER_URL" 'echo smoke-default-ok' 2>&1)"
printf '%s' "$OUT2" | grep -q "smoke-default-ok" || { echo "expected smoke-default-ok in default exec output: $OUT2"; exit 1; }
echo "  ok"

echo "[join-smoke] 7/7 delete node: removed, relay dropped, heartbeat rejected"
DEL_STATUS="$(curl -s -o /dev/null -w '%{http_code}' -X DELETE "$CENTER_URL/api/control/nodes/$NODE_ID")"
if [ "$DEL_STATUS" != "200" ]; then
  echo "expected 200 on delete, got $DEL_STATUS"; exit 1
fi
NODES_AFTER="$(curl -sf "$CENTER_URL/api/control/nodes" || true)"
if printf '%s' "$NODES_AFTER" | grep -q "\"id\":\"$NODE_ID\""; then
  echo "node still listed after delete: $NODES_AFTER"; exit 1
fi
HB_STATUS="$(curl -s -o /dev/null -w '%{http_code}' -X POST "$CENTER_URL/api/control/nodes/$NODE_ID/heartbeat" -H 'Content-Type: application/json' -d '{"version":"dev"}')"
if [ "$HB_STATUS" = "200" ]; then
  echo "expected heartbeat of deleted node to be rejected, got 200"; exit 1
fi
# The node process is still running and keeps trying to reconnect; it must not
# resurrect itself. Wait briefly and confirm it stays gone.
sleep 3
NODES_LATER="$(curl -sf "$CENTER_URL/api/control/nodes" || true)"
if printf '%s' "$NODES_LATER" | grep -q "\"id\":\"$NODE_ID\""; then
  echo "deleted node resurrected via reconnect: $NODES_LATER"; exit 1
fi
echo "  ok"

echo "[join-smoke] PASS"
