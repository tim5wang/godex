package workcontext

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// CreateFact records a new fact. Facts always start as candidates; confirmation
// happens through ReviewFact, which enforces the source + observation time
// requirement and only then supersedes the referenced predecessor.
func (s *Store) CreateFact(ctx context.Context, scope Scope, contextID string, input FactInput) (Fact, error) {
	tenantID, ownerID := scopeArgs(scope)
	contextID = strings.TrimSpace(contextID)
	if err := checkScopeWritable(scope); err != nil {
		return Fact{}, err
	}
	if err := requiredText("title", input.Title); err != nil {
		return Fact{}, err
	}
	if err := requiredText("content", input.Content); err != nil {
		return Fact{}, err
	}
	if err := validateScopeJSON(input.Scope); err != nil {
		return Fact{}, err
	}
	provenance, err := normalizeProvenance(input.Provenance)
	if err != nil {
		return Fact{}, err
	}
	if err := validateFactSource(input.Source, false); err != nil {
		return Fact{}, err
	}
	if err := validateObservedAt(input.ObservedAt); err != nil {
		return Fact{}, err
	}
	if strings.TrimSpace(input.Title) == "" {
		return Fact{}, fmt.Errorf("%w: title is required", ErrInvalid)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Fact{}, err
	}
	defer tx.Rollback()
	if _, err := requireEditableContextTx(ctx, tx, tenantID, ownerID, contextID); err != nil {
		return Fact{}, err
	}
	now := nowRFC3339()
	supersedesID := strings.TrimSpace(input.SupersedesID)
	// Validating the predecessor up front keeps supersession explicit: scope and
	// subject/predicate must match, and cross-owner/context references must not
	// resolve. The predecessor is NOT marked superseded here.
	if supersedesID != "" {
		if _, err := validatedPredecessorTx(ctx, tx, tenantID, contextID, supersedesID, input); err != nil {
			return Fact{}, err
		}
	}
	item := Fact{
		ID:             newID(),
		ContextID:      contextID,
		Title:          strings.TrimSpace(input.Title),
		Content:        strings.TrimSpace(input.Content),
		Kind:           input.Kind,
		Status:         "candidate",
		Scope:          strings.TrimSpace(input.Scope),
		Subject:        strings.TrimSpace(input.Subject),
		Predicate:      strings.TrimSpace(input.Predicate),
		Source:         strings.TrimSpace(input.Source),
		SourceExcerpt:  input.SourceExcerpt,
		SourceRevision: strings.TrimSpace(input.SourceRevision),
		ObservedAt:     strings.TrimSpace(input.ObservedAt),
		Provenance:     provenance,
		SupersedesID:   supersedesID,
		Version:        1,
		CreatedAt:      now,
	}
	if err := insertFactTx(ctx, tx, tenantID, ownerID, item); err != nil {
		return Fact{}, err
	}
	if err := recordEventTx(ctx, tx, tenantID, contextID, "fact_created",
		"新增事实："+item.Title, "store", "fact:"+item.ID, now); err != nil {
		return Fact{}, err
	}
	if err := tx.Commit(); err != nil {
		return Fact{}, err
	}
	return item, nil
}

