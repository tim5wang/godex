# 工作桌面与工作委派设计（Work Context Desktop）

> 状态：Implemented（WD1–WD3 已落地：个人 SQLite 持久化 + 真实委派/知识回流联动；团队共享与 PG 迁移仍为 WD4 待评估）。
> 日期：2026-10-29（WD0 草案）/ 2026-10-30（WD1–WD3 实现）
> 关联：[taskboard-plugin-design.md](./taskboard-plugin-design.md)、[taskboard-collaboration-design.md](./taskboard-collaboration-design.md)、[memory-design-principles.md](./memory-design-principles.md)、[scope-isolation-design.md](./scope-isolation-design.md)
> 定位：长期工作主线是对象，工作桌面是视图，任务看板是委派执行器，记忆是可治理的知识，不引入第二套 Agent Runtime。

## 1. 需求与已确认约束

### 1.1 要解决的问题

- 多个需求方、几十个在线服务、模型评测和版本迭代同时推进，切换时重复恢复背景。
- 一条主线可持续数月，跨代码仓库、网页、文档、运行节点和多段对话，不能绑定为一个目录或超长聊天。
- AI 写的代码需要保留入口、设计理由、验证和运维方法，不只是 diff 或对话摘要。
- 历史回忆允许模糊，但生产参数、部署版本等精确事实必须可追溯且有时效。
- 各主线可以相关、共享资源、互相依赖；不是知识孤岛。

### 1.2 决策

1. 新增一级入口「工作桌面」，现有「任务看板」保留并逐步补齐工作委派入口，暂不全局改名。
2. 工作上下文 WorkContext 与 Taskboard Project、Session、运行节点、知识条目分别建模。
3. 工作桌面是关注焦点，不是权限隔离边界；跨桌面共享不得绕过租户、用户和资源权限。
4. 首版正式存储只使用已有 `database/sql` + `modernc.org/sqlite`。不引入 ORM、图数据库、向量服务、Redis、消息队列。
5. PostgreSQL 是后续迁移目标，不在首版增加驱动、双写、通用 SQL 方言框架。
6. Pieces 不可用；Screenpipe 不是前置依赖。后续如获安全及许可批准，只作为可选痕迹来源。
7. 先做可交互 UI 原型对齐信息架构，再实现持久化与运行链路。

### 1.3 非目标

- 不做操作系统虚拟桌面管理，不自动恢复/执行终端命令，不默认打开一组网站。
- 不把所有对话、屏幕和文件复制进第二个知识库；引用原始来源优先。
- 不让 AI 摘要替代验收、部署记录或实时监控。
- 不以工作桌面的关联关系扩大 sandbox、节点或工具权限。
- 首版不做任意布局编辑器、图谱画布、持续录屏、自动生产写操作。

## 2. 当前事实与实现状态

| 能力 | 当前事实 | 本方案增量 |
|---|---|---|
| Taskboard Project/Card | `internal/plugins/taskboard/types.go`；Project 支持多个 WorkDirs，Card 有工作目录、Research、Checklist、Execution、Version | 关联上下文，不改变 Project 的认领/执行边界 |
| 任务执行 | `internal/services/backend/taskboard_executor.go`；每张卡有专属可恢复执行会话 | 派发时附有版本的上下文快照 |
| 主线对话 | Session Runtime 已持久化，多入口共用 | WorkContext 外部关联多个 session_id，不重写对话存储 |
| 会话恢复 | `ui/web/src/lib/chatRoutes.ts`；完整 locator 影响会话身份 | 桌面关联不写入旧会话身份 metadata |
| Memory | Markdown durable memory 真源、SQLite/FTS 索引、候选箱、相关召回 | 添加上下文引用、事实时效与替代关系，不把 DB 索引误当真源 |
| Notes | 笔记与记忆双向关联 | 资源引用复用 Notes |
| Web 工作台 | React/Ant Design，已有 lazy app registry、多面板；布局是 localStorage | 新页面复用组件/主题；业务数据将进入服务端 SQLite |
| 任务依赖和经验回流 | 协作文档标为 Planned；当前不能假定已有 | 在 WD2/WD3 分期落地 |

### 2.1 本次 WD0 原型契约

- 入口：工作桌面是**聊天页左侧视图切换**的一个视图（不是独立路由），选中主线保存在页面状态，支持浏览器前进/后退。
  > 更新（WD3.16）：已并入聊天页，独立路由 `/work-desktop` 与其页面已删除。本节为 WD0 原型期契约的历史记录。
- 使用明确标识的虚构示例数据，无真实业务配置、账号、token 或生产操作。
- 可切换/搜索主线、创建演示桌面、编辑检查点、预览恢复简报、查看资源与关联、记录主题笔记、保存委派草稿、检索和审阅演示知识。
- 所有业务变更仅在当前页面内存中；重新加载会重置。切换桌面不丢已保存的演示状态；未提交的表单/笔记按明确交互保存或取消。
- 不调用模型，不创建真实任务或会话，不写入 durable memory，不读取目录、不打开终端、不探测内网。
- 「真实任务看板」仅导航现有页面，不隐式传入/创建原型中的任务。
- WD0 的记录与简报不得伪装为后台实时查询或 AI 已完成操作。

## 3. 领域模型与生命周期

### 3.1 WorkContext：长期工作主线

字段：`id / tenant_id / owner_id / title / background / goal / status / version / created_at / updated_at / archived_at`。

- status：`active / paused / archived`；暂停不取消已派发任务，归档不删除历史或共享资源。
- 不强制设置主仓库；可关联多个项目、目录和节点，也可以是无代码工作。
- 重命名不改 ID。桌面名称不是资源或 session 的身份。
- 个人默认只有本人可见；WD1 不新增复杂共享 ACL，租户内协作沿用获批的身份权限后再开放。
- 归档后默认只读；显式恢复再写入。删除是单独操作，不与归档混淆。

### 3.2 Checkpoint：可恢复状态

结构：`summary / completed[] / in_progress[] / open_questions[] / blockers[] / next_actions[] / evidence_refs[] / observed_at / author / version`。

- 每次保存产生不可变修订，桌面只保留当前指针；人工编辑采用乐观锁。
- 工作切换先保存轻量位置和已确认状态，不等待 LLM。
- AI 生成内容是草稿；用 `generated_from_revision` 防止异步旧摘要覆盖新的人工编辑。
- 当前任务状态来自看板投影，避免检查点手工文本与任务账本各维护一份状态。
- next_actions 是建议，不是命令执行授权。

### 3.3 Resource：共享资源及来源

类型：`web / directory / repository / note / document / dashboard / service / model / artifact`。

通用字段：标题、定位 URI/对象 ID、所属权限域、可选 node_id、来源版本/hash、最后验证时间、敏感级别。

- 资源独立保存，`context_resources` 多对多引用；桌面侧存用途、置顶和排序。
- 一个业务系统的部署信息只维护一份源，多个桌面引用。
- 目录必须绑定执行节点；同名路径在不同节点是不同目标。
- URL 只允许获批协议；服务端抓取必须经过现有网络策略，阻止 SSRF/metadata；不是用户贴了链接就自动抓取。
- 不存明文凭据，不把 signed URL、token query 或环境变量完整快照带入检索。
- 文件内容由现有 Files/Notes/Artifact 系统管理，Resource 只保存引用和必要摘要。

### 3.4 关系

- `related_to`：对称参考关系，不阻塞工作。
- `depends_on`：有向依赖，必须说明满足条件；可落到具体卡片、里程碑或产物。
- `context_resources`：共享资源，不用一条模糊 related 边替代所有语义。
- `context_tasks`：主归属上下文 + 可选相关上下文；任务状态仍以 Card 为准。
- `context_sessions`：一个桌面关联多个主题对话；跨桌面引用需显式确认。
- 依赖不自动调度。真正阻塞执行的依赖由 WD2 检查，并有独立任务级验收条件。
- 新增依赖时检查环，拒绝自依赖；多跳检索有深度和结果数量上限。

### 3.5 Conversation：主线持续，不是单会话永生

- 主线包含多个主题 Session；新主题新建会话，旧主题可继续。
- 通过 session_id 查询现有 locator 并用现有路由恢复；不拼接一个缺 metadata 的新 locator。
- 关联表保存关系，不把 `work_context_id` 直接加入旧 SessionLocator 身份字段。
- 复用现有 Chat/MessageFeed/Composer，WD0 只展示主题记录和笔记，不再实现聊天内核。
- 新会话按本轮预算加载背景、检查点和相关证据，不灌入全部历史。

## 4. 信息架构与主要交互

### 4.1 桌面

左侧：搜索、主线列表、状态、当前焦点、待审提示。中间：标题、恢复/检查点/委派动作，概览、主题对话、委派任务、知识与回忆、时间线。右侧：资源入口、关联桌面、依赖条件。

宽屏三列，中屏资源落到主区域下方，小屏主线改为下拉选择、单列内容；不要求横向拖动才能访问关键操作。所有按钮有文本或 aria-label，键盘可到达。

### 4.2 切换

1. 本地保存位置与已保存状态；未提交编辑明确保留或提示，不静默丢失。
2. 切换 URL 中的 context ID，不变更正在执行的卡片归属。
3. 立即呈现已有 checkpoint；后台读取自上次访问后的增量事件。
4. 如需 LLM 生成简报，异步生成并标时效；失败保留可用的结构化简报。
5. A 桌面任务继续运行，结果仍回到 A；B 桌面只显示关联事件的提醒。

### 4.3 恢复简报

输出固定分区：背景与目标、上次进展、暂停后变更、阻塞/未验证、相关证据、建议下一步、需要重新查询的事实。

- 显示生成时间、依据 checkpoint revision、来源最新时间。
- 未接入部署系统时明确写“未核实当前生产状态”。
- 简报是可再生投影，不是新的权威事实表。
- 保留“打开证据”，避免只返回一句无法追溯的结论。

### 4.4 委派

桌面创建草稿 → 选择 Taskboard Project/WorkDir/Template → 确认目标和 DoD → 生成 ContextPack → 调用现有建卡 → 人工派发/已有执行器。

