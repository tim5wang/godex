package message

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/tim5wang/godex/internal/platform/fsutil"
)

// MsgType represents message type
type MsgType string

const (
	MsgTypeMessage              MsgType = "message"
	MsgTypeBroadcast            MsgType = "broadcast"
	MsgTypeShutdownRequest      MsgType = "shutdown_request"
	MsgTypeShutdownResponse     MsgType = "shutdown_response"
	MsgTypePlanApprovalResponse MsgType = "plan_approval_response"
)

// Message represents an inter-agent message
type Message struct {
	ID        string    `json:"id"`
	Type      MsgType   `json:"type"`
	From      string    `json:"from"`
	To        string    `json:"to"`
	Content   string    `json:"content"`
	Timestamp time.Time `json:"timestamp"`
}

// Bus handles message routing
type Bus struct {
	mu        sync.RWMutex
	inbox     map[string][]Message
	inboxDir  string
	seen      map[string]struct{}
	files     map[string]string
	notifiers []func(Message)
}

// NewBus creates a new message bus
func NewBus(inboxDir string) *Bus {
	return &Bus{
		inbox:    make(map[string][]Message),
		inboxDir: inboxDir,
		seen:     make(map[string]struct{}),
		files:    make(map[string]string),
	}
}

// Send sends a message to a teammate
func (b *Bus) Send(msg Message) error {
	b.normalizeMessage(&msg)
	path := b.messagePath(msg.ID)

	if err := b.saveMessage(msg); err != nil {
		return err
	}

	b.mu.Lock()
	b.inbox[msg.To] = append(b.inbox[msg.To], msg)
	b.seen[msg.ID] = struct{}{}
	b.files[msg.ID] = path
	b.mu.Unlock()

	b.notify(msg)
	return nil
}

// Broadcast sends a message to all teammates
func (b *Bus) Broadcast(from, content string, teammates []string) error {
	if len(teammates) == 0 {
		return nil
	}

	messages := make([]Message, 0, len(teammates))
	for _, teammate := range teammates {
		msg := Message{
			Type:    MsgTypeBroadcast,
			From:    from,
			To:      teammate,
			Content: content,
		}
		b.normalizeMessage(&msg)
		messages = append(messages, msg)
	}

	var savedPaths []string
	for _, msg := range messages {
		if err := b.saveMessage(msg); err != nil {
			for _, path := range savedPaths {
				_ = os.Remove(path)
			}
			return err
		}
		savedPaths = append(savedPaths, b.messagePath(msg.ID))
	}

	b.mu.Lock()
	for _, msg := range messages {
		b.inbox[msg.To] = append(b.inbox[msg.To], msg)
		b.seen[msg.ID] = struct{}{}
		b.files[msg.ID] = b.messagePath(msg.ID)
	}
	b.mu.Unlock()

	for _, msg := range messages {
		b.notify(msg)
	}

	return nil
}

// ReadInbox reads and clears messages for a teammate
func (b *Bus) ReadInbox(name string) []Message {
	b.mu.Lock()
	messages := b.inbox[name]
	b.inbox[name] = nil

	paths := make([]string, 0, len(messages))
	for _, msg := range messages {
		path := b.files[msg.ID]
		if path == "" && msg.ID != "" {
			path = b.messagePath(msg.ID)
		}
		if path != "" {
			paths = append(paths, path)
		}
		delete(b.files, msg.ID)
	}
	b.mu.Unlock()

	for _, path := range paths {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			// Best-effort cleanup; the in-memory queue has already been drained.
			continue
		}
	}

	return messages
}

// PeekInbox reads messages without clearing
func (b *Bus) PeekInbox(name string) []Message {
	b.mu.RLock()
	defer b.mu.RUnlock()

	messages := make([]Message, len(b.inbox[name]))
	copy(messages, b.inbox[name])
	return messages
}

// AckInbox removes previously previewed messages from the inbox and deletes their persisted files.
func (b *Bus) AckInbox(name string, messages []Message) {
	if len(messages) == 0 {
		return
	}

	ackIDs := make(map[string]struct{}, len(messages))
	for _, msg := range messages {
		if msg.ID != "" {
			ackIDs[msg.ID] = struct{}{}
		}
	}
	if len(ackIDs) == 0 {
		return
	}

	b.mu.Lock()
	current := b.inbox[name]
	kept := make([]Message, 0, len(current))
	paths := make([]string, 0, len(messages))
	for _, msg := range current {
		if _, ok := ackIDs[msg.ID]; ok {
			path := b.files[msg.ID]
			if path == "" && msg.ID != "" {
				path = b.messagePath(msg.ID)
			}
			if path != "" {
				paths = append(paths, path)
			}
			delete(b.files, msg.ID)
			delete(b.seen, msg.ID)
			continue
		}
		kept = append(kept, msg)
	}
	b.inbox[name] = kept
	b.mu.Unlock()

	for _, path := range paths {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			continue
		}
	}
}

// Load loads persisted messages from disk
func (b *Bus) Load() error {
	entries, err := os.ReadDir(b.inboxDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(b.inboxDir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		var msg Message
		if err := json.Unmarshal(data, &msg); err == nil {
			b.normalizeMessage(&msg)

			b.mu.Lock()
			if _, ok := b.seen[msg.ID]; !ok {
				b.inbox[msg.To] = append(b.inbox[msg.To], msg)
				b.seen[msg.ID] = struct{}{}
				b.files[msg.ID] = path
			}
			b.mu.Unlock()
		}
	}
	return nil
}

// RegisterNotifier registers a callback for newly delivered messages.
func (b *Bus) RegisterNotifier(fn func(Message)) {
	if fn == nil {
		return
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	b.notifiers = append(b.notifiers, fn)
}

func (b *Bus) normalizeMessage(msg *Message) {
	if msg.ID == "" {
		msg.ID = fmt.Sprintf("%d", time.Now().UnixNano())
	}
	if msg.Timestamp.IsZero() {
		msg.Timestamp = time.Now()
	}
}

func (b *Bus) saveMessage(msg Message) error {
	data, err := json.MarshalIndent(msg, "", "  ")
	if err != nil {
		return err
	}

	return fsutil.WriteFileAtomic(b.messagePath(msg.ID), data, 0644)
}

func (b *Bus) messagePath(id string) string {
	filename := fmt.Sprintf("%s.json", id)
	return filepath.Join(b.inboxDir, filename)
}

func (b *Bus) notify(msg Message) {
	b.mu.RLock()
	notifiers := append([]func(Message){}, b.notifiers...)
	b.mu.RUnlock()

	for _, notifier := range notifiers {
		notifier(msg)
	}
}
