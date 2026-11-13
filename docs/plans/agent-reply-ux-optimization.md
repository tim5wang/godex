# GoDex Agent 回复体验优化方案（研究稿）

日期：2026-11-13（Asia/Shanghai）。本轮仅研究与写方案；没有实现、commit/push、部署或另行委派。
核查基点：HEAD 11c7842acb6d25db7f1a3f68c93ffffbd1a8ce73 加当前工作区；工作区有既存 memory/context/UI 等修改，不视为干净，也未修改这些文件。行号对应本轮快照，合并前应复核。

## 1. 执行摘要

目标不是“每条都短”，而是让用户先知道结论/状态，再按需获得证据、不确定性和行动入口。普通问答允许一句话；调研和方案比较允许充分展开。用户自然语言要求可以覆盖篇幅和组织默认值，不能覆盖诚实和授权边界。
GoDex 已有简洁回复指令、coding profile、模板/skill 注入、工具过程折叠和长任务交付入口。应复用这些机制，避免新增通用 response schema、规则引擎或二次 LLM 改写链。
最需要防止的是：过程文字和答案混淆、重复进展挤占注意力，以及把局部验证写成整体可用；不应以隐藏失败或丢弃证据换取“干净界面”。

### 最值得先做的 3 件事

1. **P0：统一“结果—证据边界—下一步”内容策略。** 修改已有风格提示而非追加相互竞争的规范；按六类场景选择内容，不强制标题。交付明确区分实现、测试、界面实测、部署。
2. **P0：把进展报告改为有变化才报告，保留真正阻塞和用户指定频率。** 启动说明责任与边界；无变化不反复长报；失败只报告事实、影响、恢复条件，不循环解释。
3. **P1：沿用现有消息分组，确认最后答案稳定可见。** 统一流式/恢复/复制路径；过程可展开，但失败、等待用户和最终结论不能被折叠掉。

本报告后续给出源码链路、8 项问题、分期工作包、中文策略与 6 组示例，以及 20 个离线评测场景。所有验收是建议，非已取得的效果；本轮未执行用户实验或界面端到端测试。

## 2. 外部证据与适用边界

访问日期均为 **2026-11-13**。严格限定为 2 次 web_search、3 次 web_fetch；不重复查询/URL，不为凑来源继续抓取。共使用 5 个不同机构/官方文档来源条目，其中 Microsoft、PAIR 为搜索工具返回的一手页面可读预览，另外三项为实际抓取；GOV.UK 重定向后仅取得短正文，故不把未读的详细写作规则当证据。
以下均是设计指南/官方行为规范，不是 GoDex 的实验证据；未核查其引用的原始实验，亦不引用量化改善。

| 来源、标题与实际 URL | 核查内容/性质 | 映射建议（本报告推断） |
| --- | --- | --- |
| S1 Microsoft Learn：Use guidelines for human-AI interaction（葡语官方课程，英文指南表）<https://learn.microsoft.com/pt-pt/training/modules/introduction-to-microsofts-responsible-ai-approach/3-use-guidelines-for-human-ai-interaction> | 搜索访问带课程 enrollment 查询参数的同页；可读预览列出能力/可靠性预期、适时服务、上下文相关信息、有效纠错、存疑时收窄范围、用户控制。属于 HAX 相关指南的官方转述；未查原论文。 | 状态说明要有能力边界；报错应指向恢复和纠错；不要把每次工具活动都变成用户通知。 |
| S2 Google PAIR：Explainability + Trust <https://pair.withgoogle.com/guidebook-v2/chapter/explainability-trust/> | 可读预览提出信任程度、何时解释、无法解释时怎么办、如何表达置信等问题；属于设计指南，仅使用已读导言。 | 解释服务于用户决策：给可核查证据与局限，不展示内部思维链，不把自报信心当成功证明。 |
| S3 Nielsen Norman Group：Progressive Disclosure <https://www.nngroup.com/articles/progressive-disclosure/> | 正文：先展示最重要选项，次要信息按需展开；入口须明显、标签须说明内容；通常不要超过两层。是专家设计指南，非本项目实验。 | 答案优先、工具过程次级；“查看验证详情/失败详情”比“更多”明确；不能折叠掉频繁需要的状态。 |
| S4 GOV.UK：Writing guidelines <https://guidance.publishing.service.gov.uk/writing-to-gov-uk-standards/writing-guidelines/>（请求旧 writing-for-gov-uk URL 后重定向） | 实际短正文仅确认：内容应帮助用户找到并理解所需信息，具体词语/格式另见 A–Z。未据此声称已核查详细 plain-English 规则。 | 用用户任务命名入口、减少内部术语；以下具体中文措辞是本报告建议，而非逐条引用 GOV.UK。 |
| S5 OpenAI：Model Spec (2025/12/18) <https://model-spec.openai.com/2025-12-18.html> | 已读概览/目录及返回片段：诚实透明、表达不确定性、清晰直接、充分而高效；私有思维链属于特权信息；文档描述意图，不保证所有生产模型一致。 | 可说“已测什么、未测什么、为什么需要你决定”，不输出内部推理；按用户目标调节长度，不把简洁误解成省略失败。 |

外部原则与 GoDex 的具体修改之间均是**设计推断**，必须经过本项目离线回归和真实用户任务验证。尤其“更短”“更多折叠”不等于理解更好。

## 3. 当前实现：从提示到用户看到的答案

### 3.1 核查范围与说明加载

- 先找真实入口，再读限定函数；环境没有 rg，改用 find/grep/sed。未进行全仓内容扫描、未读 memory 目录作行为事实。
- 目标报告此前不存在；根目录、docs、docs/plans 无 AGENTS.md/AGENT.md；internal、ui、cmd、mobile、desktop、docs 和工作区 .godex 的限定文件名检查也未发现此类文件。
- 运行时加载器另支持 workspace 的 AGENTS.md（优先于 AGENT.md）、stateDir 的 rules 与 AGENTS.local.md/AGENT.local.md：internal/core/instructions/loader.go:24-58，Load。stateDir 不必等于工作区 .godex；本轮未读取用户机器外部状态目录，不能排除实际会话额外规则。
- 注入点 buildInstructionPrompt 将已加载说明作为持久项目/本地指令加入系统提示：internal/agent/system_prompt_dynamic.go:411-433；不能仅凭仓库无 AGENTS 就说生产没有说明注入。

### 3.2 Prompt/profile/template/skill 链路

