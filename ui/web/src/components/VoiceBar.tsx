import { useCallback, useEffect, useRef, useState } from "react";
import { Button, Select, Space, Tooltip, Typography } from "antd";
import { AudioOutlined, AudioMutedOutlined } from "@ant-design/icons";
import { createPCMPlayer, type PCMPlayer } from "../lib/ttsPlayback";

const { Text } = Typography;

interface FlowSessionVoiceTarget {
  flowId: string;
  flowSessionId: string;
  afterSequence: number;
  eventType: string;
  outputField?: string;
}

/**
 * VoiceBar supports both chat dictation and a FlowSession Realtime turn.
 *
 * FlowSession mode sends durable ASR turns into the pinned workflow and plays
 * terminal string outputs through the same voice-engine connection.
 */
interface VoiceBarProps {
  token: string | null;
  sessionId: string | null;
  flowSession?: FlowSessionVoiceTarget;
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
  turn_id?: string;
  source?: string;
  sequence?: number;
  source_sequence?: number;
  event_type?: string;
  output_field?: string;
  asr_model?: string;
  tts_model?: string;
  vad?: string;
  models?: string[];
  asr_models?: string[];
  vad_models?: string[];
  tts_models?: string[];
  default_asr?: string;
  default_vad?: string;
  default_tts?: string;
  event?: { type?: string; sequence?: number; source?: string; source_sequence?: number };
}

interface FlowVoiceTurn {
  id: string;
  source: string;
  sourceSequence: number;
}

interface ActiveTTSSpeech {
  id: string;
  turnID?: string;
  source?: string;
  sourceSequence?: number;
}

interface VoiceStatus {
  enabled: boolean;
  engine_addr: string;
  reachable: boolean;
}

const TARGET_RATE = 16000;

function readStoredSequence(key: string, fallback: number, clampToFallback = true) {
  try {
    const value = Number(window.localStorage.getItem(key));
    return Number.isSafeInteger(value) && value >= 0
      ? (clampToFallback ? Math.max(value, fallback) : value)
      : fallback;
  } catch {
    return fallback;
  }
}

function nextFlowVoiceTurn(flowId: string, flowSessionId: string) {
  const prefix = `godex.flow-voice.${flowId}.${flowSessionId}`;
  let source = "";
  let sequence = 1;
  try {
    const sourceKey = `${prefix}.source`;
    source = window.localStorage.getItem(sourceKey) ?? "";
    if (!source) {
      source = `web-${window.crypto?.randomUUID?.() ?? `${Date.now()}-${Math.random().toString(36).slice(2)}`}`;
      window.localStorage.setItem(sourceKey, source);
    }
    const sequenceKey = `${prefix}.source-sequence`;
    sequence = Math.max(0, Number(window.localStorage.getItem(sequenceKey)) || 0) + 1;
    window.localStorage.setItem(sequenceKey, String(sequence));
  } catch {
    source = source || `web-${Date.now()}`;
    sequence = Date.now();
  }
  const turnID = `turn-${window.crypto?.randomUUID?.() ?? `${Date.now()}-${Math.random().toString(36).slice(2)}`}`;
  return { source, sequence, turnID };
}

function matchesFlowVoiceTurn(
  message: { source?: string; source_sequence?: number; turn_id?: string },
  turn: FlowVoiceTurn | null,
) {
  if (!turn) return true;
  if (message.source !== turn.source) return true;
  if (message.source_sequence !== undefined && message.source_sequence < turn.sourceSequence) {
    return false;
  }
  return !message.turn_id || message.turn_id === turn.id;
}

function rememberAcceptedVoiceSequence(
  sourceKey: string,
  sequenceKey: string,
  source: string | undefined,
  sequence: number | undefined,
) {
  if (!source || !Number.isSafeInteger(sequence) || !sequence || sequence < 1) return;
  try {
    if (window.localStorage.getItem(sourceKey) !== source) return;
    const current = Number(window.localStorage.getItem(sequenceKey)) || 0;
    if (sequence > current) window.localStorage.setItem(sequenceKey, String(sequence));
  } catch {
    /* storage is optional; the event journal remains durable */
  }
}

