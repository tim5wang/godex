# `nk_` 改 per-node：迁移与重下发方案

> 状态：Implemented / Partial（P1+P2 已落地并提交：per-node 签发 + 双认 + 身份接线 + 节点自助换发；P4 收尾待部署后执行）
> 日期：2026-10-28
> 关联：[multi-tenant-commercialization-design.md](./multi-tenant-commercialization-design.md) §1.3 / §9 R2、[multi-tenant-authorization-matrix.md](./multi-tenant-authorization-matrix.md)
> 用途：把设计 §9 R2「`nk_` 现为全局单凭据」从风险条目变成**可执行的迁移 + 重下发步骤**，
> 并如实给出中断面、回滚与验收标准。

---

## 0. 结论速览

| 项 | 结论 |
|---|---|
| 现状 | 中心只有**一个** `nk_`（`control.node_proxy_token`，全局），任一节点持有它即**不可区分** |
| 目标 | `nk_` **per-node**：中心能判定「是哪个节点」，据此得到该节点的 **id + tenant** |
| 中断面 | **可为零**：新旧并存（dual-accept），节点自助换发后再清掉全局值 |
| 重下发 | **自助为主**（节点已持有自己的 `ck_`），CLI/超管接口为兜底；**无需人去每台机器改配置** |
| 真实收益 | ① 修一个**多租户下节点连不进自己租户节点**的既存缺陷；② 吊销/轮换粒度；③ 审计归因 |
| ⚠️ 实测发现 | 本机 `mac` 的 `GODEX_CONTROL_CENTER_TOKEN` **就是中心完整 web token**（不是 `nk_`），见 §2.3 |
| 需要决策 | 三项，见 §7 |

---

## 1. 现状（均为代码/实测事实，不是设计稿描述）

### 1.1 全局单凭据是怎么形成的

| 位置 | 事实 |
|---|---|
| `internal/services/relay/credential.go:24` | `GenerateNodeProxyToken()` 生成 `nk_` + 32 字节 hex（67 字符） |
| `internal/runtime/httpapi/routes_join.go:235` | `ensureNodeProxyToken`：**已存在则直接返回现值** → 全局只有一个 |
| `internal/runtime/httpapi/routes_self_join.go:224` | `POST /control/node-proxy-token`（`protected`）把**同一个** `nk_` 发给任何持 web token 者 |
| 中心配置 | 明文存 `control.node_proxy_token`（Secret，schema.go:112） |

### 1.2 `nk_` 到底解锁哪些面（爆炸半径）

认证器（`withBearerAuthProvider` 的 `relayTrust...` 参数）与分类器（`principalForRequest`）是两件事，必须分开看：

| 面 | `nk_` 是否解锁 | 证据 |
|---|---|---|
| `nodeProxyRead`（`GET /control/nodes`、`/{id}`、`/{id}/overview`） | ✅ 是**认证器**之一 | `httpapi.go:202-204` |
| `protected`（`/config`、`/sessions`、`/admin/*`） | ❌ 否（只在分类器里，不在认证器里） | `httpapi.go:188-190`；实测 401 |
| proxy / forward / forwards CRUD | ✅ 是（`nodeProxyAuthorize` 无条件 `bearerMatches(nk_)`） | `cmd/godex/main.go:953` |
| `/push/` | ❌ 否（`relayAuthorize` 只认 web token） | `cmd/godex/main.go:929` |

**生产实测（只读探测，未发任何写操作）**：

```
nk_ -> /control/nodes        200   （列出全部 6 个节点）
nk_ -> /control/nodes/win-pc 200   （拿到该节点完整记录）
nk_ -> /control/nodes/<不存在> 404
nk_ -> /config               401
nk_ -> /sessions             401
nk_ -> /admin/tenants        401
```

### 1.3 分类后是什么身份（问题的根）

`internal/runtime/httpapi/principal.go:91`：`nk_` → `Principal{UserID:"relay-node", Kind:KindNode, Entitlements:[relay_only]}`。

**关键点：这个身份既没有节点 id，也没有 tenant。** 由此推出两个后果：

- `identity.Policy.Authorize`（`policy.go`）里，节点行动者 `TenantID == ""`；
  当目标节点**有** tenant 时，落进「跨租户」分支 → 被拒。
  **即多租户下，一个节点连自己租户的节点都进不去**——这是既存缺陷，不是新问题。
- 单租户下（当前生产：6 个节点 `tenant=None`）走 `principalTenant=="" && nodeTenant==""` 的放行分支，
  于是 `nk_` 能枚举全部节点并读取其记录（上表实测）。

### 1.4 设计文档里需要修正的一句话

