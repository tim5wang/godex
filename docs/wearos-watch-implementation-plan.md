# GoDex 手表客户端（腕上遥控器）实施方案

> 状态：Draft（实施方案，待评审）
> 日期：2026-10-21
> 关联：`docs/prd-wearos-watch-client.md`（需求与交互设计）、`docs/prd-xiaomi-watch5-compat.md`（设备兼容性）、`docs/public/mockups/wearos/`（15 张 UI 稿）
> 目标设备：小米 Watch 5 国行（Android 底子 / HyperOS 3，**无 GMS**），实现栈按 Wear OS 走，同时覆盖海外 Wear OS 6
> 本文定位：**可直接开工的施工图** —— 契约、文件、签名、分期、验收、验证命令。

---

## 0. 当前交付状态（实证，2026-10-21）

后端与手表 App 主体已落地并通过验证；**仅剩真机门禁 0.4–0.6 需在小米 Watch 5 上确认**。

| 面 | 状态 | 证据 |
|---|---|---|
| 后端 `/watch/*`（W2/W3/W4/W7/W8/W9/W10） | ✅ 已实现 | `internal/runtime/httpapi/routes_watch.go`、`routes_watch_asr.go` |
| 配对与受限 token | ✅ 已实现 | `internal/services/watchauth/watchauth.go` |
| 端到端接口测试（真实 `godex serve`，非 mock） | ✅ **35/35 通过** | `.godex/tmp/e2e/e2e_watch.py` |
| 手表 App（8 屏，圆屏 Compose） | ✅ 已实现并可跑 | `wear/app/src/main/java/com/godex/wear/` |
| 仿真器实机联调（配对→首页→各屏） | ✅ 通过，0 崩溃 | `.godex/tmp/e2e/pair_drive.py`、`verify_screens.py` |
| **稿 06 单条审批屏**（完整命令/风险/两次确认） | ✅ 已实现 + **真实待审批端到端验证** | `wear/.../ui/ApprovalDetailScreen.kt`、`.godex/tmp/e2e/verify_approval_detail.py` |
| **稿 08 通知**（BigText + 拒绝/批准动作） | ✅ 已实现 + **实机通知已验** | `wear/.../notify/WearNotifier.kt`、`WatchActionReceiver.kt` |
| **稿 02 Tile / 稿 12 常驻活动** | 🟡 已实现（渠道/组件已验，渲染待真机 0.4–0.6） | `wear/.../tile/GodexTileService.kt`、`notify/WearNotifier.kt` |
| 稿 01 表盘 + Complication | ⬜ 未做（待真机 0.4–0.6 定形态） | — |
| W1（`noderegistry` 加 `kind`） | ⬜ 未做（当前用 `control` 节点注册表） | — |
| W5/W6（手机桥接推送、事件钩子） | ⬜ 未做（M2 范围）→ 见 §12.4 | — |
| **跨端配套能力（配对 UI / 精简回复 / TTS / 通知投递 / 工具日志）** | ⬜ 未做 → **见 §12** | — |

### 0.1 联调中发现并修复的两个真实缺陷

两者都是**单元测试测不到、只有把 App 跑在 Android 上才会暴露**的问题，已修复并回归：

1. **Android 9+ 默认禁明文 HTTP** → 配对直接失败
   （`CLEARTEXT communication to 10.0.2.2 not permitted`）。
   修复：新增 `res/xml/network_security_config.xml` 并在 manifest 挂载；
   同时把默认地址与用户输入统一补 `/api` 前缀
   （外部请求必须带 `/api`，webui 会剥离后转给 API handler；
   不带则落到 Web UI 返回 SPA HTML）。
2. **配对成功后 `base_url` 未持久化** → 重启后永远卡在配对页。
   `Credentials.isUsable` 要求 `paired && token && baseUrl` 三者齐备，
   而 `savePairedDevice` 原先只写 token/scopes/paired，漏了 base URL。
   修复：`savePairedDevice` 接收并归一化写回 `base_url`。
   实证方式为 `adb shell run-as com.godex.wear cat files/datastore/godex_wear.preferences_pb`。

### 0.2 已知限制（非缺陷）

- 测试服务上 `GET /watch/sessions` 对「已建但无 turn」的会话返回空列表：
  **主 API `GET /sessions` 行为一致**，属会话存储层语义，非 watch 路由问题。
- 无会话时「说话」按钮按设计为禁用态（`enabled = currentSessionId.isNotEmpty()`）。

---

## 1. 目标与交付范围

| 交付物 | 内容 |
|---|---|
| **A. godex 后端集成面** | W1–W9（§4）：设备类型、配对签发、腕上摘要、审批聚合、手机桥接推送、事件钩子、受限 token、语音落地 |
| **B. 手表 App（新）** | Kotlin + Compose for Wear OS，15 张稿中的 12 张直接可用 + 3 张降级路径（§5） |
| **C. 手机端桥接改造** | 复用 `mobile/` 现有通知能力，把 godex 事件镜像到手表通知栏（§6，**无 FCM 的唯一通路**） |
| **D. 验证与验收** | 真机门禁（§3）+ 分期验收（§7）+ 测试策略（§8） |

**明确不做**：手表端不跑 agent、不读写文件、不执行工具、不做终端/diff 审阅；不改 taskboard / Flow 账本状态机。

---

## 2. 仓库布局

```
godex/
├── internal/
│   ├── services/
│   │   ├── noderegistry/          # [改] 加 endpoint kind（W1，未做）
│   │   └── watchauth/             # [新] 手表设备与受限 token（W2/W7）✅
│   ├── runtime/httpapi/
│   │   ├── routes_watch.go        # [新] /watch/* 全部路由 + withWatchScope 中间件 ✅
│   │   ├── routes_watch_asr.go    # [新] W9 语音转写（WAV 解析 + 转写桥）✅
│   │   ├── routes_watch_test.go   # [新] 路由与 scope 测试 ✅
│   │   └── httpapi.go             # [改] 装配注册 ✅
│   └── plugins/taskboard/plugin.go# [改] 卡片状态迁移 → 事件（W6，handler 层，未做）
├── mobile/                        # [改] 通知桥接到手表（§6，未做）
└── wear/                          # [新] 手表 App（Gradle 独立工程）✅
    ├── settings.gradle.kts
    ├── gradle/libs.versions.toml
    └── app/src/main/
        ├── AndroidManifest.xml    # + network_security_config（明文 HTTP 必需）
        ├── java/com/godex/wear/
        │   ├── GodexWearApp.kt
        │   ├── MainActivity.kt    # 单 Activity + 状态式导航
        │   ├── data/              # WatchApi / Dto / WatchStore(datastore)
        │   ├── ui/                # 7 屏：Home/Sessions/Approvals/Devices/Talk/Settings/Pairing + Components/Theme
        │   └── voice/             # VoiceCapture（录音 + 权限）
        └── res/xml/               # network_security_config.xml
```

> 实现期调整：`withWatchScope` 直接放在 `routes_watch.go`（未单独建 `watchauth.go`）；
> `domain/` 与 `tile/` 未建 —— 状态集中在 `ui/WatchViewModel.kt`，Tile 留待真机 0.4–0.6 结论。
> `wear/.gitignore` 沿用 `mobile/.gitignore` 惯例：骨架入库、构建产物与本机 `local.properties` 忽略。

