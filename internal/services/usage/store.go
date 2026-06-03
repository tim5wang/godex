package usage

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", h)
}

// SQLiteStore implements Store using a local SQLite database.
type SQLiteStore struct {
	path string
	db   *sql.DB
}

// NewSQLiteStore creates or loads the SQLite-backed usage store under stateDir.
func NewSQLiteStore(stateDir string) (*SQLiteStore, error) {
	if strings.TrimSpace(stateDir) == "" {
		return nil, fmt.Errorf("missing usage store directory")
	}
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		return nil, fmt.Errorf("mkdir usage store: %w", err)
	}
	path := filepath.Join(stateDir, "usage-gateway.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open usage sqlite store: %w", err)
	}
	store := &SQLiteStore{path: path, db: db}
	if err := store.init(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *SQLiteStore) init() error {
	stmts := []string{
		`PRAGMA journal_mode=WAL`,
		`CREATE TABLE IF NOT EXISTS usage_keys (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			key_hash TEXT NOT NULL,
			key_prefix TEXT NOT NULL,
			enabled INTEGER NOT NULL,
			budget_credits REAL NOT NULL,
			warning_threshold REAL NOT NULL,
			allowed_models TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_usage_keys_hash ON usage_keys(key_hash)`,
		`CREATE TABLE IF NOT EXISTS usage_models (
			id TEXT PRIMARY KEY,
			public_model TEXT NOT NULL UNIQUE,
			target_profile_id TEXT NOT NULL,
			target_model TEXT NOT NULL,
			credit_weight REAL NOT NULL,
			enabled INTEGER NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS usage_calls (
			id TEXT PRIMARY KEY,
			timestamp TEXT NOT NULL,
			api_key_id TEXT NOT NULL,
			public_model TEXT NOT NULL,
			target_profile_id TEXT NOT NULL,
			target_model TEXT NOT NULL,
			input_tokens INTEGER NOT NULL,
			output_tokens INTEGER NOT NULL,
			cache_read_tokens INTEGER NOT NULL,
			cache_write_tokens INTEGER NOT NULL,
			billable_tokens INTEGER NOT NULL,
			credit_weight REAL NOT NULL,
			credits REAL NOT NULL,
			estimated INTEGER NOT NULL,
			status TEXT NOT NULL,
			error TEXT NOT NULL,
			latency_ms INTEGER NOT NULL,
			source_channel TEXT NOT NULL,
			session_id TEXT NOT NULL,
			turn_id TEXT NOT NULL,
			job_id TEXT NOT NULL,
			error_code TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_usage_calls_day_key ON usage_calls(substr(timestamp, 1, 10), api_key_id)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("initialize usage sqlite store: %w", err)
		}
	}
	return nil
}

func (s *SQLiteStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *SQLiteStore) ListKeys() ([]ProxyAPIKey, error) {
	rows, err := s.db.Query(`SELECT id, name, key_prefix, enabled, budget_credits, warning_threshold, allowed_models, created_at, updated_at FROM usage_keys ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProxyAPIKey
	for rows.Next() {
		var key ProxyAPIKey
		var enabled int
		var allowed string
		var created, updated string
		if err := rows.Scan(&key.ID, &key.Name, &key.KeyPrefix, &enabled, &key.BudgetCredits, &key.WarningThreshold, &allowed, &created, &updated); err != nil {
			return nil, err
		}
		key.Enabled = enabled != 0
		key.AllowedModels = decodeStringSlice(allowed)
		key.CreatedAt = parseTime(created)
		key.UpdatedAt = parseTime(updated)
		out = append(out, key)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) GetKey(id string) (*ProxyAPIKey, error) {
	return s.getKey(`id = ?`, id)
}

func (s *SQLiteStore) GetKeyByHash(hash string) (*ProxyAPIKey, error) {
	return s.getKey(`key_hash = ?`, hash)
}

func (s *SQLiteStore) getKey(where string, arg string) (*ProxyAPIKey, error) {
	row := s.db.QueryRow(`SELECT id, name, key_hash, key_prefix, enabled, budget_credits, warning_threshold, allowed_models, created_at, updated_at FROM usage_keys WHERE `+where, arg)
	var key ProxyAPIKey
	var enabled int
	var allowed string
	var created, updated string
	if err := row.Scan(&key.ID, &key.Name, &key.KeyHash, &key.KeyPrefix, &enabled, &key.BudgetCredits, &key.WarningThreshold, &allowed, &created, &updated); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("key not found")
		}
		return nil, err
	}
	key.Enabled = enabled != 0
	key.AllowedModels = decodeStringSlice(allowed)
	key.CreatedAt = parseTime(created)
	key.UpdatedAt = parseTime(updated)
	return &key, nil
}

