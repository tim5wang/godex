import { useCallback, useEffect, useRef, useState } from "react";
import { Button, Tooltip } from "antd";
import { AudioOutlined, AudioMutedOutlined } from "@ant-design/icons";
import { createPCMPlayer, type PCMPlayer } from "../lib/ttsPlayback";

/**
 * VoiceBar —— 点击式语音输入（M5）。
 *
 * 链路：麦克风 PCM(16k s16) → WS /v1/voice → godex 编排桥 → voice-engine
 *       （VAD+ASR → agent → TTS）→ 下行 PCM(24k) → 浏览器播放。
 *
 * 交互：单击开始录音（脉冲动效 + 分段识别回显），再单击停止并发送。
 * 服务端 VAD 负责切分，每个分段完成即回显 asr_partial 文本（准流式反馈）。
 *
 * 状态：未启用（media.audio.voice_enabled=false）时禁用并提示；
 * 连接失败时通过 /v1/voice/status 诊断区分「未启用 / 引擎不可达 / 鉴权失败」。
 */
interface VoiceBarProps {
  token: string | null;
  sessionId: string | null;
  /** 后端是否启用了语音（meta.voice_enabled）。false 时禁用按钮。 */
  enabled?: boolean;
  disabled?: boolean;
  /** 录音停止时回调识别文本（由调用方填入输入框，用户编辑后发送）。 */
  onResult?: (text: string) => void;
}

interface VoiceMsg {
  type: string;
  code?: string;
  text?: string;
  id?: string;
}

interface VoiceStatus {
  enabled: boolean;
  engine_addr: string;
  reachable: boolean;
}

const TARGET_RATE = 16000;

