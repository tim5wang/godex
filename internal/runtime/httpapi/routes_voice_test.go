package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/tim5wang/godex/internal/agent"
	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/flow"
	"github.com/tim5wang/godex/internal/domain/message"
	"github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/services/commands"

	voiceclient "github.com/tim5wang/agent-local-voice-engine/client"
	"github.com/tim5wang/agent-local-voice-engine/protocol"
)

// TestVoiceMsgJSON 验证 voiceMsg 控制消息编解码（Web UI ↔ godex 契约）。
func TestVoiceMsgJSON(t *testing.T) {
	in := voiceMsg{Type: string(protocol.KindASRFinal), Text: "你好"}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out voiceMsg
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Type != string(protocol.KindASRFinal) || out.Text != "你好" {
		t.Errorf("round trip mismatch: %+v", out)
	}
}

// TestSourceVoiceEnvelope 验证 SourceVoice 信封源常量可用。
func TestSourceVoiceEnvelope(t *testing.T) {
	env := message.NewRuntimeEnvelope(message.SourceVoice, "s1", "voice", "hello", time.Now(), nil)
	if env.Source != message.SourceVoice {
		t.Errorf("source = %q, want voice", env.Source)
	}
}

// TestNewVoiceBridgeDefaultAddr 验证默认引擎地址为协议默认值。
func TestNewVoiceBridgeDefaultAddr(t *testing.T) {
	t.Setenv("GODEX_VOICE_ENGINE_ADDR", "")
	b := newVoiceBridge(nil, nil)
	if b.engineAddr != protocol.DefaultAddr {
		t.Errorf("engine addr = %q, want %q", b.engineAddr, protocol.DefaultAddr)
	}
}

// TestVoiceBridgeEngineUnreachable 验证 voice-engine 不可达时
// WebSocket 升级后收到 engine_unreachable 错误帧。
// 注意：handleVoice 在 Dial 失败时立即返回，不需要真实 backend service。
func TestVoiceBridgeEngineUnreachable(t *testing.T) {
	b := &voiceBridge{service: nil, engineAddr: "ws://127.0.0.1:1/ws"}
	srv := httptest.NewServer(http.HandlerFunc(b.handleVoice))
	defer srv.Close()

	url := "ws" + strings.TrimPrefix(srv.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var msg voiceMsg
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatalf("unmarshal %s: %v", data, err)
	}
	if msg.Type != "error" || msg.Code != "engine_unreachable" {
		t.Errorf("expected engine_unreachable, got %+v", msg)
	}
}

func newTestWebsocketPair(t *testing.T) (*websocket.Conn, *websocket.Conn) {
	t.Helper()
	accepted := make(chan *websocket.Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		accepted <- conn
	}))
	client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		srv.Close()
		t.Fatalf("dial websocket pair: %v", err)
	}
	server := <-accepted
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
		srv.Close()
	})
	return client, server
}

