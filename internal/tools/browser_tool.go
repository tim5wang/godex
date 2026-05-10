package tools

import (
	"context"
	"fmt"
	"strings"
)

type browserArgs struct {
	Action        string             `json:"action"`
	PageID        string             `json:"page_id,omitempty"`
	URL           string             `json:"url,omitempty"`
	Ref           string             `json:"ref,omitempty"`
	Selector      string             `json:"selector,omitempty"`
	MatchText     string             `json:"match_text,omitempty"`
	Placeholder   string             `json:"placeholder,omitempty"`
	Label         string             `json:"label,omitempty"`
	Tag           string             `json:"tag,omitempty"`
	HrefContains  string             `json:"href_contains,omitempty"`
	InputType     string             `json:"input_type,omitempty"`
	Text          string             `json:"text,omitempty"`
	Key           string             `json:"key,omitempty"`
	TimeMS        int                `json:"time_ms,omitempty"`
	MaxChars      int                `json:"max_chars,omitempty"`
	MaxEntries    int                `json:"max_entries,omitempty"`
	NetworkIdleMS int                `json:"network_idle_ms,omitempty"`
	FullPage      bool               `json:"full_page,omitempty"`
	Query         string             `json:"query,omitempty"`
	Reason        string             `json:"reason,omitempty"`
	Path          string             `json:"path,omitempty"`
	Paths         []string           `json:"paths,omitempty"`
	FileName      string             `json:"file_name,omitempty"`
	Fields        []BrowserFormField `json:"fields,omitempty"`
}

