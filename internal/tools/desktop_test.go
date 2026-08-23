package tools

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestDesktopToolSchemaIncludesCoreActions(t *testing.T) {
	tool := NewDesktopTool(NewDesktopService(t.TempDir()))
	spec := tool.Spec()
	props, ok := spec.InputSchema["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected properties map, got %#v", spec.InputSchema["properties"])
	}
	actionSchema, ok := props["action"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected action schema, got %#v", props["action"])
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
	for _, want := range []string{"status", "screenshot", "list_windows", "click", "type_text", "key", "clipboard_get", "clipboard_set", "ocr", "find_text", "click_text"} {
		if !containsString(enums, want) {
			t.Fatalf("expected desktop action %q in schema, got %v", want, enums)
		}
	}
}

func TestDesktopStatusReportsLinuxBackendDependencies(t *testing.T) {
	service := NewDesktopService(t.TempDir())
	service.osName = "linux"
	service.lookPath = func(name string) (string, error) {
		switch name {
		case "xdotool", "gnome-screenshot", "wl-copy", "wl-paste", "tesseract":
			return "/usr/bin/" + name, nil
		default:
			return "", os.ErrNotExist
		}
	}

	status := service.Status()
	if !status.Supported || status.Backend != "linux-cli+tesseract" || len(status.MissingDependencies) != 0 {
		t.Fatalf("expected supported Linux backend, got %+v", status)
	}
}

func TestDesktopStatusReportsUnknownOSUnsupported(t *testing.T) {
	service := NewDesktopService(t.TempDir())
	service.osName = "freebsd"

	status := service.Status()
	if status.Supported || !strings.Contains(status.Message, "macOS, Linux, and Windows") {
		t.Fatalf("expected unsupported OS message, got %+v", status)
	}
}

func TestDesktopScreenshotReturnsArtifactPath(t *testing.T) {
	tempDir := t.TempDir()
	service := NewDesktopService(tempDir)
	service.osName = "darwin"
	service.now = func() time.Time { return time.Date(2026, 4, 25, 10, 11, 12, 13, time.UTC) }
	var gotName string
	var gotArgs []string
	service.run = func(ctx context.Context, name string, args []string, stdin string) ([]byte, error) {
		gotName = name
		gotArgs = append([]string{}, args...)
		if len(args) == 2 {
			if err := os.WriteFile(args[1], []byte("png"), 0644); err != nil {
				t.Fatalf("write fake screenshot: %v", err)
			}
		}
		return nil, nil
	}

	result, err := service.Screenshot(context.Background())
	if err != nil {
		t.Fatalf("screenshot: %v", err)
	}
	if gotName != "screencapture" || len(gotArgs) != 2 || gotArgs[0] != "-x" {
		t.Fatalf("unexpected screencapture call %q %v", gotName, gotArgs)
	}
	if filepath.Dir(result.ArtifactPath) != filepath.Join(tempDir, "desktop", "screenshots") {
		t.Fatalf("unexpected artifact path %q", result.ArtifactPath)
	}
	if _, err := os.Stat(result.ArtifactPath); err != nil {
		t.Fatalf("expected screenshot artifact: %v", err)
	}
}

func TestDesktopActionsUseMacSystemEvents(t *testing.T) {
	service := NewDesktopService(t.TempDir())
	service.osName = "darwin"
	var calls []string
	service.run = func(ctx context.Context, name string, args []string, stdin string) ([]byte, error) {
		calls = append(calls, name+" "+strings.Join(args, " ")+" stdin="+stdin)
		return nil, nil
	}

	if _, err := service.Click(context.Background(), 12, 34); err != nil {
		t.Fatalf("click: %v", err)
	}
	if _, err := service.TypeText(context.Background(), "hello"); err != nil {
		t.Fatalf("type text: %v", err)
	}
	if _, err := service.Key(context.Background(), "enter"); err != nil {
		t.Fatalf("key: %v", err)
	}
	if _, err := service.SetClipboard(context.Background(), "clip"); err != nil {
		t.Fatalf("set clipboard: %v", err)
	}

	joined := strings.Join(calls, "\n")
	for _, want := range []string{
		`osascript -e tell application "System Events" to click at {12, 34}`,
		`osascript -e tell application "System Events" to keystroke "hello"`,
		`osascript -e tell application "System Events" to key code 36`,
		`pbcopy  stdin=clip`,
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected call containing %q, got\n%s", want, joined)
		}
	}
}

