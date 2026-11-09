// godex-feature: metering
// 租户转发计量（设计 §7 基础档）：转发请求数/字节，按租户与时间窗聚合，供对账。
// 入口：relay ProxyHandler 的 Record 回调、GET /admin/usage/relay、GET /usage/relay
// 文档：docs/multi-tenant-commercialization-design.md §7
//
// 只记录计量所需的元数据（谁、对哪个节点、多少字节、结果码），**不记录内容**：
// 这与「对租户数据，中心只做认证/授权/转发/计量」（设计 §1.4 原则）一致，
// 也让「只转发不存数据」（决策②）保持成立。
package metering

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/tim5wang/godex/internal/platform/logger"
)

// Record is one forwarded request. TenantID may be empty for the platform
// operator's own traffic (a single-tenant deployment, or the operator driving a
// node directly); those rows are stored under the empty tenant and reported
// separately rather than being attributed to some tenant that did not make the
// request.
type Record struct {
	TenantID      string
	UserID        string
	NodeID        string
	Method        string
	Path          string
	Status        int
	RequestBytes  int
	ResponseBytes int
}

// Summary is an aggregate over a time window: the unit of reconciliation.
type Summary struct {
	TenantID      string `json:"tenant_id"`
	Requests      int64  `json:"requests"`
	RequestBytes  int64  `json:"request_bytes"`
	ResponseBytes int64  `json:"response_bytes"`
	FirstAt       string `json:"first_at,omitempty"`
	LastAt        string `json:"last_at,omitempty"`
}

// Service stores forwarded-request counters.
type Service struct {
	dir string
	db  *sql.DB
	mu  sync.Mutex
	now func() time.Time
}

// New opens (creating if needed) the metering store under stateDir.
func New(stateDir string) (*Service, error) {
	if strings.TrimSpace(stateDir) == "" {
		return nil, fmt.Errorf("missing metering store directory")
	}
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		return nil, fmt.Errorf("mkdir metering store: %w", err)
	}
	path := filepath.Join(stateDir, "metering.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open metering sqlite store: %w", err)
	}
	service := &Service{dir: stateDir, db: db, now: time.Now}
	if err := service.init(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return service, nil
}

func (s *Service) init() error {
	statements := []string{
		// One row per forwarded request. Rows are the source of truth; aggregates
		// are computed on read so a new reconciliation window (or grouping) never
		// needs a migration or a backfill.
		`CREATE TABLE IF NOT EXISTS relay_usage (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			at TEXT NOT NULL,
			tenant_id TEXT NOT NULL DEFAULT '',
			user_id TEXT NOT NULL DEFAULT '',
			node_id TEXT NOT NULL DEFAULT '',
			method TEXT NOT NULL DEFAULT '',
			path TEXT NOT NULL DEFAULT '',
			status INTEGER NOT NULL DEFAULT 0,
			request_bytes INTEGER NOT NULL DEFAULT 0,
			response_bytes INTEGER NOT NULL DEFAULT 0
		)`,
		// Reconciliation reads by window and tenant; node is a secondary filter.
		`CREATE INDEX IF NOT EXISTS idx_relay_usage_at ON relay_usage(at)`,
		`CREATE INDEX IF NOT EXISTS idx_relay_usage_tenant_at ON relay_usage(tenant_id, at)`,
		// One row per center-side forwarding ATTEMPT (allowed or refused).
		//
		// A separate table from relay_usage on purpose: relay_usage is a billing
		// counter (requests/bytes, one row per forwarded request), while this is a
		// security record of attempts, including the ones that were refused and
		// therefore never produced any usage at all. Folding them together would
		// make "how many bytes did tenant X move" depend on a table whose rows are
		// not all transfers.
		`CREATE TABLE IF NOT EXISTS forward_audit (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			at TEXT NOT NULL,
			tenant_id TEXT NOT NULL DEFAULT '',
			user_id TEXT NOT NULL DEFAULT '',
			node_id TEXT NOT NULL DEFAULT '',
			target TEXT NOT NULL DEFAULT '',
			allowed INTEGER NOT NULL DEFAULT 0,
			reason TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_forward_audit_at ON forward_audit(at)`,
		`CREATE INDEX IF NOT EXISTS idx_forward_audit_tenant_at ON forward_audit(tenant_id, at)`,
	}
	for _, statement := range statements {
		if _, err := s.db.Exec(statement); err != nil {
			return fmt.Errorf("init metering schema: %w", err)
		}
	}
	return nil
}

