package memory

import (
	"sort"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/core/compress"
	"github.com/tim5wang/godex/internal/platform/retrieval"
)

const (
	maxIdentityContextMemories = 2
	maxCoreContextMemories     = 3
	maxRelevantContextMemories = 5
	scopedRelevantPoolFactor   = 4
	maxIdentityContextTokens   = 120
	maxCoreContextTokens       = 180
	maxRelevantContextTokens   = 320
	maxIdentitySummaryTokens   = 18
	maxCoreSummaryTokens       = 24
	maxRelevantSummaryTokens   = 32
	maxIdentityBodyTokens      = 48
	maxCoreRetrievedBodyTokens = 72
	maxRelevantRetrievedTokens = 140
)

// ContextLayers groups durable memory into identity, stable core, and
// query-time relevant recall.
type ContextLayers struct {
	Diagnostics []*retrieval.Diagnostics `json:"diagnostics,omitempty"`
	Identity    []RelevantMemory         `json:"identity"`
	Core        []RelevantMemory         `json:"core"`
	Relevant    []RelevantMemory         `json:"relevant"`
	Excluded    map[string]int           `json:"excluded,omitempty"`
}

// Current removes entries that expired while a cached recall was being reused.
func (layers ContextLayers) Current(now time.Time) ContextLayers {
	filter := func(entries []RelevantMemory) []RelevantMemory {
		var kept []RelevantMemory
		for _, entry := range entries {
			if entryAutoRecallEligible(entry.Entry, now) {
				kept = append(kept, entry)
			}
		}
		return kept
	}
	layers.Identity = filter(layers.Identity)
	layers.Core = filter(layers.Core)
	layers.Relevant = filter(layers.Relevant)
	return layers
}

// BuildContextLayers returns bounded memory sections for prompt injection.
func (m *Manager) BuildContextLayers(query string) (ContextLayers, error) {
	return m.buildContextLayers(query, true)
}

// PreviewContextLayers returns the bounded memory sections without updating
// LastReferencedAt. Use it for inspectors and previews that do not inject the
// selected memories into an actual model request.
func (m *Manager) PreviewContextLayers(query string) (ContextLayers, error) {
	return m.buildContextLayers(query, false)
}

