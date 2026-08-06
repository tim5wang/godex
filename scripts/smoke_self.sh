#!/usr/bin/env bash
# self node 本地直连冒烟：验证中心服务器自身节点
#   1. relay_status = connected（网页端远程操作按钮可用）
#   2. /control/nodes/{self}/proxy/... 走本地直连（不走 relay，无需额外节点）
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

CENTER_PORT=3921
CENTER_URL="http://127.0.0.1:${CENTER_PORT}"

WORK="$(mktemp -d)"
BIN="$WORK/godex"
CENTER_HOME="$WORK/center"
mkdir -p "$CENTER_HOME"

CENTER_PID=""
cleanup() {
  [ -n "$CENTER_PID" ] && kill "$CENTER_PID" 2>/dev/null || true
  wait "$CENTER_PID" 2>/dev/null || true
  rm -rf "$WORK"
}
trap cleanup EXIT

echo "[self-smoke] build binary"
export PATH="/usr/local/go/bin:$PATH"
go build -o "$BIN" ./cmd/godex

echo "[self-smoke] start center on :${CENTER_PORT}"
GODEX_HOME="$CENTER_HOME" "$BIN" serve --addr "127.0.0.1:${CENTER_PORT}" >"$WORK/center.log" 2>&1 &
CENTER_PID=$!

echo "[self-smoke] wait for center /api/meta"
for i in $(seq 1 60); do
  if curl -sf "$CENTER_URL/api/meta" >/dev/null 2>&1; then
    break
  fi
  sleep 0.5
done
curl -sf "$CENTER_URL/api/meta" >/dev/null || { echo "center did not come up"; cat "$WORK/center.log"; exit 1; }

SELF_ID="$(grep -o '"id"[^,}]*' "$CENTER_HOME/state/node.json" 2>/dev/null | head -1 | sed 's/.*: *"//; s/"//')"
if [ -z "$SELF_ID" ]; then
  echo "failed to read self node id from state/node.json"
  echo "--- center log ---"; cat "$WORK/center.log"
  exit 1
fi
echo "[self-smoke] self node id: $SELF_ID"

echo "[self-smoke] 1/3 self node relay_status is connected"
NODES="$(curl -sf "$CENTER_URL/api/control/nodes")"
printf '%s' "$NODES" | grep -q "\"id\":\"$SELF_ID\"" || { echo "self node missing from list: $NODES"; exit 1; }
printf '%s' "$NODES" | grep -q '"relay_status":"connected"' || { echo "self node relay_status not connected: $NODES"; exit 1; }
echo "  ok"

echo "[self-smoke] 2/3 local-direct proxy serves /meta"
PROXY="$CENTER_URL/api/control/nodes/${SELF_ID}/proxy"
META="$(curl -sf "$PROXY/meta")"
printf '%s' "$META" | grep -q '"version"' || { echo "expected version in local-direct meta: $META"; exit 1; }
echo "  ok"

echo "[self-smoke] 3/3 local-direct proxy serves files list"
FILES="$(curl -sf "$PROXY/files/list?path=.&max_depth=1")"
printf '%s' "$FILES" | grep -q '"items"' || { echo "expected items in local-direct files: $FILES"; exit 1; }
echo "  ok"

echo "[self-smoke] PASS"
