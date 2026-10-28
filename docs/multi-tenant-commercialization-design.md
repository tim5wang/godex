# GoDex 商业化多租户设计（SaaS 中继 · 订阅制）

> 状态：Active / Partial（P0–P3 主体已落地：身份底座、节点归属与可见性、中心执行收窄、基础档计量、账号面与成员管理；未完成项集中在 §8 状态表与 §9 开放问题，其中 `nk_` 改 per-node 仍为 ⏳ 未做）
> 日期：2026-10-25
> 关联：[node-mesh-design.md](./node-mesh-design.md)、[node-center-bridge-design.md](./node-center-bridge-design.md)、[node-onboarding.md](./node-onboarding.md)、[remote-sandbox-design.md](./remote-sandbox-design.md)
> 决策输入（用户拍板，2026-10-25）：
> ① 做 **SaaS**（不是客户自托管）；② 服务器**只转发，不存用户数据**；
> ③ 现有节点**手动迁移**，本轮不写迁移脚本；④ 节点可见性**由节点主自己决定**，
> 但**是否允许共享由超管控制**；⑤ 中心执行 agent 的实现方式**由本设计决定**。
> ⑥ **节点默认 `private`**（细化④）；⑦ **平台超管允许代看租户内容**，但需用户协议/授权流程（§3.6）；
> ⑧ 中心的**微信 channel 会话必须保留**（§1.4）——由此修正了「中心不跑 agent」的前提。
> ⑨ **服务器上能跑 agent 的只能是一个用户（超管）**：微信 bot / 定时任务 / Web UI
> 均收窄到超管；**暂不做容器部署隔离**（以后规划）；
> ⑩ 中心的**历史 cron 任务不迁移**（丢弃即可）；**cron 功能保留**，超管仍需定时任务；
> ⑪ 代看**默认走用户主动授权**；
> ⑫ 微信 bot 白名单等超管专属配置，**在超管身份的 settings 界面管理**（不另做运维入口）；
> ⑬ 订阅**暂时只做基础档**（纯转发），进阶档留待以后。

---

## 0. 结论摘要

**三点需求不是并列模块，而是一条依赖链：身份 → 节点互联 → 订阅。**

| 需求 | 定位 | 前置 |
|---|---|---|
| ② 租户系统 + 节点可见性隔离 | **地基**（谁在调用、节点归谁） | 无 |
| ① 只转发节点互联服务器 | ② 的应用（注册表加归属 + 授权闸） | ② |
| ③ 两档订阅 | ② 的变现层（entitlement 检查） | ② |

**根重构不是 forward，而是「身份」。** 但 forward 确实要重构，理由是现在有
**5 处各自实现「谁能到哪个节点」**（见 §5），加租户后不收敛就要写 5 遍 ACL。

**当前代码库是「单信任域 + 全局共享凭据」，离 SaaS 多租户有 4 处本轮阻断项**（§1.1–§1.4）；
另有 1 处（§1.5 容器编排）经决策⑨ 已排到以后，不再是本轮阻断项。

> 决策输入与两条重要修正：
> - 前一版设计写「中心不跑 agent」，**已根据决策⑧ 修正**——微信 channel 必须在中心执行（§1.4）。
> - 据此，租户隔离的闸门是**按主体的 entitlement**，而非按部署形态全局关闭（§3.3）。

---

## 1. 现状实证（全部经代码或真实请求验证）

### 1.1 阻断项 A：身份只有一把共享密钥

| 事实 | 证据 |
|---|---|
| 没有账号体系 | `grep login\|jwt\|bcrypt\|password_hash` → 仅命中 provider 登录，无用户认证 |
| 身份 = 单一 `WebToken` | `internal/core/config/config.go:36`；`httpapi.go:154` `protected := withBearerAuthProvider(...WebToken)` |
| 无租户/用户/归属概念 | `NodeInput`/`NodeView` 字段中**无** Owner/Tenant/Org/Team（逐个确认） |
| 规模 | 非测试路由注册 **284 处 / 41 文件**，其中受鉴权保护 **237 处** |

**后果**：SaaS 下无法区分「谁的请求」。同一把 token 拿到即全权。

### 1.2 阻断项 B：节点可自报身份与信任级，无归属

```go
// routes_control.go:83  POST /control/nodes/register → protected（共享 web token 即可）
// registry.go:139    输入 ID 直接采用，唯一校验是「非空」
// registry.go:358    TrustLevel 也取自请求体（自报）
```

**后果**：任何持有该 token 的调用方可注册任意 `id`（含冒用他人节点名），
并自报 `trust_level`。`mergeLocked`（`registry.go:327`）会覆盖既有节点字段。

### 1.3 阻断项 C：`nk_` 是全局单凭据，跨节点全通（已复现）

`node_proxy_token` 在中心是**全局一个**（`routes_self_join.go:170`：已存在则直接返回现值），
且被 `nodeProxyRead` 接受（`httpapi.go:162-164`）。实测：

```
GET /api/control/nodes                        -> 200  返回全部 6 个节点
GET /api/control/nodes/mac/proxy/meta         -> 200  泄漏 model / workspace_dir
GET /api/control/nodes/win-min/proxy/meta     -> 200  泄漏 model / workspace_dir
```

**后果**：任一已加入的节点持有 `nk_` 即可**枚举所有节点并代理进入任意节点**。
这是多租户隔离的**直接对立面**，必须改为 per-node 凭据 + 目标节点授权。

**进展（如实分开记）**：

| 子项 | 状态 |
|---|---|
| **目标节点授权**（读面遮蔽 + 代理/转发闸门） | ✅ **已做**：`GET /control/nodes` 按 `VisibleNodeIDs` 遮蔽；`GET /control/nodes/{id}` 与 `/{id}/overview` 跨租户 403；`/control/nodes/{id}/proxy/...` 走 `ProxyHandler.AuthorizeNode`；`/control/nodes/{id}/forward`（TCP 转发，同一漏洞的第二个面）走 `ForwardHandler.AuthorizeNode`；`/control/forwards`（托管隧道 CRUD，**第三个面**）走同一 NodeAccess 决策 |
| **`nk_` 改 per-node 凭据** | ⏳ **未做**：仍是全局单凭据。即当前 `nk_` 持有者仍可**探测**任意节点是否存在（拿 403 而非 200），但拿不到数据、也代理不进去 |

> **第三个面（本补充实测发现并修复）**：`/control/forwards` 的凭据闸门（`nodeProxyAuthorize`）
> 接受**任意登录会话**，而 `registerForwardRoutes` 当时不接收 registry/policy，结构性无法判节点归属。
> 结果：租户 A 的登录用户可 `POST /control/forwards` 指定租户 B 的节点，在中心
> `127.0.0.1:<port>` 起监听并桥进 B 节点网络；**创建时没有闸门，授权发生在 `Add` 之后**，
> 所以「响应说不行」时监听已经建好了。已修为：`POST`/`DELETE`/`{id}/check` 都先判目标节点，
> `GET` 按可达性遮蔽（与节点列表同形），并把 `nil` 决策函数改为**拒绝**（503）而非放行——
> 「没配检查」绝不能退化成「所有节点都可达」，那正是这个面最初失去隔离的形状。
> 两个面现由**同一个** `newNodeAccessAuthorizer` 工厂产出，避免再各自漂移。

> 遮罩后 `nk_` 的“枚举”仍会泄漏一个信息：某个 id 是否存在（403 vs 404）。
> 彻底消除需 per-node 凭据（每个节点只能拿自己的 `nk_`），属 P1。

### 1.4 阻断项 D：中心正在跑 agent（收窄到单一账号）

中心实为**中继 + 执行体**：`cron.enabled: true`，`/root/.godex/sessions` 有 **18 个会话**。
按来源实测分布与处置（决策 2026-10-25）：

| 来源 | 数量 | 处置 |
|---|---|---|
| `cron-*` | 7 | ❌ 历史任务**不迁移**（决策⑩，丢弃即可）；**cron 功能保留给超管**（§3.4） |
| `web-*` | 6 | ✅ 保留但**收窄到超管**（决策⑨） |
| `local-*` | 3 | ✅ 保留但**收窄到超管**（决策⑨） |
| `openai_api-*` | 1 | ✅ 保留但**收窄到超管**（决策⑨） |
| **`weixin-*`** | **1** | ✅ **保留**（决策⑧）且**发送者收窄到超管**（决策⑨） |

