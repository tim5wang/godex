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

	"github.com/tim5wang/godex/internal/agent"
	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/services/backend"

	voiceclient "github.com/tim5wang/agent-local-voice-engine/client"
	"github.com/tim5wang/agent-local-voice-engine/protocol"
)

const maxVoiceWSMessage = 64 << 10

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
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()
	status := voiceStatus{Enabled: b.voiceEnabled(), EngineAddr: b.engineAddr}
	ve, err := dialVoiceEngine(ctx, b.engineAddr)
	if err != nil {
		status.Error = err.Error()
		writeJSON(w, http.StatusOK, status)
		return
	}
	capabilities := ve.Capabilities()
	_ = ve.Close()
	status.Reachable = true
	status.ASRModels = capabilities.ASRModels
	status.VADModels = capabilities.VADModels
	status.TTSModels = capabilities.TTSModels
	status.DefaultASR = capabilities.DefaultASR
	status.DefaultVAD = capabilities.DefaultVAD
	status.DefaultTTS = capabilities.DefaultTTS
	missing := make([]string, 0, 3)
	if capabilities.DefaultASR == "" || !capabilityContains(capabilities.ASRModels, capabilities.DefaultASR) {
		missing = append(missing, "default ASR model")
	}
	if capabilities.DefaultVAD == "" || !capabilityContains(capabilities.VADModels, capabilities.DefaultVAD) {
		missing = append(missing, "default VAD model")
	}
	if capabilities.DefaultTTS == "" || !capabilityContains(capabilities.TTSModels, capabilities.DefaultTTS) {
		missing = append(missing, "default TTS model")
	}
	status.Ready = len(missing) == 0
	if !status.Ready {
		status.Error = "voice-engine WebSocket handshake succeeded, but readiness is missing " + strings.Join(missing, ", ")
	}
	writeJSON(w, http.StatusOK, status)
}

// voiceStatus 是 /v1/voice/status 的响应体。
type voiceStatus struct {
	Enabled    bool     `json:"enabled"`
	EngineAddr string   `json:"engine_addr"`
	Reachable  bool     `json:"reachable"`
	Ready      bool     `json:"ready"`
	Error      string   `json:"error,omitempty"`
	ASRModels  []string `json:"asr_models,omitempty"`
	VADModels  []string `json:"vad_models,omitempty"`
	TTSModels  []string `json:"tts_models,omitempty"`
	DefaultASR string   `json:"default_asr,omitempty"`
	DefaultVAD string   `json:"default_vad,omitempty"`
	DefaultTTS string   `json:"default_tts,omitempty"`
}

func capabilityContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
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
	ws                *websocket.Conn
	writeMu           sync.Mutex
	stateMu           sync.Mutex
	service           *backend.Service
	interruptTurn     func(flowID, sessionID, source, eventType string, newSourceSequence uint64) (bool, error)
	ve                *voiceEngineConn
	sessionID         string
	flowID            string
	flowSessionID     string
	afterSequence     uint64
	voiceAfterSeq     uint64
	eventOffset       int64
	currentSourceSeq  uint64
	currentSource     string
	flowVoiceSource   string
	flowStartSeq      uint64
	flowNextSourceSeq uint64
	flowSpeechSeen    bool
	flowSpeechActive  bool
	flowEventType     string
	flowOutputField   string
	flowTTSModel      string
	eventType         string
	currentEventType  string
	outputField       string
	ttsModel          string
	currentTurnID     string
	turnGeneration    uint64
	turnSourceSeq     uint64
	pendingSpeech     map[string]pendingVoiceSpeech
	turnTranscript    strings.Builder
	closeCh           chan struct{}
}

type pendingVoiceSpeech struct {
	sequence       uint64
	generation     uint64
	turnID         string
	source         string
	sourceSequence uint64
}

