#!/usr/bin/env bash
# Relay 观测聚合端到端冒烟（Phase 2 验证）
#
# 场景：
#   1. 起中心 godex（127.0.0.1:3901）
#   2. 起节点 godex 生成 node_id，注册 + 签发 ck_ 凭证
#   3. 重启节点配置 center_url + credential：agent 出站连中心，Observer 周期推送快照事件
#   4. 中心侧 curl /api/control/nodes/{id}/overview：
#      - node 视图存在（id/version）
#      - overview.node_id 正确
#      - recent_events 非空（证明快照事件已从节点推送到中心聚合存储）
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

CENTER_PORT=3911
NODE_PORT=3912
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

echo "[obs-smoke] build binary"
export PATH="/usr/local/go/bin:$PATH"
go build -o "$BIN" ./cmd/godex

echo "[obs-smoke] start center on :${CENTER_PORT}"
GODEX_HOME="$CENTER_HOME" "$BIN" serve --addr "127.0.0.1:${CENTER_PORT}" >"$WORK/center.log" 2>&1 &
CENTER_PID=$!

echo "[obs-smoke] wait for center /api/meta"
for i in $(seq 1 60); do
  if curl -sf "$CENTER_URL/api/meta" >/dev/null 2>&1; then
    break
  fi
  sleep 0.5
done
curl -sf "$CENTER_URL/api/meta" >/dev/null || { echo "center did not come up"; cat "$WORK/center.log"; exit 1; }

echo "[obs-smoke] start node once to generate node id"
GODEX_HOME="$NODE_HOME" "$BIN" serve --addr "127.0.0.1:${NODE_PORT}" >"$WORK/node-first.log" 2>&1 &
NODE_PID=$!
for i in $(seq 1 60); do
  [ -f "$NODE_HOME/state/node.json" ] && break
  sleep 0.5
done
NODE_ID="$(grep -o '"id"[^,}]*' "$NODE_HOME/state/node.json" | head -1 | sed 's/.*: *"//; s/"//')"
if [ -z "$NODE_ID" ]; then
  echo "failed to read node id"; cat "$WORK/node-first.log"; exit 1
fi
echo "[obs-smoke] node id: $NODE_ID"
kill "$NODE_PID" 2>/dev/null || true
wait "$NODE_PID" 2>/dev/null || true
NODE_PID=""

echo "[obs-smoke] register node and issue credential"
curl -sf -X POST "$CENTER_URL/api/control/nodes/register" \
  -H 'Content-Type: application/json' \
  -d "{\"id\":\"$NODE_ID\",\"name\":\"obs-node\",\"version\":\"dev\"}" >/dev/null
CRED_RESP="$(curl -sf -X POST "$CENTER_URL/api/control/nodes/${NODE_ID}/credential")"
CREDENTIAL="$(printf '%s' "$CRED_RESP" | grep -o '"credential"[^,}]*' | head -1 | sed 's/.*: *"//; s/"//')"
if [ -z "$CREDENTIAL" ]; then
  echo "failed to issue credential: $CRED_RESP"; exit 1
fi
echo "[obs-smoke] issued credential: ${CREDENTIAL:0:12}..."

echo "[obs-smoke] restart node with center_url + credential"
GODEX_HOME="$NODE_HOME" \
GODEX_CONTROL_CENTER_URL="$CENTER_URL" \
GODEX_CONTROL_CREDENTIAL="$CREDENTIAL" \
"$BIN" serve --addr "127.0.0.1:${NODE_PORT}" >"$WORK/node.log" 2>&1 &
NODE_PID=$!

echo "[obs-smoke] wait for node relay connected"
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
echo "[obs-smoke] node relay connected"

echo "[obs-smoke] wait for observation snapshot to reach center (overview recent_events)"
OVERVIEW=""
for i in $(seq 1 60); do
  OVERVIEW="$(curl -sf "$CENTER_URL/api/control/nodes/${NODE_ID}/overview" || true)"
  if printf '%s' "$OVERVIEW" | grep -q '"recent_events"'; then
    break
  fi
  sleep 0.5
done
printf '%s' "$OVERVIEW" | grep -q '"recent_events"' || {
  echo "overview never received snapshot events"
  echo "--- overview ---"; printf '%s\n' "$OVERVIEW"
  echo "--- node log ---"; cat "$WORK/node.log"
  echo "--- center log ---"; cat "$WORK/center.log"
  exit 1
}

printf '%s' "$OVERVIEW" | grep -q "\"node_id\":\"$NODE_ID\"" || {
  echo "overview node_id mismatch: $OVERVIEW"; exit 1
}
printf '%s' "$OVERVIEW" | grep -q '"kind":"snapshot"' || {
  echo "expected a snapshot-kind recent event: $OVERVIEW"; exit 1
}

echo "[obs-smoke] overview received: $(printf '%s' "$OVERVIEW" | head -c 400)..."
echo "[obs-smoke] PASS"
