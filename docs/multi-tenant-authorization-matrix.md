# 多租户越权面与测试矩阵

> 状态：Active / Implemented（2026-10-28 核对；收敛 §8 P2「越权测试矩阵」遗留项）
> 日期：2026-10-28
> 关联：[multi-tenant-commercialization-design.md](./multi-tenant-commercialization-design.md)（§1.3 阻断项 C、§3 身份面、§4 节点归属、§5 NodeAccess seam、§8 状态表）、[feature-implementation-matrix.md](./feature-implementation-matrix.md)
> 用途：判断「某个面是否真的有租户隔离」时**先查本文**；每条结论都指向可运行的测试。

---

## 1. 为什么需要这份矩阵

设计 §1.3 的阻断项 C 是「任一已加入节点持有的凭据可代理进入任意节点」。加固过程分多次落地，
每处闸门都写在自己的测试文件里，**同一类漏洞的第三个面因此漏了**：

- `/control/nodes/{id}/forward` 加了 `ForwardHandler.AuthorizeNode`；
- 但 `/control/forwards`（托管隧道 CRUD，§5 消费点 #4 的另一半）没有——它的凭据闸门
  接受任意登录会话，且 `registerForwardRoutes` 不接收 registry/policy，结构性无法判归属。

所以「有没有闸门」不能靠设计文档的勾选，要看**每个面 + 它的测试**。本表是那份清单。

## 2. 六类「到某节点」的面

| # | 面 | 入口 | 授权闸门 | 回归测试 |
|---|---|---|---|---|
| 1 | 节点列表（遮蔽） | `GET /control/nodes` | `identity.Policy.VisibleNodeIDs` | `TestNodeListMasksOtherTenants`、`TestNodeListPreservesRegistryOrder`、`TestNodeListEmptyWithoutPrincipal`、`TestNodeListSingleTenantStillWorks`（`httpapi/node_access_test.go`）；`TestVisibleNodeIDsMasksForeignNodes`（`core/identity`） |
| 2 | 单个节点 | `GET /control/nodes/{id}` | `authorizeNodeAccess` → `Policy.Authorize` | `TestNodeGetForbidsCrossTenant` |
| 3 | 节点观测投影 | `GET /control/nodes/{id}/overview` | 同上（同一 seam，overview 带节点活动故必须同闸） | `TestNodeOverviewForbidsCrossTenant` |
| 4 | HTTP 代理 | `/control/nodes/{id}/proxy/...` | `ProxyHandler.AuthorizeNode` | `TestProxyHandlerAuthorizeNodeDeniesForward`、`…AllowsForward`、`…RunsBeforeLocalDispatch`、`TestProxyHandlerWithoutAuthorizeNodeIsUnchanged`（`relay/proxy_nodeaccess_test.go`） |
| 5 | WS TCP 隧道 | `/control/nodes/{id}/forward` | `ForwardHandler.AuthorizeNode` | `TestForwardHandlerAuthorizeNodeDeniesUpgrade`、`…AllowsUpgrade`、`…WithoutAuthorizeNodeUnchanged`、`TestForwardHandlerDenialBodyIsForbiddenError`（`relay/forward_nodeaccess_test.go`） |
| 6 | **托管隧道 CRUD** | `GET/POST /control/forwards`、`DELETE /{id}`、`POST /{id}/check` | 与 #4/#5 **同一个** `newNodeAccessAuthorizer` 工厂 | `TestForwardCreateRejectsCrossTenantNode`、`TestForwardCreateStillAllowsOwnNode`、`TestForwardListMasksOtherTenants`、`TestForwardDeleteAndCheckRejectCrossTenantNode`、`TestForwardGateStillAcceptsTenantSession`、`TestForwardAuthorizerAgreesWithProxySeam`（`cmd/godex/forward_authz_test.go`） |

**面 6 的两个要点**（这次修复补上，容易被后续改动退化）：

1. **授权必须在副作用之前**。创建路径的 `denyNode` 在 `server.Add()` **之前**调用——
   否则监听已经绑好，「响应说不行」时隧道已经存在。`TestForwardCreateRejectsCrossTenantNode`
   同时断言 403 **和** 隧道数为 0，就是为了钉住这个顺序。