export function VoiceBar({ token, sessionId, enabled = true, disabled = false, onResult }: VoiceBarProps) {
  const [connected, setConnected] = useState(false);
  const [listening, setListening] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // 录音期间累计的识别文本（每个 asr_partial 分段追加）。
  const [partial, setPartial] = useState<string>("");
  // 同步 ref：stopListening（空依赖 useCallback）需要读到最新累计文本。
  const partialRef = useRef<string>("");
  const onResultRef = useRef(onResult);
  onResultRef.current = onResult;

  const wsRef = useRef<WebSocket | null>(null);
  const streamRef = useRef<MediaStream | null>(null);
  const audioCtxRef = useRef<AudioContext | null>(null);
  const sourceRef = useRef<MediaStreamAudioSourceNode | null>(null);
  const processorRef = useRef<ScriptProcessorNode | null>(null);
  // TTS 下行播放器（首帧即播、后续帧排队续播）。
  const ttsPlayerRef = useRef<PCMPlayer | null>(null);

  const wsUrl = useCallback(() => {
    const base = window.location.origin.replace(/^http/, "ws");
    const params = new URLSearchParams();
    if (token) params.set("token", token);
    if (sessionId) params.set("session_id", sessionId);
    return `${base}/v1/voice?${params.toString()}`;
  }, [token, sessionId]);

  // 连接 /v1/voice
  useEffect(() => {
    if (disabled || !sessionId || !enabled) return;
    let closed = false;
    const ws = new WebSocket(wsUrl());
    wsRef.current = ws;

    ws.onopen = () => {
      if (closed) return;
      setConnected(true);
      setError(null);
      ws.send(JSON.stringify({ type: "start" } satisfies VoiceMsg));
    };
    ws.onmessage = (ev) => {
      if (typeof ev.data !== "string") {
        // 下行 TTS PCM（binary）→ 共享播放器排队播放（首帧即播）。
        void ev.data.arrayBuffer().then((buf: ArrayBuffer) => {
          if (!ttsPlayerRef.current) {
            ttsPlayerRef.current = createPCMPlayer();
          }
          ttsPlayerRef.current?.enqueue(new Uint8Array(buf));
        });
        return;
      }
      let msg: VoiceMsg;
      try {
        msg = JSON.parse(ev.data);
      } catch {
        return;
      }
      if (msg.type === "error") {
        setError(msg.text || msg.code || "voice error");
      } else if (msg.type === "asr_partial" && msg.text) {
        // 分段识别回显：追加到累计文本。
        setPartial((prev) => {
          const next = prev ? `${prev}${msg.text}` : (msg.text ?? "");
          partialRef.current = next;
          return next;
        });
      }
    };
    ws.onclose = () => {
      if (!closed) setConnected(false);
    };
    ws.onerror = () => {
      if (!closed) void diagnose(token, setError);
    };

    return () => {
      closed = true;
      ws.close();
      wsRef.current = null;
      ttsPlayerRef.current?.close();
      ttsPlayerRef.current = null;
      setConnected(false);
    };
  }, [wsUrl, disabled, sessionId, enabled]);

  // 开始录音：采集 16k s16 PCM 上行
  const startListening = useCallback(async () => {
    if (!wsRef.current || wsRef.current.readyState !== WebSocket.OPEN) {
      setError("voice not connected");
      return;
    }
    try {
      const stream = await navigator.mediaDevices.getUserMedia({ audio: true });
      streamRef.current = stream;
      const Ctx = window.AudioContext || (window as unknown as { webkitAudioContext: typeof AudioContext }).webkitAudioContext;
      const ctx = new Ctx();
      audioCtxRef.current = ctx;
      const source = ctx.createMediaStreamSource(stream);
      sourceRef.current = source;
      const processor = ctx.createScriptProcessor(4096, 1, 1);
      processorRef.current = processor;

      processor.onaudioprocess = (e) => {
        const input = e.inputBuffer.getChannelData(0); // float32 @ ctx.sampleRate
        const srcRate = ctx.sampleRate;
        // 线性插值降采样到 16k
        const out: number[] = [];
        const step = srcRate / TARGET_RATE;
        let pos = 0;
        while (pos < input.length) {
          const i0 = Math.floor(pos);
          const i1 = Math.min(i0 + 1, input.length - 1);
          const frac = pos - i0;
          out.push(input[i0] * (1 - frac) + input[i1] * frac);
          pos += step;
        }
        // 转 s16 小端 → 二进制帧
        const pcm = new Int16Array(out.length);
        for (let i = 0; i < out.length; i++) {
          let v = out[i];
          if (v > 1) v = 1;
          if (v < -1) v = -1;
          pcm[i] = v * 0x7fff;
        }
        if (wsRef.current?.readyState === WebSocket.OPEN) {
          wsRef.current.send(pcm.buffer as ArrayBuffer);
        }
      };
      source.connect(processor);
      processor.connect(ctx.destination); // 保持处理器活跃（静音输出）
      setListening(true);
      setError(null);
    } catch (err) {
      setError(`mic denied: ${String(err)}`);
    }
  }, []);

  // 结束录音：停止采集并通知服务端 flush VAD（发送本次语音）。
  const stopListening = useCallback(() => {
    try {
      processorRef.current?.disconnect();
      sourceRef.current?.disconnect();
      streamRef.current?.getTracks().forEach((t) => t.stop());
    } catch {
      /* noop */
    }
    void audioCtxRef.current?.close();
    processorRef.current = null;
    sourceRef.current = null;
    streamRef.current = null;
    audioCtxRef.current = null;
    setListening(false);
    // 通知服务端一句话说完（flush VAD）
    if (wsRef.current?.readyState === WebSocket.OPEN) {
      wsRef.current.send(JSON.stringify({ type: "audio_end" } satisfies VoiceMsg));
    }
    // 把累计识别文本交给调用方（填入输入框，用户编辑后发送）。
    const text = partialRef.current.trim();
    partialRef.current = "";
    setPartial("");
    if (text) {
      onResultRef.current?.(text);
    }
  }, []);

  useEffect(() => () => stopListening(), [stopListening]);

  // 点击 toggle：录音中 → 停止并发送；否则 → 开始录音（清空上次回显）。
  const toggle = useCallback(() => {
    if (listening) {
      stopListening();
    } else {
      setPartial("");
      void startListening();
    }
  }, [listening, startListening, stopListening]);

  const notEnabled = !enabled;
  const tip = notEnabled
    ? "语音未启用（设置 → Media / Audio → Voice Chat Enabled）"
    : error ?? (listening ? "录音中…点击停止，识别文本将填入输入框" : connected ? "点击开始说话" : "语音未连接");

  return (
    <>
      {partial && (
        <Tooltip title="已识别内容（分段实时回显）">
          <span className="voice-partial">🎙 {partial}</span>
        </Tooltip>
      )}
      <Tooltip title={tip}>
        <Button
          size="small"
          shape="circle"
          type={listening ? "primary" : "default"}
          danger={listening}
          className={listening ? "voice-btn-recording" : undefined}
          icon={listening ? <AudioOutlined /> : <AudioMutedOutlined />}
          disabled={disabled || !connected || notEnabled}
          onClick={toggle}
          aria-label={listening ? "停止录音并发送" : "开始语音输入"}
        />
      </Tooltip>
    </>
  );
}

/** 连接失败时调用 /v1/voice/status 诊断，给出明确错误文案。
 *  status 端点与 /v1/voice 一样受 web-token 保护，需带 Bearer header。 */
async function diagnose(token: string | null, setError: (msg: string) => void) {
  try {
    const resp = await fetch("/v1/voice/status", {
      headers: { Accept: "application/json", ...(token ? { Authorization: `Bearer ${token}` } : {}) },
    });
    if (resp.status === 401) {
      setError("语音鉴权失败：web token 无效，请在设置中更新");
      return;
    }
    if (resp.status === 404) {
      setError("语音未启用（设置 → Media / Audio → Voice Chat Enabled）");
      return;
    }
    if (!resp.ok) {
      setError(`语音服务错误 (HTTP ${resp.status})`);
      return;
    }
    const st = (await resp.json()) as VoiceStatus;
    if (!st.enabled) {
      setError("语音未启用（设置 → Media / Audio → Voice Chat Enabled）");
      return;
    }
    if (!st.reachable) {
      setError(`语音引擎不可达（${st.engine_addr}），请启动 voice-engine`);
      return;
    }
    setError("语音连接失败，请刷新页面重试");
  } catch {
    setError("语音连接失败（无法访问诊断端点）");
  }
}