func (s *SQLiteStore) CreateKey(key *ProxyAPIKey) error {
	_, err := s.db.Exec(`INSERT INTO usage_keys (id, name, key_hash, key_prefix, enabled, budget_credits, warning_threshold, allowed_models, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		key.ID, key.Name, key.KeyHash, key.KeyPrefix, boolInt(key.Enabled), key.BudgetCredits, key.WarningThreshold, encodeStringSlice(key.AllowedModels), formatTime(key.CreatedAt), formatTime(key.UpdatedAt))
	return err
}

func (s *SQLiteStore) UpdateKey(key *ProxyAPIKey) error {
	result, err := s.db.Exec(`UPDATE usage_keys SET name = ?, key_hash = ?, key_prefix = ?, enabled = ?, budget_credits = ?, warning_threshold = ?, allowed_models = ?, created_at = ?, updated_at = ? WHERE id = ?`,
		key.Name, key.KeyHash, key.KeyPrefix, boolInt(key.Enabled), key.BudgetCredits, key.WarningThreshold, encodeStringSlice(key.AllowedModels), formatTime(key.CreatedAt), formatTime(key.UpdatedAt), key.ID)
	return resultError(result, err, "key not found: "+key.ID)
}

func (s *SQLiteStore) ListModels() ([]ProxyModel, error) {
	rows, err := s.db.Query(`SELECT id, public_model, target_profile_id, target_model, credit_weight, enabled, created_at, updated_at FROM usage_models ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProxyModel
	for rows.Next() {
		model, err := scanModel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, model)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) GetModel(id string) (*ProxyModel, error) {
	return s.getModel(`id = ?`, id)
}

func (s *SQLiteStore) GetModelByPublicName(name string) (*ProxyModel, error) {
	return s.getModel(`public_model = ?`, name)
}

func (s *SQLiteStore) getModel(where string, arg string) (*ProxyModel, error) {
	row := s.db.QueryRow(`SELECT id, public_model, target_profile_id, target_model, credit_weight, enabled, created_at, updated_at FROM usage_models WHERE `+where, arg)
	model, err := scanModel(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("model mapping not found")
		}
		return nil, err
	}
	return &model, nil
}

func (s *SQLiteStore) CreateModel(model *ProxyModel) error {
	_, err := s.db.Exec(`INSERT INTO usage_models (id, public_model, target_profile_id, target_model, credit_weight, enabled, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		model.ID, model.PublicModel, model.TargetProfileID, model.TargetModel, model.CreditWeight, boolInt(model.Enabled), formatTime(model.CreatedAt), formatTime(model.UpdatedAt))
	return err
}

func (s *SQLiteStore) UpdateModel(model *ProxyModel) error {
	result, err := s.db.Exec(`UPDATE usage_models SET public_model = ?, target_profile_id = ?, target_model = ?, credit_weight = ?, enabled = ?, created_at = ?, updated_at = ? WHERE id = ?`,
		model.PublicModel, model.TargetProfileID, model.TargetModel, model.CreditWeight, boolInt(model.Enabled), formatTime(model.CreatedAt), formatTime(model.UpdatedAt), model.ID)
	return resultError(result, err, "model not found: "+model.ID)
}

func (s *SQLiteStore) RecordCall(call *UsageCall) error {
	_, err := s.db.Exec(`INSERT INTO usage_calls (id, timestamp, api_key_id, public_model, target_profile_id, target_model, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, billable_tokens, credit_weight, credits, estimated, status, error, latency_ms, source_channel, session_id, turn_id, job_id, error_code) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		call.ID, formatTime(call.Timestamp), call.APIKeyID, call.PublicModel, call.TargetProfileID, call.TargetModel, call.InputTokens, call.OutputTokens, call.CacheReadTokens, call.CacheWriteTokens, call.BillableTokens, call.CreditWeight, call.Credits, boolInt(call.Estimated), call.Status, call.Error, call.LatencyMs, call.SourceChannel, call.SessionID, call.TurnID, call.JobID, call.ErrorCode)
	return err
}

