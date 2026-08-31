# OpenAI Responses 协议改造方案（usage proxy / LLM provider / 远程模式）

> 日期：2026-08-23 ｜ 状态：Historical（方案已实施；当前契约见 `routes_responses.go`、Responses clients 与 v1.4 release notes）

## 0. 现状盘点（关键事实）

### 协议层 `internal/core/conversation/`
- **中立协议** `protocol.Request / Response`（Anthropic 风格）：`System / Messages / Tools / MaxTokens / ReasoningEffort / PromptCacheKey/Retention`，Response 含 `Content []Block`（text/thinking/tool_use/tool_result）+ `StopReason + ReasoningContent + Usage`。
- **两个接口**：`Caller.Call()` 与 `StreamCaller.Stream()`（`StreamHandler` 回调：OnTextDelta / OnToolUse / OnThinkingDelta / OnMessageStart / OnStreamStarted）。
- **三个客户端实现**：
  | 客户端 | 协议 | 实现方式 |
  |---|---|---|
  | `Client` (client.go) | Anthropic Messages | 手写 SSE 解析 |
  | `OpenAIClient` (openai_client.go) | OpenAI Chat Completions | 手写 `/v1/chat/completions` + SSE |
  | `OpenAICodexClient` (openai_codex_client.go) | **OpenAI Responses** | **openai-go SDK v3 `Responses.NewStreaming`** ✅ |
- `NewCallerForProfile`（factory.go）：按 `profile.Provider` 分派三类。

### Usage Gateway `internal/services/usage/ + routes_usage.go`
- 端点：`POST /v1/chat/completions`（OpenAI 兼容）+ `POST /v1/messages`（Anthropic 兼容）；`Bearer gdx_` 代理 key 认证。
- `usageGatewayProtocolRequest()` 把 OpenAI 请求转 `protocol.Request`；`streamUsageGatewayChatCompletions()` 把 provider 流转回 `chat.completion.chunk` SSE。
- 设计文档明确 Non-Goal：**No `/v1/responses`**。

### LLM Provider 模块
- `llm.ProviderConfig.Type`（`anthropic_compatible` / `openai_compatible` / `openai_codex`）+ `NormalizeProviderType()`。
- `config.ModelProfileConfig`（Provider/Model/BaseURL/APIKey/MaxTokens/RequestGzip）→ 设置页 LLMProvidersEditor 的 type 下拉。
- `openai_codex` 已走 Responses（官方端点自动前缀缓存，见 docs/codex-cache.md）。

### 远程模式 `internal/services/relay/`
- node 侧 relay agent WebSocket 保活，`FrameRequest` 把任意 path（含 `/v1/chat/completions`）转发到 node httpapi → **新端点天然透传，无需 relay 改造**。

## 1. 核心洞察

**OpenAICodexClient 已经完整实现了 Responses 协议**：`codexResponsesParams / codexInputFromProtocol / codexToolsFromProtocol / applyCodexResponsesSDKEvent / codexStreamStateToProtocol / codexUsageToProtocol` 全部就绪（openai-go v3 `responses.ResponseNewParams` + `NewStreaming` + 事件联合体解析）。

→ 改造不是"从零写 Responses 客户端"，而是**把 codex 客户端的 Responses 能力泛化成一个通用 `OpenAIResponsesClient`**，再接入三条消费链（provider 分派 / usage gateway / 远程透传）。

## 2. 目标架构

```
                        protocol.Request / Response（中立，不变）
                                    │
        ┌───────────────────────────┼────────────────────────────┐
        │                           │                            │
   anthropic_client          openai_client               openai_responses_client ★新
   (Chat/Messages)      (Chat Completions)          (openai-go SDK, 从 codex 泛化)
                                                              │
                                   ┌──────────────────────────┼──────────────┐
                                   │                          │              │
                          provider 分派                  usage gateway   远程 relay
                    (factory + 设置页 type)          POST /v1/responses   FrameRequest
                                                                   透传（无需改）
```

## 3. 分阶段改造方案

### Phase A — 泛化 Responses 客户端（核心，低风险）
**目标**：`openai_responses` provider 类型能像 `openai_codex` 一样工作，但面向任意 base_url。

