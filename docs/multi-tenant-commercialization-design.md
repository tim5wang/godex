# GoDex 商业化多租户设计（SaaS 中继 · 订阅制）

> 状态：Draft（设计待评审，尚未实施）
> 日期：2026-10-25
> 关联：[node-mesh-design.md](./node-mesh-design.md)、[node-center-bridge-design.md](./node-center-bridge-design.md)、[node-onboarding.md](./node-onboarding.md)、[remote-sandbox-design.md](./remote-sandbox-design.md)
> 决策输入（用户拍板，2026-10-25）：
> ① 做 **SaaS**（不是客户自托管）；② 服务器**只转发，不存用户数据**；
> ③ 现有节点**手动迁移**，本轮不写迁移脚本；④ 节点可见性**由节点主自己决定**，
> 但**是否允许共享由超管管**；⑤ 超管独占 agent 能力的实现方式**由本设计决定**。

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

**当前代码库是「单信任域 + 全局共享凭据」，离 SaaS 多租户有 5 处阻断项**（§1）。

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

### 1.4 阻断项 D：中心正在跑 agent，与「仅转发」冲突

中心配置 `cron.enabled: true`，`/root/.godex/sessions` 有 **18 个会话**（`cron-*`）。
即当前中心**既是中继又是执行体**。

**后果**：需求②要求服务器「不存用户数据」，而中心自己就在产生会话数据。
SaaS 中央节点必须收敛为**控制面**（注册/授权/中继/计量），不承载会话。
（用于运维的定时任务应移到独立实例或就地关闭。）

### 1.5 阻断项 E：进阶档的容器编排底座不存在

| 事实 | 证据 |
|---|---|
| docker 只是 CLI 包装 | `tooling.go:609` `exec.CommandContext(ctx, "docker", "run", "--rm", "-v", ...)` |
| 无 docker SDK / 无编排 | `go.mod` 无 `docker/docker`、`moby`；无容器生命周期管理 |
| 已有执行路由缝 | `ExecutionMode{Local,Docker,SSH,Relay}`（`tooling.go:322`）+ `RemoteSandbox`（`sandbox/remote.go`，5 测试） |

**后果**：「进阶档=服务器部署容器跑 agent」需要**真正的编排服务**（创建/回收/配额/隔离/挂载），
不能靠现有 CLI 包装。但**执行路由的抽象已经存在**，这是有利条件（§7）。

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
┌─────────────── 控制面（SaaS 中心，无 agent）───────────────┐
│  身份/租户   │  节点注册与归属  │  授权(NodeAccess)  │  计量/订阅  │
│  （P0）      │      （P1）      │      （P1）        │   （P3）    │
└──────────────────────────┬─────────────────────────────────┘
                           │ 中继（WSS 出站）+ HTTP 代理转发
        ┌──────────────────┴──────────────────┐
        ▼                                     ▼
  租户自有节点（用户电脑/服务器）        进阶档：平台置备的容器节点
  - 跑 agent，数据全在本机              - 由平台创建，作为「一个节点」注册
  - 只经中心转发，不落数据              - 数据在租户卷；中心不过手内容
