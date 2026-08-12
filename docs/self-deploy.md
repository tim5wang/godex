# GoDex Self-Deployment Guide

> 状态：Active（部署手册）

GoDex 部署到 `mycloud` 服务器的简化流程。服务安装、启动、重启和日志查看统一使用 `godex service` 命令，不再手写 systemd unit。

## 服务器信息

| 项目 | 值 |
|------|------|
| 服务器 | `mycloud` |
| 系统 | Ubuntu 24.04.4 LTS |
| 部署目录 | `/opt/godex/` |
| 域名 | `https://godex.claw.carc.top` |
| 端口 | `3801` |

## 快速更新

> 本机 Go 在 `/usr/local/go/bin/go` 时，先设置 `PATH`。

```bash
export PATH="/usr/local/go/bin:$PATH"

# 1. 构建 linux/amd64 二进制
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o godex-linux-amd64 ./cmd/godex

# 2. 上传新版本
ssh mycloud "mkdir -p /opt/godex"
scp godex-linux-amd64 mycloud:/opt/godex/godex-new

# 3. 原子替换并重启服务
DATE_TAG=$(date +%Y%m%d)
ssh mycloud "cd /opt/godex && mv godex godex-backup-${DATE_TAG} 2>/dev/null || true; mv godex-new godex && chmod +x godex && ./godex service restart --scope system --name godex"

# 4. 验证
ssh mycloud "/opt/godex/godex service status --scope system --name godex"
ssh mycloud "ss -tlnp | grep 3801"
curl -s -o /dev/null -w '%{http_code}\n' https://godex.claw.carc.top/
```

## 首次部署

```bash
export PATH="/usr/local/go/bin:$PATH"

# 1. 构建并上传
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o godex-linux-amd64 ./cmd/godex
ssh mycloud "mkdir -p /opt/godex"
scp godex-linux-amd64 mycloud:/opt/godex/godex
ssh mycloud "chmod +x /opt/godex/godex"
rm -f godex-linux-amd64

# 2. 安装并启动 system service
ssh mycloud "cd /opt/godex && ./godex service install --scope system --name godex --addr 0.0.0.0:3801 --gomemlimit 200MiB --gogc 50 --gomaxprocs 1 --memory-high 260M --memory-max 300M"
ssh mycloud "/opt/godex/godex service start --scope system --name godex"

# 3. 验证
ssh mycloud "/opt/godex/godex service status --scope system --name godex"
curl -s -o /dev/null -w '%{http_code}\n' https://godex.claw.carc.top/
```

`service install` 会创建 systemd unit，并写入：

- `GOMEMLIMIT`、`GOGC`、`GOMAXPROCS`、`GODEBUG`
- `Restart=always`
- `WatchdogSec`
- 可选 `MemoryHigh` / `MemoryMax`

## 常用操作

| 操作 | 命令 |
|------|------|
| 查看状态 | `ssh mycloud "/opt/godex/godex service status --scope system --name godex"` |
| 启动 | `ssh mycloud "/opt/godex/godex service start --scope system --name godex"` |
| 重启 | `ssh mycloud "/opt/godex/godex service restart --scope system --name godex"` |
| 停止 | `ssh mycloud "/opt/godex/godex service stop --scope system --name godex"` |
| 日志 | `ssh mycloud "/opt/godex/godex service logs --scope system --name godex --follow"` |
| 回滚 | `ssh mycloud "cd /opt/godex && ./godex service stop --scope system --name godex && mv godex godex-bad && mv godex-backup-YYYYMMDD godex && chmod +x godex && ./godex service start --scope system --name godex"` |

## 故障排查

| 症状 | 检查 |
|------|------|
| 服务启动失败 | `ssh mycloud "/opt/godex/godex service logs --scope system --name godex"` |
| OOM 或频繁重启 | `ssh mycloud "systemctl show godex -p NRestarts -p MemoryCurrent -p MemoryPeak -p Result"` |
| Web UI 显示异常 | `ssh mycloud "ls -lh /opt/godex/godex"`，文件过小可能未嵌入 UI |
| 端口冲突 | `ssh mycloud "ss -tlnp | grep 3801"`，重新 `service install --addr <addr>` 后再启动 |

## 注意事项

- 架构必须是 `linux/amd64`。
- 更新前保留 `/opt/godex/godex-backup-YYYYMMDD`，便于回滚。
- 300M 内存预算建议使用 `--gomemlimit 200MiB --memory-high 260M --memory-max 300M`。
- 如果远端需要 root 权限管理 system service，请在 SSH 命令中按服务器配置使用 `sudo`。
