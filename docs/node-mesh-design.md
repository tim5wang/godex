# GoDex 节点互联与远程开发设计（Node Mesh v2）

> 状态：Draft（待确认后启动开发）
> 日期：2026-08-05
> 相关代码：`internal/services/noderegistry/`、`internal/runtime/httpapi/httpapi.go`（control 路由）、`cmd/godex/main.go`（serve 装配）、`ui/web/src/features/nodes/`、`ui/web/src/app/appRegistry.tsx`
> 参考设计：`temp/orca/`（Orca：AI Orchestrator，桌面端 + 移动 companion + SSH worktree + relay 协议）

---

## 0. 已确认决策（2026-08-05）

| # | 决策 | 结论 |
|---|---|---|
| 1 | 节点凭证管理方式 | **中心生成 `ck_` 凭证 → 复制到节点配置**（简单优先，不做注册审批流） |
| 2 | 端口转发 | **需要支持**：内网节点经服务器跳板访问内网其它服务（relay TCP 转发，Phase 4） |
| 3 | 手机端 | **不做原生 App**：手机浏览器直接用已适配的 Web UI，后端地址即中心、由中心代理到目标节点；可选 PWA 增强（Phase 5）。**Web Push 已纳入 Phase 5；Android 本地 Agent 已立项（实验性）** |
| 4 | 节点 session 历史 | **中心不持久化**：只做在线观测，节点本地是唯一事实源 |

---

## 1. 现状梳理（As-Is）

### 1.1 「节点」app 当前是什么

当前「节点」= **只读 Control Plane Dashboard**，核心是轻量 Node Registry + 心跳，只有观测能力，没有控制能力。

**后端（Go）**

| 组件 | 位置 | 说明 |
|---|---|---|
| Node Registry | `internal/services/noderegistry/registry.go` | 内存 map + JSON 持久化（`<godex_home>/control/nodes.json`）；`Register/Heartbeat/List/Get/SeedConfigured`；离线判定 `offline_after_seconds`（默认 60s） |
| 本地心跳 | `internal/services/noderegistry/heartbeat.go` `LocalHeartbeat` | serve 启动时把自身注册进本地 registry，然后每 `heartbeat_seconds` 心跳一次 |
| 远端心跳 | `internal/services/noderegistry/heartbeat.go` `RemoteHeartbeat` | 配置了 `control.center_url` 时，POST 到中心 `/api/control/nodes/register` + `/api/control/nodes/{id}/heartbeat`，带 `Bearer <web_token>` |
| HTTP 路由 | `internal/runtime/httpapi/httpapi.go` L155-212 | `GET /control/nodes`、`GET /control/nodes/{id}`、`POST /control/nodes/register`、`POST /control/nodes/{id}/heartbeat`，全部走 `protected`（web token） |
| 装配 | `cmd/godex/main.go` L225-271 | serve 时：建 registry → `SeedConfigured` 手动节点 → 注册自身 → LocalHeartbeat → 若配置 center_url 则加 RemoteHeartbeat |
| 节点身份 | `registry.go` `EnsureNodeID` / `SelfNodeWithVersion` | 节点 ID 持久化在 `<state_dir>/node.json`（`node_<hex>`），name 默认取 hostname，endpoint 由 `--addr` 推导 |

**前端（React）**

- `ui/web/src/features/nodes/NodesPage.tsx`：表格展示 id/name/status/workspace/endpoint/version/last_seen/capabilities，15s 轮询刷新。
- `ui/web/src/app/appRegistry.tsx`：`nodes` 是一级 app，navPath `/nodes`。

**配置**（`internal/core/config/config.go` `ControlConfig` + `schema.go`）

```yaml
control:
  node_name: local-project-a     # 本节点名字
  center_url: https://godex.claw.carc.top  # 中心服务地址（可选，配置后自动注册+心跳）
  heartbeat_seconds: 15
  offline_after_seconds: 60
  nodes:                          # 手动声明的节点（可选）
    - id: cloud-prod
      name: Cloud Prod
      endpoint: https://godex.example.com
      workspace_dir: /opt/godex
      ...
```

### 1.2 现状能力边界（gap 分析）

