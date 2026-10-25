# GoDex 商业化多租户设计（SaaS 中继 · 订阅制）

> 状态：Draft（设计待评审，尚未实施）
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
> ⑩ 中心的 **cron 任务全部不要**，无需迁移；⑪ 代看**默认走用户主动授权**。

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

### 1.4 阻断项 D：中心正在跑 agent（收窄到单一账号）

中心实为**中继 + 执行体**：`cron.enabled: true`，`/root/.godex/sessions` 有 **18 个会话**。
按来源实测分布与处置（决策 2026-10-25）：

| 来源 | 数量 | 处置 |
|---|---|---|
| `cron-*` | 7 | ❌ **全部删除，无需迁移**（决策⑩） |
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

**一个必须收口的安全问题**：`channels.weixin.allow_from: []` 在
`senderAllowed` 中语义为「**放行全部发送者**」（`channel.go`：`len(allow)==0 → true`；
配置说明亦写明 "Empty or * allows all senders"）。即**任何知道该微信 bot 的人都能
触发中心上的 agent 执行**（在中心 workspace 内、带完整工具）。

按决策⑨，此项**无需新代码**，改为配置收口：`allow_from` 设为**超管自己的微信 ID 白名单**。
若不做，决策⑨ 的「只有一个用户能跑」在微信入口上就是假的。

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

### 3.2 Principal 传递

把 `protected` 从「比对常量」改为「解析身份」：

```
Authorization: Bearer <token>
        ↓
Principal{TenantID, UserID, PlatformRole, TenantRole, Entitlements}
        ↓  context
handler / NodeAccess
```

`withBearerAuthProvider(func() string)` 现签名只返回字符串
（`httpapi.go:154`），需扩展为返回 `Principal`。

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

**为什么不用「特殊租户」**：那会导致授权判断里到处出现
`if tenant == "system"` 特例，是越权的温床。能力位是正交的，检查点只有一处。

**微信入口必须同步收口**（否则「只有一个用户能跑」在微信上是假的）：
`channels.weixin.allow_from` 设为**超管微信 ID 白名单**（现为 `[]` = 放行全部，见 §1.4）。

### 3.4 中心 cron：全部删除（决策⑩）

中心 `cron.enabled: true` 与 7 个 `cron-*` 会话**全部不要，无需迁移**。
理由：中心的定位是中继控制面，定时任务属执行体职责，不该与租户流量混在同一实例。

**已核实「删了不会断活依赖」**（真机取证，2026-10-25）：

| 任务名 | 调度 | enabled | 投递 |
|---|---|---|---|
| 体检提醒 | `at 2026-08-28T14:00` | **false** | — |
| 周六面试知识点复习 | `at 2026-08-22T10:00` | **false** | — |
| 美团Q2财报摘要 | `at 2026-08-28T20:00` | **false** | — |
| 周日面试知识点复习 | `at 2026-08-23T10:00` | **false** | — |

四个任务**全部 `enabled: false`**，且都是一次性 `at` 计划（已过期一个多月），
即实际已无任何周期任务在跑。历史 `runs/` 显示它们曾通过微信投递，且全部为
`status: error`（`weixin sendmessage failed: ret=-2 ... prepare failed`）——
**本就没成功过**，故不存在“删了会断掉某条对外推送”的风险。

> 数字口径：会话数 **7**，任务定义 **4** 个，run 记录 **4** 条。实测对应关系：
> 4 条 run 记录引用的会话与 4 个任务一一对应
> （`60d4a15209ae22f1→b5e56f32f8cf1e3f`、`658f54ea1c1ccd69→ebcd9bf15b5e11e3`、
> `a308df6b062d3827→1314d9060ffb4c6c`、`cfd020e737330b4d→98c1eaa45e55afd3`）；
> 另有 **3 个会话无任何 run 记录引用**（`cron-3b6cf066e6bb2626`、
> `cron-a127e19bb1053931`、`cron-d030bf3015681f27`），**成因未确定**，
> 本设计不对其做推测。它们同属 `cron-*` 遗留，清理时一并删除。

> 处置：关闭 `cron.enabled`，清空 `cron-*` 会话与 `state/cron/jobs|runs`。
> 若将来需要定时任务，建在**超管自己的节点**上，而不是中心。

### 3.5 自助注册（SaaS 必需）