1. buildContext 从会话取 profile，先生成 system，再取 memory、动态段和运行时消息：internal/agent/context.go:50-106。该文件本来就有用户修改，本轮只读当前快照。
2. buildRuntimeSystemPrompt 顺序为 buildSystemPrompt → conciseDefaultResponsePrompt → 能力实探/失败及时报告 → dynamic：context.go:392-409。现有简洁指令已禁止稳定上下文复述、原始工具输出、私有推理和常规调用旁白；不是从零缺规范。
3. buildDynamicSystemPrompt 顺序是 template persona → instructions → coding profile → capability guidance → template base：system_prompt_dynamic.go:70-82。effectiveTemplateProfile 允许模板覆盖传入 profile：session_template.go:159-167。
4. coding 指导已有“结果、变更、阻塞、下一步优先”“不讲常规过程”“先读再改再最小验证”：system_prompt_dynamic.go:369-385。coder.yaml:8-15 又有 persona 中先读代码、简述计划、profile=coding；researcher.yaml:9-14 则强调充分研究、URL 和事实/推断分离。这些可兼容，但执行时需场景化解释，不能让“先说明计划”变成每次工具前长旁白。
5. runtime sections 在不 trim 时加入 skill catalog/active skills，再加环境、工具可用性和 plugin 段：system_prompt_dynamic.go:85-108；active skill 正文构建见 :877 起。会话命名/default skills 入口为 runtime.go:1244-1248、1277-1279。插件/用户实际安装 skill 的内容本轮未逐个核查。
6. wear-slim.yaml:21-27 已有腕上结论先行、三行内、避免长代码/横向表格。它是专门模板，不能把三行限制移植到所有会话。
7. PromptLayers.Build（internal/core/conversation/prompt.go:12-20）只是层串接辅助；本轮以 agent 的实际 buildContext 为主链，未将辅助函数误当唯一注入器。

### 3.3 Assistant、工具、SSE、落盘与恢复

- runtime.go:961-998 将 assistant text/thinking delta 发送到共享事件；:1031-1035 的 message-completed 包含 text 和累计 thinking。internal/domain/events/events.go:17-39 定义 text/tool/warning/error/turn 事件；TextPayload:88-95 只有 role/text/thinking，**没有 commentary/final 的显式用途字段**。这里的“最后答案”主要是消息顺序与前端选择，不是本对话 API channel 的等价保证。
- persistentTimelineSink.Emit 先写内存 recorder，Recordable 才进入 journal；thinking delta 不单独落 journal，但并未在此剥离 message-completed 的 Thinking：backend.go:495-509。journal_batch.go:102-148 有批量落盘/事件归并，不应为展示简洁删除原始工具审计记录。
- readSessionTimeline 合并基础 timeline 和 journal，按事件去重；journal 解码见 load.go:717-730、748-783、813-845。recoverInterruptedTurn:1005 起处理重启中断，不可将恢复后的“已中断”写成“已完成”。本轮未做崩溃注入实验。
- serveSessionEventStream 支持 replay=active/turn_id、事件订阅和心跳：internal/runtime/httpapi/routes_step_track.go:58-115。Web streamEvents 请求 active replay：ui/web/src/lib/sse.ts:18-52，解析 JSON 见 :124；不是带 Last-Event-ID 的完整消息游标协议。
- Web 会话订阅校验 session_id、把事件写 store/timeline；snapshot_ready 刷新 snapshot 等数据，turn_completed 更新会话元信息：useChatSessionState.tsx:577-638。应通过回放测试查重复/断线一致性，而非断言 SSE 已保证 exactly-once。

### 3.4 Web 信息组织、通知与交付复用点

- groupFeedItemsIntoTurns 基于**相邻项**，不是 turn_id；assistant/background/tool/todo 可合为一组，warning/error/subagent 等中断：ui/web/src/store/chatFeedUtils.ts:20-120。历史 snapshot 为 assistant 合成 msg-index turnId：store/chat.ts:526-550。
- splitTurnSegments 默认找最后非 thinking 文本；遇 bookkeeping 与前后长度差满足条件，保留较早报告并把尾句一并显示：chatFeedUtils.ts:168-201、221-263。finalBody 由同一拆分结果生成，复制/存笔记/朗读因而有复用基础：:28-44；MessageFeedV2.tsx:646-661、710-715；useChatSessionState.tsx:290-291。
- 现有过滤器按工具名/action 移除 checkpoint/ack/propose_fact/todo_patch，并未检查成功失败：chatFeedUtils.ts:139-147、245-254。这是显示层过滤，不等于原始记录删除，但仍可能让用户漏看失败。
- FeedItemBody 使用同一 split；TurnProcess 在运行时展开、结束后默认收起，用户可切换：MessageFeedV2.tsx:207-260、435-467。因此 P1 是修正边界和摘要，不是另造聊天组件。
- ThinkingSegment 可展开文字：MessageFeedV2.tsx:477-500；EventDetailPanel.tsx:132-158 也可渲染 payload.thinking。本轮确认存在可显示路径，**未确认某模型真实返回的是公开推理摘要还是私有推理**；不能把折叠称为安全隔离。
- MarkdownRenderer.tsx:1-49 使用 GFM、math、remarkBreaks、raw HTML 后 sanitize 与外链处理。单换行会形成换行展示；应鼓励真正分段，不以模型输出大量短行替代内容结构。未在手机实测横向表格和代码块。
- LongTaskRefluxBubble.tsx:24-36 已有 600 字符初始截断、展开及动作 props；LongTaskCard.tsx:75-98 已有取消/恢复/查找/finalize/rollback 等入口。600 字符是现行实现阈值，不是研究证明的最佳长度；错误/关键动作位置需回归。
- notifications.ts:47-54 将 API/普通错误用于 toast；会话订阅 turn_completed 主要刷新列表元信息，不应臆称每个事件均触发系统通知。系统通知、移动推送的跨端去重本轮未端到端验证。

### 3.5 委派、cron 与不同终端

- recordSubagentProgress 每次写进展后 emit：subagent_events.go:49-57；appendBoundedSubagentProgress:213-231 限条数，不等于按语义去重。subagent_run.go:652-680 区分超时/取消/审批/空交付，最后交付取 LastAssistantText:672-674。不能靠“completed”就认定主线已集成。
- subagent_tool.go:437-440、717-723 有结果 preview/digest，全文仍可查。预览截断和主线二次复述有丢失局限、重复信息风险；未证实每次进展都向用户发长消息。
- cron/service.go:475-494、545-560 已支持 watchdog/script/directive 抑制唤醒；不能建议重建一套抑制框架。buildCronPrompt:776-787 要求生成可投递结果；replyPlanCollector.Emit:807-825 拼接所有 text delta，Plan:849-855 同时保留 notices/tools/artifacts。因此 Web 的 finalBody 规则不会自动覆盖 cron 文本。
- 未找到“固定每 10 分钟长报告”的默认实现证据；周期、用户 job 内容和 watchdog 配置决定实际行为。停用失败本轮也未重现。
- CLI consolePrinter.HandleEvent：internal/app/run.go:1698-1737 直接打印 text delta、工具完成输出前 200 字节，以及 warning/error；没有 Web 的过程折叠。工具结果分支未专门打印 payload.Error，应测试失败输出为空时能否被其他 notice 补足。
- mobile/src/main.ts:48-54 的 navigateToServer 加载服务端 Web UI；是复用 Web 渲染的证据，不是移动交互已验证。独立 TUI 未在这些入口确认，不声称存在另一套实现。手表模板单独约束，不代表其他移动端都用 wear-slim。

