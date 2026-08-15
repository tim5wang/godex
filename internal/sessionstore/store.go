package sessionstore

import (
	"context"
	"encoding/json"
	"fmt"
)

type Backend string

const (
	BackendJSON   Backend = "json"
	BackendSQLite Backend = "sqlite"
)

type SessionData struct {
	SessionID    string
	Manifest     json.RawMessage
	State        json.RawMessage
	Timeline     json.RawMessage
	Turns        json.RawMessage
	Queue        json.RawMessage
	EventJournal json.RawMessage
	Graph        json.RawMessage
	Checkpoint   *CheckpointData
}

type CheckpointData struct {
	ID       string
	Pointer  json.RawMessage
	Manifest json.RawMessage
	State    json.RawMessage
	Timeline json.RawMessage
	Turns    json.RawMessage
	Queue    json.RawMessage
}

type Diagnostics struct {
	Backend       string
	Path          string
	SQLitePath    string
	SchemaVersion int
	Healthy       bool
	Error         string
}

type Store interface {
	Load(ctx context.Context, id string) (SessionData, bool, error)
	Save(ctx context.Context, data SessionData) error
	List(ctx context.Context) ([]string, error)
	Delete(ctx context.Context, id string) error
	Diagnostics(ctx context.Context) Diagnostics
}

// ManifestLoader is the optional lightweight read path used by session lists.
// Loading a list item must not pull large state, timeline, graph, and checkpoint
// blobs for every session.
type ManifestLoader interface {
	LoadManifest(ctx context.Context, id string) (json.RawMessage, bool, error)
}

func CopySession(ctx context.Context, dst Store, src Store, sessionID string) error {
	data, ok, err := src.Load(ctx, sessionID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("session %q not found", sessionID)
	}
	return dst.Save(ctx, data)
}
