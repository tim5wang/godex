package message

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

type testRepository struct {
	messages   map[string]Message
	saveCalls  int
	failSaveAt int
	saveErr    error
	deleteErr  error
}

func newTestRepository() *testRepository {
	return &testRepository{messages: make(map[string]Message)}
}

func (r *testRepository) LoadAll() ([]Message, error) {
	messages := make([]Message, 0, len(r.messages))
	for _, item := range r.messages {
		messages = append(messages, item)
	}
	return messages, nil
}

func (r *testRepository) Save(item Message) error {
	r.saveCalls++
	if r.failSaveAt > 0 && r.saveCalls == r.failSaveAt {
		return r.saveErr
	}
	r.messages[item.ID] = item
	return nil
}

func (r *testRepository) Delete(id string) error {
	if r.deleteErr != nil {
		return r.deleteErr
	}
	delete(r.messages, id)
	return nil
}

func TestSendGeneratesUniqueIDsAndPersistsMessages(t *testing.T) {
	repository := newTestRepository()
	bus := NewBus(repository)

	first := Message{
		Type:    MsgTypeMessage,
		From:    "lead",
		To:      "worker",
		Content: "first",
	}
	second := Message{
		Type:    MsgTypeMessage,
		From:    "lead",
		To:      "worker",
		Content: "second",
	}

	if err := bus.Send(first); err != nil {
		t.Fatalf("send first message: %v", err)
	}
	if err := bus.Send(second); err != nil {
		t.Fatalf("send second message: %v", err)
	}

	inbox := bus.PeekInbox("worker")
	if len(inbox) != 2 {
		t.Fatalf("expected 2 messages in inbox, got %d", len(inbox))
	}
	if inbox[0].ID == "" || inbox[1].ID == "" {
		t.Fatalf("expected generated message IDs, got %+v", inbox)
	}
	if inbox[0].ID == inbox[1].ID {
		t.Fatalf("expected unique message IDs, both were %q", inbox[0].ID)
	}

	if len(repository.messages) != 2 {
		t.Fatalf("expected 2 persisted messages, got %d", len(repository.messages))
	}
}

func TestBroadcastReportsRollbackDeleteFailure(t *testing.T) {
	saveErr := errors.New("save failed")
	deleteErr := errors.New("delete failed")
	repository := newTestRepository()
	repository.failSaveAt = 2
	repository.saveErr = saveErr
	repository.deleteErr = deleteErr
	bus := NewBus(repository)

	err := bus.Broadcast("lead", "status", []string{"alice", "bob"})
	if !errors.Is(err, saveErr) {
		t.Fatalf("expected broadcast error to include save failure, got %v", err)
	}
	if !errors.Is(err, deleteErr) {
		t.Fatalf("expected broadcast error to include rollback delete failure, got %v", err)
	}
	if got := bus.PeekInbox("alice"); len(got) != 0 {
		t.Fatalf("expected failed broadcast not to update in-memory inbox, got %+v", got)
	}
	if len(repository.messages) != 1 {
		t.Fatalf("expected failed rollback to leave one persisted message, got %d", len(repository.messages))
	}
}

func TestLoadIsIdempotentAndReadInboxRemovesPersistedFiles(t *testing.T) {
	repository := newTestRepository()
	sender := NewBus(repository)

	msg := Message{
		Type:    MsgTypeMessage,
		From:    "lead",
		To:      "worker",
		Content: "persist me",
	}
	if err := sender.Send(msg); err != nil {
		t.Fatalf("send message: %v", err)
	}

	receiver := NewBus(repository)
	if err := receiver.Load(); err != nil {
		t.Fatalf("load messages: %v", err)
	}
	if err := receiver.Load(); err != nil {
		t.Fatalf("load messages second time: %v", err)
	}

	inbox := receiver.PeekInbox("worker")
	if len(inbox) != 1 {
		t.Fatalf("expected 1 loaded message, got %d", len(inbox))
	}

	read := receiver.ReadInbox("worker")
	if len(read) != 1 {
		t.Fatalf("expected 1 read message, got %d", len(read))
	}

	if len(repository.messages) != 0 {
		t.Fatalf("expected persisted messages to be removed after read, got %d", len(repository.messages))
	}
}