> `wear/` 放顶层（与 `mobile/`、`desktop/` 同级），独立 Gradle 工程，不进 Go 构建。

---

## 3. 阶段 0：真机门禁（**阻断项，先做，0.5 人日**）

### 3.0 无线调试工具（已就绪）

**无需安装新工具**：Android SDK 自带 **`adb 37.0.1`**（`~/Library/Android/sdk/platform-tools/adb`），原生支持
无线调试（`adb pair` / `connect` / `mdns`）。只是**不在 PATH 上**，故封装为：

```bash
bash scripts/watch-adb.sh --help      # 查看全部子命令
```

| 子命令 | 用途 |
|---|---|
| `discover` | mDNS 自动发现同 WiFi 下的手表（配对/连接地址） |
| `pair <IP:端口> <配对码>` | 配对（也可自动发现地址） |
| `connect [IP:端口]` | 连接（缺省自动取 connect 端口） |
| `status` | 列出设备 + **自动跑门禁 0.1（GMS 检查）**与型号/API |
| `install [apk]` | 装包（缺省找 `wear/app/.../app-debug.apk`） |
| `shot <out.png>` | 截图拉回本地 |
| `logcat` | 过滤 godex/FATAL 日志 |
| **`net <IP:端口>`** | **网络诊断**：路由/接口/ARP/TCP 探测，判定是否被 VPN 隧道劫持 |
| **`up <配对IP:端口> <码> [连接IP:端口]`** | **一键**：重启 adb server → 配对 → 连接 → 体检（推荐） |
| **`try <IP:端口>`** | 直接 connect（已配对过 / 端口即连接端口时） |
| `path` | 打印 PATH 配置建议 |

**手表侧一次性准备**：设置 → 关于手表 → 连点「版本号」7 次 → 开发者选项 →
打开「**无线调试**」→「使用配对码配对设备」→ 记录 `IP:端口` + 6 位配对码。

> 首条命令（一段完成）：
> 手表弹窗开启后，**30 秒内**跑：
> ```bash
> bash scripts/watch-adb.sh up <弹窗里的IP:端口> <6位配对码> <主页面IP:端口>
> ```
> 该命令会重启 adb server（清 VPN 残留）、配对、连接并自动体检（含 GMS/API 结论）。

> ⚠️ **实测陷阱（2026-10-21 已复现）**：公司 VPN（如 Cisco AnyConnect）会**接管局域网路由**——
> 本机与手表同在 `192.168.0.0/24`，但 `192.168.0` 的路由指向 VPN 隧道（`utun4`），
> 导致 `adb pair` 报 `protocol fault (couldn't read status message)`、ping/TCP 全部超时。
> **这不是配对码或端口问题。** 先跑 `bash scripts/watch-adb.sh net <IP:端口>` 确认；
> 解法见下方「无线调试网络要求」。

> ⚠️ **第二个坑：两个端口不一样，且都会过期**（同日实测）：
> - 「无线调试」**主页面**显示的 `IP:端口` = **连接端口**（给 `adb connect`）
> - 「**使用配对码配对设备**」**弹窗里**的 `IP:端口` = **配对端口**（给 `adb pair`，通常与上面不同）
> - 配对弹窗关闭后该端口**立即停止监听**（约 5 分钟），再用旧端口会继续报 `protocol fault`——
>   此时 `net` 诊断会显示 ping 通、但端口 closed。**重新打开弹窗取新码新端口即可。**

**无线调试网络要求**（三条，按推荐度排序）：

| # | 方案 | 说明 |
|---|---|---|
| 1 | **手机热点**（推荐） | Mac 与手表同时连同一个热点 → 得到 VPN 未接管的子网（如 `172.20.10.x`），配对+调试全程可用 |
| 2 | 加主机路由 | `sudo route add -host <手表IP> -interface en0`（需密码；**VPN 重连后失效**，需重加） |
| 3 | 临时断开公司 VPN | 最直接，但受合规约束 |

### 3.0b 本地仿真环境（已装好，绕开真机网络问题）

真机无线调试被企业网络阻断时，**仿真器走 loopback，不受影响**，可独立完成样式/交互/依赖验证。

```bash
bash scripts/watch-emulator.sh --help
```

| 子命令 | 用途 |
|---|---|
| `create` | 幂等创建 AVD（装 emulator + 系统镜像，写圆屏/软渲染配置） |
| `start [-w]` | 后台启动（headless，软渲染）；`-w` 等开机完成 |
| `stop` / `reset` | 停止 / 冷启清数据 |
| `status` | 设备与配置概览（API/指纹/镜像/屏幕/宿主连通） |
| **`gates`** | **跑门禁 0.1 / 0.2 / 0.3 / 0.7** |
| **`nogms`** / `withgms` | **禁用 / 恢复 Google 包**（复现无 GMS 基线） |
| `shot <out.png>` | 截图 |

**已装资源（本机实测）**：

| 项 | 值 |
|---|---|
| 仿真器 | emulator 37.2.12.0 |
| 镜像 | `system-images;android-30;android-wear-cn;arm64-v8a` |
| AVD | `godex_wear_cn`（`wearos_large_round`） |
| 屏幕 | **454×454**，density 320，**circular=yes** |
| ABI | **arm64-v8a**（与小米 Watch 5 的骁龙 W5 一致 ✔） |
| 宿主连通 | `10.0.2.2` 可达 ✔（仿真器内可直接访问宿主上的 godex 服务） |

> ⚠️ **重要发现：「中国版」镜像也内置 GMS**（指纹 `google/sdk_gwear_sw_arm64`，含
> `com.google.android.gms` / `com.android.vending` / `com.google.android.gsf`）。
> 它**不是**无谷歌框架镜像。要复现无 GMS 基线，必须跑：
> `bash scripts/watch-emulator.sh nogms`（`pm disable-user` 可逆，`withgms` 恢复）。

### 3.1 门禁清单

> **决策（2026-10-21，用户拍板）**：**按「无 GMS」为基线实现**，调试环境问题后补。
> 因此下表 **0.1 已从「待验证」降为「基线假设」**，不再是阻断项；FCM 不进入一期设计。
> 若后续实测发现**有** GMS，属于**增量增强**（可加 FCM 直推），不影响已经写下的任何代码路径。

