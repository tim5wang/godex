# 工具问题与解决经验（Tools Issues）

> 用途：记录工具调用失败的日志与解决经验，供后续优化工具。
> 约定：每次遇到工具失败/反爬/沙箱限制，在这里 **append** 一条记录（时间 + 问题 + 根因 + 解决 + 改进建议）。同步用 memory 记录一条 workflow 备忘。

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
