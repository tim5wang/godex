# GoDex Self-Deployment Guide

GoDex 部署到 mycloud (47.120.112.175) 服务器的指南。

## 服务器信息

| 项目 | 值 |
|------|------|
| 服务器 | mycloud |
| IP | 47.120.112.175 |
| 系统 | Ubuntu 24.04.4 LTS |
| 部署目录 | `/opt/godex/` |
| 域名 | `https://godex.claw.carc.top` |
| 端口 | 3800 |

---

## 快速更新（推荐，4 步）

> **注意**：本机 Go 在 `/usr/local/go/bin/go`，不在默认 PATH。编译前请设置 `PATH` 或使用绝对路径。

```bash
# 0. 设置 Go 路径（如果 go 命令找不到）
export PATH="/usr/local/go/bin:$PATH"

# 1. 交叉编译
GOOS=linux GOARCH=amd64 go build -o godex-linux-amd64 ./cmd/godex

# 2. 停止服务
ssh mycloud "systemctl stop godex"

# 3. 上传新版本
scp godex-linux-amd64 mycloud:/opt/godex/godex-new

# 4. 备份旧版本 → 安装新版本 → 启动（安全策略限制：请勿在 SSH 中使用 $(date) 命令替换，预先定义变量）
DATE_TAG=$(date +%Y%m%d)
ssh mycloud "mv /opt/godex/godex /opt/godex/godex-backup-${DATE_TAG} && mv /opt/godex/godex-new /opt/godex/godex && chmod +x /opt/godex/godex && systemctl start godex"

# 5. 验证
ssh mycloud "systemctl status godex --no-pager"
ssh mycloud "ss -tlnp | grep 3800"
curl -s -o /dev/null -w '%{http_code}' https://godex.claw.carc.top/
```

### 一键更新脚本

保存为 `scripts/deploy-mycloud.sh`，直接运行 `bash scripts/deploy-mycloud.sh`：

```bash
#!/bin/bash
set -e

cd "$(dirname "$0")/.."
DATE_TAG=$(date +%Y%m%d)

echo "=== GoDex Deploy to mycloud ==="

# 设置 Go 路径（如 go 已在 PATH 可跳过）
export PATH="/usr/local/go/bin:$PATH"

echo "[1/4] Building linux/amd64 binary..."
GOOS=linux GOARCH=amd64 go build -o godex-linux-amd64 ./cmd/godex

echo "[2/4] Stopping service..."
ssh mycloud "systemctl stop godex" || true

echo "[3/4] Uploading binary..."
scp godex-linux-amd64 mycloud:/opt/godex/godex-new
ssh mycloud "mv /opt/godex/godex /opt/godex/godex-backup-${DATE_TAG} 2>/dev/null || true; mv /opt/godex/godex-new /opt/godex/godex && chmod +x /opt/godex/godex"

echo "[4/4] Starting service..."
ssh mycloud "systemctl start godex && sleep 2 && systemctl status godex --no-pager"

echo "=== Deploy completed ==="
rm -f godex-linux-amd64
ssh mycloud "ss -tlnp | grep 3800 && echo 'Service is running on port 3800'"
```

---

## 首次部署

### 1. 准备目录结构

```bash
ssh mycloud "mkdir -p /opt/godex/{log,ui/web}"
ssh mycloud "mkdir -p /opt/godex/.godex/{sessions,tasks,todos,team,transcripts,tmp,.tool-results,channels,control,cron,heartbeat,memory,notes,rules,security,skills,packages,workflows,background}"
```

### 2. 上传文件

```bash
# 二进制（交叉编译）
export PATH="/usr/local/go/bin:$PATH"
GOOS=linux GOARCH=amd64 go build -o godex-linux-amd64 ./cmd/godex
scp godex-linux-amd64 mycloud:/opt/godex/godex
ssh mycloud "chmod +x /opt/godex/godex"
rm -f godex-linux-amd64

# Web UI（如需要）
scp -r ui/web/dist/* mycloud:/opt/godex/ui/web/dist/
```

### 3. Systemd 服务配置

```bash
# 创建 systemd unit（仅首次）
ssh mycloud "cat > /etc/systemd/system/godex.service << 'EOF'
[Unit]
Description=GoDex AI Assistant
After=network.target network-online.target

[Service]
Type=simple
User=root
WorkingDirectory=/opt/godex
Environment=GODEX_HOME=/opt/godex/.godex
Environment=GODEX_PROJECT_DIR=/opt/godex
ExecStart=/opt/godex/godex serve --addr 0.0.0.0:3800
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF"

ssh mycloud "systemctl daemon-reload && systemctl enable godex && systemctl start godex"
```

---

## 验证

```bash
ssh mycloud "systemctl status godex --no-pager"
ssh mycloud "ss -tlnp | grep 3800"
curl -s https://godex.claw.carc.top/ | head -5
```

---

## 常用操作

| 操作 | 命令 |
|------|------|
| 查看状态 | `ssh mycloud "systemctl status godex"` |
| 重启 | `ssh mycloud "systemctl restart godex"` |
| 日志 | `ssh mycloud "journalctl -u godex -f"` |
| 应用日志 | `ssh mycloud "tail -f /opt/godex/log/godex.log"` |
| 回滚 | `ssh mycloud "systemctl stop godex && mv /opt/godex/godex /opt/godex/godex-bad && mv /opt/godex/godex-backup-20250507 /opt/godex/godex && systemctl start godex"` |

---

## 故障排查

| 症状 | 检查 |
|------|------|
| 服务启动失败 | `journalctl -u godex --no-pager \| tail -30` |
| Web UI 显示异常 | `ls -lh /opt/godex/godex`（正常 ~72MB，过小则未嵌入 UI） |
| 端口冲突 | `ss -tlnp \| grep 3800`，可修改 systemd 中的端口 |

---

## 注意事项

- **Go 版本**：本机 Go 在 `/usr/local/go/bin/go`，版本 `1.26.0`。编译前确保 PATH 正确。
- **架构**：必须交叉编译为 `linux/amd64`
- **备份**：更新前自动备份旧版本，如需回滚使用备份文件
- **安全策略限制**：在 AI agent 环境中，SSH 命令中不能使用 `$(date)` 命令替换，需提前赋值变量再传入
