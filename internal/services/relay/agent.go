package relay

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// AgentConfig configures the node-side relay agent.
type AgentConfig struct {
	// CenterURL is the hub endpoint. It may be ws:// / wss:// (used verbatim)
	// or an https:// center origin, in which case the agent appends /api/relay.
	CenterURL string
	NodeID    string
	Credential string
	Version   string
	Caps      []string
	// Handler is the node's local HTTP API surface (httpapi handler).
	Handler http.Handler

	ReconnectMin time.Duration
	ReconnectMax time.Duration
}

// Agent dials the hub outbound, authenticates with a per-node credential, and
// serves forwarded requests against the local HTTP handler.
type Agent struct {
	cfg AgentConfig

	mu     sync.Mutex
	conn   *websocket.Conn
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewAgent creates an agent. CenterURL, NodeID, Credential, and Handler are
// required; zero reconnect bounds get safe defaults.
func NewAgent(cfg AgentConfig) *Agent {
	if cfg.ReconnectMin <= 0 {
		cfg.ReconnectMin = 500 * time.Millisecond
	}
	if cfg.ReconnectMax <= 0 {
		cfg.ReconnectMax = 30 * time.Second
	}
	return &Agent{cfg: cfg}
}

// Start launches the outbound connection loop. It returns immediately; the
// agent keeps reconnecting until Stop is called.
func (a *Agent) Start(ctx context.Context) error {
	runCtx, cancel := context.WithCancel(ctx)
	a.cancel = cancel
	a.wg.Add(1)
	go a.runLoop(runCtx)
	return nil
}

// Stop closes the connection and waits for the loop to exit.
func (a *Agent) Stop(ctx context.Context) error {
	if a.cancel == nil {
		return nil
	}
	a.cancel()
	a.mu.Lock()
	if a.conn != nil {
		_ = a.conn.Close()
	}
	a.mu.Unlock()
	done := make(chan struct{})
	go func() {
		a.wg.Wait()
		close(done)
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return nil
	}
}

func (a *Agent) runLoop(ctx context.Context) {
	defer a.wg.Done()
	backoff := a.cfg.ReconnectMin
	for {
		if ctx.Err() != nil {
			return
		}
		err := a.connectOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff *= 2
			if backoff > a.cfg.ReconnectMax {
				backoff = a.cfg.ReconnectMax
			}
			continue
		}
		// A clean connection cycle completed; reset backoff.
		backoff = a.cfg.ReconnectMin
	}
}

func (a *Agent) connectOnce(ctx context.Context) error {
	dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	conn, _, err := dialer.DialContext(ctx, a.wsURL(), nil)
	if err != nil {
		return err
	}
	defer conn.Close()

	hello := Frame{
		Type:       FrameHello,
		NodeID:     a.cfg.NodeID,
		Credential: a.cfg.Credential,
		Version:    a.cfg.Version,
		Caps:       a.cfg.Caps,
	}
	if err := writeFrame(conn, hello); err != nil {
		return err
	}
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, data, err := conn.ReadMessage()
	if err != nil {
		return err
	}
	ack, err := DecodeFrame(data)
	if err != nil {
		return err
	}
	if ack.Type != FrameHelloOK {
		return errHelloRejected(ack.Reason)
	}

	a.mu.Lock()
	a.conn = conn
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		if a.conn == conn {
			a.conn = nil
		}
		a.mu.Unlock()
	}()

	for {
		if ctx.Err() != nil {
			return nil
		}
		conn.SetReadDeadline(time.Now().Add(90 * time.Second))
		_, data, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		frame, err := DecodeFrame(data)
		if err != nil {
			continue
		}
		switch frame.Type {
		case FramePing:
			_ = writeFrame(conn, Frame{Type: FramePong, Seq: frame.Seq})
		case FrameRequest:
			if err := a.serveRequest(ctx, conn, frame); err != nil {
				return err
			}
		case FrameClose:
			return nil
		}
	}
}

func (a *Agent) serveRequest(ctx context.Context, conn *websocket.Conn, frame Frame) error {
	req, err := http.NewRequestWithContext(ctx, frame.Method, frame.Path, bytes.NewReader(decodedBody(frame.BodyB64)))
	if err != nil {
		return a.sendError(conn, frame.ReqID, err)
	}
	req.URL.RawQuery = frame.Query
	for key, value := range frame.Headers {
		req.Header.Set(key, value)
	}

	rec := httptest.NewRecorder()
	a.cfg.Handler.ServeHTTP(rec, req)

	headers := make(map[string]string, len(rec.Header()))
	for key, values := range rec.Header() {
		if len(values) > 0 {
			headers[key] = values[0]
		}
	}
	resp := Frame{
		Type:    FrameResponse,
		ReqID:   frame.ReqID,
		Status:  rec.Code,
		Headers: headers,
		BodyB64: base64.StdEncoding.EncodeToString(rec.Body.Bytes()),
	}
	return writeFrame(conn, resp)
}

func (a *Agent) sendError(conn *websocket.Conn, reqID string, err error) error {
	return writeFrame(conn, Frame{
		Type:   FrameError,
		ReqID:  reqID,
		Reason: err.Error(),
		Status: http.StatusBadGateway,
	})
}

// SendEvent pushes an observation event frame (e.g. a NodeSnapshot) to the
// hub over the current connection. It returns an error when the agent is not
// currently connected.
func (a *Agent) SendEvent(kind string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal event payload: %w", err)
	}
	a.mu.Lock()
	conn := a.conn
	a.mu.Unlock()
	if conn == nil {
		return errors.New("relay agent not connected")
	}
	return writeFrame(conn, Frame{
		Type:    FrameEvent,
		Kind:    kind,
		Payload: data,
	})
}

func (a *Agent) wsURL() string {
	raw := strings.TrimSpace(a.cfg.CenterURL)
	if strings.HasPrefix(raw, "ws://") || strings.HasPrefix(raw, "wss://") {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/api/relay"
	return u.String()
}

func decodedBody(b64 string) []byte {
	if b64 == "" {
		return nil
	}
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil
	}
	return data
}

type helloRejectedError struct{ reason string }

func (e helloRejectedError) Error() string {
	if e.reason == "" {
		return "relay hello rejected"
	}
	return "relay hello rejected: " + e.reason
}

func errHelloRejected(reason string) error { return helloRejectedError{reason: reason} }
