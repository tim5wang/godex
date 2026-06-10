package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/mattn/go-runewidth"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
)

// MarkdownRenderer renders markdown text to ANSI-formatted terminal lines.
type MarkdownRenderer struct {
	parser      goldmark.Markdown
	highlighter *Highlighter
	mu          sync.Mutex
	cache       map[string][]string // key: "text|width"
}

// NewMarkdownRenderer creates a new MarkdownRenderer.
func NewMarkdownRenderer(highlighter *Highlighter) *MarkdownRenderer {
	md := goldmark.New(
		goldmark.WithExtensions(
			extension.Table,
			extension.Strikethrough,
			extension.TaskList,
			extension.Linkify,
		),
		goldmark.WithParserOptions(
			parser.WithAutoHeadingID(),
		),
	)
	return &MarkdownRenderer{
		parser:      md,
		highlighter: highlighter,
		cache:       make(map[string][]string),
	}
}

// Render parses markdown text and returns ANSI-formatted terminal lines.
func (mr *MarkdownRenderer) Render(src string, width int) []string {
	if strings.TrimSpace(src) == "" {
		return nil
	}

	key := src + "|" + strconv.Itoa(width)
	mr.mu.Lock()
	cached, ok := mr.cache[key]
	mr.mu.Unlock()
	if ok {
		return cached
	}

	source := []byte(src)
	doc := mr.parser.Parser().Parse(text.NewReader(source))
	lines := mr.renderNode(doc, source, width, nil)

	mr.mu.Lock()
	mr.cache[key] = lines
	mr.mu.Unlock()
	return lines
}

// InvalidateCache clears the rendered output cache.
func (mr *MarkdownRenderer) InvalidateCache() {
	mr.mu.Lock()
	mr.cache = make(map[string][]string)
	mr.mu.Unlock()
}

// renderNode is the recursive AST node renderer.
// source is the original markdown bytes, needed for Text() calls.
func (mr *MarkdownRenderer) renderNode(n ast.Node, source []byte, width int, listData *listInfo) []string {
	var lines []string

	switch n := n.(type) {
	case *ast.Document:
		for child := n.FirstChild(); child != nil; child = child.NextSibling() {
			childLines := mr.renderNode(child, source, width, nil)
			lines = append(lines, childLines...)
			if needsTrailingBlank(child, child.NextSibling()) {
				lines = append(lines, "")
			}
		}

	case *ast.Paragraph:
		text := string(n.Text(source))
		if text != "" {
			wrapped := wrapWithIndent(text, "", "", width)
			lines = append(lines, wrapped...)
		}

	case *ast.Heading:
		text := string(n.Text(source))
		if text == "" {
			break
		}
		prefix := strings.Repeat("#", n.Level) + " "
		if n.Level == 1 {
			lines = append(lines, heading1Style.Render(prefix+text))
		} else {
			lines = append(lines, headingStyle.Render(prefix+text))
		}

	case *ast.FencedCodeBlock:
		code := string(n.Text(source))
		lang := string(n.Language(source))
		lines = append(lines, renderCodeBlock(mr.highlighter, code, lang, width)...)

	case *ast.CodeBlock:
		code := string(n.Text(source))
		lines = append(lines, renderCodeBlock(mr.highlighter, code, "", width)...)

	case *ast.Blockquote:
		contentWidth := maxInt(1, width-2)
		for child := n.FirstChild(); child != nil; child = child.NextSibling() {
			childLines := mr.renderNode(child, source, contentWidth, nil)
			for _, line := range childLines {
				if line == "" {
					lines = append(lines, quoteBorderStyle.Render("│ "))
				} else {
					lines = append(lines, quoteBorderStyle.Render("│ ")+quoteStyle.Render(line))
				}
			}
		}
		if n.NextSibling() != nil {
			lines = append(lines, "")
		}

	case *ast.List:
		isOrdered := n.IsOrdered()
		startNum := n.Start
		itemIndex := 1

		for child := n.FirstChild(); child != nil; child = child.NextSibling() {
			listItem, ok := child.(*ast.ListItem)
			if !ok {
				continue
			}

			var marker string
			if isOrdered {
				marker = strconv.Itoa(startNum+itemIndex-1) + ". "
			} else {
				marker = "- "
			}

			taskPrefix := ""
			if listItem.HasChildren() {
				if cb, ok := listItem.FirstChild().(*extast.TaskCheckBox); ok {
					if cb.IsChecked {
						taskPrefix = taskDoneStyle.Render("[x] ")
					} else {
						taskPrefix = taskPendingStyle.Render("[ ] ")
					}
				}
			}

			fullMarker := listBulletStyle.Render(marker)

			li := &listInfo{
				marker:      fullMarker + taskPrefix,
				markerWidth: runewidth.StringWidth(marker) + runewidth.StringWidth(taskPrefix),
			}

			itemLines := mr.renderListItem(listItem, source, width, li)
			lines = append(lines, itemLines...)

			if child.NextSibling() != nil {
				if lastItemLine(itemLines) != "" {
					lines = append(lines, "")
				}
			}
			itemIndex++
		}
		if n.NextSibling() != nil {
			lines = append(lines, "")
		}

	case *extast.Table:
		tableLines := mr.renderTable(n, source, width)
		lines = append(lines, tableLines...)

	case *ast.ThematicBreak:
		hrWidth := maxInt(1, width)
		if hrWidth > 80 {
			hrWidth = 80
		}
		lines = append(lines, hrStyle.Render(strings.Repeat("─", hrWidth)))

	case *ast.Text:
		textContent := string(n.Text(source))
		if textContent != "" {
			styled := mr.applyInlineStyles(n, textContent)
			lines = append(lines, styled)
		}

	case *ast.String:
		lines = append(lines, string(n.Value))

	default:
		for child := n.FirstChild(); child != nil; child = child.NextSibling() {
			childLines := mr.renderNode(child, source, width, listData)
			lines = append(lines, childLines...)
		}
	}

	return lines
}

