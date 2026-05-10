package feishu

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	controlMethod = 0
	dataMethod    = 1
	pingInterval  = 120 * time.Second
)

type binarySocket struct {
	mu        sync.Mutex
	conn      *websocket.Conn
	serviceID int32
}

func newBinarySocket() *binarySocket {
	return &binarySocket{}
}

func (s *binarySocket) Connect(ctx context.Context, endpoint string, handler func(context.Context, []byte) error) error {
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, endpoint, nil)
	if err != nil {
		return fmt.Errorf("dial feishu websocket: %w", err)
	}
	defer conn.Close()

	s.mu.Lock()
	s.conn = conn
	s.serviceID = parseServiceID(endpoint)
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.conn = nil
		s.serviceID = 0
		s.mu.Unlock()
	}()

	pingCtx, cancelPing := context.WithCancel(ctx)
	defer cancelPing()
	go s.pingLoop(pingCtx)

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		_, data, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		frame, err := decodeFrame(data)
		if err != nil {
			continue
		}
		if frame.Method == controlMethod && frame.header("type") == "pong" {
			continue
		}
		if frame.Method != dataMethod {
			continue
		}
		if frameType := frame.header("type"); frameType != "" && frameType != "event" {
			continue
		}

		handlerErr := handler(ctx, frame.Payload)
		if err := s.ack(frame, handlerErr); err != nil {
			return err
		}
	}
}

func (s *binarySocket) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn == nil {
		return nil
	}
	err := s.conn.Close()
	s.conn = nil
	return err
}

func (s *binarySocket) pingLoop(ctx context.Context) {
	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = s.send(frame{
				Method:  controlMethod,
				Service: s.currentServiceID(),
				Headers: []frameHeader{{Key: "type", Value: "ping"}},
			})
		}
	}
}

func (s *binarySocket) ack(original *frame, handlerErr error) error {
	code := 200
	if handlerErr != nil {
		code = 500
	}
	payload, _ := json.Marshal(map[string]int{"code": code})
	return s.send(frame{
		SeqID:   original.SeqID,
		LogID:   original.LogID,
		Method:  dataMethod,
		Service: original.Service,
		Headers: append(copyHeaders(original.Headers), frameHeader{Key: "biz_rt", Value: "0"}),
		Payload: payload,
	})
}

func (s *binarySocket) send(f frame) error {
	s.mu.Lock()
	conn := s.conn
	s.mu.Unlock()
	if conn == nil {
		return fmt.Errorf("feishu websocket not connected")
	}
	return conn.WriteMessage(websocket.BinaryMessage, encodeFrame(f))
}

func (s *binarySocket) currentServiceID() int32 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.serviceID
}

type frameHeader struct {
	Key   string
	Value string
}

type frame struct {
	SeqID   uint64
	LogID   uint64
	Service int32
	Method  int32
	Headers []frameHeader
	Payload []byte
}

func (f *frame) header(key string) string {
	for _, header := range f.Headers {
		if header.Key == key {
			return header.Value
		}
	}
	return ""
}

func encodeFrame(f frame) []byte {
	var out bytes.Buffer
	writeVarintField(&out, 1, f.SeqID)
	writeVarintField(&out, 2, f.LogID)
	writeVarintField(&out, 3, uint64(f.Service))
	writeVarintField(&out, 4, uint64(f.Method))
	for _, header := range f.Headers {
		var entry bytes.Buffer
		writeBytesField(&entry, 1, []byte(header.Key))
		writeBytesField(&entry, 2, []byte(header.Value))
		writeBytesField(&out, 5, entry.Bytes())
	}
	if len(f.Payload) > 0 {
		writeBytesField(&out, 8, f.Payload)
	}
	return out.Bytes()
}

func decodeFrame(data []byte) (*frame, error) {
	reader := bytes.NewReader(data)
	result := &frame{}

	for reader.Len() > 0 {
		tag, err := binary.ReadUvarint(reader)
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		fieldNumber := tag >> 3
		wireType := tag & 0x7

		switch wireType {
		case 0:
			value, err := binary.ReadUvarint(reader)
			if err != nil {
				return nil, err
			}
			switch fieldNumber {
			case 1:
				result.SeqID = value
			case 2:
				result.LogID = value
			case 3:
				result.Service = int32(value)
			case 4:
				result.Method = int32(value)
			}
		case 2:
			length, err := binary.ReadUvarint(reader)
			if err != nil {
				return nil, err
			}
			buf := make([]byte, length)
			if _, err := io.ReadFull(reader, buf); err != nil {
				return nil, err
			}
			switch fieldNumber {
			case 5:
				header, err := decodeHeader(buf)
				if err == nil {
					result.Headers = append(result.Headers, header)
				}
			case 8:
				result.Payload = buf
			}
		default:
			return nil, fmt.Errorf("unsupported frame wire type %d", wireType)
		}
	}

	return result, nil
}

func decodeHeader(data []byte) (frameHeader, error) {
	reader := bytes.NewReader(data)
	header := frameHeader{}
	for reader.Len() > 0 {
		tag, err := binary.ReadUvarint(reader)
		if err != nil {
			if err == io.EOF {
				break
			}
			return header, err
		}
		if tag&0x7 != 2 {
			return header, fmt.Errorf("unsupported header tag %d", tag)
		}
		length, err := binary.ReadUvarint(reader)
		if err != nil {
			return header, err
		}
		buf := make([]byte, length)
		if _, err := io.ReadFull(reader, buf); err != nil {
			return header, err
		}
		switch tag >> 3 {
		case 1:
			header.Key = string(buf)
		case 2:
			header.Value = string(buf)
		}
	}
	return header, nil
}

func writeVarintField(out *bytes.Buffer, fieldNumber int, value uint64) {
	if value == 0 {
		return
	}
	writeUvarint(out, uint64(fieldNumber<<3))
	writeUvarint(out, value)
}

func writeBytesField(out *bytes.Buffer, fieldNumber int, data []byte) {
	writeUvarint(out, uint64(fieldNumber<<3|2))
	writeUvarint(out, uint64(len(data)))
	_, _ = out.Write(data)
}

func writeUvarint(out *bytes.Buffer, value uint64) {
	var buf [10]byte
	n := binary.PutUvarint(buf[:], value)
	_, _ = out.Write(buf[:n])
}

func copyHeaders(headers []frameHeader) []frameHeader {
	cloned := make([]frameHeader, len(headers))
	copy(cloned, headers)
	return cloned
}

func parseServiceID(endpoint string) int32 {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return 0
	}
	raw := parsed.Query().Get("service_id")
	if raw == "" {
		return 0
	}
	value, err := strconv.ParseInt(raw, 10, 32)
	if err != nil {
		return 0
	}
	return int32(value)
}
