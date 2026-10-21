// godex-feature: watch-tts
// B7 腕上朗读：文本（或某个会话的精简回复）→ voice-engine TTS → WAV 音频。
// 入口：POST /watch/tts（见 routes_watch.go）
// 文档：docs/wearos-watch-implementation-plan.md §12.2 B7
//
// 复用而非另造：godex 已有 POST /v1/tts（文本 → voice-engine → WAV），这里只是
// 把手表凭证接进同一条合成链路。加一条独立的 TTS 通路会变成两份需要同步维护的
// 合成逻辑，且手表那条必然会与 Web 端产生行为差异。
package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	voiceclient "github.com/tim5wang/agent-local-voice-engine/client"
	"github.com/tim5wang/godex/internal/core/config"
)

// watchTTSProvider is the optional text-to-speech bridge. Handlers detect it by
// type assertion, exactly like watchASRProvider, so the watch surface still builds
// and serves 503 (not a panic) with voice disabled.
type watchTTSProvider interface {
	Synthesize(ctx context.Context, text string) ([]byte, error)
}

type watchTTSBridge struct {
	manager *config.Manager
	// timeout bounds synthesis. The synthesiser is local CPU inference, so a short
	// reply is quick; this is generous because a cold model load is the slow case,
	// and a watch waiting on a spinner is worse than a slightly slow answer.
	timeout time.Duration
}

func newWatchTTSBridge(manager *config.Manager) *watchTTSBridge {
	return &watchTTSBridge{manager: manager, timeout: 90 * time.Second}
}

// registerWatchTTSRoute wires POST /watch/tts (B7).
//
// Scope is watch:command, not watch:read: synthesis burns node CPU on a
// caller-supplied string, and the voice surface (ASR + TTS) stays on one scope. A
// read-only device can hear a reply by reading reply_summary (B4) instead.
func registerWatchTTSRoute(mux *http.ServeMux, api watchAPI, command func(http.Handler) http.Handler) {
	tts := api.tts

	mux.Handle("POST /watch/tts", command(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tts == nil {
			writeError(w, http.StatusServiceUnavailable,
				fmt.Errorf("speech synthesis is not configured on this node"))
			return
		}
		if api.service == nil {
			writeError(w, http.StatusServiceUnavailable, fmt.Errorf("session service unavailable"))
			return
		}
		var req watchTTSRequest
		if err := decodeJSONAllowEmpty(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}

		text := strings.TrimSpace(req.Text)
		// No text supplied: read back the session's last reply. Deriving it on the
		// node keeps the wrist from having to re-implement the B4 shaping, and makes
		// the common case ("hear what it just said") one short request.
		if text == "" {
			sessionID := strings.TrimSpace(req.SessionID)
			if sessionID == "" {
				writeError(w, http.StatusBadRequest,
					fmt.Errorf("text or session_id is required"))
				return
			}
			snapshot, err := loadWatchSession(r.Context(), api.service, sessionID)
			if err != nil {
				writeError(w, statusForSessionError(err), err)
				return
			}
			text = watchReplySummary(lastAssistantText(snapshot.Messages))
			if text == "" {
				// Nothing to read back. 404 rather than synthesising silence, so the
				// watch can show "还没有回复" instead of playing an empty clip.
				writeError(w, http.StatusNotFound,
					fmt.Errorf("this session has no reply to speak yet"))
				return
			}
		}

		wav, err := tts.Synthesize(r.Context(), text)
		if err != nil {
			writeError(w, http.StatusBadGateway, err)
			return
		}
		w.Header().Set("Content-Type", "audio/wav")
		w.Header().Set("Content-Length", strconv.Itoa(len(wav)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(wav)
	})))
}

// watchTTSRequest is the POST /watch/tts body. Both fields are optional but at
// least one is required; see the handler.
type watchTTSRequest struct {
	// SessionID reads back that session's latest reply (shaped for a wrist).
	SessionID string `json:"session_id,omitempty"`
	// Text speaks an explicit string.
	Text string `json:"text,omitempty"`
}

// engineAddr mirrors watchASRBridge's precedence (config → env → protocol default).
// Duplicated deliberately rather than shared: the two bridges are independent
// optional capabilities, and coupling them would make disabling one touch the other.
func (b *watchTTSBridge) engineAddr() string {
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

// Synthesize renders text to a WAV container.
func (b *watchTTSBridge) Synthesize(ctx context.Context, text string) ([]byte, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, fmt.Errorf("empty text")
	}

	ctx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()

	conn, err := voiceclient.Dial(ctx, b.engineAddr())
	if err != nil {
		return nil, fmt.Errorf("voice engine unreachable at %s: %w", b.engineAddr(), err)
	}
	defer conn.Close()

	wav, err := conn.SynthesizeWAV(ctx, "watch-tts", text)
	if err != nil {
		return nil, fmt.Errorf("tts synthesize: %w", err)
	}
	if len(wav) == 0 {
		return nil, fmt.Errorf("voice engine returned no audio")
	}
	return wav, nil
}