func (s *SQLiteStore) GetCalls(date string, apiKeyID string) ([]UsageCall, error) {
	query := `SELECT id, timestamp, api_key_id, public_model, target_profile_id, target_model, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, billable_tokens, credit_weight, credits, estimated, status, error, latency_ms, source_channel, session_id, turn_id, job_id, error_code FROM usage_calls WHERE 1=1`
	args := []any{}
	if date != "" {
		query += ` AND substr(timestamp, 1, 10) = ?`
		args = append(args, date)
	}
	if apiKeyID != "" {
		query += ` AND api_key_id = ?`
		args = append(args, apiKeyID)
	}
	query += ` ORDER BY timestamp`
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanCalls(rows)
}

func (s *SQLiteStore) GetSummary(rangeType, apiKeyID string) ([]UsageSummary, error) {
	conditions := []string{}
	args := []any{}

	if apiKeyID != "" {
		conditions = append(conditions, "api_key_id = ?")
		args = append(args, apiKeyID)
	}

	// Build period expression
	var periodExpr string
	switch rangeType {
	case "week":
		// strftime('%Y-W%W') gives ISO-like week, pad single-digit weeks
		periodExpr = "printf('%d-W%02d', cast(strftime('%Y', timestamp) as integer), cast(strftime('%W', timestamp) as integer))"
	case "year":
		periodExpr = "substr(timestamp, 1, 4)"
	case "month":
		periodExpr = "substr(timestamp, 1, 7)"
	default: // "day" or anything else
		periodExpr = "substr(timestamp, 1, 10)"
	}

	whereClause := ""
	if len(conditions) > 0 {
		whereClause = " WHERE " + strings.Join(conditions, " AND ")
	}

	query := fmt.Sprintf(`SELECT %s AS period,
		api_key_id,
		COALESCE(SUM(input_tokens), 0),
		COALESCE(SUM(output_tokens), 0),
		COALESCE(SUM(cache_read_tokens), 0),
		COALESCE(SUM(cache_write_tokens), 0),
		COALESCE(SUM(billable_tokens), 0),
		COALESCE(SUM(credits), 0),
		COUNT(*) AS call_count,
		COALESCE(SUM(CASE WHEN status = 'error' THEN 1 ELSE 0 END), 0) AS error_count
		FROM usage_calls%s
		GROUP BY period, api_key_id
		ORDER BY period`, periodExpr, whereClause)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []UsageSummary
	for rows.Next() {
		var s UsageSummary
		var period, keyID string
		if err := rows.Scan(&period, &keyID, &s.InputTokens, &s.OutputTokens,
			&s.CacheReadTokens, &s.CacheWriteTokens, &s.BillableTokens,
			&s.Credits, &s.CallCount, &s.ErrorCount); err != nil {
			return nil, err
		}
		s.Period = period
		s.APIKeyID = keyID
		s.Credits = float64(int(s.Credits*1e6+0.5)) / 1e6
		out = append(out, s)
	}
	if out == nil {
		out = []UsageSummary{}
	}
	return out, rows.Err()
}

