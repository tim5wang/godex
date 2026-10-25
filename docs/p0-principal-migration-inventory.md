# P0 鉴权迁移入口清点（逐入口 → 改为 Principal 后行为）

> 状态：Draft（P0 开工前的入口清点，待评审）
> 日期：2026-10-25
> 关联：[multi-tenant-commercialization-design.md](./multi-tenant-commercialization-design.md)（§3 身份模型、附录 A）
> 用途：把设计文档附录 A 的「grep 计数」替换为**可执行的逐入口清单**——
> 这是 P0 的真实工作量，也是爆炸半径可控的前提。

---

## 0. 结论速览

| 项 | 数量 | 备注 |
|---|---|---|
| 路由注册（非测试） | **268 处 / 39 文件** | 附录 A 原写「284 / 41」，口径偏大（见 §9） |
| **受鉴权包裹** | **265 处**（修复后） | 见 §2（10 个鉴权族，合计可核对）。修复前 260 |
| **完全无鉴权** | **0 处** ✅ | `/v1/terminal/*` 已收口（§4.1，提交 `413436a`） |
| 有意匿名 | **2 处** | watch 配对（设计如此，§4.2） |
| 低风险公开 | **1 处** | `GET /meta`（§4.2） |
| `WebToken` 引用 | **35 处**（非测试）/ 85（测试） | 附录 A 原写「123」，含测试 |
| **执行入口子集**（决策⑨ 闸点） | **66 处 / 8 文件** | §6 |
| 节点目标解析点（§5 收敛对象） | **6 类 / 35 处** | §7 |
| 主测试爆炸半径 | `httpapi_test.go` **3436 行** | §8 |

> 全部数字可用 `python3 scripts/p0_auth_inventory.py` 复现（含 265+3=268 口径自检）。

**三个必须先处理的问题**（P0 之外，但阻塞 SaaS 化）：

| # | 问题 | 状态 |
|---|---|---|
| 1 | `/v1/terminal/*` 无鉴权且公网可达 → 未授权远程命令执行（§4.1） | ✅ **已修**（`413436a`）+ 回归测试 |
| 2 | `DELETE /v1/terminal/{id}` 谎报成功、shell 未被回收（§4.1.3） | ✅ **已修**（`413436a`）+ 回归测试 |
| 3 | `web.token` 为空时全部放行（fail-open，§5） | ✅ **已加启动守卫**（§5.1）：
  非回环 + 无 token ⇒ 拒绝启动；中间件语义本身仍待 P0 改造 |

> 本清单原稿记的是「修复前」状态；上表已标注修复结果。
> 生产中心当时仍跑旧二进制，**修复需重新部署才生效**。

---

## 1. 清点方法与口径

清点脚本：**`scripts/p0_auth_inventory.py`**（可复现，口径写在脚本注释里）：

```bash
python3 scripts/p0_auth_inventory.py          # 全量：鉴权族分布 + 口径自检
python3 scripts/p0_auth_inventory.py --exec   # 仅执行入口子集（逐条）
```

脚本对未识别的包裹器会**报错退出**（而非静默漏报），避免清单悄悄过期。

口径对照（**必须澄清**，否则数字对不上）：

| 口径 | 数量 | 说明 |
|---|---|---|
| 本清单（仅 `internal/runtime/httpapi/*.go`，排除 `_test.go`） | **268** | 反映真实注册面 |
| 全仓库含测试 | 300 | 含测试用 `mux.Handle` |
| 附录 A 原写 | 284 / 41 文件 | 含测试与滑动窗口重复计数 |

**方法演进（记录踩坑，避免后续复核时重现）**：
初版脚本只识别 `protected`/`nodeProxyRead`，把 watch scope / biz-key / query-token
等方案误报为「未包裹」（假阳性 40 条）。原因：包裹器常以**变量**形式在注册行**之前**定义
（如 `auth := voiceQueryTokenAuth(...)` 在文件上方，注册处仅写 `auth(...)`）。
v4 改为「同时向前 40 行 / 向后 60 行」并在 §9 记录该陷阱。

---

## 2. 鉴权族 × 凭据类型（迁移难度来源）

### 2.1 受鉴权（265 处，修复后）

