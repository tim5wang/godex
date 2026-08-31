package message

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
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

// Repository persists messages without exposing a storage backend to the
// domain bus.
type Repository interface {
	LoadAll() ([]Message, error)
	Save(Message) error
	Delete(id string) error
}

// Bus handles message routing.
type Bus struct {
	mu         sync.RWMutex
	inbox      map[string][]Message
	seen       map[string]struct{}
	repository Repository
	notifiers  []func(Message)
	nextID     uint64
}

// NewBus creates a new message bus backed by repository. A nil repository
// creates an in-memory bus.
func NewBus(repository Repository) *Bus {
	return &Bus{
		inbox:      make(map[string][]Message),
		seen:       make(map[string]struct{}),
		repository: repository,
	}
}

// Send sends a message to a teammate
func (b *Bus) Send(msg Message) error {
	b.normalizeMessage(&msg)

	if err := b.saveMessage(msg); err != nil {
		return err
	}

	b.mu.Lock()
	b.inbox[msg.To] = append(b.inbox[msg.To], msg)
	b.seen[msg.ID] = struct{}{}
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

	var savedIDs []string
	for _, msg := range messages {
		if err := b.saveMessage(msg); err != nil {
			rollbackErrs := []error{err}
			for _, id := range savedIDs {
				if deleteErr := b.deleteMessage(id); deleteErr != nil {
					rollbackErrs = append(rollbackErrs, fmt.Errorf("rollback delete message %q: %w", id, deleteErr))
				}
			}
			return errors.Join(rollbackErrs...)
		}
		savedIDs = append(savedIDs, msg.ID)
	}

	b.mu.Lock()
	for _, msg := range messages {
		b.inbox[msg.To] = append(b.inbox[msg.To], msg)
		b.seen[msg.ID] = struct{}{}
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

	ids := make([]string, 0, len(messages))
	for _, msg := range messages {
		if msg.ID != "" {
			ids = append(ids, msg.ID)
		}
	}
	b.mu.Unlock()

	for _, id := range ids {
		b.deleteMessage(id)
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
	ids := make([]string, 0, len(messages))
	for _, msg := range current {
		if _, ok := ackIDs[msg.ID]; ok {
			if msg.ID != "" {
				ids = append(ids, msg.ID)
			}
			delete(b.seen, msg.ID)
			continue
		}
		kept = append(kept, msg)
	}
	b.inbox[name] = kept
	b.mu.Unlock()

	for _, id := range ids {
		b.deleteMessage(id)
	}
}

// Load loads persisted messages from the repository.
func (b *Bus) Load() error {
	if b.repository == nil {
		return nil
	}
	messages, err := b.repository.LoadAll()
	if err != nil {
		return err
	}
	for _, msg := range messages {
		b.normalizeMessage(&msg)

		b.mu.Lock()
		if _, ok := b.seen[msg.ID]; !ok {
			b.inbox[msg.To] = append(b.inbox[msg.To], msg)
			b.seen[msg.ID] = struct{}{}
		}
		b.mu.Unlock()
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
		msg.ID = fmt.Sprintf("%d-%d", time.Now().UnixNano(), atomic.AddUint64(&b.nextID, 1))
	}
	if msg.Timestamp.IsZero() {
		msg.Timestamp = time.Now()
	}
}

func (b *Bus) saveMessage(msg Message) error {
	if b.repository == nil {
		return nil
	}
	return b.repository.Save(msg)
}

func (b *Bus) deleteMessage(id string) error {
	if b.repository == nil {
		return nil
	}
	return b.repository.Delete(id)
}

func (b *Bus) notify(msg Message) {
	b.mu.RLock()
	notifiers := append([]func(Message){}, b.notifiers...)
	b.mu.RUnlock()

	for _, notifier := range notifiers {
		notifier(msg)
	}
}
