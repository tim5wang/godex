package codexnative

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"time"

	"github.com/gorilla/websocket"
)

const maxFrameBytes = 32 * 1024 * 1024

// The shared daemon speaks WebSocket over a Unix socket, not stdio JSONL.
// Keep JSON message boundaries independent of the underlying transport.
type rpcTransport interface {
	ReadMessage() ([]byte, error)
	WriteMessage([]byte) error
	Close() error
}

type socketTransport struct{ conn *websocket.Conn }

func dialSocket(ctx context.Context, socket string) (rpcTransport, error) {
	// The advertised socket may be a rendezvous symlink to a shorter physical
	// path. Resolve it before dialing to avoid Unix socket path length limits.
	physical, err := filepath.EvalSymlinks(socket)
	if err != nil {
		return nil, fmt.Errorf("Codex resolve Unix socket: %w", err)
	}
	dialer := websocket.Dialer{
		HandshakeTimeout: 5 * time.Second,
		NetDialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", physical)
		},
		// No HTTP proxy, compression extensions, TCP listener or CLI subprocess.
	}
	conn, response, err := dialer.DialContext(ctx, "ws://localhost/", nil)
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		return nil, fmt.Errorf("Codex WebSocket handshake over Unix socket: %w", err)
	}
	conn.SetReadLimit(maxFrameBytes)
	return &socketTransport{conn: conn}, nil
}

func (t *socketTransport) ReadMessage() ([]byte, error) {
	kind, data, err := t.conn.ReadMessage()
	if err != nil {
		return nil, err
	}
	if kind != websocket.TextMessage {
		return nil, fmt.Errorf("unexpected Codex WebSocket message type %d; expected uncompressed JSON text", kind)
	}
	return data, nil
}

func (t *socketTransport) WriteMessage(data []byte) error {
	return t.conn.WriteMessage(websocket.TextMessage, data)
}
func (t *socketTransport) Close() error { return t.conn.Close() }

// The injected Dial test seam retains JSONL support for in-process fake peers.
// Production always uses the message-oriented Unix/WebSocket transport above.
type lineTransport struct {
	io.ReadWriteCloser
	scanner *bufio.Scanner
}

func newLineTransport(stream io.ReadWriteCloser) *lineTransport {
	scanner := bufio.NewScanner(stream)
	scanner.Buffer(make([]byte, 64*1024), maxFrameBytes)
	return &lineTransport{ReadWriteCloser: stream, scanner: scanner}
}

func (t *lineTransport) ReadMessage() ([]byte, error) {
	if t.scanner.Scan() {
		return t.scanner.Bytes(), nil
	}
	if err := t.scanner.Err(); err != nil {
		return nil, err
	}
	return nil, io.EOF
}

func (t *lineTransport) WriteMessage(data []byte) error {
	data = append(data, '\n')
	n, err := t.Write(data)
	if err == nil && n != len(data) {
		return io.ErrShortWrite
	}
	return err
}
