package relay

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/tim5wang/godex/internal/core/identity"
	"github.com/tim5wang/godex/internal/platform/logger"
)

// ForwardHandler serves the center-side forward session endpoint. A CLI client
// connects (e.g. "godex node forward --node X --local 3306 --target H:P"),
// sends tcp_open frames for each accepted local connection, and the handler
// bridges bytes between the client WebSocket and the node's dialed TCP stream.
type ForwardHandler struct {
	hub       *Hub
	authorize func(*http.Request) bool
	// AuthorizeNode, when set, decides whether the CALLER may reach the target
	// node (identity.Policy.Authorize) — the same NodeAccess seam the HTTP proxy
	// uses. authorize above only proves "some valid credential"; without this a
	// valid credential could open TCP forward streams into ANY node, which once
	// tenants exist means reaching another tenant's network services.
	// nil skips the check, keeping existing callers unchanged.
	AuthorizeNode func(r *http.Request, nodeID string) identity.Decision
	// AuditRecorder, when set, receives one record per forward ATTEMPT —
	// refused and allowed alike. nil skips auditing, keeping existing callers
	// unchanged.
	//
	// This exists because a REFUSED forward used to be invisible: the node
	// answered tcp_close and logged nothing, so "someone tried to reach a
	// service they may not touch" left no trace anywhere, and the center could
	// not even tell that the attempt happened. An audit trail that records only
	// successes cannot answer the question it exists for.
	AuditRecorder func(ForwardAudit)
}

// ForwardAudit is one forwarding attempt as seen by the center: who tried to
// reach which target on which node, and whether it was allowed.
//
// Carries no payload content — only who/where/whether — which keeps decision ②
// ("the center forwards tenant data without storing it") intact: an audit row
// must not become the place that data accumulates.
type ForwardAudit struct {
	// TenantID/UserID come from the NodeAccess decision, not from headers — the
	// same rule the HTTP metering path follows, and for the same reason: a caller
	// must not be able to choose whose audit trail its attempt is filed under.
	TenantID string
	UserID   string
	NodeID   string
	// Target is the host:port the caller asked for. Empty when the refusal
	// happened before any target was named (NodeAccess refuses the whole node).
	Target string
	// Allowed reports the decision; Reason explains a refusal — either the
	// policy's reason or the node's own egress-refusal message.
	Allowed bool
	Reason  string
	At      time.Time
}

// NewForwardHandler creates the center-side forward endpoint. authorize may be
// nil to accept every client (web token check).
func NewForwardHandler(hub *Hub, authorize func(*http.Request) bool) *ForwardHandler {
	return &ForwardHandler{hub: hub, authorize: authorize}
}

// recordAudit reports one attempt to the wired recorder, if any.
func (h *ForwardHandler) recordAudit(caller identity.Decision, nodeID, target string, allowed bool, reason string) {
	if h == nil || h.AuditRecorder == nil {
		return
	}
	h.AuditRecorder(ForwardAudit{
		TenantID: caller.TenantID,
		UserID:   caller.UserID,
		NodeID:   nodeID,
		Target:   target,
		Allowed:  allowed,
		Reason:   reason,
		At:       time.Now(),
	})
}

