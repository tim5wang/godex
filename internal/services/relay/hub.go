package relay

import (
	"context"
	"encoding/base64"
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

type hubConn struct {
	hub     *Hub
	nodeID  string
	ws      *websocket.Conn
	writeMu sync.Mutex
	mu      sync.Mutex
	pending map[string]chan Frame
	lastPong time.Time
	closed  bool
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
func (h *Hub) Forward(ctx context.Context, nodeID string, req ForwardRequest) (*ForwardResponse, error) {
	h.mu.Lock()
	conn, ok := h.conns[nodeID]
	if !ok {
		h.mu.Unlock()
		return nil, ErrNodeOffline
	}
	reqID := "r_" + strconv.FormatInt(h.nextReq.Add(1), 10)
	ch := make(chan Frame, 1)
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

	select {
	case <-ctx.Done():
		h.dropPending(nodeID, reqID)
		return nil, ctx.Err()
	case resp := <-ch:
		body, err := base64.StdEncoding.DecodeString(resp.BodyB64)
		if err != nil {
			return nil, err
		}
		return &ForwardResponse{Status: resp.Status, Headers: resp.Headers, Body: body}, nil
	}
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
		hub:     h,
		nodeID:  nodeID,
		ws:      ws,
		pending: make(map[string]chan Frame),
		lastPong: time.Now(),
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
		case FrameResponse, FrameStreamEnd:
			conn.deliver(frame)
		case FrameStream:
			conn.deliver(frame)
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
	if ok {
		delete(c.pending, frame.ReqID)
	}
	c.mu.Unlock()
	if ok {
		ch <- frame
	}
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