| # | 验证 | 状态 | 命令 / 方法 | 为否时的处置 |
|---|---|---|---|---|
| 0.1 | **有无 GMS** | **已定基线：无 GMS** | `bash scripts/watch-emulator.sh gates` | 推送只走手机桥接（**已定为基线**）；仿真器需先跑 `nogms` |
| 0.2 | **API level / Wear 版本** | ✅ **仿真器已验：API 30 / circular=true** | `bash scripts/watch-emulator.sh gates` | **`minSdk 30` 已由仿真器验证**（Wear OS 3 基线）；真机待复核 |
| 0.3 | **侧载通路** | ✅ **仿真器已验：可用** | `bash scripts/watch-emulator.sh gates` | 实测装 112MB APK 成功（1.8s）；真机待复核 |
| 0.4 | **Tile 渲染** | 待验（需真机） | 装官方 Wear Tile 示例，表盘左滑轮播查看 | 不渲染 → 稿件 02 降级为「表冠直达首页」 |
| 0.5 | **Complication 渲染** | 待验（需真机） | 官方示例挂 complication 到表盘 | 不渲染 → 稿件 01 降级为应用内聚合页 |
| 0.6 | **Ongoing Activity** | 待验（需真机） | 官方示例实测 | 不生效 → 稿件 12 降级为常驻通知 |
| 0.7 | **语音识别可用性** | ⚠️ **仿真器已验：无 provider** | `bash scripts/watch-emulator.sh gates` | **仿真器无 `RecognitionService`** → **W9（录音 + 服务端 ASR）定为语音主路径**；真机若自带厂商语音则可作快路径 |
| 0.8 | **网络可达** | ✅ **仿真器：宿主 `10.0.2.2` 可达** | 仿真器内访问宿主 godex 服务 | 真机需另验（WiFi/蜂窝/eSIM） |

> **仿真器实测结论（2026-10-21）**：
> - **0.2 已定：`minSdk 30`**（仿真器 API 30 / Wear OS 3 基线）；
> - **0.3 已定：侧载可用**（112MB APK 安装 Success）；
> - **0.7 有结论（但有边界）**：仿真器**未找到任何 `RecognitionService`**（即使镜像带 GMS）
>   → `SpeechRecognizer` 在仿真环境不可用；而麦克风硬件特性存在（`android.hardware.microphone`）
>   → **录音可用**。
>   ⚠️ **注意边界**：仿真器缺识别器**不等于**小米真机也缺（真机可能有厂商自研语音服务）。
>   因此实施策略：**W9（录音 + voice-engine ASR）定为语音主路径**，
>   `SpeechRecognizer` 保留为「运行时探测，可用则优先」的快路径，不依赖它、也不删它。
> - 0.4/0.5/0.6（Tile / Complication / Ongoing Activity）属**系统壳渲染行为**，
>   仿真器与 HyperOS 真实表端不一致，**必须真机验**；每项均有降级路径。

**门禁定位调整**：原为「阻断项，写码前必须全过」，**现降级为非阻断**——

- **0.1 已定基线**（无 GMS），不再是未知数；
- **0.2 / 0.3 已由仿真器验证**（`minSdk 30`、侧载可用），真机仅需复核而非探索；
- **0.7 已由仿真器给出结论**（无语音 provider）→ W9 升为必需路径；
- **0.4–0.6** 属系统壳渲染行为，只影响 3 张稿（01/02/12）的形态，**每个都已有降级路径**，待真机验；

> 结论：**M1 不需要等门禁。** 待调试环境打通后一次性补验，把「暂定值」替换成实测值即可。

**可选增强**：屏幕镜像用 **scrcpy**（`--no-audio` 适于低功耗设备）看实时手势与功耗表现。
⚠️ 本机 Homebrew 为 x86_64 版且 Rosetta 不可用，**无法 `brew install`**；
改从 GitHub 直下 `scrcpy-macos-aarch64-*.tar.gz` 解压即用（源：`github.com/Genymobile/scrcpy/releases`）。

---

## 4. 后端集成面（W1–W9）

### W1 · 设备类型：`noderegistry` 加 `kind`

**文件**：`internal/services/noderegistry/registry.go`

现有结构（已核实 `registry.go:31-61`）已有 `TrustLevel` / `Capabilities` / `Metadata` / `Source`。

```go
type NodeInput struct {
    // ... 现有字段不动
    Kind string `json:"kind,omitempty"` // pc | cloud | phone
}
type NodeView struct {
    // ... 现有字段不动
    Kind string `json:"kind,omitempty"`
}
```

- 各实例通过配置声明自身类型（手机端 App 设 `phone`，服务器 `cloud`，桌面 `pc`）；默认 `pc`。
- **手表不进 noderegistry**：手表是**客户端**，不是被遥控的目标端（目标端才有 agent）。手表的设备身份在 W2 的 `watchauth` 里。
- 校验：只接受 `pc|cloud|phone`，非法值回退默认并记日志。

### W2 · 配对签发（RFC 8628 式）

**文件**：`internal/runtime/httpapi/routes_watch.go`（新）、`internal/services/watchauth/`（新）

对齐既有先例 `registerSelfJoinRoute`（`routes_self_join.go:39+`：校验 → 注册 → 换受限凭证 → 落盘）：

```
POST /watch/pair/start                                  [无鉴权，设备自身发起]
  → {device_code, user_code(8位), verification_uri, expires_in:300, interval:5}

POST /watch/pair/approve                                [protected：手机/Web 端]
  body {user_code, device_name, scopes:["watch:read","watch:command","watch:approve"]}
  → {ok:true, device_id}

POST /watch/pair/token                                  [无鉴权，手表轮询]
  body {device_code}
  → {status:"pending"} 
  | {status:"approved", device_id, access_token:"wk_...", refresh_token, scopes, expires_at}
```

**存储**：`<godex_home>/watch/devices.json`

```go
type WatchDevice struct {
    DeviceID     string    `json:"device_id"`      // idgen.New("wch_", 4)
    Name         string    `json:"name"`
    Scopes       []string  `json:"scopes"`
    CredentialHash string  `json:"credential_hash"` // sha256(access_token)，不落明文
    RefreshHash  string    `json:"refresh_hash"`
    CreatedAt    time.Time `json:"created_at"`
    LastSeen     time.Time `json:"last_seen,omitempty"`
    Revoked      bool      `json:"revoked,omitempty"`
}
```

**约束**：
- `device_code` / `user_code` 一次性、5 分钟过期、未授权前不签发任何凭证；
- token 永不含 `watch:admin`（管理类操作一律 403）；
- 配对成功/失败、token 轮询都记审计日志（对齐 `routes_self_join.go:66` 的 `audit` 风格）；
- 配置项（`watch.enabled` / `watch.token_ttl` 等）须按既有 **schema + setter + stored/effective** 契约登记，否则 Web 配置面板写不进去。

### W3 · 腕上摘要（Tile / Complication 一次拿全）

```
GET /watch/overview        [watch:read]
  → {
      generated_at, target_device_id,
      running: 2, pending_approvals: 1,
      nodes_online: 4, nodes_total: 5,
      devices: [{id,name,kind,status,trust_level,running,pending_approvals,last_seen}],
      latest_event: {kind,title,at}          // 一句话，给 Tile 用
    }
```

**设计要点**：字段为「腕上一次渲染」而收敛——**单次请求、单体对象、无分页、体积 < 4KB**（Tile 不可滚动、刷新不可控）。内部聚合 `noderegistry.List` + 各端 pending 计数。

### W4 · 审批聚合队列 + 动作

```
GET  /watch/approvals                 [watch:read]
  → {items:[{
        id, kind:"permission"|"human_task",
        device_id, session_id, session_title,
        request_id, tool, command,
        risk:"high"|"normal",              // 高危 → 腕上要求长按确认
        created_at, expires_at
     }]}
POST /watch/approvals/{id}/approve    [watch:approve]  body {scope:"once"|"session"|"task"}
POST /watch/approvals/{id}/deny       [watch:approve]
```

