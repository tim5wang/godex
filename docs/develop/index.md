# 参与开发

本区面向修改 GoDex 本身的开发者。先确认功能状态和代码归属，再动手实现。

## 阅读顺序

1. [功能—实现—文档矩阵](../feature-implementation-matrix.md)：确认能力是 Implemented、Partial 还是 Planned。
2. [项目结构](../project-structure.md)：定位最窄的责任层。
3. [架构 SPEC](../architecture-v2-spec.md)：理解模块边界与长期方向。
4. 对应模块的 Active 设计文档：了解契约和未完成项。

## 代码分层

| 目录 | 职责 |
|---|---|
| `cmd/godex` | 二进制入口 |
| `internal/app` | CLI、serve、生命周期与命令装配 |
| `internal/agent` | Agent loop、context、turn runtime、subagent |
| `internal/runtime` | HTTP/Web UI、IM、Cron、Heartbeat 适配器 |
| `internal/services` | 适配器背后的可复用服务 |
| `internal/toolruntime` | 工具框架、权限、拦截器、执行上下文 |
| `internal/tools` | 具体工具和工具 bundle |
| `internal/core` | Memory、Skill、Package、MCP 等核心模块 |
| `internal/platform` | 文件系统、日志、本地存储等基础设施 |
| `ui/web` | React/Vite Web UI |

完整边界见[项目结构](../project-structure.md)。

## 最小验证

改动后先执行与改动匹配的测试，再执行项目门禁：

```bash
make verify
```

其中包含 Go test/vet、`make docs-check`、Web typecheck/test 和 bundle budget 检查。

## 文档约定

- CLI 命令与参数以 help 为事实源，不在多篇文档复制完整语法。
- Slash Command、Web app、工具和 HTTP 路径分别以代码中的 metadata、registry、catalog 和 route registrar 为事实源。
- 新增顶层 Markdown 后，要加入[完整文档索引](../README.md)。
- 提交前运行 `make docs-check`；修改文档站时再运行 `make docs-build`。

## 常用参考

- [Workflow Runtime](../workflow-runtime.md)
- [Memory 设计](../memory-design-principles.md)
- [Scope 隔离](../scope-isolation-design.md)
- [Agent 角色与工具集](../agent-role-and-bundle-design.md)
- [Business Flow Runtime](../business-flow-runtime-design.md)
