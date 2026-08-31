# GoDex 节点接入手册

> 状态：Active（Node Registry、relay 与 `godex node` 接入手册）
> 版本：2026-08-06（对应节点接入产品化 + 删除闭环）
> 适用对象：需要把内网设备上的 GoDex 接入中心服务器的使用者
> 相关功能：中心「节点」页接入引导、`godex node join`、`godex node exec/forward`、节点删除

---

## 1. 概念：中心与节点

GoDex 的「节点」能力把多台机器上的 GoDex 实例连成一张网：

```
┌─────────────────────────┐       出站 WebSocket（节点主动连中心）
│  中心服务器（云端）        │ ◄──────────────────────┐
│  https://godex.claw.carc.top │                        │
│  - 节点注册表 / 心跳        │                        │
│  - Relay 中继（跳板）       │                        │
│  - Web 控制台（网页端）     │                        │
└──────────┬──────────────┘                        │
           │ 经中心代理访问节点                       │
           ▼                                        │
    ┌───────────────┐   ┌───────────────┐   ┌───────────────┐
    │ 内网设备 A     │   │ 内网设备 B     │   │ 内网服务 C     │
    │（跑 GoDex）    │   │（跑 GoDex）    │   │（MySQL:3306）  │
    └───────────────┘   └───────────────┘   └───────────────┘
```

- **中心**：部署在云上的 GoDex 实例（本手册示例 `https://godex.claw.carc.top/`）。
- **节点**：要接入的内网 GoDex 实例（笔记本、台式机、内网服务器等），**主动出站连接中心**，因此不需要开放内网端口、不需要公网 IP。
- **接入的本质**：中心给节点签发一次性凭证（`ck_...`），节点配置好「中心地址 + 凭证 + 节点 ID」后，重启 `godex serve` 即自动注册并建立 Relay 通道。

---

## 2. 快速开始（3 步接入）

| 步骤 | 在哪做 | 做什么 |
|---|---|---|
| ① 生成接入命令 | 中心 Web 控制台「节点」页 | 填节点 ID / 信任级别，点「生成接入命令」，复制 |
| ② 执行接入命令 | 内网节点终端 | 粘贴执行 `godex node join ...` |
| ③ 重启节点 | 内网节点终端 | 重启 `godex serve` |

完成后，中心「节点」页能看到该节点状态为 **online / relay connected**，即可在中心网页端远程操作，或在中心服务器上用 `godex node exec/forward` 访问它。

---

## 3. 详细步骤

### 3.1 中心侧：生成接入命令

1. 浏览器打开中心控制台：`https://godex.claw.carc.top/`
2. 左侧进入 **「节点」** 页（`/nodes`）
3. 顶部 **「接入新节点」** 卡片：
   - **节点 ID**：填写自己易记的名字（如 `my-laptop`、`office-db`）。留空则自动生成 `node_xxxxxx`；**建议填写**，之后用这个名字操作节点
   - **名称**（可选）：给人看的中文/备注名
   - **信任级别**：
     - `trusted`（默认）：完全访问，写操作免审批 —— 适合自家可信设备
     - `guarded-remote`：写操作需在中心网页端审批 —— 适合不完全受控的设备
   - 点 **「生成接入命令」**
4. 页面展示一条命令，点 **「复制」**。

生成的命令形如：

```bash
godex node join 'https://godex.claw.carc.top' --id my-laptop --credential ck_a1b2c3d4e5 --trust trusted --name '我的笔记本'
```

### 3.2 内网节点侧：执行接入命令

在内网设备上打开终端（GoDex 所在机器），直接粘贴执行第 3.1 步复制的命令：

```bash
godex node join 'https://godex.claw.carc.top' --id my-laptop --credential ck_a1b2c3d4e5 --trust trusted --name '我的笔记本'
```

命令会：
1. 校验参数（中心地址必须是 http(s)、凭证必须以 `ck_` 开头、节点 ID 只能含字母/数字/`_`/`-`）
2. 把 `center_url` / `node_id` / `trust_level` 写入该机器的 `godex.yaml`（`control` 段）
3. 把凭证 `ck_...` 写入该机器 home 目录的 `.env`（`GODEX_CONTROL_CREDENTIAL`，敏感信息不落 yaml）
4. 提示下一步

> 提示：重复执行 `node join` 是安全的 —— 只会覆盖以上几个字段，不会动其它配置。

### 3.3 重启节点完成接入

在内网设备上重启 GoDex：

