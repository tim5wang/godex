#!/usr/bin/env bash
# 远程 TCP 端口转发（Phase 4）端到端冒烟
#
# 场景（等价 ssh -L 跳板）：
#   1. 起中心 godex（127.0.0.1:3931）
#   2. 起节点 godex 生成 node_id，注册 + 签发 ck_ 凭证
#   3. 重启节点配置 center_url + credential + forward_allow 白名单
#   4. 节点侧起一个"内网"TCP echo 服务（模拟内网数据库/服务）
#   5. 中心侧跑 `godex node forward --node X --local 39306 --target 127.0.0.1:39399`
#      本地连接 39306 → 数据经中心 relay → 节点拨号 → 内网服务往返
#   6. 验证 forward_allow 白名单：未允许的 target 连接被拒绝
#
# 用法: smoke_forward.sh [build]  — 传 build 则先编译二进制，否则用 .godex/tmp/fwd-smoke/godex
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

CENTER_PORT=3931
NODE_PORT=3932
ECHO_PORT=39399
LOCAL_PORT=39306
DENY_PORT=39307
CENTER_URL="http://127.0.0.1:${CENTER_PORT}"

WORK="$ROOT_DIR/.godex/tmp/fwd-smoke"
BIN="$WORK/godex"
CENTER_HOME="$WORK/center"
NODE_HOME="$WORK/node"
mkdir -p "$CENTER_HOME" "$NODE_HOME"

if [ "${1:-}" = "build" ]; then
  echo "[fwd-smoke] build binary"
  export PATH="/usr/local/go/bin:$PATH"
  go build -o "$BIN" ./cmd/godex
fi
[ -x "$BIN" ] || { echo "binary missing: $BIN (run with 'build' first)"; exit 1; }

CENTER_PID=""
NODE_PID=""
ECHO_PID=""
FWD_PID=""
DENY_FWD_PID=""

cleanup() {
  [ -n "$DENY_FWD_PID" ] && kill "$DENY_FWD_PID" 2>/dev/null || true
  [ -n "$FWD_PID" ] && kill "$FWD_PID" 2>/dev/null || true
  [ -n "$ECHO_PID" ] && kill "$ECHO_PID" 2>/dev/null || true
  [ -n "$NODE_PID" ] && kill "$NODE_PID" 2>/dev/null || true
  [ -n "$CENTER_PID" ] && kill "$CENTER_PID" 2>/dev/null || true
}
trap cleanup EXIT

pkill -f "godex serve --addr 127.0.0.1:${CENTER_PORT}" 2>/dev/null || true
pkill -f "godex serve --addr 127.0.0.1:${NODE_PORT}" 2>/dev/null || true
pkill -f "godex node forward" 2>/dev/null || true
pkill -f "python3 -c" 2>/dev/null || true
sleep 0.3

echo "[fwd-smoke] start center on :${CENTER_PORT}"
GODEX_HOME="$CENTER_HOME" "$BIN" serve --addr "127.0.0.1:${CENTER_PORT}" >"$WORK/center.log" 2>&1 &
CENTER_PID=$!
for i in $(seq 1 40); do
  curl -sf "$CENTER_URL/api/meta" >/dev/null 2>&1 && break
  sleep 0.25
done
curl -sf "$CENTER_URL/api/meta" >/dev/null || { echo "center did not come up"; tail -5 "$WORK/center.log"; exit 1; }

echo "[fwd-smoke] start node once to generate node id"
GODEX_HOME="$NODE_HOME" "$BIN" serve --addr "127.0.0.1:${NODE_PORT}" >"$WORK/node-first.log" 2>&1 &
NODE_PID=$!
for i in $(seq 1 40); do
  [ -f "$NODE_HOME/state/node.json" ] && break
  sleep 0.25
done
NODE_ID="$(grep -o '"id"[^,}]*' "$NODE_HOME/state/node.json" | head -1 | sed 's/.*: *"//; s/"//')"
[ -n "$NODE_ID" ] || { echo "failed to read node id"; tail -5 "$WORK/node-first.log"; exit 1; }
echo "[fwd-smoke] node id: $NODE_ID"
kill "$NODE_PID" 2>/dev/null || true
wait "$NODE_PID" 2>/dev/null || true
NODE_PID=""