> **决策⑨ 的实质**：上表四个保留来源全部属于**同一个主体——超管**。
> 所以目标不是「区分平台主体与租户主体」，而是更简单的一条：
> **中心实例只认一个可执行账号**。租户账号一律不可在中心执行。

**微信 channel 确实触发 agent 执行**（完整链路已核实）：

```
weixin/poller.go:170   轮询到入站消息
  → poller.go:303      c.manager.RouteInbound(...)
  → channels.go:1097   AttachSink（挂事件收集器）
  → channels.go:1109   m.backend.Submit(...)   ← 真的跑 agent turn
```

其会话目录含 `turns.json / todos.json / project_ledger.json`
（后者有 `changed_files`、`commands` 字段），即**中心侧确有工具执行**。

**因此「中心不跑 agent」不能作为全局部署开关**（§3.3 已按此修正）：
中心必须为**超管**保留 agent 能力；要挡的是**租户用户**借中心算力执行。

**一个必须收口的安全问题（原状）**：`channels.weixin.allow_from: []` 在
`senderAllowed` 中语义曾为「**放行全部发送者**」（`channel.go`：`len(allow)==0 → true`；
配置说明亦写明 "Empty or * allows all senders"）。即**任何知道该微信 bot 的人都能
触发中心上的 agent 执行**（在中心 workspace 内、带完整工具）。

**已按方案 A 修复（2026-10-28）**：空 `allow_from` 现在语义为
「**只放行扫码绑定该 bot 的那个身份**」，而绑定身份（`account.json` 的 `ilink_user_id`）
是扫码登录时就已记录的，无需手工填写。因此**默认即安全**：

| `allow_from` | 入站闸门 |
|---|---|
| 空（默认） | 只放行**绑定者本人**；无绑定者时**拒绝**（fail closed） |
| `['*']` | 显式放行全部（逃生口，需主动选择） |
| `['id1','id2']` | 只放行这些 id（旧语义保留，显式列表总是生效） |

实现：`Manager.SetInboundOwner`/`InboundOwner`（由 weixin 轮询循环推入绑定身份，
因 `weixin` 已 import `channels`，反向依赖会成环）+ `checkInboundAccess` 的 weixin 分支。
登出/会话过期/无凭据时**显式清空** owner，否则残留身份会继续放行一个已不存在的绑定。
测试：`TestWeixinInboundGateDefaultsToTheBoundOwner`、`...FailsClosedWithoutBoundOwner`、
`...ClosesWhenOwnerCleared`、`...HonoursExplicitAllowList`（`channels_test.go`）；
绑定身份推入侧：`TestChannelPublishesBoundOwnerForInboundGate`（`weixin/channel_test.go`）。

> **ID 空间已实证（真机 2026-10-28）**：两个生产绑定账号的 `ilink_user_id`
> 均为 `o9cq80_...@im.wechat`（同一人，`ilink_bot_id` 则各自不同），与入站
> `from_user_id` 同为 `@im.wechat` 后缀——故绑定者 = 发送者可直接比对。
> 若当初不做这一步实证，方案 A 可能把绑定者自己拒在门外。

旧表述「按决策⑨⑫ 此项无需新代码，只需配置收口」**已过时**（见上），
那条路径现在是可选逃生口，不再是必需手工步骤。

**绑定的闸门（2026-10-28 决策）**：`allow_from` 只管「谁能发消息来驱动 agent」，
**不管「bot 归谁」**；后者由**扫码绑定**决定，而绑定接口原先只挂 `protected`，
即**任何登录租户用户都能扫码把 bot 绑给自己**。已修为
`requirePlatformAdminOrNodeActor`（`httpapi/admin_gate.go`）：

| 调用方 | 结果 |
|---|---|
| 超管（web token） | ✅ 可绑定 |
| 租户用户（登录会话） | ❌ **403** |
| 节点身份（经中心代理的远端节点） | ✅ 放行——`/channels/*` 是节点作用域的，
远端节点激活时请求被代理过去，只卡平台角色会让**远端节点无法绑定**（已用证伪测试钉住） |

测试：`TestWeixinAuthBindingRequiresPlatformAdmin`、
`TestWeixinAuthBindingStillAllowsRelayNodeActor`（`signup_and_channel_gate_test.go`）。

### 1.5 阻断项 E（**已降级为“不属本轮”**）：容器编排底座不存在

> **决策⑨ 已把容器部署隔离排到以后**，故本节**不再是本轮阻断项**，
> 仅作为“将来若要做”的事实底子保留。原判定（进阶档 = 容器跑 agent）已作废（§7）。

| 事实 | 证据 |
|---|---|
| docker 只是 CLI 包装 | `tooling.go:609` `exec.CommandContext(ctx, "docker", "run", "--rm", "-v", ...)` |
| 无 docker SDK / 无编排 | `go.mod` 无 `docker/docker`、`moby`；无容器生命周期管理 |
| 已有执行路由缝 | `ExecutionMode{Local,Docker,SSH,Relay}`（`tooling.go:322`）+ `RemoteSandbox`（`sandbox/remote.go`，5 测试） |

**将来若做**：需要真正的编排服务（创建/回收/配额/隔离/挂载），不能靠现有 CLI 包装。
有利条件：**执行路由的抽象已经存在**（上表末行）。

### 1.6 非阻断但须知情

- **上行观测含会话标题**：`eventstore.go:200/207` 把 `session.Title` 写进事件明细。
  `EventStore` **纯内存**（`disk ops=0`），重启即失——但「仅转发」口径下标题仍属用户内容，需明确取舍（§6）。
- **注册表落盘内容**：`~/.godex/control/nodes.json`（实测 6 节点）字段含
  `credential_hash / trust_level / last_seen / source`；`NodeInput` 还含 `WorkspaceDir/GodexHome`（部署路径）。
- **`scope.Personal/Org` 已定义但几无使用者**：仅 `taskboard/plugin.go:66`、`agent/package_runtime.go:110` 硬编码 `Org("godex")`。**不要**把租户直接映射到 `scope.Org`，两者语义不同（scope 是数据隔离，租户是计费+授权边界）。
- **可复用的 per-principal 先例**：`watchauth.Device`（含 `Revoked`/`CredentialHash`，`watchauth.go:64`）——手表设备就是「按主体签发、可吊销」，是租户凭据的良好模板。

---

## 2. 目标架构

```
┌───────────── 控制面（SaaS 中心）─────────────┐
│  身份/租户   │  节点注册与归属  │  授权(NodeAccess)  │  计量/订阅   │
│  （P0）      │      （P1）      │      （P1）        │   （P3）     │
│                                                │
│  中心侧 agent：**仅限超管一个账号**（决策⑨）      │
│  微信 bot / 定时任务 / Web UI 均归属于它           │
│  租户用户：不得借中心跑 agent                      │
└──────────────────────┬─────────────────────────┘
                       │ 中继（WSS 出站）+ HTTP 代理转发
        ┌───────────────┴───────────────┐
        ▼                               ▼
  租户自有节点（用户电脑/服务器）  （将来）平台置备的容器节点
  - 跑 agent，数据全在本机          - 决策⑨：**本轮不做**
  - 只经中心转发，不落数据          - 仅记录为以后规划方向
```

**关键原则**：对**租户数据**，中心只做四件事——认证、授权、转发、计量；
租户的执行永远发生在「节点」侧（用户自己的机器）。

**但不是「中心无 agent」**（修正前一版假设）：决策⑧ 要求保留微信 channel，
而它确实在中心侧执行 agent（§1.4 实证）。所以约束的对象是**主体**而非部署形态：
**超管**可在中心执行，**租户**不行（§3.3）。

---

## 3. 身份模型（P0）

### 3.1 实体