设计 §1.3 把「彻底消除枚举泄漏」寄望于 per-node 凭据。**这话只对一半**：
per-node 身份带来的是**归属与粒度**，而「403 vs 404 能探测某 id 是否存在」这一点，
只有在**多租户**下被收窄到「本节点自己租户内」才算消除；单租户下它依然存在
（任何注册表查询都天然会区分存在/不存在）。本方案按此如实设定收益，不夸大。

---

## 2. 目标模型与实测发现

### 2.1 目标身份

`nk_` 命中某个节点后，分类出的主体改为：

```
Principal{
  UserID:   <presenting node id>,     // 而不是常量 "relay-node"
  TenantID: <该节点在注册表里的 tenant>, // 新增：这正是修 §1.3 缺陷的关键
  Kind:     KindNode,
  Entitlements: [relay_only],
  AuthMethod:   "node_proxy_token",
}
```

`relay_only` 与 `IsNodeActor()` 语义不变——节点仍**不是**操作员，拿不到 `center_execution`
（`TestClassifyPrincipalNodeProxyTokenIsNotOperator` 继续有效）。

### 2.2 镜像既有 `ck_` 模式，不发明新东西

`ck_` 已经是 per-node 的现成范式，直接复用：

| `ck_`（已有） | `nk_`（本方案） |
|---|---|
| `NodeView.CredentialHash`（`registry.go:105`） | 新增 `NodeView.ProxyTokenHash` |
| `SetCredentialHash`（`registry.go:275`） | 新增 `SetProxyTokenHash` |
| `POST /control/nodes/{id}/credential` 重签发（`routes_control.go:133`） | 新增 `POST /control/nodes/{id}/proxy-credential` |
| 中心侧 `relay.ValidateCredential(cred, hash)` 比对 | 复用同一函数 |

### 2.3 ⚠️ 实测发现：本机 `mac` 持有的是**完整 web token**

```
mac  ~/.godex/.env  GODEX_CONTROL_CENTER_TOKEN  len=48  sha256=ee0f5887…
中心  godex.yaml  web.token                     len=48  sha256=ee0f5887…   ← 完全一致
中心  godex.yaml  control.node_proxy_token      len=67（nk_，前缀 nk_）
mac  godex.yaml  control.center_token           len=32（另一个值，被 .env 覆盖）
```

结论与影响：

1. `mac` 上生效的 center token 是**中心超管令牌**，不是受限 `nk_`。
   `mac` 对被控中心拥有全部权限（config / sessions / admin），远超 `nk_` 的授权面。
2. 因此 per-node 迁移**不会自动修掉它**：`mac` 必须被**显式换发**成 per-node `nk_`，
   否则迁移完成度是假的。§5 把它列为独立验收项。
3. 值由 `resolve.go` 的 env 覆盖优先级决定（`GODEX_CONTROL_CENTER_TOKEN` > yaml `control.center_token`），
   所以「改 yaml」不会生效——重下发必须落到 `.env`。

---

## 3. 分阶段实施

### P1 — 存储 + 签发 + 双认（零中断）

1. `noderegistry`：`NodeView` 加 `ProxyTokenHash`（`omitempty`，旧记录加载后为空）、`ProxyTokenIssuedAt`。
2. `mintNodeProxyToken(registry, nodeID) (plaintext, error)`：生成 → 存 hash → **一次性**返回明文。
3. 三个签发点全部改为**per-node**：
   - `registerJoinRoute`（join token 路径）、
   - `registerSelfJoinRoute`（自助接入路径），
   - 新增 `POST /control/nodes/{id}/proxy-credential`（`protected`，超管重签发/兜底）。
   三条路都已经知道 `node_id`，无需额外输入。
4. 解析器 `nodeProxyTokenResolver(registry, manager) func(*http.Request) (nodeID string, legacy bool, ok bool)`：
   **先查 per-node hash，未命中再退回全局值**（`legacy=true`）。
5. `classifyPrincipal` 用解析结果填 `UserID`/`TenantID`；legacy 命中维持今天的 `"relay-node"` 行为。

> P1 结束时：新接入节点自动 per-node；存量节点仍用全局值照常工作。**无任何中断。**

### P2 — 自助重下发（关键：不用挨台改配置）

新增 `POST /control/self/proxy-credential`：**用节点自己的 `ck_` 认证**（不是 web token）。

- 认证：新增 `nodeCredentialChecker(registry)`，按 `CredentialHash` 反查出发起节点；
  仅挂在这一个路由上（不做成通用认证器，避免扩大 `ck_` 的授权面）。