type scanner interface {
	Scan(dest ...any) error
}

func scanModel(row scanner) (ProxyModel, error) {
	var model ProxyModel
	var enabled int
	var created, updated string
	if err := row.Scan(&model.ID, &model.PublicModel, &model.TargetProfileID, &model.TargetModel, &model.CreditWeight, &enabled, &created, &updated); err != nil {
		return ProxyModel{}, err
	}
	model.Enabled = enabled != 0
	model.CreatedAt = parseTime(created)
	model.UpdatedAt = parseTime(updated)
	return model, nil
}

func scanCalls(rows *sql.Rows) ([]UsageCall, error) {
	var out []UsageCall
	for rows.Next() {
		var call UsageCall
		var timestamp string
		var estimated int
		if err := rows.Scan(&call.ID, &timestamp, &call.APIKeyID, &call.PublicModel, &call.TargetProfileID, &call.TargetModel, &call.InputTokens, &call.OutputTokens, &call.CacheReadTokens, &call.CacheWriteTokens, &call.BillableTokens, &call.CreditWeight, &call.Credits, &estimated, &call.Status, &call.Error, &call.LatencyMs, &call.SourceChannel, &call.SessionID, &call.TurnID, &call.JobID, &call.ErrorCode); err != nil {
			return nil, err
		}
		call.Timestamp = parseTime(timestamp)
		call.Estimated = estimated != 0
		out = append(out, call)
	}
	if out == nil {
		out = []UsageCall{}
	}
	return out, rows.Err()
}

func resultError(result sql.Result, err error, notFound string) error {
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("%s", notFound)
	}
	return nil
}

func encodeStringSlice(values []string) string {
	if values == nil {
		values = []string{}
	}
	raw, _ := json.Marshal(values)
	return string(raw)
}

func decodeStringSlice(raw string) []string {
	var values []string
	_ = json.Unmarshal([]byte(raw), &values)
	if values == nil {
		return []string{}
	}
	return values
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339Nano)
}

