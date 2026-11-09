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

### 5.1 中心本地面（非节点面）

上面 §2–§5 都在回答「某个**到节点**的面能否隔离」。还有一类面根本不到节点：数据存在**本实例**上。
「多节点平台多租户」形态下它们**不属于任何租户**——中心的 flow 店、notes 店、durable memory、
采集日志、cron 任务，都是**平台操作员自己这台机器的状态**（设计 §6：中心不留租户内容）。
所以这类面的正确边界不是「按租户作用域」，而是「**租户根本到不了**」。

闸门：`requireCenterLocalData`（`httpapi/center_local_gate.go`）——平台管理员通过、节点行动者通过
（远程节点 Flows/Settings/Memory 页要能用自己的身份打到自己的数据，与 `requirePlatformAdminOrNodeActor`、
`authorizeConfigWrite` 同一条豁免）、其余主体 403、无 Principal 则 401（失败关闭）。

**接线方式很重要**：闸门加在 `httpapi.go` 的**接线处**（`centerLocal := requireCenterLocalData(protected)`），
按**面**传给整个 registrar，而不是在每个 registrar 里逐条路由包。一条线覆盖一个面，后来新增的路由
**不可能被忘记**——正是「逐路由」这个写法让 flow 面一次漏掉 19 条路由。

| 已收口的面 | 入口（代表） | 回归测试 |
|---|---|---|
| Flow 定义/会话/运行/巡检/人工任务 | `/v1/flows`、`/v1/flows/{id}/...`、`/v1/flow-runs/*`、`/v1/flow-inspection`、`/v1/human-tasks` | `centerLocalCalls()` 表驱动 |
| FlowSession 事件流（WebSocket） | `GET /v1/flows/{id}/sessions/{sessionID}/ws` | 同上（闸门经 `voiceQueryTokenAuth` 注入 Principal 后生效） |
| 知识面 | `/notes...`、`/memory...` | 同上 |
| 采集日志（本实例全量请求/响应正文） | `/llm-capture/*` | 同上 |
| 本实例的定时作业 | `/automation/cron/*`、`/automation/heartbeat*` | 同上 |
| 机器配置与能力清单 | `/agent-templates*`、`/acp/agents/*`、`/skills/catalog`、`/v1/node-library*`、`/v1/mcp/*`、`/providers*` | 同上 |
| 包/模型目录与仓库面 | `/models`、`/security/*`、`/packages*`、`/prompts`、`/commands` | 同上 |
| **中心自己的工作区**（文件 / git / 终端 / shell / 媒体 / 桌面事件） | `/files/*`、`/git/diff*`、`/v1/terminal/*`、`/control/sandbox/*`、`/v1/voice*`、`/v1/tts*`、`/browser/frames`、`/desktop/events` | `TestTenantUserIsRefusedTheNodeLocalSurfaces`、`TestOperatorStillReachesTheNodeLocalSurfaces`、`TestNodeActorStillReachesTheNodeLocalSurfaces` |
| **元 LLM 网关的用量与管理**（key/model/调用/会话/缓存） | `/usage/*`（**除** `/usage/relay`）、`/v1/biz/keys*` | `TestTenantUserIsRefusedTheUsageAndBizKeySurfaces`、`TestTenantCannotDestroyTheOperatorsBizKey`、`TestOperatorStillReachesTheUsageAndBizKeySurfaces`（`center_local_gate_usage_test.go`） |
| **实例自属面**（它自己的服务运行时 / 绑定了哪些渠道 bot） | `/runtime/service*`、`/channels` | `TestTenantUserIsRefusedTheInstanceWideSurfaces`、`TestOperatorStillReachesTheInstanceWideSurfaces`、`TestNodeActorStillReachesTheInstanceWideSurfaces`（同上文件） |
| **节点注册表「写」面**（入网机器 / 签发凭据 / 删节点 / 让本机加入某中心） | `POST /control/nodes/register`、`.../{id}/heartbeat`、`.../{id}/credential`、`DELETE /control/nodes/{id}`、`POST /control/node-proxy-token`、`POST /control/self/join` | `TestTenantUserIsRefusedTheNodeAdminWriteSurfaces`、`TestTenantCannotDestroyTheOperatorsNode`、`TestOperatorStillReachesTheNodeAdminWriteSurfaces`、`TestUnauthenticatedCallerIsRefusedTheNodeAdminWriteSurfaces`（`center_local_gate_node_admin_test.go`，表名 `nodeAdminWriteCalls()`） |

