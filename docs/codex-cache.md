# Codex 缓存命中率优化：官方 Responses API 端点

> 中文为主，English summary at the end.

## 问题背景

godex 的 `openai_codex` provider 默认指向 `https://chatgpt.com/backend-api/codex`（ChatGPT 订阅 OAuth 端点，device-code + token exchange）。实测该端点**服务器端缓存封顶**：即使前缀字节完全稳定、相邻请求前 249 项一致，`cache_read_tokens` 恒定在 ~2560（仅系统提示/固定头部），input 从 54K 涨到 67K 也不增长。缓存命中率 ~4%，成本高、体验差。

这不是 godex 客户端缺陷（已逐字段对照 pi 参考实现），而是该 OAuth 端点的服务端行为，客户端无解。

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
   - **转发 `prompt_cache_retention: "24h"`**（官方 API 支持 gpt-5.x-codex 系列，延长前缀缓存 TTL；OAuth 端点会 400 拒绝，仍不转发）。
   - **不转发 `prompt_cache_key`**：官方端点上 `prompt_cache_key` 走确定性缓存，语义对"整段 prompt 全量匹配、变化即整体失效"的 agent 增长前缀不安全；自动前缀缓存做最长前缀匹配，是官方文档化的产品路径。若实测想对比确定性缓存，改一行即可恢复转发。
   - `store:false` 保持不变（官方端点允许 store，但无存储需求；OAuth 端点强制 false）。
3. **OAuth 端点行为完全不变**（保持现状）。

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
- forward `prompt_cache_retention: "24h"` on official (supported for gpt-5.x-codex);
- do NOT forward `prompt_cache_key` on official — deterministic caching is all-or-nothing on the whole prompt and unsafe for a growing prefix; automatic prefix caching is the documented path;
- OAuth endpoint behavior unchanged.

Verify via the new `model_request_completed` timeline event's `cache_read_tokens`.
