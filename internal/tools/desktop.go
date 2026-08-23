package tools

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

type desktopArgs struct {
	Action          string `json:"action"`
	X               int    `json:"x,omitempty"`
	Y               int    `json:"y,omitempty"`
	Text            string `json:"text,omitempty"`
	Key             string `json:"key,omitempty"`
	MaxResults      int    `json:"max_results,omitempty"`
	Amount          int    `json:"amount,omitempty"`
	ScreenshotAfter bool   `json:"screenshot_after,omitempty"`
	Lang            string `json:"lang,omitempty"`
}

type DesktopStatus struct {
	Supported           bool     `json:"supported"`
	OS                  string   `json:"os"`
	Backend             string   `json:"backend,omitempty"`
	Actions             []string `json:"actions,omitempty"`
	MissingDependencies []string `json:"missing_dependencies,omitempty"`
	OCRLanguages        []string `json:"ocr_languages,omitempty"`
	Message             string   `json:"message,omitempty"`
}

type DesktopScreenshotResult struct {
	ArtifactPath               string    `json:"artifact_path"`
	Kind                       string    `json:"kind"`
	CreatedAt                  time.Time `json:"created_at"`
	AutoAttachInSupportedReply bool      `json:"auto_attach_in_supported_replies,omitempty"`
}

type DesktopActionResult struct {
	Status     string                   `json:"status"`
	Action     string                   `json:"action"`
	X          int                      `json:"x,omitempty"`
	Y          int                      `json:"y,omitempty"`
	Target     string                   `json:"target,omitempty"`
	Amount     int                      `json:"amount,omitempty"`
	Screenshot *DesktopScreenshotResult `json:"screenshot,omitempty"`
}

type DesktopClipboardResult struct {
	Text string `json:"text,omitempty"`
}

type DesktopWindow struct {
	App   string `json:"app"`
	Title string `json:"title,omitempty"`
}

type DesktopWindowsResult struct {
	Windows []DesktopWindow `json:"windows"`
}

type DesktopOCRWord struct {
	Text       string  `json:"text"`
	Left       int     `json:"left"`
	Top        int     `json:"top"`
	Width      int     `json:"width"`
	Height     int     `json:"height"`
	Confidence float64 `json:"confidence,omitempty"`
	LineKey    string  `json:"line_key,omitempty"`
}

type DesktopOCRLine struct {
	Text       string           `json:"text"`
	Left       int              `json:"left"`
	Top        int              `json:"top"`
	Width      int              `json:"width"`
	Height     int              `json:"height"`
	Words      []DesktopOCRWord `json:"words,omitempty"`
	Confidence float64          `json:"confidence,omitempty"`
}

type DesktopOCRResult struct {
	Screenshot DesktopScreenshotResult `json:"screenshot"`
	Engine     string                  `json:"engine"`
	Words      []DesktopOCRWord        `json:"words"`
	Lines      []DesktopOCRLine        `json:"lines"`
}

type DesktopTextMatch struct {
	Text       string  `json:"text"`
	Left       int     `json:"left"`
	Top        int     `json:"top"`
	Width      int     `json:"width"`
	Height     int     `json:"height"`
	CenterX    int     `json:"center_x"`
	CenterY    int     `json:"center_y"`
	Confidence float64 `json:"confidence,omitempty"`
}

type DesktopFindTextResult struct {
	Query      string                  `json:"query"`
	Screenshot DesktopScreenshotResult `json:"screenshot"`
	Matches    []DesktopTextMatch      `json:"matches"`
}

type desktopRunner func(ctx context.Context, name string, args []string, stdin string) ([]byte, error)
type desktopLookPath func(name string) (string, error)

type DesktopService struct {
	tempDir  string
	osName   string
	now      func() time.Time
	run      desktopRunner
	lookPath desktopLookPath

	mu         sync.Mutex
	ocrBackend OCRBackend
}

func NewDesktopService(tempDir string) *DesktopService {
	service := &DesktopService{
		tempDir:  tempDir,
		osName:   runtime.GOOS,
		now:      time.Now,
		lookPath: exec.LookPath,
	}
	service.run = defaultDesktopRunner
	return service
}

