package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/platform/storagegc"
)

func (s *BrowserService) Close(sessionID, pageID string) error {
	s.mu.Lock()
	state, err := s.pageStateLocked(sessionID, pageID)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	s.mu.Unlock()
	state.mu.Lock()
	if err := state.page.Close(); err != nil {
		state.mu.Unlock()
		return err
	}
	state.mu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.pages[sessionID], pageID)
	if len(s.pages[sessionID]) == 0 {
		delete(s.pages, sessionID)
	}
	return nil
}

func (s *BrowserService) start(ctx context.Context) error {
	s.launchMu.Lock()
	defer s.launchMu.Unlock()

	s.mu.Lock()
	if s.browser != nil {
		s.mu.Unlock()
		return nil
	}
	cfg := s.cfg
	s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if cfg.CDPURL != "" {
		browser, err := connectRodBrowser(
			rod.New().ControlURL(cfg.CDPURL),
			cfgTimeout(cfg.ActionTimeoutSeconds),
			func(browser *rod.Browser) error { return browser.Connect() },
		)
		if err != nil {
			return fmt.Errorf("connect browser: %w", err)
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.browser != nil {
			_ = browser.Close()
			return nil
		}
		s.browser = browser
		return nil
	}
	launchCtx, cancel := newBrowserLaunchContext()
	defer cancel()
	launch, controlURL, err := s.launchLocalBrowser(launchCtx, cfg)
	if err != nil {
		return fmt.Errorf("launch browser: %w", err)
	}
	browser, err := connectRodBrowser(
		rod.New().ControlURL(controlURL),
		cfgTimeout(cfg.ActionTimeoutSeconds),
		func(browser *rod.Browser) error { return browser.Connect() },
	)
	if err != nil {
		launch.Kill()
		launch.Cleanup()
		return fmt.Errorf("connect browser: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.browser != nil {
		_ = browser.Close()
		launch.Kill()
		launch.Cleanup()
		return nil
	}
	s.launcher = launch
	s.browser = browser
	return nil
}

func (s *BrowserService) startLocked(ctx context.Context) error {
	if s.browser != nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	cfg := s.cfg
	if cfg.CDPURL != "" {
		browser, err := connectRodBrowser(
			rod.New().ControlURL(cfg.CDPURL),
			cfgTimeout(cfg.ActionTimeoutSeconds),
			func(browser *rod.Browser) error { return browser.Connect() },
		)
		if err != nil {
			return fmt.Errorf("connect browser: %w", err)
		}
		s.browser = browser
		return nil
	}
	launchCtx, cancel := newBrowserLaunchContext()
	defer cancel()
	launch, controlURL, err := s.launchLocalBrowser(launchCtx, cfg)
	if err != nil {
		return fmt.Errorf("launch browser: %w", err)
	}
	browser, err := connectRodBrowser(
		rod.New().ControlURL(controlURL),
		cfgTimeout(cfg.ActionTimeoutSeconds),
		func(browser *rod.Browser) error { return browser.Connect() },
	)
	if err != nil {
		launch.Kill()
		launch.Cleanup()
		return fmt.Errorf("connect browser: %w", err)
	}
	s.launcher = launch
	s.browser = browser
	return nil
}

func (s *BrowserService) launchLocalBrowser(ctx context.Context, cfg config.BrowserConfig) (*launcher.Launcher, string, error) {
	binPath, err := s.resolveLaunchBinary(ctx, cfg)
	if err != nil {
		return nil, "", err
	}
	if userDataDir, err := s.ensureBrowserDir("user-data"); err != nil {
		return nil, "", err
	} else if workingDir, err := s.ensureBrowserDir("work"); err != nil {
		return nil, "", err
	} else {
		if err := s.cleanBrowserCacheIfConfigured(); err != nil {
			return nil, "", err
		}
		launch := newLocalLauncher(ctx, cfg.Headless, binPath, userDataDir, workingDir)
		controlURL, err := launch.Launch()
		if err != nil {
			return nil, "", err
		}
		return launch, controlURL, nil
	}
}

func newLocalLauncher(ctx context.Context, headless bool, binPath, userDataDir, workingDir string) *launcher.Launcher {
	launch := launcher.New().
		Context(ctx).
		Leakless(true).
		Headless(headless)
	if strings.TrimSpace(binPath) != "" {
		launch = launch.Bin(binPath)
	}
	if strings.TrimSpace(userDataDir) != "" {
		launch = launch.UserDataDir(userDataDir)
	}
	if strings.TrimSpace(workingDir) != "" {
		launch = launch.WorkingDir(workingDir)
	}
	launch = launch.Set("disable-component-update").Set("disable-background-networking").Set("disable-features", "OptimizationHints,OptimizationGuideModelDownloading,Translate")
	return launch
}

func connectRodBrowser(base *rod.Browser, timeout time.Duration, connect func(*rod.Browser) error) (*rod.Browser, error) {
	connectCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	connected := base.Context(connectCtx)
	if err := connect(connected); err != nil {
		return nil, err
	}
	return connected.Context(context.Background()), nil
}

func (s *BrowserService) resolveLaunchBinary(ctx context.Context, cfg config.BrowserConfig) (string, error) {
	if s.resolveBrowserPath != nil {
		if path := strings.TrimSpace(s.resolveBrowserPath(cfg.BrowserPath)); path != "" {
			return path, nil
		}
	}
	if s.downloadBrowser == nil {
		return "", nil
	}
	cacheDir, err := s.ensureBrowserDir("cache")
	if err != nil {
		return "", err
	}
	binPath, err := s.downloadBrowser(ctx, cacheDir)
	if err != nil {
		return "", fmt.Errorf("prepare browser binary: %w", err)
	}
	return strings.TrimSpace(binPath), nil
}

func (s *BrowserService) resolveLaunchBinaryLocked(ctx context.Context) (string, error) {
	return s.resolveLaunchBinary(ctx, s.cfg)
}

func (s *BrowserService) ensureBrowserDir(name string) (string, error) {
	s.mu.Lock()
	root := s.tempDir
	persistent := s.cfg.PersistentProfile
	stateDir := s.stateDir
	s.mu.Unlock()
	// Persistent profile: keep the browser profile (cookies, sessions,
	// logins) in the durable state dir so it survives restarts instead of a
	// throwaway temp dir. Other caches (work, screenshot) stay in temp.
	if name == "user-data" && persistent && strings.TrimSpace(stateDir) != "" {
		root = stateDir
	}
	if strings.TrimSpace(root) == "" {
		return "", nil
	}
	dir := filepath.Join(root, "browser", name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	return dir, nil
}

func (s *BrowserService) cleanBrowserCacheIfConfigured() error {
	if !s.storage.BrowserCacheAutoClean {
		return nil
	}
	_, err := storagegc.CleanBrowserCache(storagegc.Options{
		TempDir: s.tempDir,
		DryRun:  false,
		Now:     s.now(),
	})
	return err
}

func (s *BrowserService) cleanBrowserCacheIfConfiguredLocked() error {
	return s.cleanBrowserCacheIfConfigured()
}

func newBrowserLaunchContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), browserLaunchTimeout)
}

