package tools

import (
	"context"
	"fmt"
	urlpkg "net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/platform/browserutil"
)

func NewBrowserService(cfg config.BrowserConfig, tempDir string, storage ...config.StorageConfig) *BrowserService {
	service := &BrowserService{
		pages:              make(map[string]map[string]*browserPageState),
		now:                time.Now,
		tempDir:            tempDir,
		resolveBrowserPath: browserutil.ResolvePath,
		downloadBrowser:    defaultDownloadBrowser,
	}
	if len(storage) > 0 {
		service.storage = storage[0]
	}
	service.ApplyConfig(cfg, tempDir)
	return service
}

func (s *BrowserService) ApplyStorageConfig(storage config.StorageConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.storage = storage
}

func (s *BrowserService) ApplyConfig(cfg config.BrowserConfig, tempDir string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg = normalizeBrowserConfig(cfg)
	s.tempDir = tempDir
	s.stopLocked()
	s.pages = make(map[string]map[string]*browserPageState)
}

func (s *BrowserService) Status() BrowserStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupExpiredLocked()
	status := BrowserStatus{
		Enabled: s.cfg.Enabled,
		Running: s.browser != nil,
	}
	for _, pages := range s.pages {
		if len(pages) > 0 {
			status.Sessions++
		}
		status.Pages += len(pages)
	}
	return status
}

func (s *BrowserService) ListPages(sessionID string) []BrowserPage {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupExpiredLocked()
	pages := s.pages[sessionID]
	out := make([]BrowserPage, 0, len(pages))
	for _, state := range pages {
		out = append(out, state.pageInfo)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].LastUsed.Equal(out[j].LastUsed) {
			return out[i].PageID < out[j].PageID
		}
		return out[i].LastUsed.After(out[j].LastUsed)
	})
	return out
}

func (s *BrowserService) Open(ctx context.Context, sessionID, rawURL string) (BrowserPage, error) {
	if sessionID == "" {
		return BrowserPage{}, fmt.Errorf("browser requires a session-scoped context")
	}
	if _, err := validateRemoteURL(rawURL, s.cfg.AllowPrivateHosts); err != nil {
		return BrowserPage{}, err
	}
	s.mu.Lock()
	if !s.cfg.Enabled {
		s.mu.Unlock()
		return BrowserPage{}, fmt.Errorf("browser is disabled in tools.browser.enabled")
	}
	s.cleanupExpiredLocked()
	s.mu.Unlock()
	if err := s.start(ctx); err != nil {
		return BrowserPage{}, err
	}
	return s.openPageLocked(ctx, sessionID, rawURL)
}

func (s *BrowserService) openPageLocked(ctx context.Context, sessionID, rawURL string) (BrowserPage, error) {
	browser, cfg, err := s.browserSnapshot()
	if err != nil {
		return BrowserPage{}, err
	}
	s.mu.Lock()
	sessionPages := s.pages[sessionID]
	if sessionPages == nil {
		sessionPages = make(map[string]*browserPageState)
		s.pages[sessionID] = sessionPages
	}
	if len(sessionPages) >= cfg.MaxPagesPerSession {
		s.mu.Unlock()
		return BrowserPage{}, fmt.Errorf("session already has %d open browser pages", cfg.MaxPagesPerSession)
	}
	pageID := s.nextPageIDLocked()
	now := s.now()
	s.mu.Unlock()

	timeout := cfgTimeout(cfg.ActionTimeoutSeconds)
	pageCtx, pageCancel := context.WithTimeout(ctx, timeout)
	defer pageCancel()
	page, err := browser.Context(pageCtx).Page(proto.TargetCreateTarget{URL: rawURL})
	if err != nil {
		return BrowserPage{}, err
	}
	if err := page.WaitStable(300 * time.Millisecond); err != nil && ctx.Err() == nil {
		// Best effort: still keep the page if it opened.
	}
	state := &browserPageState{
		page: page,
		pageInfo: BrowserPage{
			PageID:    pageID,
			LastUsed:  now,
			SessionID: sessionID,
		},
		refs: make(map[string]string),
	}
	state.mu.Lock()
	s.refreshPageInfoLocked(state)
	state.mu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	sessionPages = s.pages[sessionID]
	if sessionPages == nil {
		sessionPages = make(map[string]*browserPageState)
		s.pages[sessionID] = sessionPages
	}
	sessionPages[state.pageInfo.PageID] = state
	return state.pageInfo, nil
}

