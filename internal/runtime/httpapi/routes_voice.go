package httpapi

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/tim5wang/godex/internal/domain/events"
	"github.com/tim5wang/godex/internal/domain/message"
	"github.com/tim5wang/godex/internal/services/backend"

	voiceclient "github.com/tim5wang/voice-engine/client"
	"github.com/tim5wang/voice-engine/protocol"
)

// voiceBridge 桥接 Web UI ↔ voice-engine ↔ godex agent：
//
//	Web UI --ws--> godex /v1/voice --ws--> voice-engine
//	         上行麦克风 PCM → voice-engine VAD+ASR
//	         asr_final 文本 → SubmitAsync 提交 agent
//	         agent 回复 → voice-engine TTS → 下行 PCM → Web UI
type voiceBridge struct {
	service    *backend.Service
	engineAddr string
}

func newVoiceBridge(service *backend.Service) *voiceBridge {
	addr := strings.TrimSpace(os.Getenv("GODEX_VOICE_ENGINE_ADDR"))
	if addr == "" {
		addr = protocol.DefaultAddr
	}
	return &voiceBridge{service: service, engineAddr: addr}
}

// registerVoiceRoutes 注册语音桥接端点。
// 鉴权同时接受 Bearer header 与 ?token= query：浏览器 WebSocket 无法设置
// Authorization header，必须用 query token（见 withPreviewAuthProvider 先例）。
// tokenProvider 返回当前 web token（如 manager.Current().WebToken）。
func registerVoiceRoutes(mux *http.ServeMux, service *backend.Service, protected func(http.Handler) http.Handler, tokenProvider func() string) {
	if service == nil {
		return
	}
	b := newVoiceBridge(service)
	auth := voiceQueryTokenAuth(protected, tokenProvider)
	mux.Handle("GET /v1/voice", auth(http.HandlerFunc(b.handleVoice)))
}

// voiceQueryTokenAuth 包装 protected 鉴权，额外允许 ?token= query 通过。
// 这是浏览器 WebSocket 客户端（无法设置 header）连接 /v1/voice 的唯一途径。
func voiceQueryTokenAuth(protected func(http.Handler) http.Handler, tokenProvider func() string) func(http.Handler) http.Handler {
	return func(h http.Handler) http.Handler {
		base := protected(h)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tok := ""
			if tokenProvider != nil {
				tok = strings.TrimSpace(tokenProvider())
			}
			if q := strings.TrimSpace(r.URL.Query().Get("token")); q != "" && tok != "" && q == tok {
				h.ServeHTTP(w, r)
				return
			}
			base.ServeHTTP(w, r)
		})
	}
}

// voiceConn 是 Web UI ↔ godex 的连接状态。
type voiceConn struct {
	ws        *websocket.Conn
	writeMu   sync.Mutex
	ve        *voiceclient.Conn
	sessionID string
	closeCh   chan struct{}
}