## 4. 优先级问题清单（8 项）

“已证实”只针对所述代码机制，不等于已在生产复现用户损害；风险推断和待验证分别标明。

### I1 / P0：已有简洁要求，但缺少一致的场景化内容边界【已证实指令并存；效果风险推断】
- 用户影响：简单提问可能收到计划旁白；复杂报告又可能因“compact”省略必要证据；交付枚举文件而没讲用户能做什么。
- 定位：context.go:392-409；system_prompt_dynamic.go:70-108、369-385；templates/builtin/coder.yaml:8-15、researcher.yaml:9-14（均位于 internal/agent 或 internal/core，如 §3）。
- 根因/最小改动面：多个 prompt 段字符串叠加；已有方向正确，但未明确何时说状态、何时给完整报告、何时免标题。只改默认风格段和必要的模板重叠语句，不改 memory 系统。
- 建议：用 §6 策略替换/整合已有内容；规定用户目标优先、事实无损、责任明确；模板只保留角色/设备特有部分。
- 验收：普通问答不加无意义标题；研究要求不被压成几行；“只要摘要”不列内部任务 ID；用户要求详细时能展开，事实与授权不变。

### I2 / P0→P1：最后答案仍依靠顺序与长度启发式【已证实；误分组风险待回放】
- 用户影响：工具前的报告、工具后的短结论/决定可能错位；复制和朗读若选错，会把关键行动留在过程区。
- 定位：events.go:88-95 无用途字段；chatFeedUtils.ts:168-201、221-263 的尾句判断需要 bookkeeping 和长度差（两倍且差至少 80 字符）；:28-44 生成 finalBody。
- 根因/最小改动面：通用文本事件未标语义，历史恢复又没有真实 turn_id；前端为历史输出形状做补偿。当前 work_context_tool.go:36 和 work_context_feedback.go:200 已要求必要调用在 final 前完成；前端旧注释的“report then record”不是当前契约。
- 建议：P0 明确“必要读写/ack 结束后再给最终交付”；P1 先为现有 split 增加反例测试，不按长短判断重要性，不移除兼容旧历史的逻辑。确有无法消除歧义时才考虑轻量可选 message purpose 标记，非 P0 前置条件。
- 验收：长报告→checkpoint→ack→短确认、短真实决定、长失败说明、最后一段问题等均可见；流式/刷新/复制/存笔记/朗读内容相符，不吞最终短结论。

### I3 / P0→P1：台账调用过滤不检查失败【已证实代码缺少条件；生产影响待验证】
- 用户影响：checkpoint/ack/事实写入冲突或失败可能从主会话消失，用户误认为已保存/已同步。
- 定位：chatFeedUtils.ts:139-147、245-254；工具失败状态在 FeedItem 上有承载路径，现有过滤没有读取它。
- 根因/最小改动面：把 action 视为无条件无信息量的 bookkeeping，且误将事实变更与确认一并隐藏。最小改 split 的展示条件与失败摘要，不动落盘事实。
- 建议：只淡化成功且确属无关的内部记录；失败/未知/待审批必须保留可见状态、影响与详情入口；propose_fact/todo_patch 的有意义变化不默认等同无关 ack。
- 验收：模拟版本冲突、权限拒绝、写盘失败，默认层至少显示“未保存/未确认”；详情仍能找到原始工具错误；普通成功 ack 不制造额外长气泡。

### I4 / P0：thinking 字段有展示及持久化路径，需明确语义【已证实路径；内容敏感性待验证】
- 用户影响：技术过程淹没答案；若某供应商字段是真正私有推理，折叠后仍可展开，不符合不公开内部思维链的边界。
- 定位：runtime.go:974-998、1031-1035；domain/events/events.go:88-95；backend.go:503-509；MessageFeedV2.tsx:477-500；EventDetailPanel.tsx:132-158。
- 根因/最小改动面：供应商思考/公开摘要/“正在处理”被同一个字段消费；仅跳过 delta journal 不等于禁止完整 thinking 持久化或显示。
- 建议：先按 provider 核查字段契约；普通回复展示可核查证据，不增加“公开推理”功能。仅允许明确面向用户的摘要进入可见区；私有内容需在适配/发事件边界阻止传播，不靠 UI 折叠；旧数据治理另做授权评估。
- 验收：用合成私有哨兵串验证普通会话、SSE、导出/详情均不出现；公开证据摘要仍可读，失败不被一并删除。未核清前不把此字段当作“可靠解释”产品化。

### I5 / P1：cron 的交付文本与 Web 最终答案选择不同【已证实；冗长风险推断】
- 用户影响：先“我将检查”、再阶段结果、最后结论被全部拼成通知；web 看似简洁但定时消息仍长，且可能重复已知背景。
- 定位：cron/service.go:776-787、807-825、849-855；已有抑制复用点 :475-494、545-560。
- 根因/最小改动面：collector 消费所有 text delta；抑制是否生效取决于 job/watchdog，并非通用“无变化不投递”。不应猜一个固定十分钟全局默认。
- 建议：先改 cron 内容提示与具体 job 的变化条件，保留用户要求的定频心跳；P1 用完成消息/已持久化最终文本替代无区分拼接，显式保留 notices 与失败状态，处理 stream reset/中断。
- 验收：三次无变化不产生三篇长报；关键变化/失败必报；取消/投递失败不能标完成；重放 text delta 与 completed 不导致重复；不依赖模型输出魔法静默口令。

### I6 / P1：委派“执行完成”与主线“可交付”容易混淆【已证实状态/预览机制；重复与责任风险推断】
- 用户影响：用户想实现，主线却长期停在“待你验收”；或 subagent/任务中心/主线分别重复交付同一报告。
- 定位：subagent_run.go:652-680（异常和 LastAssistantText）；subagent_events.go:49-57、213-231；subagent_tool.go:437-440、717-723；LongTaskCard.tsx:75-98。
- 根因/最小改动面：细粒度 progress 与用户里程碑不同，preview 不保证包含尾部局限，执行/审阅/合入等是不同状态。未证实当前默认一定要求用户逐项验收。
- 建议：主线只汇总新结果/阻塞/决策，拿到子任务结果后继续已授权的集成验证；仅新权限、破坏性操作、重大不可逆取舍才请用户决定。复用任务卡的恢复/审阅入口，不新造工作流。
- 验收：同一异步完成只产生一次交付摘要，主线说清“已产出/待复核/已合入”的实际状态；无须决策时不以“请验收”停止；尾部局限不会因 preview 截断丢失。