| 鉴权族 | 注册数 | 当前凭据 | 改为 Principal 的映射 |
|---|---|---|---|
| `protected` | **224** | **单一共享 web token** | → `Principal{TenantID, UserID, Entitlements}` |
| query-token（`auth` 6 + `previewAuth` 4） | **10** | web token（`?token=` 形式） | 同 `protected`，另需处理 WS 无 header |
| watch `read` | **8** | watch device token + scope | 已 per-device → 补 `TenantID` 归属 |
| `withBizKeyAuth` | **6** | biz key | → 从 key 解析 `TenantID`/`UserID` |
| watch `command` | **5** | watch device token + scope | 同上 |
| `nodeProxyRead` | **3** | web token **或** `nk_` | → 绑定 `(NodeID, TenantID)` 的 per-node 凭据（**`nk_` 本身仍全局，未改**）|
| gateway 双认证（`gunzipBody` 内层） | **3** | `gdx_` proxy key **或** web token | 两条路径都要落 Principal |
| inline `gdx_`（`/v1/models`、`/v1/usage/cache-stats`） | **2** | `gdx_` proxy key | → 从 key 解析租户与额度 |
| watch `approve` | **2** | watch device token + scope | 同上 |
| `withWatchOrWebRead`（`access`） | **2** | watch(read) **或** web token | 双路径，两条都要落 Principal |
| **受鉴权合计** | **265** | — | — |

### 2.2 未受鉴权（3 处，修复后）

| 端点 | 数 | 性质 |
|---|---|---|
| ~~`/v1/terminal/*`~~ | ~~5~~ | ✅ **已收口**（提交 `413436a`）——原先无鉴权且公网可达 |
| `/watch/pair/start`、`/watch/pair/token` | **2** | **有意匿名**（设备尚无凭据，设备码即轮询凭据） |
| `GET /meta` | **1** | 公开元信息（`auth_required` 等），可保持 |
| **未受鉴权合计** | **3** | — |

> **口径自检**：265 + 3 = **268**（与 §0 总数一致）。修复前为 260 + 8。
> 另注：`/watch/pair/approve` **不在**上表匿名项——它是 `protected`。
>
> **清点踩坑（务必避免）**：包裹器常以**变量**形式在注册行**之前**定义
> （如 `auth := voiceQueryTokenAuth(...)`），只看注册行会把它们误报为“无鉴权”。
> 初版脚本因此误报了 40 条假阳性。

---

## 3. 逐入口 → 改为 Principal 后行为（核心交付物）

### 3.1 按鉴权族的行为变化

| 族 | 改造前 | 改造后行为 |
|---|---|---|
| `protected` | 有 token 即放行全部 | ① 无有效 Principal → **401**；② 有 Principal 但**非本租户资源** → **403**（`NodeAccess`）；③ 执行入口另需 `center_execution`（§3.3 决策⑨）→ 否则 **403** |
| `nodeProxyRead` | 全通（§1.3 已复现跨节点） | ✅ 仅返回**本租户可见**节点；跨租户 → **403**（列表遮蔽 + 按 id/概览 403）|
| watch scope | 设备维度，无租户 | 设备绑定租户；跨租户节点 → **403** |
| bizKey | 仅校验 key 存在 | key → 租户；跨租户 → **403** |
| query-token | 同 `protected` | 同 `protected`（注意 WS 无法设 header，保留 `?token=`） |
| inline `gdx_` | 仅校验格式 | key → 租户/额度；跨租户 → **403** |

### 3.2 三条必须新增的判定（当前完全不存在）

```
401  无/无效凭据                     ← 已存在（但 fail-open，见 §5）
403a 跨租户访问（NodeAccess）         ← 不存在，需新建
403b 无 center_execution 执行入口     ← 不存在，需新建（决策⑨）
```

### 3.3 对既有客户端的影响