现状自助加入需要**中心的完整 web token**（`routes_self_join.go:24-25`：`req.Token` 用 full web token）。
SaaS 下这等于把主密钥给了每个用户，**必须替换**：改为**租户内一次性 join token**，
注册时由中心解析出 `tenant_id` 并写入节点归属（§4）。

### 3.6 运营代看（决策⑪：默认用户主动授权）

超管**可以**代看租户内容排障，但必须是**显式、有时限、可审计**的，不能是静默旁路。

| 要素 | 设计 |
|---|---|
| **默认模式** | **租户主动授权**（决策⑪，已拍板）：租户管理员在控制台授权并指定时长 |
| **备用模式** | 紧急模式（双人复核 + 事后通知租户）——保留但非默认 |
| **用户协议** | 注册/订阅时明确告知并留痕（`consent_record`：租户 id、条款版本、时间、来源 IP） |
| **时限** | 代看会话自动过期（如 2 小时），到期即失效；不提供「无痕续期」 |
| **可见性** | 代看期间租户侧**显式提示**（横幅 / 审计条目），不做隐蔽访问 |
| **审计** | 每次代看写入审计记录：谁、看哪个租户、何时、时长、范围 |
| **最小化** | 默认只读，且限排障所需的最小范围（不默认给全量导出） |

**复用既有范式**：`channels.Manager` 的 `AccessDecision`
（`channels.go:183`：`Action / Reason / DecidedAt` + `auditAccessDecision` 落审计）
就是「有决定、有理由、有审计」的现成形状，代看可对齐它。

> 注意：这条与「只转发不存数据」（决策②）不矛盾——代看的是**租户节点上**的数据，
> 中心只是持有访问通道；但通道本身必须受上述约束，否则「允许代看」会退化成「随时可看」。
> **不能靠超管的 `center_execution` 能力代替授权**：那是执行能力，不是跨租户读权。

---

## 4. 节点归属与可见性（P1）

### 4.1 注册表字段扩展

```go
type NodeInput struct {
    // ... 既有字段 ...
    TenantID    string // 由中心从 join token 解析，不接受请求体自报
    OwnerUserID string // 由中心从 join token 解析
    Visibility  string // "private" | "tenant"，节点主可改（决策④）
}
```

**必须由中心推导、忽略请求体**：`TenantID`/`OwnerUserID`。
这直接修掉 §1.2 的「可自报身份」问题。

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

### 4.4 现有节点迁移（决策③：手动）

不写脚本。提供一个**管理操作**把存量节点指认到租户：

```
POST /admin/nodes/{id}/claim   {tenant_id, owner_user_id}
```

实测存量 6 个节点（`node_8ae51969bb5cc3bb` / `mac` / `win-min` /
`node-153f6098` / `node-podman-test` / `win-pc`）由你按需指认。

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

**建议**：观测投影默认**不含标题**（新增 `relay.observation.include_titles`，默认 false）。
理由：需求②是「不存用户数据」，会话标题属用户内容；关闭后中心只看到
`session id + running 状态`，足够支撑 Tile/表盘，代价是列表标题需从目标节点实时取
（本来也是经代理取的）。

---

## 7. 订阅与 entitlement（P3）

两个 entitlement **不是同一件事**，别混用：

| entitlement | 含义 | 给谁 |
|---|---|---|
| `center_execution` | 允许在**中心实例**上跑 agent | **仅超管一个账号**（决策⑨）。**租户永不得**（§3.3） |
| `agent_runtime` | 平台为其**置备容器节点** | ❌ **暂不做**（决策⑨：容器部署隔离以后再规划） |

| 档位 | entitlement | 能力 | 计量 |
|---|---|---|---|
| **基本档** | `relay_only` | 仅经服务器转发/代理到**自己的节点**；节点数上限 | 转发字节/请求数、在线节点数 |
| **进阶档** | `relay_only` | 同上，区别在**额度与配额**（更多节点数 / 更高转发量） | 同上 |

> **两档暂都是「纯转发」**（决策⑨）：进阶档的区别在**额度与配额**，
> 而非「给容器跑 agent」。容器方案（`agent_runtime`）保留为以后规划，
> 本轮**不实现、不宣传**。
>
> `center_execution` 与 `relay_only` 不是一回事：前者是**平台自用能力位**（仅超管），
> 后者是**租户套餐能力**。两者都不应互相代替。

