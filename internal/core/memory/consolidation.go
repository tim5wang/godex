package memory

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// DefaultConsolidateAfter is the default candidate-inbox size that triggers an
// LLM consolidation pass (mirrors DEFAULT_CONSOLIDATE_AFTER in the reference
// implementation).
const DefaultConsolidateAfter = 10

// MemoryConsolidationPrompt instructs the model to emit ONLY consolidation
// actions for a numbered list of pending memory candidates. It mirrors
// MEMORY_CONSOLIDATION_PROMPT from temp/qm/src/memory/strategies/consolidation.ts.
const MemoryConsolidationPrompt = `You consolidate an agent's pending memory candidates. The input is a numbered list
of candidate memories (each may carry a type and summary).
Output ONLY actions, one per line, in these exact forms:
UPDATE <n>: <revised candidate text>
DELETE <n>
ADD: <new candidate text>
If nothing needs changing, output exactly: NONE

Rules:
- Prefer UPDATE over DELETE+ADD when a candidate has evolved or two candidates
  should merge (UPDATE one, DELETE the other).
- Keep candidates atomic: one standalone fact per candidate. Split a compound
  candidate with an UPDATE plus ADDs.
- DELETE candidates that are stale, contradicted by newer candidates, exact or
  near duplicates, or trivially derivable from other candidates.
- DELETE pure system mechanics that can be looked up when needed (API
  endpoints/headers, credential/broker plumbing, state-file paths, tool
  invocation details) — but KEEP user-stated conventions about them, and keep
  one existence-level fact for a standing system the user relies on.
- NEVER delete or weaken a candidate the user explicitly asked to remember.
- Do not reword candidates that are already fine. When in doubt, leave a
  candidate alone.`

// ConsolidationAction is one parsed LLM action over the candidate inbox.
type ConsolidationAction struct {
	Kind  string // "update" | "delete" | "add"
	Index int    // 1-based candidate index for update/delete
	Text  string // revised text (update) or new candidate text (add)
}

// parseConsolidationActions parses model output into actions. Empty or "NONE"
// output yields no actions. Malformed lines are skipped.
func parseConsolidationActions(out string) []ConsolidationAction {
	var actions []ConsolidationAction
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.EqualFold(line, "NONE") {
			continue
		}
		upper := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(upper, "UPDATE "):
			rest := strings.TrimSpace(line[len("UPDATE "):])
			idx, text := splitActionIndexText(rest)
			if idx > 0 {
				actions = append(actions, ConsolidationAction{Kind: "update", Index: idx, Text: text})
			}
		case strings.HasPrefix(upper, "DELETE "):
			idx, _ := splitActionIndexText(strings.TrimSpace(line[len("DELETE "):]))
			if idx > 0 {
				actions = append(actions, ConsolidationAction{Kind: "delete", Index: idx})
			}
		case strings.HasPrefix(upper, "ADD:"):
			text := strings.TrimSpace(line[len("ADD:"):])
			if text != "" {
				actions = append(actions, ConsolidationAction{Kind: "add", Text: text})
			}
		}
	}
	return actions
}

// splitActionIndexText splits "<n>: <text>" into the 1-based index and the
// remaining text (empty allowed). Bare "<n>" (no colon) is also accepted so
// DELETE lines like "DELETE 3" parse correctly.
func splitActionIndexText(s string) (int, string) {
	s = strings.TrimSpace(s)
	colon := strings.Index(s, ":")
	if colon < 0 {
		idx, err := strconv.Atoi(s)
		if err != nil || idx <= 0 {
			return 0, ""
		}
		return idx, ""
	}
	idx, err := strconv.Atoi(strings.TrimSpace(s[:colon]))
	if err != nil || idx <= 0 {
		return 0, ""
	}
	return idx, strings.TrimSpace(s[colon+1:])
}

// applyConsolidationActions applies parsed actions to the candidate list and
// returns the updated list plus the count of effective mutations. Invalid
// indexes are ignored. Updated candidates get a fresh fingerprint.
func applyConsolidationActions(candidates []Candidate, actions []ConsolidationAction) ([]Candidate, int) {
	next := append([]Candidate{}, candidates...)
	mutations := 0
	// Deletes are applied in descending index order so earlier indexes stay valid.
	var deletes []int
	for _, action := range actions {
		switch action.Kind {
		case "update":
			if action.Index < 1 || action.Index > len(next) {
				continue
			}
			c := next[action.Index-1]
			revised := action.Text
			c.Content = revised
			if strings.TrimSpace(c.Summary) != "" && strings.TrimSpace(revised) != "" {
				c.Summary = revised
			}
			c.Fingerprint = candidateFingerprint(c.Type, c.Title, c.Summary, c.Content)
			next[action.Index-1] = c
			mutations++
		case "delete":
			if action.Index < 1 || action.Index > len(next) {
				continue
			}
			deletes = append(deletes, action.Index)
			mutations++
		case "add":
			text := strings.TrimSpace(action.Text)
			if text == "" {
				continue
			}
			title := candidateTitleFromText(text)
			next = append(next, newCandidate(title, text, text, TypeProject, "consolidation"))
			mutations++
		}
	}
	for i := len(deletes) - 1; i >= 0; i-- {
		idx := deletes[i]
		if idx < 1 || idx > len(next) {
			continue
		}
		next = append(next[:idx-1], next[idx:]...)
	}
	return next, mutations
}

