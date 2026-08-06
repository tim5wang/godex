#!/usr/bin/env bash
# Web Push（Phase 5 移动端）端到端冒烟
#
# 场景：
#   1. 起中心 godex（127.0.0.1:3951）
#   2. 验证 /api/push/public-key 返回 VAPID 公钥
#   3. 验证 /api/push/subscribe 注册订阅（内存）
#   4. 验证 /api/push/test 触发通知（无真实订阅时返回 notified=0 不报错）
#   5. 验证 /api/push/unsubscribe 移除订阅
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

CENTER_PORT=3951
CENTER_URL="http://127.0.0.1:${CENTER_PORT}"

WORK="$ROOT_DIR/.godex/tmp/push-smoke"
BIN="$WORK/godex"
CENTER_HOME="$WORK/home"
mkdir -p "$CENTER_HOME"

if [ "${1:-}" = "build" ]; then
  echo "[push-smoke] build binary"
  export PATH="/usr/local/go/bin:$PATH"
  go build -o "$BIN" ./cmd/godex
fi
[ -x "$BIN" ] || { echo "binary missing: $BIN (run with 'build' first)"; exit 1; }

CENTER_PID=""
cleanup() {
  [ -n "$CENTER_PID" ] && kill "$CENTER_PID" 2>/dev/null || true
}
trap cleanup EXIT

pkill -f "godex serve --addr 127.0.0.1:${CENTER_PORT}" 2>/dev/null || true
sleep 0.3

echo "[push-smoke] start center on :${CENTER_PORT}"
GODEX_HOME="$CENTER_HOME" "$BIN" serve --addr "127.0.0.1:${CENTER_PORT}" >"$WORK/center.log" 2>&1 &
CENTER_PID=$!
for i in $(seq 1 40); do
  curl -sf "$CENTER_URL/api/meta" >/dev/null 2>&1 && break
  sleep 0.25
done
curl -sf "$CENTER_URL/api/meta" >/dev/null || { echo "center did not come up"; tail -5 "$WORK/center.log"; exit 1; }

echo "[push-smoke] 1/4 public-key returns VAPID application server key"
PUBKEY="$(curl -sf "$CENTER_URL/api/push/public-key")"
printf '%s' "$PUBKEY" | grep -q '"public_key"' || { echo "missing public_key: $PUBKEY"; exit 1; }
echo "  ok ($(printf '%s' "$PUBKEY" | head -c 40)...)"

echo "[push-smoke] 2/4 subscribe registers in-memory subscription"
SUB_RESP="$(curl -sf -X POST "$CENTER_URL/api/push/subscribe" \
  -H 'Content-Type: application/json' \
  -d '{"endpoint":"https://push.example.com/s/demo","keys":{"auth":"a","p256dh":"d"}}')"
printf '%s' "$SUB_RESP" | grep -q '"ok":true' || { echo "subscribe failed: $SUB_RESP"; exit 1; }
echo "  ok"

echo "[push-smoke] 3/4 test push runs without error (fake endpoint not reachable -> pruned, notified=0)"
TEST_RESP="$(curl -sf -X POST "$CENTER_URL/api/push/test" || true)"
echo "  raw: $TEST_RESP"
printf '%s' "$TEST_RESP" | grep -q '"notified"' || { echo "test push did not return notified count"; exit 1; }
echo "  ok"

echo "[push-smoke] 4/4 unsubscribe removes subscription"
UNSUB_RESP="$(curl -sf -X POST "$CENTER_URL/api/push/unsubscribe" \
  -H 'Content-Type: application/json' \
  -d '{"endpoint":"https://push.example.com/s/demo"}')"
printf '%s' "$UNSUB_RESP" | grep -q '"ok":true' || { echo "unsubscribe failed: $UNSUB_RESP"; exit 1; }
echo "  ok"

echo "[push-smoke] PASS"