ContextPack 固定一次执行使用的背景版本、checkpoint revision、资源版本、事实和开放点、约束、验收；执行中重要更新以通知/新快照显式追加，不能静默改变已开跑任务输入。

验收结果：代码入口、关键理由、运行/测试方法、报告与 commit、已知限制、回滚说明（适用时）。代理可提交 in_review，不得自行 done；复用现有协议闸。

## 5. 记忆治理

### 5.1 四层与权威源

| 层 | 保存位置 | 语义 |
|---|---|---|
| 原始痕迹 | 现有 session/timeline/artifact，未来可选 Screenpipe | 出现过什么，不证明尝试已成功 |
| 工作状态 | WorkContext checkpoint + 看板投影 | 当前工作进展，不等于生产状态 |
| 确认知识 | 现有 durable memory/Notes + 上下文引用 | 有依据的决策、方法、经验 |
| 精确事实 | 有来源的事实版本，必要时现查外部系统 | 某范围、某时间观测到的值 |

### 5.2 事实结构

`subject_ref / predicate / value_json / scope_json / status / source_ref / source_revision / observed_at / valid_from / valid_until / verified_at / supersedes_id / recorded_at / version`。

- scope 至少能区分服务、业务、环境、区域，未知项不能自动填默认生产。
- status：`candidate / confirmed / stale / superseded / rejected`。
- `observed_at` 是观测时间，`recorded_at` 是入库时间；“今天补记上周事实”不能当成今天刚核实。
- stale 表示需重核，不等于 false；同范围冲突并列展示，不能按“最新文字”直接覆盖。
- 机器声明必须附证据；“AI 说测试通过”不是测试证据。
- 人工报告与系统查询区分 provenance，不把两者混成同等级 verified。
- 模型、配置、评测和发布关联 code commit、model ID/hash、config hash、dataset version、image digest、report、rollback ref；密钥只引用。

### 5.3 自动化等级

- 自动记录客观事件：任务状态、报告引用、提交、授权查询。
- 自动整理草稿：恢复简报、检查点建议、关联建议。
- 长期知识进入现有候选 inbox，继续 accept/dismiss/suppression。
- 生产事实确认依赖带范围的授权查询/人工审核；发布和生产写操作不由摘要触发。
- 相同来源版本使用幂等 key，不重复制造候选；摘要不能递归摘要后再当原始证据。

### 5.4 召回

流程：身份鉴权 → 识别问题是回忆/知识/当前事实 → 当前桌面精确过滤 → 直接关联桌面/共享资源 → 明确允许的全局范围 → 排序/去重 → 限预算注入。

- 模糊回忆先复用 FTS5 + 标题/别名/标签 + 时间过滤；中文用规范化别名和可选应用层分词处理，不能宣称 unicode61 天然解决中文语义检索。
- 关系跳转默认一跳，有数量上限，不把所有相邻桌面全文注入。
- 精确事实先按对象/环境/业务匹配，再检查时间与来源；问“现在”时应现查，不能用旧 recalled value 冒充。
- 回答分“已确认 / 可能相关 / 未核实”，每项可打开原始来源。
- LLM 是可选重排与表达层；模型不可用时仍能使用目录、检查点、结构化筛选和全文检索。
- WD0 的搜索只在演示数据中做文本匹配，不宣称语义召回。

### 5.5 遗忘、删除和安全

- 归档桌面不删共享资源；删除引用不删原始会话；删除原始来源需标记依赖条目 source_missing 并重新审核。
- 真正删除敏感来源时，执行索引清理、缓存失效、衍生摘要清理/脱敏；保留不含敏感原文的审计。
- 备份和外部同步副本有独立保留策略；不能承诺在线删除立即清除离线备份。
- 来源内容按不可信输入处理，提示词注入不能改变权限、调用工具或写入 confirmed 知识。
- 外发给模型前应用数据等级和 provider allowlist；本地采集不等于允许云端推理。

## 6. SQLite 数据设计

### 6.1 存储边界

新领域先使用 `<StateDir>/work-contexts.sqlite`，复用已有驱动和连接初始化实践，不迁移现有 taskboard ledger、session 或 Markdown 真源。

- `database/sql`、WAL、foreign_keys=ON、busy_timeout；PRAGMA 必须覆盖实际每个连接。初期单写连接、短事务，不在事务中调用网络或 LLM。
- SQLite 文件不放 NFS/共享盘，不允许多节点直接同时挂载写；远程节点通过服务 API 访问。
- ID 在应用层生成；时间统一 UTC RFC3339，API 显示本地时间；业务排序用标准化时间/序列。
- 核心可查询字段用普通列，少量扩展内容用 JSON TEXT；不把全领域塞进单个 JSON blob。
- tenant_id/owner_id 来自 Principal，而非请求体；复合唯一键/外键与服务检查共同防串租户。

### 6.2 逻辑表

| 表 | 主键/关键字段 | 用途 |
|---|---|---|
| `wc_schema_migrations` | version, applied_at | 有序 SQL 迁移 |
| `wc_contexts` | tenant_id, id, owner_id, title, background, goal, status, version, timestamps | 主线 |
| `wc_checkpoints` | tenant_id, id, context_id, revision, payload_json, actor_id, created_at | 不可变检查点；unique(context_id, revision) 含 tenant |
| `wc_resources` | tenant_id, id, kind, title, locator, node_id, classification, source_revision, verified_at, version | 共享资源 |
| `wc_context_resources` | tenant_id, context_id, resource_id, purpose, pinned, sort_order | 多对多引用 |
| `wc_context_sessions` | tenant_id, context_id, session_id, topic, role, created_at | 多主题会话引用 |
| `wc_context_tasks` | tenant_id, context_id, project_id, card_id, role, pack_id | 看板引用，不复制 status |
| `wc_relations` | tenant_id, id, from_id, to_id, kind, condition_json, version | 主线关系，禁止自依赖 |
| `wc_facts` | tenant_id, id, subject_ref, predicate, scope_key, value_json, status, source_ref, provenance, observed_at, valid_from/until, supersedes_id, version | 带范围的事实版本 |
| `wc_knowledge_links` | tenant_id, context_id, source_kind, source_id, purpose | 引用 Memory/Notes，不复制真源 |
| `wc_context_packs` | tenant_id, id, context_id, revision, payload_json, hash, created_at | 委派不可变输入 |
| `wc_events` | tenant_id, id, context_id, kind, source_type, source_id, dedup_key, payload_json, created_at | 增量变化、审计、重建投影 |
| `wc_outbox` | tenant_id, id, operation, dedup_key, payload_json, attempts, next_attempt_at, state | 跨存储投影/建卡补偿；WD2 才引入 |

外部 session/card/source ID 没有跨数据库 SQL 外键，关联时通过服务权限检查，读取时允许“源已删除/节点离线”的明确状态，不因悬空引用崩溃。

索引：contexts(tenant_id, owner_id, status, updated_at)、events(tenant_id, context_id, created_at, id)、facts(tenant_id, subject_ref, predicate, scope_key, status, observed_at)、双向 relation 索引、各外部引用唯一键。

FTS5 是可重建投影，只索引允许检索的标题/背景/检查点/授权摘要，并携带 tenant_id/context_id 映射。参数绑定、限制长度和 token 数，不允许用户文本直接变成任意 MATCH 表达式。现有两张投影：`wc_contexts_fts`（主线，供 `list_streams` 检索）与 `wc_facts_fts`（事实，供 recall 选候选）。unicode61 不切分中文，因此索引文本先经应用层分词（CJK 切 unigram+bigram+整串，见 `indexingText`）再写入；投影在保存事务内增量维护，并在每次 `Open` 时按行重建（可修复早期写入或损坏的投影），与 §6.3「索引失败可重建」一致。

### 6.3 并发与跨存储一致性

- 更新使用 `UPDATE ... SET version=version+1 WHERE tenant_id=? AND id=? AND version=?`；0 rows 返回 409，UI 不覆盖别人的新修改。
- 检查点、当前指针和本地事件在同一 DB 事务提交。
- 现有看板是 ledger.json，Memory 真源是 Markdown，不能声称和 SQLite 原子提交。
- WD2 建卡采用持久化 operation ID + 幂等协作：必要时给现有 ledger 增加可持久化的 external request ID 去重，再提交关联；后台重试与对账修补中断。
- durable memory 更新仍走现有候选/接受流程；outbox 仅记录桥接请求和结果引用，不绕过审阅。
- 搜索索引失败可重建，不回滚已经成功保存的人工检查点；UI 显示“索引待更新”。

### 6.4 PostgreSQL 迁移策略

现在只保留清晰的领域 service 与具体 SQL store 边界，不做一实现一个 interface 的抽象工厂。

1. API、ID、DTO、事务和业务校验不暴露 SQLite rowid/JSON/FTS 方言。
2. SQLite SQL 和 FTS 放在 store 文件，领域 service 不拼 SQL；不依赖最后插入 rowid，使用应用 ID。
3. PostgreSQL 真正需要时再增加驱动和第二 store，实现已有领域行为测试；到时才提取被两个实现实际使用的窄接口。
4. 明确适配差异：`?`/`$n`、PRAGMA、TEXT JSON/JSONB、SQLite FTS5/PG 搜索、时间类型、锁与唯一约束错误。
5. 停写导出 → PG schema migration → 保留 ID/版本/UTC 导入 → 核对行数/引用/hash/租户隔离 → 重建索引 → 小流量验收 → 切换。
6. 保留原 SQLite 只读快照。回滚必须说明切换后的新增写入如何回放，不能简单把旧库重新打开导致数据丢失。

## 7. API 与 Agent 工具（均待实施）

遵循当前 httpapi 注册约定，路由不注册 `/api` 前缀；Web proxy 由现有层处理。

