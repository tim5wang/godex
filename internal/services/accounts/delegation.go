// godex-feature: delegation
// 运营代看：租户主动授权 + 时限 + 审计 + 同意留痕（设计 §3.6，决策⑪）。
// 入口：/tenant/delegations（租户授权/撤回）、/admin/delegations（超管查看）、
//
//	identity.Policy 的授权缝、NodeAccess 审计点
//
// 文档：docs/multi-tenant-commercialization-design.md §3.6
package accounts

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/core/identity"
	"github.com/tim5wang/godex/internal/platform/idgen"
)

// ErrDelegationNotFound is returned when a delegation id is unknown.
var ErrDelegationNotFound = errors.New("delegation not found")

// ErrNotTenantAdmin is returned when a non-admin tries to grant or revoke access
// on a tenant's behalf. Only the tenant may authorize being looked at, and only
// through someone who speaks for it.
var ErrNotTenantAdmin = errors.New("only a tenant owner or admin may manage access grants")

// CreateDelegation records a tenant's grant for one platform operator.
//
// grantedBy must be an owner/admin OF THAT TENANT: the whole point is that the
// tenant decides, so letting an operator mint its own grant would turn explicit
// consent into self-service access.
func (s *Service) CreateDelegation(input CreateDelegationInput) (identity.Delegation, error) {
	tenantID := strings.TrimSpace(input.TenantID)
	grantedBy := strings.TrimSpace(input.GrantedBy)
	operator := strings.TrimSpace(input.OperatorUserID)
	if tenantID == "" {
		return identity.Delegation{}, fmt.Errorf("missing tenant id")
	}
	if operator == "" {
		return identity.Delegation{}, fmt.Errorf("missing operator_user_id: a grant must name who it is for")
	}
	if !s.IsTenantAdmin(tenantID, grantedBy) {
		return identity.Delegation{}, ErrNotTenantAdmin
	}

	ttl := input.TTL
	if ttl <= 0 {
		ttl = identity.DefaultDelegationTTL
	}
	// Capped rather than free choice: "grant for a year" is functionally the
	// standing silent bypass this design rejects. The error names the cap so the
	// caller can pick the longest allowed window instead of guessing.
	if ttl > identity.MaxDelegationTTL {
		return identity.Delegation{}, fmt.Errorf(
			"delegation window %s exceeds the maximum of %s", ttl, identity.MaxDelegationTTL)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	record := identity.Delegation{
		ID:             idgen.New("dlg_", 8),
		TenantID:       tenantID,
		OperatorUserID: operator,
		GrantedBy:      grantedBy,
		Scope:          identity.NormalizeScope(input.Scope),
		Reason:         strings.TrimSpace(input.Reason),
		CreatedAt:      now.Format(time.RFC3339Nano),
		ExpiresAt:      now.Add(ttl).Format(time.RFC3339Nano),
	}
	if _, err := s.db.Exec(
		`INSERT INTO delegations (id, tenant_id, operator_user_id, granted_by, scope, reason, created_at, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		record.ID, record.TenantID, record.OperatorUserID, record.GrantedBy,
		record.Scope, record.Reason, record.CreatedAt, record.ExpiresAt,
	); err != nil {
		return identity.Delegation{}, fmt.Errorf("create delegation: %w", err)
	}
	return record, nil
}

// CreateDelegationInput describes a tenant's grant request.
type CreateDelegationInput struct {
	TenantID       string
	OperatorUserID string
	GrantedBy      string
	Scope          string
	Reason         string
	// TTL overrides the default window; zero uses DefaultDelegationTTL.
	TTL time.Duration
}

// ListDelegations returns a tenant's grants, newest first.
func (s *Service) ListDelegations(tenantID string) ([]identity.Delegation, error) {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("missing tenant id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.queryDelegationsLocked(
		`SELECT id, tenant_id, operator_user_id, granted_by, scope, reason, created_at, expires_at, revoked_at
		 FROM delegations WHERE tenant_id = ? ORDER BY created_at DESC, id DESC`, tenantID)
}

// ListOperatorDelegations returns every grant issued to one operator, so the
// operator's own console can show what it is currently allowed to look at.
func (s *Service) ListOperatorDelegations(operatorUserID string) ([]identity.Delegation, error) {
	operatorUserID = strings.TrimSpace(operatorUserID)
	if operatorUserID == "" {
		return nil, fmt.Errorf("missing operator user id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.queryDelegationsLocked(
		`SELECT id, tenant_id, operator_user_id, granted_by, scope, reason, created_at, expires_at, revoked_at
		 FROM delegations WHERE operator_user_id = ? ORDER BY created_at DESC, id DESC`, operatorUserID)
}

// ActiveDelegationFor reports the currently valid grant for one operator over one
// tenant, if any.
//
// This is the function the authorization seam calls, so it answers exactly one
// question: may this operator read this tenant right now. Expiry and revocation are
// applied here rather than at the call site, so no seam can honour a grant the
// tenant already withdrew.
func (s *Service) ActiveDelegationFor(operatorUserID, tenantID string) (identity.Delegation, bool) {
	operatorUserID = strings.TrimSpace(operatorUserID)
	tenantID = strings.TrimSpace(tenantID)
	if operatorUserID == "" || tenantID == "" {
		return identity.Delegation{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	records, err := s.queryDelegationsLocked(
		`SELECT id, tenant_id, operator_user_id, granted_by, scope, reason, created_at, expires_at, revoked_at
		 FROM delegations WHERE tenant_id = ? AND operator_user_id = ?
		 ORDER BY created_at DESC, id DESC LIMIT 5`, tenantID, operatorUserID)
	if err != nil {
		return identity.Delegation{}, false
	}
	now := s.now().UTC()
	for _, record := range records {
		if record.Active(now) && record.AllowsRead() {
			return record, true
		}
	}
	return identity.Delegation{}, false
}

// RevokeDelegation ends a grant early. Only a tenant owner/admin may do it.
//
// The tenant is read from the record rather than taken as a parameter: a caller
// that supplied the tenant id could revoke someone else's grant by asserting it.
func (s *Service) RevokeDelegation(id, revokedBy string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("missing delegation id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	var tenantID string
	if err := s.db.QueryRow(`SELECT tenant_id FROM delegations WHERE id = ?`, id).Scan(&tenantID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrDelegationNotFound
		}
		return fmt.Errorf("lookup delegation: %w", err)
	}
	if !s.isTenantAdminLocked(tenantID, strings.TrimSpace(revokedBy)) {
		return ErrNotTenantAdmin
	}
	if _, err := s.db.Exec(`UPDATE delegations SET revoked_at = ? WHERE id = ?`, s.timestamp(), id); err != nil {
		return fmt.Errorf("revoke delegation: %w", err)
	}
	return nil
}

// RecordDelegationUse writes one audit row. Called at each authorization point so
// "who looked at what, when" is answerable after the fact rather than only while
// a grant is live.
func (s *Service) RecordDelegationUse(entry DelegationAudit) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.db.Exec(
		`INSERT INTO delegation_audit (at, delegation_id, tenant_id, operator_user_id, action, target, allowed, reason)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		s.timestamp(), strings.TrimSpace(entry.DelegationID), strings.TrimSpace(entry.TenantID),
		strings.TrimSpace(entry.OperatorUserID), strings.TrimSpace(entry.Action),
		strings.TrimSpace(entry.Target), boolToInt(entry.Allowed), strings.TrimSpace(entry.Reason),
	); err != nil {
		return fmt.Errorf("record delegation audit: %w", err)
	}
	return nil
}

// DelegationAudit is one recorded use of a grant.
type DelegationAudit struct {
	DelegationID   string `json:"delegation_id,omitempty"`
	TenantID       string `json:"tenant_id,omitempty"`
	OperatorUserID string `json:"operator_user_id,omitempty"`
	Action         string `json:"action,omitempty"`
	Target         string `json:"target,omitempty"`
	Allowed        bool   `json:"allowed"`
	Reason         string `json:"reason,omitempty"`
	At             string `json:"at,omitempty"`
}

// ListDelegationAudit returns a tenant's audit trail, newest first, so the tenant
// can see who read what.
func (s *Service) ListDelegationAudit(tenantID string, limit int) ([]DelegationAudit, error) {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("missing tenant id")
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(
		`SELECT delegation_id, tenant_id, operator_user_id, action, target, allowed, reason, at
		 FROM delegation_audit WHERE tenant_id = ? ORDER BY id DESC LIMIT ?`, tenantID, limit)
	if err != nil {
		return nil, fmt.Errorf("list delegation audit: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []DelegationAudit{}
	for rows.Next() {
		var item DelegationAudit
		var allowed int
		if err := rows.Scan(&item.DelegationID, &item.TenantID, &item.OperatorUserID,
			&item.Action, &item.Target, &allowed, &item.Reason, &item.At); err != nil {
			return nil, fmt.Errorf("scan delegation audit: %w", err)
		}
		item.Allowed = allowed != 0
		out = append(out, item)
	}
	return out, rows.Err()
}

// RecordConsent stores the user-agreement trail at signup (design §3.6).
//
// Best effort from the caller's perspective: a signup must not fail because the
// consent row could not be written, but the row is what makes "they agreed" a
// fact rather than an assumption, so the error is returned for the caller to log.
func (s *Service) RecordConsent(record identity.ConsentRecord) error {
	if strings.TrimSpace(record.TenantID) == "" || strings.TrimSpace(record.TermsVersion) == "" {
		return fmt.Errorf("consent record requires tenant_id and terms_version")
	}
	at := strings.TrimSpace(record.AcceptedAt)
	if at == "" {
		at = s.timestamp()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.db.Exec(
		`INSERT INTO consent_records (tenant_id, user_id, terms_version, accepted_at, source_ip)
		 VALUES (?, ?, ?, ?, ?)`,
		strings.TrimSpace(record.TenantID), strings.TrimSpace(record.UserID),
		strings.TrimSpace(record.TermsVersion), at, strings.TrimSpace(record.SourceIP),
	); err != nil {
		return fmt.Errorf("record consent: %w", err)
	}
	return nil
}

// ConsentRecords returns a tenant's agreement history.
func (s *Service) ConsentRecords(tenantID string) ([]identity.ConsentRecord, error) {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("missing tenant id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(
		`SELECT tenant_id, user_id, terms_version, accepted_at, source_ip
		 FROM consent_records WHERE tenant_id = ? ORDER BY id DESC`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list consent records: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []identity.ConsentRecord{}
	for rows.Next() {
		var item identity.ConsentRecord
		if err := rows.Scan(&item.TenantID, &item.UserID, &item.TermsVersion, &item.AcceptedAt, &item.SourceIP); err != nil {
			return nil, fmt.Errorf("scan consent record: %w", err)
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// IsTenantAdmin reports whether a user speaks for a tenant (owner or admin).
func (s *Service) IsTenantAdmin(tenantID, userID string) bool {
	tenantID = strings.TrimSpace(tenantID)
	userID = strings.TrimSpace(userID)
	if tenantID == "" || userID == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.isTenantAdminLocked(tenantID, userID)
}

// isTenantAdminLocked is the shared check. Caller holds s.mu.
func (s *Service) isTenantAdminLocked(tenantID, userID string) bool {
	var role string
	if err := s.db.QueryRow(
		`SELECT role FROM memberships WHERE tenant_id = ? AND user_id = ?`, tenantID, userID,
	).Scan(&role); err != nil {
		return false
	}
	switch role {
	case identity.RoleTenantOwner, identity.RoleTenantAdmin:
		return true
	default:
		return false
	}
}

// queryDelegationsLocked runs a delegation select. Caller holds s.mu.
func (s *Service) queryDelegationsLocked(query string, args ...any) ([]identity.Delegation, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query delegations: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []identity.Delegation{}
	for rows.Next() {
		var record identity.Delegation
		if err := rows.Scan(&record.ID, &record.TenantID, &record.OperatorUserID, &record.GrantedBy,
			&record.Scope, &record.Reason, &record.CreatedAt, &record.ExpiresAt, &record.RevokedAt); err != nil {
			return nil, fmt.Errorf("scan delegation: %w", err)
		}
		out = append(out, record)
	}
	return out, rows.Err()
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
