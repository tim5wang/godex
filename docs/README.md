# GoDex 文档索引（docs/）

> 本索引帮助快速定位每份文档的用途、状态与相互关系。
> 修订日志：2026-08-12 首次建立索引；统一文档状态头与修订日志约定，标注被合并/被取代的旧文档。
> 2026-08-28：补充 taskboard-plugin-design.md 条目（taskboard 插件已交付 M1/M2/M2.5）。
> 2026-08-31：补齐全部顶层文档索引；加入功能实现矩阵、全模块审查与自动 `docs-check`。
> 2026-09-03：文档整理（对标 DSH）：新增「读者速查」分层导航、新增 E. 文档组织区与整理方案（documentation-organization-plan.md）、补收 agent-template-agent-implementation-design.md 并规范化其状态头。

## 状态约定

| 状态 | 含义 | 使用建议 |
|------|------|---------|
| **Active** | 当前权威文档，以它为准 | 实现、评审、排期以此为据 |
| **Implemented** | 已落地的当前契约或实现记录，通常与 Active 组合 | 以代码/help/测试为事实源，文档解释边界 |
| **Partial** | 主链已落地但仍有明确缺口 | 先查文首实现矩阵，不把剩余设计当承诺 |
| **Draft / Plan** | 设计草案或实施计划，尚未落地或部分落地 | 阅读了解方向，实施前需确认 |
| **Superseded** | 已被其他文档合并/取代，仅保留追溯 | 不要作为当前依据 |
| **Historical** | 历史记录（修复、验证、issue），内容可能过时 | 仅作参考 |
| **Analysis** | 实测或研究资料，不定义产品契约 | 结论需回到代码、help、测试或功能矩阵验证 |

## 读者速查（Quick Start by Role）

| 读者 | 入口文档 | 说明 |
|------|---------|------|
| 终端用户 | [user-guide.md](./user-guide.md) → [extension-runtime-user-guide.md](./extension-runtime-user-guide.md) → [vscode-acp.md](./vscode-acp.md) | 安装配置、日常使用、扩展运行时（Package/MCP/ACP/WASM）、IDE 接入 |
| 开发者 | [feature-implementation-matrix.md](./feature-implementation-matrix.md) → [architecture-v2-spec.md](./architecture-v2-spec.md) → [project-structure.md](./project-structure.md) → [godex-optimization-roadmap.md](./godex-optimization-roadmap.md) | 先查功能矩阵（功能是否已实现），再看架构与路线图 |
| 部署运维 | [self-deploy.md](./self-deploy.md) → [node-onboarding.md](./node-onboarding.md) → [node-mesh-design.md](./node-mesh-design.md) | 自部署、节点接入与控制面 |
| 历史查阅 | [release-notes-v1.4.0.md](./release-notes-v1.4.0.md)、[tools_issues.md](./tools_issues.md)、下方 Superseded / Historical 区 | 发布记录、工具排障经验 |

> 文档整理方案（对标 DSH 的借鉴点、读者分层、每篇处理方式）见 [documentation-organization-plan.md](./documentation-organization-plan.md)。

---

## A. 当前权威（Active）