| 方法/路径 | 行为 |
|---|---|
| `GET/POST /v1/work-contexts` | 权限内列表/创建；游标分页 |
| `GET/PATCH /v1/work-contexts/{id}` | 详情/if_version 更新、暂停/归档/恢复 |
| `GET/POST /v1/work-contexts/{id}/checkpoints` | 修订历史/新增检查点 |
| `GET /v1/work-contexts/{id}/brief` | 结构化可用简报，含 stale/source timestamps |
| `POST /v1/work-contexts/{id}/brief-refresh` | 显式异步摘要刷新；返回 job id，不阻塞切换 |
| `GET/POST/DELETE /v1/work-contexts/{id}/resource-links` | 资源引用；删除只断开关系 |
| `GET/POST/DELETE /v1/work-contexts/{id}/session-links` | 会话关联；创建会话仍走现有 Session Runtime |
| `GET/POST /v1/work-contexts/{id}/delegations` | 关联任务/幂等创建 ContextPack + Card |
| `GET/POST/PATCH /v1/work-context-relations` | 关系、条件、版本及环检查 |
| `POST /v1/work-context-recall` | query、当前/关联/全部授权范围、时间和事实过滤 |
| `GET/POST/PATCH /v1/work-context-facts` | 查询/候选/确认/替代；权限与来源验证 |
| `GET /v1/work-contexts/{id}/events` | since cursor 的增量事件，首版轮询；必要时再 SSE |

统一错误：400 输入、401 未登录、403/404 按现有防枚举策略、409 版本/依赖冲突、422 来源/环境缺失、503 节点或连接器不可达。请求大小、分页、超时和检索上限均受控。

Agent 的 `work_context` 工具（桌面联动会话默认激活）：actions = list_streams/get/brief/recall/checkpoint/propose_fact/supersede。`list_streams` 按关键词检索本人其它主线（含检查点文本、排除归档），不需要当前会话已绑定；其余 action 都作用于已绑定主线。`propose_fact` 只在有 scope/source/observed_at 时写入候选事实；`supersede` 通过 `supersedes_id` 引用旧事实提出替代（前任只在替代被确认后才作废，不静默覆盖）。工作记忆由 Agent 随工作过程主动维护，而不是靠人逐项填表单：

- **工具层（Agent 主动维护）**：会话与主线绑定时注入维护规约提示（何时更新检查点、何时沉淀事实、冲突时如何替代、何类内容标为未核实），Agent 在轮次边界调用 `work_context` 完成更新。
- **按需整理**：`POST /v1/work-contexts/{id}/organize` 在主线已绑定的会话上排入一个**仅维护**轮次（`source=background`，提示明确要求不执行、不查生产），由 Agent 巡检并报告；无绑定会话或已归档时拒绝，不静默新建会话。
- **定期整理/冲突解决**：由既有 cron/heartbeat 周期性触发同一 organize 入口，不在桌面里另造定时器。

`work_context` 不会因为知道 context ID 就获得文件或生产权限；已有 taskboard 工具仍是执行状态的权威入口。

## 8. 代码落点与依赖

拟新增：

```text
internal/core/workcontext/             # 类型、校验、具体 SQLite store、迁移、检索
internal/services/backend/work_context*.go  # 与 Session/Taskboard/Memory 的应用协作
internal/runtime/httpapi/work_context*.go   # auth 后路由，复用 Principal
ui/web/src/features/workdesktop/       # 页面、原型数据/视图模型、样式、测试
ui/web/src/pages/WorkDesktopPage.tsx    # 与既有页面同样的 re-export
```

复用：`appRegistry.tsx` lazy 路由、Ant Design、现有 icons、React Router、TanStack Query、已有 Chat/Notes/Files/Taskboard API、modernc SQLite。WD0 不增加 npm/Go 依赖。

工作桌面可独立于 Taskboard 插件存在；插件未启用时资源/知识/对话仍可用，委派区显示不可用，不能让任务插件依赖 core workcontext 造成循环。集成在 backend 适配层完成。

## 9. 安全、运维与性能

- 每个对象、来源和关系跳转都要鉴权；仅列表过滤不够。缓存 key 包含 tenant/user/权限版本，跨桌面不能泄露私有笔记。
- WD1 先个人桌面，同租户共享资源也需授权；跨租户关系直接拒绝。
- 节点目录不自动变更现有 sandbox；打开目录、执行命令分离，生产侧默认只读。
- 读取内容也受审计和外发策略；不自动抓取任意 URL，不把敏感资源标题泄露到不可见桌面。
- 快照备份使用 SQLite backup/受控 checkpoint 流程，不只拷贝活跃 WAL 数据库的主文件。备份和恢复一并验证外部引用及权限。
- 表级版本迁移事务化，失败保留旧库；备份恢复与租户隔离是上线门禁。
- 本地索引/brief 可重建，原始 checkpoint/pack 不可由摘要覆盖。
- 结构化首屏先返回，昂贵连接器并行有 deadline；部分失败不拖垮整个桌面。
- 指标：恢复耗时、召回有证据比例、陈旧事实误用、候选积压、重复任务调查率、跨存储待补偿数、索引延迟。日志不输出敏感正文。

## 10. 分期与验收

### WD0：方案 + UI 原型（本次）

- [x] 本文明确完整领域、存储、治理、API、迁移边界。
- [x] 既有导航接入、三条关联示例主线、恢复卡、资源/依赖、对话记录、委派草稿、知识来源/审阅。
- [x] 演示数据及当前页内存保存边界明确，无真实模型、任务或生产写入。
- [ ] 用户评审信息架构和主线颗粒度。

### WD1：个人桌面持久化

- SQLite migrations、CRUD、权限、版本冲突、资源/会话引用、检查点历史。
- UI 使用 TanStack Query 替换演示状态，缺失数据保持 Empty/Error/Loading，不自动加载演示数据掩盖故障。
- 验收：重启服务/跨浏览器恢复不丢；旧会话恢复身份不变；敏感资源隔离；归档/恢复；备份恢复。

### WD2：工作委派闭环

- ContextPack、已有项目/模板/工作目录选择、幂等建卡、任务关联、状态投影、结果回流。
- 任务级 dependencies/冲突检查在现有闸门上扩展，明确人工越过审计。
- 验收：重试不重复建卡；任务在切桌面后归属不变；代理不能 done；断电后对账能修补关联。

### WD3：记忆治理与精确事实

- 当前/关联召回、来源和时间范围、事实候选/替代/陈旧、既有 memory inbox 桥接、删除传播。
- 先连接 Git/评测/部署等高信号只读来源；Screenpipe 无强依赖。
- 验收：真实问题集区分“当时”与“现在”；冲突不静默覆盖；过期标记不能误作 false；拒绝来源注入。

### WD3.5：桌面密度与 Agent 主动记忆维护（2026-10-30）

- 移除占用纵向空间的常驻说明（存储 banner、各 Tab 的整宽提示），改为悬浮 info 图标（`InfoHint`，`role="note"` 带完整 `aria-label`）；头部合并为标题行 + 小尺寸按钮，统计行压缩。
- 在每个桌面内嵌两个面板，避免跳转聊天路由打断工作上下文：`chat`（复用桌面会话本身的 snapshot + SSE + MessageFeedV2 + Composer，独立 chat store 实例）与 `files`（复用 `FilesPanel mode="dock" fillContainer`，root 取主线绑定的 directory 资源）。两个 Tab 都指向**已绑定会话/目录**，不新建。
- AI 维护入口按上文 §7：`supersede` action + `POST .../organize`。

### WD4：按实际效果增强

- 团队共享、增量推送、更好中文/语义召回、可选采集器、PG 迁移。
- 每项必须有实际瓶颈再做，不作为 WD1-WD3 上线前置。

## 11. 验证计划

### 11.1 WD0 实际验证记录（2026-10-29）

- `cd ui/web && pnpm build`：通过，包含真实 `tsc -b` 与 Vite 生产构建；仍有现有大 chunk 的体积提示。
- `cd ui/web && pnpm test`：47 个测试文件、482 个测试全部通过，包含新视图模型检查与全量 i18n 对齐。
- `cd ui/web && pnpm exec playwright test e2e/work-desktop.spec.ts --reporter=line --workers=1`：13 passed / 2 skipped；桌面、375px 手机、768px 平板覆盖，两个小屏跳过的是同一桌面专用深交互用例。
- `make docs-check` 与 `git diff --check`：通过。
- E2E 包含：无模型配置仍可访问原型、未知主线/Tab 降级、中英切换、来源抽屉、移动端无横向溢出、检查点/委派/笔记归属、草稿跨主线保留、刷新重置、候选演示审阅、归档只读、共享资源跳转与浏览器返回。
- 已修复新页面抽屉被既有 `z-index: 1200` 顶栏遮挡，沿用项目其他弹窗的 1300 层级；来源抽屉用更高一层以支持从恢复简报打开。
- Playwright 的 `chromium-tablet` 配置显式使用 Chromium，避免 iPad preset 暗中切换 WebKit；这是浏览器测试配置修正，不是新增运行依赖。
- 截图产物：`.godex/tmp/work-desktop-wide.png`、`work-desktop-chromium-mobile.png`、`work-desktop-recovery.png`。截图来自 Mock Backend + 演示数据，不是正式后端功能验收。
- 没有 Go 代码、依赖或数据库变更；因此不宣称已验证 WD1-WD3，也没有运行整个仓库的 Go 发布门禁。

### 11.2 WD1–WD3 实际验证记录（2026-10-30）

