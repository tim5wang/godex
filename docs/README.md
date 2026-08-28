# GoDex 文档索引（docs/）

> 本索引帮助快速定位每份文档的用途、状态与相互关系。
> 修订日志：2026-08-12 首次建立索引；统一文档状态头与修订日志约定，标注被合并/被取代的旧文档。
> 2026-08-28：补充 taskboard-plugin-design.md 条目（taskboard 插件已交付 M1/M2/M2.5）。

## 状态约定

| 状态 | 含义 | 使用建议 |
|------|------|---------|
| **Active** | 当前权威文档，以它为准 | 实现、评审、排期以此为据 |
| **Draft / Plan** | 设计草案或实施计划，尚未落地或部分落地 | 阅读了解方向，实施前需确认 |
| **Superseded** | 已被其他文档合并/取代，仅保留追溯 | 不要作为当前依据 |
| **Historical** | 历史记录（修复、验证、issue），内容可能过时 | 仅作参考 |

---

## A. 当前权威（Active）

| 文档 | 内容 | 备注 |
|------|------|------|
| [godex-optimization-roadmap.md](./godex-optimization-roadmap.md) | **优化路线图（统一版）**，含 P0-P6 分期定义、Phase 0-6 任务与完成状态 | **P0-P6 分期以本文档为准**；合并自 qm-roadmap / longtask-analysis / roadmap-high-roi / roadmap-runtime-hardening / architecture-v2-spec / agent-role-and-bundle-design |
| [architecture-v2-spec.md](./architecture-v2-spec.md) | GoDex 2.0 架构 SPEC（中文） | 含英文版 [architecture-v2-spec.en.md](./architecture-v2-spec.en.md) |
| [user-guide.md](./user-guide.md) | 用户指南：安装运行、配置、Provider、CLI、Web UI、工具、Memory、命令、HTTP API、自动化、安全、故障排查 | README 的补充细节；2026-08-15 全量重写对齐实现 |
| [code-review-2026-08-15.md](./code-review-2026-08-15.md) | 代码与设计 Review 记录：文档↔实现不一致、代码侧发现、设计观察 | 2026-08-15 |
| [project-structure.md](./project-structure.md) | 项目目录结构与分层规范 | |
| [memory-design-principles.md](./memory-design-principles.md) | Memory 模块目标、边界、组成与约束 | |
| [workflow-runtime.md](./workflow-runtime.md) | durable workflow runtime（长任务/多 agent） | |
| [scope-isolation-design.md](./scope-isolation-design.md) | Scope 隔离模型设计（roadmap 6.2，M1-M5 已完成） | |
| [taskboard-plugin-design.md](./taskboard-plugin-design.md) | taskboard 插件（需求池 #1）：设计与实现参照（M1 核心闭环/M2 看板/M2.5 执行进度跳转 ✅，M3 未开始）；含卡片死锁修复（human 越锁 + 离开 in_progress 自动终结 running execution） | |
| [per-session-workspace-plan.md](./per-session-workspace-plan.md) | Per-Session 工作目录（已实施 2026-07-31） | |
| [spec-of-chat-layout-optimize.md](./spec-of-chat-layout-optimize.md) | Web UI 聊天工作区升级 SPEC（M0/M1 已落地） | |
| [p0-p4-visualization-design.md](./p0-p4-visualization-design.md) | P0-P4 可视化设计（A1/A2/B1/B2/C1 完成，A3/B3/C2 待办） | |
| [node-onboarding.md](./node-onboarding.md) | 节点接入手册（2026-08-06） | |
| [self-deploy.md](./self-deploy.md) | 自部署指南（mycloud） | |
| [vscode-acp.md](./vscode-acp.md) | VS Code ACP Client 集成指南 | |

## B. 设计草案 / 计划（Draft / Plan）