| 能力 | 现状 | 缺口 |
|---|---|---|
| 节点发现/注册 | ✅ 自动（心跳注册）+ 手动（配置 seed） | 无信任握手，只有共享 web token |
| 在线状态 | ✅ 心跳 + 超时离线 | 无 ping/rtt、无丢包重连、无最后健康详情 |
| 只读观测 | ✅ Nodes Dashboard（表格） | 无节点详情页、无运行中 session/job 聚合 |
| **双向通信** | ❌ 只有节点→中心单向 POST | **核心缺口：中心无法向节点发起任何请求** |
| 远程会话/chat | ❌ | 无法在中心 Web 上对远端节点发起/加入 session |
| 远程终端 | ❌（本地 PTY 已有，`routes_terminal.go` 支持 local/ssh/docker 三种 backend） | 无法把 PTY 通道经中心转发到内网节点 |
| 远程文件/编辑 | ❌ | 无法浏览/编辑远端节点 workspace |
| 跨节点审批 | ❌ | 无法在中心聚合/处理远端节点审批 |
| 远程任务派发 | ❌ | 无法从中心向节点派发 longtask/subagent |
| 安全/信任模型 | ⚠️ 共享 web token，无 per-node 凭证 | 需要 per-node 凭证 + 信任级别 + 审计 |
| 服务器节点自身 | ⚠️ 服务器也注册为普通节点 | 需要同等纳入管理（用户明确要求） |

### 1.3 关键结论

- 现在的「节点」app 只完成了 **registry + observability** 这第一步。
- 产品方向（内网设备 godex ↔ 服务器 godex 互联 → 远程控制/远程编程）需要的是一个 **双向、可路由、可流式传输的控制通道**，而不是单向心跳。
- 内网设备**没有公网入站可达性**，因此连接模型必须是 **节点主动出站连中心（outbound）**，中心通过已建立的通道反向复用（tunnel / relay），类似 Orca 的移动 companion → 桌面端 WebSocket RPC。

---

## 2. 产品目标与设计原则

### 2.1 目标

1. **互联**：任意内网设备上运行的 godex，通过一台公网 godex（中心）互联；服务器节点自身也作为普通节点纳入同一张网。
2. **远程控制**：通过服务器 Web UI（或本地 CLI/TUI 以服务器为跳板），对任意在线节点执行：
   - 发起/加入 chat session、下发 prompt
   - 远程终端（PTY）
   - 文件浏览与编辑
   - 审批处理
   - 任务派发/取消（longtask、subagent）
3. **远程编程**：在服务器网页端获得接近本地的工作台体验（Chat + Terminal + Files + Git + 观测），远程完成开发闭环。
4. **安全默认**：per-node 凭证、信任分级、节点本地安全策略不被绕过（延续 `docs/capability-enhancement-v1.md` 的 control plane 原则）。

### 2.2 设计原则

1. **Outbound-first 连接模型**：节点始终主动连中心；中心不反向 dial 节点。NAT/防火墙天然穿透。
2. **节点自治**：每个节点仍是完整 godex runtime，保留本地 workspace、配置、权限、审批；中心是协调面不是替代者。
3. **复用而非重造**：
   - 复用已有 HTTP API（sessions/terminal/files/git 路由）作为**节点本地能力面**；
   - 新增长连接 relay 作为**传输面**，把中心的请求经通道转发到节点的本地 API。
   - 直接复用本地 PTY（`routes_terminal.go`）、session runtime、审批框架。
4. **渐进式**：分阶段落地，每阶段可独立上线、可回滚；Phase 1 只加传输层不动业务。
5. **审计**：所有跨节点操作记录来源（中心用户/CLI）与目标节点，进入 audit log。

---

## 3. 目标架构（To-Be）

### 3.1 总体拓扑

```
┌─────────────┐   HTTPS/WSS    ┌──────────────────────────────┐
│  浏览器/CLI  │ ─────────────▶│        中心 godex（公网）      │
│  (用户入口)  │                │  godex.claw.carc.top          │
└─────────────┘                │  - Web UI（含 Nodes app）     │
                               │  - Node Registry              │
┌─────────────┐   WSS 出站     │  - Relay Hub（新）             │
│ 内网节点 A   │ ─────────────▶│  - 自身也注册为一个节点         │
│ (godex)     │                └──────────────────────────────┘
└─────────────┘                        ▲
┌─────────────┐   WSS 出站             │
│ 内网节点 B   │ ───────────────────────┘
│ (godex)     │   （经公网服务器中转，穿透 NAT）
└─────────────┘
```

