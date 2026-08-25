package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/tim5wang/godex/internal/domain/message"

	"github.com/tim5wang/voice-engine/protocol"
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
	b := newVoiceBridge(nil)
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
