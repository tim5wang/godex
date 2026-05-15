package usage

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", h)
}

// JSONStore implements Store using a local JSON file.
type JSONStore struct {
	mu   sync.RWMutex
	path string
	data *storeData
}

type storeData struct {
	Keys   []ProxyAPIKey `json:"keys"`
	Models []ProxyModel  `json:"models"`
	Calls  []UsageCall   `json:"calls"`
}

// NewJSONStore creates or loads a JSON-backed store.
func NewJSONStore(stateDir string) (*JSONStore, error) {
	p := filepath.Join(stateDir, "usage-gateway.json")
	s := &JSONStore{
		path: p,
		data: &storeData{
			Keys:   []ProxyAPIKey{},
			Models: []ProxyModel{},
			Calls:  []UsageCall{},
		},
	}
	if _, err := os.Stat(p); err == nil {
		raw, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("read usage store: %w", err)
		}
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, s.data); err != nil {
				return nil, fmt.Errorf("parse usage store: %w", err)
			}
		}
	}
	return s, nil
}

func (s *JSONStore) save() error {
	raw, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal usage store: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0755); err != nil {
		return fmt.Errorf("mkdir usage store: %w", err)
	}
	return os.WriteFile(s.path, raw, 0644)
}

// ListKeys returns all proxy keys (hash field cleared for safety).
func (s *JSONStore) ListKeys() ([]ProxyAPIKey, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ProxyAPIKey, len(s.data.Keys))
	for i, k := range s.data.Keys {
		k.KeyHash = "" // never expose hash in list responses
		out[i] = k
	}
	return out, nil
}

// GetKey returns a key by ID.
func (s *JSONStore) GetKey(id string) (*ProxyAPIKey, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i := range s.data.Keys {
		if s.data.Keys[i].ID == id {
			key := s.data.Keys[i]
			return &key, nil
		}
	}
	return nil, fmt.Errorf("key not found: %s", id)
}

// GetKeyByHash looks up a key by its SHA-256 hash.
func (s *JSONStore) GetKeyByHash(hash string) (*ProxyAPIKey, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i := range s.data.Keys {
		if s.data.Keys[i].KeyHash == hash {
			key := s.data.Keys[i]
			return &key, nil
		}
	}
	return nil, fmt.Errorf("key not found by hash")
}

// CreateKey stores a new key.
func (s *JSONStore) CreateKey(key *ProxyAPIKey) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.Keys = append(s.data.Keys, *key)
	return s.save()
}

// UpdateKey updates an existing key.
func (s *JSONStore) UpdateKey(key *ProxyAPIKey) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Keys {
		if s.data.Keys[i].ID == key.ID {
			s.data.Keys[i] = *key
			return s.save()
		}
	}
	return fmt.Errorf("key not found: %s", key.ID)
}

// ListModels returns all model mappings.
func (s *JSONStore) ListModels() ([]ProxyModel, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ProxyModel, len(s.data.Models))
	copy(out, s.data.Models)
	return out, nil
}

// GetModel returns a model mapping by ID.
func (s *JSONStore) GetModel(id string) (*ProxyModel, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i := range s.data.Models {
		if s.data.Models[i].ID == id {
			model := s.data.Models[i]
			return &model, nil
		}
	}
	return nil, fmt.Errorf("model not found: %s", id)
}

// GetModelByPublicName looks up a model mapping by public model name.
func (s *JSONStore) GetModelByPublicName(name string) (*ProxyModel, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i := range s.data.Models {
		if s.data.Models[i].PublicModel == name {
			model := s.data.Models[i]
			return &model, nil
		}
	}
	return nil, fmt.Errorf("model mapping not found: %s", name)
}

// CreateModel stores a new model mapping.
func (s *JSONStore) CreateModel(model *ProxyModel) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.Models = append(s.data.Models, *model)
	return s.save()
}

// UpdateModel updates an existing model mapping.
func (s *JSONStore) UpdateModel(model *ProxyModel) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Models {
		if s.data.Models[i].ID == model.ID {
			s.data.Models[i] = *model
			return s.save()
		}
	}
	return fmt.Errorf("model not found: %s", model.ID)
}

// RecordCall appends a usage call to the ledger.
func (s *JSONStore) RecordCall(call *UsageCall) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.Calls = append(s.data.Calls, *call)
	return s.save()
}

// GetCalls returns calls for a given date and optionally filtered by api key ID.
func (s *JSONStore) GetCalls(date string, apiKeyID string) ([]UsageCall, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []UsageCall
	datePrefix := date // "2006-01-02"
	for _, c := range s.data.Calls {
		if c.Timestamp.Format("2006-01-02") != datePrefix {
			continue
		}
		if apiKeyID != "" && c.APIKeyID != apiKeyID {
			continue
		}
		out = append(out, c)
	}
	if out == nil {
		out = []UsageCall{}
	}
	return out, nil
}

// GetSummary returns aggregated usage summaries by day or week, optionally filtered by key.
func (s *JSONStore) GetSummary(rangeType, apiKeyID string) ([]UsageSummary, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Group by date-period and optionally by key
	type groupKey struct {
		period string
		keyID  string
	}
	groups := make(map[groupKey]*UsageSummary)

	for _, c := range s.data.Calls {
		if apiKeyID != "" && c.APIKeyID != apiKeyID {
			continue
		}

		var period string
		t := c.Timestamp
		switch rangeType {
		case "week":
			year, week := t.ISOWeek()
			period = fmt.Sprintf("%d-W%02d", year, week)
		default: // "day"
			period = t.Format("2006-01-02")
		}

		gk := groupKey{period: period, keyID: c.APIKeyID}
		s, ok := groups[gk]
		if !ok {
			s = &UsageSummary{Period: period, APIKeyID: c.APIKeyID}
			groups[gk] = s
		}
		s.InputTokens += c.InputTokens
		s.OutputTokens += c.OutputTokens
		s.CacheReadTokens += c.CacheReadTokens
		s.CacheWriteTokens += c.CacheWriteTokens
		s.BillableTokens += c.BillableTokens
		s.Credits += c.Credits
		s.CallCount++
		if c.Status == "error" {
			s.ErrorCount++
		}
	}

	var out []UsageSummary
	for _, s := range groups {
		// Round credits to 6 decimal places
		s.Credits = float64(int(s.Credits*1e6+0.5)) / 1e6
		out = append(out, *s)
	}
	if out == nil {
		out = []UsageSummary{}
	}
	return out, nil
}

// Ensure JSONStore implements Store.
var _ Store = (*JSONStore)(nil)

// NewID generates a simple unique ID for demo/testing purposes.
// In production, use UUIDs.
func NewID(prefix string) string {
	return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
}
