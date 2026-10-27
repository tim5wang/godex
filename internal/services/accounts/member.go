// godex-feature: accounts
// 成员管理：租户管理员/超管邀请成员、改角色、移除成员（设计 §3.6）。
// 入口：/tenant/members*（租户管理员）、/admin/tenants/{id}/members*（超管）
// 文档：docs/multi-tenant-commercialization-design.md §3.6
package accounts

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/tim5wang/godex/internal/core/identity"
	"github.com/tim5wang/godex/internal/platform/idgen"
)

// UserInvitePrefix marks person-invite codes so they can be recognized by shape
// before lookup, mirroring the other credential families.
const UserInvitePrefix = "gui_"

// userInviteTTL bounds an unused invite. Same reasoning as the node join token:
// a code is handed over out of band and expected to be redeemed soon, so a leaked
// one stops working without anyone remembering to revoke it.
const userInviteTTL = 7 * 24 * time.Hour

var (
	// ErrNotMemberActor is returned when the caller may not administer the tenant.
	ErrNotMemberActor = errors.New("only a tenant owner/admin or a platform admin may manage members")
	// ErrInvalidInvite covers unknown, expired, used, and revoked codes. One error
	// for all four, like the node join token: a caller holding an invalid code
	// gains nothing from learning which way it is invalid.
	ErrInvalidInvite = errors.New("invalid, expired, or already used invite code")
	// ErrInvalidMemberRole is returned for a role that is not a tenant role, or one
	// the caller may not grant.
	ErrInvalidMemberRole = errors.New("invalid member role")
	// ErrOwnershipProtected protects the tenant's owner from removal or demotion.
	// Removing the last owner would leave a tenant nobody can administer — and,
	// because the owner is also the node-ownership fallback, nodes nobody owns.
	ErrOwnershipProtected = errors.New("the tenant owner cannot be removed or demoted (ownership transfer is not implemented)")
	// ErrMemberNotFound is returned when the target user is not in the tenant.
	ErrMemberNotFound = errors.New("member not found")
	// ErrCannotGrantOwner is returned when an invite or role change asks for owner.
	// Deliberately separate from ErrInvalidMemberRole so the message can explain
	// that ownership transfer is a distinct, deliberate action.
	ErrCannotGrantOwner = errors.New("ownership cannot be granted through member management")
)

// MemberActor names who is performing a member-management action.
//
// PlatformAdmin is passed in rather than looked up here so the rule lives in one
// place: the HTTP layer knows how the caller authenticated (web token vs session)
// and this package decides what that permits on a given tenant.
type MemberActor struct {
	UserID        string
	PlatformAdmin bool
}

// UserInvite is a one-time code that adds a person to a tenant (design §3.6).
//
// The person-side counterpart of JoinToken. Serialised with explicit snake_case
// tags and with NO plaintext field, so a listed record can never carry a reusable
// code.
type UserInvite struct {
	ID         string `json:"id"`
	TenantID   string `json:"tenant_id"`
	Role       string `json:"role"`
	CreatedBy  string `json:"created_by,omitempty"`
	CreatedAt  string `json:"created_at,omitempty"`
	ExpiresAt  string `json:"expires_at,omitempty"`
	UsedAt     string `json:"used_at,omitempty"`
	UsedByUser string `json:"used_by_user,omitempty"`
	RevokedAt  string `json:"revoked_at,omitempty"`
}

// Usable reports whether the invite can still be redeemed.
func (i UserInvite) Usable(now time.Time) bool {
	if i.UsedAt != "" || i.RevokedAt != "" {
		return false
	}
	expires := parseTime(i.ExpiresAt)
	return expires.IsZero() || now.Before(expires)
}

// CreateUserInviteInput asks for a person-invite code.
type CreateUserInviteInput struct {
	TenantID  string
	InvitedBy string
	Role      string
	TTL       time.Duration
	// PlatformAdmin marks the caller as the platform superadmin rather than a
	// tenant member. Passed in rather than looked up because only the HTTP layer
	// knows how the caller authenticated; without it the admin surface would be
	// indistinguishable from an ordinary member trying their luck.
	PlatformAdmin bool
}

// AcceptUserInviteResult is what a successful redemption returns.
type AcceptUserInviteResult struct {
	User      identity.User
	Tenant    identity.Tenant
	Member    identity.Membership
	Token     string
	Expires   time.Time
	CreatedOK bool // whether a NEW user row was created (vs. an existing account added to the tenant)
}

// mayManageMembersLocked is the single rule for "who may administer this tenant's
// members". Caller holds s.mu.
func (s *Service) mayManageMembersLocked(tenantID string, actor MemberActor) bool {
	if actor.PlatformAdmin {
		return true
	}
	return s.isTenantAdminLocked(tenantID, strings.TrimSpace(actor.UserID))
}