func (m *Manager) buildContextLayers(query string, trackReferences bool) (ContextLayers, error) {
	if err := m.ensureStore(); err != nil {
		return ContextLayers{}, err
	}

	entries, err := m.readEntries()
	if err != nil {
		return ContextLayers{}, err
	}
	trace := &retrieval.Diagnostics{Query: query, Intent: retrieval.QueryIntent(query), Cache: "not_run"}
	excluded := make(map[string]int)
	now := time.Now()
	for _, entry := range entries {
		switch {
		case normalizeStatus(entry.Status) == StatusArchived:
			excluded["archived"]++
		case entry.SupersededBy != "":
			excluded["superseded"]++
		case !entry.ExpiresAt.IsZero() && !now.Before(entry.ExpiresAt):
			excluded["expired"]++
		case entry.RecallPolicy == "manual":
			excluded["manual_only"]++
		}
	}
	filtered := entries[:0]
	for _, entry := range entries {
		if entry.Origin != nil && m.originValidator != nil && !m.originValidator(*entry.Origin) {
			excluded["source_invalid"]++
			continue
		}
		filtered = append(filtered, entry)
	}
	entries = activeEntries(filtered)
	if len(entries) == 0 {
		trace.Eligibility = recallEligibility(excluded)
		return ContextLayers{Excluded: excluded, Diagnostics: []*retrieval.Diagnostics{trace}}, nil
	}

	identity, err := m.selectIdentityMemories(entries, maxIdentityContextMemories)
	if err != nil {
		return ContextLayers{}, err
	}
	identity = trimMemoriesToTokenBudget(identity, maxIdentityContextTokens)

	core, err := m.selectCoreMemories(entries, maxCoreContextMemories)
	if err != nil {
		return ContextLayers{}, err
	}
	if len(identity) > 0 && len(core) > 0 {
		identityIDs := make(map[string]struct{}, len(identity))
		for _, mem := range identity {
			identityIDs[mem.ID] = struct{}{}
		}
		filtered := core[:0]
		for _, mem := range core {
			if _, ok := identityIDs[mem.ID]; ok {
				continue
			}
			filtered = append(filtered, mem)
		}
		core = filtered
	}
	core = trimMemoriesToTokenBudget(core, maxCoreContextTokens)

	relevant, err := m.selectRelevantMemories(query, maxRelevantContextMemories, trace)
	if err != nil {
		return ContextLayers{}, err
	}

	if (len(identity) > 0 || len(core) > 0) && len(relevant) > 0 {
		coreIDs := make(map[string]struct{}, len(identity)+len(core))
		for _, mem := range identity {
			coreIDs[mem.ID] = struct{}{}
		}
		for _, mem := range core {
			coreIDs[mem.ID] = struct{}{}
		}
		filtered := relevant[:0]
		for _, mem := range relevant {
			if _, ok := coreIDs[mem.ID]; ok {
				continue
			}
			filtered = append(filtered, mem)
		}
		relevant = filtered
	}
	relevant = trimMemoriesToTokenBudget(relevant, maxRelevantContextTokens)

	if len(identity) == 0 && len(core) == 0 && len(relevant) == 0 {
		excluded["not_selected_by_relevance_scope_or_budget"] = len(entries)
		trace.Eligibility = recallEligibility(excluded)
		return ContextLayers{Excluded: excluded, Diagnostics: []*retrieval.Diagnostics{trace}}, nil
	}
	if trackReferences {
		// Best-effort: record which memories actually reached the prompt so the
		// UI can surface stale, never-referenced entries. Failures are best effort.
		m.markReferenced(referencedIDs(identity, core, relevant))
	}
	if count := len(entries) - len(identity) - len(core) - len(relevant); count > 0 {
		excluded["not_selected_by_relevance_scope_or_budget"] = count
	}
	ids, tokens := []string{}, 0
	for _, r := range relevant {
		ids = append(ids, r.ID)
		tokens += len([]rune(r.Title+r.Content)) / 2
	}
	retrieval.FinalizeSelection(trace, ids, tokens)
	trace.Eligibility = recallEligibility(excluded)
	return ContextLayers{Identity: identity, Core: core, Relevant: relevant, Excluded: excluded, Diagnostics: []*retrieval.Diagnostics{trace}}, nil
}

func recallEligibility(excluded map[string]int) map[string]int {
	out := map[string]int{}
	for reason, count := range excluded {
		if reason != "not_selected_by_relevance_scope_or_budget" {
			out[reason] = count
		}
	}
	return out
}

// referencedIDs collects the IDs of memories that were selected into any
// context layer, for LastReferencedAt tracking.
func referencedIDs(layers ...[]RelevantMemory) map[string]struct{} {
	ids := make(map[string]struct{})
	for _, layer := range layers {
		for _, mem := range layer {
			ids[mem.ID] = struct{}{}
		}
	}
	return ids
}

func (m *Manager) selectRelevantMemories(query string, limit int, diagnostics ...*retrieval.Diagnostics) ([]RelevantMemory, error) {
	if strings.TrimSpace(query) == "" || limit <= 0 {
		return nil, nil
	}

	scopes := inferRecallScopes(query)
	poolLimit := limit
	if len(scopes) > 0 {
		poolLimit = limit * scopedRelevantPoolFactor
		if poolLimit < limit+3 {
			poolLimit = limit + 3
		}
	}

	var trace *retrieval.Diagnostics
	if len(diagnostics) > 0 {
		trace = diagnostics[0]
	}
	relevant, err := m.findRelevant(query, poolLimit, trace)
	if err != nil {
		return nil, err
	}
	if len(relevant) == 0 {
		return nil, nil
	}
	if len(scopes) == 0 {
		if len(relevant) > limit {
			return relevant[:limit], nil
		}
		return relevant, nil
	}
	return prioritizeRelevantByScope(relevant, scopes, limit), nil
}

type rankedCoreMemory struct {
	entry    Entry
	priority int
}

