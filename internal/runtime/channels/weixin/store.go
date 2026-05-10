package weixin

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/platform/fsutil"
)

// ContextTokenInspection describes the persisted context-token cache for one Weixin account.
type ContextTokenInspection struct {
	AccountID   string    `json:"account_id"`
	UserID      string    `json:"user_id,omitempty"`
	TokenCount  int       `json:"token_count"`
	UpdatedAt   time.Time `json:"updated_at,omitempty"`
	TokenMasked string    `json:"token_masked,omitempty"`
	Token       string    `json:"token,omitempty"`
}

type stateStore struct {
	root string
}

func newStateStore(stateDir, accountID string) *stateStore {
	accountID = normalizeAccountID(accountID)
	return &stateStore{
		root: filepath.Join(strings.TrimSpace(stateDir), "channels", channelName, accountID),
	}
}

func (s *stateStore) Root() string {
	return s.root
}

func (s *stateStore) Ensure() error {
	return os.MkdirAll(s.root, 0755)
}

func (s *stateStore) AccountPath() string {
	return filepath.Join(s.root, "account.json")
}

func (s *stateStore) CursorPath() string {
	return filepath.Join(s.root, "cursor.json")
}

func (s *stateStore) ContextTokensPath() string {
	return filepath.Join(s.root, "context_tokens.json")
}

func (s *stateStore) LoadAccount() (*accountState, error) {
	var state accountState
	if err := s.readJSON(s.AccountPath(), &state); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	return &state, nil
}

func (s *stateStore) SaveAccount(state *accountState) error {
	if state == nil {
		return nil
	}
	copy := *state
	if copy.UpdatedAt.IsZero() {
		copy.UpdatedAt = time.Now()
	}
	return s.writeJSON(s.AccountPath(), copy)
}

func (s *stateStore) ClearAccount() error {
	if err := os.Remove(s.AccountPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (s *stateStore) LoadCursor() (string, error) {
	var state cursorState
	if err := s.readJSON(s.CursorPath(), &state); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(state.GetUpdatesBuf), nil
}

func (s *stateStore) SaveCursor(cursor string) error {
	return s.writeJSON(s.CursorPath(), cursorState{
		GetUpdatesBuf: strings.TrimSpace(cursor),
		UpdatedAt:     time.Now(),
	})
}

func (s *stateStore) ClearCursor() error {
	if err := os.Remove(s.CursorPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (s *stateStore) LoadContextTokens() (map[string]string, error) {
	var state contextTokensState
	if err := s.readJSON(s.ContextTokensPath(), &state); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]string{}, nil
		}
		return nil, err
	}
	if state.Tokens == nil {
		return map[string]string{}, nil
	}
	return state.Tokens, nil
}

func (s *stateStore) LookupContextToken(userID string) (string, error) {
	tokens, err := s.LoadContextTokens()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(tokens[strings.TrimSpace(userID)]), nil
}

func (s *stateStore) SaveContextToken(userID, token string) error {
	userID = strings.TrimSpace(userID)
	token = strings.TrimSpace(token)
	if userID == "" || token == "" {
		return nil
	}
	tokens, err := s.LoadContextTokens()
	if err != nil {
		return err
	}
	tokens[userID] = token
	return s.writeJSON(s.ContextTokensPath(), contextTokensState{
		Tokens:    tokens,
		UpdatedAt: time.Now(),
	})
}

func (s *stateStore) ClearContextTokens() error {
	if err := os.Remove(s.ContextTokensPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (s *stateStore) RemoveAll() error {
	if err := os.RemoveAll(s.root); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (s *stateStore) readJSON(path string, out any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

func (s *stateStore) writeJSON(path string, value any) error {
	if err := s.Ensure(); err != nil {
		return err
	}
	return fsutil.WriteJSONAtomic(path, value, 0644)
}

// InspectContextTokens reports the persisted Weixin context-token cache for one
// account and optionally reveals the token for a single sender.
func InspectContextTokens(stateDir, accountID, userID string, reveal bool) (ContextTokenInspection, error) {
	store := newStateStore(stateDir, accountID)
	var state contextTokensState
	if err := store.readJSON(store.ContextTokensPath(), &state); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ContextTokenInspection{AccountID: normalizeAccountID(accountID), UserID: strings.TrimSpace(userID)}, nil
		}
		return ContextTokenInspection{}, err
	}
	if state.Tokens == nil {
		state.Tokens = map[string]string{}
	}
	userID = strings.TrimSpace(userID)
	token := strings.TrimSpace(state.Tokens[userID])
	inspection := ContextTokenInspection{
		AccountID:   normalizeAccountID(accountID),
		UserID:      userID,
		TokenCount:  len(state.Tokens),
		UpdatedAt:   state.UpdatedAt,
		TokenMasked: maskContextToken(token),
	}
	if reveal {
		inspection.Token = token
	}
	return inspection, nil
}

func maskContextToken(token string) string {
	token = strings.TrimSpace(token)
	if token == "" {
		return ""
	}
	if len(token) <= 8 {
		return strings.Repeat("*", len(token))
	}
	return token[:4] + strings.Repeat("*", len(token)-8) + token[len(token)-4:]
}

func normalizeAccountID(accountID string) string {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return defaultAccountID
	}
	return accountID
}
