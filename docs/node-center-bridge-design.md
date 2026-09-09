# 节点中心桥（Node Center Bridge）设计

> 状态：Implemented（2026-09-07，按 D1-D4 建议执行）
> 日期：2026-09-07
> 相关代码：`cmd/godex/main.go`（serve 装配）、`internal/runtime/httpapi/`（control 路由/proxy）、
> `internal/services/relay/`（hub/agent/proxy/trust）、`internal/app/node.go`（CLI node exec/forward 跳板）、
> `ui/web/src/lib/apiClient.ts`（nodeProxyPath 黑名单）、`ui/web/src/features/nodes/`（节点板块 UI）、
> `internal/core/config/`（ControlConfig + schema）、`docs/desktop-shell.md`、`mobile/README.md`

---

## 1. 需求（已澄清确认）

- **Q1**：本地端 = 内网节点 A（桌面端/Android 自托管的本地 godex，已 join 中心 S）。A 经中心 S 远程操作另一个内网节点 B。✅
- **Q2**：聊天 / 终端 / 文件 / 任务看板 / 业务智能体 等远程操作**全部**经桥接支持。✅
- **Q3**：本地「节点」板块列表 = **合并显示**（本地自身节点 + 中心上所有节点）。✅

目标拓扑：

```
桌面端/Android（本地 godex = 内网节点 A）
  │  本地 Web UI（节点板块 / 聊天 / 终端 / 文件）
  ▼
本地 httpapi  /control/nodes/{B}/proxy/{path}...   ← 前端 nodeProxyPath 黑名单机制自动生成
  │  （新增：中心桥转发层）
  ▼  HTTP + Bearer <center_token>
中心 godex S（公网，Hub）
  │  /control/nodes/{B}/proxy/{path}...   ← 既有 ProxyHandler（relayAuthorize + SSE 流式）
  ▼  relay channel（WSS，B 出站连 S）
内网节点 B（本地 httpapi，relay trust 头认证）
```

## 2. 现状关键事实（代码实证）

1. **前端黑名单机制对任意 godex 生效**：`apiClient.ts nodeProxyPath()` 在设置了 nodeID 后，把所有非 `/meta`、`/control`、`/push`、`/relay` 的 API 请求改写为 `/api/control/nodes/{id}/proxy/...`（`ui/web/src/lib/apiClient.ts` L44-59）。**前端与「本地 or 中心」无关，零改动**即可让本地 UI 请求落到本地后端。
2. **本地 serve 也装配了 registry + hub + proxy**（`cmd/godex/main.go` L295-414）：本地 ProxyHandler 只对 self node 本地直连（`proxy.SetLocalHandler(selfNode.ID, apiHandler)`），对其它节点 `hub.ForwardStream` 返回 ErrNodeOffline → 503。
3. **中心 proxy 认证 = 中心 web token**：`relayAuthorize(cfg)` 校验 `Authorization: Bearer <web.token>`（`main.go` L710-722）。节点凭证（ck_）不能用于调用中心 proxy（中心只存 CredentialHash，无法重算节点签名）。
4. **CLI 跳板模式已存在**：`godex node exec --node B --center <S> --token <中心 web token>` 直接 POST 中心 `/api/control/nodes/{B}/proxy/v1/exec`（`internal/app/node.go` L155-249）。→ **本地端要做的就是把这套 CLI 跳板能力搬进本地 httpapi 的 proxy 层**，让 Web UI 复用。
5. **节点侧 B 认证**：relay agent 把中心转来的请求注入 `X-Godex-Relay-Trusted`（B 自己 credential 签名）后交给本地 httpapi，B 的 protected 路由经 `relayTrustChecker` 放行（`agent.go` L242、`httpapi.go` L361-390）。→ B 侧**无需任何改动**。
6. **overview**：本地 `GET /control/nodes/{id}/overview` 由 `registryWithOverview`（本地 EventStore）提供（`routes_control.go` L141-162）；中心节点在本地无观测数据，需转发中心同名端点。

## 3. 设计

### 3.1 新增配置：`control.center_token`