// candidateTitleFromText derives a short title from candidate text.
func candidateTitleFromText(text string) string {
	title := text
	if i := strings.IndexAny(title, "。.!！\n"); i > 0 {
		title = title[:i]
	}
	if len([]rune(title)) > 60 {
		title = string([]rune(title)[:60])
	}
	title = strings.TrimSpace(title)
	if title == "" {
		title = "Consolidated Fact"
	}
	return title
}

// candidateFingerprint mirrors newCandidate's fingerprint derivation.
func candidateFingerprint(candidateType Type, title, summary, content string) string {
	input := strings.Join([]string{string(candidateType), title, summary, content}, "\n")
	sum := sha1.Sum([]byte(input))
	return hex.EncodeToString(sum[:])
}

// Consolidator runs LLM-driven consolidation over the pending candidate inbox.
// It degrades to capture-only (no-op) when the model call fails or the store
// does not support rewrites, mirroring the reference implementation.
type Consolidator struct {
	manager  *Manager
	oneShot  func(ctx context.Context, prompt, input string) (string, error)
	afterN   int
	degraded bool
	now      func() time.Time
	log      func(msg string)
}

// ConsolidatorOptions configures a Consolidator.
type ConsolidatorOptions struct {
	Manager *Manager
	// OneShot performs a single model call returning plain text. Required for
	// consolidation; nil disables consolidation (capture-only).
	OneShot  func(ctx context.Context, prompt, input string) (string, error)
	AfterN   int
	Now      func() time.Time
	Log      func(msg string)
}

// NewConsolidator builds a Consolidator. AfterN <= 0 disables consolidation.
func NewConsolidator(opts ConsolidatorOptions) *Consolidator {
	if opts.AfterN <= 0 {
		opts.AfterN = DefaultConsolidateAfter
	}
	if opts.Now == nil {
		opts.Now = func() time.Time { return time.Now().UTC() }
	}
	if opts.Log == nil {
		opts.Log = func(string) {}
	}
	return &Consolidator{
		manager: opts.Manager,
		oneShot: opts.OneShot,
		afterN:  opts.AfterN,
		now:     opts.Now,
		log:     opts.Log,
	}
}

// MaybeMaintain runs consolidation when the pending inbox has grown past the
// threshold. It is a no-op when consolidation is unavailable or degraded.
func (c *Consolidator) MaybeMaintain(ctx context.Context) error {
	if c == nil || c.degraded || c.manager == nil || c.oneShot == nil {
		return nil
	}
	candidates, err := c.manager.ListCandidates()
	if err != nil {
		return err
	}
	if len(candidates) < c.afterN {
		return nil
	}
	return c.maintain(ctx, candidates)
}

// Maintain forces a consolidation pass regardless of threshold.
func (c *Consolidator) Maintain(ctx context.Context) error {
	if c == nil || c.degraded || c.manager == nil || c.oneShot == nil {
		return nil
	}
	candidates, err := c.manager.ListCandidates()
	if err != nil {
		return err
	}
	if len(candidates) == 0 {
		return nil
	}
	return c.maintain(ctx, candidates)
}

func (c *Consolidator) maintain(ctx context.Context, candidates []Candidate) error {
	numbered := make([]string, 0, len(candidates))
	for i, candidate := range candidates {
		numbered = append(numbered, fmt.Sprintf("%d. [%s] %s — %s", i+1, candidate.Type, candidate.Title, candidate.Summary))
	}
	out, err := c.oneShot(ctx, MemoryConsolidationPrompt, strings.Join(numbered, "\n"))
	if err != nil {
		c.log(fmt.Sprintf("[memory] consolidation model call failed: %v; capture-only", err))
		c.degraded = true
		return nil
	}
	actions := parseConsolidationActions(out)
	if len(actions) == 0 {
		return nil
	}
	next, mutations := applyConsolidationActions(candidates, actions)
	if mutations == 0 {
		return nil
	}
	if err := c.manager.writeCandidates(next); err != nil {
		c.log(fmt.Sprintf("[memory] consolidation write failed: %v; capture-only", err))
		c.degraded = true
		return nil
	}
	c.log(fmt.Sprintf("[memory] consolidation applied %d mutation(s)", mutations))
	return nil
}
