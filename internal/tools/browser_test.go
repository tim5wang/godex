package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/cdp"
	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/domain/automation"
)

func TestBrowserToolRequiresSessionContextForOpen(t *testing.T) {
	tool := NewBrowserTool(NewBrowserService(config.BrowserConfig{
		Enabled:              true,
		Headless:             true,
		ActionTimeoutSeconds: 10,
		IdleTimeoutSeconds:   60,
		MaxPagesPerSession:   2,
	}, t.TempDir()), t.TempDir())

	if _, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "open",
		"url":    "https://example.com",
	}); err == nil {
		t.Fatalf("expected missing session context error")
	}
}

func TestBrowserServiceStatusDoesNotBlockDuringLaunch(t *testing.T) {
	service := NewBrowserService(config.BrowserConfig{
		Enabled:              true,
		Headless:             true,
		ActionTimeoutSeconds: 1,
		IdleTimeoutSeconds:   60,
		MaxPagesPerSession:   2,
	}, t.TempDir())
	downloadStarted := make(chan struct{})
	releaseDownload := make(chan struct{})
	service.resolveBrowserPath = func(string) string { return "" }
	service.downloadBrowser = func(ctx context.Context, root string) (string, error) {
		_ = root
		close(downloadStarted)
		select {
		case <-releaseDownload:
			return "", context.Canceled
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}

	openDone := make(chan error, 1)
	go func() {
		_, err := service.Open(context.Background(), "session-1", "https://example.com")
		openDone <- err
	}()
	<-downloadStarted

	statusDone := make(chan BrowserStatus, 1)
	go func() {
		statusDone <- service.Status()
	}()
	select {
	case status := <-statusDone:
		if !status.Enabled {
			t.Fatalf("expected status to observe enabled browser service, got %+v", status)
		}
	case <-time.After(100 * time.Millisecond):
		close(releaseDownload)
		t.Fatal("Status blocked behind browser launch I/O")
	}
	close(releaseDownload)
	<-openDone
}

func TestBrowserServiceListPagesDoesNotBlockDuringLaunch(t *testing.T) {
	service := NewBrowserService(config.BrowserConfig{
		Enabled:              true,
		Headless:             true,
		ActionTimeoutSeconds: 1,
		IdleTimeoutSeconds:   60,
		MaxPagesPerSession:   2,
	}, t.TempDir())
	downloadStarted := make(chan struct{})
	releaseDownload := make(chan struct{})
	service.resolveBrowserPath = func(string) string { return "" }
	service.downloadBrowser = func(ctx context.Context, root string) (string, error) {
		_ = root
		close(downloadStarted)
		select {
		case <-releaseDownload:
			return "", context.Canceled
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}

	openDone := make(chan error, 1)
	go func() {
		_, err := service.Open(context.Background(), "session-1", "https://example.com")
		openDone <- err
	}()
	<-downloadStarted

	listDone := make(chan []BrowserPage, 1)
	go func() {
		listDone <- service.ListPages("session-1")
	}()
	select {
	case pages := <-listDone:
		if len(pages) != 0 {
			t.Fatalf("expected no pages while launch is pending, got %+v", pages)
		}
	case <-time.After(100 * time.Millisecond):
		close(releaseDownload)
		t.Fatal("ListPages blocked behind browser launch I/O")
	}
	close(releaseDownload)
	<-openDone
}

func TestNewBrowserToolSchemaIncludesEnhancedActions(t *testing.T) {
	tool := NewBrowserTool(NewBrowserService(config.BrowserConfig{
		Enabled: true,
	}, t.TempDir()), t.TempDir())
	spec := tool.Spec()
	props, ok := spec.InputSchema["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected properties map, got %#v", spec.InputSchema["properties"])
	}
	actionSchema, ok := props["action"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected action schema map, got %#v", props["action"])
	}
	enums, ok := actionSchema["enum"].([]string)
	if !ok {
		raw, okAny := actionSchema["enum"].([]interface{})
		if !okAny {
			t.Fatalf("expected action enum, got %#v", actionSchema["enum"])
		}
		enums = make([]string, 0, len(raw))
		for _, item := range raw {
			if value, ok := item.(string); ok {
				enums = append(enums, value)
			}
		}
	}
	for _, name := range []string{"find", "fill_form", "upload_file", "wait_network_idle", "network_snapshot", "download", "capture_page", "search_and_open", "handoff", "resume"} {
		if !containsString(enums, name) {
			t.Fatalf("expected enhanced browser action %q in schema, got %v", name, enums)
		}
	}
}

func TestBrowserToolHandoffRequiresSessionContext(t *testing.T) {
	tool := NewBrowserTool(NewBrowserService(config.BrowserConfig{
		Enabled:              true,
		Headless:             true,
		ActionTimeoutSeconds: 10,
		IdleTimeoutSeconds:   60,
		MaxPagesPerSession:   2,
	}, t.TempDir()), t.TempDir())

	if _, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "handoff",
		"url":    "https://example.com",
	}); err == nil {
		t.Fatalf("expected missing session context error")
	}
}

