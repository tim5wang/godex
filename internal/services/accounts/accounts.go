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
)

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
		`CREATE TABLE IF NOT EXISTS tenant_policies (
			tenant_id TEXT PRIMARY KEY,
			sharing_enabled INTEGER NOT NULL DEFAULT 1,
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
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("init accounts schema: %w", err)
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
type TenantPolicy struct {
	TenantID       string
	SharingEnabled bool
	UpdatedAt      string
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
	return TenantPolicy{TenantID: tenantID, SharingEnabled: enabled, UpdatedAt: now}, nil
}

// TenantSummary is one row of the superadmin tenant list: the tenant plus the
// counts an operator needs to decide what to do with it.
type TenantSummary struct {
	Tenant         identity.Tenant
	SharingEnabled bool
	MemberCount    int
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
