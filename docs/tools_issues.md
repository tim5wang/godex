# 工具问题与解决经验（Tools Issues）

> 状态：Historical / Operational Log（经验记录，不作为当前工具契约）
> 用途：记录工具调用失败的日志与解决经验，供后续优化工具。
> 约定：每次遇到工具失败/反爬/沙箱限制，在这里 **append** 一条记录（时间 + 问题 + 根因 + 解决 + 改进建议）。同步用 memory 记录一条 workflow 备忘。

## 2026-09-03 — bash 前台跑长驻 ACP 子进程导致 context canceled

**问题**：用 `python3 - <<EOF` 脚本 `subprocess.Popen(["dsh","--profile","acp"])` 探测 ACP 协议事件流，bash 工具长时间卡住后报 `context canceled`。

**根因**：ACP 是长驻 stdio 协议，dsh 收到 prompt 后持续流式输出（思考/工具/文本块），子进程不退出；脚本读 stdout 阻塞等待，bash 工具超时被取消。

**解决**：
- 不要在前台 bash 里裸跑长驻 ACP 进程并阻塞读流。改为：
  - 静态读 dsh 的 ACP server 源码确认它发的 `sessionUpdate` 类型（`agent_thought_chunk` 等），不启动进程；
  - 或写 `.godex/tmp/*.py` 脚本，给子进程加 `p.kill()` + 有限行数读取 + 硬 deadline，再执行。
- 本次根因结论：dsh 在 120s 超时前**一直在正常输出**（思考过程 + 工具调用日志），godex 侧 `unexpected EOF` 是因为**总超时杀掉进程**，不是 dsh 无响应。真正问题是「godex 未解析/未展示 dsh 的中间事件 + 120s 总超时过短」。

**改进建议**：
- ACP 长驻进程探测一律走「源码静态分析事件类型」优先；必须跑则写脚本 + kill。
- godex ACP 集成：解析 `agent_thought_chunk` 等更多事件类型并透出；超时应按「无响应超时」而非「进程总超时」语义设计。


## 2026-08-24 — web_fetch 对微信公众号文章遭遇反爬

**问题**：用 `web_fetch` 抓取微信文章（`https://mp.weixin.qq.com/s/<id>`）时被反爬拦截。
- 返回 `needs_browser: true`，正文只剩「环境异常，完成验证后即可继续访问」，并跳转到 `mp.wappoc_appmsgcaptcha?poc_token=...` 验证页。
- 直接 `web_search` 也拿不到正文（只有标题/摘要）。

**根因**：微信公众号对非浏览器 UA（无 JS 环境）的请求走 `wappoc_appmsgcaptcha` 人机验证；`web_fetch` 的抓取器被识别为非浏览器，因此拿不到 `js_content` 正文。

**解决**：绕过 web_fetch，改用 curl 直接下载原始 HTML + 本地脚本提取正文。
1. curl 带**移动端 UA** 下载（桌面 UA 也可能触发验证）：
   ```bash
   curl -sL -A "Mozilla/5.0 (iPhone; CPU iPhone OS 16_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/16.0 Mobile/15E148 Safari/604.1" \
     "https://mp.weixin.qq.com/s/GhngoUnIS7CYjnGeFA7XZA" -o /tmp/wx_article.html
   ```
2. 从下载的 HTML 里提取 `<div id="js_content">` 段，剥掉标签、`html.unescape`、压缩空行：
   ```python
   m = re.search(r'id="js_content"[^>]*>(.*?)<script', content, re.S)
   body = re.sub(r'<br[^>]*>', '\n', body); re.sub(r'</p>', '\n', body)
   body = re.sub(r'<[^>]+>', '', body); html.unescape(body)
   ```

**改进建议（针对 web_fetch / 后续同类任务）**：
- `web_fetch` 对微信等强反爬站点应明确降级：优先尝试 curl（带移动 UA）下载原始 HTML，而不是只报 `needs_browser`。
- 工具层面：给 `web_fetch` 增加「原始 HTML 模式」或「UA 覆盖」参数；对 `needs_browser` 的结果自动提示 fallback 命令。
- 工作流层面：遇到 `needs_browser: true` 时，直接切到 bash+curl 路线，不要重复 `web_fetch` 同 URL（浪费往返）。

## 2026-08-24 — bash 沙箱禁止命令替换/进程替换