- 中心 = Hub：维护每个在线节点的长连接通道，提供「按节点路由」的能力。
- 节点 = Spoke：`control.center_url` 指向中心；启动即建立 relay 连接；不依赖入站端口。
- 服务器自身节点：同样走 relay（连自己的 Hub），或直接本地调用（不走网络），两种都支持。

### 3.2 核心组件（新增/改造）

| 组件 | 类型 | 职责 |
|---|---|---|
| **Relay Hub**（中心侧） | 新增 | 接受节点 WSS 连接、鉴权、维护 `node_id → connection` 路由表、转发请求/流 |
| **Relay Agent**（节点侧） | 新增 | 出站连接 Hub、鉴权、把 Hub 转发来的请求映射到**节点本地 HTTP API**（复用现有 handler） |
| **Node Registry v2** | 改造 | 字段扩展：`trust_level`、`public_key`/`credential_id`、`relay_status`、`last_health`、`active_sessions/jobs` 计数 |
| **控制面 API v2** | 改造 | 在 `/control/` 下新增代理式端点：`POST /control/nodes/{id}/proxy/{path...}`（或等价 RPC） |
| **Nodes UI v2** | 改造 | 节点详情页（健康、session、审批、能力）、节点→操作入口（Open Chat / Terminal / Files） |
| **跨节点代理层** | 新增 | 中心侧把「用户请求」翻译成「目标节点的本地 API 调用」，支持流式（SSE/WS 透传） |

### 3.3 传输层设计：Relay 协议（Phase 1 核心）

借鉴 Orca 的 relay 设计（`temp/orca/src/relay/protocol.ts`），但简化到 Go 侧够用：

**传输**：WebSocket（`wss://center/api/relay`，gorilla/websocket 已在 go.mod）。

**帧格式**（JSON 消息，文本帧；大负载走二进制帧）：

```jsonc
// 客户端→Hub 方向（节点发起的控制消息）
{ "type": "hello",   "node_id": "node_abc", "credential": "ck_xxx", "version": "v1.2.0", "caps": ["chat","terminal","files","git"] }
{ "type": "pong",    "seq": 5, "t": 1720000000000 }
{ "type": "event",   "kind": "session_updated", "node_id": "node_abc", "payload": {...} }   // 节点主动推送状态

// Hub→节点 方向（中心转发用户请求）
{ "type": "request", "req_id": "r_1", "method": "http", "path": "/api/sessions", "method_verb": "POST", "body_b64": "...", "stream": false }
{ "type": "request", "req_id": "r_2", "method": "ws",    "path": "/api/terminal/ws", "target": "terminal-session-1" }  // 双向流复用
{ "type": "ping",    "seq": 3, "t": 1720000000000 }

// 节点→Hub 方向（响应）
{ "type": "response", "req_id": "r_1", "status": 200, "body_b64": "...", "headers": {...} }
{ "type": "stream",   "req_id": "r_2", "chunk_b64": "...", "final": false }
{ "type": "stream_end","req_id": "r_2" }
```

关键点：

1. **请求/响应模型**：`req_id` 关联；普通 HTTP 请求（chat、files、git）走 request/response；终端等流式能力走 `stream` 帧（节点侧把 PTY 输出切成 chunk 推回，输入反向走）。
2. **心跳双轨**：现有 `/control/nodes/{id}/heartbeat` 保留（兼容 + registry 离线判定）；relay 通道内另有应用层 ping/pong 检测连接健康。
3. **多路复用**：单条 WSS 连接承载多个并发 request/stream（按 `req_id` 解复用），避免每请求一条连接。
4. **重连**：节点侧指数退避重连 + 幂等（`req_id` 由中心生成，节点不重复执行已应答请求；断线重连后节点向 Hub 重新 hello，Hub 恢复路由）。
5. **流控**：终端/大文件流加简单背压（滑动窗口 ACK 或按 chunk 计数），避免 Hub 内存被慢消费者打爆（参照 Orca `dispatcher-client-writer` 的 lane/window 思路，先做简化版）。

