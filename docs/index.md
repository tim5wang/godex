---
layout: home
title: GoDex 文档
titleTemplate: false

hero:
  name: GoDex
  text: 本地优先的 AI Agent 工作台
  tagline: 用一套 Session Runtime 连接聊天、工具、记忆、自动化、多 Agent 工作流与业务系统。
  image:
    src: /brand/godex-icon.jpg
    alt: GoDex
  actions:
    - theme: brand
      text: 快速开始
      link: /guide/getting-started
    - theme: alt
      text: 阅读架构
      link: /reference/
    - theme: alt
      text: GitHub
      link: https://github.com/tim5wang/godex

features:
  - icon: 🧭
    title: 共享 Session Runtime
    details: CLI、TUI、Web、HTTP API 与 IM 入口共享会话、事件、附件、审批和长期记忆。
    link: /reference/
    linkText: 了解架构
  - icon: 🧰
    title: 工具与扩展
    details: 统一管理本地工具、MCP、Skills、Packages、WASM 与 ACP 外部 Agent。
    link: /extension-runtime-user-guide
    linkText: 扩展运行时
  - icon: 🕸️
    title: 多 Agent 与工作流
    details: Subagent、Workflow、Agent Graph、LongTask 与可视化 Business Flow 覆盖长任务编排。
    link: /workflow-runtime
    linkText: 查看运行时
  - icon: 🧠
    title: Context 与 Memory
    details: 历史召回、上下文压缩、durable memory、候选记忆与作用域隔离均可审计。
    link: /memory-design-principles
    linkText: Memory 设计
  - icon: 🛡️
    title: 安全边界
    details: WorkspaceFS、审批模式、工具白名单、scope 隔离和 sandbox 共同约束高风险动作。
    link: /scope-isolation-design
    linkText: 安全模型
  - icon: 🚀
    title: 单二进制部署
    details: React Web UI 嵌入 Go 二进制，支持本地运行、服务安装、节点接入与 Relay。
    link: /operations/
    linkText: 部署与运维
---

## 按你的目标开始

<div class="vp-doc">

| 我要做什么 | 从这里开始 |
|---|---|
| 安装并运行 GoDex | [5 分钟快速开始](./guide/getting-started.md) |
| 学会 Web、CLI、配置、Memory 与自动化 | [用户指南](./user-guide.md) |
| 接入 MCP、Package、Skill、WASM 或 ACP | [扩展运行时指南](./extension-runtime-user-guide.md) |
| 理解代码分层与运行时 | [架构参考](./reference/index.md) |
| 部署服务或连接远程节点 | [部署与运维](./operations/index.md) |
| 判断某项功能是否真的已经实现 | [功能—实现—文档矩阵](./feature-implementation-matrix.md) |
| 查阅设计稿、历史记录或路线图 | [完整文档索引](./README.md) |

</div>
