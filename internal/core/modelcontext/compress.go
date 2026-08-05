package modelcontext

import "strings"

// Tool output compression thresholds. Mirrors the spirit of rtk's
// truncation caps (CAP_ERRORS / CAP_LIST / CAP_INVENTORY) but tuned for
// godex's bash/read_file tool outputs. Compression only kicks in above
// CompressMinBytes so small outputs pass through untouched (zero overhead).
const (
	// CompressMinBytes: outputs smaller than this are returned unchanged.
	CompressMinBytes = 2 * 1024

	// CompressMaxLines is the total line budget for generic compression.
	CompressMaxLines = 120
	// CompressHeadLines kept from the start of the output.
	CompressHeadLines = 60
	// CompressTailLines kept from the end of the output.
	CompressTailLines = 20
	// CompressMaxLineChars truncates over-long single lines.
	CompressMaxLineChars = 500

	// Semantic caps for command-aware filters (rtk-inspired).
	SemanticErrorCap = 20 // test failures / build errors shown before "+N more"
	SemanticListCap  = 20 // flat listings (git status, ls) before "+N more"

	// minCompressRatio is the minimum size reduction required before the
	// compressed form replaces the original (fail-safe: no churn for
	// near-optimal outputs).
	minCompressRatio = 0.2
)

// CompressToolOutput applies token-saving compression to a tool result.
//
//   - toolName is the tool that produced output (bash, read_file, ...).
//   - command is the raw command/input string used to select a
//     command-aware filter (empty for non-bash tools).
//   - output is the model-visible text.
//
// It returns the compressed text, or the original output unchanged when the
// output is below the compression threshold, no command-aware filter
// applies, or compression would not save at least minCompressRatio bytes.
// The returned value never exceeds the input size (fail-safe).
func CompressToolOutput(toolName, command, output string) string {
	if len(output) < CompressMinBytes {
		return output
	}

	// Command-aware filters first (they know structure and can cut more).
	if semantic, ok := compressSemantic(toolName, command, output); ok {
		if len(semantic) < len(output) && ratio(len(semantic), len(output)) >= minCompressRatio {
			return semantic
		}
		// fall through to generic compression even if the semantic
		// attempt did not save enough
	}

	generic := compressGeneric(output)
	if len(generic) < len(output) && ratio(len(generic), len(output)) >= minCompressRatio {
		return generic
	}
	return output
}

func ratio(compressed, original int) float64 {
	if original <= 0 {
		return 0
	}
	return 1 - float64(compressed)/float64(original)
}

// compressGeneric is the L0 universal compressor: collapse blank runs,
// fold repeated lines, truncate over-long lines, and cap total lines with a
// head/tail window. Safe for any textual tool output.
func compressGeneric(output string) string {
	lines := strings.Split(output, "\n")
	if len(lines) == 0 {
		return output
	}

	kept := make([]string, 0, len(lines))
	blankRun := 0
	lastRaw := "" // raw (normalized) text of the previous content line
	lastIdx := -1 // index in kept of the previous content line

	for _, line := range lines {
		trimmed := strings.TrimRight(line, " \t\r")
		if strings.TrimSpace(trimmed) == "" {
			blankRun++
			if blankRun == 1 && len(kept) > 0 {
				kept = append(kept, "")
			}
			continue
		}
		blankRun = 0

		if len(trimmed) > CompressMaxLineChars {
			trimmed = trimmed[:CompressMaxLineChars] + " …"
		}

		if trimmed == lastRaw {
			// Same content as the previous line: bump its run marker.
			kept[lastIdx] = bumpFold(kept[lastIdx])
			continue
		}
		lastRaw = trimmed
		kept = append(kept, trimmed)
		lastIdx = len(kept) - 1
	}

	// Cap total lines with a head/tail window.
	if len(kept) <= CompressMaxLines {
		return strings.Join(kept, "\n")
	}
	head := kept[:CompressHeadLines]
	tail := kept[len(kept)-CompressTailLines:]
	omitted := len(kept) - CompressHeadLines - CompressTailLines
	var b strings.Builder
	b.WriteString(strings.Join(head, "\n"))
	b.WriteString("\n…[")
	b.WriteString(itoa(omitted))
	b.WriteString(" lines omitted]…\n")
	b.WriteString(strings.Join(tail, "\n"))
	return b.String()
}

// bumpFold turns a repeated line into a run marker:
//
//	"foo"      -> "foo (x2)"
//	"foo (x2)" -> "foo (x3)"
func bumpFold(line string) string {
	if idx := strings.LastIndex(line, " (x"); idx != -1 && strings.HasSuffix(line, ")") {
		base := line[:idx]
		numStr := line[idx+3 : len(line)-1]
		if n, ok := atoi(numStr); ok {
			return base + " (x" + itoa(n+1) + ")"
		}
	}
	return line + " (x2)"
}