**TCP 端口转发帧**（Phase 4，已确认需要）：

```jsonc
{ "type": "tcp_open",  "conn_id": "c_1", "target": "10.0.0.5:3306" }   // 中心→节点：请拨号
{ "type": "tcp_data",  "conn_id": "c_1", "chunk_b64": "..." }         // 双向字节流
{ "type": "tcp_close", "conn_id": "c_1" }                               // 任一端关闭
```

- 中心侧暴露 `godex node forward --node X --local 3306 --target 10.0.0.5:3306`（本地监听 → 经中心 → 节点 → 内网目标），等价 `ssh -L` 的跳板体验。
- 节点侧 `control.forward_allow` 白名单（如 `["10.0.0.5:3306", "127.0.0.1:*"]`）控制可转发目标；未在白名单一律拒绝并审计。

### 3.4 安全与信任模型

| 层 | 机制 |
|---|---|
| 节点→中心 鉴权 | **已确认：中心生成 `ck_` 前缀 per-node 凭证 → 复制到节点 `control.credential`**（`godex node issue-token <node-id>` 或中心 Web 生成）；不再依赖共享 web token 做节点鉴权（web token 仍用于浏览器用户） |
| 传输加密 | 全链路 TLS/WSS（服务器已有 https 反代）；可选的 E2EE 层（Orca 移动端有 e2ee 实现，可作为远期增强，Phase 1 不做） |
| 中心→节点 信任 | 节点配置 `control.trust`（如 `local` / `guarded-remote` / `untrusted`）；`guarded-remote` 默认要求节点侧审批才执行危险动作 |
| 用户→节点 授权 | 中心用户必须先通过 web token；跨节点写操作默认进目标节点的审批队列（复用现有 approval 框架），中心 UI 聚合展示 |
| 审计 | 跨节点请求记录 `{user, node_id, method, path, req_id, ts, status}` |

### 3.5 控制面 API v2（中心侧对外）

在中心 Web/API 上暴露统一「节点操作」入口（不要求前端知道 relay 细节）：

```
GET  /control/nodes                       # 现有：节点列表（扩展字段）
GET  /control/nodes/{id}                  # 现有：详情（扩展 active_sessions/approvals）
POST /control/nodes/{id}/sessions         # 代理：在目标节点创建/打开 session
POST /control/nodes/{id}/sessions/{sid}/messages   # 代理：发消息
WS   /control/nodes/{id}/terminal/ws      # 代理：远端终端
GET  /control/nodes/{id}/files/...        # 代理：文件浏览/读取
POST /control/nodes/{id}/files/...        # 代理：文件写入/编辑
GET  /control/nodes/{id}/approvals        # 聚合：该节点待审批
POST /control/nodes/{id}/approvals/{aid}  # 代理：处理审批
POST /control/nodes/{id}/tasks            # 代理：派发 longtask/subagent
POST /control/nodes/{id}/disconnect       # 管理：踢下线（可选）
```

实现方式：中心侧一个 `nodeProxy` handler 负责「解析目标节点 → 查路由表 → 若在线则封装成 relay request 转发 → 聚合响应/流回用户；若离线返回 503」。

### 3.6 前端 Nodes app v2

- 列表页：保留现有表格，增加 trust level、active sessions/jobs 徽标、approvals 计数、快捷操作（Open Chat / Terminal / Files）。
- 详情页（新）：健康时间线、能力清单、运行中 session/job、待审批、最近事件。
- 节点工作台（Phase 3）：点击节点后进入「该节点的 Chat/Terminal/Files」——复用现有三个 feature 组件，把 api client 的 base 换成 `/control/nodes/{id}/...` 代理前缀，UI 改动小、复用度高。

---

## 4. 参考：Orca 设计借鉴点（temp/orca）

