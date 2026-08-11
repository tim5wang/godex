package security

import (
	"context"
	"sort"
)

// ScreenHook identifies the pipeline point where a payload is screened.
type ScreenHook string

const (
	// ScreenHookUserInput marks inbound human/user messages.
	ScreenHookUserInput ScreenHook = "user_input"
	// ScreenHookToolResponse marks tool results flowing back to the model.
	ScreenHookToolResponse ScreenHook = "tool_response"
)

// ScreenDecision is the normalized classifier decision.
type ScreenDecision string

const (
	// ScreenDecisionAuto means the payload did not cross the malicious
	// threshold; treat as ordinary untrusted data.
	ScreenDecisionAuto ScreenDecision = "auto"
	// ScreenDecisionStrict means the payload crossed the malicious threshold
	// and warrants elevated scrutiny.
	ScreenDecisionStrict ScreenDecision = "strict"
)

// ScreenVerdict is the normalized outcome of one or more classifiers.
type ScreenVerdict struct {
	Decision  ScreenDecision `json:"decision"`
	Reason    string         `json:"reason,omitempty"`
	Score     float64        `json:"score,omitempty"`
	Threshold float64        `json:"threshold,omitempty"`
	Outcome   string         `json:"outcome,omitempty"`
	// Unscreened marks a payload that could not be classified (screener
	// unavailable, timeout, invalid response). Callers must treat it as
	// untrusted data, never as instructions.
	Unscreened bool `json:"unscreened,omitempty"`
}

// Malicious reports whether the verdict crossed the malicious threshold.
func (v ScreenVerdict) Malicious() bool {
	return v.Decision == ScreenDecisionStrict && !v.Unscreened
}

const (
	// UnscreenedReason is the machine reason attached to unscreened verdicts.
	UnscreenedReason = "screen_unavailable"
	// UnscreenedPrefix marks content that was not screened. It mirrors the QM
	// reference's "[NOT security-screened ...]" degradation notice.
	UnscreenedPrefix = "[NOT security-screened"

	// MaxScreenChars caps the total payload screened per call.
	MaxScreenChars = 16000
	// ScreenChunkChars is the per-chunk size for long payloads.
	ScreenChunkChars = 1600
	// ScreenChunkOverlap keeps context continuity across chunk boundaries.
	ScreenChunkOverlap = 256
)

// UnscreenedVerdict builds the degradation verdict used when the screener is
// unavailable. Kind is a short noun like "user message" or "tool result".
func UnscreenedVerdict(kind string) ScreenVerdict {
	return ScreenVerdict{
		Decision:   ScreenDecisionAuto,
		Reason:     UnscreenedReason,
		Unscreened: true,
		Outcome:    "unscreened",
	}
}

// ScreenChunks splits a payload into overlapping chunks, mirroring the QM
// reference: ≤ ScreenChunkChars per chunk, ScreenChunkOverlap overlap, with
// surrogate-pair-safe boundaries. A short payload yields a single chunk.
func ScreenChunks(text string) []string {
	if text == "" {
		return nil
	}
	if len(text) <= ScreenChunkChars {
		return []string{text}
	}
	runes := []rune(text)
	if len(runes) <= ScreenChunkChars {
		return []string{text}
	}
	var chunks []string
	start := 0
	for start < len(runes) {
		end := start + ScreenChunkChars
		if end > len(runes) {
			end = len(runes)
		}
		chunks = append(chunks, string(runes[start:end]))
		if end == len(runes) {
			break
		}
		next := end - ScreenChunkOverlap
		if next <= start {
			next = start + 1
		}
		start = next
	}
	return chunks
}

// AggregateVerdicts merges multi-model classifications (QM reference: strict
// wins on a decision tie, otherwise the higher score wins).
func AggregateVerdicts(verdicts []ScreenVerdict) ScreenVerdict {
	if len(verdicts) == 0 {
		return UnscreenedVerdict("payload")
	}
	// Any unscreened verdict keeps the aggregate unscreened: a classifier that
	// could not run must not vouch for safety.
	for _, v := range verdicts {
		if v.Unscreened {
			v.Decision = ScreenDecisionAuto
			v.Reason = UnscreenedReason
			return v
		}
	}
	sorted := append([]ScreenVerdict(nil), verdicts...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Decision != sorted[j].Decision {
			return sorted[i].Decision == ScreenDecisionStrict
		}
		return sorted[i].Score > sorted[j].Score
	})
	return sorted[0]
}

// Screener classifies untrusted content before it reaches the model.
//
// Implementations must never block the caller beyond their own timeout and
// must degrade to an UnscreenedVerdict instead of failing the turn.
type Screener interface {
	// Classify screens one payload at one hook point.
	Classify(ctx context.Context, payload string, hook ScreenHook, metadata map[string]string) ScreenVerdict
	// Shadow reports whether the screener runs in shadow mode: verdicts are
	// recorded for audit but never gate the pipeline.
	Shadow() bool
	// Provider identifies the classifier for audit trails.
	Provider() string
}

// NoopScreener is the default disabled screener: always auto, never blocks.
type NoopScreener struct{}

func (NoopScreener) Classify(context.Context, string, ScreenHook, map[string]string) ScreenVerdict {
	return ScreenVerdict{Decision: ScreenDecisionAuto}
}

func (NoopScreener) Shadow() bool { return true }

func (NoopScreener) Provider() string { return "none" }