func NewDesktopTool(service *DesktopService) Tool {
	return NewTypedTool(NewToolSpec("desktop", "Use the local desktop UI for controlled automation: status, screenshot, list_windows, click, type_text, key, scroll, activate_window, clipboard_get, clipboard_set, ocr, find_text, and click_text. Prefer browser automation for web pages; use desktop only for OS dialogs, native apps, file pickers, and cross-window workflows.", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"description": "status | screenshot | list_windows | click | type_text | key | scroll | activate_window | clipboard_get | clipboard_set | ocr | find_text | click_text | dump_accessibility | ocr_backend",
				"enum":        []string{"status", "screenshot", "list_windows", "click", "type_text", "key", "scroll", "activate_window", "clipboard_get", "clipboard_set", "ocr", "find_text", "click_text", "dump_accessibility", "ocr_backend"},
			},
			"x": map[string]interface{}{
				"type":        "integer",
				"description": "Screen x coordinate for click",
			},
			"y": map[string]interface{}{
				"type":        "integer",
				"description": "Screen y coordinate for click",
			},
			"text": map[string]interface{}{
				"type":        "string",
				"description": "Text to type, set on the clipboard, find on screen, click by OCR text, or the app/window name for activate_window",
			},
			"key": map[string]interface{}{
				"type":        "string",
				"description": "Named key to press. Supported names include enter, tab, escape, space, delete, left, right, up, down, home, end.",
			},
			"amount": map[string]interface{}{
				"type":        "integer",
				"description": "Scroll amount in wheel ticks: positive scrolls up, negative scrolls down (default 1).",
			},
			"screenshot_after": map[string]interface{}{
				"type":        "boolean",
				"description": "Take a screenshot after the action and attach it, so you can verify the action took effect. Default false.",
			},
			"lang": map[string]interface{}{
				"type":        "string",
				"description": "OCR language pack for tesseract (e.g. eng, chi_sim, chi_tra). Check the status action's ocr_languages for what is installed.",
			},
			"max_results": map[string]interface{}{
				"type":        "integer",
				"description": "Maximum OCR text matches to return. Defaults to 10.",
			},
		},
		"required": []string{"action"},
	}, nil), func(ctx context.Context, args desktopArgs) (ToolResult, error) {
		if service == nil {
			return ToolResult{}, fmt.Errorf("desktop service is unavailable")
		}
		action := strings.TrimSpace(args.Action)
		var (
			payload any
			err     error
		)
		switch action {
		case "status":
			payload = service.Status()
		case "screenshot":
			payload, err = service.Screenshot(ctx)
		case "list_windows":
			payload, err = service.ListWindows(ctx)
		case "click":
			payload, err = service.Click(ctx, args.X, args.Y)
		case "type_text":
			payload, err = service.TypeText(ctx, args.Text)
		case "key":
			payload, err = service.Key(ctx, args.Key)
		case "scroll":
			payload, err = service.Scroll(ctx, args.Amount)
		case "activate_window":
			payload, err = service.ActivateWindow(ctx, args.Text)
		case "clipboard_get":
			payload, err = service.GetClipboard(ctx)
		case "clipboard_set":
			payload, err = service.SetClipboard(ctx, args.Text)
		case "ocr":
			payload, err = service.OCRWithLang(ctx, args.Lang)
		case "find_text":
			payload, err = service.FindTextWithLang(ctx, args.Text, args.MaxResults, args.Lang)
		case "click_text":
			payload, err = service.ClickTextWithLang(ctx, args.Text, args.Lang)
		case "dump_accessibility":
			payload, err = service.DumpAccessibility(ctx)
		case "ocr_backend":
			payload = map[string]string{"backend": service.OCRBackendName()}
		default:
			return ToolResult{}, fmt.Errorf("unknown desktop action %q", action)
		}
		if err != nil {
			return ToolResult{}, err
		}
		// Optional screenshot-after: capture the screen post-action so the
		// model can verify the action took effect.
		if args.ScreenshotAfter {
			if actionResult, ok := payload.(DesktopActionResult); ok && actionResult.Screenshot == nil {
				if shot, shotErr := service.Screenshot(ctx); shotErr == nil {
					actionResult.Screenshot = &shot
					payload = actionResult
				}
			}
		}
		result := ToolResult{Structured: payload}
		if screenshot, ok := payload.(DesktopScreenshotResult); ok && strings.TrimSpace(screenshot.ArtifactPath) != "" {
			result.ArtifactPaths = []string{strings.TrimSpace(screenshot.ArtifactPath)}
		}
		if ocr, ok := payload.(DesktopOCRResult); ok && strings.TrimSpace(ocr.Screenshot.ArtifactPath) != "" {
			result.ArtifactPaths = []string{strings.TrimSpace(ocr.Screenshot.ArtifactPath)}
		}
		if found, ok := payload.(DesktopFindTextResult); ok && strings.TrimSpace(found.Screenshot.ArtifactPath) != "" {
			result.ArtifactPaths = []string{strings.TrimSpace(found.Screenshot.ArtifactPath)}
		}
		if actionResult, ok := payload.(DesktopActionResult); ok && actionResult.Screenshot != nil && strings.TrimSpace(actionResult.Screenshot.ArtifactPath) != "" {
			result.ArtifactPaths = append(result.ArtifactPaths, strings.TrimSpace(actionResult.Screenshot.ArtifactPath))
		}
		return result, nil
	})
}

func (s *DesktopService) Status() DesktopStatus {
	osName := s.currentOS()
	status := DesktopStatus{
		Supported: true,
		OS:        osName,
		Actions:   []string{"screenshot", "list_windows", "click", "type_text", "key", "clipboard_get", "clipboard_set", "ocr", "find_text", "click_text", "scroll", "activate_window"},
	}
	status.OCRLanguages = s.ensureOCRBackend().Languages()
	switch osName {
	case "darwin":
		status.Backend = "macos-system-commands+tesseract"
		status.MissingDependencies = s.missingGroups([][]string{{"screencapture"}, {"osascript"}, {"pbpaste"}, {"pbcopy"}, {"tesseract"}})
	case "linux":
		status.Backend = "linux-cli+tesseract"
		status.MissingDependencies = s.missingGroups([][]string{{"xdotool"}, {"gnome-screenshot", "scrot", "import"}, {"wl-copy", "xclip", "xsel"}, {"wl-paste", "xclip", "xsel"}, {"tesseract"}})
		if len(status.MissingDependencies) > 0 {
			status.Message = "Install xdotool, a screenshot CLI, clipboard CLI, and tesseract for full desktop automation."
		}
	case "windows":
		status.Backend = "windows-powershell+tesseract"
		status.MissingDependencies = s.missingGroups([][]string{{"powershell.exe", "powershell", "pwsh"}, {"tesseract"}})
	default:
		status.Supported = false
		status.Backend = ""
		status.Actions = nil
		status.Message = "desktop automation v1 supports macOS, Linux, and Windows"
	}
	return status
}