func TestVoiceFlowSessionAdapterKeepsPCMVolatileAndReplaysASRSemantics(t *testing.T) {
	ve := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		engineConn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer engineConn.Close()
		_, data, err := engineConn.ReadMessage()
		if err != nil {
			return
		}
		var hello protocol.Message
		if json.Unmarshal(data, &hello) != nil || hello.T != protocol.KindHello {
			return
		}
		_ = engineConn.WriteJSON(protocol.Message{T: protocol.KindReady, ProtocolVersion: protocol.ProtocolVersion})
		_, data, err = engineConn.ReadMessage()
		if err != nil {
			return
		}
		var start protocol.Message
		if json.Unmarshal(data, &start) != nil || start.T != protocol.KindStart {
			return
		}
		messageType, _, err := engineConn.ReadMessage()
		if err != nil || messageType != websocket.BinaryMessage {
			return
		}
		emitSegment := func(text string) {
			_ = engineConn.WriteJSON(protocol.Message{T: protocol.MessageKind("speech_started")})
			_ = engineConn.WriteJSON(protocol.Message{T: protocol.KindASRFinal, Text: text})
			_ = engineConn.WriteJSON(protocol.Message{T: protocol.MessageKind("asr_utterance_end")})
		}
		emitSegment("识别内容")
		secondSegmentSent := false
		for {
			messageType, data, err := engineConn.ReadMessage()
			if err != nil {
				return
			}
			if messageType == websocket.BinaryMessage {
				if !secondSegmentSent {
					secondSegmentSent = true
					emitSegment("第二段")
				}
				continue
			}
			var request protocol.Message
			if json.Unmarshal(data, &request) != nil {
				continue
			}
			if request.T == protocol.KindStop {
				return
			}
			if request.T == protocol.KindAudioEnd {
				_ = engineConn.WriteJSON(protocol.Message{T: protocol.KindASREnd})
				continue
			}
			if request.T == protocol.KindTTS {
				_ = engineConn.WriteJSON(protocol.Message{T: protocol.KindTTSStart, ID: request.ID, SampleRate: 24000})
				_ = engineConn.WriteMessage(websocket.BinaryMessage, make([]byte, 240))
				_ = engineConn.WriteJSON(protocol.Message{T: protocol.KindTTSDone, ID: request.ID})
			}
		}
	}))
	defer ve.Close()

	cfg := newTestConfig(t)
	service := backend.NewService(cfg, agent.NewSharedDependencies(cfg), commands.NewService(cfg))
	serviceCtx, stopService := context.WithCancel(context.Background())
	if err := service.Start(serviceCtx); err != nil {
		stopService()
		t.Fatalf("start FlowSession runtime: %v", err)
	}
	defer func() {
		stopService()
		stopCtx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		if err := service.Stop(stopCtx); err != nil {
			t.Errorf("stop FlowSession runtime: %v", err)
		}
	}()
	def := &flow.Definition{
		FlowID:        "fl_voice_adapter",
		Version:       "1",
		Status:        "draft",
		ExecutionMode: flow.ExecutionModeSession,
		SessionWorkflow: &flow.SessionWorkflowSpec{Triggers: []flow.SessionTrigger{
			{EventType: "voice.asr_final", EntryNode: "capture"},
		}},
		Nodes: []flow.Node{{
			ID: "capture", Kind: flow.KindFunction,
			Function: &flow.FunctionSpec{
				Runtime: flow.FunctionRuntimeJS,
				Source: `function handle(ctx, event) {
					return {
						speech: "Voice reply: " + event.payload.text,
						session_state: {recognized: event.payload.text}
					};
				}`,
			},
			Outputs: []flow.VarDef{
				{Name: "speech", Type: "string"},
				{Name: "session_state", Type: "object"},
			},
		}},
	}
	if _, err := service.CreateFlow(agent.FlowCreateArgs{Def: def}); err != nil {
		t.Fatalf("create voice session flow: %v", err)
	}
	session, err := service.CreateFlowSession(def.FlowID, def.Version, nil)
	if err != nil {
		t.Fatalf("create voice session: %v", err)
	}

	bridge := &voiceBridge{
		service:    service,
		engineAddr: strings.TrimPrefix(ve.URL, "http://"),
	}
	server := httptest.NewServer(http.HandlerFunc(bridge.handleVoice))
	defer server.Close()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") +
		"/?flow_id=" + def.FlowID +
		"&flow_session_id=" + session.SessionID +
		"&after_sequence=0"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial voice flow session: %v", err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, data, err := conn.ReadMessage(); err != nil {
		t.Fatalf("read resume marker: %v", err)
	} else {
		var resumed voiceMsg
		if json.Unmarshal(data, &resumed) != nil || resumed.Type != "resumed" || resumed.AfterSequence != 0 {
			t.Fatalf("unexpected resume marker: %s", data)
		}
	}
	const turnID = "turn-flow-voice-1"
	if err := conn.WriteJSON(voiceMsg{
		Type: string(protocol.KindStart), TurnID: turnID, Source: "voice-client-a", SourceSequence: 7,
	}); err != nil {
		t.Fatalf("send start: %v", err)
	}
	if err := conn.WriteMessage(websocket.BinaryMessage, []byte{1, 2, 3, 4}); err != nil {
		t.Fatalf("send PCM: %v", err)
	}

	var sessionEvents []map[string]any
	var spokenTexts []string
	spokenTurnIDs := make(map[string]string)
	pcmCount := 0
	gotFirstResponse := false
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && (!gotFirstResponse) {
		_ = conn.SetReadDeadline(deadline)
		messageType, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read voice adapter output: %v", err)
		}
		if messageType == websocket.BinaryMessage {
			if len(data) > 0 {
				pcmCount++
			}
			continue
		}
		var message map[string]any
		if err := json.Unmarshal(data, &message); err != nil {
			t.Fatalf("decode voice adapter output %s: %v", data, err)
		}
		switch message["type"] {
		case "session_event":
			event, _ := message["event"].(map[string]any)
			if event["type"] == "voice.asr_final" {
				sessionEvents = append(sessionEvents, event)
			}
		case "assistant_text":
			if text, _ := message["text"].(string); text != "" {
				spokenTexts = append(spokenTexts, text)
				spokenTurnIDs[text], _ = message["turn_id"].(string)
			}
		case string(protocol.KindTTSDone):
			gotFirstResponse = true
		}
	}
	if !gotFirstResponse {
		t.Fatalf("first Voice Agent response timed out: events=%d texts=%q", len(sessionEvents), spokenTexts)
	}
	if err := conn.WriteMessage(websocket.BinaryMessage, []byte{5, 6, 7, 8}); err != nil {
		t.Fatalf("send second utterance PCM: %v", err)
	}
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && (len(sessionEvents) < 2 || len(spokenTexts) < 2 || pcmCount < 2) {
		_ = conn.SetReadDeadline(deadline)
		messageType, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read second voice adapter output: %v", err)
		}
		if messageType == websocket.BinaryMessage {
			if len(data) > 0 {
				pcmCount++
			}
			continue
		}
		var message map[string]any
		if err := json.Unmarshal(data, &message); err != nil {
			t.Fatalf("decode second voice adapter output %s: %v", data, err)
		}
		switch message["type"] {
		case "session_event":
			event, _ := message["event"].(map[string]any)
			if event["type"] == "voice.asr_final" {
				sessionEvents = append(sessionEvents, event)
			}
		case "assistant_text":
			if text, _ := message["text"].(string); text != "" {
				spokenTexts = append(spokenTexts, text)
				spokenTurnIDs[text], _ = message["turn_id"].(string)
			}
		}
	}
	if len(sessionEvents) != 2 {
		t.Fatalf("got %d FlowSession ASR events before audio_end, want 2: %+v", len(sessionEvents), sessionEvents)
	}
	for i, expected := range []struct {
		sequence float64
		text     string
	}{{7, "识别内容"}, {8, "第二段"}} {
		event := sessionEvents[i]
		payload, ok := event["payload"].(map[string]any)
		if event["type"] != "voice.asr_final" ||
			event["source"] != "voice-client-a" ||
			event["source_sequence"] != expected.sequence ||
			!ok || payload["text"] != expected.text {
			t.Fatalf("unexpected ASR semantic event %d: %+v", i+1, event)
		}
	}
	if err := conn.WriteJSON(voiceMsg{Type: string(protocol.KindAudioEnd)}); err != nil {
		t.Fatalf("send audio end after FlowSession output: %v", err)
	}
	if len(spokenTexts) != 2 || pcmCount < 2 {
		t.Fatalf("FlowSession did not run before audio_end: texts=%q pcm_count=%d", spokenTexts, pcmCount)
	}
	spokenSet := map[string]bool{}
	for _, text := range spokenTexts {
		spokenSet[text] = true
	}
	if !spokenSet["Voice reply: 识别内容"] || !spokenSet["Voice reply: 第二段"] {
		t.Fatalf("unexpected spoken outputs before audio_end: %q", spokenTexts)
	}
	if spokenTurnIDs["Voice reply: 识别内容"] != "voice:voice-client-a:7" ||
		spokenTurnIDs["Voice reply: 第二段"] != "voice:voice-client-a:8" {
		t.Fatalf("each utterance should have its own fenced turn ID, got %+v", spokenTurnIDs)
	}
	gotASREnd := false
	for !gotASREnd {
		_, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read asr_end after audio_end: %v", err)
		}
		var message map[string]any
		if err := json.Unmarshal(data, &message); err == nil && message["type"] == "asr_end" {
			gotASREnd = true
		}
	}
	events, err := service.FlowSessionEvents(def.FlowID, session.SessionID, 0, 10)
	if err != nil {
		t.Fatalf("read persisted semantic events: %v", err)
	}
	inputCount, outputCount := 0, 0
	for _, event := range events {
		switch event.Type {
		case "voice.asr_final":
			inputCount++
		case agent.FlowSessionOutputEventType:
			outputCount++
		}
	}
	if inputCount != 2 || outputCount != 2 {
		t.Fatalf("expected two ASR inputs and two Flow outputs without audio_end duplication, got %+v", events)
	}
	inputSequence := func(sourceSequence uint64) uint64 {
		for _, event := range events {
			if event.Type == "voice.asr_final" &&
				event.Source == "voice-client-a" &&
				event.SourceSequence == sourceSequence {
				return event.Sequence
			}
		}
		return 0
	}
	firstInputSequence := inputSequence(7)
	if firstInputSequence == 0 {
		t.Fatalf("could not find the first accepted utterance in persisted events: %+v", events)
	}
	secondInputSequence := inputSequence(8)
	if secondInputSequence == 0 {
		t.Fatalf("could not find the second accepted utterance in persisted events: %+v", events)
	}
	duplicate, err := service.AppendFlowSessionEvent(def.FlowID, session.SessionID, agent.FlowSessionEventInput{
		Source: "voice-client-a", SourceSequence: 8, Type: "voice.asr_final",
		CorrelationID: "voice:voice-client-a:8",
		Payload:       json.RawMessage(`{"text":"第二段"}`),
	})
	if err != nil {
		t.Fatalf("replay accepted utterance after reconnect: %v", err)
	}
	if !duplicate.Duplicate || duplicate.Sequence != secondInputSequence {
		t.Fatalf("reconnect replay should return the latest original accepted event: receipt=%+v second_sequence=%d", duplicate, secondInputSequence)
	}
	_, err = service.AppendFlowSessionEvent(def.FlowID, session.SessionID, agent.FlowSessionEventInput{
		Source: "voice-client-a", SourceSequence: 7, Type: "voice.asr_final",
		CorrelationID: "voice:voice-client-a:7",
		Payload:       json.RawMessage(`{"text":"识别内容"}`),
	})
	if !errors.Is(err, agent.ErrFlowSessionConflict) {
		t.Fatalf("replay of an older utterance should be rejected without allocating another event, got %v", err)
	}
	events, err = service.FlowSessionEvents(def.FlowID, session.SessionID, 0, 10)
	if err != nil {
		t.Fatalf("re-read events after reconnect replay: %v", err)
	}
	inputCount = 0
	for _, event := range events {
		if event.Type == "voice.asr_final" {
			inputCount++
		}
	}
	if inputCount != 2 {
		t.Fatalf("replayed accepted utterance should remain a single input event, got %+v", events)
	}
}