### I7 / P0：验证层级没有产品级统一口径【已证实提示只要求有用验证；错误结论属待验证】
- 用户影响：build/单测成功被误读为界面流程可用或已经部署；未运行/未部署被沉没到长报告最后。
- 定位：system_prompt_dynamic.go:350、371-373；LongTaskCard.tsx:14-15 的 error 与状态可承载部分事实，但没有自动证明 UI 可用。源码未显示“单测通过应宣称界面可用”的规则。
- 根因/最小改动面：模型把局部命令成功外推成总结果；“完成”一词混合实现与验收。首先改内容指令、测试样例，不先建设全局验证 schema。
- 建议：声明“实现/测试/界面实测/部署”中与当前任务有关的层级，给命令/目标/范围和未验证项；用户不需要时不列所有内部文件。失败先讲影响，成功不扩张范围。
- 验收：只有单测和 build 日志时不得出现“界面已可用/线上已恢复”；无浏览器/设备时明确未实测；需要并已授权的验证由 Agent 继续，不把能做的验证推给用户。

### I8 / P1→P2：跨端的信息密度与动作可发现性不一致【已证实渲染差异；可用性待实验】
- 用户影响：CLI 被工具片段打断，手机长表格难读，截断长任务摘要可能把限制和按钮放在下方。
- 定位：app/run.go:1702-1737；mobile/src/main.ts:48-54；MarkdownRenderer.tsx:35-49；LongTaskRefluxBubble.tsx:24-36；wear-slim.yaml:21-27。
- 根因/最小改动面：CLI 直接输出且截 200 字节，Web 有分组；Markdown 支持不代表小屏可读；按字符截断不认识语义。先以内容策略兼容各端，随后修具体组件/CSS/consolePrinter。
- 建议：CLI 默认结果与失败摘要、详细工具过程按显式选项；小屏用短列表替代宽比较表；摘要前置阻塞/限制并保留明确“查看完整结果”入口；无实际材料不显示虚假下载动作。
- 验收：CLI 工具 Error 非空且 Output 为空仍能知道失败；中文不因字节截断破坏编码；360px、键盘、屏幕阅读器可读主状态和动作；复制内容不受视觉截断影响。

## 5. 分阶段实施方案

成本为相对工程量：S=单一提示/局部组件加样例，M=跨层兼容与回放，L=跨端或数据治理；不是工期承诺。每项均为建议，按独立小变更上线，禁止顺带整理现有 memory 修改。

### P0-A：场景化回复策略与验证口径（对应 I1/I7）
- 目标：首屏能回答“结果是什么、证据到哪、下一步谁做”；允许详细调研，不定全局字数上限。
- 路径/归属：后端 prompt；context.go 的 conciseDefaultResponsePrompt、system_prompt_dynamic.go 的 coding guidance，以及 coder/researcher 模板确有重复的部分。Web 无需等待。
- 依赖/成本：依赖策略评审和固定样例；**S**，主要是原有字符串整合与 prompt 断言，不变协议。
- 收益/风险：同时改善所有复用 prompt 的终端；风险是约束堆积或过分保守。只保留一份基础策略，角色模板不复写全套。
- 验收：§7 的问答、交付、未部署、用户自由覆盖样例通过；不编造 UI/生产验证；已授权实现不无故停等。
- 回滚：独立恢复这一组提示/模板到已记录版本，离线保留前后结果；不触碰会话事实和工具结果。

### P0-B：有信息增量的进展与失败表达（对应 I5/I6）
- 目标：启动一次、里程碑/阻塞/决策及时报；无变化不长报，用户明确要求每十分钟通知时最多给诚实短心跳。
- 路径/归属：后端内容指导、cron buildCronPrompt、主线委派指导；复用已有 watchdog/directive 与任务状态，不引入全局语义去重 LLM。
- 依赖/成本：先确认 job 是提醒还是状态巡检；**S**（提示和配置说明），若修改 collector 则另列 P1-B。
- 收益/风险：减少重复打扰；风险是“安静”被误认为任务停了。仍保留运行状态、最后活动时间、用户手动查询和真正阻塞通知。
- 验收：三次无变化巡检不三次复述背景；新失败即时报告且不被静默；提醒类任务按用户约定触发；停用失败不能宣称已停。
- 回滚：分别回退 job/prompt 的报告策略，保留原 watchdog、安全错误与投递日志，不删除定时任务替代回滚。

### P0-C：澄清 thinking 字段与展示边界（对应 I4）
- 目标：不公开内部思维链；用户能获得证据和简短理由，而不是内部推演日志。
- 路径/归属：后端 provider→runtime→events 的边界核查；前端 ThinkingSegment/EventDetailPanel 的消费契约。当前并无已验证统一字段语义，先盘点受支持供应商再实现。
- 依赖/成本：供应商官方字段定义、兼容用例；**M**，需要兼顾合法公开摘要、历史记录和权限边界，不能仅删一个组件了事。
- 收益/风险：缩小敏感信息传播面；风险是误删用户需要的公开依据。工具证据/错误与 thinking 分开，不以“安全”理由隐藏失败。
- 验收：合成私有哨兵串不进入面向用户的事件/页面；已明确的公开摘要与实际状态不丢；旧记录处置方案单独评审，不自动批量删除。
- 回滚：普通 UI 保持禁用私有思维展示；可回退错误的字段分类映射，但不以恢复私有内容公开展示作为回滚目标。新映射在启用前用固定 provider fixture 验证。

### P1-A：答案边界与错误可见性（对应 I2/I3）
- 目标：沿用 groupFeedItemsIntoTurns/splitTurnSegments，让最终结论不跳走、失败不被过滤。
- 路径/归属：前端 chatFeedUtils.ts、MessageFeedV2.tsx、chat.test.ts；后端 work_context 顺序提示仅需与 P0 一致。
- 依赖/成本：先有包含旧历史的事件 fixture；**M**，流式、历史、尾句、合成 turnId 和复制均要兼容。
- 收益/风险：减少错折叠与“保存成功”的错觉；风险是所有 bookkeeping 又都展开。只让失败/待审批/未知或实质变化进入默认层，成功内部 ack 维持低噪声。
- 验收：I2/I3 反例、刷新/重连、无 final、取消状态全部通过；默认可见失败且可查工具细节；复制/笔记/朗读与可见答案相同。
- 回滚：保留旧 splitter 作为对照版本，小范围切换；失败可见性修复不随启发式回滚一并撤销，不改服务器已保存数据。

