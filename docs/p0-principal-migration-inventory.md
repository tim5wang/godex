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
| **受鉴权包裹** | **260 处** | 见 §2（10 个鉴权族，合计可核对） |
| **完全无鉴权** | **5 处** ⚠️ | `/v1/terminal/*`，且公网可达（§4.1） |
| 有意匿名 | **2 处** | watch 配对（设计如此，§4.2） |
| 低风险公开 | **1 处** | `GET /meta`（§4.2） |
| `WebToken` 引用 | **35 处**（非测试）/ 85（测试） | 附录 A 原写「123」，含测试 |
| **执行入口子集**（决策⑨ 闸点） | **66 处 / 8 文件** | §6 |
| 节点目标解析点（§5 收敛对象） | **6 类 / 35 处** | §7 |
| 主测试爆炸半径 | `httpapi_test.go` **3436 行** | §8 |

> 全部数字可用 `python3 scripts/p0_auth_inventory.py` 复现（含 260+8=268 口径自检）。

**三个必须先处理的问题**（P0 之外，但阻塞 SaaS 化）：

1. **`/v1/terminal/*` 无鉴权且公网可达** → 未授权远程命令执行（§4.1，**已实测复现**）
2. **`DELETE /v1/terminal/{id}` 谎报成功**：响应硬编码 `exited: true`，但进程可能存活
   （§4.1.3，实测）——给出「已终止」的假保证，掩盖残留 shell
3. **`web.token` 为空时全部放行（fail-open）** → 一次误配置即静默失去所有鉴权（§5）

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

### 2.1 受鉴权（260 处）

| 鉴权族 | 注册数 | 当前凭据 | 改为 Principal 的映射 |
|---|---|---|---|
| `protected` | **219** | **单一共享 web token** | → `Principal{TenantID, UserID, Entitlements}` |
| query-token（`auth` 6 + `previewAuth` 4） | **10** | web token（`?token=` 形式） | 同 `protected`，另需处理 WS 无 header |
| watch `read` | **8** | watch device token + scope | 已 per-device → 补 `TenantID` 归属 |
| `withBizKeyAuth` | **6** | biz key | → 从 key 解析 `TenantID`/`UserID` |
| watch `command` | **5** | watch device token + scope | 同上 |
| `nodeProxyRead` | **3** | web token **或** `nk_` | → 绑定 `(NodeID, TenantID)` 的 per-node 凭据 |
| gateway 双认证（`gunzipBody` 内层） | **3** | `gdx_` proxy key **或** web token | 两条路径都要落 Principal |
| inline `gdx_`（`/v1/models`、`/v1/usage/cache-stats`） | **2** | `gdx_` proxy key | → 从 key 解析租户与额度 |
| watch `approve` | **2** | watch device token + scope | 同上 |
| `withWatchOrWebRead`（`access`） | **2** | watch(read) **或** web token | 双路径，两条都要落 Principal |
| **受鉴权合计** | **260** | — | — |

### 2.2 未受鉴权（8 处）

| 端点 | 数 | 性质 |
|---|---|---|
| `/v1/terminal/*` | **5** ⚠️ | **缺陷**：注释声称“本地开发工具”，但中心公网可达 |
| `/watch/pair/start`、`/watch/pair/token` | **2** | **有意匿名**（设备尚无凭据，设备码即轮询凭据） |
| `GET /meta` | **1** | 公开元信息（`auth_required` 等），可保持 |
| **未受鉴权合计** | **8** | — |

> **口径自检**：260 + 8 = **268**（与 §0 总数一致）。
> 另注：`/watch/pair/approve` **不在**上表匿名项——它是 `protected`。
>
> **清点踩坑（务必避免）**：包裹器常以**变量**形式在注册行**之前**定义
> （如 `auth := voiceQueryTokenAuth(...)`），只看注册行会把它们误报为“无鉴权”。
> 初版脚本因此瞖报了 40 条假阳性。

---

## 3. 逐入口 → 改为 Principal 后行为（核心交付物）

### 3.1 按鉴权族的行为变化

| 族 | 改造前 | 改造后行为 |
|---|---|---|
| `protected` | 有 token 即放行全部 | ① 无有效 Principal → **401**；② 有 Principal 但**非本租户资源** → **403**（`NodeAccess`）；③ 执行入口另需 `center_execution`（§3.3 决策⑨）→ 否则 **403** |
| `nodeProxyRead` | 全通（§1.3 已复现跨节点） | 仅返回**本租户可见**节点；跨租户 → **403** |
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
  **不含用户身份**（`relayTrustChecker`，`httpapi.go:480`）。中心代节点转发时会**丢失主体**，
  这是 P0 的结构性障碍：需在 relay 帧上加 `Principal` 或让中心按目标节点重新授权。

---

## 4. 未受保护端点（逐条核实）