func (b *voiceBridge) handleVoice(w http.ResponseWriter, r *http.Request) {
	flowID, flowSessionID := strings.TrimSpace(r.URL.Query().Get("flow_id")), strings.TrimSpace(r.URL.Query().Get("flow_session_id"))
	var afterSequence uint64
	var sessionView agent.FlowSessionView
	eventType := strings.TrimSpace(r.URL.Query().Get("event_type"))
	if (flowID == "") != (flowSessionID == "") {
		writeError(w, http.StatusBadRequest, fmt.Errorf("flow_id and flow_session_id must be provided together"))
		return
	}
	if flowID != "" {
		if b.service == nil {
			writeError(w, http.StatusServiceUnavailable, fmt.Errorf("flow session service unavailable"))
			return
		}
		view, err := b.service.GetFlowSession(flowID, flowSessionID)
		if err != nil {
			writeFlowSessionError(w, err)
			return
		}
		if view.Status != "active" {
			writeError(w, http.StatusConflict, fmt.Errorf("voice adapter requires an active flow session, got %q", view.Status))
			return
		}
		sessionView = view
		if afterSequence, err = parseFlowSessionSequence(r.URL.Query().Get("after_sequence")); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if afterSequence > view.LastSequence {
			writeError(w, http.StatusBadRequest, fmt.Errorf("after_sequence %d exceeds the latest session sequence %d", afterSequence, view.LastSequence))
			return
		}
		if eventType == "" {
			eventType = "voice.asr_final"
		}
		if len(eventType) > 128 {
			writeError(w, http.StatusBadRequest, fmt.Errorf("event_type must not exceed 128 characters"))
			return
		}
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
	conn.SetReadLimit(maxVoiceWSMessage)

	// 连接 voice-engine。
	ve, err := dialVoiceEngine(r.Context(), b.engineAddr)
	if err != nil {
		writeVoiceError(conn, "engine_unreachable", err.Error())
		return
	}
	defer ve.Close()

	vc := &voiceConn{
		ws:            conn,
		service:       b.service,
		ve:            ve,
		flowID:        flowID,
		flowSessionID: flowSessionID,
		afterSequence: afterSequence,
		eventType:     eventType,
		closeCh:       make(chan struct{}),
		pendingSpeech: make(map[string]pendingVoiceSpeech),
	}
	if sid := strings.TrimSpace(r.URL.Query().Get("session_id")); sid != "" {
		vc.sessionID = sid
	}
	if flowID != "" {
		if inFlight := sessionView.InFlight; inFlight != nil {
			vc.currentSource = inFlight.Source
			vc.currentSourceSeq = inFlight.SourceSequence
			vc.turnSourceSeq = inFlight.SourceSequence
			vc.currentEventType = inFlight.EventType
		}
		voiceAfterSequence := sessionView.LastSequence
		if rawVoiceSequence := strings.TrimSpace(r.URL.Query().Get("voice_after_sequence")); rawVoiceSequence != "" {
			voiceAfterSequence, err = parseFlowSessionSequence(rawVoiceSequence)
			if err != nil {
				writeVoiceError(conn, "bad_request", err.Error())
				return
			}
			if voiceAfterSequence > sessionView.LastSequence {
				writeVoiceError(conn, "bad_request", fmt.Sprintf("voice_after_sequence %d exceeds the latest session sequence %d", voiceAfterSequence, sessionView.LastSequence))
				return
			}
		}
		vc.voiceAfterSeq = voiceAfterSequence
		vc.outputField = strings.TrimSpace(r.URL.Query().Get("output_field"))
		vc.writeJSON(mustJSON(map[string]any{
			"type": "resumed", "after_sequence": afterSequence,
			"status": sessionView.Status, "state": sessionView.State, "state_version": sessionView.StateVersion,
			"latest_signal_outputs": sessionView.LatestSignalOutputs,
		}))
	}
	capabilities := ve.Capabilities()
	vc.writeText(voiceMsg{
		Type: "ready", Models: capabilities.Models,
		ASRModels: capabilities.ASRModels, VADModels: capabilities.VADModels, TTSModels: capabilities.TTSModels,
		DefaultASR: capabilities.DefaultASR, DefaultVAD: capabilities.DefaultVAD, DefaultTTS: capabilities.DefaultTTS,
	})

	// 事件泵：voice-engine 事件 → Web UI。
	go b.pumpEngine(vc)
	if flowID != "" {
		go b.pumpFlowSession(vc)
	}

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
		case voiceclient.EventKind("speech_started"):
			b.beginDetectedVoiceTurn(vc)
		case voiceclient.EventKind("asr_partial"):
			b.ensureDetectedVoiceTurn(vc)
			if strings.TrimSpace(ev.Text) != "" {
				vc.writeText(voiceMsg{Type: "asr_partial", Text: ev.Text})
			}
		case voiceclient.EventASRFinal:
			b.ensureDetectedVoiceTurn(vc)
			if strings.TrimSpace(ev.Text) != "" {
				vc.recordASRFinal(ev.Text)
				vc.writeText(voiceMsg{Type: "asr_final", Text: ev.Text})
			}
		case voiceclient.EventKind("asr_utterance_end"):
			if vc.flowID != "" {
				if err := b.persistFlowSessionTranscript(vc); err != nil {
					vc.writeText(voiceMsg{Type: "error", Code: "flow_session_event", Text: err.Error()})
				}
				vc.stateMu.Lock()
				vc.flowSpeechActive = false
				vc.stateMu.Unlock()
			}
		case voiceclient.EventASREnd:
			if vc.flowID != "" {
				// Compatibility fallback for older voice-engine versions that only
				// emit asr_end after audio_end.
				if err := b.persistFlowSessionTranscript(vc); err != nil {
					vc.writeText(voiceMsg{Type: "error", Code: "flow_session_event", Text: err.Error()})
				}
				vc.stateMu.Lock()
				vc.flowSpeechActive = false
				vc.stateMu.Unlock()
			}
			// 本次录音全部转写完毕（audio_end → flush → asr_end）：通知前端填充输入框。
			vc.writeText(voiceMsg{Type: "asr_end"})
		case voiceclient.EventPCM:
			vc.writeSpeechPCM(ev.ID, ev.PCM)
		case voiceclient.EventTTSStart:
			if !vc.writeSpeechMessage(ev.ID, voiceMsg{Type: string(ev.Kind), ID: ev.ID}) {
				go func(id string) { _ = vc.ve.CancelTTS(id) }(ev.ID)
			}
		case voiceclient.EventTTSDone:
			vc.completeSpeech(ev.ID)
		case voiceclient.EventKind("tts_cancelled"):
			vc.skipPendingSpeech(ev.ID)
		case voiceclient.EventError:
			vc.writeText(voiceMsg{Type: "error", Code: ev.Code, Text: ev.Text})
		}
	}
}