func (m *Manager) selectCoreMemories(entries []Entry, limit int) ([]RelevantMemory, error) {
	if limit <= 0 {
		return nil, nil
	}

	ranked := make([]rankedCoreMemory, 0, len(entries))
	for _, entry := range entries {
		priority := corePriority(entry)
		if priority < 0 {
			continue
		}
		ranked = append(ranked, rankedCoreMemory{entry: entry, priority: priority})
	}
	if len(ranked) == 0 {
		return nil, nil
	}

	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].priority == ranked[j].priority {
			if ranked[i].entry.UpdatedAt.Equal(ranked[j].entry.UpdatedAt) {
				return ranked[i].entry.Title < ranked[j].entry.Title
			}
			return ranked[i].entry.UpdatedAt.After(ranked[j].entry.UpdatedAt)
		}
		return ranked[i].priority < ranked[j].priority
	})
	if len(ranked) > limit {
		ranked = ranked[:limit]
	}

	core := make([]RelevantMemory, 0, len(ranked))
	for _, item := range ranked {
		record, err := m.readStoredMemory(item.entry)
		if err != nil {
			return nil, err
		}
		core = append(core, RelevantMemory{
			Entry:          record.Entry,
			Content:        record.Content,
			SourceDocument: sourceDocumentFromContent(record.Content),
		})
	}
	return core, nil
}

func (m *Manager) selectIdentityMemories(entries []Entry, limit int) ([]RelevantMemory, error) {
	if limit <= 0 {
		return nil, nil
	}

	ranked := make([]rankedCoreMemory, 0, len(entries))
	for _, entry := range entries {
		if entry.Type != TypeIdentity {
			continue
		}
		ranked = append(ranked, rankedCoreMemory{entry: entry})
	}
	if len(ranked) == 0 {
		return nil, nil
	}

	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].entry.UpdatedAt.Equal(ranked[j].entry.UpdatedAt) {
			return ranked[i].entry.Title < ranked[j].entry.Title
		}
		return ranked[i].entry.UpdatedAt.After(ranked[j].entry.UpdatedAt)
	})
	if len(ranked) > limit {
		ranked = ranked[:limit]
	}

	identity := make([]RelevantMemory, 0, len(ranked))
	for _, item := range ranked {
		record, err := m.readStoredMemory(item.entry)
		if err != nil {
			return nil, err
		}
		identity = append(identity, RelevantMemory{
			Entry:          record.Entry,
			Content:        record.Content,
			SourceDocument: sourceDocumentFromContent(record.Content),
		})
	}
	return identity, nil
}

func corePriority(entry Entry) int {
	if entry.Type == TypeIdentity {
		return -1
	}
	if hasTag(entry.Tags, "core") {
		return 0
	}
	switch entry.Type {
	case TypeUser:
		return 1
	default:
		return -1
	}
}

var recallScopeKeywords = map[string][]string{
	"browser": {"browser", "网页", "页面", "截图", "表单", "导航", "download", "upload"},
	"weixin":  {"weixin", "wechat", "微信"},
	"feishu":  {"feishu", "lark", "飞书"},
	"memory":  {"memory", "记忆", "candidate", "suppression", "core", "history_search"},
	"runtime": {"runtime", "backend", "session", "timeline", "approve", "approval", "权限", "会话"},
	"config":  {"config", "settings", "yaml", "env", "token", "配置"},
}

func inferRecallScopes(query string) []string {
	queryLower := strings.ToLower(strings.TrimSpace(query))
	if queryLower == "" {
		return nil
	}

	scopes := make([]string, 0, len(recallScopeKeywords))
	for scope, keywords := range recallScopeKeywords {
		for _, keyword := range keywords {
			if keyword != "" && strings.Contains(queryLower, strings.ToLower(keyword)) {
				scopes = append(scopes, scope)
				break
			}
		}
	}
	sort.Strings(scopes)
	return scopes
}

