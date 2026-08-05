package relay

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// ErrNodeOffline is returned when a forward targets a node with no live relay connection.
var ErrNodeOffline = errors.New("relay node offline")

// ForwardRequest is a user-facing API request that the hub routes to a node.
type ForwardRequest struct {
	Method  string
	Path    string
	Query   string
	Headers map[string]string
	Body    []byte
}

// ForwardResponse is the aggregated reply from the target node.
type ForwardResponse struct {
	Status  int
	Headers map[string]string
	Body    []byte
}

// CredentialValidator reports whether a node may authenticate with the given credential.
type CredentialValidator func(nodeID, credential string) bool

// StatusHook receives node relay lifecycle events (online=true on connect,
// online=false after disconnect).
type StatusHook func(nodeID string, online bool)

// EventSink receives unsolicited node→hub event frames (session/job/approval
// observation updates). The sink is called with the authenticated node ID and
// the decoded frame.
type EventSink func(nodeID string, frame Frame)

type hubConn struct {
	hub     *Hub
	nodeID  string
	ws      *websocket.Conn
	writeMu sync.Mutex
	mu      sync.Mutex
	pending map[string]chan Frame
	tcpStreams map[string]chan Frame
	lastPong time.Time
	closed   bool
}

// Hub accepts outbound WebSocket connections from nodes, maintains the
// node_id → connection routing table, and forwards user requests to nodes.
type Hub struct {
	validate     CredentialValidator
	pingInterval time.Duration

	mu       sync.Mutex
	conns    map[string]*hubConn
	nextReq  atomic.Int64
	statusHook StatusHook
	eventSink  EventSink

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewHub creates a relay hub. validate may be nil to accept any hello.
func NewHub(validate CredentialValidator) *Hub {
	ctx, cancel := context.WithCancel(context.Background())
	hub := &Hub{
		validate:     validate,
		pingInterval: 15 * time.Second,
		conns:        make(map[string]*hubConn),
		cancel:       cancel,
	}
	hub.wg.Add(1)
	go hub.pingLoop(ctx)
	return hub
}

// SetPingInterval overrides the application-level ping interval (tests use short values).
func (h *Hub) SetPingInterval(interval time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if interval > 0 {
		h.pingInterval = interval
	}
}

// SetStatusHook registers a callback invoked when a node's relay connection
// comes online or goes offline.
func (h *Hub) SetStatusHook(hook StatusHook) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.statusHook = hook
}

// SetEventSink registers a callback invoked for every event frame a connected
// node pushes (session/job/approval observation updates).
func (h *Hub) SetEventSink(sink EventSink) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.eventSink = sink
}

// Start implements the lifecycle contract; the hub's background loops are
// already running after NewHub, so Start is a no-op.
func (h *Hub) Start(ctx context.Context) error { return nil }

// Stop implements the lifecycle contract by shutting down all connections.
func (h *Hub) Stop(ctx context.Context) error { return h.Shutdown(ctx) }

// Shutdown closes all node connections and stops background loops.
func (h *Hub) Shutdown(ctx context.Context) error {
	h.cancel()
	h.mu.Lock()
	for _, conn := range h.conns {
		_ = conn.ws.Close()
	}
	h.mu.Unlock()
	h.wg.Wait()
	return nil
}

func (h *Hub) pingLoop(ctx context.Context) {
	defer h.wg.Done()
	ticker := time.NewTicker(h.pingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.mu.Lock()
			conns := make([]*hubConn, 0, len(h.conns))
			for _, conn := range h.conns {
				conns = append(conns, conn)
			}
			h.mu.Unlock()
			for _, conn := range conns {
				seq := h.nextReq.Add(1)
				_ = conn.write(Frame{Type: FramePing, Seq: seq})
			}
		}
	}
}

// IsOnline reports whether a node currently holds a live relay connection.
func (h *Hub) IsOnline(nodeID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.conns[nodeID]
	return ok
}

// LastPong returns the most recent pong time for a connected node.
func (h *Hub) LastPong(nodeID string) time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	conn, ok := h.conns[nodeID]
	if !ok {
		return time.Time{}
	}
	conn.mu.Lock()
	defer conn.mu.Unlock()
	return conn.lastPong
}

