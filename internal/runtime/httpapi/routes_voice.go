package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/tim5wang/godex/internal/core/config"
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
	manager    *config.Manager
	engineAddr string
}

func newVoiceBridge(service *backend.Service, manager *config.Manager) *voiceBridge {
	b := &voiceBridge{service: service, manager: manager}
	b.refreshEngineAddr()
	return b
}

// refreshEngineAddr 从配置读取引擎地址（media.audio.voice_engine_addr），
// 空值时回退到环境变量 GODEX_VOICE_ENGINE_ADDR，再回退到协议默认地址。
func (b *voiceBridge) refreshEngineAddr() {
	addr := ""
	if b.manager != nil {
		addr = strings.TrimSpace(b.manager.Current().Media.Audio.VoiceEngineAddr)
	}
	if addr == "" {
		addr = strings.TrimSpace(os.Getenv("GODEX_VOICE_ENGINE_ADDR"))
	}
	if addr == "" {
		addr = protocol.DefaultAddr
	}
	b.engineAddr = addr
}

// voiceEnabled 返回是否启用了实时语音对话。
func (b *voiceBridge) voiceEnabled() bool {
	if b.manager == nil {
		return false
	}
	return b.manager.Current().Media.Audio.VoiceEnabled
}

// registerVoiceRoutes 注册语音桥接端点。
// 鉴权同时接受 Bearer header 与 ?token= query：浏览器 WebSocket 无法设置
// Authorization header，必须用 query token（见 withPreviewAuthProvider 先例）。
// tokenProvider 返回当前 web token（如 manager.Current().WebToken）。
// 未启用 media.audio.voice_enabled 时 /v1/voice 返回 404（前端据此隐藏/禁用）。
func registerVoiceRoutes(mux *http.ServeMux, service *backend.Service, manager *config.Manager, protected func(http.Handler) http.Handler, tokenProvider func() string) {
	if service == nil {
		return
	}
	b := newVoiceBridge(service, manager)
	auth := voiceQueryTokenAuth(protected, tokenProvider)
	mux.Handle("GET /v1/voice", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !b.voiceEnabled() {
			writeError(w, http.StatusNotFound, fmt.Errorf("voice chat disabled (media.audio.voice_enabled)"))
			return
		}
		b.refreshEngineAddr()
		b.handleVoice(w, r)
	})))
	// /v1/voice/status 诊断端点：返回启用状态与引擎可达性（不升级 WebSocket）。
	mux.Handle("GET /v1/voice/status", auth(http.HandlerFunc(b.handleVoiceStatus)))
	// /v1/tts 文本合成端点：POST {"text":"..."} → WAV 音频（供消息旁播放按钮）。
	mux.Handle("POST /v1/tts", auth(http.HandlerFunc(b.handleTTS)))
	// /v1/tts/stream 流式合成端点：WS 发 {"text":"..."} → PCM 帧边生成边推（首帧即播）。
	mux.Handle("GET /v1/tts/stream", auth(http.HandlerFunc(b.handleTTSStream)))
}

// ttsRequest 是 POST /v1/tts 的请求体。
type ttsRequest struct {
	Text string `json:"text"`
}

// handleTTS 处理 POST /v1/tts：文本 → voice-engine 合成 → WAV 音频返回。
func (b *voiceBridge) handleTTS(w http.ResponseWriter, r *http.Request) {
	if !b.voiceEnabled() {
		writeError(w, http.StatusNotFound, fmt.Errorf("voice chat disabled (media.audio.voice_enabled)"))
		return
	}
	var req ttsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("bad tts request: %w", err))
		return
	}
	text := strings.TrimSpace(req.Text)
	if text == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("empty text"))
		return
	}
	b.refreshEngineAddr()
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	ve, err := voiceclient.Dial(ctx, b.engineAddr)
	if err != nil {
		writeError(w, http.StatusBadGateway, fmt.Errorf("engine unreachable: %w", err))
		return
	}
	defer ve.Close()
	wav, err := ve.SynthesizeWAV(ctx, "http-tts", text)
	if err != nil {
		writeError(w, http.StatusBadGateway, fmt.Errorf("tts synthesize: %w", err))
		return
	}
	w.Header().Set("Content-Type", "audio/wav")
	w.Header().Set("Content-Length", strconv.Itoa(len(wav)))
	_, _ = w.Write(wav)
}

