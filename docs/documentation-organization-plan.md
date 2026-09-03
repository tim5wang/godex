# godex 文档整理方案（对标 DSH）

> 状态：Draft（2026-09-03 建立，部分落地）
> 日期：2026-09-03
> 一句话用途：以 DSH 文档（deepseekdocs.com）为对标，明确 godex 文档的读者分层、顶层入口、分类目录与每篇处理方式，并记录已落地改进与待办。

---

## 一、背景与目标

现状：`docs/` 下 57 篇顶层 Markdown + 索引 `README.md`，总量不小但读者难以快速定位「该读哪篇」。目标读者（终端用户 / 开发者 / 部署运维）入口不区分，Superseded/Historical 文档与 Active 文档混在根 README 推荐列表里。

目标：**结构清晰**（读者 30 秒内找到入口）+ **内容详细**（每篇聚焦一个主题，链接可循）。遵循最小改动，不做全量重写。

## 二、DSH 文档结构调研（可借鉴点）

调研对象：https://deepseekdocs.com/docs/guides/acp-automation-server 及同站 guide 页（deepseek-harness-status）。

| 借鉴点 | DSH 做法 | godex 落地 |
|--------|----------|-----------|
| 每页顶部 TL;DR | 页首「本页总览 > 一句话版」+ 核验日期，30 秒判断是否所需 | 索引每行已有一句话「内容」；重要页首已有状态/修订日志（保留） |
| 按读者/主题分区导航 | guides / reference 等分类，侧边栏清晰 | docs/README.md 新增「读者速查」四类入口（终端/开发/运维/历史） |
| 每篇聚焦单一主题 | 一篇一主题，交叉引用相关页 | 现有文档大多单主题；user-guide 偏大但保持单篇（避免拆分风险） |
| 状态与时效标注 | 标注源码/npm 版本、核验日期 | 已有状态约定（Active/Partial/Superseded/Historical…）+ `make docs-check` 强制 |
| 示例与编排 | 大量可复制命令/配置示例 | user-guide 已含命令与配置示例；待办：为关键页补示例锚点 |

## 三、现状盘点

- 顶层 Markdown：57 篇（含 README.md 索引）；另有 `_images/`、`superpowers/` 子目录。
- 索引 `docs/README.md` 已按状态分类（A 权威 / B 演进设计 / C 已合并 / D 历史），并维护常用速查与维护规则。
- 自动校验：`make docs-check`（scripts/check_docs.sh）检查①顶层文档全量被索引 ②状态头合规 ③本地 md 链接有效 ④3 个关键实现事实。
- 本次发现的 2 个存量问题（已修复）：`agent-template-agent-implementation-design.md` 漏索引；其状态头用词（"已实施"）不在识别词表内。

## 四、目标读者分层

| 读者 | 关注 | 推荐入口 |
|------|------|---------|
| 终端用户 | 装起来、用起来（CLI/TUI/Web/IM）、配置、扩展 | user-guide → extension-runtime-user-guide → vscode-acp |
| 开发者 | 架构、功能是否已实现、设计文档、路线图、评审 | feature-implementation-matrix → architecture-v2-spec → project-structure → godex-optimization-roadmap → 各 design 文档 |
| 部署运维 | 自部署、节点接入、控制面 | self-deploy → node-onboarding → node-mesh-design |
| 历史查阅 | release notes、排障经验、被取代的旧方案 | release-notes-v1.4.0 / tools_issues / Superseded、Historical 区 |

## 五、重构方案：顶层入口 + 分类目录 + 每篇处理方式

**顶层入口设计（3 层）**：

1. 仓库根 `README.md`：产品总览 + 快速开始 + **按读者分三类的短入口**（已落地），不再平铺全部文档。
2. `docs/README.md`：唯一权威索引（读者速查 + A/B/C/D/E 分类 + 速查 + 维护规则）（已落地）。
3. 各分类文档：单主题、状态头合规、被索引覆盖。

**每篇处理方式**（沿用 = 保持现状无需改；修复 = 本次已修的存量问题；新增 = 本次新增；其余潜在重写/合并列入待办，不擅自执行）：

| 处理 | 文档 |
|------|------|
| **沿用（权威/Active）** | godex-optimization-roadmap、architecture-v2-spec(.en)、user-guide、feature-implementation-matrix、project-structure、memory-design-principles、workflow-runtime、scope-isolation-design、taskboard-plugin-design、taskboard-collaboration-design、taskboard-reconcile-design、agent-role-and-bundle-design、per-session-workspace-plan、spec-of-chat-layout-optimize、p0-p4-visualization-design、node-onboarding、self-deploy、vscode-acp、agent-step-platform-design/details/sdk、business-agents-console-design、extension-runtime-user-guide、compaction-optimization-plan、session-timeline-inspector、code-review-2026-08-15、code-and-docs-review-2026-08-31 |
| **沿用（演进/历史记录）** | research_of_dsh_for_godex_optimize、node-mesh-design、big-file-split-plan、cache-optimization-plan、tui-bubbletea-design、voice-plugin-extensibility-design、plugin-system-evolution-plan、ui-ux-p0-p6-e2e-cases、p0-p6-e2e-validation、tui-hang-fix、issues、responses-protocol-plan、workflows-integration-guide、release-notes-v1.3.0/v1.4.0、codex-cache、cache-hitrate-analysis、tools_issues |
| **沿用（Superseded，仅追溯）** | qm-roadmap、longtask-analysis、roadmap-high-roi、roadmap-runtime-hardening、agent-template-board-design、capability-enhancement-v1/v2、workflow-integration-plan、workflows-board-design |
| **修复（本次）** | agent-template-agent-implementation-design.md（补索引 + 状态头规范化） |
| **新增（本次）** | documentation-organization-plan.md（本文档） |
| **重写/合并（待办，未执行）** | user-guide 可选按主题拆分子页；capability-enhancement-v2 与 roadmap 的关系待复核后标注或合并 |

## 六、已落地改进（2026-09-03）

1. 根 `README.md`「文档」节重构：按读者三类入口 + 指向 docs/README.md 完整索引，移除 Superseded 文档平铺。
2. 根 `README.en.md` 同步英文版入口。
3. `docs/README.md`：新增「读者速查」分层导航；新增 E. 文档组织区收录本文档；补收 agent-template-agent-implementation-design.md 索引行；修订日志更新。
4. 修复 `agent-template-agent-implementation-design.md` 状态头（Partial + 已实施说明），docs-check 恢复全绿。

## 七、待办

- [ ] user-guide 拆分评估（体积 ~60KB，可按 CLI/Web/API/安全拆子页，需同步索引与交叉引用）。
- [ ] 关键页（self-deploy、vscode-acp、extension-runtime-user-guide）补「可复制示例」锚点，对齐 DSH 示例密度。
- [ ] 英文文档镜像范围确认（当前仅 architecture-v2-spec.en / README.en）。
- [ ] 定期跑 `make docs-check` 并纳入 verify 门禁（verify 已含 docs-check）。