func (mr *MarkdownRenderer) renderListItem(li *ast.ListItem, source []byte, width int, info *listInfo) []string {
	// 收集所有非嵌套列表的子节点渲染文本
	var textParts []string
	var nestedLines []string
	firstParagraph := true

	for child := li.FirstChild(); child != nil; child = child.NextSibling() {
		// 跳过 checkbox
		if _, ok := child.(*extast.TaskCheckBox); ok {
			continue
		}

		// 嵌套列表特殊处理
		if nestedList, ok := child.(*ast.List); ok {
			nestedLinesInner := mr.renderNode(nestedList, source, width, nil)
			for i := range nestedLinesInner {
				nestedLinesInner[i] = "  " + nestedLinesInner[i]
			}
			nestedLines = nestedLinesInner
			continue
		}

		// 渲染子节点，收集文本
		childLines := mr.renderNode(child, source, width, info)
		for _, line := range childLines {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" {
				continue
			}
			if firstParagraph {
				textParts = append(textParts, trimmed)
				firstParagraph = false
			} else {
				// 后续段落：合并到最后一个文本部分
				if len(textParts) > 0 {
					textParts[len(textParts)-1] += " " + trimmed
				} else {
					textParts = append(textParts, trimmed)
				}
			}
		}
	}

	// 构造输出行
	var lines []string
	if len(textParts) > 0 {
		combined := strings.Join(textParts, " ")
		lines = append(lines, info.marker+combined)
	} else {
		lines = append(lines, info.marker)
	}

	// 添加嵌套列表
	lines = append(lines, nestedLines...)

	return lines
}