// Close releases the store.
func (s *Service) Close() error { return s.db.Close() }

// SetNow overrides the clock (tests).
func (s *Service) SetNow(now func() time.Time) { s.now = now }

// Record stores one forwarded request.
//
// Returns an error so a caller CAN surface a store problem, but the relay path
// deliberately logs and continues: metering is bookkeeping, and failing a user's
// request because the counter could not be written would trade a working feature
// for an accounting detail. A dropped row is visible as a gap in reconciliation;
// a refused request is not recoverable by the user at all.
func (s *Service) Record(record Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(
		`INSERT INTO relay_usage (at, tenant_id, user_id, node_id, method, path, status, request_bytes, response_bytes)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.now().UTC().Format(time.RFC3339Nano),
		strings.TrimSpace(record.TenantID), strings.TrimSpace(record.UserID), strings.TrimSpace(record.NodeID),
		strings.ToUpper(strings.TrimSpace(record.Method)), strings.TrimSpace(record.Path), record.Status,
		record.RequestBytes, record.ResponseBytes,
	)
	if err != nil {
		return fmt.Errorf("record relay usage: %w", err)
	}
	return nil
}

// RecordQuiet is Record for the request path: it reports failures through the log
// instead of returning them, so a store problem cannot turn into a failed user
// request.
//
// Deliberately still synchronous. A background queue would make the write
// non-blocking, but it would also need bounded buffering and a drop policy to
// avoid growing without limit under load — real complexity for a counter insert
// that SQLite handles inline. The relay path already does a network round-trip to
// the target node, so one local insert is not the bottleneck.
func (s *Service) RecordQuiet(record Record) {
	if s == nil {
		return
	}
	if err := s.Record(record); err != nil {
		logger.Warnf("metering: %v", err)
	}
}

// ForwardAuditRecord is one center-side forwarding attempt. It is the stored
// form of relay.ForwardAudit; declared here rather than imported so this package
// keeps no dependency on the relay package (same shape as Record/ForwardUsage).
type ForwardAuditRecord struct {
	TenantID string `json:"tenant_id,omitempty"`
	UserID   string `json:"user_id,omitempty"`
	NodeID   string `json:"node_id"`
	// Target is host:port, empty when nothing was named (node-level refusal).
	Target  string `json:"target,omitempty"`
	Allowed bool   `json:"allowed"`
	Reason  string `json:"reason,omitempty"`
	At      string `json:"at"`
}

// RecordForwardAudit stores one forwarding attempt.
func (s *Service) RecordForwardAudit(record ForwardAuditRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	allowed := 0
	if record.Allowed {
		allowed = 1
	}
	_, err := s.db.Exec(
		`INSERT INTO forward_audit (at, tenant_id, user_id, node_id, target, allowed, reason)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		s.now().UTC().Format(time.RFC3339Nano),
		strings.TrimSpace(record.TenantID), strings.TrimSpace(record.UserID), strings.TrimSpace(record.NodeID),
		strings.TrimSpace(record.Target), allowed, strings.TrimSpace(record.Reason),
	)
	if err != nil {
		return fmt.Errorf("record forward audit: %w", err)
	}
	return nil
}

// RecordForwardAuditQuiet is RecordForwardAudit for the request path: it reports
// failures through the log instead of returning them, so a store problem cannot
// turn into a failed user request (the same trade-off RecordQuiet documents).
func (s *Service) RecordForwardAuditQuiet(record ForwardAuditRecord) {
	if s == nil {
		return
	}
	if err := s.RecordForwardAudit(record); err != nil {
		logger.Warnf("metering: %v", err)
	}
}