// CreateUserInvite issues a one-time code that adds a PERSON to a tenant.
//
// Returns the PLAINTEXT token, which exists only in this return value: the store
// keeps a hash. Owner is not grantable here — an invite that silently handed over
// the tenant would be a privilege escalation, so ownership transfer is a separate
// deliberate action (ErrCannotGrantOwner names that).
func (s *Service) CreateUserInvite(input CreateUserInviteInput) (UserInvite, string, error) {
	tenantID := strings.TrimSpace(input.TenantID)
	if tenantID == "" {
		return UserInvite{}, "", fmt.Errorf("missing tenant id")
	}
	role := identity.NormalizeTenantRole(input.Role)
	if role == "" {
		return UserInvite{}, "", ErrInvalidMemberRole
	}
	if role == identity.RoleTenantOwner {
		return UserInvite{}, "", ErrCannotGrantOwner
	}
	actor := MemberActor{UserID: input.InvitedBy, PlatformAdmin: input.PlatformAdmin}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return UserInvite{}, "", fmt.Errorf("generate invite: %w", err)
	}
	token := UserInvitePrefix + hex.EncodeToString(raw)

	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.mayManageMembersLocked(tenantID, actor) {
		return UserInvite{}, "", ErrNotMemberActor
	}
	now := s.now().UTC()
	ttl := input.TTL
	if ttl <= 0 {
		ttl = userInviteTTL
	}
	record := UserInvite{
		ID:        idgen.New("inv_", 8),
		TenantID:  tenantID,
		Role:      role,
		CreatedBy: strings.TrimSpace(input.InvitedBy),
		CreatedAt: now.Format(time.RFC3339Nano),
		ExpiresAt: now.Add(ttl).Format(time.RFC3339Nano),
	}
	if _, err := s.db.Exec(
		`INSERT INTO user_invites (token_hash, id, tenant_id, role, created_by, created_at, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		hashToken(token), record.ID, record.TenantID, record.Role,
		record.CreatedBy, record.CreatedAt, record.ExpiresAt,
	); err != nil {
		return UserInvite{}, "", fmt.Errorf("insert invite: %w", err)
	}
	return record, token, nil
}

// ListUserInvites returns a tenant's issued invite codes, newest first.
func (s *Service) ListUserInvites(tenantID string, actor MemberActor) ([]UserInvite, error) {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("missing tenant id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.mayManageMembersLocked(tenantID, actor) {
		return nil, ErrNotMemberActor
	}
	rows, err := s.db.Query(
		`SELECT id, tenant_id, role, created_by, created_at, expires_at, used_at, used_by_user, revoked_at
		 FROM user_invites WHERE tenant_id = ? ORDER BY created_at DESC`, tenantID,
	)
	if err != nil {
		return nil, fmt.Errorf("list invites: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []UserInvite{}
	for rows.Next() {
		var item UserInvite
		if err := rows.Scan(&item.ID, &item.TenantID, &item.Role, &item.CreatedBy, &item.CreatedAt,
			&item.ExpiresAt, &item.UsedAt, &item.UsedByUser, &item.RevokedAt); err != nil {
			return nil, fmt.Errorf("scan invite: %w", err)
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// RevokeUserInvite invalidates an unused invite. Idempotent in the same way as
// RevokeJoinToken: an unknown id is an error, an already-revoked one is not.
func (s *Service) RevokeUserInvite(id string, actor MemberActor) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("missing invite id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Resolve the invite's tenant first: authorization is per tenant, so it cannot
	// be checked before the target is known.
	var tenantID string
	if err := s.db.QueryRow(`SELECT tenant_id FROM user_invites WHERE id = ?`, id).Scan(&tenantID); err != nil {
		return fmt.Errorf("invite not found: %s", id)
	}
	if !s.mayManageMembersLocked(tenantID, actor) {
		return ErrNotMemberActor
	}
	result, err := s.db.Exec(`UPDATE user_invites SET revoked_at = ? WHERE id = ? AND revoked_at = ''`, s.timestamp(), id)
	if err != nil {
		return fmt.Errorf("revoke invite: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		var exists int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM user_invites WHERE id = ?`, id).Scan(&exists); err != nil {
			return fmt.Errorf("revoke invite: %w", err)
		}
		if exists == 0 {
			return fmt.Errorf("invite not found: %s", id)
		}
	}
	return nil
}