2. **`nil` 决策函数必须拒绝**（503），不是放行。「没配检查」若退化成「所有节点都可达」，
   就是面 6 最初失去隔离的形状；接线错误要响亮且安全。

## 3. 策略层（六类面共用同一个决策）

面 2/3/4/5/6 都汇到 `identity.Policy.Authorize`，所以策略本身的正确性决定了它们全部。

| 性质 | 测试 |
|---|---|
| 跨租户默认拒绝 | `TestAuthorizeNodeAccess`（`core/identity/identity_test.go`） |
| 单租户部署下：超管与节点角色放行，**无租户的普通主体拒绝**（防止「tenant id 加载失败」变成「可达所有节点」） | 同上 |
| 同租户内：`private` 仅 owner 可达；`tenant` 可见性看共享开关 | `TestSharingDisabledBlocksPeerReachButNotSelf`（`httpapi/routes_admin_test.go`） |
| 共享开关关闭只阻断对等，不影响 owner 自达 | 同上 |
| 节点归属只由可信路径写入，注册请求不能自报 | `TestNodeInputDoesNotAcceptOwnership`、`TestRegistryRegisterCannotOverwriteOwnership`、`TestRegistryOwnershipDefaultsToPrivate`（`noderegistry/ownership_test.go`） |
| **节点角色 ≠ 操作员**（`nk_` / relay 身份不获得 `center_execution`） | `TestNodeActorIsNotOperator` |
| 机器凭据（proxy key）无租户 → 不可达任何节点 | `TestProxyKeyPrincipalIsUnscopedAndThereforeCannotReachNodes`、`TestProxyKeyPrincipalHoldsNoCapabilities`（`httpapi/proxy_key_identity_test.go`） |
| 手表凭据无租户 → 不可达任何节点 | `TestWatchPrincipalIsUnscopedAndThereforeCannotReachNodes`、`TestWatchPrincipalHoldsNoCapabilities`（`httpapi/watch_principal_test.go`） |

## 4. 中心执行与平台面（与节点面并列的另一组边界）

| 性质 | 测试 |
|---|---|
| 只有超管能在中心跑 agent；租户用户/租户管理员 403；缺 principal 时**fail closed** | `TestRequireCenterExecutionAllowsOperator`、`…RefusesTenantUser`、`…RefusesTenantAdmin`、`…AllowsNodeActor`、`…RefusesBareRole`、`…FailsClosedWithoutPrincipal`（`httpapi/center_execution_test.go`） |
| 租户用户被拒于中心会话面；且**不能删掉操作员的会话** | `TestTenantUserIsRefusedTheCenterSessionSurface`、`TestTenantDeleteCannotDestroyTheOperatorsSession`、`TestOperatorStillOwnsTheCenterSessionSurface`（`httpapi/routes_center_local_gate_test.go`） |
| 平台管理面拒绝租户用户（连自己租户的行也不行） | `TestAdminRoutesRefuseTenantUser`、`TestAdminRoutesRequireAuthentication` |
| 节点归属变更（claim）仅平台管理员；节点角色不得 claim | `TestAdminClaimAssignsOwnership`、`...RequiresPlatformAdmin`、`TestAdminClaimRefusesNodeActor`、`TestRequirePlatformAdminRefusesTenantAdmin`（`httpapi/admin_claim_test.go`） |
| 平台专属配置键（如微信 `allow_from`）拒绝租户用户、fail closed | `TestSchemaMarksWeixinAllowFromPlatformAdmin`、`TestAuthorizeConfigWriteRefusesTenantUser`、`...FailsClosedWithoutPrincipal`、`TestRestrictedConfigKeysInspectsValuesAndClearSecrets`（`httpapi/config_admin_gate_test.go`） |
| 匿名 guard：代理请求无 token 必须拒（并点明是哪个 header） | `TestAnonymousGuardRefusesProxiedRequestsWithoutToken`、`...NamesTheHeader`、`...StepsAsideWhenTokenConfigured`（`httpapi/anonymous_guard_test.go`） |