func (b *voiceBridge) handleVoice(w http.ResponseWriter, r *http.Request) {
	upgrader := websocket.Upgrader{
		HandshakeTimeout: 5 * time.Second,
		CheckOrigin:      func(*http.Request) bool { return true },
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	// 连接 voice-engine。
	ve, err := voiceclient.Dial(r.Context(), b.engineAddr)
	if err != nil {
		writeVoiceError(conn, "engine_unreachable", err.Error())
		return
	}
	defer ve.Close()

	vc := &voiceConn{
		ws:      conn,
		ve:      ve,
		closeCh: make(chan struct{}),
	}
	if sid := strings.TrimSpace(r.URL.Query().Get("session_id")); sid != "" {
		vc.sessionID = sid
	}

	// 事件泵：voice-engine 事件 → Web UI。
	go b.pumpEngine(vc)

	// 读循环：Web UI → voice-engine。
	vc.serve(r.Context())
}

// pumpEngine 把 voice-engine 事件转发给 Web UI，并在 asr_final 时编排 agent。
func (b *voiceBridge) pumpEngine(vc *voiceConn) {
	for ev := range vc.ve.Events() {
		select {
		case <-vc.closeCh:
			return
		default:
		}
		switch ev.Kind {
		case voiceclient.EventASRFinal:
			b.orchestrate(vc, ev.Text)
		case voiceclient.EventPCM:
			vc.writeBinary(ev.PCM)
		case voiceclient.EventTTSStart, voiceclient.EventTTSDone:
			vc.writeText(voiceMsg{Type: string(ev.Kind), ID: ev.ID})
		case voiceclient.EventError:
			vc.writeText(voiceMsg{Type: "error", Code: ev.Code, Text: ev.Text})
		}
	}
}

// orchestrate 把 ASR 文本提交给 agent，收集回复并请求 TTS。
func (b *voiceBridge) orchestrate(vc *voiceConn, text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	sessionID := vc.sessionID
	if sessionID == "" {
		// 未指定会话时复用默认 web 会话（key=voice）。
		opened, err := b.service.OpenSession(context.Background(), backend.SessionLocator{
			Channel: "web",
			Key:     "voice",
		})
		if err != nil {
			vc.writeText(voiceMsg{Type: "error", Code: "session_error", Text: err.Error()})
			return
		}
		sessionID = opened.SessionID
		vc.sessionID = sessionID
	}

	envelope := message.NewRuntimeEnvelope(message.SourceVoice, sessionID, "voice", text, time.Now(), nil)
	result, err := b.service.SubmitAsync(context.Background(), sessionID, envelope)
	if err != nil {
		vc.writeText(voiceMsg{Type: "error", Code: "submit_error", Text: err.Error()})
		return
	}

	// 收集 assistant 回复（同 TurnID 的 delta，直到 turn 完成）。
	reply := b.collectReply(context.Background(), sessionID, result.TurnID)
	if strings.TrimSpace(reply) == "" {
		return
	}
	vc.writeText(voiceMsg{Type: "assistant_text", Text: reply})
	if err := vc.ve.Synthesize("reply-"+result.TurnID, reply); err != nil {
		vc.writeText(voiceMsg{Type: "error", Code: "tts_error", Text: err.Error()})
	}
}

// collectReply 收集 agent 一个 turn 的完整回复文本。
func (b *voiceBridge) collectReply(ctx context.Context, sessionID, turnID string) string {
	eventCh := make(chan events.Event, 128)
	unsubscribe, err := b.service.AttachSink(sessionID, events.SinkFunc(func(event events.Event) {
		select {
		case <-ctx.Done():
		case eventCh <- event:
		default:
		}
	}))
	if err != nil {
		return ""
	}
	defer unsubscribe()

	var builder strings.Builder
	timer := time.NewTimer(60 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return builder.String()
		case <-timer.C:
			return builder.String()
		case event := <-eventCh:
			if event.TurnID != turnID {
				continue
			}
			switch event.Type {
			case events.EventAssistantTextDelta:
				if payload, ok := event.Payload.(events.TextPayload); ok {
					builder.WriteString(payload.Text)
				}
			case events.EventTurnCompleted:
				return builder.String()
			case events.EventErrorRaised:
				if payload, ok := event.Payload.(events.NoticePayload); ok {
					log.Printf("[voice] turn error: %s", payload.Message)
				}
				return builder.String()
			}
		}
	}
}

// serve 读循环：Web UI 的二进制音频与控制消息 → voice-engine。
func (vc *voiceConn) serve(ctx context.Context) {
	defer close(vc.closeCh)
	for {
		mt, data, err := vc.ws.ReadMessage()
		if err != nil {
			return
		}
		if mt == websocket.BinaryMessage {
			if err := vc.ve.SendAudio(data); err != nil {
				vc.writeText(voiceMsg{Type: "error", Code: "send_audio", Text: err.Error()})
				return
			}
			continue
		}
		var msg voiceMsg
		if err := json.Unmarshal(data, &msg); err != nil {
			vc.writeText(voiceMsg{Type: "error", Code: "bad_message", Text: "invalid JSON"})
			continue
		}
		switch msg.Type {
		case string(protocol.KindStart):
			if err := vc.ve.Start(protocol.VADServer); err != nil {
				vc.writeText(voiceMsg{Type: "error", Code: "start_error", Text: err.Error()})
				return
			}
		case string(protocol.KindAudioEnd):
			_ = vc.ve.AudioEnd()
		case string(protocol.KindStop):
			return
		}
	}
}

func (vc *voiceConn) writeText(msg voiceMsg) {
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	vc.writeMu.Lock()
	defer vc.writeMu.Unlock()
	_ = vc.ws.WriteMessage(websocket.TextMessage, data)
}

func (vc *voiceConn) writeBinary(pcm []byte) {
	vc.writeMu.Lock()
	defer vc.writeMu.Unlock()
	_ = vc.ws.WriteMessage(websocket.BinaryMessage, pcm)
}

// voiceMsg 是 Web UI ↔ godex 的 JSON 控制消息。
// 字段与 voice-engine protocol.Message 对齐，加 assistant_text 透传 agent 回复。
type voiceMsg struct {
	Type  string `json:"type"`
	Code  string `json:"code,omitempty"`
	Text  string `json:"text,omitempty"`
	ID    string `json:"id,omitempty"`
}

// writeVoiceError 向尚未升级的 HTTP 响应写错误（升级前失败用）。
func writeVoiceError(conn *websocket.Conn, code, text string) {
	_ = conn.WriteMessage(websocket.TextMessage, mustJSON(voiceMsg{Type: "error", Code: code, Text: text}))
}

func mustJSON(v any) []byte {
	data, _ := json.Marshal(v)
	return data
}