// OCRBackendName returns the active OCR backend name (tesseract | rapidocr),
// for the status report.
func (s *DesktopService) OCRBackendName() string {
	return s.ensureOCRBackend().Name()
}

func (s *DesktopService) Screenshot(ctx context.Context) (DesktopScreenshotResult, error) {
	dir := filepath.Join(s.effectiveTempDir(), "desktop", "screenshots")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return DesktopScreenshotResult{}, err
	}
	now := s.now()
	path := filepath.Join(dir, "desktop-"+now.Format("20060102-150405.000000000")+".png")
	switch s.currentOS() {
	case "darwin":
		if _, err := s.run(ctx, "screencapture", []string{"-x", path}, ""); err != nil {
			return DesktopScreenshotResult{}, fmt.Errorf("capture macOS desktop screenshot: %w", err)
		}
	case "linux":
		if err := s.linuxScreenshot(ctx, path); err != nil {
			return DesktopScreenshotResult{}, err
		}
	case "windows":
		if err := s.windowsPowerShell(ctx, windowsScreenshotScript(path), ""); err != nil {
			return DesktopScreenshotResult{}, fmt.Errorf("capture Windows desktop screenshot: %w", err)
		}
	default:
		return DesktopScreenshotResult{}, s.unsupported()
	}
	return DesktopScreenshotResult{
		ArtifactPath:               path,
		Kind:                       "image",
		CreatedAt:                  now,
		AutoAttachInSupportedReply: true,
	}, nil
}

func (s *DesktopService) Click(ctx context.Context, x, y int) (DesktopActionResult, error) {
	if x < 0 || y < 0 {
		return DesktopActionResult{}, fmt.Errorf("desktop click requires non-negative x/y coordinates")
	}
	switch s.currentOS() {
	case "darwin":
		script := fmt.Sprintf(`tell application "System Events" to click at {%d, %d}`, x, y)
		if _, err := s.run(ctx, "osascript", []string{"-e", script}, ""); err != nil {
			return DesktopActionResult{}, fmt.Errorf("macOS desktop click failed: %w", err)
		}
	case "linux":
		if _, err := s.run(ctx, "xdotool", []string{"mousemove", fmt.Sprint(x), fmt.Sprint(y), "click", "1"}, ""); err != nil {
			return DesktopActionResult{}, fmt.Errorf("Linux desktop click failed; install xdotool and ensure an X11 session is available: %w", err)
		}
	case "windows":
		if err := s.windowsPowerShell(ctx, windowsClickScript(x, y), ""); err != nil {
			return DesktopActionResult{}, fmt.Errorf("Windows desktop click failed: %w", err)
		}
	default:
		return DesktopActionResult{}, s.unsupported()
	}
	return DesktopActionResult{Status: "ok", Action: "click"}, nil
}

func (s *DesktopService) TypeText(ctx context.Context, text string) (DesktopActionResult, error) {
	if text == "" {
		return DesktopActionResult{}, fmt.Errorf("desktop type_text requires text")
	}
	switch s.currentOS() {
	case "darwin":
		script := `tell application "System Events" to keystroke ` + appleScriptString(text)
		if _, err := s.run(ctx, "osascript", []string{"-e", script}, ""); err != nil {
			return DesktopActionResult{}, fmt.Errorf("macOS desktop type_text failed: %w", err)
		}
	case "linux":
		if _, err := s.run(ctx, "xdotool", []string{"type", "--", text}, ""); err != nil {
			return DesktopActionResult{}, fmt.Errorf("Linux desktop type_text failed; install xdotool and ensure an X11 session is available: %w", err)
		}
	case "windows":
		if err := s.windowsPowerShell(ctx, windowsSendKeysScript(escapeWindowsSendKeys(text)), ""); err != nil {
			return DesktopActionResult{}, fmt.Errorf("Windows desktop type_text failed: %w", err)
		}
	default:
		return DesktopActionResult{}, s.unsupported()
	}
	return DesktopActionResult{Status: "ok", Action: "type_text"}, nil
}

