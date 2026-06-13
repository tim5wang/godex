# GoDex Web UI 聊天工作区升级 SPEC

> 目标:把当前 GoDex Web 的聊天工作区,从一个"带任务中心顶栏 + 独立文件页"的中等密度工作台,升级为接近 IDE 体验的多面板工作区(PC),同时保留移动端现有的全屏独占切换体验(本 SPEC **不改造一级 AppNav**,只在 Workspace 内增加二级 Tab 导航)。

- 范围:仅 `ui/web/`(以及嵌入式 dist 的构建产物)。
- 不在范围:TUI 客户端(`internal/tui`)、后端 API 的能力扩展(若有依赖会单独标注,但默认所有面板都复用现有接口)。
- 关联能力:
  - 聊天核心:`features/chat` + `pages/ChatPage.tsx` + `components/MessageFeed.tsx` / `Composer.tsx` / `Sidebar.tsx`
  - 文件 app:`features/files` + `pages/FilesPage.tsx`
  - 任务中心:当前是 `ChatPage` 内置的顶栏(`任务中心 / 0运行中 / 0已阻塞 / 0待审核 / 0已合并 / 0失败`)
  - 现有抽屉视图:由 `app/...` 提供的全局 Drawer 触发(用现有协议,不重写抽屉内容)

## 0. 当前进展快照(2026-06-13)

> 本节是实现进展索引,不替代下面的产品设计。详细接手记录见 `M0-WEB-IDE-EXPERIENCE.md`。

- **M0 IDE 基础已完成**:布局 store、AppNav 折叠、SessionList 折叠、任务中心 chip + 共享右侧 inspector 抽屉、FilesPanel 双模式、CenterGrid、移动端二级 Tab、TerminalPanel、布局持久化、i18n 与基础验收均已落地。
- **M1+ candidate A 已完成**:Terminal 已从前端 mock/polling fallback 升级为真实 Go PTY HTTP client + 后端 terminal routes。
- **M1+ candidate C 已进一步推进**:store 与 CenterGrid 已支持 3x3 preset;2x2/3x3 面板标题栏菜单、标题栏拖拽/目标高亮、slot-to-slot swap、面板折叠到书签并恢复已落地;2x2 splitter 拖拽比例持久化与双击收起/恢复已接入;拖拽过程改为本地预览、松手后持久化,降低 resize 卡顿;拖拽到接近 0/100% 时自动折叠对应面板为书签,恢复时回到最近安全比例。4 象限与任意拖拽布局仍作为后续议题。
- **崩溃与截图可用性修复已完成**:修复 Chat React #185、Files React #306;补齐 workspace header 有效信息、grid files 预览/附加到 composer/多文件切换、chat composer 贴底、terminal 状态可见且可输入执行、TaskCenter 并入右侧 inspector Tab 且复用任务中心 chip 折叠/展开;CenterGrid 顶部书签条保持常驻,chat/files/terminal 均可折叠成书签并恢复;FilesPanel 文件树可折叠,宽屏下文件树与预览左右并排;并加固 Playwright 验收,避免错误边界页面被误判为通过。
- **仍需后续推进**:把最近的 debug/isolate/feature 提交整理成干净历史;把目前分散在 CenterGrid/Session/Files 的书签条实现收敛成统一抽象和统一角标策略;补真实截图 diff/视觉基线;继续评估 M1-C 的 4 象限/任意布局与可能的一级 AppNav 新 SPEC。

状态标记:

- `[x]` 已完成并有测试/构建验证。
- `[~]` 部分完成,核心状态或底层能力已落地,但交互/集成仍缺。
- `[ ]` 未开始或需要新 SPEC 重新定义。

---

## 1. 现状摘要(基于截图 + 代码摸排)

截图里 4 列横向结构:

1. **应用导航** `Sidebar`:`聊天 / 文件 / 自动化 / 节点 / 笔记 / 技能 / 记忆 / 设置 / 用量`,目前总是展开,带文字。
2. **Session 列表**:`新建` 按钮 + `全部 / acp / local` 过滤 + 分组的会话条目(标题 + 来源标签 + 时间),展开状态下文本会做截断。
3. **聊天工作区**:
   - 顶部元信息条:`新会话 / m3 模型 / Reasoning / 会话 / 空闲 / 删除/分支 / 待审批 / 上下文与召回 / 轮次`
   - 任务中心(侵占严重):含 5 个状态计数 chip(`0运行中 / 0已阻塞 / 0待审核 / 0已合并 / 0失败`)和 `待审核 / 展开` 操作
   - 消息流(目前空)+ 底部状态条(Connected / ctx / calls / msgs)+ Composer
4. **右侧抽屉视图**:截图最右侧是某个 drawer 内容(`当前没有待审批请求。`),由 `app/...` 触发。

并行的 "文件 app" 是 `pages/FilesPage.tsx`,通过左侧菜单切换,而不是聊天页内可调起的面板。

---

## 2. 用户诉求(逐条对应)

| # | 诉求 | 抽象为产品语言 |
|---|---|---|
| 1 | 最左侧功能菜单可折叠成 icon 列 | **应用导航** 宽度可切换 `expanded (≈200px) ⇄ collapsed (≈48px icon-only)` |
| 2 | Session 列表可折叠,给工作区更大空间 | **Session 列表** 宽度可切换 `expanded (≈280px) ⇄ collapsed (≈40px strip)`,折叠后保留新建/搜索/最近 session 入口 |
| 3 | 任务中心侵占聊天顶栏 | **任务中心** 抽离成"右侧抽屉 / 底部面板 / Workspace 网格"三种位置,默认折叠成 chip + 角标 |
| 4 | 把文件 app 整合进聊天界面(IDE 体验) | **文件面板** 提升为聊天页内可挂载的 dock / 网格面板之一(左侧 / 右侧 / 网格内,均可) |
| 5 | 增加 terminal 窗口 | **终端面板** 复用 agent runtime 的 sandbox/REPL,作为 chat 旁的网格面板 |
| 6 | 大改布局:PC 宽屏 IDE 风、移动端保留现状 | 见 §3 |
| 7 | (补充)Workspace 支持"上左=文件 / 上右=聊天 / 下整行=terminal"这类 **2×2 网格** | 见 §3.2 / §4.1 |

---

## 3. 总体设计:IDE 风格工作区

### 3.1 视觉与交互原则

- **PC 宽屏(>= 1024px)**:四区域自左到右
  `AppNav | SessionList | Workspace(Center) | Dock(可选)`,每个区域可单独折叠/展开/调整宽度。
- **移动端窄屏(< 1024px)**:Workspace 顶部增加 **二级 Tab 栏**(`对话框 / terminal / 文件 / 抽屉 / 任务`),一次只显示一个子视图;**一级 AppNav 行为不变**,依旧从左侧呼出。
- **Workspace 网格(PC only)**:**2×2 网格**,由两层 `Splitter` 嵌套(外层 vertical/horizontal,内层 horizontal/vertical)。5 种预设布局可一键切换,任意分隔条可拖拽改比例,双击收起成 0。
- **折叠态 = 书签条**:每个可折叠面板在折叠时显示成一个 **垂直(或水平)极窄书签条**,只露出 icon + 角标(任务数、未读数),点击展开。书签条方向跟随所在侧。
- **状态持久化**:面板展开/折叠/宽度/网格预设/比例/格子占用,写入 `localStorage`,key 形如 `godex.workspace.layout.v1`,按 `app + view` 隔离。

### 3.2 区域与默认布局(PC)

**默认布局(Preset 2: `topFilesChat_bottomTerminal`)**:

```
+------+----------+----------------------------------------+--------------+
| App  | Session  |       Workspace (2x2 Grid)             |     Dock     |
| Nav  |  List    | +-----------------+-----------------+ | (default:    |
|      |          | |   Files Panel   |   Chat Panel    | | collapsed,  |
| 48px | 280px    | |  (Tree+Preview) | (Messages +     | | 展开=抽屉)   |
|  ⇄   |   ⇄      | |                 |  Composer)      | |             |
| 200  |  40      | +-----------------+-----------------+ |  40px strip |
|      |  strip   | |           Terminal Panel          | |   ⇄ 320px   |
|      |          | |   (xterm, 占满下整行)              | |             |
+------+----------+----------------------------------------+--------------+
```