echo "[fwd-smoke] register node and issue credential"
curl -sf -X POST "$CENTER_URL/api/control/nodes/register" \
  -H 'Content-Type: application/json' \
  -d "{\"id\":\"$NODE_ID\",\"name\":\"forward-node\",\"version\":\"dev\",\"trust_level\":\"trusted\"}" >/dev/null
CRED_RESP="$(curl -sf -X POST "$CENTER_URL/api/control/nodes/${NODE_ID}/credential")"
CREDENTIAL="$(printf '%s' "$CRED_RESP" | grep -o '"credential"[^,}]*' | head -1 | sed 's/.*: *"//; s/"//')"
[ -n "$CREDENTIAL" ] || { echo "failed to issue credential: $CRED_RESP"; exit 1; }

echo "[fwd-smoke] restart node with center_url + credential + forward_allow"
GODEX_HOME="$NODE_HOME" \
GODEX_CONTROL_CENTER_URL="$CENTER_URL" \
GODEX_CONTROL_CREDENTIAL="$CREDENTIAL" \
GODEX_CONTROL_FORWARD_ALLOW="127.0.0.1:*" \
"$BIN" serve --addr "127.0.0.1:${NODE_PORT}" >"$WORK/node.log" 2>&1 &
NODE_PID=$!

echo "[fwd-smoke] wait for node relay connected"
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
echo "[fwd-smoke] node relay connected"

echo "[fwd-smoke] start internal-network echo service on node side (port ${ECHO_PORT})"
python3 -c "
import socket, threading
s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
s.bind(('127.0.0.1', ${ECHO_PORT}))
s.listen(5)
while True:
    c, _ = s.accept()
    def handle(conn):
        try:
            while True:
                data = conn.recv(4096)
                if not data:
                    break
                conn.sendall(data)
        except Exception:
            pass
        finally:
            conn.close()
    threading.Thread(target=handle, args=(c,), daemon=True).start()
" >"$WORK/echo.log" 2>&1 &
ECHO_PID=$!
sleep 0.3

echo "[fwd-smoke] 1/2 forward local ${LOCAL_PORT} -> node -> echo ${ECHO_PORT}"
GODEX_HOME="$CENTER_HOME" "$BIN" node forward \
  --node "$NODE_ID" --local "$LOCAL_PORT" --target "127.0.0.1:${ECHO_PORT}" \
  --center "$CENTER_URL" >"$WORK/forward.log" 2>&1 &
FWD_PID=$!
sleep 0.8

RESP="$(python3 -c "
import socket
s = socket.create_connection(('127.0.0.1', ${LOCAL_PORT}), timeout=5)
s.sendall(b'ping-forward-39399')
data = s.recv(4096)
s.close()
print(data.decode())
")"
if [ "$RESP" != "ping-forward-39399" ]; then
  echo "forward echo mismatch: got '$RESP'"
  echo "--- forward log ---"; cat "$WORK/forward.log"
  echo "--- node log ---"; tail -8 "$WORK/node.log"
  exit 1
fi
echo "  ok (echo round-trip: $RESP)"

echo "[fwd-smoke] 2/2 forward_allow denies unlisted target"
GODEX_HOME="$CENTER_HOME" "$BIN" node forward \
  --node "$NODE_ID" --local "$DENY_PORT" --target "10.0.0.5:3306" \
  --center "$CENTER_URL" >"$WORK/deny-forward.log" 2>&1 &
DENY_FWD_PID=$!
sleep 0.8

DENIED=""
if python3 -c "
import socket
try:
    s = socket.create_connection(('127.0.0.1', ${DENY_PORT}), timeout=3)
    s.sendall(b'x')
    s.settimeout(2)
    try:
        data = s.recv(4096)
    except ConnectionResetError:
        raise SystemExit(0)  # reset -> node rejected the stream
    finally:
        s.close()
    if data:
        raise SystemExit(1)  # got echo data back -> forward unexpectedly succeeded
    raise SystemExit(0)      # clean EOF -> node rejected the stream
except OSError:
    raise SystemExit(0)      # refused -> node rejected the stream
" 2>/dev/null; then
  DENIED=1
fi
if [ -z "$DENIED" ]; then
  echo "deny test failed: unlisted target returned data instead of being rejected"
  echo "--- deny-forward log ---"; cat "$WORK/deny-forward.log"
  exit 1
fi
echo "  ok (unlisted target rejected)"

echo "[fwd-smoke] PASS"