func parseTime(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	t, _ := time.Parse(time.RFC3339Nano, value)
	return t
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func (s *SQLiteStore) GetCacheStats(query CacheStatsQuery) ([]CacheStats, error) {
	// Build WHERE clauses
	conditions := []string{}
	args := []any{}

	switch strings.TrimSpace(query.RangeType) {
	case "day":
		conditions = append(conditions, "substr(timestamp, 1, 10) = date('now')")
	case "week":
		conditions = append(conditions, "timestamp >= datetime('now', '-7 days')")
	case "month":
		conditions = append(conditions, "timestamp >= datetime('now', '-30 days')")
	}
	if query.Model != "" {
		conditions = append(conditions, "target_model = ?")
		args = append(args, query.Model)
	}
	conditions = append(conditions, "status = 'success'")

	whereClause := ""
	if len(conditions) > 0 {
		whereClause = " WHERE " + strings.Join(conditions, " AND ")
	}

	periodExpr := "substr(timestamp, 1, 10)"
	if query.RangeType == "week" {
		periodExpr = "strftime('%Y-W%W', timestamp)"
	} else if query.RangeType == "month" {
		periodExpr = "substr(timestamp, 1, 7)"
	}

	querySQL := `SELECT ` + periodExpr + ` AS period,
		target_model,
		COUNT(*) AS total_calls,
		COALESCE(SUM(input_tokens), 0) AS input_tokens,
		COALESCE(SUM(cache_read_tokens), 0) AS cache_read_tokens,
		COALESCE(SUM(cache_write_tokens), 0) AS cache_write_tokens
		FROM usage_calls` + whereClause + `
		GROUP BY period, target_model
		ORDER BY period DESC, target_model`

	rows, err := s.db.Query(querySQL, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []CacheStats
	for rows.Next() {
		var stat CacheStats
		var period, model string
		var totalCalls int
		var inputTokens, cacheRead, cacheWrite int64
		if err := rows.Scan(&period, &model, &totalCalls, &inputTokens, &cacheRead, &cacheWrite); err != nil {
			return nil, err
		}
		stat.Period = period
		stat.Model = model
		stat.TotalCalls = totalCalls
		stat.InputTokens = inputTokens
		stat.CacheReadTokens = cacheRead
		stat.CacheWriteTokens = cacheWrite
		total := inputTokens + cacheRead
		if total > 0 {
			stat.HitRate = float64(cacheRead) / float64(total) * 100.0
			stat.HitRate = float64(int(stat.HitRate*100+0.5)) / 100
		}
		stat.TokensSaved = cacheRead
		out = append(out, stat)
	}
	if out == nil {
		out = []CacheStats{}
	}
	return out, rows.Err()
}

func (s *SQLiteStore) GetTimeSeries(query TimeSeriesQuery) ([]TimeSeriesPoint, error) {
	conditions := []string{}
	args := []any{}

	if query.StartTime != "" {
		conditions = append(conditions, "timestamp >= ?")
		args = append(args, query.StartTime)
	}
	if query.EndTime != "" {
		conditions = append(conditions, "timestamp <= ?")
		args = append(args, query.EndTime)
	}
	if query.APIKeyID != "" {
		conditions = append(conditions, "api_key_id = ?")
		args = append(args, query.APIKeyID)
	}
	if query.SessionID != "" {
		conditions = append(conditions, "session_id = ?")
		args = append(args, query.SessionID)
	}
	if query.Model != "" {
		conditions = append(conditions, "(public_model = ? OR target_model = ?)")
		args = append(args, query.Model, query.Model)
	}

	periodExpr := "substr(timestamp, 1, 10)"
	if query.Granularity == "hour" {
		periodExpr = "substr(timestamp, 1, 13)"
	}

	whereClause := ""
	if len(conditions) > 0 {
		whereClause = " WHERE " + strings.Join(conditions, " AND ")
	}

	sqlQuery := `SELECT ` + periodExpr + ` AS bucket,
		COUNT(*) AS call_count,
		COALESCE(SUM(CASE WHEN status = 'error' THEN 1 ELSE 0 END), 0) AS error_count,
		COALESCE(SUM(input_tokens), 0) AS input_tokens,
		COALESCE(SUM(output_tokens), 0) AS output_tokens,
		COALESCE(SUM(cache_read_tokens), 0) AS cache_read_tokens,
		COALESCE(SUM(cache_write_tokens), 0) AS cache_write_tokens,
		COALESCE(SUM(billable_tokens), 0) AS billable_tokens,
		COALESCE(SUM(credits), 0) AS credits,
		CASE WHEN COUNT(*) > 0 THEN CAST(SUM(latency_ms) AS REAL) / COUNT(*) ELSE 0 END AS avg_latency_ms
		FROM usage_calls` + whereClause + `
		GROUP BY bucket
		ORDER BY bucket`

	rows, err := s.db.Query(sqlQuery, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []TimeSeriesPoint
	for rows.Next() {
		var p TimeSeriesPoint
		var bucket string
		var callCount, errCount, inTokens, outTokens, cacheR, cacheW, billable int
		var credits float64
		var avgLatency float64
		if err := rows.Scan(&bucket, &callCount, &errCount, &inTokens, &outTokens, &cacheR, &cacheW, &billable, &credits, &avgLatency); err != nil {
			return nil, err
		}
		p.Bucket = bucket
		p.CallCount = callCount
		p.ErrorCount = errCount
		p.InputTokens = inTokens
		p.OutputTokens = outTokens
		p.CacheReadTokens = cacheR
		p.CacheWriteTokens = cacheW
		p.BillableTokens = billable
		p.Credits = credits
		p.AvgLatencyMs = avgLatency
		out = append(out, p)
	}
	if out == nil {
		out = []TimeSeriesPoint{}
	}
	return out, rows.Err()
}

func (s *SQLiteStore) GetSessionUsage(sessionID string) (*SessionUsageSummary, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("session_id is required")
	}

	// Aggregate top-level session stats
	row := s.db.QueryRow(`SELECT
		COUNT(*) AS call_count,
		COALESCE(SUM(input_tokens), 0),
		COALESCE(SUM(output_tokens), 0),
		COALESCE(SUM(cache_read_tokens), 0),
		COALESCE(SUM(cache_write_tokens), 0),
		COALESCE(SUM(billable_tokens), 0),
		COALESCE(SUM(credits), 0),
		MIN(timestamp),
		MAX(timestamp)
		FROM usage_calls WHERE session_id = ?`, sessionID)

	var summary SessionUsageSummary
	var firstCall, lastCall string
	if err := row.Scan(&summary.CallCount, &summary.InputTokens, &summary.OutputTokens,
		&summary.CacheReadTokens, &summary.CacheWriteTokens, &summary.BillableTokens,
		&summary.Credits, &firstCall, &lastCall); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("session not found")
		}
		return nil, err
	}
	summary.SessionID = sessionID
	summary.FirstCall = firstCall
	summary.LastCall = lastCall

	// Aggregate per-model breakdown
	modelRows, err := s.db.Query(`SELECT
		public_model,
		COUNT(*),
		COALESCE(SUM(input_tokens), 0),
		COALESCE(SUM(output_tokens), 0)
		FROM usage_calls WHERE session_id = ?
		GROUP BY public_model
		ORDER BY COUNT(*) DESC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer modelRows.Close()

	for modelRows.Next() {
		var m ModelTokenUsage
		if err := modelRows.Scan(&m.Model, &m.CallCount, &m.InputTokens, &m.OutputTokens); err != nil {
			return nil, err
		}
		summary.ModelUsage = append(summary.ModelUsage, m)
	}
	if summary.ModelUsage == nil {
		summary.ModelUsage = []ModelTokenUsage{}
	}
	return &summary, modelRows.Err()
}

func (s *SQLiteStore) ListSessions(apiKeyID string, limit, offset int) ([]SessionUsageSummary, error) {
	if limit <= 0 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}

	conditions := []string{"session_id != ''"}
	args := []any{}
	if apiKeyID != "" {
		conditions = append(conditions, "api_key_id = ?")
		args = append(args, apiKeyID)
	}
	whereClause := " WHERE " + strings.Join(conditions, " AND ")

	query := `SELECT session_id,
		COUNT(*) AS call_count,
		COALESCE(SUM(input_tokens), 0),
		COALESCE(SUM(output_tokens), 0),
		COALESCE(SUM(cache_read_tokens), 0),
		COALESCE(SUM(cache_write_tokens), 0),
		COALESCE(SUM(billable_tokens), 0),
		COALESCE(SUM(credits), 0),
		MIN(timestamp),
		MAX(timestamp)
		FROM usage_calls` + whereClause + `
		GROUP BY session_id
		ORDER BY MAX(timestamp) DESC
		LIMIT ? OFFSET ?`
	args = append(args, limit, offset)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []SessionUsageSummary
	for rows.Next() {
		var s SessionUsageSummary
		var sessionID, firstCall, lastCall string
		if err := rows.Scan(&sessionID, &s.CallCount, &s.InputTokens, &s.OutputTokens,
			&s.CacheReadTokens, &s.CacheWriteTokens, &s.BillableTokens,
			&s.Credits, &firstCall, &lastCall); err != nil {
			return nil, err
		}
		s.SessionID = sessionID
		s.FirstCall = firstCall
		s.LastCall = lastCall
		s.ModelUsage = []ModelTokenUsage{}
		out = append(out, s)
	}
	if out == nil {
		out = []SessionUsageSummary{}
	}
	return out, rows.Err()
}

// Ensure SQLiteStore implements Store.
var _ Store = (*SQLiteStore)(nil)

// NewID generates a simple unique ID for demo/testing purposes.
// In production, use UUIDs.
func NewID(prefix string) string {
	return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
}