func TestBrowserServiceHandoffRequiresURLOrPage(t *testing.T) {
	service := NewBrowserService(config.BrowserConfig{
		Enabled:              true,
		Headless:             true,
		ActionTimeoutSeconds: 10,
		IdleTimeoutSeconds:   60,
		MaxPagesPerSession:   2,
	}, t.TempDir())

	_, err := service.Handoff(context.Background(), "session-1", "", "", "login required", 2000)
	if err == nil || !strings.Contains(err.Error(), "requires page_id") {
		t.Fatalf("expected handoff target error, got %v", err)
	}
}

func TestResolveBrowserUploadPathsRejectsDirectories(t *testing.T) {
	workspace := t.TempDir()
	dir := filepath.Join(workspace, "uploads")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir uploads dir: %v", err)
	}
	if _, err := resolveBrowserUploadPaths(workspace, "uploads", nil); err == nil {
		t.Fatal("expected directory upload path to be rejected")
	}
}

func TestSanitizeDownloadFileName(t *testing.T) {
	got := sanitizeDownloadFileName(`foo/bar\baz:name.pdf`)
	if got != "foo_bar_baz_name.pdf" {
		t.Fatalf("unexpected sanitized file name %q", got)
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func TestBrowserServiceSmoke(t *testing.T) {
	if os.Getenv("GODEX_BROWSER_SMOKE") == "" {
		t.Skip("set GODEX_BROWSER_SMOKE=1 to run rod-backed browser smoke test")
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html><body><h1>Hello</h1><button id="go">Go</button></body></html>`))
	}))
	defer server.Close()

	service := NewBrowserService(config.BrowserConfig{
		Enabled:              true,
		Headless:             true,
		ActionTimeoutSeconds: 15,
		IdleTimeoutSeconds:   60,
		MaxPagesPerSession:   2,
		AllowPrivateHosts:    true,
	}, t.TempDir())
	defer func() {
		service.mu.Lock()
		service.stopLocked()
		service.mu.Unlock()
	}()

	page, err := service.Open(context.Background(), "session-1", server.URL)
	if err != nil {
		t.Fatalf("open page: %v", err)
	}
	snapshot, err := service.Snapshot(context.Background(), "session-1", page.PageID, 2000)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if !strings.Contains(snapshot.Text, "Hello") {
		t.Fatalf("unexpected snapshot text: %#v", snapshot)
	}
	if err := service.Close("session-1", page.PageID); err != nil {
		t.Fatalf("close page: %v", err)
	}
}

func TestBrowserServiceEnhancedSmoke(t *testing.T) {
	if os.Getenv("GODEX_BROWSER_SMOKE") == "" {
		t.Skip("set GODEX_BROWSER_SMOKE=1 to run rod-backed browser smoke test")
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			_, _ = w.Write([]byte(`<!doctype html>
<html>
  <head><title>Search Home</title></head>
  <body>
    <h1>Demo Browser Site</h1>
    <form action="/search" method="GET">
      <label for="search">Search</label>
      <input id="search" name="q" type="search" placeholder="Search docs" />
      <button type="submit">Search</button>
    </form>
    <label for="upload">Upload file</label>
    <input id="upload" name="upload" type="file" />
    <a id="download" href="/download?name=report.txt">Download report</a>
  </body>
</html>`))
		case "/search":
			query := r.URL.Query().Get("q")
			_, _ = w.Write([]byte(fmt.Sprintf(`<!doctype html><html><head><title>Results</title></head><body><h1>Results for %s</h1></body></html>`, query)))
		case "/download":
			name := r.URL.Query().Get("name")
			if name == "" {
				name = "download.txt"
			}
			w.Header().Set("Content-Type", "text/plain")
			w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, name))
			_, _ = io.WriteString(w, "browser download payload")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	tempDir := t.TempDir()
	workspace := t.TempDir()
	uploadPath := filepath.Join(workspace, "upload.txt")
	if err := os.WriteFile(uploadPath, []byte("upload payload"), 0644); err != nil {
		t.Fatalf("write upload payload: %v", err)
	}

	service := NewBrowserService(config.BrowserConfig{
		Enabled:              true,
		Headless:             true,
		ActionTimeoutSeconds: 20,
		IdleTimeoutSeconds:   60,
		MaxPagesPerSession:   2,
		AllowPrivateHosts:    true,
	}, tempDir)
	defer func() {
		service.mu.Lock()
		service.stopLocked()
		service.mu.Unlock()
	}()

	page, err := service.Open(context.Background(), "session-1", server.URL)
	if err != nil {
		t.Fatalf("open page: %v", err)
	}

	elements, err := service.Find(context.Background(), "session-1", page.PageID, BrowserLocator{Placeholder: "Search docs"}, 5)
	if err != nil {
		t.Fatalf("find search input: %v", err)
	}
	if len(elements) == 0 {
		t.Fatal("expected at least one matching element")
	}

	fillResult, err := service.FillForm(context.Background(), "session-1", page.PageID, []BrowserFormField{{
		Ref:   elements[0].Ref,
		Value: "meituan",
	}})
	if err != nil {
		t.Fatalf("fill form: %v", err)
	}
	if fillResult.FilledFields != 1 {
		t.Fatalf("expected one filled field, got %+v", fillResult)
	}

	if value := evalString(t, service, "session-1", page.PageID, `() => {
const el = document.querySelector("#search");
return el ? el.value : "";
}`); value != "meituan" {
		t.Fatalf("expected search input value to be set, got %q", value)
	}

	network, err := service.NetworkSnapshot(context.Background(), "session-1", page.PageID, 10)
	if err != nil {
		t.Fatalf("network snapshot: %v", err)
	}
	if !strings.Contains(network.URL, server.URL) {
		t.Fatalf("expected network snapshot url to reference test server, got %+v", network)
	}
	if len(network.Entries) == 0 {
		t.Fatalf("expected at least one performance entry, got %+v", network)
	}

	searchResult, err := service.SearchAndOpen(context.Background(), "session-1", page.PageID, "", "meituan", 500, 2000)
	if err != nil {
		t.Fatalf("search and open: %v", err)
	}
	if !strings.Contains(searchResult.Snapshot.Text, "Results for meituan") {
		t.Fatalf("unexpected search result snapshot: %+v", searchResult.Snapshot)
	}

	if _, err := service.Navigate(context.Background(), "session-1", page.PageID, server.URL); err != nil {
		t.Fatalf("navigate back home: %v", err)
	}

	if err := service.UploadFiles(context.Background(), "session-1", page.PageID, BrowserLocator{Selector: "#upload"}, []string{uploadPath}); err != nil {
		t.Fatalf("upload files: %v", err)
	}
	if uploaded := evalString(t, service, "session-1", page.PageID, `() => {
const el = document.querySelector("#upload");
return el && el.files && el.files.length > 0 ? el.files[0].name : "";
}`); uploaded != "upload.txt" {
		t.Fatalf("expected uploaded file name, got %q", uploaded)
	}

	downloadResult, err := service.Download(context.Background(), "session-1", page.PageID, BrowserLocator{HrefContains: "/download"}, "", "report.txt")
	if err != nil {
		t.Fatalf("download file: %v", err)
	}
	if _, err := os.Stat(filepath.FromSlash(downloadResult.ArtifactPath)); err != nil {
		t.Fatalf("expected downloaded artifact to exist: %v", err)
	}

	captureResult, err := service.CapturePage(context.Background(), "session-1", page.PageID, "", "", 0, 500, true, 2000)
	if err != nil {
		t.Fatalf("capture page: %v", err)
	}
	if _, err := os.Stat(filepath.FromSlash(captureResult.Screenshot.ArtifactPath)); err != nil {
		t.Fatalf("expected screenshot artifact to exist: %v", err)
	}
}

func TestBrowserToolEnhancedArtifactSmoke(t *testing.T) {
	if os.Getenv("GODEX_BROWSER_SMOKE") == "" {
		t.Skip("set GODEX_BROWSER_SMOKE=1 to run rod-backed browser smoke test")
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<!doctype html><html><head><title>Capture</title></head><body><h1>Capture me</h1></body></html>`))
	}))
	defer server.Close()

	workspace := t.TempDir()
	service := NewBrowserService(config.BrowserConfig{
		Enabled:              true,
		Headless:             true,
		ActionTimeoutSeconds: 20,
		IdleTimeoutSeconds:   60,
		MaxPagesPerSession:   2,
		AllowPrivateHosts:    true,
	}, t.TempDir())
	defer func() {
		service.mu.Lock()
		service.stopLocked()
		service.mu.Unlock()
	}()
	tool := NewBrowserTool(service, workspace)
	handler := NewToolHandler()
	handler.Register(tool)

	ctx := WithSessionID(context.Background(), "session-1")
	ctx = WithSessionContext(ctx, automation.SessionContext{
		SessionID: "session-1",
	})
	result, err := handler.HandleResult(ctx, "browser", map[string]interface{}{
		"action":          "capture_page",
		"url":             server.URL,
		"network_idle_ms": 500,
		"full_page":       true,
		"max_chars":       2000,
	})
	if err != nil {
		t.Fatalf("capture page via tool handler: %v", err)
	}
	if len(result.ArtifactPaths) != 1 {
		t.Fatalf("expected one screenshot artifact path, got %+v", result.ArtifactPaths)
	}
	if _, err := os.Stat(filepath.FromSlash(result.ArtifactPaths[0])); err != nil {
		t.Fatalf("expected screenshot artifact to exist: %v", err)
	}
}

