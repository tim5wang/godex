#!/bin/bash
set -e

cd "$(dirname "$0")/.."

echo "=== GoDex Deploy to mycloud ==="

# Build
echo "[1/4] Building linux/amd64 binary..."
GOOS=linux GOARCH=amd64 go build -o godex-linux-amd64 ./cmd/godex

# Stop service
echo "[2/4] Stopping service..."
ssh mycloud "systemctl stop godex" || true

# Upload
echo "[3/4] Uploading binary..."
scp godex-linux-amd64 mycloud:/opt/godex/godex-new
ssh mycloud "mv /opt/godex/godex /opt/godex/godex-backup-$(date +%Y%m%d-%H%M%S) 2>/dev/null || true; mv /opt/godex/godex-new /opt/godex/godex && chmod +x /opt/godex/godex"

# Start
echo "[4/4] Starting service..."
ssh mycloud "systemctl start godex && sleep 2 && systemctl status godex --no-pager"

echo ""
echo "=== Deploy completed ==="
ssh mycloud "ss -tlnp | grep 3800 && echo 'Service is running on port 3800'"