| 文档 | 内容 | 备注 |
|------|------|------|
| [reseach_of_dsh_for_godex_optimize.md](./reseach_of_dsh_for_godex_optimize.md) | DSH/Cordis 插件设计 → GoDex 改进方案（插件内核 P0-P4、WASM 边界、wazero 兼容路径、路线图） | Draft（2026-08-15，含阶段 0 起步点） |
| [node-mesh-design.md](./node-mesh-design.md) | 节点互联与远程开发设计（Node Mesh v2） | Draft（待确认后启动） |
| [big-file-split-plan.md](./big-file-split-plan.md) | 大文件拆分改造方案（技术债） | 2026-08-05 |
| [cache-optimization-plan.md](./cache-optimization-plan.md) | Cache & Tool 优化计划 | |
| [tui-bubbletea-design.md](./tui-bubbletea-design.md) | 统一多入口交互与 Bubble Tea TUI 设计 | |
| [voice-plugin-extensibility-design.md](./voice-plugin-extensibility-design.md) | 插件能力扩展设计（已对齐）：A. turn 级中间件（用户输入→LLM→回复钩子）+ B. UI 插槽（settings 配置声明 + ui_card 卡片）+ C. 语音后端 L2（OpenAI 兼容 REST→Realtime），含 dsh/pi 调研结论 | Draft（2026-08-27，对齐范围后更新） |
| [plugin-system-evolution-plan.md](./plugin-system-evolution-plan.md) | 插件系统长期主义演进方案：dsh-taskboard 适配验证（P-A 路由/P-B 前端 UI 插槽/P-C 服务注入/P-D 调度）+ 需求池子优先级排序 | Draft（2026-08-27） |

## C. 已合并 / 被取代（Superseded）

> 这些文档的规划内容已合并进 `godex-optimization-roadmap.md`，或已被修订版取代。**保留仅作追溯，勿作当前依据。**

| 文档 | 被谁取代/合并 |
|------|--------------|
| [qm-roadmap.md](./qm-roadmap.md) | 已合并入 godex-optimization-roadmap |
| [longtask-analysis.md](./longtask-analysis.md) | 已合并入 godex-optimization-roadmap |
| [roadmap-high-roi.md](./roadmap-high-roi.md) | 已合并入 godex-optimization-roadmap |
| [roadmap-runtime-hardening.md](./roadmap-runtime-hardening.md) | 已合并入 godex-optimization-roadmap |
| [agent-template-board-design.md](./agent-template-board-design.md) | 草稿已吸收进 [agent-role-and-bundle-design.md](./agent-role-and-bundle-design.md)（agent 模板板块主设计文档） |
| [capability-enhancement-v1.md](./capability-enhancement-v1.md) | 已被 v2 修订取代 |
| [capability-enhancement-v2.md](./capability-enhancement-v2.md) | 产品化规划 v2（另一条规划线，非 P0-P6 分期来源） |

## D. 历史记录（Historical）

| 文档 | 内容 | 备注 |
|------|------|------|
| [p0-p6-e2e-validation.md](./p0-p6-e2e-validation.md) | 旧迭代的端到端验证清单 | **旧迭代遗留，P0-P6 定义以 roadmap 为准，勿以此文档为准** |
| [ui-ux-p0-p6-e2e-cases.md](./ui-ux-p0-p6-e2e-cases.md) | UI UX 优化测试用例（覆盖 Phase 0-6，基于 roadmap 分期） | Active（2026-08-12 按 roadmap Phase 0-6 重写） |
| [tui-hang-fix-2026-05-27.md](./tui-hang-fix-2026-05-27.md) | TUI 卡死问题排查修复记录 | |
| [issues.md](./issues.md) | issue 清单（多数已解决） | |

---

## 常用速查

- **P0-P6 分期与完成状态** → `godex-optimization-roadmap.md`（唯一权威）
- **Web UI 可视化设计** → `p0-p4-visualization-design.md`
- **Memory 设计** → `memory-design-principles.md` + roadmap Phase 3
- **Scope 隔离** → `scope-isolation-design.md`（roadmap 6.2）
- **架构总览** → `architecture-v2-spec.md` + `project-structure.md`

## 文档维护规则

1. 新文档必须在开头注明：状态（Active/Draft/Superseded/Historical）、日期、一句话用途。
2. 内容被其他文档合并或取代时，在头部标注 `> 状态：Superseded（被 xxx 取代）`，不要静默删除。
3. 修改重要文档后，更新本索引的对应行。