本地 A 调用中心 proxy/节点列表需要中心 web token。

- `internal/core/config/config.go` ControlConfig 增加 `CenterToken string`。
- `internal/core/config/schema.go` 增加条目：
  `{Path: "control.center_token", Label: "Center Token", Description: "Center web token used to reach other nodes through the center bridge.", Type: "string", Secret: true, Env: "GODEX_CONTROL_CENTER_TOKEN"}`
- `godex node join` 已支持 `--token`（现仅用于 llm-proxy auto），**顺带持久化**到 `control.center_token`（Secret 写 .env 或 yaml，与 credential 一致策略）。

### 3.2 新增中心桥组件（本地侧）

新文件 `internal/runtime/httpapi/centerbridge.go`（或 `internal/services/centerbridge/`，见 §4 决策）：

```go
type CenterBridge struct {
    CenterURL  string          // control.center_url（无 /api 后缀）
    Token      string          // control.center_token
    HTTPClient *http.Client
}

// apiURL 组装 {center}/api{path}
func (b *CenterBridge) apiURL(path string) string

// ListNodes 拉中心 GET /api/control/nodes（Bearer center_token），失败返回错误
func (b *CenterBridge) ListNodes(ctx) ([]noderegistry.NodeView, error)

// GetOverview 拉中心 GET /api/control/nodes/{id}/overview
func (b *CenterBridge) GetOverview(ctx, id) (map[string]any, error)

// ServeProxy 把本地 /control/nodes/{B}/proxy/{path} 转发到中心同名端点：
//   替换 Authorization → Bearer center_token；透传 method/body/query/Content-Type；
//   SSE（text/event-stream）流式透传（httputil.ReverseProxy 或手动 Flush 循环）。
func (b *CenterBridge) ServeProxy(w, r)
```

转发要点（对齐中心 `relay.ProxyHandler` 语义）：
- **SSE 必须流式**：`Accept: text/event-stream` 时不能用默认 60s 超时截断，逐 chunk Flush。
- 透传 `Content-Type` 与 body；丢弃客户端原始 `Authorization`（换成 center_token）。
- 非 2xx 原样透传状态码与 body（如 B offline → 503、guard-remote 审批 → 403）。

### 3.3 本地装配改造（`cmd/godex/main.go`）

1. 判断「桥接模式」：`control.center_url` 非空且 ≠ 本节点 endpoint，且 `control.center_token` 非空 → 创建 `CenterBridge`。
2. **proxy 路由包装**：`root.Handle("/control/nodes/{id}/proxy/", ...)` 处，把 handler 换成：
   ```go
   var nodeProxy http.Handler = relay.NewProxyHandler(...) // 既有：本地节点/self 直连
   if bridge != nil {
       nodeProxy = &bridgeProxyHandler{local: nodeProxy, bridge: bridge, isLocalNode: func(id) bool {...}}
   }
   ```
   `bridgeProxyHandler.ServeHTTP`：解析 `{id}`，若 id 是本地节点（self 或本地 hub 在线节点）→ 交给 `local`（既有直连/本地 relay 逻辑）；否则 → `bridge.ServeProxy` 转发中心。
3. **节点列表合并**：`registryWithOverview` 或新 wrapper 的 `List(ctx)` 改为：
   ```go
   local := registry.List(ctx)
   if bridge != nil {
       remote, err := bridge.ListNodes(ctx)   // 带 15s 短 TTL 缓存，中心不可达时降级仅本地
       return mergeNodes(local, remote)        // 去重（本地优先）；中心节点 Source="center"
   }
   return local
   ```
4. **overview 转发**：`registryWithOverview.Overview(id)` 对本地节点走本地 EventStore；对中心节点（本地 registry 无此 id 或 Source=center）→ `bridge.GetOverview(id)`。
5. 其余 control 路由（credential/delete/heartbeat/register）保持本地语义不变：本地端仍是「自己这个节点」，不代理中心的管理操作（中心管理操作在中心 UI 上做）。

### 3.4 前端（最小改动，可选）