func (mr *MarkdownRenderer) renderTable(table *extast.Table, source []byte, width int) []string {
	var headerRow []string
	var dataRows [][]string
	numCols := 0

	headNode := table.FirstChild()
	// 表头可能是 TableHeader（goldmark 扩展语法）或 TableRow
	switch h := headNode.(type) {
	case *extast.TableHeader:
		for cell := h.FirstChild(); cell != nil; cell = cell.NextSibling() {
			if c, ok := cell.(*extast.TableCell); ok {
				headerRow = append(headerRow, string(c.Text(source)))
			}
		}
	case *extast.TableRow:
		for cell := h.FirstChild(); cell != nil; cell = cell.NextSibling() {
			if c, ok := cell.(*extast.TableCell); ok {
				headerRow = append(headerRow, string(c.Text(source)))
			}
		}
	}
	numCols = len(headerRow)

	for row := headNode.NextSibling(); row != nil; row = row.NextSibling() {
		if r, ok := row.(*extast.TableRow); ok {
			var rowData []string
			for cell := r.FirstChild(); cell != nil; cell = cell.NextSibling() {
				if c, ok := cell.(*extast.TableCell); ok {
					rowData = append(rowData, string(c.Text(source)))
				}
			}
			if len(rowData) > 0 {
				dataRows = append(dataRows, rowData)
				if len(rowData) > numCols {
					numCols = len(rowData)
				}
			}
		}
	}

	if numCols == 0 {
		return nil
	}

	for len(headerRow) < numCols {
		headerRow = append(headerRow, "")
	}
	for i := range dataRows {
		for len(dataRows[i]) < numCols {
			dataRows[i] = append(dataRows[i], "")
		}
	}

	borderOverhead := 3*numCols + 1
	availableForCells := maxInt(1, width-borderOverhead)

	if availableForCells < numCols {
		var lines []string
		lines = append(lines, "| "+strings.Join(headerRow, " | ")+" |")
		for _, row := range dataRows {
			lines = append(lines, "| "+strings.Join(row, " | ")+" |")
		}
		return lines
	}

	naturalWidths := make([]int, numCols)
	minWordWidths := make([]int, numCols)
	for i := range numCols {
		if i < len(headerRow) {
			naturalWidths[i] = runewidth.StringWidth(headerRow[i])
		}
		minWordWidths[i] = 1
		if i < len(headerRow) {
			minWordWidths[i] = maxInt(1, longestWordWidth(headerRow[i]))
		}
	}
	for _, row := range dataRows {
		for i, cell := range row {
			w := runewidth.StringWidth(cell)
			if w > naturalWidths[i] {
				naturalWidths[i] = w
			}
			mw := longestWordWidth(cell)
			if mw > minWordWidths[i] {
				minWordWidths[i] = mw
			}
		}
	}

	totalNatural := 0
	totalMin := 0
	for i := range numCols {
		totalNatural += naturalWidths[i]
		totalMin += minWordWidths[i]
	}

	columnWidths := make([]int, numCols)

	if totalNatural <= availableForCells {
		for i := range numCols {
			columnWidths[i] = naturalWidths[i]
		}
	} else {
		for i := range numCols {
			columnWidths[i] = minWordWidths[i]
		}
		remaining := availableForCells - totalMin
		for remaining > 0 {
			distributed := false
			for i := range numCols {
				if remaining <= 0 {
					break
				}
				if columnWidths[i] < naturalWidths[i] {
					columnWidths[i]++
					remaining--
					distributed = true
				}
			}
			if !distributed {
				break
			}
		}
	}

	borderStyle := tableBorderStyle

	topParts := make([]string, numCols)
	for i, w := range columnWidths {
		topParts[i] = strings.Repeat("─", w)
	}
	lines := []string{borderStyle.Render("┌─" + strings.Join(topParts, "─┬─") + "─┐")}

	headerParts := make([]string, numCols)
	for i := range numCols {
		text := headerRow[i]
		padded := text + strings.Repeat(" ", maxInt(0, columnWidths[i]-runewidth.StringWidth(text)))
		headerParts[i] = boldStyle.Render(padded)
	}
	lines = append(lines, borderStyle.Render("│ ")+strings.Join(headerParts, borderStyle.Render(" │ "))+borderStyle.Render(" │"))

	sepParts := make([]string, numCols)
	for i, w := range columnWidths {
		sepParts[i] = strings.Repeat("─", w)
	}
	lines = append(lines, borderStyle.Render("├─"+strings.Join(sepParts, "─┼─")+"─┤"))

	for rowIdx, row := range dataRows {
		cellLines := make([][]string, numCols)
		maxCellLines := 0
		for i := range numCols {
			text := row[i]
			if runewidth.StringWidth(text) > columnWidths[i] {
				cellLines[i] = wrapTextSimple(text, columnWidths[i])
			} else {
				cellLines[i] = []string{text}
			}
			if len(cellLines[i]) > maxCellLines {
				maxCellLines = len(cellLines[i])
			}
		}

		for lineIdx := 0; lineIdx < maxCellLines; lineIdx++ {
			rowParts := make([]string, numCols)
			for i := range numCols {
				var cellText string
				if lineIdx < len(cellLines[i]) {
					cellText = cellLines[i][lineIdx]
				}
				padded := cellText + strings.Repeat(" ", maxInt(0, columnWidths[i]-runewidth.StringWidth(cellText)))
				rowParts[i] = padded
			}
			line := borderStyle.Render("│ ") + strings.Join(rowParts, borderStyle.Render(" │ ")) + borderStyle.Render(" │")
			lines = append(lines, line)
		}

		if rowIdx < len(dataRows)-1 {
			sepParts := make([]string, numCols)
			for i, w := range columnWidths {
				sepParts[i] = strings.Repeat("─", w)
			}
			lines = append(lines, borderStyle.Render("├─"+strings.Join(sepParts, "─┼─")+"─┤"))
		}
	}

	bottomParts := make([]string, numCols)
	for i, w := range columnWidths {
		bottomParts[i] = strings.Repeat("─", w)
	}
	lines = append(lines, borderStyle.Render("└─"+strings.Join(bottomParts, "─┴─")+"─┘"))

	return lines
}

