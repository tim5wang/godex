package memory

import (
	"sort"
	"strings"

	"github.com/tim5wang/godex/internal/core/compress"
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
	Identity []RelevantMemory `json:"identity"`
	Core     []RelevantMemory `json:"core"`
	Relevant []RelevantMemory `json:"relevant"`
}

// BuildContextLayers returns bounded memory sections for prompt injection.
func (m *Manager) BuildContextLayers(query string) (ContextLayers, error) {
	if err := m.ensureStore(); err != nil {
		return ContextLayers{}, err
	}

	entries, err := m.readEntries()
	if err != nil {
		return ContextLayers{}, err
	}
	if len(entries) == 0 {
		return ContextLayers{}, nil
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

	relevant, err := m.selectRelevantMemories(query, maxRelevantContextMemories)
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
		return ContextLayers{}, nil
	}
	return ContextLayers{Identity: identity, Core: core, Relevant: relevant}, nil
}

func (m *Manager) selectRelevantMemories(query string, limit int) ([]RelevantMemory, error) {
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

	relevant, err := m.FindRelevant(query, poolLimit)
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
			Entry:   record.Entry,
			Content: record.Content,
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
			Entry:   record.Entry,
			Content: record.Content,
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
	case TypeProject:
		return 2
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
	unmatched := make([]RelevantMemory, 0, len(memories))
	for idx, mem := range memories {
		affinity := scopeAffinityScore(mem, scopes)
		if affinity > 0 {
			matched = append(matched, scopedRelevant{memory: mem, affinity: affinity, index: idx})
			continue
		}
		unmatched = append(unmatched, mem)
	}
	if len(matched) == 0 {
		if len(memories) > limit {
			return memories[:limit]
		}
		return memories
	}

	sort.SliceStable(matched, func(i, j int) bool {
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
	for _, mem := range unmatched {
		if len(prioritized) >= limit {
			break
		}
		prioritized = append(prioritized, mem)
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
	trimmed.Content = truncateTextToTokenBudget(trimmed.Content, contentBudgetForType(trimmed.Type))

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
	trimmed.Content = truncateTextToTokenBudget(trimmed.Content, contentBudget)
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