- **必须**：无。黑名单机制 + 合并列表后，NodeDetailPage 的「打开聊天/终端/文件/任务看板/业务智能体」按钮直接可用（`node?.relay_status === "connected"` 需中心列表带 relay_status，中心 `/control/nodes` 已返回）。
- **可选增强**：`NodesPage` 列表为 `source === "center"` 的节点加 Tag 标识「经中心」，提升可辨识度（一个小改动，不影响主流程）。

### 3.5 认证与安全

| 环节 | 机制 |
|---|---|
| 本地 UI → 本地后端 | 本地 web token（既有，桌面壳自动注入） |
| 本地后端 → 中心 | `Authorization: Bearer <control.center_token>`（新增 Secret 配置） |
| 中心 → 节点 B | 既有 relay channel + B 的 credential（B 侧 trust 头放行），B 的 guarded-remote 审批不绕过 |

### 3.6 不在范围内（YAGNI）

- 中心侧零改动（proxy/registry/relay 全复用）。
- 不做「中心管理操作经本地代理」（delete/credential 等仍在中心 UI 操作）。

### 3.7 后续：A 的 Web UI 配置经中心转发隧道（ForwardServer 中心桥回退）✅ Implemented

背景：用户确认「先 A 后 B」——因为 B 端不一定能执行 CLI（如 Android），A 端也不一定能执行 CLI（如 Pod 容器），所以必须在 A 的 Web UI 上直接配置到 B 的隧道。

实现（`internal/services/relay/forward_server.go` + `cmd/godex/main.go` + 前端）：

1. `relay.ForwardWSURL` 从 `internal/app/node.go` 提升到 relay 包导出（CLI 与中心桥共用）。
2. `ForwardServer.SetCenterBridge(centerURL, token)`：A 的 serve 装配时若配了 center bridge 则注入（`main.go`）。
3. `dialStream(ctx, nodeID, target)`：目标节点在本地 hub 在线 → `hub.OpenTCPStream`；否则有中心桥 → `dialCenterForward`（`DialForward` 连中心 `/api/control/nodes/{id}/forward`，CLI 同款路径）。
4. `forwardEntry` 的 acceptLoop/bridge/Check 全部改走 `dialStream`（不再直接持 hub）；`ForwardStatus` 加 `via_center` 字段。
5. `Check` Leg2 区分 localOnline / viaCenter / offline；Leg3 用 dialStream 探测。
6. 前端 `ForwardTunnelsCard` 对 `via_center` 隧道显示「经中心」Tag（复用 `nodes.sourceCenter` i18n）。

效果：A 的 Web UI 上打开 B 的详情页 → 转发隧道卡片 → 新增转发（本地端口 + B 内网 target）→ **A 进程本地监听，经中心 WS 中转**，与 CLI `godex node forward` 完全等价；隧道状态标记「经中心」；连通性检测/删除/持久化复用既有。

### 3.8 后续：B 方案（UI 生成 CLI 命令，待做）

在 B 的详情页（或 A 的转发卡片）提供「复制 CLI 命令」按钮，生成：

```bash
godex node forward --node B --local <port> --target <host:port> --center <A 的中心> --token <中心 web token>
```

作为 Web UI 配置的补充（适合 A 有终端可用、想要一次性隧道的场景）。

### 3.9 节点侧「接入中心」自动注册 + 热生效 ✅ Implemented（2026-09-09）

背景（用户确认）：Q1=自动注册、Q2=节点板块卡片 + 迁移 forward_allow、Q3=保存后热生效；保留中心侧 `JoinNodeCard`（生成接入命令+一键复制）不变。

**A. 自动注册端点 `POST /control/self/join`**（`internal/runtime/httpapi/routes_self_join.go`）：
- 请求 `{center_url, token(中心web token), node_id?, name?, trust_level?}`；node_id 缺省自动生成。
- 后端调中心 `register` + `issue credential`（带中心 web token）→ 写 `.env`（GODEX_CONTROL_CREDENTIAL / GODEX_CONTROL_CENTER_TOKEN）+ yaml（center_url/node_id/trust_level/node_name）+ 同步 node.json。
- 不依赖本地 CLI，Android / 容器环境在 Web UI 填两字段即可接入。