| 实体 | 说明 |
|---|---|
| `Tenant` | 计费与隔离边界。`id`, `name`, `plan`, `created_at` |
| `User` | 登录主体。`id`, `email`, `password_hash`, `platform_role` |
| `Membership` | `(tenant_id, user_id, role)`。role ∈ `owner\|admin\|member` |
| `Entitlement` | 能力位：`relay_only`（租户套餐）/ `center_execution`（仅超管）；`agent_runtime` 为将来预留，见 §7 |

> `platform_role`（全局）与 `Membership.role`（租户内）**分离**：
> 一个是平台员工，一个是租户成员，混在一起必然出现「A 租户管理员能管 B 租户」的越权。

### 3.2 Principal 传递 ✅ 已落地（`internal/runtime/httpapi/principal.go`）

```
Authorization: Bearer <token>
        ↓  withBearerAuthProvider：先做放/拒判定（不变）
        ↓  withPrincipalAuth：仅对已被放行的请求附加身份
Principal{TenantID, UserID, PlatformRole, Kind, Entitlements, AuthMethod}
        ↓  context
handler / NodeAccess
```

**分层而非改写中间件**：`withBearerAuthProvider(func() string)` 签名未改（改它会影响
若干依赖该行为的测试与回环开发体验），而是在它**外层**包一层 `withPrincipalAuth`，
职责严格分开——内层决定“能不能进”，外层只记录“谁进了”。
因此身份分类**不可能扩大访问**：分类出错只会写错审计记录，
而不会放行一个本应被拒的请求（`TestWithPrincipalAuthDoesNotRunHandlerWhenDenied` 钉住这点）。

分类顺序（有序性有测试保护）：`relay_trust` → `node_proxy_token(nk_)` → web token 默认。
节点凭证**必须**先于操作员默认分类，否则节点会继承操作员的 `center_execution`
（`TestClassifyPrincipalNodeProxyTokenIsNotOperator` / `...RelayTrustWinsOverBearerOrder`）。

**单租户兼容（决策⑨）**：当前 `web token` 持有者 = 平台超管，是唯一带
`center_execution` 的主体；`nk_`/relay 身份一律只给 `relay_only`。
这与现有单操作员行为完全一致；多租户登录上线后，仅需替换默认分支里的
“取配置”为“真实查库”。

### 3.3 中心执行能力的归属（决策⑤⑧⑨ 的落地）

**先修正前一版设计的一个错误假设**：原稿写「中心实例不注册 agent 路由」，
但 §1.4 已实证**微信 bot 必须在中心执行 agent**，
故**不能用全局部署开关关闭中心执行**。

**修正后的方案（按决策⑨ 进一步简化为单账号）**：

| 主体 | 中心执行 agent | 说明 |
|---|---|---|
| **超管账号**（一个） | ✅ 允许 | 微信 bot / 定时任务 / Web UI 执行均归属它 |
| **租户用户**（含租户管理员） | ❌ 拒绝 | 只能经中心**转发**到自己节点 |

> 决策⑨ 比前一版更简单：不是「区分平台主体 vs 租户主体」，而是
> **中心实例只认一个可执行账号**。实现上就是给超管账号标记
> `center_execution`，其余所有主体一律无此能力。

检查点唯一：`Principal.Entitlements` 是否含 `center_execution`。
无此能力的主体在中心任何执行入口（`POST /sessions`、`POST /sessions/{id}/messages`、
`POST /v1/chat/completions`、`/v1/responses`、`/v1/messages`、`/v1/agent-steps`）一律 403。

**落地形态（`internal/runtime/httpapi/center_execution.go`）**：

```go
requireCenterExecution(protected)(handler)   // 鉴权 → 能力位 → handler
```

- **包装顺序有实质意义**：它包在 `protected` **外面**，因为闸门要读鉴权已附加的
  Principal（反过来的话读不到）。未带身份时**失败关闭**（401），而非默认当操作员
  ——一个忘记挂鉴权的路由必须是不可用，不是全开。
- **已接线的执行入口（18 处）**：`POST /sessions`、`POST /sessions/{id}/messages`、
  `POST /sessions/{id}/turns/{turnID}/retry|resume`、`POST /v1/exec`、
  `POST /sessions/{id}/subagents/{jobID}/resume`、`POST /sessions/{id}/longtasks`、
  `POST /sessions/{id}/longtasks/{workflowID}/run|finalize|rollback`、
  `POST /v1/flows/{id}/sessions`、`.../sessions/{sessionID}/resume`、`POST /v1/flows/{id}/runs`、
  `POST /v1/flow-runs/{runID}/step`、`POST /v1/flow-runs/{runID}/human/{nodeID}/reply`、
  `POST /v1/flows/{id}/sessions/{sessionID}/events|signals`。
  另含 `POST /v1/chat/completions` 的 **web token 分支**：它不是纯 LLM 网关，
  而是**起 agent 会话**（该端点的 `gdx_` 网关分支不闸，因为它只按 key 额度纯转发）。
  选择判据：**会启动/推进一次 agent 执行**的写路由；只读查询、取消、审批回复、
  配置写入（如 skills install/load、flow publish/validate、diagnose）不在内。
  `events|signals` 算在内是因为它们经 `scheduler.wake(...)` **确实会唤醒等待中的
  flow 会话**（已读实现确认）；不接则可被非操作者驱动中心上已在跑的流程。
  余下仍走 `protected` 的待后续接线；清单 §6 已同步逐文件计数。
- **节点行动者豁免**（否则会打断已有能力）：relay-originated 请求以 `KindNode` 到达，
  无 `center_execution`；若闸门只认能力位，「从中心驱动远程节点执行」这条文档化能力
  会被 403 打死。故 `canExecuteOnCenter = IsNodeActor() || CanExecuteOnCenter()`。
  这是**豁免而非授权**：节点行动者仍不持有 `center_execution`，故在
  `AuthorizeCenterExecution` 等其他地方仍被拒（两侧各有测试钉住）。

**为什么不用「特殊租户」**：那会导致授权判断里到处出现
`if tenant == "system"` 特例，是越权的温床。能力位是正交的，检查点只有一处。

**微信入口必须同步收口**（否则「只有一个用户能跑」在微信上是假的）：
`channels.weixin.allow_from` 的默认语义已改为「**只放行绑定者**」（见 §1.4 方案 A），
不再需要手工填入超管微信 ID。

**决策⑫：该白名单在「超管身份的 settings 界面」管理**，不另做运维入口。
现状与缺口（已核实）：

| 事实 | 证据 |
|---|---|
| 字段已是声明式 schema | `channel.go:42`：`Type: "string_list"`、`LiveApply: true`、`Env: WEIXIN_ALLOW_FROM` |
| settings UI 已能渲染 | `SettingsConfigFields.tsx:618` 已处理 `string_list`（tags Select） |
| ⚠️ **但无角色闸门** | `PUT /config` 仅受 `protected`（WebToken）保护（`routes_config.go:19`），任何持 token 者均可改 |

**所以决策⑫ 的前置是 P0**：先把 `protected` 改为解析 `Principal`（✅ 已做，§3.2），
并给超管专属配置项（含 `allow_from`）加**平台角色级闸门**（✅ 已做，下节），
否则「超管身份管理」只是一句口号。

#### ⚠️ 实现前必须知道的坑：闸门要按**配置项**加，不能按**路由**加

`/config` 在 Web UI 里是**节点作用域**的：`apiClient.ts` 的 `nodeProxyPath`
只把 `/meta`、`/control..`、`/push..`、`/relay` 留在中心，**其余全部**（含 settings）
转发到当前选中的远程节点。所以「选中了远程节点」时，Settings 页的
`GET /config`、`PUT /config` 实际打的是 `/control/nodes/{id}/proxy/config`。

后果：若把 **`PUT /config` 整个路由**用 `requirePlatformAdmin` 包起来，
远程节点上的编辑会全部 403——因为请求经 relay 到达目标节点时身份是
`KindNode`（relay-trust），`IsPlatformAdmin()` 为假。这与 `center_execution`
那次需要节点行动者豁免是**同一类陷阱**。

正确的形状（✅ 已实现，`config_admin_gate.go`）：

- 闸门加在**配置项粒度**：只对超管专属项（`web.token`、
  `channels.weixin.allow_from`）要求平台角色；其余节点本地设置
  （model、workspace 等）保持可改。
