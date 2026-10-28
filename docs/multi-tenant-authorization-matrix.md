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

## 6. 已知缺口（本文不含「已解决」的部分）

| 缺口 | 现状 |
|---|---|
| **`nk_` 仍为全局单凭据** | ⏳ 未做。持有者可**探测**任意节点是否存在（403 vs 404），但拿不到数据、也代理不进去（面 1–6 已闸）。改 per-node 会打断现存已加入节点（含本机 `mac`），需一次性重新下发 → 设计 §9 R2 |
| 手表转发面（`withForwardToNode`） | 经面 4 的同一个 proxy handler 转发（`watchRelay.Set(bridgeProxy)`），因此继承面 4 的闸门；手表侧测试覆盖在 `httpapi/routes_watch_proxy*_test.go`，**未**像面 1–6 那样逐条断言跨租户 403 |

## 7. 维护约定

新增任何「按 id 访问某节点」的入口时：

1. 走现有 seam（HTTP 走 `authorizeNodeAccess` / `ProxyHandler.AuthorizeNode`；进程内其它面走
   `newNodeAccessAuthorizer`），**不要**自己写一遍策略——面 6 就是这么漏的。
2. 在本文件 §2 增加一行，并在§2 的测试列给出**跨租户 403** 的用例（只有「正常路径能跑通」不算）。
3. 若该面有副作用（建监听、开连接、写文件），断言**副作用未发生**，而不只是响应码。