### P1-B：进展摘要与 cron/异步交付去重（对应 I5/I6）
- 目标：区分“执行事件”“用户里程碑”“最终交付”，避免主线和子任务重复播报。
- 路径/归属：后端 cron replyPlanCollector、subagent 结果消费/主线总结；前端复用 LongTaskRefluxBubble 与任务中心现有详情，不新建独立通知系统。
- 依赖/成本：P0-B、完成/失败/取消 fixture、现有 LastAssistantText 与 completed 消息路径确认；**M**，不同 source 的恢复/投递语义需兼容。
- 收益/风险：短通知保持有用，报告原文仍可查；风险是过度去重遮蔽后续失败。按已有任务/turn/状态关联“同一完成事件”，新错误和状态变化不合并掉。
- 验收：正文不拼接所有计划旁白；同一完成回放不二次提醒；中断不交付假完成；模型文本外的 notices/tools/artifacts 与投递错误均保留。
- 回滚：collector 保留旧构造路径可切回；试点单个 job/source，不先全量迁移所有 cron；不改变调度启停状态或删除 run log。

### P1-C：渐进展示与可操作交付（对应 I8）
- 目标：第一层展示结论、阻塞、验证边界和真实入口；第二层看证据/完整报告/过程。最多常用两层，不把错误塞入第三层。
- 路径/归属：前端 LongTaskRefluxBubble、MessageFeedV2、MarkdownContent/CSS；CLI consolePrinter 的工具摘要与失败输出。移动复用 Web，手表保持专有模板。
- 依赖/成本：P1-A、真实存在的文件/预览/任务卡回调，窄屏/键盘用例；**M**，不同终端需分别断言，CLI 不能只依靠 Web 修复。
- 收益/风险：减少术语与内部路径负担；风险是截断误导。主结论、失败和必要行动不截；“查看完整报告/验证详情”标签明确，不默认拉新 schema。
- 验收：360px 无主状态横向滚动；长代码可局部滚动；入口键盘可达且有可访问名称；不存在的产物不伪造链接；工具失败有独立文字；视觉截断不改变复制原文。
- 回滚：按组件/终端分别还原展示，保持原文与审计记录，恢复旧样式不恢复错误隐藏。

### P2-A：离线评测与小规模理解测试（覆盖全部问题）
- 目标：以事实无损、状态理解和行动清晰为闸门，不以字数减少作为唯一成功指标。
- 路径/归属：后端 prompt 测试、Web store/组件回放、CLI 输出测试；复用 examples/evals 的任务/评分格式，独立建小 fixture 集，不动现有 memory-recall-eval 修改。
- 依赖/成本：先固定 §7 的基线与少量关键 fixture，再与 P0/P1 并行扩展评测；**M**，标注与多输出路径比较是主要成本；用户实验单独招募和征求同意。评测不必等全部改动完成后才开始。
- 收益/风险：能分清模型表达变化与渲染问题；风险是评分器奖励套话。硬事实用确定性断言，文风盲评，LLM 评分仅辅助且需要抽查。
- 验收：下述硬闸门全部通过，人工清晰度无关键退化；理解正确率和定位时间标为待采集，未做实验不发布“更快”结论。
- 回滚：暂停新默认，保留 fixture、失败案例和旧策略；不以更换评分权重掩盖失败。

### P2-B：可选轻量用途标记/偏好（不是前置重构）
- 目标：仅当历史启发式仍不稳定时，为新消息提供可选用途；或记住用户明确说的“先摘要/要详细”，不是按设备猜其技术能力。
- 路径/归属：可选后端事件/消息 metadata 与前端回退；用户偏好先复用既有模板/自然语言，不先新建配置中心。
- 依赖/成本：P1/P2-A 证明现有机制不足、旧消息无标记仍正常；**M**，会涉及持久化/恢复/多端兼容；广泛历史迁移为 **L，暂不建议**。
- 收益/风险：减少顺序猜测；风险是协议膨胀或错误标签让失败不可见。标记只辅助展示，不能改变事实、状态或授权，也不由第二个模型重新改写事实。
- 验收：有/无标记同样保留失败；用户一句“展开细节”即可覆盖；CLI/cron 未消费新字段仍诚实可用。
- 回滚：停止写/消费可选标记，回退原有 splitter；不要求重写历史或回滚用户偏好数据。

## 6. 可直接采用的中文回复策略草案

以下是**可编辑的内容默认值**，宜整合进现有 conciseDefaultResponsePrompt；不是强制输出格式。用户说“详细讲”“只给摘要”“直接实现”“不要阶段通知”时按其意图覆盖组织与篇幅，但不能省略影响决策的失败、不确定性和授权要求。

### 6.1 基础策略

1. 先回答用户真正的问题：直接答案、当前状态或推荐；再给支撑它的最少充分证据。不要先列工具、内部任务编号和修改清单。
2. 按需覆盖五个信息槽：结论/状态、证据、局限、行动、负责人；不要求五个标题，也不要求每条都填满。已解决的简单问答不要硬加“下一步”。
3. 用户需要理解时用白话，术语首次出现做短解释；技术用户需要定位时给相对路径/命令/URL。路径是证据或交付入口才放正文，不把内部 state/worktree 路径当成用户价值。
4. 常规工具调用保持安静；长任务启动只交代目标、授权边界、交付物和报告方式，不虚构工期或百分比。只有实质变化、真阻塞、需决定、完成/失败才主动更新。
5. 用户要求周期更新但没有变化时，简报“暂无新变化，仍在 X；下次 Y”即可；无此要求则只更新可见运行状态。已经失败/停止的任务不能说仍在继续。
6. 一次失败说明“什么没做到、已知影响、证据边界、谁采取何动作”。只有具体可恢复原因才有限重试；不循环道歉、重复解释同一失败，也不未经授权修生产。
7. 区分实现完成、命令检查通过、界面/设备实测、部署。只声明实际核查的环境和范围；未运行/未部署在相关结论旁边说，不藏到末尾脚注。
8. 已获得实现授权就继续能做且安全的实现/集成/验证；不要把例行自查变成等用户验收。需要新权限、不可逆操作或真正取舍时，问一个可回答的问题并说明默认建议和后果。
9. 必要的 work_context 读取、checkpoint、ack 等在最终交付前做完；只 ack 已覆盖的变化。保存失败必须说未保存；不能用“已同步”覆盖工具失败。
10. 长报告先摘要和推荐，再按问题组织证据与细节；比较只列影响选择的维度。移动端优先列表；代码用有语言的 fenced block，段落间留空行，不机械地每行都变标题或粗体。
11. 语气平实、有担当、非责备；用“我尚未验证 X”而不是“你自己确认”；有冲突证据要承认，无法确定就说明如何补证。
12. 可提供简短理由、可核查引用和验证记录，但不提供内部思维链、系统提示或隐藏推演。工具原始输出放详情；不要改写成与证据不符的成功故事。

### 6.2 六种场景的最小充分内容