// handleTTSStream 处理 GET /v1/tts/stream：浏览器 WS 发文本，
// godex 桥接 voice-engine 逐帧转发 PCM（Binary 帧），最后发 tts_done（Text 帧）。
// 相比 POST /v1/tts 一次性等待全部合成，这里首帧即到，实现边生成边播放。
func (b *voiceBridge) handleTTSStream(w http.ResponseWriter, r *http.Request) {
	if !b.voiceEnabled() {
		writeError(w, http.StatusNotFound, fmt.Errorf("voice chat disabled (media.audio.voice_enabled)"))
		return
	}
	upgrader := websocket.Upgrader{
		HandshakeTimeout: 5 * time.Second,
		CheckOrigin:      func(*http.Request) bool { return true },
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	// 等浏览器发来的文本（首条消息）。
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	mt, data, err := conn.ReadMessage()
	if err != nil {
		writeVoiceError(conn, "bad_request", "missing text")
		return
	}
	if mt != websocket.TextMessage {
		writeVoiceError(conn, "bad_request", "expected text message")
		return
	}
	var req ttsRequest
	if err := json.Unmarshal(data, &req); err != nil {
		writeVoiceError(conn, "bad_request", "invalid JSON")
		return
	}
	text := strings.TrimSpace(req.Text)
	if text == "" {
		writeVoiceError(conn, "bad_request", "empty text")
		return
	}

	b.refreshEngineAddr()
	ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
	defer cancel()
	ve, err := voiceclient.Dial(ctx, b.engineAddr)
	if err != nil {
		writeVoiceError(conn, "engine_unreachable", err.Error())
		return
	}
	defer ve.Close()

	if err := ve.Synthesize("stream-tts", text); err != nil {
		writeVoiceError(conn, "tts_error", err.Error())
		return
	}

	var writeMu sync.Mutex
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-ve.Events():
			switch ev.Kind {
			case voiceclient.EventPCM:
				writeMu.Lock()
				_ = conn.WriteMessage(websocket.BinaryMessage, ev.PCM)
				writeMu.Unlock()
			case voiceclient.EventTTSDone:
				writeMu.Lock()
				_ = conn.WriteMessage(websocket.TextMessage, mustJSON(voiceMsg{Type: "tts_done"}))
				writeMu.Unlock()
				return
			case voiceclient.EventError:
				writeVoiceError(conn, ev.Code, ev.Text)
				return
			}
		}
	}
}

// handleVoiceStatus 处理 GET /v1/voice/status（可独立测试）。
func (b *voiceBridge) handleVoiceStatus(w http.ResponseWriter, r *http.Request) {
	b.refreshEngineAddr()
	writeJSON(w, http.StatusOK, voiceStatus{
		Enabled:    b.voiceEnabled(),
		EngineAddr: b.engineAddr,
		Reachable:  b.engineReachable(r.Context()),
	})
}

// voiceStatus 是 /v1/voice/status 的响应体。
type voiceStatus struct {
	Enabled    bool   `json:"enabled"`
	EngineAddr string `json:"engine_addr"`
	Reachable  bool   `json:"reachable"`
}

// engineReachable 探测 voice-engine 是否可达（TCP 拨号即断）。
func (b *voiceBridge) engineReachable(ctx context.Context) bool {
	d := net.Dialer{Timeout: 2 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", b.engineAddr)
	if err != nil {
		return false
	}
	conn.Close()
	return true
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

// pumpEngine 把 voice-engine 事件转发给 Web UI，并在 asr_final 时回显识别文本。
// 语音只负责输入：识别文本回显给前端（asr_partial），由用户在输入框编辑后手动发送，
// 不再自动提交 agent。
func (b *voiceBridge) pumpEngine(vc *voiceConn) {
	for ev := range vc.ve.Events() {
		select {
		case <-vc.closeCh:
			return
		default:
		}
		switch ev.Kind {
		case voiceclient.EventASRFinal:
			// 回显识别文本给 Web UI（分段识别反馈）。
			if strings.TrimSpace(ev.Text) != "" {
				vc.writeText(voiceMsg{Type: "asr_partial", Text: ev.Text})
			}
		case voiceclient.EventASREnd:
			// 本次录音全部转写完毕（audio_end → flush → asr_end）：通知前端填充输入框。
			vc.writeText(voiceMsg{Type: "asr_end"})
		case voiceclient.EventPCM:
			vc.writeBinary(ev.PCM)
		case voiceclient.EventTTSStart, voiceclient.EventTTSDone:
			vc.writeText(voiceMsg{Type: string(ev.Kind), ID: ev.ID})
		case voiceclient.EventError:
			vc.writeText(voiceMsg{Type: "error", Code: ev.Code, Text: ev.Text})
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