| Orca 能力 | 借鉴点 | 对应到 godex |
|---|---|---|
| Mobile companion（React Native ↔ 桌面 WebSocket RPC，port 6768） | 出站 WebSocket + JSON-RPC 式协议 + 配对/凭证（借鉴其出站连接模型；godex 手机端直接用已适配的移动 Web，不跟 RN） | Relay 通道 + per-node 凭证 |
| SSH Worktree（本地 UI ↔ 远端 box 全量文件/git/终端，自动重连、端口转发） | 远端作为「被控端点」，本地作为「控制面」 | 服务器作为控制面，内网节点作为被控端点 |
| relay 协议（`src/relay/protocol.ts`）：handshake/keepalive/帧解码/流控/取消 | 双向流 + 背压 + req_id 复用 | Phase 1 relay 协议 |
| `agent.execNonInteractive` / `fs.*` / `git.*` handler 划分 | 能力按领域划分，客户端按能力探测降级 | 节点 capabilities 已有雏形，v2 扩展 |
| `--pairing-address`、ready JSON 契约 | 部署可观测性（stdout ready 块） | 中心 serve 时打印「Relay Hub ready + 在线节点数」 |
| 移动端 host endpoint / 连接健康管理（`mobile-endpoint-supervisor` 等） | 连接健康检测、重连、端点切换 | 节点侧 relay 连接状态机 |

注意：Orca 是「桌面端为富端、手机/远端为薄端」，godex 是「每个节点都是富端（完整 runtime）」，因此 godex 的 relay 主要做**请求路由与流透传**，不需要在节点侧实现完整 agent 执行协议——节点本地 API 已经是能力面。

---

## 5. 分阶段计划（建议）

> 每阶段交付后可独立部署验证；用户确认整体设计后，从 Phase 1 开始。

### Phase 1：Relay 传输层（✅ 已完成 2026-08-06）

**目标**：建立「节点出站长连接 + 中心路由 + 请求/响应透传」的最小闭环，验证内网节点可达。

- [x] 中心：`/api/relay` WSS endpoint + Relay Hub（连接表、鉴权、req_id 复用、ping/pong）——`internal/services/relay/hub.go`
- [x] 节点：Relay Agent（出站连接、指数退避重连、hello/pong、把 request 转发到本地 httpapi handler、流式 chunk 回传）——`internal/services/relay/agent.go`
- [x] per-node 凭证：中心生成 `ck_` 凭证 → 复制到节点 `control.credential`；中心只存 hash——`internal/services/relay/credential.go` + `POST /control/nodes/{id}/credential`
- [x] registry 字段扩展（relay_status、last_health、trust_level、credential_hash）——`internal/services/noderegistry/registry.go`
- [x] 中心代理端点 `POST /control/nodes/{id}/proxy/{path...}`——`internal/services/relay/proxy.go`
- [x] serve 装配：Hub 挂 `/api/relay`、Agent 在配置 center_url+credential 时启动、节点凭证校验接 registry——`cmd/godex/main.go`
- [x] 端到端冒烟：`scripts/smoke_relay.sh` 已跑通（注册 → 签发凭证 → 节点出站连接 → 中心 curl 代理端点拿到节点 /meta）

验证（2026-08-06 实测）：`curl http://127.0.0.1:3901/api/control/nodes/{id}/proxy/meta` 返回节点版本；registry 中 `relay_status=connected`。

### Phase 2：远程观测聚合

### Phase 2：远程观测聚合

**目标**：中心能看到每个节点的运行中 session/job/审批，Nodes UI 有详情页。

- [ ] 节点侧：把 session/job/approval 变更作为 `event` 帧主动推给中心（或中心按需拉取）
- [ ] 中心：节点状态聚合存储（内存 + 可选持久化），`/control/nodes/{id}/overview`
- [ ] Nodes UI：详情页（健康、能力、运行中 session/job、待审批、最近事件）
- 验证：内网节点跑一个 longtask，中心 Web 能看到其 phase/turn 进度。

### Phase 2：远程观测聚合（✅ 已完成 2026-08-06）

**目标**：中心能看到每个节点的运行中 session/job/审批，Nodes UI 有详情页。

