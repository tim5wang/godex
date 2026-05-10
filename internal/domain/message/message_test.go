package message

import (
	"os"
	"testing"
)

func TestSendGeneratesUniqueIDsAndPersistsMessages(t *testing.T) {
	bus := NewBus(t.TempDir())

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

	entries, err := os.ReadDir(bus.inboxDir)
	if err != nil {
		t.Fatalf("read persisted messages: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 persisted message files, got %d", len(entries))
	}
}

func TestLoadIsIdempotentAndReadInboxRemovesPersistedFiles(t *testing.T) {
	dir := t.TempDir()
	sender := NewBus(dir)

	msg := Message{
		Type:    MsgTypeMessage,
		From:    "lead",
		To:      "worker",
		Content: "persist me",
	}
	if err := sender.Send(msg); err != nil {
		t.Fatalf("send message: %v", err)
	}

	receiver := NewBus(dir)
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

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read inbox dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected persisted messages to be removed after read, got %d files", len(entries))
	}
}

func TestAckInboxRemovesPreviewedMessagesOnlyAfterExplicitAck(t *testing.T) {
	dir := t.TempDir()
	bus := NewBus(dir)

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
