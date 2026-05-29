# TUI Permission Blocker 修复 (2026-05-29)

## 问题描述

approve 一个 permission 后，状态栏仍显示 "Blocked by approval" 直到异步 snapshot 刷新完成。

## 根因

`permissionFinishedMsg` 处理中，approve 成功后只触发了异步 `fetchSnapshotCmd()`，但没立即清除 `m.snapshot.ActivePermissionBlocker`。

## 修复

在 `permissionFinishedMsg` 成功路径中添加 `m.snapshot.ActivePermissionBlocker = nil`。

## Commit

`d15f390` fix(tui): clear permission blocker immediately on approval
