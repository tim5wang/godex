package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// OCRBackend abstracts how screenshot text is extracted. godex ships two
// backends: a tesseract CLI backend (zero-dependency, available everywhere)
// and a RapidOCR backend (ONNX-based, far better Chinese/Latin accuracy,
// fully offline). The desktop tool auto-selects RapidOCR when its binary is
// present and falls back to tesseract otherwise; callers may also override
// via the ocr_backend action.
type OCRBackend interface {
	// Name identifies the backend for the status report (tesseract | rapidocr).
	Name() string
	// Languages returns the OCR language packs the backend understands
	// (e.g. eng, chi_sim). An empty list means "no language concept".
	Languages() []string
	// Available reports whether the backend can run on this machine.
	Available() bool
	// OCR runs the backend over the screenshot at path and returns detected
	// words with pixel boxes. lang, when non-empty, selects a language pack.
	OCR(ctx context.Context, imagePath, lang string) ([]DesktopOCRWord, error)
}

// newDefaultOCRBackend picks the best available OCR backend: RapidOCR first
// (better accuracy, especially for Chinese), tesseract as the universal
// fallback.
func newDefaultOCRBackend(run desktopRunner, lookPath desktopLookPath) OCRBackend {
	rapid := &RapidOCRBackend{run: run, lookPath: lookPath}
	if rapid.Available() {
		return rapid
	}
	return &TesseractBackend{run: run, lookPath: lookPath}
}

// TesseractBackend uses the tesseract CLI in TSV mode for word-level boxes.
type TesseractBackend struct {
	run      desktopRunner
	lookPath desktopLookPath
}

func (b *TesseractBackend) Name() string { return "tesseract" }

func (b *TesseractBackend) Available() bool {
	return b.hasCommand("tesseract")
}

func (b *TesseractBackend) Languages() []string {
	if !b.Available() {
		return nil
	}
	out, err := b.run(context.Background(), "tesseract", []string{"--list-langs"}, "")
	if err != nil {
		return nil
	}
	languages := make([]string, 0, 4)
	for _, line := range strings.Split(string(out), "\n") {
		lang := strings.TrimSpace(line)
		if lang == "" || strings.HasPrefix(lang, "List of available languages") || strings.HasPrefix(lang, "(") {
			continue
		}
		languages = append(languages, lang)
	}
	return languages
}

func (b *TesseractBackend) OCR(ctx context.Context, imagePath, lang string) ([]DesktopOCRWord, error) {
	if !b.Available() {
		return nil, fmt.Errorf("desktop OCR requires tesseract CLI")
	}
	args := []string{imagePath, "stdout", "--psm", "11"}
	if lang = strings.TrimSpace(lang); lang != "" {
		args = append(args, "-l", lang)
	}
	args = append(args, "tsv")
	out, err := b.run(ctx, "tesseract", args, "")
	if err != nil {
		return nil, fmt.Errorf("tesseract OCR failed: %w", err)
	}
	return parseTesseractTSV(string(out)), nil
}

func (b *TesseractBackend) hasCommand(name string) bool {
	look := b.lookPath
	if look == nil {
		look = exec.LookPath
	}
	_, err := look(name)
	return err == nil
}

// RapidOCRBackend uses the RapidOCR CLI (rapidocr, an ONNX-Runtime based OCR
// tool with excellent Chinese accuracy). It reads the JSON output mode
// (--json) that emits word boxes with normalized coordinates. The CLI is
// invoked as: rapidocr <image> --json
type RapidOCRBackend struct {
	run      desktopRunner
	lookPath desktopLookPath
	// bin is the resolved binary name; defaults to "rapidocr".
	bin string
}

func (b *RapidOCRBackend) Name() string { return "rapidocr" }

func (b *RapidOCRBackend) Available() bool {
	return b.hasCommand(b.binary())
}

func (b *RapidOCRBackend) binary() string {
	if b.bin = strings.TrimSpace(b.bin); b.bin != "" {
		return b.bin
	}
	return "rapidocr"
}

func (b *RapidOCRBackend) Languages() []string {
	// RapidOCR auto-detects language from the model bundle; report the common
	// presets so the model knows Chinese + Latin are covered.
	return []string{"auto", "ch", "en"}
}