func TestBrowserServiceRealWorldSmoke(t *testing.T) {
	if os.Getenv("GODEX_BROWSER_REAL_WORLD") == "" {
		t.Skip("set GODEX_BROWSER_REAL_WORLD=1 to run external browser smoke test")
	}

	service := NewBrowserService(config.BrowserConfig{
		Enabled:              true,
		Headless:             true,
		ActionTimeoutSeconds: 25,
		IdleTimeoutSeconds:   60,
		MaxPagesPerSession:   2,
	}, t.TempDir())
	defer func() {
		service.mu.Lock()
		service.stopLocked()
		service.mu.Unlock()
	}()

	ctx := context.Background()
	page, err := service.Open(ctx, "session-real", "https://example.com")
	if err != nil {
		t.Fatalf("open example.com: %v", err)
	}

	capture, err := service.CapturePage(ctx, "session-real", page.PageID, "", "", 0, 1200, true, 2000)
	if err != nil {
		t.Fatalf("capture example.com: %v", err)
	}
	if capture.Page.PageID == "" || capture.Screenshot.ArtifactPath == "" {
		t.Fatalf("unexpected capture result: %+v", capture)
	}
	if _, err := os.Stat(filepath.FromSlash(capture.Screenshot.ArtifactPath)); err != nil {
		t.Fatalf("expected real-world screenshot artifact to exist: %v", err)
	}
	if !strings.Contains(strings.ToLower(capture.Snapshot.Text), "example domain") {
		t.Fatalf("unexpected capture snapshot text: %+v", capture.Snapshot)
	}
}

