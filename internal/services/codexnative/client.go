// Package codexnative connects to an existing Codex app-server. It never owns
// the server, an agent loop, or a Codex rollout file.
package codexnative

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tim5wang/godex/internal/platform/logger"
)

type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *RPCError) Error() string { return e.Message }

func (e *RPCError) Is(target error) bool {
	// Codex currently reports cross-process writer ownership as an invalid
	// request (-32600), with no dedicated error data. Match its precise message
	// shape instead of treating every invalid request as a writer conflict.
	return target == ErrActiveWriter && strings.HasPrefix(e.Message, "thread ") && strings.HasSuffix(e.Message, " already has an active writer")
}

type frame struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *RPCError       `json:"error,omitempty"`
}

type Event struct {
	Sequence uint64          `json:"sequence"`
	Method   string          `json:"method"`
	Params   json.RawMessage `json:"params"`
}

type reply struct {
	frame
	sequence uint64
}

type subscription struct {
	thread string
	events chan Event
}

type approval struct {
	ID     string          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	rawID  json.RawMessage
}

// users includes watchers and waiters, so the keyed lock cannot be removed
// while a leave/join is still using it. refs is protected by gate, users by mu.
type threadWatch struct {
	gate  chan struct{}
	users int
	refs  int
}

type client struct {
	transport rpcTransport
	writeMu   sync.Mutex
	mu        sync.Mutex
	pending   map[string]chan reply
	subs      map[*subscription]struct{}
	approvals map[string]approval
	watches   map[string]*threadWatch
	done      chan struct{}
	once      sync.Once
	ids       atomic.Uint64
	lastUsed  atomic.Int64
	epoch     string
	err       error
}

func newClient(transport rpcTransport) *client {
	c := &client{transport: transport, pending: make(map[string]chan reply), subs: make(map[*subscription]struct{}), approvals: make(map[string]approval), watches: make(map[string]*threadWatch), done: make(chan struct{}), epoch: fmt.Sprint(time.Now().UnixNano())}
	c.lastUsed.Store(time.Now().UnixNano())
	go c.read()
	return c
}

func (c *client) close(err error) {
	c.once.Do(func() {
		c.mu.Lock()
		c.err = err
		close(c.done)
		for sub := range c.subs {
			close(sub.events)
			delete(c.subs, sub)
		}
		c.mu.Unlock()
		_ = c.transport.Close()
	})
}

func (c *client) write(ctx context.Context, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	result := make(chan error, 1)
	go func() {
		c.writeMu.Lock()
		defer c.writeMu.Unlock()
		select {
		case <-c.done:
			result <- io.ErrClosedPipe
			return
		default:
		}
		err := c.transport.WriteMessage(data)
		result <- err
	}()
	select {
	case err := <-result:
		if err != nil {
			c.close(err)
		}
		return err
	case <-ctx.Done():
		// Closing our connection unblocks a stalled write; it does not stop Codex.
		c.close(ctx.Err())
		return ctx.Err()
	case <-c.done:
		return io.ErrClosedPipe
	}
}

func (c *client) call(ctx context.Context, method string, params any, out any) (uint64, error) {
	started := time.Now()
	defer func() {
		if elapsed := time.Since(started); elapsed >= time.Second {
			// Never log parameters or response bodies (prompts, tools and secrets).
			logger.Infof("codex native slow rpc: method=%q duration=%s", method, elapsed.Round(time.Millisecond))
		}
	}()
	c.lastUsed.Store(time.Now().UnixNano())
	id := fmt.Sprintf("godex-%d", c.ids.Add(1))
	rawID, _ := json.Marshal(id)
	ch := make(chan reply, 1)
	c.mu.Lock()
	c.pending[string(rawID)] = ch
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.pending, string(rawID)); c.mu.Unlock() }()
	if err := c.write(ctx, map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return 0, err
	}
	select {
	case response := <-ch:
		if response.Error != nil {
			return response.sequence, response.Error
		}
		if out != nil {
			return response.sequence, json.Unmarshal(response.Result, out)
		}
		return response.sequence, nil
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-c.done:
		return 0, fmt.Errorf("Codex connection closed: %w", c.err)
	}
}