func (s *DesktopService) Key(ctx context.Context, key string) (DesktopActionResult, error) {
	normalized := strings.ToLower(strings.TrimSpace(key))
	switch s.currentOS() {
	case "darwin":
		code, ok := macKeyCodes[normalized]
		if !ok {
			return DesktopActionResult{}, fmt.Errorf("unsupported macOS desktop key %q", key)
		}
		script := fmt.Sprintf(`tell application "System Events" to key code %d`, code)
		if _, err := s.run(ctx, "osascript", []string{"-e", script}, ""); err != nil {
			return DesktopActionResult{}, fmt.Errorf("macOS desktop key failed: %w", err)
		}
	case "linux":
		name, ok := linuxKeyNames[normalized]
		if !ok {
			return DesktopActionResult{}, fmt.Errorf("unsupported Linux desktop key %q", key)
		}
		if _, err := s.run(ctx, "xdotool", []string{"key", name}, ""); err != nil {
			return DesktopActionResult{}, fmt.Errorf("Linux desktop key failed; install xdotool and ensure an X11 session is available: %w", err)
		}
	case "windows":
		name, ok := windowsKeyNames[normalized]
		if !ok {
			return DesktopActionResult{}, fmt.Errorf("unsupported Windows desktop key %q", key)
		}
		if err := s.windowsPowerShell(ctx, windowsSendKeysScript(name), ""); err != nil {
			return DesktopActionResult{}, fmt.Errorf("Windows desktop key failed: %w", err)
		}
	default:
		return DesktopActionResult{}, s.unsupported()
	}
	return DesktopActionResult{Status: "ok", Action: "key"}, nil
}

// Scroll rotates the mouse wheel. amount > 0 scrolls up (content moves down),
// amount < 0 scrolls down; each unit is one wheel tick.
func (s *DesktopService) Scroll(ctx context.Context, amount int) (DesktopActionResult, error) {
	if amount == 0 {
		return DesktopActionResult{}, fmt.Errorf("desktop scroll requires a non-zero amount")
	}
	switch s.currentOS() {
	case "darwin":
		script := macScrollJXA(amount)
		if _, err := s.run(ctx, "osascript", []string{"-l", "JavaScript", "-e", script}, ""); err != nil {
			return DesktopActionResult{}, fmt.Errorf("macOS desktop scroll failed: %w", err)
		}
	case "linux":
		// xdotool click 4 = wheel up, click 5 = wheel down.
		button := "4"
		repeat := amount
		if amount < 0 {
			button = "5"
			repeat = -amount
		}
		if repeat > 100 {
			repeat = 100
		}
		if _, err := s.run(ctx, "xdotool", []string{"click", "--repeat", fmt.Sprint(repeat), button}, ""); err != nil {
			return DesktopActionResult{}, fmt.Errorf("Linux desktop scroll failed; install xdotool and ensure an X11 session is available: %w", err)
		}
	case "windows":
		if err := s.windowsPowerShell(ctx, windowsScrollScript(amount), ""); err != nil {
			return DesktopActionResult{}, fmt.Errorf("Windows desktop scroll failed: %w", err)
		}
	default:
		return DesktopActionResult{}, s.unsupported()
	}
	return DesktopActionResult{Status: "ok", Action: "scroll", Amount: amount}, nil
}

// ActivateWindow brings a window to the foreground. name is matched against
// the app/process name on macOS and Windows, and the window title on Linux.
func (s *DesktopService) ActivateWindow(ctx context.Context, name string) (DesktopActionResult, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return DesktopActionResult{}, fmt.Errorf("desktop activate_window requires a window or app name")
	}
	switch s.currentOS() {
	case "darwin":
		script := fmt.Sprintf(`tell application "System Events"
	set frontmost of first process whose name contains %s to true
end tell`, appleScriptString(name))
		if _, err := s.run(ctx, "osascript", []string{"-e", script}, ""); err != nil {
			return DesktopActionResult{}, fmt.Errorf("macOS desktop activate_window failed: %w", err)
		}
	case "linux":
		if _, err := s.run(ctx, "wmctrl", []string{"-a", name}, ""); err != nil {
			return DesktopActionResult{}, fmt.Errorf("Linux desktop activate_window failed; install wmctrl and ensure an EWMH-compatible window manager: %w", err)
		}
	case "windows":
		if err := s.windowsPowerShell(ctx, windowsActivateWindowScript(name), ""); err != nil {
			return DesktopActionResult{}, fmt.Errorf("Windows desktop activate_window failed: %w", err)
		}
	default:
		return DesktopActionResult{}, s.unsupported()
	}
	return DesktopActionResult{Status: "ok", Action: "activate_window", Target: name}, nil
}

func (s *DesktopService) GetClipboard(ctx context.Context) (DesktopClipboardResult, error) {
	var (
		out []byte
		err error
	)
	switch s.currentOS() {
	case "darwin":
		out, err = s.run(ctx, "pbpaste", nil, "")
	case "linux":
		out, err = s.linuxClipboardGet(ctx)
	case "windows":
		out, err = s.windowsPowerShellOutput(ctx, "Get-Clipboard -Raw")
	default:
		return DesktopClipboardResult{}, s.unsupported()
	}
	if err != nil {
		return DesktopClipboardResult{}, fmt.Errorf("get desktop clipboard: %w", err)
	}
	return DesktopClipboardResult{Text: string(out)}, nil
}

func (s *DesktopService) SetClipboard(ctx context.Context, text string) (DesktopActionResult, error) {
	var err error
	switch s.currentOS() {
	case "darwin":
		_, err = s.run(ctx, "pbcopy", nil, text)
	case "linux":
		err = s.linuxClipboardSet(ctx, text)
	case "windows":
		err = s.windowsPowerShell(ctx, "Set-Clipboard -Value ([Console]::In.ReadToEnd())", text)
	default:
		return DesktopActionResult{}, s.unsupported()
	}
	if err != nil {
		return DesktopActionResult{}, fmt.Errorf("set desktop clipboard: %w", err)
	}
	return DesktopActionResult{Status: "ok", Action: "clipboard_set"}, nil
}