func defaultDownloadBrowser(ctx context.Context, rootDir string) (string, error) {
	browser := launcher.NewBrowser()
	browser.Context = ctx
	if strings.TrimSpace(rootDir) != "" {
		browser.RootDir = rootDir
	}
	return browser.Get()
}

func (s *BrowserService) stopLocked() {
	for sessionID, pages := range s.pages {
		for pageID, state := range pages {
			_ = state.page.Close()
			delete(pages, pageID)
		}
		delete(s.pages, sessionID)
	}
	if s.browser != nil {
		_ = s.browser.Close()
		s.browser = nil
	}
	if s.launcher != nil {
		s.launcher.Kill()
		s.launcher.Cleanup()
		s.launcher = nil
	}
}

func (s *BrowserService) pageStateLocked(sessionID, pageID string) (*browserPageState, error) {
	if !s.cfg.Enabled {
		return nil, fmt.Errorf("browser is disabled in tools.browser.enabled")
	}
	s.cleanupExpiredLocked()
	sessionPages := s.pages[sessionID]
	if sessionPages == nil {
		return nil, fmt.Errorf("no browser pages for this session")
	}
	state := sessionPages[pageID]
	if state == nil {
		return nil, fmt.Errorf("unknown page_id %q", pageID)
	}
	return state, nil
}

func (s *BrowserService) pageState(sessionID, pageID string) (*browserPageState, config.BrowserConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.pageStateLocked(sessionID, pageID)
	if err != nil {
		return nil, config.BrowserConfig{}, err
	}
	return state, s.cfg, nil
}

func (s *BrowserService) browserSnapshot() (*rod.Browser, config.BrowserConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.cfg.Enabled {
		return nil, config.BrowserConfig{}, fmt.Errorf("browser is disabled in tools.browser.enabled")
	}
	if s.browser == nil {
		return nil, config.BrowserConfig{}, fmt.Errorf("browser is not running")
	}
	return s.browser, s.cfg, nil
}