func (b *voiceBridge) beginDetectedVoiceTurn(vc *voiceConn) {
	if vc.flowID == "" {
		vc.writeText(voiceMsg{Type: "speech_started"})
		return
	}

	// A prior utterance may have failed its first durable append. Retry it
	// before switching turns so its text cannot be merged into the next one.
	// The append key is stable (source + sequence), so an ambiguous write is
	// safe to retry without executing an accepted event twice.
	if err := b.persistFlowSessionTranscript(vc); err != nil {
		vc.writeText(voiceMsg{Type: "error", Code: "flow_session_event", Text: err.Error()})
	}

	vc.stateMu.Lock()
	sequence := vc.flowNextSourceSeq
	if sequence == 0 && !vc.flowSpeechSeen {
		sequence = vc.flowStartSeq
	}
	if sequence != 0 {
		if sequence == ^uint64(0) {
			vc.flowNextSourceSeq = 0
		} else {
			vc.flowNextSourceSeq = sequence + 1
		}
	}
	msg := voiceMsg{
		Type: "start", Source: vc.flowVoiceSource,
		SourceSequence: sequence,
		EventType:      vc.flowEventType,
		OutputField:    vc.flowOutputField,
		TTSModel:       vc.flowTTSModel,
	}
	vc.stateMu.Unlock()
	if msg.SourceSequence == 0 {
		vc.writeText(voiceMsg{
			Type: "error", Code: "voice_sequence_missing",
			Text: "语音轮次缺少 source_sequence，请停止后重新开始麦克风。",
		})
		return
	}
	msg.TurnID = voiceTurnID(msg.Source, msg.SourceSequence)
	previous, err := vc.fenceTurn(msg)
	if err != nil {
		vc.writeText(voiceMsg{Type: "error", Code: "voice_turn_start", Text: err.Error()})
		return
	}
	vc.stateMu.Lock()
	vc.flowSpeechSeen = true
	vc.flowSpeechActive = true
	vc.stateMu.Unlock()
	vc.writeText(voiceMsg{
		Type: "speech_started", TurnID: msg.TurnID, Source: msg.Source,
		SourceSequence: msg.SourceSequence,
	})
	// Fence the old turn before acknowledging the new onset, but do the
	// durable scheduler cancellation and engine-side TTS cancellation off the
	// event pump. Those operations may wait on worker/session locks; blocking
	// this pump also blocks the ASR final for the utterance that just began.
	go b.finishDetectedVoiceTurn(vc, previous, msg.Source, msg.SourceSequence)
}

func (b *voiceBridge) ensureDetectedVoiceTurn(vc *voiceConn) {
	if vc.flowID == "" {
		return
	}
	vc.stateMu.Lock()
	active := vc.flowSpeechActive
	vc.stateMu.Unlock()
	if !active {
		// Older v1 engines may not emit speech_started. Treat their first ASR
		// event as a turn boundary so reconnect-compatible engines still work.
		b.beginDetectedVoiceTurn(vc)
	}
}