func TestVoiceFlowSessionStartRequiresStableReconnectKey(t *testing.T) {
	vc := &voiceConn{flowID: "flow-session"}
	if err := vc.beginTurn(voiceMsg{Type: string(protocol.KindStart)}); err == nil {
		t.Fatal("flow-bound voice start without source_sequence should fail")
	}
	if err := vc.beginTurn(voiceMsg{
		Type: string(protocol.KindStart), Source: "device-1", SourceSequence: 9,
	}); err != nil {
		t.Fatalf("start with stable reconnect key: %v", err)
	}
	if vc.currentSource != "device-1" || vc.currentSourceSeq != 9 {
		t.Fatalf("unexpected stable reconnect key: source=%q sequence=%d", vc.currentSource, vc.currentSourceSeq)
	}
	if vc.currentTurnID != "voice-turn:9" || vc.turnGeneration != 1 || vc.turnSourceSeq != 9 {
		t.Fatalf("unexpected turn fence: turn_id=%q generation=%d source_sequence=%d", vc.currentTurnID, vc.turnGeneration, vc.turnSourceSeq)
	}
}

func TestVoiceDetectedUtteranceReservesSequenceBeforeASRCompletes(t *testing.T) {
	browser, bridge := newTestWebsocketPair(t)
	vc := &voiceConn{
		ws:                bridge,
		flowID:            "flow-session",
		flowSessionID:     "session",
		flowVoiceSource:   "device-1",
		flowEventType:     "voice.asr_final",
		flowStartSeq:      7,
		flowNextSourceSeq: 7,
		pendingSpeech:     make(map[string]pendingVoiceSpeech),
	}
	b := &voiceBridge{}

	b.beginDetectedVoiceTurn(vc)
	_ = browser.SetReadDeadline(time.Now().Add(2 * time.Second))
	var first voiceMsg
	if err := browser.ReadJSON(&first); err != nil {
		t.Fatalf("read first speech_started: %v", err)
	}
	if first.Type != "speech_started" || first.SourceSequence != 7 {
		t.Fatalf("first utterance turn = %+v, want sequence 7", first)
	}

	// Simulate a VAD segment with no final transcript. It must still consume a
	// turn sequence so the next onset gets a distinct turn ID and fence.
	vc.stateMu.Lock()
	vc.flowSpeechActive = false
	vc.stateMu.Unlock()
	b.beginDetectedVoiceTurn(vc)

	_ = browser.SetReadDeadline(time.Now().Add(2 * time.Second))
	var second voiceMsg
	if err := browser.ReadJSON(&second); err != nil {
		t.Fatalf("read second speech_started: %v", err)
	}
	if second.Type != "speech_started" || second.SourceSequence != 8 {
		t.Fatalf("second utterance turn = %+v, want sequence 8", second)
	}
}

