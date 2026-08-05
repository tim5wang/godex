package modelcontext

import (
	"strings"
	"testing"
)

func TestCompressToolOutputBelowThresholdUntouched(t *testing.T) {
	in := "small output"
	if got := CompressToolOutput("bash", "git status", in); got != in {
		t.Fatalf("expected untouched small output, got %q", got)
	}
}

func TestCompressGenericFoldsRepeatedLines(t *testing.T) {
	lines := make([]string, 0, 40)
	for i := 0; i < 30; i++ {
		lines = append(lines, "same line")
	}
	in := strings.Join(lines, "\n")
	got := compressGeneric(in)
	if strings.Count(got, "same line") != 1 {
		t.Fatalf("expected folded single occurrence, got:\n%s", got)
	}
	if !strings.Contains(got, "(x30)") {
		t.Fatalf("expected run marker (x30), got:\n%s", got)
	}
}

func TestCompressGenericCollapsesBlankRuns(t *testing.T) {
	in := "a\n\n\n\n\nb\n\n\nc"
	got := compressGeneric(in)
	if strings.Contains(got, "\n\n\n") {
		t.Fatalf("blank run not collapsed:\n%q", got)
	}
}

func TestCompressGenericHeadTailWindow(t *testing.T) {
	lines := make([]string, 0, CompressMaxLines*2)
	for i := 0; i < CompressMaxLines*2; i++ {
		lines = append(lines, "line-"+itoa(i))
	}
	in := strings.Join(lines, "\n")
	got := compressGeneric(in)
	if !strings.Contains(got, "lines omitted") {
		t.Fatalf("expected omission marker, got:\n%s", got)
	}
	if !strings.Contains(got, "line-0") || !strings.Contains(got, "line-"+itoa(CompressMaxLines*2-1)) {
		t.Fatalf("expected head and tail preserved")
	}
}

func TestCompressGenericTruncatesLongLines(t *testing.T) {
	long := strings.Repeat("x", CompressMaxLineChars*2)
	in := "header\n" + long + "\nfooter"
	got := compressGeneric(in)
	for _, line := range strings.Split(got, "\n") {
		if len(line) > CompressMaxLineChars+4 {
			t.Fatalf("line not truncated: %d chars", len(line))
		}
	}
}

func TestCompressGitStatus(t *testing.T) {
	var b strings.Builder
	b.WriteString("On branch main\n")
	b.WriteString("Changes not staged for commit:\n")
	b.WriteString("  (use \"git add <file>...\" to update what will be committed)\n")
	for i := 0; i < 30; i++ {
		b.WriteString("\tM modified" + itoa(i) + ".go\n")
	}
	b.WriteString("\nUntracked files:\n")
	b.WriteString("\t?? newfile.go\n")
	in := b.String()

	got, ok := compressGitStatus(in)
	if !ok {
		t.Fatalf("expected git status filter to apply")
	}
	if strings.Count(got, "modified") > SemanticListCap {
		t.Fatalf("too many status entries kept: %d lines", strings.Count(got, "modified"))
	}
	if !strings.Contains(got, "more changed paths") {
		t.Fatalf("expected omitted-count marker:\n%s", got)
	}
	if !strings.Contains(got, "Untracked files") {
		t.Fatalf("expected untracked section header:\n%s", got)
	}
}

func TestCompressGoTest(t *testing.T) {
	var b strings.Builder
	b.WriteString("=== RUN   TestFoo\n")
	b.WriteString("=== RUN   TestBar\n")
	b.WriteString("--- PASS: TestFoo (0.00s)\n")
	b.WriteString("    foo_test.go:12: some detail\n")
	b.WriteString("--- FAIL: TestBar (0.01s)\n")
	b.WriteString("    bar_test.go:34: expected 1, got 2\n")
	b.WriteString("FAIL\n")
	b.WriteString("FAIL\tgithub.com/example/pkg\t0.500s\n")
	in := b.String()

	got, ok := compressGoTest(in)
	if !ok {
		t.Fatalf("expected go test filter to apply")
	}
	if strings.Contains(got, "=== RUN") {
		t.Fatalf("expected === RUN noise removed:\n%s", got)
	}
	if !strings.Contains(got, "--- FAIL: TestBar") {
		t.Fatalf("expected failure kept:\n%s", got)
	}
	if !strings.Contains(got, "bar_test.go:34") {
		t.Fatalf("expected failure source kept:\n%s", got)
	}
	if !strings.Contains(got, "FAIL\tgithub.com/example/pkg") {
		t.Fatalf("expected package summary kept:\n%s", got)
	}
}

func TestCompressList(t *testing.T) {
	in := "file1.go\nfile2.go\nfile3.go\n"
	got, ok := compressList(in)
	if !ok {
		t.Fatalf("expected list filter to apply")
	}
	if !strings.Contains(got, "file1.go file2.go file3.go") {
		t.Fatalf("expected flattened list, got:\n%s", got)
	}
	if !strings.Contains(got, "3 entries") {
		t.Fatalf("expected entry count, got:\n%s", got)
	}
}

func TestCompressGitDiffKeepsStructuralLines(t *testing.T) {
	in := "diff --git a/x.go b/x.go\nindex 123..456 100644\n--- a/x.go\n+++ b/x.go\n@@ -1,3 +1,4 @@\n context line\n+added line\n-removed line\n context2\n"
	got, ok := compressGitDiffLog(in)
	if !ok {
		t.Fatalf("expected diff filter to apply")
	}
	if !strings.Contains(got, "diff --git") || !strings.Contains(got, "@@") {
		t.Fatalf("expected structural lines kept:\n%s", got)
	}
	if !strings.Contains(got, "+added line") || !strings.Contains(got, "-removed line") {
		t.Fatalf("expected +/- lines kept:\n%s", got)
	}
}

func TestCompressToolOutputFailsafeNeverGrows(t *testing.T) {
	// A pathological input that compression cannot shrink must be returned
	// unchanged (fail-safe): many distinct short lines under the line cap,
	// no blank runs, no over-long lines, no repeats.
	var b strings.Builder
	for i := 0; i < 100; i++ {
		b.WriteString("distinct-line-" + itoa(i) + "-content-padding-here\n")
	}
	in := b.String()
	if len(in) < CompressMinBytes {
		t.Fatalf("test input too small (%d bytes); raise line count", len(in))
	}
	got := CompressToolOutput("bash", "unknown-cmd", in)
	if got != in {
		t.Fatalf("expected unchanged fail-safe output")
	}
}

func TestBumpFold(t *testing.T) {
	tests := []struct{ in, want string }{
		{"foo", "foo (x2)"},
		{"foo (x2)", "foo (x3)"},
		{"foo (x10)", "foo (x11)"},
	}
	for _, tc := range tests {
		if got := bumpFold(tc.in); got != tc.want {
			t.Fatalf("bumpFold(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