func TestAckInboxRemovesPreviewedMessagesOnlyAfterExplicitAck(t *testing.T) {
	repository := newTestRepository()
	bus := NewBus(repository)

	if err := bus.Send(Message{
		Type:    MsgTypeMessage,
		From:    "lead",
		To:      "worker",
		Content: "first",
	}); err != nil {
		t.Fatalf("send first message: %v", err)
	}
	if err := bus.Send(Message{
		Type:    MsgTypeMessage,
		From:    "lead",
		To:      "worker",
		Content: "second",
	}); err != nil {
		t.Fatalf("send second message: %v", err)
	}

	preview := bus.PeekInbox("worker")
	if len(preview) != 2 {
		t.Fatalf("expected 2 preview messages, got %d", len(preview))
	}

	bus.AckInbox("worker", preview[:1])

	remaining := bus.PeekInbox("worker")
	if len(remaining) != 1 {
		t.Fatalf("expected 1 remaining message after partial ack, got %d", len(remaining))
	}
	if remaining[0].Content != "second" {
		t.Fatalf("expected second message to remain, got %+v", remaining[0])
	}
}

func TestSendGeneratesShortSequenceIDs(t *testing.T) {
	repository := newTestRepository()
	bus := NewBus(repository)

	for _, content := range []string{"one", "two", "three"} {
		if err := bus.Send(Message{
			Type:    MsgTypeMessage,
			From:    "lead",
			To:      "worker",
			Content: content,
		}); err != nil {
			t.Fatalf("send %q: %v", content, err)
		}
	}

	inbox := bus.PeekInbox("worker")
	if len(inbox) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(inbox))
	}
	for i, msg := range inbox {
		want := fmt.Sprintf("%d", i+1)
		if msg.ID != want {
			t.Fatalf("message %d: expected ID %q, got %q", i, want, msg.ID)
		}
	}
}

func TestLoadRestoresCounterSoRestartNeverCollides(t *testing.T) {
	repository := newTestRepository()
	first := NewBus(repository)
	for _, content := range []string{"one", "two"} {
		if err := first.Send(Message{
			Type:    MsgTypeMessage,
			From:    "lead",
			To:      "worker",
			Content: content,
		}); err != nil {
			t.Fatalf("seed message %q: %v", content, err)
		}
	}

	// Simulate a restart: a fresh bus loads the same persisted messages.
	restarted := NewBus(repository)
	if err := restarted.Load(); err != nil {
		t.Fatalf("load after restart: %v", err)
	}
	if err := restarted.Send(Message{
		Type:    MsgTypeMessage,
		From:    "lead",
		To:      "worker",
		Content: "after restart",
	}); err != nil {
		t.Fatalf("send after restart: %v", err)
	}

	inbox := restarted.PeekInbox("worker")
	if len(inbox) != 3 {
		t.Fatalf("expected 3 messages after restart, got %d", len(inbox))
	}
	if got := inbox[2].ID; got != "3" {
		t.Fatalf("expected new message ID %q after restart, got %q", "3", got)
	}
}

func TestLoadSkipsLegacyNanosecondIDs(t *testing.T) {
	repository := newTestRepository()
	// Seed legacy nanosecond-timestamp IDs (~1e18), the historical format.
	repository.messages["1756987412345678901"] = Message{
		ID:        "1756987412345678901",
		Type:      MsgTypeMessage,
		From:      "lead",
		To:        "worker",
		Content:   "legacy",
		Timestamp: time.Now(),
	}

	bus := NewBus(repository)
	if err := bus.Load(); err != nil {
		t.Fatalf("load legacy message: %v", err)
	}
	if err := bus.Send(Message{
		Type:    MsgTypeMessage,
		From:    "lead",
		To:      "worker",
		Content: "fresh",
	}); err != nil {
		t.Fatalf("send fresh message: %v", err)
	}

	// The legacy ID must not push the counter into 19-digit territory; the
	// new message keeps the short sequence ID "1".
	inbox := bus.PeekInbox("worker")
	for _, msg := range inbox {
		if msg.Content == "fresh" && msg.ID != "1" {
			t.Fatalf("expected fresh message ID %q, got %q (legacy ID polluted the counter)", "1", msg.ID)
		}
	}
}