func TestVoiceOnsetAcknowledgesAndForwardsASRWhileInterruptIsBlocked(t *testing.T) {
	browser, bridge := newTestWebsocketPair(t)
	interruptStarted := make(chan struct{}, 1)
	releaseInterrupt := make(chan struct{})
	engineEvents := make(chan voiceclient.Event, 2)
	vc := &voiceConn{
		ws:                bridge,
		flowID:            "flow-session",
		flowSessionID:     "session",
		ve:                &voiceEngineConn{events: engineEvents},
		flowVoiceSource:   "device-1",
		flowStartSeq:      2,
		flowNextSourceSeq: 2,
		flowEventType:     "voice.asr_final",
		currentSource:     "device-1",
		currentSourceSeq:  1,
		turnSourceSeq:     1,
		currentEventType:  "voice.asr_final",
		pendingSpeech:     make(map[string]pendingVoiceSpeech),
		interruptTurn: func(string, string, string, string, uint64) (bool, error) {
			interruptStarted <- struct{}{}
			<-releaseInterrupt
			return true, nil
		},
	}
	b := &voiceBridge{}
	pumpDone := make(chan struct{})
	go func() {
		b.pumpEngine(vc)
		close(pumpDone)
	}()
	engineEvents <- voiceclient.Event{Kind: voiceclient.EventKind("speech_started")}
	engineEvents <- voiceclient.Event{Kind: voiceclient.EventASRFinal, Text: "second turn"}

	select {
	case <-interruptStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("durable interrupt did not start")
	}
	_ = browser.SetReadDeadline(time.Now().Add(2 * time.Second))
	gotSpeechStarted, gotASRFinal := false, false
	for !gotSpeechStarted || !gotASRFinal {
		var message voiceMsg
		if err := browser.ReadJSON(&message); err != nil {
			t.Fatalf("read voice event while interrupt is blocked: %v", err)
		}
		switch message.Type {
		case "speech_started":
			gotSpeechStarted = message.SourceSequence == 2
		case "asr_final":
			gotASRFinal = message.Text == "second turn"
		}
	}

	close(releaseInterrupt)
	close(engineEvents)
	select {
	case <-pumpDone:
	case <-time.After(2 * time.Second):
		t.Fatal("voice engine event pump did not stop")
	}
}

