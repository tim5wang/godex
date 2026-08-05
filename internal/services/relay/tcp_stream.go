package relay

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"sync"
)

// tcpStream is a bidirectional byte stream between the center and a TCP
// connection dialed by a node, carried over tcp_data / tcp_close frames. It
// implements io.ReadWriteCloser so callers can io.Copy between it and a local
// listener — the ssh -L style jump-host experience.
type tcpStream struct {
	hub    *Hub
	nodeID string
	connID string
	ch     chan Frame
	buf    bytes.Buffer
	once   sync.Once
}

var _ io.ReadWriteCloser = (*tcpStream)(nil)

// OpenTCPStream asks the node identified by nodeID to dial target and returns
// a byte stream bound to connID. The stream is multiplexed over the node's
// relay connection; ErrNodeOffline is returned when no live connection exists.
func (h *Hub) OpenTCPStream(ctx context.Context, nodeID, connID, target string) (io.ReadWriteCloser, error) {
	h.mu.Lock()
	conn, ok := h.conns[nodeID]
	if !ok {
		h.mu.Unlock()
		return nil, ErrNodeOffline
	}
	ch := make(chan Frame, 64)
	conn.mu.Lock()
	if conn.closed {
		conn.mu.Unlock()
		h.mu.Unlock()
		return nil, ErrNodeOffline
	}
	conn.tcpStreams[connID] = ch
	conn.mu.Unlock()
	h.mu.Unlock()

	payload, _ := json.Marshal(TCPOpenPayload{ConnID: connID, Target: target})
	if err := conn.write(Frame{Type: FrameTCPOpen, Payload: payload}); err != nil {
		h.dropTCPStream(nodeID, connID)
		return nil, err
	}
	return &tcpStream{hub: h, nodeID: nodeID, connID: connID, ch: ch}, nil
}

// Read returns the next bytes received from the node's dialed connection,
// blocking until data arrives or the node closes the stream (io.EOF).
func (s *tcpStream) Read(p []byte) (int, error) {
	for s.buf.Len() == 0 {
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
			s.buf.Write(chunk)
		case FrameTCPClose:
			return 0, io.EOF
		}
	}
	return s.buf.Read(p)
}

// Write forwards p to the node's dialed connection as a tcp_data frame.
func (s *tcpStream) Write(p []byte) (int, error) {
	s.hub.mu.Lock()
	conn, ok := s.hub.conns[s.nodeID]
	s.hub.mu.Unlock()
	if !ok {
		return 0, ErrNodeOffline
	}
	if err := conn.write(Frame{
		Type:    FrameTCPData,
		Payload: tcpDataPayloadJSON(s.connID),
		BodyB64: base64.StdEncoding.EncodeToString(p),
	}); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Close sends tcp_close to the node and releases the stream slot.
func (s *tcpStream) Close() error {
	s.once.Do(func() {
		payload, _ := json.Marshal(TCPClosePayload{ConnID: s.connID})
		s.hub.mu.Lock()
		conn, ok := s.hub.conns[s.nodeID]
		s.hub.mu.Unlock()
		if ok {
			_ = conn.write(Frame{Type: FrameTCPClose, Payload: payload})
		}
		s.hub.dropTCPStream(s.nodeID, s.connID)
	})
	return nil
}

// dropTCPStream removes and closes the pending channel for a tcp stream.
func (h *Hub) dropTCPStream(nodeID, connID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if conn, ok := h.conns[nodeID]; ok {
		conn.mu.Lock()
		if ch, ok := conn.tcpStreams[connID]; ok {
			delete(conn.tcpStreams, connID)
			close(ch)
		}
		conn.mu.Unlock()
	}
}
