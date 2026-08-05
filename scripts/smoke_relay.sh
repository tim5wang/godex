#!/usr/bin/env bash
# Relay 传输层端到端冒烟（Phase 1 验证）
#
# 场景：
#   1. 起中心 godex（模拟公网中心，127.0.0.1:3901）
#   2. 起节点 godex 一次，生成持久化 node_id
#   3. 在中心注册该节点并签发 ck_ 凭证
#   4. 重启节点，配置 center_url + credential，agent 出站连中心
#   5. 中心侧 curl 代理端点 /control/nodes/{id}/proxy/meta，应返回节点 /meta 响应
#   6. 验证节点在中心 registry 中 relay_status=connected
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

CENTER_PORT=3901
NODE_PORT=3902
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

echo "[relay-smoke] build binary"
export PATH="/usr/local/go/bin:$PATH"
go build -o "$BIN" ./cmd/godex

echo "[relay-smoke] start center on :${CENTER_PORT}"
GODEX_HOME="$CENTER_HOME" "$BIN" serve --addr "127.0.0.1:${CENTER_PORT}" >"$WORK/center.log" 2>&1 &
CENTER_PID=$!

echo "[relay-smoke] wait for center /api/meta"
for i in $(seq 1 60); do
  if curl -sf "$CENTER_URL/api/meta" >/dev/null 2>&1; then
    break
  fi
  sleep 0.5
done
curl -sf "$CENTER_URL/api/meta" >/dev/null || { echo "center did not come up"; cat "$WORK/center.log"; exit 1; }

echo "[relay-smoke] start node once to generate node id (no credential yet)"
GODEX_HOME="$NODE_HOME" "$BIN" serve --addr "127.0.0.1:${NODE_PORT}" >"$WORK/node-first.log" 2>&1 &
NODE_PID=$!
for i in $(seq 1 60); do
  [ -f "$NODE_HOME/state/node.json" ] && break
  sleep 0.5
done
NODE_ID="$(grep -o '"id"[^,}]*' "$NODE_HOME/state/node.json" | head -1 | sed 's/.*: *"//; s/"//')"
if [ -z "$NODE_ID" ]; then
  echo "failed to read node id from $NODE_HOME/state/node.json"
  cat "$WORK/node-first.log"
  exit 1
fi
echo "[relay-smoke] node id: $NODE_ID"
kill "$NODE_PID" 2>/dev/null || true
wait "$NODE_PID" 2>/dev/null || true
NODE_PID=""

echo "[relay-smoke] register node at center and issue credential"
curl -sf -X POST "$CENTER_URL/api/control/nodes/register" \
  -H 'Content-Type: application/json' \
  -d "{\"id\":\"$NODE_ID\",\"name\":\"smoke-node\",\"version\":\"dev\"}" >/dev/null
CRED_RESP="$(curl -sf -X POST "$CENTER_URL/api/control/nodes/${NODE_ID}/credential")"
CREDENTIAL="$(printf '%s' "$CRED_RESP" | grep -o '"credential"[^,}]*' | head -1 | sed 's/.*: *"//; s/"//')"
if [ -z "$CREDENTIAL" ]; then
  echo "failed to issue credential: $CRED_RESP"
  exit 1
fi
echo "[relay-smoke] issued credential: ${CREDENTIAL:0:12}..."

echo "[relay-smoke] restart node with center_url + credential"
GODEX_HOME="$NODE_HOME" \
GODEX_CONTROL_CENTER_URL="$CENTER_URL" \
GODEX_CONTROL_CREDENTIAL="$CREDENTIAL" \
"$BIN" serve --addr "127.0.0.1:${NODE_PORT}" >"$WORK/node.log" 2>&1 &
NODE_PID=$!

echo "[relay-smoke] wait for node online via registry (relay_status=connected)"
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
echo "[relay-smoke] node relay connected"

echo "[relay-smoke] curl center proxy endpoint -> node /meta"
META="$(curl -sf "$CENTER_URL/api/control/nodes/${NODE_ID}/proxy/meta")"
echo "[relay-smoke] node meta: $META"
printf '%s' "$META" | grep -q '"version"' || { echo "expected version in node meta"; exit 1; }

echo "[relay-smoke] PASS"