- 后端 `internal/core/workcontext`：真实 `modernc.org/sqlite`，有序 migration + reopen 持久化、`if_version` CAS、检查点/事件同事务、`(tenant_id, owner_id)` 双键隔离、依赖环拒绝、委派 `request_id` 幂等保留 pack、候选确认须有来源与 `observed_at`、替代（supersedes）延后到确认阶段并在事务内校验前任。`gofmt`/`go vet` 通过。
- 个人 Scope 由身份派生：HTTP 层从 `PrincipalFromContext` 生成 `Scope{TenantID, OwnerID}`，请求体里的伪造 `tenant_id`/`owner_id` 被忽略；跨 owner/跨租户读取返回 404；relay-only 账号在 `/v1/work-contexts` 与宿主执行面一样被 `requireCenterExecution` 拒绝。
- **远程访问的 owner 透传（2026-11-08 修）**：中继跳原本只带 scopes、不带用户身份，节点侧 `classifyPrincipal` 的 relay-trust 分支把任何中继请求定成占位 `relay-node`。而桌面 scope 由身份派生、按 `(tenant_id, owner_id)` 严格过滤，于是**远程访问节点桌面恒为空**（同库本地 3 条、经代理 0 条），且因 `canExecuteOnCenter` 对 `KindNode` 放行而返回 200 `[]` 而非 403，看起来像「坏了」而不是「被拒」。修法（方案 A）：中心转发时**断言它自己已认证的身份**——新增 `X-Godex-Vouch-Identity`，值由 NodeAccess decision 生成、**绝不从调用方拷贝**（不入 `relayForwardedHeaders` 白名单，故伪造值必被覆盖）；节点侧仅在**自身已认证的中继信道**（`RelayTrustHeader`）上采信，且只改 `UserID`/`TenantID`、**不改 `Kind`**（命名用户不得换来任何能力）。无 vouch、值为空或无法解析时保持 `relay-node` 占位——它不拥有任何数据，所以**非法/不可归因的访问得到空列表**，而不是 403，也不是别人的数据。已知边界：与 `WatchScopesHeader` 同构，故两跳（多中心）拓扑上身份不跨第二跳透传；本修复覆盖的单中心一跳即目标场景不受影响。
- 会话上下文注入：`refreshWorkContextFeedback` 在每个用户轮次边界按**已持久化的 session→context 绑定**刷新临时背景消息，多 owner 绑定同一 session 时拒绝猜测，未绑定/失效时清空而不留陈旧快照；不改动 locator 身份。
- 真实委派：`DelegateWork` 冻结不变 ContextPack → 校验既有项目/工作目录/模板 → SQL reserve → 用 `ExternalRequestID` 建真实任务卡 → SQL complete；跨存储无事务，靠看板侧持久化幂等键补偿，重放不再重复建卡。任务卡状态由 ledger 实时投影，卡缺失显式 `source_missing`。
- 知识回流：`CaptureWorkResult` 要求任务已 `in_review`/`done` 且存在真实证据，产出的始终是候选事实，需人工确认；`ProposeWorkMemory` 只把已确认事实写入**既有** memory 候选箱，不直接写 durable memory。
- 前端生产页不再加载任何 demo：空库是正常首态，loading/error/409/归档只读显式；委派建真实卡但**不自动执行**，执行/验收严格手动；未知主线改为「提示 + 展示第一条」而非静默清空。
- 验证命令：
  - `go test ./internal/core/workcontext ./internal/services/backend ./internal/runtime/httpapi ./internal/plugins/taskboard ./internal/core/memory ./internal/core/conversation -count=1`：通过。
  - `cd ui/web && pnpm build`（`tsc -b` + Vite 生产构建）、`pnpm test`：48 文件 / 489 测试通过。
  - `pnpm exec playwright test e2e/work-desktop.spec.ts`：14 passed / 4 skipped（深交互用例在小屏跳过）。
- 已知的仓库既有失败（与本次改动无关，基线同样失败）：`internal/architecture` 的 `TestFrontendSourceLineBudget`（`ChatPageView.tsx` 920 行）与 `TestGoFunctionComplexityBudget`（`routes_account.go:registerMemberRoutes` score 45）；另有 `internal/runtime/cron`、`internal/core/background`、`services/backend` TempDir 清理在满负载下的偶发 flake，单测隔离运行通过。

### 11.3 WD3.5 实际验证记录（2026-10-30）

- 后端：`work_context` 工具新增 `supersede`（缺 `supersedes_id` 拒绝）；`OrganizeWorkContext` 在无绑定会话时报错、归档主线报 `ErrArchived`；新增路由 `POST /v1/work-contexts/{id}/organize`；新增单测 `TestOrganizeWorkContextRefusesUnlinkedStream`。
- 前端：移除顶部常驻 banner 与各 Tab 整宽提示，改 `InfoHint` 悬浮；新增 `chat`/`files` 内嵌 Tab 与 `WorkChatDock`；`pnpm typecheck`、`pnpm build`、`pnpm vitest run src/i18n test/accountSurfacesI18n.test.ts`（9 通过）、`src/features/workdesktop`（12 通过）均通过；`playwright test e2e/work-desktop.spec.ts --project=chromium-desktop` 6 passed。
- 无新增依赖（复用 antd `Tooltip`、既有 `FilesPanel`/`MessageFeedV2`/`Composer`/`FilesPanel`）。

### 11.4 WD3.6 工作记忆存储与召回完善（2026-10-30）

- 状态过滤：recall（有 query 与无 query 两条路径）都排除 `superseded`/`rejected`，`stale` 保留（stale 是「需重核」而非 false，符合 §5.2）。历史仍在桌面详情可见，只是不进入召回。
- FTS 接线：`wc_facts_fts` 新投影（migration v2）承接事实候选匹配，外加 LIKE 兜底；`wc_contexts_fts` 引出 `SearchContexts` 并接入 `work_context` 的 `list_streams`，使主线索引真正可读（此前只写不读）。
- 中文：沿用 §5.4 的「应用层分词」，索引文本预分词（探测确认 unicode61 对中文无效、trigram 仅 ≥3 字，故预分词是唯一稳妥路径）。
- 关系跳转保持**一跳**（§5.4 明确默认一跳、有上限）；多跳 `relationWalkDepth` 仅用于写时 `wouldCycleTx` 环检测，不作为召回扩展。
- 测试：`TestRecallMatchesShortChineseAndHidesRetiredFacts`（2 字中文命中、退役不可召回、stale 保留）、`TestSearchContextsMatchesCheckpointText`（检查点文本命中、归档排除、空串=List）。
- 门禁：`go vet`/`go test ./internal/core/workcontext ./internal/runtime/httpapi ./internal/services/backend` 通过（`services/backend` 的 `TestHarnessMetadataRejectsUnavailableHarness` 为既有 TempDir 清理 flake，隔离复跑通过）；`make docs-check`、`git diff --check` 通过。
- 未做（有意）：资源侧检索（资源是有界的手工清单，`ListResources` 全量返回即可）、关系多跳召回（与 §5.4 冲突）。

### WD3.7 工作焦点、计划与就地编辑（2026-10-31）

能力侧（解决“工具不能充分理解和辅助管理桌面”）：

- **计划是一等实体，不是检查点的自由文本**。新增 `wc_todos`（migration v3）+ `Todo{status todo|doing|done|blocked, owner, due_at}`，`CreateTodo`/`PatchTodo`（`if_version` CAS）/`DeleteTodo`，每次变更都写 `wc_events`，因而计划变更会自动进入“自上次以来的变动”。
- **焦点 = 计划 + 变动**。新增 `wc_seen`（migration v3）与 `Focus{last_seen_at, changes, todos}`；`GET .../focus` 只读，`POST .../seen` 才推进水位（查看不是编辑，不动 context version）。`ack`（工具）/`seen`（HTTP）都是显式动作，避免“打开桌面即静默清掉变动高亮”。
- **工具 action 扩展**：`work_context` 增加 `focus` / `ack` / `plan` / `todo_patch`（`get` 的 detail 已带 `todos`）；`todo_patch` 支持 status/owner/due_at/text，允许 Agent 边工作边维护计划。
- **`if_version` 是按 action 分实体的，工具必须说清**。同一个字段在 `checkpoint` 上指主线（stream）版本，在 `todo_patch` 上指**该条计划自己的版本**（通常 1-2，永远不是主线版本）——传错实体的版本必然冲突。因此：① `todo_patch` 的 `if_version=0`（省略）表示无前置校验（与 `SaveCheckpoint` 一致，仍由事务内 `WHERE version=?` 防并发覆盖）；② 冲突错误必须可行动——报出实体（plan item）、id、你传的版本与**当前版本**，并给出重试指引，而不是裸的 "version conflict"；③ schema/描述显式写明 per-action 语义。此前 `PatchTodo` 无条件比较版本且报错不可行动，导致「想标一条计划完成却被无解地挡回」。
- **台账工具是可装卸的 bundle，不是 always_on 成员**。`work_context` 注册为 `ToolMeta{Bundle: "work_context", DefaultActive: true}`（原为 `AlwaysActive: true`）：`always_on` 是**模板固定**束，`tool_exchange` 明确拒绝开关，所以旧注册方式使台账工具无法按需激活。改为独立 bundle 后既保持绑定会话默认可用（注册后显式 `ActivateBundles`，因为 `RegisterTools` 已跑过 `ActivateDefaults`），又能被 `tool_exchange` 开/关；并在 `bundleAliases` 补「台账/工作桌面/工作主线/检查点/计划/事实」等别名，使自然语言查询能发现它。
- **会话注入追加**：`renderWorkContextFeedback` 现在同时注入「计划（受阻/进行中优先，含负责人与截止）」与「自上次查看以来的变动（最新在前）」以及 `ack` 提示，Agent 在任意轮次都能看到桌面焦点而不必再查询。
- **内置模板按入口注入**：`work-desk` 内置模板（`internal/core/templates/builtin/work-desk.yaml`，`scenarios: [workdesk]`）只授予桌面会话；`work_context` 工具本身仍由后端按“模板=work-desk 或已绑定主线”门控（`load.go`），不是仅靠模板文本。二者是双保险：模板是场景提示，门控是权限边界。

UI 侧（解决“对话不能滚动 / 顶部标题栏无用 / 交互割裂 / 信息密度低”）：