- 清单**从 schema 派生**，不硬编码：`FieldSchema` 新增 `PlatformAdmin bool`，
  标记与字段定义同处一处，因此新加的限制项不可能被闸门漏掉，
  且渲染同一 schema 的 UI 可据此禁用控件。
  （我们遇到的一个真实陷阱：`channels.weixin.allow_from` 在 base schema
  与 weixin 包的 `ConfigSchema()` 里**各声明一份**，两处都已标记。）
- `clear_secrets` **同样检查**：清掉一个受限 secret 与写入它同样是一次变更，
  否则可以不清就绕开闸门（如直接抹掉 `web.token`）。
- 路径比较先归一化（大小写/空白），避免用 `" Web.Token "` 绕过。
- 无 Principal → **401**（而非 403）：把一个未认证的调用者报成 "forbidden"
  会把客户端引向用同样缺失的凭据重试。

**节点行动者在此处放行**（与 `center_execution` 的豁免同一道理）：
节点被中心驱动时写的是**它自己的**配置，就是远程 Settings 页要做的事。
多租户下的限制靠 delegation 收紧（尚未做）；单租户下节点行动者即操作员。

### 3.4 历史 cron 任务不迁移；但 cron 功能保留（决策⑩）

**先分清两件事**（这是前一版设计的错误之处）：

| | 处置 |
|---|---|
| **cron 功能**（`runtime/cron` + `CronSection`） | ✅ **保留**。运行时内建能力，与中心角色解耦；**超管仍需要定时任务** |
| **中心现有的 4 个历史任务** | ❌ **不迁移**（丢弃即可）。均为已过期的一次性计划，且从未成功投递 |

> **修正记录**：前一版设计误把「历史任务不迁移」写成「cron 功能全部删除 /
> 关闭 `cron.enabled` / 将来建在超管自己的节点上」——这既砍掉了超管需要的功能，
> 也把理由写反了：**超管的定时任务恰恰可以留在中心**，因为中心就是超管执行
> agent 的地方（§3.3），cron 与微信 bot / Web UI 同属该账号的执行能力。

**历史任务核实（真机取证，2026-10-25）**：

| 任务名 | 调度 | enabled | 投递 |
|---|---|---|---|
| 体检提醒 | `at 2026-08-28T14:00` | **false** | — |
| 周六面试知识点复习 | `at 2026-08-22T10:00` | **false** | — |
| 美团Q2财报摘要 | `at 2026-08-28T20:00` | **false** | — |
| 周日面试知识点复习 | `at 2026-08-23T10:00` | **false** | — |

四个任务**全部 `enabled: false`**，且都是一次性 `at` 计划（已过期一个多月），
即实际已无任何周期任务在跑。历史 `runs/` 显示它们曾通过微信投递，且全部为
`status: error`（`weixin sendmessage failed: ret=-2 ... prepare failed`）——
**本就没成功过**，故不存在“不迁移会断掉某条对外推送”的风险。

> 数字口径：会话数 **7**，任务定义 **4** 个，run 记录 **4** 条。实测对应关系：
> 4 条 run 记录引用的会话与 4 个任务一一对应
> （`60d4a15209ae22f1→b5e56f32f8cf1e3f`、`658f54ea1c1ccd69→ebcd9bf15b5e11e3`、
> `a308df6b062d3827→1314d9060ffb4c6c`、`cfd020e737330b4d→98c1eaa45e55afd3`）；
> 另有 **3 个会话无任何 run 记录引用**（`cron-3b6cf066e6bb2626`、
> `cron-a127e19bb1053931`、`cron-d030bf3015681f27`），**成因未确定**，
> 本设计不对其做推测。它们同属 `cron-*` 遗留，清理时一并删除。

> 处置：只清理这 4 个任务及其 `cron-*` 遗留会话与 `state/cron/jobs|runs` 记录。
> **保留 `cron.enabled` 与整个 cron 能力**：超管可继续在中心创建定时任务
> （与微信 bot、Web UI 同属超管的执行能力，§3.3）。

### 3.5 自助注册（SaaS 必需）

现状自助加入需要**中心的完整 web token**（`routes_self_join.go:24-25`：`req.Token` 用 full web token）。
SaaS 下这等于把主密钥给了每个用户，**必须替换**：改为**租户内一次性 join token**，
注册时由中心解析出 `tenant_id` 并写入节点归属（§4）。

**自助注册默认关闭（2026-10-28 决策）**：`POST /auth/register` 会**新建一个租户**，
所以开着它 = 任何能访问该实例的人都能自建租户。现由 `web.allow_self_signup`
（默认 `false`，env `GODEX_WEB_ALLOW_SELF_SIGNUP`）控制：

| 场景 | 行为 |
|---|---|
| 开关关闭（默认） | 匿名调用 **403**；**持 web token 的超管仍可创建租户**（能力不丢，只去掉匿名敞口） |
| 开关打开 | 匿名可注册（SaaS 形态），行为与收紧前一致 |

`/meta` 返回 `allow_self_signup`，登录页据此隐藏会 403 的「注册」页签。
拒绝发生在解析请求体之前，避免借错误差异探测邮箱是否已存在。
测试：`TestSelfSignupRefusedByDefaultWithoutOperatorCredential`、
`TestSelfSignupOpenWhenOperatorOptsIn`（`signup_and_channel_gate_test.go`）。

**加入既有租户仍走 `/auth/accept-invite`**（一次性邀请码，未改动），所以关闭自助注册
不影响「第二个人加入同一个租户」。

### 3.6 运营代看（决策⑪：默认用户主动授权）✅ 已落地

超管**可以**代看租户内容排障，但必须是**显式、有时限、可审计**的，不能是静默旁路。

| 要素 | 设计 | 实现 |
|---|---|---|
| **默认模式** | **租户主动授权**（决策⑪，已拍板）：租户管理员在控制台授权并指定时长 | ✅ `POST /tenant/delegations` |
| **备用模式** | 紧急模式（双人复核 + 事后通知租户）——保留但非默认 | ⬜ 未做（本项只做默认模式） |
| **用户协议** | 注册/订阅时明确告知并留痕（`consent_record`：租户 id、条款版本、时间、来源 IP） | ✅ 注册即写 `consent_records` + `GET/POST /tenant/consent` |
| **时限** | 代看会话自动过期（如 2 小时），到期即失效；不提供「无痕续期」 | ✅ 默认 2h，上限 24h，`Active()` 统一判过期+撤回 |
| **可见性** | 代看期间租户侧**显式提示**（横幅 / 审计条目），不做隐蔽访问 | ✅ app 级横幅（有生效授权就显示，60s 刷新）+ 审计条目 `GET /tenant/delegations/audit` |
| **审计** | 每次代看写入审计记录：谁、看哪个租户、何时、时长、范围 | ✅ `delegation_audit`，在**授权缝**记录（非各 handler 各写一遍） |
| **最小化** | 默认只读，且限排障所需的最小范围（不默认给全量导出） | ✅ 唯一 scope `read_nodes`；未知 scope 一律收敛为该值 |

**落地形状**（`internal/core/identity/delegation.go` + `internal/services/accounts/delegation.go`
+ `internal/runtime/httpapi/routes_delegation.go`）：

- **租户侧**：`POST/GET /tenant/delegations`、`DELETE /tenant/delegations/{id}`、
  `GET /tenant/delegations/audit`、`GET/POST /tenant/consent`
- **操作员侧**：`GET /delegations/mine`（只列**发给自己的**授权，不做「谁在看谁」的地图）
- **授权缝**：`identity.Policy.DelegatedRead` 钩子 + `NodeAccessPolicy` 装配；
  跨租户分支处的例外，位置即语义（读代码能看出「授权是规则的例外」）
- **归属不可自选**：租户 id 取自 principal；grant 必须指名 `operator_user_id`
  （否则就是「任意超管随时可看」）；撤回时租户 id 取自记录（不看调用方声明）
- **失败方向**：未接 delegation 系统 ⇒ 超管**无**跨租户读权（`DelegatedRead == nil` 不放行）

**审计必须接在缝上，不是接在各 handler 上**：