- [x] 节点侧：`relay.Observer` 周期收集本地状态（sessions/longtasks/approvals）→ 经 agent 以 `event` 帧推送 `NodeSnapshot` 快照——`internal/services/relay/observer.go` + `internal/services/nodeobs/provider.go`（backend adapter，映射 `ListedSession`/`LongTaskView`/`PendingPermission`）
- [x] 中心：`relay.EventStore` 内存聚合（最新快照 + 50 条最近事件，按节点），hub `SetEventSink` + `StoreEvents` 桥接写入——`internal/services/relay/eventstore.go`
- [x] `GET /control/nodes/{id}/overview`：合并 registry `NodeView`（健康/能力/trust）+ EventStore `NodeOverview`（session/job/审批/最近事件）——`internal/runtime/httpapi`
- [x] Nodes UI 详情页 `/nodes/:id`：健康/中继状态/能力/运行中 session/job（含 phase/turn 进度条）/待审批/最近事件——`ui/web/src/features/nodes/NodeDetailPage.tsx`
- [x] serve 装配：中心建 EventStore + hub sink + overview 端点；节点建 Observer（agent 推快照）——`cmd/godex/main.go`
- [x] 端到端验证：`scripts/smoke_obs.sh` PASS（节点连中心 → 快照事件到达中心 overview）；协议级 `TestSnapshotPushEndToEnd` 验证 job phase/turn 变化在中心可见

验证（2026-08-06 实测）：`curl http://127.0.0.1:3911/api/control/nodes/{id}/overview` 返回 `overview.recent_events` 含 `kind=snapshot` 事件；修复了 Observer 首轮 poll 早于 agent 连线的丢快照 bug（`TestObserverRetriesWhenAgentNotConnected` 回归测试）。

### Phase 3：远程控制（chat / terminal / files）

**目标**：远程编程的第一版——在中心 Web 上对节点完成「聊天 + 终端 + 文件」操作。

- [ ] 中心 `nodeProxy`：sessions / terminal(WS 透传) / files 三类代理端点
- [ ] 节点侧 relay 支持 WS 双向流（terminal）
- [ ] Nodes UI v2：节点详情 + 「Open Chat / Terminal / Files」入口；前端 api client 支持节点前缀
- [ ] 审批聚合：节点写操作审批出现在中心，可在中心处理
- [ ] 安全：trust 级别生效（`guarded-remote` 节点默认审批）
- 验证：在内网节点 workspace 里，通过中心 Web 起一个 chat turn → agent 编辑文件 → 终端跑 `go test`，全程在中心浏览器完成。

### Phase 3：远程控制（chat / terminal / files）（✅ 已完成 2026-08-06）

**目标**：远程编程的第一版——在中心 Web 上对节点完成「聊天 + 终端 + 文件」操作。

- [x] relay 真流式：agent 侧 `streamWriter`（`http.Flusher` → `FrameStream` 帧，非流式仍单帧 `FrameResponse`）——`internal/services/relay/stream.go`；hub `ForwardStream` 实时回调 + `Forward` 聚合兼容——`internal/services/relay/hub.go`
- [x] 中心 nodeProxy 流式透传：`ProxyHandler` 改用 `ForwardStream`（SSE/chat 事件实时回浏览器，`http.Flusher` flush；Timeout 默认跟随客户端，不掐断长连接）——`internal/services/relay/proxy.go`
- [x] 节点侧 terminal：节点本地已有 HTTP 轮询式 PTY 端点（`/v1/terminal/create|output|input|resize`），经中心代理即可远程使用，无需新增 WS 适配
- [x] 前端节点上下文：`useNodeContextStore`（活动节点）+ `apiURL` 对节点业务路径加 `/control/nodes/{id}/proxy` 前缀（sse.ts 自动生效）+ terminalClient `getBaseUrl` 读取节点上下文——`ui/web/src/store/nodeContext.ts`、`ui/web/src/lib/api.ts`、`ui/web/src/lib/terminalClient.ts`
- [x] Nodes UI：节点详情页 Open Chat / Terminal / Files 入口 + App 层远程节点 banner（可退出远程模式）——`ui/web/src/features/nodes/NodeDetailPage.tsx`、`ui/web/src/App.tsx`
- [x] 审批聚合：节点写操作审批（Phase 2 快照 approvals）可在中心处理——`approveNodePermission` / `denyNodePermission` 经中心代理转发到节点，NodeDetailPage 待审批表加 Approve/Deny 操作
- [x] 安全：trust 级别生效——`guarded-remote` 节点写操作默认 403（`X-Godex-Trust-Approved` 头显式放行），`ProxyHandler.TrustLevel` 从 registry 解析——`internal/services/relay/proxy.go` + `cmd/godex/main.go`
- [x] 端到端验证：`scripts/smoke_remote.sh` PASS（中心经代理操作节点：meta / files.list / terminal.create / sessions + guarded-remote 403/200）