func TestDesktopListWindowsParsesSystemEventsOutput(t *testing.T) {
	service := NewDesktopService(t.TempDir())
	service.osName = "darwin"
	service.run = func(ctx context.Context, name string, args []string, stdin string) ([]byte, error) {
		return []byte("Safari\tExample Domain\nCode\tmain.go\n"), nil
	}

	result, err := service.ListWindows(context.Background())
	if err != nil {
		t.Fatalf("list windows: %v", err)
	}
	want := []DesktopWindow{{App: "Safari", Title: "Example Domain"}, {App: "Code", Title: "main.go"}}
	if !reflect.DeepEqual(result.Windows, want) {
		t.Fatalf("expected windows %+v, got %+v", want, result.Windows)
	}
}

func TestDesktopOCRParsesTesseractAndFindsText(t *testing.T) {
	tempDir := t.TempDir()
	service := NewDesktopService(tempDir)
	service.osName = "darwin"
	service.lookPath = func(name string) (string, error) {
		switch name {
		case "screencapture", "tesseract":
			return "/usr/bin/" + name, nil
		default:
			return "", os.ErrNotExist
		}
	}
	service.run = func(ctx context.Context, name string, args []string, stdin string) ([]byte, error) {
		switch name {
		case "screencapture":
			if len(args) == 2 {
				if err := os.WriteFile(args[1], []byte("png"), 0644); err != nil {
					t.Fatalf("write fake screenshot: %v", err)
				}
			}
		case "tesseract":
			return []byte(strings.Join([]string{
				"level\tpage_num\tblock_num\tpar_num\tline_num\tword_num\tleft\ttop\twidth\theight\tconf\ttext",
				"5\t1\t1\t1\t1\t1\t10\t20\t40\t12\t95\tCancel",
				"5\t1\t1\t1\t2\t1\t80\t120\t38\t14\t91\tOpen",
				"5\t1\t1\t1\t2\t2\t122\t120\t30\t14\t89\tFile",
			}, "\n")), nil
		}
		return nil, nil
	}

	ocr, err := service.OCR(context.Background())
	if err != nil {
		t.Fatalf("ocr: %v", err)
	}
	if len(ocr.Words) != 3 || len(ocr.Lines) != 2 {
		t.Fatalf("unexpected OCR result: %+v", ocr)
	}
	found, err := service.FindText(context.Background(), "open file", 10)
	if err != nil {
		t.Fatalf("find text: %v", err)
	}
	if len(found.Matches) == 0 || found.Matches[0].CenterX != 116 || found.Matches[0].CenterY != 127 {
		t.Fatalf("unexpected text matches: %+v", found.Matches)
	}
}