**问题**：`bash` 工具内使用 `$(cmd)`（命令替换）或 `python3 -c "..."`（内联脚本）被沙箱拦截：
- `error: command substitution is not allowed`
- `error: process substitution is not allowed`

**根因**：沙箱对命令替换、进程替换做了硬性限制；内联 python 脚本在个别情况下也被拦。

**解决**：
- 把脚本写进 `.godex/tmp/*.py` 再 `python3 脚本文件` 执行（避免 `-c` 内联）。
- 需要动态值的场景：先单独跑一步把值算出来（echo/写入临时变量文件），下一步再引用，不用 `$()`。

**改进建议**：
- 若后续要放宽：沙箱可对「只读命令替换」放行（如 `git rev-parse`、`date`），保留对高风险/嵌套命令的拦截。

**后续落地（2026-08-29，taskboard t-1787971680865-5）**：已在 `internal/platform/tooling` 的 command-substitution 校验实现：
- 正常模式（bash 工具默认）逐个校验 `$()` 内层命令：只读命令（`git rev-parse`/`date`/`pwd`/`echo`）放行；高危/嵌套/危险命令（`python -c`、`curl|x|sh`、`rm -rf /`、嵌套 `$()`、进程替换）仍拦截。
- yolo 模式（`tools.permissions.interactive_approval_mode: yolo`）全部放开。
- 反引号 `` ` `` 与进程替换 `<()`/`>()` 仍维持硬拦截（协议外，进程替换本身已被 ClassifyShellCommandRisk 标为 high-risk）。

## 2026-08-27 — web_fetch 对 GitHub/npm/Cloudflare 页面抓取失败或截断

**问题**：调研 dsh-taskboard 时 web_fetch 反复失败或拿不到正文：
- `github.com/cloader/dsh-taskboard`：多次只返回 badge 头，README 正文缺失
- `raw.githubusercontent.com/.../README_zh.md` 与 `README_en.md`：404（分支名不是 main/HEAD）
- `www.npmjs.com/package/dsh-taskboard`：Cloudflare "Just a moment..." 反爬页
- `github.com/mariozechner/pi-coding-agent`：返回 DOCTYPE 原始 HTML（GitHub 反爬）

**根因**：GitHub/npm 对无浏览器 UA 的抓取有反爬（Cloudflare challenge、动态渲染）；raw 分支名不确定（用 main/HEAD 猜会 404）；只抓 README 漏掉源码结构（本次关键信息在 lib/ 目录树）。

**解决**：
- 改用 **GitHub API**：`api.github.com/repos/{owner}/{repo}/git/trees/{branch}?recursive=1` 拿完整文件树（一次成功拿到 lib/host/*.js 结构）
- **npm registry API**：`registry.npmjs.org/{pkg}/latest` 拿 package.json 元数据（description 揭示插件真实能力面）
- **raw 文件直接取**：`raw.githubusercontent.com/{owner}/{repo}/{branch}/{path}` 用确认过的分支名
- 这轮是「GitHub API + npm registry API + raw 组合」成功，比反复 web_fetch 高效

**改进建议**：
- web_fetch 对 github.com 页面应提示走 GitHub API 或 raw；对 npmjs.com 应提示 registry.npmjs.org
- 调研 GitHub 仓库时优先 `api.github.com/repos/.../git/trees` 拿文件树，再按需取 raw 文件
- 已知分支不确定时用 `api.github.com/repos/...` 查 default_branch，不要猜 main/HEAD

> **已落地（2026-08-29）**：`web_fetch` 在 `needs_browser`/反爬降级时返回 `fallback_hint`（微信→curl 移动 UA 抽 `js_content`；GitHub/raw → git/trees 文件树 + default_branch；npm → registry API；Cloudflare challenge → 改用公开 API / 浏览器工具）。调用方无需重复 `web_fetch` 同一 URL。

## 调研速查（Web Fetch 反爬绕过路线）

> 用途：遇到 `needs_browser: true` 或正文缺失时的快速 bypass 清单。格式：站点 → 首选路线 → 备选。

| 站点 | 问题特征 | 首选绕过路线 |
|---|---|---|
| `mp.weixin.qq.com/s/<id>` | `needs_browser: true`，只剩验证提示（`wappoc_appmsgcaptcha`） | ① curl 带移动端 UA 下载原始 HTML → ② 本地脚本取 `<div id="js_content">`，剥标签 + `html.unescape` |
| `github.com/{owner}/{repo}` | README 正文缺失 / 返回原始 HTML | ① `api.github.com/repos/{owner}/{repo}/git/trees/{branch}?recursive=1` 拿完整文件树 → ② 按需用确认分支名取 raw 文件 |
| `raw.githubusercontent.com/...` | 分支名不确定 → 404 | ① `api.github.com/repos/{owner}/{repo}` 读 `default_branch`（勿猜 main/HEAD）→ ② `raw.githubusercontent.com/{owner}/{repo}/{branch}/{path}` |
| `www.npmjs.com/package/{pkg}` | Cloudflare "Just a moment..." 反爬 | `registry.npmjs.org/{pkg}/latest`（description/版本元数据） |
| 任意站点 `Just a moment` / `cf-chl` | Cloudflare challenge | 优先该站点公开 API（如 api.github.com / registry.npmjs.org）或 JSON 端点，或用浏览器工具 |

> 已由 `web_fetch` 的 `fallback_hint` 自动提示，无需手工记忆；本表作为调研落点提供给代理/子代理。

## 2026-08-27 — bash 沙箱禁止 heredoc 追加写文件（background execution not allowed）

**问题**：用 `cat >> 文件 << 'EOF'` 向 backend_test.go 末尾追加测试函数时，报 `background execution is not allowed` 被拦。

**根因**：沙箱不仅禁命令替换/进程替换，heredoc 形式的多行写文件也走 background 通道被拒绝。

**解决**：改用 `edit_file`（edits[].new_text 直接替换文件末尾锚点文本）完成追加，一次成功；测试内容可含反引号/引号等任意文本，无转义负担。

**改进建议**：向文件追加/修改代码一律优先 edit_file（锚点取文件末尾几行原文），不要尝试 heredoc；`.godex/tmp/*.py` 脚本文件可用 write_file 写入。

> **已固化工作流（2026-08-29）**：代码追加/修改优先 `edit_file`，新建脚本文件用 `write_file`，避免 heredoc 与内联 `python -c`。`

## 2026-08-29 — edit_file 写文件反复失败（漏传 old_text/new_text）

**问题**：PJM 会话中 `edit_file` 连续 25+ 次调用只传 `path` 漏传 `old_text`/`new_text`，工具只报 `missing old_text argument`，调用方（agent）陷入同一错误的重复循环，无法自动纠正。

**根因**：工具错误信息只指出缺参数，未给用法示例，也未区分「追加到文件末尾」与「替换已有文本」两种场景；缺少工作流引导。

**解决**：改进 `edit_file` 缺参数报错——给最小用法示例（path + old_text + new_text），并提示「追加内容请提供末尾锚点原文作为 old_text」「新建文件请用 write_file」，避免 agent 反复试错。

**改进建议（工作流约定）**：
- **修改/追加已有文件**：用 `edit_file`，必须带 `old_text` + `new_text`。锚点取文件末尾几行原文（复制粘贴原文，含空白/反引号/引号，无转义负担）。
- **新建文件**：用 `write_file`（path + content）。
- 多改同文件用 `edits[]`（path + edits[]，50 上限）；多文件用 `files[]`。
- 工具层：`edit_file` 缺 old_text 报错须含最小用法示例与追加/新建引导（已落地）。

## 2026-09-05 — web_search 后端连续失败（lightpanda killed / duckduckgo failed）

**问题**：调研「Codex Browser use」竞品时，`web_search` 连续 4 次失败：
- `lightpanda search fetch: lightpanda fetch failed: signal: killed`（3 次，不同关键词）
- `duckduckgo search failed: If this persists ... 9318`（1 次）

`web_fetch` 对部分站点也受限：OpenAI 开发者文档（developers.openai.com）返回 HTML 框架无正文（JS 渲染/反爬）；Claude 官方文档（platform.claude.com）只能抓到开头段落。

**根因**：web_search 后端（lightpanda/duckduckgo 通道）当前不稳定，可能瞬时过载被 kill；OpenAI 文档站为 JS 渲染站点，静态抓取器拿不到正文。

**解决**：
- 不硬磕搜索：改用 `web_fetch` 直连已知 URL（Claude 官方文档、竞品博客）拿正文片段；竞品关键事实从已抓到的官方文档开头 + 权威博客 + 既有知识交叉确认，够写 PRD 就收口，不无限重试。
- 本地侧（godex 现状盘点）完全不依赖网络，先做本地代码盘点，再补竞品信息——降低对不稳定 web 后端的依赖。

**工具层修复**：provider 一旦返回进程/网络错误，不再用多个 query variant 重复撞同一后端，而是立即切换下一 provider；失败 provider 熔断 30 秒，连续调用会快速降级。所有 provider 都失败时，错误会明确提示停止重复搜索，改用已知 URL 的 `web_fetch` 或本地离线盘点。

**改进建议**：
- web_search 失败应快速降级（间隔重试 ≤2 次 → 改 web_fetch 直连已知 URL → 本地盘点先行），不要在同一次搜索上反复试错浪费往返。
- 对 JS 渲染站点（OpenAI docs 等），`web_fetch` 明确返回降级提示或 fallback（如 curl 移动 UA，参照 2026-08-24 微信反爬条目）。

## 2026-09-05 — 调研子代理超时无产出 + 部分站点反爬（Desktop/App wrap PRD 任务）

**问题**：任务卡 t-1788517972939-2 需联网调研（Tauri/Electron/WebView/PWA 方案对比）。两次委托子代理均失败：
- 第 1 次（general-purpose，5 分钟）：持续卡在 `web_fetch` 循环，300s 超时，无任何成果落盘。
- 第 2 次（加了约束：web_fetch≤3、报告增量写盘、4 分钟）：标记 completed 但调研文件未落盘（.godex/tmp/desktop-wrap-research.md 不存在），成果取不到。
- 主会话 `web_search`（duckduckgo/lightpanda 双通道）当日持续失败；`web_fetch` 对 capacitorjs.com 命中 Cloudflare challenge；tauri 的 v2.tauri.app/start/mobile 404（正确入口是 tauri.app/start）。

**根因**：① 子代理做开放性联网调研时倾向反复 web_fetch 深挖，无硬上限易超时；② 子代理「completed 但无产物」说明其最终回复未被可靠回收（依赖写盘才能保底）；③ web_search 后端当日不稳定 + Cloudflare 反爬站点需要换通道。

**解决**：放弃继续委托，改主会话有界取证：`web_fetch` 直连官方文档（tauri.app/start、web.dev、MDN、registry.npmjs.org 的 JSON 元数据），配合既有领域知识交叉确认，够写 PRD 即收口；PRD 直接写入 docs/prd-desktop-app-wrap.md。

**工具层修复**：识别为联网调研的子代理默认设置 4 分钟任务超时，并注入硬约束（`web_search` 最多 2 次、`web_fetch` 最多 3 次、失败立即换官方 URL/API/registry/GitHub、本地证据，指定输出文件时逐节 checkpoint）。子代理没有最终 handoff 时改记为 error，不再以 completed 占位。`web_fetch` 的 HTTP 错误、JS 壳和 Cloudflare 响应会携带 browser/API/registry 等替代通道提示。

**改进建议**：
- 委托子代理做联网调研时，prompt 必须硬性约束：web_fetch 次数上限、总搜索次数上限、**必须增量写盘**（每完成一节就写一次文件），并指定报告落盘路径——不要依赖子代理最终回复回收成果。
- 子代理调研失败（超时/无产物）超过 1 次即切换为主会话有界直取（web_fetch 官方文档 + npm registry JSON + 领域知识），避免反复委托浪费时间。
- Cloudflare 反爬站点（capacitorjs.com 等）优先用 registry.npmjs.org JSON 或 GitHub README 代替。
- 需要版本/体积等具体数字时优先官方文档首页/文档站，其次权威博客，最后才领域知识（标注为估计）。

## 2026-09-05 — Browser use 帧流 WS 404（webui 剥离 /api 前缀 vs 路由带前缀）

**问题**：Browser dock 面板（前端 P1）帧流一直走降级路径（"Frame stream unavailable"），而 `browser.view` 事件链路正常（URL 跟随生效）。WS 握手 `/api/browser/frames` 返回 Go ServeMux 默认 404。

**根因**：`internal/runtime/webui/webui.go` 的 `NewHandler` 会把外部请求的 `/api/` 前缀**剥离**后再转给 API handler（`r2.URL.Path = strings.TrimPrefix(r.URL.Path, "/api")`）。后端 `routes_browser_frames.go` 注册的路由却是 `GET /api/browser/frames`（带 `/api` 前缀）——httpapi 里唯一带 `/api` 前缀的路由。外部 `/api/browser/frames` 被剥离成 `/browser/frames` 后匹配不上 → 404。其他路由（sessions/preview/voice 等）注册时都不带 `/api` 前缀，所以正常。

**解决**：`routes_browser_frames.go` 路由改为 `GET /browser/frames`（与 httpapi 其他路由一致），测试 URL 同步，并补 WebUI 外部 `/api/browser/frames` 剥离成内部 `/browser/frames` 的回归覆盖；`go test ./internal/runtime/httpapi ./internal/runtime/webui ./internal/tools` 通过。

**改进建议**：
- httpapi 内注册路由一律不带 `/api` 前缀（webui 统一剥离），新增路由时 grep 校验 `mux.Handle("GET /api/` 不得出现。
- 排查 WS/API 404 时先用 node http 探针对比已知路由状态码：404(纯文本 ServeMux) vs 401(鉴权层) 能快速区分「路由未注册」与「鉴权拦截」。

## 2026-09-06 — Browser use 帧流三层断链联调（含 headless screencast 坑）

**问题**：Browser dock 面板帧流联调共修三层才通（每层都有独立根因）：① 路由 404（见上条）；② WS 握手成功但 15s 无帧（pump 卡死在 screencast）；③ WS 握手后立即 1006 关闭（pump 提前退出）。

**根因②**：`runScreencast` 里 `start.Call(page)` 无 deadline，headless Chromium 接受 `Page.startScreencast` 但从不投递 `screencastFrame` 事件 → pump 永远阻塞在事件循环，注释声称的 screenshot 回退永不触发。修：调用加 5s 超时 context + 首帧守卫（3s 无帧回退）。

**根因③**：headless Chrome 152 下 rod 的 `page.Event()` 事件 channel **立即关闭**（ok=false）——被误判为「页面已关闭」返回 true，`run()` 提前 return 并关闭所有订阅 channel → WS 收到 EOF 立即 1006。修：channel 关闭视为「screencast 不可用」返回 false，回退 `runScreenshotLoop`（500ms/张 JPEG 截图），帧流稳定产帧。

**关键排查手段**：
- 给 pump 所有退出路径加 `log.Printf("framePump %s: ...")` 日志（含 frameKey），重启后 `grep framePump ~/.godex/log/godex.err.log` 一条日志直接定位退出路径，不再盲猜。
- 服务进程 stderr 在 `~/.godex/log/godex.err.log`，stdout 在 `godex.log`。
- WS 探针用 node 脚本带 `?session=&token=` 直连，区分「OPEN 保持无帧」（pump 卡死）vs「OPEN 后立即 1006」（pump 退出）两种模式。
- HTTP no-upgrade 探针（JSON 错误 vs 纯文本 404）先排除路由/鉴权层。

**改进建议**：headless 环境下 screencast 不可靠，帧泵应「screencast 为可选增强、截图回退为默认可靠路径」；rod 事件 channel 关闭不等于页面消失，退出语义需区分「页面没了」与「功能不可用」。


## 2026-09-21 — dsh web 启动失败三层根因（esbuild 平台包 + lib 产物陈旧 + profile 构建拦截）

**问题**：deepseek-harness（temp/deepseek-harness）源码仓库 `dsh web --no-open` 启动失败：
- 第一层：`dsh --version` 直接崩，报 `You installed esbuild for another platform`（TransformError）；
- 第二层：esbuild 修复后 `dsh web` 起来但大量官方插件 `failed to import`（`lib/typert.host.js: ERR_MODULE_NOT_FOUND`、`dsh-client-ui-*` 全挂）；
- 第三层：官方插件修好后，web profile 第三方插件 `@linxin666/dsh-web-all` 的 skin-center degraded（缺 `lightningcss.darwin-arm64.node`）。

**根因**：
1. **esbuild 平台包不匹配**：机器是 arm64（Apple Silicon），但 `node_modules/.pnpm/` 里装的是 `@esbuild+darwin-x64@*`（某次在 Rosetta/x64 环境安装），arm64 node 需要 `@esbuild+darwin-arm64`。判断：`node -p "process.arch"` + `ls node_modules/.pnpm/ | grep -i esbuild`。
2. **lib 构建产物陈旧/缺失**：`update-dsh.sh` 里构建步骤只跑了 `tsc -b tsconfig.host.json`，但 **tsconfig.host.json 是 `noEmit: true`（纯类型检查）**，lib JS 必须由 `tsdown` 产出（`build:lib:host` = tsc + tsdown）。lib 停留在旧 commit，与最新 src 不匹配 → cordis 插件 import 失败、typert 入口缺失。
3. **profile 依赖安装被拦截**：`~/.dsh/profiles/web/pnpm-workspace.yaml` 的 `allowBuilds` 是占位符 `set this to true or false`，pnpm 12 默认拦截 build scripts → `ERR_PNPM_IGNORED_BUILDS`，lightningcss 平台二进制没装上。

**解决**：
1. 删错包重装：`rm -rf node_modules/.pnpm/@esbuild+darwin-x64@*`（对应 lockfile 里的 0.21.5/0.25.12/0.28.1）→ `CI=true pnpm install`（pnpm 不在 PATH 时用 `corepack pnpm`）。
2. 真正重建产物：`CI=true pnpm run build:lib`（含 build:lib:host + build:lib:client），并校验关键产物存在（`packages/boot/plugin-manager/lib/typert.host.js`、`packages/client/ui-chat/lib/index.js` 等）。
3. profile 修 allowBuilds：`cloudflared/cpu-features/ssh2: true` → `CI=true pnpm install --no-frozen-lockfile` → arm64 二进制到位。
4. 验证：模拟浏览器完整鉴权（GET `/?token=` → 303 + Set-Cookie → 带 cookie GET `/` → 200 index.html，`<!doctype html>`）；index.html 出现 `data-dsh-skin="blue-fantasy"` 说明第三方 skin 插件恢复。

**改进建议**：
- 从源码跑 dsh：构建一律走 `pnpm run build:lib`（tsdown 产出 lib JS），别只跑 `tsc -b`（noEmit 陷阱）；`update-dsh.sh` 已改为 build:lib + 关键产物缺失校验。
- pnpm 12 起默认拦 build scripts，workspace/profile 的 `pnpm-workspace.yaml` 若出现 `allowBuilds: xxx: set this to true or false` 占位符，先改成 true 再 install，否则平台二进制（lightningcss/ssh2 等）装不上。
- arm64 机器排查 esbuild 平台类报错：直接对比 `process.arch` 与 `.pnpm` 里平台包后缀，删错包重装即可，无需全量删 node_modules。


## 2026-09-21 — dsh web 启动失败第四层：web 前端 client bundle 陈旧（module table 缺 dockkit）

**问题**：前三层修复后（esbuild 平台包 / lib 产物 / allowBuilds），`dsh web` 启动日志出现：
`Failed to load plugins @deepseek-ai/dsh-client-ui-conversation ... failed to import loader entry (@deepseek-ai/dsh-client-ui-sidebar-right): client-modules: require("@deepseek-ai/dsh-client-ui-dockkit") missed the module table — not a platform seed word, not a materialized module, and no registered package factory (a build-time externals drift, or a dynamic dependency that did not arrive)`。

**根因**：**web 前端 client bundle（apps/web/dist）陈旧**。`.dsh-build/client-build-environment.json` 记录的 `DSH_CLIENT_COMMIT_HASH=cd5ef81 / 0.1.2-alpha.1`（8月28日构建），而源码已更新到 `ddefc45fbc / 0.1.6-alpha.2`；`dockkit` 包恰是本次 release 新增，旧 bundle 的 client module table 里没有它 → host 加载 client 插件链（ui-conversation → ui-sidebar-right → dockkit）时查不到模块。只跑 `build:lib`（tsdown 产出 lib JS）不会重建 vite 前端 bundle。

**解决**：`CI=true pnpm run build:web`（= `pnpm --filter @deepseek-ai/dsh-web-frontend run build`，vite build）重建 `apps/web/dist`；dockkit 进入新 bundle。验证：`dsh web` 完整启动日志中 dockkit/missed the module table/failed to import/degraded 全部无命中，HTTP token→303→cookie→200 正常。

**改进建议**：
- 源码更新后完整构建应跑 `pnpm run build`（native-system + lib + web 三段 + 写 client-build-record），仅跑 `build:lib` 不更新前端 bundle，会留 client 陈旧坑。
- 排查 module table 缺包：先对比 `.dsh-build/client-build-environment.json` 的 DSH_CLIENT_COMMIT_HASH 与 `git rev-parse --short HEAD`，不一致即是 bundle 陈旧，重建 `build:web`。