func NewBrowserTool(service *BrowserService, workspace string) Tool {
	return NewTypedTool(NewToolSpec("browser", "Control a lightweight browser for dynamic pages: open, navigate, snapshot, click, type, wait, screenshot, handoff to a visible browser for user assistance, resume, and close.", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"description": "status | open | navigate | snapshot | click | type | press | wait | screenshot | close | list_pages | find | fill_form | upload_file | wait_network_idle | network_snapshot | download | capture_page | search_and_open | handoff | resume",
				"enum":        []string{"status", "open", "navigate", "snapshot", "click", "type", "press", "wait", "screenshot", "close", "list_pages", "find", "fill_form", "upload_file", "wait_network_idle", "network_snapshot", "download", "capture_page", "search_and_open", "handoff", "resume"},
			},
			"page_id":       map[string]interface{}{"type": "string"},
			"url":           map[string]interface{}{"type": "string"},
			"ref":           map[string]interface{}{"type": "string"},
			"selector":      map[string]interface{}{"type": "string"},
			"match_text":    map[string]interface{}{"type": "string"},
			"placeholder":   map[string]interface{}{"type": "string"},
			"label":         map[string]interface{}{"type": "string"},
			"tag":           map[string]interface{}{"type": "string"},
			"href_contains": map[string]interface{}{"type": "string"},
			"input_type":    map[string]interface{}{"type": "string"},
			"text":          map[string]interface{}{"type": "string"},
			"key":           map[string]interface{}{"type": "string"},
			"time_ms":       map[string]interface{}{"type": "integer"},
			"max_entries":   map[string]interface{}{"type": "integer"},
			"network_idle_ms": map[string]interface{}{
				"type":        "integer",
				"description": "Idle duration in milliseconds before network is considered quiet",
			},
			"max_chars": map[string]interface{}{
				"type":        "integer",
				"description": "Maximum body text chars to include in snapshot",
			},
			"full_page": map[string]interface{}{"type": "boolean"},
			"query":     map[string]interface{}{"type": "string"},
			"reason": map[string]interface{}{
				"type":        "string",
				"description": "Why user assistance is needed for browser handoff",
			},
			"path": map[string]interface{}{"type": "string"},
			"paths": map[string]interface{}{
				"type":  "array",
				"items": map[string]interface{}{"type": "string"},
			},
			"file_name": map[string]interface{}{"type": "string"},
			"fields": map[string]interface{}{
				"type": "array",
				"items": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"ref":         map[string]interface{}{"type": "string"},
						"selector":    map[string]interface{}{"type": "string"},
						"text":        map[string]interface{}{"type": "string"},
						"placeholder": map[string]interface{}{"type": "string"},
						"label":       map[string]interface{}{"type": "string"},
						"tag":         map[string]interface{}{"type": "string"},
						"input_type":  map[string]interface{}{"type": "string"},
						"value":       map[string]interface{}{"type": "string"},
					},
					"required": []string{"value"},
				},
			},
		},
		"required": []string{"action"},
	}, nil), func(ctx context.Context, args browserArgs) (ToolResult, error) {
		if service == nil {
			return ToolResult{}, fmt.Errorf("browser service is unavailable")
		}
		sessionID := SessionIDFromContext(ctx)
		if sessionID == "" {
			sessionID = strings.TrimSpace(SessionContextFromContext(ctx).SessionID)
		}
		action := strings.TrimSpace(args.Action)
		locator := locatorFromArgs(args)

		var (
			payload any
			err     error
		)
		switch action {
		case "status":
			payload = service.Status()
		case "list_pages":
			payload = service.ListPages(sessionID)
		case "open":
			payload, err = service.Open(ctx, sessionID, args.URL)
		case "navigate":
			payload, err = service.Navigate(ctx, sessionID, args.PageID, args.URL)
		case "snapshot":
			payload, err = service.Snapshot(ctx, sessionID, args.PageID, args.MaxChars)
		case "click":
			err = service.ClickTarget(ctx, sessionID, args.PageID, locator)
			payload = map[string]string{"status": "ok"}
		case "type":
			err = service.TypeTarget(ctx, sessionID, args.PageID, locator, args.Text)
			payload = map[string]string{"status": "ok"}
		case "press":
			err = service.Press(ctx, sessionID, args.PageID, args.Key)
			payload = map[string]string{"status": "ok"}
		case "wait":
			err = service.Wait(ctx, sessionID, args.PageID, args.Text, args.TimeMS)
			payload = map[string]string{"status": "ok"}
		case "find":
			payload, err = service.Find(ctx, sessionID, args.PageID, locator, args.MaxEntries)
		case "fill_form":
			payload, err = service.FillForm(ctx, sessionID, args.PageID, args.Fields)
		case "upload_file":
			paths, resolveErr := resolveBrowserUploadPaths(workspace, args.Path, args.Paths)
			if resolveErr != nil {
				return ToolResult{}, resolveErr
			}
			err = service.UploadFiles(ctx, sessionID, args.PageID, locator, paths)
			payload = map[string]interface{}{
				"status":      "ok",
				"uploaded":    len(paths),
				"source_path": paths,
			}
		case "wait_network_idle":
			err = service.WaitNetworkIdle(ctx, sessionID, args.PageID, args.NetworkIdleMS)
			payload = map[string]string{"status": "ok"}
		case "network_snapshot":
			payload, err = service.NetworkSnapshot(ctx, sessionID, args.PageID, args.MaxEntries)
		case "download":
			payload, err = service.Download(ctx, sessionID, args.PageID, locator, args.URL, args.FileName)
		case "screenshot":
			var path string
			path, err = service.Screenshot(ctx, sessionID, args.PageID, args.FullPage)
			if err == nil {
				payload = BrowserScreenshotResult{
					PageID:                     strings.TrimSpace(args.PageID),
					ArtifactPath:               path,
					Kind:                       "image",
					AutoAttachInSupportedReply: true,
				}
			}
		case "capture_page":
			payload, err = service.CapturePage(ctx, sessionID, args.PageID, args.URL, args.Text, args.TimeMS, args.NetworkIdleMS, args.FullPage, args.MaxChars)
		case "search_and_open":
			payload, err = service.SearchAndOpen(ctx, sessionID, args.PageID, args.URL, args.Query, args.NetworkIdleMS, args.MaxChars)
		case "handoff":
			payload, err = service.Handoff(ctx, sessionID, args.PageID, args.URL, args.Reason, args.MaxChars)
		case "resume":
			payload, err = service.ResumeHandoff(ctx, sessionID, args.PageID, args.MaxChars)
		case "close":
			err = service.Close(sessionID, args.PageID)
			payload = map[string]string{"status": "ok"}
		default:
			return ToolResult{}, fmt.Errorf("unknown browser action %q", action)
		}
		if err != nil {
			return ToolResult{}, err
		}
		result := ToolResult{Structured: payload}
		if action == "screenshot" {
			if screenshot, ok := payload.(BrowserScreenshotResult); ok && strings.TrimSpace(screenshot.ArtifactPath) != "" {
				result.ArtifactPaths = []string{strings.TrimSpace(screenshot.ArtifactPath)}
			}
		}
		if action == "download" {
			if download, ok := payload.(BrowserDownloadResult); ok && strings.TrimSpace(download.ArtifactPath) != "" {
				result.ArtifactPaths = []string{strings.TrimSpace(download.ArtifactPath)}
			}
		}
		if action == "capture_page" {
			if capture, ok := payload.(BrowserCaptureResult); ok && strings.TrimSpace(capture.Screenshot.ArtifactPath) != "" {
				result.ArtifactPaths = []string{strings.TrimSpace(capture.Screenshot.ArtifactPath)}
			}
		}
		return result, nil
	})
}