// Forward sends a request to the target node and waits for its response.
// Streaming responses (FrameStream chunks) are aggregated into one body so
// callers that do not need real-time delivery keep working unchanged.
func (h *Hub) Forward(ctx context.Context, nodeID string, req ForwardRequest) (*ForwardResponse, error) {
	ch, err := h.startRequest(ctx, nodeID, req)
	if err != nil {
		return nil, err
	}

	var body []byte
	status := 0
	headers := map[string]string{}
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case frame := <-ch:
			chunk, decodeErr := base64.StdEncoding.DecodeString(frame.BodyB64)
			if decodeErr != nil {
				return nil, decodeErr
			}
			body = append(body, chunk...)
			if frame.Status != 0 {
				status = frame.Status
			}
			if len(frame.Headers) > 0 {
				headers = frame.Headers
			}
			if frame.Type == FrameResponse || frame.Type == FrameStreamEnd {
				return &ForwardResponse{Status: status, Headers: headers, Body: body}, nil
			}
		}
	}
}

// StreamChunkHandler receives one chunk of a streaming relay response. final
// is true on the last call (FrameResponse or FrameStreamEnd).
type StreamChunkHandler func(status int, headers map[string]string, chunk []byte, final bool) error

// ForwardStream sends a request to the target node and invokes onChunk in
// real time for every FrameStream chunk, ending with final=true. This is the
// transport used for SSE-style remote endpoints (chat events, terminal output).
func (h *Hub) ForwardStream(ctx context.Context, nodeID string, req ForwardRequest, onChunk StreamChunkHandler) error {
	ch, err := h.startRequest(ctx, nodeID, req)
	if err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case frame := <-ch:
			chunk, decodeErr := base64.StdEncoding.DecodeString(frame.BodyB64)
			if decodeErr != nil {
				return decodeErr
			}
			if frame.Type == FrameResponse || frame.Type == FrameStreamEnd {
				return onChunk(frame.Status, frame.Headers, chunk, true)
			}
			if err := onChunk(frame.Status, frame.Headers, chunk, false); err != nil {
				return err
			}
		}
	}
}

// startRequest registers a pending response channel and transmits the request
// frame. Both Forward and ForwardStream share this preamble.
func (h *Hub) startRequest(ctx context.Context, nodeID string, req ForwardRequest) (<-chan Frame, error) {
	h.mu.Lock()
	conn, ok := h.conns[nodeID]
	if !ok {
		h.mu.Unlock()
		return nil, ErrNodeOffline
	}
	reqID := "r_" + strconv.FormatInt(h.nextReq.Add(1), 10)
	ch := make(chan Frame, 64)
	conn.mu.Lock()
	if conn.closed {
		conn.mu.Unlock()
		h.mu.Unlock()
		return nil, ErrNodeOffline
	}
	conn.pending[reqID] = ch
	conn.mu.Unlock()
	h.mu.Unlock()

	frame := Frame{
		Type:    FrameRequest,
		ReqID:   reqID,
		Method:  req.Method,
		Path:    req.Path,
		Query:   req.Query,
		Headers: req.Headers,
		BodyB64: base64.StdEncoding.EncodeToString(req.Body),
	}
	if err := conn.write(frame); err != nil {
		h.dropPending(nodeID, reqID)
		return nil, err
	}
	return ch, nil
}

func (h *Hub) dropPending(nodeID, reqID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if conn, ok := h.conns[nodeID]; ok {
		conn.mu.Lock()
		delete(conn.pending, reqID)
		conn.mu.Unlock()
	}
}

// ServeHTTP upgrades the WebSocket and runs the node connection lifecycle.
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	upgrader := websocket.Upgrader{
		HandshakeTimeout: 5 * time.Second,
		// The hub is reachable only through the center server; same-origin
		// browser clients are not expected, so check origin loosely.
		CheckOrigin: func(*http.Request) bool { return true },
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	h.handleConn(conn)
}

