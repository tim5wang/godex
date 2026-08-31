package historysearch

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/domain/history"

	_ "modernc.org/sqlite"
)

const SidecarDBFileName = "history_search.db"

type historySourceState struct {
	SourceKey      string
	SourceKind     string
	SourceRef      string
	SessionID      string
	SessionTitle   string
	Path           string
	MTimeUnixNano  int64
	SizeBytes      int64
	TranscriptRefs []string
}

type historyIndexSource struct {
	SourceKey      string
	SourceKind     string
	SourceRef      string
	SessionID      string
	SessionTitle   string
	Path           string
	Timestamp      time.Time
	MTimeUnixNano  int64
	SizeBytes      int64
	TranscriptRefs []string
}

type historySidecarQuery struct {
	Query      string
	Role       string
	SourceKind string
	SourceRefs []string
}

func (s *Service) sessionArchiveEntriesFromSidecar(current history.Current, sessionID, query, role string) ([]searchableEntry, bool, error) {
	archive := s.collectSessionArchiveRefs(current, sessionID)
	if len(archive.normalizedRefs) == 0 {
		return nil, true, nil
	}

	db, err := s.openSidecar()
	if err != nil {
		return nil, false, nil
	}
	defer db.Close()

	if err := s.reconcileTranscriptSidecar(db, archive.normalizedRefs, archive.owner); err != nil {
		return nil, false, nil
	}
	entries, err := queryHistorySidecar(db, historySidecarQuery{
		Query:      query,
		Role:       role,
		SourceKind: "transcript",
		SourceRefs: archive.normalizedRefs,
	})
	if err != nil {
		return nil, false, nil
	}
	return entries, true, nil
}

func (s *Service) allArchiveEntriesFromSidecar(query, role string) ([]searchableEntry, bool, error) {
	db, err := s.openSidecar()
	if err != nil {
		return nil, false, nil
	}
	defer db.Close()

	if err := s.reconcileAllArchiveSidecar(db); err != nil {
		return nil, false, nil
	}
	entries, err := queryHistorySidecar(db, historySidecarQuery{Query: query, Role: role})
	if err != nil {
		return nil, false, nil
	}
	return entries, true, nil
}

