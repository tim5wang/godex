package conversation

import "strings"

// PromptLayers represents layered system prompt content.
type PromptLayers struct {
	Base   string
	Skills []string
	Extra  []string
}

// Build renders the layered prompt into a single system prompt string.
func (p PromptLayers) Build() string {
	parts := make([]string, 0, 1+len(p.Skills)+len(p.Extra))
	if p.Base != "" {
		parts = append(parts, p.Base)
	}
	parts = append(parts, p.Skills...)
	parts = append(parts, p.Extra...)
	return strings.Join(parts, "\n\n")
}

// Clone returns a deep copy of the prompt layers.
func (p PromptLayers) Clone() PromptLayers {
	clone := PromptLayers{Base: p.Base}
	if len(p.Skills) > 0 {
		clone.Skills = append([]string{}, p.Skills...)
	}
	if len(p.Extra) > 0 {
		clone.Extra = append([]string{}, p.Extra...)
	}
	return clone
}