// ForwardAudits returns one tenant's attempts since the given time, newest
// first. limit <= 0 applies a bounded default: this is a security review list,
// and an unbounded read would grow with retention.
func (s *Service) ForwardAudits(tenantID string, since time.Time, limit int) ([]ForwardAuditRecord, error) {
	return s.forwardAudits(`WHERE tenant_id = ? AND at >= ?`, limit, strings.TrimSpace(tenantID), since.UTC().Format(time.RFC3339Nano))
}

// AllForwardAudits returns every tenant's attempts (the operator's
// cross-tenant view), newest first.
func (s *Service) AllForwardAudits(since time.Time, limit int) ([]ForwardAuditRecord, error) {
	return s.forwardAudits(`WHERE at >= ?`, limit, since.UTC().Format(time.RFC3339Nano))
}

// forwardAudits runs the shared read for an optional predicate.
func (s *Service) forwardAudits(whereClause string, limit int, args ...any) ([]ForwardAuditRecord, error) {
	if limit <= 0 {
		limit = 200
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	query := `SELECT at, tenant_id, user_id, node_id, target, allowed, reason FROM forward_audit ` +
		whereClause + ` ORDER BY at DESC, id DESC LIMIT ?`
	rows, err := s.db.Query(query, append(args, limit)...)
	if err != nil {
		return nil, fmt.Errorf("read forward audit: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []ForwardAuditRecord{}
	for rows.Next() {
		var item ForwardAuditRecord
		var allowed int
		if err := rows.Scan(&item.At, &item.TenantID, &item.UserID, &item.NodeID, &item.Target, &allowed, &item.Reason); err != nil {
			return nil, fmt.Errorf("scan forward audit: %w", err)
		}
		item.Allowed = allowed != 0
		out = append(out, item)
	}
	return out, rows.Err()
}

// Summary aggregates one tenant over [since, now].
func (s *Service) Summary(tenantID string, since time.Time) (Summary, error) {
	tenantID = strings.TrimSpace(tenantID)
	summaries, err := s.summarize(`WHERE tenant_id = ? AND at >= ?`, tenantID, since.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return Summary{}, err
	}
	if len(summaries) == 0 {
		return Summary{TenantID: tenantID}, nil
	}
	return summaries[0], nil
}

// TenantSummaries aggregates every tenant with traffic since the given time.
//
// Grouped in SQL rather than in Go so a large window does not have to be pulled
// into memory to be counted — the whole point of storing counters instead of logs.
func (s *Service) TenantSummaries(since time.Time) ([]Summary, error) {
	return s.summarize(`WHERE at >= ? GROUP BY tenant_id`, since.UTC().Format(time.RFC3339Nano))
}

// summarize runs the shared aggregate for an optional predicate/grouping.
func (s *Service) summarize(whereClause string, args ...any) ([]Summary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// GROUP BY tenant_id is appended for the single-tenant case too, so both
	// callers share one query shape and cannot drift on which columns are summed.
	query := `SELECT tenant_id, COUNT(*), COALESCE(SUM(request_bytes), 0), COALESCE(SUM(response_bytes), 0),
		MIN(at), MAX(at) FROM relay_usage ` + whereClause
	if !strings.Contains(whereClause, "GROUP BY") {
		query += ` GROUP BY tenant_id`
	}
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("summarize relay usage: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := []Summary{}
	for rows.Next() {
		var item Summary
		var firstAt, lastAt sql.NullString
		if err := rows.Scan(&item.TenantID, &item.Requests, &item.RequestBytes, &item.ResponseBytes, &firstAt, &lastAt); err != nil {
			return nil, fmt.Errorf("scan relay usage summary: %w", err)
		}
		item.FirstAt = firstAt.String
		item.LastAt = lastAt.String
		out = append(out, item)
	}
	return out, rows.Err()
}