func TestDesktopClickTextClicksFirstOCRMatch(t *testing.T) {
	tempDir := t.TempDir()
	service := NewDesktopService(tempDir)
	service.osName = "linux"
	service.lookPath = func(name string) (string, error) {
		switch name {
		case "gnome-screenshot", "tesseract", "xdotool":
			return "/usr/bin/" + name, nil
		default:
			return "", os.ErrNotExist
		}
	}
	var calls []string
	service.run = func(ctx context.Context, name string, args []string, stdin string) ([]byte, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		switch name {
		case "gnome-screenshot":
			if len(args) == 2 {
				if err := os.WriteFile(args[1], []byte("png"), 0644); err != nil {
					t.Fatalf("write fake screenshot: %v", err)
				}
			}
		case "tesseract":
			return []byte(strings.Join([]string{
				"level\tpage_num\tblock_num\tpar_num\tline_num\tword_num\tleft\ttop\twidth\theight\tconf\ttext",
				"5\t1\t1\t1\t1\t1\t20\t30\t60\t20\t90\tContinue",
			}, "\n")), nil
		}
		return nil, nil
	}

	result, err := service.ClickText(context.Background(), "continue")
	if err != nil {
		t.Fatalf("click text: %v", err)
	}
	if result.X != 50 || result.Y != 40 || result.Target != "Continue" {
		t.Fatalf("unexpected click text result: %+v", result)
	}
	if !strings.Contains(strings.Join(calls, "\n"), "xdotool mousemove 50 40 click 1") {
		t.Fatalf("expected xdotool click call, got %v", calls)
	}
}

func TestDesktopLinuxBackendUsesSmallNativeCLIs(t *testing.T) {
	service := NewDesktopService(t.TempDir())
	service.osName = "linux"
	service.lookPath = func(name string) (string, error) {
		switch name {
		case "xdotool", "gnome-screenshot", "wl-copy", "wl-paste", "wmctrl", "tesseract":
			return "/usr/bin/" + name, nil
		default:
			return "", os.ErrNotExist
		}
	}
	var calls []string
	service.run = func(ctx context.Context, name string, args []string, stdin string) ([]byte, error) {
		calls = append(calls, name+" "+strings.Join(args, " ")+" stdin="+stdin)
		switch name {
		case "gnome-screenshot":
			if len(args) == 2 {
				if err := os.WriteFile(args[1], []byte("png"), 0644); err != nil {
					t.Fatalf("write fake screenshot: %v", err)
				}
			}
		case "wmctrl":
			return []byte("0x001  0 host Example Window\n"), nil
		case "wl-paste":
			return []byte("clipboard"), nil
		}
		return nil, nil
	}

	if _, err := service.Screenshot(context.Background()); err != nil {
		t.Fatalf("linux screenshot: %v", err)
	}
	if _, err := service.Click(context.Background(), 10, 20); err != nil {
		t.Fatalf("linux click: %v", err)
	}
	if _, err := service.TypeText(context.Background(), "hello"); err != nil {
		t.Fatalf("linux type: %v", err)
	}
	if _, err := service.Key(context.Background(), "enter"); err != nil {
		t.Fatalf("linux key: %v", err)
	}
	if got, err := service.GetClipboard(context.Background()); err != nil || got.Text != "clipboard" {
		t.Fatalf("linux get clipboard got %+v err=%v", got, err)
	}
	if _, err := service.SetClipboard(context.Background(), "clip"); err != nil {
		t.Fatalf("linux set clipboard: %v", err)
	}
	windows, err := service.ListWindows(context.Background())
	if err != nil {
		t.Fatalf("linux list windows: %v", err)
	}
	if len(windows.Windows) != 1 || windows.Windows[0].Title != "Example Window" {
		t.Fatalf("unexpected linux windows: %+v", windows)
	}

	joined := strings.Join(calls, "\n")
	for _, want := range []string{
		"gnome-screenshot -f ",
		"xdotool mousemove 10 20 click 1",
		"xdotool type -- hello",
		"xdotool key Return",
		"wl-copy  stdin=clip",
		"wmctrl -l",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected call containing %q, got\n%s", want, joined)
		}
	}
}

