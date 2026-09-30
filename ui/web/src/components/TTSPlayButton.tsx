import { App as AntApp, Button, Tooltip } from "antd";
import { LoadingOutlined, SoundOutlined } from "@ant-design/icons";
import { useRef, useState } from "react";
import { useI18n } from "../i18n";
import { createPCMPlayer, type PCMPlayer } from "../lib/ttsPlayback";

export function TTSPlayButton({ text, token, voiceEnabled = true }: { text: string; token?: string | null; voiceEnabled?: boolean }) {
  const { message } = AntApp.useApp();
  const { t } = useI18n();
  const [playing, setPlaying] = useState(false);
  const playerRef = useRef<PCMPlayer | null>(null);
  const wsRef = useRef<WebSocket | null>(null);
  const doneRef = useRef(false); // 本次播放是否已收尾（不再入队新帧）
  const pendingRef = useRef(0); // 尚未 enqueue 的 PCM 帧计数
  const playIdRef = useRef(0); // 播放代次：防旧代次异步帧串入新播放器

  const stop = () => {
    playIdRef.current += 1;
    doneRef.current = true;
    wsRef.current?.close();
    wsRef.current = null;
    playerRef.current?.close();
    playerRef.current = null;
    pendingRef.current = 0;
    setPlaying(false);
  };

  const play = () => {
    const trimmed = text.trim();
    if (playing || !trimmed || !voiceEnabled) return;
    const playId = ++playIdRef.current;
    setPlaying(true);
    doneRef.current = false;
    pendingRef.current = 0;
    // 流式端点：WS 发文本，收 PCM 帧边生成边播，收到 tts_done 后收尾。
    const base = window.location.origin.replace(/^http/, "ws");
    const params = new URLSearchParams();
    if (token) params.set("token", token);
    const ws = new WebSocket(`${base}/v1/tts/stream?${params.toString()}`);
    wsRef.current = ws;

      const enqueueFrame = (buf: ArrayBuffer) => {
        pendingRef.current -= 1;
        // 旧代次（已 stop/重新播放）的残留帧直接丢弃。
        if (playIdRef.current !== playId) return;
        // 已收尾且播放器已释放时，丢弃尚在转换中的残留帧。
        if (doneRef.current && !playerRef.current) return;
      if (!playerRef.current) {
        playerRef.current = createPCMPlayer(() => {
          playerRef.current = null;
          setPlaying(false);
        });
        }
        playerRef.current?.enqueue(new Uint8Array(buf));
        // 收尾时仍有异步帧待转换，等最后一帧入队后再结束播放器。
        if (doneRef.current && pendingRef.current <= 0) {
          playerRef.current?.end();
      }
    };

    const finish = (immediate: boolean) => {
      if (doneRef.current) return;
      doneRef.current = true;
      ws.close();
      wsRef.current = null;
      if (immediate) {
        // 错误或手动停止时丢弃排队音频。
        playerRef.current?.close();
        playerRef.current = null;
        pendingRef.current = 0;
        setPlaying(false);
      } else if (pendingRef.current <= 0) {
        // 正常收尾：播完自动复位；没有音频帧则立即复位。
        if (playerRef.current) {
          playerRef.current.end();
        } else {
          setPlaying(false);
        }
      }
      // pendingRef > 0 时由最后一次 enqueueFrame 触发 end()。
    };

    ws.onopen = () => {
      ws.send(JSON.stringify({ text: trimmed }));
    };
    ws.onmessage = (ev) => {
      if (typeof ev.data !== "string") {
        // 二进制下行帧先计数，避免 tts_done 抢在 arrayBuffer 转换前收尾。
        pendingRef.current += 1;
        void ev.data.arrayBuffer().then(enqueueFrame);
        return;
      }
      let msg: { type: string };
      try {
        msg = JSON.parse(ev.data);
      } catch {
        return;
      }
      if (msg.type === "tts_done") {
        finish(false);
      } else if (msg.type === "error") {
        void message.error(t("chat.playSpeechFailed"));
        finish(true);
      }
    };
    ws.onerror = () => {
      void message.error(t("chat.playSpeechFailed"));
      finish(true);
    };
    ws.onclose = () => {
      // 网络中断也保留已经排队的音频。
      finish(false);
    };
  };

  return (
    <Tooltip title={t("chat.speakMessage")}>
      <Button
        aria-label={t("chat.speakMessage")}
        icon={playing ? <LoadingOutlined /> : <SoundOutlined />}
        onClick={(event) => {
          event.stopPropagation();
          if (playing) {
            stop();
          } else {
            play();
          }
        }}
        shape="circle"
        size="small"
        type="text"
      />
    </Tooltip>
  );
}
