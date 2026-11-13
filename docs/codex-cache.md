# Codex 缓存命中率优化：官方 Responses API 端点

> 状态：已更新 Responses 请求实现（2026-11-13）；下方早期端点样本仅作历史记录，不能推导通用的服务端缓存上限。
> 中文为主，English summary at the end.

## 当前实现与排查方法（2026-11-13）

- GPT-5.6+ 的 `openai_responses` / 官方 `openai_codex` 请求使用 `prompt_cache_options: {mode: explicit, ttl: 30m}`，在历史 user 文本和工具结果的 `input_text` 末尾保留显式断点。当前 runtime 尾部不设断点；旧断点不会随着历史增长被移动或删除。不将旧 `24h` 自动升级为写入价格更高的 `12h`。
- GPT/o/Codex 的原生 Responses output（包括加密 reasoning、assistant phase、原始工具参数）随 assistant 元数据保存，下一轮在原位置回放。已经发送的 runtime 尾部也在原位置回放；内容相同的尾部不重复追加。聊天界面不增加运行时 user 消息。仅同一 endpoint/credential/model、且 assistant 内容未编辑时复用；压缩保留的消息沿用元数据，不需要存量迁移。
- DeepSeek/未知兼容模型维持旧的消息格式，不发送新缓存字段或 OpenAI 原生回放项。旧 GPT 保持既有 retention；OAuth 不发送新缓存控制。兼容网关在流开始前明确以 HTTP 400 拒绝新缓存字段时，去掉断点和 options 重试一次，并在该 client/model 生命周期内记住降级；其他错误以及已开始的流不因此重试。
- usage 分成互不重叠的普通输入、cache read、cache write；输入总量为三者之和。命中率为 `read / (ordinary + read + write)`。`reasoning_tokens` 是 output 的子集，不再加一次；在 capture 和 model-request timeline payload 中提供该明细，不改变网关现有 credit 单价策略。
- 开启 LLM capture 后，记录 `request_diagnostics`：最终请求（含降级结果）的规范化指纹、system/tools/settings hash、逐 input item hash、runtime 尾部字节数、原生回放项数、cache mode/TTL/fallback，以及与上次同 session/job/model/origin 请求的共同前缀项数和首个差异下标（从 0 开始，包含追加/删除）。新增诊断不记录凭证、URL 或提示词原文；capture 本身仍是含正文的显式 opt-in。关闭 capture 不计算这些指纹。

验证：重新构建并重启实际运行的 Godex；新跑一个有多轮工具调用的 GPT 任务，观察后续请求的 `cached_tokens` 是否随历史增长。通过 capture 检查旧断点和 wire prefix 是否延续、`cache_fallback` 是否为 true、system/tools/settings 是否变化。旧会话已有的输出无法补出丢失的加密 reasoning；优化从新请求开始生效。不要用旧累计值判断，也不保证固定命中率：压缩、模型切换、工具集或前缀变化、网关行为都可能造成合理 miss。