E2E 实证过这一点——最初只把审计接在 httpapi 的读路由（`filterVisibleNodes` /
`authorizeNodeAccess`），真实二进制跑出：经**代理**的代看读取（Web UI 实际走的面）
成功读了却**零审计行**。已补 `httpapi.RecordDelegatedRead` 并接到
`cmd/godex` 的 `proxy.AuthorizeNode`。教训：审计点必须与授权点是**同一处**，
否则「审计覆盖度取决于操作员走了哪个面」。

**只记代看，不记租户自读**：判据是 decision 的 `DelegationID`。租户成员读自己租户的节点
不是「代看」，记了反而会把租户真正要看的那些行埋掉（已由测试钉住）。

**复用既有范式**：`channels.Manager` 的 `AccessDecision`
（`channels.go:183`：`Action / Reason / DecidedAt` + `auditAccessDecision` 落审计）
就是「有决定、有理由、有审计」的现成形状，代看已对齐它。

**测试**：identity 3 个（授权缝例外/非管理员不可借道/scope 收敛）、
accounts 8 个（需租户管理员、窗口上限、指名操作员、过期+撤回即时生效、
审计租户隔离与倒序、同意留痕、角色边界）、httpapi 9 个（授权/撤回往返、
窗口与操作者校验、操作员自读隔离、操作员不可自助授权、未认证 401、
注册即留痕、显式同意、**审计端到端 + 撤回后不再新增**、租户自读不入审计）、
真实二进制 E2E **36/36**（含重启后授权/审计/同意仍在）。

**前端**（`ui/web`）：

| 界面 | 位置 | 作用 |
|---|---|---|
| 租户代看面 | 设置 → 「代看与协议」tab（`?tab=access` 可深链） | 授权、列表、撤回、审计表、同意留痕 |
| 代看横幅 | app 级，有生效授权时出现 | 租户不用去翻审计就知道正在被看 |
| 操作员自读面 | 设置 → 「租户管理」tab | 只列**发给自己的**授权，不做「谁在看谁」的地图 |
| 节点可见性 | 节点详情页（拥有者可见控件，他人看到说明） | 节点主体自己控制暴露范围 |

真实浏览器渲染校验 **6/6**（Chrome，跑在构建产物上）：横幅显示操作者与到期时间；
租户面渲染授权/审计/同意三张表；超管面把「总额≠行和」判为不一致并标注无租户行；
用量页转发计量 tab 渲染出计数；可见性控件渲染两档且**切换会发出 POST**。

> 前端开发中值得记下的一点：第一次跑渲染校验时 13/13 全挂，堆栈看起来像页面崩溃，
> 实际上是**我的临时 mock 形状不忠实**（`/config/schema` 要数组却返回了 `{}`）。
> 改用仓库既有的 `mockBackend` 并只叠加新增端点后即正常。
> 教训：夹具与组件同时“不对”时，先证实自己的夹具是否正确。

> 注意：这条与「只转发不存数据」（决策②）不矛盾——代看的是**租户节点上**的数据，
> 中心只是持有访问通道；但通道本身必须受上述约束，否则「允许代看」会退化成「随时可看」。
> **不能靠超管的 `center_execution` 能力代替授权**：那是执行能力，不是跨租户读权。

### 3.7 账号面与租户成员管理 ✅ 已落地

§3.1 建了账号/租户/成员实体，但**用户没有面去用它**：改了密码无处可改、登过的设备
看不了、第二个成员无法加入。本节登记该面的形状（后端 `routes_account.go`）。

**三条边界（决定了路由怎么分）**：

| 面 | 路由族 | 作用域 | 关键约束 |
|---|---|---|---|
| **用户中心** | `GET /account`、`/account/sessions`、`DELETE /account/sessions/{id}`、`POST /account/sessions/revoke-others`、`GET /account/login-events`、`POST /account/password` | **只能看/改自己** | user id 取自鉴权 principal，**不从请求读**——没有可篡改的 id，也就没有“查别人账号”这条路 |
| **成员管理（租户侧）** | `GET /tenant/members`、`PUT /tenant/members/{userID}/role`、`DELETE /tenant/members/{userID}`、`POST/GET /tenant/invites`、`DELETE /tenant/invites/{id}` | 本租户 | 租户 id 取自 principal；成员操作需 owner/admin |
| **成员管理（超管侧）** | `/admin/tenants/{id}/members`、`/admin/tenants/{id}/invites` | 任意租户 | `requirePlatformAdmin`；**同一套 service 方法，只换 actor**，所以「owner 不可移除」等规则在一处定义，不会在两个面上漂移 |

**四条容易做错的规则（均有测试铉住）**：

- **改密码必须校验当前密码**：仅凭一个已登录会话就能改密码，等于会话被盗即账号被夺。改完**撤销其它会话**。
- **邀请码只能加入既有租户**：没有这条，第二个成员永远无法加入——自助注册**总是新建租户**（§3.5）。
  码是**一次性**且服务端**只存哈希**（明文仅在创建响应里出现一次）。
- **owner 不可移除/降级**：否则租户会变成无人可管，且节点归属以 owner 为兼底（§4）。
- **共享 web token 不是账号**：它没有 user 行，所以 `/account/sessions` 这类面返回 400 而非空列表——
  空列表会被读成「没有设备在登录」，与事实相反。前端据此改为**说明原因**而非渲染空表。

**另一个必须分开记的事实**：中继身份账号（租户计划 `relay_only`）**不能读写本机工作区**。
`/sessions` 等入口走 `requireCenterExecution`（§3.3），对租户账号一律 403。
当时先修正了这个闸门（`59065da`：租户不再能读/改/删超管的会话），但**前端仍只给一个永远会
失败的重试**——403 是稳定事实而非抖动，现已在会话栏改为说明原因 + 两条出路（切到我的节点 /
用共享令牌登录）。同一条事实在**移动端**也补了（`a318984`：watcher 不再静默吞掉 403）。

> ⚠️ **文档历史修正**：代码注释与提交信息曾把本面记作「设计 §3.6」。§3.6 实为**运营代看**，
> 与本面无关；本面已在本文档立为 §3.7，代码注释同步更正。

---

## 4. 节点归属与可见性（P1）

### 4.1 注册表字段扩展 ✅ 已落地（`78cb3fc` 身份底座 + 本增量）

原设计把字段加在 `NodeInput` 上、再「解码后忽略」。**实现时改成加在 `NodeView` 上，
`NodeInput` 完全不设这三个字段**——因为 `POST /control/nodes/register` 与
`/heartbeat` 是直接把请求体解码进 `NodeInput` 的：字段只要存在于该 DTO，
就多一条「忘了忽略」的路；字段不存在，自报在**结构上不可能**，且可被测试钉住。

```go
// NodeView（持久化 + 出参）
type NodeView struct {
    // ... 既有字段 ...
    TenantID    string `json:"tenant_id,omitempty"`
    OwnerUserID string `json:"owner_user_id,omitempty"`
    Visibility  string `json:"visibility,omitempty"` // "private"(默认) | "tenant"
}

// NodeInput（请求体 DTO）：刻意不含以上三字段。
```

**唯一可信写入面**：`Registry.SetOwnership(ctx, id, tenantID, ownerUserID, visibility)`。
只有它写 `TenantID`/`OwnerUserID`，供 §4.4 的 admin claim 操作使用；
`mergeLocked`（register/heartbeat 路径）一律不碰这三个字段。

`Visibility` 的归一化委托给 `identity.NormalizeVisibility`，
保证注册表与授权策略不会漂移（否则可出现「存成 tenant 可见、策略读成 private」
或反过来的错配）；读路径统一补默认值，故历史 `nodes.json` 记录加载后不会是空值。

**这直接修掉 §1.2 的「可自报身份」问题**，并由
`TestNodeInputDoesNotAcceptOwnership` / `TestRegistryRegisterCannotOverwriteOwnership`
两个测试钉住（前者断言字段不在 DTO 上，后者断言已 claim 的节点不被
后续 register 冲掉）。

### 4.2 可见性与共享开关（决策④⑥）