func TestBrowserServiceRealWorldSearchAndDownload(t *testing.T) {
	if os.Getenv("GODEX_BROWSER_REAL_WORLD") == "" {
		t.Skip("set GODEX_BROWSER_REAL_WORLD=1 to run external browser smoke test")
	}

	service := NewBrowserService(config.BrowserConfig{
		Enabled:              true,
		Headless:             true,
		ActionTimeoutSeconds: 30,
		IdleTimeoutSeconds:   60,
		MaxPagesPerSession:   2,
	}, t.TempDir())
	defer func() {
		service.mu.Lock()
		service.stopLocked()
		service.mu.Unlock()
	}()

	ctx := context.Background()
	page, err := service.Open(ctx, "session-real", "https://www.wikipedia.org")
	if err != nil {
		t.Fatalf("open wikipedia.org: %v", err)
	}

	search, err := service.SearchAndOpen(ctx, "session-real", page.PageID, "", "Go programming language", 1500, 3000)
	if err != nil {
		t.Fatalf("real-world search_and_open: %v", err)
	}
	combined := strings.ToLower(search.Page.Title + "\n" + search.Snapshot.Text)
	if !strings.Contains(combined, "go") {
		t.Fatalf("unexpected real-world search result: %+v", search)
	}

	download, err := service.Download(ctx, "session-real", search.Page.PageID, BrowserLocator{}, "https://www.w3.org/WAI/ER/tests/xhtml/testfiles/resources/pdf/dummy.pdf", "dummy.pdf")
	if err != nil {
		t.Fatalf("real-world direct download: %v", err)
	}
	if _, err := os.Stat(filepath.FromSlash(download.ArtifactPath)); err != nil {
		t.Fatalf("expected real-world download artifact to exist: %v", err)
	}
	if !strings.HasSuffix(strings.ToLower(download.FileName), ".pdf") {
		t.Fatalf("expected downloaded file name to look like a pdf, got %+v", download)
	}
}