// AcceptUserInvite redeems a person-invite: it creates the account if the email
// is new, adds the membership, and signs the person in.
//
// An EXISTING account is added to the tenant too, but only after its password is
// verified. Without that check the invite code alone would be enough to attach
// somebody else's account to a tenant — the code authorizes "this code may be
// used", not "this email belongs to whoever holds the code".
func (s *Service) AcceptUserInvite(token, email, password string) (AcceptUserInviteResult, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return AcceptUserInviteResult{}, ErrInvalidInvite
	}
	normalized := identity.NormalizeEmail(email)
	if !validEmail(normalized) {
		return AcceptUserInviteResult{}, ErrInvalidEmail
	}
	if len(strings.TrimSpace(password)) < minPasswordLength {
		return AcceptUserInviteResult{}, ErrWeakPassword
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return AcceptUserInviteResult{}, fmt.Errorf("hash password: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	invite, err := s.lookupUserInviteLocked(token)
	if err != nil {
		return AcceptUserInviteResult{}, err
	}
	if !invite.Usable(s.now().UTC()) {
		return AcceptUserInviteResult{}, ErrInvalidInvite
	}
	tenant, ok := s.tenantLocked(invite.TenantID)
	if !ok {
		return AcceptUserInviteResult{}, fmt.Errorf("invite tenant no longer exists")
	}

	now := s.timestamp()
	var (
		user      identity.User
		createdOK bool
	)
	err = s.db.QueryRow(`SELECT id, email, password_hash, platform_role, created_at FROM users WHERE email = ?`, normalized).
		Scan(&user.ID, &user.Email, &user.PasswordHash, &user.PlatformRole, &user.CreatedAt)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		user = identity.User{ID: idgen.New("usr_", 8), Email: normalized, CreatedAt: now}
		createdOK = true
	case err != nil:
		return AcceptUserInviteResult{}, fmt.Errorf("lookup user: %w", err)
	default:
		// Existing account: prove it is theirs before attaching it.
		if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)) != nil {
			return AcceptUserInviteResult{}, ErrInvalidCredentials
		}
		if s.isMemberLocked(tenant.ID, user.ID) {
			return AcceptUserInviteResult{}, fmt.Errorf("this account is already a member of the tenant")
		}
	}

	tx, err := s.db.Begin()
	if err != nil {
		return AcceptUserInviteResult{}, fmt.Errorf("begin accept invite: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if createdOK {
		if _, err := tx.Exec(`INSERT INTO users (id, email, password_hash, platform_role, created_at) VALUES (?, ?, ?, ?, ?)`,
			user.ID, user.Email, string(hash), "", user.CreatedAt); err != nil {
			return AcceptUserInviteResult{}, fmt.Errorf("insert user: %w", err)
		}
	}
	member := identity.Membership{TenantID: tenant.ID, UserID: user.ID, Role: invite.Role, JoinedAt: now}
	if _, err := tx.Exec(`INSERT INTO memberships (tenant_id, user_id, role, joined_at) VALUES (?, ?, ?, ?)`,
		member.TenantID, member.UserID, member.Role, member.JoinedAt); err != nil {
		return AcceptUserInviteResult{}, fmt.Errorf("insert membership: %w", err)
	}
	// One-time: stamping used_at inside the same transaction is what makes a
	// concurrent second redemption impossible rather than merely unlikely.
	if _, err := tx.Exec(`UPDATE user_invites SET used_at = ?, used_by_user = ? WHERE token_hash = ?`,
		now, user.ID, hashToken(token)); err != nil {
		return AcceptUserInviteResult{}, fmt.Errorf("redeem invite: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return AcceptUserInviteResult{}, fmt.Errorf("commit accept invite: %w", err)
	}

	sessionToken, expires, err := s.issueSessionLocked(user.ID, tenant.ID, member.Role)
	if err != nil {
		return AcceptUserInviteResult{}, err
	}
	return AcceptUserInviteResult{
		User: user, Tenant: tenant, Member: member,
		Token: sessionToken, Expires: expires, CreatedOK: createdOK,
	}, nil
}

// lookupUserInviteLocked fetches an invite by its hash. Caller holds s.mu.
func (s *Service) lookupUserInviteLocked(token string) (UserInvite, error) {
	var record UserInvite
	err := s.db.QueryRow(
		`SELECT id, tenant_id, role, created_by, created_at, expires_at, used_at, used_by_user, revoked_at
		 FROM user_invites WHERE token_hash = ?`, hashToken(token),
	).Scan(&record.ID, &record.TenantID, &record.Role, &record.CreatedBy, &record.CreatedAt,
		&record.ExpiresAt, &record.UsedAt, &record.UsedByUser, &record.RevokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return UserInvite{}, ErrInvalidInvite
	}
	if err != nil {
		return UserInvite{}, fmt.Errorf("lookup invite: %w", err)
	}
	return record, nil
}

// tenantLocked reads a tenant. Caller holds s.mu.
func (s *Service) tenantLocked(id string) (identity.Tenant, bool) {
	var tenant identity.Tenant
	err := s.db.QueryRow(`SELECT id, name, plan, created_at FROM tenants WHERE id = ?`, id).
		Scan(&tenant.ID, &tenant.Name, &tenant.Plan, &tenant.CreatedAt)
	if err != nil {
		return identity.Tenant{}, false
	}
	return tenant, true
}

// isMemberLocked reports membership. Caller holds s.mu.
func (s *Service) isMemberLocked(tenantID, userID string) bool {
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM memberships WHERE tenant_id = ? AND user_id = ?`,
		tenantID, userID).Scan(&count); err != nil {
		return false
	}
	return count > 0
}

// RemoveMember drops a user's membership from a tenant.
//
// Refuses to remove an owner: the owner is both the tenant's administrator of last
// resort and the node-ownership fallback, so removing it would leave a tenant
// nobody can administer and nodes nobody owns.
func (s *Service) RemoveMember(tenantID, userID string, actor MemberActor) error {
	tenantID = strings.TrimSpace(tenantID)
	userID = strings.TrimSpace(userID)
	if tenantID == "" || userID == "" {
		return ErrMemberNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.mayManageMembersLocked(tenantID, actor) {
		return ErrNotMemberActor
	}
	var role string
	err := s.db.QueryRow(`SELECT role FROM memberships WHERE tenant_id = ? AND user_id = ?`, tenantID, userID).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrMemberNotFound
	}
	if err != nil {
		return fmt.Errorf("lookup member: %w", err)
	}
	if identity.NormalizeTenantRole(role) == identity.RoleTenantOwner {
		return ErrOwnershipProtected
	}
	if _, err := s.db.Exec(`DELETE FROM memberships WHERE tenant_id = ? AND user_id = ?`, tenantID, userID); err != nil {
		return fmt.Errorf("remove member: %w", err)
	}
	// Drop that tenant's sessions for the removed user: a live session would keep
	// carrying the membership until it expired, so "removed" would not be true yet.
	if _, err := s.db.Exec(`DELETE FROM sessions WHERE user_id = ? AND tenant_id = ?`, userID, tenantID); err != nil {
		return fmt.Errorf("revoke removed member's sessions: %w", err)
	}
	return nil
}

// SetMemberRole changes a member's tenant role.
//
// Owner is refused in both directions: granting it is an ownership handover, and
// demoting it orphans the tenant. Both are deliberate actions with side effects
// (billing ownership, node-ownership fallback), not a dropdown change.
func (s *Service) SetMemberRole(tenantID, userID, role string, actor MemberActor) (identity.Membership, error) {
	tenantID = strings.TrimSpace(tenantID)
	userID = strings.TrimSpace(userID)
	normalized := identity.NormalizeTenantRole(role)
	if normalized == "" {
		return identity.Membership{}, ErrInvalidMemberRole
	}
	if normalized == identity.RoleTenantOwner {
		return identity.Membership{}, ErrCannotGrantOwner
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.mayManageMembersLocked(tenantID, actor) {
		return identity.Membership{}, ErrNotMemberActor
	}
	var current string
	err := s.db.QueryRow(`SELECT role FROM memberships WHERE tenant_id = ? AND user_id = ?`, tenantID, userID).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return identity.Membership{}, ErrMemberNotFound
	}
	if err != nil {
		return identity.Membership{}, fmt.Errorf("lookup member: %w", err)
	}
	if identity.NormalizeTenantRole(current) == identity.RoleTenantOwner {
		return identity.Membership{}, ErrOwnershipProtected
	}
	if _, err := s.db.Exec(`UPDATE memberships SET role = ? WHERE tenant_id = ? AND user_id = ?`,
		normalized, tenantID, userID); err != nil {
		return identity.Membership{}, fmt.Errorf("update member role: %w", err)
	}
	// Sessions are NOT deleted: Authenticate re-reads the role on every request, so
	// the new role applies immediately anyway, and deleting sessions would sign the
	// person out for a routine permission change.
	return identity.Membership{TenantID: tenantID, UserID: userID, Role: normalized}, nil
}

// MemberView is a member plus the facts the UI needs to render a row.
type MemberView struct {
	UserID   string `json:"user_id"`
	Email    string `json:"email,omitempty"`
	Role     string `json:"role"`
	JoinedAt string `json:"joined_at,omitempty"`
}

// MemberViews joins memberships with user emails for display.
//
// A membership whose user row is gone is still reported (with an empty email):
// hiding it would make the membership look repaired when it is not.
func (s *Service) MemberViews(tenantID string) ([]MemberView, error) {
	members, err := s.Members(tenantID)
	if err != nil {
		return nil, err
	}
	out := make([]MemberView, 0, len(members))
	for _, member := range members {
		view := MemberView{UserID: member.UserID, Role: member.Role, JoinedAt: member.JoinedAt}
		if user, ok := s.User(member.UserID); ok {
			view.Email = user.Email
		}
		out = append(out, view)
	}
	return out, nil
}