**聚合来源（两个既有接口，已核实）**：
- `GET /sessions/{id}/permissions` → 工具权限请求；
- `GET /v1/human-tasks?queue=&status=`（`routes_flows.go:466`，`service.ListHumanTasks`）→ Flow 人工卡点。

**动作路由**：动作端点只做「查 id → 定位所属 device → 经中心代理转发到该节点」，复用现有：
- 权限：`POST /sessions/{id}/permissions/{requestID}/approve|deny`（`routes_workflows.go:197`，`service.ApprovePermission(ctx, sessionID, requestID, scope)`，`scope` 类型 `tools.PermissionGrantScope`）；
- Flow 人工：`POST /v1/flow-runs/{runID}/human/{nodeID}/reply`。

**幂等**：已被别端处理 → 返回 `409` + 当前状态，腕上据此纠偏（PRD §7.2）。

> ⚠️ **`guarded-remote` 节点**：经中心代理的**写操作**需带审批头（`internal/services/relay/proxy.go:123-126` 实证）。腕上动作转发时必须带上，否则 403。

### W5 · 手机桥接推送（**无 FCM 的唯一通路**）

**后端**：不做推送网关，只出「待推送事件」队列，供手机端拉取：

```
GET /watch/push/pending?since=<cursor>&device_id=<watch>   [watch:read]
  → {cursor, events:[{id,kind,title,body,device_id,session_id,created_at}]}
```

- 队列**内存 + 有界**（如最近 200 条 / 24h），不写入节点账本；
- 手机端确认投递后 `POST /watch/push/ack {ids}`（可选，凭 cursors 前进即可）。

### W6 · 事件钩子（旁路，不动账本）

**已实证结论（2026-10-21 读码；同日 B2 落地复核后修订）**：4 类事件**全部走差异派生**，无一处真钩子（原判定的「唯一真钩子」经复核不成立，见下）。

| 事件 | 触发点 | 实现方式 |
|---|---|---|
| `card_finished` | 看板卡片离开 `in_progress`（进入 `in_review`，或经验收进入 `done`） | ✅ **已落地（B2）= 派生**：`collectFinishedCards` 读 `ledger.ListCards` 取 `in_review`/`done` 卡片。**未按原计划挂 handler 钩子**，原因见下 |
| `approval_requested` | 工具权限闸：`toolruntime/permissions.go:784 ensurePendingLocked`（新 pending 注册点） | ✅ **不用钩子**：直接从 W4 的 `/watch/approvals` 聚合结果做**前后差异**派生（`ListPending` `:477`） |
| `flow_waiting_human` | Flow 人工卡点 | ✅ **不用钩子**：与 `ListHumanTasks`（`routes_flows.go:466`）同源，同样差异派生 |
| `turn_finished` | 会话 turn 结束 | ✅ **不用钩子**：复用 `mobile/` 现有 `running true→false` 轮询判定 |

**为何选择「派生」而非「钩子」**：`ensurePendingLocked` 在 `PermissionManager` 的互斥锁内（`m.mu.Lock()`，见 `permissions.go:784`），在其中发射外部事件有阻塞与重入风险；而 W5 本就是**拉取型队列**，差异派生零侵入、无锁竞争、可测试。

**为何 `card_finished` 也改为派生（原计划的「唯一真钩子」不成立，2026-10-21 复核）**：
- 原计划称 `routes.go:160 handlePatchCard` 是「唯一插入点」。**不成立**：`MoveCard` 在生产代码有**两个**调用者——`routes.go:180`（Web handler）与 `tool_actions.go:107`（agent 的 taskboard 工具）。后者正是看板主路径（执行会话收尾自己的卡），只挂 handler 会漏掉**最该通知**的迁移；
- 插件（`cmd/godex` 装配）到推送队列（`registerWatchRoutes` 内部创建）**不存在接线缝**，这正是另外 3 类事件当初选「派生」的同一原因；
- 派生是**精确**而非近似：`in_review` 只能由 `in_progress` 进入，`done` 只能由 `in_review` 进入（`allowedTransitions` + `CompleteCard`），故「当前是 `in_review`/`done`」等价于「已离开 `in_progress`」——无需记录前态，**不改账本状态机**。

**`card_finished` 落地边界（均有测试锚点）**：
- `in_progress`（含执行**失败**：`FinishExecution` 只关掉执行，卡片仍停在 `in_progress`）不算完成，不通知；
- 评审活动（评论 / 勾选清单）会 bump 版本，**不重复通知**；
- 验收（`in_review → done`）是**第二次**完成、另一套文案。此处落地时修掉一个真缺陷：该步执行 id 不变（离开 `in_progress` 时执行已收尾），故 `finishAnchor` 把**状态**纳入身份，否则验收会被静默吞掉、且「已完成」文案永不可达；
- 重新派发（新执行）是新完成，会再次通知。

统一发到 W5 队列（内存 + 有界）；**未**记入 `relay.EventStore`——该 store 聚合的是节点观测快照，与通知队列语义不同。

> ✅ 原「待确认的两处代码点」已全部实证完毕，锚点见 §11。**始终不得改动账本状态机**。

### W7 · 受限 token 中间件

**文件**：`internal/runtime/httpapi/watchauth.go`（新）

对齐既有 `nodeProxyTokenChecker`（`httpapi.go:381-389`）的形态：

```go
// withWatchScope 校验 wk_ token 且要求指定 scope；越权 403。
func withWatchScope(manager *config.Manager, watchAuth *watchauth.Service, scope string, next http.Handler) http.Handler
```

- 校验：`Authorization: Bearer wk_...` → sha256 比对 `CredentialHash` → `Revoked`/过期检查 → scope 检查；
- 刷新 `LastSeen`；
- 装配位置：`httpapi.go` 的 `registerWatchRoutes(...)`，紧邻 `registerControlNodeRoutes`（`httpapi.go:143`）。

### W8 · 语音落地（指令发送）

```
POST /watch/command        [watch:command]
  body {device_id, session_id?, text, mode:"append"|"agent_step"}
  → {accepted:true, turn_id?, queued:bool, session_id}
```

- `mode=append`：复用既有会话链路 `POST /sessions/{id}/messages`（`routes_turns.go:128`）；
- `mode=agent_step`：走 `POST /v1/agent-steps`（`biz_` 认证面，复用）；
- **对齐既有「发送一律排队」语义**：会话 `running` 时返回 `queued:true`，腕上显示「已排队」并提供 `POST /sessions/{id}/queued/{queueID}/steer|cancel`。

### W9 · 语音转文字（**主路径**）

```
POST /watch/asr            [watch:command]   multipart: audio(PCM/WAV) → {text, confidence}
```

**主路径（已定为默认）**：手表录音（`MediaRecorder`；仿真器已验 `android.hardware.microphone` 存在）
→ 上传本端点 → 转发既有 voice-engine ASR（`internal/services/backend` + `routes_voice.go` 的桥）。

