package backend

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/core/insights"
	"github.com/tim5wang/godex/internal/core/memory"
	"github.com/tim5wang/godex/internal/core/scope"
	"github.com/tim5wang/godex/internal/core/workcontext"
	"github.com/tim5wang/godex/internal/domain/events"
	"github.com/tim5wang/godex/internal/platform/logger"
	"github.com/tim5wang/godex/internal/platform/retrieval"
	"github.com/tim5wang/godex/internal/platform/textutil"
)

type workMemoryBinding struct {
	Scope     workcontext.Scope
	ContextID string
	Err       error
}

func sessionMemoryOwnerMatches(ctx context.Context, session *sessionState) bool {
	owner, authenticated := memory.OwnerFromContext(ctx)
	if !authenticated {
		return true
	} // Host-only compatibility path.
	if session == nil {
		return false
	}
	session.mu.RLock()
	defer session.mu.RUnlock()
	return owner == session.memoryOwner
}

// Requests that target a resident session must use its persisted owner too;
// checking OpenSession alone would leave direct submit/Inspector paths open.
func (s *Service) requireSessionFor(ctx context.Context, id string) (*sessionState, error) {
	session, err := s.requireSession(id)
	if err != nil {
		return nil, err
	}
	if !sessionMemoryOwnerMatches(ctx, session) {
		return nil, newSessionNotFoundError(id)
	}
	return session, nil
}

type desktopMemory struct {
	store     *workcontext.Store
	binding   workMemoryBinding
	sessionID string
	personal  *memory.Manager
}

func (s *Service) personalMemory(owner memory.Owner) *memory.Manager {
	manager := memory.PersonalManager(s.cfg.MemoryDir, owner)
	if err := memory.ImportLegacyPreferences(s.cfg.MemoryDir, owner, manager); err != nil {
		logger.Warnf("legacy preference migration: %v", err)
	}
	store, err := s.WorkContexts()
	if err != nil {
		manager.SetOriginValidation(func(memory.Origin) bool { return false }, func() string { return "unavailable" })
		return manager
	}
	boundary := workcontext.Scope{TenantID: owner.TenantID, OwnerID: owner.UserID}
	manager.SetOriginValidation(func(origin memory.Origin) bool {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		f, e := store.Fact(ctx, boundary, origin.ContextID, origin.FactID)
		return e == nil && f.Current(time.Now()) && f.Version == origin.FactVersion && f.Scope == origin.Applicability
	}, func() string {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		return store.MemoryRevision(ctx, boundary)
	})
	return manager
}

// configureSessionMemory routes every default write into a session or the
// current desktop; accepted personal knowledge is a separate read-only overlay.
func (s *Service) configureSessionMemory(session *sessionState, store *workcontext.Store, binding workMemoryBinding) {
	owner := session.memoryOwner
	if owner.UserID == "" {
		owner = memory.Owner{UserID: "local-operator"}
	}
	if binding.ContextID != "" && binding.Err == nil {
		owner = memory.Owner{TenantID: binding.Scope.TenantID, UserID: binding.Scope.OwnerID}
	}
	personal := s.personalMemory(owner)
	local := memory.NewScopedManager(s.cfg.MemoryDir, scope.Session(session.id))
	var strategy memory.Strategy // Agent builds the configured session stack.
	key := "session:" + session.id + ":" + owner.TenantID + ":" + owner.UserID + ":" + s.cfg.MemoryDir + ":" + s.cfg.Memory.Strategy
	if binding.ContextID != "" && binding.Err == nil {
		writer := &desktopMemory{store: store, binding: binding, sessionID: session.id, personal: personal}
		key = "desktop:" + workcontext.ScopeKey(binding.Scope) + ":" + binding.ContextID + ":" + s.cfg.MemoryDir + ":" + s.cfg.Memory.Strategy
		strategy = desktopMemoryStrategy{writer: writer, kind: memory.ParseStrategyKind(s.cfg.Memory.Strategy)}
		session.agent.SetMemoryBoundary(key, personal, nil, writer, strategy)
	} else {
		session.agent.SetMemoryBoundary(key, local, personal, local, strategy)
	}
}

func desktopRecord(f workcontext.Fact) memory.StoredMemory {
	status := memory.StatusActive
	if f.Status == "rejected" || f.Status == "superseded" || f.Status == "archived" {
		status = memory.StatusArchived
	}
	return memory.StoredMemory{Retrieval: f.Recall, Entry: memory.Entry{ID: f.ID, File: "fact:" + f.ID, Title: f.Title, Summary: textutil.TruncateRunes(f.Content, 180), Type: memory.TypeWorkFact, Source: f.Source, Status: status, Tags: []string{f.Status, f.Kind}, RecallPolicy: "manual", Origin: &memory.Origin{ContextID: f.ContextID, FactID: f.ID, FactVersion: f.Version, Applicability: f.Scope, SourceRevision: f.SourceRevision, ValidUntil: f.ValidUntil, ObservedAt: f.ObservedAt, VerifiedAt: f.VerifiedAt}}, Content: fmt.Sprintf("Verification status: %s\nApplicability: %s\nObserved: %s\nSource: %s\n\n%s", f.Status, f.Scope, f.ObservedAt, f.Source, f.Content)}
}