- 去掉注册表头部的 title/subtitle 与 shell header（`headerTitleKey: ""`），说明移到导航图标悬浮提示并扩充文案。
- 修复内嵌对话滚动：`.wd-chat-feed` 补上 `display:flex; flex-direction:column`，与 Flow 面板一致的完整高度链（`.chat-feed-v2-scrollport { flex:1; min-height:0 }`）。**缺这一条时 scrollport 会按内容撑高，被 `overflow:hidden` 裁掉，永远无法滚动**——这是滚动 bug 的真实根因。
- 原地编辑成为默认交互：新增 `workDesktopInline.tsx`（`InlineEdit`/`InlineSelect`，Enter 保存、Esc 取消、blur 保存、失败保留输入并显示错误，编辑中不被后台 refetch 覆盖）。
- 资源改为气泡瀑布：新增 `ResourceChips.tsx`，按 kind 上色/配图标，点击**就地展开**（不再打开抽屉），展开时字段就地编辑、外链就地跳转、目录资源可就地跳到 files Tab。资源更新走同一 upsert 接口带 `id`，共享资源是被更新而不是被复制。
- 计划面板：新增 `TodoBoard.tsx`，状态/文本/负责人/截止时间全部原位编辑，底部一行内联新增；概览新增「自上次查看以来的变动」卡片（含最近查看时间与「标记已读」）。

验证（2026-10-31）：

- `go build ./...` 通过；`go test ./internal/core/workcontext ./internal/core/templates ./internal/runtime/httpapi` 通过；新增 `TestTodosLifecycleAndEvents`、`TestTodosRejectMissingTextAndBadDueAt`、`TestFocusTracksChangesSinceSeen`、`TestWorkContextHTTPTodosAndFocus`（覆盖 CAS 冲突 409、Focus 不推进水位、seen 为空变动）。
- `pnpm typecheck` 通过；`pnpm vitest run` 48 文件 / 489 通过；`vitest run src/i18n` 对齐门禁通过（新增键中英同步）。
- `playwright test work-desktop.spec.ts --project=chromium-desktop` 7 passed（新增「计划原地编辑 + 标记已读」用例）。
- 已知的仓库既有失败不变：`internal/architecture` 的 `TestFrontendSourceLineBudget`（`ChatPageView.tsx` 920 行）与 `TestGoFunctionComplexityBudget`（`routes_account.go:registerMemberRoutes` score 45）；两个文件本轮均未改动，`git show HEAD:` 基线同样失败。另有 `internal/services/backend` 的 TempDir 清理类 flake，隔离复跑通过。

### WD3.8 注意力收件箱与主动提醒（2026-10-31）

目标：从「你要来看桌面」变成「有进展才叫你」——并行推进六七条主线时能一键切换、一眼扫清、有进展才被打扰。

- **进展不再依赖你打开桌面**。此前卡状态事件只在读 `WorkContextDetail` 时才写入（按 version 去重），taskboard 自己不写工作桌面事件；因此「有进展」只在你访问后才存在。新增后台巡检（`work_context_attention.go`，挂在既有 `Service.Start` 的独占进程锁与 runCtx 上，与 Flow reconciler 同生命周期），按每个 (tenant, owner) 主动投影卡状态并写事件。
- **写事件与读事件共用同一去重键**（`task:<cardID>:v<version>`），所以巡检先写过之后再打开桌面不会重复，未读计数才是诚实的。
- **注意力收件箱**（`GET /v1/work-attention`）：跨主线汇总「需要你动作」的条目——待验收委派（决策类，恒为 high）、过期/即将到期的计划项；每条带 `context_id` 与**应跳转的页签**（tasks / plan），前端一键切主线+页签，不用回墙上找。同一响应带每条主线的未读数，供主线列表打红点。打开收件箱**只读**，不推进 seen 水位。
- **推送纪律**（决定了这功能是帮忙还是添乱）：只对决策类（卡进 `in_review`）实时推送，纯进展只落事件抬未读；每条按 dedup 键去重，一分钟一次的巡检不会一分钟推一次；静默时段 22:00–08:00（本地时间）；每人每日上限 12 条。**纪律检查在写事件之前**：否则被静默时段拦下的提醒会被记成「已发」而永久丢失——这是提醒功能绝不能有的结果。
- **推送按所有者隔离**（安全边界）：`webpush` 订阅新增归属（`SubscribeFor`/`NotifyOwner`），归属键 = `workcontext.ScopeKey{tenant, owner}`，由 `NewPushOwnerResolver` 用与全站一致的 principal 分类器解析；通知正文引用的是该用户的主线标题，因此绝不能广播。归属为空的订阅只收广播，收不到任何人的工作条目；`NotifyOwner("")` 直接返回 0 而不是退化成广播（避免把隔离 bug 变成跨用户泄露）。
- **订阅持久化 + 进桌面静默重订**（2026-10-31 补）：订阅原先只在内存，且只有手动去设置页点「启用」才注册——两者叠加的实际后果是「不去设置页就一条也收不到」，服务一重启更是全部清零，对有进展才叫人的功能等于白做。现在 `LoadOrCreate` 绑定的服务把注册表落盘到 `state/push_subscriptions.json`（含归属，故 owner 隔离也跨重启成立；temp+rename 原子写；`New`/`NewWithKeys` 仍为内存态）；失效端点被剔除时同步落盘，浏览器退订后不会被下次重启复活。前端在工作桌面挂载时调 `ensurePushSubscriptionSilent`：**已授权才重订、从不弹窗**（权限请求只能在用户手势里发起，页面加载弹窗只会训练用户点「阻止」，永久静音掉他要的提醒）；未授权则保持安静。
- **不做**：让 LLM 判断「什么时候该提醒」（不确定、要定时起模型、会幻觉）；通用 IM 集成；新建提醒实体（复用 `wc_todos.due_at` 与既有 events 表，**无新 migration**）。

验证（2026-10-31）：

- 新增 `TestAttentionSweepRecordsProgressWithoutOpeningTheDesktop`（巡检独立落事件 + 读桌面不重复计数 + 决策类推送）、`TestAttentionSweepDoesNotRenotify`（重复巡检不重复推送）、`TestPushDisciplineQuietHoursAndCap`（静默时段拦截且**不烧掉**提醒，日上限生效）、`TestAttentionInboxSurfacesDecisionsAndDues`（决策排最前、带正确跳转页签与计数）、`TestUnreadCountsArePerStreamAndRespectSeeWatermark`、`TestAttentionSweepNeverCrossesOwners`（两个 owner 的条目不会串投）。
- `go build ./...`、`go vet` 通过；`go test ./internal/core/workcontext ./internal/services/webpush` 通过；`internal/services/backend` 与 `internal/runtime/httpapi` 除既有 TempDir 清理 flake（隔离复跑通过）外通过。订阅持久化新增 `TestSubscriptionsSurviveRestart`、`TestOwnerScopingSurvivesRestart`（重启后 owner 隔离仍成立）、`TestUnsubscribeIsPersisted`（退订不被重启复活）、`TestUnattributedSubscriptionNeverReceivesOwnerNudges`、`TestInMemoryServiceDoesNotTouchDisk`，`webpush` 共 10 个测试通过。
- 前端：`pnpm typecheck` 通过；`vitest run` 49 文件 / 492 通过（含新增 `test/pushSilent.test.ts`：未授权绝不弹窗、拒绝权限不抛错、不支持浏览器直接跳过）；`playwright work-desktop --project=chromium-desktop` **8 passed**（新增「跨主线收件箱并可跳到对应页签」用例，含「打开收件箱不标记已读」断言）。
- 既有门禁失败不变（`ChatPageView.tsx` 920 行、`routes_account.go:registerMemberRoutes` 45），两个文件本轮未改。

### WD3.9 标题/背景/目标就地编辑与关联已有对话（2026-11-03）

目标：桌面上的编辑不要再打断工作上下文——改标题不该开弹窗，拉回一条已经发生过的对话不该去别处翻会话 ID。

- **就地编辑（标题/背景/目标）**：主线的标题、背景、目标不再走「编辑主线」弹窗，而是在标题旁加一个铅笔按钮、在概览卡片里直接点字段编辑（复用既有 `InlineEdit` 与 `PATCH /v1/work-contexts/{id}`，**无后端改动**）。`InlineEdit` 新增 `editIconLabel` 模式：值保持静态、只有铅笔可点——因为把标题包进 `<button>` 会让它不再是 heading（既有 e2e 依赖 `getByRole("heading")`，屏幕阅读器也依赖它）。背景/目标仍用点值即编辑，回车提交、Esc 取消、失焦保存，保存失败保持编辑态。原来的「编辑主线」按钮与 `editContext` 弹窗分支及 i18n 键一并删除（不再是死代码）。
- **关联已有对话**：新增 `LinkSessionPicker.tsx`——拉取 `GET /sessions`，**只列出尚未关联本主线的会话**，选定后把会话标题预填为主题（后端拒绝空主题），提交走既有 `POST /v1/work-contexts/{id}/sessions`。选择器而不是「填会话 ID」输入框，因为用户认得出对话的标题、认不出不透明 id。关联只记录引用：**不移动、不重命名、不删除原会话**（e2e 断言写入中无 DELETE）。会话列表仅在拾取器打开时才拉取，不进桌面首屏渲染路径。
- **既有 e2e 竞态根因修复**：本轮的 InlineEdit（概览卡片多两个实例）让概览渲染变重，暴露出既有 e2e 的一个既有竞态——保存后未等 antd 弹窗**卸载完成**就去断言/点击：弹窗仍在时它的输入框也匹配断言文本（strict 冲突），残留遮罩还会吞掉下一次点击（表现为「点了另一个 tab 却没切换」）。这不是本轮引入的 bug，但本轮把它从偶发变成必现。修法是根因层：每次弹窗保存后都 `await expect(page.locator(".ant-modal-wrap")).toHaveCount(0)` 再继续。已用二分（回退页面文件则 3/3 通过，带改动 3/3 失败）定位到确为本轮渲染变化触发。

验证（2026-11-03）：

- 前端：`pnpm typecheck` 通过；`vitest run` 49 文件 / 492 通过；`vitest run src/i18n` 中英对齐通过（新增 `editTitle`/`linkExistingHint`/`linkPickSession`/`linkNoAvailable` 同步，删除死键 `editContext`）。
- `playwright work-desktop --project=chromium-desktop` **10 passed**（新增「铅笔就地改标题/背景」与「关联已有对话且不移动原会话」两条用例）；被暴露的既有用例 `--repeat-each=5` 5 次全过。
- 无后端改动，故二进制无需重建；仅重建前端产物（webui 优先读盘，刷新即生效，不重启服务）。