1. **新增 `internal/core/conversation/openai_responses_client.go`**
   - 从 `openai_codex_client.go` 抽取并泛化：`ResponsesClient`（`official` 语义反转：默认不带 codex 专属头；保留 `OpenAI-Beta` 头可配）。
   - 复用现有转换函数（改名去 codex 前缀，或包内别名复用）：
     - `codexInputFromProtocol` → `responsesInputFromProtocol`（input item 列表）
     - `codexToolsFromProtocol` → `responsesToolsFromProtocol`
     - `applyCodexResponsesSDKEvent` → `applyResponsesSDKEvent`（事件状态机：`response.output_text.delta` / `reasoning_* .delta` / `function_call_arguments.*` / `output_item.added/done` / `completed`）
     - `codexStreamStateToProtocol` → `responsesStreamStateToProtocol`
     - `codexUsageToProtocol` → `responsesUsageToProtocol`（已正确处理 cached tokens 扣除）
   - 实现 `Caller + StreamCaller`（Call 内部走 Stream，与 codex 一致）。
   - **新增能力**（Responses 相比 Chat Completions 的增值点）：
     - `previous_response_id` 服务端状态透传（`protocol.Request` 加 `PreviousResponseID` 字段）——agent 场景价值有限，先留扩展位，不接入。
     - 结构化输出：`response.output_text` / `function_call` 分离——状态机已天然支持。
     - 并行/嵌套工具调用：`function_call_arguments.delta` 按 `output_index` 累积，状态机已支持多 tool call。

2. **配置接入**
   - `internal/core/llm/types.go`：`ProviderResponses = "openai_responses"`；`NormalizeProviderType()` 收编。
   - `internal/core/config/config.go` `normalizeProfileProvider()`：新增分支。
   - `factory.go` `NewCallerForProfile()`：`case ProviderResponses: return NewResponsesClient(...)`。
   - 设置页 `SettingsPage.tsx` LLMProvidersEditor 的 type 下拉加 `openai_responses` 选项（i18n 补 key）。
   - 默认 base_url 建议 `https://api.openai.com/v1`（SDK 自动拼 `/responses`）。

3. **测试**
   - 转换函数单测（protocol→Responses params、Responses 事件→protocol，含多 tool call 累积、reasoning delta、usage cached 扣除）——从 codex 测试复制改造。
   - `go test ./internal/core/conversation/ ./internal/agent/`。

### Phase B — Usage Gateway 增加 `/v1/responses`（✅ 已完成）

落地：`internal/runtime/httpapi/routes_responses.go` + `httpapi.go` 路由注册。
- 入方向 `responsesRequestToProtocol`：instructions→System、system/developer→System、function_call→tool_use、function_call_output→tool_result、未知 item 跳过；透传 max_output_tokens/tools（扁平结构）/prompt_cache_retention/reasoning.effort
- 出方向非流式 `protocolToResponsesResponse`（{id,object,status,model,output[],usage}，input_tokens 加回 cached）+ 流式 `streamUsageGatewayResponses`（response.output_text.delta / reasoning_summary_text.delta / output_item.added / function_call_arguments.delta / response.completed + [DONE]）
- 双路径：`Bearer gdx_` → gateway；web-token → `handleResponsesWebToken`（虚拟 admin key 跳过预算，与 Anthropic 网关一致）
- 17 个新测试全绿（10 转换 + 3 出方向 + 5 HTTP），httpapi 全量回归无新增失败

**背景**：Responses 协议客户端（如新版 openai SDK、Cursor 类工具）直接 POST `/v1/responses`。网关要把它翻译成内部 `protocol.Request` 再转发，等价于现有 `/v1/chat/completions` 路径。

1. **新增 `internal/runtime/httpapi/routes_responses.go`**
   - `handleUsageGatewayResponses()`：认证（`Bearer gdx_`）→ 解析 `ResponseNewParams`（model/input/instructions/tools/stream/previous_response_id/max_output_tokens/reasoning/prompt_cache_retention）→ 转 `protocol.Request` → 预算检查 → `NewCallerForProfile` → 非流式 `Call` 或流式 `Stream`。
   - **入方向转换** `responsesRequestToProtocol()`：Responses `input` 是 item 数组（`{"type":"message",...}` / `{"type":"function_call",...}` / `{"type":"function_call_output",...}`）→ `protocol.APIMessage`（复用现有 tool_call/tool_result 映射逻辑思路）；`instructions` → `System`。
   - **出方向转换** `protocolToResponsesResponse()` / 流式 `emitResponsesEvent()`：
     - 非流式：`{id, object:"response", status, output:[{type:"message",...}], usage, ...}`。
     - 流式 SSE：`response.output_text.delta` / `response.function_call_arguments.delta` / `response.output_item.done` / `response.completed`（直接复用 Phase A 的状态机事件名，出方向是同一套 openai-go SDK 结构体的 JSON）。
     - usage 透传：`protocol.Usage` → `ResponseUsage`（cached tokens 加回，与现有 prompt_tokens 还原逻辑一致）。