**（容器方案——本轮不做，保留为今后规划）**

若将来做容器部署隔离，容器节点 = **一个「加入了中心的普通节点」**，
不是中心内部的执行路径。这与 §2 原则一致，也能复用现有的节点注册/中继/授权链路。
届时需新建：

1. **编排服务**（创建/回收容器，挂租户卷，注入 join token）
2. **租户配额**（并发容器数、CPU/内存上限）
3. **计量与对账**（转发量、容器时长、token）

> **决策⑨ 已明确：本轮不做**。基础档与进阶档均为纯转发，足以验证商业化，
> 且不依赖编排。本段仅记录将来的方向，不构成承诺。

---

## 8. 分期建议与验收

| 阶段 | 内容 | 验收 |
|---|---|---|
| **P0 身份底座** | Tenant/User/Membership/Principal；`protected` 改为解析身份；自助注册改 join token | 两个租户各登录 → 互不可见；节点注册无法自报 `tenant_id` |
| **P1 归属 + NodeAccess** | 注册表加归属/可见性；抽 `NodeAccess`；5 个消费点收敛 | 跨租户代理返回 403（**当前为 200，见 §1.3**）；`nk_` 改为 per-node |
| **P2 隔离硬化** | 越权测试矩阵；观测投影去标题；**中心执行收窄到超管一个账号**（§3.3）；**删除中心 cron**（§3.4） | 租户账号在任何执行入口 403；超管照常工作；中心无 `cron-*` |
| **P3 订阅** | 两档（**均为纯转发**）+ 计量 | 无 `center_execution` 的账号在执行入口 403；计量可对账 |

**P0 是最大风险项**：237 处受保护路由注册、41 个文件、123 处 `WebToken` 引用
（详见附录 A）。建议 P0 单独一期，不与业务改动混合。

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
| R3 | 中心侧 agent 收窄范围 | 决策⑨⑩ 已定：**微信保留、cron 全删**（§1.4 / §3.4）。需收窄的是「租户借中心算力」与**微信 bot 发送者白名单**（§1.4：`allow_from: []` 现为放行全部） |
| R4 | 「不存数据」与可用性冲突 | 去标题后，中心侧列表需实时取；离线节点将无标题可显示（可接受，但要在 UI 明说） |
| R5 | ~~容器编排是全新子系统~~ | **不再适用**（决策⑨：本轮不做容器隔离，§1.5 已降级）。将来若重启该方向，风险包括逃逸隔离与配额 |
| R6 | 支付/发票/税务 | 本设计不含计费通道（Stripe 等）与合规，属另一条线 |
| R7 | 代看权限本身的风险 | 「允许代看」（决策⑦）若无时限/审计/告知，会退化为「随时可看」；§3.6 给了约束，且默认走用户主动授权（⑪） |
| R8 | **进阶档差异化困难** | 两档均为纯转发（决策⑨）后，进阶档只能靠「额度与配额」拉开差距；若配额定不出吸引力，订阅分层可能立不住 |

**已拍板（2026-10-25）**：
- ⑥ 节点默认可见性 = **`private`** → §4.2
- ⑦ 平台超管**允许代看**租户内容，需用户协议/授权流程 → §3.6
- ⑧ 中心**微信 channel 会话保留** → §1.4，并据此修正了 §3.3 的前提
- ⑨ **中心可执行账号 = 超管一个**（微信 bot / 定时任务 / Web UI 均归它）；
  **暂不做容器部署隔离** → §1.4 / §3.3 / §7
- ⑩ 中心 **cron 全部删除**、无需迁移 → §3.4
- ⑪ 代看**默认走用户主动授权** → §3.6

**仍待拍板**：
1. 微信 bot 白名单具体收窄到哪个（超管的）微信 ID？（现为 `[]` = 放行全部）
2. 基础档与进阶档的**配额差异**（节点数 / 转发量 / 并发）如何定？（两档都是纯转发，需要靠配额拉开差距，见 R8）
3. 代看的**紧急备用模式**是否本期实现，还是只做「用户主动授权」一条路径？

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

> 本表是**初版**（grep 计数 + 人工分类）。P0 开工前应补一份「逐入口 → 改为 Principal 后行为」的清单，
> 这才是 P0 的真实工作量。

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
