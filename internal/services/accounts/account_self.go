// godex-feature: accounts
// 用户中心后端：登录事件留痕、改密码、会话（登录设备）查看与撤销（设计 §3.6「用户中心」）。
// 入口：/auth/* 与 /account/* 路由
// 文档：docs/multi-tenant-commercialization-design.md §3.6
package accounts

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// Login event kinds. Recorded as strings rather than an enum column so a new kind
// is a code change only, and so the audit text is readable in a raw sqlite dump.
const (
	LoginEventRegister         = "register"
	LoginEventLogin            = "login"
	LoginEventLogout           = "logout"
	LoginEventPasswordChanged  = "password_changed"
	LoginEventSessionsRevoked  = "sessions_revoked"
	LoginEventSessionRevoked   = "session_revoked"
	LoginEventJoinedTenant    = "joined_tenant"
)

// ErrWrongPassword is returned when a password change supplies a bad current
// password. Distinct from ErrInvalidCredentials so the UI can say "current
// password is wrong" instead of "invalid email or password".
var ErrWrongPassword = errors.New("current password is incorrect")

// ErrPasswordUnchanged is returned when a new password equals the current one. A
// silent success here would tell the user their password changed when the
// credential is byte-identical.
var ErrPasswordUnchanged = errors.New("new password must differ from the current one")

// ErrSessionNotFound is returned for an unknown session id in this user's list.
var ErrSessionNotFound = errors.New("session not found")

// LoginEvent is one entry of the user's own sign-in history.
type LoginEvent struct {
	ID       int64  `json:"id"`
	TenantID string `json:"tenant_id,omitempty"`
	At       string `json:"at"`
	Event    string `json:"event"`
	SourceIP string `json:"source_ip,omitempty"`
}

// SessionInfo describes one live login session. ID is the rowid, not the token
// hash: the hash is a credential and must never be handed back, while the user
// still needs a stable handle to revoke a session.
type SessionInfo struct {
	ID        int64  `json:"id"`
	TenantID  string `json:"tenant_id,omitempty"`
	Role      string `json:"role,omitempty"`
	CreatedAt string `json:"created_at"`
	ExpiresAt string `json:"expires_at"`
	Current   bool   `json:"current"`
}

// RecordLoginEvent appends one entry to the user's sign-in history.
//
// Best effort by construction: the caller logs a failure rather than failing the
// operation, because refusing a successful sign-in over an audit row would deny
// service for an accounting problem (same reasoning as the consent trail).
func (s *Service) RecordLoginEvent(userID, tenantID, event, sourceIP string) error {
	userID = strings.TrimSpace(userID)
	event = strings.TrimSpace(event)
	if userID == "" || event == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.db.Exec(
		`INSERT INTO login_events (user_id, tenant_id, at, event, source_ip) VALUES (?, ?, ?, ?, ?)`,
		userID, strings.TrimSpace(tenantID), s.timestamp(), event, strings.TrimSpace(sourceIP),
	); err != nil {
		return fmt.Errorf("record login event: %w", err)
	}
	return nil
}