func (s *DesktopService) ListWindows(ctx context.Context) (DesktopWindowsResult, error) {
	var (
		out []byte
		err error
	)
	switch s.currentOS() {
	case "darwin":
		script := `
tell application "System Events"
	set output to ""
	repeat with proc in (application processes whose background only is false)
		set procName to name of proc
		repeat with win in windows of proc
			set output to output & procName & tab & (name of win as text) & linefeed
		end repeat
	end repeat
	return output
end tell`
		out, err = s.run(ctx, "osascript", []string{"-e", script}, "")
	case "linux":
		out, err = s.run(ctx, "wmctrl", []string{"-l"}, "")
		if err == nil {
			return DesktopWindowsResult{Windows: parseLinuxWindows(string(out))}, nil
		}
		return DesktopWindowsResult{}, fmt.Errorf("list Linux desktop windows failed; install wmctrl: %w", err)
	case "windows":
		out, err = s.windowsPowerShellOutput(ctx, `Get-Process | Where-Object { $_.MainWindowTitle } | ForEach-Object { "$($_.ProcessName)`+"`t"+`$($_.MainWindowTitle)" }`)
	default:
		return DesktopWindowsResult{}, s.unsupported()
	}
	if err != nil {
		return DesktopWindowsResult{}, fmt.Errorf("list desktop windows: %w", err)
	}
	return DesktopWindowsResult{Windows: parseDesktopWindows(string(out))}, nil
}

func (s *DesktopService) OCR(ctx context.Context) (DesktopOCRResult, error) {
	return s.OCRWithLang(ctx, "")
}

func (s *DesktopService) OCRWithLang(ctx context.Context, lang string) (DesktopOCRResult, error) {
	screenshot, err := s.Screenshot(ctx)
	if err != nil {
		return DesktopOCRResult{}, err
	}
	backend := s.ensureOCRBackend()
	words, err := backend.OCR(ctx, screenshot.ArtifactPath, lang)
	if err != nil {
		return DesktopOCRResult{}, err
	}
	return DesktopOCRResult{
		Screenshot: screenshot,
		Engine:     backend.Name() + "-tsv" + tesseractLangSuffix(lang),
		Words:      words,
		Lines:      buildOCRLines(words),
	}, nil
}

func (s *DesktopService) FindText(ctx context.Context, query string, maxResults int) (DesktopFindTextResult, error) {
	return s.FindTextWithLang(ctx, query, maxResults, "")
}

func (s *DesktopService) FindTextWithLang(ctx context.Context, query string, maxResults int, lang string) (DesktopFindTextResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return DesktopFindTextResult{}, fmt.Errorf("desktop find_text requires text")
	}
	ocr, err := s.OCRWithLang(ctx, lang)
	if err != nil {
		return DesktopFindTextResult{}, err
	}
	return DesktopFindTextResult{
		Query:      query,
		Screenshot: ocr.Screenshot,
		Matches:    findOCRTextMatches(ocr.Lines, ocr.Words, query, boundedDesktopMaxResults(maxResults)),
	}, nil
}

func (s *DesktopService) ClickText(ctx context.Context, query string) (DesktopActionResult, error) {
	return s.ClickTextWithLang(ctx, query, "")
}

func (s *DesktopService) ClickTextWithLang(ctx context.Context, query, lang string) (DesktopActionResult, error) {
	found, err := s.FindTextWithLang(ctx, query, 1, lang)
	if err != nil {
		return DesktopActionResult{}, err
	}
	if len(found.Matches) == 0 {
		return DesktopActionResult{}, fmt.Errorf("desktop click_text found no match for %q", query)
	}
	match := found.Matches[0]
	if _, err := s.Click(ctx, match.CenterX, match.CenterY); err != nil {
		return DesktopActionResult{}, err
	}
	return DesktopActionResult{Status: "ok", Action: "click_text", X: match.CenterX, Y: match.CenterY, Target: match.Text}, nil
}

// DumpAccessibility reads the focused app's UI element tree (role, title,
// coordinates) on macOS - a non-vision, non-OCR observation channel. The
// structured elements let the model locate controls precisely; click/type can
// then target the element's coordinates. On non-macOS platforms it returns an
// explicit unsupported error so callers fall back to OCR.
func (s *DesktopService) DumpAccessibility(ctx context.Context) (DesktopAccessibilityResult, error) {
	if s.currentOS() != "darwin" {
		return DesktopAccessibilityResult{Engine: "macos-accessibility"}, fmt.Errorf("dump_accessibility is only supported on macOS (System Events accessibility tree)")
	}
	return s.dumpMacAccessibility(ctx)
}

func (s *DesktopService) ocrImage(ctx context.Context, path string) ([]DesktopOCRWord, error) {
	return s.ensureOCRBackend().OCR(ctx, path, "")
}

func (s *DesktopService) ocrImageWithLang(ctx context.Context, path, lang string) ([]DesktopOCRWord, error) {
	return s.ensureOCRBackend().OCR(ctx, path, lang)
}

