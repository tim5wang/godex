package relay

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"
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

	// Block until the node confirms the dial (tcp_open_ack), reports failure
	// (tcp_close / error), or the caller's context is cancelled, so callers
	// learn the dial result before sending any bytes. Without this ack the
	// node drops tcp_data frames for conn ids it has not dialed yet, losing
	// the first bytes of a forwarded connection.
	select {
	case frame, ok := <-ch:
		if !ok {
			h.dropTCPStream(nodeID, connID)
			return nil, ErrNodeOffline
		}
		switch frame.Type {
		case FrameTCPOpenAck:
			// Dial succeeded; the stream is ready.
		case FrameTCPClose:
			h.dropTCPStream(nodeID, connID)
			return nil, tcpDialFailedError(frame)
		case FrameError:
			h.dropTCPStream(nodeID, connID)
			return nil, fmt.Errorf("relay tcp open: %s", strings.TrimSpace(frame.Reason))
		default:
			// Unexpected early frame (e.g. a banner before the ack). Buffer it
			// for tcpStream.Read instead of dropping the bytes.
			select {
			case ch <- frame:
			default:
			}
		}
	case <-ctx.Done():
		h.dropTCPStream(nodeID, connID)
		return nil, ctx.Err()
	}
	return &tcpStream{hub: h, nodeID: nodeID, connID: connID, ch: ch}, nil
}

// tcpDialFailedError converts a tcp_close frame (the node-side dial failed)
// into a readable error carrying the node's reason when present.
func tcpDialFailedError(frame Frame) error {
	var payload TCPClosePayload
	_ = json.Unmarshal(frame.Payload, &payload)
	reason := strings.TrimSpace(payload.Reason)
	if reason == "" {
		reason = "dial failed on node"
	}
	return fmt.Errorf("%s", reason)
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
		case FrameTCPOpenAck:
			// Defensive: OpenTCPStream consumes the ack; ignore any stragglers.
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