**快路径（运行时探测，可用则优先）**：若 `SpeechRecognizer.isRecognitionAvailable()` 为真，
直接在表上识别、**不走后端**（省一次往返与流量）；失败则自动回退主路径。

> 为何主路径改为录音：仿真器实测**无任何 `RecognitionService`**（见 §3.1 门禁 0.7），
> 说明 `SpeechRecognizer` 在无谷歌/厂商语音服务的环境里不可靠；而录音只依赖硬件麦克风。
> 保留快路径是**不放弃可能的省电优化**，而非依赖。

---

## 5. 手表 App 架构

### 5.1 依赖清单（守「少外部依赖 · 无 GMS」）

| 用途 | 选型 | 备注 |
|---|---|---|
| UI | `androidx.wear.compose:compose-material` + `compose-foundation` | 官方 |
| Tile | `androidx.wear.tiles:tiles` | **待 0.4 验证** |
| Complication | `androidx.wear:wear-complications-data-source` | **待 0.5 验证**，可选 |
| 网络 | OkHttp + kotlinx.serialization | 单依赖，纯 JVM 库 |
| 存储 | `androidx.datastore:datastore-preferences` | token / 偏好 / 置顶会话 |
| 语音 | `android.speech.SpeechRecognizer` / `MediaRecorder` | **不用 GMS** |
| ❌ 禁止 | `play-services-*`、任何需 GMS 的库 | 国行装不上/跑不起来 |

### 5.2 页面 ↔ 稿件 ↔ 数据源

| 稿 | 页面 | 主要数据源 |
|---|---|---|
| 03 | HomeScreen（按住说话 + 三圆钮） | `/watch/overview` + 本地当前设备/会话 |
| 04 | TaskListScreen | 当前设备 `/sessions`（经中心代理） |
| 05 | TaskDetailScreen | `GET /sessions/{id}` + `/sessions/{id}/longtasks` |
| 06 | ApprovalScreen | `/watch/approvals/{id}` |
| 07 | VoiceScreen | 本机 ASR（兜底 `/watch/asr`） |
| 08 | Notification 动作 | `/watch/approvals/{id}/approve\|deny` |
| 09 | DeviceListScreen | `/watch/overview`.devices |
| 10 | PairingScreen | `/watch/pair/start` + `/watch/pair/token` |
| 11 | SettingsScreen | 本地 DataStore |
| 12 | OngoingActivity | `NotificationCompat`（降级） |
| 13 | SessionSwitcher | 当前设备 `/sessions` + 本地置顶 |
| 14 | CrossDeviceList | `/watch/overview`.devices + 各端 `/sessions` |
| 15 | ApprovalQueue | `/watch/approvals` |

### 5.3 两个正交状态（PRD §7.4）

```kotlin
// DataStore 持久化
data class RemoteState(
    val targetDeviceId: String?,   // 当前遥控目标（可锁定）
    val locked: Boolean = false,   // 手动锁定后不自动跟随
    val sessionByDevice: Map<String, String>, // device_id -> session_id
    val pinnedSessions: List<PinnedSession>   // 置顶（跨设备可用）
)
```

- **切设备** → 换 `targetDeviceId`，清空/替换该设备的会话缓存，自动选最近活跃会话；
- **切会话** → 只改 `sessionByDevice[target]`；
- 顶部常显设备、底部常显会话（稿件 03）。

### 5.4 交互硬约束（全平台通用）

- **禁止水平滑动承载业务语义**（Wear OS 右滑=返回；小米右滑=负一屏、左滑=卡片）；
- 滚动用旋转表冠（`ScalingLazyColumn` + `PositionIndicator`）；
- **返回用显式顶部控件**（小米不能依赖系统右滑）；
- 触控 ≥48dp（圆钮 62dp、主按钮 78dp）；圆屏安全区外圈 24px 不可点。

### 5.5 低功耗实现要点

- **无后台长连、无后台轮询**：只在页面可见时请求；退到后台立即停；
- Tile：进入视野刷新 + 手动刷新，后台最多 30min 一次；
- Complication：短文本 / `RANGED_VALUE`，随亮屏刷新；
- AOD：只显示「运行 N / 待决 M」纯文本，无动画；
- 网络请求带短超时 + 本地缓存先渲染（HA favorites 经验）。

---

## 6. 手机桥接推送链路（无 FCM）

```
godex（事件，W6） → 待推送队列（W5）
        ↓  手机端轮询 GET /watch/push/pending
手机 godex App（mobile/，Capacitor）
        ↓  发一条普通 Android 本地通知
Android 系统自动桥接到配对手表
        ↓  用户点通知
手表 App 打开并 deep link 定位（device_id + session_id）
```

**改造点**（`mobile/`）：
- `mobile/public/godex-watcher.js`：现有逻辑是轮询 `/api/sessions` 判 `running true→false` → `LocalNotifications.schedule(...)`。**保留该逻辑，新增**轮询 `/watch/push/pending` 并把事件投递为本地通知；
- 通知携带 deep link data（`device_id` / `session_id`），手表端解析后直达对应页面（PRD §7.6）；
- 审批类通知尽量用 `NotificationCompat.Action`（批准/拒绝）——**原生 Android 能力，不依赖 GMS**；若手表侧桥接不保留动作按钮，则在手表 App 内做（稿件 06），兜底路径已存在。

> 该链路正是 Home Assistant Wear OS 的默认通路（`NotificationManager` 自动桥接），已被验证可行。

---

## 7. 分期计划与验收

| 阶段 | 范围 | 后端 | 手表 | 验收（对应 PRD G1–G10） |
|---|---|---|---|---|
| **P0 门禁** | **已降为非阻断**：0.1 已定基线（无 GMS）；0.2/0.3/0.7 已由仿真器验证；0.4–0.6 待真机 | — | — | 不再阻塞 M1；仅 3 张稿（01/02/12）待真机定形态 |
| **M1 只读遥控** | 配对 + 主屏 + 任务列表/详情 + 设备切换 + 会话切换 | W1/W2/W3/W7 | 03/04/05/09/10/11/13/14 | 绑一次即用；切设备后会话列表跟随；切会话后语音落点正确；无后台长连 |
| **M2 通知与审批** | 审批整屏 + 待审批队列 + 通知动作 | W4/W5/W6 | 06/08/15 | 审批创建 ≤5s 到腕（**经手机桥接**）；腕上批准后 PC 侧继续；他端已处理时腕上 409 纠偏 |
| **M3 指令与 Tile** | 语音输入 + Tile + 常驻活动 | W8/W9 | 07/02/12 | 语音落到正确目标并有回执；running 时显示「已排队」可引导/取消；Tile 按 0.4 结论落地或降级 |
| **M4 打磨** | 表盘聚合（若 0.5 通过）、侧载流程固化、近场增强（可选） | — | 01 | 无 GMS 环境可用；耗电达标（G6） |

**关键路径**：**W2/W7（配对与鉴权）→ M1**。**M2 依赖 W5 手机桥接，是最大不确定性**（需与手机端改造协同）。