默认 `AppNav=expanded, SessionList=expanded, Workspace=Preset 2(files|chat 上 + terminal 下整行), Dock=collapsed(bookmark)`。

> **Workspace 内的网格规则**:
> - 基础是 **2×2 网格**,5 种预设可一键切换:
>   1. `topChat_bottomTerminal` —— 上整行 chat,下整行 terminal(经典 IDE 上下分屏)。
>   2. `topFilesChat_bottomTerminal` —— 上 files|chat,下整行 terminal(**默认**)。
>   3. `topChat_bottomFilesTerminal` —— 上整行 chat,下 files|terminal。
>   4. `leftCol2x2` —— 左 files|terminal,右整列 chat(适合窄一点但有 terminal 需求)。
>   5. `single` —— 单格 chat(terminal/files 都折叠成 0,获得最大聊天空间)。
> - 网格的 **拆分条** 由 antd `Splitter` 嵌套两次实现(一次外层、一次内层),任何一条都可以:
>   - **拖拽** 改比例(0~1)。
>   - 拖拽到接近 0/100% 时,不持久化不可恢复的 0 尺寸,而是把对应面板或行折叠成 CenterGrid 书签;点击书签恢复到最近一次非极端比例。
>   - **双击** 收起成 0(等同隐藏该格);再双击恢复。
>   - 比例持久化。
> - **面板可在格子间移动**:右键格子标题栏 → "移动到 → 上左/上右/下左/下整行"等;或直接拖拽标题栏到目标格子的边缘,出现高亮占位后松手。
> - **更复杂的 "(>2)×(>2)"**(如 3×3、4 象限)作为 v2 议题,本期不做(本期 ≤ 2×2)。
> - 终端面板与聊天面板的"是否同源"是独立的:terminal 与 chat session 解耦,关闭 chat 不会关掉 terminal;关闭 terminal 不影响 chat。

**面板占用约定**(一个面板在 Workspace 内只能出现一次):
- `chat` 默认占一格(主格),可被折叠成 CenterGrid 顶部书签并恢复,但不能从 occupancy 中删除。
- `terminal` 默认占一格(可被收起成 0)。
- `files / tasks / drawer / skills / memory` 抢剩下的格子;超出时,优先放 Dock 区,放不下时给出"Workspace 已满,先收起某个面板"的提示。

### 3.3 移动端(Workspace 内二级 Tab 导航)

- **一级 AppNav 不调整**:原本的左侧应用导航(聊天 / 文件 / 自动化 / 节点 / 笔记 / 技能 / 记忆 / 设置 / 用量)在移动端依旧从左侧抽屉式呼出(汉堡按钮),行为与现在一致,**本 SPEC 不改造一级 AppNav**。
- **Workspace 顶部新增二级 Tab 栏**(仅 < 1024px 时显示):`💬 对话框 | 🖥 Terminal | 📁 文件 | 🗂 抽屉 | 📋 任务中心`。这五个 Tab 切换的是 **当前 Workspace 里展示哪一个子视图**,每次只显示一个,不做网格。
  - **Tab 是工作区级状态**:PC 与移动端复用同一份 store(但 PC 上 Tab 是次要入口,网格布局优先;移动端上 Tab 是主入口)。
  - **状态可记忆**:选中的 Tab 写入 `localStorage`,刷新保留。
  - **切换 chat session 时,默认回到 `💬 对话框` Tab**(其他 Tab 仅是辅助视图)。
- **不做网格、不显示书签条**:Workspace 整列全屏,文件/终端/任务都是独立子视图,移动端上不分组、不分屏。
- < 768px 一级 AppNav 收纳为左侧抽屉(汉堡);Drawer 类内容用 `useBreakpoint` 升级为全屏。

---

## 4. 关键模块设计

### 4.1 工作区框架 `WorkspaceShell`

新增组件 `components/workspace/WorkspaceShell.tsx`,作为聊天路由(以及未来"工作台"型路由)的统一外壳。