func TestDesktopWindowsBackendUsesPowerShell(t *testing.T) {
	service := NewDesktopService(t.TempDir())
	service.osName = "windows"
	service.lookPath = func(name string) (string, error) {
		if name == "powershell.exe" {
			return `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`, nil
		}
		return "", os.ErrNotExist
	}
	var calls []string
	service.run = func(ctx context.Context, name string, args []string, stdin string) ([]byte, error) {
		calls = append(calls, name+" "+strings.Join(args, " ")+" stdin="+stdin)
		if strings.Contains(strings.Join(args, " "), "Get-Clipboard") {
			return []byte("clip"), nil
		}
		if strings.Contains(strings.Join(args, " "), "Get-Process") {
			return []byte("notepad\tUntitled - Notepad\n"), nil
		}
		return nil, nil
	}

	if _, err := service.Click(context.Background(), 1, 2); err != nil {
		t.Fatalf("windows click: %v", err)
	}
	if _, err := service.TypeText(context.Background(), "a+b"); err != nil {
		t.Fatalf("windows type: %v", err)
	}
	if _, err := service.Key(context.Background(), "enter"); err != nil {
		t.Fatalf("windows key: %v", err)
	}
	if got, err := service.GetClipboard(context.Background()); err != nil || got.Text != "clip" {
		t.Fatalf("windows get clipboard got %+v err=%v", got, err)
	}
	if _, err := service.SetClipboard(context.Background(), "new clip"); err != nil {
		t.Fatalf("windows set clipboard: %v", err)
	}
	windows, err := service.ListWindows(context.Background())
	if err != nil {
		t.Fatalf("windows list windows: %v", err)
	}
	if len(windows.Windows) != 1 || windows.Windows[0].App != "notepad" || windows.Windows[0].Title != "Untitled - Notepad" {
		t.Fatalf("unexpected windows: %+v", windows)
	}

	joined := strings.Join(calls, "\n")
	for _, want := range []string{
		"powershell.exe -NoProfile -NonInteractive -Command",
		"[GodexMouse]::SetCursorPos(1, 2)",
		"[System.Windows.Forms.SendKeys]::SendWait('a{+}b')",
		"[System.Windows.Forms.SendKeys]::SendWait('{ENTER}')",
		"Set-Clipboard -Value ([Console]::In.ReadToEnd()) stdin=new clip",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected call containing %q, got\n%s", want, joined)
		}
	}
}

func TestDesktopScrollUsesPlatformBackends(t *testing.T) {
	// darwin: JXA CoreGraphics scroll event via osascript -l JavaScript.
	darwin := NewDesktopService(t.TempDir())
	darwin.osName = "darwin"
	var darwinCall string
	darwin.run = func(ctx context.Context, name string, args []string, stdin string) ([]byte, error) {
		darwinCall = name + " " + strings.Join(args, " ")
		return nil, nil
	}
	if _, err := darwin.Scroll(context.Background(), 3); err != nil {
		t.Fatalf("darwin scroll: %v", err)
	}
	if !strings.Contains(darwinCall, `osascript -l JavaScript -e ObjC.import('CoreGraphics')`) ||
		!strings.Contains(darwinCall, "CGEventCreateScrollWheelEvent(null, $.kCGScrollEventUnitLine, 1, 3, 0, 0)") {
		t.Fatalf("unexpected darwin scroll call %q", darwinCall)
	}

	// linux: xdotool click --repeat N 4/5.
	linux := NewDesktopService(t.TempDir())
	linux.osName = "linux"
	var linuxCalls []string
	linux.run = func(ctx context.Context, name string, args []string, stdin string) ([]byte, error) {
		linuxCalls = append(linuxCalls, name+" "+strings.Join(args, " "))
		return nil, nil
	}
	if _, err := linux.Scroll(context.Background(), -5); err != nil {
		t.Fatalf("linux scroll: %v", err)
	}
	if !strings.Contains(strings.Join(linuxCalls, "\n"), "xdotool click --repeat 5 5") {
		t.Fatalf("expected xdotool wheel-down, got %v", linuxCalls)
	}

	// windows: PowerShell mouse_event with delta = amount * 120.
	windows := NewDesktopService(t.TempDir())
	windows.osName = "windows"
	windows.lookPath = func(name string) (string, error) { return "powershell.exe", nil }
	var windowsCall string
	windows.run = func(ctx context.Context, name string, args []string, stdin string) ([]byte, error) {
		windowsCall = strings.Join(args, " ")
		return nil, nil
	}
	if _, err := windows.Scroll(context.Background(), -2); err != nil {
		t.Fatalf("windows scroll: %v", err)
	}
	if !strings.Contains(windowsCall, "mouse_event(0x0800, 0, 0, -240, [UIntPtr]::Zero)") {
		t.Fatalf("expected windows wheel delta -240, got %q", windowsCall)
	}

	// amount 0 is rejected.
	if _, err := darwin.Scroll(context.Background(), 0); err == nil {
		t.Fatal("expected scroll amount 0 to fail")
	}
}