func (w *desktopMemory) localFacts() ([]workcontext.Fact, error) {
	return w.store.Facts(context.Background(), w.binding.Scope, w.binding.ContextID)
}
func (w *desktopMemory) Get(key string) (*memory.StoredMemory, error) {
	key = strings.TrimPrefix(key, "fact:")
	facts, err := w.localFacts()
	if err != nil {
		return nil, err
	}
	for _, f := range facts {
		if f.ID == key || strings.EqualFold(f.Title, key) {
			r := desktopRecord(f)
			return &r, nil
		}
	}
	return w.personal.Get(key)
}
func (w *desktopMemory) List() ([]memory.Entry, error) {
	records, err := w.Search(memory.SearchOptions{})
	if err != nil {
		return nil, err
	}
	out := make([]memory.Entry, 0, len(records))
	for _, r := range records {
		out = append(out, r.Entry)
	}
	return out, nil
}
func (w *desktopMemory) Search(options memory.SearchOptions) ([]memory.StoredMemory, error) {
	var facts []workcontext.Fact
	var err error
	if options.Automatic {
		facts, err = w.store.RecallCurrent(context.Background(), w.binding.Scope, w.binding.ContextID, options.Query)
	} else {
		facts, err = w.store.Recall(context.Background(), w.binding.Scope, w.binding.ContextID, "current", options.Query)
	}
	if options.Status == memory.StatusAll || options.Status == memory.StatusArchived {
		facts, err = w.localFacts()
	}
	if err != nil {
		return nil, err
	}
	out := []memory.StoredMemory{}
	for _, f := range facts {
		if f.Recall == nil && !desktopFactMatchesQuery(f, options.Query) {
			continue
		}
		r := desktopRecord(f)
		if options.Type != "" && options.Type != r.Type {
			continue
		}
		if options.Status != "" && options.Status != memory.StatusAll && options.Status != r.Status {
			continue
		}
		if options.Source != "" && !strings.EqualFold(options.Source, r.Source) {
			continue
		}
		if options.Tag != "" && !strings.EqualFold(options.Tag, f.Status) && !strings.EqualFold(options.Tag, f.Kind) {
			continue
		}
		if options.Automatic && !f.Current(time.Now()) {
			continue
		}
		out = append(out, r)
	}
	shared, err := w.personal.Search(options)
	if err != nil {
		return nil, err
	}
	out = append(out, shared...)
	if options.Limit > 0 && len(out) > options.Limit {
		out = out[:options.Limit]
	}
	return out, nil
}