| 控制项 | 谁决定 | 语义 |
|---|---|---|
| `visibility` | **节点主** | **`private`（默认，已拍板⑥）**：仅本人可见；`tenant`：租户内可见 |
| `sharing_enabled` | **超管**（租户级或平台级） | 关闭后，本租户**不允许**把节点设为 `tenant` 可见 |

> **默认 `private` 已由用户拍板（决策⑥）**：要共享必须显式切换，且需超管未关闭共享策略。
> 代价：新建节点不会自动出现在同租户成员列表里，需引导用户主动开启（UI 上要说明白）。

### 4.3 节点访问决策（授权唯一入口）

```
allow(principal, targetNodeID) ⟺
      targetNode.tenant_id == principal.tenant_id
   AND ( visibility == "tenant"
         OR targetNode.owner_user_id == principal.user_id )
   AND 超管共享策略未封禁
   AND (写操作时) 目标节点信任级允许
```

### 4.4 现有节点迁移（决策③：手动）✅ 已落地

不写脚本。提供一个**管理操作**把存量节点指认到租户：

```
POST /admin/nodes/{id}/claim   {tenant_id, owner_user_id, visibility?}
```

实测存量 6 个节点（`node_8ae51969bb5cc3bb` / `mac` / `win-min` /
`node-153f6098` / `node-podman-test` / `win-pc`）由你按需指认。

**实现要点（几个刻意的选择）**：

- **闸门用 `requirePlatformAdmin`，不用 `requireCenterExecution`**：后者对
  节点行动者有豁免（为了保留中心驱动远程执行），复用它会让已加入的节点
  能认领（即改属）其他节点。改归属是管理动作，因此**无任何豁免**。
- **`tenant_id` 与 `owner_user_id` 必须同时给**：只给一个会造出无人可及的
  节点（策略首先比租户），故在入口就 400 拒绝，而不是落盘一条无法解读的归属。
- **通过可选能力（`nodeOwnerSetter`）接入**，而非扩 `controlNodeRegistry` 接口：
  后者会打断每个实现者（包括测试 fake），而这是只有真实 registry 才提供的能力；
  不支持时答 501 而不是 panic。

**为何这个路由是必需的而不是锦上添花**：`SetOwnership` 是 `TenantID`/
`OwnerUserID` 的**唯一**写入面（节点不能自报，§4.1），所以没有这个路由，
归属字段就永远是死的——`visibility` 也无法从默认 `private` 改开。

#### 实测行为（真实二进制，务必知晓）

claim 到租户后，**超管自己也看不到该节点了**（E2E 实测 `still_listed=False`）。
这与决策③「代看默认走用户主动授权」一致——租户数据不因超管身份自动可见。
两个后果需注意：

1. **迁移顺序**：把节点 claim 给某租户后超管就管不到它了；误 claim 需该租户
   配合，或直接改 `nodes.json`。建议 claim 放在最后、且逐个确认。
2. **「超管代看」目前未实现**（delegation 属 P1）。在此之前平台侧无法查看
   已 claim 节点的状态。

---

## 5. NodeAccess seam：真正要做的重构

现在有 **5 处**各自实现「解析目标节点 → 判在线 → 判权限 → 路由」：

| # | 机制 | 位置 |
|---|---|---|
| 1 | HTTP 代理（本地 or 经中心） | `httpapi/centerbridge.go:303` `NewBridgeProxyHandler` |
| 2 | 手表转发 | `routes_watch_proxy.go`（上轮 `WatchRelayHolder` 就是**手工复用第 1 个**） |
| 3 | WS TCP 隧道 | `relay/forward.go` `ForwardHandler` |
| 4 | 环回端口隧道 | `relay/forward_server.go`（绑 `127.0.0.1`） |
| 5 | 沙箱执行调用 | `routes_sandbox.go`（`withForwardToNode`） |

**收敛为一个 seam**：

```go
// NodeAccess 是所有「到某个节点」请求的唯一授权入口。
// 租户 ACL、手表、代理、沙箱全部经它，避免 ACL 写 5 遍。
type NodeAccess interface {
    Resolve(ctx context.Context, p Principal, nodeID string) (Decision, error)
}
type Decision struct {
    Allowed   bool
    ServedBy  string // 目标节点 id
    ViaCenter bool   // 本地直连 or 经中心
    Reason    string // 拒绝理由（面向用户可读）
}
```

收益：加租户只在**一处**改；上轮「选了 mac 显示中心列表」那类
「重复实现导致的不一致」也从结构上消除。

**为什么不是「先重写 forward」**：`relay/forward.go` 承担的是 WS 字节流隧道，
是 5 个消费点之一；它是结果而非根因。先做 seam，forward 改为消费它。

---

## 6. 「只转发不存用户数据」的边界（决策②）

必须逐一确认下列面**不含用户内容**：

| 面 | 现状 | 处置 |
|---|---|---|
| 会话历史/消息 | ✅ 从不落中心（`eventstore.go:11-13` 注释明确「never persists session history」） | 保持 |
| `EventStore` 事件日志 | ⚠️ **纯内存**但含 `session.Title`（`:200/:207`） | 见下 |
| 节点注册表 | ⚠️ 含 `WorkspaceDir/GodexHome`（部署路径） | 视为**注册元数据**，租户作用域内保存 |
| watch push 队列 | ⚠️ 有界内存队列（200 条 / 24h）带事件载荷 | 需审载荷是否含标题/正文 |
| usage 计量 | ✅ 仅 token 计数（无内容） | 保持 |
| **超管会话**（微信 bot / Web UI / 网关） | ⚠️ **中心侧确实落盘**（`weixin-*`：含 `turns.json`/`todos.json`/`project_ledger.json`） | **非租户数据**（超管自己的主体），但必须与租户数据**物理分离或显式标记**，避免日后混淆 |

> **关键区分**：「只转发不存用户数据」约束的是**租户数据**。超管在中心产生的会话
> 属**平台自己的数据**，不在该约束内（决策⑨ 已把中心执行收窄到超管一个账号，见 §3.3）。

**已落地**：观测投影默认**不含标题**，开关为 `relay.observation.include_titles`（默认 false）。
理由：需求②是「不存用户数据」，会话标题属用户内容；关闭后中心只看到
`session id + running 状态`，足够支撑 Tile/表盘。

剥离发生在**中心侧的摄入点**（`relay.EventStore.RecordSnapshot`，随 `StoreEvents` 帧入口），
而不是节点侧上行：这样「中心不留用户内容」由中心自己强制，节点即使继续上报标题也进不来，
也无需升级任何已加入节点即可让默认值生效。派生事件日志（`diffEvents`）同样改用 session id 命名，
否则标题会从事件明细这条旁路泄到 UI。

代价与处置（见 R4）：中心侧列表不再有标题。`Nodes` 节点详情的标题列加了提示说明
「中心默认不保存会话标题」，「从目标节点实时取」**未做**——节点的 `GET /sessions` 挂在
`requireCenterExecution` 下（它用于中心驱动远程执行），要实时取标题需另开一条只读代理面，
超出本轮范围。

---

## 7. 订阅与 entitlement（P3）

**决策⑬：本轮只做基础档**（纯转发）。进阶档保留设计、不实现。

两个 entitlement **不是同一件事**，别混用：

| entitlement | 含义 | 给谁 |
|---|---|---|
| `center_execution` | 允许在**中心实例**上跑 agent | **仅超管一个账号**（决策⑨）。**租户永不得**（§3.3） |
| `agent_runtime` | 平台为其**置备容器节点** | ❌ **暂不做**（决策⑨：容器部署隔离以后再规划） |

| 档位 | 本轮 | entitlement | 能力 | 计量 |
|---|---|---|---|---|
| **基本档** | ✅ **做** | `relay_only` | 仅经服务器转发/代理到**自己的节点**；节点数上限（§7.2） | 转发字节/请求数、在线节点数 |
| **进阶档** | ❌ 以后 | （待定） | 待定（曾设想容器跑 agent，已作废；见下） | 待定 |

### 7.1 基础档计量 ✅ 已落地（P3 验收项「计量可对账」）

`internal/services/metering` + `relay.ProxyHandler.UsageRecorder` + 对账两路由：

