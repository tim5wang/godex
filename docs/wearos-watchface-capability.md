# 真机表盘能力探测（2026-10-25，Xiaomi Watch 5 eSIM）

设备：`M2505W1` / brand=xiaomi / Android 14 (SDK 34) / `ro.build.characteristics=nosdcard,watch`
OS：**小米 MIWEAR**，`ro.mi.os.flavor=wearable`、`persist.miwear.region=CN`

## 关键实测结果

| 探测 | 命令 | 结果 |
|---|---|---|
| 标准 Wear 表盘入口 | `query-activities -a MAIN -c com.google.android.wearable.action.WATCH_FACE` | **No activities found** |
| 小米表盘实现方式 | `dumpsys package com.xiaomi.miwear.watchface.runtime` | `WallpaperService` + `BIND_WALLPAPER` |
| 小米表盘注册类目 | 同上 | `com.xiaomi.wear.watchface.category.RUNTIME_WATCH_FACE_SERVICE` |
| 控制服务保护权限 | 同上 | `com.xiaomi.wear.permission.watchface.DWF_RUNTIME_PRIVILEGED`（特权级） |
| 系统默认表盘 | `query-services -a android.service.wallpaper.WallpaperService` | `com.xiaomi.miwear.services/.watchfaces.defaultservice.DefaultWatchFace` |
| **Tile provider** | `query-services -a androidx.wear.tiles.action.BIND_TILE_PROVIDER` | **命中 `com.godex.wear/.tile.GodexTileService`** ✅ |

## 结论

1. 标准 Wear OS 表盘（`WatchFaceService`）在这台设备上**不被系统解析**，注册了也不会出现在表盘列表。
2. 小米自家表盘走壁纸服务，且控制面需要**特权权限** `DWF_RUNTIME_PRIVILEGED` —— 第三方普通应用无法注册成系统表盘。
3. **Tile 是这台设备上唯一被系统解析的第三方常驻展示面**，且我们的 Tile 已注册成功（实证命中）。

因此「替换系统表盘」对第三方不可行；等价可交付物 = 应用内表盘式主屏（实时时间/日期/电量 + GoDex 任务与推送）+ 已生效的系统级 Tile。

复用注意：`rememberGodexRotaryBehavior` 在无 GMS 设备上必须用于可滚动列表，否则旋转表冠会崩。