func (s *BrowserService) touchPage(state *browserPageState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.touchPageLocked(state)
}

func (s *BrowserService) touchPageLocked(state *browserPageState) {
	state.pageInfo.LastUsed = s.now()
}

func (s *BrowserService) refreshPageInfoLocked(state *browserPageState) {
	info, err := state.page.Info()
	if err != nil || info == nil {
		return
	}
	state.pageInfo.URL = info.URL
	state.pageInfo.Title = info.Title
}

func (s *BrowserService) nextPageIDLocked() string {
	s.counter++
	return fmt.Sprintf("page-%d", s.counter)
}

func (s *BrowserService) cleanupExpiredLocked() {
	if s.cfg.IdleTimeoutSeconds <= 0 {
		return
	}
	threshold := s.now().Add(-time.Duration(s.cfg.IdleTimeoutSeconds) * time.Second)
	for sessionID, pages := range s.pages {
		for pageID, state := range pages {
			if state.pageInfo.LastUsed.After(threshold) {
				continue
			}
			_ = state.page.Close()
			delete(pages, pageID)
		}
		if len(pages) == 0 {
			delete(s.pages, sessionID)
		}
	}
}

func (s *BrowserService) resolveSelectorLocked(state *browserPageState, locator BrowserLocator) (string, error) {
	return s.resolveSelectorOnPageLocked(state, state.page, locator)
}

func (s *BrowserService) resolveSelectorOnPageLocked(state *browserPageState, page *rod.Page, locator BrowserLocator) (string, error) {
	locator.Ref = strings.TrimSpace(locator.Ref)
	if locator.Ref != "" {
		selector, ok := state.refs[locator.Ref]
		if !ok {
			return "", fmt.Errorf("unknown browser ref %q; take a fresh snapshot or use selector/text/label", locator.Ref)
		}
		return selector, nil
	}
	elements, err := s.findElementsOnPageLocked(state, page, locator, 1)
	if err != nil {
		return "", err
	}
	if len(elements) == 0 {
		return "", fmt.Errorf("no browser element matched the requested locator")
	}
	return strings.TrimSpace(elements[0].Selector), nil
}

func (s *BrowserService) findElementsLocked(state *browserPageState, locator BrowserLocator, limit int) ([]BrowserElement, error) {
	return s.findElementsOnPageLocked(state, state.page, locator, limit)
}

func (s *BrowserService) findElementsOnPageLocked(state *browserPageState, page *rod.Page, locator BrowserLocator, limit int) ([]BrowserElement, error) {
	if limit <= 0 {
		limit = 10
	}
	result, err := page.Eval(findElementsScript(locator, limit))
	if err != nil {
		return nil, err
	}
	var payload browserFindPayload
	if err := decodeEvalJSONString(result, &payload); err != nil {
		return nil, err
	}
	if strings.TrimSpace(payload.Error) != "" {
		return nil, errors.New(strings.TrimSpace(payload.Error))
	}
	for i := range payload.Elements {
		if strings.TrimSpace(payload.Elements[i].Selector) == "" {
			continue
		}
		ref := s.nextElementRefLocked(state)
		payload.Elements[i].Ref = ref
		state.refs[ref] = payload.Elements[i].Selector
	}
	return payload.Elements, nil
}

func (s *BrowserService) findSearchInputSelectorLocked(state *browserPageState) (string, error) {
	return s.findSearchInputSelectorOnPageLocked(state, state.page)
}

func (s *BrowserService) findSearchInputSelectorOnPageLocked(state *browserPageState, page *rod.Page) (string, error) {
	result, err := page.Eval(searchInputScript())
	if err != nil {
		return "", err
	}
	var payload browserFindPayload
	if err := decodeEvalJSONString(result, &payload); err != nil {
		return "", err
	}
	if strings.TrimSpace(payload.Error) != "" {
		return "", errors.New(strings.TrimSpace(payload.Error))
	}
	if strings.TrimSpace(payload.Selector) == "" {
		return "", fmt.Errorf("no visible search input found on the page")
	}
	return strings.TrimSpace(payload.Selector), nil
}

func (s *BrowserService) nextElementRefLocked(state *browserPageState) string {
	base := len(state.refs) + 1
	for i := base; ; i++ {
		ref := fmt.Sprintf("l%d", i)
		if _, exists := state.refs[ref]; !exists {
			return ref
		}
	}
}