**三轮扫描的共同教训（值得单独记住）**：后两轮之所以首扫时“看不见”，是因为**夹具没接那个依赖**
（无 usage 库 / 无 registry）⇒ 路由**根本没注册** ⇒ 返回 404，而我把 404 读成了「没问题」。
**未注册路由的 404 不是关于闸门的证据**；要判闸门，必须先把依赖接上再测。

**节点面的读写分裂是刻意的**（别“顺手统一”）：注册表的**读**面（`GET /control/nodes`、`/{id}`、`/{id}/overview`）
**继续接受登录会话**（Web UI 的节点页用会话，租户靠 handler 内遮蔽），并由
`TestNodeProxyReadAcceptsSessionWithoutEscalation` 钉住；**写**面才需要操作员。
另外三处同样刻意不接：`POST /control/self/proxy-credential`（节点用自己的 `ck_` 换发，**不能**要求 web token，
否则把平台主密钥塞回每台机器）、`POST /control/join`（租户一次性 join token 入网，§3.5）、
以及 `godex node join`（CLI 直接打中心，不走本机路由，故闸门不影响它）。

回归测试（`httpapi/center_local_gate_test.go`）：`TestTenantUserIsRefusedTheCenterLocalSurface`（表驱动，
租户全部 403）、`TestOperatorStillReachesTheCenterLocalSurface`（操作员非 403）、
`TestNodeActorStillReachesTheCenterLocalFlowSurface`（节点行动者非 403）、
`TestUnauthenticatedCallerIsRefusedTheCenterLocalSurface`（未认证 401）。表名 `centerLocalCalls()`——
新增一条路由只加一行；表中**有意排除**的项都写了理由（如本夹具无 MCP manager 故 `/v1/mcp/*` 未注册）。

#### ⚠️ 2026-11-10 修正：一条被写错的前提，漏了整个「中心自己的工作区」

本节初版把 `/files`、`/git`、`/terminal`、`/control/sandbox`、`/desktop`、`/voice`、`/browser`
列在「**有意不加此闸门**」里，理由写的是「是**节点本地**数据，经 `/control/nodes/{id}/proxy` 到达，
由 **NodeAccess** 把关」。该理由对**代理路径**成立，但这些路由**同时也注册在中心自己的 mux 上**
（`registerFileRoutes(mux, protected, manager)` 等），而 `protected` 是接受登录会话的。

用**租户成员**的会话实测（修正前）：

| 请求 | 实际结果 |
|---|---|
| `POST /control/sandbox/exec {"command":"echo probe"}` | **200，且命令真的执行了** |
| `GET /files/list` | 200 |
| `GET /git/diff` | 200 |
| `GET /v1/voice/status` | 200 |
| `GET /desktop/events` | 200（SSE 被接受） |
| `DELETE /v1/terminal/{id}` | 404（404 是那个不存在的 id，说明 handler 跑到了） |

即：**任意租户的任意登录用户可读操作员的工作区，并在其中执行命令。**

教训的形状值得记住：**「这块数据在别处有闸门」是关于一条路径的断言**；一个同时挂在两条路径上的路由，
需要在**两条**上都检查。比这更早的同类错误是 flow 面（§5.1）——那次是「忘了」，这次是「以为别处管了」。

修正后（实测）：上述全部 **403**；闸门用 `requireCenterLocalData`（**不是** `requirePlatformAdmin`），
因为节点行动者必须继续放行，否则中心驱动远程节点的文件/shell 会断。

**`/preview` 是唯一仍不包此闸门的面**，且是刻意的：它自带只认 web token（**不认登录会话**）的鉴权，
租户在那里本来就拿 **401**（比 403 更严）；而它依赖的 `?token=` 路径是在该检查**之后**才装上 Principal 的，
外面再包一层闸门会把合法 iframe 打成 401。见 `TestTenantUserIsRefusedPreviewWithoutItsOwnCredential`。

**有意不加此闸门的面**（写在这里，免得后来者“顺手统一”反而打断能力）：

| 面 | 为何不用本闸门 |
|---|---|
| `/preview/static*`、`/preview/proxy/*` | 自带只认 web token 的鉴权（租户 401）；`?token=` 路径在检查后才装 Principal，外包闸门会打断合法 iframe |
| `/usage`（node 模型流量）与 `/usage/relay`（转发计量） | 一个属节点、一个是**已按租户作用域**的中心数据（§5 已覆盖）；一刀切会把租户自己的用量报表封死 |
| `/config` | **已收口**（2026-11-10）：见下。原先“按配置项闸”的说法只保护了受限**写**，读面与非受限写（`model`/`workspace_dir`）对任意登录用户开着 |
| `POST /control/self/proxy-credential` | 节点用自己已持有的 `ck_` 换发（§9 R2），**不能**要求 web token，否则把平台主密钥塞回每台机器 |
| `POST /control/join` | 租户一次性 join token 入网（§3.5），token 就是凭据 |