- Props:`center: ReactNode`、`docks?: Record<"left" | "right" | "bottom", PanelConfig>`,`panel` 可为 `chat | terminal | files | tasks | drawer`。
- 内部分四栏:
  1. `<AppNavRail>` —— 来自现有 `Sidebar` 改造,新增 `collapsed` 状态(用 antd `Button` icon 切换,折叠后仅图标、tooltip 显示名称)。
  2. `<SessionListRail>` —— 同上,折叠后变窄条,顶部一个 ➕ 新建、点开弹浮层显示 session 列表(类似 VS Code Explorer 折叠行为)。
  3. `<CenterGrid>` —— Workspace 中心区,2×2 网格 + 5 种预设(见 §3.2)。由两层 `Splitter` 嵌套,任意分隔条可拖拽或双击收起。
  4. `<DockPanel>` —— 右侧(以及可选底部)抽屉,内容根据 dock 选中态决定。
- **移动端(< 1024px)**:`AppNavRail` 与 `SessionListRail` 隐藏(`display: none`),Workspace 占满全宽;Workspace 顶部渲染 `<MobileWorkspaceTabs>`(`对话框 / terminal / 文件 / 抽屉 / 任务`),根据选中态切到对应子视图。

### 4.1.1 任务中心进入共享右侧 inspector 抽屉(用户决定,P1 修正点)

- **不**新增独立 TaskCenter Drawer;任务中心与 `待审批 / 上下文与召回 / 轮次 / Subagents / LongTasks / Timeline` 共用 ChatPage 右侧 `chat-inspector` 面板。
- **Tab 位置**:任务中心是 inspector 的第一个 Tab。点击聊天 header 里的 `任务 N` chip 会展开右侧 inspector 并选中 `任务中心`;如果当前已经展开且选中任务中心,再次点击 chip 会收起 inspector。
- **收起入口**:共享 inspector 顶部提供 `收起` 按钮;任务中心面板自身的收起动作也折叠整个 inspector,而不是打开另一个 App 级 Drawer。
- **移动端**:任务中心仍显示在移动端 inspector Drawer 内,与其他 inspector Tab 共用同一抽屉;一级 AppNav hamburger 只负责移动端 AppNav。
- **抽离位置**:`features/tasks/` 保留轻量 header chip 与 selector;完整任务中心面板仍由 ChatPage 传入 inspector,因为它依赖 ChatPage 本地 mutation/query。
  - `<TaskCenterChip />` —— chat header 角标(`任务 N 🔴` / `任务 N 🟠`)
  - `selectTaskCenterHeaderContract(state) => { running, blocked, pendingReview, merged, failed, total, hasUnread, dotColor }` —— 纯函数 selector
- **数据源不动**:任务统计来自现有 `useChatStore.overlayItems` 中 `kind === "command" | "subagent" | "warning" | "error"` 的事件流;P1 只重组视图。

### 4.2 任务中心迁移

- 抽出 `features/chat/components/TaskCenterInline.tsx`(当前内联在 `ChatPage.tsx` 顶栏),重构为 `features/tasks` 模块,提供:
  - `<TaskCenterDockPanel />`:完整列表(运行/阻塞/待审核/合并/失败 tab + 时间线)
  - `<TaskCenterChip />`:折叠态顶栏 chip(显示"任务 N"+ 红色未读角标)
  - `<TaskCenterTrigger />`:右侧 dock 书签条上的入口
- 在聊天页 header 中,任务中心替换为一个 **chip + 展开/收起按钮语义**;展开动作 = 展开右侧 inspector 并切到 `任务中心` Tab;再次点击当前任务中心 chip = 收起右侧 inspector。
- 任务中心状态/数据来源不动(继续走 `features/chat` 现有 store / SSE),只重组视图。

### 4.3 文件 app 内嵌

- 不再是路由独占,而是 `features/files` 提供 `<FilesPanel mode="dock" />` 与 `<FilesPanel mode="page" />` 两个容器。
- `mode="page"` 维持现有 `FilesPage`(移动端 + 旧菜单仍可用)。
- `mode="dock"` 提供:
  - 顶部工具条:`新建文件夹 / 上传 / 搜索 / 路径面包屑`
  - 左侧树(可折叠为 40px 图标列)
  - 右侧预览/编辑器(支持 markdown / 代码只读预览,不重写编辑器)
  - 在网格内宽屏展示时,文件树与预览保持左右并排,避免上下堆叠浪费横向空间。