func (b *RapidOCRBackend) OCR(ctx context.Context, imagePath, lang string) ([]DesktopOCRWord, error) {
	if !b.Available() {
		return nil, fmt.Errorf("desktop OCR requires the rapidocr CLI (pip install rapidocr) or tesseract")
	}
	args := []string{imagePath, "--json"}
	if lang = strings.TrimSpace(lang); lang != "" && lang != "auto" && lang != "ch" && lang != "en" {
		// Unknown lang: rapidocr uses bundled models; ignore unsupported langs.
		_ = lang
	}
	out, err := b.run(ctx, b.binary(), args, "")
	if err != nil {
		return nil, fmt.Errorf("rapidocr failed: %w", err)
	}
	return parseRapidOCRJSON(out), nil
}

func (b *RapidOCRBackend) hasCommand(name string) bool {
	look := b.lookPath
	if look == nil {
		look = exec.LookPath
	}
	_, err := look(name)
	return err == nil
}

// rapidOCRWord mirrors RapidOCR's JSON output entry.
type rapidOCRWord struct {
	Text       string  `json:"text"`
	Box        [][]int `json:"box"`
	Score      float64 `json:"score"`
	Confidence float64 `json:"confidence,omitempty"`
}

// parseRapidOCRJSON converts RapidOCR's JSON output into the shared
// DesktopOCRWord shape. RapidOCR box coordinates are pixel points (x,y pairs);
// we take the top-left and bottom-right corners to derive left/top/width/height.
func parseRapidOCRJSON(data []byte) []DesktopOCRWord {
	var result struct {
		Result []rapidOCRWord `json:"result"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		// Some builds emit a bare array.
		var arr []rapidOCRWord
		if err2 := json.Unmarshal(data, &arr); err2 != nil {
			return nil
		}
		result.Result = arr
	}
	words := make([]DesktopOCRWord, 0, len(result.Result))
	for _, item := range result.Result {
		if strings.TrimSpace(item.Text) == "" || len(item.Box) < 4 {
			continue
		}
		word := DesktopOCRWord{
			Text:   strings.TrimSpace(item.Text),
			Left:   item.Box[0][0],
			Top:    item.Box[0][1],
			Width:  item.Box[2][0] - item.Box[0][0],
			Height: item.Box[2][1] - item.Box[0][1],
		}
		if word.Width < 0 {
			word.Width = -word.Width
		}
		if word.Height < 0 {
			word.Height = -word.Height
		}
		word.Confidence = item.Score
		if word.Confidence <= 0 {
			word.Confidence = item.Confidence
		}
		word.LineKey = fmt.Sprintf("rapid.%d.%d", word.Top, word.Left)
		words = append(words, word)
	}
	return words
}

// DesktopAccessibilityElement is one entry in the macOS accessibility tree
// dump: the UI element's role, title/label, and screen coordinates. The model
// reads this structured text to decide what to click/type - no OCR, no vision.
type DesktopAccessibilityElement struct {
	Role   string `json:"role"`
	Title  string `json:"title,omitempty"`
	Value  string `json:"value,omitempty"`
	Left   int    `json:"left,omitempty"`
	Top    int    `json:"top,omitempty"`
	Width  int    `json:"width,omitempty"`
	Height int    `json:"height,omitempty"`
	// Enabled is false for disabled controls.
	Enabled bool `json:"enabled,omitempty"`
	// Children are nested elements (menus, scroll areas).
	Children []DesktopAccessibilityElement `json:"children,omitempty"`
}

// DesktopAccessibilityResult is the dump result. When no accessible elements
// are found (e.g. the focused app exposes none), the caller falls back to OCR.
type DesktopAccessibilityResult struct {
	App      string                        `json:"app,omitempty"`
	Elements []DesktopAccessibilityElement `json:"elements"`
	Engine   string                        `json:"engine"`
}

// macAccessibilityScript is an AppleScript that dumps the focused app's UI
// element tree as tab-separated lines: role, title, value, left, top, width,
// height, enabled. Requires accessibility permission for the host process.
func macAccessibilityScript() string {
	return `
on dumpElement(el, depth, out)
	if depth > 5 then return
	set lineParts to {}
	try
		set end of lineParts to (role of el as text)
	on error
		set end of lineParts to ""
	end try
	try
		set end of lineParts to (title of el as text)
	on error
		set end of lineParts to ""
	end try
	try
		set end of lineParts to (value of el as text)
	on error
		set end of lineParts to ""
	end try
	try
		set p to position of el
		set sz to size of el
		set end of lineParts to ((item 1 of p as integer) as text)
		set end of lineParts to ((item 2 of p as integer) as text)
		set end of lineParts to ((item 1 of sz as integer) as text)
		set end of lineParts to ((item 2 of sz as integer) as text)
	on error
		set end of lineParts to "0"
		set end of lineParts to "0"
		set end of lineParts to "0"
		set end of lineParts to "0"
	end try
	try
		set end of lineParts to ((enabled of el as boolean) as text)
	on error
		set end of lineParts to "true"
	end try
	set end of out to my join(lineParts, tab)
	try
		repeat with child in (every UI element of el)
			my dumpElement(child, depth + 1, out)
		end repeat
	end try
end dumpElement

on join(listValue, delim)
	set oldDelim to AppleScript's text item delimiters
	set AppleScript's text item delimiters to delim
	set joined to listValue as text
	set AppleScript's text item delimiters to oldDelim
	return joined
end join

on run
	set output to ""
	tell application "System Events"
		set frontProc to first application process whose frontmost is true
		set procName to name of frontProc
		set rootEl to front window of frontProc
		my dumpElement(rootEl, 0, output)
	end tell
	return procName & linefeed & output
end run`
}

// parseMacAccessibilityLines converts the tab-separated dump into structured
// elements. Each line: role\ttitle\tvalue\tleft\ttop\twidth\theight\tenabled.
func parseMacAccessibilityLines(output string) (string, []DesktopAccessibilityElement) {
	lines := strings.Split(output, "\n")
	if len(lines) == 0 {
		return "", nil
	}
	app := strings.TrimSpace(lines[0])
	elements := make([]DesktopAccessibilityElement, 0)
	for _, line := range lines[1:] {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 8 {
			continue
		}
		el := DesktopAccessibilityElement{
			Role:    strings.TrimSpace(fields[0]),
			Title:   strings.TrimSpace(fields[1]),
			Value:   strings.TrimSpace(fields[2]),
			Left:    parseDesktopInt(fields[3]),
			Top:     parseDesktopInt(fields[4]),
			Width:   parseDesktopInt(fields[5]),
			Height:  parseDesktopInt(fields[6]),
			Enabled: !strings.EqualFold(strings.TrimSpace(fields[7]), "false"),
		}
		if el.Role == "" && el.Title == "" {
			continue
		}
		elements = append(elements, el)
	}
	return app, elements
}

// dumpMacAccessibility runs the AppleScript dump and parses the result. If the
// script or parsing fails, an empty result with a hint is returned so callers
// can fall back to OCR.
func (s *DesktopService) dumpMacAccessibility(ctx context.Context) (DesktopAccessibilityResult, error) {
	out, err := s.run(ctx, "osascript", []string{"-e", macAccessibilityScript()}, "")
	if err != nil {
		return DesktopAccessibilityResult{Engine: "macos-accessibility"}, fmt.Errorf("macOS accessibility dump failed: %w", err)
	}
	app, elements := parseMacAccessibilityLines(string(out))
	return DesktopAccessibilityResult{App: app, Elements: elements, Engine: "macos-accessibility"}, nil
}

// accessibilityToWords converts an accessibility dump into the shared OCR word
// shape so find_text/click_text can locate elements by title without OCR.
// Each element's title becomes a pseudo-word whose box is the element box.
func accessibilityToWords(elements []DesktopAccessibilityElement) []DesktopOCRWord {
	words := make([]DesktopOCRWord, 0)
	var walk func([]DesktopAccessibilityElement, string)
	walk = func(items []DesktopAccessibilityElement, prefix string) {
		for _, el := range items {
			text := strings.TrimSpace(el.Title)
			if text == "" {
				text = strings.TrimSpace(el.Value)
			}
			if text != "" {
				words = append(words, DesktopOCRWord{
					Text:    text,
					Left:    el.Left,
					Top:     el.Top,
					Width:   el.Width,
					Height:  el.Height,
					LineKey: prefix + el.Role + "." + text,
				})
			}
			walk(el.Children, prefix+el.Role+".")
		}
	}
	walk(elements, "")
	return words
}

// ensureOCRBackend returns the service's configured backend, defaulting to
// auto-selection on first use.
func (s *DesktopService) ensureOCRBackend() OCRBackend {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ocrBackend == nil {
		s.ocrBackend = newDefaultOCRBackend(s.run, s.lookPath)
	}
	return s.ocrBackend
}

// SetOCRBackend overrides the OCR backend (tests and callers that want to pin
// rapidocr or a fake backend).
func (s *DesktopService) SetOCRBackend(backend OCRBackend) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ocrBackend = backend
}

var _ = os.WriteFile // keep os import if unused elsewhere