**`/config` 的收口，以及它之前为何被挡（2026-11-10）**：设计 §3.3 当初反对路由级闸门，
理由是「`/config` 在 UI 里是节点作用域，路由闸门会 403 掉远程节点自己的设置编辑」。
该前提仍成立，但**结论不再成立**：`requireCenterLocalData` **放行节点行动者**，
代理到远程节点的请求以 relay-trust 身份到达，改的是**它自己的**配置——正是远程 Settings 今天在做的事。
实测（修正前，租户成员）：`GET /config`、`/config/meta`、`/config/schema`、`/config/doctor` 均 200
（视图内密钥**已掩码**，故泄露的是机器布局：路径/workspace/model）；而 `PUT /config` 的
**非受限项**（`model`、`workspace_dir`）被接受——逐项规则只拒 schema 标了 `PlatformAdmin` 的那些。
现全部走 `requireCenterLocalData`，`authorizeConfigWrite` 作为第二层保留（它的标记也是 UI 禁用控件的依据）。
回归：`TestTenantUserIsRefusedTheConfigSurface`、`TestOperatorStillReachesTheConfigSurface`、
`TestNodeActorStillReachesTheConfigSurface`（表名 `configLocalCalls()`）。

### 5.2 由本次收口核出的独立漏洞：`/config/reveal` 明文密钥

发现于 2026-11-09 收口过程中：`POST /config/reveal` **明文回传**凭据（`web.token`、
`channels.feishu.app_secret`、搜索/媒体 API key），却只挂 `protected`——即**任意登录用户**（含租户成员）
可读出本实例自己的密钥，其中 `web.token` 就是授权操作员本人那个凭据。

修复：改用 `requirePlatformAdminOrNodeActor`（不是逐项闸门——这是**披露**不是设置编辑，逐项那套不适用；
需要的是同一条节点豁免，因为选中远程节点时 UI 会代理这个调用，而该节点必须能取自己的密钥）。
回归：`TestRevealSecretIsRefusedToTenantUser`（租户 403 且响应体不含值、操作员 200、未认证 401）。

**与「加一列 tenant_id」的区别**（本节要点，容易被后来者改回去）：

1. **正确性**：这些店现在都是全局单目录，加列就得连同**每一处读写**都带谓词。
   这次一次漏掉整个 flow 面（19 条路由）+ 一整组同类面，正是「靠记忆」的代价。
2. **必要性**：租户自己的 flow/notes/记忆跑在**租户自己的节点**上，中心一个字节都没有（§6）；
   给中心这些店加 tenant_id 是在为一个不存在的东西建隔离，还会掩盖真正的修法。
3. **切换前提**：若将来确实要做「注册即用 SaaS」，正确形态是 **instance-per-tenant**
   （每租户一进程/容器，隔离从「代码正确性」变成「部署边界」），而不是在单进程里续堆 tenant_id。

**memory 的语义已确认**（先前待决项）：HTTP 面经 `service.memoryManager()` → `memory.NewManager(cfg.MemoryDir)`，
拿到的是**未指定 scope** 的 manager（即 org/legacy 共享层）；会话内的 `NewScopedManager(..., scope.Session(id))`
只用在 agent 会话内部，不经 HTTP 暴露。所以 `scope=session/personal` 那套与「中心本地面」**不冲突**：
HTTP 面看到的本就是共享层，闸到操作员即可。

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
2. 优**传整个 registrar**（`registerXxxRoutes(mux, ..., centerLocal)`），不要逐条路由包——
   逐条包正是 flow 面一次漏 19 条的原因。
3. 只有动作是**启动/唤醒一次执行**时才额外叠 `requireCenterExecution`；`requireCenterLocalData`
   不能替代它（两者管不同的事：谁能看这块数据 vs 谁能在中心跑 agent）。
4. WebSocket / iframe 入口要留意闸门与 query-token 解析的**嵌套顺序**（见 §5.1）。
5. 把新路由加进 `centerLocalCalls()` 表，并至少断言：租户 403、操作员非 403、节点行动者非 403、未认证 401。
6. **不要**因为它“不隔离”就顺手给它加 tenant_id 列——先回看 §5.2 的三条区别。
7. 反过来，**不要**把节点本地 / 已按租户作用域 / 按配置项闸的面“顺手统一”进本闸门——
   见 §5.1 的「有意不加此闸门的面」表。
