package httpapi

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/tim5wang/godex/internal/agent"
	"github.com/tim5wang/godex/internal/core/flow"
	"github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/services/commands"

	"github.com/tim5wang/agent-local-voice-engine/protocol"
)

// TestVoiceFlowSessionAdapterWithRealVoiceEngine runs the complete local
// ASR -> FlowSession -> TTS path against real voice-engine models.
//
// Set GODEX_REAL_VOICE_ENGINE_ADDR and GODEX_REAL_VOICE_SAMPLE_WAV to opt in.
func TestVoiceFlowSessionAdapterWithRealVoiceEngine(t *testing.T) {
	engineAddr := strings.TrimSpace(os.Getenv("GODEX_REAL_VOICE_ENGINE_ADDR"))
	samplePath := strings.TrimSpace(os.Getenv("GODEX_REAL_VOICE_SAMPLE_WAV"))
	if engineAddr == "" || samplePath == "" {
		t.Skip("set GODEX_REAL_VOICE_ENGINE_ADDR and GODEX_REAL_VOICE_SAMPLE_WAV to run real-model voice E2E")
	}
	pcm, err := readPCM16Mono16KWave(samplePath)
	if err != nil {
		t.Fatalf("read voice sample: %v", err)
	}

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
		FlowID:        "fl_voice_real_e2e",
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

	bridge := &voiceBridge{service: service, engineAddr: engineAddr}
	server := httptest.NewServer(http.HandlerFunc(bridge.handleVoice))
	defer server.Close()
	query := url.Values{
		"flow_id":         {def.FlowID},
		"flow_session_id": {session.SessionID},
	}
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/?"+query.Encode(), nil)
	if err != nil {
		t.Fatalf("dial Godex voice bridge: %v", err)
	}
	defer conn.Close()

	_ = conn.SetReadDeadline(time.Now().Add(90 * time.Second))
	readVoiceMessage(t, conn, "resumed")
	readVoiceMessage(t, conn, "ready")
	if err := conn.WriteJSON(voiceMsg{
		Type: string(protocol.KindStart), TurnID: "real-model-turn",
		Source: "real-model-e2e", SourceSequence: 1,
		ASRModel: "asr/sensevoice-small-int8", TTSModel: "tts/kokoro-82m", VAD: "server",
	}); err != nil {
		t.Fatalf("start real-model voice turn: %v", err)
	}

	const frameBytes = 640 // 20ms at 16kHz, mono, signed 16-bit PCM.
	for offset := 0; offset < len(pcm); offset += frameBytes {
		end := offset + frameBytes
		if end > len(pcm) {
			end = len(pcm)
		}
		if err := conn.WriteMessage(websocket.BinaryMessage, pcm[offset:end]); err != nil {
			t.Fatalf("send real audio frame: %v", err)
		}
	}
	if err := conn.WriteJSON(voiceMsg{Type: string(protocol.KindAudioEnd)}); err != nil {
		t.Fatalf("finish real audio turn: %v", err)
	}

	gotASR := false
	gotInputEvent := false
	gotFlowOutput := false
	gotAssistantText := false
	gotTTSStart := false
	gotTTSDone := false
	gotASREnd := false
	pcmBytes := 0
	for !(gotASR && gotInputEvent && gotFlowOutput && gotAssistantText && gotTTSStart && gotTTSDone && gotASREnd && pcmBytes > 0) {
		messageType, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read real voice result: %v", err)
		}
		if messageType == websocket.BinaryMessage {
			pcmBytes += len(data)
			continue
		}
		var message voiceMsg
		if err := json.Unmarshal(data, &message); err != nil {
			t.Fatalf("decode Godex voice message %s: %v", data, err)
		}
		switch message.Type {
		case "error":
			t.Fatalf("voice bridge error: %s: %s", message.Code, message.Text)
		case "asr_final":
			gotASR = gotASR || strings.TrimSpace(message.Text) != ""
		case "session_event":
			var envelope struct {
				Event agent.FlowSessionEvent `json:"event"`
			}
			if err := json.Unmarshal(data, &envelope); err == nil {
				gotInputEvent = gotInputEvent || envelope.Event.Type == "voice.asr_final"
				gotFlowOutput = gotFlowOutput || envelope.Event.Type == agent.FlowSessionOutputEventType
			}
		case "assistant_text":
			gotAssistantText = gotAssistantText || strings.TrimSpace(message.Text) != ""
		case "tts_start":
			gotTTSStart = true
		case "tts_done":
			gotTTSDone = true
		case "asr_end":
			gotASREnd = true
		}
	}
	t.Logf("real voice E2E passed: asr=%t input_event=%t flow_output=%t tts_pcm_bytes=%d", gotASR, gotInputEvent, gotFlowOutput, pcmBytes)
}

func readVoiceMessage(t *testing.T, conn *websocket.Conn, wantType string) voiceMsg {
	t.Helper()
	for {
		messageType, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read %s message: %v", wantType, err)
		}
		if messageType != websocket.TextMessage {
			continue
		}
		var message voiceMsg
		if err := json.Unmarshal(data, &message); err != nil {
			t.Fatalf("decode %s message %s: %v", wantType, data, err)
		}
		if message.Type == "error" {
			t.Fatalf("voice bridge error before %s: %s: %s", wantType, message.Code, message.Text)
		}
		if message.Type == wantType {
			return message
		}
	}
}

func readPCM16Mono16KWave(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) < 12 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return nil, fmt.Errorf("%q is not a RIFF/WAVE file", path)
	}
	var format, channels, bitsPerSample uint16
	var sampleRate uint32
	var pcm []byte
	for offset := 12; offset+8 <= len(data); {
		chunkSize := int(binary.LittleEndian.Uint32(data[offset+4 : offset+8]))
		chunkStart := offset + 8
		chunkEnd := chunkStart + chunkSize
		if chunkEnd > len(data) {
			return nil, fmt.Errorf("%q has a truncated WAV chunk", path)
		}
		switch string(data[offset : offset+4]) {
		case "fmt ":
			if chunkSize < 16 {
				return nil, fmt.Errorf("%q has an invalid fmt chunk", path)
			}
			format = binary.LittleEndian.Uint16(data[chunkStart : chunkStart+2])
			channels = binary.LittleEndian.Uint16(data[chunkStart+2 : chunkStart+4])
			sampleRate = binary.LittleEndian.Uint32(data[chunkStart+4 : chunkStart+8])
			bitsPerSample = binary.LittleEndian.Uint16(data[chunkStart+14 : chunkStart+16])
		case "data":
			pcm = append([]byte(nil), data[chunkStart:chunkEnd]...)
		}
		offset = chunkEnd + chunkSize%2
	}
	if format != 1 || channels != 1 || sampleRate != 16000 || bitsPerSample != 16 {
		return nil, fmt.Errorf(
			"%q must be PCM16 mono 16kHz (got format=%d channels=%d rate=%d bits=%d)",
			path, format, channels, sampleRate, bitsPerSample,
		)
	}
	if len(pcm) == 0 {
		return nil, fmt.Errorf("%q has no PCM data", path)
	}
	return pcm, nil
}
