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

- 入口 `/work-desktop`，选中主线保存在 URL 查询参数，支持分享原型定位和浏览器前进/后退。
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

FTS5 是可重建投影，只索引允许检索的标题/背景/检查点/授权摘要，并携带 tenant_id/context_id 映射。参数绑定、限制长度和 token 数，不允许用户文本直接变成任意 MATCH 表达式。

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

Agent 的 `work_context` 工具（桌面联动会话默认激活）：actions = get/brief/recall/checkpoint/propose_fact/supersede。`propose_fact` 只在有 scope/source/observed_at 时写入候选事实；`supersede` 通过 `supersedes_id` 引用旧事实提出替代（前任只在替代被确认后才作废，不静默覆盖）。工作记忆由 Agent 随工作过程主动维护，而不是靠人逐项填表单：

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

### 11.4 后续实现门禁

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
