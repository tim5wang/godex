package history

import "strings"

const (
	HistorySearchScopeCurrentSession = "current_session"
	HistorySearchScopeSessionArchive = "session_archive"
	HistorySearchScopeAllArchives    = "all_archives"
)

// HistorySearchPolicy controls whether history recall should be exposed or
// suggested for a given turn. It only governs recall access; it does not
// execute any search itself.
type HistorySearchPolicy struct {
	Enabled bool                     `json:"enabled"`
	Auto    HistorySearchAutoPolicy  `json:"auto"`
	Cues    HistorySearchCuePolicy   `json:"cues"`
	Blocks  HistorySearchBlockPolicy `json:"blocks"`
}

type HistorySearchAutoPolicy struct {
	Enabled                   bool   `json:"enabled"`
	MaxPerTurn                int    `json:"max_per_turn"`
	DefaultScope              string `json:"default_scope,omitempty"`
	AllowArchiveOnClear       bool   `json:"allow_archive_on_clear"`
	AllowArchiveOnCompact     bool   `json:"allow_archive_on_compact"`
	AllowAllArchivesAutomatic bool   `json:"allow_all_archives_automatic"`
	MinScore                  int    `json:"min_score"`
}

type HistorySearchCuePolicy struct {
	Explicit []string `json:"explicit,omitempty"`
	Implicit []string `json:"implicit,omitempty"`
}

type HistorySearchBlockPolicy struct {
	SessionSources []string `json:"session_sources,omitempty"`
}

type HistoryRecallEvaluationInput struct {
	Query                    string
	SessionSource            string
	CurrentContextSufficient bool
	StrongMemoryHit          bool
	AlreadyUsedThisTurn      int
	HistoryWasCleared        bool
	HistoryWasCompacted      bool
}

type HistoryRecallEvaluation struct {
	AllowTool        bool     `json:"allow_tool"`
	Automatic        bool     `json:"automatic"`
	ExplicitRequest  bool     `json:"explicit_request"`
	RecommendedScope string   `json:"recommended_scope,omitempty"`
	Score            int      `json:"score"`
	Reasons          []string `json:"reasons,omitempty"`
}

func DefaultHistorySearchPolicy() HistorySearchPolicy {
	return HistorySearchPolicy{
		Enabled: true,
		Auto: HistorySearchAutoPolicy{
			Enabled:                   true,
			MaxPerTurn:                1,
			DefaultScope:              HistorySearchScopeCurrentSession,
			AllowArchiveOnClear:       true,
			AllowArchiveOnCompact:     true,
			AllowAllArchivesAutomatic: false,
			MinScore:                  3,
		},
		Cues: HistorySearchCuePolicy{
			Explicit: []string{
				"刚才", "之前", "上次", "前面", "聊天记录", "你说过", "我提过",
				"earlier", "previously", "chat history", "you said", "i mentioned",
			},
			Implicit: []string{
				"不是说过", "定过", "还记得", "previous", "remember", "mentioned before",
			},
		},
		Blocks: HistorySearchBlockPolicy{
			SessionSources: []string{"automation", "heartbeat", "cron", "review"},
		},
	}
}

