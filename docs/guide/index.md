# GoDex 是什么

GoDex 是一个**本地优先的 AI Agent 工作台与运行时**。它把 CLI、TUI、Web、HTTP API、Feishu、Weixin 等入口接到同一套后端，让聊天、工具执行、文件附件、长期记忆、子 Agent、审批和运行审计共享一个 Session Runtime。

## 适合什么场景

- 在真实代码库中理解、修改、测试和交付代码。
- 让长任务由多个 Agent 并行执行，并保留恢复、评审与合并能力。
- 用 Web、API 或 IM 构建团队内部的业务智能体。
- 在本地治理 MCP、Skills、Packages、工具权限、Memory 和自动化。
- 将 Agent 运行记录、审批、用量和失败诊断放在同一处观察。

## 核心模型

```text
入口（Web / CLI / TUI / API / IM）
                 │
                 ▼
          Session Runtime
    ┌────────────┼────────────┐
    ▼            ▼            ▼
 Context       Tools        Events
    │            │            │
 Memory    MCP / Skill    Timeline / Audit
                 │
                 ▼
   Subagent / Workflow / Business Flow
```

同一个 Session 承载身份、上下文、消息、工具调用、附件和事件；入口只是不同的适配器。这使得一次任务可以从 Web 发起，在后台继续执行，并在 API、TUI 或通知渠道中观察结果。

## 三条阅读路线

### 使用 GoDex

1. [快速开始](./getting-started.md)
2. [用户指南](../user-guide.md)
3. [扩展运行时](../extension-runtime-user-guide.md)

### 开发 GoDex

1. [参与开发](../develop/index.md)
2. [项目结构](../project-structure.md)
3. [架构参考](../reference/index.md)

### 部署 GoDex

1. [部署与运维](../operations/index.md)
2. [自部署](../self-deploy.md)
3. [节点接入](../node-onboarding.md)

::: tip 功能是否存在？
设计文档可能包含尚未落地的方向。判断当前能力时，先查[功能—实现—文档矩阵](../feature-implementation-matrix.md)。
:::