func tesseractLangSuffix(lang string) string {
	if lang = strings.TrimSpace(lang); lang != "" {
		return "-" + lang
	}
	return ""
}

func (s *DesktopService) linuxScreenshot(ctx context.Context, path string) error {
	switch {
	case s.hasCommand("gnome-screenshot"):
		if _, err := s.run(ctx, "gnome-screenshot", []string{"-f", path}, ""); err != nil {
			return fmt.Errorf("capture Linux desktop screenshot with gnome-screenshot: %w", err)
		}
	case s.hasCommand("scrot"):
		if _, err := s.run(ctx, "scrot", []string{path}, ""); err != nil {
			return fmt.Errorf("capture Linux desktop screenshot with scrot: %w", err)
		}
	case s.hasCommand("import"):
		if _, err := s.run(ctx, "import", []string{"-window", "root", path}, ""); err != nil {
			return fmt.Errorf("capture Linux desktop screenshot with ImageMagick import: %w", err)
		}
	default:
		return fmt.Errorf("capture Linux desktop screenshot requires gnome-screenshot, scrot, or ImageMagick import")
	}
	return nil
}

func (s *DesktopService) linuxClipboardGet(ctx context.Context) ([]byte, error) {
	switch {
	case s.hasCommand("wl-paste"):
		return s.run(ctx, "wl-paste", nil, "")
	case s.hasCommand("xclip"):
		return s.run(ctx, "xclip", []string{"-selection", "clipboard", "-o"}, "")
	case s.hasCommand("xsel"):
		return s.run(ctx, "xsel", []string{"--clipboard", "--output"}, "")
	default:
		return nil, fmt.Errorf("Linux clipboard read requires wl-paste, xclip, or xsel")
	}
}

func (s *DesktopService) linuxClipboardSet(ctx context.Context, text string) error {
	switch {
	case s.hasCommand("wl-copy"):
		_, err := s.run(ctx, "wl-copy", nil, text)
		return err
	case s.hasCommand("xclip"):
		_, err := s.run(ctx, "xclip", []string{"-selection", "clipboard", "-i"}, text)
		return err
	case s.hasCommand("xsel"):
		_, err := s.run(ctx, "xsel", []string{"--clipboard", "--input"}, text)
		return err
	default:
		return fmt.Errorf("Linux clipboard write requires wl-copy, xclip, or xsel")
	}
}

func (s *DesktopService) windowsPowerShell(ctx context.Context, script, stdin string) error {
	_, err := s.windowsPowerShellOutputWithStdin(ctx, script, stdin)
	return err
}

func (s *DesktopService) windowsPowerShellOutput(ctx context.Context, script string) ([]byte, error) {
	return s.windowsPowerShellOutputWithStdin(ctx, script, "")
}

func (s *DesktopService) windowsPowerShellOutputWithStdin(ctx context.Context, script, stdin string) ([]byte, error) {
	bin, err := s.findPowerShell()
	if err != nil {
		return nil, err
	}
	return s.run(ctx, bin, []string{"-NoProfile", "-NonInteractive", "-Command", script}, stdin)
}

func (s *DesktopService) findPowerShell() (string, error) {
	for _, candidate := range []string{"powershell.exe", "powershell", "pwsh"} {
		if s.hasCommand(candidate) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("Windows desktop automation requires PowerShell")
}

func (s *DesktopService) currentOS() string {
	if s.osName == "" {
		s.osName = runtime.GOOS
	}
	return s.osName
}

func (s *DesktopService) unsupported() error {
	return fmt.Errorf("desktop automation v1 supports macOS, Linux, and Windows; current OS is %s", s.currentOS())
}

func (s *DesktopService) hasCommand(name string) bool {
	if s.lookPath == nil {
		s.lookPath = exec.LookPath
	}
	_, err := s.lookPath(name)
	return err == nil
}

func (s *DesktopService) missingGroups(groups [][]string) []string {
	missing := make([]string, 0)
	for _, group := range groups {
		found := false
		for _, name := range group {
			if s.hasCommand(name) {
				found = true
				break
			}
		}
		if !found {
			missing = append(missing, strings.Join(group, "|"))
		}
	}
	return missing
}

func (s *DesktopService) effectiveTempDir() string {
	if strings.TrimSpace(s.tempDir) != "" {
		return s.tempDir
	}
	return filepath.Join(os.TempDir(), "godex")
}

func defaultDesktopRunner(ctx context.Context, name string, args []string, stdin string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return out, fmt.Errorf("%w: %s", err, msg)
		}
		return out, err
	}
	return out, nil
}

func appleScriptString(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	parts := strings.Split(value, "\n")
	for idx, part := range parts {
		part = strings.ReplaceAll(part, `\`, `\\`)
		part = strings.ReplaceAll(part, `"`, `\"`)
		parts[idx] = `"` + part + `"`
	}
	return strings.Join(parts, " & linefeed & ")
}

func parseDesktopWindows(output string) []DesktopWindow {
	windows := make([]DesktopWindow, 0)
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		window := DesktopWindow{App: strings.TrimSpace(parts[0])}
		if len(parts) > 1 {
			window.Title = strings.TrimSpace(parts[1])
		}
		if window.App != "" {
			windows = append(windows, window)
		}
	}
	return windows
}