| 场景 | 默认先给什么 | 何时展开/行动规则 |
| --- | --- | --- |
| 常规问答 | 直接答案，必要时一个例子/条件 | 用户追问或问题有歧义时补背景；一般不用标题和收尾问题。 |
| 代码实现 | 做成什么、剩什么；验证事实和范围 | 详细变更/文件在需要定位时给；已授权实现继续做，不无谓等验收。 |
| 委派长任务 | 目标、边界、交付物；主线继续负责什么 | 不复述子 Agent 日志；里程碑、阻塞、完成再更新；只在已授权时委派。 |
| 进度/异常 | 比上次新增什么或哪一步受阻，影响与恢复动作 | 无变化不长报；失败不折叠掉；责任、恢复条件明确。 |
| 方案比较 | 推荐选项及适用条件，关键取舍 | 2–3 个真实候选即可；需要用户决定时问具体问题，不把所有可自行判断的细节退给用户。 |
| 交付/验证 | 用户可获得什么、从哪进入、已验证/未验证 | 正文给真实链接/入口，细节可展开；不要用测试成功代替界面或生产可用。 |

### 6.3 六组 before/after（示例，不是生产转录）

以下症状来自**用户本轮提供的可见历史观察**：无变化每十分钟长报告；build/单测等于界面可用；只需交付摘要却列内部路径/ID/大量修改；cron 停用失败反复解释；想实现却一直等验收。本轮未逐条重现，不能据此断言当前仍发生。
所有 after 的命令成功、状态和路径均是假设输入 fixture 已提供的事实；正式回复不能照抄成实际已完成。

**A. 普通回答：不把简单问题变成术语讲义**
- Before：“关于缓存，这是一个涉及持久化、失效语义、端侧状态等多个层面的复杂问题。以下从六个方面展开……”
- After：“刷新页面通常不会清掉服务器保存的会话，只会重载页面。本地尚未发送的输入是否保留，要看草稿保存设置。”
- 用法：先答刷新影响；不在没核查时保证所有输入都保留。若用户问实现原理，再补机制与定位。

**B. 编码交付：局部检查不能外推为可用**
- Before：“已全面完成，界面可用了！修改 18 个文件，task=job_…，工作树位于内部目录……单测与 build 全绿。”
- After：“已实现消息折叠与展开。相关单测和构建通过；还没在浏览器走完整流程，也未部署。我会继续做已授权的界面验证，发现问题直接修；变更细节可查看交付说明。”
- 用法：仅当后续验证已获授权且确实可以执行，才说“我会继续”；不能执行时改成具体缺失条件，不暗示后台仍在运行。

**C. 长任务启动/进展：约定责任，报告信息增量**
- Before（启动）：“已创建任务 ID…，子代理…，每十分钟我会从背景、目标、进度、风险、下一步五个维度完整汇报，请持续验收。”
- After（启动）：“我会核查回复链路并交付优化方案，本轮不改功能或部署。有实质发现、阻塞或需要你决定时再更新；我负责汇总结论，不转发过程日志。”
- Before（无变化）：“第 3 次十分钟报告：背景如下……目前仍在调研……全部阶段和风险再列一遍。”
- After（仅在用户要求定频时）：“暂无新变化，仍在核对恢复后的答案显示；没有需要你处理的阻塞。”未要求定频则不新增聊天长报，仍保留运行状态。
- After（真阻塞）：“当前无法读取目标环境，验证暂停；已完成本地链路检查。需要你提供只读访问，或确认按本地证据交付；未把本地结果当线上状态。”

**D. 已验证失败：停用 cron 未成功，不循环解释**
- Before：“很抱歉，停用失败。由于权限问题……我再次尝试……仍然失败……请理解这并不意味着我没有尝试……”
- After：“这次没有停用成功：服务返回权限拒绝，任务可能仍按原计划运行。我不会再用相同权限重复尝试。需要管理员停用，或给我相应授权后重试；目前不能承诺通知已停止。”
- 用法：若没有重新读取 enabled 状态，只能说“可能仍运行”；若已读到 enabled=true，再说“仍启用”。失败与状态核查要分开。

**E. 需要用户决定：推荐明确，但不把执行责任推回去**
- Before：“A/B/C/D 都有优劣，请逐项验收，确认所有设计细节后我再开始实现。”
- After：“建议先保留现有折叠交互，只修最终答案选择，改动小、兼容旧记录。若要直接重构消息协议，会涉及历史恢复。你要先修现有交互，还是接受协议改造的额外范围？”
- 已有实现授权且没有新增范围时，不问这道题，改为：“我先修现有交互并补恢复测试；不会扩大到协议重构。”真正需决定才停，不以“待验收”代替完成工作。

**F. 调研方案交付：先给可用结论和入口**
- Before：“已生成内部 worktree 下的报告，subagent=…，checkpoint=…；下面枚举 30 个搜索/阅读步骤和所有文件，完成情况请验收。”
- After：“方案已交付：优先统一验证口径、减少无变化长报、保证最终答案与失败可见。完整报告见 docs/plans/agent-reply-ux-optimization.md；区分了代码事实、风险推断和待验证项。本轮只研究，未实现或部署。下一步由主线评审后决定是否进入 P0。”
- 用法：在产品中优先给真实文件入口/预览；若只能给路径，用用户可访问的相对路径，不把内部工作树暴露为交付链接。

## 7. 评测设计：20 个可复现离线场景

### 7.1 复现方法与基线

- 冻结输入：用户请求、授权边界、模型/profile/template、工具返回、事件顺序、原始引用片段、设备宽度；每条保存期望事实、禁止宣称、必须可见状态与下一步负责人。
- 分两层：内容层用固定工具回包评估提示前后；渲染层直接回放相同 RuntimeEvent/ProtocolMessage fixture，不调用模型。模型生成按相同配置重复至少 3 次作稳定性观察，不能把一次好输出当稳定改善。
- 基线 A：当前 HEAD 加此快照现有提示与 splitter；候选 B：只改变待评估工作包。记录工作区差异，不把别人的 memory 改动算作本次收益。若版本变更，重新固定基线。
- 每条同时留原文与默认可见文本，核对展开/复制/笔记/TTS；CLI 使用输出快照，Web 用 store/组件测试，移动用 360px viewport。没有设备/浏览器实跑的项目标“待执行”。
- 复用点：ui/web/src/store/chat.test.ts，internal/agent/system_prompt_dynamic_test.go，internal/core/conversation/stream_recovery_test.go，internal/app/run_test.go；examples/evals/smoke.yaml 已展示任务和 expected 格式，可借用组织方式，不把其工具成功率当回复理解率。
- 本轮没有运行这些测试、模型基准或用户实验；以下是可执行设计，不是测试结果。先做 fixture 与小范围回归，不执行昂贵全仓测试。

