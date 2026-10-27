// godex-feature: accounts
// 多租户账户：邮箱+密码注册登录、Tenant/User/Membership 存储、会话令牌（设计 §3.1/§3.5）。
// 入口：/auth/* API、Web UI 登录页
// 文档：docs/multi-tenant-commercialization-design.md
package accounts

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"

	"github.com/tim5wang/godex/internal/core/identity"
	"github.com/tim5wang/godex/internal/platform/idgen"
)

// SessionTokenPrefix marks session credentials so a token can be recognized by
// shape before it is looked up, mirroring the existing credential families.
const SessionTokenPrefix = "gxs_"

var (
	// ErrEmailTaken is returned when registering an email that already exists.
	ErrEmailTaken = errors.New("email already registered")
	// ErrInvalidCredentials covers both unknown email and wrong password. One
	// error for both on purpose: distinguishing them tells an attacker which
	// emails are registered.
	ErrInvalidCredentials = errors.New("invalid email or password")
	// ErrInvalidSession is returned for an unknown, revoked, or expired token.
	ErrInvalidSession = errors.New("invalid or expired session")
	// ErrInvalidEmail is returned for a malformed email address.
	ErrInvalidEmail = errors.New("invalid email address")
	// ErrWeakPassword is returned when a password is too short to store.
	ErrWeakPassword = errors.New("password must be at least 8 characters")
	// ErrInvalidJoinToken covers unknown, expired, already-used, and revoked join
	// tokens. One error for all four on purpose: a caller holding an invalid code
	// gains nothing from learning WHICH way it is invalid, and distinguishing them
	// would let someone probe for issued codes.
	ErrInvalidJoinToken = errors.New("invalid, expired, or already used join token")
)

// JoinTokenPrefix marks tenant enrolment codes so a token can be recognized by
// shape before it is looked up, mirroring the other credential families
// (gxs_ sessions, ck_ node credentials, nk_ node proxy tokens).
const JoinTokenPrefix = "gjt_"

// joinTokenTTL is how long an unused join token stays valid. Short by design:
// an enrolment code is handed to a device that is expected to redeem it now, and
// a leaked one should stop working without anyone remembering to revoke it.
const joinTokenTTL = 24 * time.Hour

// minPasswordLength is the shortest password we will hash. Long enough to matter
// for online guessing, short enough not to fight the user.
const minPasswordLength = 8

// sessionTTL bounds how long a login lasts. Finite on purpose: a leaked session
// token stops working without any further action.
const sessionTTL = 30 * 24 * time.Hour

// Service owns tenants, users, memberships and sessions.
type Service struct {
	dir string
	db  *sql.DB
	mu  sync.Mutex
	now func() time.Time
}

// New opens (creating if needed) the account store under stateDir.
func New(stateDir string) (*Service, error) {
	if strings.TrimSpace(stateDir) == "" {
		return nil, fmt.Errorf("missing accounts store directory")
	}
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		return nil, fmt.Errorf("mkdir accounts store: %w", err)
	}
	path := filepath.Join(stateDir, "accounts.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open accounts sqlite store: %w", err)
	}
	service := &Service{dir: stateDir, db: db, now: time.Now}
	if err := service.init(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return service, nil
}

// Close releases the underlying database handle.
func (s *Service) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// SetNow overrides the clock (tests).
func (s *Service) SetNow(now func() time.Time) {
	if now != nil {
		s.now = now
	}
}

func (s *Service) timestamp() string {
	return s.now().UTC().Format(time.RFC3339Nano)
}

