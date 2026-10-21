// godex-feature: watch-asr
// W9 语音转写桥：手表录音 → voice-engine ASR → 文本。
// 入口：POST /watch/asr（见 routes_watch.go）
// 文档：docs/wearos-watch-implementation-plan.md §4 W9
//
// 为什么手表不自带识别：仿真器实测（§3.1 门禁 0.7）在无谷歌/厂商语音服务的
// 环境里没有任何 RecognitionService，SpeechRecognizer 不可用；而麦克风硬件
// 特性始终存在。因此「录音上传 + 服务端转写」是无 GMS 手表的主路径。
package httpapi

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	voiceclient "github.com/tim5wang/agent-local-voice-engine/client"
	"github.com/tim5wang/agent-local-voice-engine/protocol"
	"github.com/tim5wang/godex/internal/core/config"
)

// watchASRBridge transcribes a finished audio buffer by replaying it through
// the existing voice-engine ASR pipeline.
//
// It dials one short-lived connection per request rather than keeping a warm
// socket: watch dictation is bursty (a few utterances per hour), and an idle
// upstream socket would pin a model session on the engine for no benefit.
type watchASRBridge struct {
	manager *config.Manager
	// timeout bounds the whole transcription. A watch tap already waited for
	// the user to stop speaking; hanging here would strand the UI spinner.
	timeout time.Duration
}

func newWatchASRBridge(manager *config.Manager) *watchASRBridge {
	return &watchASRBridge{manager: manager, timeout: 45 * time.Second}
}

// engineAddr resolves the voice-engine websocket address using the same
// precedence as the live voice bridge (config → env → default).
func (b *watchASRBridge) engineAddr() string {
	addr := ""
	if b.manager != nil {
		addr = strings.TrimSpace(b.manager.Current().Media.Audio.VoiceEngineAddr)
	}
	if addr == "" {
		addr = strings.TrimSpace(os.Getenv("GODEX_VOICE_ENGINE_ADDR"))
	}
	if addr == "" {
		addr = "ws://127.0.0.1:17021/ws"
	}
	return addr
}

// Transcribe sends audio to the engine and returns the concatenated final
// transcript. Confidence is reported as 1.0 on success: the engine's protocol
// does not carry a per-utterance score, and inventing one would be misleading.
func (b *watchASRBridge) Transcribe(ctx context.Context, audio []byte, mimeType string) (string, float64, error) {
	if len(audio) == 0 {
		return "", 0, fmt.Errorf("empty audio buffer")
	}
	pcm, err := toEnginePCM(audio, mimeType)
	if err != nil {
		return "", 0, err
	}

	ctx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()

	conn, err := voiceclient.Dial(ctx, b.engineAddr())
	if err != nil {
		return "", 0, fmt.Errorf("voice engine unreachable at %s: %w", b.engineAddr(), err)
	}
	defer conn.Close()

	// Server-side VAD would try to segment a buffer that is already one
	// complete utterance, so ask for none and rely on audio_end to flush.
	if err := conn.StartASR("", protocol.VADNone); err != nil {
		return "", 0, fmt.Errorf("start asr: %w", err)
	}
	if err := conn.SendAudio(pcm); err != nil {
		return "", 0, fmt.Errorf("send audio: %w", err)
	}
	if err := conn.AudioEnd(); err != nil {
		return "", 0, fmt.Errorf("audio end: %w", err)
	}

	// Collect final segments until the engine signals end-of-utterance.
	var parts []string
	for {
		select {
		case <-ctx.Done():
			if len(parts) > 0 {
				return strings.Join(parts, " "), 1.0, nil
			}
			return "", 0, fmt.Errorf("transcription timed out: %w", ctx.Err())
		case ev, ok := <-conn.Events():
			if !ok {
				// Channel closed without asr_end: return what we have rather
				// than discarding a partial transcript.
				if len(parts) > 0 {
					return strings.Join(parts, " "), 1.0, nil
				}
				return "", 0, fmt.Errorf("voice engine closed the stream before transcribing")
			}
			switch ev.Kind {
			case voiceclient.EventASRFinal:
				if text := strings.TrimSpace(ev.Text); text != "" {
					parts = append(parts, text)
				}
			case voiceclient.EventASREnd:
				return strings.Join(parts, " "), 1.0, nil
			case voiceclient.EventError:
				return "", 0, fmt.Errorf("voice engine error: %s", firstNonEmptyString(ev.Text, ev.Code, "unknown"))
			}
		}
	}
}

// toEnginePCM normalises an uploaded audio buffer to the engine's expected
// format (s16 / 16kHz / mono).
//
// The watch records in whatever the platform offers, so the bridge accepts the
// common containers and strips the header rather than forcing the watch — a
// battery-constrained client — to do format conversion.
// errUnsupportedAudioType marks a rejection caused by the CALLER's input, not by
// the engine. The route maps it to 400 instead of 502: a 502 tells the watch the
// node or engine is broken and invites a retry, but retrying re-sends the same
// unsupported container forever. The watch needs to be told to fix its upload.
var errUnsupportedAudioType = errors.New("unsupported audio type")

func toEnginePCM(audio []byte, mimeType string) ([]byte, error) {
	mt := strings.ToLower(strings.TrimSpace(mimeType))
	if i := strings.Index(mt, ";"); i >= 0 {
		mt = strings.TrimSpace(mt[:i])
	}
	switch mt {
	case "", "audio/wav", "audio/wave", "audio/x-wav", "application/octet-stream":
		pcm, ok := stripWAVHeader(audio)
		if !ok {
			// Not a RIFF container: assume the caller already sent raw PCM.
			return audio, nil
		}
		return pcm, nil
	case "audio/pcm", "audio/l16", "audio/raw":
		return audio, nil
	default:
		return nil, fmt.Errorf("%w %q (send audio/wav or audio/pcm)", errUnsupportedAudioType, mimeType)
	}
}

// stripWAVHeader returns the PCM payload of a RIFF/WAVE buffer.
//
// It walks the chunk list rather than assuming a 44-byte header, because
// recorders on different platforms emit extra chunks (LIST/fact) before the
// data chunk and a fixed offset would corrupt the first samples.
func stripWAVHeader(audio []byte) ([]byte, bool) {
	if len(audio) < 12 || string(audio[0:4]) != "RIFF" || string(audio[8:12]) != "WAVE" {
		return nil, false
	}
	offset := 12
	for offset+8 <= len(audio) {
		chunkID := string(audio[offset : offset+4])
		chunkSize := int(uint32(audio[offset+4]) | uint32(audio[offset+5])<<8 |
			uint32(audio[offset+6])<<16 | uint32(audio[offset+7])<<24)
		dataStart := offset + 8
		if chunkID == "data" {
			if dataStart > len(audio) {
				return nil, false
			}
			end := dataStart + chunkSize
			if chunkSize <= 0 || end > len(audio) {
				// Declared size lies (common with streamed writers); trust the
				// actual buffer length and take the remainder.
				end = len(audio)
			}
			return audio[dataStart:end], true
		}
		// Chunks are word-aligned: odd sizes carry a pad byte.
		advance := chunkSize
		if advance%2 != 0 {
			advance++
		}
		if advance <= 0 {
			return nil, false
		}
		offset = dataStart + advance
	}
	return nil, false
}
