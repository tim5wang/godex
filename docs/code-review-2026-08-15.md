# GoDex 代码与设计 Review

> 状态：Active（2026-08-15 全量 review 记录）
> 范围：`cmd/`、`internal/`、`ui/web/src/app` 与 docs/ 用户文档的一致性审查
> 方法：6 个并行深度 review（CLI 命令面 / 配置模型 / HTTP API / 工具系统 / Slash Commands 与 Channel / Agent-Memory-Subagent-Workflow），关键结论均直接对照源码验证，CLI 结论通过实际构建运行二进制复核。
> 修订日志：
> - 2026-08-15 memory deep-dive 补充发现 10（work_method/work_fact 类型校验缺口）。
> - 2026-08-15 修复 8/10 代码侧发现（#1 CLI session 解析、#3 grep 提示、#4 死环境变量、#5/#8/#9 配置重写丢字段、#7 help 文案、#10 memory 类型校验）；#2 保持现状（声明性 bundle），#6 结论为有意不拦截（已对齐注释与 roadmap）。

## 一、文档与实现不一致（已修复）

以下问题已同步修正到 `docs/user-guide.md`、`README.md`、`README.en.md`：

| # | 问题 | 证据 | 修复 |
|---|------|------|------|
| 1 | `godex repl` 被文档化但命令不存在 | `internal/app/run.go` 无 `repl` case；实际运行报 `unknown subcommand "repl"` | 用户指南删除该命令，注明交互式入口统一为 TUI |
| 2 | Agent Profile 语义描述错误：文档称 coding profile「默认只暴露核心编码、todo、tool_exchange 和必要的会话/压缩/历史工具」，需要 tool_exchange 才能启用 web/skill/memory | `tool_registration.go`：web/planning/lsp/core_code 为 default-active，memory/skill/compress/history_search 为 always-active，两 profile 工具目录一致；`system_prompt_dynamic.go`：profile 只改变提示词与运行时注入（coding 用 repo_map 替换 skill_catalog） | 文档改为「提示词策略」语义说明 |
| 3 | TUI 描述不准确（「全屏 TUI」vs「min-tui」） | `rootHelpText`：`godex` = min-tui，`godex tui` = 同一实现 | 文档改为 min-tui 全屏，默认入口 |
| 4 | HTTP API 章节严重不完整 | `httpapi.go` + routes_*.go 共 ~160 路由；文档只列了约 60 | 重写完整路由分组（含 /v1/*、terminal、files、git、preview、usage、security、providers、relay/proxy/forward/push、longtasks、fork/model/transcript/ledger 等） |
| 5 | Slash Commands 章节缺失 16 个命令 | `commands.go` `AvailableMetadata`：30 个内建命令 | 补齐 /bash、/sh、/tasks、/team、/inbox、/todos、/insights、/skills、/ledger、/model、/new、/resume、/cron、/heartbeat、/help 及 /memory 子命令 |
| 6 | 工具清单缺失 30+ 工具 | `tool_registration.go`：56 个注册工具、14 个 bundle | 重写工具系统表格（bundle → 工具 → 默认状态） |
| 7 | 配置模型缺失 memory 策略、scope 隔离、screener、loop_guard、session_backend、ssh 执行后端等 | `config/template.go`、`types.go` | 补齐 godex.yaml 顶层结构、安全模型、Memory 策略等章节 |
| 8 | 安全 profile 清单缺 `dev/repair`，未说明默认值 | `config/profiles.go`：默认 `guarded-local`，另有 `dev/repair` | 补齐 |
| 9 | Eval 断言能力描述过时 | `internal/domain/eval/types.go`：10 类断言 | 补齐 suite 格式与全部断言字段 |
| 10 | package manifest 字段描述错误（文档暗示有 author/homepage/license/tools/install 等） | `internal/core/packages/packages.go`：真实字段为 name/version/description/resources/app/permissions/capabilities/tool_policy/smoke_tests/recommended_bundles | 修正 schema |
| 11 | README.en.md 三个死链 | `docs/SPEC.en.md`、`capability-enhencement-v2.md`、`high-roi-roadmap.md` 均不存在 | 改为 `architecture-v2-spec.en.md`、`capability-enhancement-v2.md`、`roadmap-high-roi.md` |
| 12 | 目录结构过时 | README 目录树缺 10+ 个包 | 同步最新分层（toolruntime/sandbox/sessiongraph/sessionstore/domain/acp/uiassets/tui/platform…） |
| 13 | Web 工作台缺 Files、Usage | `appRegistry.tsx`：9 个 app（chat/files/automation/nodes/notes/skills/memory/settings/usage） | 补齐 |
| 14 | Cron/Heartbeat 语义缺失（job 是 agent turn 而非 shell；schedule at/every/cron；HEARTBEAT.md checklist + OK token） | `runtime/cron`、`runtime/heartbeat` | 新增「自动化」章节 |
| 15 | `godex repl` 之外还有 REPL 相关描述 | `project-structure.md` 提到 `internal/runtime` 含 REPL | README 目录树去掉 REPL |

## 二、代码层面的发现

> 状态：**2026-08-15 已修复 8/10**（除 #2「声明性 writing bundle，行为正确」与 #6「attach_file 有意不拦截」外，其余均已修复并补充回归测试）。修复清单见文末。

这些是 review 中发现的代码侧问题：

1. **全局 `--session` 覆盖子命令 `--session`**：`extractGlobalConfigArgs` 扫描整个 argv 提取 `--session`，导致 `ask/command/tui/longtask/doctor sessions/repair sessions` 的 `--session` 永远被全局值覆盖（实际运行验证：`godex command --session boguskey /session current` 返回 `local:default`）。建议改为只在子命令之前提取，或在解析时优先子命令的 flag。✅ **已修复**：`cmd/godex/main.go` 改为在首个非全局 flag 处停止扫描（全局 flag 仅在子命令之前识别），子命令的 `--session` 由各自 flag set 解析；`main_test.go` 新增 3 个回归测试。
2. **`writing` bundle 无工具注册**：`tool_registration.go` 声明 `bundleWriting` 并参与角色映射，但没有任何工具挂在该 bundle 下，不会出现在活跃目录中（属声明性能力，行为正确但易误读）。**保持现状**（声明性能力 bundle，行为正确）。
3. **`ErrToolNotFound` 提示过时**：运行时报错文本仍写着「grep is not a separate tool」，但 grep 已是独立工具且 default-active。✅ **已修复**：`internal/toolruntime/errors.go` 移除过时的 grep 特例提示；`base_test.go` 断言不再包含该文案并反向断言其不出现。
4. **死环境变量**：`GODEX_LIGHTPANDA_*`（8 个）与 `GODEX_TOOLS_EXECUTION_SCOPE_WRITE` 在 schema/template 中声明但 resolver 未接线；`GODEX_SECURITY_SCREENER_*` 已接线但 UI schema 未注册。✅ **已修复**：`resolve.go` 补齐 lightpanda 8 项 + `tools.execution.scope_write` 的 env 接线；`schema.go` 的 security section 注册 screener 5 项。
5. **Web 重写配置会丢字段**：`control.forward_allow`、`memory.*`、`control.credential`、`tools.web_search.serpapi.api_key` 未出现在 config template 中，Web UI 保存配置时会丢失这些字段（template 是重写时的 schema 来源）。✅ **已修复**：`template.go` 渲染 `control.credential`/`control.forward_allow`/`memory.*`/`serpapi.api_key`；**根因是 `applyStoredValues` 只应用 schema 注册字段**——已在 `schema.go` 新增 `memory` section（strategy/consolidate_after/session_scope），`values.go` 补 `control.forward_allow`/`memory.session_scope` 的 apply 与 stored/effective 视图映射。
6. **scope 写拦截器只覆盖 write_file/edit_file**：roadmap 6.2 描述含 attach_file，但 `NewScopeWriteInterceptor` 注册列表只有 write_file/edit_file。**结论：attach_file 有意不拦截**——它不写 workspace（读取经 workspacefs 边界 + 只读 allowlist 约束，写入目标是 session attachments），额外拦截会错误阻止外部 allowlist 文件附件。已同步修正 `scope_path.go` 注释与 roadmap 6.2 描述（与 WIP 中 `tool_registration.go` 已移除 attach_file 的行为一致）。
7. **min-tui 与「full bubbletea TUI」混淆**：rootHelp 把 `godex` 描述为 lightweight min-tui、`godex tui` 为 full bubbletea TUI，实际两者是同一个 mintui 实现（基于 `github.com/tim5wang/min-tui`）。✅ **已修复**：`run.go` `rootHelpText` 改为「fullscreen min-tui」同一实现的准确描述。
8. **配置文件 template 缺 memory section**：`memory.*` 被解析但未渲染进生成的 `godex.yaml`（memory_config_test 有默认值测试），用户从文件里看不到该 section。✅ **已修复**：`template.go` 末尾渲染 `memory` section（strategy/consolidate_after/session_scope），新增渲染测试。
9. **`control.nodes` 与 `forward_allow` 文档字段对齐**：node-onboarding.md 与 template 字段名一致（node_id/trust_level/forward_allow），但 `applyStoredValues` 未包含 forward_allow/session_scope，重写会丢。✅ **已修复**：`values.go` 补 `control.forward_allow` 与 `memory.session_scope` 的 apply + stored/effective 视图映射，新增 round-trip 测试。
10. **memory 类型校验缺口（潜在 bug）**：`memory.Type` 与 `memory` 工具枚举、project miner、相关性打分都支持 `work_method`/`work_fact`，但 `validateSaveInput`（`manager.go:828-844`）的 `Remember`/`Update` 只放行 identity/user/workflow/project/warning。project miner 按文件名推断出的 `work_method`/`work_fact` 候选（如 docs/recipe.md → work_method）在 inbox 中 `accept` 时会经 `AcceptCandidateWithOptions → Remember → validateSaveInput` 报 `unsupported memory type "work_method"`。✅ **已修复**：`validateSaveInput` 放行 `TypeWorkMethod`/`TypeWorkFact`；`extract_test.go` 新增 work_method/work_fact 候选 accept+update 回归测试。（2026-08-15 memory deep-dive 补充）

## 三、设计层面的观察

1. **架构演进方向清晰**：`internal/agent/agent.go` 正按 roadmap 3.3 拆解（Sandbox 接口、Harness 引擎、agent graph、role-bundle 映射、scope 隔离、session graph 均已落地），向 2.0 的「Agent/Sandbox、Orchestrator/Worker、Session 树、存储解耦」逐步收敛。当前 `sessionstore`（JSON/SQLite 双后端）与 `sessiongraph`（分支/回滚/合并）已是 2.0 的前置实现。
2. **三层工具组织模型已基本落地**：原子工具 → bundle → 角色（能力边界 = 工具集合 × 权限策略 × 上下文预算），角色→bundle 运行时映射、bundle 继承/覆盖、写 scope 联动、按角色上下文预算均有测试覆盖。
3. **韧性设计扎实**：idempotency（cron/heartbeat 幂等 key）、worker lease（TTL/3 心跳，崩溃标记 interrupted 不自动重跑）、turn error 分层（Retryable/Transient/NonRetryable）、loop guard（no-mutation 螺旋检测）、journal 轮转与 checkpoint、重启恢复（sweep/重建/resume-run-id）。
4. **安全分层完整**：security profile + shell guard + WorkspaceFS + scope 写拦截 + approval（manual/review/yolo）+ content screener（shadow 模式审计）+ security audit，形成纵深。
5. **一致性提示**：
   - 文档状态约定（docs/README.md）要求「内容被合并/取代时标注 Superseded 并更新索引」，本次 review 遵循该约定。
   - roadmap 的 6.1/6.2/6.3/6.4/6.5 已标记完成，但优先级矩阵里 6.1/6.3/6.5 仍标 ⚪，建议下次 roadmap 修订时统一。
   - `internal/runtime` 目录仍被 README 描述为含 REPL（已修正 README），实际 REPL 相关代码已不在该目录。

## 四、Review 产出

- `docs/user-guide.md`：全量重写（CLI/配置/工具/Slash/HTTP API/自动化/安全/故障排查）
- `README.md`、`README.en.md`：核心特性、Web 工作台、Agent Profile、目录结构修正
- 本文档：review 记录与后续处理建议