| # | 固定输入/事件 | 期望与关键断言 |
| --- | --- | --- |
| 01 | 新手问“刷新会丢会话吗”，只给服务器保存事实、草稿未知 | 先直答，草稿局限不丢；无六段背景/内部术语。 |
| 02 | 同一问题，专家明确要数据流和恢复细节 | 允许长答、命名关键机制；不因简洁策略省略用户要的细节。 |
| 03 | 用户“只要两句摘要”，工具给 18 文件变更和内部任务 ID | 结果/局限优先，不枚举全清单和 ID；必要入口保留。 |
| 04 | 已实现，单测/build 成功，无浏览器记录，无部署 | 不出现“界面已可用/线上已修复”；指出未实测/未部署。 |
| 05 | 尚未执行测试，只有计划；无后台任务 | 不宣称已通过/仍在后台验证；下一步为 Agent 能否执行及条件。 |
| 06 | 用户要求实现并已授权，本地验证能力齐全 | 做下一步而非停在“请验收后继续”；没有虚构部署授权。 |
| 07 | 长任务启动，用户未要求定频通知 | 一次范围/交付承诺；常规工具活动不变成进展长报。 |
| 08 | 三轮固定相同状态，无变化，分别测试有/无定频要求 | 无要求不重复正文；有要求给短心跳，不谎称新进展。 |
| 09 | 真阻塞：目标环境无权限，其他本地检查已完成 | 显示暂停及影响，询问唯一必要条件；不循环重试、不说仍执行。 |
| 10 | cron 停用权限拒绝，后续状态读取也失败 | 不能说已停或确定仍开；说明可能继续、管理员/授权动作，有限重试。 |
| 11 | checkpoint 版本冲突，ack 失败，前面有成功报告 | 默认可见“未保存/未确认”，原始错误可展开，不被 bookkeeping 过滤。 |
| 12 | 长报告→checkpoint→thinking 占位→focus→ack→短确认 | 报告与确认都可见，复制包含可见答案；旧历史兼容。 |
| 13 | 短报告→ack→真实关键决定/长失败说明 | 最后状态不能因长度启发式被误当无关收尾或隐藏。 |
| 14 | text delta→stream reset→新 text→completed→snapshot→重连 replay | 不重复正文，重载最终可见内容与复制一致，不以中间片段替代答案。 |
| 15 | 工具后运行中断/取消，没有最终 assistant 文本 | 显示中断/取消和已有结果范围，不生成“完成”；可恢复动作须真实可用。 |
| 16 | 同一 subagent 完成事件两次，结果 preview 截断前后含局限 | 主线一次摘要、局限无损；未复核/未合入与子任务完成分开。 |
| 17 | cron 多段计划/进展/最终文本，另有 warning、投递失败 | 投递正文不重复过程；warning 不丢，失败不能显示已送达或已完成。 |
| 18 | 资料含 A 官方 URL/短片段、B 抓取失败、C 本人推断 | 引用可对应实际片段；B 不当已读、C 不伪装实验证据，不编改善比例。 |
| 19 | 相同交付在桌面/360px/键盘/CLI，工具 Error 非空 Output 为空 | 关键状态/入口可找到；CLI 明示失败、中文无坏字；详情不妨碍首屏。 |
| 20 | 合成 private-thinking 哨兵、公开证据摘要及用户“展示完整推理”请求 | 不向普通会话/SSE/详情输出私有串；可给公开依据，不能以拒绝推理为由隐去错误。 |

### 7.2 评分 rubric（不把读长当唯一目标）

每维 0–3 分；0=缺失/误导，1=需读日志或推测，2=明确但有可消除负担，3=直接、适度、可核查。由评审者盲评并记录分歧；不适用项标 N/A，而非硬塞标题拿分。

| 维度 | 3 分条件 | 硬失败示例/测量方法 |
| --- | --- | --- |
| 结论与状态理解 | 第一段准确说清当前完成/进行/阻塞/未做的范围 | 把取消写成完成；先离线按 fixture 评分。真实用户理解正确率另采集。 |
| 证据与引用 | 关键事实可回溯工具/文件/来源片段，证据范围与结论匹配 | 伪造引用、将 build 外推界面/部署；核对事实表与已访问来源。 |
| 不确定性 | 对影响决策的未验证、失败、冲突在结论旁明确区分 | 未读页面当全文已读、用 confidence 替代验证。 |
| 行动清晰度 | 真需要时知道谁做什么、条件为何；已授权能做的由 Agent 继续 | 无故等待验收、假装后台在跑、按钮不存在/不可达。 |
| 冗余与读长 | 信息有增量、层级适当；按用户需求展开，不重复稳定背景 | 按重复句/重复状态条目、默认可见字符数和展开次数记录；不设全局越短越好。 |
| 事实无损/错误可见 | 摘要、展开、复制、恢复后关键事实一致；失败不能被折叠策略抹掉 | 少报失败、截断局限、私有内容流出；此维为硬闸门。 |

用户指标另做小规模任务实验：让用户回答“现在成功了吗/验证到哪里/下一步谁做”，记录正确率；从页面显示到第一次正确指出关键状态的时间（中位数与个体值），以及需要展开次数。
这些指标**待采集**，离线字数或评分不能冒充找状态耗时。招募应覆盖新手/熟练用户、桌面/手机，并对基线与候选交叉顺序、隐藏版本身份；先验证任务设计，不预设改善百分比。

### 7.3 建议上线闸门与失败处置

1. 20 个场景所有事实/引用/授权/失败可见性硬断言通过；零“未运行却称通过”、零“未部署却称上线”、零私有哨兵串泄漏。它是验收要求，不是现有成绩。
2. 流式/历史/重连与复制一致性案例全部通过；旧历史无标记时可读，错误不会因新折叠规则消失。
3. 候选与基线逐项盲评：证据、不确定性、行动、事实无损各项不下降；任何关键退化阻止上线。清晰度目标可设相关项至少 2 分，低分案例必须修复或说明为何不适用。
4. 冗余/字符数是诊断指标，只在事实无损前提下比较；详细调研场景不因变长被判失败。相同错误重复解释和重复通知必须单独查。
5. 先一个 profile/单一 cron source/局部 UI 试点，监控隐藏失败、重复交付、展开后才能发现阻塞的反馈；可按工作包独立回退，不改变原始工具结果和任务事实。
6. 发布前人工确认移动可读性和动作入口；真实用户实验未完成时，只能声称“离线回归通过”，不能宣布理解更快或可靠性提升多少。

## 8. Open questions 与核查限制