func (b *voiceBridge) finishDetectedVoiceTurn(vc *voiceConn, previous voiceTurnFence, source string, sequence uint64) {
	vc.notifySkippedSpeech(previous.speech)
	vc.cancelPreviousSpeech(previous.speech)
	if err := vc.interruptPreviousTurn(previous, source, sequence); err != nil {
		_ = vc.writeText(voiceMsg{
			Type: "error", Code: "turn_interrupt_failed",
			Text: "the previous Flow turn could not be durably interrupted: " + err.Error(),
		})
	}
}

func (b *voiceBridge) pumpFlowSession(vc *voiceConn) {
	changed, unsubscribe := b.service.SubscribeFlowSessionEvents(vc.flowID, vc.flowSessionID)
	defer unsubscribe()
	for {
		cursor, offset := vc.flowSessionReplayCursor()
		err := writeFlowSessionEventPages(
			&cursor,
			&offset,
			100,
			func(after uint64, byteOffset int64, limit int) ([]agent.FlowSessionEvent, int64, error) {
				return b.service.FlowSessionEventPage(vc.flowID, vc.flowSessionID, after, byteOffset, limit)
			},
			func(event agent.FlowSessionEvent) error {
				message, err := json.Marshal(map[string]any{"type": "session_event", "event": event})
				if err != nil {
					return err
				}
				if err := vc.writeJSON(message); err != nil {
					return err
				}
				b.speakFlowSessionOutput(vc, event)
				return nil
			},
		)
		if err != nil {
			vc.writeText(voiceMsg{Type: "error", Code: "flow_session_events", Text: err.Error()})
			return
		}
		vc.setFlowSessionReplayCursor(cursor, offset)
		view, err := b.service.GetFlowSession(vc.flowID, vc.flowSessionID)
		if err != nil {
			vc.writeText(voiceMsg{Type: "error", Code: "flow_session_snapshot", Text: err.Error()})
			return
		}
		snapshot, err := json.Marshal(map[string]any{
			"type": "session_snapshot", "status": view.Status, "state": view.State,
			"state_version": view.StateVersion, "latest_signal_outputs": view.LatestSignalOutputs,
			"in_flight": view.InFlight,
		})
		if err == nil {
			if err := vc.writeJSON(snapshot); err != nil {
				return
			}
		}
		select {
		case <-vc.closeCh:
			return
		case <-changed:
		}
	}
}

func (vc *voiceConn) beginTurn(msg voiceMsg) error {
	if vc.flowID == "" {
		return nil
	}
	previous, err := vc.fenceTurn(msg)
	if err != nil {
		return err
	}
	vc.notifySkippedSpeech(previous.speech)
	vc.cancelPreviousSpeech(previous.speech)
	return vc.interruptPreviousTurn(previous, msg.Source, msg.SourceSequence)
}

type voiceTurnFence struct {
	source         string
	sourceSequence uint64
	eventType      string
	speech         map[string]pendingVoiceSpeech
}

func (vc *voiceConn) fenceTurn(msg voiceMsg) (voiceTurnFence, error) {
	if vc.flowID == "" {
		return voiceTurnFence{}, nil
	}
	var err error
	msg, err = vc.normalizeFlowTurn(msg)
	if err != nil {
		return voiceTurnFence{}, err
	}
	eventType := msg.EventType
	source := msg.Source
	turnID := msg.TurnID

	// Serialize the generation change with outbound TTS frames. Once this
	// returns, no PCM frame from a previous turn can be written to the browser.
	vc.writeMu.Lock()
	vc.stateMu.Lock()
	oldSource := vc.currentSource
	oldSourceSequence := vc.turnSourceSeq
	oldEventType := vc.currentEventType
	oldSpeech := vc.pendingSpeech
	vc.pendingSpeech = make(map[string]pendingVoiceSpeech)
	vc.turnGeneration++
	if vc.turnGeneration == 0 {
		vc.turnGeneration = 1
	}
	vc.currentTurnID = turnID
	vc.turnSourceSeq = msg.SourceSequence
	vc.currentSourceSeq = msg.SourceSequence
	vc.currentSource = source
	vc.currentEventType = vc.eventType
	if outputField := strings.TrimSpace(msg.OutputField); outputField != "" {
		vc.outputField = outputField
	}
	vc.ttsModel = strings.TrimSpace(msg.TTSModel)
	if eventType != "" {
		vc.currentEventType = eventType
	}
	vc.turnTranscript.Reset()
	for _, speech := range oldSpeech {
		if speech.sequence > vc.voiceAfterSeq {
			vc.voiceAfterSeq = speech.sequence
		}
	}
	vc.stateMu.Unlock()
	vc.writeMu.Unlock()
	return voiceTurnFence{
		source: oldSource, sourceSequence: oldSourceSequence,
		eventType: oldEventType, speech: oldSpeech,
	}, nil
}