2. **路由注册**（httpapi.go）
   - `mux.Handle("POST /v1/responses", gunzipBody(...))`：`gdx_` → usage gateway；否则 web-token → `handleOpenAIResponses`（管理面直连，与现有 chat/completions 双路径一致）。
   - `handleOpenAIResponses`（routes_openai.go 类比）：走 backend service 的 session 路径（把 Responses 请求当一次用户 turn），复用现有 `streamOpenAIChatCompletion` 的事件→SSE 转换骨架。

3. **测试**
   - 入/出转换单测（含嵌套 tool_call_output、多轮 input、reasoning）。
   - HTTP 测试：gdx_ 认证 / 预算超限 / 模型映射 / 流式终止 sentinel（镜像 routes_usage_test.go 既有模式）。

### Phase C — 远程模式（✅ 已完成，零代码改动）
- relay 链路确认**无 path 白名单**：node 侧 `Agent.serveRequest` 任意 path 直接转本地 httpapi（仅注入 relay trust 头）；center 侧 `ProxyHandler` 对 `/control/nodes/{id}/proxy/{path...}` 任意 path 透传（唯一 allowlist 是 TCP `forward_allow`，host:port 非 path）。
- `/v1/responses` 已注册在 node httpapi（Phase B），远程 node 部署新二进制后**天然可用**；中心侧把 `POST /v1/responses` 经 `/control/nodes/{id}/proxy/v1/responses` 代理到 node 即可。
- 远程 godex（godex.claw.carc.top）的 web.token 认证（既有 `relayTrustChecker` 机制）同样覆盖 `/v1/responses`，无需新代码。

### Phase D — 主链 Chat UI 切 Responses（❌ YAGNI 砍掉）

原设想：让 `openai_compatible` provider 可配 `api_protocol: chat|responses`（ProviderConfig 加字段），把默认模型的 `OpenAIClient` 换成 `ResponsesClient`。

**已砍掉**，理由：
- Phase A 已提供入口：想用 Responses 的用户直接在配置里把 type 改成 `openai_responses` 即可，零代码。
- Phase D 唯一真正的增量是「同一 provider 双协议切换」，但 fallback 策略（`NewStrategyCallerForProfiles`）已支持多 profile 混排——配两个 profile（一个 `openai_compatible` 一个 `openai_responses`）即可获得等价语义。
- 省掉的是高风险的 agent 全链路回归（tool loop / compaction / memory 注入 / subagent / loop_guard）。

替代方案（文档即可，无代码）：
```yaml
api:
  providers:
    default:
      type: openai_responses   # 主链走 Responses（官方前缀缓存）
      base_url: https://api.openai.com/v1
      api_key_env: OPENAI_API_KEY
      models:
        gpt-5.2: { model: gpt-5.2 }
```
若需 chat/responses 双路 fallback，再配一个 `openai_compatible` profile 进 `fallback_profile_ids`。

## 4. 工作量与风险

| Phase | 工作量 | 风险 | 关键文件 |
|---|---|---|---|
| A 泛化客户端 | M（代码复用率高） | 低 | openai_responses_client.go, llm/types.go, config.go, factory.go, SettingsPage.tsx |
| B 网关 /v1/responses | L | 中（协议转换细节多） | routes_responses.go, httpapi.go, routes_usage.go 参考 |
| C 远程透传 | S | 近零 | 无代码改动（确认无 path 白名单） |
| D 主链切换 | ~~L~~ | ~~高~~ | ❌ YAGNI 砍掉（配置换 type 即可，无需代码） |

## 5. 建议落地顺序

1. **Phase A** 先落地并全量回归（`go test ./internal/...`）——纯增量，不动现有路径，风险最低、收益立即可测（新增 provider 类型即可用 Responses）。
2. **Phase B** 独立里程碑：/v1/responses 网关是外部客户端的接入面，测试覆盖要足。
3. **Phase C** 随 B 一起发布（已确认 relay 无 path 白名单，零改动）。
4. **Phase D** ~~单独评审后再做~~ **已砍掉**：想用 Responses 改配置 type 为 `openai_responses` 即可；chat/responses 双路用 fallback 多 profile，不做代码。

## 6. 附：Responses 协议相对 Chat Completions 的落地价值

| 优点 | godex 落地点 |
|---|---|
| 服务端状态（previous_response_id） | Phase A 留扩展字段；agent 自管会话暂不依赖 |
| output 结构化（text/reasoning/function_call 分离） | Phase A 状态机天然产出，补强 reasoning 与多工具 |
| 工具调用更好（arguments 增量 JSON） | Phase A 已按 output_index 累积；网关出方向直接透传 |
| 官方前缀缓存 | 已在 openai_codex 官方端点验证（docs/codex-cache.md ~95% hit）；Phase A 的 `openai_responses` 类型默认开启（自动前缀缓存），主链想用直接换 type |