func TestDesktopActivateWindowUsesPlatformBackends(t *testing.T) {
	darwin := NewDesktopService(t.TempDir())
	darwin.osName = "darwin"
	var darwinCall string
	darwin.run = func(ctx context.Context, name string, args []string, stdin string) ([]byte, error) {
		darwinCall = name + " " + strings.Join(args, " ")
		return nil, nil
	}
	if _, err := darwin.ActivateWindow(context.Background(), "Safari"); err != nil {
		t.Fatalf("darwin activate: %v", err)
	}
	if !strings.Contains(darwinCall, `whose name contains "Safari"`) {
		t.Fatalf("unexpected darwin activate call %q", darwinCall)
	}

	linux := NewDesktopService(t.TempDir())
	linux.osName = "linux"
	var linuxCalls []string
	linux.run = func(ctx context.Context, name string, args []string, stdin string) ([]byte, error) {
		linuxCalls = append(linuxCalls, name+" "+strings.Join(args, " "))
		return nil, nil
	}
	if _, err := linux.ActivateWindow(context.Background(), "My Window"); err != nil {
		t.Fatalf("linux activate: %v", err)
	}
	if !strings.Contains(strings.Join(linuxCalls, "\n"), `wmctrl -a My Window`) {
		t.Fatalf("expected wmctrl activate, got %v", linuxCalls)
	}

	windows := NewDesktopService(t.TempDir())
	windows.osName = "windows"
	windows.lookPath = func(name string) (string, error) { return "powershell.exe", nil }
	var windowsCall string
	windows.run = func(ctx context.Context, name string, args []string, stdin string) ([]byte, error) {
		windowsCall = strings.Join(args, " ")
		return nil, nil
	}
	if _, err := windows.ActivateWindow(context.Background(), "chrome"); err != nil {
		t.Fatalf("windows activate: %v", err)
	}
	if !strings.Contains(windowsCall, "SetForegroundWindow") || !strings.Contains(windowsCall, "chrome") {
		t.Fatalf("expected windows activate script, got %q", windowsCall)
	}

	if _, err := darwin.ActivateWindow(context.Background(), ""); err == nil {
		t.Fatal("expected empty activate_window name to fail")
	}
}

func TestDesktopToolSchemaIncludesNewActionsAndParams(t *testing.T) {
	tool := NewDesktopTool(NewDesktopService(t.TempDir()))
	spec := tool.Spec()
	props, _ := spec.InputSchema["properties"].(map[string]interface{})
	actionSchema, _ := props["action"].(map[string]interface{})
	raw, _ := actionSchema["enum"].([]interface{})
	enums := make([]string, 0, len(raw))
	for _, item := range raw {
		if value, ok := item.(string); ok {
			enums = append(enums, value)
		}
	}
	for _, want := range []string{"scroll", "activate_window"} {
		if !containsString(enums, want) {
			t.Fatalf("expected desktop action %q in schema, got %v", want, enums)
		}
	}
	for _, param := range []string{"amount", "screenshot_after", "lang"} {
		if _, ok := props[param]; !ok {
			t.Fatalf("expected desktop param %q in schema, got %v", param, props)
		}
	}
}

