package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	voiceclient "github.com/tim5wang/agent-local-voice-engine/client"
	"github.com/tim5wang/agent-local-voice-engine/protocol"
)

type voiceEngineCapabilities struct {
	Models     []string `json:"models,omitempty"`
	ASRModels  []string `json:"asr_models,omitempty"`
	VADModels  []string `json:"vad_models,omitempty"`
	TTSModels  []string `json:"tts_models,omitempty"`
	DefaultASR string   `json:"default_asr,omitempty"`
	DefaultVAD string   `json:"default_vad,omitempty"`
	DefaultTTS string   `json:"default_tts,omitempty"`
}

// voiceEngineConn is the Realtime bridge's protocol client. The public client
// package remains used by the simpler HTTP TTS endpoints; this connection also
// understands incremental ASR and the additive model/cancellation fields.
type voiceEngineConn struct {
	ws         *websocket.Conn
	events     chan voiceclient.Event
	closeCh    chan struct{}
	closeOnce  sync.Once
	writeMu    sync.Mutex
	capability voiceEngineCapabilities
}

func dialVoiceEngine(ctx context.Context, addr string) (*voiceEngineConn, error) {
	wsURL, err := voiceEngineURL(addr)
	if err != nil {
		return nil, err
	}
	conn, _, err := (&websocket.Dialer{HandshakeTimeout: 5 * time.Second}).DialContext(ctx, wsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("voice-engine dial %s: %w", wsURL, err)
	}
	client := &voiceEngineConn{
		ws:      conn,
		events:  make(chan voiceclient.Event, 256),
		closeCh: make(chan struct{}),
	}
	if err := client.send(protocol.Message{T: protocol.KindHello}); err != nil {
		_ = conn.Close()
		return nil, err
	}
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	for {
		msgType, data, err := conn.ReadMessage()
		if err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("voice-engine handshake: %w", err)
		}
		if msgType != websocket.TextMessage {
			continue
		}
		var envelope struct {
			T protocol.MessageKind `json:"t"`
			voiceEngineCapabilities
			Code    string `json:"code,omitempty"`
			Message string `json:"message,omitempty"`
		}
		if err := json.Unmarshal(data, &envelope); err != nil {
			continue
		}
		switch envelope.T {
		case protocol.KindReady:
			client.capability = envelope.voiceEngineCapabilities
			_ = conn.SetReadDeadline(time.Time{})
			go client.readLoop()
			return client, nil
		case protocol.KindError:
			_ = conn.Close()
			return nil, fmt.Errorf("voice-engine handshake %s: %s", envelope.Code, envelope.Message)
		}
	}
}

func voiceEngineURL(addr string) (string, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return "", fmt.Errorf("voice-engine address is empty")
	}
	if !strings.Contains(addr, "://") {
		addr = "ws://" + strings.TrimRight(addr, "/") + "/ws"
	} else {
		u, err := url.Parse(addr)
		if err != nil {
			return "", fmt.Errorf("bad voice-engine address %q: %w", addr, err)
		}
		switch u.Scheme {
		case "http":
			u.Scheme = "ws"
		case "https":
			u.Scheme = "wss"
		case "ws", "wss":
		default:
			return "", fmt.Errorf("unsupported voice-engine scheme %q", u.Scheme)
		}
		if u.Path == "" || u.Path == "/" {
			u.Path = "/ws"
		}
		return u.String(), nil
	}
	return addr, nil
}

func (c *voiceEngineConn) Capabilities() voiceEngineCapabilities {
	return c.capability
}

func (c *voiceEngineConn) Events() <-chan voiceclient.Event { return c.events }

func (c *voiceEngineConn) Start(msg voiceMsg) error {
	start := map[string]any{"t": protocol.KindStart}
	if msg.ASRModel != "" {
		start["asr_model"] = msg.ASRModel
	}
	if msg.TTSModel != "" {
		start["tts_model"] = msg.TTSModel
	}
	if msg.VADModel != "" {
		start["vad_model"] = msg.VADModel
	}
	mode := strings.TrimSpace(msg.VAD)
	if mode == "" {
		mode = string(protocol.VADServer)
	}
	start["vad"] = mode
	return c.send(start)
}

func (c *voiceEngineConn) SendAudio(pcm []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_ = c.ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return c.ws.WriteMessage(websocket.BinaryMessage, pcm)
}

func (c *voiceEngineConn) AudioEnd() error {
	return c.send(protocol.Message{T: protocol.KindAudioEnd})
}

func (c *voiceEngineConn) Synthesize(id, text, model string) error {
	msg := map[string]any{"t": protocol.KindTTS, "id": id, "text": text}
	if model != "" {
		msg["tts_model"] = model
	}
	return c.send(msg)
}

func (c *voiceEngineConn) CancelTTS(id string) error {
	return c.send(map[string]any{"t": "tts_cancel", "id": id})
}

func (c *voiceEngineConn) send(message any) error {
	data, err := json.Marshal(message)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_ = c.ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return c.ws.WriteMessage(websocket.TextMessage, data)
}

func (c *voiceEngineConn) readLoop() {
	defer close(c.events)
	activeTTSID := ""
	emit := func(event voiceclient.Event) bool {
		select {
		case c.events <- event:
			return true
		case <-c.closeCh:
			return false
		}
	}
	for {
		msgType, data, err := c.ws.ReadMessage()
		if err != nil {
			select {
			case <-c.closeCh:
			default:
				emit(voiceclient.Event{Kind: voiceclient.EventKind("error"), Code: "connection_lost", Text: err.Error()})
			}
			return
		}
		if msgType == websocket.BinaryMessage {
			if !emit(voiceclient.Event{Kind: voiceclient.EventKind("pcm"), ID: activeTTSID, PCM: append([]byte(nil), data...)}) {
				return
			}
			continue
		}
		var msg protocol.Message
		if json.Unmarshal(data, &msg) != nil {
			continue
		}
		var event voiceclient.Event
		switch string(msg.T) {
		case "asr_partial":
			event = voiceclient.Event{Kind: voiceclient.EventKind("asr_partial"), Text: msg.Text}
		case string(protocol.KindASRFinal):
			event = voiceclient.Event{Kind: voiceclient.EventKind("asr_final"), Text: msg.Text}
		case "asr_utterance_end":
			event = voiceclient.Event{Kind: voiceclient.EventKind("asr_utterance_end")}
		case string(protocol.KindASREnd):
			event = voiceclient.Event{Kind: voiceclient.EventKind("asr_end")}
		case string(protocol.KindTTSStart):
			activeTTSID = msg.ID
			event = voiceclient.Event{Kind: voiceclient.EventKind("tts_start"), ID: msg.ID, SampleRate: msg.SampleRate}
		case string(protocol.KindTTSDone):
			event = voiceclient.Event{Kind: voiceclient.EventKind("tts_done"), ID: msg.ID}
			if activeTTSID == msg.ID {
				activeTTSID = ""
			}
		case "tts_cancelled":
			event = voiceclient.Event{Kind: voiceclient.EventKind("tts_cancelled"), ID: msg.ID}
			if activeTTSID == msg.ID {
				activeTTSID = ""
			}
		case string(protocol.KindError):
			event = voiceclient.Event{Kind: voiceclient.EventKind("error"), Code: msg.Code, Text: msg.Message}
		default:
			continue
		}
		if !emit(event) {
			return
		}
	}
}

func (c *voiceEngineConn) Close() error {
	var closeErr error
	c.closeOnce.Do(func() {
		_ = c.send(protocol.Message{T: protocol.KindStop})
		close(c.closeCh)
		closeErr = c.ws.Close()
	})
	return closeErr
}