func parseLinuxWindows(output string) []DesktopWindow {
	windows := make([]DesktopWindow, 0)
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		title := strings.TrimSpace(strings.Join(fields[3:], " "))
		if title != "" {
			windows = append(windows, DesktopWindow{App: "window", Title: title})
		}
	}
	return windows
}

func parseTesseractTSV(output string) []DesktopOCRWord {
	words := make([]DesktopOCRWord, 0)
	for idx, line := range strings.Split(output, "\n") {
		line = strings.TrimRight(line, "\r")
		if idx == 0 || strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.SplitN(line, "\t", 12)
		if len(fields) < 12 {
			continue
		}
		text := strings.TrimSpace(fields[11])
		if text == "" {
			continue
		}
		conf, _ := strconv.ParseFloat(strings.TrimSpace(fields[10]), 64)
		if conf < 0 {
			conf = 0
		}
		word := DesktopOCRWord{
			Text:       text,
			Left:       parseDesktopInt(fields[6]),
			Top:        parseDesktopInt(fields[7]),
			Width:      parseDesktopInt(fields[8]),
			Height:     parseDesktopInt(fields[9]),
			Confidence: conf,
			LineKey:    strings.Join(fields[1:5], "."),
		}
		words = append(words, word)
	}
	return words
}

func buildOCRLines(words []DesktopOCRWord) []DesktopOCRLine {
	orderedKeys := make([]string, 0)
	grouped := make(map[string][]DesktopOCRWord)
	for _, word := range words {
		key := word.LineKey
		if key == "" {
			key = fmt.Sprintf("%d.%d", word.Top, word.Left)
		}
		if _, ok := grouped[key]; !ok {
			orderedKeys = append(orderedKeys, key)
		}
		grouped[key] = append(grouped[key], word)
	}
	lines := make([]DesktopOCRLine, 0, len(orderedKeys))
	for _, key := range orderedKeys {
		lineWords := grouped[key]
		if len(lineWords) == 0 {
			continue
		}
		line := DesktopOCRLine{
			Words: lineWords,
			Left:  lineWords[0].Left,
			Top:   lineWords[0].Top,
		}
		right := line.Left + lineWords[0].Width
		bottom := line.Top + lineWords[0].Height
		var confidence float64
		parts := make([]string, 0, len(lineWords))
		for _, word := range lineWords {
			parts = append(parts, word.Text)
			if word.Left < line.Left {
				line.Left = word.Left
			}
			if word.Top < line.Top {
				line.Top = word.Top
			}
			if word.Left+word.Width > right {
				right = word.Left + word.Width
			}
			if word.Top+word.Height > bottom {
				bottom = word.Top + word.Height
			}
			confidence += word.Confidence
		}
		line.Text = strings.Join(parts, " ")
		line.Width = right - line.Left
		line.Height = bottom - line.Top
		line.Confidence = confidence / float64(len(lineWords))
		lines = append(lines, line)
	}
	return lines
}

func findOCRTextMatches(lines []DesktopOCRLine, words []DesktopOCRWord, query string, maxResults int) []DesktopTextMatch {
	query = normalizeOCRText(query)
	if query == "" {
		return nil
	}
	matches := make([]DesktopTextMatch, 0)
	addMatch := func(text string, left, top, width, height int, confidence float64) {
		if len(matches) >= maxResults {
			return
		}
		matches = append(matches, DesktopTextMatch{
			Text:       text,
			Left:       left,
			Top:        top,
			Width:      width,
			Height:     height,
			CenterX:    left + width/2,
			CenterY:    top + height/2,
			Confidence: confidence,
		})
	}
	for _, line := range lines {
		if strings.Contains(normalizeOCRText(line.Text), query) {
			addMatch(line.Text, line.Left, line.Top, line.Width, line.Height, line.Confidence)
		}
	}
	for _, word := range words {
		if len(matches) >= maxResults {
			break
		}
		if strings.Contains(normalizeOCRText(word.Text), query) {
			addMatch(word.Text, word.Left, word.Top, word.Width, word.Height, word.Confidence)
		}
	}
	return matches
}

func boundedDesktopMaxResults(value int) int {
	if value <= 0 {
		return 10
	}
	if value > 50 {
		return 50
	}
	return value
}

func normalizeOCRText(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(value)), " "))
}

func parseDesktopInt(value string) int {
	parsed, _ := strconv.Atoi(strings.TrimSpace(value))
	return parsed
}

func windowsScreenshotScript(path string) string {
	return `
Add-Type -AssemblyName System.Windows.Forms
Add-Type -AssemblyName System.Drawing
$bounds = [System.Windows.Forms.Screen]::PrimaryScreen.Bounds
$bitmap = New-Object System.Drawing.Bitmap $bounds.Width, $bounds.Height
$graphics = [System.Drawing.Graphics]::FromImage($bitmap)
$graphics.CopyFromScreen($bounds.Location, [System.Drawing.Point]::Empty, $bounds.Size)
$bitmap.Save(` + powerShellSingleQuoted(path) + `, [System.Drawing.Imaging.ImageFormat]::Png)
$graphics.Dispose()
$bitmap.Dispose()`
}