func (vc *voiceConn) notifySkippedSpeech(speech map[string]pendingVoiceSpeech) {
	for id, item := range speech {
		_ = vc.writeText(voiceMsg{
			Type: "voice_output_skipped", ID: id, Sequence: item.sequence,
			TurnID: item.turnID, Source: item.source, SourceSequence: item.sourceSequence,
		})
	}
}

func (vc *voiceConn) cancelPreviousSpeech(speech map[string]pendingVoiceSpeech) {
	for id := range speech {
		if vc.ve != nil {
			_ = vc.ve.CancelTTS(id)
		}
	}
}

func (vc *voiceConn) interruptPreviousTurn(previous voiceTurnFence, source string, newSourceSequence uint64) error {
	interrupt := vc.interruptTurn
	if interrupt == nil && vc.service != nil {
		interrupt = vc.service.InterruptFlowSessionTurn
	}
	if interrupt != nil &&
		previous.source == source &&
		previous.sourceSequence > 0 &&
		newSourceSequence > previous.sourceSequence {
		if _, err := interrupt(
			vc.flowID,
			vc.flowSessionID,
			previous.source,
			previous.eventType,
			newSourceSequence,
		); err != nil {
			return err
		}
	}
	return nil
}

func (vc *voiceConn) prepareFlowVoice(msg voiceMsg) error {
	if vc.flowID == "" {
		return nil
	}
	var err error
	msg, err = vc.normalizeFlowTurn(msg)
	if err != nil {
		return err
	}
	// start configures the media stream only. Agent work and TTS are fenced
	// when server-side VAD reports speech_started, not when the user merely
	// enables the microphone.
	vc.stateMu.Lock()
	vc.flowVoiceSource = msg.Source
	vc.flowStartSeq = msg.SourceSequence
	vc.flowNextSourceSeq = msg.SourceSequence
	vc.flowSpeechSeen = false
	vc.flowSpeechActive = false
	vc.flowEventType = msg.EventType
	vc.flowOutputField = strings.TrimSpace(msg.OutputField)
	vc.flowTTSModel = strings.TrimSpace(msg.TTSModel)
	vc.stateMu.Unlock()
	return nil
}

func (vc *voiceConn) normalizeFlowTurn(msg voiceMsg) (voiceMsg, error) {
	eventType := strings.TrimSpace(msg.EventType)
	if eventType != "" && len(eventType) > 128 {
		return msg, fmt.Errorf("event_type must not exceed 128 characters")
	}
	if msg.SourceSequence == 0 {
		return msg, fmt.Errorf("flow-bound voice start requires a stable source_sequence for reconnect-safe retries")
	}
	source := strings.TrimSpace(msg.Source)
	if source == "" {
		source = "voice-adapter"
	}
	if len(source) > 128 || source == "godex" {
		return msg, fmt.Errorf("source must be between 1 and 128 characters and must not be reserved")
	}
	turnID := strings.TrimSpace(msg.TurnID)
	if turnID == "" {
		turnID = fmt.Sprintf("voice-turn:%d", msg.SourceSequence)
	}
	if len(turnID) > 128 {
		return msg, fmt.Errorf("turn_id must not exceed 128 characters")
	}
	msg.Source = source
	msg.EventType = eventType
	msg.TurnID = turnID
	return msg, nil
}

type flowVoiceOutput struct {
	OutputID       string         `json:"output_id"`
	InputSource    string         `json:"input_source"`
	InputSourceSeq uint64         `json:"input_source_sequence"`
	NodeID         string         `json:"node_id"`
	Outputs        map[string]any `json:"outputs"`
}