// ReviewFact applies a review status transition guarded by optimistic
// concurrency. Confirming requires a stored source, a valid observation time,
// and a non-empty scope; only on confirmation is a referenced predecessor
// marked superseded, in the same transaction.
func (s *Store) ReviewFact(ctx context.Context, scope Scope, contextID, factID string, review FactReview) (Fact, error) {
	tenantID, ownerID := scopeArgs(scope)
	contextID = strings.TrimSpace(contextID)
	factID = strings.TrimSpace(factID)
	if err := checkScopeWritable(scope); err != nil {
		return Fact{}, err
	}
	status := strings.TrimSpace(review.Status)
	if !validFactStatuses[status] {
		return Fact{}, fmt.Errorf("%w: unknown fact status %q", ErrInvalid, status)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Fact{}, err
	}
	defer tx.Rollback()
	if _, err := requireEditableContextTx(ctx, tx, tenantID, ownerID, contextID); err != nil {
		return Fact{}, err
	}
	item, err := getFactTx(ctx, tx, tenantID, contextID, factID)
	if err != nil {
		return Fact{}, err
	}
	if item.Version != review.IfVersion {
		return Fact{}, ErrConflict
	}
	if status == "confirmed" {
		if err := validateScopeJSON(item.Scope); err != nil {
			return Fact{}, fmt.Errorf("%w: cannot confirm a fact without a valid scope", ErrInvalid)
		}
		if strings.TrimSpace(item.Source) == "" {
			return Fact{}, fmt.Errorf("%w: cannot confirm a fact without a source", ErrInvalid)
		}
		if err := validateFactSource(item.Source, true); err != nil {
			return Fact{}, fmt.Errorf("%w: confirmed fact source is invalid", ErrInvalid)
		}
		if strings.TrimSpace(item.ObservedAt) == "" {
			return Fact{}, fmt.Errorf("%w: cannot confirm a fact without an observed_at time", ErrInvalid)
		}
		if err := validateObservedAt(item.ObservedAt); err != nil {
			return Fact{}, err
		}
	}
	now := nowRFC3339()
	item.Status = status
	item.Version++
	if status == "confirmed" && item.VerifiedAt == "" {
		item.VerifiedAt = now
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE wc_facts SET status=?, verified_at=?, version=? WHERE tenant_id=? AND id=? AND context_id=? AND version=?`,
		item.Status, item.VerifiedAt, item.Version, tenantID, factID, contextID, review.IfVersion); err != nil {
		return Fact{}, err
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(1) FROM wc_facts WHERE tenant_id=? AND id=?`,
		tenantID, factID).Scan(&count); err != nil {
		return Fact{}, err
	}
	if count == 0 {
		return Fact{}, ErrNotFound
	}
	// Supersession is deferred to confirmation: the earlier confirmed fact is
	// only retired once this replacement is actually confirmed.
	if status == "confirmed" && strings.TrimSpace(item.SupersedesID) != "" {
		if _, err := validatedPredecessorTx(ctx, tx, tenantID, contextID, item.SupersedesID, FactInput{
			Scope: item.Scope, Subject: item.Subject, Predicate: item.Predicate,
		}); err != nil {
			return Fact{}, err
		}
		if err := supersedeFactTx(ctx, tx, tenantID, contextID, item.SupersedesID); err != nil {
			return Fact{}, err
		}
	}
	if err := recordEventTx(ctx, tx, tenantID, contextID, "fact_reviewed",
		fmt.Sprintf("事实审阅：%s -> %s", item.Title, status), "store", fmt.Sprintf("fact_review:%s:%d", factID, item.Version), now); err != nil {
		return Fact{}, err
	}
	if err := tx.Commit(); err != nil {
		return Fact{}, err
	}
	return item, nil
}