> **P0 门禁已不再阻塞**：0.2（`minSdk 30`）/ 0.3（侧载）/ 0.7（语音走 W9）均已定论，
> 仅 0.4–0.6 需真机定形态且各有降级路径。可直接开工 M1。

---

## 8. 测试与验证策略

### 8.1 后端（Go）

```bash
# 单元 + 路由
go test ./internal/services/watchauth/... ./internal/services/noderegistry/...
go test ./internal/runtime/httpapi/ -run 'Watch|Pair|Approval|Overview'
go test ./internal/plugins/taskboard/...

# 项目门禁
make verify
```

**必须覆盖的用例**：
- 配对：未授权不签发；过期 `device_code` 拒绝；`user_code` 错误 3 次后失效；
- 鉴权：`wk_` 缺失/过期/已吊销 → 401；scope 不足 → 403；`watch:admin` 永不签发；
- 审批：批准 → 目标节点收到；重复批准 → 409；`guarded-remote` 节点写操作带头成功、不带头 403；
- 摘要：`/watch/overview` 响应体积 < 4KB、字段完整；
- 事件：卡片进 `in_review`/`done` → 队列出现事件；**taskboard 账本行为回归不变**（既有测试必须全绿，`TestFlowHumanTaskLifecycle` 等顺序 flake 属既有）。

### 8.2 手表 App（Kotlin）

```bash
cd wear && ./gradlew :app:assembleDebug      # 构建
./gradlew :app:testDebugUnitTest             # 纯 JVM 单测（DTO/状态机/URL 构造）
adb install -r app/build/outputs/apk/debug/app-debug.apk
adb shell am start -n com.godex.wear/.MainActivity
```

**真机手工清单**（每条对应一个验收点）：

| # | 操作 | 期望 |
|---|---|---|
| 1 | 首次启动 → 出码 → 手机批准 | 5 秒内进主屏，显示账号与设备 |
| 2 | 顶部 chip → 切换设备 | 列表整体刷新；离线设备置灰并有说明 |
| 3 | 底部胶囊 → 切会话 | 语音默认落到新会话 |
| 4 | 按住说话 → 松开 | 文本正确、目标可见、有回执 |
| 5 | 会话 running 时发送 | 显示「已排队」+ 引导/取消 |
| 6 | PC 触发高危命令 | ≤5s 腕上收到通知；长按批准后 PC 继续 |
| 7 | 手机端先批准 | 腕上操作 → 提示「已在其它设备处理」并移除该条 |
| 8 | 息屏 30 分钟后再抬腕 | 无异常耗电（`adb shell dumpsys batterystats` 对比基线） |

---

## 9. 风险与决策清单

| # | 风险 / 待决 | 影响 | 处置 |
|---|---|---|---|
| 1 | ~~GMS 有无未实测~~ | 推送架构 | ✅ **已定基线（2026-10-21 用户拍板）**：**按无 GMS 实现**，一期只用手机桥接，不引入 `play-services-*`；若后续实测确实有 GMS，FCM 作为**增量增强**补上，不改已有代码路径 |
| 2 | **Tile / Complication 渲染未验证**（0.4/0.5） | 3 张稿形态 | 均设计为可降级；**不阻塞 M1** |
| 3 | **手机桥接需与 `mobile/` 协同** | M2 关键路径 | 优先锁定该改造的责任人与时间 |
| 4 | `SpeechRecognizer` 在无谷歌/厂商语音服务环境无 provider（**仿真器已实证**） | 稿件 07 | **W9 服务端 ASR 已定为语音主路径**；`SpeechRecognizer` 仅作运行时探测的快路径 |
| 5 | `guarded-remote` 节点写操作需审批头 | 腕上动作 403 | W4 转发时带头；并在 UI 提前提示（PRD §7.5） |
| 6 | ~~待确认代码点：`Ledger.MoveCard` 函数体、agent 权限请求创建点~~ | W6 钩子位置 | ✅ **已实证解除并落地**（2026-10-21）：4 事件**全部**改为差异派生。原判定「仅 `card_finished` 需钩子」经复核**不成立**（`MoveCard` 另有 agent 工具调用点，且插件无到队列的接线缝），`card_finished` 亦改为派生（§4 W6），锚点见 §11 |
| 7 | 配置项需按 schema/setter/stored/effective 契约登记 | Web 配置面板 | W2 落地时一并登记，勿遗漏 |
| 8 | 手表 App 放 `wear/` 顶层（待确认） | 仓库结构 | 如需与 `mobile/` 合并可调整 |

---

## 10. 工作量估算

| 工作包 | 人日 |
|---|---|
| P0 真机门禁 | 0.5 |
| W1 设备类型 | 0.5 |
| W2 配对签发 + 存储 + 测试 | 3 |
| W3 腕上摘要 | 1 |
| W4 审批聚合 + 动作转发 | 3 |
| W5 推送队列 + 手机端桥接改造 | 3 |
| W6 事件钩子（1 处真钩子 + 3 类差异派生） | 1.5 |
| W7 受限 token 中间件 | 1 |
| W8/W9 指令与 ASR（录音 + 服务端转写为主路径） | 2 |
| 手表 M1（配对/主屏/列表/详情/设备/会话） | 9 |
| 手表 M2（通知/审批/队列） | 5 |
| 手表 M3（语音/Tile/常驻） | 5 |
| 手表 M4（打磨/降级） | 3 |
| 联调与真机验收 | 5 |
| **合计** | **≈ 42.5 人日** |

- 单人：约 **8–9 周**；
- 两人并行（后端 / 手表 App）：约 **4–5 周**（手机桥接改造需第三人协同，或后端兼）。

---

## 11. 附录：已核实的关键代码锚点