### WD3.10 概览展示全面就地编辑（2026-11-03）

目标：把「其余展示也改成原地编辑」落到实处——概览上能看到的值，都应当当场改，而不是回到弹窗、也不是只读。

- **检查点全部字段就地编辑**：当前进展、下一步、最近完成、阻塞、待确认、证据引用均可在概览直接改，**「编辑检查点」按钮与 `editContext` 式弹窗分支一并删除**。列表字段用「每行一项」文本就地编辑、渲染回 `<ol>/<ul>`（用户输入与阅读同一套格式）。
- **列表用铅笔模式而非裸点值**：列表是块级内容，把它嵌进点击目标的 `<button>` 里是非法 HTML，屏幕阅读器输出也会错乱；所以列表值保持静态标记，只有铅笔可点（复用 WD3.9 的 `editIconLabel`）。单行字段仍保留「点值即编辑」的手感。
- **一次字段编辑仍提交整份检查点**：后端检查点是「一份带版本的文档」而非逐字段 patch，所以 `checkpointMutation` 改为读当前文档 + 只替换被改字段再整体提交。该合并在页面层（只有它知道当前文档），`OverviewPanel` 只负责把 `Partial<CheckpointInput>` 交给它。这避免了新 UI 把未触碰字段静默清空（e2e 断言 `completed` 在改 `summary` 后未丢）。
- **会话主题就地编辑**：主题对话卡片标题改为可就地重命名，走既有 `POST /v1/work-contexts/{id}/sessions`——后端对已存在链接是**更新 topic** 而非新增第二条（`LinkSession` 现有语义，未新增接口）。
- **不做（后端不支持内容更新，故不做以免造假接口）**：关联条件、事实内容。`PATCH .../facts/{id}` 只收 `status`（`FactReview`），relation 只有新增/删除，没有改条件/内容的路径。要支持需新增后端接口，另行评估。
- **行数预算**：页面越改越大的真风险是撞 900 行门禁；把概览（含检查点就地编辑 + 变动卡片）整体抽成 `OverviewPanel.tsx`，页面从 884 降到 **844** 行。
- **清死键**：弹窗删除后 `checkpoint`（编辑检查点）、`checkpointInProgress`、`checkBlank` 变成死 i18n 键，一并删除（中英同步，parity 门禁通过）。

验证（2026-11-03）：

- 前端：`pnpm typecheck` 通过；`vitest run` 49 文件 / 492 通过；`vitest run src/i18n` 中英对齐通过。
- `playwright work-desktop --project=chromium-desktop` **10 passed**。「saves checkpoints…」用例改为就地编辑：断言 `POST /checkpoints` 与 revision 自增、未触碰的 `completed` 未丢、以及**全程无弹窗**（`getByRole("dialog")` 计数 0）。只读门禁用例从「编辑检查点」按钮改为断言标题铅笔 `disabled`（就地编辑器同样受 readOnly 约束）。
- 无后端改动，故不重建二进制、不重启服务；仅重建前端产物并只读核验。

### WD3.11 目标需就地编辑 + 多行文本原样渲染（2026-11-03）

目标：把「原地编辑」推进到标题区，并让多行文本真的按多行显示——不能编辑的值要能改，能编辑的值不能在渲染时被压成一行。

- **标题下的 subtitle 就是目标，现在可编辑**：它原来是个只读 `Typography.Paragraph`，显示 `goal || background`。改为 `InlineEdit`（`rich`），只绑 `goal`——**不再回退到 `background`**，否则一个空目标会显示成背景、用户「编辑目标」时看到的是背景文本，保存下去就把背景写成了目标。
- **多行文本原样渲染（复用 `MarkdownContent`，不新写渲染器）**：`MarkdownContent` 已经是现成能力——纯文本路径用 `white-space: pre-wrap` 保留换行，检测到 markdown 语法才进 `MarkdownRenderer`（已挂 `remark-breaks` + `rehype-sanitize` 白名单）。所以换行与 markdown 用同一条路径解决，且有既有消毒保障。`InlineEdit` 新增 `rich` 模式，把值交给 `MarkdownContent`。
- **`rich` 自动隐含铅笔模式**：markdown 会产出 `p/ul/ol` 等块级元素，嵌进可点击的 `<button>` 里是非法 HTML，所以 `rich` 的值必须是静态标记、只有铅笔可点（与列表用铅笔同一个理由）。包装元素也必须是块级（`div`），否则浏览器会把 `div` 从 `span` 里重排出去。标题仍不能用「点值即编辑」（必须保持 heading 角色）。
- **应用到哪些多行字段**：目标（subtitle + 概览）、背景、检查点 summary、资源 purpose，知识卡片 `fact.content` 展示（只读→`MarkdownContent`），以及简报里 background/goal。检查点 summary 同时补上 `multiline`——它在旧弹窗里本来就是 3 行 textarea，之前就地编辑沿用了单行契约（回车提交），是一个漏掉的行为降级。
- **e2e 抓到的真后果（本轮我自己改出来的）**：summary 改成多行后，旧 `press("Enter")` 只插入换行不再提交，断言因此失败。修法是改断言（`Control+Enter` 提交），不是改回去。两个铅笔的可达名需要区分：标题区铅笔叫「编辑目标」，概览卡片铅笔仍是裸「目标」。

验证（2026-11-03）：

- 前端：`pnpm typecheck` 通过；`vitest run` 49 文件 / 492 通过；`vitest run src/i18n` 中英对齐通过（新增 `editGoal` 中英同步）。
- `playwright work-desktop --project=chromium-desktop` **11 passed**。新增用例：标题下目标渲染为真实列表（`li` 计数 2，单换行不丢）、经「编辑目标」铅笔就地改写、`PATCH /work-contexts/ctx-1` 写入、全程无弹窗。
- 无后端改动，故不重建二进制、不重启服务；仅重建前端产物并只读核验。

### WD3.12 内嵌对话状态栏 + 会话双开方式 + 布局高度预算（2026-11-03）

目标：桌面上的对话要“看得见状态、放得下输入框”，切换/打开会话要有选择，顶部不要再占固定高度。

- **顶部收拢（症状：“输入框要往上滚才看得到”）**：真根因不是 padding，而是 work-desktop 路由从未采用视图受限外壳——`.godex-content` 是整页滚动，而对话 dock 在用 `min(72vh, 760px)` 自己猜高度，于是 composer 被推到首屏外。修法是根因层：路由加 `shellClassName: "chat-shell"`（与 Chat 同外壳），`.wd-page` 改为列内滚动，并让 `.wd-main > .ant-tabs` 撑满剩余高度（tab 栏固定、面板滚动）。另两处关键：基类 `.wd-layout` 是 `align-items: start`，不改成 `stretch` 则中列永远不会撑满；chat/files 面板自己管滚动，所以它们的 pane 必须 `overflow: hidden`（用 `:has()`）否则会双滚动条。`<1024px` 保留单页滚动。
- **顶部内容搬家**：副标题（目标）与四个统计计数从固定头部移入概览。它们描述“这条主线是什么”，不是“我在哪”，放在概览更合理，也把头部高度让给了对话。计数移到概览后头部只剩标题行 + 操作按钮。
- **“待我处理”右移**：从主区顶部移到右侧栏顶部（那里本来就是资源/关联侧栏），不再占用中列垂直空间。侧栏窄，行内加 `flex-wrap` 并把冗余的“打开”链接收窄。
- **范围 switch（当前桌面/全部桌面）**：内侧新增 `Segmented`，本地 state 默认“全部桌面”（跨主线汇总的初衷），切“当前桌面”只看本条。**过滤在页面层做**（`scopedAttention`），AttentionInbox 只负责展示——取数与展示解耦。
- **会话两种打开方式**：主题对话卡片从单一“打开对话”改为“在桌面打开”（切到 chat tab 且带 `?session=<id>`，不离开桌面）+“跳转 Chat 打开”（原行为）。页面把 `session` 参数纳入 chat tab 的目标会话选择，所以“在桌面打开”能精确指向某条会话，而不是永远只开第一条。
- **内嵌对话状态栏（对齐 Chat）**：dock 顶部补上模型名 + 上下文用量徽章 + 流连接状态 + 运行/空闲 + 刷新。**复用 Chat 的同一个 `ContextStatusInline` 与 `buildContextStatusSummary`**，不是长得像的复制品——所以你看到的“上下文用了多少”在两边含义一致。
- **顺手修掉的真 bug（e2e 抽出来的）**：dock 把快照与三个状态查询都门控在 `!!token` 上，而桌面页本身的查询只门控 `!!contextId`（token 可空）。在 `auth_required=false` 的部署里没有 token 但会话完全可读，于是 dock 的**整个 feed 和状态栏都永远空白**。已改为与页面一致地只门控会话。
- **e2e 断言含金量**：状态栏只断言“徽章可见”是抓不住这个 bug 的（坏的时候它也可见，只是显示 `ctx 0 · calls 0 · msgs 0`），所以断言改成校验数值（`1.2k/10k`）与 `chat-composer` 在视口内（`toBeInViewport`，直接盯住本次痛点）。

验证（2026-11-03）：

- 前端：`pnpm typecheck` 通过；`vitest run` 49 文件 / 492 通过；`vitest run src/i18n` 中英对齐通过（新增 `openInDesktop`/`attentionScope*`/`refresh`，删除死键 `editGoal` 与死样式 `.wd-subtitle`）。
- `playwright work-desktop` **三端 33 passed / 6 skipped**（窄屏用例按设计跳过）。新增用例：会话在桌面内打开且状态栏显示真实数值、注意力范围 switch 真的会过滤（给另一条主线造了一条过期项，切“当前桌面”后它必须消失）。“saves checkpoints…” 用例里的旧按钮名“打开对话”改为“跳转 Chat 打开”。
- 无后端改动，故不重建二进制、不重启服务；仅重建前端产物并只读核验。

### WD3.13 对话滚动修复 + 各 tab 等宽（2026-11-03）