// SetFactCandidate stores the durable memory candidate reference for a fact.
// The store never writes durable memory itself; the workflow inbox does.
func (s *Store) SetFactCandidate(ctx context.Context, scope Scope, contextID, factID, candidateID string) (Fact, error) {
	tenantID, ownerID := scopeArgs(scope)
	contextID = strings.TrimSpace(contextID)
	factID = strings.TrimSpace(factID)
	candidateID = strings.TrimSpace(candidateID)
	if err := checkScopeWritable(scope); err != nil {
		return Fact{}, err
	}
	if err := requiredText("candidate id", candidateID); err != nil {
		return Fact{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Fact{}, err
	}
	defer tx.Rollback()
	if _, err := requireEditableContextTx(ctx, tx, tenantID, ownerID, contextID); err != nil {
		return Fact{}, err
	}
	item, err := getFactTx(ctx, tx, tenantID, contextID, factID)
	if err != nil {
		return Fact{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE wc_facts SET memory_candidate_id=?, version=version+1 WHERE tenant_id=? AND id=? AND context_id=?`,
		candidateID, tenantID, factID, contextID); err != nil {
		return Fact{}, err
	}
	item.MemoryCandidateID = candidateID
	item.Version++
	now := nowRFC3339()
	if err := recordEventTx(ctx, tx, tenantID, contextID, "fact_candidate_proposed",
		"提交记忆候选", "store", fmt.Sprintf("fact_candidate:%s:%s", factID, candidateID), now); err != nil {
		return Fact{}, err
	}
	if err := tx.Commit(); err != nil {
		return Fact{}, err
	}
	return item, nil
}

// Recall searches facts for a context. rangeScope selects the search space:
// current (this context), related/direct (contexts reachable in one hop), or
// all (every context owned by the caller). Results are ranked by term overlap.
func (s *Store) Recall(ctx context.Context, scope Scope, contextID, rangeScope, query string) ([]Fact, error) {
	tenantID, ownerID := scopeArgs(scope)
	contextID = strings.TrimSpace(contextID)
	if _, err := s.getContext(ctx, tenantID, ownerID, contextID); err != nil {
		return nil, err
	}
	contextIDs, err := s.recallScopeContexts(ctx, tenantID, ownerID, contextID, rangeScope)
	if err != nil {
		return nil, err
	}
	if len(contextIDs) == 0 {
		return []Fact{}, nil
	}
	terms := recallTerms(query)
	if len(terms) == 0 {
		return s.factsForContexts(ctx, tenantID, contextIDs)
	}
	ordered, err := s.searchFacts(ctx, tenantID, contextIDs, terms)
	if err != nil {
		return nil, err
	}
	return ordered, nil
}

// --- helpers ---

// recallScopeContexts resolves the set of context ids a recall range covers.
func (s *Store) recallScopeContexts(ctx context.Context, tenantID, ownerID, contextID, rangeScope string) ([]string, error) {
	switch strings.TrimSpace(rangeScope) {
	case "", "current", "self":
		return []string{contextID}, nil
	case "all", "global":
		rows, err := s.db.QueryContext(ctx, `
			SELECT id FROM wc_contexts WHERE tenant_id=? AND owner_id=? AND status<>'archived'`,
			tenantID, ownerID)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := []string{}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return nil, err
			}
			out = append(out, id)
		}
		return out, rows.Err()
	case "related", "direct":
		rows, err := s.db.QueryContext(ctx, `
			SELECT CASE WHEN from_id = ? THEN to_id ELSE from_id END AS other
			FROM wc_relations
			WHERE tenant_id=? AND (from_id=? OR to_id=?)
			ORDER BY other`, contextID, tenantID, contextID, contextID)
		if err != nil {
			return nil, err
		}
		seen := map[string]bool{contextID: true}
		out := []string{}
		for rows.Next() {
			var other string
			if err := rows.Scan(&other); err != nil {
				rows.Close()
				return nil, err
			}
			if seen[other] {
				continue
			}
			seen[other] = true
			out = append(out, other)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
		// Keep the anchor context first so callers can prefer local facts.
		ordered := []string{contextID}
		for _, other := range out {
			if other != contextID {
				ordered = append(ordered, other)
			}
		}
		return ordered, nil
	default:
		return nil, fmt.Errorf("%w: unknown recall scope %q", ErrInvalid, rangeScope)
	}
}

// factsForContexts returns all facts in the given contexts when no query is
// provided.
func (s *Store) factsForContexts(ctx context.Context, tenantID string, contextIDs []string) ([]Fact, error) {
	out := []Fact{}
	for _, cid := range contextIDs {
		items, err := s.listFacts(ctx, tenantID, cid)
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
	}
	return out, nil
}

// searchFacts ranks facts in the given contexts by term overlap using FTS plus
// a LIKE fallback (important for CJK text).
func (s *Store) searchFacts(ctx context.Context, tenantID string, contextIDs, terms []string) ([]Fact, error) {
	placeholders := make([]string, 0, len(contextIDs))
	args := make([]any, 0, len(contextIDs)+len(terms))
	args = append(args, tenantID)
	for _, cid := range contextIDs {
		placeholders = append(placeholders, "?")
		args = append(args, cid)
	}
	likeClauses := make([]string, 0, len(terms))
	for _, term := range terms {
		likeClauses = append(likeClauses, `(LOWER(title) LIKE ? ESCAPE '\' OR LOWER(content) LIKE ? ESCAPE '\')`)
		pattern := "%" + escapeLike(term) + "%"
		args = append(args, pattern, pattern)
	}
	query := `
		SELECT id, context_id, title, content, kind, status, scope, subject, predicate,
		       source, source_excerpt, source_revision, observed_at, verified_at,
		       provenance, supersedes_id, memory_candidate_id, version, created_at
		FROM wc_facts
		WHERE tenant_id=? AND context_id IN (` + strings.Join(placeholders, ",") + `)
		  AND (` + strings.Join(likeClauses, " OR ") + `)
		ORDER BY created_at DESC, id`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	scored := []scoredFact{}
	for rows.Next() {
		item, err := scanFactRow(rows)
		if err != nil {
			return nil, err
		}
		scored = append(scored, scoredFact{fact: item, score: factScore(item, terms)})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sortScored(scored)
	out := make([]Fact, 0, len(scored))
	for _, s := range scored {
		out = append(out, s.fact)
	}
	return out, nil
}

// scoredFact pairs a fact with its recall score.
type scoredFact struct {
	fact  Fact
	score int
}

// factScore counts how many distinct terms appear in a fact's searchable text,
// weighting title and subject matches higher.
func factScore(fact Fact, terms []string) int {
	title := strings.ToLower(fact.Title)
	subject := strings.ToLower(fact.Subject)
	content := strings.ToLower(fact.Content)
	score := 0
	for _, term := range terms {
		switch {
		case strings.Contains(title, term):
			score += 3
		case strings.Contains(subject, term):
			score += 2
		case strings.Contains(content, term):
			score++
		}
	}
	return score
}

// sortScored orders facts by descending score then newest first.
func sortScored(items []scoredFact) {
	for i := 1; i < len(items); i++ {
		for j := i; j > 0; j-- {
			left, right := items[j-1], items[j]
			swap := false
			if right.score > left.score {
				swap = true
			} else if right.score == left.score && right.fact.CreatedAt > left.fact.CreatedAt {
				swap = true
			}
			if !swap {
				break
			}
			items[j-1], items[j] = items[j], items[j-1]
		}
	}
}

// insertFactTx inserts a fact row.
func insertFactTx(ctx context.Context, tx *sql.Tx, tenantID, ownerID string, item Fact) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO wc_facts(tenant_id, id, context_id, owner_id, title, content, kind, status, scope,
			subject, predicate, source, source_excerpt, source_revision, observed_at, verified_at,
			provenance, supersedes_id, memory_candidate_id, version, created_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		tenantID, item.ID, item.ContextID, ownerID, item.Title, item.Content, item.Kind, item.Status, item.Scope,
		item.Subject, item.Predicate, item.Source, item.SourceExcerpt, item.SourceRevision, item.ObservedAt, item.VerifiedAt,
		item.Provenance, item.SupersedesID, item.MemoryCandidateID, item.Version, item.CreatedAt)
	return err
}

// validatedPredecessorTx loads the fact a new candidate supersedes and rejects
// cross-owner/context references and scope/subject/predicate mismatches. It is
// the single place the supersession contract is enforced.
func validatedPredecessorTx(ctx context.Context, tx *sql.Tx, tenantID, contextID, predecessorID string, input FactInput) (Fact, error) {
	predecessor, err := getFactTx(ctx, tx, tenantID, contextID, predecessorID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return Fact{}, fmt.Errorf("%w: supersedes target not found in this context", ErrInvalid)
		}
		return Fact{}, err
	}
	if strings.TrimSpace(input.Scope) != strings.TrimSpace(predecessor.Scope) {
		return Fact{}, fmt.Errorf("%w: supersedes scope must match the predecessor", ErrInvalid)
	}
	if strings.TrimSpace(input.Subject) != strings.TrimSpace(predecessor.Subject) {
		return Fact{}, fmt.Errorf("%w: supersedes subject must match the predecessor", ErrInvalid)
	}
	if strings.TrimSpace(input.Predicate) != strings.TrimSpace(predecessor.Predicate) {
		return Fact{}, fmt.Errorf("%w: supersedes predicate must match the predecessor", ErrInvalid)
	}
	if predecessor.Status == "rejected" {
		return Fact{}, fmt.Errorf("%w: cannot supersede a rejected fact", ErrInvalid)
	}
	return predecessor, nil
}

// supersedeFactTx marks a predecessor fact as superseded.
func supersedeFactTx(ctx context.Context, tx *sql.Tx, tenantID, contextID, predecessorID string) error {
	res, err := tx.ExecContext(ctx, `
		UPDATE wc_facts SET status='superseded', version=version+1
		WHERE tenant_id=? AND context_id=? AND id=? AND status<>'rejected'`, tenantID, contextID, predecessorID)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return fmt.Errorf("%w: superseded fact %s not found", ErrNotFound, predecessorID)
	}
	return nil
}

// getFactTx loads one fact scoped by tenant and context.
func getFactTx(ctx context.Context, tx *sql.Tx, tenantID, contextID, factID string) (Fact, error) {
	row := tx.QueryRowContext(ctx, `
		SELECT id, context_id, title, content, kind, status, scope, subject, predicate,
		       source, source_excerpt, source_revision, observed_at, verified_at,
		       provenance, supersedes_id, memory_candidate_id, version, created_at
		FROM wc_facts WHERE tenant_id=? AND context_id=? AND id=?`, tenantID, contextID, factID)
	item, err := scanFactRow(row)
	if err != nil {
		return Fact{}, notFound(err)
	}
	return item, nil
}

// listFacts returns all facts for one context, newest first.
func (s *Store) listFacts(ctx context.Context, tenantID, contextID string) ([]Fact, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, context_id, title, content, kind, status, scope, subject, predicate,
		       source, source_excerpt, source_revision, observed_at, verified_at,
		       provenance, supersedes_id, memory_candidate_id, version, created_at
		FROM wc_facts WHERE tenant_id=? AND context_id=?
		ORDER BY created_at DESC, id`, tenantID, contextID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Fact{}
	for rows.Next() {
		item, err := scanFactRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// scanFactRow scans the standard fact column set.
func scanFactRow(scanner interface{ Scan(...any) error }) (Fact, error) {
	var item Fact
	err := scanner.Scan(&item.ID, &item.ContextID, &item.Title, &item.Content, &item.Kind, &item.Status,
		&item.Scope, &item.Subject, &item.Predicate, &item.Source, &item.SourceExcerpt, &item.SourceRevision,
		&item.ObservedAt, &item.VerifiedAt, &item.Provenance, &item.SupersedesID, &item.MemoryCandidateID,
		&item.Version, &item.CreatedAt)
	if err != nil {
		return Fact{}, err
	}
	return item, nil
}
