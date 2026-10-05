# GoDex 架构

改动 `internal/agent`、`internal/runtime`、`internal/services`、`internal/toolruntime` 或 `internal/core` 前，建议先阅读本文及对应模块的 Active 设计文档。

## 总览

GoDex 的核心不是某一个聊天界面，而是多个入口共享的 **Session Runtime**：

```text
┌─────────────────────────────────────────────────────┐
│ Web · CLI · TUI · HTTP API · Feishu · Weixin · ACP │
└──────────────────────────┬──────────────────────────┘
                           ▼
┌─────────────────────────────────────────────────────┐
│ Session Runtime                                     │
│ identity · messages · turns · events · attachments │
│ context · permissions · usage · audit              │
└──────────────┬──────────────────┬───────────────────┘
               ▼                  ▼
       Agent / Harness       Tool Runtime
       provider/model        bundles · approval
       context/compact       sandbox · scope
               │                  │
               ├──────────┬───────┤
               ▼          ▼       ▼
            Memory      MCP    Local tools
               │
               ▼
 Subagent · Workflow · AgentGraph · LongTask · Flow
```

## 主要层次

### 入口与适配器

`internal/runtime` 承载 HTTP/Web UI、IM、Cron、Heartbeat 等外部入口；`internal/app` 负责 CLI、serve 与生命周期装配。入口不应各自实现一套 Agent 行为。

### Session 与 Agent

Session 保存身份、消息、turn、事件与附件。`internal/agent` 构建上下文、选择 provider 或 harness、执行 turn，并把输出和工具过程写回事件流。

### Tool Runtime

`internal/toolruntime` 统一工具 schema、权限、审批、拦截器和执行上下文；`internal/tools` 提供具体工具。Bundle 只控制工具暴露，不应绕过权限边界。

### Context 与 Memory

Context 是单轮模型输入；Memory 是跨轮或跨会话的 durable 信息。压缩、历史召回和作用域策略控制输入规模及隔离边界。

### Durable Orchestration

Subagent、Workflow、AgentGraph、LongTask 和 Business Flow 复用 Session、工具与事件能力，但提供不同抽象层级的并发、恢复和人工介入机制。

## 关键原则

1. **单一事实源**：命令、工具、路由、Web app 与功能状态从可执行代码或 registry 生成。
2. **入口共享运行时**：入口只做协议适配，不复制领域逻辑。
3. **默认本地优先**：配置、session、memory 与附件默认留在 `GODEX_HOME`。
4. **能力最小化**：模板、bundle、scope、sandbox 与审批共同限制 Agent。
5. **事件可审计**：消息、工具调用、节点状态和人工动作都能进入 timeline。
6. **长任务可恢复**：durable job 和 workflow 用持久状态而不是进程内协程表达生命周期。

## 深入阅读

- [GoDex 2.0 架构 SPEC](../architecture-v2-spec.md)
- [项目结构](../project-structure.md)
- [Workflow Runtime](../workflow-runtime.md)
- [Business Flow Runtime](../business-flow-runtime-design.md)
- [Memory 设计原则](../memory-design-principles.md)
- [Scope 隔离设计](../scope-isolation-design.md)
- [功能—实现—文档矩阵](../feature-implementation-matrix.md)

::: warning 设计与实现
架构文档同时包含现状和演进方向。判断能力能否使用时，以[功能—实现—文档矩阵](../feature-implementation-matrix.md)、help、测试与代码入口为准。
:::
