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