export function VoiceBar({
  token,
  sessionId,
  flowSession,
  enabled = true,
  disabled = false,
  onResult,
}: VoiceBarProps) {
  const [connected, setConnected] = useState(false);
  const [listening, setListening] = useState(false);
  const [awaitingResult, setAwaitingResult] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [partial, setPartial] = useState<string>("");
  const [assistantText, setAssistantText] = useState("");
  const [asrModels, setAsrModels] = useState<string[]>([]);
  const [ttsModels, setTtsModels] = useState<string[]>([]);
  const [asrModel, setAsrModel] = useState("");
  const [ttsModel, setTtsModel] = useState("");
  const [vadMode, setVadMode] = useState("server");
  const partialRef = useRef<string>("");
  const finalTranscriptRef = useRef("");
  const pendingEndRef = useRef(false);
  const endTimerRef = useRef<number | null>(null);
  const onResultRef = useRef(onResult);
  onResultRef.current = onResult;
  const flowSessionID = flowSession?.flowSessionId ?? "";
  const flowId = flowSession?.flowId ?? "";
  const isFlowSession = Boolean(flowId && flowSessionID);
  const flowVoicePrefix = `godex.flow-voice.${flowId}.${flowSessionID}`;
  const sourceKey = `${flowVoicePrefix}.source`;
  const sourceSequenceKey = `${flowVoicePrefix}.source-sequence`;
  const eventCursorKey = `${flowVoicePrefix}.event-sequence`;
  const voiceCursorKey = `${flowVoicePrefix}.voice-sequence`;
  const initialAfterSequenceRef = useRef(flowSession?.afterSequence ?? 0);

  const wsRef = useRef<WebSocket | null>(null);
  const streamRef = useRef<MediaStream | null>(null);
  const audioCtxRef = useRef<AudioContext | null>(null);
  const sourceRef = useRef<MediaStreamAudioSourceNode | null>(null);
  const processorRef = useRef<ScriptProcessorNode | null>(null);
  const ttsPlayerRef = useRef<PCMPlayer | null>(null);
  const activeFlowTurnRef = useRef<FlowVoiceTurn | null>(null);
  const activeTTSSpeechRef = useRef<ActiveTTSSpeech | null>(null);
  const eventCursorRef = useRef(0);
  const voiceCursorRef = useRef(0);

  const wsUrl = useCallback(() => {
    const base = window.location.origin.replace(/^http/, "ws");
    const params = new URLSearchParams();
    if (token) params.set("token", token);
    if (sessionId) params.set("session_id", sessionId);
    if (isFlowSession) {
      params.set("flow_id", flowId);
      params.set("flow_session_id", flowSessionID);
      eventCursorRef.current = readStoredSequence(eventCursorKey, initialAfterSequenceRef.current);
      voiceCursorRef.current = readStoredSequence(
        voiceCursorKey,
        initialAfterSequenceRef.current,
        false,
      );
      params.set("after_sequence", String(eventCursorRef.current));
      params.set("voice_after_sequence", String(voiceCursorRef.current));
    }
    return `${base}/v1/voice?${params.toString()}`;
  }, [
    token,
    sessionId,
    isFlowSession,
    flowId,
    flowSessionID,
    eventCursorKey,
    voiceCursorKey,
  ]);

  // 连接 /v1/voice
  useEffect(() => {
    if (disabled || (!sessionId && !isFlowSession) || !enabled) return;
    let closed = false;
    const ws = new WebSocket(wsUrl());
    wsRef.current = ws;

    ws.onopen = () => {
      if (closed) return;
      setConnected(true);
      setError(null);
      if (!isFlowSession) {
        ws.send(JSON.stringify({ type: "start" } satisfies VoiceMsg));
      }
    };
    ws.onmessage = (ev) => {
      if (typeof ev.data !== "string") {
        const speech = activeTTSSpeechRef.current;
        const speechID = speech?.id;
        if (isFlowSession && (!speech || !matchesFlowVoiceTurn(speech, activeFlowTurnRef.current))) {
          return;
        }
        void ev.data.arrayBuffer().then((buf: ArrayBuffer) => {
          if (isFlowSession) {
            const currentSpeech = activeTTSSpeechRef.current;
            if (
              !currentSpeech ||
              currentSpeech.id !== speechID ||
              !matchesFlowVoiceTurn(currentSpeech, activeFlowTurnRef.current)
            ) {
              return;
            }
          }
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
      } else if (msg.type === "ready") {
        const listedASR = msg.asr_models?.length
          ? msg.asr_models
          : (msg.models ?? []).filter((model) => model.startsWith("asr/"));
        const listedTTS = msg.tts_models?.length
          ? msg.tts_models
          : (msg.models ?? []).filter((model) => model.startsWith("tts/"));
        setAsrModels(listedASR);
        setTtsModels(listedTTS);
        setAsrModel((current) =>
          current && listedASR.includes(current) ? current : msg.default_asr || listedASR[0] || "",
        );
        setTtsModel((current) =>
          current && listedTTS.includes(current) ? current : msg.default_tts || listedTTS[0] || "",
        );
      } else if (msg.type === "asr_partial" && msg.text) {
        const next = [finalTranscriptRef.current, msg.text].filter(Boolean).join(" ");
        partialRef.current = next;
        setPartial(next);
      } else if (msg.type === "asr_final" && msg.text) {
        finalTranscriptRef.current = [finalTranscriptRef.current, msg.text].filter(Boolean).join(" ");
        partialRef.current = finalTranscriptRef.current;
        setPartial(finalTranscriptRef.current);
      } else if (msg.type === "asr_end") {
        if (pendingEndRef.current) {
          pendingEndRef.current = false;
          setAwaitingResult(false);
          if (endTimerRef.current !== null) {
            window.clearTimeout(endTimerRef.current);
            endTimerRef.current = null;
          }
          const text = partialRef.current.trim();
          if (text && !isFlowSession) {
            onResultRef.current?.(text);
            partialRef.current = "";
            finalTranscriptRef.current = "";
            setPartial("");
          }
        }
      } else if (msg.type === "assistant_text") {
        if (isFlowSession && !matchesFlowVoiceTurn(msg, activeFlowTurnRef.current)) return;
        activeTTSSpeechRef.current = null;
        setAssistantText(msg.text ?? "");
        ttsPlayerRef.current?.close();
        ttsPlayerRef.current = null;
      } else if (msg.type === "tts_start") {
        if (isFlowSession && !matchesFlowVoiceTurn(msg, activeFlowTurnRef.current)) return;
        if (msg.id) {
          activeTTSSpeechRef.current = {
            id: msg.id,
            turnID: msg.turn_id,
            source: msg.source,
            sourceSequence: msg.source_sequence,
          };
        }
      } else if (msg.type === "tts_done") {
        if (isFlowSession && activeTTSSpeechRef.current?.id !== msg.id) return;
        ttsPlayerRef.current?.end();
        ttsPlayerRef.current = null;
        activeTTSSpeechRef.current = null;
      } else if (msg.type === "tts_cancelled") {
        if (isFlowSession && activeTTSSpeechRef.current?.id !== msg.id) return;
        ttsPlayerRef.current?.close();
        ttsPlayerRef.current = null;
        activeTTSSpeechRef.current = null;
      } else if (msg.type === "session_event") {
        const event = msg.event;
        if (event?.sequence && event.sequence > eventCursorRef.current) {
          eventCursorRef.current = event.sequence;
          try {
            window.localStorage.setItem(eventCursorKey, String(event.sequence));
          } catch {
            /* storage is optional; the server journal remains durable */
          }
        }
        rememberAcceptedVoiceSequence(
          sourceKey,
          sourceSequenceKey,
          event?.source,
          event?.source_sequence,
        );
      } else if (msg.type === "flow_event_accepted") {
        rememberAcceptedVoiceSequence(
          sourceKey,
          sourceSequenceKey,
          msg.source,
          msg.source_sequence,
        );
      } else if ((msg.type === "voice_output_done" || msg.type === "voice_output_skipped") && msg.sequence) {
        voiceCursorRef.current = Math.max(voiceCursorRef.current, msg.sequence);
        try {
          window.localStorage.setItem(voiceCursorKey, String(voiceCursorRef.current));
        } catch {
          /* storage is optional; replay may repeat a completed response */
        }
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
      activeFlowTurnRef.current = null;
      activeTTSSpeechRef.current = null;
      setAwaitingResult(false);
      setConnected(false);
    };
  }, [
    wsUrl,
    disabled,
    sessionId,
    enabled,
    isFlowSession,
    sourceKey,
    sourceSequenceKey,
    eventCursorKey,
    voiceCursorKey,
  ]);

  // 开始录音：采集 16k s16 PCM 上行
  const startListening = useCallback(async () => {
    if (!wsRef.current || wsRef.current.readyState !== WebSocket.OPEN) {
      setError("voice not connected");
      return;
    }
    if (awaitingResult) return;
    if (endTimerRef.current !== null) {
      window.clearTimeout(endTimerRef.current);
      endTimerRef.current = null;
    }
    ttsPlayerRef.current?.close();
    ttsPlayerRef.current = null;
    activeTTSSpeechRef.current = null;
    finalTranscriptRef.current = "";
    partialRef.current = "";
    setPartial("");
    setAssistantText("");
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

      if (isFlowSession) {
        const turn = nextFlowVoiceTurn(flowId, flowSessionID);
        activeFlowTurnRef.current = {
          id: turn.turnID,
          source: turn.source,
          sourceSequence: turn.sequence,
        };
        wsRef.current.send(JSON.stringify({
          type: "start",
          turn_id: turn.turnID,
          source: turn.source,
          source_sequence: turn.sequence,
          event_type: flowSession?.eventType,
          output_field: flowSession?.outputField,
          asr_model: asrModel,
          tts_model: ttsModel,
          vad: vadMode,
        } satisfies VoiceMsg));
      }

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
      pendingEndRef.current = false;
      setError(null);
    } catch (err) {
      setError(`mic denied: ${String(err)}`);
    }
  }, [
    awaitingResult,
    isFlowSession,
    flowId,
    flowSessionID,
    flowSession?.eventType,
    flowSession?.outputField,
    asrModel,
    ttsModel,
    vadMode,
  ]);

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
      pendingEndRef.current = true;
      setAwaitingResult(true);
      endTimerRef.current = window.setTimeout(() => {
        endTimerRef.current = null;
        if (!pendingEndRef.current) return;
        pendingEndRef.current = false;
        setAwaitingResult(false);
        const text = partialRef.current.trim();
        if (text && !isFlowSession) {
          onResultRef.current?.(text);
        }
      }, 2500);
    } else {
      // 连接已断：直接用已累计文本（若有）。
      const text = partialRef.current.trim();
      partialRef.current = "";
      setPartial("");
      if (text) {
        onResultRef.current?.(text);
      }
    }
    // 不再立即读 partialRef：等服务端 flush 完成回 asr_end 再填充，
    // 避免最后一段识别结果还在途时提前读取（点击结束有时不填充）。
  }, [isFlowSession]);

  useEffect(() => () => stopListening(), [stopListening]);

  // 卸载时清理 asr_end 兜底 timer，避免组件销毁后回调 setState。
  useEffect(
    () => () => {
      if (endTimerRef.current !== null) {
        window.clearTimeout(endTimerRef.current);
        endTimerRef.current = null;
      }
    },
    [],
  );

  // 点击 toggle：录音中 → 停止并发送；否则 → 开始录音（清空上次回显）。
  const toggle = useCallback(() => {
    if (listening) {
      stopListening();
    } else {
      setPartial("");
      finalTranscriptRef.current = "";
      partialRef.current = "";
      void startListening();
    }
  }, [listening, startListening, stopListening]);

  const notEnabled = !enabled;
  const tip = notEnabled
    ? "语音未启用（设置 → Media / Audio → Voice Chat Enabled）"
    : error ?? (listening ? "录音中…点击停止" : awaitingResult ? "正在完成语音识别…" : connected ? "点击开始说话" : "语音未连接");

  return (
    <Space wrap size={6}>
      {isFlowSession && asrModels.length > 0 && (
        <Select
          size="small"
          aria-label="ASR model"
          value={asrModel || undefined}
          options={asrModels.map((model) => ({ value: model, label: model.replace(/^asr\//, "") }))}
          onChange={setAsrModel}
          style={{ width: 170 }}
          disabled={listening || awaitingResult}
        />
      )}
      {isFlowSession && asrModel.includes("zipformer") && (
        <Select
          size="small"
          aria-label="VAD mode"
          value={vadMode}
          options={[
            { value: "none", label: "Push-to-talk" },
            { value: "server", label: "Server VAD" },
          ]}
          onChange={setVadMode}
          style={{ width: 125 }}
          disabled={listening || awaitingResult}
        />
      )}
      {isFlowSession && ttsModels.length > 1 && (
        <Select
          size="small"
          aria-label="TTS model"
          value={ttsModel || undefined}
          options={ttsModels.map((model) => ({ value: model, label: model.replace(/^tts\//, "") }))}
          onChange={setTtsModel}
          style={{ width: 150 }}
          disabled={listening || awaitingResult}
        />
      )}
      {partial && (
        <Tooltip title="已识别内容（分段实时回显）">
          <span className="voice-partial">🎙 {partial}</span>
        </Tooltip>
      )}
      {assistantText && <Text type="secondary" style={{ maxWidth: 320 }} ellipsis>{assistantText}</Text>}
      <Tooltip title={tip}>
        <Button
          size="small"
          shape="circle"
          type={listening ? "primary" : "default"}
          danger={listening}
          className={listening ? "voice-btn-recording" : undefined}
          icon={listening ? <AudioOutlined /> : <AudioMutedOutlined />}
          disabled={disabled || !connected || !enabled || awaitingResult}
          onClick={toggle}
          aria-label={listening ? "停止录音并发送" : "开始语音输入"}
        />
      </Tooltip>
    </Space>
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