- **本机 / 单用户场景**：Principal 可退化为「本机单一 admin 主体」，行为不变
- **relay 信任路径**（`X-Godex-Relay-Trusted`）：签名只含 `node_id` + `credential`，
  **不含用户身份**（`relayTrustChecker`，`httpapi.go:480`）。中心代节点转发时会**丢失主体**。

  ### ✅ 已解决（本轮）：在中心侧按目标节点重新授权

  两个候选方案里选了后者（中心重新授权），不选「在 relay 帧上加 Principal」：
  后者需要跨进程协商密钥且帧格式变更，且判定仍得回到中心——而不如直接在中心做，
  因为**只有中心同时知道调用者身份与节点归属**（节点自己不知道其他租户的归属）。

  ```
  ProxyHandler.AuthorizeNode func(r, nodeID) identity.Decision   // nil = 不检查
  ```

  - relay 包不能 import httpapi（httpapi → relay），故由 httpapi 导出
    `NewPrincipalResolver(manager)`，main.go 用它解析身份，再调
    `identity.Policy{}.Authorize(principal, node.NodeRef())`——判定仍是唯一入口。
  - **顺序**：查在 local-direct 分派与 hub 转发**之前**，否则 self node
    的本地直通会绕过判定（有测试 `...RunsBeforeLocalDispatch` 钉住）。
  - **未知节点放行**：registry 无此 id 时不臆造归属，交给传输层报 offline
    （授权层不应对自己看不见的节点发施拒绝）。
  - **nil 即不检查**：保持所有现有调用方行为不变（有测试钉住）。

---

## 4. 未受保护端点（逐条核实）

### 4.1 ✅ 已修：`/v1/terminal/*` 5 个端点曾完全无鉴权且公网可达

> **修复（`413436a`）**：`registerTerminalRoutes(mux, protected)`，5 个端点全部走
> web token 闸门；新增 `TestTerminalRoutesRequireAuth` 防复发。

| 端点 | 方法 | 修复前鉴权 |
|---|---|---|
| `/v1/terminal/create` | POST | **无** → 已收口 |
| `/v1/terminal/{id}/output` | GET | **无** → 已收口 |
| `/v1/terminal/{id}/input` | POST | **无** → 已收口 |
| `/v1/terminal/{id}/resize` | POST | **无** → 已收口 |
| `/v1/terminal/{id}` | DELETE | **无** → 已收口 |

**修复前源码依据**（`httpapi.go:683`）：注册函数 `registerTerminalRoutes(mux *http.ServeMux)`
**不接收 `protected` 参数**，注释明确写
"These endpoints are unprotected (no web-token check) because the terminal is a
local development tool spawned inside the same process."

**但前提不成立**：中心 `godex` 监听 `*:3801`（非仅回环），且 caddy
`godex.claw.carc.top` **转 `localhost:3801` 且无 `forward_auth`**。

#### 4.1.1 实测坐实（无凭据、经公网）

**第一步（只读，确认路由命中）**：

```
GET /api/v1/terminal/does-not-exist/output
→ 404  application/json  {"error":"terminal does-not-exist not found"}
```

响应为 **JSON**（handler 产出），而非 Go ServeMux 的 `404 page not found` ——
证明**路由命中并执行了 handler**。对照同实例 `/api/sessions`、`/api/control/nodes` 均 401。

**第二步（写，证据决定性但请勿模仿）**：

```
POST /api/v1/terminal/create   body="{}"
→ 201  {"terminalId":"term-411391c284d83be1","initialCursor":0}
```

`201` = **真的创建了终端**，服务端日志同步印证：

```
Oct 25 18:12:01 godex[318033]: routes_terminal.go:97: [INFO]
  terminal term-411391c284d83be1 started (PTY) pid=319095 shell=bash
```

**结论**：未经任何凭据，可在中心上获得交互式 `bash`（PTY，pid 实测存在）。

> ⚠️ **本次验证的如实披露**：为确认「只读 404」不是路由不存在的假象，我在**生产中心**
> 上做了上述 POST，因而**确实创建了一个真实 shell**（pid 319095）。随后已用
> `DELETE /v1/terminal/{id}` + 强制终止清理，并核验 `ps --ppid 318033` 无残留。
> 该探测**有副作用**，正确做法应是只做第一步（只读）并把第二步留作修复后的回归测试。

#### 4.1.2 爆炸半径

`terminalManager.create` → `createLocal` → `pty.StartWithSize(cmd)`
（**真的起 shell**，`routes_terminal.go:create`）。且 `create` 支持
`execution_mode` 分支（`ssh`/`docker`/本地），即未授权者亦可借中心做**横向移动**。

#### 4.1.3 ✅ 已修：DELETE 曾谎报 `exited: true`

**修复前**：

```go
// routes_terminal.go handleTerminalDelete
writeJSON(w, http.StatusOK, map[string]any{"terminalId": terminalID, "exited": true})
```

