package tools

import (
	"context"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/tim5wang/godex/internal/core/config"
)

type BrowserStatus struct {
	Enabled  bool `json:"enabled"`
	Running  bool `json:"running"`
	Sessions int  `json:"sessions"`
	Pages    int  `json:"pages"`
}

type BrowserPage struct {
	PageID    string    `json:"page_id"`
	URL       string    `json:"url,omitempty"`
	Title     string    `json:"title,omitempty"`
	LastUsed  time.Time `json:"last_used"`
	SessionID string    `json:"session_id,omitempty"`
}

type BrowserElement struct {
	Ref      string `json:"ref"`
	Selector string `json:"selector,omitempty"`
	Tag      string `json:"tag,omitempty"`
	Text     string `json:"text,omitempty"`
	Type     string `json:"type,omitempty"`
	Href     string `json:"href,omitempty"`
}

type BrowserSnapshot struct {
	PageID   string           `json:"page_id"`
	Title    string           `json:"title,omitempty"`
	URL      string           `json:"url,omitempty"`
	Text     string           `json:"text,omitempty"`
	Elements []BrowserElement `json:"elements,omitempty"`
}

type BrowserLocator struct {
	Ref          string `json:"ref,omitempty"`
	Selector     string `json:"selector,omitempty"`
	Text         string `json:"text,omitempty"`
	Placeholder  string `json:"placeholder,omitempty"`
	Label        string `json:"label,omitempty"`
	Tag          string `json:"tag,omitempty"`
	HrefContains string `json:"href_contains,omitempty"`
	InputType    string `json:"input_type,omitempty"`
}

type BrowserScreenshotResult struct {
	PageID                     string `json:"page_id,omitempty"`
	ArtifactPath               string `json:"artifact_path"`
	Kind                       string `json:"kind,omitempty"`
	AutoAttachInSupportedReply bool   `json:"auto_attach_in_supported_replies,omitempty"`
}

type BrowserDownloadResult struct {
	PageID                     string `json:"page_id,omitempty"`
	ArtifactPath               string `json:"artifact_path"`
	FileName                   string `json:"file_name,omitempty"`
	URL                        string `json:"url,omitempty"`
	Kind                       string `json:"kind,omitempty"`
	AutoAttachInSupportedReply bool   `json:"auto_attach_in_supported_replies,omitempty"`
}

type BrowserNetworkEntry struct {
	EntryType     string  `json:"entry_type,omitempty"`
	URL           string  `json:"url,omitempty"`
	InitiatorType string  `json:"initiator_type,omitempty"`
	TransferSize  int64   `json:"transfer_size,omitempty"`
	DurationMS    float64 `json:"duration_ms,omitempty"`
	StartTimeMS   float64 `json:"start_time_ms,omitempty"`
}

type BrowserNetworkSnapshot struct {
	PageID  string                `json:"page_id,omitempty"`
	URL     string                `json:"url,omitempty"`
	Entries []BrowserNetworkEntry `json:"entries,omitempty"`
}

type BrowserFormField struct {
	Ref         string `json:"ref,omitempty"`
	Selector    string `json:"selector,omitempty"`
	Text        string `json:"text,omitempty"`
	Placeholder string `json:"placeholder,omitempty"`
	Label       string `json:"label,omitempty"`
	Tag         string `json:"tag,omitempty"`
	InputType   string `json:"input_type,omitempty"`
	Value       string `json:"value"`
}

type BrowserFillFormResult struct {
	PageID        string   `json:"page_id,omitempty"`
	FilledFields  int      `json:"filled_fields"`
	FilledTargets []string `json:"filled_targets,omitempty"`
}

type BrowserCaptureResult struct {
	Page       BrowserPage             `json:"page"`
	Snapshot   BrowserSnapshot         `json:"snapshot"`
	Screenshot BrowserScreenshotResult `json:"screenshot"`
}

type BrowserSearchResult struct {
	Page     BrowserPage     `json:"page"`
	Query    string          `json:"query"`
	Snapshot BrowserSnapshot `json:"snapshot"`
}

type BrowserHandoffResult struct {
	Page             BrowserPage `json:"page"`
	Status           string      `json:"status"`
	Mode             string      `json:"mode"`
	Reason           string      `json:"reason,omitempty"`
	Message          string      `json:"message"`
	ResumeAction     string      `json:"resume_action"`
	StartedAt        time.Time   `json:"started_at"`
	NeedsUserAction  bool        `json:"needs_user_action"`
	Headed           bool        `json:"headed"`
	ExternalCDP      bool        `json:"external_cdp"`
	ReopenedFromPage string      `json:"reopened_from_page_id,omitempty"`
}

type BrowserResumeResult struct {
	Page     BrowserPage     `json:"page"`
	Status   string          `json:"status"`
	Message  string          `json:"message"`
	Snapshot BrowserSnapshot `json:"snapshot"`
}

type browserFindPayload struct {
	Error    string           `json:"error,omitempty"`
	Selector string           `json:"selector,omitempty"`
	Elements []BrowserElement `json:"elements,omitempty"`
}

type browserPageState struct {
	mu            sync.Mutex
	page          *rod.Page
	pageInfo      BrowserPage
	refs          map[string]string
	handoffActive bool
	handoffReason string
	handoffAt     time.Time
}

// BrowserService manages lightweight rod-backed browser automation for Godex sessions.
type BrowserService struct {
	mu                 sync.Mutex
	launchMu           sync.Mutex
	cfg                config.BrowserConfig
	storage            config.StorageConfig
	tempDir            string
	browser            *rod.Browser
	launcher           *launcher.Launcher
	pages              map[string]map[string]*browserPageState
	now                func() time.Time
	counter            uint64
	resolveBrowserPath func(string) string
	downloadBrowser    func(context.Context, string) (string, error)
}

const browserLaunchTimeout = 10 * time.Minute