func (c *client) retainWatch(id string) (*threadWatch, func()) {
	c.mu.Lock()
	watch := c.watches[id]
	if watch == nil {
		watch = &threadWatch{gate: make(chan struct{}, 1)}
		c.watches[id] = watch
	}
	watch.users++
	c.mu.Unlock()
	return watch, func() {
		c.mu.Lock()
		watch.users--
		if watch.users == 0 {
			delete(c.watches, id)
		}
		c.mu.Unlock()
	}
}

func eventThread(params json.RawMessage) string {
	var p struct {
		ThreadID string `json:"threadId"`
		Thread   struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	_ = json.Unmarshal(params, &p)
	if p.ThreadID != "" {
		return p.ThreadID
	}
	return p.Thread.ID
}

func (c *client) publish(event Event, thread string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for sub := range c.subs {
		if sub.thread != thread {
			continue
		}
		select {
		case sub.events <- event:
		default:
			// A slow browser must never block the native protocol reader. Closing
			// forces a snapshot resync instead of silently losing terminal events.
			close(sub.events)
			delete(c.subs, sub)
		}
	}
}

func (c *client) read() {
	var sequence uint64
	for {
		data, err := c.transport.ReadMessage()
		if err != nil {
			c.close(err)
			return
		}
		var message frame
		if json.Unmarshal(data, &message) != nil {
			c.close(errors.New("invalid Codex JSON-RPC frame"))
			return
		}
		sequence++
		if message.Method == "" {
			c.mu.Lock()
			ch := c.pending[string(message.ID)]
			c.mu.Unlock()
			if ch != nil {
				select {
				case ch <- reply{frame: message, sequence: sequence}:
				default:
				}
			}
			continue
		}
		thread := eventThread(message.Params)
		if len(message.ID) != 0 {
			switch message.Method {
			case "item/commandExecution/requestApproval", "item/fileChange/requestApproval":
				if thread != "" {
					key := c.epoch + ":" + string(message.ID)
					request := approval{ID: key, Method: message.Method, Params: message.Params, rawID: message.ID}
					c.mu.Lock()
					c.approvals[key] = request
					c.mu.Unlock()
					params, _ := json.Marshal(request)
					c.publish(Event{Sequence: sequence, Method: "codex/approval", Params: params}, thread)
					continue
				}
			}
			// Do not invent answers, approve unknown tools, or leave a request
			// owned by this connection hanging forever.
			go func(id json.RawMessage) {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = c.write(ctx, frame{ID: id, Error: &RPCError{Code: -32601, Message: "Godex does not support this client request; use a compatible native client"}})
			}(message.ID)
			continue
		}
		if message.Method == "serverRequest/resolved" {
			var p struct {
				RequestID json.RawMessage `json:"requestId"`
			}
			_ = json.Unmarshal(message.Params, &p)
			c.mu.Lock()
			key := c.epoch + ":" + string(p.RequestID)
			if req, ok := c.approvals[key]; ok {
				if thread == "" {
					thread = eventThread(req.Params)
				}
				delete(c.approvals, key)
			}
			c.mu.Unlock()
		}
		if thread != "" {
			c.publish(Event{Sequence: sequence, Method: message.Method, Params: message.Params}, thread)
		}
	}
}

func (c *client) subscribe(thread string) (*subscription, func()) {
	sub := &subscription{thread: thread, events: make(chan Event, 512)}
	c.mu.Lock()
	select {
	case <-c.done:
		close(sub.events)
	default:
		c.subs[sub] = struct{}{}
	}
	c.mu.Unlock()
	return sub, func() {
		c.mu.Lock()
		if _, ok := c.subs[sub]; ok {
			delete(c.subs, sub)
			close(sub.events)
		}
		c.mu.Unlock()
	}
}

func (c *client) pendingApprovals(thread string) []approval {
	c.mu.Lock()
	defer c.mu.Unlock()
	result := make([]approval, 0)
	for _, request := range c.approvals {
		if eventThread(request.Params) == thread {
			result = append(result, request)
		}
	}
	return result
}