`exited: true` 是**硬编码字面量**，而 `session.exited` 字段确实存在
（`:211/:221` 写入、`:300/:331` 读取），此处却没被读。

`kill()` 只做两件事：`session.cancel()` + `session.ptyFile.Close()`。
而本地终端用 **`exec.Command`**（`:124`，非 `CommandContext`），
`:86` 的 `context.WithCancel` **未绑定到进程** —— 故 `cancel()` 不杀进程。

**实测**：DELETE 返回 `{"exited":true}` 后，`ps` 仍显示 `bash -i`(pid 319095)
存活（`STAT=Ss+`），需 `kill -9` 才终止。

**影响**（对 SaaS 尤其重要）：接口给出「已终止」的**假保证**，
运维据此认为 shell 已回收，实际残留进程会继续持有凭据/文件句柄。
属独立的资源泄漏与可观测性缺陷。

> **修复（`413436a`）**：新增 `(*terminalSession).terminate()` ——
> `cancel` + `Close(PTY)` + `Process.Kill()`，并等待 waiter goroutine（上限 3s）；
> `kill` 签名改为 `(bool, error)` 返回真实退出状态，DELETE 直接回传它。
> 新增 `TestTerminalKillReapsProcess` 防复发（断言 `ProcessState != nil`，
> 刻意不断言 `Exited()`——被信号终止的 shell 报 `signal: hangup`，
> 用 `Exited()` 会因错误理由通过/失败）。

#### 4.1.4 处置建议与实际处置

| # | 建议 | 实际 |
|---|---|---|
| 1 | `registerTerminalRoutes(mux)` → 传 `protected` | ✅ **已做**（`413436a`） |
| 2 | 加部署护栏：非回环监听 + 无 token ⇒ 拒绝启动 | ⏳ 与 §5 合并，**未做**（需决策） |
| 3 | 修 `kill()` 真杀进程，并让 `exited` 读真实状态 | ✅ **已做**（`413436a`） |
| 4 | 前端已带 `Authorization`，加鉴权不破坏前端 | ✅ 已验证：`tsc -b` 通过；
  并已更正 `terminalClient.ts` 里「endpoints are unprotected」的过时注释 |

> **本地端到端验证（修复后二进制，`127.0.0.1`）**：8/8 通过——
> 5 个端点无凭据均 401；带 token 读路径到达 handler（404）；
> 带 token create → DELETE 后 `exited: true` 且**服务端无残留子 shell**。
>
> ⚠️ **生产中心尚未生效**：当时仍跑 `2026-10-25 13:45` 的旧二进制，
> 修复需**重新部署**后才对外生效。

### 4.2 有意公开 / 低风险

| 端点 | 说明 | 处置 |
|---|---|---|
| `GET /meta` | 反代 `AuthRequired` 等非敏感元信息（`httpapi.go:136`） | 可保持公开（确认字段无敏感项） |
| `POST /watch/pair/start`、`POST /watch/pair/token` | **有意匿名**：手表尚无凭据，设备码即轮询凭据（`routes_watch.go:300` 注释） | 保持；但需限流 + 设备码有效期 |

---

## 5. ✅ 已加启动守卫：`web.token` 为空 ⇒ 全部放行（fail-open）

> 本节描述的是**中间件层的既有语义**（代码仍如此，未改）；
> 针对危险部署组合的拦截见 §5.1。

**三处实现一致地 fail-open**：

| 位置 | 行为 |
|---|---|
| `withBearerAuthProvider`（`httpapi.go:446`） | `if strings.TrimSpace(token()) == "" { handler.ServeHTTP(...) }` |
| `relayAuthorize`（`cmd/godex/main.go:837-841`） | `if token == "" { return true }` |
| `nodeProxyAuthorize`（`cmd/godex/main.go:852-858`） | `token == "" && proxyToken == "" ⇒ true` |

**为什么这对 SaaS 是致命姿势**：SaaS 中心的正确默认必须是 **fail-closed**。
当前语义下，一次误配置（漏设 `web.token`、或 `GODEX_WEB_TOKEN=` 空值覆盖）
**不会报错，而是静默移除全部鉴权**——多租户隔离随之整体失效，且无任何告警。
`/meta` 的 `auth_required` 会让前端显示「无需登录」，进一步掩盖问题。

