package usage

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
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
	path      string
	db        *sql.DB
	masterKey []byte
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
	masterKey, err := loadOrCreateMasterKey(stateDir)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("load master key: %w", err)
	}
	store := &SQLiteStore{path: path, db: db, masterKey: masterKey}
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
		`CREATE TABLE IF NOT EXISTS biz_keys (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			default_prompt TEXT NOT NULL DEFAULT '',
			template_id TEXT NOT NULL DEFAULT '',
			key_hash TEXT NOT NULL,
			key_prefix TEXT NOT NULL,
			enabled INTEGER NOT NULL,
			mcp_servers TEXT NOT NULL,
			providers TEXT NOT NULL,
			sandbox_tools TEXT NOT NULL,
			skills TEXT NOT NULL DEFAULT '',
			packages TEXT NOT NULL DEFAULT '',
			models TEXT NOT NULL,
			project_dir TEXT NOT NULL DEFAULT '',
			secret_encrypted TEXT NOT NULL DEFAULT '',
			pin_hash TEXT NOT NULL DEFAULT '',
			budget_credits REAL NOT NULL,
			warning_threshold REAL NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_biz_keys_hash ON biz_keys(key_hash)`,
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
	if err := s.migrateBizKeyColumns(); err != nil {
		return fmt.Errorf("migrate biz keys columns: %w", err)
	}
	return nil
}

