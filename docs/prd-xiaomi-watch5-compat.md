# 小米 Watch 5 兼容性分析（对腕上遥控器设计的影响）

> 状态：Draft（调研产出，待评审）
> 日期：2026-10-20
> 关联：`docs/prd-wearos-watch-client.md`（腕上遥控器设计）、`docs/public/mockups/wearos/`（15 张稿）
> 触发：用户实际设备为 **小米 Watch 5 国行版**
> **决策前提（用户已明确）：国行版按 Android / Wear OS 路线开发，不按小米 Vela。**

---

## 1. 结论（先看这个）

**一句话：按 Wear OS 开发这条路线成立 —— 国行版底层就是 Android（HyperOS 3 + 骁龙 W5），可实现栈与原设计一致；但因为没有 Google 服务，需替换三件事：分发、推送、以及 Tile/Complication 的落地形态。**

| 项 | 判定 |
|---|---|
| 实现栈（Kotlin + Compose for Wear OS） | ✅ **可用**，与原设计一致 |
| 屏幕（圆形 1:1 **480×480**、312 PPI、1500 nits） | ✅ 完全兼容；圆屏安全区规则直接适用（基准比 456 更宽松） |
| 交互硬件（**旋转表冠**旋转+点按、触摸、振动、麦克风） | ✅ 全部具备 |
| 应用安装 | ✅ **可侧载**（无线调试 / ADB 类工具实测可行），不受应用商店审核卡脖子 |
| **GMS / Google Play** | ❌ **国行没有** → ① 不能上 Play 分发 ② **FCM 推送不可用** |
| **推送通道** | ⚠️ **必须走「手机端桥接」**（`mobile/` 已有能力），FCM 方案作废 |
| **Tile / Complication 渲染** | ⚠️ **待真机验证**：这是 Wear OS 系统组件，HyperOS 表端是否渲染第三方 Tile/Complication 未经证实 |
| 手势 | ⚠️ HyperOS 有自己的系统手势（见 §4），但「**禁止水平滑动承载业务语义**」这一结论两平台都成立 |
| Ongoing Activity | ⚠️ 同 Tile，属 Wear OS 系统能力，**待真机验证**，不可用则降级为常驻通知 |

**实质结论**：技术路线不变（Wear OS 栈），但**设计里有 4 处依赖「Wear OS 系统组件」的地方要真机验证**（Tile / Complication / Ongoing Activity / FCM）。核心遥控流程（主屏、列表、详情、审批、语音、设备与会话切换）**不依赖这些系统组件，可直接落地**。

---

## 2. 事实基础：同为 Android 底子，区域差异在「Google 服务」

| 区域 | 系统 | 应用生态 |
|---|---|---|
| **国行（CN）** | **HyperOS 3**（Android 底子） | 表端应用商店（70+ 应用）+ **可侧载 APK** |
| **海外（Global）** | **Wear OS 6** | Google Play、Gemini、Google Pay |

**硬件（两版一致）**：47mm 表壳、1.54" 圆形 AMOLED **480×480** / 312 PPI / 1500 nits、
**高通 Snapdragon W5 Gen 1（4nm）** + 恒玄 BES2800 低功耗协处理器、930 mAh、
**旋转表冠（旋转 + 点按）**、国行 eSIM 版可脱机独立使用。

> **关键推论（修正常见误解）**：国行**不是**小米 Vela 那套「HTML/CSS/JS 快应用」框架。
> 底层是 Android（HyperOS 3），因此 **Android / Wear OS 应用开发栈适用**。
> 有实测资料显示国行可通过**无线调试 / 甲壳虫 ADB 助手 / WearOS 工具箱**安装第三方 APK（含非商店应用），
> 这为「自用/内测分发」提供了现实路径，不必依赖商店审核。

**由此推导出的两个硬约束**：

1. **无 GMS → 无 FCM**：国行 Android 设备普遍不预置 Google 服务框架，FCM 推送不可用。
   → 原 PRD §9 的 **W5 必须一期就走「手机端桥接」**（手机装 godex App，收到事件后转发/镜像到手表通知栏），
   **FCM 直推方案直接作废**（原 M4「FCM 二期」应删除或改为「厂商推送通道」）。
2. **分发靠侧载**：无法上 Google Play；国行表端商店对第三方应用的开放度不确定。
   → 自用/小范围用**无线调试 + ADB 安装**即可闭环；若要给他人用，需评估商店上架。

---

## 3. 实现栈（与原 Wear OS 设计一致）

| 项 | 选择 |
|---|---|
| 语言/UI | **Kotlin + Jetpack Compose for Wear OS**（`androidx.wear.compose`） |
| 最低版本 | Wear OS 3 基线（API 30+），按国行 HyperOS 3 的实际 API level 校准 |
| 依赖原则 | **只用官方 AndroidX / Wear 库**；**严禁依赖 GMS**（`play-services-*`），否则国行装不上/跑不起来 |
| 网络 | OkHttp / 官方网络栈 → REST + WS（不依赖 GMS 的 FCM 之外的通道） |
| 本地存储 | DataStore（token + 偏好 + 置顶会话，见 PRD §7.6） |
| 语音 | **Android 原生 `SpeechRecognizer`**（不依赖 GMS 语音服务）；不可用时降级为应用内录音 + 服务端 ASR（godex 已有 `voice-engine`） |
| 产物 | 常规 APK，**无线调试 / ADB 侧载** |

