package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/tim5wang/godex/internal/core/config"
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