| 文档 | 内容 | 备注 |
|------|------|------|
| [godex-optimization-roadmap.md](./godex-optimization-roadmap.md) | **优化路线图（统一版）**，含 P0-P6 分期定义、Phase 0-6 任务与完成状态 | **P0-P6 分期以本文档为准**；合并自 qm-roadmap / longtask-analysis / roadmap-high-roi / roadmap-runtime-hardening / architecture-v2-spec / agent-role-and-bundle-design |
| [architecture-v2-spec.md](./architecture-v2-spec.md) | GoDex 2.0 架构 SPEC（中文） | 含英文版 [architecture-v2-spec.en.md](./architecture-v2-spec.en.md) |
| [user-guide.md](./user-guide.md) | 用户指南：安装运行、配置、Provider、CLI、Web UI、工具、Memory、命令、HTTP API、自动化、安全、故障排查 | README 的补充细节；2026-08-15 全量重写对齐实现 |
| [code-review-2026-08-15.md](./code-review-2026-08-15.md) | 代码与设计 Review 记录：文档↔实现不一致、代码侧发现、设计观察 | 2026-08-15 |
| [code-and-docs-review-2026-08-31.md](./code-and-docs-review-2026-08-31.md) | 全模块代码/架构/文档审查，含工具证据、测试基线与优先级 | 2026-08-31 |
| [feature-implementation-matrix.md](./feature-implementation-matrix.md) | 功能—实现—入口—权威文档单一索引 | **判断功能是否已实现时先查本文** |
| [project-structure.md](./project-structure.md) | 项目目录结构与分层规范 | |
| [memory-design-principles.md](./memory-design-principles.md) | Memory 模块目标、边界、组成与约束 | |
| [workflow-runtime.md](./workflow-runtime.md) | durable workflow runtime（长任务/多 agent） | |
| [scope-isolation-design.md](./scope-isolation-design.md) | Scope 隔离模型设计（roadmap 6.2，M1-M5 已完成） | |
| [taskboard-plugin-design.md](./taskboard-plugin-design.md) | taskboard 插件设计与实现参照：核心看板、模板执行/PJM、协作与 reconcile 分文档演进 | baseline 已实现，剩余项查功能矩阵 |
| [taskboard-collaboration-design.md](./taskboard-collaboration-design.md) | research 传递与路径冲突四闸门 baseline；依赖拓扑/经验回流仍 Planned | Active / Partial（2026-08-31 核对） |
| [taskboard-reconcile-design.md](./taskboard-reconcile-design.md) | 手动 reconcile P0 已实现；自动调度、历史、dry-run/auto-recover 仍 Planned | Active / Partial（2026-08-31 核对） |
| [agent-role-and-bundle-design.md](./agent-role-and-bundle-design.md) | AgentTemplate 人才市场、对话/TaskBoard/Biz/PJM 多入口设计与实现映射 | M1–M3、M4 P1、M5 P1–P3 已落地 |
| [per-session-workspace-plan.md](./per-session-workspace-plan.md) | Per-Session 工作目录（已实施 2026-07-31） | |
| [spec-of-chat-layout-optimize.md](./spec-of-chat-layout-optimize.md) | Web UI 聊天工作区升级 SPEC（M0/M1 已落地） | |
| [p0-p4-visualization-design.md](./p0-p4-visualization-design.md) | P0-P4 可视化设计（A1/A2/B1/B2/C1 完成，A3/B3/C2 待办） | |
| [node-onboarding.md](./node-onboarding.md) | 节点接入手册（2026-08-06） | |
| [self-deploy.md](./self-deploy.md) | 自部署指南（mycloud） | |
| [vscode-acp.md](./vscode-acp.md) | VS Code ACP Client 集成指南 | |
| [agent-step-platform-design.md](./agent-step-platform-design.md) | Agent Step 产品边界与冻结决策 | Phase A/B/C 已实现 |
| [agent-step-platform-details.md](./agent-step-platform-details.md) | Agent Step Phase A HTTP/MCP/认证/输出契约 | 已实现，契约以 routes/tests 为准 |
| [agent-step-sdk.md](./agent-step-sdk.md) | Agent Step TypeScript SDK | Phase B 已实现 |
| [business-agents-console-design.md](./business-agents-console-design.md) | Business Agents 管理台设计与实现基线 | UI/API 已存在，后续项按文档标注 |
| [extension-runtime-user-guide.md](./extension-runtime-user-guide.md) | Package/MCP/ACP/WASM 使用与信任边界 | Active |
| [compaction-optimization-plan.md](./compaction-optimization-plan.md) | Compaction Phase 1-4 实施记录与验收 | 已落地 |
| [session-timeline-inspector.md](./session-timeline-inspector.md) | Timeline inspector 数据与 UI 契约 | 阶段 1/2 已实现 |
| [agent-template-agent-implementation-design.md](./agent-template-agent-implementation-design.md) | Agent 模板指定外部 agent 内核设计方案 | M1 已实施（2026-09-02），M2/M3 待做 |