| 用途 | 位置 |
|---|---|
| 节点结构（加 kind） | `internal/services/noderegistry/registry.go:31-61` |
| 受限 token 校验范式 | `internal/runtime/httpapi/httpapi.go:381-389`（`nodeProxyTokenChecker`） |
| 路由装配点 | `internal/runtime/httpapi/httpapi.go:136-143` |
| 凭证签发/落盘先例 | `internal/runtime/httpapi/routes_self_join.go:39-84` |
| 权限批准签名 | `internal/runtime/httpapi/routes_workflows.go:197`（`ApprovePermission(ctx, sessionID, requestID, scope)`） |
| 权限列表 | `GET /sessions/{id}/permissions`（`routes_ownership_test.go:113`） |
| 人工任务列表 | `internal/runtime/httpapi/routes_flows.go:466`（`ListHumanTasks(queue, status)`） |
| Flow 人工回复 | `POST /v1/flow-runs/{runID}/human/{nodeID}/reply` |
| 会话消息（发送） | `routes_turns.go:128`（`POST /sessions/{id}/messages`） |
| 排队引导/取消 | `POST /sessions/{id}/queued/{queueID}/steer\|cancel` |
| 卡片状态迁移（钩子位） | `internal/plugins/taskboard/ledger.go:641`（`MoveCard(id, ifVersion, to, actor)`） |
| **`card_finished` 派生锚点（已落地，B2）** | `internal/runtime/httpapi/routes_watch_push.go`：`collectFinishedCards`（读 `ledger.ListCards` 取 `in_review`/`done`）、`finishAnchor`（身份＝执行 id + 状态）、`watchPushEventForCard` |
| ~~`card_finished` 钩子插入点（原计划，已废弃）~~ | ~~`routes.go:160 handlePatchCard`：`case "move"` `:180`、`case "complete"` `:182`~~——原称「唯一插入点」，实际 `MoveCard` 另有 `tool_actions.go:107`（agent 工具）调用点 |
| **卡片收尾/`done` 闸（已实证）** | `ledger.go:665-671`（离开 in_progress → `closeRunningExecutions`）、`:705`（CompleteCard 收尾）、`to==done` 被 `ErrDoneIsHumanOnly` 拒绝（`:647`） |
| **权限 pending 注册点（已实证）** | `internal/toolruntime/permissions.go:784`（`ensurePendingLocked`，持锁内 → **不在此发事件**） |
| 权限 pending 读取 | `internal/toolruntime/permissions.go:477`（`ListPending`）、`internal/agent/permission_facade.go:55`（`PendingPermissions`） |
| 权限 approve/deny 实现 | `internal/toolruntime/permissions.go:602`（`ApprovePending`）、`:648`（`DenyPending`）、`:683`（`CancelPending`） |
| 插件路由注册 | `internal/plugins/taskboard/plugin.go:104,127-137` |
| guarded-remote 写拦截 | `internal/services/relay/proxy.go:123-126`、`proxy_test.go:257` |
| ID 生成 | `internal/platform/idgen/idgen.go:15` |
| 手机端通知现状 | `mobile/src/main.ts:55-63`、`mobile/public/godex-watcher.js:55-70` |
| 推送（Web 侧，仅参考） | `internal/runtime/httpapi/push.go`、`internal/services/webpush/` |

---

## 12. 跨端配套待办（Android / 后端 / Web / Mobile）

> 2026-10-21 补齐稿 06 后，按点名方向逐项核实的结果。每条只写**已实证的现状**：
> 说"未做"的都有证据（文件不存在 / 路由 404 / grep 0 命中），不凭印象。
> **同日第三批补齐后**：§12.2/§12.4 的 B1–B4、B6/B7、C1/C2、D1 状态行已改为实测结论；被推翻的旧判定（§4 W6 的"唯一真钩子"）也已就地更正，而非只追加一句。

### 12.1 六个方向的总览

> **2026-10-21 第二批更新**：A1 / A2 / A3 / B5 已补齐并实证，下表即为更新后的状态。
> **2026-10-21 第三批更新**：C1 / B1+D1 / B4 / B6+B7 / B2 / B3 已补齐并实证；上表与 §12.2–§12.4 已按落地结果改写。剩余：A5（表盘）、C2 的真机体验、B8（token 续期）、D2（可选）。

| 方向 | 现状 | 证据 | 缺口定位 |
|---|---|---|---|
| **配对逻辑** | ✅ **已补（C1）**：后端 + Web 配对页齐备 | `ui/web/src/features/watch/WatchPage.tsx`（输入 8 位码 → 选 scope → 批准）+ `lib/apiWatch.ts` | — |
| **腕上精简回复** | ✅ **已补（B4）** | 服务端 `watch_reply_summary.go` 产出 `reply_summary`，详情屏渲染摘要 | — |
| **语音模块** | ✅ **已补（B6/B7）**：ASR 有端到端测试，TTS 已双向 | 真实引擎跑 `b6b7_verify.py` **18/18**；`POST /watch/tts` 骰下可朗读（腕上 `b7_wrist_verify.py` **12/12**） | — |
| **通知跨设备传递** | ✅ **已补（B1/D1）**：推送队列 + 手机桥接 | `GET /watch/push/pending` 已实测可用；`mobile/public/godex-watcher.js` 新增队列轮询 + 深链 data | — |
| **腕上工具日志** | ✅ **已补**（B5） | `watchSessionDetail` 新增 `recent_tools`（名称/结果/耗时，有界 5 条） | — |
| **审批消息** | ✅ 稿 06/08/15 + 深链分流已完成并实证 | 见下方 A1 | — |

### 12.2 后端（`internal/`）

| # | 待办 | 现状与证据 | 落点 | 验收 |
|---|---|---|---|---|
| B1 | **W5 推送队列**（无 FCM 时的唯一通路） | ✅ **已补**（b3448d3）：`registerWatchPushRoutes` + 内存有界队列（200 条 / 24h TTL，游标 + `ack`），`[watch:read]`；`/watch/push/pending` 已实测返回事件 | `routes_watch_push.go` | 造一条真实待审批 → 返回该事件；越权 401/403（已有测试） |
| B2 | **W6 `card_finished`** | ✅ **已补**（44c2be5），**改为派生而非钩子**（原施工图的「唯一插入点」经复核不成立，见 §4 W6） | `routes_watch_push.go`：`collectFinishedCards` / `finishAnchor` / `watchPushEventForCard` | 看板卡片离 `in_progress` → 队列出现 `card_finished`。实测：`b2_verify.py` **17/17** |
| B3 | **W1 `noderegistry` 加 `kind`** | ✅ **已补**（545860d）：`NodeInput/NodeView.Kind`（pc\|cloud\|phone，空→pc，未知值告警回退）+ `control.kind` 配置登记 + `/watch/nodes` 透传 + 腕上行标注 | `registry.go`、`config/*`、`routes_watch.go`、`Dto.kt`、`DevicesScreen.kt` | 设备列表能区分：实测 `e2e-node · 在线 · 云服务器`（`b3_wrist_verify.py` **5/5**） |
| B4 | **腕上精简回复** | ✅ **已补**（8cfab65）：服务端按来源整形，新增 `reply_summary` 字段（`watch_reply_summary.go`），腕上渲染摘要而非整段 | `routes_watch.go` + `SessionDetailScreen` | 同一问题从腕上发 → 详情屏拿到 ≤2 行摘要 |
| B5 | **腕上工具日志** | ✅ **已补**：`watchSessionDetail` 新增 `recent_tools`（`recentToolEvents`，started/finished 折叠、有界 5 条、`pending` 显式声明）。回归：`TestRecentToolEvents*` | `routes_watch.go` + `SessionDetailScreen.ToolRow` | 跑一个多工具 turn → 详情屏列出序列（已实测显示 `当前工具 · bash`） |
| B6 | **ASR 端到端测试** | ✅ **已补**：`b6b7_verify.py` 对真实 voice-engine 跑 18/18（真语音样本、转写内容比对、只读凭证 403、空音频/不支持类型 400）；路由层回归 4 例 | `.godex/tmp/e2e/b6b7_verify.py` + `routes_watch_asr_route_test.go` | 上传 WAV → 200 + 文本（已实测：『请帮我检查一下构建结果。』） |
| B7 | **TTS** | ✅ **已补**：`POST /watch/tts`（`{"text"}` 或 `{"session_id"}` 读回复，复用既有 voice-engine 合成路径）+ 腕上 `AudioTrack` 朗读（无外部依赖） | `routes_watch_tts.go` + `VoicePlayback.kt` + 详情屏「朗读回复」 | 腕上能朗读：实测设备侧写入 **32004 帧** vs 节点 **31968 帧**（偏差 0.1%，`b7_wrist_verify.py` **12/12**） |
| B8 | **token 续期/轮换** | `watchauth` 有 `ExpiresAt`/`Revoked`/吊销路由，但无续期 | 可暂不做；长期需考虑 1 年有效期到期后的体验 | 到期前可续；失效后 App 能引导重配对 |

