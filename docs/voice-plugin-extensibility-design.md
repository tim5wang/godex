# GoDex 第三方 ASR/TTS 插件化扩展设计

> 状态：方案设计（未实施） · 日期：2026-08-26
> 目标：回答「godex 能否以 plugin 方式持有第三方 ASR/TTS 接口」并给出落地路径

## 1. 结论（TL;DR）

**可行，但正确形态不是「WASM 插件内嵌推理引擎」，而是「插件化语音后端适配器」。**

- 现状语音链路（Web UI VoiceBar → godex 桥 → voice-engine 独立服务）已经是最优架构，**保留不动**；
- 第三方 ASR/TTS（云端 API 或另一套本地引擎）以 **pluginrt 原生 Go 插件** 形式注册为 `voice:asr@1` / `voice:tts@1` 能力，voiceBridge 从能力注册表解析后端；
- WASM 插件受沙箱限制（无 POST/WS/流式音频、内存 32MiB、超时 30s），**不适合承载 ASR/TTS 推理或流式协议**；作为「协议适配器」需要先扩展网络 host calls（L3，可选远期）。

## 2. 现状盘点（事实）

| 层 | 现状 |
|---|---|
| 语音链路 | Web UI `VoiceBar` → `/v1/voice` WS → `voiceBridge`（httpapi）→ voice-engine（WS 流式 protocol）→ Silero VAD + 多后端 ASR + Kokoro TTS |
| 协议 | voice-engine `protocol` 包：hello/start/audio_end/tts/stop + ready/asr_partial/asr_final/asr_end/tts_start/tts_done/error；`client` 公开包 Dial/StartASR/SendAudio/AudioEnd/Synthesize/SynthesizeWAV |
| 多后端 | voice-engine `asrbackends` 注册表已支持 sensevoice/zipformer/vosk，`start.asr_model` 会话级切换 |
| godex 插件 | `pluginrt`：Manifest(requires/provides) + 能力注册表(scope 级) + NativePlugin/ToolContributor；`wasmrt`：wazero，ABI `godex:plugin@0.1`，host calls = Log/KVGet/KVSet/WorkspaceRead/**HTTPGet**/CredentialGet |
| WASM 沙箱限制 | 仅 `godex_http_get`（受控 GET）；无 POST/WebSocket/原始 socket；MaxMemoryPages 512（32MiB）；单次调用超时 30s |
| 动态加载 | pluginrt **无** `plugin.Open`（原生插件编译进 godex） |

## 3. 可行性分析

### 3.1 WASM 插件内嵌 ASR/TTS 推理 → 不可行
- ASR/TTS 需要：实时音频流双向通道（麦克风上行 16k PCM / 下行 PCM）、大模型内存（SenseVoice 228MB / Kokoro ~200MB，远超 32MiB）、长时推理（>30s 超时）。
- 沙箱只给受控 HTTP GET，没有 POST 也没有 WS；模型要么编译进 WASM（wazero 解释执行性能不可接受），要么靠 host callback 外调——那还不如直接做原生适配器。

### 3.2 WASM 插件做「协议适配器」→ 需先扩展网络 host（L3，可选）
- 若未来想用 WASM 写第三方 API 适配器（把统一语音协议翻译成 OpenAI/Azure 等 HTTP API），需给 wasmrt 增加 `godex_http_post` / `godex_ws_connect` 受控 host calls（走现有 policy 引擎的 allow/deny 域与超时）。
- 收益：插件可独立分发、热加载；成本：网络 host 面扩大 = 信任边界扩大，需与现有「WASM 只获得 manifest 明确授权」的安全模型配套。

### 3.3 pluginrt 原生 Go 插件 → 推荐（L2）
- pluginrt 的 `NativePlugin` / `ToolContributor` 机制天然支持「Go 组件贡献能力 + 可逆注册 + scope 级能力解析」。
- 第三方后端以原生插件形式贡献 `voice:asr@1` / `voice:tts@1` 能力，voiceBridge 变为能力消费者——**与现有 voice-engine 桥接可以并存**（voice-engine 本身也可包成一个内置后端插件）。
- 原生插件 = 编译进 godex（或未来支持 .so 动态加载），无沙箱限制，可自由持有 HTTP/WS client、密钥等。

### 3.4 纯配置式多后端（L1，最简，可不引入插件）
- 若第三方后端都是「HTTP/WS 服务」（如 OpenAI Realtime、Azure Speech、自建 whisper 服务），最简方案是在 `media.audio` 配置里加 `backend` 枚举 + 各后端地址/密钥，voiceBridge 按配置选择适配器。
- 优点：零插件基建；缺点：新增后端要改 godex 代码，不满足「第三方可独立扩展」。