> ⚠️ **落地注意**：`androidx.wear.compose` 的少数能力（部分语音/地图类）可能间接依赖 GMS。
> 选型时逐项确认；语音这一条已知可用原生 `SpeechRecognizer` 或服务端 ASR 兜底。

---

## 4. 操作逻辑对照：手势模型

小米手表（HyperOS）的系统手势：

| 手势 | 小米 HyperOS Watch | Wear OS（对比） |
|---|---|---|
| 表盘**右滑** | 进入**负一屏** | **退出 / 返回** |
| 表盘**左滑** | 进入**应用卡片**页（最多 5 个卡片） | Tiles 轮播 |
| **从上往下滑** | **通知栏** | 通知栏 |
| **从下往上滑** | **控制面板**（飞行模式 / 查找手机 / 静音 / 省电 / 手电筒…） | 应用列表 |
| 表冠 | **旋转**（滚动）+ **点按**（返回主页） | 旋转侧键 / 表圈，行为相近 |

**判定**：

- ✅ **「水平滑动被系统占用」在两平台都成立**，只是含义不同 → PRD §7.7「**禁止用水平滑动承载业务语义**」**无需修改，继续有效**，且在小米上更严格（左右滑都各有系统含义）。
- ✅ **旋表冠滚动**：两边都支持，列表滚动设计**直接适用**。
- ⚠️ **返回**：小米靠**表冠点按回主页**，页内返回需应用自备返回控件 → 稿件各页的**顶部返回入口是必需的**，不能依赖系统右滑返回。
- ⚠️ **应用卡片位次极稀缺**（系统上限 5 个）→ 稿件 02 的 Tile 即使可用，也只能做**一张**、信息密度最高。

---

## 5. 概念映射与风险（Wear OS 设计要素 → 国行现实）

| 设计要素 | 依赖的 Wear OS 能力 | 国行可用性 | 处理 |
|---|---|---|---|
| **Tile**（稿件 02） | `androidx.wear.tiles` + 系统渲染 | ⚠️ **待真机验证** | 保留设计；不可用则降级为「表冠点按直达的首页」或手机通知 |
| **Complication**（稿件 01） | `androidx.wear.complications` + 表盘渲染 | ⚠️ **待真机验证**（HyperOS 自研表盘未必渲染第三方 complication） | 不可用则降级为「应用内聚合页」，丢弃表盘四角挂载 |
| **Ongoing Activity**（稿件 12） | Ongoing Activity API | ⚠️ **待真机验证** | 不可用则降级为 `NotificationCompat` 常驻通知 |
| **通知动作按钮**（稿件 06/08） | `NotificationCompat` + `Action` | ✅ 原生 Android 能力，**不依赖 GMS** | 直接可用 |
| **通知直达腕** | FCM | ❌ **不可用**（无 GMS） | **改手机端桥接** |
| **圆屏安全区** | 无 | ✅ | 规则通用；基准按 **480×480** |
| **旋转表冠滚动** | Android rotary input | ✅ | 直接用 |
| **语音输入** | `SpeechRecognizer` / 录音 | ✅（可能需服务端 ASR 兜底） | 直接用 |
| **数据/网络** | OkHttp / WS | ✅ | 直接用 |

---

## 6. 15 张设计稿逐条判定

| 稿 | 界面 | 国行判定 | 说明 |
|---|---|---|---|
| 01 | 表盘 + 四角 Complication | ⚠️ 待验证 | 依赖系统 complication 渲染；兜底=应用内聚合页 |
| 02 | Tile 卡片 | ⚠️ 待验证 | 依赖系统 tile 渲染；位次仅 5；兜底=表冠直达首页 |
| 03 | 主屏（按住说话 + 三圆钮） | ✅ **直接可用** | 核心屏，不依赖系统组件 |
| 04 | 任务列表 | ✅ 直接可用 | 旋表冠滚动 |
| 05 | 任务详情 | ✅ 直接可用 | |
| 06 | 审批整屏 | ✅ 直接可用 | |
| 07 | 语音输入 | ✅ 直接可用 | |
| 08 | 通知 + 腕上动作按钮 | ✅ 直接可用 | 原生通知能力；**但送达靠手机桥接**（无 FCM） |
| 09 | 设备切换 | ✅ 直接可用 | |
| 10 | 配对绑定（二维码 + 码） | ✅ 直接可用 | 建议**手表出码、手机扫**（手机侧有 godex App） |
| 11 | 设置 | ✅ 直接可用 | |
| 12 | 常驻活动 / Live Update | ⚠️ 待验证 | 兜底=常驻通知 |
| 13 | 会话切换 | ✅ 直接可用 | |
| 14 | 跨设备统一列表 | ✅ 直接可用 | |
| 15 | 待审批队列 | ✅ 直接可用 | |

