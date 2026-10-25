# 真机表盘能力探测（2026-10-25，Xiaomi Watch 5 eSIM）

> 状态：Analysis（实测记录，不定义产品契约；结论已回写 `wearos-watch-implementation-plan.md`）

设备：`M2505W1` / brand=xiaomi / Android 14 (SDK 34) / `480x480 @320dpi`
OS：**小米 MIWEAR**，`ro.mi.os.flavor=wearable`、`persist.miwear.region=CN`

## 一、系统表盘是什么形态

| 探测 | 命令 | 结果 |
|---|---|---|
| 标准 Wear 表盘入口 | `query-activities -a MAIN -c com.google.android.wearable.action.WATCH_FACE` | **No activities found** |
| 当前表盘由谁渲染 | `dumpsys wallpaper` | `mWallpaperComponent=com.xiaomi.miwear.watchface.runtime/.render.MiWatchFaceService01` |
| 该服务类型 | `dumpsys package com.xiaomi.miwear.watchface.runtime` | `WallpaperService` + `BIND_WALLPAPER` |
| 小米表盘类目 | 同上 | `com.xiaomi.wear.watchface.category.RUNTIME_WATCH_FACE_SERVICE` |
| 表盘控制服务权限 | 同上 | `com.xiaomi.wear.permission.watchface.DWF_RUNTIME_PRIVILEGED`（特权级） |
| 系统表盘选择器 | `query-activities -a android.service.wallpaper.CHANGE_LIVE_WALLPAPER` | `com.xiaomi.miwear.home/...watchfacepicker2.WatchFacePickerActivity` |

结论：**系统表盘槽位 = wallpaper 槽位**，形态是 WallpaperService，不是 Wear 的 `WatchFaceService`。

## 二、第三方能否进入这个槽位（逐层实测）

| 实验 | 结果 |
|---|---|
| 声明 WallpaperService 后，系统是否解析到 GoDex | ✅ 解析到（`query-services -a WallpaperService` 共 6 条，含 `com.godex.wear/.watchface.GodexWatchFaceService`） |
| 补上小米类目 `RUNTIME_WATCH_FACE_SERVICE` 后，按该类目过滤能否解析到 | ✅ 能（`query-services -a WallpaperService -c com.xiaomi.wear.watchface.category.RUNTIME_WATCH_FACE_SERVICE` 命中 GoDex） |
| 打开系统表盘选择器后，列表里是否出现 GoDex | ❌ **没有** |
| 选择器实际枚举到的表盘 | 仅小米自家：`多功能模块2`、`万重山峰时感`、`探境运动轨迹` 等 |
| 程序化设置表盘（`WallpaperManager.setWallpaperComponent`） | ❌ 不在公开 SDK（`javap` 确认该类只有 `getWallpaperInfo` / `isWallpaperSupported`，无 setter），且需签名级 `SET_WALLPAPER_COMPONENT` |
| `WallpaperManager.ACTION_CHANGE_LIVE_WALLPAPER` + `EXTRA_LIVE_WALLPAPER_COMPONENT` | ✅ 可用（注意：两个常量在 `WallpaperManager` 上，不在 `Intent` 上），但落到的是**小米自家选择器**（`com.xiaomi.miwear.home/.mainui.activity.SysUiActivity`），它不列出第三方 wallpaper |

## 三、结论（重要）

1. **标准 Wear 表盘在这台设备上不存在**，注册了也不会出现。
2. **第三方 WallpaperService 能被系统解析，但进不了小米的表盘选择器**。小米的表盘列表是封闭的（大概率按签名/特权权限 `DWF_RUNTIME_PRIVILEGED` 白名单过滤），这不是应用侧代码能绕过的。
3. 因此「让 GoDex 成为抬腕/常亮时显示的系统表盘」在**该机型上对第三方不可行**，属设备厂商限制。

## 四、尝试过并撤回的方案（避免后人重走）

曾实现 `GodexWatchFaceService`（WallpaperService，按 mockup 01 绘制）并注册，另加小米类目
`RUNTIME_WATCH_FACE_SERVICE`。实测：PackageManager 按 `WallpaperService` 与按小米类目**都能解析到它**，
但小米「表盘主题」选择器**始终不列出它**；模拟器上同样不列出。该实现**已撤回**，不在代码库中。

## 五、实际交付的面（mockup 01 落在看得见的地方）

| 面 | 状态 |
|---|---|
| 首页 `ClockCompact`（时间 + 日期，点按进整屏） | ✅ 真机验证：`03:04` / `10月 25 · 周日` |
| 整屏 `WatchFace`（环形进度 + 四角 complications + 大字时间） | ✅ 真机验证：`1 运行中` / `0 待审批` / `4/9 节点在线` / 当前设备名 |
| Tile（`GodexTileService`） | ✅ 系统解析并展示，含任务状态 + 数据观测时间 |

四角取值只来自同一次刷新，未读到数据时显示 `–` 而不是 `0`——避免把「还没问」显示成「没有待审批」。
环形进度仅在真的有 turn 在跑时出现，长度取 agent 自己的步骤百分比。

## 五、复用注意

- `rememberGodexRotaryBehavior` 在无 GMS 设备上必须用于可滚动列表，否则旋转表冠会崩。
- 表盘按**分钟**刷新（对齐分钟边界），不按秒——秒级刷新在手表上纯耗电且画面无差异。
- 时间/日期格式必须走平台 formatter（`DateFormat.getTimeFormat`），硬编码 `HH:mm` 会让设成 12 小时制的用户看到错的时间。
