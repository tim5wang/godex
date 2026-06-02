package tui

import (
	"fmt"
	"strings"

	"github.com/sergi/go-diff/diffmatchpatch"
)

// renderEditDiff renders a visual diff for edit tool results.
// input is the tool input map, expected to contain "edits" with oldText/newText pairs.
// width is the available terminal width.
func renderEditDiff(input map[string]interface{}, width int) []string {
	edits := parseEditInput(input)
	if len(edits) == 0 {
		return nil
	}

	filePath, _ := input["path"].(string)
	var lines []string

	for i, edit := range edits {
		if filePath != "" {
			header := fmt.Sprintf("── %s ──", filePath)
			if len(edits) > 1 {
				header = fmt.Sprintf("── %s (edit %d/%d) ──", filePath, i+1, len(edits))
			}
			if w := maxInt(10, width-4); len(header) > w {
				header = ellipsize(header, w)
			}
			lines = append(lines, diffFileHeaderStyle.Render(header))
		}

		diffLines := renderTextDiff(edit.oldText, edit.newText, maxInt(10, width-4))
		lines = append(lines, diffLines...)

		if i < len(edits)-1 {
			lines = append(lines, "")
		}
	}

	return lines
}

type editPair struct {
	oldText string
	newText string
}

// parseEditInput extracts edit pairs from tool input.
func parseEditInput(input map[string]interface{}) []editPair {
	rawEdits, ok := input["edits"]
	if !ok {
		return nil
	}

	editsList, ok := rawEdits.([]interface{})
	if !ok {
		return nil
	}

	var result []editPair
	for _, raw := range editsList {
		editMap, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		oldText, _ := editMap["oldText"].(string)
		newText, _ := editMap["newText"].(string)
		if oldText == "" && newText == "" {
			continue
		}
		result = append(result, editPair{oldText: oldText, newText: newText})
	}
	return result
}

// renderTextDiff renders a unified diff between oldText and newText with ANSI colors.
func renderTextDiff(oldText, newText string, width int) []string {
	dmp := diffmatchpatch.New()
	diffs := dmp.DiffMain(oldText, newText, true)
	diffs = dmp.DiffCleanupSemantic(diffs)

	var lines []string
	oldLineNum := 1
	newLineNum := 1

	// 计算最大行号宽度
	oldLines := strings.Split(oldText, "\n")
	newLines := strings.Split(newText, "\n")
	maxLineNum := len(oldLines)
	if len(newLines) > maxLineNum {
		maxLineNum = len(newLines)
	}
	lineNumWidth := 1
	for maxLineNum >= 10 {
		lineNumWidth++
		maxLineNum /= 10
	}

	for i, diff := range diffs {
		text := diff.Text
		textLines := strings.Split(text, "\n")
		if strings.HasSuffix(text, "\n") {
			textLines = textLines[:len(textLines)-1]
		}

		switch diff.Type {
		case diffmatchpatch.DiffEqual:
			lastWasChange := len(lines) > 0 && isChangeLine(lines[len(lines)-1])
			nextIsChange := hasChangeAfterIndex(diffs, i)

			if lastWasChange || nextIsChange {
				for _, line := range textLines {
					lineStr := formatDiffLine(' ', lineNumWidth, oldLineNum, line)
					lines = append(lines, diffContextStyle.Render(lineStr))
					oldLineNum++
					newLineNum++
				}
			} else {
				// 跳过不属于上下文块的相等行
				if len(textLines) <= 3 {
					for range textLines {
						oldLineNum++
						newLineNum++
					}
				} else {
					// 显示省略号
					skip := len(textLines)
					oldLineNum += skip
					newLineNum += skip
				}
			}

		case diffmatchpatch.DiffDelete:
			if len(textLines) == 1 && i+1 < len(diffs) && diffs[i+1].Type == diffmatchpatch.DiffInsert {
				// 单行删除+新增→行内差异
				next := diffs[i+1].Text
				nextLines := strings.Split(next, "\n")
				if len(nextLines) == 1 || (len(nextLines) == 2 && nextLines[1] == "") {
					delLine, addLine := renderWordDiff(textLines[0], strings.TrimRight(next, "\n"))
					oldStr := formatDiffLine('-', lineNumWidth, oldLineNum, delLine)
					newStr := formatDiffLine('+', lineNumWidth, newLineNum, addLine)
					lines = append(lines, diffRemovedStyle.Render(oldStr))
					lines = append(lines, diffAddedStyle.Render(newStr))
					oldLineNum++
					newLineNum++
					continue
				}
			}
			for _, line := range textLines {
				lineStr := formatDiffLine('-', lineNumWidth, oldLineNum, line)
				lines = append(lines, diffRemovedStyle.Render(lineStr))
				oldLineNum++
			}

		case diffmatchpatch.DiffInsert:
			for _, line := range textLines {
				lineStr := formatDiffLine('+', lineNumWidth, newLineNum, line)
				lines = append(lines, diffAddedStyle.Render(lineStr))
				newLineNum++
			}
		}
	}

	// 截断到可用宽度
	for i, line := range lines {
		lines[i] = truncateANSI(line, width)
	}

	return lines
}

