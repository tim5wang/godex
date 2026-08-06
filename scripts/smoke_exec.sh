#!/usr/bin/env bash
# godex node exec（远程命令）端到端冒烟
#
# 场景：
#   1. 起中心 godex（127.0.0.1:3941）
#   2. 起节点 godex 生成 node_id，注册 + 签发 ck_ 凭证
#   3. 重启节点配置 center_url + credential：agent 出站连中心
#   4. 中心侧跑 `godex node exec --node X --center CENTER 'cmd'`
#      命令经中心 proxy → 节点 /v1/exec 执行，输出 SSE 流式回传
#   5. 验证输出内容 + 非零退出码透传
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

CENTER_PORT=3941
NODE_PORT=3942
CENTER_URL="http://127.0.0.1:${CENTER_PORT}"

WORK="$ROOT_DIR/.godex/tmp/exec-smoke"
BIN="$WORK/godex"
CENTER_HOME="$WORK/center"
NODE_HOME="$WORK/node"
mkdir -p "$CENTER_HOME" "$NODE_HOME"

if [ "${1:-}" = "build" ]; then
  echo "[exec-smoke] build binary"
  export PATH="/usr/local/go/bin:$PATH"
  go build -o "$BIN" ./cmd/godex
fi
[ -x "$BIN" ] || { echo "binary missing: $BIN (run with 'build' first)"; exit 1; }

CENTER_PID=""
NODE_PID=""

cleanup() {
  [ -n "$NODE_PID" ] && kill "$NODE_PID" 2>/dev/null || true
  [ -n "$CENTER_PID" ] && kill "$CENTER_PID" 2>/dev/null || true
}
trap cleanup EXIT

pkill -f "godex serve --addr 127.0.0.1:${CENTER_PORT}" 2>/dev/null || true
pkill -f "godex serve --addr 127.0.0.1:${NODE_PORT}" 2>/dev/null || true
sleep 0.3

echo "[exec-smoke] start center on :${CENTER_PORT}"
GODEX_HOME="$CENTER_HOME" "$BIN" serve --addr "127.0.0.1:${CENTER_PORT}" >"$WORK/center.log" 2>&1 &
CENTER_PID=$!
for i in $(seq 1 40); do
  curl -sf "$CENTER_URL/api/meta" >/dev/null 2>&1 && break
  sleep 0.25
done
curl -sf "$CENTER_URL/api/meta" >/dev/null || { echo "center did not come up"; tail -5 "$WORK/center.log"; exit 1; }

echo "[exec-smoke] start node once to generate node id"
GODEX_HOME="$NODE_HOME" "$BIN" serve --addr "127.0.0.1:${NODE_PORT}" >"$WORK/node-first.log" 2>&1 &
NODE_PID=$!
for i in $(seq 1 40); do
  [ -f "$NODE_HOME/state/node.json" ] && break
  sleep 0.25
done
NODE_ID="$(grep -o '"id"[^,}]*' "$NODE_HOME/state/node.json" | head -1 | sed 's/.*: *"//; s/"//')"
[ -n "$NODE_ID" ] || { echo "failed to read node id"; tail -5 "$WORK/node-first.log"; exit 1; }
echo "[exec-smoke] node id: $NODE_ID"
kill "$NODE_PID" 2>/dev/null || true
wait "$NODE_PID" 2>/dev/null || true
NODE_PID=""

echo "[exec-smoke] register node and issue credential"
curl -sf -X POST "$CENTER_URL/api/control/nodes/register" \
  -H 'Content-Type: application/json' \
  -d "{\"id\":\"$NODE_ID\",\"name\":\"exec-node\",\"version\":\"dev\",\"trust_level\":\"trusted\"}" >/dev/null
CRED_RESP="$(curl -sf -X POST "$CENTER_URL/api/control/nodes/${NODE_ID}/credential")"
CREDENTIAL="$(printf '%s' "$CRED_RESP" | grep -o '"credential"[^,}]*' | head -1 | sed 's/.*: *"//; s/"//')"
[ -n "$CREDENTIAL" ] || { echo "failed to issue credential: $CRED_RESP"; exit 1; }

echo "[exec-smoke] restart node with center_url + credential"
GODEX_HOME="$NODE_HOME" \
GODEX_CONTROL_CENTER_URL="$CENTER_URL" \
GODEX_CONTROL_CREDENTIAL="$CREDENTIAL" \
"$BIN" serve --addr "127.0.0.1:${NODE_PORT}" >"$WORK/node.log" 2>&1 &
NODE_PID=$!

echo "[exec-smoke] wait for node relay connected"
CONNECTED=""
for i in $(seq 1 40); do
  NODES="$(curl -sf "$CENTER_URL/api/control/nodes" || true)"
  if printf '%s' "$NODES" | grep -q "\"id\":\"$NODE_ID\"" && printf '%s' "$NODES" | grep -q '"relay_status":"connected"'; then
    CONNECTED=1
    break
  fi
  sleep 0.25
done
if [ -z "$CONNECTED" ]; then
  echo "node never became connected"
  echo "--- node log ---"; tail -8 "$WORK/node.log"
  echo "--- center log ---"; tail -8 "$WORK/center.log"
  exit 1
fi
echo "[exec-smoke] node relay connected"

echo "[exec-smoke] 1/3 node exec streams multi-line output"
OUT="$(GODEX_HOME="$CENTER_HOME" "$BIN" node exec \
  --node "$NODE_ID" --center "$CENTER_URL" \
  'echo hello-remote && sleep 0.3 && echo line2' 2>"$WORK/exec1.err")"
echo "  output: $(printf '%s' "$OUT" | tr '\n' '|')"
printf '%s' "$OUT" | grep -q "hello-remote" || { echo "missing hello-remote"; cat "$WORK/exec1.err"; exit 1; }
printf '%s' "$OUT" | grep -q "line2" || { echo "missing line2"; cat "$WORK/exec1.err"; exit 1; }
echo "  ok (streamed 2 lines)"

echo "[exec-smoke] 2/3 node exec surfaces non-zero exit code"
if GODEX_HOME="$CENTER_HOME" "$BIN" node exec \
  --node "$NODE_ID" --center "$CENTER_URL" \
  'exit 7' >"$WORK/exec2.out" 2>&1; then
  echo "expected failure for exit 7"; cat "$WORK/exec2.out"; exit 1
fi
grep -q "exit code 7\|code 7" "$WORK/exec2.out" || { echo "missing exit code hint: $(cat "$WORK/exec2.out")"; exit 1; }
echo "  ok (exit code 7 propagated)"

echo "[exec-smoke] 3/3 exec endpoint via center proxy (curl)"
CURL_OUT="$(curl -sf -N -X POST "$CENTER_URL/api/control/nodes/${NODE_ID}/proxy/v1/exec" \
  -H 'Content-Type: application/json' \
  -d '{"command":"echo via-curl"}' || true)"
printf '%s' "$CURL_OUT" | grep -q "via-curl" || { echo "missing via-curl: $CURL_OUT"; exit 1; }
echo "  ok (SSE via proxy)"

echo "[exec-smoke] PASS"
