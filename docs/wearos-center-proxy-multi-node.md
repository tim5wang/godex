# 手表端「中心代理」多节点遥控 — 调研记录 + 实施设计

日期：2026-10-25
目标：watch 端能选「设备」列表里任意在线设备遥控，机制与 Android/PC 端访问远程节点**一致**（复用中心代理，不自创一套）。

## 一、现状核实（代码为证）

### 手表端「选择设备」目前是假的

| 事实 | 证据 |
|---|---|
| 选择只改本地状态 | `DevicesScreen.onClick → onSelect(node.id)` → `vm.setTargetDevice()` → `store.setTargetDevice()` + `_state.copy(targetDeviceId=…)` |
| 遥控请求无设备维度 | `WatchApi` 的 `sendCommand/stopSession/approve/deny/sessionDetail` 只带 `sessionId`；`grep addQueryParameter` 无命中 |
| 选择被刷新覆盖 | `WatchViewModel.kt:301` `targetDeviceId = overview.devices.firstOrNull()?.id ?: …` → 每次 refresh 强制回到第一个设备 |
| 持久化写了没人读 | `WatchStore.targetDeviceFlow` 全仓库引用次数 = 1（仅定义处），重启即丢 |
| `targetDeviceId` 真实用途 | 只有 `ClockFace` 显示设备名 + `DevicesScreen` 高亮，两处都是纯 UI |

### 后端 `/watch/*` 无跨节点参数

路由清单（`grep '"GET/POST /watch/…"'`）：`/watch/sessions`、`/watch/command`、`/watch/sessions/{id}/stop`、`/watch/approvals/…`、`/watch/overview`、`/watch/cross-device`、`/watch/nodes` 等，**均无** `node_id` / `device_id` 查询参数。

### 现有跨节点能力（可复用，不要另造）

1. **中心代理路径**：`/control/nodes/{nodeID}/proxy/{path...}`
   - 中心侧：`internal/services/relay/proxy.go` `ProxyHandler.ServeHTTP`
     - `strings.Cut(rest, "/proxy/")` 取出 nodeID 与 after
     - 本地 self 节点 → in-process 克隆请求，剥掉 `proxy/` 前缀后交 `localHandler`
     - `guarded-remote` 信任级 + 写操作 → 需 `trustApprovedHeader`，否则 403
     - 其余 → `ForwardRequest{Method, Path:"/"+after, …}` 经 WSS 中继到目标节点
   - 节点侧包装：`internal/runtime/httpapi/centerbridge.go` `bridgeProxyHandler`
     - `isLocalNode(nodeID)` 为真 → 本地代理；否则转发给配置的中心
     - 中心未配置 → 503 `node offline: not reachable locally and no center bridge configured`
2. **Web UI 的做法**（本次要对齐的「一致」标准）：`ui/web/src/lib/apiClient.ts` `nodeProxyPath()`
   - 有 `nodeID` 时把任意路径重写为 `/control/nodes/{nodeID}/proxy{p}`
   - **黑名单**保留中心自身控制面：`/meta`、`/control*`、`/push*`、`/relay`
3. **节点→中心的凭据调用**：`internal/platform/tooling/relay_client.go`
   - 形如 `POST {CenterURL}/api/control/nodes/{NodeID}/proxy/control/sandbox/exec`，`Bearer nk_…`
   - 配置项在 `ControlSection`：`center_url`、`center_token`、`node_id`、`default_node`、`trust_level`、`node_proxy_token`
   - 同类调用还有 `internal/app/node.go:302` `execURL()`
4. **目标节点的放行机制**：`withBearerAuthProvider(token, relayTrust...)`
   - 中心转发时注入「用该节点自身凭据签名」的 relay trust 头 → 目标节点跳过 web token 校验
   - 注释明确：这就是让中心把 chat/terminal/files 转发到「自己配了 web token 的节点」的机制

## 二、真正的难点（之前已向用户点明）

手表凭证（`watchauth` 的 `wch_`/`wk_`）是**某个节点签发并存在该节点 registry**（`stateDir/watch/devices.json`）里的，scope 为 `watch:read|watch:command|watch:approve`，由 `withWatchScope(auth, scope)` 校验。

而 relay trust 只绕过 **web token**（`withBearerAuthProvider`），**不绕过** `withWatchScope`。所以「手表只配对一次、遥控任意节点」不能靠直接把 wch_ 头透传给目标节点（目标节点不认识这个 token）。

两条可走的路：

- **A. 中心代理 + 节点级内部凭据（采用）**：转发方（手表配对的那个节点）用自己的 `center_token` 走中心代理；目标节点用一个「仅中心 relay trust 可用」的内部 watch 代理入口接受该请求，并显式携带「原始手表已授权的 scope」，由目标节点按 scope 收紧。
- B. 手表直连每个目标节点（各自配对）——与用户「手表是遥控器、只配对一次」的定位冲突，且要为每台设备输码，放弃。

### 安全要点（A 的成本所在）

- 内部入口必须**同时**满足：relay trust 头有效（中心/节点自身凭据签名）**且**请求带 watch 代理标记头。
- scope 必须**取交集**：`min(手表 token 的 scope, 内部入口允许的 scope)`，不能因为走内部通道就放大权限（尤其 `watch:approve`）。
- 审计：沿用 `relay proxy: caller=… target=…` 的日志风格，记录「哪个手表 → 经哪个节点 → 遥控哪个节点」。