```

**关键原则**：中心**只做四件事**——认证、授权、转发、计量。
执行永远发生在「节点」侧，不论该节点是用户的机器还是平台置备的容器。

**这也顺带回答了需求⑤**：超管不需要「特殊租户」，而是**平台级角色**
（`platform_admin`）——见 §3.3。

---

## 3. 身份模型（P0）

### 3.1 实体

| 实体 | 说明 |
|---|---|
| `Tenant` | 计费与隔离边界。`id`, `name`, `plan`, `created_at` |
| `User` | 登录主体。`id`, `email`, `password_hash`, `platform_role` |
| `Membership` | `(tenant_id, user_id, role)`。role ∈ `owner\|admin\|member` |
| `Entitlement` | 租户能力位：`relay_only` / `agent_runtime`（进阶档），见 §7 |

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

### 3.3 超管独占 agent 能力（决策⑤的落地）

**方案：能力位 + 平台角色，不引入特殊租户。**

理由：特殊租户会在授权判断里到处开 `if tenant == "system"` 特例，是越权的温床；
而能力位是**正交**的——加一个 `agent_runtime` entitlement，检查点只有一处。

具体三步：
1. 中心实例配置 `capabilities.execution: false` → **中心自身**不注册 agent 路由
   （直接关闭 `POST /sessions`、`POST /sessions/{id}/messages`、
   `POST /v1/chat/completions`、`/v1/responses`、`/v1/messages`、`/v1/agent-steps`
   等执行入口，共 237 处受保护路由中的执行子集）。
2. 普通用户（无 `agent_runtime`）经中心**只能转发**到自己的节点；中心不代跑。
3. 平台超管（`platform_role=admin`）保留诊断能力，但走**独立运维入口**，不与此混用。

> 这样「普通用户不具备 agent 执行能力」是**由部署形态保证的**，而不是靠每个 handler 记得判断。

### 3.4 自助注册（SaaS 必需）

现状自助加入需要**中心的完整 web token**（`routes_self_join.go:24-25`：`req.Token` 用 full web token）。
SaaS 下这等于把主密钥给了每个用户，**必须替换**：改为**租户内一次性 join token**，
注册时由中心解析出 `tenant_id` 并写入节点归属（§4）。

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

### 4.2 可见性与共享开关（决策④）

| 控制项 | 谁决定 | 语义 |
|---|---|---|
| `visibility` | **节点主** | `private`（默认）：仅本人可见；`tenant`：租户内可见 |
| `sharing_enabled` | **超管**（租户级或平台级） | 关闭后，本租户**不允许**把节点设为 `tenant` 可见 |

> 默认值取 `private`（更安全的默认）。要共享必须显式切换，且需超管未关闭共享策略。

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

**建议**：观测投影默认**不含标题**（新增 `relay.observation.include_titles`，默认 false）。
理由：需求②是「不存用户数据」，会话标题属用户内容；关闭后中心只看到
`session id + running 状态`，足够支撑 Tile/表盘，代价是列表标题需从目标节点实时取
（本来也是经代理取的）。

---

## 7. 订阅与 entitlement（P3）

| 档位 | entitlement | 能力 | 计量 |
|---|---|---|---|
| **基本档** | `relay_only` | 仅经服务器转发/代理到**自己的节点**；节点数上限 | 转发字节/请求数、在线节点数 |
| **进阶档** | `relay_only` + `agent_runtime` | 平台**置备容器节点**跑 agent；数据在租户卷 | 容器 CPU/内存时长、token 用量 |

**进阶档容器节点 = 一个「加入了中心的普通节点」**，不是中心内部的执行路径。
这与 §2 原则一致，也能复用现有的节点注册/中继/授权链路。

需要新建（§1.5 的空缺）：

1. **编排服务**（创建/回收容器，挂租户卷，注入 join token）
2. **租户配额**（并发容器数、CPU/内存上限）
3. **计量与对账**（转发量、容器时长、token）

> 不建议本轮做。基础档（纯转发）本身就足以验证商业化，且不依赖编排。

---

## 8. 分期建议与验收

| 阶段 | 内容 | 验收 |
|---|---|---|
| **P0 身份底座** | Tenant/User/Membership/Principal；`protected` 改为解析身份；自助注册改 join token | 两个租户各登录 → 互不可见；节点注册无法自报 `tenant_id` |
| **P1 归属 + NodeAccess** | 注册表加归属/可见性；抽 `NodeAccess`；5 个消费点收敛 | 跨租户代理返回 403（**当前为 200，见 §1.3**）；`nk_` 改为 per-node |
| **P2 隔离硬化** | 越权测试矩阵；观测投影去标题；中心关闭 agent 路由 | 中心无会话落盘；跨租户遍历测试全绿 |
| **P3 订阅** | 两档 + 计量；进阶档编排 | 无 entitlement 时执行入口 403；计量可对账 |

**P0 是最大风险项**：237 处受保护路由注册、41 个文件、123 处 `WebToken` 引用
（详见附录 A）。建议 P0 单独一期，不与业务改动混合。

---

## 9. 风险与开放问题

| # | 风险 | 说明 |
|---|---|---|
| R1 | **P0 爆炸半径大** | 鉴权路径贯穿全部路由；建议先做一次全量入口清点（附录 A 为初版） |
| R2 | `nk_` 现为全局单凭据 | 改 per-node 会**打断现存已加入节点**（含你本机 `mac`），需一次性重新下发 |
| R3 | 中心当前在跑 cron agent | 收敛为控制面会**中断现有定时任务**，需先确认哪些是有用的 |
| R4 | 「不存数据」与可用性冲突 | 去标题后，中心侧列表需实时取；离线节点将无标题可显示（可接受，但要在 UI 明说） |
| R5 | 容器编排是全新子系统 | 无 docker SDK 基础；包含安全隔离（不能逃逸到宿主/其他租户）与配额 |
| R6 | 支付/发票/税务 | 本设计不含计费通道（Stripe 等）与合规，属另一条线 |

**开放问题（需你后续拍板）**：
1. 节点默认可见性 `private` 是否合适？（更安全，但「加入即可见」的体验会变差）
2. 平台超管是否允许**代看**租户内容以排障？（安全与可运维的经典取舍）
3. 进阶档容器节点的数据卷归谁保管、是否加密？

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
