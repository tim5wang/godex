package agent

import (
	"fmt"
	"strings"
	"time"
)

// AgentIdentity is the stable runtime identity/manifest for a main agent,
// durable subagent, workflow node, or automation actor.
type AgentIdentity struct {
	ID                string            `json:"id"`
	Name              string            `json:"name,omitempty"`
	Kind              string            `json:"kind,omitempty"`
	Role              string            `json:"role,omitempty"`
	ParentID          string            `json:"parent_id,omitempty"`
	SessionID         string            `json:"session_id,omitempty"`
	Source            string            `json:"source,omitempty"`
	CreatedAt         time.Time         `json:"created_at"`
	UpdatedAt         time.Time         `json:"updated_at"`
	CapabilitySummary []string          `json:"capability_summary,omitempty"`
	ModelHint         string            `json:"model_hint,omitempty"`
	BudgetHint        string            `json:"budget_hint,omitempty"`
	Display           map[string]string `json:"display,omitempty"`
}

// AgentManifest currently aliases identity plus executable contract metadata.
type AgentManifest struct {
	Identity AgentIdentity `json:"identity"`
	Tools    []string      `json:"tools,omitempty"`
	Bundles  []string      `json:"bundles,omitempty"`
}

func NewAgentIdentity(now time.Time, sessionID, kind, name, role, parentID, source string, capabilities []string) AgentIdentity {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	kind = strings.TrimSpace(kind)
	if kind == "" {
		kind = "agent"
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = kind
	}
	idSeed := compactIdentityPart(sessionID)
	if idSeed == "" {
		idSeed = fmt.Sprintf("%d", now.UnixNano())
	}
	return AgentIdentity{
		ID:                kind + "_" + idSeed + "_" + fmt.Sprintf("%x", now.UnixNano())[:8],
		Name:              name,
		Kind:              kind,
		Role:              strings.TrimSpace(role),
		ParentID:          strings.TrimSpace(parentID),
		SessionID:         strings.TrimSpace(sessionID),
		Source:            strings.TrimSpace(source),
		CreatedAt:         now,
		UpdatedAt:         now,
		CapabilitySummary: uniqueIdentityStrings(capabilities),
	}
}

func NormalizeAgentIdentity(identity AgentIdentity, now time.Time, sessionID, kind, name string, capabilities []string) AgentIdentity {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if strings.TrimSpace(identity.ID) == "" {
		identity = NewAgentIdentity(now, sessionID, kind, name, identity.Role, identity.ParentID, identity.Source, capabilities)
	}
	if strings.TrimSpace(identity.SessionID) == "" {
		identity.SessionID = strings.TrimSpace(sessionID)
	}
	if strings.TrimSpace(identity.Kind) == "" {
		identity.Kind = strings.TrimSpace(kind)
	}
	if strings.TrimSpace(identity.Name) == "" {
		identity.Name = strings.TrimSpace(name)
	}
	if identity.CreatedAt.IsZero() {
		identity.CreatedAt = now
	}
	if identity.UpdatedAt.IsZero() {
		identity.UpdatedAt = now
	}
	if len(identity.CapabilitySummary) == 0 {
		identity.CapabilitySummary = uniqueIdentityStrings(capabilities)
	}
	return identity
}

func compactIdentityPart(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-', r == '_':
			b.WriteRune(r)
		}
		if b.Len() >= 24 {
			break
		}
	}
	return strings.Trim(b.String(), "-_")
}

func uniqueIdentityStrings(items []string) []string {
	out := make([]string, 0, len(items))
	seen := map[string]struct{}{}
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}