参考：[OpenAI prompt caching](https://developers.openai.com/api/docs/guides/prompt-caching)、[Responses create](https://developers.openai.com/api/reference/resources/responses/methods/create)、[Reasoning](https://developers.openai.com/api/docs/guides/reasoning)。

## 问题背景

godex 的 `openai_codex` provider 默认指向 `https://chatgpt.com/backend-api/codex`（ChatGPT 订阅 OAuth 端点，device-code + token exchange）。早期样本曾出现类似“缓存封顶”的现象：相邻请求前 249 项一致，`cache_read_tokens` 恒定在 ~2560，input 从 54K 涨到 67K 也不增长，命中率约 4%。这只是当时样本，不是端点的协议保证。

早期曾据此判断为 OAuth 服务端限制。这个判断不能推广到其他模型/网关：新样本中同一 GPT 主 Agent 命中很低、子 Agent 却超过 95%，说明必须同时检查客户端的历史稳定性、缓存写入边界和 reasoning 回放，不能直接归因于端点。

## 解决方案：切换到官方 Responses API

godex 的 codex 客户端本来就讲 Responses API 协议（openai-go SDK 的 `Responses.NewStreaming`）。把 `base_url` 指向官方平台端点 + 使用真实 API key，即启用官方**自动前缀缓存**：

| 维度 | chatgpt.com OAuth 端点 | api.openai.com/v1（官方） |
|---|---|---|
| 凭证 | 订阅 OAuth token | 真实 API key（Tier 1+ 即可用 codex 模型） |
| 缓存 | 固定头部封顶，前缀不复用 | 自动前缀缓存：≥1024 token 前缀、最长字节前缀匹配、TTL 5–10 分钟（GPT-5.6+ 30m） |
| 命中计费 | — | 0.1× input 单价（90% 折扣），如 gpt-5.1-codex $1.25/M → $0.125/M |
| 实测对比 | cached:input ≈ 1:1 | ≈ 20:1（≈95%） |

社区实测同一请求在 OAuth 端点 ~1:1、官方 API ~20:1（[openai/codex #5556](https://github.com/openai/codex/issues/5556)）；同前缀 append-only agent 循环预期 80–95%+ 命中。

## 代码改动（已实现，commit 见下）

`internal/core/conversation/openai_codex_client.go`：

1. **端点识别**：`isOfficialResponsesBaseURL()` 检测 `base_url` 是否含 `api.openai.com`；`NewOpenAICodexClientForEndpoint(..., official bool)` 为测试注入点。
2. **官方端点行为**：
   - 去掉 codex 专属头：`originator`、`OpenAI-Beta: responses=experimental`、`chatgpt-account-id`（仅 OAuth JWT 会解析出来）。
   - 旧 GPT 沿用 `prompt_cache_retention: "24h"`；GPT-5.6+ 改用上节的显式断点和 30m TTL；OAuth 仍不转发 retention/options。
   - 官方路径目前不转发 `prompt_cache_key`。注意：它不是“整段 prompt 全量匹配”的开关；旧模型上它帮助缓存路由，新模型上主要用于分组记账，不能替代正确的缓存边界。
   - `store:false` 保持不变（官方端点允许 store，但无存储需求；OAuth 端点强制 false）。
3. OAuth 的缓存参数和 headers 保持原有策略；原生输出回放也适用于它。

新增事件 `model_request_completed`（见下节）帮助实测命中率。

## 配置示例（godex.yaml）

```yaml
api:
  providers:
    codex:
      name: OpenAI Codex (Platform API)
      type: openai_codex
      base_url: https://api.openai.com/v1    # SDK 自动拼 /responses
      api_key_env: OPENAI_API_KEY            # 真实 API key（sk-...）
      timeout_seconds: 600
      models:
        gpt-5.1-codex:
          name: GPT-5.1 Codex
          model: gpt-5.1-codex
          max_tokens: 4096
        gpt-5.1-codex-mini:
          name: GPT-5.1 Codex Mini
          model: gpt-5.1-codex-mini
          max_tokens: 4096
```

> 注意：`godex login codex --mode platform-api-key` 目前实际配置的是 Chat Completions provider（`openai_compatible`），不是 Responses。要走官方 Responses API 需手改上述配置，或后续在 CLI 里增加专用模式。

## 验证步骤

1. 配置 provider + `OPENAI_API_KEY`，重启 godex。
2. 跑一轮 agent 任务（多轮工具调用）。
3. 在 Timeline 面板打开「Model request」详情，看 `cache_read_tokens` 占比；或看日志里 `usage.input_tokens_details.cached_tokens`。
4. 预期：从第 2 个请求起 `cache_read_tokens` 随前缀增长而增长，命中率 80%+。

## 风险与注意事项

- 命中仍是 best-effort，偶发 0（[openai/codex #30425](https://github.com/openai/codex/issues/30425)）。
- **前缀字节稳定性是命脉**：system + tools + 历史严格 append-only，动态内容（时间戳/随机 id/session 串）绝不进前缀；godex 的压缩保留尾 + 确定性 repo map 快照正是为此设计。
- 社区报告 gpt-5.4/5.5 长尾部 user 内容 >500 tokens 时字节前缀匹配可能不生效（[社区帖](https://community.openai.com/t/prompt-cache-documented-byte-prefix-matching-does-not-occur-on-gpt-5-4-gpt-5-5-when-trailing-user-content-exceeds-500-tokens/1384129/5)），codex 模型是否受影响未确认，实测注意观察。
- 按量计费下 output 才是大头（gpt-5.1-codex output $10/M）；命中率解决的是 input 成本（90% 折扣）。订阅转按量前先按会话长度估算。

## 相关

- 事件 `model_request_completed`：每次模型请求完成后发射，payload 含 `model / input_tokens / output_tokens / cache_read_tokens / cache_write_tokens / started_at / first_token_at / completed_at / duration_ms / ttft_ms / stop_reason / error`，用于时间线详情与命中率观测（见 `docs/session-timeline-inspector.md`）。
- 参考实现：`temp/pi/packages/ai/src/providers/openai-codex-responses.ts`（pi 的 codex 客户端镜像）。

---

## OpenAI-compatible 供应商的"假低命中"：流式响应不带 usage（已修复）

> 另一类"缓存命中率低"其实是**观测盲区**，不是服务端缓存差。以 Seed-Coding（火山方舟 `ark.cn-beijing.volces.com/api/coding/v3`）为例：

- godex 对 `openai_compatible` 供应商**始终走流式**（`runner.callModel` → `Stream`）。
- 该类端点**流式 chunk 一律返回 `usage: null`**（实测确认），只有非流式响应才带 usage。
- godex 的 `parseOpenAIStream` 只从流 chunk 里抓 usage → `resp.Usage` 恒为 `nil` → `model_request_completed` 时间线事件、session 缓存统计、状态栏全部没有 input/output/cache_read → 界面上命中率显示 0%/未知，看起来像"命中率低"。
- 但服务端 KV 前缀缓存本身是好的：实测同一大请求第 2 次 `cached_tokens` = 12160/12208（99.6%）；模拟 agent 增长前缀（A→A+4 条消息）仍 99.4%；尾部变化不影响前缀命中；`prompt_cache_key`/`prompt_cache_retention` 参数不影响前缀缓存。

**修复**（`internal/core/conversation/openai_client.go`）：OpenAI-compatible 流式请求体加 `stream_options: {"include_usage": true}`（OpenAI 标准字段），供应商就会在流尾部下发带 `cached_tokens` 的 usage chunk；godex 现有 `parseOpenAIStream` 已能解析 usage-only chunk，无需改解析器。对拒绝该字段的供应商（HTTP 400），`Stream` 会自动去掉 `stream_options` 重试一次，行为回退到旧版（仅失去观测，不报错）。

> 注意：这与上文 chatgpt.com OAuth 端点的"服务端真封顶（~4%）"是**两个不同问题**。前者是观测盲区（本修复解决），后者是服务端行为（需切换官方 Responses API，见上文）。

---

## English Summary

The `openai_codex` provider pointed at `chatgpt.com/backend-api/codex` (ChatGPT subscription OAuth) is **server-side cache-capped**: `cache_read_tokens` stays ~2560 regardless of a byte-stable growing prefix, giving ~4% hit rate. This is backend behavior, not a client bug.

**Fix**: point `base_url` at `https://api.openai.com/v1` with a real API key. godex already speaks the Responses protocol; the official endpoint provides automatic prefix caching (≥1024-token byte-exact longest-prefix match, cached input billed at 0.1×), with community-measured ~95% hit for append-only agent loops.

Client changes (this commit):
- detect official endpoint from base URL; drop codex-only headers (`originator`, `OpenAI-Beta`, `chatgpt-account-id`);
- retain legacy retention for older models; use explicit historical breakpoints and a 30m TTL for GPT-5.6+ on official/generic Responses endpoints;
- a cache key is not a whole-prompt equality requirement; it cannot replace correct cache boundaries;
- preserve native output/reasoning and originating runtime inputs for GPT/o/Codex; OAuth cache parameters remain unchanged.

Verify via the new `model_request_completed` timeline event's `cache_read_tokens`.