### 4.1 ⚠️ 严重：`/v1/terminal/*` 5 个端点完全无鉴权，且公网可达

| 端点 | 方法 | 鉴权 |
|---|---|---|
| `/v1/terminal/create` | POST | **无** |
| `/v1/terminal/{id}/output` | GET | **无** |
| `/v1/terminal/{id}/input` | POST | **无** |
| `/v1/terminal/{id}/resize` | POST | **无** |
| `/v1/terminal/{id}` | DELETE | **无** |

**源码依据**（`httpapi.go:680-690`）：注册函数 `registerTerminalRoutes(mux *http.ServeMux)`
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

#### 4.1.3 附带缺陷：DELETE 谎报 `exited: true`

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

#### 4.1.4 处置建议（按优先级）

1. 立即把 `registerTerminalRoutes(mux)` 改为 `registerTerminalRoutes(mux, protected)`
2. 加部署护栏：非回环监听 + 无 token ⇒ 拒绝启动（与 §5 合并处理）
3. 修 `kill()`：改用 `exec.CommandContext` 或显式 `Process.Kill()`，
   并让 `exited` 读真实状态（对齐 `session.exited.Load()`）
4. 前端 `terminalClient.ts` 已带 `Authorization`（`:22-31` 注释就写着
   "Terminal endpoints are unprotected when..."），故加鉴权**不会破坏前端**

### 4.2 有意公开 / 低风险

| 端点 | 说明 | 处置 |
|---|---|---|
| `GET /meta` | 反代 `AuthRequired` 等非敏感元信息（`httpapi.go:136`） | 可保持公开（确认字段无敏感项） |
| `POST /watch/pair/start`、`POST /watch/pair/token` | **有意匿名**：手表尚无凭据，设备码即轮询凭据（`routes_watch.go:300` 注释） | 保持；但需限流 + 设备码有效期 |

---

## 5. ⚠️ 系统性 posture：`web.token` 为空 ⇒ 全部放行（fail-open）

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

**处置建议**：改为「非回环监听且无 token ⇒ 启动失败」，本地开发用显式开关
（如 `--allow-anonymous-local`）保留便利性。

---

## 6. 执行入口子集（决策⑨ 的闸点，66 处 / 8 文件）

这批路由是「超管独占 `center_execution`」的检查点：

| 文件 | 数量 | 说明 |
|---|---|---|
| `routes_workflows.go` | 18 | 工作流 = 执行路径 |
| `routes_sessions.go` | 17 | 会话 = 执行入口 |
| `routes_turns.go` | 11 | turn = 执行单元 |
| `routes_skills.go` | 10 | 会话级 skill 装配 |
| `routes_gateway.go` | 4 | `/v1/chat/completions`、`/v1/responses`、`/v1/messages`、`/v1/models` |
| `routes_step_track.go` | 4 | agent-steps 轮询/取消/回复/事件 |
| `routes_flow_gateway.go` | 1 | `/v1/gateway/{route}` |
| `routes_steps.go` | 1 | `POST /v1/agent-steps` |
| **合计** | **66** | |

> 判据：这 66 处是「租户用户必须 403、超管放行」的判定点。
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

1. **修两个安全缺口**（§4.1 terminal、§5 fail-open）——独立于多租户，可立即做
2. **§4.2 `/meta` 与 pairing 保持**（明确不动，避免误伤）
3. **relay 信任路径补主体**（§3.3 结构性障碍，先解决它再动 `protected`）
4. **`protected` 改为解析 Principal**（219 处，最大工作量）
5. **抽 `NodeAccess`**、收敛 6 类解析点（§7）
6. **加两条新判定**（403a 跨租户、403b `center_execution`）
7. **落地其余族**（watch scope / bizKey / query-token / `gdx_`）

---

## 9. 与原计划的差异（如实记录）

| 项 | 原以为 | 实际 |
|---|---|---|
| 路由总数 | 284 / 41 文件 | **268 / 39**（原口径含测试与重复计数） |
| `WebToken` 引用 | 123 处 | **35 处非测试** + 85 测试（原数含测试） |
| 「未包裹」路由 | 约 40 条 | **实际 8 条**（5 terminal 缺陷 + 2 有意匿名 + 1 公开）；其余是脚本误判 |
| 鉴权族 | 6 类 | **10 个受鉴权族 + 3 类无鉴权**（原漏 watch `access`、query-token、inline `gdx_`） |
| 中心 agent 面 | 仅 `sessions*`/`gateway*` | 另含 **workflows(18) + skills(10)**，共 66 处 |
| 未列出的风险 | — | **`/v1/terminal/*` 公网无鉴权**、**DELETE 谎报成功**、**fail-open 姿势**（§4.1/§5） |

> 前四项是口径修正；**后三项是新发现的安全问题**，不在原设计范围内，
> 建议独立于 P0 先行处理（改动小、风险明确）。