验证（2026-08-06 实测）：`curl http://127.0.0.1:3921/api/control/nodes/{id}/proxy/v1/terminal/create` 返回 `terminalId`；`TestProxyHandlerStreamsSSEInRealTime` 证明 SSE 事件 400ms 内实时到达（非缓冲）。

### Phase 4：远程编程工作台 + 本地跳板（✅ 已完成 TCP 端口转发，2026-08-06）

**目标**：完整远程开发闭环，并支持本地 CLI/TUI 以服务器为跳板。

- [x] **TCP 端口转发**：relay `tcp_open/data/close` 帧 + 中心 `godex node forward` 命令 + 节点 `control.forward_allow` 白名单（跳板访问内网服务）
  - 协议：`internal/services/relay/protocol.go` 新增 `FrameTCPOpen/FrameTCPData/FrameTCPClose`；`AllowForward` 白名单校验（host:port 精确 + `*` 通配，空列表默认全拒）——`internal/services/relay/tcp.go`
  - hub TCP 流原语：`Hub.OpenTCPStream(ctx, nodeID, connID, target)` 返回实现 `io.ReadWriteCloser` 的 `tcpStream`（cli 侧可 io.Copy 桥接）——`internal/services/relay/tcp_stream.go`
  - 中心 forward 会话端点：`ForwardHandler`（`/api/control/nodes/{id}/forward` WS，web token 鉴权）+ CLI 侧 `ForwardClient`（`Open(target)` 返回流）——`internal/services/relay/forward.go`
  - CLI 命令：`godex node forward --node X --local 3306 --target 10.0.0.5:3306 [--center URL] [--token]`，本地监听 → 每连接开一条节点侧 TCP 流双向搬运（等价 ssh -L）——`internal/app/node.go`
  - 节点侧拨号服务：agent 收 `tcp_open` → `AllowForward` 校验（未命中拒绝并回 tcp_close）→ `net.DialTimeout` → 双向泵（conn→hub 发 tcp_data；hub→conn 写数据）——`internal/services/relay/agent.go`
  - 配置：`control.forward_allow`（yaml + `GODEX_CONTROL_FORWARD_ALLOW` env，逗号分隔）——`internal/core/config/{types,config,resolve,schema}.go`
  - 装配：serve 注册 forward 端点 + agent 透传白名单——`cmd/godex/main.go`
  - 端到端验证：`scripts/smoke_forward.sh` PASS（中心跳板命令 → 节点内网 echo 服务往返 + 未列入白名单 target 拒绝）
- [ ] 节点工作台体验打磨（文件树 + diff + 终端多开 + 长任务面板）
- [x] 中心侧 `godex node exec --node <id> <cmd>` 类 CLI 跳板命令（✅ 已落地：节点侧新增 `POST /v1/exec` SSE 流式端点，复用 `localbash.RunBash`；CLI `godex node exec --node X 'cmd' [--dir] [--center] [--token]` 经中心 proxy 转发、增量打印流式输出、非零退出码透传——`internal/runtime/httpapi/httpapi.go` + `internal/app/node.go`；`scripts/smoke_exec.sh` PASS）；本地 TUI 通过中心连节点
- [ ] 跨节点任务编排（可选）：从中心把同一 prompt 派到多节点（借鉴 Orca parallel worktrees，非必须）
- [ ] 审计报表、存储/健康聚合（doctor per node）
- 验证：`godex node forward --node 内网A --local 3306 --target 10.0.0.5:3306` 可连内网数据库（✅ smoke_forward.sh 已证：本地端口数据经中心 relay → 节点拨号 → 内网服务往返）

### Phase 5：移动端（已确认：不做原生 App）