func (b *voiceBridge) speakFlowSessionOutput(vc *voiceConn, event agent.FlowSessionEvent) {
	if event.Type != agent.FlowSessionOutputEventType {
		return
	}
	var output flowVoiceOutput
	if err := json.Unmarshal(event.Payload, &output); err != nil {
		vc.writeText(voiceMsg{Type: "error", Code: "voice_output_invalid", Text: err.Error()})
		return
	}
	vc.stateMu.Lock()
	field := vc.outputField
	vc.stateMu.Unlock()
	text, err := selectVoiceOutputText(output.Outputs, field)
	if err != nil {
		vc.writeText(voiceMsg{Type: "error", Code: "voice_output_field", Text: err.Error()})
		return
	}
	outputID := strings.TrimSpace(output.OutputID)
	if outputID == "" {
		outputID = fmt.Sprintf("flow-output-%d", event.Sequence)
	}

	vc.stateMu.Lock()
	if event.Sequence <= vc.voiceAfterSeq {
		vc.stateMu.Unlock()
		return
	}
	turnID := voiceTurnID(output.InputSource, output.InputSourceSeq)
	if vc.currentTurnID != "" && output.InputSource == vc.currentSource {
		if output.InputSourceSeq < vc.turnSourceSeq {
			vc.stateMu.Unlock()
			vc.skipFlowVoiceOutput(outputID, event.Sequence, turnID, output.InputSource, output.InputSourceSeq)
			return
		}
		turnID = vc.currentTurnID
	}
	if vc.pendingSpeech == nil {
		vc.pendingSpeech = make(map[string]pendingVoiceSpeech)
	}
	speech := pendingVoiceSpeech{
		sequence: event.Sequence, generation: vc.turnGeneration, turnID: turnID,
		source: output.InputSource, sourceSequence: output.InputSourceSeq,
	}
	vc.pendingSpeech[outputID] = speech
	model := vc.ttsModel
	vc.stateMu.Unlock()

	if !vc.writeSpeechMessage(outputID, voiceMsg{
		Type: "assistant_text", ID: outputID, OutputID: outputID,
		NodeID: output.NodeID, Text: text, Sequence: event.Sequence,
		Source: output.InputSource, SourceSequence: output.InputSourceSeq, TurnID: turnID,
	}) {
		return
	}
	if vc.ve == nil {
		vc.discardPendingSpeech(outputID, speech)
		vc.writeText(voiceMsg{Type: "error", Code: "tts_error", Text: "voice engine unavailable"})
		return
	}
	if err := vc.ve.Synthesize(outputID, text, model); err != nil {
		vc.discardPendingSpeech(outputID, speech)
		vc.writeText(voiceMsg{Type: "error", Code: "tts_error", Text: err.Error()})
		return
	}
}

func voiceTurnID(source string, sequence uint64) string {
	if strings.TrimSpace(source) == "" || sequence == 0 {
		return ""
	}
	return "voice:" + strings.TrimSpace(source) + ":" + strconv.FormatUint(sequence, 10)
}

func selectVoiceOutputText(outputs map[string]any, field string) (string, error) {
	if field != "" {
		value, ok := outputs[field]
		if !ok {
			return "", fmt.Errorf("voice output field %q was not found", field)
		}
		text, ok := value.(string)
		if !ok || strings.TrimSpace(text) == "" {
			return "", fmt.Errorf("voice output field %q must be a non-empty string", field)
		}
		return strings.TrimSpace(text), nil
	}
	for _, candidate := range []string{"speech", "text", "response", "reply", "assistant_text", "content", "utterance"} {
		if text, ok := outputs[candidate].(string); ok && strings.TrimSpace(text) != "" {
			return strings.TrimSpace(text), nil
		}
	}
	var only string
	count := 0
	for _, value := range outputs {
		text, ok := value.(string)
		if !ok || strings.TrimSpace(text) == "" {
			continue
		}
		only = strings.TrimSpace(text)
		count++
	}
	if count == 1 {
		return only, nil
	}
	if count == 0 {
		return "", fmt.Errorf("FlowSession output has no non-empty string field to speak")
	}
	return "", fmt.Errorf("FlowSession output has multiple string fields; configure output_field")
}

func (vc *voiceConn) writeSpeechMessage(id string, msg voiceMsg) bool {
	vc.writeMu.Lock()
	defer vc.writeMu.Unlock()
	vc.stateMu.Lock()
	speech, ok := vc.pendingSpeech[id]
	if !ok || speech.generation != vc.turnGeneration {
		vc.stateMu.Unlock()
		return false
	}
	msg.TurnID = speech.turnID
	msg.Source = speech.source
	msg.SourceSequence = speech.sourceSequence
	vc.stateMu.Unlock()
	return vc.writeJSONLocked(mustJSON(msg)) == nil
}

