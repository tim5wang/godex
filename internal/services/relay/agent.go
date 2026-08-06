package relay

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
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
	CenterURL  string
	NodeID     string
	Credential string
	Version    string
	Caps       []string
	// Handler is the node's local HTTP API surface (httpapi handler).
	Handler http.Handler
	// ForwardAllow is the node's forward_allow allowlist (host:port entries,
	// "*" wildcards allowed). Empty/nil denies all TCP forwarding.
	ForwardAllow []string

	ReconnectMin time.Duration
	ReconnectMax time.Duration
}

// Agent dials the hub outbound, authenticates with a per-node credential, and
// serves forwarded requests against the local HTTP handler.
type Agent struct {
	cfg AgentConfig

	mu      sync.Mutex
	writeMu sync.Mutex // serializes WebSocket writes (requests + events)
	conn    *websocket.Conn
	cancel  context.CancelFunc
	wg      sync.WaitGroup

	dialsMu sync.Mutex
	dials   map[string]net.Conn // connID → dialed TCP connection (TCP forwarding)
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
	return &Agent{cfg: cfg, dials: make(map[string]net.Conn)}
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
			a.writeMu.Lock()
			_ = writeFrame(conn, Frame{Type: FramePong, Seq: frame.Seq})
			a.writeMu.Unlock()
		case FrameRequest:
			// Serve forwarded requests concurrently so a long-lived handler (e.g.
			// an SSE event stream) never blocks this read loop: the loop must
			// keep answering hub pings, otherwise the hub's 30s read deadline
			// drops the relay connection and every later proxy request fails
			// with 503 node offline.
			go a.serveRequest(ctx, conn, frame)
		case FrameTCPOpen:
			a.handleTCPOpen(conn, frame)
		case FrameTCPData:
			a.handleTCPData(frame)
		case FrameTCPClose:
			a.handleTCPClose(frame)
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
	// Prove to the local httpapi that this request arrived over the
	// authenticated relay channel: only this node holds its credential, so
	// only the relay agent can produce a matching trust signature. The
	// center-side proxy never forwards this header from the outside, so its
	// presence means the caller already passed the center's own auth.
	req.Header.Set(RelayTrustHeader, SignRelayTrust(a.cfg.NodeID, a.cfg.Credential))

	// The streamWriter sends SSE-style output as relay stream frames in real
	// time; plain handlers still produce a single buffered response.
	writer := newStreamWriter(a, conn, frame.ReqID)
	a.cfg.Handler.ServeHTTP(writer, req)
	return writer.Close()
}

func (a *Agent) sendError(conn *websocket.Conn, reqID string, err error) error {
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
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
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	return writeFrame(conn, Frame{
		Type:    FrameEvent,
		Kind:    kind,
		Payload: data,
	})
}

// handleTCPOpen serves the TCP-forwarding side of the agent: it validates the
// target against the forward_allow allowlist, dials the target, and pumps bytes
// in both directions until either side closes.
func (a *Agent) handleTCPOpen(conn *websocket.Conn, frame Frame) {
	var payload TCPOpenPayload
	if err := json.Unmarshal(frame.Payload, &payload); err != nil || payload.ConnID == "" || payload.Target == "" {
		a.sendTCPClose(conn, tcpConnID(frame), "invalid tcp_open payload")
		return
	}
	if !AllowForward(a.cfg.ForwardAllow, payload.Target) {
		a.sendTCPClose(conn, payload.ConnID, "forward target not allowed")
		return
	}
	dialed, err := net.DialTimeout("tcp", payload.Target, 10*time.Second)
	if err != nil {
		a.sendTCPClose(conn, payload.ConnID, "dial failed: "+err.Error())
		return
	}
	a.dialsMu.Lock()
	a.dials[payload.ConnID] = dialed
	a.dialsMu.Unlock()

	// Pump local dialed-connection bytes back to the hub as tcp_data frames.
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := dialed.Read(buf)
			if n > 0 {
				if writeErr := a.sendTCPData(conn, payload.ConnID, buf[:n]); writeErr != nil {
					a.closeDial(payload.ConnID)
					return
				}
			}
			if err != nil {
				a.sendTCPClose(conn, payload.ConnID, "")
				a.closeDial(payload.ConnID)
				return
			}
		}
	}()
}

// handleTCPData writes hub-arriving bytes into the dialed TCP connection.
func (a *Agent) handleTCPData(frame Frame) {
	connID := tcpConnID(frame)
	a.dialsMu.Lock()
	dialed, ok := a.dials[connID]
	a.dialsMu.Unlock()
	if !ok {
		return
	}
	chunk, err := decodedTCPChunk(frame)
	if err != nil || len(chunk) == 0 {
		return
	}
	if _, err := dialed.Write(chunk); err != nil {
		a.closeDial(connID)
	}
}

// handleTCPClose tears down the dialed connection when the hub closes the stream.
func (a *Agent) handleTCPClose(frame Frame) {
	a.closeDial(tcpConnID(frame))
}

func (a *Agent) closeDial(connID string) {
	a.dialsMu.Lock()
	dialed, ok := a.dials[connID]
	if ok {
		delete(a.dials, connID)
	}
	a.dialsMu.Unlock()
	if ok {
		_ = dialed.Close()
	}
}

func (a *Agent) sendTCPData(conn *websocket.Conn, connID string, chunk []byte) error {
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	return writeFrame(conn, Frame{
		Type:    FrameTCPData,
		Payload: tcpDataPayloadJSON(connID),
		BodyB64: base64.StdEncoding.EncodeToString(chunk),
	})
}

func (a *Agent) sendTCPClose(conn *websocket.Conn, connID, reason string) error {
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	return writeFrame(conn, Frame{Type: FrameTCPClose, Payload: mustTCPClosePayload(connID, reason)})
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