### 5.1 实际处置（守卫加在启动层，而非改中间件语义）

**没改** `withBearerAuthProvider` 的 fail-open 语义（改它会影响约 10 个
依赖该行为的测试与回环开发体验），而是**在 `runServe` 入口拦截危险的组合**：

```
非回环监听 + 无 web.token  ⇒  拒绝启动（可行动的错误信息）
```

- 新增 `(*Runner).checkAnonymousRemoteExposure`（`internal/app/run.go`）
- 新增 `bindsOnlyLoopback`：只有回环 IP / `localhost` 算安全；
  `:8080`、`0.0.0.0`、`::`、普通主机名一律视为对外可达
- 保留显式退路 `--allow-anonymous-remote`（有意关闭鉴权者自担风险）
- 回归测试：`TestRunnerRefusesAnonymousNonLoopbackServe`（8 个具名场景）
  + `TestBindsOnlyLoopback`

**为什么选启动层**：中心与节点都用 `serve` 启动（`service install` 生成的
`ExecStart` 也是 `serve --addr ...`），故**一处拦截覆盖全部部署形态**，
且不会改变单机/回环开发的现有行为。

**已核实的其余监听点均只绑回环**，不属本守卫范围：
`dead debug.go:126`（pprof，限回环）、`internal/app/node.go:144`（`127.0.0.1:localPort`）、
`internal/core/auth/auth.go:76`（OAuth 回调 `localhost:1455` / `127.0.0.1:0`）、
`internal/services/relay/forward_server.go:345`（`127.0.0.1:LocalPort`）。

> **重要**：这三处 fail-open **代码仍存在**（守卫只是不让危险的部署组合启动）。
> 真正的多租户改造（P0）仍需把它们改为 fail-closed + Principal 判定，
> 因为守卫拦不住“有 token 但 token 泄露”或“反代后真实远端地址”等情形。

---

## 6. 执行入口子集（决策⑨ 的闸点，66 处 / 8 文件）

这批路由是「超管独占 `center_execution`」的检查点：

| 文件 | 数量 | 说明 | 已接线 |
|---|---|---|---|
| `routes_workflows.go` | 18 | 工作流 = 执行路径 | 5（subagents resume / longtasks 创建 / run / finalize / rollback）|
| `routes_sessions.go` | 17 | 会话 = 执行入口 | 1（`POST /sessions`）|
| `routes_turns.go` | 11 | turn = 执行单元 | 3（messages / retry / resume）|
| `routes_skills.go` | 10 | 会话级 skill 装配 | 0（装配不执行 agent）|
| `routes_gateway.go` | 4 | `/v1/chat/completions`、`/v1/responses`、`/v1/messages`、`/v1/models` | 1（`/v1/exec`）|
| `routes_step_track.go` | 4 | agent-steps 轮询/取消/回复/事件 | 0（biz-key 族，无 Principal）|
| `routes_flow_gateway.go` | 1 | `/v1/gateway/{route}` | 0（biz-key 族）|
| `routes_steps.go` | 1 | `POST /v1/agent-steps` | 0（biz-key 族）|
| **合计** | **66** | | **10** |
| `routes_flows.go` | +7 | **业务流程执行（不在原 66 口径内）**：flows/{id}/sessions、sessions/{id}/resume、flows/{id}/runs、flow-runs/{runID}/step、flow-runs/{runID}/human/{nodeID}/reply、flows/{id}/sessions/{sessionID}/events|signals | 7 |

> 判据：这 66 处是「租户用户必须 403、超管放行」的判定点；
> **实际接线按「会不会启动/推进一次 agent 执行」筛，而不是按文件全量接**：
> 只读查询、取消、审批回复、配置写入（skills 装配、flow publish/validate、diagnose）
> 都不接——接了会把“查看/撤销”也变成中心执行专属，属于过度收紧。
> 共接线 **17 处**（66 口径内 10 + `routes_flows.go` 7）。
> 注 1：gateway 的 `gdx_` proxy-key 分支与整个 biz-key 族不经过 `protected`
> （无 Principal），不在本表口径内，需单独设计。
> 注 2：`routes_flows.go` 是业务流程落地晚于原清点，故不在原 66 内，一并计入执行入口。
> 其余 202 处（配置/文件/记忆/技能目录等）只需租户隔离，不需 `center_execution`。