## 4. 推荐方案 L2：语音后端插件（pluginrt 扩展）

### 4.1 能力契约（新增命名空间）
```go
// pluginrt 能力注册表新增 voice 命名空间（沿用 parseCapability 语法 namespace:name@major）
Provides: []string{"voice:asr@1", "voice:tts@1"}   // 插件声明提供
Requires: []string{"godex:config@1"}              // 插件需要读配置（地址/密钥）
```

### 4.2 SpeechBackend 接口（新增包 internal/speech）
```go
package speech

// Backend 是语音后端统一接口。第三方插件实现该接口并注册到 pluginrt。
type Backend interface {
    // ASR：流式音频输入 → 识别文本事件（复用 voice-engine 事件语义）
    ASR() ASR
    // TTS：文本 → PCM 流（复用 voice-engine 下行语义）
    TTS() TTS
}

type ASR interface {
    Start(ctx context.Context, sampleRate int) (ASRSession, error) // 会话级：VAD 由后端自理或复用服务端 VAD
    // 或离线式：Transcribe(ctx, audio []float32, sr int) (string, error)
}

type ASRSession interface {
    Accept(ctx context.Context, pcm []byte) error
    End(ctx context.Context) (string, error) // flush 后返回最终文本（含 asr_end 语义）
    Close() error
}

type TTS interface {
    SynthesizeStream(ctx context.Context, text string) (<-chan []byte, error) // 每块 = 一段 PCM（24k s16）
}
```

### 4.3 voiceBridge 改造（消费能力，不硬编码）
```
voiceBridge.resolveBackend() :
  1. pluginrt.Registry.Providers(scope, "voice:asr@1") / "voice:tts@1"
  2. 命中 → 用插件 Backend（适配器模式：内部可转发到第三方 HTTP/WS 服务）
  3. 未命中 → 回退现有 voice-engine 桥接（内置后端，保持默认行为）
```
- Web UI / protocol 不变：`asr_partial / asr_final / asr_end / tts_start / tts_done` 事件语义与现在完全一致，插件后端只是换了「音频/文本的实际生产者」。
- 密钥：走现有 `CredentialBroker`（阶段 C 已就绪），插件 manifest 授权后 `godex_credential_get` 取第三方 API key。

### 4.4 插件装配示例
```go
// 内置：voice-engine 桥后端（现有逻辑包一层，默认启用）
pluginrt.Register(&NativeBackendPlugin{
    ManifestValue: Manifest{ID: "builtin-voice-engine", Scope: scope.Global,
        Provides: []string{"voice:asr@1", "voice:tts@1"}},
    Backend: voiceenginebackend.New(addr), // 内部 = voiceclient
})

// 第三方示例：OpenAI Realtime 适配器（未来）
pluginrt.Register(&NativeBackendPlugin{
    ManifestValue: Manifest{ID: "openai-realtime", Scope: scope.Global,
        Provides: []string{"voice:asr@1", "voice:tts@1"},
        Requires: []string{"godex:credential@1"}},
    Backend: openaibackend.New(cfg), // 内部 = HTTP/WS 转发
})
```

## 5. 路线图与工作量（估算）

| 阶段 | 内容 | 工作量 |
|---|---|---|
| L1 | 配置式多后端（枚举 + 适配器分发，不动插件系统） | 0.5–1 人日 |
| L2a | `internal/speech` 接口 + pluginrt 能力命名空间 + NativeBackendPlugin | 1–2 人日 |
| L2b | voice-engine 桥包成内置后端（默认回退保持兼容） | 0.5 人日 |
| L2c | 一个真实第三方后端示例（如 OpenAI Realtime 适配器） | 1–2 人日 |
| L3 | wasmrt 扩展 `godex_http_post` / `godex_ws_connect`（受控）+ WASM 适配器 demo | 2–3 人日（可选） |

## 6. 验收标准（可验证）

1. 设置页「Voice Engine Addr」旁可见 voice-engine 仓库链接（本次已随设置页改动提交）。
2. 无插件时：语音链路行为与现在完全一致（默认回退 voice-engine 桥）。
3. 注册第三方后端插件后：`voice:asr@1` / `voice:tts@1` 能力在 registry 可查，voiceBridge 切到该后端，VoiceBar 交互不变、识别文本/播放音频正常。
4. 插件停用后：效果可逆，能力注册撤销，回退默认后端。
5. 第三方密钥经 CredentialBroker 授权读取，不进配置明文（除用户显式输入）。
