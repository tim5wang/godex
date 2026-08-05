#!/usr/bin/env bash
# 远程控制（Phase 3）端到端冒烟
#
# 场景：
#   1. 起中心 godex（127.0.0.1:3921）
#   2. 起节点 godex 生成 node_id，注册 + 签发 ck_ 凭证
#   3. 重启节点配置 center_url + credential：agent 出站连中心
#   4. 中心侧通过代理端点操作节点：
#      - GET  /proxy/meta                        → 节点版本
#      - GET  /proxy/files/list                  → 节点工作区文件
#      - POST /proxy/v1/terminal/create          → 节点上创建 PTY 终端
#      - GET  /proxy/sessions                    → 节点会话列表
#   5. 验证 guarded-remote 信任级别拦截写操作（未带审批头 → 403）
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

echo "[remote-smoke] build binary"
export PATH="/usr/local/go/bin:$PATH"
go build -o "$BIN" ./cmd/godex

echo "[remote-smoke] start center on :${CENTER_PORT}"
GODEX_HOME="$CENTER_HOME" "$BIN" serve --addr "127.0.0.1:${CENTER_PORT}" >"$WORK/center.log" 2>&1 &
CENTER_PID=$!

echo "[remote-smoke] wait for center /api/meta"
for i in $(seq 1 60); do
  if curl -sf "$CENTER_URL/api/meta" >/dev/null 2>&1; then
    break
  fi
  sleep 0.5
done
curl -sf "$CENTER_URL/api/meta" >/dev/null || { echo "center did not come up"; cat "$WORK/center.log"; exit 1; }

echo "[remote-smoke] start node once to generate node id"
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
echo "[remote-smoke] node id: $NODE_ID"
kill "$NODE_PID" 2>/dev/null || true
wait "$NODE_PID" 2>/dev/null || true
NODE_PID=""

echo "[remote-smoke] register node and issue credential"
curl -sf -X POST "$CENTER_URL/api/control/nodes/register" \
  -H 'Content-Type: application/json' \
  -d "{\"id\":\"$NODE_ID\",\"name\":\"remote-node\",\"version\":\"dev\",\"trust_level\":\"trusted\"}" >/dev/null
CRED_RESP="$(curl -sf -X POST "$CENTER_URL/api/control/nodes/${NODE_ID}/credential")"
CREDENTIAL="$(printf '%s' "$CRED_RESP" | grep -o '"credential"[^,}]*' | head -1 | sed 's/.*: *"//; s/"//')"
if [ -z "$CREDENTIAL" ]; then
  echo "failed to issue credential: $CRED_RESP"; exit 1
fi

echo "[remote-smoke] restart node with center_url + credential"
GODEX_HOME="$NODE_HOME" \
GODEX_CONTROL_CENTER_URL="$CENTER_URL" \
GODEX_CONTROL_CREDENTIAL="$CREDENTIAL" \
"$BIN" serve --addr "127.0.0.1:${NODE_PORT}" >"$WORK/node.log" 2>&1 &
NODE_PID=$!

echo "[remote-smoke] wait for node relay connected"
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
echo "[remote-smoke] node relay connected"

PROXY="$CENTER_URL/api/control/nodes/${NODE_ID}/proxy"

echo "[remote-smoke] 1/5 proxy -> node /meta"
META="$(curl -sf "$PROXY/meta")"
printf '%s' "$META" | grep -q '"version"' || { echo "expected version in meta: $META"; exit 1; }
echo "  ok"

echo "[remote-smoke] 2/5 proxy -> node /files/list"
FILES="$(curl -sf "$PROXY/files/list?path=.&max_depth=1")"
printf '%s' "$FILES" | grep -q '"items"' || { echo "expected items in files list: $FILES"; exit 1; }
echo "  ok"

echo "[remote-smoke] 3/5 proxy -> node /v1/terminal/create"
TERM_RESP="$(curl -s --max-time 10 -w '\nHTTP_STATUS:%{http_code}' -X POST "$PROXY/v1/terminal/create" -H "Content-Type: application/json" -d "{\"workspace_dir\":\"$NODE_HOME\"}")"
echo "  raw response: $TERM_RESP"
TERM_ID="$(printf '%s' "$TERM_RESP" | grep -o '"terminalId"[^,}]*' | head -1 | sed 's/.*: *"//; s/"//')"
if [ -z "$TERM_ID" ]; then
  echo "terminal create failed: $TERM_RESP"; exit 1
fi
echo "  ok (terminal $TERM_ID)"

echo "[remote-smoke] 4/5 proxy -> node /sessions (read-only list)"
SESSIONS="$(curl -sf "$PROXY/sessions")"
echo "  ok"

echo "[remote-smoke] 5/5 guarded-remote blocks writes unless approved"
# Flip the node to guarded-remote via heartbeat, then verify the proxy gates
# mutating requests. The node keeps its relay connection (heartbeat is a
# registry-level update).
curl -sf -X POST "$CENTER_URL/api/control/nodes/${NODE_ID}/heartbeat" \
  -H 'Content-Type: application/json' \
  -d "{\"trust_level\":\"guarded-remote\"}" >/dev/null
sleep 1

STATUS="$(curl -s -o /dev/null -w '%{http_code}' -X POST "$PROXY/sessions" -H 'Content-Type: application/json' -d '{"prompt":"hi"}')"
if [ "$STATUS" != "403" ]; then
  echo "expected 403 for guarded-remote write, got $STATUS"; exit 1
fi
echo "  ok (403 without approval)"

APPROVED_STATUS="$(curl -s -o /dev/null -w '%{http_code}' -X POST "$PROXY/sessions" -H 'Content-Type: application/json' -H 'X-Godex-Trust-Approved: 1' -d '{"prompt":"hi"}')"
if [ "$APPROVED_STATUS" != "200" ]; then
  echo "expected 200 for approved write, got $APPROVED_STATUS"; exit 1
fi
echo "  ok (200 with approval header)"

echo "[remote-smoke] PASS"