## 三、实施切分（已实现）

> 状态（2026-10-25）：Step 1–3 已实现，单测/集成测试 + 编译通过；真机双节点验证待做。

### Step 1 — 手表端「选得住」

1. `bootstrap()` 读 `targetDeviceFlow` 恢复选择（原「引用次数=1」的死代码接上）。
2. `reconcileTargetDevice()`：选择仍在列表就保留；**默认回落本机 `local`，而不是 `devices.first()`**
   —— 后者会把首台设备静默当目标，用户配对后第一条指令可能发到另一台机器。
3. `WatchApi` 的 `sendCommand/stopSession/approve/deny/sessionDetail/sessionHistory/sessions/approvals`
   增加 `nodeId`（空=本机；旧节点忽略参数仍可用）。
4. 刷新时先定目标设备、再按设备取会话与审批；远端读取失败回落本地列表而不是白屏。
5. `normalizeSelf()`：后端新增 `self` 标记 → 统一映射为 `local`。
   设备列表带真实 node id，而会话列表把本机标为 `local`；不归一化则
   **选中「自己」会被当成远端**，经中心绕回本机，无中心的节点上直接失败。
6. 切换设备时清空当前会话并 `refresh()` 重新拉取（不是用旧列表过滤）。

### Step 2 — 后端接受 `node_id` 并分流（`routes_watch_proxy.go`）

- `watchNodeTarget()`：`""` / `"local"` → 本地；其它 → 远端。
- `watchProxyForwarder.forward()`：经 `CenterBridge.ServeProxy` 发往
  `/control/nodes/{id}/proxy{path}`；**剥掉 `node_id`** 防 B→B 无限转发；
  丢掉手表 `wch_` 凭据（目标不认），改带 scopes 头。
- `withForwardToNode()` 包在 `command` / `sessions/{id}/stop` / `approvals/{…}/approve|deny` /
  `sessions` / `sessions/{id}` / `sessions/{id}/messages` / `approvals` 上。
- 无中心配置 → 503 并点名目标节点，**绝不回落本地执行**（避免指令跑错机器）。

### Step 3 — 目标节点侧内部入口

- `withWatchScopeOrRelay(auth, scope, trust)`：手表凭证 **或** relay 到达且携带所需 scope。
- `relayWatchScopes()`：区分「非中继」(→401) 与「中继但无权」(→403)，不隐式放大。
- `watchGrantedScopeKey{}`：只转发本次已验证的那一个 scope（最小权限），
  而不是设备的全部授权。

### 中继头部链路（本方案的关键修复）

原先两处只转发 `Content-Type`/`Authorization`，scopes 头会在半路被丢弃：
- `relay.ProxyHandler`（中心→节点）：新增 allowlist `relayForwardedHeaders`。
- `CenterBridge.ServeProxy`（A→中心）：显式转发 `X-Godex-Watch-Scopes`。

### 验证

- `internal/services/relay/proxy_watch_scopes_test.go`：scopes 跨中继；伪造 trust 头被丢弃。
- `internal/runtime/httpapi/routes_watch_proxy_test.go`：目标归一化、scope 交集、503、剥 node_id。
- `internal/runtime/httpapi/routes_watch_proxy_relay_test.go`：**远端请求不落到本地执行**、本地路径零变化、最小权限。
- 待做：真机双节点（手机 + Mac）选远端发指令 / 看会话 / 审批。

## 四、鉴权事实（已核实，决定方案可行性）

| 检查器 | 作用 | 能否绕 `withWatchScope` |
|---|---|---|
| `withWatchScope(auth, scope)` | 只认 `wch_` 凭证 + `HasScope` | 基准 |
| `withBearerAuthProvider(token, relayTrust…)` | relay trust 可跳过 **web token** | 不适用（watch 不走 web token） |
| `relayTrustChecker` → `relay.ValidateRelayTrust(header, nodeID, credential)` | 节点用**自己**的 `control.credential` 验 HMAC | 可复用为内部通道 |

关键结论：**relay trust 头是目标节点自己的 relay agent 注入的**（中心经 WSS 转发后，B 的 agent 用 B 的 credential 签名）。所以 B 的 httpapi 可以把「带有效 relay trust 头」的请求判定为「经认证中继到达」，这与 `routes_sandbox.go` 已有的做法一致（注释原文：token OR the relay trust header (which the center injects when forwarding)）。

因此中心代理方案成立：
1. 节点 A（手表配对的那个）用自己 `center_token`（nk_）调 `{center}/control/nodes/{B}/proxy/watch/...`
2. 中心校验后经 WSS 转发到 B
3. B 的 relay agent 注入 relay trust 头，B 的 watch 路由据此放行
4. **scope 必须显式传递并取交集**：A 在头里带 `X-Godex-Watch-Scopes`（A 已从 wch_ token 校验出的 scope），B 只接受其中列出的 scope；头缺失一律拒绝（不隐式放大到全量）

## 四、待确认

- 目标节点内部入口的放行，是否能直接复用 `withBearerAuthProvider` 的 relayTrust 检查器（需确认 relay trust 头在 watch 路由处可获取，且不需要 wch_）。
- `guarded-remote` 信任级下写操作需 `trustApprovedHeader`：手表发指令是否要额外确认，需产品决策（倾向：手表端 `watch:command` 已是一次显式点按，视为批准，但要在 UI 明示）。
