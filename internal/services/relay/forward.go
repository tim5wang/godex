package relay

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// ForwardHandler serves the center-side forward session endpoint. A CLI client
// connects (e.g. "godex node forward --node X --local 3306 --target H:P"),
// sends tcp_open frames for each accepted local connection, and the handler
// bridges bytes between the client WebSocket and the node's dialed TCP stream.
type ForwardHandler struct {
	hub       *Hub
	authorize func(*http.Request) bool
}

// NewForwardHandler creates the center-side forward endpoint. authorize may be
// nil to accept every client (web token check).
func NewForwardHandler(hub *Hub, authorize func(*http.Request) bool) *ForwardHandler {
	return &ForwardHandler{hub: hub, authorize: authorize}
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
		s.replyClose(payload.ConnID, err.Error())
		return
	}
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