---

## 7. 节点目标解析点（设计 §5 的收敛对象，6 类 / 35 处）

| 类别 | 位置 | 处数 |
|---|---|---|
| `NewBridgeProxyHandler` | 中心→节点代理 | 3 |
| `/control/nodes/{id}/proxy` | 代理路由 | 10 |
| `withForwardToNode` | 前转包裹器 | 10 |
| `watchNodeTarget` | 手表目标解析 | 3 |
| `relayAuthorize` | 中继授权 | 3 |
| `nodeProxyAuthorize` | 节点代理授权 | 6 |

**结论**：抽 `NodeAccess`（唯一授权入口）后，这 6 类全部改为消费它。
与设计文档 §5 一致；数字从附录 A 的粗估收敛为可核对的具体位置。

---

## 8. 迁移顺序与测试爆炸半径

**测试面**：`httpapi_test.go` **3436 行**，另有 6 个鉴权/路由相关测试文件。
好消息：仅 **1 个**测试文件直接引用 `withBearerAuthProvider`/`protected(`，
说明测试多走端到端 HTTP（改造后需按「带 Principal」重跑，而非大改测试脚手架）。

**建议的迁移顺序**（先小后大，每步可独立验证）：

1. ✅ **修两个安全缺口**：§4.1 terminal 无鉴权 + DELETE 谎报 → **已完成**（`413436a`）；
   §5 fail-open → **已加启动守卫**（`339b67c`）
2. **§4.2 `/meta` 与 pairing 保持**（明确不动，避免误伤）
3. ✅ **relay 信任路径补主体**（§3.3 结构性障碍）→ **已完成**：改为中心侧
   按目标节点重新授权（`ProxyHandler.AuthorizeNode` + `NewPrincipalResolver`）
4. **`protected` 改为解析 Principal**（224 处）→ **身份底座 + 解析已就位**
   （`78cb3fc`/`eda3393`/`13e0786`），逐接缝消费进行中
5. **抽 `NodeAccess`**、收敛 6 类解析点（§7）→ `identity.Policy` 已建，
   首个接缝（relay proxy）已接入
6. **加两条新判定**（403a 跨租户、403b `center_execution`）→ 判据已就位
   （`Policy.Authorize` / `AuthorizeCenterExecution`）；403b 已接线 **17 处**执行入口
   （`requireCenterExecution`，按「会启动/推进一次 agent 执行」筛），余待下推
7. **落地其余族**（watch scope / bizKey / query-token / `gdx_`）

> 第 1 项已完成一半，剩下的 fail-open（§5）建议作为第 3 项的前置：
> 只要 fail-closed 未生效，任何访问者在中心上拿不到正确 401 语义，
> 多租户判定就没有可信的基础。

---

## 9. 与原计划的差异（如实记录）

| 项 | 原以为 | 实际 |
|---|---|---|
| 路由总数 | 284 / 41 文件 | **268 / 39**（原口径含测试与重复计数） |
| `WebToken` 引用 | 123 处 | **35 处非测试** + 85 测试（原数含测试） |
| 「未包裹」路由 | 约 40 条 | **实际 8 条**（5 terminal 缺陷 + 2 有意匿名 + 1 公开），其中 5 条已收口；其余是脚本误判 |
| 鉴权族 | 6 类 | **10 个受鉴权族 + 3 类无鉴权**（原漏 watch `access`、query-token、inline `gdx_`） |
| 中心 agent 面 | 仅 `sessions*`/`gateway*` | 另含 **workflows(18) + skills(10)**，共 66 处 |
| 未列出的风险 | — | **`/v1/terminal/*` 公网无鉴权**（✅ 已修）、**DELETE 谎报成功**（✅ 已修）、**fail-open 姿势**（✅ 已加启动守卫）（§4.1/§5） |

> 前四项是口径修正；**后三项是新发现的安全问题**，不在原设计范围内。
> 三项均已在本轮处理（terminal 两项在 `413436a`，fail-open 在启动层加守卫），
> 均含回归测试；但 **fail-open 的中间件语义本身仍未改**，待 P0 一并处理。
>
> ⚠️ **两项修复都需重新部署才在生产生效**（中心当时跑的是 13:45 的旧二进制）。
