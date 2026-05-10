package sessionrepair

import "time"

const (
	CodeCheckpointPointerInvalid = "checkpoint_pointer_invalid"
	CodeRootStateRestored        = "root_state_restored"
	CodeManifestDigestRecomputed = "manifest_digest_recomputed"
	CodeStaleTurnInterrupted     = "stale_turn_interrupted"
	CodeOrphanInjectedQueued     = "orphan_injected_queued"
)

type Request struct {
	SessionsDir string
	SessionID   string
	DryRun      bool
	Now         time.Time
}

type Report struct {
	Sessions   []SessionReport `json:"sessions"`
	Findings   []Finding       `json:"findings,omitempty"`
	Actions    []Action        `json:"actions,omitempty"`
	Changed    bool            `json:"changed"`
	Candidates int             `json:"candidates"`
	Errors     []string        `json:"errors,omitempty"`
}

type SessionReport struct {
	SessionID string    `json:"session_id"`
	Findings  []Finding `json:"findings,omitempty"`
	Actions   []Action  `json:"actions,omitempty"`
	BackupDir string    `json:"backup_dir,omitempty"`
	Changed   bool      `json:"changed"`
	Error     string    `json:"error,omitempty"`
}

type Finding struct {
	Code     string `json:"code"`
	Severity string `json:"severity,omitempty"`
	Path     string `json:"path,omitempty"`
	Message  string `json:"message,omitempty"`
}

type Action struct {
	Code    string `json:"code"`
	Status  string `json:"status"`
	Path    string `json:"path,omitempty"`
	Message string `json:"message,omitempty"`
}

func (r *Report) addSession(session SessionReport) {
	r.Sessions = append(r.Sessions, session)
	r.Findings = append(r.Findings, session.Findings...)
	r.Actions = append(r.Actions, session.Actions...)
	if len(session.Findings) > 0 {
		r.Candidates++
	}
	if session.Changed {
		r.Changed = true
	}
	if session.Error != "" {
		r.Errors = append(r.Errors, session.Error)
	}
}