- 挂载位置:Workspace 网格的任一格,或 Dock 区右侧;移动端二级 Tab `📁 文件` 直接走 `mode="dock"`。
- 文件/目录的数据接口不变(走 `features/files` 已有的 `lib/api.ts`)。

### 4.4 Terminal 面板

- 新增 `features/terminal`:
  - `TerminalPanel.tsx`:用 `xterm.js` + `xterm-addon-fit`(新增依赖:`@xterm/xterm`、`@xterm/addon-fit`)。
  - 后端:复用现有 `internal/sandbox` + `internal/runtime` 的 exec 通道;Web 侧通过 SSE/WS 与之通讯。**第一版若后端没有 WebSocket,可降级为 polling 拉取 buffer,SPEC 标注为"v1.0 可降级"**。
  - 生命周期:每个 session 一个 PTY(或者全局一个 PTY 池,先简单实现:每开一个 terminal tab = 一个 PTY),与当前 chat session 解耦,无 chat session 也能用。
- 入口:Workspace 网格(下整行 / 下左 / 下右 任一格)、右侧 Dock 书签;移动端二级 Tab `🖥 Terminal`。
- 快捷键:`Ctrl/Cmd + \`` 唤起/隐藏 terminal(仅 PC)。

### 4.5 折叠 / 书签条

所有可折叠面板在折叠态下变成 **窄条书签**:

- 左侧书签条:垂直排列 icon,顶部一个 `<<` 按钮展开。
- 右侧书签条:垂直排列 icon,顶部一个 `>>` 按钮展开。
- 底部书签条:水平排列 icon,右侧一个 `v` 按钮展开。
- 角标:任务中心显示未审/未合并数;终端显示是否有进行中的 job;文件显示是否有未保存改动。

### 4.6 状态持久化

`store/layout.ts`(新增,zustand):

```ts
type PanelKey = "appNav" | "sessions" | "chat" | "tasks" | "files" | "terminal" | "drawer";
type PanelState = { collapsed: boolean; width?: number; visible: boolean };
type GridPresetId =
  | "topChat_bottomTerminal"
  | "topFilesChat_bottomTerminal"
  | "topChat_bottomFilesTerminal"
  | "leftCol2x2"
  | "single";
type Slot = "topLeft" | "topRight" | "bottomLeft" | "bottomRight" | "topFull" | "bottomFull";

type LayoutState = {
  panels: Record<PanelKey, PanelState>;
  centerGridPreset: GridPresetId;
  centerGridRatios: {
    outerSplit: number;        // 0~1,top vs bottom
    innerTopSplit?: number;     // 0~1,topLeft vs topRight
    innerBottomSplit?: number;  // 0~1,bottomLeft vs bottomRight
  };
  centerGrid: {
    topLeft: PanelKey | null;
    topRight: PanelKey | null;
    bottomLeft: PanelKey | null;
    bottomRight: PanelKey | null;
  };
  mobileActiveTab: "chat" | "terminal" | "files" | "drawer" | "tasks";
  dockSide: "right" | "bottom";
  // actions
  toggle: (k: PanelKey) => void;
  setWidth: (k: PanelKey, w: number) => void;
  setGridPreset: (id: GridPresetId) => void;
  movePanelToGrid: (panel: PanelKey, slot: Slot) => void;
  setGridRatio: (key: keyof GridRatios, v: number) => void;
  setMobileActiveTab: (t: MobileTab) => void;
  reset: () => void;
};
```

- localStorage 同步 + 跨标签 `storage` 事件监听,保证多 tab 一致。
- 移动端(< 1024px)进入时,所有 panel 强制 `visible=false, collapsed=true`,只渲染 `mobileActiveTab` 对应的子视图。

---

## 5. 任务中心在聊天 header 中的最终表现(详细)

聊天 header 自左到右:

```
[新会话] [模型▾] [Reasoning▾] [●流连接已建立] [Idle] [🗑][⎇]   ……   [任务 N 🔴] [⋮]
```