func windowsClickScript(x, y int) string {
	return fmt.Sprintf(`
Add-Type @"
using System;
using System.Runtime.InteropServices;
public class GodexMouse {
	[DllImport("user32.dll")]
	public static extern bool SetCursorPos(int X, int Y);
	[DllImport("user32.dll")]
	public static extern void mouse_event(uint flags, uint dx, uint dy, uint data, UIntPtr extra);
}
"@
[GodexMouse]::SetCursorPos(%d, %d) | Out-Null
[GodexMouse]::mouse_event(0x0002, 0, 0, 0, [UIntPtr]::Zero)
[GodexMouse]::mouse_event(0x0004, 0, 0, 0, [UIntPtr]::Zero)`, x, y)
}

func windowsSendKeysScript(keys string) string {
	return `Add-Type -AssemblyName System.Windows.Forms
[System.Windows.Forms.SendKeys]::SendWait(` + powerShellSingleQuoted(keys) + `)`
}

// macScrollJXA emits a JavaScript-for-Automation snippet that posts a
// CoreGraphics scroll-wheel event. amount > 0 scrolls up, amount < 0 down;
// each unit is one line. Requires accessibility permission for the host app.
func macScrollJXA(amount int) string {
	return fmt.Sprintf(`ObjC.import('CoreGraphics');
const ev = $.CGEventCreateScrollWheelEvent(null, $.kCGScrollEventUnitLine, 1, %d, 0, 0);
$.CGEventPost($.kCGHIDEventTap, ev);`, amount)
}

// windowsScrollScript emits a PowerShell snippet that posts a mouse-wheel
// event via user32 mouse_event (WHEEL = 0x0800). delta > 0 scrolls up, delta
// < 0 down; each unit is one wheel tick (120 per notch).
func windowsScrollScript(amount int) string {
	delta := amount * 120
	return fmt.Sprintf(`
Add-Type @"
using System;
using System.Runtime.InteropServices;
public class GodexScroll {
	[DllImport("user32.dll")]
	public static extern void mouse_event(uint flags, uint dx, uint dy, int data, UIntPtr extra);
}
"@
[GodexScroll]::mouse_event(0x0800, 0, 0, %d, [UIntPtr]::Zero)`, delta)
}

// windowsActivateWindowScript emits a PowerShell snippet that brings the first
// window whose process name matches name to the foreground. name is embedded
// with PowerShell single-quote escaping to prevent injection.
func windowsActivateWindowScript(name string) string {
	escaped := powerShellSingleQuoted(name) // "'name'"
	pattern := "'*" + strings.ReplaceAll(name, "'", "''") + "*'"
	return fmt.Sprintf(`
Add-Type @"
using System;
using System.Runtime.InteropServices;
public class GodexFocus {
	[DllImport("user32.dll")]
	public static extern bool SetForegroundWindow(IntPtr hWnd);
}
"@
$proc = Get-Process | Where-Object { $_.MainWindowTitle -and $_.ProcessName -like %s } | Select-Object -First 1
if ($proc) { [GodexFocus]::SetForegroundWindow($proc.MainWindowHandle) | Out-Null }
if (-not $proc) { throw "no window found for process matching %s" }`, pattern, escaped)
}

func powerShellSingleQuoted(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func escapeWindowsSendKeys(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	var builder strings.Builder
	for _, r := range text {
		switch r {
		case '\n':
			builder.WriteString("{ENTER}")
		case '+', '^', '%', '~', '(', ')', '[', ']', '{', '}':
			builder.WriteString("{")
			builder.WriteRune(r)
			builder.WriteString("}")
		default:
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

var macKeyCodes = map[string]int{
	"enter":     36,
	"return":    36,
	"tab":       48,
	"space":     49,
	"escape":    53,
	"esc":       53,
	"delete":    51,
	"backspace": 51,
	"left":      123,
	"right":     124,
	"down":      125,
	"up":        126,
	"home":      115,
	"end":       119,
	"page_up":   116,
	"pageup":    116,
	"page_down": 121,
	"pagedown":  121,
}

var linuxKeyNames = map[string]string{
	"enter":     "Return",
	"return":    "Return",
	"tab":       "Tab",
	"space":     "space",
	"escape":    "Escape",
	"esc":       "Escape",
	"delete":    "Delete",
	"backspace": "BackSpace",
	"left":      "Left",
	"right":     "Right",
	"down":      "Down",
	"up":        "Up",
	"home":      "Home",
	"end":       "End",
	"page_up":   "Page_Up",
	"pageup":    "Page_Up",
	"page_down": "Page_Down",
	"pagedown":  "Page_Down",
}

var windowsKeyNames = map[string]string{
	"enter":     "{ENTER}",
	"return":    "{ENTER}",
	"tab":       "{TAB}",
	"space":     " ",
	"escape":    "{ESC}",
	"esc":       "{ESC}",
	"delete":    "{DEL}",
	"backspace": "{BACKSPACE}",
	"left":      "{LEFT}",
	"right":     "{RIGHT}",
	"down":      "{DOWN}",
	"up":        "{UP}",
	"home":      "{HOME}",
	"end":       "{END}",
	"page_up":   "{PGUP}",
	"pageup":    "{PGUP}",
	"page_down": "{PGDN}",
	"pagedown":  "{PGDN}",
}
