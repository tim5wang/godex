package memory

import (
	"context"
	"crypto/sha256"
	"fmt"
	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/core/insights"
	"github.com/tim5wang/godex/internal/core/scope"
	"github.com/tim5wang/godex/internal/domain/events"
	"strings"
	"time"
)

// Owner comes from host authentication or a persisted session binding, never
// from a model's tool arguments. Personal stores include both tenant and owner.
type Owner struct {
	TenantID string `json:"tenant_id,omitempty"`
	UserID   string `json:"user_id"`
}
type ownerContextKey struct{}

func WithOwner(ctx context.Context, owner Owner) context.Context {
	return context.WithValue(ctx, ownerContextKey{}, owner)
}
func OwnerFromContext(ctx context.Context) (Owner, bool) {
	if ctx == nil {
		return Owner{}, false
	}
	v, ok := ctx.Value(ownerContextKey{}).(Owner)
	return v, ok && strings.TrimSpace(v.UserID) != ""
}
func PersonalManager(baseDir string, owner Owner) *Manager {
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(owner.TenantID+"\x00"+owner.UserID)))
	return NewScopedManager(baseDir, scope.Personal(key))
}

// ExtractCandidates returns proposals without choosing a persistence scope.
func ExtractCandidates(messages []protocol.Message) []Candidate {
	return extractCandidatesFromMessages(messages)
}

// SetOriginValidation checks live evidence only inside this owner's store.
func (m *Manager) SetOriginValidation(validate func(Origin) bool, revision func() string) {
	m.originValidator = validate
	m.originRevision = revision
}
func (m *Manager) autoRecallEligible(entry Entry, now time.Time) bool {
	return entryAutoRecallEligible(entry, now) && (entry.Origin == nil || m.originValidator == nil || m.originValidator(*entry.Origin))
}

func ExtractInsightCandidates(report *insights.Report) []Candidate {
	return extractCandidatesFromInsightsReport(report)
}
func ExtractTimelineCandidates(items []events.Event) []Candidate {
	return extractCandidatesFromTimeline(items)
}
