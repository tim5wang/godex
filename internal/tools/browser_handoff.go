package tools

import (
	"context"
	"fmt"
	"strings"
	"time"
)

func (s *BrowserService) Handoff(ctx context.Context, sessionID, pageID, rawURL, reason string, maxChars int) (BrowserHandoffResult, error) {
	_ = maxChars
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return BrowserHandoffResult{}, fmt.Errorf("browser handoff requires a session-scoped context")
	}
	targetURL := strings.TrimSpace(rawURL)
	requestedPageID := strings.TrimSpace(pageID)
	reason = strings.TrimSpace(reason)

	s.mu.Lock()
	cfg := s.cfg
	s.mu.Unlock()
	if !cfg.Enabled {
		return BrowserHandoffResult{}, fmt.Errorf("browser is disabled in tools.browser.enabled")
	}

	var (
		state          *browserPageState
		reopenedFromID string
	)
	if requestedPageID != "" {
		current, _, err := s.pageState(sessionID, requestedPageID)
		if err != nil {
			if targetURL == "" {
				return BrowserHandoffResult{}, err
			}
		} else {
			current.mu.Lock()
			s.refreshPageInfoLocked(current)
			currentURL := strings.TrimSpace(current.pageInfo.URL)
			current.mu.Unlock()
			state = current
			if targetURL == "" {
				targetURL = currentURL
			}
		}
	}
	if targetURL == "" {
		return BrowserHandoffResult{}, fmt.Errorf("browser handoff requires page_id with a current URL or url")
	}
	if _, err := validateRemoteURL(targetURL, cfg.AllowPrivateHosts); err != nil {
		return BrowserHandoffResult{}, err
	}

	mode := "headed"
	switchedToHeaded := false
	s.mu.Lock()
	cfg = s.cfg
	switch {
	case strings.TrimSpace(cfg.CDPURL) != "":
		mode = "external_cdp"
	case cfg.Headless:
		reopenedFromID = requestedPageID
		s.stopLocked()
		s.cfg.Headless = false
		switchedToHeaded = true
		state = nil
		requestedPageID = ""
		mode = "local_headed"
	}
	s.mu.Unlock()

	if err := s.start(ctx); err != nil {
		if switchedToHeaded {
			s.mu.Lock()
			s.cfg.Headless = true
			s.mu.Unlock()
		}
		return BrowserHandoffResult{}, err
	}

	if state == nil && requestedPageID != "" {
		if current, _, err := s.pageState(sessionID, requestedPageID); err == nil {
			state = current
		}
	}
	if state == nil {
		page, err := s.openPageLocked(ctx, sessionID, targetURL)
		if err != nil {
			return BrowserHandoffResult{}, err
		}
		current, _, err := s.pageState(sessionID, page.PageID)
		if err != nil {
			return BrowserHandoffResult{}, err
		}
		state = current
	} else if raw := strings.TrimSpace(rawURL); raw != "" {
		state.mu.Lock()
		currentURL := strings.TrimSpace(state.pageInfo.URL)
		currentPageID := state.pageInfo.PageID
		state.mu.Unlock()
		if currentURL != targetURL {
			_, cfg, err := s.pageState(sessionID, currentPageID)
			if err != nil {
				return BrowserHandoffResult{}, err
			}
			actionCtx, cancel := context.WithTimeout(ctx, cfgTimeout(cfg.ActionTimeoutSeconds))
			defer cancel()
			state.mu.Lock()
			page := state.page.Context(actionCtx)
			if err := page.Navigate(targetURL); err != nil {
				state.mu.Unlock()
				return BrowserHandoffResult{}, err
			}
			if err := page.WaitStable(300 * time.Millisecond); err != nil && ctx.Err() != nil {
				state.mu.Unlock()
				return BrowserHandoffResult{}, ctx.Err()
			}
			s.refreshPageInfoLocked(state)
			state.mu.Unlock()
		}
	}
	if state == nil {
		return BrowserHandoffResult{}, fmt.Errorf("browser handoff could not resolve an active page")
	}
	state.mu.Lock()
	state.handoffActive = true
	state.handoffReason = reason
	state.handoffAt = s.now()
	s.touchPageLocked(state)
	s.refreshPageInfoLocked(state)
	pageInfo := state.pageInfo
	handoffAt := state.handoffAt
	state.mu.Unlock()

	message := "A visible browser is ready for user assistance. Complete the required steps, then ask GoDex to resume the browser."
	if reason != "" {
		message = message + " Reason: " + reason
	}
	return BrowserHandoffResult{
		Page:             pageInfo,
		Status:           "waiting_for_user",
		Mode:             mode,
		Reason:           reason,
		Message:          message,
		ResumeAction:     fmt.Sprintf("browser resume page_id=%s", pageInfo.PageID),
		StartedAt:        handoffAt,
		NeedsUserAction:  true,
		Headed:           true,
		ExternalCDP:      mode == "external_cdp",
		ReopenedFromPage: strings.TrimSpace(reopenedFromID),
	}, nil
}

func (s *BrowserService) ResumeHandoff(ctx context.Context, sessionID, pageID string, maxChars int) (BrowserResumeResult, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return BrowserResumeResult{}, fmt.Errorf("browser resume requires a session-scoped context")
	}
	pageID = strings.TrimSpace(pageID)
	var page BrowserPage
	s.mu.Lock()
	state, err := s.pageStateForResumeLocked(sessionID, pageID)
	if err != nil {
		s.mu.Unlock()
		return BrowserResumeResult{}, err
	}
	s.mu.Unlock()
	state.mu.Lock()
	state.handoffActive = false
	state.handoffReason = ""
	state.handoffAt = time.Time{}
	s.touchPageLocked(state)
	s.refreshPageInfoLocked(state)
	page = state.pageInfo
	state.mu.Unlock()

	snapshot, err := s.Snapshot(ctx, sessionID, page.PageID, maxChars)
	if err != nil {
		return BrowserResumeResult{}, err
	}
	return BrowserResumeResult{
		Page:     page,
		Status:   "resumed",
		Message:  "Browser handoff resumed. Use the snapshot to continue the task.",
		Snapshot: snapshot,
	}, nil
}

func (s *BrowserService) pageStateForResumeLocked(sessionID, pageID string) (*browserPageState, error) {
	if pageID != "" {
		return s.pageStateLocked(sessionID, pageID)
	}
	pages := s.pages[sessionID]
	if len(pages) == 0 {
		return nil, fmt.Errorf("no browser pages for this session")
	}
	if len(pages) > 1 {
		return nil, fmt.Errorf("browser resume requires page_id when the session has multiple pages")
	}
	for _, state := range pages {
		return state, nil
	}
	return nil, fmt.Errorf("no browser pages for this session")
}