**结论**：Web UI 已自带移动端适配（viewport + 720/900/1180px media query + 移动导航抽屉），手机浏览器直接打开中心地址即可，**不需要 React Native**。手机访问流程与桌面完全一致：手机浏览器 → 中心 Web UI → 中心 `nodeProxy` 代理 → 目标内网节点（复用 Phase 3 同一套控制面 API）。

- [ ] 移动端体验补强（✅ 已纳入 Web Push，PWA 待做）：PWA manifest + service worker（主屏图标/离线缓存）、终端触屏键盘、审批按钮触屏优化
- [x] 推送（✅ 已完成 2026-08-06）：Web Push（中心不持久化历史，只推实时事件）——`internal/services/webpush`（VAPID 密钥生成/持久化到 state/push_keys.json + 内存订阅注册表 + `Notify` 发送，失败端点自动剔除）；中心端点 `/api/push/{public-key,subscribe,unsubscribe,test}`（`internal/runtime/httpapi/push.go`，web token 鉴权）；前端 `ui/web/public/sw.js`（service worker）+ `lib/push.ts`（订阅/测试）+ Settings「通知」区块；`scripts/smoke_push.sh` PASS
- [ ] 实验性「手机即节点」（✅ 已立项）：godex 编译到 Android（`GOOS=android GOARCH=arm64`，依赖基本纯 Go：modernc sqlite / gorilla websocket / creack-pty 的 linux tag 匹配 android），在 Termux 类环境跑本地 agent，作为节点注册到中心——手机本身成为节点，对应「保留移动端本地 agent 能力」的诉求。iOS 因沙箱限制（无 shell/任意进程/PTY）不作为本地 agent 目标，仅用浏览器。
- 说明：手机只是另一类客户端 + 可选节点；内网节点无需任何改动。

---

## 6. 风险与开放问题

---

## 6. 风险与开放问题

| # | 风险/问题 | 影响 | 对策 |
|---|---|---|---|
| 1 | 内网节点网络不稳定 → relay 频繁断连 | 流式体验中断 | 指数退避重连 + req_id 幂等 + 终端 session 在节点侧持久（重连后重新 attach） |
| 2 | 共享 web token 泄露 → 节点被冒充 | 严重 | Phase 1 即上 per-node 凭证；凭证只存节点配置，不随 UI 下发 |
| 3 | 中心成为单点/瓶颈 | 大流（终端、文件）占用 Hub 带宽内存 | 流控背压；Hub 无状态化（后续可水平扩展，relay 粘性路由） |
| 4 | 跨节点写操作的安全边界 | 违反节点本地策略 | trust 分级 + 默认审批 + audit；节点本地 approval 永不绕过 |
| 5 | 前端复用度 | 节点工作台 UI 改动大 | 前端 api client 前缀化，尽量复用现有 Chat/Terminal/Files 组件 |
| 6 | 服务器节点自身的接入方式 | 服务器既当 Hub 又当节点 | 支持「本地直连（不走网络）」与「relay 连自己」两种，默认直连 |
| 7 | 版本兼容 | 新节点连旧中心 | hello 带 version，中心按最低支持版本拒绝或降级 |

开放问题（4 项已确认，见 §0）：

1. ~~节点凭证的管理方式~~ ✅ 已确认：中心生成复制
2. ~~是否需要端口转发~~ ✅ 已确认：需要（Phase 4 TCP 转发）
3. ~~是否需要手机端~~ ✅ 已确认：不做原生 App，用移动 Web + 可选 PWA；Android 本地 agent 为实验性
4. ~~中心是否持久化 session 历史~~ ✅ 已确认：不持久化，只在线观测

遗留待定：

- Android 本地 agent（Termux 类）的实验节奏与首版范围（先能跑 godex serve + 注册中心）。
- `forward_allow` 白名单默认策略（默认全拒 vs 默认放行 localhost）。

---

## 7. 建议的下一步

1. 用户确认本设计的范围与 Phase 1 边界（开放问题已确认，见 §0）；
2. ✅ Phase 1（Relay 传输层）已交付（2026-08-06），见 §5；
3. 启动 Phase 2 开发（远程观测聚合）：节点把 session/job/approval 变更作为 `event` 帧推给中心，Nodes UI 详情页。