func TestBrowserServiceResolveLaunchBinaryUsesLocalPathBeforeDownload(t *testing.T) {
	service := NewBrowserService(config.BrowserConfig{
		Enabled: true,
	}, t.TempDir())

	service.resolveBrowserPath = func(configured string) string {
		if configured != "" {
			t.Fatalf("expected empty configured path, got %q", configured)
		}
		return "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
	}
	service.downloadBrowser = func(context.Context, string) (string, error) {
		t.Fatal("did not expect browser download when a local browser is available")
		return "", nil
	}

	got, err := service.resolveLaunchBinaryLocked(context.Background())
	if err != nil {
		t.Fatalf("resolve launch binary: %v", err)
	}
	if got != "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" {
		t.Fatalf("unexpected browser path %q", got)
	}
}

func TestBrowserServiceResolveLaunchBinaryDownloadsIntoStableCacheDir(t *testing.T) {
	tempDir := t.TempDir()
	service := NewBrowserService(config.BrowserConfig{
		Enabled: true,
	}, tempDir)

	service.resolveBrowserPath = func(string) string { return "" }
	service.downloadBrowser = func(ctx context.Context, rootDir string) (string, error) {
		expectedRoot := filepath.Join(tempDir, "browser", "cache")
		if rootDir != expectedRoot {
			return "", errors.New("unexpected browser cache dir")
		}
		deadline, ok := ctx.Deadline()
		if !ok {
			return "", errors.New("expected browser download context deadline")
		}
		if remaining := time.Until(deadline); remaining < 9*time.Minute {
			return "", errors.New("browser download timeout is too short")
		}
		return filepath.Join(rootDir, "chromium", "chrome"), nil
	}

	launchCtx, cancel := newBrowserLaunchContext()
	defer cancel()
	got, err := service.resolveLaunchBinaryLocked(launchCtx)
	if err != nil {
		t.Fatalf("resolve launch binary: %v", err)
	}
	expected := filepath.Join(tempDir, "browser", "cache", "chromium", "chrome")
	if got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestNewBrowserLaunchContextIgnoresCanceledCaller(t *testing.T) {
	callerCtx, cancelCaller := context.WithCancel(context.Background())
	cancelCaller()
	if err := callerCtx.Err(); err == nil {
		t.Fatal("expected caller context to be canceled")
	}

	launchCtx, cancelLaunch := newBrowserLaunchContext()
	defer cancelLaunch()
	if err := launchCtx.Err(); err != nil {
		t.Fatalf("expected independent launch context, got %v", err)
	}
	deadline, ok := launchCtx.Deadline()
	if !ok {
		t.Fatal("expected launch context deadline")
	}
	if remaining := time.Until(deadline); remaining < 9*time.Minute {
		t.Fatalf("expected long launch timeout, got %s remaining", remaining)
	}
}

func TestNewLocalLauncherDoesNotPanicWithUserDataDir(t *testing.T) {
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("expected local launcher construction not to panic, got %v", recovered)
		}
	}()

	launch := newLocalLauncher(
		context.Background(),
		true,
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		t.TempDir(),
		t.TempDir(),
		"",
	)
	if launch == nil {
		t.Fatal("expected launcher instance")
	}
}