func (s *Service) openSidecar() (*sql.DB, error) {
	path := s.sidecarPath()
	if path == "" {
		return nil, fmt.Errorf("history sidecar path unavailable")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA busy_timeout = 5000`); err != nil {
		db.Close()
		return nil, err
	}
	if err := ensureHistorySidecarSchema(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func (s *Service) sidecarPath() string {
	if strings.TrimSpace(s.sessionsDir) != "" {
		return filepath.Join(filepath.Dir(s.sessionsDir), SidecarDBFileName)
	}
	if strings.TrimSpace(s.transcriptsDir) != "" {
		return filepath.Join(filepath.Dir(s.transcriptsDir), SidecarDBFileName)
	}
	return ""
}

func ensureHistorySidecarSchema(db *sql.DB) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS history_sources (
			source_key TEXT PRIMARY KEY,
			source_kind TEXT NOT NULL,
			source_ref TEXT NOT NULL,
			session_id TEXT NOT NULL,
			session_title TEXT NOT NULL,
			path TEXT NOT NULL,
			mtime_unix_nano INTEGER NOT NULL,
			size_bytes INTEGER NOT NULL,
			transcript_refs TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS history_entries (
			id TEXT PRIMARY KEY,
			source_key TEXT NOT NULL,
			source_kind TEXT NOT NULL,
			source_ref TEXT NOT NULL,
			session_id TEXT NOT NULL,
			session_title TEXT NOT NULL,
			timestamp TEXT NOT NULL,
			role TEXT NOT NULL,
			ordinal INTEGER NOT NULL,
			text TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_history_entries_source ON history_entries(source_kind, source_ref)`,
		`CREATE INDEX IF NOT EXISTS idx_history_entries_role ON history_entries(role)`,
		`CREATE INDEX IF NOT EXISTS idx_history_entries_timestamp ON history_entries(timestamp DESC)`,
		`CREATE VIRTUAL TABLE IF NOT EXISTS history_fts USING fts5(
			id UNINDEXED,
			text,
			session_title,
			tokenize='unicode61'
		)`,
	}
	for _, stmt := range statements {
		if _, err := db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) reconcileAllArchiveSidecar(db *sql.DB) error {
	current, err := loadHistorySourceState(db)
	if err != nil {
		return err
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	desired := make(map[string]struct{})
	owners := make(map[string]transcriptOwner)

	if strings.TrimSpace(s.sessionsDir) != "" {
		dirs, err := os.ReadDir(s.sessionsDir)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		for _, dir := range dirs {
			if !dir.IsDir() {
				continue
			}
			sessionID := strings.TrimSpace(dir.Name())
			source, messages, refs, ok, err := s.sessionStateIndexSource(sessionID, current[historySourceKey("session_state", sessionID)])
			if err != nil || !ok {
				continue
			}
			desired[source.SourceKey] = struct{}{}
			if messages != nil {
				entries := visibleEntries("session_state", sessionID, source.SessionTitle, source.Timestamp, messages)
				if err := upsertHistorySourceTx(tx, source, entries); err != nil {
					return err
				}
			}
			if len(refs) == 0 {
				refs = current[source.SourceKey].TranscriptRefs
			}
			for _, ref := range refs {
				owners[ref] = transcriptOwner{sessionID: sessionID, title: source.SessionTitle}
			}
		}
	}

	if strings.TrimSpace(s.transcriptsDir) != "" {
		files, err := os.ReadDir(s.transcriptsDir)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		for _, file := range files {
			if file.IsDir() || !strings.HasPrefix(file.Name(), "transcript_") || !strings.HasSuffix(file.Name(), ".json") {
				continue
			}
			ref := normalizeTranscriptRef(file.Name())
			owner := owners[ref]
			source, messages, ok, err := s.transcriptIndexSource(ref, owner, current[historySourceKey("transcript", ref)])
			if err != nil || !ok {
				continue
			}
			desired[source.SourceKey] = struct{}{}
			if messages != nil {
				title := source.SessionTitle
				if title == "" {
					title = deriveSessionTitle(messages)
					source.SessionTitle = title
				}
				entries := visibleEntries("transcript", source.SessionID, title, source.Timestamp, messages)
				if err := upsertHistorySourceTx(tx, source, entries); err != nil {
					return err
				}
			}
		}
	}

	for key := range current {
		if _, ok := desired[key]; ok {
			continue
		}
		if err := deleteHistorySourceRowsTx(tx, key); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (s *Service) reconcileTranscriptSidecar(db *sql.DB, refs []string, owner transcriptOwner) error {
	current, err := loadHistorySourceState(db)
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, ref := range refs {
		source, messages, ok, err := s.transcriptIndexSource(ref, owner, current[historySourceKey("transcript", ref)])
		if err != nil || !ok || messages == nil {
			continue
		}
		title := source.SessionTitle
		if title == "" {
			title = deriveSessionTitle(messages)
			source.SessionTitle = title
		}
		entries := visibleEntries("transcript", source.SessionID, title, source.Timestamp, messages)
		if err := upsertHistorySourceTx(tx, source, entries); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Service) sessionStateIndexSource(sessionID string, current historySourceState) (historyIndexSource, []protocol.Message, []string, bool, error) {
	dir := filepath.Join(s.sessionsDir, sessionID)
	mtime, size, ok, err := combinedSessionStat(dir)
	if err != nil || !ok {
		return historyIndexSource{}, nil, nil, false, err
	}
	source := historyIndexSource{
		SourceKey:      historySourceKey("session_state", sessionID),
		SourceKind:     "session_state",
		SourceRef:      sessionID,
		SessionID:      sessionID,
		SessionTitle:   current.SessionTitle,
		Path:           dir,
		Timestamp:      unixNanoTime(current.MTimeUnixNano),
		MTimeUnixNano:  mtime.UnixNano(),
		SizeBytes:      size,
		TranscriptRefs: append([]string{}, current.TranscriptRefs...),
	}
	if !historySourceNeedsRefresh(source, current) {
		return source, nil, source.TranscriptRefs, true, nil
	}

	state, manifest, err := s.readSessionFiles(sessionID)
	if err != nil {
		return historyIndexSource{}, nil, nil, false, err
	}
	title, timestamp, refs := sessionArchiveMetadata(state, manifest, mtime)
	source.SessionTitle = title
	source.Timestamp = timestamp
	source.TranscriptRefs = refs
	return source, state.Messages, source.TranscriptRefs, true, nil
}

func (s *Service) transcriptIndexSource(ref string, owner transcriptOwner, current historySourceState) (historyIndexSource, []protocol.Message, bool, error) {
	ref = normalizeTranscriptRef(ref)
	path := s.transcriptPath(ref)
	if strings.TrimSpace(path) == "" {
		return historyIndexSource{}, nil, false, nil
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return historyIndexSource{}, nil, false, nil
		}
		return historyIndexSource{}, nil, false, err
	}
	source := historyIndexSource{
		SourceKey:     historySourceKey("transcript", ref),
		SourceKind:    "transcript",
		SourceRef:     ref,
		SessionID:     strings.TrimSpace(owner.sessionID),
		SessionTitle:  strings.TrimSpace(owner.title),
		Path:          path,
		Timestamp:     info.ModTime(),
		MTimeUnixNano: info.ModTime().UnixNano(),
		SizeBytes:     info.Size(),
	}
	if !historySourceNeedsRefresh(source, current) {
		return source, nil, true, nil
	}
	messages, _, err := readTranscriptMessages(path)
	if err != nil {
		return historyIndexSource{}, nil, false, err
	}
	return source, messages, true, nil
}

func historySourceNeedsRefresh(source historyIndexSource, current historySourceState) bool {
	return current.SourceKey == "" ||
		current.SourceKind != source.SourceKind ||
		current.SourceRef != source.SourceRef ||
		current.SessionID != source.SessionID ||
		current.SessionTitle != source.SessionTitle ||
		current.Path != source.Path ||
		current.MTimeUnixNano != source.MTimeUnixNano ||
		current.SizeBytes != source.SizeBytes ||
		strings.Join(current.TranscriptRefs, "\x00") != strings.Join(source.TranscriptRefs, "\x00")
}

func combinedSessionStat(dir string) (time.Time, int64, bool, error) {
	statePath := filepath.Join(dir, "state.json")
	stateInfo, err := os.Stat(statePath)
	if err != nil {
		if os.IsNotExist(err) {
			return time.Time{}, 0, false, nil
		}
		return time.Time{}, 0, false, err
	}
	mtime := stateInfo.ModTime()
	size := stateInfo.Size()
	if manifestInfo, err := os.Stat(filepath.Join(dir, "manifest.json")); err == nil {
		if manifestInfo.ModTime().After(mtime) {
			mtime = manifestInfo.ModTime()
		}
		size += manifestInfo.Size()
	} else if err != nil && !os.IsNotExist(err) {
		return time.Time{}, 0, false, err
	}
	return mtime, size, true, nil
}

func loadHistorySourceState(db *sql.DB) (map[string]historySourceState, error) {
	rows, err := db.Query(`SELECT source_key, source_kind, source_ref, session_id, session_title, path, mtime_unix_nano, size_bytes, transcript_refs FROM history_sources`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]historySourceState)
	for rows.Next() {
		var state historySourceState
		var refsJSON string
		if err := rows.Scan(
			&state.SourceKey,
			&state.SourceKind,
			&state.SourceRef,
			&state.SessionID,
			&state.SessionTitle,
			&state.Path,
			&state.MTimeUnixNano,
			&state.SizeBytes,
			&refsJSON,
		); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(refsJSON), &state.TranscriptRefs)
		state.TranscriptRefs = normalizeTranscriptRefs(state.TranscriptRefs)
		result[state.SourceKey] = state
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func upsertHistorySourceTx(tx *sql.Tx, source historyIndexSource, entries []searchableEntry) error {
	if err := deleteHistorySourceRowsTx(tx, source.SourceKey); err != nil {
		return err
	}
	refsJSON, err := json.Marshal(normalizeTranscriptRefs(source.TranscriptRefs))
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`
		INSERT INTO history_sources(source_key, source_kind, source_ref, session_id, session_title, path, mtime_unix_nano, size_bytes, transcript_refs)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		source.SourceKey,
		source.SourceKind,
		source.SourceRef,
		source.SessionID,
		source.SessionTitle,
		source.Path,
		source.MTimeUnixNano,
		source.SizeBytes,
		string(refsJSON),
	); err != nil {
		return err
	}

	for i, entry := range entries {
		id := fmt.Sprintf("%s:%06d", source.SourceKey, i)
		timestamp := entry.timestamp.UTC().Format(time.RFC3339Nano)
		if entry.timestamp.IsZero() {
			timestamp = ""
		}
		if _, err := tx.Exec(`
			INSERT INTO history_entries(id, source_key, source_kind, source_ref, session_id, session_title, timestamp, role, ordinal, text)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`,
			id,
			source.SourceKey,
			entry.sourceKind,
			source.SourceRef,
			entry.sessionID,
			entry.sessionTitle,
			timestamp,
			entry.role,
			i,
			entry.text,
		); err != nil {
			return err
		}
		if _, err := tx.Exec(`
			INSERT INTO history_fts(id, text, session_title)
			VALUES(?, ?, ?)
		`, id, entry.text, entry.sessionTitle); err != nil {
			return err
		}
	}
	return nil
}

func deleteHistorySourceRowsTx(tx *sql.Tx, sourceKey string) error {
	rows, err := tx.Query(`SELECT id FROM history_entries WHERE source_key = ?`, sourceKey)
	if err != nil {
		return err
	}
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	for _, id := range ids {
		if _, err := tx.Exec(`DELETE FROM history_fts WHERE id = ?`, id); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`DELETE FROM history_entries WHERE source_key = ?`, sourceKey); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM history_sources WHERE source_key = ?`, sourceKey); err != nil {
		return err
	}
	return nil
}

func queryHistorySidecar(db *sql.DB, req historySidecarQuery) ([]searchableEntry, error) {
	ids := make(map[string]struct{})
	for _, fn := range []func(*sql.DB, historySidecarQuery) ([]string, error){
		queryHistoryFTSIDs,
		queryHistoryLikeIDs,
	} {
		matched, err := fn(db, req)
		if err != nil {
			return nil, err
		}
		for _, id := range matched {
			ids[id] = struct{}{}
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	return loadHistoryEntriesByID(db, ids)
}

func queryHistoryFTSIDs(db *sql.DB, req historySidecarQuery) ([]string, error) {
	ftsQuery := buildHistoryFTSQuery(req.Query)
	if ftsQuery == "" {
		return nil, nil
	}
	where, args := historySidecarFilterSQL(req)
	args = append([]any{ftsQuery}, args...)
	rows, err := db.Query(`
		SELECT e.id
		FROM history_fts f
		JOIN history_entries e ON e.id = f.id
		WHERE history_fts MATCH ?`+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanIDRows(rows)
}

func queryHistoryLikeIDs(db *sql.DB, req historySidecarQuery) ([]string, error) {
	patterns := historyLikePatterns(req.Query)
	if len(patterns) == 0 {
		return nil, nil
	}
	where, args := historySidecarFilterSQL(req)
	like := make([]string, 0, len(patterns))
	for _, pattern := range patterns {
		like = append(like, `LOWER(e.text) LIKE ? ESCAPE '\'`)
		args = append(args, pattern)
	}
	rows, err := db.Query(`
		SELECT e.id
		FROM history_entries e
		WHERE 1 = 1`+where+` AND (`+strings.Join(like, " OR ")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanIDRows(rows)
}

func historySidecarFilterSQL(req historySidecarQuery) (string, []any) {
	clauses := make([]string, 0, 3)
	args := make([]any, 0)
	if req.Role != "" && req.Role != "any" {
		clauses = append(clauses, `e.role = ?`)
		args = append(args, req.Role)
	}
	if strings.TrimSpace(req.SourceKind) != "" {
		clauses = append(clauses, `e.source_kind = ?`)
		args = append(args, strings.TrimSpace(req.SourceKind))
	}
	if len(req.SourceRefs) > 0 {
		placeholders := make([]string, 0, len(req.SourceRefs))
		for _, ref := range req.SourceRefs {
			placeholders = append(placeholders, "?")
			args = append(args, normalizeTranscriptRef(ref))
		}
		clauses = append(clauses, `e.source_ref IN (`+strings.Join(placeholders, ",")+`)`)
	}
	if len(clauses) == 0 {
		return "", args
	}
	return " AND " + strings.Join(clauses, " AND "), args
}

func scanIDRows(rows *sql.Rows) ([]string, error) {
	ids := make([]string, 0)
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
	return ids, nil
}

func loadHistoryEntriesByID(db *sql.DB, ids map[string]struct{}) ([]searchableEntry, error) {
	keys := make([]string, 0, len(ids))
	for id := range ids {
		keys = append(keys, id)
	}
	entries := make([]searchableEntry, 0, len(keys))
	const chunkSize = 500
	for start := 0; start < len(keys); start += chunkSize {
		end := start + chunkSize
		if end > len(keys) {
			end = len(keys)
		}
		chunk := keys[start:end]
		placeholders := make([]string, 0, len(chunk))
		args := make([]any, 0, len(chunk))
		for _, id := range chunk {
			placeholders = append(placeholders, "?")
			args = append(args, id)
		}
		rows, err := db.Query(`
			SELECT source_kind, session_id, session_title, timestamp, role, text
			FROM history_entries
			WHERE id IN (`+strings.Join(placeholders, ",")+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var entry searchableEntry
			var timestamp string
			if err := rows.Scan(&entry.sourceKind, &entry.sessionID, &entry.sessionTitle, &timestamp, &entry.role, &entry.text); err != nil {
				rows.Close()
				return nil, err
			}
			if ts, err := time.Parse(time.RFC3339Nano, timestamp); err == nil {
				entry.timestamp = ts
			}
			entries = append(entries, entry)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return entries, nil
}

func buildHistoryFTSQuery(query string) string {
	terms := queryTerms(query)
	if len(terms) == 0 {
		return ""
	}
	clauses := make([]string, 0, len(terms))
	for _, term := range terms {
		escaped := strings.ReplaceAll(term, `"`, `""`)
		clauses = append(clauses, fmt.Sprintf(`"%s"`, escaped))
	}
	return strings.Join(clauses, " OR ")
}

func historyLikePatterns(query string) []string {
	values := append([]string{normalizeHistorySearchText(query)}, queryTerms(query)...)
	values = uniqueStrings(values)
	patterns := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		patterns = append(patterns, "%"+escapeLike(value)+"%")
	}
	return patterns
}

func escapeLike(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `%`, `\%`)
	value = strings.ReplaceAll(value, `_`, `\_`)
	return value
}

func historySourceKey(kind, ref string) string {
	return strings.TrimSpace(kind) + ":" + strings.TrimSpace(ref)
}

func normalizeTranscriptRefs(refs []string) []string {
	normalized := make([]string, 0, len(refs))
	for _, ref := range refs {
		if value := normalizeTranscriptRef(ref); value != "" {
			normalized = append(normalized, value)
		}
	}
	return uniqueStrings(normalized)
}

func normalizeTranscriptRef(ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	return filepath.Base(ref)
}

func unixNanoTime(value int64) time.Time {
	if value <= 0 {
		return time.Time{}
	}
	return time.Unix(0, value).UTC()
}