```bash
# 若以服务方式运行
godex service restart

# 或前台重启
godex serve
```

重启后节点自动完成三件事：
1. 以指定的 `node_id`（如 `my-laptop`）注册到中心
2. 携带凭证与中心建立 Relay 通道（**节点出站连接，无需开放内网端口**）
3. 按 `heartbeat_seconds`（默认 15s）周期心跳

### 3.4 验证接入成功

- **中心网页端**：刷新「节点」页，看到 `my-laptop` 状态 **online**，Relay 列为 **connected**（网页「节点」页是查看节点列表的正式入口）
- **命令行**（在中心服务器上，用 `node exec` 验证连通）：

```bash
# 在节点上执行一条命令验证连通
godex node exec --node my-laptop 'echo hello from laptop'
```

看到 `hello from laptop` 即接入成功。

---

## 4. 使用：远程操作节点

### 4.1 网页端远程操作（无需装任何东西）

中心「节点」页 → 点击节点进入详情 → 右上角三个按钮（节点 Relay connected 时可用）：
- **打开聊天**：在中心网页里和该节点上的 GoDex 对话
- **打开终端**：在中心网页里打开该节点上的终端
- **打开文件**：浏览/编辑该节点工作区文件

> 这些操作全部经中心转发（中心作为跳板），目标节点无需公网可达。

### 4.2 命令行：在节点上执行命令

在**中心服务器**上：

```bash
godex node exec --node my-laptop 'cd ~/proj && go test ./...'
godex node exec --node my-laptop --dir /data/app 'ls -la'
```

参数：
- `--node <id>`：目标节点 ID（可用 `control.default_node` 省略，见 4.4）
- `--dir <path>`：节点侧工作目录（默认节点工作区）
- `--center <url>`：中心地址（默认取本机 `control.center_url`）
- `--token <token>`：中心 Web token（默认取本机配置）

### 4.3 命令行：端口转发（访问节点内网服务）

等价 `ssh -L`：把中心/本地端口转发到节点内网里的服务。

```bash
# 把本地 3306 端口转发到节点内网的 10.0.0.5:3306（数据库）
godex node forward --node my-laptop --local 3306 --target 10.0.0.5:3306
```

之后本地 `mysql -h 127.0.0.1 -P 3306` 即可连到内网数据库。

- `--node <id>`：目标节点 ID（可省略，见 4.4）
- `--local <port>`：本地监听端口（默认 3306）
- `--target <host:port>`：节点内网的目标地址
- 节点侧受 `control.forward_allow` 白名单约束（见 6.3），不在白名单一律拒绝

### 4.4 默认节点：省略 --node

设置默认节点后，`node exec/forward` 可不带 `--node`。两种方式（任选其一）：

**方式 A：环境变量（临时/单次）**

```bash
GODEX_CONTROL_DEFAULT_NODE=my-laptop godex node exec 'echo uses default node'
GODEX_CONTROL_DEFAULT_NODE=my-laptop godex node forward --local 3306 --target 10.0.0.5:3306
```

**方式 B：写入中心服务器 `godex.yaml`（持久）**

在中心服务器的 `godex.yaml` 中配置：

```yaml
control:
  default_node: my-laptop
```

之后直接省略 `--node`：

```bash
godex node exec 'echo uses default node'
godex node forward --local 3306 --target 10.0.0.5:3306
```

> 显式 `--node` 始终优先于默认节点。

---

## 5. 节点管理

### 5.1 查看节点

- 网页端：「节点」页实时列表（15s 自动刷新），含状态 / Relay / 最后心跳 / 能力
- 状态含义：
  - `online` + Relay `connected`：正常接入
  - `offline`：超过 `offline_after_seconds`（默认 60s）无心跳 —— 多为节点关机/断网/GoDex 未运行

### 5.2 删除节点

中心「节点」页 → 目标节点行末 **删除** 按钮 → 确认。删除会：

1. 从中心注册表移除该节点
2. **强制断开**它的 Relay 连接
3. 使其凭证作废 —— 即使节点进程还在运行，**心跳与重连都会被拒绝**，不会自己复活

删除后想重新接入：重新走 3.1~3.3 生成新命令并执行即可（重新注册会自动解除删除标记）。

### 5.3 重启规则速查