// migrateBizKeyColumns adds columns added after the original biz_keys table was
// first created. CREATE TABLE IF NOT EXISTS above covers fresh databases; this
// covers upgrades of existing ones (SQLite ALTER TABLE fails if the column
// already exists, so we only add missing columns).
func (s *SQLiteStore) migrateBizKeyColumns() error {
	rows, err := s.db.Query(`PRAGMA table_info(biz_keys)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	have := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return err
		}
		have[name] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	adds := []string{
		"description TEXT NOT NULL DEFAULT ''",
		"default_prompt TEXT NOT NULL DEFAULT ''",
		"template_id TEXT NOT NULL DEFAULT ''",
		"skills TEXT NOT NULL DEFAULT ''",
		"packages TEXT NOT NULL DEFAULT ''",
		"project_dir TEXT NOT NULL DEFAULT ''",
		"secret_encrypted TEXT NOT NULL DEFAULT ''",
		"pin_hash TEXT NOT NULL DEFAULT ''",
	}
	for _, add := range adds {
		col := strings.Fields(add)[0]
		if have[col] {
			continue
		}
		if _, err := s.db.Exec(`ALTER TABLE biz_keys ADD COLUMN ` + add); err != nil {
			return err
		}
	}
	// No history to migrate: older keys were stored hash-only (one-way) and
	// cannot gain a pin retroactively, so any key without an encrypted secret
	// is dropped. Admin re-creates them with a pin via the console.
	if _, err := s.db.Exec(`DELETE FROM biz_keys WHERE secret_encrypted = '' OR pin_hash = ''`); err != nil {
		return err
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

// ---- biz keys ----

func (s *SQLiteStore) ListBizKeys() ([]BizAPIKey, error) {
	rows, err := s.db.Query(`SELECT id, name, description, default_prompt, template_id, key_prefix, enabled, mcp_servers, providers, sandbox_tools, skills, packages, models, project_dir, budget_credits, warning_threshold, created_at, updated_at FROM biz_keys ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BizAPIKey
	for rows.Next() {
		var key BizAPIKey
		var enabled int
		var mcpServers, providers, sandboxTools, skills, packages, models, created, updated string
		if err := rows.Scan(&key.ID, &key.Name, &key.Description, &key.DefaultPrompt, &key.TemplateID, &key.KeyPrefix, &enabled,
			&mcpServers, &providers, &sandboxTools, &skills, &packages, &models,
			&key.ProjectDir, &key.BudgetCredits, &key.WarningThreshold, &created, &updated); err != nil {
			return nil, err
		}
		key.Enabled = enabled != 0
		key.MCPServers = decodeStringSlice(mcpServers)
		key.Providers = decodeProviderRefs(providers)
		key.SandboxTools = decodeStringSlice(sandboxTools)
		key.Skills = decodeStringSlice(skills)
		key.Packages = decodeStringSlice(packages)
		key.Models = decodeStringSlice(models)
		key.CreatedAt = parseTime(created)
		key.UpdatedAt = parseTime(updated)
		out = append(out, key)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) GetBizKey(id string) (*BizAPIKey, error) {
	return s.getBizKey(`id = ?`, id)
}

func (s *SQLiteStore) GetBizKeyByHash(hash string) (*BizAPIKey, error) {
	return s.getBizKey(`key_hash = ?`, hash)
}

func (s *SQLiteStore) getBizKey(where string, arg string) (*BizAPIKey, error) {
	row := s.db.QueryRow(`SELECT id, name, description, default_prompt, template_id, key_hash, key_prefix, enabled, mcp_servers, providers, sandbox_tools, skills, packages, models, project_dir, secret_encrypted, pin_hash, budget_credits, warning_threshold, created_at, updated_at FROM biz_keys WHERE `+where, arg)
	return scanBizKeyRow(row)
}

func (s *SQLiteStore) CreateBizKey(key *BizAPIKey) error {
	_, err := s.db.Exec(`INSERT INTO biz_keys (id, name, description, default_prompt, template_id, key_hash, key_prefix, enabled, mcp_servers, providers, sandbox_tools, skills, packages, models, project_dir, secret_encrypted, pin_hash, budget_credits, warning_threshold, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		key.ID, key.Name, key.Description, key.DefaultPrompt, key.TemplateID, key.KeyHash, key.KeyPrefix, boolInt(key.Enabled),
		encodeStringSlice(key.MCPServers), encodeProviderRefs(key.Providers), encodeStringSlice(key.SandboxTools), encodeStringSlice(key.Skills), encodeStringSlice(key.Packages), encodeStringSlice(key.Models),
		key.ProjectDir, key.SecretEncrypted, key.PinHash, key.BudgetCredits, key.WarningThreshold, formatTime(key.CreatedAt), formatTime(key.UpdatedAt))
	return err
}

func (s *SQLiteStore) UpdateBizKey(key *BizAPIKey) error {
	result, err := s.db.Exec(`UPDATE biz_keys SET name = ?, description = ?, default_prompt = ?, template_id = ?, key_hash = ?, key_prefix = ?, enabled = ?, mcp_servers = ?, providers = ?, sandbox_tools = ?, skills = ?, packages = ?, models = ?, project_dir = ?, secret_encrypted = ?, pin_hash = ?, budget_credits = ?, warning_threshold = ?, updated_at = ? WHERE id = ?`,
		key.Name, key.Description, key.DefaultPrompt, key.TemplateID, key.KeyHash, key.KeyPrefix, boolInt(key.Enabled),
		encodeStringSlice(key.MCPServers), encodeProviderRefs(key.Providers), encodeStringSlice(key.SandboxTools), encodeStringSlice(key.Skills), encodeStringSlice(key.Packages), encodeStringSlice(key.Models),
		key.ProjectDir, key.SecretEncrypted, key.PinHash, key.BudgetCredits, key.WarningThreshold, formatTime(key.UpdatedAt), key.ID)
	return resultError(result, err, "biz key not found: "+key.ID)
}

func (s *SQLiteStore) DeleteBizKey(id string) error {
	result, err := s.db.Exec(`DELETE FROM biz_keys WHERE id = ?`, id)
	return resultError(result, err, "biz key not found: "+id)
}

// ---- biz secret crypto (master-key backed) ----

// EncryptBizSecret seals a plaintext secret with the store master key.
func (s *SQLiteStore) EncryptBizSecret(plain string) (string, error) {
	return encryptSecret(s.masterKey, plain)
}

// DecryptBizSecret opens an EncryptBizSecret payload.
func (s *SQLiteStore) DecryptBizSecret(encoded string) (string, error) {
	return decryptSecret(s.masterKey, encoded)
}

// HashBizPin returns the keyed hash of a pin for storage.
func (s *SQLiteStore) HashBizPin(pin string) string {
	return hashPin(s.masterKey, pin)
}

// VerifyBizPin constant-time checks a pin against its stored hash.
func (s *SQLiteStore) VerifyBizPin(pin, hash string) bool {
	return verifyPin(s.masterKey, pin, hash)
}

// scanBizKeyRow scans one biz_keys row. Columns must match the SELECT used by
// every biz key query (id, name, description, default_prompt, [key_hash],
// key_prefix, enabled, mcp_servers, providers, sandbox_tools, skills, packages,
// models, project_dir, secret_encrypted, pin_hash, budget_credits,
// warning_threshold, created_at, updated_at).
func scanBizKeyRow(scanner interface{ Scan(dest ...any) error }) (*BizAPIKey, error) {
	var key BizAPIKey
	var enabled int
	var mcpServers, providers, sandboxTools, skills, packages, models, created, updated string
	err := scanner.Scan(&key.ID, &key.Name, &key.Description, &key.DefaultPrompt, &key.TemplateID, &key.KeyHash, &key.KeyPrefix, &enabled,
		&mcpServers, &providers, &sandboxTools, &skills, &packages, &models,
		&key.ProjectDir, &key.SecretEncrypted, &key.PinHash,
		&key.BudgetCredits, &key.WarningThreshold, &created, &updated)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("biz key not found")
		}
		return nil, err
	}
	key.Enabled = enabled != 0
	key.MCPServers = decodeStringSlice(mcpServers)
	key.Providers = decodeProviderRefs(providers)
	key.SandboxTools = decodeStringSlice(sandboxTools)
	key.Skills = decodeStringSlice(skills)
	key.Packages = decodeStringSlice(packages)
	key.Models = decodeStringSlice(models)
	key.CreatedAt = parseTime(created)
	key.UpdatedAt = parseTime(updated)
	return &key, nil
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

func encodeProviderRefs(values []ProviderRef) string {
	if values == nil {
		values = []ProviderRef{}
	}
	raw, _ := json.Marshal(values)
	return string(raw)
}

func decodeProviderRefs(raw string) []ProviderRef {
	var values []ProviderRef
	_ = json.Unmarshal([]byte(raw), &values)
	if values == nil {
		return []ProviderRef{}
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

// GetCacheStats returns cache performance statistics grouped by
// model and time period.
//
// The query now supports three previously-missing axes:
//   - call-level hit rate (cache_hit_calls / total_calls), which is
//     the operator's "what fraction of requests benefit from the
//     cache?" number;
//   - per-key filtering (query.APIKeyID), so a proxy key user can
//     see only their own cache stats rather than the global view
//     that would leak cross-tenant information;
//   - the "all" range type, which returns the lifetime aggregate
//     with no period grouping (the dashboard uses this to render
//     the "since you started using the gateway" tile).
//
// The token-level cache efficiency (cache_read / (input +
// cache_read)) is also surfaced separately as CacheEfficiency so
// the dashboard can render the cost-saving story alongside the
// call-level hit rate.
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
	case "all":
		// No time filter; we still exclude non-success calls so
		// the cached-read numbers don't get polluted by 4xx/5xx
		// provider errors.
	default:
		// Empty / unknown range falls back to the lifetime
		// aggregate. The previous implementation rejected this
		// case with no useful error; we accept it as "all" so
		// the dashboard's "lifetime" tile never breaks when the
		// URL omits the range parameter.
		query.RangeType = "all"
	}
	if query.Model != "" {
		conditions = append(conditions, "target_model = ?")
		args = append(args, query.Model)
	}
	if query.APIKeyID != "" {
		// Per-key filtering is what lets the proxy-key path
		// expose its own cache stats without leaking the
		// global aggregate. The web-token admin path leaves
		// this empty to get the all-tenants view.
		conditions = append(conditions, "api_key_id = ?")
		args = append(args, query.APIKeyID)
	}
	conditions = append(conditions, "status = 'success'")

	whereClause := ""
	if len(conditions) > 0 {
		whereClause = " WHERE " + strings.Join(conditions, " AND ")
	}

	// periodExpr picks the SQL expression for the per-row
	// "period" column. For "all" we return an empty string so
	// the SQL groups by no period column and the response
	// carries a single lifetime row per (target_model) rather
	// than a per-day / per-week / per-month series. The
	// dashboard can still render the series by issuing a
	// "week" / "month" query in addition.
	periodExpr := "''"
	groupBy := "target_model"
	orderBy := "target_model"
	switch strings.TrimSpace(query.RangeType) {
	case "day":
		periodExpr = "substr(timestamp, 1, 10)"
		groupBy = "period, target_model"
		orderBy = "period DESC, target_model"
	case "week":
		periodExpr = "strftime('%Y-W%W', timestamp)"
		groupBy = "period, target_model"
		orderBy = "period DESC, target_model"
	case "month":
		periodExpr = "substr(timestamp, 1, 7)"
		groupBy = "period, target_model"
		orderBy = "period DESC, target_model"
	}

	// A call is a "cache hit" when its cache_read_tokens field
	// is non-zero. We use SUM(... > 0) rather than
	// SUM(cache_read_tokens > 0) so the SQL is portable to
	// older SQLite versions that don't support boolean
	// expressions in aggregates; the cast to INTEGER gives us
	// 0/1 either way.
	querySQL := `SELECT ` + periodExpr + ` AS period,
		target_model,
		COUNT(*) AS total_calls,
		COALESCE(SUM(CASE WHEN cache_read_tokens > 0 THEN 1 ELSE 0 END), 0) AS cache_hit_calls,
		COALESCE(SUM(input_tokens), 0) AS input_tokens,
		COALESCE(SUM(output_tokens), 0) AS output_tokens,
		COALESCE(SUM(cache_read_tokens), 0) AS cache_read_tokens,
		COALESCE(SUM(cache_write_tokens), 0) AS cache_write_tokens
		FROM usage_calls` + whereClause + `
		GROUP BY ` + groupBy + `
		ORDER BY ` + orderBy

	rows, err := s.db.Query(querySQL, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []CacheStats
	for rows.Next() {
		var stat CacheStats
		var period, model string
		var totalCalls, cacheHitCalls int
		var inputTokens, outputTokens, cacheRead, cacheWrite int64
		if err := rows.Scan(&period, &model, &totalCalls, &cacheHitCalls, &inputTokens, &outputTokens, &cacheRead, &cacheWrite); err != nil {
			return nil, err
		}
		stat.Period = period
		stat.Model = model
		stat.TotalCalls = totalCalls
		stat.CacheHitCalls = cacheHitCalls
		stat.CacheMissCalls = totalCalls - cacheHitCalls
		if stat.CacheMissCalls < 0 {
			// Defensive: never expose a negative miss count if
			// the schema or query drifts in the future. The
			// dashboard renders a zero rather than "–12".
			stat.CacheMissCalls = 0
		}
		stat.InputTokens = inputTokens
		stat.OutputTokens = outputTokens
		stat.CacheReadTokens = cacheRead
		stat.CacheWriteTokens = cacheWrite

		// Call-level hit rate: the share of calls that saw at
		// least one cached token. This is the metric operators
		// usually want when they ask "what fraction of
		// requests benefit from the cache?". Without this
		// field the dashboard had to compute it client-side
		// from raw call counts and could disagree with the
		// server's understanding of "hit".
		if totalCalls > 0 {
			stat.HitRate = roundPercent(float64(cacheHitCalls) / float64(totalCalls) * 100.0)
		}

		// Token-level cache efficiency: the share of input
		// context that came from cache. This is the metric
		// that drives the cost-saving story because cached
		// tokens are billed at a steep discount by every
		// provider that supports prompt caching.
		efficiencyDenominator := inputTokens + cacheRead
		if efficiencyDenominator > 0 {
			stat.CacheEfficiency = roundPercent(float64(cacheRead) / float64(efficiencyDenominator) * 100.0)
		}

		// TokensSaved is the wall-clock equivalent of
		// cache_read tokens. The previous implementation
		// returned this as a separate field; we keep it for
		// backward compatibility so the dashboard can keep
		// rendering "X tokens saved" without recomputing.
		stat.TokensSaved = cacheRead

		// EstimatedSavingsCredits is the credit reduction the
		// cache delivered, computed against the gateway's own
		// 4x cache-read discount. The gateway treats cached
		// tokens as 0.25x billable in RecordCall (the rest of
		// the model would have been 1x billable input), so the
		// saving per cache_read token is (1 - 0.25) = 0.75 of
		// the unit credit rate. We multiply by the model's
		// CreditWeight so a high-weight model (e.g. Opus)
		// shows a proportionally larger saving. The number is
		// rounded to six decimal places to match the precision
		// of UsageCall.Credits.
		//
		// We don't know the per-row CreditWeight from the SQL
		// (it lives in the model mapping, not the ledger), so
		// the service layer re-multiplies by the resolved
		// weight when surfacing the result. We initialise
		// the field to the unweighted saving here; callers
		// that need a weighted value re-compute via the
		// service method that joins against the model
		// mapping. The "1x - 0.25x = 0.75x" factor is the
		// discount the gateway actually applies, so the
		// unweighted number is still a meaningful estimate
		// for the dashboard's "rough cost" column.
		stat.EstimatedSavingsCredits = math.Round(float64(cacheRead)*0.75*1e6) / 1e6

		out = append(out, stat)
	}
	if out == nil {
		out = []CacheStats{}
	}
	return out, rows.Err()
}

// roundPercent rounds a percentage to two decimal places. The
// previous implementation inlined the rounding math in
// GetCacheStats, which made it easy to forget on the new fields;
// centralising it here keeps the rounding rule consistent across
// HitRate and CacheEfficiency. Two decimal places is enough for
// the dashboard (1.23%) while keeping the JSON compact.
func roundPercent(v float64) float64 {
	return float64(int(v*100+0.5)) / 100
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
