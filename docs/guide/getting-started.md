# 快速开始

本页用最短路径从源码启动 GoDex Web 工作台。

## 环境要求

- Go 1.26+
- Node.js 24+ 与 Corepack（仅构建 Web UI 时需要）
- 一个可用的 LLM provider

## 1. 获取并构建

```bash
git clone https://github.com/tim5wang/godex.git
cd godex
go mod download
corepack pnpm -C ui/web install
corepack pnpm -C ui/web build
```

## 2. 启动服务

```bash
go run ./cmd/godex serve --addr 127.0.0.1:8080
```

打开 <http://127.0.0.1:8080>。

## 3. 配置模型

推荐在 Web 的 **Settings** 中添加 provider、模型和密钥引用。也可以使用 CLI：

```bash
go run ./cmd/godex login openai --mode platform-api-key
go run ./cmd/godex providers list
go run ./cmd/godex providers test <provider-id>
```

配置与默认运行数据保存在 `~/.godex`；项目目录只保留显式创建的项目文件。

## 4. 开始使用

```bash
# 单次提问
go run ./cmd/godex ask "总结当前仓库结构"

# 交互式入口
go run ./cmd/godex

# 初始化项目配置
go run ./cmd/godex setup --dir /path/to/project
```

## 5. 验证开发环境

```bash
make docs-check
make web-typecheck
go test ./cmd/... ./examples/... ./internal/...
```

完整发布质量门禁是：

```bash
make verify
```

## 下一步

- 配置、CLI、Web、API、Memory、安全与故障排查：[用户指南](../user-guide.md)
- MCP、Skill、Package、WASM 与 ACP：[扩展运行时](../extension-runtime-user-guide.md)
- 代码分层与贡献流程：[开发指南](../develop/index.md)
- 服务安装与节点接入：[部署与运维](../operations/index.md)

::: warning 版本事实源
CLI 参数以 `godex --help` 为准；Slash Command 以运行时 metadata 为准；工具和 bundle 以当前 tool catalog 为准。
:::