- 语义：只能给自己换发（hash 决定的节点 id 即目标，**请求体不接受 node id**）。
- 节点侧：
  - CLI `godex node rotate-proxy-token`（确定性路径）；
  - `CenterBridge` 收到 **401** 时自动调用一次并热重载（`control_runtime.go` 的
    `controlConfigChanged` 已把 `CenterToken` 纳入热重载条件，落盘即生效，无需重启）。

> 这样存量节点**自助完成**换发：要么运维跑一条 CLI，要么节点自己撞到 401 就自愈。

### P3 — 身份/租户接线

`classifyPrincipal` 填好 `TenantID` 后，`Policy.Authorize` 立刻从「节点行动者无租户 → 跨租户被拒」
变成「节点行动者按自己租户判定」。**这修的是 §1.3 里那条真缺陷**，且不需要改 Policy 逻辑——
只需主体带上真实 tenant。补一条回归测试锁住它。

### P4 — 收尾

- 超管确认无节点再使用全局值后，清空 `control.node_proxy_token`。

  > **⚠️ 实测修正（2026-10-28）**：本节原文写「`clear_secrets` 路径已受平台角色闸门保护」，
  > **这句是错的**。核查发现 `control.node_proxy_token` **未标记 `PlatformAdmin`**，
  > 因此它是**非受限键**，`authorizeConfigWrite` 对租户用户直接放行——既可改写也可清空。
  > 而该值就是解锁节点代理面的凭据：能写它的人可以自选一个 nk_、自己出示，
  > 解析器便报 legacy 命中（无节点、无租户），在单租户下被策略放行——
  > 等于拿到 per-node 迁移正要消除的那份访问。已补 `PlatformAdmin: true`
  > （fail-first 证实：修前租户用户对「改写」与「清空」均未被拒）。
  > 教训：**「已受闸门保护」必须用 `platformAdminOnlyPaths` 实测，不能凭印象写**。
- 文档：设计 §1.3 / §9 R2、授权矩阵第 82 行、`admin-console-guide.md:162/172/333` 同步为 per-node（✅ 已做，P4b）。
- 全局值字段标记 deprecated，后续版本删除。

### P4a 前置条件：实测**尚未满足**（2026-10-28）

清空全局值前必须满足方案 §4 的完成判据「中心不再观测到全局值命中」。实测：

| 检查项 | 实况 | 结论 |
|---|---|---|
| 生产中心二进制 | `godex v1.5.0 commit b70a5d1040e3 built 2026-10-27T15:37:41Z`——**早于** P1（`bdd7ac9`） | **旧代码**，无 per-node 解析/存储（已用 `git show 2306511:...` 核验：`NodeByProxyTokenHash`、`ProxyTokenHash` 命中数均为 0） |
| 节点换发状态 | 6 个节点的 `proxy_token_hash` **全部为空** | **零个节点已换发** |
| 完成判据 | 全部节点仍在用全局值 | **不成立** |

⇒ **此时清空会打断节点侧的「以中心为客户端」路径**（旧中心对旧 nk_ 直接 401）。

但影响面**不是「全部 6 个节点」**（本节初稿这么写，属过度表述，现修正）：

| 路径 | 用什么凭据 | 清空全局 nk_ 是否受影响 |
|---|---|---|
| 中心**驱动**节点（proxy / exec / forward 到目标节点） | 目标节点的 `ck_`（注册表 `credential_hash`） | **否**——这条主链走 ck_，与 nk_ 无关 |
| 节点**作为中心客户端**（bridge 列节点 / 转发 / 远端沙箱） | 自己的 `center_token` | **仅当其 `center_token` == 全局 nk_** 时受影响 |
| 本机 `mac` | **中心完整 web token**（sha256 `ee0f5887…`，与中心 `web.token` 完全一致；**不等于**全局 nk_，后者 sha256 `6ef7645e…`） | **否** |

⇒ 精确说法：只有「`center_token` 恰为全局 nk_ 且在跑」的节点会被打断。
其余 5 个节点的 `center_token` 实值未逐一探测（`win-pc`/`win-min`/`node-podman-test`/`node-153f6098` 多为离线），
**故在清空前必须先用中心日志确认「再无请求命中全局 nk_」**，而不是靠节点数量推断。

**正确顺序**：先部署 P1+P2 到中心（存量节点零中断）→ 节点逐步换发（CLI 或 401 自愈）
→ 确认不再有请求命中全局值 → 备份原值后清空。

> 注意：401 自愈的代码在**节点自己的二进制**里，旧节点不会自愈；未升级的节点需显式跑
> `godex node rotate-proxy-token`。因此「节点侧升级」是这一步的真实工作量，不是零成本。

---

## 4. 为什么不会中断（与设计的 R2 结论不同）

设计 R2 写「改 per-node 会打断现存已加入节点，需一次性重新下发」。**用双认后这个前提不成立**：

