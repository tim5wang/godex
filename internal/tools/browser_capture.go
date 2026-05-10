package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/go-rod/rod/lib/input"
)

func (s *BrowserService) CapturePage(ctx context.Context, sessionID, pageID, rawURL, waitText string, waitMS, idleMS int, fullPage bool, maxChars int) (BrowserCaptureResult, error) {
	capturePageID := strings.TrimSpace(pageID)
	var page BrowserPage
	var err error

	if strings.TrimSpace(rawURL) != "" {
		if capturePageID == "" {
			page, err = s.Open(ctx, sessionID, rawURL)
		} else {
			page, err = s.Navigate(ctx, sessionID, capturePageID, rawURL)
		}
		if err != nil {
			return BrowserCaptureResult{}, err
		}
		capturePageID = page.PageID
	} else if capturePageID == "" {
		return BrowserCaptureResult{}, fmt.Errorf("capture_page requires page_id or url")
	}

	if waitMS > 0 || strings.TrimSpace(waitText) != "" {
		if err = s.Wait(ctx, sessionID, capturePageID, waitText, waitMS); err != nil {
			return BrowserCaptureResult{}, err
		}
	}
	if idleMS > 0 {
		if err = s.WaitNetworkIdle(ctx, sessionID, capturePageID, idleMS); err != nil {
			return BrowserCaptureResult{}, err
		}
	}

	snapshot, err := s.Snapshot(ctx, sessionID, capturePageID, maxChars)
	if err != nil {
		return BrowserCaptureResult{}, err
	}
	path, err := s.Screenshot(ctx, sessionID, capturePageID, fullPage)
	if err != nil {
		return BrowserCaptureResult{}, err
	}
	if strings.TrimSpace(page.PageID) == "" {
		page = BrowserPage{PageID: capturePageID, URL: snapshot.URL, Title: snapshot.Title, SessionID: sessionID}
	}
	return BrowserCaptureResult{
		Page:     page,
		Snapshot: snapshot,
		Screenshot: BrowserScreenshotResult{
			PageID:                     capturePageID,
			ArtifactPath:               path,
			Kind:                       "image",
			AutoAttachInSupportedReply: true,
		},
	}, nil
}

func (s *BrowserService) SearchAndOpen(ctx context.Context, sessionID, pageID, rawURL, query string, idleMS, maxChars int) (BrowserSearchResult, error) {
	if strings.TrimSpace(query) == "" {
		return BrowserSearchResult{}, fmt.Errorf("search_and_open requires query")
	}
	var (
		page BrowserPage
		err  error
	)
	if strings.TrimSpace(rawURL) != "" {
		if strings.TrimSpace(pageID) == "" {
			page, err = s.Open(ctx, sessionID, rawURL)
		} else {
			page, err = s.Navigate(ctx, sessionID, pageID, rawURL)
		}
		if err != nil {
			return BrowserSearchResult{}, err
		}
		pageID = page.PageID
	} else {
		state, _, stateErr := s.pageState(sessionID, pageID)
		if stateErr == nil {
			state.mu.Lock()
			page = state.pageInfo
			state.mu.Unlock()
		}
		if stateErr != nil {
			return BrowserSearchResult{}, stateErr
		}
	}

	state, cfg, err := s.pageState(sessionID, pageID)
	if err != nil {
		return BrowserSearchResult{}, err
	}
	actionCtx, cancel := context.WithTimeout(ctx, cfgTimeout(cfg.ActionTimeoutSeconds))
	defer cancel()
	state.mu.Lock()
	pageHandle := state.page.Context(actionCtx)
	selector, err := s.findSearchInputSelectorOnPageLocked(state, pageHandle)
	if err != nil {
		state.mu.Unlock()
		return BrowserSearchResult{}, err
	}
	el, err := pageHandle.Element(selector)
	if err != nil {
		state.mu.Unlock()
		return BrowserSearchResult{}, err
	}
	if err := el.SelectAllText(); err == nil {
		err = el.Input(query)
	}
	if err != nil {
		state.mu.Unlock()
		return BrowserSearchResult{}, err
	}
	submitted := false
	if result, submitErr := el.Eval(`() => {
if (!this || !this.form) return false;
if (typeof this.form.requestSubmit === "function") {
  this.form.requestSubmit();
} else {
  this.form.submit();
}
return true;
	}`); submitErr == nil {
		submitted = result.Value.Bool()
	}
	if !submitted {
		if err := pageHandle.Keyboard.Press(input.Enter); err != nil {
			state.mu.Unlock()
			return BrowserSearchResult{}, err
		}
	}
	s.touchPageLocked(state)
	s.refreshPageInfoLocked(state)
	page = state.pageInfo
	state.mu.Unlock()

	if idleMS > 0 {
		if err := s.WaitNetworkIdle(ctx, sessionID, pageID, idleMS); err != nil {
			return BrowserSearchResult{}, err
		}
	} else if err := s.Wait(ctx, sessionID, pageID, "", 1000); err != nil {
		return BrowserSearchResult{}, err
	}
	snapshot, err := s.Snapshot(ctx, sessionID, pageID, maxChars)
	if err != nil {
		return BrowserSearchResult{}, err
	}
	if state, _, stateErr := s.pageState(sessionID, pageID); stateErr == nil {
		state.mu.Lock()
		page = state.pageInfo
		state.mu.Unlock()
	}
	return BrowserSearchResult{
		Page:     page,
		Query:    query,
		Snapshot: snapshot,
	}, nil
}