// applyInlineStyles wraps text with ANSI styles based on parent AST nodes.
func (mr *MarkdownRenderer) applyInlineStyles(n ast.Node, text string) string {
	result := text
	for parent := n.Parent(); parent != nil; parent = parent.Parent() {
		switch p := parent.(type) {
		case *ast.Emphasis:
			// Level 2 = 粗体, Level 1 = 斜体
			if p.Level >= 2 {
				result = boldStyle.Render(result)
			} else {
				result = italicStyle.Render(result)
			}
		case *ast.CodeSpan:
			result = codeStyle.Render(result)
		case *extast.Strikethrough:
			result = strikethroughStyle.Render(result)
		case *ast.Link:
			result = linkStyle.Render(result)
		case *ast.Heading:
			if p.Level == 1 {
				result = heading1Style.Render(result)
			} else {
				result = headingStyle.Render(result)
			}
		}
	}
	return result
}

// maxCodeBlockLines caps how many lines of a single fenced code block
// the TUI will feed to chroma. Code blocks longer than this are
// folded into a head + "... N lines skipped ..." + tail
// representation. This is the second line of defence against the
// chroma v2.23.1 + dlclark/regexp2 v1.11.5 runaway that pinned the
// TUI's Update goroutine at 100% CPU in 2026-06-10: the first line
// is HighlightWithTimeout (bounds latency); this line bounds input
// size so chroma never sees the pathological inputs that trigger
// regexp2's catastrophic backtracking in the first place.
const maxCodeBlockLines = 500

// truncateCodeBlock folds very long code blocks into a head+tail
// representation so the highlighter only ever sees a small, bounded
// amount of input. The returned slice of strings is what feeds
// chroma; the original code is not retained.
func truncateCodeBlock(code string) (folded []string, skipped int) {
	lines := strings.Split(code, "\n")
	if len(lines) <= maxCodeBlockLines {
		return lines, 0
	}
	head := maxCodeBlockLines * 2 / 3
	tail := maxCodeBlockLines - head
	out := make([]string, 0, head+tail+1)
	out = append(out, lines[:head]...)
	out = append(out, fmt.Sprintf("\u2026 %d lines skipped \u2026", len(lines)-head-tail))
	out = append(out, lines[len(lines)-tail:]...)
	return out, len(lines) - head - tail
}

// renderCodeBlock renders a code block with syntax highlighting and borders.
func renderCodeBlock(hl *Highlighter, code, lang string, width int) []string {
	var lines []string
	_ = width

	indent := "  "
	langTag := lang
	if langTag == "" {
		langTag = "code"
	}
	lines = append(lines, codeBlockBorderStyle.Render("```"+langTag))

	if hl != nil && code != "" {
		folded, _ := truncateCodeBlock(code)
		highlighted := hl.HighlightWithTimeout(context.Background(), strings.Join(folded, "\n"), lang, highlightTimeout)
		if len(highlighted) > 0 {
			for _, line := range highlighted {
				lines = append(lines, indent+line)
			}
		} else {
			for _, line := range folded {
				lines = append(lines, indent+codeBlockStyle.Render(line))
			}
		}
	} else if code != "" {
		for _, line := range strings.Split(code, "\n") {
			lines = append(lines, indent+codeBlockStyle.Render(line))
		}
	}

	lines = append(lines, codeBlockBorderStyle.Render("```"))
	return lines
}

func wrapTextSimple(text string, width int) []string {
	if width <= 0 {
		return []string{text}
	}
	return strings.Split(runewidth.Wrap(text, width), "\n")
}

func longestWordWidth(text string) int {
	maxW := 0
	for _, word := range strings.Fields(text) {
		w := runewidth.StringWidth(word)
		if w > maxW {
			maxW = w
		}
	}
	return maxInt(1, maxW)
}

func needsTrailingBlank(n, next ast.Node) bool {
	if next == nil {
		return false
	}
	switch n.(type) {
	case *ast.Heading, *ast.FencedCodeBlock, *ast.CodeBlock, *ast.Blockquote, *extast.Table, *ast.ThematicBreak:
		return true
	}
	return false
}

func lastItemLine(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	return lines[len(lines)-1]
}

type listInfo struct {
	marker      string
	markerWidth int
}