func (vc *voiceConn) writeSpeechPCM(id string, pcm []byte) bool {
	vc.writeMu.Lock()
	defer vc.writeMu.Unlock()
	vc.stateMu.Lock()
	speech, ok := vc.pendingSpeech[id]
	valid := ok && speech.generation == vc.turnGeneration
	vc.stateMu.Unlock()
	if !valid {
		return false
	}
	_ = vc.ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return vc.ws.WriteMessage(websocket.BinaryMessage, pcm) == nil
}

func (vc *voiceConn) completeSpeech(id string) bool {
	vc.writeMu.Lock()
	defer vc.writeMu.Unlock()
	vc.stateMu.Lock()
	speech, ok := vc.pendingSpeech[id]
	if !ok || speech.generation != vc.turnGeneration {
		vc.stateMu.Unlock()
		return false
	}
	delete(vc.pendingSpeech, id)
	if speech.sequence > vc.voiceAfterSeq {
		vc.voiceAfterSeq = speech.sequence
	}
	vc.stateMu.Unlock()
	_ = vc.writeJSONLocked(mustJSON(voiceMsg{
		Type: string(voiceclient.EventTTSDone), ID: id,
		TurnID: speech.turnID, Source: speech.source, SourceSequence: speech.sourceSequence,
	}))
	_ = vc.writeJSONLocked(mustJSON(voiceMsg{
		Type: "voice_output_done", ID: id, Sequence: speech.sequence,
		TurnID: speech.turnID, Source: speech.source, SourceSequence: speech.sourceSequence,
	}))
	return true
}

func (vc *voiceConn) discardPendingSpeech(id string, expected pendingVoiceSpeech) {
	vc.stateMu.Lock()
	if current, ok := vc.pendingSpeech[id]; ok && current == expected {
		delete(vc.pendingSpeech, id)
	}
	vc.stateMu.Unlock()
}

func (vc *voiceConn) skipPendingSpeech(id string) {
	vc.writeMu.Lock()
	vc.stateMu.Lock()
	speech, ok := vc.pendingSpeech[id]
	if ok {
		delete(vc.pendingSpeech, id)
		if speech.sequence > vc.voiceAfterSeq {
			vc.voiceAfterSeq = speech.sequence
		}
	}
	vc.stateMu.Unlock()
	if ok {
		_ = vc.writeJSONLocked(mustJSON(voiceMsg{
			Type: "voice_output_skipped", ID: id, Sequence: speech.sequence,
			TurnID: speech.turnID, Source: speech.source, SourceSequence: speech.sourceSequence,
		}))
	}
	vc.writeMu.Unlock()
}

func (vc *voiceConn) skipFlowVoiceOutput(id string, sequence uint64, turnID, source string, sourceSequence uint64) {
	vc.writeMu.Lock()
	vc.stateMu.Lock()
	delete(vc.pendingSpeech, id)
	if sequence <= vc.voiceAfterSeq {
		vc.stateMu.Unlock()
		vc.writeMu.Unlock()
		return
	}
	vc.voiceAfterSeq = sequence
	vc.stateMu.Unlock()
	_ = vc.writeJSONLocked(mustJSON(voiceMsg{
		Type: "voice_output_skipped", ID: id, Sequence: sequence,
		TurnID: turnID, Source: source, SourceSequence: sourceSequence,
	}))
	vc.writeMu.Unlock()
}

func (vc *voiceConn) recordASRFinal(text string) {
	if vc.flowID == "" {
		return
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	vc.stateMu.Lock()
	defer vc.stateMu.Unlock()
	if vc.turnTranscript.Len() > 0 {
		vc.turnTranscript.WriteByte(' ')
	}
	vc.turnTranscript.WriteString(text)
}

func (b *voiceBridge) persistFlowSessionTranscript(vc *voiceConn) error {
	vc.stateMu.Lock()
	text := strings.TrimSpace(vc.turnTranscript.String())
	sequence := vc.currentSourceSeq
	source := vc.currentSource
	eventType := vc.currentEventType
	vc.stateMu.Unlock()
	if text == "" {
		return nil
	}
	if sequence == 0 {
		return fmt.Errorf("voice turn is missing source_sequence; send it with the start message")
	}
	if eventType == "" {
		eventType = "voice.asr_final"
	}
	if source == "" {
		source = "voice-adapter"
	}
	payload, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return err
	}
	input := agent.FlowSessionEventInput{
		Source:         source,
		SourceSequence: sequence,
		Type:           eventType,
		CorrelationID:  "voice:" + source + ":" + strconv.FormatUint(sequence, 10),
		Payload:        payload,
	}
	var receipt agent.FlowSessionEventReceipt
	for attempt := 0; attempt < 3; attempt++ {
		receipt, err = b.service.AppendFlowSessionEvent(vc.flowID, vc.flowSessionID, input)
		if err == nil {
			break
		}
		if attempt < 2 {
			time.Sleep(time.Duration(attempt+1) * 40 * time.Millisecond)
		}
	}
	if err != nil {
		return err
	}
	vc.stateMu.Lock()
	if vc.currentSource == source &&
		vc.currentSourceSeq == sequence &&
		strings.TrimSpace(vc.turnTranscript.String()) == text {
		vc.turnTranscript.Reset()
	}
	vc.stateMu.Unlock()
	vc.writeText(voiceMsg{
		Type: "flow_event_accepted", EventType: eventType,
		Source: source, SourceSequence: sequence, ID: strconv.FormatUint(receipt.Sequence, 10),
	})
	return nil
}