func hasChangeAfterIndex(diffs []diffmatchpatch.Diff, idx int) bool {
	for j := idx + 1; j < len(diffs); j++ {
		if diffs[j].Type != diffmatchpatch.DiffEqual {
			return true
		}
	}
	return false
}

// formatDiffLine formats a diff line with prefix and line number.
func formatDiffLine(prefix rune, lineNumWidth, lineNum int, content string) string {
	numStr := fmt.Sprintf("%d", lineNum)
	padded := strings.Repeat(" ", lineNumWidth-len(numStr)) + numStr
	return fmt.Sprintf("%c%s %s", prefix, padded, content)
}

// renderWordDiff computes word-level diff between two single lines.
func renderWordDiff(oldLine, newLine string) (string, string) {
	dmp := diffmatchpatch.New()
	diffs := dmp.DiffMain(oldLine, newLine, true)
	diffs = dmp.DiffCleanupSemantic(diffs)

	var removedBuilder, addedBuilder strings.Builder
	for _, diff := range diffs {
		switch diff.Type {
		case diffmatchpatch.DiffEqual:
			removedBuilder.WriteString(diff.Text)
			addedBuilder.WriteString(diff.Text)
		case diffmatchpatch.DiffDelete:
			removedBuilder.WriteString(renderInlineHighlight(diff.Text, true))
		case diffmatchpatch.DiffInsert:
			addedBuilder.WriteString(renderInlineHighlight(diff.Text, false))
		}
	}
	return removedBuilder.String(), addedBuilder.String()
}

// renderInlineHighlight wraps text with inline diff highlight markers.
func renderInlineHighlight(text string, isRemoved bool) string {
	if isRemoved {
		return "\033[7m" + text + "\033[27m" // reverse video
	}
	return "\033[7m" + text + "\033[27m"
}

// isChangeLine checks if a rendered line is a change (prefixed with + or -).
func isChangeLine(line string) bool {
	stripped := stripANSIPrefix(line)
	return len(stripped) > 0 && (stripped[0] == '+' || stripped[0] == '-')
}

func stripANSIPrefix(line string) string {
	// Strip leading ANSI codes
	result := line
	for strings.HasPrefix(result, "\033[") {
		if idx := strings.IndexByte(result, 'm'); idx >= 0 {
			result = result[idx+1:]
		} else {
			break
		}
	}
	return result
}



// truncateANSI truncates a string with ANSI codes to the given width.
func truncateANSI(s string, width int) string {
	if width <= 0 {
		return ""
	}
	// Simple approach: count visible characters, preserve ANSI codes
	var visible strings.Builder
	var ansiBuf strings.Builder
	inANSI := false
	count := 0

	for _, r := range s {
		if inANSI {
			ansiBuf.WriteRune(r)
			if r == 'm' {
				inANSI = false
				if count <= width {
					visible.WriteString(ansiBuf.String())
				}
				ansiBuf.Reset()
			}
			continue
		}
		if r == '\033' {
			inANSI = true
			ansiBuf.WriteRune(r)
			continue
		}
		if count < width {
			visible.WriteRune(r)
			count++
		}
	}

	result := visible.String()
	if len(s) > len(result) {
		// Close any open ANSI codes
		result += "\033[0m"
	}
	return result
}