- 任务中心不再占据整行,而是一个 chip 形式:`任务 N`,有红点(待审核>0)或橙点(阻塞>0)。
- 点击 `任务 N` = 展开共享右侧 inspector 并选中 `任务中心` Tab;当前已展开且选中任务中心时再次点击会收起 inspector。
- `待审批 / 上下文与召回 / 轮次 / Subagents / LongTasks / Timeline` 与 `任务中心` 同属右侧 inspector Tabs,不再有独立 TaskCenter Drawer。

---

## 6. 验收与度量

- 视觉/功能:
  - PC ≥ 1280px 截图里默认能看到 `AppNav + SessionList + (Files|Chat) + Terminal` 四列,且 `Dock` 折叠成右侧书签条 ≤ 40px。
  - 切换到 Preset 1(上 chat / 下 terminal)后,terminal 占下整行;Preset 4(左列 2×2)后,左列上下分别是 files 与 terminal,右列整列 chat。
  - 移动端 375px 截图里:左侧抽屉式 AppNav(汉堡)+ 顶部二级 Tab 栏(对话框/terminal/文件/抽屉/任务)+ 当前子视图。无书签条、无网格。
- 行为:
  - 刷新页面后,所有折叠/宽度/网格预设/比例/格子占用/移动端选中 Tab 都保留(读 `localStorage`)。
  - 切换路由后再回来,布局不变。
  - `Ctrl/Cmd + \`` 可唤起/隐藏 terminal(PC only)。
  - 顶部"任务 N"chip 数字与 `features/tasks` 实时数据一致(SSE 推送)。
  - 双击网格分隔条,该格被收起成 0;再双击恢复。
  - 拖拽面板标题栏到目标格子的边缘,出现高亮占位,松手后面板移动到目标格。
- 兼容:
  - 现有 `Drawer` 入口仍能打开(URL hash 不变)。
  - 现有 `/files` 路由仍可单独打开(对外链接不破)。
  - 一级 AppNav 行为不变(本 SPEC 不改造)。
- 性能:
  - 折叠/展开/网格切换首帧 < 16ms;终端滚动 FPS ≥ 30(基于 xterm 自身能力)。
- 国际化:新增文案进 `i18n/`,zh-CN 与 en 双语。

---

## 7. 非目标 / 后续

- 不在本期重写 `MessageFeed` 的渲染管线,只做位置调整。
- 不在本期引入 Monaco 编辑器(文件预览保持只读 CodeMirror)。
- 终端多 tab / 终端会话保存为后续迭代。
- 真正的"项目工程"概念(多 workspace / 工作区)作为 v2 议题。
- **(>2)×(>2) 的网格 / 任意拖拽布局作为 v2 议题。**
- **一级 AppNav 的移动端行为改造 / 桌面端侧栏布局改造不在本 SPEC 范围。**

---

## 8. 实施分阶段(与 §9 任务清单对应)

| 阶段 | 状态 | 内容 | 当前实现/验证 |
|---|---:|---|---|
| P0 | [x] | `WorkspaceShell` 骨架 + AppNav/Session 折叠 + CenterGrid 2×2 + 5 种预设 + 移动端二级 Tab(不改造一级 AppNav) | 已抽出 `components/workspace/WorkspaceShell.tsx`;其余能力分布在 `ChatWorkspaceCanvas.tsx`、`CenterGrid.tsx`、`MobileWorkspaceTabs.tsx`;有 layout / centerGrid / mobile tabs / workspace shell 单测 |
| P1 | [x] | 任务中心抽离 + 顶栏 chip 化 | `features/tasks` selector/chip 已落地;完整 TaskCenterPanel 并入 ChatPage 右侧 inspector Tabs,chip 负责展开/收起并选中任务中心 |
| P2 | [x] | 文件 panel 内嵌(dock / 网格内) | `FilesPanel mode="dock" | "page"` 已落地;`/files` 路由兼容;移动端 files tab 使用 dock panel |
| P3 | [x] | Terminal panel(xterm + 后端协议) | `TerminalPanel` + `terminalClient.ts` + Go HTTP terminal routes 已落地;M1+ 已替换 mock client |
| P4 | [x] | 布局持久化 + 快捷键 + i18n | `layoutPersistence.ts`、跨 tab storage sync、`Ctrl/Cmd+\``、en/zh 文案与测试已落地 |
| M1-B | [x] | Playwright 验收基础设施 | `ui/web/e2e` 已加入;已加固错误边界断言与截图反馈问题覆盖,desktop/mobile project 可跑 |
| M1-C | [~] | 3×3 / 4 象限网格 | 3x3 preset 的 store/renderer/test 已落地;2x2/3x3 面板移动交互已落地;4 象限和任意拖拽布局未做 |