func prioritizeRelevantByScope(memories []RelevantMemory, scopes []string, limit int) []RelevantMemory {
	if len(memories) == 0 || limit <= 0 {
		return nil
	}

	type scopedRelevant struct {
		memory   RelevantMemory
		affinity int
		index    int
	}

	matched := make([]scopedRelevant, 0, len(memories))
	for idx, mem := range memories {
		affinity := scopeAffinityScore(mem, scopes)
		if affinity > 0 {
			matched = append(matched, scopedRelevant{memory: mem, affinity: affinity, index: idx})
			continue
		}
	}
	if len(matched) == 0 {
		return nil
	}

	sort.SliceStable(matched, func(i, j int) bool {
		if matched[i].memory.Score != matched[j].memory.Score {
			return matched[i].memory.Score > matched[j].memory.Score
		}
		if matched[i].affinity == matched[j].affinity {
			return matched[i].index < matched[j].index
		}
		return matched[i].affinity > matched[j].affinity
	})

	prioritized := make([]RelevantMemory, 0, min(limit, len(memories)))
	for _, item := range matched {
		if len(prioritized) >= limit {
			break
		}
		prioritized = append(prioritized, item.memory)
	}
	return prioritized
}

func scopeAffinityScore(mem RelevantMemory, scopes []string) int {
	if len(scopes) == 0 {
		return 0
	}

	affinity := 0
	titleLower := strings.ToLower(mem.Title)
	summaryLower := strings.ToLower(mem.Summary)
	sourceLower := strings.ToLower(mem.Source)
	fileLower := strings.ToLower(mem.File)
	contentLower := strings.ToLower(mem.Content)
	tagSet := make(map[string]struct{}, len(mem.Tags))
	for _, tag := range mem.Tags {
		tagSet[strings.ToLower(strings.TrimSpace(tag))] = struct{}{}
	}

	for _, scope := range scopes {
		keywords := recallScopeKeywords[scope]
		matchedScope := false
		for _, keyword := range keywords {
			needle := strings.ToLower(strings.TrimSpace(keyword))
			if needle == "" {
				continue
			}
			if _, ok := tagSet[needle]; ok {
				affinity += 5
				matchedScope = true
				break
			}
		}
		if matchedScope {
			continue
		}
		for _, keyword := range keywords {
			needle := strings.ToLower(strings.TrimSpace(keyword))
			if needle == "" {
				continue
			}
			switch {
			case strings.Contains(sourceLower, needle), strings.Contains(fileLower, needle):
				affinity += 4
				matchedScope = true
			case strings.Contains(titleLower, needle), strings.Contains(summaryLower, needle):
				affinity += 3
				matchedScope = true
			case strings.Contains(contentLower, needle):
				affinity += 1
				matchedScope = true
			}
			if matchedScope {
				break
			}
		}
	}
	return affinity
}

func hasTag(tags []string, target string) bool {
	target = strings.TrimSpace(strings.ToLower(target))
	if target == "" {
		return false
	}
	for _, tag := range tags {
		if strings.TrimSpace(strings.ToLower(tag)) == target {
			return true
		}
	}
	return false
}

// activeEntries filters out archived memories so they never reach prompt
// injection or default recall.
func activeEntries(entries []Entry) []Entry {
	if len(entries) == 0 {
		return nil
	}
	kept := entries[:0]
	for _, entry := range entries {
		if !entryAutoRecallEligible(entry, time.Now()) {
			continue
		}
		kept = append(kept, entry)
	}
	return kept
}

func trimMemoriesToTokenBudget(memories []RelevantMemory, budget int) []RelevantMemory {
	if len(memories) == 0 || budget <= 0 {
		return nil
	}

	trimmed := make([]RelevantMemory, 0, len(memories))
	used := 0
	for i, mem := range memories {
		remaining := budget - used
		if remaining <= 0 {
			break
		}
		fitted, ok := fitMemoryToBudget(mem, remaining, i == 0)
		if !ok {
			continue
		}
		used += estimateRelevantMemoryTokens(fitted)
		trimmed = append(trimmed, fitted)
	}
	return trimmed
}