| 操作 | 中心需要重启吗 | 内网节点需要重启吗 |
|---|---|---|
| 新增节点接入 | ❌ 不用（网页操作热生效） | ✅ 需要（`node join` 后重启一次 `serve`） |
| 删除节点 | ❌ 不用（网页操作热生效） | ❌ 不用（Relay 被强制断开；若进程还在，重连会被拒） |
| 修改节点配置（id/中心地址） | ❌ 不用 | ✅ 需要（改完重启 `serve`） |

---

## 6. 配置参考

### 6.1 内网节点 `godex.yaml` 的 `control` 段（`node join` 写入/修改的字段）

```yaml
control:
  node_name: 我的笔记本        # 给人看的名称（可选）
  node_id: my-laptop          # 节点 ID，注册与操作时使用
  default_node: ""            # 中心侧使用；节点侧一般无需设置
  trust_level: trusted        # trusted | guarded-remote
  center_url: https://godex.claw.carc.top
  heartbeat_seconds: 15       # 心跳间隔（秒）
  offline_after_seconds: 60   # 中心判定离线阈值（秒）
  forward_allow: []           # 端口转发白名单，见 6.3
```

凭证 `ck_...` 不写入 yaml，而是存于该机器 home 目录 `.env`：
`GODEX_CONTROL_CREDENTIAL=ck_...`（也可用环境变量 `GODEX_CONTROL_CENTER_URL` / `GODEX_CONTROL_NODE_ID` / `GODEX_CONTROL_TRUST_LEVEL` 覆盖）。

### 6.2 中心侧「节点」能力

- 中心自身也是一个节点（自注册进自己的注册表），在「节点」页可见
- 中心把节点接入所需能力（聊天 / 终端 / 文件 / 执行等）随注册上报，网页端据此决定可用操作

### 6.3 端口转发白名单 `forward_allow`（可选加固）

节点侧可在 `godex.yaml` 限制端口转发目标，未在白名单的转发一律拒绝：

```yaml
control:
  forward_allow:
    - "10.0.0.5:3306"    # 只允许转发到内网数据库
    - "127.0.0.1:*"      # 允许转发到节点本机任意端口
```

---

## 7. 信任级别说明

| 级别 | 语义 | 适用场景 |
|---|---|---|
| `trusted` | 完全信任：远程操作（含写操作）免审批 | 自家设备、完全可控的内网机器 |
| `guarded-remote` | 受限信任：通过中心代理的**写操作**需在中心网页端逐次审批（带审批头的请求才放行） | 不完全受控/临时接入的设备 |

> 信任级别由接入时确定；删除后重新接入可重新选择。

---

## 8. 常见问题（FAQ）

**Q1：`node join` 报 `invalid node id`？**
节点 ID 只能包含字母、数字、`_` 和 `-`，不能含空格或特殊字符。

**Q2：节点页一直显示 offline？**
1. 确认节点上 `godex serve` 在运行（`node join` 后需要重启一次）
2. 确认节点能出站访问中心（可 `curl https://godex.claw.carc.top/api/meta`）
3. 确认 `.env` 里有 `GODEX_CONTROL_CREDENTIAL=ck_...`
4. 等 1~2 个心跳周期（默认 15s/个）再刷新

**Q3：`node exec` 报 `--node is required`？**
未设默认节点时需显式带 `--node <id>`；或先设置 `control.default_node`（见 4.4：环境变量或中心服务器 `godex.yaml`）。

**Q4：`node exec` 报 `missing center URL`？**
在中心服务器上执行时需要指定中心：`--center https://godex.claw.carc.top`，或在该机 `godex.yaml` 配置 `control.center_url`。

**Q5：删除节点后它又出现了？**
正常接入的节点被删除后不会复活（心跳/重连都被拒绝）。如果又出现，通常是有人**重新执行了 `node join` 并重启** —— 那是重新接入，属预期行为。

**Q6：转发被拒绝（forward 报白名单错误）？**
目标不在节点 `control.forward_allow` 白名单内。按 6.3 配置白名单后重启节点。

**Q7：换中心 / 换凭证怎么改？**
直接在内网节点重新执行新的 `node join` 命令（覆盖旧配置），然后重启 `serve`。旧凭证随即作废。

---

## 9. 相关参考

- 设计文档：`docs/node-mesh-design.md`（节点互联与远程开发设计）
- 端到端冒烟脚本：`scripts/smoke_join.sh`（覆盖接入、指定 ID、默认节点、删除防复活全流程）
- 相关代码：`internal/services/noderegistry/`、`internal/services/relay/`、`internal/runtime/httpapi/httpapi.go`、`cmd/godex/main.go`、`ui/web/src/features/nodes/`
