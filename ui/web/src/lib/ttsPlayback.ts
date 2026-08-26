/** TTS 下行采样率（Kokoro 输出 24k s16 mono）。 */
export const TTS_SAMPLE_RATE = 24000;

export interface PCMPlayer {
  /** 追加一段 s16 小端 PCM：首帧立即播，后续帧排队续播（不乱序）。 */
  enqueue(bytes: Uint8Array): void;
  /** 正常收尾：不再接收新帧，但让已排队音频播完，全部结束后自动释放。 */
  end(): void;
  /** 立即停止并释放 AudioContext（丢弃排队中的音频）。 */
  close(): void;
}

/**
 * 创建 PCM 播放器：把 TTS 下行 PCM 帧（s16 小端 / 24k / mono）按到达顺序
 * 调度到 AudioContext，实现「首帧即播、边生成边播放」。
 * 同一播放器串行排队，保证帧间不乱序、不卡顿。
 */
export function createPCMPlayer(onDone?: () => void): PCMPlayer {
  const Ctx = window.AudioContext || (window as unknown as { webkitAudioContext: typeof AudioContext }).webkitAudioContext;
  const ctx = new Ctx();
  // 用户手势内创建后立即 resume，确保后续 WS 异步到达的 PCM 帧能正常出声。
  void ctx.resume().catch(() => {});
  let cursor = 0; // 已调度播放的累计时长（秒）
  let closed = false; // context 已释放
  let finished = false; // end() 已调用，不再接收新帧
  // 持有所有已调度节点的引用，防止被 GC 提前回收（Web Audio 经典坑：
  // 无引用的 AudioBufferSourceNode 可能在播放前被回收，导致排队播放的
  // 中间帧静默丢失——症状为「只播开头+结尾、中间全丢」）。
  const sources = new Set<AudioBufferSourceNode>();

  const release = () => {
    if (closed) return;
    closed = true;
    void ctx.close().catch(() => {});
  };

  // 正常收尾（end 后所有节点播完）→ 释放并通知调用方。
  const maybeDone = () => {
    if (finished && !closed && sources.size === 0) {
      release();
      onDone?.();
    }
  };

  return {
    enqueue(bytes: Uint8Array) {
      if (closed || finished || !bytes || bytes.length === 0) return;
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
      src.onended = () => {
        sources.delete(src);
        maybeDone();
      };
      sources.add(src);
      const dur = buffer.duration;
      const when = Math.max(ctx.currentTime, cursor);
      src.start(when);
      cursor = when + dur;
    },
    // 正常收尾：不再接收新帧，但让已排队音频全部播完，播完自动释放。
    end() {
      if (closed || finished) return;
      finished = true;
      maybeDone();
    },
    // 立即停止并释放（丢弃排队中的音频）。
    close() {
      if (closed) return;
      finished = true;
      closed = true;
      for (const src of sources) {
        try {
          src.stop();
        } catch {
          /* 已结束的节点 stop() 会抛错，忽略 */
        }
        src.disconnect();
      }
      sources.clear();
      void ctx.close().catch(() => {});
    },
  };
}