func (h *Hub) handleConn(ws *websocket.Conn) {
	defer ws.Close()

	ws.SetReadDeadline(time.Now().Add(30 * time.Second))
	_, data, err := ws.ReadMessage()
	if err != nil {
		return
	}
	hello, err := DecodeFrame(data)
	if err != nil || hello.Type != FrameHello {
		return
	}

	nodeID := hello.NodeID
	if h.validate != nil && !h.validate(nodeID, hello.Credential) {
		_ = writeFrame(ws, Frame{Type: FrameHelloError, Reason: "invalid credential"})
		return
	}

	conn := &hubConn{
		hub:        h,
		nodeID:     nodeID,
		ws:         ws,
		pending:    make(map[string]chan Frame),
		tcpStreams: make(map[string]chan Frame),
		lastPong:   time.Now(),
	}

	h.mu.Lock()
	if existing, ok := h.conns[nodeID]; ok {
		_ = existing.ws.Close()
	}
	h.conns[nodeID] = conn
	h.mu.Unlock()

	if h.statusHook != nil {
		h.statusHook(nodeID, true)
	}

	if err := conn.write(Frame{Type: FrameHelloOK, Version: hello.Version}); err != nil {
		h.removeConn(nodeID, conn)
		return
	}

	for {
		ws.SetReadDeadline(time.Now().Add(30 * time.Second))
		_, data, err := ws.ReadMessage()
		if err != nil {
			break
		}
		frame, err := DecodeFrame(data)
		if err != nil {
			continue
		}
		switch frame.Type {
		case FramePong:
			conn.mu.Lock()
			conn.lastPong = time.Now()
			conn.mu.Unlock()
		case FrameEvent:
			h.mu.Lock()
			sink := h.eventSink
			h.mu.Unlock()
			if sink != nil {
				sink(nodeID, frame)
			}
		case FrameResponse, FrameStreamEnd:
			conn.deliver(frame)
		case FrameStream:
			conn.deliver(frame)
		case FrameTCPData, FrameTCPClose:
			conn.deliverTCP(frame)
		case FrameClose:
			h.removeConn(nodeID, conn)
			return
		}
	}
	h.removeConn(nodeID, conn)
}

func (c *hubConn) deliver(frame Frame) {
	c.mu.Lock()
	ch, ok := c.pending[frame.ReqID]
	// Keep the pending slot alive across FrameStream chunks; only terminal
	// frames (response / stream_end) remove it.
	if ok && (frame.Type == FrameResponse || frame.Type == FrameStreamEnd || frame.Type == FrameError) {
		delete(c.pending, frame.ReqID)
	}
	c.mu.Unlock()
	if ok {
		ch <- frame
	}
}

// deliverTCP routes a tcp_data / tcp_close frame to the pending stream bound
// to the frame's conn_id. tcp_close releases the stream slot.
func (c *hubConn) deliverTCP(frame Frame) {
	connID := tcpConnID(frame)
	if connID == "" {
		return
	}
	c.mu.Lock()
	ch, ok := c.tcpStreams[connID]
	if ok && frame.Type == FrameTCPClose {
		delete(c.tcpStreams, connID)
	}
	c.mu.Unlock()
	if ok {
		ch <- frame
	}
}

// tcpConnID extracts the conn_id from a tcp frame payload.
func tcpConnID(frame Frame) string {
	var payload struct {
		ConnID string `json:"conn_id"`
	}
	_ = json.Unmarshal(frame.Payload, &payload)
	return payload.ConnID
}

func (c *hubConn) write(frame Frame) error {
	data, err := EncodeFrame(frame)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if err := c.ws.WriteMessage(websocket.TextMessage, data); err != nil {
		return err
	}
	return nil
}

func (h *Hub) removeConn(nodeID string, conn *hubConn) {
	h.mu.Lock()
	if current, ok := h.conns[nodeID]; ok && current == conn {
		delete(h.conns, nodeID)
	}
	h.mu.Unlock()
	conn.mu.Lock()
	conn.closed = true
	for connID, ch := range conn.tcpStreams {
		delete(conn.tcpStreams, connID)
		close(ch)
	}
	conn.mu.Unlock()
	if h.statusHook != nil {
		h.statusHook(nodeID, false)
	}
}

func writeFrame(ws *websocket.Conn, frame Frame) error {
	data, err := EncodeFrame(frame)
	if err != nil {
		return err
	}
	return ws.WriteMessage(websocket.TextMessage, data)
}