## 5. 数据面（「中心不留租户数据」的对应测试）

| 性质 | 测试 |
|---|---|
| 账号面自作用域：调用者只能读自己的资料/会话/登录记录，**路径里没有可寻址他人 id 的入口** | `TestUserCentreScopesToTheCaller`、`TestAccountSurfacesRequireAuthentication`（`httpapi/routes_account_test.go`） |
| 邀请码一次性、不能授予 owner、owner 不可被移除/降级 | `TestInviteCodeIsOneTime`、`TestInviteCannotGrantOwnership`、`TestOwnerIsProtectedFromRemovalAndDemotion` |
| 租户成员面拒绝无租户调用者 | `TestTenantSurfaceRefusesCallersWithoutATenant` |
| 转发计量按调用者租户作用域；跨租户对账仅超管 | `TestTenantMeteringIsScopedToTheCaller`、`TestAdminMeteringCrossesTenants`（`httpapi/routes_metering_test.go`） |
| 代看（delegation）：只用活跃授权，审计落库，撤销即止；**同租户对等读不算代看**（不污染审计） | `TestDelegatedReadIsAuditedAndEndsAtRevocation`、`TestTenantPeerReadIsNotAuditedAsDelegation`、`TestAuthorizeDelegationCoversOnlyActiveReadGrants`、`TestAuthorizeDelegationDoesNotExtendToNonAdmins` |
| 观测投影默认不含会话标题（标题属用户内容） | `TestEventStoreStripsSessionTitlesByDefault`、`TestEventStoreStrippedTitleNeverReachesEventLog`、`TestEventStoreIncludeTitlesOptIn`（`relay/eventstore_test.go`）；配置默认值见 `core/config/relay_config_test.go` |
| 租户节点数上限强制（超限拒绝入网，超管 claim 不受限） | `TestJoinRouteRefusesEnrolmentPastTenantLimit`、`TestJoinRouteUnlimitedByDefault`、`TestAdminClaimIgnoresTheAllowance`（`httpapi/join_node_limit_test.go`） |

### 5.1 中心本地面（非节点面）：flow 及其同类

上面 §2–§5 都在回答「某个**到节点**的面能否隔离」。还有一类面根本不到节点：数据存在**本实例**上。
「多节点平台多租户」形态下它们**不属于任何租户**——中心的 flow 店、notes 店、taskboard 台账、
本机模型配置，都是**平台操作员自己这台机器的状态**（设计 §6：中心不留租户内容）。
所以这类面的正确边界不是「按租户作用域」，而是「**租户根本到不了**」。

闸门：`requireCenterLocalData`（`httpapi/center_local_gate.go`）——平台管理员通过、节点行动者通过
（远程节点 Settings/Flows 页要能用自己的身份打到自己的数据，与 `requirePlatformAdminOrNodeActor`、
`authorizeConfigWrite` 同一条豁免）、其余主体 403、无 Principal 则 401（失败关闭）。

| 面 | 入口 | 闸门 | 回归测试 |
|---|---|---|---|
| Flow 定义面（list / generate / create / get / validate / publish / delete version / delete） | `GET\|POST\|DELETE /v1/flows...` | `requireCenterLocalData(protected)` | `TestTenantUserIsRefusedTheCenterLocalFlowSurface`、`TestOperatorStillReachesTheCenterLocalFlowSurface`、`TestNodeActorStillReachesTheCenterLocalFlowSurface`、`TestUnauthenticatedCallerIsRefusedTheCenterLocalFlowSurface`（`httpapi/center_local_gate_test.go`） |
| Flow 会话/运行面（读 + cancel / diagnose / step / human reply / inspection / human-tasks / events|signals）：读面走本闸门，**执行**面另叠 `requireCenterExecution` | `/v1/flows/{id}/sessions*`、`/v1/flow-runs/*`、`/v1/flow-inspection`、`/v1/human-tasks` | 同上（层叠） | 同上表（`centerLocalFlowCalls` 是表驱动的，新增一条路由只加一行） |
| FlowSession 事件流（WebSocket，可走 `?token=`） | `GET /v1/flows/{id}/sessions/{sessionID}/ws` | `voiceQueryTokenAuth(requireCenterLocalData(protected))`——**闸门必须在 query-token 解析之内**，否则合法 iframe/WS 会拿到 401（它本来没有 header 可带） | 同上 |

