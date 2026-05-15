package security

import "time"

// SecurityPolicy is the normalized runtime security posture shown to users.
type SecurityPolicy struct {
	InteractiveApprovalEnabled bool     `json:"interactive_approval_enabled"`
	InteractiveApprovalMode    string   `json:"interactive_approval_mode,omitempty"`
	ApprovalSources            []string `json:"approval_sources,omitempty"`
	ApprovalTools              []string `json:"approval_tools,omitempty"`
	PendingTTLSeconds          int      `json:"pending_ttl_seconds,omitempty"`
	TrustedPathPrefixes        []string `json:"trusted_path_prefixes,omitempty"`
	TrustedCommandPrefixes     []string `json:"trusted_command_prefixes,omitempty"`
	BlockAutomationMutations   bool     `json:"block_automation_mutations"`
	MemoryIdentityReview       bool     `json:"memory_identity_review"`
	PackageInstallReview       bool     `json:"package_install_review"`
	SubagentWorkspaceIsolation bool     `json:"subagent_workspace_isolation"`
}

// SecurityEvent records one audit-relevant runtime action.
type SecurityEvent struct {
	ID        string            `json:"id"`
	At        time.Time         `json:"at"`
	Category  string            `json:"category"`
	Action    string            `json:"action"`
	Severity  string            `json:"severity,omitempty"`
	SessionID string            `json:"session_id,omitempty"`
	Source    string            `json:"source,omitempty"`
	Summary   string            `json:"summary,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

// RiskSummary is a compact per-axis risk slice.
type RiskSummary struct {
	Axis   string   `json:"axis"`
	Level  string   `json:"level"`
	Score  int      `json:"score"`
	Items  []string `json:"items,omitempty"`
	Advice []string `json:"advice,omitempty"`
}

// CIKSummary groups Capability, Identity, and Knowledge risk state.
type CIKSummary struct {
	GeneratedAt time.Time       `json:"generated_at"`
	Policy      SecurityPolicy  `json:"policy"`
	Capability  RiskSummary     `json:"capability"`
	Identity    RiskSummary     `json:"identity"`
	Knowledge   RiskSummary     `json:"knowledge"`
	Recent      []SecurityEvent `json:"recent,omitempty"`
}