## B. 演进设计与历史实施记录（含剩余 Planned 项）

| 文档 | 内容 | 备注 |
|------|------|------|
| [research_of_dsh_for_godex_optimize.md](./research_of_dsh_for_godex_optimize.md) | DSH/Cordis → pluginrt/toolruntime/WASM/ACP 演进记录 | 阶段 0/A/B 与 P1–P4 已落地，C 为 MVP/演进 |
| [node-mesh-design.md](./node-mesh-design.md) | Node Mesh v2 实现与剩余路线 | Phase 1–3 已完成，Phase 4/5 Partial |
| [big-file-split-plan.md](./big-file-split-plan.md) | 四个大文件拆分的历史方案与当前结果 | Go 三项完成，ChatPage Partial |
| [cache-optimization-plan.md](./cache-optimization-plan.md) | Cache & Tool 优化实施记录 | 1.1–2.3 完成，adaptive TTL Planned |
| [tui-bubbletea-design.md](./tui-bubbletea-design.md) | 统一多入口架构设计记录 | Historical / Implemented |
| [voice-plugin-extensibility-design.md](./voice-plugin-extensibility-design.md) | turn middleware + plugin UI/config + voice L2 | 基础 ui_card/voice 已实现，插件化部分 Planned |
| [plugin-system-evolution-plan.md](./plugin-system-evolution-plan.md) | 插件 routes/services/schedule/UI 演进 | P-A/P-C/P-D 已实现，P-B Partial |

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
| [workflow-integration-plan.md](./workflow-integration-plan.md) | 被 Agent Step + Business Agents 接入路径取代 |
| [workflows-board-design.md](./workflows-board-design.md) | Workflows 页面已删除，只保留 UiCardView |

## D. 历史记录（Historical）

| 文档 | 内容 | 备注 |
|------|------|------|
| [p0-p6-e2e-validation.md](./p0-p6-e2e-validation.md) | 旧迭代的端到端验证清单 | **旧迭代遗留，P0-P6 定义以 roadmap 为准，勿以此文档为准** |
| [ui-ux-p0-p6-e2e-cases.md](./ui-ux-p0-p6-e2e-cases.md) | UI UX 优化测试用例（覆盖 Phase 0-6，基于 roadmap 分期） | Active（2026-08-12 按 roadmap Phase 0-6 重写） |
| [tui-hang-fix-2026-05-27.md](./tui-hang-fix-2026-05-27.md) | TUI 卡死问题排查修复记录 | |
| [issues.md](./issues.md) | issue 清单（多数已解决） | |
| [responses-protocol-plan.md](./responses-protocol-plan.md) | Responses 协议实施前方案；功能已落地 | Historical |
| [workflows-integration-guide.md](./workflows-integration-guide.md) | 旧 Workflows 第三方接入说明 | Historical |
| [release-notes-v1.3.0.md](./release-notes-v1.3.0.md) | v1.3.0 release record | Historical |
| [release-notes-v1.4.0.md](./release-notes-v1.4.0.md) | v1.4.0 release record | Historical |
| [codex-cache.md](./codex-cache.md) | Codex/Responses 端点缓存行为分析 | Analysis |
| [cache-hitrate-analysis.md](./cache-hitrate-analysis.md) | Cache hit-rate 实测分析 | Analysis（独立任务产物） |
| [tools_issues.md](./tools_issues.md) | 工具失败与规避经验日志 | Historical / Operational Log |

---

## E. 文档组织（Documentation Governance）

| 文档 | 内容 | 备注 |
|------|------|------|
| [documentation-organization-plan.md](./documentation-organization-plan.md) | 文档整理方案：DSH 对标、读者分层、分类目录与每篇处理方式 | 2026-09-03 建立，已落地部分见文内 |

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
4. 提交前执行 `make docs-check`；它会校验顶层索引覆盖、状态头和本地 Markdown 链接。