**与「加一列 tenant_id」的区别**（这是本节的要点，容易被后来者改回去）：

1. **正确性**：flow 店现在是全局单目录，加列就得连同**每一处读写**都带谓词。
   这次漏掉整个 flow 面，正是这种「靠记忆」的代价；面还有很多（notes/memory/taskboard/skills/…）。
2. **必要性**：租户自己的 flow 与 run 跑在**租户自己的节点**上，中心一个字节都没有（§6）；
   给中心这张表加 tenant_id 是在为一个不存在的东西建隔离，还会掩盖真正的修法。
3. **切换前提**：若将来确实要做「注册即用 SaaS」，正确形态是 **instance-per-tenant**
   （每租户一进程/容器，隔离从「代码正确性」变成「部署边界」），而不是在单进程里续堆 tenant_id。

**尚未收口的同类面**（同形：数据在本实例、路由仅 `protected`）：notes、memory、taskboard、
skills/agent-templates/node-library、automation（cron/heartbeat，连 `CreatedBy` 都硬编码 `"web"`）、
config/providers 等。纳入时应**逐个确认是否真是中心本地面**（例如 memory 的 scope 已是 session/personal/org，
属于另一套隔离语义），不要成批包一层了事。

## 6. 已知缺口（本文不含「已解决」的部分）

| 缺口 | 现状 |
|---|---|
| **`nk_` 已改 per-node（P1+P2）** | ✅ 已实现（2026-10-28）：签发 per-node、解析先查 per-node 再退回全局（双认）、节点自助换发（CLI + 401 自愈）。主体首次带 `(NodeID, TenantID)` ⇒ **多租户下**节点只能触达自己租户节点。**但仍剩一处**：单租户（节点无 tenant）下节点行动者依旧放行，故「403 vs 404 探测 id 是否存在」在该形态下仍存在；彻底消除需可达性策略，本期不做 → 设计 §1.3 / §9 R2 |
| 手表转发面（`withForwardToNode`） | 经面 4 的同一个 proxy handler 转发（`watchRelay.Set(bridgeProxy)`），因此继承面 4 的闸门；手表侧测试覆盖在 `httpapi/routes_watch_proxy*_test.go`，**未**像面 1–6 那样逐条断言跨租户 403 |

## 7. 维护约定

### 7.1 新增「按 id 访问某节点」的入口时

1. 走现有 seam（HTTP 走 `authorizeNodeAccess` / `ProxyHandler.AuthorizeNode`；进程内其它面走
   `newNodeAccessAuthorizer`），**不要**自己写一遍策略——面 6 就是这么漏的。
2. 在本文件 §2 增加一行，并在§2 的测试列给出**跨租户 403** 的用例（只有「正常路径能跑通」不算）。
3. 若该面有副作用（建监听、开连接、写文件），断言**副作用未发生**，而不只是响应码。

### 7.2 新增「中心本地面」路由时（§5.1）

1. 先问一句：**这块数据在哪个实例上？** 在本实例上且不带租户维度 ⇒ 用
   `requireCenterLocalData`，不要用裸 `protected`（“任意登录用户”不是“操作员”）。
2. 只有动作是**启动/唤醒一次执行**时才额外叠 `requireCenterExecution`；`requireCenterLocalData`
   不能替代它（两者管不同的事：谁能看这块数据 vs 谁能在中心跑 agent）。
3. WebSocket / iframe 入口要留意闸门与 query-token 解析的**嵌套顺序**（见 §5.1 表第三行）。
4. 把新路由加进 `centerLocalFlowCalls` 那样的**表驱动**用例，并至少断言：租户 403、操作员非 403、
   节点行动者非 403、未认证 401。
5. **不要**因为它“不隔离”就顺手给它加 tenant_id 列——先回看 §5.1 的三条区别。