func (s *BrowserService) Navigate(ctx context.Context, sessionID, pageID, rawURL string) (BrowserPage, error) {
	if _, err := validateRemoteURL(rawURL, s.cfg.AllowPrivateHosts); err != nil {
		return BrowserPage{}, err
	}
	state, cfg, err := s.pageState(sessionID, pageID)
	if err != nil {
		return BrowserPage{}, err
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	navCtx, navCancel := context.WithTimeout(ctx, cfgTimeout(cfg.ActionTimeoutSeconds))
	defer navCancel()
	page := state.page.Context(navCtx)
	if err := page.Navigate(rawURL); err != nil {
		return BrowserPage{}, err
	}
	if err := page.WaitStable(300 * time.Millisecond); err != nil && ctx.Err() != nil {
		return BrowserPage{}, ctx.Err()
	}
	s.refreshPageInfoLocked(state)
	s.touchPageLocked(state)
	return state.pageInfo, nil
}

func (s *BrowserService) Snapshot(ctx context.Context, sessionID, pageID string, maxChars int) (BrowserSnapshot, error) {
	state, cfg, err := s.pageState(sessionID, pageID)
	if err != nil {
		return BrowserSnapshot{}, err
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if maxChars <= 0 {
		maxChars = 4000
	}
	actionCtx, cancel := context.WithTimeout(ctx, cfgTimeout(cfg.ActionTimeoutSeconds))
	defer cancel()
	page := state.page.Context(actionCtx)
	result, err := page.Eval(snapshotScript(maxChars))
	if err != nil {
		return BrowserSnapshot{}, err
	}
	var snapshot BrowserSnapshot
	if err := decodeEvalJSONString(result, &snapshot); err != nil {
		return BrowserSnapshot{}, err
	}
	snapshot.PageID = pageID
	state.refs = make(map[string]string, len(snapshot.Elements))
	for _, element := range snapshot.Elements {
		if element.Ref != "" && element.Selector != "" {
			state.refs[element.Ref] = element.Selector
		}
	}
	s.touchPageLocked(state)
	s.refreshPageInfoLocked(state)
	return snapshot, nil
}

func (s *BrowserService) Click(ctx context.Context, sessionID, pageID, ref string) error {
	state, cfg, err := s.pageState(sessionID, pageID)
	if err != nil {
		return err
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	selector, ok := state.refs[ref]
	if !ok {
		return fmt.Errorf("unknown browser ref %q; take a fresh snapshot first", ref)
	}
	actionCtx, cancel := context.WithTimeout(ctx, cfgTimeout(cfg.ActionTimeoutSeconds))
	defer cancel()
	page := state.page.Context(actionCtx)
	el, err := page.Element(selector)
	if err != nil {
		return err
	}
	if err := el.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return err
	}
	s.touchPageLocked(state)
	return nil
}

func (s *BrowserService) Type(ctx context.Context, sessionID, pageID, ref, text string) error {
	state, cfg, err := s.pageState(sessionID, pageID)
	if err != nil {
		return err
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	selector, ok := state.refs[ref]
	if !ok {
		return fmt.Errorf("unknown browser ref %q; take a fresh snapshot first", ref)
	}
	js := fmt.Sprintf(`() => {
const el = document.querySelector(%q);
if (!el) { throw new Error("element not found"); }
el.focus();
if ("value" in el) {
  el.value = %q;
  el.dispatchEvent(new Event("input", { bubbles: true }));
  el.dispatchEvent(new Event("change", { bubbles: true }));
  return "ok";
}
if (el.isContentEditable) {
  el.textContent = %q;
  el.dispatchEvent(new Event("input", { bubbles: true }));
  return "ok";
}
throw new Error("element does not accept text input");
}`, selector, text, text)
	actionCtx, cancel := context.WithTimeout(ctx, cfgTimeout(cfg.ActionTimeoutSeconds))
	defer cancel()
	page := state.page.Context(actionCtx)
	if _, err := page.Eval(js); err != nil {
		return err
	}
	s.touchPageLocked(state)
	return nil
}

func (s *BrowserService) Press(ctx context.Context, sessionID, pageID, key string) error {
	state, cfg, err := s.pageState(sessionID, pageID)
	if err != nil {
		return err
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	actionCtx, cancel := context.WithTimeout(ctx, cfgTimeout(cfg.ActionTimeoutSeconds))
	defer cancel()
	page := state.page.Context(actionCtx)
	if err := page.Keyboard.Press(mapKey(key)); err != nil {
		return err
	}
	s.touchPageLocked(state)
	return nil
}

func (s *BrowserService) Wait(ctx context.Context, sessionID, pageID, text string, timeMS int) error {
	state, cfg, err := s.pageState(sessionID, pageID)
	if err != nil {
		return err
	}
	if timeMS > 0 {
		select {
		case <-time.After(time.Duration(timeMS) * time.Millisecond):
			state.mu.Lock()
			defer state.mu.Unlock()
			s.touchPageLocked(state)
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if strings.TrimSpace(text) != "" {
		actionCtx, cancel := context.WithTimeout(ctx, cfgTimeout(cfg.ActionTimeoutSeconds))
		defer cancel()
		page := state.page.Context(actionCtx)
		err := rod.Try(func() {
			page.Timeout(cfgTimeout(cfg.ActionTimeoutSeconds)).MustElementR("*", text)
		})
		if err != nil {
			return err
		}
		s.touchPageLocked(state)
		return nil
	}
	actionCtx, cancel := context.WithTimeout(ctx, cfgTimeout(cfg.ActionTimeoutSeconds))
	defer cancel()
	page := state.page.Context(actionCtx)
	if err := page.WaitStable(300 * time.Millisecond); err != nil {
		return err
	}
	s.touchPageLocked(state)
	return nil
}

func (s *BrowserService) Screenshot(ctx context.Context, sessionID, pageID string, fullPage bool) (string, error) {
	state, cfg, err := s.pageState(sessionID, pageID)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	tempDir := s.tempDir
	now := s.now()
	s.mu.Unlock()
	actionCtx, cancel := context.WithTimeout(ctx, cfgTimeout(cfg.ActionTimeoutSeconds))
	defer cancel()
	state.mu.Lock()
	defer state.mu.Unlock()
	page := state.page.Context(actionCtx)
	data, err := page.Screenshot(fullPage, &proto.PageCaptureScreenshot{
		Format: proto.PageCaptureScreenshotFormatPng,
	})
	if err != nil {
		return "", err
	}
	dir := filepath.Join(tempDir, "browser", sessionID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, fmt.Sprintf("%s-%d.png", pageID, now.UnixNano()))
	if err := os.WriteFile(path, data, 0644); err != nil {
		return "", err
	}
	s.touchPageLocked(state)
	return filepath.ToSlash(path), nil
}

func (s *BrowserService) Find(ctx context.Context, sessionID, pageID string, locator BrowserLocator, limit int) ([]BrowserElement, error) {
	state, cfg, err := s.pageState(sessionID, pageID)
	if err != nil {
		return nil, err
	}
	actionCtx, cancel := context.WithTimeout(ctx, cfgTimeout(cfg.ActionTimeoutSeconds))
	defer cancel()
	state.mu.Lock()
	defer state.mu.Unlock()
	elements, err := s.findElementsOnPageLocked(state, state.page.Context(actionCtx), locator, limit)
	if err != nil {
		return nil, err
	}
	s.touchPageLocked(state)
	s.refreshPageInfoLocked(state)
	return elements, nil
}

func (s *BrowserService) ClickTarget(ctx context.Context, sessionID, pageID string, locator BrowserLocator) error {
	state, cfg, err := s.pageState(sessionID, pageID)
	if err != nil {
		return err
	}
	actionCtx, cancel := context.WithTimeout(ctx, cfgTimeout(cfg.ActionTimeoutSeconds))
	defer cancel()
	state.mu.Lock()
	defer state.mu.Unlock()
	page := state.page.Context(actionCtx)
	selector, err := s.resolveSelectorOnPageLocked(state, page, locator)
	if err != nil {
		return err
	}
	el, err := page.Element(selector)
	if err != nil {
		return err
	}
	if err := el.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return err
	}
	s.touchPageLocked(state)
	s.refreshPageInfoLocked(state)
	return nil
}

func (s *BrowserService) TypeTarget(ctx context.Context, sessionID, pageID string, locator BrowserLocator, text string) error {
	state, cfg, err := s.pageState(sessionID, pageID)
	if err != nil {
		return err
	}
	actionCtx, cancel := context.WithTimeout(ctx, cfgTimeout(cfg.ActionTimeoutSeconds))
	defer cancel()
	state.mu.Lock()
	defer state.mu.Unlock()
	page := state.page.Context(actionCtx)
	selector, err := s.resolveSelectorOnPageLocked(state, page, locator)
	if err != nil {
		return err
	}
	el, err := page.Element(selector)
	if err != nil {
		return err
	}
	if err := el.SelectAllText(); err == nil {
		if err := el.Input(text); err == nil {
			s.touchPageLocked(state)
			s.refreshPageInfoLocked(state)
			return nil
		}
	}
	if _, err := el.Eval(`(v) => {
if (this && this.isContentEditable) {
  this.textContent = v;
  this.dispatchEvent(new Event("input", { bubbles: true }));
  return "ok";
}
throw new Error("element does not accept text input");
}`, text); err != nil {
		return err
	}
	s.touchPageLocked(state)
	s.refreshPageInfoLocked(state)
	return nil
}

func (s *BrowserService) FillForm(ctx context.Context, sessionID, pageID string, fields []BrowserFormField) (BrowserFillFormResult, error) {
	if len(fields) == 0 {
		return BrowserFillFormResult{}, fmt.Errorf("fill_form requires at least one field")
	}
	filledTargets := make([]string, 0, len(fields))
	for _, field := range fields {
		locator := locatorFromField(field)
		if err := s.TypeTarget(ctx, sessionID, pageID, locator, field.Value); err != nil {
			return BrowserFillFormResult{}, err
		}
		target := strings.TrimSpace(field.Ref)
		if target == "" {
			target = strings.TrimSpace(field.Selector)
		}
		if target == "" {
			target = strings.TrimSpace(field.Label)
		}
		if target == "" {
			target = strings.TrimSpace(field.Placeholder)
		}
		if target == "" {
			target = strings.TrimSpace(field.Text)
		}
		filledTargets = append(filledTargets, target)
	}
	return BrowserFillFormResult{
		PageID:        pageID,
		FilledFields:  len(fields),
		FilledTargets: filledTargets,
	}, nil
}

func (s *BrowserService) UploadFiles(ctx context.Context, sessionID, pageID string, locator BrowserLocator, paths []string) error {
	if len(paths) == 0 {
		return fmt.Errorf("upload_file requires at least one path")
	}
	state, cfg, err := s.pageState(sessionID, pageID)
	if err != nil {
		return err
	}
	actionCtx, cancel := context.WithTimeout(ctx, cfgTimeout(cfg.ActionTimeoutSeconds))
	defer cancel()
	state.mu.Lock()
	defer state.mu.Unlock()
	page := state.page.Context(actionCtx)
	selector, err := s.resolveSelectorOnPageLocked(state, page, locator)
	if err != nil {
		return err
	}
	el, err := page.Element(selector)
	if err != nil {
		return err
	}
	if err := el.SetFiles(paths); err != nil {
		return err
	}
	s.touchPageLocked(state)
	s.refreshPageInfoLocked(state)
	return nil
}

func (s *BrowserService) WaitNetworkIdle(ctx context.Context, sessionID, pageID string, idleMS int) error {
	if idleMS <= 0 {
		idleMS = 500
	}
	state, cfg, err := s.pageState(sessionID, pageID)
	if err != nil {
		return err
	}
	actionCtx, cancel := context.WithTimeout(ctx, cfgTimeout(cfg.ActionTimeoutSeconds))
	defer cancel()
	state.mu.Lock()
	defer state.mu.Unlock()
	page := state.page.Context(actionCtx).Timeout(cfgTimeout(cfg.ActionTimeoutSeconds))
	wait := page.WaitRequestIdle(time.Duration(idleMS)*time.Millisecond, nil, nil, nil)
	wait()
	s.touchPageLocked(state)
	s.refreshPageInfoLocked(state)
	return nil
}

func (s *BrowserService) NetworkSnapshot(ctx context.Context, sessionID, pageID string, maxEntries int) (BrowserNetworkSnapshot, error) {
	state, cfg, err := s.pageState(sessionID, pageID)
	if err != nil {
		return BrowserNetworkSnapshot{}, err
	}
	if maxEntries <= 0 {
		maxEntries = 40
	}
	actionCtx, cancel := context.WithTimeout(ctx, cfgTimeout(cfg.ActionTimeoutSeconds))
	defer cancel()
	state.mu.Lock()
	defer state.mu.Unlock()
	page := state.page.Context(actionCtx)
	result, err := page.Eval(networkSnapshotScript(maxEntries))
	if err != nil {
		return BrowserNetworkSnapshot{}, err
	}
	var snapshot BrowserNetworkSnapshot
	if err := decodeEvalJSONString(result, &snapshot); err != nil {
		return BrowserNetworkSnapshot{}, err
	}
	snapshot.PageID = pageID
	s.touchPageLocked(state)
	s.refreshPageInfoLocked(state)
	return snapshot, nil
}

func (s *BrowserService) Download(ctx context.Context, sessionID, pageID string, locator BrowserLocator, rawURL, fileName string) (BrowserDownloadResult, error) {
	s.mu.Lock()
	allowPrivateHosts := s.cfg.AllowPrivateHosts
	tempDir := s.tempDir
	s.mu.Unlock()
	if strings.TrimSpace(rawURL) != "" {
		if _, err := validateRemoteURL(rawURL, allowPrivateHosts); err != nil {
			return BrowserDownloadResult{}, err
		}
	}
	state, cfg, err := s.pageState(sessionID, pageID)
	if err != nil {
		return BrowserDownloadResult{}, err
	}
	browser, _, err := s.browserSnapshot()
	if err != nil {
		return BrowserDownloadResult{}, err
	}
	downloadDir := filepath.Join(tempDir, "browser", sessionID, "downloads")
	if err := os.MkdirAll(downloadDir, 0755); err != nil {
		return BrowserDownloadResult{}, err
	}
	actionCtx, cancel := context.WithTimeout(ctx, cfgTimeout(cfg.ActionTimeoutSeconds))
	defer cancel()
	state.mu.Lock()
	defer state.mu.Unlock()
	page := state.page.Context(actionCtx)
	waitDownload := browser.Context(actionCtx).WaitDownload(downloadDir)
	switch {
	case strings.TrimSpace(rawURL) != "":
		if err := page.Navigate(rawURL); err != nil && !strings.Contains(err.Error(), "net::ERR_ABORTED") {
			return BrowserDownloadResult{}, err
		}
	default:
		selector, err := s.resolveSelectorOnPageLocked(state, page, locator)
		if err != nil {
			return BrowserDownloadResult{}, err
		}
		el, err := page.Element(selector)
		if err != nil {
			return BrowserDownloadResult{}, err
		}
		if href, hrefErr := el.Attribute("href"); hrefErr == nil && href != nil && strings.TrimSpace(*href) != "" {
			targetURL := strings.TrimSpace(*href)
			if base, err := urlpkg.Parse(strings.TrimSpace(state.pageInfo.URL)); err == nil {
				if rel, err := urlpkg.Parse(targetURL); err == nil {
					targetURL = base.ResolveReference(rel).String()
				}
			}
			if err := page.Navigate(targetURL); err != nil && !strings.Contains(err.Error(), "net::ERR_ABORTED") {
				return BrowserDownloadResult{}, err
			}
			rawURL = targetURL
		} else {
			if err := el.Click(proto.InputMouseButtonLeft, 1); err != nil {
				return BrowserDownloadResult{}, err
			}
		}
	}
	info := waitDownload()
	if info == nil {
		if strings.TrimSpace(rawURL) == "" {
			return BrowserDownloadResult{}, fmt.Errorf("download did not complete")
		}
		path, resolvedName, err := downloadFileViaHTTP(ctx, downloadDir, rawURL, fileName)
		if err != nil {
			return BrowserDownloadResult{}, fmt.Errorf("download did not complete")
		}
		s.touchPageLocked(state)
		s.refreshPageInfoLocked(state)
		return BrowserDownloadResult{
			PageID:                     pageID,
			ArtifactPath:               filepath.ToSlash(path),
			FileName:                   resolvedName,
			URL:                        strings.TrimSpace(rawURL),
			Kind:                       "file",
			AutoAttachInSupportedReply: true,
		}, nil
	}
	path := filepath.Join(downloadDir, info.GUID)
	targetName := sanitizeDownloadFileName(strings.TrimSpace(fileName))
	if targetName == "" {
		targetName = sanitizeDownloadFileName(strings.TrimSpace(info.SuggestedFilename))
	}
	if targetName != "" && targetName != info.GUID {
		targetPath := filepath.Join(downloadDir, targetName)
		if err := os.Rename(path, targetPath); err == nil {
			path = targetPath
		}
	}
	s.touchPageLocked(state)
	s.refreshPageInfoLocked(state)
	return BrowserDownloadResult{
		PageID:                     pageID,
		ArtifactPath:               filepath.ToSlash(path),
		FileName:                   filepath.Base(path),
		URL:                        strings.TrimSpace(rawURL),
		Kind:                       "file",
		AutoAttachInSupportedReply: true,
	}, nil
}