| 面 | 路径 | 读什么 |
|---|---|---|
| 租户自读 | `GET /usage/relay` | **只能**读自己租户的转发量（租户取自 principal，`?tenant_id=` 被忽略） |
| 超管对账 | `GET /admin/usage/relay` | 跨租户逐行 + **总额** |

关键设计选择（都由测试钉住）：

- **按行存、读时聚合**：换窗口/分组不需迁移与回填；大窗口不必把明细拉进内存
- **只记元数据不记内容**：中心转发租户数据而不留存（决策②），计量表不能变成数据堆积处
- **三条路径都计**：远程转发、**本地直连**（中心即自身节点）、**转发失败**。
  本地直连漏计会让「租户用量」取决于它的节点挂在何处；失败也计，因为
  「租户反复打一个离线节点」正是对账要暴露的模式
- **拒绝的不计**：NodeAccess 403 从未到达任何节点，计费等于为被中心拒掉的流量收钱
- **归属取自 decision 而非请求头**：调用方不能选择用量记到谁头上
- **总额必须等于各行之和**：不等即说明有行没归到任何租户（超管自身直连），
  正是这个面要暴露的缺口
- 窗口非法返回 **400 而非静默换期**（否则数字会**错**而非缺失）；无计量库时回 **503**
  而不是报 0 用量

真实二进制 E2E **18/18**（转发计量、归属、累加、对账行=自读、总额=行和、
403/401/400 边界、重启后持久化）。

> 本项顺带修了一个真问题：`main` 因新增的 nil 检查被复杂度门禁拦下（score 41 / limit 40），
> 已抽成 `wireMetering` 辅助函数。

### 7.2 节点数上限（配额强制）✅ 已落地

节点配额是**租户策略**，不是租户身份，所以存在既有的 `tenant_policies` 表里，
复用超管面（决定④的表，不是给 `tenants` 加列）。

| 面 | 路径 | 说明 |
|---|---|---|
| 超管设置 | `PUT /admin/tenants/{id}/max-nodes` | `max_nodes` 必传；**0 = 不限** |
| 超管读取 | `GET /admin/tenants` | 每行带 `max_nodes` + `node_count`（该租户现有节点数） |
| 租户加入 | `POST /control/join` | 超限返回 **409**（请求合法、调用方也有权，只是额度用尽） |

关键设计选择（都由测试钉住）：

- **0 = 不限，且缺策略行也 = 不限**：与 `sharing_enabled` 同一种「缺省即宽松」编码。
  反过来（缺省=0 个节点）会让**升级后所有存量租户**立刻无法再加节点，这是本项
  最危险的一个默认值，因此专门有一条迁移回归测试（手工造出没有 `max_nodes`
  列的旧表 → 走 `init()` → 断言迁移后仍是 0/不限，且既有 `sharing_enabled` 不丢）。
- **检查与写入在同一把锁内**：两个并发加入如果各自读到「5 满 / 共 5」，都会成功并
  落到 6，这正是一个配额不能允许的。
- **注册与归属合并为一次原子写入**（`RegisterOwned`）：先注册后归属的话，一旦归属
  被拒就会**留下一个已注册但无归属的幽灵节点**——而 id 已被占用，同一台设备再也
  重试不了（会收到「已注册」）。这条是**我自己的测试抓出来的真 bug**，不是设想：
  第一版就是两次调用，`TestJoinRouteRefusesEnrolmentPastTenantLimit` 直接报
  「a refused enrolment left a registered node behind」+ 第二次加入 409 且理由错误。
- **只有「新增占用」才被拒**：重新保存一个已属本租户的节点不占额度（否则满额租户的
  拥有者连改自己节点的可见性都会被拒）；调低上限**不驱逐**已有节点，只拒绝新的加入，
  所以改策略不会静默让正在跑的机器失联。
- **未归属节点不算任何租户的占用**：否则没人认领的机器会平白吃掉额度。
- **超管修复路径不受额度限制**（`/admin/nodes/{id}/claim` 走不限额的 setter）：
  操作员修一个错归属的节点时，不能被他可能正在修的额度挡住。
- **随时可加**：`max-nodes` 与 `sharing` 是两个端点、只写自己那一列，所以设额度
  不会静默重置共享开关（两个方向都有测试）。

测试：registry 7 个（超限拒/0 与负数=不限/调低不驱逐/重存已属节点不占额/
未归属不占额/普通 setter 不受限/只统计目标租户）、accounts 6 个（默认不限、
往返、不互踩 sharing、缺租户 id 拒绝、列表带 max_nodes、**迁移回归**）、
httpapi 8 个（加入超限 409 且不留幽灵、默认不限、超管修复不受限、
`max_nodes` 必须显式传、负数 400、持久化+列表、不互踩 sharing、需平台管理员）。
全量 `go test ./internal/runtime/httpapi/ ./internal/services/noderegistry/ ./internal/services/accounts/` 通过。

**前端**：「租户管理」tab 租户表新增一列 —— 直接显示 `已用/上限`，可就地编辑保存
（`TenantNodeLimitCell`）。

- **就地单元格而非弹窗**：操作员的任务是「扫一遍所有租户、改几个」，
  每行一个弹窗会把两秒的事变成一串对话框，而「已用/上限」只有在其他租户旁边才有意义。
- **草稿态是本地状态**：输入到一半的数字不会写服务端；清空后反悔点取消即回滚到服务器值
  （空框不能读作「0 = 不限」）。
- **超限显示为橙色而非错误**：调低上限不驱逐已有节点，所以「6 / 5」是策略收紧后的
  合法界面，报错会暗示必须立刻处理，而选择权在操作员。

**前端**：用量页「转发计量」tab（租户自读）+ 「租户管理」tab 的对账卡片
（逐行 + 总额 + **对账结论**，不一致时给出两侧数字）。对账判定抽成
`reconciliation.ts` 并单测（含「无总额不判为一致」「缺计数字段不产生 NaN」
两个失败方向），因为「总额≠行和」正是这个面存在的理由。

> **两档都不得含 `center_execution`**：那是平台自用能力位（仅超管），
> 租户套餐不得与之混用（上文已列）。

**决策⑬ 的后果（须知情）**：本轮只有一个档位，因此
**暂时不存在“分层”问题**——定价只需定一个价，R8 的风险延后到做进阶档时再面对。
代价：商业化验证只能证明“有人愿意为转发付费”，无法验证阶梯定价。

**（容器方案——本轮不做，保留为今后规划）**

若将来做容器部署隔离，容器节点 = **一个「加入了中心的普通节点」**，
不是中心内部的执行路径。这与 §2 原则一致，也能复用现有的节点注册/中继/授权链路。
届时需新建：

1. **编排服务**（创建/回收容器，挂租户卷，注入 join token）
2. **租户配额**（并发容器数、CPU/内存上限）
3. **计量与对账**（转发量、容器时长、token）

> **决策⑨⑬ 已明确：本轮不做容器方案，也不做进阶档**。
> 基础档（纯转发）足以验证商业化，且不依赖编排。
> 本段仅记录将来的方向，不构成承诺。

---

## 8. 分期建议与验收

| 阶段 | 内容 | 验收 | 状态 |
|---|---|---|---|
| **P0 身份底座** | Tenant/User/Membership/Principal；`protected` 改为解析身份；自助注册改 join token | 两个租户各登录 → 互不可见；节点注册无法自报 `tenant_id` | ✅ 已落地 |
| **P1 归属 + NodeAccess** | 注册表加归属/可见性；抽 `NodeAccess`；5 个消费点收敛 | 跨租户代理返回 403（✅ 见 §1.3，四个面均已加闸门）；`nk_` 改为 per-node | ✅ 主体已落地；**子项 `nk_` per-node ⏳ 未做**（§1.3） |
| **P2 隔离硬化** | 越权测试矩阵；观测投影去标题；**中心执行收窄到超管一个账号**（§3.3）；**清理历史 cron 任务**（§3.4，功能保留） | 租户账号在任何执行入口 403；超管照常工作；历史 `cron-*` 已清理，超管仍可新建定时任务 | ⚠️ 部分：中心执行收窄 ✅；**观测投影去标题 ✅**（`relay.observation.include_titles` 默认 false，摄入点剥离 + 事件日志改用 id + UI 明说）；**越权面硬化 ✅ 六个面**（节点列表遮蔽 / 单节点 / overview / proxy / forward / `/control/forwards`）；**越权限测试矩阵 ✅ 已收敛**为 [multi-tenant-authorization-matrix.md](./multi-tenant-authorization-matrix.md)（逐面闸门 + 对应回归测试 + 已知缺口）；历史 `cron-*` 清理需在中心机核对 |
| **P3 订阅** | **只做基础档**（纯转发）+ 计量（决策⑬；进阶档以后） | 无 `center_execution` 的账号在执行入口 403；计量可对账 | ✅ 已落地 |