**统计**：15 张稿 **12 张直接可用**，3 张（01/02/12）依赖 Wear OS 系统组件需真机验证并可降级。
→ **设计的核心（遥控器主流程 + 多端互操作）在国行上完全成立。**

---

## 7. 立项前必须验证的清单（按优先级）

| # | 待验证 | 为什么关键 | 怎么验 |
|---|---|---|---|
| 1 | **无线调试 / ADB 装自签 APK 是否顺畅** | 决定开发与分发闭环（无 Play Store） | 开开发者选项 → 无线调试 → `adb install`；复用现成 ADB 助手类工具 |
| 2 | **第三方 Tile 是否被 HyperOS 渲染** | 决定稿件 02 存废 | 装官方 Wear Tile 示例 App，看表盘左滑轮播是否出现 |
| 3 | **第三方 Complication 是否被表盘渲染** | 决定稿件 01 存废 | 同上，试挂 complication |
| 4 | **Ongoing Activity 是否生效** | 决定稿件 12 | 官方示例 App 实测 |
| 5 | **手机端桥接通知到手表** | **这是唯一推送通道**（无 FCM） | 手机发通知，确认手表能收到；必要时做「镜像到手表」逻辑 |
| 6 | **`SpeechRecognizer` 在国行可用性** | 决定稿件 07 的语音来源 | 真机跑一次识别；不行则服务端 ASR |
| 7 | **实际 API level / Wear OS 版本号** | 决定 minSdk 与可用 API | `adb shell getprop ro.build.version.sdk` |
| 8 | **网络：WiFi（实测为单频）+ 蜂窝** | 决定 L2/L4 可达性 | 真机测内外网访问 godex 服务 |

> 建议：**1、2、5 三条先做**（各半小时内可出结论），就能确定 M1 的形态。

---

## 8. 路线建议

**主路线（推荐）：单一 Wear OS 实现，覆盖国行 + 海外**

- 按 `docs/prd-wearos-watch-client.md` 实施 **Kotlin + Compose for Wear OS**；
- 国行差异集中处理三点：**① 侧载分发 ② 手机桥接推送 ③ Tile/Complication/OngoingActivity 的真机验证与降级**；
- 海外版（Wear OS 6 + GMS）自动获得 FCM 与 Play 分发，**无需第二套代码**——这正是「按 Wear OS 开发」的好处：**一套代码两个市场**。

**兜底（若 Tile/Complication 均不渲染）**：
不影响主流程——把「瞥一眼」的入口从表盘/卡片改为 **① 常驻通知 ② 表冠点按直达首页**，其余 13 张稿不变。

**不推荐**：为小米单独维护第二套实现（Vela/其他框架）。除非未来出现明确需求，否则单栈是最优解。

---

## 9. 对现有设计的净影响

| 项 | 变化 |
|---|---|
| 实现栈 | ✅ **不变**（Kotlin + Compose for Wear OS） |
| 新增硬约束 | **严禁 GMS 依赖**（`play-services-*`）；分发靠侧载 |
| **推送方案** | 🔴 **FCM 作废**，**一期必须手机端桥接**（原 PRD §9 W5 提前到 M1/M2） |
| 稿件 01 / 02 / 12 | 由「可用」改为「**待真机验证 + 可降级**」 |
| 稿件 03–11、13–15 | **不变**（12 张） |
| §7.7 手势结论 | **不变**（两平台都禁水平滑动语义），补充「返回须用显式控件」 |
| 后端集成面 W1–W8 | **不变**（与手表 OS 无关） |
| 圆屏基准 | 456 → **480**（小米原生基准） |

---

## 10. 参考来源

- 小米 Watch 5 国行（HyperOS 3、480×480、骁龙 W5 Gen 1 4nm、eSIM）：https://www.fonearena.com/blog/472008/xiaomi-watch-5-price-features.html ；https://news.mydrivers.com/1/1095/1095134.htm
- 双版本系统标注（Global: Wear OS 6 / China: HyperOS 3）：https://www.gsmfact.com/xiaomi-watch-5
- 海外版（Wear OS 6 + Google 生态）：https://www.fonearena.com/blog/476480/xiaomi-watch-5-price-features-global.html ；https://www.notebookcheck.net/Xiaomi-releases-new-Wear-OS-smartwatch-globally.1236847.0.html
- 屏幕/硬件与独立应用商店：https://m.163.com/dy/article/KHN8GPNG0511CPVM.html ；http://m.mydrivers.com/newsview/1094900_6.html
- **国行可侧载第三方 APK（实测）**：https://www.snm0516.aisee.tv/video/BV1p3BiBKEkF/（甲壳虫 ADB 助手装 QQ）；https://post.smzdm.com/p/agollxw3/（WearOS 工具箱）
- 小米手表系统手势（右滑负一屏 / 左滑卡片 / 下滑通知 / 上滑控制面板）：https://www.mi.com/service/smart/miwatch/use
- 旋转表冠（S4 起支持旋转 + 点按）：https://digi.ithome.com/archiver/806/244.htm
- Wear OS 官方开发文档（Tile / Complication / Ongoing Activity）：https://developer.android.com/training/wearables/tiles ；https://developer.android.com/training/wearables/notifications/ongoing-activity