func (s *Service) init() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS tenants (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			plan TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS users (
			id TEXT PRIMARY KEY,
			email TEXT NOT NULL UNIQUE,
			password_hash TEXT NOT NULL,
			platform_role TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS memberships (
			tenant_id TEXT NOT NULL,
			user_id TEXT NOT NULL,
			role TEXT NOT NULL,
			joined_at TEXT NOT NULL,
			PRIMARY KEY (tenant_id, user_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_memberships_user ON memberships(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_memberships_tenant ON memberships(tenant_id)`,
		// tenant_policies holds platform-level knobs the superadmin sets per tenant
		// (decision ④). A separate table rather than columns on tenants: these are
		// policy decisions, not tenant identity, and they get their own audit trail
		// as features are added.
		//
		// max_nodes is the tenant's node allowance (design §7, basic tier); 0 means
		// unlimited, which is the same "absence is permissive" default sharing_enabled
		// uses.
		`CREATE TABLE IF NOT EXISTS tenant_policies (
			tenant_id TEXT PRIMARY KEY,
			sharing_enabled INTEGER NOT NULL DEFAULT 1,
			max_nodes INTEGER NOT NULL DEFAULT 0,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS sessions (
			token_hash TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			tenant_id TEXT NOT NULL DEFAULT '',
			role TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			expires_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions(user_id)`,
		// join_tokens are one-time, tenant-scoped enrolment codes (design §3.5).
		// Only the hash is stored: the plaintext is shown once at creation, so a
		// database leak does not hand out the ability to enrol nodes. SHA-256 is
		// enough here because the value is 32 random bytes (unlike a password, which
		// is low-entropy and needs a slow hash).
		`CREATE TABLE IF NOT EXISTS join_tokens (
			token_hash TEXT PRIMARY KEY,
			id TEXT NOT NULL UNIQUE,
			tenant_id TEXT NOT NULL,
			owner_user_id TEXT NOT NULL DEFAULT '',
			label TEXT NOT NULL DEFAULT '',
			created_by TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			expires_at TEXT NOT NULL,
			used_at TEXT NOT NULL DEFAULT '',
			used_by_node TEXT NOT NULL DEFAULT '',
			revoked_at TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_join_tokens_tenant ON join_tokens(tenant_id)`,
		// delegations are a tenant's explicit, time-bounded grant for a platform
		// operator to read its data (design §3.6). Stored apart from any role: the
		// grant is an act by the TENANT, so it must be revocable and expire without
		// touching the operator's account.
		`CREATE TABLE IF NOT EXISTS delegations (
			id TEXT PRIMARY KEY,
			tenant_id TEXT NOT NULL,
			operator_user_id TEXT NOT NULL DEFAULT '',
			granted_by TEXT NOT NULL DEFAULT '',
			scope TEXT NOT NULL DEFAULT '',
			reason TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			expires_at TEXT NOT NULL,
			revoked_at TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_delegations_tenant ON delegations(tenant_id)`,
		`CREATE INDEX IF NOT EXISTS idx_delegations_operator ON delegations(operator_user_id)`,
		// Every use of a delegation is audited, so "we looked" is answerable after
		// the fact: who looked, at which tenant, when, and what they asked for.
		`CREATE TABLE IF NOT EXISTS delegation_audit (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			at TEXT NOT NULL,
			delegation_id TEXT NOT NULL DEFAULT '',
			tenant_id TEXT NOT NULL DEFAULT '',
			operator_user_id TEXT NOT NULL DEFAULT '',
			action TEXT NOT NULL DEFAULT '',
			target TEXT NOT NULL DEFAULT '',
			allowed INTEGER NOT NULL DEFAULT 0,
			reason TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_delegation_audit_tenant ON delegation_audit(tenant_id, at)`,
		// The user-agreement trail (design §3.6 "用户协议"): what a tenant agreed to
		// and when, recorded at signup so it does not depend on the operator
		// remembering to ask later.
		`CREATE TABLE IF NOT EXISTS consent_records (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			tenant_id TEXT NOT NULL,
			user_id TEXT NOT NULL DEFAULT '',
			terms_version TEXT NOT NULL,
			accepted_at TEXT NOT NULL,
			source_ip TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_consent_records_tenant ON consent_records(tenant_id)`,
		// Login history for the user centre (design §3.6 "用户中心"). A separate
		// append-only table because the `sessions` table is NOT a history: it holds
		// live tokens and Logout DELETEs the row, so once a session ends there is no
		// record it ever existed. Answering "when did I last sign in, and from
		// where?" requires recording the event itself.
		//
		// source_ip is recorded for the same reason the consent trail records it: it
		// is the part a user can act on ("that wasn't me"). No user-agent string: it
		// is low-value for the user and high-value to an attacker correlating clients.
		`CREATE TABLE IF NOT EXISTS login_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id TEXT NOT NULL,
			tenant_id TEXT NOT NULL DEFAULT '',
			at TEXT NOT NULL,
			event TEXT NOT NULL,
			source_ip TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_login_events_user ON login_events(user_id, at)`,
		// user_invites let a tenant owner/admin add a PERSON (design §3.6), mirroring
		// join_tokens for nodes (§3.5). Reusing the invite-code shape rather than
		// adding email delivery keeps this offline-first and consistent with how
		// nodes already enrol; the code is handed over out of band.
		//
		// role is the membership the redeemer gets. Stored explicitly rather than
		// implied: an invite that silently granted owner would be a privilege
		// escalation, so the role is chosen at issuance and validated against what
		// the issuer may grant.
		`CREATE TABLE IF NOT EXISTS user_invites (
			token_hash TEXT PRIMARY KEY,
			id TEXT NOT NULL UNIQUE,
			tenant_id TEXT NOT NULL,
			role TEXT NOT NULL DEFAULT 'member',
			created_by TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			expires_at TEXT NOT NULL,
			used_at TEXT NOT NULL DEFAULT '',
			used_by_user TEXT NOT NULL DEFAULT '',
			revoked_at TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_user_invites_tenant ON user_invites(tenant_id)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("init accounts schema: %w", err)
		}
	}
	// CREATE TABLE IF NOT EXISTS covers fresh databases; this covers upgrades of
	// existing ones, where the table already exists without the newer column.
	if err := s.migrateTenantPolicyColumns(); err != nil {
		return err
	}
	return nil
}

// migrateTenantPolicyColumns adds tenant_policies columns introduced after the
// table first shipped.
//
// This matters for the allowance in particular: an upgraded center must default
// to unlimited (0) rather than to zero nodes, because "unlimited" is the
// behaviour it had before the column existed. Getting this backwards would
// silently stop every existing tenant from enrolling any node.
func (s *Service) migrateTenantPolicyColumns() error {
	rows, err := s.db.Query(`PRAGMA table_info(tenant_policies)`)
	if err != nil {
		return fmt.Errorf("inspect tenant_policies: %w", err)
	}
	defer func() { _ = rows.Close() }()
	have := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return fmt.Errorf("scan tenant_policies column: %w", err)
		}
		have[name] = true
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate tenant_policies columns: %w", err)
	}
	adds := []string{"max_nodes INTEGER NOT NULL DEFAULT 0"}
	for _, add := range adds {
		col := strings.Fields(add)[0]
		if have[col] {
			continue
		}
		if _, err := s.db.Exec(`ALTER TABLE tenant_policies ADD COLUMN ` + add); err != nil {
			return fmt.Errorf("add tenant_policies.%s: %w", col, err)
		}
	}
	return nil
}

// hashToken hashes a session token for storage. Session tokens are high-entropy
// random values, so a plain SHA-256 is the right tool here (unlike passwords,
// which need a slow hash because they are low-entropy and guessable).
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// RegisterInput is a self-service signup: one person creating their own tenant.
type RegisterInput struct {
	Email    string
	Password string
	// TenantName names the tenant created for this user. Empty falls back to the
	// email's local part, so a signup never fails for a missing display name.
	TenantName string
}

// RegisterResult is what a successful signup returns.
type RegisterResult struct {
	User    identity.User
	Tenant  identity.Tenant
	Member  identity.Membership
	Token   string
	Expires time.Time
	Created bool
}

// Register creates a user and a tenant they own, then signs them in.
//
// Signup always creates a fresh tenant with the caller as owner: this is the SaaS
// self-service path (design §3.5). Joining an existing tenant is a separate,
// explicitly authorized action (CreateJoinToken/AcceptJoinToken) rather than
// something a signup can do by guessing a tenant id.
func (s *Service) Register(input RegisterInput) (RegisterResult, error) {
	email := identity.NormalizeEmail(input.Email)
	if !validEmail(email) {
		return RegisterResult{}, ErrInvalidEmail
	}
	if len(strings.TrimSpace(input.Password)) < minPasswordLength {
		return RegisterResult{}, ErrWeakPassword
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
	if err != nil {
		return RegisterResult{}, fmt.Errorf("hash password: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	var existing string
	err = s.db.QueryRow(`SELECT id FROM users WHERE email = ?`, email).Scan(&existing)
	switch {
	case err == nil:
		return RegisterResult{}, ErrEmailTaken
	case !errors.Is(err, sql.ErrNoRows):
		return RegisterResult{}, fmt.Errorf("lookup user: %w", err)
	}

	now := s.timestamp()
	user := identity.User{
		ID:        idgen.New("usr_", 8),
		Email:     email,
		CreatedAt: now,
	}
	// The hash stays in a local: it is required for the INSERT but must not ride
	// along in the returned struct, where it would be one careless writeJSON away
	// from being served to the client.
	passwordHash := string(hash)
	tenantName := strings.TrimSpace(input.TenantName)
	if tenantName == "" {
		tenantName = localPart(email)
	}
	tenant := identity.Tenant{
		ID:        idgen.New("tnt_", 8),
		Name:      tenantName,
		Plan:      identity.EntitlementRelayOnly,
		CreatedAt: now,
	}
	member := identity.Membership{
		TenantID: tenant.ID,
		UserID:   user.ID,
		Role:     identity.RoleTenantOwner,
		JoinedAt: now,
	}

	tx, err := s.db.Begin()
	if err != nil {
		return RegisterResult{}, fmt.Errorf("begin register: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`INSERT INTO users (id, email, password_hash, platform_role, created_at) VALUES (?, ?, ?, ?, ?)`,
		user.ID, user.Email, passwordHash, user.PlatformRole, user.CreatedAt); err != nil {
		return RegisterResult{}, fmt.Errorf("insert user: %w", err)
	}
	if _, err := tx.Exec(`INSERT INTO tenants (id, name, plan, created_at) VALUES (?, ?, ?, ?)`,
		tenant.ID, tenant.Name, tenant.Plan, tenant.CreatedAt); err != nil {
		return RegisterResult{}, fmt.Errorf("insert tenant: %w", err)
	}
	if _, err := tx.Exec(`INSERT INTO memberships (tenant_id, user_id, role, joined_at) VALUES (?, ?, ?, ?)`,
		member.TenantID, member.UserID, member.Role, member.JoinedAt); err != nil {
		return RegisterResult{}, fmt.Errorf("insert membership: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return RegisterResult{}, fmt.Errorf("commit register: %w", err)
	}

	token, expires, err := s.issueSessionLocked(user.ID, tenant.ID, member.Role)
	if err != nil {
		return RegisterResult{}, err
	}
	return RegisterResult{User: user, Tenant: tenant, Member: member, Token: token, Expires: expires, Created: true}, nil
}

// Login verifies an email/password pair and starts a session.
func (s *Service) Login(email, password string) (identity.Principal, string, time.Time, error) {
	normalized := identity.NormalizeEmail(email)

	s.mu.Lock()
	defer s.mu.Unlock()

	var (
		user     identity.User
		hashText string
	)
	err := s.db.QueryRow(`SELECT id, email, password_hash, platform_role, created_at FROM users WHERE email = ?`, normalized).
		Scan(&user.ID, &user.Email, &hashText, &user.PlatformRole, &user.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		// Hash anyway so a missing user and a wrong password take comparable time:
		// otherwise response latency reveals which emails are registered.
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
		return identity.Principal{}, "", time.Time{}, ErrInvalidCredentials
	}
	if err != nil {
		return identity.Principal{}, "", time.Time{}, fmt.Errorf("lookup user: %w", err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hashText), []byte(password)); err != nil {
		return identity.Principal{}, "", time.Time{}, ErrInvalidCredentials
	}
	user.PasswordHash = hashText

	tenantID, role, err := s.primaryMembershipLocked(user.ID)
	if err != nil {
		return identity.Principal{}, "", time.Time{}, err
	}
	token, expires, err := s.issueSessionLocked(user.ID, tenantID, role)
	if err != nil {
		return identity.Principal{}, "", time.Time{}, err
	}
	return identity.PrincipalForUser(tenantID, user.ID, user.PlatformRole, role, "session"), token, expires, nil
}

// Authenticate resolves a session token into a Principal.
//
// The Principal is rebuilt from stored facts (user + membership) rather than from
// whatever the session row last recorded, so a role change or a revocation takes
// effect on the next request instead of lingering until the session expires.
func (s *Service) Authenticate(token string) (identity.Principal, bool) {
	token = strings.TrimSpace(token)
	if !strings.HasPrefix(token, SessionTokenPrefix) {
		return identity.Principal{}, false
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	var (
		userID, tenantID, expiresAt string
	)
	err := s.db.QueryRow(`SELECT user_id, tenant_id, expires_at FROM sessions WHERE token_hash = ?`, hashToken(token)).
		Scan(&userID, &tenantID, &expiresAt)
	if err != nil {
		return identity.Principal{}, false
	}
	if !s.now().Before(parseTime(expiresAt)) {
		// Expired: drop the row now rather than leaving dead credentials behind.
		_, _ = s.db.Exec(`DELETE FROM sessions WHERE token_hash = ?`, hashToken(token))
		return identity.Principal{}, false
	}

	var platformRole string
	if err := s.db.QueryRow(`SELECT platform_role FROM users WHERE id = ?`, userID).Scan(&platformRole); err != nil {
		return identity.Principal{}, false
	}
	// Resolve the CURRENT membership: a role change or removal must apply at once.
	role := ""
	if tenantID != "" {
		if err := s.db.QueryRow(`SELECT role FROM memberships WHERE tenant_id = ? AND user_id = ?`, tenantID, userID).Scan(&role); err != nil {
			// Membership revoked: the session no longer has access to that tenant.
			return identity.Principal{}, false
		}
	}
	return identity.PrincipalForUser(tenantID, userID, platformRole, role, "session"), true
}

// Logout removes a session. Idempotent: logging out twice is not an error.
func (s *Service) Logout(token string) error {
	token = strings.TrimSpace(token)
	if !strings.HasPrefix(token, SessionTokenPrefix) {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`DELETE FROM sessions WHERE token_hash = ?`, hashToken(token))
	return err
}

// issueSessionLocked mints and stores a session token. Caller holds s.mu.
func (s *Service) issueSessionLocked(userID, tenantID, role string) (string, time.Time, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, fmt.Errorf("generate session token: %w", err)
	}
	token := SessionTokenPrefix + hex.EncodeToString(raw)
	expires := s.now().UTC().Add(sessionTTL)
	if _, err := s.db.Exec(
		`INSERT INTO sessions (token_hash, user_id, tenant_id, role, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?)`,
		hashToken(token), userID, tenantID, role, s.timestamp(), expires.Format(time.RFC3339Nano),
	); err != nil {
		return "", time.Time{}, fmt.Errorf("store session: %w", err)
	}
	return token, expires, nil
}

// primaryMembershipLocked returns the user's tenant and role. Caller holds s.mu.
//
// A user with several memberships resolves to the oldest one: deterministic, and
// tenant switching is an explicit future action rather than an accident of map
// ordering.
func (s *Service) primaryMembershipLocked(userID string) (string, string, error) {
	var tenantID, role string
	err := s.db.QueryRow(
		`SELECT tenant_id, role FROM memberships WHERE user_id = ? ORDER BY joined_at ASC, tenant_id ASC LIMIT 1`,
		userID,
	).Scan(&tenantID, &role)
	if errors.Is(err, sql.ErrNoRows) {
		// A platform-admin user without a tenant is legitimate (decision ⑨): the
		// platform role is global, not a tenant membership.
		return "", "", nil
	}
	if err != nil {
		return "", "", fmt.Errorf("lookup membership: %w", err)
	}
	return tenantID, role, nil
}

// Tenant returns the tenant record for id, if it exists.
func (s *Service) Tenant(id string) (identity.Tenant, bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		return identity.Tenant{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var tenant identity.Tenant
	err := s.db.QueryRow(`SELECT id, name, plan, created_at FROM tenants WHERE id = ?`, id).
		Scan(&tenant.ID, &tenant.Name, &tenant.Plan, &tenant.CreatedAt)
	if err != nil {
		return identity.Tenant{}, false
	}
	return tenant, true
}

// User returns the user record for id, if it exists. The password hash is
// omitted on purpose: callers of this method are rendering identity, never
// verifying a password (that is Login's job).
func (s *Service) User(id string) (identity.User, bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		return identity.User{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var user identity.User
	err := s.db.QueryRow(`SELECT id, email, platform_role, created_at FROM users WHERE id = ?`, id).
		Scan(&user.ID, &user.Email, &user.PlatformRole, &user.CreatedAt)
	if err != nil {
		return identity.User{}, false
	}
	return user, true
}

// TenantPolicy is the superadmin's per-tenant policy (decision ④).
// SharingEnabled gates whether a tenant's members may expose nodes to each other.
// MaxNodes is the tenant's node allowance (design §7, basic tier).
type TenantPolicy struct {
	TenantID       string
	SharingEnabled bool
	// MaxNodes caps how many nodes this tenant may own. 0 means UNLIMITED, which
	// is also the default for a tenant with no stored policy row: absence is
	// permissive here for the same reason it is for SharingEnabled — the limit is
	// an additive commercial control, and defaulting it to "no nodes allowed"
	// would break every tenant that existed before the column did.
	MaxNodes  int
	UpdatedAt string
}

// TenantSharingEnabled reports whether tenant members may expose their nodes to
// peers. A tenant with no stored policy defaults to ENABLED, which preserves the
// pre-policy behaviour (node visibility alone decided reach) and keeps this
// switch purely additive: the superadmin turns it OFF, nodes default to private
// on their own (decision ⑥).
func (s *Service) TenantSharingEnabled(tenantID string) bool {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tenantSharingEnabledLocked(tenantID)
}

// SetTenantSharing sets the sharing switch for a tenant. It does not require the
// tenant to exist: a superadmin may set policy before any node is claimed, and
// refusing that would force an ordering nobody needs.
func (s *Service) SetTenantSharing(tenantID string, enabled bool) (TenantPolicy, error) {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return TenantPolicy{}, fmt.Errorf("missing tenant id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	flag := 0
	if enabled {
		flag = 1
	}
	now := s.timestamp()
	if _, err := s.db.Exec(
		`INSERT INTO tenant_policies (tenant_id, sharing_enabled, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT(tenant_id) DO UPDATE SET sharing_enabled = excluded.sharing_enabled, updated_at = excluded.updated_at`,
		tenantID, flag, now,
	); err != nil {
		return TenantPolicy{}, fmt.Errorf("set tenant sharing: %w", err)
	}
	return TenantPolicy{TenantID: tenantID, SharingEnabled: enabled, MaxNodes: s.tenantMaxNodesLocked(tenantID), UpdatedAt: now}, nil
}

// TenantMaxNodes returns the tenant's node allowance (design §7). 0 means
// unlimited. An unknown tenant returns 0 for the same reason SetTenantSharing
// accepts one: policy may be set before the tenant exists.
func (s *Service) TenantMaxNodes(tenantID string) int {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tenantMaxNodesLocked(tenantID)
}

// SetTenantMaxNodes sets the tenant's node allowance. maxNodes <= 0 clears the
// limit rather than meaning "no nodes": the operator expresses "unlimited" by
// the same value the default uses, so there is one encoding rather than two.
func (s *Service) SetTenantMaxNodes(tenantID string, maxNodes int) (TenantPolicy, error) {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return TenantPolicy{}, fmt.Errorf("missing tenant id")
	}
	if maxNodes < 0 {
		maxNodes = 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.timestamp()
	// Only max_nodes is written, so this cannot silently reset the sharing
	// switch an operator set separately.
	if _, err := s.db.Exec(
		`INSERT INTO tenant_policies (tenant_id, max_nodes, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT(tenant_id) DO UPDATE SET max_nodes = excluded.max_nodes, updated_at = excluded.updated_at`,
		tenantID, maxNodes, now,
	); err != nil {
		return TenantPolicy{}, fmt.Errorf("set tenant node limit: %w", err)
	}
	return TenantPolicy{
		TenantID:       tenantID,
		SharingEnabled: s.tenantSharingEnabledLocked(tenantID),
		MaxNodes:       maxNodes,
		UpdatedAt:      now,
	}, nil
}

// TenantSummary is one row of the superadmin tenant list: the tenant plus the
// counts an operator needs to decide what to do with it.
type TenantSummary struct {
	Tenant         identity.Tenant
	SharingEnabled bool
	MemberCount    int
	// MaxNodes is the tenant's node allowance; 0 means unlimited. The node count
	// itself is not here because this store does not own the node registry — the
	// admin route pairs the two.
	MaxNodes int
}

// ListTenants returns every tenant with its member count and sharing state.
//
// This is a platform-admin surface (design §3.6): it crosses tenant boundaries by
// design, so the route that exposes it must be operator-only.
func (s *Service) ListTenants() ([]TenantSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rows, err := s.db.Query(`SELECT id, name, plan, created_at FROM tenants ORDER BY created_at ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("list tenants: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := []TenantSummary{}
	for rows.Next() {
		var tenant identity.Tenant
		if err := rows.Scan(&tenant.ID, &tenant.Name, &tenant.Plan, &tenant.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan tenant: %w", err)
		}
		out = append(out, TenantSummary{Tenant: tenant, SharingEnabled: true})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tenants: %w", err)
	}

	// Counts and policies are fetched per tenant rather than joined: the tenant
	// count on a single center is small (one per signup) and a correlated query
	// keeps the "no policy row means enabled" default in one place — the same
	// method the authorization seam uses.
	for i := range out {
		id := out[i].Tenant.ID
		var count int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM memberships WHERE tenant_id = ?`, id).Scan(&count); err != nil {
			return nil, fmt.Errorf("count members for %s: %w", id, err)
		}
		out[i].MemberCount = count
		out[i].SharingEnabled = s.tenantSharingEnabledLocked(id)
		out[i].MaxNodes = s.tenantMaxNodesLocked(id)
	}
	return out, nil
}

// tenantSharingEnabledLocked is the shared default logic. Caller holds s.mu.
func (s *Service) tenantSharingEnabledLocked(tenantID string) bool {
	var enabled int
	if err := s.db.QueryRow(`SELECT sharing_enabled FROM tenant_policies WHERE tenant_id = ?`, tenantID).Scan(&enabled); err != nil {
		return true
	}
	return enabled != 0
}

// tenantMaxNodesLocked is the shared default logic. Caller holds s.mu.
//
// A read failure (no row for this tenant) returns 0 = unlimited, matching the
// column default, so the enforcement seam cannot accidentally turn a missing
// policy row into "this tenant may own nothing".
func (s *Service) tenantMaxNodesLocked(tenantID string) int {
	var maxNodes int
	if err := s.db.QueryRow(`SELECT max_nodes FROM tenant_policies WHERE tenant_id = ?`, tenantID).Scan(&maxNodes); err != nil {
		return 0
	}
	if maxNodes < 0 {
		return 0
	}
	return maxNodes
}

// Members lists a tenant's memberships. Operator-only surface, like ListTenants.
func (s *Service) Members(tenantID string) ([]identity.Membership, error) {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("missing tenant id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT tenant_id, user_id, role, joined_at FROM memberships WHERE tenant_id = ? ORDER BY joined_at ASC, user_id ASC`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list members: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []identity.Membership{}
	for rows.Next() {
		var m identity.Membership
		if err := rows.Scan(&m.TenantID, &m.UserID, &m.Role, &m.JoinedAt); err != nil {
			return nil, fmt.Errorf("scan member: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// JoinToken is a tenant-scoped, one-time enrolment code (design §3.5).
//
// It exists to replace the previous self-join credential — the center's FULL web
// token — which meant a SaaS tenant could not add a node without being handed the
// platform master key.
//
// Serialised with explicit snake_case tags: without them Go emits the Go field
// names (ID, TenantID, ...), which no client in this codebase expects. There is
// deliberately NO plaintext field here, so a listed record can never carry a
// reusable code.
type JoinToken struct {
	ID          string `json:"id"`
	TenantID    string `json:"tenant_id"`
	OwnerUserID string `json:"owner_user_id,omitempty"`
	Label       string `json:"label,omitempty"`
	CreatedBy   string `json:"created_by,omitempty"`
	CreatedAt   string `json:"created_at,omitempty"`
	ExpiresAt   string `json:"expires_at,omitempty"`
	// UsedAt/UsedByNode record redemption. A token is one-time: once used_at is
	// set it can never be redeemed again, so a leaked code is useless after the
	// legitimate device has enrolled.
	UsedAt     string `json:"used_at,omitempty"`
	UsedByNode string `json:"used_by_node,omitempty"`
	RevokedAt  string `json:"revoked_at,omitempty"`
}

// Usable reports whether the token can still redeem a node.
func (t JoinToken) Usable(now time.Time) bool {
	if t.UsedAt != "" || t.RevokedAt != "" {
		return false
	}
	expires := parseTime(t.ExpiresAt)
	return expires.IsZero() || now.Before(expires)
}

// CreateJoinToken issues a one-time enrolment code for a tenant.
//
// Returns the PLAINTEXT token, which exists only in this return value: the store
// keeps a hash. A caller that loses it must issue a new one, which is the correct
// trade for an enrolment credential.
func (s *Service) CreateJoinToken(input CreateJoinTokenInput) (JoinToken, string, error) {
	tenantID := strings.TrimSpace(input.TenantID)
	if tenantID == "" {
		return JoinToken{}, "", fmt.Errorf("missing tenant id")
	}
	ownerUserID := strings.TrimSpace(input.OwnerUserID)
	// Validate the requested owner BEFORE the code exists. Redemption is one-time,
	// so discovering a bad owner at redeem time would burn the operator's code and
	// force them to issue another; failing at issuance keeps the code reusable-free
	// and tells the operator immediately.
	if ownerUserID != "" && !s.IsMember(tenantID, ownerUserID) {
		return JoinToken{}, "", fmt.Errorf("owner_user_id is not a member of tenant %s", tenantID)
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return JoinToken{}, "", fmt.Errorf("generate join token: %w", err)
	}
	token := JoinTokenPrefix + hex.EncodeToString(raw)

	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	ttl := input.TTL
	if ttl <= 0 {
		ttl = joinTokenTTL
	}
	record := JoinToken{
		ID:          idgen.New("jt_", 8),
		TenantID:    tenantID,
		OwnerUserID: ownerUserID,
		Label:       strings.TrimSpace(input.Label),
		CreatedBy:   strings.TrimSpace(input.CreatedBy),
		CreatedAt:   now.Format(time.RFC3339Nano),
		ExpiresAt:   now.Add(ttl).Format(time.RFC3339Nano),
	}
	if _, err := s.db.Exec(
		`INSERT INTO join_tokens (token_hash, id, tenant_id, owner_user_id, label, created_by, created_at, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		hashToken(token), record.ID, record.TenantID, record.OwnerUserID, record.Label,
		record.CreatedBy, record.CreatedAt, record.ExpiresAt,
	); err != nil {
		return JoinToken{}, "", fmt.Errorf("create join token: %w", err)
	}
	return record, token, nil
}

// CreateJoinTokenInput describes a join-token issuance request.
//
// OwnerUserID is optional: empty means the redeeming node is registered to the
// tenant but left unowned, for the operator to claim later. Requiring an owner up
// front would force the operator to already know which user will present the
// code, which is exactly the case a join code exists to avoid.
type CreateJoinTokenInput struct {
	TenantID    string
	OwnerUserID string
	Label       string
	CreatedBy   string
	// TTL overrides the default lifetime; zero uses joinTokenTTL.
	TTL time.Duration
}

// RedeemJoinToken validates a one-time code and marks it used, returning the
// tenant and owner the node must be registered under.
//
// Validation and the mark-used write happen under one lock so two concurrent
// redemptions cannot both succeed: the second sees used_at already set. Without
// that, "one-time" would be a comment rather than a property.
func (s *Service) RedeemJoinToken(token, nodeID string) (JoinToken, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return JoinToken{}, ErrInvalidJoinToken
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	record, err := s.lookupJoinTokenLocked(token)
	if err != nil {
		return JoinToken{}, err
	}
	if !record.Usable(s.now().UTC()) {
		return JoinToken{}, ErrInvalidJoinToken
	}
	if _, err := s.db.Exec(
		`UPDATE join_tokens SET used_at = ?, used_by_node = ? WHERE token_hash = ?`,
		s.timestamp(), strings.TrimSpace(nodeID), hashToken(token),
	); err != nil {
		return JoinToken{}, fmt.Errorf("redeem join token: %w", err)
	}
	return record, nil
}

// lookupJoinTokenLocked fetches a token by its hash. Caller holds s.mu.
func (s *Service) lookupJoinTokenLocked(token string) (JoinToken, error) {
	var record JoinToken
	err := s.db.QueryRow(
		`SELECT id, tenant_id, owner_user_id, label, created_by, created_at, expires_at, used_at, used_by_node, revoked_at
		 FROM join_tokens WHERE token_hash = ?`, hashToken(token),
	).Scan(&record.ID, &record.TenantID, &record.OwnerUserID, &record.Label, &record.CreatedBy,
		&record.CreatedAt, &record.ExpiresAt, &record.UsedAt, &record.UsedByNode, &record.RevokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return JoinToken{}, ErrInvalidJoinToken
	}
	if err != nil {
		return JoinToken{}, fmt.Errorf("lookup join token: %w", err)
	}
	return record, nil
}

// ListJoinTokens returns a tenant's issued codes for the admin surface. Hashes are
// never selected, so a caller cannot replay a listed record.
func (s *Service) ListJoinTokens(tenantID string) ([]JoinToken, error) {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("missing tenant id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(
		`SELECT id, tenant_id, owner_user_id, label, created_by, created_at, expires_at, used_at, used_by_node, revoked_at
		 FROM join_tokens WHERE tenant_id = ? ORDER BY created_at DESC, id DESC`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list join tokens: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []JoinToken{}
	for rows.Next() {
		var record JoinToken
		if err := rows.Scan(&record.ID, &record.TenantID, &record.OwnerUserID, &record.Label, &record.CreatedBy,
			&record.CreatedAt, &record.ExpiresAt, &record.UsedAt, &record.UsedByNode, &record.RevokedAt); err != nil {
			return nil, fmt.Errorf("scan join token: %w", err)
		}
		out = append(out, record)
	}
	return out, rows.Err()
}

// RevokeJoinToken invalidates an unused code. Idempotent: revoking an already
// revoked or used token reports not-found only when the id is unknown, so a
// double-click does not surface as an error.
func (s *Service) RevokeJoinToken(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("missing join token id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.db.Exec(
		`UPDATE join_tokens SET revoked_at = ? WHERE id = ? AND revoked_at = ''`, s.timestamp(), id)
	if err != nil {
		return fmt.Errorf("revoke join token: %w", err)
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		// Distinguish "unknown id" from "already revoked/used": only the former is
		// an error, so a retry stays quiet while a typo still fails loudly.
		var exists int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM join_tokens WHERE id = ?`, id).Scan(&exists); err != nil {
			return fmt.Errorf("revoke join token: %w", err)
		}
		if exists == 0 {
			return fmt.Errorf("join token not found: %s", id)
		}
	}
	return nil
}

// TenantOwnerUserID returns the user who owns the tenant (its "owner" membership).
//
// Used as the join token's owner fallback. Falling back to the token's CREATOR
// instead would hand the node to the platform operator whenever an operator issued
// the code — a user who is not a member of that tenant, so the node would be owned
// by nobody reachable and, being private by default, would be usable by nobody.
func (s *Service) TenantOwnerUserID(tenantID string) (string, bool) {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var userID string
	err := s.db.QueryRow(
		`SELECT user_id FROM memberships WHERE tenant_id = ? AND role = ? ORDER BY joined_at ASC, user_id ASC LIMIT 1`,
		tenantID, identity.RoleTenantOwner,
	).Scan(&userID)
	if err != nil {
		return "", false
	}
	return userID, true
}

// IsMember reports whether a user belongs to a tenant.
//
// The join path validates the requested owner against this before writing
// ownership: an owner id that matches no membership produces a node nobody in the
// tenant can reach, which looks like a successful enrolment until someone tries to
// use it.
func (s *Service) IsMember(tenantID, userID string) bool {
	tenantID = strings.TrimSpace(tenantID)
	userID = strings.TrimSpace(userID)
	if tenantID == "" || userID == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var count int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM memberships WHERE tenant_id = ? AND user_id = ?`, tenantID, userID,
	).Scan(&count); err != nil {
		return false
	}
	return count > 0
}

// dummyHash is a valid bcrypt hash used to equalize timing on unknown emails.
var dummyHash = []byte("$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy")

func localPart(email string) string {
	if index := strings.IndexByte(email, '@'); index > 0 {
		return email[:index]
	}
	return email
}

// validEmail is a deliberately permissive check: one "@" with non-empty sides and
// no spaces. Full RFC 5322 validation rejects addresses that work in practice, and
// the real proof that an address is reachable is mail delivery, not a regex.
func validEmail(email string) bool {
	if email == "" || strings.ContainsAny(email, " \t\n") {
		return false
	}
	at := strings.IndexByte(email, '@')
	if at <= 0 || at != strings.LastIndexByte(email, '@') {
		return false
	}
	local, domain := email[:at], email[at+1:]
	if local == "" || domain == "" {
		return false
	}
	// A domain without a dot cannot receive mail.
	return strings.ContainsRune(domain, '.') && !strings.HasPrefix(domain, ".") && !strings.HasSuffix(domain, ".")
}

func parseTime(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}
	}
	return parsed
}
