package relay

import (
	"bytes"
	"encoding/base64"
	"net/http"

	"github.com/gorilla/websocket"
)

// streamWriter adapts a local http.Handler's response into relay frames sent
// back to the hub. Non-streaming handlers (write once, return) still produce a
// single FrameResponse for backward compatibility; handlers that call Flush
// (SSE-style) switch to FrameStream chunks terminated by FrameStreamEnd.
type streamWriter struct {
	agent  *Agent
	conn   *websocket.Conn
	reqID  string

	header      http.Header
	status      int
	buffer      bytes.Buffer
	sentHeader  bool
	streaming   bool
	wroteStream bool
}

var _ http.ResponseWriter = (*streamWriter)(nil)
var _ http.Flusher = (*streamWriter)(nil)

func newStreamWriter(agent *Agent, conn *websocket.Conn, reqID string) *streamWriter {
	return &streamWriter{
		agent:  agent,
		conn:   conn,
		reqID:  reqID,
		header: make(http.Header),
	}
}

func (w *streamWriter) Header() http.Header { return w.header }

func (w *streamWriter) WriteHeader(code int) {
	if w.sentHeader {
		return
	}
	w.status = code
	w.sentHeader = true
}

// Write buffers output; Flush turns the buffer into a stream chunk so SSE
// events reach the hub in real time.
func (w *streamWriter) Write(p []byte) (int, error) {
	if !w.sentHeader {
		w.status = http.StatusOK
		w.sentHeader = true
	}
	w.buffer.Write(p)
	return len(p), nil
}

// Flush implements http.Flusher. The first flush switches the response into
// streaming mode: buffered output is sent as a FrameStream carrying status and
// headers. Callers that never flush get a single FrameResponse at Close.
func (w *streamWriter) Flush() {
	if !w.sentHeader {
		w.status = http.StatusOK
		w.sentHeader = true
	}
	if w.buffer.Len() == 0 {
		return
	}
	w.streaming = true
	w.wroteStream = true
	w.sendStream(w.buffer.Bytes())
	w.buffer.Reset()
}

// Close finalizes the response: a FrameStreamEnd for streaming handlers, or a
// single FrameResponse otherwise.
func (w *streamWriter) Close() error {
	if w.streaming {
		if w.buffer.Len() > 0 {
			w.wroteStream = true
			w.sendStream(w.buffer.Bytes())
			w.buffer.Reset()
		}
		return w.send(Frame{
			Type:  FrameStreamEnd,
			ReqID: w.reqID,
		})
	}
	headers := make(map[string]string, len(w.header))
	for key, values := range w.header {
		if len(values) > 0 {
			headers[key] = values[0]
		}
	}
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.send(Frame{
		Type:    FrameResponse,
		ReqID:   w.reqID,
		Status:  w.status,
		Headers: headers,
		BodyB64: base64.StdEncoding.EncodeToString(w.buffer.Bytes()),
	})
}

func (w *streamWriter) sendStream(chunk []byte) {
	_ = w.send(Frame{
		Type:    FrameStream,
		ReqID:   w.reqID,
		Status:  w.status,
		Headers: headersMap(w.header),
		BodyB64: base64.StdEncoding.EncodeToString(chunk),
	})
}

// send serializes writes to the WebSocket; the agent's write mutex guards
// against concurrent SendEvent calls from the observer goroutine.
func (w *streamWriter) send(frame Frame) error {
	data, err := EncodeFrame(frame)
	if err != nil {
		return err
	}
	w.agent.writeMu.Lock()
	defer w.agent.writeMu.Unlock()
	return w.conn.WriteMessage(websocket.TextMessage, data)
}

func headersMap(header http.Header) map[string]string {
	out := make(map[string]string, len(header))
	for key, values := range header {
		if len(values) > 0 {
			out[key] = values[0]
		}
	}
	return out
}