两个都是 WD3.12 那套高度 CSS 的回踏，共同点是**当时没有任何测试覆盖**（dock 的 snapshot mock 一直返回空消息），所以两个 bug 都是靠**实测数字**定位，不是靠推理。

- **病状一：对话上下滚不动，输入框看不到**。真根因是 **CSS 级联顺序**：我为填充视口写的 `@media (min-width: 1024px)` 块放在文件**开头**，而基类 `.wd-layout { align-items: start }` 在**后面**——同特异性下后者胜出，于是 `align-items: stretch` **从未生效**。后果是中列被内容撑到 5069px，再被 `.wd-layout{overflow:hidden}` 裁到 862px，所以 feed 永不滚动、composer 被裁在视口外。修法：把整块填充规则**移到文件末尾**（基类之后），并加注释说明“这个位置是有意的”，避免下次被“整理文件顺序”再次弄坏。
- **病状二：其它 tab 左右留白多、宽度与主线对话不一致**。真根因是 `.wd-page` **shrink-to-fit**：它在一个**列向 flex 容器**里，而 `margin: 0 auto` 在 cross 轴上会**取消默认的 stretch**，于是页宽变成**内容宽度**。每个 tab 各按自己内容定宽，窄的就在空白里居中。修法：补 `width: 100%`（保留 `margin: 0 auto` 与 `max-width` 用于居中上限），所有 tab 共用同一宽度。
- **实测证据**（不是推测）：修前 `.wd-main` h=5069px、`.wd-layout` `alignItems: "start"`、`.wd-page` `natural === maxContent === 1359` 而父级 1744；修后 `align: stretch`、`dock: 724`、`feedClient 563 < feedScroll 4770`（可滚）、全部 tab `pageW === parentW === 1744`、`overflowPx: 0`。
- **e2e 补上真正缺失的覆盖**：mock 的 snapshot 现在返回 40 条消息，新增用例断言（①）长对话**确实溢出**（`scrollHeight > clientHeight`）、（②）滚到底能看到最后一条、（③）composer 在视口内、（④）各 tab 宽度相等且无横向溢出。前两个断言在修前会失败——这正是之前漏掉的。
- **窄屏门控要精确**：composer 视口断言只在 `>=1024px` 跑（窄屏按设计是整页滚动，是单独的待办）；**状态栏断言不做门控**，因为它要防的失败模式（dock 查询被 token 错误门控导致全空）与视口宽度无关，为了窄屏有覆盖而整体跳过会丢掉真正的保护。

验证（2026-11-03）：

- 前端：`pnpm typecheck` 通过；`vitest run` 49 文件 / 492 通过；`vitest run src/i18n` 中英对齐通过（无新增文案）。
- `playwright work-desktop` **三端 37 passed / 8 skipped / 0 failed**。
- 无后端改动，故不重建二进制、不重启服务；仅重建前端产物并只读核验。

### WD3.14 知识与回忆渲染可读性（2026-11-03）

背景：知识/回忆是工作桌面里信息密度最高的地方（长文本、列表、带换行的标题与证据摘录），却是最不可读的一块。分三件事修，每件都有可复现的根因。

- **一、Markdown 不渲染**。`fact.content` 以前走 `MarkdownContent`，但它默认走「看起来像 markdown 才渲染」的启发式——那套启发式的存在理由是**不让工具偶发输出里的 `*` / `_` / 路径被 markdown 误伤**，而**知识条目是人工/有意撰写的文本**（标题、列表本来就是想渲染的）。所以知识条目改用 `forceMarkdown`。这是个语义修正：同一个组件，两种来源，用不同策略。
- **二、换行丢失（两个字段）**。① 卡片**标题**：`.wd-knowledge .ant-card-head-title { white-space: normal }` 会把标题里的换行**压成一行**，于是带换行的标题变成一句连读的乱句。② **来源证据** `source_excerpt`：原先走 `Typography.Paragraph`，而 Paragraph 默认 `white-space: normal`，同样压掉换行。两处都改成 `pre-wrap` + `break-words`——证据摘录常是从日志/报告里引的，**引文的原始版式本身就是证据**，不能压。
- **三、长条目折叠**。知识条目字数很多，一屏铺满全文就没法扫。抽出 `KnowledgeCard` 组件（页面已 863/900 行，必须外移），按**行**折叠（`PREVIEW_LINES = 4`），默认只显示开头几行，一次点击展开。折叠一并隐藏 `适用范围/观测时间/来源/证据/替代关系` 这些细节——折叠态就是要简明扼要。
- **一个实现取舍**：折叠用**按行切片**而不是 CSS `-webkit-line-clamp`。行切片在**渲染前**就决定了内容和 DOM 规模（e2e 能断言列表项数），而且行分割对 markdown 块结构是可预期的；clamp 只是视觉裁剪，DOM 里全长文本仍在，也断不了「到底藏了几行」。
- **e2e 对准能真失败的断言**：① 折叠态 `li` 数 = 3、展开后 = 5（**断言元素数而非「卡片存在」**，否则折叠被删掉也照样通过）② `h1` 文本 = 根因分析（证明真走了 markdown）③ 标题与证据的 `white-space` 计算值是 `pre-wrap`（**只能比计算样式**：`textContent` 两种情况下都保留 `\n`，只有 `white-space` 能区分「保留」与「塔陷成一行」）④ 能折回去。

验证（2026-11-03）：

- 前端：`pnpm typecheck` 通过；`vitest run` 49 文件 / 492 通过；i18n 中英对齐通过（新增 `expand`/`collapse` 两键）。
- `playwright work-desktop` **三端 40 passed / 8 skipped / 0 failed**（新增用例 1 条，全项目跑）。
- arch 门禁两个失败已实测为**既有基线**（`ChatPageView.tsx` 在 HEAD 就是 920 行；`routes_account.go` 本次未触碰）。
- 无后端改动，故不重建二进制、不重启服务；仅重建前端产物并只读核验。

### WD3.15 重启后内嵌对话打不开（读之前必须先 open）+ 排队消息入内嵌对话（2026-11-06）

两个都是「桌面内嵌对话与 Chat 页行为不一致」的后果：Chat 页会做的事，dock 少做了一步。

- **一、重启后对话一直转圈，去 Chat 页打开一次才恢复**。真根因：**会话读接口只读驻留内存的会话**。`GET /sessions/{id}` → `Snapshot` → `requireSession`（`internal/services/backend/session.go:1080`）只查内存 map，查不到就 `ErrSessionNotFound` → HTTP 404；而 `POST /sessions`（`OpenSession`）才会从磁盘 hydrate 一个已持久化但未驻留的会话。服务重启后所有会话都是「已落盘但未驻留」，所以 dock 一直 404。Chat 页从来是先 `POST /sessions`（用完整 locator 重算 id）再读快照，而 dock **只读不 open** ➜ 只有先去 Chat 页把它 hydrate 一次，dock 才能读。修法不是加重试，而是**把 dock 补上 `openSession` 这一步**：先 `GET /sessions` 把存储的 `session_id` 解回**完整 locator**（裸 id 无法重哈希；桌面只存 id），再 `POST /sessions` 打开，然后把下面所有读都改门控在**已打开**的会话上（`openSessionId`），而不是存储的 id。这样桌面自给自足，不再依赖「先去 Chat 页」。
- **二、open 失败不再伪装成空会话**。open 报错时显式报错 + 重试（新增 `chatOpenFailed` 键），而不是渲染成「这条对话没有消息」——空会话和打不开是两回事。
- **三、排队消息接进内嵌对话**。dock 原来只本地拼 `pendingSends`（乐观气泡），**从不消费 `snapshot.queued_turns`** ——而服务端在 turn 运行中收到的消息是进入**服务端队列**、不产生本地 pending 的，于是「运行中发的消息静默消失」。现在 dock 渲染与 Chat 页同款 `.chat-queued-strip` / `.chat-queued-item`（直接复用 `stylesChatContentDetails.css` 与 `chat.queuedMessage/editQueued/cancelQueued/steerQueued` 这批既有 i18n 键，不复制一套），状态行追加 `· N 排队中的消息`，每条带「编辑」（`cancelQueuedTurn`，服务端返回原文 ➜ 回填 `composerRef.setText` 供重发）与「引导」（`steerQueuedTurn`）。
- **e2e 用真实成因建模**：mock 新增 `coldStart` 状态（会话未驻留时读返回 404、`POST /sessions` 才置为驻留）与 `POST /sessions`（由 locator key 反推回同一 session_id）。断言不是「dock 可见」，而是① 长对话渲染出来（`消息 40` 可见）② 真的发生了 `POST /sessions`。两个用例都**反向验证过非空洞**：去掉各自修复后会真失败（重启用例停在 feed 不可见；队列用例停在 strip 不可见）。

验证（2026-11-06）：

- 前端：`pnpm typecheck` 通过；`vitest run` 49 文件 / 492 通过；i18n 中英对齐通过（新增 `chatOpenFailed` 键）。
- `playwright work-desktop` **三端 54 跑 46 passed / 8 skipped / 0 failed**（新增「重启后不开 Chat 页也能打开对话」用例；窄屏用例按设计跳过）。
- 无后端改动（是前端少了一步 open），故不重建二进制；仅重建前端产物并只读核验。
- arch 门禁两个失败仍为**既有基线**（`ChatPageView.tsx` 920 行、`routes_account.go` 未触碰）。

### WD3.16 注意力收件箱完整化 + 合批 + 杠杆排序（AE1，2026-11-07）

WD3.8 之后，收件箱只覆盖三类信号（待验收委派、过期/即将到期的计划项），用户仍须去别的板块手工扫一遍——这正是收件箱要消除的 O(N) 行为。本轮按 [work-context-attention-economics.md](./work-context-attention-economics.md) §AE1 把它补齐。