func TestVoiceNewTurnInterruptsOnlyOlderDurableWork(t *testing.T) {
	cfg := newTestConfig(t)
	service := backend.NewService(cfg, agent.NewSharedDependencies(cfg), commands.NewService(cfg))
	def := &flow.Definition{
		FlowID:        "fl_voice_turn_interrupt",
		Version:       "1",
		Status:        "draft",
		ExecutionMode: flow.ExecutionModeSession,
		SessionWorkflow: &flow.SessionWorkflowSpec{Triggers: []flow.SessionTrigger{
			{EventType: "voice.asr_final", EntryNode: "capture"},
		}},
		Nodes: []flow.Node{{
			ID: "capture", Kind: flow.KindFunction,
			Function: &flow.FunctionSpec{
				Runtime: flow.FunctionRuntimeJS,
				Source:  `function handle() { return {session_state: {handled: true}}; }`,
			},
			Outputs: []flow.VarDef{{Name: "session_state", Type: "object"}},
		}},
	}
	if _, err := service.CreateFlow(agent.FlowCreateArgs{Def: def}); err != nil {
		t.Fatalf("create voice session flow: %v", err)
	}
	session, err := service.CreateFlowSession(def.FlowID, def.Version, nil)
	if err != nil {
		t.Fatalf("create voice session: %v", err)
	}
	if _, err := service.AppendFlowSessionEvent(def.FlowID, session.SessionID, agent.FlowSessionEventInput{
		Source: "device-1", SourceSequence: 7, Type: "voice.asr_final",
		Payload: json.RawMessage(`{"text":"old turn"}`),
	}); err != nil {
		t.Fatalf("append old turn: %v", err)
	}

	vc := &voiceConn{
		service: service, flowID: def.FlowID, flowSessionID: session.SessionID,
		eventType:     "voice.asr_final",
		currentSource: "device-1", currentSourceSeq: 7, turnSourceSeq: 7,
		currentEventType: "voice.asr_final", pendingSpeech: make(map[string]pendingVoiceSpeech),
		currentTurnID: "turn-7", turnGeneration: 1,
	}
	if err := vc.prepareFlowVoice(voiceMsg{
		Type: string(protocol.KindStart), Source: "device-1", SourceSequence: 8,
	}); err != nil {
		t.Fatalf("prepare voice capture: %v", err)
	}
	view, err := service.GetFlowSession(def.FlowID, session.SessionID)
	if err != nil {
		t.Fatalf("read session after enabling microphone: %v", err)
	}
	if view.ProcessedSequence != 0 || vc.currentTurnID != "turn-7" || vc.turnGeneration != 1 {
		t.Fatalf("enabling the microphone must not interrupt an Agent turn: session=%+v turn=%q generation=%d",
			view, vc.currentTurnID, vc.turnGeneration)
	}
	if err := vc.beginTurn(voiceMsg{
		Type: string(protocol.KindStart), Source: "device-1", SourceSequence: 7,
	}); err != nil {
		t.Fatalf("begin same-sequence retry: %v", err)
	}
	view, err = service.GetFlowSession(def.FlowID, session.SessionID)
	if err != nil {
		t.Fatalf("read session after same-sequence retry: %v", err)
	}
	if view.ProcessedSequence != 0 {
		t.Fatalf("same-sequence retry must not cancel durable work: %+v", view)
	}

	if err := vc.beginTurn(voiceMsg{
		Type: string(protocol.KindStart), Source: "device-1", SourceSequence: 8,
	}); err != nil {
		t.Fatalf("begin newer turn: %v", err)
	}
	view, err = service.GetFlowSession(def.FlowID, session.SessionID)
	if err != nil {
		t.Fatalf("read session after newer turn: %v", err)
	}
	if view.ProcessedSequence != 1 || view.LastExecution == nil || view.LastExecution.Status != "canceled" ||
		view.State["handled"] != nil {
		t.Fatalf("newer voice turn did not skip old durable work cleanly: %+v", view)
	}
}