// ServeHTTP upgrades the WebSocket and runs the forward session.
func (h *ForwardHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.authorize != nil && !h.authorize(r) {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/control/nodes/")
	nodeID, _, ok := strings.Cut(rest, "/forward")
	if !ok || nodeID == "" {
		http.Error(w, `{"error":"invalid forward path"}`, http.StatusBadRequest)
		return
	}

	// NodeAccess (P0): checked before the WebSocket upgrade so a denial is an
	// ordinary HTTP 403 rather than an upgraded-then-closed socket.
	var caller identity.Decision
	if h.AuthorizeNode != nil {
		caller = h.AuthorizeNode(r, nodeID)
		if !caller.Allowed {
			logger.Warnf("relay forward: denied caller=%s target=%s reason=%s user=%s",
				clientIP(r), nodeID, caller.Reason, caller.UserID)
			// Audited before the response: the refusal is the event an audit trail
			// exists to capture, and a rejection that returns early must not skip it.
			h.recordAudit(caller, nodeID, "", false, caller.Reason)
			http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
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

	session := &forwardSession{
		handler: h,
		caller:  caller,
		nodeID:  nodeID,
		conn:    conn,
		streams: make(map[string]*tcpStream),
	}
	session.run(r.Context())
}

// forwardSession is one CLI↔center WebSocket carrying multiple tcp streams
// multiplexed by conn_id.
type forwardSession struct {
	handler *ForwardHandler
	// caller is the NodeAccess decision this session was admitted under. Kept on
	// the session because the per-TARGET decision happens later, when a tcp_open
	// frame arrives, and the audit row for that attempt needs the identity.
	caller  identity.Decision
	nodeID  string
	conn    *websocket.Conn
	mu      sync.Mutex
	streams map[string]*tcpStream
}

func (s *forwardSession) run(ctx context.Context) {
	for {
		_ = s.conn.SetReadDeadline(time.Now().Add(90 * time.Second))
		_, data, err := s.conn.ReadMessage()
		if err != nil {
			return
		}
		frame, err := DecodeFrame(data)
		if err != nil {
			continue
		}
		switch frame.Type {
		case FrameTCPOpen:
			s.handleOpen(ctx, frame)
		case FrameTCPData:
			s.handleData(frame)
		case FrameTCPClose:
			s.handleClose(frame)
		}
	}
}

func (s *forwardSession) handleOpen(ctx context.Context, frame Frame) {
	var payload TCPOpenPayload
	if err := json.Unmarshal(frame.Payload, &payload); err != nil || payload.ConnID == "" || payload.Target == "" {
		s.replyClose(payload.ConnID, "invalid tcp_open payload")
		return
	}
	stream, err := s.handler.hub.OpenTCPStream(ctx, s.nodeID, payload.ConnID, payload.Target)
	if err != nil {
		// Covers the node's own egress refusal too: the node answers
		// "forward target not allowed" as a tcp_close, which OpenTCPStream
		// surfaces as this error. That is the case the audit trail exists for —
		// a target the operator's allowlist refuses was previously invisible
		// everywhere.
		s.handler.recordAudit(s.caller, s.nodeID, payload.Target, false, err.Error())
		s.replyClose(payload.ConnID, err.Error())
		return
	}
	s.handler.recordAudit(s.caller, s.nodeID, payload.Target, true, "")
	s.mu.Lock()
	s.streams[payload.ConnID] = stream.(*tcpStream)
	s.mu.Unlock()
	// Pump node bytes back to the CLI client until EOF or close.
	go s.pump(payload.ConnID, stream)
}

func (s *forwardSession) handleData(frame Frame) {
	connID := tcpConnID(frame)
	s.mu.Lock()
	stream, ok := s.streams[connID]
	s.mu.Unlock()
	if !ok {
		return
	}
	chunk, err := decodedTCPChunk(frame)
	if err != nil || len(chunk) == 0 {
		return
	}
	if _, err := stream.Write(chunk); err != nil {
		s.closeStream(connID)
	}
}

func (s *forwardSession) handleClose(frame Frame) {
	connID := tcpConnID(frame)
	s.closeStream(connID)
}

func (s *forwardSession) closeStream(connID string) {
	s.mu.Lock()
	stream, ok := s.streams[connID]
	if ok {
		delete(s.streams, connID)
	}
	s.mu.Unlock()
	if ok {
		_ = stream.Close()
	}
}

// pump copies bytes from the node stream back to the CLI WebSocket as tcp_data
// frames, ending with a tcp_close when the node side finishes.
func (s *forwardSession) pump(connID string, stream io.Reader) {
	buf := make([]byte, 32*1024)
	for {
		n, err := stream.Read(buf)
		if n > 0 {
			frame := Frame{
				Type:    FrameTCPData,
				Payload: tcpDataPayloadJSON(connID),
				BodyB64: base64.StdEncoding.EncodeToString(buf[:n]),
			}
			if writeErr := writeFrame(s.conn, frame); writeErr != nil {
				s.closeStream(connID)
				return
			}
		}
		if err != nil {
			_ = writeFrame(s.conn, Frame{Type: FrameTCPClose, Payload: mustTCPClosePayload(connID, "")})
			s.closeStream(connID)
			return
		}
	}
}

func (s *forwardSession) replyClose(connID, reason string) {
	_ = writeFrame(s.conn, Frame{Type: FrameTCPClose, Payload: mustTCPClosePayload(connID, reason)})
}

func mustTCPClosePayload(connID, reason string) json.RawMessage {
	data, _ := json.Marshal(TCPClosePayload{ConnID: connID, Reason: reason})
	return data
}

// ForwardWSURL converts a center base URL (http(s)://host or ws(s)://host)
// into the forward session WebSocket URL for a node. The center serves relay
// endpoints under /api (the webui strips the prefix), so the path mirrors the
// external proxy URL: /api/control/nodes/{id}/forward.
func ForwardWSURL(centerURL, nodeID string) (string, error) {
	raw := strings.TrimSpace(centerURL)
	if raw == "" || nodeID == "" {
		return "", fmt.Errorf("empty center URL or node id")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid center URL %q: %w", raw, err)
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	case "ws", "wss":
		// keep as-is
	default:
		return "", fmt.Errorf("unsupported center URL scheme %q", u.Scheme)
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/api/control/nodes/" + nodeID + "/forward"
	return u.String(), nil
}

// DialForward connects a CLI forward client to the center's forward endpoint.
// token, when non-empty, is sent as a Bearer credential.
func DialForward(ctx context.Context, wsURL, token string) (*ForwardClient, error) {
	headers := http.Header{}
	if token != "" {
		headers.Set("Authorization", "Bearer "+token)
	}
	dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	conn, _, err := dialer.DialContext(ctx, wsURL, headers)
	if err != nil {
		return nil, err
	}
	client := &ForwardClient{
		conn:    conn,
		streams: make(map[string]chan Frame),
	}
	go client.readLoop()
	return client, nil
}

// ForwardClient is the CLI-side half of a forward session.
type ForwardClient struct {
	conn *websocket.Conn
	mu   sync.Mutex
	// streams maps conn_id → pending frame channel.
	streams map[string]chan Frame
}

// Close terminates the forward session.
func (c *ForwardClient) Close() error {
	return c.conn.Close()
}

// Open asks the center to dial target on the configured node and returns a
// byte stream for one accepted local connection.
func (c *ForwardClient) Open(target string) (io.ReadWriteCloser, error) {
	connID := "c_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	ch := make(chan Frame, 64)
	c.mu.Lock()
	c.streams[connID] = ch
	c.mu.Unlock()
	if err := writeFrame(c.conn, Frame{Type: FrameTCPOpen, Payload: mustTCPOpenPayload(connID, target)}); err != nil {
		c.release(connID)
		return nil, err
	}
	return &forwardClientStream{client: c, connID: connID, ch: ch}, nil
}

func (c *ForwardClient) release(connID string) {
	c.mu.Lock()
	if ch, ok := c.streams[connID]; ok {
		delete(c.streams, connID)
		close(ch)
	}
	c.mu.Unlock()
}

func (c *ForwardClient) readLoop() {
	for {
		_ = c.conn.SetReadDeadline(time.Now().Add(90 * time.Second))
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			c.mu.Lock()
			for _, ch := range c.streams {
				close(ch)
			}
			c.streams = make(map[string]chan Frame)
			c.mu.Unlock()
			return
		}
		frame, err := DecodeFrame(data)
		if err != nil {
			continue
		}
		switch frame.Type {
		case FrameTCPData, FrameTCPClose:
			connID := tcpConnID(frame)
			c.mu.Lock()
			ch, ok := c.streams[connID]
			if ok && frame.Type == FrameTCPClose {
				delete(c.streams, connID)
			}
			c.mu.Unlock()
			if ok {
				ch <- frame
			}
		}
	}
}

// forwardClientStream is one CLI-side byte stream bound to a conn_id.
type forwardClientStream struct {
	client *ForwardClient
	connID string
	ch     chan Frame
	once   sync.Once
}

var _ io.ReadWriteCloser = (*forwardClientStream)(nil)

func (s *forwardClientStream) Read(p []byte) (int, error) {
	for {
		frame, ok := <-s.ch
		if !ok {
			return 0, io.EOF
		}
		switch frame.Type {
		case FrameTCPData:
			chunk, err := decodedTCPChunk(frame)
			if err != nil {
				return 0, err
			}
			return copy(p, chunk), nil
		case FrameTCPClose:
			return 0, io.EOF
		}
	}
}

func (s *forwardClientStream) Write(p []byte) (int, error) {
	if err := writeFrame(s.client.conn, Frame{
		Type:    FrameTCPData,
		Payload: tcpDataPayloadJSON(s.connID),
		BodyB64: base64.StdEncoding.EncodeToString(p),
	}); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (s *forwardClientStream) Close() error {
	s.once.Do(func() {
		_ = writeFrame(s.client.conn, Frame{Type: FrameTCPClose, Payload: mustTCPClosePayload(s.connID, "")})
		s.client.release(s.connID)
	})
	return nil
}

func mustTCPOpenPayload(connID, target string) json.RawMessage {
	data, _ := json.Marshal(TCPOpenPayload{ConnID: connID, Target: target})
	return data
}

var errForwardClosed = errors.New("forward session closed")
