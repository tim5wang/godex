package tooling

import (
	"context"
	"os"
	"strings"
	"testing"
)

// TestOutputCaptureStripsANSIColorCodes reproduces the macOS colored `ls`
// output seen in the bug report: directory entries wrapped in
// \x1b[1m\x1b[36m...\x1b[39;49m\x1b[0m must come back as plain names.
func TestOutputCaptureStripsANSIColorCodes(t *testing.T) {
	raw := "Makefile\nREADME.md\n" +
		"\x1b[1m\x1b[36mcmd\x1b[39;49m\x1b[0m\n" +
		"\x1b[1m\x1b[36minternal\x1b[39;49m\x1b[0m\n" +
		"\x1b[31mgodex\x1b[39;49m\x1b[0m\n"
	want := "Makefile\nREADME.md\ncmd\ninternal\ngodex\n"

	c := NewOutputCapture(CommandOutputOptions{SpillDir: t.TempDir()})
	if _, err := c.Write([]byte(raw)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	_ = c.Close()
	got := c.Result().Text
	if got != want {
		t.Fatalf("colored ls output not stripped:\n got %q\nwant %q", got, want)
	}
}

// TestOutputCaptureStripsANSIAcrossChunks verifies that an escape sequence
// split across two writes (a pipe read can split anywhere) is still removed.
func TestOutputCaptureStripsANSIAcrossChunks(t *testing.T) {
	c := NewOutputCapture(CommandOutputOptions{SpillDir: t.TempDir()})
	// Split "\x1b[36mcmd\x1b[0m" into awkward chunks.
	chunks := [][]byte{
		[]byte("a\x1b["),
		[]byte("36mcm"),
		[]byte("d\x1b[0m"),
		[]byte("\x1b]0;title"),
		[]byte("\x07b"),
	}
	for _, ch := range chunks {
		if _, err := c.Write(ch); err != nil {
			t.Fatalf("Write(%q): %v", ch, err)
		}
	}
	_ = c.Close()
	got := c.Result().Text
	want := "acmdb"
	if got != want {
		t.Fatalf("split escape sequences not stripped: got %q, want %q", got, want)
	}
}

// TestOutputCaptureKeepsPlainText verifies non-escape output is untouched.
func TestOutputCaptureKeepsPlainText(t *testing.T) {
	raw := "hello world\nline two\n"
	c := NewOutputCapture(CommandOutputOptions{SpillDir: t.TempDir()})
	if _, err := c.Write([]byte(raw)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	_ = c.Close()
	if got := c.Result().Text; got != raw {
		t.Fatalf("plain text modified: got %q, want %q", got, raw)
	}
}

// TestRunShellStripsANSIColor verifies the full bash-tool path strips ANSI
// codes even when the command force-emits them (as macOS ls does under
// CLICOLOR_FORCE=1).
func TestRunShellStripsANSIColor(t *testing.T) {
	executor := NewWorkspaceExecutor(t.TempDir())
	out, err := executor.RunShell(context.Background(),
		`printf '\033[1m\033[36mcolored\033[39;49m\033[0m plain'`)
	if err != nil {
		t.Fatalf("RunShell: %v", err)
	}
	if strings.Contains(out, "\x1b[") {
		t.Fatalf("ANSI escape leaked into shell output: %q", out)
	}
	if !strings.Contains(out, "colored plain") {
		t.Fatalf("expected plain text in output, got %q", out)
	}
}

// TestOutputCaptureSpillFileStripped verifies spilled full output is also
// stripped of ANSI codes, not just the in-memory preview.
func TestOutputCaptureSpillFileStripped(t *testing.T) {
	dir := t.TempDir()
	opts := CommandOutputOptions{
		PreviewBytes: 16,
		SpillDir:     dir,
		SpillPrefix:  "ansi-",
	}
	payload := "\x1b[36mAAAA\x1b[0m \x1b[31mBBBB\x1b[0m \x1b[1mCCCC\x1b[0m DDDD EEEE FFFF GGGG"
	c := NewOutputCapture(opts)
	if _, err := c.Write([]byte(payload)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	_ = c.Close()
	res := c.Result()
	if strings.Contains(res.Text, "\x1b[") {
		t.Fatalf("preview leaked ANSI: %q", res.Text)
	}
	if !res.Truncated || res.FilePath == "" {
		t.Fatalf("expected spill file, got truncated=%v path=%q", res.Truncated, res.FilePath)
	}
	spilled, err := os.ReadFile(res.FilePath)
	if err != nil {
		t.Fatalf("read spill: %v", err)
	}
	if strings.Contains(string(spilled), "\x1b[") {
		t.Fatalf("spill file leaked ANSI: %q", spilled)
	}
	if strings.Contains(string(spilled), "AAAA BBBB CCCC") == false {
		t.Fatalf("spill file missing payload text: %q", spilled)
	}
}

// TestStripANSIEdgeCases covers OSC, single-char escapes, and stray ESC.
func TestStripANSIEdgeCases(t *testing.T) {
	c := NewOutputCapture(CommandOutputOptions{SpillDir: t.TempDir()})
	cases := []struct{ in, want string }{
		{"\x1b]0;window title\x07x", "x"},             // OSC terminated by BEL
		{"\x1b]8;;http://x\x1b\\link\x1b]8;;\x1b\\", "link"}, // OSC hyperlink
		{"\x1b7save\x1b8restore", "saverestore"},     // single-char escapes
		{"\x1bc", ""},                                // ESC c (reset)
		{"plain\x1b", "plain"},                       // stray trailing ESC dropped
	}
	for _, tc := range cases {
		if _, err := c.Write([]byte(tc.in)); err != nil {
			t.Fatalf("Write(%q): %v", tc.in, err)
		}
	}
	_ = c.Close()
	wantCombined := ""
	for _, tc := range cases {
		wantCombined += tc.want
	}
	if got := c.Result().Text; got != wantCombined {
		t.Fatalf("edge-case strip mismatch: got %q, want %q", got, wantCombined)
	}
}