func TestVoiceFlowNewTurnCancelsPendingSpeechAndAdvancesReplayCursor(t *testing.T) {
	browser, bridge := newTestWebsocketPair(t)
	engine, engineServer := newTestWebsocketPair(t)
	vc := &voiceConn{
		ws:             bridge,
		ve:             &voiceEngineConn{ws: engine},
		flowID:         "flow-session",
		currentTurnID:  "old-turn",
		currentSource:  "device-1",
		turnGeneration: 1,
		pendingSpeech: map[string]pendingVoiceSpeech{
			"old-output": {
				sequence: 12, generation: 1, turnID: "old-turn",
				source: "device-1", sourceSequence: 9,
			},
		},
	}
	if err := vc.beginTurn(voiceMsg{
		Type: string(protocol.KindStart), TurnID: "new-turn",
		Source: "device-1", SourceSequence: 10,
	}); err != nil {
		t.Fatalf("begin next turn: %v", err)
	}
	if vc.currentTurnID != "new-turn" || vc.turnGeneration != 2 || vc.voiceAfterSeq != 12 {
		t.Fatalf("new turn did not fence old speech: turn=%q generation=%d replay_cursor=%d", vc.currentTurnID, vc.turnGeneration, vc.voiceAfterSeq)
	}
	if len(vc.pendingSpeech) != 0 {
		t.Fatalf("pending speech was not cleared: %+v", vc.pendingSpeech)
	}

	_ = browser.SetReadDeadline(time.Now().Add(2 * time.Second))
	var skipped voiceMsg
	if err := browser.ReadJSON(&skipped); err != nil {
		t.Fatalf("read skipped output notification: %v", err)
	}
	if skipped.Type != "voice_output_skipped" || skipped.ID != "old-output" || skipped.Sequence != 12 {
		t.Fatalf("unexpected skipped output: %+v", skipped)
	}
	_ = engineServer.SetReadDeadline(time.Now().Add(2 * time.Second))
	var cancel struct {
		Type string `json:"t"`
		ID   string `json:"id"`
	}
	if err := engineServer.ReadJSON(&cancel); err != nil {
		t.Fatalf("read TTS cancellation: %v", err)
	}
	if cancel.Type != "tts_cancel" || cancel.ID != "old-output" {
		t.Fatalf("unexpected TTS cancellation: %+v", cancel)
	}
}

func TestVoiceFlowOutputFromOlderTurnIsSkipped(t *testing.T) {
	browser, bridge := newTestWebsocketPair(t)
	payload, err := json.Marshal(flowVoiceOutput{
		OutputID: "old-output", InputSource: "device-1", InputSourceSeq: 9,
		Outputs: map[string]any{"speech": "stale reply"},
	})
	if err != nil {
		t.Fatalf("marshal flow output: %v", err)
	}
	vc := &voiceConn{
		ws:             bridge,
		flowID:         "flow-session",
		currentTurnID:  "new-turn",
		currentSource:  "device-1",
		turnGeneration: 2,
		turnSourceSeq:  10,
		pendingSpeech:  make(map[string]pendingVoiceSpeech),
	}
	(&voiceBridge{}).speakFlowSessionOutput(vc, agent.FlowSessionEvent{
		Sequence: 13, Type: agent.FlowSessionOutputEventType, Payload: payload,
	})

	_ = browser.SetReadDeadline(time.Now().Add(2 * time.Second))
	var skipped voiceMsg
	if err := browser.ReadJSON(&skipped); err != nil {
		t.Fatalf("read stale output notification: %v", err)
	}
	if skipped.Type != "voice_output_skipped" || skipped.ID != "old-output" || skipped.Sequence != 13 {
		t.Fatalf("unexpected stale output notification: %+v", skipped)
	}
	if vc.voiceAfterSeq != 13 || len(vc.pendingSpeech) != 0 {
		t.Fatalf("stale output was not fenced: replay_cursor=%d pending=%+v", vc.voiceAfterSeq, vc.pendingSpeech)
	}
}