- **新增信号**（`internal/core/workcontext/attention_signals.go`）：待审 candidate 事实、`blocked` 计划项（**不要求有截止时间**——`ListOpenPlanTodos` 服务于截止日故过滤 `due_at <> ''`，恰好看不见「卡住但没定日期」）、每条主线被多少其它主线 `depends_on`（被依赖数）。三者都 JOIN `wc_contexts` 过滤 `owner_id`，跨主线也绝不越所有者。
- **新 kind**：`delegation_missing`（委派卡被删，原来静默跳过——一条无人能解析的链接本身就是待决事项）、`fact_review`、`todo_blocked`、`checkpoint_blocker`、`checkpoint_question`。
- **合批与杠杆**：同主线同 kind 折叠为一行（`count` + `also`），排序改为 `priority → 被依赖数 → 最早截止 → 稳定 tiebreak`。
- **隔离边界（范围变更）**：「flow 失败 run」原计划并入，实施前核对发现 `flowRunRecord`（`internal/agent/flow_store.go`）**无 tenant/owner 字段**、且 flow 与 work stream 无数据模型关联；按 owner 隔离面投影无归属信号 = 跨用户泄露，故**不做**（前提是先给 flow run 建归属）。
- 无新 migration；不碰巡检/推送/静默时段/日上限。细节与验证见 [work-context-attention-economics.md](./work-context-attention-economics.md) §6.1。

### WD3.17 验收包 + 未读聚焦修复 + 老化/配额/度量（AE3/AE4/AE5，2026-11-07）

按 [work-context-attention-economics.md](./work-context-attention-economics.md) §AE3–AE5 实施。用户实测反馈（「点消息数能联动概览，但概览内容缺聚焦，只能看到」）修出一个真 bug。细节与验证见该文档 §6.2/§6.3。

- **AE3 验收包（纯渲染层，无后端改动）**：委派卡不再只列验收条件文本。关键发现是**渲染时把已有字段丢了**——`WorkDelegationView.Card` 就是完整 `taskboard.Card`，而卡片只渲染 `checklist[].text`，`evidence` / `observed_paths` / `research.open_questions` / `merge_report` / `executions[].summary` 全部未用。新增 `acceptancePackage()` + `AcceptancePanel`，并守一条诚实规则：**不给「勾选但无证据」背书**（`hasEvidence` 仅在已勾选且有证据时为真；无证据时面板明说「没有可核验的证据」）。observed（gate 3）与 declared（gate 1）分开展示且去重。
- **未读聚焦修复（真 bug）**：`openUnreadChanges` 在同一次点击里既切板块又 bump nonce，使 Overview 面板**是被该请求创建出来的**；`FoldCard` 用 `useRef(nonce)` 与挂载值比较 → 判定「已处理」→ 展开与高亮都不触发。此前用例看着通过只因新卡默认展开。修法：待回应请求移到 store（`changesFocusNonce` + `changesFocusAck`），`revealNonce` 改为布尔 `reveal`；并新增一次性瞬时高亮（2s 自动消失，不新增常驻 UI，`prefers-reduced-motion` 保留标记去动画）。
- **AE4 老化降级 + 按主线配额**：未读超 72h 不再当作待办，红点降级为静音数字（计数仍真实、仍可点）；无未读的主线永不被标陈旧。新增 `attentionPerStreamDailyCap = 4`：只有 owner 级上限时，哪条主线到期项最多就吃光全天预算。两道限额仍在**写事件之前**检查。每日 digest 未做。
- **AE5 度量卡**：今日提醒用量**带上限同报**（`5 / 12`）+ 未读总量 + 陈旧主线数，均由收件箱已读数据导出。**默认折叠**即「支持隐藏」的全部实现（不加关闭按钮、不落偏好），不用它的人看到的界面与从前完全一致。

验证（2026-11-07）：

- 后端新增 4 测试（老化标记与「空未读不陈旧」、度量报告上限、按主线配额保留其它主线）；`go build`/`vet` 通过；core/workcontext 与 httpapi 全量通过。
- 前端：`pnpm typecheck` 通过；`vitest run` 49 文件 / 503 通过；i18n 中英对齐通过；`playwright work-desktop` **三端 31 passed / 44 skipped / 0 failed**。
- 揭示标记断言**反向验证过非空洞**（改回 `reveal={false}` 后真失败）。

### WD3.18 收件箱动词：claim / snooze / dismiss（AE2，2026-11-07）

按 [work-context-attention-economics.md](./work-context-attention-economics.md) §6.4 实施（选型依据见该文§7：采用**新表**，而非事件派生）。细节与验证见 §6.4。

- **为什么不是事件派生（已实测）**：`UnreadCounts` 与 `Focus.changes` 都是 kind-agnostic（只按 seen 水位过滤，见 `unread_kinds_test.go`）。把动作写进 `wc_events` 会让用户自己的 dismiss 变成一条「未读变动」被报回给自己。
- **存储**：migration 4 新表 `wc_attention_actions`，主键 `(tenant, context, item_key)`。与 `wc_seen` 同构：记录「所有者与行的关系」，不碰流版本（处置不是编辑）。
- **身份**：组级 `item_key = {kind}:{context_id}`，在收件箱投影的**归一化那一步**统一打标（不在七个构造点各打），保证 UI 处置的就是它渲染的那一行。代价是 dismiss 忽略整组，所以 **dismiss 强制带理由且可撤销**。
- **到期读时判断**：`AttentionActionActive(action, now)` 在读投影时判，未到点则不投影——不需要后台唤醒任务，也就没有「任务没跑所以提醒丢了」这类失败模式。
- **sweep 尊重动词且在写事件之前**：`pushNudge` 新增 `itemKind` 并先查动词 → 纪律检查 → 最后才写事件。顺序是关键：先写事件会让被 snooze 的提醒占住 dedup key、**永久吞掉**（有专门回归测试钉住）。
- **不计未读，但计「决策」**：处置不是阅读，所以徽标不动；但它是人的决策，所以度量卡新增「今日决策」（AE5 收益率的分母）。
- **推迟区默认折叠**：被判定的行不消失，而是收进可见、可撤销的「已推迟 / 已忽略」（显示到期时间或理由）。

验证（2026-11-07）：

- 后端新增 11 测试（存储层 7 + 服务层 4），含 owner 隔离、幂等、到期语义、**snooze 在写事件之前生效**；`go build`/`vet` 干净；core/workcontext 与 httpapi 全量通过。
- 前端：`pnpm typecheck` 通过；`vitest` 49 文件 / 503 通过；i18n 中英对齐通过；`playwright work-desktop` **三端 34 passed / 50 skipped / 0 failed**（新增 3 条动作用例）。
- **测试先于实现发现真缺陷**：store 原本读真实墙钟 `time.Now()`，与收件箱的 `s.now` 两套时钟会分叉；修为 `now` 作参数传入 + `Service.RecordAttentionAction` 注入 `s.now()`。

### WD3.19 动词细化：单条动作 + 自适应老化（2026-11-07）

按用户取舍实施（「能做到单条动作吗」/「老化做成自适应，因为不同项目的推进力度差异很大」）。细节与验证见 [work-context-attention-economics.md](./work-context-attention-economics.md) §6.5。

- **单条动作（组级与条目级共存，更具体者优先）**：新增条目级 key `{kind}:{context_id}:{entity_id}`，与组级 key 共存；条目级 > 组级（点名一条的人说得比处置整行的人多）；读路径与 sweep 走**同一个** `AttentionActionFor`，所以不会出现「收件箱隐藏了却还在推送」。逐条处理默认折叠（多数行整行处置）；被推迟的条目**单独成行**报在推迟区。
- **行不凭空消失**：只有**所有**条目都被推迟行才离开清单，否则带着剩余条目留着并报告剩余条数；只剩一条时不再算组（避免「一个条目的组」）。
- **诚实边界**：`checkpoint_blocker/question` 是检查点里的自由文本，无稳定 id → **不给**条目级动词（不造标题哈希，文本一改就静默脱钩），行内明说「只能整组处理」。
- **自适应老化**：阈值 = 该主线近期事件间隔**中位数 × 3**，夹逼 **[24h, 14d]**；历史不足 3 个间隔回落固定 72h；取中位数（一次长假不拖歪）；**排除系统自己写的 `nudge`/`task_status`**，否则提醒一个安静主线的行为会把该主线显得活跃、**重置掉正是触发提醒的那个陈旧性**。不新增后台任务（读投影时算）。
- **i18n**：老化文案不再写死「3 天」（改自适应后那句话会说谎）；中英同步。

验证（2026-11-07）：

- 后端新增 10 测试（服务层 5 + 存储层 3 + 节奏推算 2），含「系统事件不计入节奏」与「条目级压过组级」；`go build`/`vet` 干净；core/workcontext 与 httpapi 全量通过。
- 前端：`pnpm typecheck` 通过；`vitest` 49/503 通过；i18n 中英对齐通过；`playwright work-desktop` **三端 36 passed / 54 skipped / 0 failed**（新增 2 条）。

### 11.5 后续实现门禁

- 后端：SQLite migrations/reopen/version CAS、同事务事件、租户与来源鉴权、依赖环、幂等补偿、来源删除、事实时间范围；使用实际 modernc SQLite，不只 mock。
- 前端：i18n 全量对齐、视图模型测试、真实 `pnpm build`（tsc -b + Vite）、现有单测。
- UI E2E：桌面切换与 URL、恢复简报、跨桌面共享资源、检查点修改、委派草稿归属、知识来源和审阅、主题笔记、创建桌面、刷新边界、小屏无横向溢出。
- 原型截图仅做需求评审，不把演示任务“运行中”当真实后端观测。

## 12. 待通过 UI 对齐的问题

1. 默认主线颗粒度：服务长期维护、业务接入、模型专项，是否都作为并列桌面？
2. 首页优先看“下一步/阻塞”，还是“资源/对话”？目前默认下一步和恢复上下文。
3. 主线对话默认进入最近主题，还是每次先选择？目前展示主题列表，不强制一个永久主会话。
4. 关系先用列表，是否足够清楚？有实际需求再做图谱。
5. 工作委派是否全局改名？目前只在桌面内称委派，旧任务看板名不动。
6. 个人桌面先行是否满足近期使用？团队共享必须单独定义 ACL，不混同“上下文可关联”。
7. 是否需要桌面壳批量打开本地 IDE/浏览器？不影响先做 Web 工作桌面。