// compressSemantic dispatches to a command-aware filter. Returns ok=false
// when no filter matches the tool/command.
func compressSemantic(toolName, command, output string) (string, bool) {
	cmd := strings.TrimSpace(command)
	lower := strings.ToLower(cmd)
	switch {
	case toolName == "bash" && strings.HasPrefix(lower, "git status"):
		return compressGitStatus(output)
	case toolName == "bash" && (strings.HasPrefix(lower, "git diff") || strings.HasPrefix(lower, "git log")):
		return compressGitDiffLog(output)
	case toolName == "bash" && strings.HasPrefix(lower, "go test"):
		return compressGoTest(output)
	case toolName == "bash" && strings.HasPrefix(lower, "ls"):
		return compressList(output)
	case toolName == "read_file":
		return compressGeneric(output), true
	}
	return "", false
}

// compressGitStatus keeps the status summary and at most SemanticListCap
// changed-path lines, plus a trailing count of omitted entries.
func compressGitStatus(output string) (string, bool) {
	lines := strings.Split(output, "\n")
	kept := make([]string, 0, 32)
	omitted := 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		// Section headers always kept.
		if strings.HasSuffix(trimmed, ":") && !strings.Contains(trimmed, "\t") {
			kept = append(kept, trimmed)
			continue
		}
		// Status lines start with status letters/space (" M", "??", "A ").
		if isStatusEntry(trimmed) {
			if len(kept) < SemanticListCap {
				kept = append(kept, trimmed)
			} else {
				omitted++
			}
			continue
		}
		kept = append(kept, trimmed)
	}
	if omitted > 0 {
		kept = append(kept, "…+"+itoa(omitted)+" more changed paths")
	}
	if len(kept) == 0 {
		return "", false
	}
	return strings.Join(kept, "\n"), true
}

func isStatusEntry(s string) bool {
	if len(s) < 2 {
		return false
	}
	c := s[0]
	return c == ' ' || c == '?' || c == 'A' || c == 'M' || c == 'D' || c == 'R' || c == 'C'
}

// compressGitDiffLog keeps structural lines (headers, hunks, commit
// metadata) and +/- changed lines, dropping bulky unchanged context.
func compressGitDiffLog(output string) (string, bool) {
	lines := strings.Split(output, "\n")
	kept := make([]string, 0, 64)
	omitted := 0
	for _, line := range lines {
		trimmed := strings.TrimRight(line, " \t\r")
		if strings.TrimSpace(trimmed) == "" {
			continue
		}
		if isStructuralLine(trimmed) {
			if len(kept) < SemanticErrorCap*3 {
				kept = append(kept, trimmed)
			} else {
				omitted++
			}
			continue
		}
		if strings.HasPrefix(trimmed, "+") || strings.HasPrefix(trimmed, "-") {
			if len(kept) < SemanticErrorCap*3 {
				kept = append(kept, trimmed)
			} else {
				omitted++
			}
		}
	}
	if omitted > 0 {
		kept = append(kept, "…+"+itoa(omitted)+" more diff lines")
	}
	if len(kept) == 0 {
		return "", false
	}
	return strings.Join(kept, "\n"), true
}

func isStructuralLine(s string) bool {
	for _, p := range []string{
		"diff --git", "index ", "+++", "---", "@@",
		"commit ", "Author:", "Date:",
	} {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// compressGoTest keeps the test summary plus failure details, dropping the
// verbose === RUN === progress lines that carry no information.
func compressGoTest(output string) (string, bool) {
	lines := strings.Split(output, "\n")
	kept := make([]string, 0, 48)
	omitted := 0
	failures := 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "=== RUN") || strings.HasPrefix(trimmed, "=== CONT") {
			continue // progress noise
		}
		if strings.HasPrefix(trimmed, "--- FAIL") {
			failures++
			if failures <= SemanticErrorCap {
				kept = append(kept, trimmed)
			} else {
				omitted++
			}
			continue
		}
		if strings.HasPrefix(trimmed, "--- PASS") || strings.HasPrefix(trimmed, "--- SKIP") ||
			strings.HasPrefix(trimmed, "FAIL") || strings.HasPrefix(trimmed, "PASS") ||
			strings.HasPrefix(trimmed, "ok ") {
			kept = append(kept, trimmed)
			continue
		}
		// Failure source lines (file:line) and indented details.
		if (strings.Contains(trimmed, ".go:") || strings.HasPrefix(trimmed, "\t")) && failures > 0 && len(kept) < SemanticErrorCap*4 {
			kept = append(kept, trimmed)
		}
	}
	if omitted > 0 {
		kept = append(kept, "…+"+itoa(omitted)+" more test failures")
	}
	if len(kept) == 0 {
		return "", false
	}
	return strings.Join(kept, "\n"), true
}

// compressList flattens simple listings (ls) into a single compact line.
func compressList(output string) (string, bool) {
	fields := strings.Fields(output)
	if len(fields) <= 1 {
		return "", false
	}
	var b strings.Builder
	b.WriteString(strings.Join(fields, " "))
	b.WriteString("\n(")
	b.WriteString(itoa(len(fields)))
	b.WriteString(" entries)")
	return b.String(), true
}

// itoa is a small int-to-string helper (avoids importing strconv in the hot
// path for these short numbers).
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// atoi parses a small non-negative integer; ok=false on any error.
func atoi(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	n := 0
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return 0, false
		}
		n = n*10 + int(ch-'0')
	}
	return n, true
}