func TestBrowserServiceAutoCleansBrowserCacheWithoutDeletingArtifacts(t *testing.T) {
	tempDir := t.TempDir()
	cachePath := filepath.Join(tempDir, "browser", "user-data", "Default", "Cache", "Cache_Data", "entry")
	screenshotPath := filepath.Join(tempDir, "browser", "web-session", "page-1.png")
	if err := os.MkdirAll(filepath.Dir(cachePath), 0755); err != nil {
		t.Fatalf("mkdir cache: %v", err)
	}
	if err := os.WriteFile(cachePath, []byte("cache"), 0644); err != nil {
		t.Fatalf("write cache: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(screenshotPath), 0755); err != nil {
		t.Fatalf("mkdir screenshot: %v", err)
	}
	if err := os.WriteFile(screenshotPath, []byte("png"), 0644); err != nil {
		t.Fatalf("write screenshot: %v", err)
	}
	service := NewBrowserService(config.BrowserConfig{
		Enabled:              true,
		Headless:             true,
		ActionTimeoutSeconds: 10,
		IdleTimeoutSeconds:   60,
		MaxPagesPerSession:   3,
	}, tempDir, config.StorageConfig{BrowserCacheAutoClean: true})

	if err := service.cleanBrowserCacheIfConfiguredLocked(); err != nil {
		t.Fatalf("clean browser cache: %v", err)
	}
	if _, err := os.Stat(cachePath); !os.IsNotExist(err) {
		t.Fatalf("expected browser cache removed, got %v", err)
	}
	if _, err := os.Stat(screenshotPath); err != nil {
		t.Fatalf("expected screenshot preserved: %v", err)
	}
}

func TestBrowserServiceResumeRequiresPageIDForMultiplePages(t *testing.T) {
	service := NewBrowserService(config.BrowserConfig{
		Enabled:              true,
		Headless:             true,
		ActionTimeoutSeconds: 10,
		IdleTimeoutSeconds:   60,
		MaxPagesPerSession:   3,
	}, t.TempDir())
	service.mu.Lock()
	service.pages["session-1"] = map[string]*browserPageState{
		"page-1": {pageInfo: BrowserPage{PageID: "page-1", SessionID: "session-1"}},
		"page-2": {pageInfo: BrowserPage{PageID: "page-2", SessionID: "session-1"}},
	}
	_, err := service.pageStateForResumeLocked("session-1", "")
	service.mu.Unlock()
	if err == nil || !strings.Contains(err.Error(), "requires page_id") {
		t.Fatalf("expected page_id error, got %v", err)
	}
}

func TestConnectRodBrowserPreservesConnectedCloneState(t *testing.T) {
	base := rod.New()
	client := &fakeCDPClient{}

	browser, err := connectRodBrowser(base, time.Second, func(clone *rod.Browser) error {
		deadline, ok := clone.GetContext().Deadline()
		if !ok {
			t.Fatal("expected connect context deadline")
		}
		if time.Until(deadline) <= 0 {
			t.Fatal("expected active connect deadline")
		}
		clone.Client(client)
		return nil
	})
	if err != nil {
		t.Fatalf("connect rod browser: %v", err)
	}

	if browser == base {
		t.Fatal("expected returned browser to be a connected clone")
	}
	if _, ok := browser.GetContext().Deadline(); ok {
		t.Fatal("expected stable browser context without connection deadline")
	}
	if browser.GetContext().Err() != nil {
		t.Fatalf("expected active browser context, got %v", browser.GetContext().Err())
	}
	if _, err := browser.Call(browser.GetContext(), "", "Target.getTargets", map[string]string{}); err != nil {
		t.Fatalf("expected browser client to be preserved, got %v", err)
	}
	if client.lastCtx == nil {
		t.Fatal("expected fake client call context to be captured")
	}
}

type fakeCDPClient struct {
	lastCtx context.Context
}

func (f *fakeCDPClient) Event() <-chan *cdp.Event {
	ch := make(chan *cdp.Event)
	close(ch)
	return ch
}

func (f *fakeCDPClient) Call(ctx context.Context, sessionID, method string, params interface{}) ([]byte, error) {
	f.lastCtx = ctx
	return []byte(`{}`), nil
}

func evalString(t *testing.T, service *BrowserService, sessionID, pageID, script string) string {
	t.Helper()

	service.mu.Lock()
	state, err := service.pageStateLocked(sessionID, pageID)
	service.mu.Unlock()
	if err != nil {
		t.Fatalf("resolve page state: %v", err)
	}
	result, err := state.page.Eval(script)
	if err != nil {
		t.Fatalf("eval script: %v", err)
	}
	var value string
	if err := json.Unmarshal([]byte(result.Value.String()), &value); err != nil {
		value = result.Value.Str()
	}
	return value
}
