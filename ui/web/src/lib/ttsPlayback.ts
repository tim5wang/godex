/** TTS 下行采样率（Kokoro 输出 24k s16 mono）。 */
export const TTS_SAMPLE_RATE = 24000;

export interface PCMPlayer {
  /** 追加一段 s16 小端 PCM：首帧立即播，后续帧排队续播（不乱序）。 */
  enqueue(bytes: Uint8Array): void;
  /** 停止并释放 AudioContext。 */
  close(): void;
}

/**
 * 创建 PCM 播放器：把 TTS 下行 PCM 帧（s16 小端 / 24k / mono）按到达顺序
 * 调度到 AudioContext，实现「首帧即播、边生成边播放」。
 * 同一播放器串行排队，保证帧间不乱序、不卡顿。
 */
export function createPCMPlayer(): PCMPlayer {
  const Ctx = window.AudioContext || (window as unknown as { webkitAudioContext: typeof AudioContext }).webkitAudioContext;
  const ctx = new Ctx();
  // 用户手势内创建后立即 resume，确保后续 WS 异步到达的 PCM 帧能正常出声。
  void ctx.resume().catch(() => {});
  let cursor = 0; // 已调度播放的累计时长（秒）
  let closed = false;

  return {
    enqueue(bytes: Uint8Array) {
      if (closed || !bytes || bytes.length === 0) return;
      const samples = new Int16Array(bytes.buffer, bytes.byteOffset, bytes.length / 2);
      if (samples.length === 0) return;
      const buffer = ctx.createBuffer(1, samples.length, TTS_SAMPLE_RATE);
      const data = buffer.getChannelData(0);
      for (let i = 0; i < samples.length; i++) {
        data[i] = samples[i] / 0x8000;
      }
      const src = ctx.createBufferSource();
      src.buffer = buffer;
      src.connect(ctx.destination);
      const dur = buffer.duration;
      const when = Math.max(ctx.currentTime, cursor);
      src.start(when);
      cursor = when + dur;
    },
    close() {
      if (closed) return;
      closed = true;
      void ctx.close().catch(() => {});
    },
  };
}