// TestVoiceStatusEndpoint 验证 /v1/voice/status 返回启用状态与引擎可达性。
func TestVoiceStatusEndpoint(t *testing.T) {
	// 用环境变量指定不可达端口：handleVoiceStatus 内部会 refreshEngineAddr，
	// 从 manager(nil)→env→默认 依次回退，这里走 env 分支。
	t.Setenv("GODEX_VOICE_ENGINE_ADDR", "127.0.0.1:1")
	b := &voiceBridge{service: nil}
	srv := httptest.NewServer(http.HandlerFunc(b.handleVoiceStatus))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("get status: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var st voiceStatus
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if st.EngineAddr != "127.0.0.1:1" {
		t.Errorf("engine_addr = %q, want 127.0.0.1:1", st.EngineAddr)
	}
	// 默认 manager=nil → voiceEnabled=false
	if st.Enabled {
		t.Error("expected enabled=false with nil manager")
	}
	// 引擎地址不可达 → reachable=false
	if st.Reachable {
		t.Error("expected reachable=false for dead port")
	}
}

func TestVoiceStatusRequiresDefaultVADAfterWebSocketHandshake(t *testing.T) {
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var hello protocol.Message
		if json.Unmarshal(data, &hello) != nil || hello.T != protocol.KindHello {
			return
		}
		_ = conn.WriteJSON(map[string]any{
			"t":          protocol.KindReady,
			"asr_models": []string{"asr/test"}, "default_asr": "asr/test",
			"tts_models": []string{"tts/test"}, "default_tts": "tts/test",
			"default_vad": "vad/missing",
		})
		_, _, _ = conn.ReadMessage()
	}))
	defer engine.Close()
	t.Setenv("GODEX_VOICE_ENGINE_ADDR", strings.TrimPrefix(engine.URL, "http://"))

	bridge := &voiceBridge{}
	statusServer := httptest.NewServer(http.HandlerFunc(bridge.handleVoiceStatus))
	defer statusServer.Close()
	resp, err := http.Get(statusServer.URL)
	if err != nil {
		t.Fatalf("get voice status: %v", err)
	}
	defer resp.Body.Close()
	var status voiceStatus
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatalf("decode voice status: %v", err)
	}
	if !status.Reachable {
		t.Fatalf("successful WebSocket handshake should be reachable: %+v", status)
	}
	if status.Ready {
		t.Fatalf("Voice Agent must not be ready without its server VAD capability: %+v", status)
	}
	if !strings.Contains(status.Error, "default VAD model") {
		t.Fatalf("readiness error should explain the missing VAD model: %+v", status)
	}
}

// TestWithGzipAllowsWebSocketUpgrade 回归：浏览器 WebSocket 握手带
// Accept-Encoding: gzip，withGzip 包装的 writer 不支持 Hijack 会导致升级失败
// （500）。withGzip 必须对 Upgrade 请求原样透传。
func TestWithGzipAllowsWebSocketUpgrade(t *testing.T) {
	srv := httptest.NewServer(withGzip(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer conn.Close()
	})))
	defer srv.Close()

	// 模拟真实浏览器：握手请求带 Accept-Encoding: gzip。
	url := "ws" + strings.TrimPrefix(srv.URL, "http")
	dialer := websocket.Dialer{}
	conn, resp, err := dialer.Dial(url, http.Header{"Accept-Encoding": []string{"gzip"}})
	if err != nil {
		t.Fatalf("dial with gzip accept-encoding: %v", err)
	}
	defer conn.Close()
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("status = %d, want 101", resp.StatusCode)
	}
}

// TestVoiceConnConcurrentWrite 验证写路径线程安全（并发写不 panic、不挂起）。
func TestVoiceConnConcurrentWrite(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		vc := &voiceConn{ws: conn, writeMu: sync.Mutex{}, closeCh: make(chan struct{})}
		done := make(chan struct{})
		go func() {
			for i := 0; i < 50; i++ {
				vc.writeText(voiceMsg{Type: "assistant_text", Text: "x"})
			}
			close(done)
		}()
		for i := 0; i < 50; i++ {
			vc.writeBinary([]byte{0, 1, 2})
		}
		<-done
	}))
	defer srv.Close()

	url := "ws" + strings.TrimPrefix(srv.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			break
		}
	}
}