func desktopFactMatchesQuery(f workcontext.Fact, query string) bool {
	if strings.TrimSpace(query) == "" {
		return true
	}
	text := strings.ToLower(f.Title + " " + f.Content)
	for _, term := range retrieval.Terms(query) {
		if strings.Contains(text, term) {
			return true
		}
	}
	return false
}
func (w *desktopMemory) Remember(input memory.SaveInput) (*memory.Entry, error) {
	// Remember in a bound conversation records a proposal, never global truth.
	source := strings.TrimSpace(input.Source)
	if source == "" {
		source = "session://" + w.sessionID
	}
	expiry := ""
	if !input.ExpiresAt.IsZero() {
		expiry = input.ExpiresAt.Format(time.RFC3339Nano)
	}
	f, _, err := w.store.CreateObservation(context.Background(), w.binding.Scope, w.binding.ContextID, workcontext.FactInput{ValidUntil: expiry, Title: input.Title, Content: input.Content, Kind: desktopMemoryKind(input.Type), Scope: `{"work_context":"` + w.binding.ContextID + `"}`, Source: source, Provenance: "derived"})
	if err != nil {
		return nil, err
	}
	r := desktopRecord(f)
	return &r.Entry, nil
}
func (w *desktopMemory) Update(input memory.UpdateInput) (*memory.Entry, error) {
	key := strings.TrimPrefix(input.Match.File, "fact:")
	if key == "" {
		key = input.Match.Title
	}
	facts, err := w.localFacts()
	if err != nil {
		return nil, err
	}
	for _, old := range facts {
		if old.ID != key && !strings.EqualFold(old.Title, key) {
			continue
		}
		title, content := input.Title, input.Content
		if strings.TrimSpace(title) == "" {
			title = old.Title
		}
		if strings.TrimSpace(content) == "" {
			content = old.Content
		}
		source := input.Source
		if source == "" {
			source = old.Source
		}
		expiry := old.ValidUntil
		if input.ExpiresAt != nil {
			expiry = ""
			if !input.ExpiresAt.IsZero() {
				expiry = input.ExpiresAt.Format(time.RFC3339Nano)
			}
		}
		f, e := w.store.CreateFact(context.Background(), w.binding.Scope, w.binding.ContextID, workcontext.FactInput{ValidUntil: expiry, Title: title, Content: content, Kind: old.Kind, Scope: old.Scope, Subject: old.Subject, Predicate: old.Predicate, Source: source, Provenance: "derived", SupersedesID: old.ID})
		if e != nil {
			return nil, e
		}
		r := desktopRecord(f)
		return &r.Entry, nil
	}
	return nil, fmt.Errorf("desktop memory not found; shared knowledge is read-only in this conversation")
}
func (w *desktopMemory) Forget(memory.ForgetInput) (*memory.Entry, error) {
	return nil, fmt.Errorf("desktop evidence is retained; retire it through desktop review instead of deleting it")
}
func (w *desktopMemory) ListCandidates() ([]memory.Candidate, error) {
	facts, err := w.localFacts()
	if err != nil {
		return nil, err
	}
	out := []memory.Candidate{}
	for _, f := range facts {
		if f.Status == "candidate" {
			out = append(out, memory.Candidate{Fingerprint: f.ID, Title: f.Title, Summary: textutil.TruncateRunes(f.Content, 180), Content: f.Content, Type: memory.TypeWorkFact, Source: f.Source})
		}
	}
	return out, nil
}
func (w *desktopMemory) AcceptCandidate(string) (*memory.Entry, error) {
	return nil, fmt.Errorf("confirm desktop facts in the evidence review UI; cross-desktop promotion is a separate explicit action")
}
func (w *desktopMemory) DismissCandidate(id string) (*memory.Candidate, error) {
	fact, err := w.store.Fact(context.Background(), w.binding.Scope, w.binding.ContextID, id)
	if err != nil {
		return nil, err
	}
	if fact.Status != "candidate" {
		return nil, fmt.Errorf("only an unconfirmed proposal may be dismissed here")
	}
	_, err = w.store.ReviewFact(context.Background(), w.binding.Scope, w.binding.ContextID, id, workcontext.FactReview{Status: "rejected", IfVersion: fact.Version})
	if err != nil {
		return nil, err
	}
	return &memory.Candidate{Fingerprint: fact.ID, Title: fact.Title, Content: fact.Content, Type: memory.TypeWorkFact}, nil
}

type desktopMemoryStrategy struct {
	writer *desktopMemory
	kind   memory.StrategyKind
}

func (s desktopMemoryStrategy) Kind() memory.StrategyKind      { return s.kind }
func (s desktopMemoryStrategy) Maintain(context.Context) error { return nil }
func (s desktopMemoryStrategy) Capture(messages []protocol.Message) ([]memory.Candidate, error) {
	if s.kind == memory.StrategyAgentOnly {
		return nil, nil
	}
	candidates := memory.ExtractCandidates(messages)
	for _, c := range candidates {
		if _, err := s.writer.Remember(memory.SaveInput{Title: c.Title, Summary: c.Summary, Content: c.Content, Type: c.Type, Source: "session://" + s.writer.sessionID}); err != nil {
			return nil, err
		}
	}
	return candidates, nil
}

func (s desktopMemoryStrategy) captureProposals(candidates []memory.Candidate) error {
	if s.kind == memory.StrategyAgentOnly {
		return nil
	}
	for _, c := range candidates {
		if _, err := s.writer.Remember(memory.SaveInput{Title: c.Title, Summary: c.Summary, Content: c.Content, Type: c.Type, Source: "session://" + s.writer.sessionID}); err != nil {
			return err
		}
	}
	return nil
}
func (s desktopMemoryStrategy) CaptureInsightCandidates(report *insights.Report) error {
	return s.captureProposals(memory.ExtractInsightCandidates(report))
}
func (s desktopMemoryStrategy) CaptureTimelineCandidates(items []events.Event) error {
	return s.captureProposals(memory.ExtractTimelineCandidates(items))
}

func (w *desktopMemory) MemoryDescription() string {
	return "Read current-desktop work memory and reviewed personal knowledge. remember/update create unverified proposals ONLY in this desktop; updates propose explicit replacement and preserve the old evidence. Source and applicability are retained. Facts are confirmed in desktop review; cross-desktop knowledge requires explicit promotion. Shared personal knowledge is read-only here."
}
func desktopMemoryKind(kind memory.Type) string {
	switch kind {
	case memory.TypeWorkMethod, memory.TypeWorkflow:
		return "method"
	case memory.TypeWarning:
		return "warning"
	case memory.TypeUser:
		return "preference"
	default:
		return "fact"
	}
}