func EvaluateHistoryRecall(policy HistorySearchPolicy, input HistoryRecallEvaluationInput) HistoryRecallEvaluation {
	policy = normalizeHistorySearchPolicy(policy)
	result := HistoryRecallEvaluation{
		RecommendedScope: policy.Auto.DefaultScope,
	}
	if !policy.Enabled {
		result.Reasons = []string{"history_search disabled"}
		return result
	}

	query := normalizeHistoryRecallText(input.Query)
	sessionSource := normalizeHistoryRecallText(input.SessionSource)
	explicitMatches := matchingHistoryCues(query, policy.Cues.Explicit)
	implicitMatches := matchingHistoryCues(query, policy.Cues.Implicit)
	result.ExplicitRequest = len(explicitMatches) > 0

	if result.ExplicitRequest {
		result.Score += policy.Auto.MinScore + 10
		result.Reasons = append(result.Reasons, "explicit history cue")
		result.AllowTool = true
	}

	if len(implicitMatches) > 0 {
		result.Score += 2
		result.Reasons = append(result.Reasons, "implicit history cue")
	}
	if !input.CurrentContextSufficient {
		result.Score++
		result.Reasons = append(result.Reasons, "current context gap")
	}
	if input.HistoryWasCleared && policy.Auto.AllowArchiveOnClear {
		result.Score++
		result.Reasons = append(result.Reasons, "history was cleared")
		result.RecommendedScope = HistorySearchScopeSessionArchive
	}
	if input.HistoryWasCompacted && policy.Auto.AllowArchiveOnCompact {
		result.Score++
		result.Reasons = append(result.Reasons, "history was compacted")
		result.RecommendedScope = HistorySearchScopeSessionArchive
	}
	if input.CurrentContextSufficient {
		result.Score -= 3
		result.Reasons = append(result.Reasons, "current context already sufficient")
	}
	if input.StrongMemoryHit {
		result.Score -= 2
		result.Reasons = append(result.Reasons, "durable memory already matches")
	}

	if !policy.Auto.AllowAllArchivesAutomatic && result.RecommendedScope == HistorySearchScopeAllArchives {
		result.RecommendedScope = HistorySearchScopeCurrentSession
	}

	if result.ExplicitRequest {
		return result
	}
	if !policy.Auto.Enabled {
		result.Reasons = append(result.Reasons, "automatic history recall disabled")
		return result
	}
	if input.AlreadyUsedThisTurn >= policy.Auto.MaxPerTurn {
		result.Reasons = append(result.Reasons, "automatic recall already used this turn")
		return result
	}
	if sourceBlockedForAutomaticHistoryRecall(policy, sessionSource) {
		result.Reasons = append(result.Reasons, "session source blocks automatic recall")
		return result
	}
	if result.Score < policy.Auto.MinScore {
		result.Reasons = append(result.Reasons, "score below minimum")
		return result
	}
	result.AllowTool = true
	result.Automatic = true
	if !policy.Auto.AllowAllArchivesAutomatic && result.RecommendedScope == HistorySearchScopeAllArchives {
		result.RecommendedScope = HistorySearchScopeCurrentSession
	}
	return result
}

func normalizeHistorySearchPolicy(policy HistorySearchPolicy) HistorySearchPolicy {
	defaults := DefaultHistorySearchPolicy()
	if policy.Auto.MaxPerTurn <= 0 {
		policy.Auto.MaxPerTurn = defaults.Auto.MaxPerTurn
	}
	if policy.Auto.MinScore <= 0 {
		policy.Auto.MinScore = defaults.Auto.MinScore
	}
	switch policy.Auto.DefaultScope {
	case HistorySearchScopeCurrentSession, HistorySearchScopeSessionArchive, HistorySearchScopeAllArchives:
	default:
		policy.Auto.DefaultScope = defaults.Auto.DefaultScope
	}
	if len(policy.Cues.Explicit) == 0 {
		policy.Cues.Explicit = append([]string{}, defaults.Cues.Explicit...)
	}
	if len(policy.Cues.Implicit) == 0 {
		policy.Cues.Implicit = append([]string{}, defaults.Cues.Implicit...)
	}
	if len(policy.Blocks.SessionSources) == 0 {
		policy.Blocks.SessionSources = append([]string{}, defaults.Blocks.SessionSources...)
	}
	return policy
}

func sourceBlockedForAutomaticHistoryRecall(policy HistorySearchPolicy, source string) bool {
	source = normalizeHistoryRecallText(source)
	for _, blocked := range policy.Blocks.SessionSources {
		if normalizeHistoryRecallText(blocked) == source {
			return true
		}
	}
	return false
}

func matchingHistoryCues(query string, cues []string) []string {
	if query == "" || len(cues) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(cues))
	matches := make([]string, 0, len(cues))
	for _, cue := range cues {
		cue = normalizeHistoryRecallText(cue)
		if cue == "" {
			continue
		}
		if _, ok := seen[cue]; ok {
			continue
		}
		if strings.Contains(query, cue) {
			seen[cue] = struct{}{}
			matches = append(matches, cue)
		}
	}
	return matches
}

func normalizeHistoryRecallText(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