func TestDesktopScreenshotAfterAttachesArtifact(t *testing.T) {
	tempDir := t.TempDir()
	service := NewDesktopService(tempDir)
	service.osName = "darwin"
	service.now = func() time.Time { return time.Date(2026, 4, 25, 10, 11, 12, 13, time.UTC) }
	var calls []string
	service.run = func(ctx context.Context, name string, args []string, stdin string) ([]byte, error) {
		calls = append(calls, name)
		if name == "screencapture" {
			if err := os.WriteFile(args[1], []byte("png"), 0644); err != nil {
				t.Fatalf("write screenshot: %v", err)
			}
		}
		return nil, nil
	}
	tool := NewDesktopTool(service)
	handler := NewToolHandler()
	handler.Register(tool)
	ctx := WithSessionID(t.Context(), "desktop-session")

	result, err := handler.HandleResult(ctx, "desktop", map[string]interface{}{
		"action":           "click",
		"x":                10,
		"y":                20,
		"screenshot_after": true,
	})
	if err != nil {
		t.Fatalf("click with screenshot_after: %v", err)
	}
	// click (osascript) + screenshot (screencapture).
	if len(result.ArtifactPaths) != 1 {
		t.Fatalf("expected one artifact path, got %v", result.ArtifactPaths)
	}
	if _, err := os.Stat(filepath.FromSlash(result.ArtifactPaths[0])); err != nil {
		t.Fatalf("expected after screenshot artifact: %v", err)
	}
	joined := strings.Join(calls, ",")
	if !strings.Contains(joined, "screencapture") {
		t.Fatalf("expected screencapture after click, got %v", calls)
	}
}

func TestDesktopOCRWithLangPassesTesseractFlag(t *testing.T) {
	tempDir := t.TempDir()
	service := NewDesktopService(tempDir)
	service.osName = "darwin"
	service.lookPath = func(name string) (string, error) {
		if name == "screencapture" || name == "tesseract" {
			return "/usr/bin/" + name, nil
		}
		return "", os.ErrNotExist
	}
	var tesseractArgs []string
	service.run = func(ctx context.Context, name string, args []string, stdin string) ([]byte, error) {
		switch name {
		case "screencapture":
			if len(args) == 2 {
				if err := os.WriteFile(args[1], []byte("png"), 0644); err != nil {
					t.Fatalf("write screenshot: %v", err)
				}
			}
		case "tesseract":
			tesseractArgs = append([]string{}, args...)
			return []byte("level\tpage_num\tblock_num\tpar_num\tline_num\tword_num\tleft\ttop\twidth\theight\tconf\ttext\n5\t1\t1\t1\t1\t1\t10\t20\t40\t12\t95\t确定\n"), nil
		}
		return nil, nil
	}
	ocr, err := service.OCRWithLang(context.Background(), "chi_sim")
	if err != nil {
		t.Fatalf("ocr with lang: %v", err)
	}
	if ocr.Engine != "tesseract-tsv-chi_sim" {
		t.Fatalf("expected engine suffix with lang, got %q", ocr.Engine)
	}
	if len(ocr.Words) != 1 || ocr.Words[0].Text != "确定" {
		t.Fatalf("unexpected OCR words: %+v", ocr.Words)
	}
	foundLang := false
	for i, arg := range tesseractArgs {
		if arg == "-l" && i+1 < len(tesseractArgs) && tesseractArgs[i+1] == "chi_sim" {
			foundLang = true
		}
	}
	if !foundLang {
		t.Fatalf("expected -l chi_sim in tesseract args, got %v", tesseractArgs)
	}
}