// TestHandleTTSDisabled 验证语音未启用（manager=nil）时 /v1/tts 返回 404。
func TestHandleTTSDisabled(t *testing.T) {
	b := &voiceBridge{service: nil}
	srv := httptest.NewServer(http.HandlerFunc(b.handleTTS))
	defer srv.Close()

	resp, err := http.Post(srv.URL, "application/json", strings.NewReader(`{"text":"你好"}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

// TestHandleTTSMockEngine 验证语音启用后经 mock voice-engine 返回 WAV 音频。
// mock 引擎：hello→ready 握手，tts 请求 → tts_start(24k)→PCM→tts_done。
func TestHandleTTSMockEngine(t *testing.T) {
	ve := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		// 握手：等 hello → 回 ready
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var hello protocol.Message
		if err := json.Unmarshal(data, &hello); err != nil || hello.T != protocol.KindHello {
			return
		}
		ready, _ := json.Marshal(protocol.Message{T: protocol.KindReady, ProtocolVersion: protocol.ProtocolVersion})
		_ = conn.WriteMessage(websocket.TextMessage, ready)
		// 等 tts 请求
		_, data, err = conn.ReadMessage()
		if err != nil {
			return
		}
		var msg protocol.Message
		if err := json.Unmarshal(data, &msg); err != nil || msg.T != protocol.KindTTS {
			return
		}
		start, _ := json.Marshal(protocol.Message{T: protocol.KindTTSStart, ID: msg.ID, SampleRate: 24000})
		_ = conn.WriteMessage(websocket.TextMessage, start)
		_ = conn.WriteMessage(websocket.BinaryMessage, make([]byte, 240))
		done, _ := json.Marshal(protocol.Message{T: protocol.KindTTSDone, ID: msg.ID})
		_ = conn.WriteMessage(websocket.TextMessage, done)
	}))
	defer ve.Close()
	t.Setenv("GODEX_VOICE_ENGINE_ADDR", strings.TrimPrefix(ve.URL, "http://"))

	// manager：voice_enabled=true
	workspace := t.TempDir()
	mgr := newTestManager(t, &config.Config{WorkspaceDir: workspace, HomeDir: filepath.Join(workspace, "home")})
	if _, err := mgr.Update(context.Background(), config.UpdateRequest{
		Values: map[string]any{"media.audio.voice_enabled": true},
	}); err != nil {
		t.Fatalf("enable voice: %v", err)
	}
	b := &voiceBridge{manager: mgr}
	srv := httptest.NewServer(http.HandlerFunc(b.handleTTS))
	defer srv.Close()

	resp, err := http.Post(srv.URL, "application/json", strings.NewReader(`{"text":"你好"}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body: %s", resp.StatusCode, body)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(body) < 44 || string(body[0:4]) != "RIFF" || string(body[8:12]) != "WAVE" {
		t.Fatalf("not wav, got %d bytes: %q", len(body), body[:min(len(body), 16)])
	}
}

// TestHandleTTSStreamMockEngine 验证流式端点：WS 发文本 → 收到 PCM binary 帧
// （首帧即到，边生成边播）→ tts_done 收尾。
func TestHandleTTSStreamMockEngine(t *testing.T) {
	ve := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		// 握手：等 hello → 回 ready
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var hello protocol.Message
		if err := json.Unmarshal(data, &hello); err != nil || hello.T != protocol.KindHello {
			return
		}
		ready, _ := json.Marshal(protocol.Message{T: protocol.KindReady, ProtocolVersion: protocol.ProtocolVersion})
		_ = conn.WriteMessage(websocket.TextMessage, ready)
		// 等 tts 请求
		_, data, err = conn.ReadMessage()
		if err != nil {
			return
		}
		var msg protocol.Message
		if err := json.Unmarshal(data, &msg); err != nil || msg.T != protocol.KindTTS {
			return
		}
		// 模拟分帧下发：tts_start → 3 段 PCM → tts_done（验证逐帧透传）
		start, _ := json.Marshal(protocol.Message{T: protocol.KindTTSStart, ID: msg.ID, SampleRate: 24000})
		_ = conn.WriteMessage(websocket.TextMessage, start)
		for i := 0; i < 3; i++ {
			_ = conn.WriteMessage(websocket.BinaryMessage, make([]byte, 240))
		}
		done, _ := json.Marshal(protocol.Message{T: protocol.KindTTSDone, ID: msg.ID})
		_ = conn.WriteMessage(websocket.TextMessage, done)
	}))
	defer ve.Close()
	t.Setenv("GODEX_VOICE_ENGINE_ADDR", strings.TrimPrefix(ve.URL, "http://"))

	// manager：voice_enabled=true
	workspace := t.TempDir()
	mgr := newTestManager(t, &config.Config{WorkspaceDir: workspace, HomeDir: filepath.Join(workspace, "home")})
	if _, err := mgr.Update(context.Background(), config.UpdateRequest{
		Values: map[string]any{"media.audio.voice_enabled": true},
	}); err != nil {
		t.Fatalf("enable voice: %v", err)
	}
	b := &voiceBridge{manager: mgr}
	srv := httptest.NewServer(http.HandlerFunc(b.handleTTSStream))
	defer srv.Close()

	url := "ws" + strings.TrimPrefix(srv.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	if err := conn.WriteMessage(websocket.TextMessage, mustJSON(ttsRequest{Text: "你好"})); err != nil {
		t.Fatalf("send text: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var pcmFrames int
	for {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read: %v (pcmFrames=%d)", err, pcmFrames)
		}
		if mt == websocket.BinaryMessage {
			pcmFrames++
			continue
		}
		var msg voiceMsg
		if err := json.Unmarshal(data, &msg); err != nil {
			t.Fatalf("unmarshal %s: %v", data, err)
		}
		if msg.Type == "tts_done" {
			break
		}
		if msg.Type == "error" {
			t.Fatalf("stream error: %s %s", msg.Code, msg.Text)
		}
	}
	if pcmFrames != 3 {
		t.Fatalf("pcm frames = %d, want 3", pcmFrames)
	}
}