---

## 9. 任务清单(将由 TDD 流程拆分)

> 这里按当前代码状态标注。已完成项仍可能需要视觉 polish,但核心能力与测试已落地。

- [~] T1:WorkspaceShell 组件 + 折叠/书签条交互;CenterGrid 2×2 网格 + 5 种预设
  - 已完成:独立 `WorkspaceShell.tsx`、AppNav/Session/CenterGrid/MobileTabs 的等价工作区骨架、2×2 preset、基础折叠;CenterGrid chat/files/terminal 可折叠成顶部书签并恢复,书签条常驻不消失;SessionList 可收起为 40px rail。
  - 未完成:把 CenterGrid/Session/Files 当前分散实现收敛为完整书签条统一抽象和统一角标策略。
- [x] T2:AppNav collapsible(`components/Sidebar.tsx` / `App.tsx` 改造)
- [x] T3:SessionList collapsible + 折叠态下浮层入口
- [x] T4:聊天 header 顶栏重排(任务 chip / 共享 inspector 入口 / 菜单)
- [x] T5:`features/tasks` 模块拆分 + 顶栏 chip 实时角标
- [x] T6:文件 panel `mode="dock"` + 与 `FilesPage` 复用;网格内支持文件树折叠和树/预览左右并排
- [x] T7:Terminal panel + xterm 集成 + 后端通讯;支持在网格和移动端 Tab 中安放
  - M0 v1:前端 polling fallback。
  - M1+ v2:真实 Go PTY HTTP client + backend routes。
- [x] T8:`store/layout.ts` 持久化 + 跨 tab 同步
- [x] T9:移动端二级 Tab 导航(Workspace 内子视图) + 断点适配;不改造一级 AppNav
- [x] T10:i18n 文案 + 可访问性(键盘可达、aria-label)
- [x] T11:Vitest 单元测试(reducer、布局、折叠、网格预设)
- [~] T12:Playwright/手动截图回归(PC 各预设 + Mobile 各一套)
  - 已完成:Playwright 验收基础设施 + desktop/mobile route smoke + 错误边界断言 + 截图反馈问题的功能验收覆盖。
  - 未完成:真正截图 diff/视觉基线。
- [x] T13:网格预设切换 + 拖拽分隔条 + 双击收起;面板在不同格子间移动(右键菜单或拖拽标题栏)
  - 已完成:store preset 切换、ratio 持久化、splitter 本地预览/松手持久化、接近 0/100% 的拖拽自动转书签折叠并用最近安全比例恢复、双击收起/恢复、3x3 preset、`movePanelToGrid` / `swapPanelInGrid` / `swapGridSlots` store actions、标题栏菜单、标题栏拖拽、目标高亮、用户可见反馈、chat/files/terminal 面板折叠成书签并恢复。

## 10. 下一步建议

1. **先整理最近提交历史**:把 debug/isolate/patch/feature 提交压成清晰的 crash fix、T13 interaction、e2e hardening,避免远端历史包含排障噪声。
2. **收敛书签条统一抽象**:CenterGrid/SessionList/FilesPanel 已有可用折叠入口;下一步可抽统一的 bookmark rail/strip contract,减少重复 UI 与状态策略。
3. **补视觉回归**:在现有 Playwright 基础上加固定 viewport 截图 diff,覆盖 PC 默认布局、3x3 preset、mobile tabs、共享 inspector 任务中心和 terminal。
4. **继续 M1-C 后半段**:如确有产品需求,再单独推进 4 象限 / 任意拖拽布局;不要和已完成的 2x2/3x3 面板移动交互混为一项。
5. **如要改一级 AppNav**,另开新 SPEC:当前 SPEC 明确不改造一级 AppNav,不要混入本工作区升级尾项。