func TestDesktopStatusReportsOCRLanguages(t *testing.T) {
	service := NewDesktopService(t.TempDir())
	service.osName = "darwin"
	service.lookPath = func(name string) (string, error) {
		if name == "tesseract" || name == "screencapture" || name == "osascript" || name == "pbpaste" || name == "pbcopy" {
			return "/usr/bin/" + name, nil
		}
		return "", os.ErrNotExist
	}
	service.run = func(ctx context.Context, name string, args []string, stdin string) ([]byte, error) {
		if name == "tesseract" && len(args) > 0 && args[0] == "--list-langs" {
			return []byte("List of available languages (3):\neng\nchi_sim\nchi_tra\n"), nil
		}
		return nil, nil
	}
	status := service.Status()
	if len(status.OCRLanguages) != 3 {
		t.Fatalf("expected 3 OCR languages, got %v", status.OCRLanguages)
	}
	for _, want := range []string{"eng", "chi_sim", "chi_tra"} {
		if !containsString(status.OCRLanguages, want) {
			t.Fatalf("expected OCR language %q in %v", want, status.OCRLanguages)
		}
	}
}

// fakeOCRBackend is a deterministic OCRBackend for tests.
type fakeOCRBackend struct{ name string }

func (f *fakeOCRBackend) Name() string        { return f.name }
func (f *fakeOCRBackend) Languages() []string { return []string{"eng", "chi_sim"} }
func (f *fakeOCRBackend) Available() bool     { return true }
func (f *fakeOCRBackend) OCR(ctx context.Context, path, lang string) ([]DesktopOCRWord, error) {
	return []DesktopOCRWord{{Text: "你好", Left: 10, Top: 20, Width: 40, Height: 12, Confidence: 0.9}}, nil
}

func TestDesktopOCRBackendAbstractionUsesConfiguredBackend(t *testing.T) {
	service := NewDesktopService(t.TempDir())
	service.SetOCRBackend(&fakeOCRBackend{name: "rapidocr"})
	// Screenshot must still be produced for the fake backend to consume.
	service.osName = "darwin"
	service.run = func(ctx context.Context, name string, args []string, stdin string) ([]byte, error) {
		if name == "screencapture" && len(args) == 2 {
			if err := os.WriteFile(args[1], []byte("png"), 0644); err != nil {
				t.Fatalf("write screenshot: %v", err)
			}
		}
		return nil, nil
	}
	ocr, err := service.OCR(context.Background())
	if err != nil {
		t.Fatalf("ocr: %v", err)
	}
	if ocr.Engine != "rapidocr-tsv" {
		t.Fatalf("expected engine rapidocr-tsv, got %q", ocr.Engine)
	}
	if len(ocr.Words) != 1 || ocr.Words[0].Text != "你好" {
		t.Fatalf("unexpected words: %+v", ocr.Words)
	}
	if service.OCRBackendName() != "rapidocr" {
		t.Fatalf("expected backend name rapidocr, got %q", service.OCRBackendName())
	}
}

func TestDefaultOCRBackendPrefersRapidOCR(t *testing.T) {
	look := func(name string) (string, error) {
		switch name {
		case "rapidocr":
			return "/usr/local/bin/rapidocr", nil
		default:
			return "", os.ErrNotExist
		}
	}
	backend := newDefaultOCRBackend(nil, look)
	if backend.Name() != "rapidocr" {
		t.Fatalf("expected rapidocr preferred when present, got %q", backend.Name())
	}

	look2 := func(name string) (string, error) {
		switch name {
		case "tesseract":
			return "/usr/bin/tesseract", nil
		default:
			return "", os.ErrNotExist
		}
	}
	backend2 := newDefaultOCRBackend(nil, look2)
	if backend2.Name() != "tesseract" {
		t.Fatalf("expected tesseract fallback, got %q", backend2.Name())
	}
}