func fitMemoryToBudget(mem RelevantMemory, remaining int, force bool) (RelevantMemory, bool) {
	trimmed := mem
	trimmed.Summary = truncateTextToTokenBudget(trimmed.Summary, summaryBudgetForType(trimmed.Type))
	trimmed.Content = truncateTextTailToTokenBudget(trimmed.Content, contentBudgetForType(trimmed.Type))

	if estimateRelevantMemoryTokens(trimmed) <= remaining {
		return trimmed, true
	}

	base := trimmed
	base.Content = ""
	if estimateRelevantMemoryTokens(base) > remaining {
		if !force {
			return RelevantMemory{}, false
		}
		base.Summary = truncateTextToTokenBudget(base.Summary, remaining/2)
		if estimateRelevantMemoryTokens(base) > remaining {
			return RelevantMemory{}, false
		}
		return base, true
	}

	contentBudget := remaining - estimateRelevantMemoryTokens(base)
	if contentBudget <= 0 {
		return base, true
	}
	trimmed.Content = truncateTextTailToTokenBudget(trimmed.Content, contentBudget)
	if estimateRelevantMemoryTokens(trimmed) > remaining {
		return base, true
	}
	return trimmed, true
}

func summaryBudgetForType(memoryType Type) int {
	switch memoryType {
	case TypeIdentity:
		return maxIdentitySummaryTokens
	case TypeUser, TypeProject:
		return maxCoreSummaryTokens
	default:
		return maxRelevantSummaryTokens
	}
}

func contentBudgetForType(memoryType Type) int {
	switch memoryType {
	case TypeIdentity:
		return maxIdentityBodyTokens
	case TypeUser, TypeProject:
		return maxCoreRetrievedBodyTokens
	default:
		return maxRelevantRetrievedTokens
	}
}

func estimateRelevantMemoryTokens(mem RelevantMemory) int {
	total := 0
	total += compress.CountTokens(mem.Title)
	total += compress.CountTokens(string(mem.Type))
	total += compress.CountTokens(mem.File)
	total += compress.CountTokens(mem.Summary)
	total += compress.CountTokens(mem.Content)
	total += compress.CountTokens(mem.SourceDocument)
	return total
}

func truncateTextToTokenBudget(text string, maxTokens int) string {
	text = strings.TrimSpace(text)
	if text == "" || maxTokens <= 0 {
		return ""
	}
	if compress.CountTokens(text) <= maxTokens {
		return text
	}

	runes := []rune(text)
	low, high := 0, len(runes)
	best := ""
	for low <= high {
		mid := (low + high) / 2
		candidate := strings.TrimSpace(string(runes[:mid]))
		if candidate == "" {
			low = mid + 1
			continue
		}
		withEllipsis := candidate + "..."
		if compress.CountTokens(withEllipsis) <= maxTokens {
			best = withEllipsis
			low = mid + 1
			continue
		}
		high = mid - 1
	}
	if best != "" {
		return best
	}
	if compress.CountTokens("...") <= maxTokens {
		return "..."
	}
	return ""
}

// MergeContextLayers shares only already-authorized stores. Scope selection
// happens before ranking; budgets cover the combined result, not each store.
func MergeContextLayers(primary, shared ContextLayers) ContextLayers {
	merge := func(a, b []RelevantMemory, limit int) []RelevantMemory {
		seen := map[string]bool{}
		var out []RelevantMemory
		for _, v := range append(append([]RelevantMemory{}, a...), b...) {
			if !seen[v.ID] {
				seen[v.ID] = true
				out = append(out, v)
			}
		}
		return trimMemoriesToTokenBudget(out, limit)
	}
	out := ContextLayers{Identity: merge(primary.Identity, shared.Identity, maxIdentityContextTokens), Core: merge(primary.Core, shared.Core, maxCoreContextTokens), Relevant: merge(primary.Relevant, shared.Relevant, maxRelevantContextTokens), Excluded: map[string]int{}}
	out.Diagnostics = append(append([]*retrieval.Diagnostics{}, primary.Diagnostics...), shared.Diagnostics...)
	for _, m := range []map[string]int{primary.Excluded, shared.Excluded} {
		for k, v := range m {
			out.Excluded[k] += v
		}
	}
	for _, d := range out.Diagnostics {
		ids, tokens := []string{}, 0
		for _, r := range out.Relevant {
			for _, id := range d.SelectedIDs {
				if id == r.ID {
					ids = append(ids, r.ID)
					tokens += len([]rune(r.Title+r.Content)) / 2
					break
				}
			}
		}
		retrieval.FinalizeSelection(d, ids, tokens)
	}
	return out
}