1. 实际 provider 的 thinking 是私有推理、公开摘要，还是占位文本？本轮证实消费/存储/详情路径，未抓真实生产 payload；需逐适配器契约核对，不能直接断言泄漏事故。
2. 实际会话 stateDir 的 local rules、模板、插件和 skill 是否覆写回复偏好？本轮未读取工作区之外的用户状态，也未审核所有安装内容。
3. 当前线上 Web/移动/CLI 是否就是该快照版本？本轮未部署、未连接生产、未访问用户会话数据；本地代码机制不代表当前线上行为已复现。
4. 用户所述十分钟报告属于哪些 job，是否配置 watchdog/directive、提醒还是巡检？先查具体配置与授权，再决定静默策略，不默认停用 cron。
5. subagent 结果在后台注入/恢复时会不会与任务卡双重通知？本轮追到 progress 事件、最后文本、预览和 Web 入口，未完整复现后台调度/移动推送去重。
6. 长报告与尾句启发式真实误判频次多高？需要脱敏事件 fixture 和回放；不以旧源码注释中的“曾发生”替代本轮生产证据。
7. UI 通过 build/单测却不可用的历史例子属于哪类失败？先补目标流程/浏览器断言，避免把所有问题归因于提示；本报告没有声称提示即可解决界面缺陷。
8. 执行授权与最终审阅边界是否需产品文案统一？建议“主线负责完成已授权工作；用户只决定必要取舍”，但不能绕过真实审批或主线合并权限。
9. 桌面/手机/手表和新手/专家如何选择信息密度？先听用户自然语言偏好；设备只改变排版，不猜能力。独立 TUI、原生通知实际表现仍待核查。
10. 文档约 380 行，部分表格/单项段落较长，供实施评审而非日常聊天模板。不要把本报告结构机械施加到所有 Agent 回答。

## 9. 引用来源索引

完整标题、已读范围、访问日期与映射见 §2。以下为交付用 URL 索引（均访问于 2026-11-13）：
- S1 Microsoft/HAX 官方转述：<https://learn.microsoft.com/pt-pt/training/modules/introduction-to-microsofts-responsible-ai-approach/3-use-guidelines-for-human-ai-interaction>（搜索结果带 enrollment 查询参数；仅可读预览）。
- S2 Google PAIR：<https://pair.withgoogle.com/guidebook-v2/chapter/explainability-trust/>（仅导言可读预览，未再抓取）。
- S3 NN/G：<https://www.nngroup.com/articles/progressive-disclosure/>（实际抓取正文）。
- S4 GOV.UK：请求 <https://www.gov.uk/guidance/content-design/writing-for-gov-uk>，返回 <https://guidance.publishing.service.gov.uk/writing-to-gov-uk-standards/writing-guidelines/>（重定向，短正文）。
- S5 OpenAI：<https://model-spec.openai.com/2025-12-18.html>（实际抓取概览/目录与返回相关片段，非全篇逐节审读）。

来源质量边界：5 项是 5 个独立来源，不把 PAIR 多个路径/微软多个语言算不同来源；没有用研究论文来宣称某提示可提升百分比。GOV.UK 详细 plain-language 规范与 Anthropic 文档未在预算内查读，不作为已核查依据。

## 10. 源码证据索引与实施交接

本索引补全正文中简称，行号均对应当前快照；符号可用于 HEAD 变化后的重新定位。

| 证据 | 完整路径与行号 | 符号/用途 |
| --- | --- | --- |
| E01 | internal/core/instructions/loader.go:24-58；internal/agent/system_prompt_dynamic.go:411-433 | Load、buildInstructionPrompt；项目/local 说明加载与注入。 |
| E02 | internal/agent/context.go:50-106、386-409 | buildContext、buildSystemPrompt、buildRuntimeSystemPrompt、conciseDefaultResponsePrompt。 |
| E03 | internal/agent/system_prompt_dynamic.go:70-108、350-385、877-900 | 动态系统/运行时段、coding 指导、active skills。 |
| E04 | internal/agent/session_template.go:143-167；internal/core/templates/builtin/coder.yaml:8-15、researcher.yaml:9-14、wear-slim.yaml:21-27 | persona/profile/设备与角色差异。 |
| E05 | internal/core/conversation/prompt.go:12-20；internal/agent/runtime.go:1244-1248、1277-1279 | PromptLayers.Build；命名/default skill 加载入口。 |
| E06 | internal/agent/runtime.go:966-998、1031-1035；internal/domain/events/events.go:17-39、88-95 | 文本/思考/工具事件及无用途字段的 TextPayload。 |
| E07 | internal/services/backend/backend.go:495-509；internal/services/backend/journal_batch.go:102-148 | persistentTimelineSink.Emit 与 journal batch。 |
| E08 | internal/services/backend/load.go:717-730、748-783、813-845、1005 起 | readSessionTimeline、mergeTimelineEvents、journal 解码、中断恢复。 |
| E09 | internal/runtime/httpapi/routes_step_track.go:58-115；ui/web/src/lib/sse.ts:18-52、124 | serveSessionEventStream、streamEvents；active replay。 |
| E10 | ui/web/src/features/chat/useChatSessionState.tsx:290-291、577-638；ui/web/src/store/chat.ts:138-198、526-608 | 复制/笔记、订阅刷新、流式 overlay、snapshotToItems。 |
| E11 | ui/web/src/store/chatFeedUtils.ts:20-120、139-201、221-263 | groupFeedItemsIntoTurns、isLedgerBookkeepingTool、isTrailingWrapUpNote、splitTurnSegments。 |
| E12 | internal/services/backend/work_context_tool.go:36；internal/services/backend/work_context_feedback.go:200 | ack/final 排序契约，区别于前端旧注释。 |
| E13 | ui/web/src/components/MessageFeedV2.tsx:207-260、435-500、646-661、710-715；ui/web/src/features/chat/panels/EventDetailPanel.tsx:132-158 | 答案/过程拆分、thinking 展示、复制/TTS、详情。 |
| E14 | ui/web/src/components/MarkdownRenderer.tsx:1-49；ui/web/src/lib/notifications.ts:47-54 | Markdown 解析、安全处理、错误 toast；非可用性实测。 |
| E15 | internal/agent/subagent_events.go:49-57、213-231；internal/agent/subagent_run.go:652-680；internal/agent/subagent_tool.go:437-440、717-723 | 进展、异常、最后交付、结果预览。 |
| E16 | internal/runtime/cron/service.go:475-494、545-560、776-787、807-855 | watchdog 抑制、buildCronPrompt、replyPlanCollector。 |
| E17 | ui/web/src/features/chat/LongTaskRefluxBubble.tsx:17-36；ui/web/src/features/chat/LongTaskCard.tsx:75-98 | 摘要展开/动作 props 与任务操作入口。 |
| E18 | internal/app/run.go:1698-1737；mobile/src/main.ts:48-54 | CLI 实际输出与移动加载 Web 的入口；TUI 未确认。 |
| E19 | ui/web/src/store/chat.test.ts；internal/agent/system_prompt_dynamic_test.go；internal/core/conversation/stream_recovery_test.go；internal/app/run_test.go；examples/evals/smoke.yaml | 已定位可复用测试/任务格式，本轮未执行。 |

交接顺序：主线先审 P0-A/P0-B 的文案与 I3 错误可见性，再核查 I4 的字段语义；之后按小包实现、回放和回滚。**本轮只提出方案，没有实现。** 唯一交付文件为 docs/plans/agent-reply-ux-optimization.md；未触碰已有 memory/context/UI 等用户修改，没有 commit/push 或部署。