**B. 节点板块「接入中心」卡片**（`ui/web/src/features/nodes/JoinCenterCard.tsx`）：
- 表单：中心地址 + 中心 web token +（可选）node_id/name/trust_level；提交即自动注册。
- 显示当前 join 状态（center_url / node_id / trust_level）。
- 迁移 `forward_allow`（原 Settings > Control Plane）到该卡片，设置页 `API_HIDDEN_PATHS` 隐藏 `control.forward_allow` 避免双入口；其余 Control Plane 字段保留在设置页。

**C. 热生效**（`cmd/godex/control_runtime.go` + `main.go` 装配）：
- 新增 `controlRuntime` 控制器：持有 serve ctx + 当前 heartbeat/agent/observer/bridge 引用。
- `CenterBridge` 端点改为 atomic（`SetEndpoint`），`ForwardServer` 已有 `SetCenterBridge`，agent 已有 `SetForwardAllow`。
- `manager.SetApplier` 里调用 `controlRuntime.Reconcile(old,new)`：对比 center_url/credential/center_token/node_id/name/trust_level/forward_allow 差异 → 停旧 heartbeat/agent/observer、按新配置启新、更新 bridge 端点与 forward server。
- 首次接入（空→配）、换中心、退出中心（清空 center_url → 停 agent/heartbeat）同一套差异逻辑覆盖，保存即生效无需重启。

验证：新增 `routes_self_join_test.go`（端到端 fake center 注册+凭证+写配置、必填校验）；`go build ./...`、httpapi/relay/config/cmd/app 测试全绿；前端 `tsc -b` + `vite build` 通过。
- 不做「本地未 join 中心时的降级 UI 引导」之外的东西：未配置 center_token 时列表仅本地节点，proxy 对非本地节点返回 503 提示。

## 4. 待确认决策点

| # | 决策 | 我的建议 | 备选 |
|---|---|---|---|
| D1 | 桥接组件放哪 | `internal/runtime/httpapi/` 内新文件（复用 httpapi 装配，最小侵入） | 独立 `services/centerbridge` 包 |
| D2 | 节点列表合并 TTL | 15s 短缓存（与前端轮询一致），中心不可达降级仅本地 | 每次实时拉 |
| D3 | 前端是否加「经中心」Tag | 加（一行改动，提升辨识度） | 不加，纯后端 |
| D4 | join 命令持久化 center_token | 是（--token 顺带写入） | 仅 yaml 手配 |

## 5. 验证方案（可验收）

1. **单测**（Go）：
   - CenterBridge URL 组装、Authorization 替换、SSE 流式转发（httptest + 假中心）。
   - 合并逻辑：去重/本地优先/Source 标记/TTL 缓存/中心不可达降级。
   - bridgeProxyHandler 路由：本地节点走 local、中心节点走 bridge。
2. **手动 e2e**（三节点：本地 A + 中心 S + 内网 B，B 已 join S）：
   - A 配置 `control.center_url + control.credential + control.center_token` 后重启。
   - A 的节点板块出现 B（Source=center，relay_status=connected）。
   - A 上「打开聊天」→ 输入消息 → B 的 session 收到并回复（SSE 流式经双层代理）。
   - A 上「打开终端/文件」→ 能创建 PTY / 列出 B 的 workspace 文件。
   - 关闭 B 后 A 上 B 显示 offline，打开聊天按钮禁用。
3. **前端**：`tsc -b` + `vite build` 通过；新增 i18n key。

## 6. 实施步骤（确认后执行）

1. config.go / schema.go 加 `control.center_token`；node.go join 持久化。
2. 新增 centerbridge.go（ListNodes/GetOverview/ServeProxy + 合并 + 缓存）。
3. main.go 装配：bridgeProxyHandler 包装 + List/Overview 合并注入。
4. （可选）NodesPage source Tag + i18n。
5. 单测 + tsc/build 验证；三节点手动 e2e。