// LoginEvents returns the user's most recent sign-in history, newest first.
//
// Scoped to ONE user by construction: the history names tenants and IPs, so an
// unscoped read would become a map of who signs in from where.
func (s *Service) LoginEvents(userID string, limit int) ([]LoginEvent, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, fmt.Errorf("missing user id")
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(
		`SELECT id, tenant_id, at, event, source_ip FROM login_events WHERE user_id = ? ORDER BY id DESC LIMIT ?`,
		userID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list login events: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []LoginEvent{}
	for rows.Next() {
		var item LoginEvent
		if err := rows.Scan(&item.ID, &item.TenantID, &item.At, &item.Event, &item.SourceIP); err != nil {
			return nil, fmt.Errorf("scan login event: %w", err)
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// ListSessions returns the user's live sessions, newest first, flagging the one
// making the request so the UI can avoid offering "revoke" on itself.
func (s *Service) ListSessions(userID, currentToken string) ([]SessionInfo, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, fmt.Errorf("missing user id")
	}
	current := ""
	if strings.TrimSpace(currentToken) != "" {
		current = hashToken(currentToken)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(
		`SELECT rowid, token_hash, tenant_id, role, created_at, expires_at FROM sessions WHERE user_id = ? ORDER BY created_at DESC`,
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []SessionInfo{}
	for rows.Next() {
		var (
			item   SessionInfo
			hashed string
		)
		if err := rows.Scan(&item.ID, &hashed, &item.TenantID, &item.Role, &item.CreatedAt, &item.ExpiresAt); err != nil {
			return nil, fmt.Errorf("scan session: %w", err)
		}
		item.Current = current != "" && hashed == current
		out = append(out, item)
	}
	return out, rows.Err()
}

// RevokeSession drops one of the user's own sessions.
//
// The user_id is part of the WHERE clause rather than a check performed
// afterwards: "can this user revoke this row" then has exactly one answer, and a
// guessed id from another account cannot even be addressed.
func (s *Service) RevokeSession(userID string, id int64) error {
	userID = strings.TrimSpace(userID)
	if userID == "" || id <= 0 {
		return ErrSessionNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.db.Exec(`DELETE FROM sessions WHERE rowid = ? AND user_id = ?`, id, userID)
	if err != nil {
		return fmt.Errorf("revoke session: %w", err)
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return ErrSessionNotFound
	}
	return nil
}

// RevokeOtherSessions drops every session except keepToken, returning how many
// were removed. The current session is kept so the user is not signed out of the
// device they are using to press the button.
func (s *Service) RevokeOtherSessions(userID, keepToken string) (int, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return 0, fmt.Errorf("missing user id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(keepToken) == "" {
		result, err := s.db.Exec(`DELETE FROM sessions WHERE user_id = ?`, userID)
		if err != nil {
			return 0, fmt.Errorf("revoke sessions: %w", err)
		}
		affected, _ := result.RowsAffected()
		return int(affected), nil
	}
	result, err := s.db.Exec(`DELETE FROM sessions WHERE user_id = ? AND token_hash <> ?`, userID, hashToken(keepToken))
	if err != nil {
		return 0, fmt.Errorf("revoke other sessions: %w", err)
	}
	affected, _ := result.RowsAffected()
	return int(affected), nil
}

// ChangePassword verifies the current password, stores a new hash, and revokes
// every OTHER session.
//
// Revoking the others is the point of changing a password: if the credential may
// have leaked, leaving the attacker's existing sessions alive would make the
// change cosmetic. The caller's own session survives so the UI stays usable.
func (s *Service) ChangePassword(userID, keepToken, currentPassword, newPassword string) error {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return fmt.Errorf("missing user id")
	}
	if len(strings.TrimSpace(newPassword)) < minPasswordLength {
		return ErrWeakPassword
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	var hashText string
	err := s.db.QueryRow(`SELECT password_hash FROM users WHERE id = ?`, userID).Scan(&hashText)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("lookup user: %w", err)
	}
	if err != nil {
		return fmt.Errorf("lookup user: %w", err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hashText), []byte(currentPassword)); err != nil {
		return ErrWrongPassword
	}
	// Compared against the hash rather than the plaintext: the stored value is the
	// only trustworthy record of the current credential.
	if err := bcrypt.CompareHashAndPassword([]byte(hashText), []byte(newPassword)); err == nil {
		return ErrPasswordUnchanged
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	if _, err := s.db.Exec(`UPDATE users SET password_hash = ? WHERE id = ?`, string(hash), userID); err != nil {
		return fmt.Errorf("update password: %w", err)
	}

	if strings.TrimSpace(keepToken) == "" {
		if _, err := s.db.Exec(`DELETE FROM sessions WHERE user_id = ?`, userID); err != nil {
			return fmt.Errorf("revoke sessions after password change: %w", err)
		}
		return nil
	}
	if _, err := s.db.Exec(`DELETE FROM sessions WHERE user_id = ? AND token_hash <> ?`, userID, hashToken(keepToken)); err != nil {
		return fmt.Errorf("revoke other sessions after password change: %w", err)
	}
	return nil
}