### 12.3 Android（`wear/`）

| # | 待办 | 现状与证据 | 落点 | 验收 |
|---|---|---|---|---|
| **A1** | **审批通知深链落点错位** | ✅ **已修**：`openIntent(context, sessionId, approval.key)` 新增 `EXTRA_APPROVAL_KEY`；`MainActivity` 按键分流到审批屏，仅会话键仍走详情 | `WearNotifier.openIntent` + `MainActivity.applyDeepLink` 分流 | 实证：带审批键 → `待审批 1/2 … 高危命令`；仅会话键 → 任务详情（旧行为不变）。`verify_deeplink.py` ALL PASSED |
| **A2** | **后台拉取缺失（通知最大缺口）** | ✅ **已补**：`WorkManager`（纯 AndroidX，无 GMS）15 分钟周期任务 `WatchSyncWorker`；前台可见时 `appVisible` 让 worker 跳过；完成提醒前后台**共用**一套持久化 running 集合 | `wear/.../sync/WatchSync.kt` + `MainActivity` 可见性标记 | 实证：godex 非前台（`resumed=False`）下 worker 执行并投递：`run: … approvals=1`，`verify_bg_foreground_off.py` ALL PASSED |
| A3 | `expires_at` 未渲染 | ✅ **已补**：`remainingTime()` + 详情屏倒计时（实测显示「14 分钟后超时」）。服务端 `ListPending` 会 prune 过期项，故仅作提示、不作为闸门 | `Theme.kt` + `ApprovalDetailScreen` | 已实测 |
| A4 | Tile / 常驻活动渲染待真机 | 代码已实现；渠道 importance 4/2 已验，渲染需真机 | — | 见门禁 0.4–0.6 |
| A5 | 稿 01 表盘 + Complication | 未做（无 `WatchFaceService`/`data-source`） | 待 0.5 门禁结论 | 表盘可见运行中/待审批数 |

### 12.4 Web / Mobile

| # | 待办 | 现状与证据 | 落点 | 验收 |
|---|---|---|---|---|
| **C1** | **Web 配对审批页**（稿 10 的另一半） | ✅ **已补**（a92e04f）：`ui/web/src/features/watch/WatchPage.tsx` + `lib/apiWatch.ts`，输入 8 位配对码 → 选权限范围 → 批准 | `ui/web/src/features/watch/` | 手表生成码 → Web 页输入即可完成配对 |
| **C2** | **Web 手表设备管理页** | ✅ **已补**（同页）：设备列表（权限 / 最后在线 / 已吊销）+ 吊销确认 | `ui/web/src/features/watch/WatchPage.tsx` | 吊销后手表立即失效并回到配对屏 |
| **D1** | **手机桥接推送**（W5 的消费端） | ✅ **已补**（b3448d3）：`godex-watcher.js` 新增 `pushPoll`，轮询 `/api/watch/push/pending` 并投递本地通知（带 `godex_session_id` / `godex_approval_key` / `godex_event_kind` 深链 data）；含游标回退保护 | `mobile/public/godex-watcher.js` | 电脑端审批 → 手机出通知 → 点开桥接到手表 |
| D2 | 手机配对入口 | `mobile/` grep `watch/pair` 0 命中 | 可选：手机端也提供配对入口 | 手机可直接给手表配对 |

### 12.5 依赖与建议顺序

**外部依赖**：B6/B7 需 `voice-engine` 可运行（已完成并实测，见 `routes_watch_asr.go` 顶部注释）；A4/A5 **真正待真机门禁 0.4–0.6** 结论。

**建议顺序**（按"解锁最多、依赖最少"排；1–5 已全部完成）：

1. ~~**A2 后台拉取** + **A1 深链分流**~~ ✅ 已完成（见 §12.3）
2. ~~**C1 Web 配对页**~~ ✅ 已完成（C2 设备管理页同页落地）
3. ~~**B1 W5 队列** + **D1 手机端**~~ ✅ 已完成
4. ~~**B4 精简回复**~~ + ~~**B5 工具日志**~~ ✅ 均已完成
5. ~~**B6/B7 语音补测与 TTS**、**B2/B3**~~ ✅ 已完成

**仅剩 A5**：表盘 + Complication（需先定 WFF 与数据源形态，受 0.5 门禁影响）；D2（手机配对入口）为可选。

### 12.6 本轮补齐的实现要点（供后续维护）

- **A2 的关键设计**：周期任务最小粒度 15 分钟，短于两次检查的任务不会被播报——这是省电换来的固有取舍。`WatchSync.appVisible` 让前台与后台互斥，避免同一事件双提醒。
- **验证 A2 不能用 `cmd jobscheduler run -f`**：该命令会拉起 `SystemJobService`，但 WorkManager 仍会因**周期未到**而拒绝执行 worker（逐个强制 `SystemIdInfo` 里的权威 id 也只看到 `system_id` 递增、`period_count` 不变且无 worker 日志）。必须等真实周期到达，且**确认 godex 不是 resumed**——否则系统会把 MainActivity 拉回前台，worker 正确地 `skip: app is visible`，而通知其实来自前台轮询。
- **A1 的验证方式**：该模拟器的通知栏点击目标无法稳定脚本化（点正文只展开，`在手表上打开` 按钮在收起态可见、展开后消失）。因此分两半验证：**分流**用真实 Activity + 深链 extras 走通；**载荷**断言 `notifyApproval` 传入 `approval.key` 且 `MainActivity` 读取 `EXTRA_APPROVAL_KEY`。
- **B5 的 `pending` 为何显式下发**：客户端无法从 `duration_ms == 0` 推断"进行中"——亚毫秒完成的调用同样是 0，据此渲染会误报状态。
- **B2 为何最终没走钩子**：原计划的"唯一插入点"经复核不成立——`MoveCard` 在生产代码有两个调用者（Web handler 与 agent 工具），而 agent 工具才是看板主路径；且插件到推送队列无接线缝。派生是**精确**的：`in_review` 只能由 `in_progress` 进入、`done` 只能由 `in_review` 进入，故"当前是 `in_review`/`done`"等价于"已离开 `in_progress`"。
- **B7 的验证坑（值得复用）**：1 秒间隔轮询 `dumpsys audio` 会**漏掉**约 1.3s 的短音频，产生假 FAIL；改用 logcat 的 `AudioTrack: stop(N): called with M frames delivered`，并拿 M 与节点返回的 WAV 帧数**交叉核对**（32004 vs 31968），才真正证明"HTTP 取到的字节到了扬声器"而非"构造了播放器对象"。
- **B3 的验证注意**：测试配置故意声明 `kind: cloud`（**非**默认 `pc`）——若用默认值，"字段被丢掉"与"工作正常"在断言上不可区分。

