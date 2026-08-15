package sessionstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	_ "modernc.org/sqlite"
)

const schemaVersion = 1

type SQLiteStore struct {
	path string
	db   *sql.DB
}

func NewSQLiteStore(path string) (*SQLiteStore, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("missing sqlite path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	store := &SQLiteStore{path: path, db: db}
	if err := store.init(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

// LoadManifest selects only the small manifest blob so session-list requests
// do not copy the potentially multi-megabyte state and timeline columns.
func (s *SQLiteStore) LoadManifest(ctx context.Context, id string) (json.RawMessage, bool, error) {
	row := s.db.QueryRowContext(ctx, `SELECT manifest FROM sessions WHERE session_id = ?`, id)
	var manifest []byte
	if err := row.Scan(&manifest); err != nil {
		if err == sql.ErrNoRows {
			return nil, false, nil
		}
		return nil, false, err
	}
	return raw(manifest), true, nil
}

func (s *SQLiteStore) Load(ctx context.Context, id string) (SessionData, bool, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT session_id, manifest, state, timeline, turns, queue, event_journal, graph,
       checkpoint_id, checkpoint_pointer, checkpoint_manifest, checkpoint_state,
       checkpoint_timeline, checkpoint_turns, checkpoint_queue
FROM sessions WHERE session_id = ?`, id)
	var data SessionData
	var checkpointID sql.NullString
	var manifest, state, timeline, turns, queue, eventJournal, graph []byte
	var cpPointer, cpManifest, cpState, cpTimeline, cpTurns, cpQueue []byte
	err := row.Scan(
		&data.SessionID,
		&manifest,
		&state,
		&timeline,
		&turns,
		&queue,
		&eventJournal,
		&graph,
		&checkpointID,
		&cpPointer,
		&cpManifest,
		&cpState,
		&cpTimeline,
		&cpTurns,
		&cpQueue,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return SessionData{}, false, nil
		}
		return SessionData{}, false, err
	}
	data.Manifest = raw(manifest)
	data.State = raw(state)
	data.Timeline = raw(timeline)
	data.Turns = raw(turns)
	data.Queue = raw(queue)
	data.EventJournal = raw(eventJournal)
	data.Graph = raw(graph)
	if checkpointID.Valid || len(cpPointer) > 0 {
		data.Checkpoint = &CheckpointData{
			ID:       checkpointID.String,
			Pointer:  raw(cpPointer),
			Manifest: raw(cpManifest),
			State:    raw(cpState),
			Timeline: raw(cpTimeline),
			Turns:    raw(cpTurns),
			Queue:    raw(cpQueue),
		}
	}
	return data, true, nil
}

func (s *SQLiteStore) Save(ctx context.Context, data SessionData) error {
	if strings.TrimSpace(data.SessionID) == "" {
		return fmt.Errorf("missing session id")
	}
	if len(data.Manifest) == 0 || len(data.State) == 0 {
		return fmt.Errorf("missing required manifest/state")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var checkpointID *string
	var cpPointer, cpManifest, cpState, cpTimeline, cpTurns, cpQueue []byte
	if data.Checkpoint != nil {
		checkpointID = nullableString(data.Checkpoint.ID)
		cpPointer = nullableBytes(data.Checkpoint.Pointer)
		cpManifest = nullableBytes(data.Checkpoint.Manifest)
		cpState = nullableBytes(data.Checkpoint.State)
		cpTimeline = nullableBytes(data.Checkpoint.Timeline)
		cpTurns = nullableBytes(data.Checkpoint.Turns)
		cpQueue = nullableBytes(data.Checkpoint.Queue)
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO sessions (
  session_id, manifest, state, timeline, turns, queue, event_journal, graph,
  checkpoint_id, checkpoint_pointer, checkpoint_manifest, checkpoint_state,
  checkpoint_timeline, checkpoint_turns, checkpoint_queue
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(session_id) DO UPDATE SET
  manifest = excluded.manifest,
  state = excluded.state,
  timeline = excluded.timeline,
  turns = excluded.turns,
  queue = excluded.queue,
  event_journal = excluded.event_journal,
  graph = excluded.graph,
  checkpoint_id = excluded.checkpoint_id,
  checkpoint_pointer = excluded.checkpoint_pointer,
  checkpoint_manifest = excluded.checkpoint_manifest,
  checkpoint_state = excluded.checkpoint_state,
  checkpoint_timeline = excluded.checkpoint_timeline,
  checkpoint_turns = excluded.checkpoint_turns,
  checkpoint_queue = excluded.checkpoint_queue`,
		data.SessionID,
		[]byte(data.Manifest),
		[]byte(data.State),
		nullableBytes(data.Timeline),
		nullableBytes(data.Turns),
		nullableBytes(data.Queue),
		nullableBytes(data.EventJournal),
		nullableBytes(data.Graph),
		checkpointID,
		cpPointer,
		cpManifest,
		cpState,
		cpTimeline,
		cpTurns,
		cpQueue,
	)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLiteStore) List(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT session_id FROM sessions ORDER BY session_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Strings(ids)
	return ids, nil
}

func (s *SQLiteStore) Delete(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE session_id = ?`, id)
	return err
}

func (s *SQLiteStore) Diagnostics(ctx context.Context) Diagnostics {
	diag := Diagnostics{Backend: string(BackendSQLite), SQLitePath: s.path}
	if err := s.db.PingContext(ctx); err != nil {
		diag.Error = err.Error()
		return diag
	}
	row := s.db.QueryRowContext(ctx, `SELECT value FROM schema_meta WHERE key = 'version'`)
	if err := row.Scan(&diag.SchemaVersion); err != nil {
		diag.Error = err.Error()
		return diag
	}
	diag.Healthy = true
	return diag
}

func (s *SQLiteStore) init(ctx context.Context) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS schema_meta (
			key TEXT PRIMARY KEY,
			value INTEGER NOT NULL
		)`,
		`INSERT INTO schema_meta(key, value) VALUES ('version', 1)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		`CREATE TABLE IF NOT EXISTS sessions (
			session_id TEXT PRIMARY KEY,
			manifest BLOB NOT NULL,
			state BLOB NOT NULL,
			timeline BLOB,
			turns BLOB,
			queue BLOB,
			event_journal BLOB,
			graph BLOB,
			checkpoint_id TEXT,
			checkpoint_pointer BLOB,
			checkpoint_manifest BLOB,
			checkpoint_state BLOB,
			checkpoint_timeline BLOB,
			checkpoint_turns BLOB,
			checkpoint_queue BLOB
		)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

func raw(data []byte) []byte {
	if len(data) == 0 {
		return nil
	}
	return data
}

func nullableBytes(data []byte) []byte {
	if len(data) == 0 {
		return nil
	}
	return data
}

func nullableString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