func TestRapidOCRBackendParsesJSON(t *testing.T) {
	out := []byte(`{"result":[{"text":"确定","box":[[10,20],[50,20],[50,32],[10,32]],"score":0.95}]}`)
	words := parseRapidOCRJSON(out)
	if len(words) != 1 {
		t.Fatalf("expected 1 word, got %d: %+v", len(words), words)
	}
	word := words[0]
	if word.Text != "确定" || word.Left != 10 || word.Top != 20 || word.Width != 40 || word.Height != 12 {
		t.Fatalf("unexpected rapid word: %+v", word)
	}
	if word.Confidence != 0.95 {
		t.Fatalf("expected confidence 0.95, got %v", word.Confidence)
	}

	// Bare array output shape.
	out2 := []byte(`[{"text":"OK","box":[[0,0],[10,0],[10,8],[0,8]],"score":0.9}]`)
	if words := parseRapidOCRJSON(out2); len(words) != 1 || words[0].Text != "OK" {
		t.Fatalf("expected bare array parse, got %+v", words)
	}
}

func TestDesktopMacAccessibilityDumpParsesLines(t *testing.T) {
	service := NewDesktopService(t.TempDir())
	service.osName = "darwin"
	service.run = func(ctx context.Context, name string, args []string, stdin string) ([]byte, error) {
		if name != "osascript" {
			t.Fatalf("expected osascript, got %q", name)
		}
		return []byte("Safari\nbutton\t确定\t\t100\t200\t60\t24\ttrue\nbutton\tOpen File\t\t50\t60\t80\t20\tfalse\n"), nil
	}
	result, err := service.DumpAccessibility(context.Background())
	if err != nil {
		t.Fatalf("dump accessibility: %v", err)
	}
	if result.App != "Safari" {
		t.Fatalf("expected app Safari, got %q", result.App)
	}
	if len(result.Elements) != 2 {
		t.Fatalf("expected 2 elements, got %+v", result.Elements)
	}
	first := result.Elements[0]
	if first.Role != "button" || first.Title != "确定" || first.Left != 100 || first.Top != 200 || !first.Enabled {
		t.Fatalf("unexpected first element: %+v", first)
	}
	if result.Elements[1].Enabled {
		t.Fatalf("expected second element disabled, got %+v", result.Elements[1])
	}
}

func TestDesktopDumpAccessibilityUnsupportedOnLinux(t *testing.T) {
	service := NewDesktopService(t.TempDir())
	service.osName = "linux"
	if _, err := service.DumpAccessibility(context.Background()); err == nil {
		t.Fatal("expected dump_accessibility to fail on linux")
	}
}

func TestDesktopAccessibilityToWords(t *testing.T) {
	elements := []DesktopAccessibilityElement{
		{Role: "button", Title: "确定", Left: 100, Top: 200, Width: 60, Height: 24},
		{Role: "menu", Title: "", Children: []DesktopAccessibilityElement{
			{Role: "menu item", Title: "保存", Left: 10, Top: 10, Width: 30, Height: 14},
		}},
	}
	words := accessibilityToWords(elements)
	if len(words) != 2 {
		t.Fatalf("expected 2 words, got %d: %+v", len(words), words)
	}
	if words[0].Text != "确定" || words[0].Left != 100 || words[0].Top != 200 {
		t.Fatalf("unexpected word: %+v", words[0])
	}
	if words[1].Text != "保存" {
		t.Fatalf("expected nested child text, got %+v", words[1])
	}
}

func TestDesktopToolSchemaIncludesAccessibilityActions(t *testing.T) {
	tool := NewDesktopTool(NewDesktopService(t.TempDir()))
	spec := tool.Spec()
	props, _ := spec.InputSchema["properties"].(map[string]interface{})
	actionSchema, _ := props["action"].(map[string]interface{})
	raw, _ := actionSchema["enum"].([]interface{})
	enums := make([]string, 0, len(raw))
	for _, item := range raw {
		if value, ok := item.(string); ok {
			enums = append(enums, value)
		}
	}
	for _, want := range []string{"dump_accessibility", "ocr_backend"} {
		if !containsString(enums, want) {
			t.Fatalf("expected desktop action %q in schema, got %v", want, enums)
		}
	}
}