- 节点在 P1 之后**照常用旧全局 `nk_`**，中心把命中全局值的主体按今天的行为处理 → 不中断。
- 节点按自己的节奏换成 per-node（CLI 或 401 自愈）→ 逐步收敛。
- 完成判据不是「某天统一切换」，而是**中心侧观测到不再有请求命中全局值**。

风险登记与设计 R2 的差异应回填到设计文档，避免后续读者按「会中断」排期。

---

## 5. 验收标准（可执行）

| # | 验收项 | 方式 |
|---|---|---|
| V1 | per-node 生效：节点 A 的 `nk_` 解析出 A 的 id | 单测：mock registry，断言 `Principal.UserID == "A"` |
| V2 | 吊销粒度：换发 A 后**旧 A 的 nk_ 失效**，B 不受影响 | 单测 + 真机：A 换发后旧值 401，B 仍 200 |
| V3 | **多租户修复**：tenant `t1` 的节点能代理到 `t1` 的节点；跨到 `t2` 仍拒 | 单测（Policy）+ 集成 |
| V4 | 节点仍**不是**操作员 | 既有 `TestClassifyPrincipalNodeProxyTokenIsNotOperator` 保持通过 |
| V5 | 自助换发只能换自己 | 单测：A 的 `ck_` 调用后，B 的 hash 不变 |
| V6 | **`mac` 降权**（§2.3） | 换发后实测：`mac` 持有的 token → `/config` 401、`/admin/tenants` 401（当前可 200） |
| V7 | 无中断 | 灰度期间：存量节点全部继续 200；中心日志无「全局 `nk_` 命中」残留 |

---

## 6. 回滚

- P1/P3：纯加法（新字段 `omitempty`，新解析器**先查 per-node 再退回全局**）。回滚 = 还原二进制，
  全局值从未被删 → 存量节点无感。
- P4 清空全局值：**不可逆点**。故设两道前置：完成判据成立（全部节点已换发）；
  清空前先备份 `control.node_proxy_token` 原值。
  万一回滚，重新写回备份值即可（`Update` 会热重载，无需重启）。
  清空操作本身要求平台角色：该键已补 `PlatformAdmin: true`（见 §3 P4 的实测修正）。

---

## 7. 决策（已批准 2026-10-28）

1. **双认窗口**：✅ 按 §4 **保留全局值直到收敛**（不硬切）。已实现：解析器先查 per-node、
   未命中退回全局。
2. **自助换发触发**：✅ **CLI + 401 自愈**（两者都做）。已实现：
   `godex node rotate-proxy-token` 与 `CenterBridge.SetUnauthorizedHandler`。
3. **per-node 可达性策略**：✅ **不做**。理由：它会把「节点间转发」这一既有能力
   变成需要配置的功能，超出本次「修身份/吊销」的目标。
   （仍存在的一处泄漏如实记录于设计 §1.3：「403 vs 404 探测 id 是否存在」
   在单租户形态下依然存在，本期不试图消除。）

> 实现记录：P1+P2 已提交（`bdd7ac9`、`8f5668a`）。剩余 P4：
> 确认无节点再用全局值后清空 `control.node_proxy_token`，并同步文档。
> 本文档状态已从 Draft 改为 Implemented/Partial。

---

## 附录 A：证据索引

| 事实 | 出处 |
|---|---|
| `nk_` 生成 | `internal/services/relay/credential.go:24` |
| 全局单凭据 | `internal/runtime/httpapi/routes_join.go:235` |
| 下发端点 | `internal/runtime/httpapi/routes_self_join.go:224` |
| `nk_` 认证器集合（nodeProxyRead） | `internal/runtime/httpapi/httpapi.go:202-204` |
| `protected` 认证器集合（不含 `nk_`） | `internal/runtime/httpapi/httpapi.go:188-190` |
| `nk_` 分类 | `internal/runtime/httpapi/principal.go:91` |
| proxy/forward 闸门（接受 `nk_`） | `cmd/godex/main.go:953` |
| 中心按 `ck_` hash 认节点（hub） | `cmd/godex/main.go:355` |
| `ck_` per-node 范式 | `internal/services/noderegistry/registry.go:105/275`、`routes_control.go:133` |
| 节点侧换发 nk_ 的既有实现 | `internal/app/node.go:425`、`routes_self_join.go:178` |
| NodeAccess 跨租户分支 | `internal/core/identity/policy.go`（`Authorize`）、`VisibleNodeIDs:282` |
| `nk_` 作远端 sandbox 凭据 | `internal/agent/sandbox_facade.go:32`、`config/schema.go:300` |
| 生产实测 | 本次会话只读探测（§1.2、§2.3） |