**P3 验收现状**：

| 验收项 | 状态 |
|---|---|
| 无 `center_execution` 的账号在执行入口 403 | ✅ 已落地（§3.3） |
| **计量可对账** | ✅ 已落地（§7.1，真实二进制 E2E 18/18） |

**P0 曾是最大风险项**（237 处受保护路由注册、41 个文件、123 处 `WebToken` 引用——
精确值见附录 A 与 [p0-principal-migration-inventory.md](./p0-principal-migration-inventory.md)），
已按「先清点再改」的两步走完，不再单列一期。

**P2 的验收不能用「中心无会话落盘」**（前一版设计的错误验收项）：
中心需要保留超管的微信 channel，而它在中心侧确实落盘（§1.4）。
正确的验收是**「中心不落租户数据」**：超管自己的会话不算破例，
但必须能与租户数据区分开（§6）。

---

## 9. 风险与开放问题

| # | 风险 | 说明 |
|---|---|---|
| R1 | **P0 爆炸半径大** | 鉴权路径贯穿全部路由；建议先做一次全量入口清点（附录 A 为初版） |
| R2 | `nk_` 现为全局单凭据 | 改 per-node 会**打断现存已加入节点**（含你本机 `mac`），需一次性重新下发 |
| R3 | 中心侧 agent 收窄范围 | 决策⑨⑩ 已定：**微信保留、历史 cron 不迁移但功能保留**（§1.4 / §3.4）。需收窄的是「租户借中心算力」与**微信 bot 发送者**（§1.4：`allow_from: []` 已由方案 A 改为「只放行绑定者」，不再放行全部） |
| R4 | 「不存数据」与可用性冲突 | 已落地默认去标题（§6）：中心侧列表不再有标题，`Nodes` 节点详情标题列已加提示明说。「从目标节点实时取」未做——节点 `GET /sessions` 在 `requireCenterExecution` 下，需另开只读代理面；离线节点无标题可显示，与预期一致 |
| R5 | ~~容器编排是全新子系统~~ | **不再适用**（决策⑨：本轮不做容器隔离，§1.5 已降级）。将来若重启该方向，风险包括逃逸隔离与配额 |
| R6 | 支付/发票/税务 | 本设计不含计费通道（Stripe 等）与合规，属另一条线 |
| R7 | 代看权限本身的风险 | 「允许代看」（决策⑦）若无时限/审计/告知，会退化为「随时可看」；§3.6 给了约束，且默认走用户主动授权（⑪） |
| R8 | ~~进阶档差异化困难~~ | **延后**（决策⑬：暂时只做基础档，本轮只有一个档位、不存在分层问题）。做进阶档时再面对 |

**已拍板（2026-10-25）**：
- ⑥ 节点默认可见性 = **`private`** → §4.2
- ⑦ 平台超管**允许代看**租户内容，需用户协议/授权流程 → §3.6
- ⑧ 中心**微信 channel 会话保留** → §1.4，并据此修正了 §3.3 的前提
- ⑨ **中心可执行账号 = 超管一个**（微信 bot / 定时任务 / Web UI 均归它）；
  **暂不做容器部署隔离** → §1.4 / §3.3 / §7
- ⑩ 中心的**历史 cron 任务不迁移**（丢弃即可）；但 **cron 功能保留**，
  超管仍需要定时任务 → §3.4
- ⑪ 代看**默认走用户主动授权** → §3.6
- ⑫ 微信 bot 白名单等超管专属配置，**在超管身份的 settings 界面管理**
  （前置：P0 先给 `PUT /config` 加平台角色闸门）→ §3.3
- ⑬ 订阅**暂时只做基础档**（纯转发），进阶档留待以后 → §7

**仍待拍板**：
1. ~~微信 bot 白名单**具体收窄到哪个微信 ID**？~~ ✅ **已解决（2026-10-28 方案 A）**：
   无需提供具体 ID——空 `allow_from` 现在默认只放行**扫码绑定者本人**（见 §1.4）。
   仅当需要让**其他人**也能驱动 bot 时才需要填值（或填 `*` 显式放行全部）。
2. 代看的**紧急备用模式**是否本期实现，还是只做「用户主动授权」一条路径？

---

## 附录 A. 鉴权调用点清点（P0 爆炸半径初版）

| 类别 | 数量 | 代表位置 |
|---|---|---|
| 非测试路由注册 | **284 处 / 41 文件** | 分布见 `routes_*.go` |
| 受鉴权保护注册 | **237 处** | `protected(` / `nodeProxyRead(` / `read(` / `command(` / `approve(` |
| 鉴权中间件家族 | 6 类 | `protected`(219 regs)、watch `read/command/approve`(15)、`nodeProxyRead/Authorize`(3)、`relayAuthorize/relayTrustChecker`、`withBearerAuthProvider`、`withWatchScopeOrRelay` |
| `WebToken` 引用 | **123 处**（55 在测试、9 在 `httpapi.go`、4 在 `cmd/godex/main.go`） | 需全部改为 Principal |
| 节点目标解析点 | 5 类（§5） | `NewBridgeProxyHandler`(9)、`/control/nodes/{id}/proxy`(18)、`forward handler/server`(22)、`watchNodeTarget`(13)、`withForwardToNode`(15) |
| 执行入口（超管独占的闸点） | `sessions*`、`turns*`、`v1/chat/completions`、`v1/responses`、`v1/messages`、`v1/agent-steps` | `routes_sessions.go`(17)、`routes_turns.go`(11)、`routes_openai/responses/anthropic/steps` |

> 本表是**初版**（grep 计数 + 人工分类）。⚠️ 部分口径已过时：
> 精确值见 **[p0-principal-migration-inventory.md](./p0-principal-migration-inventory.md)**
> （268 处 / 39 文件；`WebToken` 非测试仅 35 处）。该清单同时给出「逐入口 →
> 改为 Principal 后行为」与迁移顺序，并附带 3 个安全发现（含 `/v1/terminal/*` 公网无鉴权）。

## 附录 B. 复现命令（实测）

```bash
# B-1 nk_ 全局凭据可枚举全部节点并代理进入任意节点（§1.3）
NK=$(ssh mycloud 'grep -E "^  node_proxy_token:" /root/.godex/godex.yaml | awk "{print \$2}"')
curl -s -H "Authorization: Bearer $NK" https://godex.claw.carc.top/api/control/nodes | jq '.nodes|length'   # 6
curl -s -o /dev/null -w "%{http_code}\n" \
  -H "Authorization: Bearer $NK" https://godex.claw.carc.top/api/control/nodes/mac/proxy/meta        # 200
curl -s -o /dev/null -w "%{http_code}\n" \
  -H "Authorization: Bearer $NK" https://godex.claw.carc.top/api/control/nodes/win-min/proxy/meta    # 200

# B-2 中心在跑 agent，与「仅转发」冲突（§1.4）
ssh mycloud 'grep -nE "^cron:|^  enabled:" /root/.godex/godex.yaml; ls /root/.godex/sessions | wc -l'  # 18

# B-3 无 docker SDK / 无编排（§1.5）
grep -c "docker/docker\|moby" go.mod          # 0
sed -n '609,626p' internal/platform/tooling/tooling.go   # exec.Command("docker","run",...)

# B-4 事件日志含会话标题且纯内存（§6）
grep -n "session.Title" internal/services/relay/eventstore.go      # :200 / :207
grep -cE "os\.|WriteFile" internal/services/relay/eventstore.go    # 0
```