func (vc *voiceConn) flowSessionReplayCursor() (uint64, int64) {
	vc.stateMu.Lock()
	defer vc.stateMu.Unlock()
	return vc.afterSequence, vc.eventOffset
}

func (vc *voiceConn) setFlowSessionReplayCursor(sequence uint64, offset int64) {
	vc.stateMu.Lock()
	defer vc.stateMu.Unlock()
	if sequence > vc.afterSequence {
		vc.afterSequence = sequence
	}
	if offset > vc.eventOffset {
		vc.eventOffset = offset
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
			if err := vc.prepareFlowVoice(msg); err != nil {
				vc.writeText(voiceMsg{Type: "error", Code: "bad_message", Text: err.Error()})
				return
			}
			if err := vc.ve.Start(msg); err != nil {
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

func (vc *voiceConn) writeText(msg voiceMsg) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return vc.writeJSON(data)
}

func (vc *voiceConn) writeJSON(data []byte) error {
	vc.writeMu.Lock()
	defer vc.writeMu.Unlock()
	return vc.writeJSONLocked(data)
}

func (vc *voiceConn) writeJSONLocked(data []byte) error {
	_ = vc.ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return vc.ws.WriteMessage(websocket.TextMessage, data)
}

func (vc *voiceConn) writeBinary(pcm []byte) {
	vc.writeMu.Lock()
	defer vc.writeMu.Unlock()
	_ = vc.ws.WriteMessage(websocket.BinaryMessage, pcm)
}

// voiceMsg 是 Web UI ↔ godex 的 JSON 控制消息。
// 字段与 voice-engine protocol.Message 对齐，加 assistant_text 透传 agent 回复。
type voiceMsg struct {
	Type           string   `json:"type"`
	Code           string   `json:"code,omitempty"`
	Text           string   `json:"text,omitempty"`
	ID             string   `json:"id,omitempty"`
	TurnID         string   `json:"turn_id,omitempty"`
	OutputID       string   `json:"output_id,omitempty"`
	NodeID         string   `json:"node_id,omitempty"`
	Sequence       uint64   `json:"sequence,omitempty"`
	EventType      string   `json:"event_type,omitempty"`
	Source         string   `json:"source,omitempty"`
	SourceSequence uint64   `json:"source_sequence,omitempty"`
	AfterSequence  uint64   `json:"after_sequence,omitempty"`
	OutputField    string   `json:"output_field,omitempty"`
	ASRModel       string   `json:"asr_model,omitempty"`
	TTSModel       string   `json:"tts_model,omitempty"`
	VADModel       string   `json:"vad_model,omitempty"`
	VAD            string   `json:"vad,omitempty"`
	Models         []string `json:"models,omitempty"`
	ASRModels      []string `json:"asr_models,omitempty"`
	VADModels      []string `json:"vad_models,omitempty"`
	TTSModels      []string `json:"tts_models,omitempty"`
	DefaultASR     string   `json:"default_asr,omitempty"`
	DefaultVAD     string   `json:"default_vad,omitempty"`
	DefaultTTS     string   `json:"default_tts,omitempty"`
}

// writeVoiceError 向尚未升级的 HTTP 响应写错误（升级前失败用）。
func writeVoiceError(conn *websocket.Conn, code, text string) {
	_ = conn.WriteMessage(websocket.TextMessage, mustJSON(voiceMsg{Type: "error", Code: code, Text: text}))
}

func mustJSON(v any) []byte {
	data, _ := json.Marshal(v)
	return data
}
