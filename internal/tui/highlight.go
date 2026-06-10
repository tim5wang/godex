package tui

import (
	"context"
	"io"
	"strings"
	"time"

	"github.com/alecthomas/chroma/v2"
	"github.com/charmbracelet/lipgloss"
)

// Highlighter provides syntax highlighting for code blocks.
type Highlighter struct {
	registry  *chroma.LexerRegistry
	formatter chroma.Formatter
	style     *chroma.Style
}

// NewHighlighter creates a Highlighter with theme matching terminal background.
func NewHighlighter() *Highlighter {
	isDark := lipgloss.HasDarkBackground()

	reg := chroma.NewLexerRegistry()
	registerGoLexer(reg)
	registerPythonLexer(reg)
	registerJSLexer(reg)
	registerTSLexer(reg)
	registerJSONLexer(reg)
	registerYAMLLexer(reg)
	registerBashLexer(reg)
	registerSQLLexer(reg)
	registerRustLexer(reg)

	style := darkChromaStyle()
	if !isDark {
		style = lightChromaStyle()
	}

	return &Highlighter{
		registry:  reg,
		formatter: chroma.FormatterFunc(tty256Formatter),
		style:     style,
	}
}

// highlightTimeout caps how long a single call to chroma/regexp2 may
// run before we abandon the result and fall back to plain text. The
// default is generous enough to highlight thousands of lines on a
// fast machine, but tight enough that a runaway regexp2 NFA
// (chroma v2.23.1 + dlclark/regexp2 v1.11.5) can no longer wedge the
// TUI's Update loop. See ./godex.dump from 2026-06-10 for the
// captured runaway: chroma -> matchRules -> regexp2.(*Regexp).run
// in runner.go:76 spinning forever, WindowSizeMsg never delivered,
// m.width stuck at 0, "Loading TUI..." forever.
const highlightTimeout = 150 * time.Millisecond

// Highlight returns syntax-highlighted lines for the given code and
// language. It exists for backwards compatibility; new callers should
// prefer HighlightWithTimeout so a runaway chroma/regexp2 NFA cannot
// pin the TUI's Update goroutine.
func (h *Highlighter) Highlight(code string, lang string) []string {
	return h.HighlightWithTimeout(context.Background(), code, lang, highlightTimeout)
}

// HighlightWithTimeout runs the same pipeline as Highlight but abandons
// the result after the given timeout. The return contract matches
// Highlight: a non-nil []string on success, nil when the input is
// empty or the language cannot be detected, and []string of plain
// text lines when chroma itself errors out. The timeout case
// additionally returns nil so the caller can fall back to a plain
// text rendering of the code block.
//
// The timeout is enforced by running chroma on a fresh goroutine and
// racing its result against a context-aware select. The chroma
// goroutine is left to run to completion (or until process exit); we
// cannot cancel it because chroma v2.23.1 does not take a context.
// This is acceptable: a leaked goroutine that the runtime reclaims
// at process exit is strictly better than a TUI that never recovers
// from "Loading TUI...".
func (h *Highlighter) HighlightWithTimeout(ctx context.Context, code string, lang string, timeout time.Duration) []string {
	if strings.TrimSpace(code) == "" {
		return nil
	}

	type result struct {
		lines []string
	}
	done := make(chan result, 1)
	go func() {
		done <- result{lines: h.highlightNoTimeout(code, lang)}
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case r := <-done:
		return r.lines
	case <-ctx.Done():
		return nil
	case <-timer.C:
		return nil
	}
}

// highlightNoTimeout is the original chroma pipeline extracted so
// HighlightWithTimeout can race it against a timer.
func (h *Highlighter) highlightNoTimeout(code, lang string) []string {
	var lexer chroma.Lexer
	if lang != "" {
		lexer = h.registry.Get(lang)
	}
	if lexer == nil {
		lexer = h.registry.Match("file." + lang)
	}
	if lexer == nil {
		lexer = h.detectLanguage(code)
	}
	if lexer == nil {
		return strings.Split(code, "\n")
	}

	iterator, err := lexer.Tokenise(nil, code)
	if err != nil || iterator == nil {
		return strings.Split(code, "\n")
	}

	var buf strings.Builder
	if err := h.formatter.Format(&buf, h.style, iterator); err != nil {
		return strings.Split(code, "\n")
	}

	return splitLines(buf.String())
}

func (h *Highlighter) detectLanguage(code string) chroma.Lexer {
	// 基于关键字的语言检测
	scores := make(map[string]int)

	lines := strings.Split(code, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		// Go
		if containsAny(line, "package ", "import (", "func ", "type ", "var ", " := ", "nil", "fmt.", "defer ", "go func", "struct {", "interface {") {
			scores["go"] += 3
		}
		if containsAny(line, "func ", "package ", "import (") && containsAny(line, "{") {
			scores["go"] += 2
		}
		// Python
		if containsAny(line, "def ", "class ", "import ", "from ", "if __name__", "elif ", "except ", "asyn", "lambda:", "yield ") {
			scores["python"] += 3
		}
		if containsAny(line, "def ", "class ") && containsAny(line, ":") && !containsAny(line, "{") {
			scores["python"] += 2
		}
		// JavaScript / TypeScript
		if containsAny(line, "const ", "let ", "var ", "=>", "function(", "import {", "export ", "console.", "document.", "require(", "module.export") {
			scores["javascript"] += 2
		}
		if containsAny(line, ": string", ": number", ": boolean", ": void", ": any", "interface ", "type ", "<T>", "as unknown") {
			scores["typescript"] += 2
		}
		// Rust
		if containsAny(line, "fn ", "let mut ", "impl ", "pub ", "struct ", "enum ", "match ", "crate::", "-> ", "=> ", "use ") && strings.Contains(line, ";") {
			scores["rust"] += 3
		}
		// Bash
		if strings.HasPrefix(line, "#!") && strings.Contains(line, "sh") {
			scores["bash"] += 5
		}
		if containsAny(line, "export ", "echo ", "${", "if [", "fi", "done", "do\n", "then", "#!/bin/bash", "#!/usr/bin/env") {
			scores["bash"] += 2
		}
		// SQL
		if containsAny(line, "SELECT ", "FROM ", "WHERE ", "INSERT INTO", "CREATE TABLE", "ALTER TABLE", "DELETE FROM", "JOIN ", "GROUP BY", "ORDER BY", "HAVING ", "UPDATE ", "SET ") {
			scores["sql"] += 3
		}
		if strings.HasPrefix(line, "--") {
			scores["sql"] += 1
		}
		// JSON: 行以 { 或 [ 开头, 或以 }, ] 结尾, 且有引号
		line = strings.TrimSpace(line)
		if (strings.HasPrefix(line, "{\"") || strings.HasPrefix(line, "[{") || (strings.Contains(line, "\":") && (strings.HasSuffix(line, ",") || strings.HasSuffix(line, "}")))) {
			scores["json"] += 2
		}
		// YAML
		if strings.HasPrefix(line, "---") || strings.HasPrefix(line, "...") {
			scores["yaml"] += 2
		}
		if strings.Contains(line, ": ") && !strings.ContainsAny(line, "{([") && (strings.HasPrefix(line, "  -") || !strings.HasPrefix(line, " ")) {
			if strings.Count(line, ": ") == 1 && !strings.Contains(line, "http") {
				scores["yaml"] += 1
			}
		}
	}

	best := ""
	bestScore := 0
	for lang, score := range scores {
		if score > bestScore {
			best = lang
			bestScore = score
		}
	}

	if bestScore >= 3 {
		return h.registry.Get(best)
	}
	return nil
}

func containsAny(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func splitLines(s string) []string {
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// ---------- chroma lexer registration ----------

func registerGoLexer(reg *chroma.LexerRegistry) {
	reg.Register(chroma.MustNewLexer(
		&chroma.Config{
			Name:      "Go",
			Aliases:   []string{"go", "golang"},
			Filenames: []string{"*.go"},
		},
		func() chroma.Rules {
			return chroma.Rules{
				"root": {
					{`\s+`, chroma.Text, nil},
					{`//.*`, chroma.CommentSingle, nil},
					{`/\*.*?\*/`, chroma.CommentMultiline, nil},
					{`0[xX][0-9a-fA-F_]+`, chroma.LiteralNumberHex, nil},
					{`0[0-7_]+`, chroma.LiteralNumberOct, nil},
					{`[0-9][0-9_]*(\.[0-9][0-9_]*)?`, chroma.LiteralNumberFloat, nil},
					{`'(?:[^'\\]|\\.)*'`, chroma.LiteralStringChar, nil},
					{`"(?:[^"\\]|\\.)*"`, chroma.LiteralStringDouble, nil},
					{"`(?:[^`\\\\]|\\\\.)*`", chroma.LiteralStringBacktick, nil},
					{`\b(break|case|chan|const|continue|default|defer|else|fallthrough|for|func|go|goto|if|import|interface|map|package|range|return|select|struct|switch|type|var)\b`, chroma.Keyword, nil},
					{`\b(bool|byte|complex64|complex128|error|float32|float64|int|int8|int16|int32|int64|rune|string|uint|uint8|uint16|uint32|uint64|uintptr)\b`, chroma.KeywordType, nil},
					{`\b(append|cap|close|copy|delete|len|make|new|panic|print|println|real|imag|recover)\b`, chroma.KeywordDeclaration, nil},
					{`\b(nil|true|false|iota)\b`, chroma.KeywordConstant, nil},
					{`[a-zA-Z_]\w*`, chroma.Name, nil},
					{`[{}()\[\],.:;]`, chroma.Punctuation, nil},
					{`[+\-*/%&|^~<>=!]`, chroma.Operator, nil},
				},
			}
		},
	))
}

func registerPythonLexer(reg *chroma.LexerRegistry) {
	reg.Register(chroma.MustNewLexer(
		&chroma.Config{
			Name:      "Python",
			Aliases:   []string{"python", "py", "sage"},
			Filenames: []string{"*.py"},
		},
		func() chroma.Rules {
			return chroma.Rules{
				"root": {
					{`\s+`, chroma.Text, nil},
					{`#.*$`, chroma.CommentSingle, nil},
					{`"""[\s\S]*?"""`, chroma.LiteralStringDoc, nil},
					{`'''[\s\S]*?'''`, chroma.LiteralStringDoc, nil},
					{`"([^"\\]|\\.)*"`, chroma.LiteralStringDouble, nil},
					{`'([^'\\]|\\.)*'`, chroma.LiteralStringSingle, nil},
					{`\b(and|as|assert|async|await|break|class|continue|def|del|elif|else|except|finally|for|from|global|if|import|in|is|lambda|nonlocal|not|or|pass|raise|return|try|while|with|yield)\b`, chroma.Keyword, nil},
					{`\b(True|False|None)\b`, chroma.KeywordConstant, nil},
					{`\b(int|float|bool|str|list|dict|tuple|set|type)\b`, chroma.KeywordType, nil},
					{`0[xX][0-9a-fA-F_]+`, chroma.LiteralNumberHex, nil},
					{`[0-9][0-9_]*(\.[0-9][0-9_]*)?`, chroma.LiteralNumberFloat, nil},
					{`@\w+`, chroma.NameDecorator, nil},
					{`[a-zA-Z_]\w*`, chroma.Name, nil},
					{`[{}()\[\],.:;]`, chroma.Punctuation, nil},
					{`[+\-*/%&|^~<>=!]`, chroma.Operator, nil},
				},
			}
		},
	))
}

func registerJSLexer(reg *chroma.LexerRegistry) {
	reg.Register(chroma.MustNewLexer(
		&chroma.Config{
			Name:      "JavaScript",
			Aliases:   []string{"javascript", "js"},
			Filenames: []string{"*.js", "*.jsx"},
		},
		func() chroma.Rules {
			return chroma.Rules{
				"root": {
					{`\s+`, chroma.Text, nil},
					{`//.*`, chroma.CommentSingle, nil},
					{`/\*.*?\*/`, chroma.CommentMultiline, nil},
					{`/[*](.|\n)*?[*]/`, chroma.CommentMultiline, nil},
					{`\b(as|async|await|break|case|catch|class|const|continue|debugger|default|delete|do|else|export|extends|finally|for|from|function|if|import|in|instanceof|let|new|of|return|static|super|switch|this|throw|try|typeof|var|void|while|with|yield)\b`, chroma.Keyword, nil},
					{`\b(true|false|null|undefined)\b`, chroma.KeywordConstant, nil},
					{`\b(Array|BigInt|Boolean|Function|Map|NaN|Number|Object|Promise|RegExp|Set|String|Symbol|WeakMap|WeakSet)\b`, chroma.KeywordType, nil},
					{`0[xX][0-9a-fA-F_]+`, chroma.LiteralNumberHex, nil},
					{`[0-9][0-9_]*(\.[0-9][0-9_]*)?`, chroma.LiteralNumberFloat, nil},
					{`"([^"\\]|\\.)*"`, chroma.LiteralStringDouble, nil},
					{`'([^'\\]|\\.)*'`, chroma.LiteralStringSingle, nil},
					{"`([^`\\\\]|\\\\.)*`", chroma.LiteralStringBacktick, nil},
					{`/[^/\n]+/[gimsuy]*`, chroma.LiteralStringRegex, nil},
					{`[a-zA-Z_$]\w*`, chroma.Name, nil},
					{`[{}()\[\],.:;]`, chroma.Punctuation, nil},
					{`[+\-*/%&|^~<>=!?]`, chroma.Operator, nil},
				},
			}
		},
	))
}

func registerTSLexer(reg *chroma.LexerRegistry) {
	reg.Register(chroma.MustNewLexer(
		&chroma.Config{
			Name:      "TypeScript",
			Aliases:   []string{"typescript", "ts"},
			Filenames: []string{"*.ts", "*.tsx"},
		},
		func() chroma.Rules {
			return chroma.Rules{
				"root": {
					{`\s+`, chroma.Text, nil},
					{`//.*`, chroma.CommentSingle, nil},
					{`/\*.*?\*/`, chroma.CommentMultiline, nil},
					{`\b(as|async|await|break|case|catch|class|const|continue|debugger|default|delete|do|else|export|extends|finally|for|from|function|if|implements|import|in|instanceof|interface|let|new|of|package|private|protected|public|readonly|return|static|super|switch|this|throw|try|type|typeof|var|void|while|with|yield)\b`, chroma.Keyword, nil},
					{`\b(true|false|null|undefined|any|never|unknown)\b`, chroma.KeywordConstant, nil},
					{`\b(Array|Boolean|Function|Map|NaN|Number|Object|Promise|RegExp|Set|String|Symbol|Promise)\b`, chroma.KeywordType, nil},
					{`0[xX][0-9a-fA-F_]+`, chroma.LiteralNumberHex, nil},
					{`[0-9][0-9_]*(\.[0-9][0-9_]*)?`, chroma.LiteralNumberFloat, nil},
					{`"([^"\\]|\\.)*"`, chroma.LiteralStringDouble, nil},
					{`'([^'\\]|\\.)*'`, chroma.LiteralStringSingle, nil},
					{"`([^`\\\\]|\\\\.)*`", chroma.LiteralStringBacktick, nil},
					{`[a-zA-Z_$]\w*`, chroma.Name, nil},
					{`[{}()\[\],.:;]`, chroma.Punctuation, nil},
					{`[+\-*/%&|^~<>=!?]`, chroma.Operator, nil},
				},
			}
		},
	))
}

func registerJSONLexer(reg *chroma.LexerRegistry) {
	reg.Register(chroma.MustNewLexer(
		&chroma.Config{
			Name:      "JSON",
			Aliases:   []string{"json"},
			Filenames: []string{"*.json"},
		},
		func() chroma.Rules {
			return chroma.Rules{
				"root": {
					{`\s+`, chroma.Text, nil},
					{`"([^"\\]|\\.)*"`, chroma.LiteralStringDouble, nil},
					{`-?(0|[1-9]\d*)(\.\d+)?([eE][+-]?\d+)?`, chroma.LiteralNumberFloat, nil},
					{`true|false`, chroma.KeywordConstant, nil},
					{`null`, chroma.KeywordConstant, nil},
					{`[{}[\],:]`, chroma.Punctuation, nil},
				},
			}
		},
	))
}

func registerYAMLLexer(reg *chroma.LexerRegistry) {
	reg.Register(chroma.MustNewLexer(
		&chroma.Config{
			Name:      "YAML",
			Aliases:   []string{"yaml", "yml"},
			Filenames: []string{"*.yaml", "*.yml"},
		},
		func() chroma.Rules {
			return chroma.Rules{
				"root": {
					{`\s+`, chroma.Text, nil},
					{`#.*$`, chroma.CommentSingle, nil},
					{`---`, chroma.Punctuation, nil},
					{`\.\.\.`, chroma.Punctuation, nil},
					{`[\-:]`, chroma.Punctuation, nil},
					{`"([^"\\]|\\.)*"`, chroma.LiteralStringDouble, nil},
					{`'([^'\\]|\\.)*'`, chroma.LiteralStringSingle, nil},
					{`[0-9][0-9_]*(\.[0-9][0-9_]*)?`, chroma.LiteralNumberFloat, nil},
					{`true|false|yes|no|on|off|null`, chroma.KeywordConstant, nil},
					{`[a-zA-Z_][\w/-]*`, chroma.Name, nil},
					{`\|>|`, chroma.Punctuation, nil},
				},
			}
		},
	))
}

func registerBashLexer(reg *chroma.LexerRegistry) {
	reg.Register(chroma.MustNewLexer(
		&chroma.Config{
			Name:      "Bash",
			Aliases:   []string{"bash", "sh", "shell", "zsh"},
			Filenames: []string{"*.sh", "*.bash"},
		},
		func() chroma.Rules {
			return chroma.Rules{
				"root": {
					{`\s+`, chroma.Text, nil},
					{`#.*$`, chroma.CommentSingle, nil},
					{`"([^"\\]|\\.)*"`, chroma.LiteralStringDouble, nil},
					{`'[^']*'`, chroma.LiteralStringSingle, nil},
					{"`[^`]*`", chroma.LiteralStringBacktick, nil},
					{`\b(if|then|else|elif|fi|for|while|do|done|case|esac|function|return|exit|continue|break|in|select|until)\b`, chroma.Keyword, nil},
					{`\b(echo|export|source|cd|pwd|ls|cat|grep|sed|awk|rm|mv|cp|mkdir|rmdir|touch|chmod|chown|find|xargs|sort|uniq|wc|head|tail|cut|tr|tee|date|sleep|kill|ps)\b`, chroma.NameBuiltin, nil},
					{`\$[a-zA-Z_]\w*`, chroma.NameVariable, nil},
					{`\$\{[^}]*\}`, chroma.NameVariable, nil},
					{`[0-9]+`, chroma.LiteralNumber, nil},
					{`[a-zA-Z_]\w*`, chroma.Name, nil},
					{`[{}()\[\]<>;&|]`, chroma.Punctuation, nil},
					{`[+\-*/%=!]`, chroma.Operator, nil},
				},
			}
		},
	))
}

func registerSQLLexer(reg *chroma.LexerRegistry) {
	reg.Register(chroma.MustNewLexer(
		&chroma.Config{
			Name:      "SQL",
			Aliases:   []string{"sql"},
			Filenames: []string{"*.sql"},
		},
		func() chroma.Rules {
			return chroma.Rules{
				"root": {
					{`\s+`, chroma.Text, nil},
					{`--.*`, chroma.CommentSingle, nil},
					{`/\*.*?\*/`, chroma.CommentMultiline, nil},
					{`\b(SELECT|FROM|WHERE|INSERT|INTO|VALUES|UPDATE|SET|DELETE|CREATE|TABLE|ALTER|DROP|INDEX|VIEW|JOIN|LEFT|RIGHT|INNER|OUTER|ON|AND|OR|NOT|IN|LIKE|BETWEEN|IS|NULL|AS|ORDER|BY|GROUP|HAVING|LIMIT|OFFSET|UNION|ALL|DISTINCT|CASE|WHEN|THEN|ELSE|END|EXISTS|WITH|RECURSIVE|PRIMARY|KEY|FOREIGN|REFERENCES|CONSTRAINT|DEFAULT|CHECK|UNIQUE|CASCADE)\b`, chroma.Keyword, nil},
					{`\b(INT|INTEGER|BIGINT|SMALLINT|TINYINT|VARCHAR|CHAR|TEXT|BOOLEAN|FLOAT|DOUBLE|DECIMAL|DATE|DATETIME|TIMESTAMP|BLOB|ENUM|SERIAL|JSON)\b`, chroma.KeywordType, nil},
					{`'[^']*'`, chroma.LiteralStringSingle, nil},
					{`[0-9]+(\.[0-9]+)?`, chroma.LiteralNumberFloat, nil},
					{`[a-zA-Z_]\w*`, chroma.Name, nil},
					{`[.,;()=*/+\-]`, chroma.Punctuation, nil},
				},
			}
		},
	))
}

func registerRustLexer(reg *chroma.LexerRegistry) {
	reg.Register(chroma.MustNewLexer(
		&chroma.Config{
			Name:      "Rust",
			Aliases:   []string{"rust", "rs"},
			Filenames: []string{"*.rs"},
		},
		func() chroma.Rules {
			return chroma.Rules{
				"root": {
					{`\s+`, chroma.Text, nil},
					{`//.*`, chroma.CommentSingle, nil},
					{`/\*.*?\*/`, chroma.CommentMultiline, nil},
					{`\b(as|async|await|break|const|continue|crate|dyn|else|enum|extern|false|fn|for|if|impl|in|let|loop|match|mod|move|mut|pub|ref|return|self|static|struct|super|trait|true|type|unsafe|use|where|while|yield)\b`, chroma.Keyword, nil},
					{`\b(bool|char|f32|f64|i8|i16|i32|i64|isize|str|String|u8|u16|u32|u64|usize|Vec|Option|Result|Box|HashMap|String)\b`, chroma.KeywordType, nil},
					{`0[xX][0-9a-fA-F_]+`, chroma.LiteralNumberHex, nil},
					{`[0-9][0-9_]*(\.[0-9][0-9_]*)?`, chroma.LiteralNumberFloat, nil},
					{`"([^"\\]|\\.)*"`, chroma.LiteralStringDouble, nil},
					{`'([^'\\]|\\.)*'`, chroma.LiteralStringChar, nil},
					{`r#?"[^"]*"`, chroma.LiteralString, nil},
					{`[a-zA-Z_]\w*`, chroma.Name, nil},
					{`[{}()\[\],.:;]`, chroma.Punctuation, nil},
					{`[+\-*/%&|^~<>=!@]`, chroma.Operator, nil},
				},
			}
		},
	))
}

// ---------- chroma style definition (inline, no XML imports) ----------

func darkChromaStyle() *chroma.Style {
	return chroma.MustNewStyle("godex-dark", chroma.StyleEntries{
		chroma.Text:                "#D4D4D4",
		chroma.Comment:             "#6A9955",
		chroma.CommentSingle:       "#6A9955",
		chroma.CommentMultiline:    "#6A9955",
		chroma.Keyword:             "#569CD6",
		chroma.KeywordType:         "#4EC9B0",
		chroma.KeywordDeclaration:  "#569CD6",
		chroma.KeywordConstant:     "#569CD6",
		chroma.KeywordNamespace:    "#C586C0",
		chroma.LiteralString:       "#CE9178",
		chroma.LiteralStringDouble: "#CE9178",
		chroma.LiteralStringSingle: "#CE9178",
		chroma.LiteralStringBacktick: "#CE9178",
		chroma.LiteralStringChar:   "#CE9178",
		chroma.LiteralStringDoc:    "#CE9178",
		chroma.LiteralStringRegex:  "#D16969",
		chroma.LiteralNumber:       "#B5CEA8",
		chroma.LiteralNumberFloat:  "#B5CEA8",
		chroma.LiteralNumberHex:    "#B5CEA8",
		chroma.LiteralNumberOct:    "#B5CEA8",
		chroma.Name:                "#D4D4D4",
		chroma.NameFunction:        "#DCDCAA",
		chroma.NameBuiltin:         "#DCDCAA",
		chroma.NameDecorator:       "#DCDCAA",
		chroma.NameVariable:        "#9CDCFE",
		chroma.NameClass:           "#4EC9B0",
		chroma.NameNamespace:       "#4EC9B0",
		chroma.NameAttribute:       "#9CDCFE",
		chroma.NameTag:             "#569CD6",
		chroma.Operator:            "#D4D4D4",
		chroma.Punctuation:         "#D4D4D4",
		chroma.Error:               "#F44747",
	})
}

func lightChromaStyle() *chroma.Style {
	return chroma.MustNewStyle("godex-light", chroma.StyleEntries{
		chroma.Text:                "#24292E",
		chroma.Comment:             "#6A737D",
		chroma.CommentSingle:       "#6A737D",
		chroma.CommentMultiline:    "#6A737D",
		chroma.Keyword:             "#D73A49",
		chroma.KeywordType:         "#6F42C1",
		chroma.KeywordDeclaration:  "#D73A49",
		chroma.KeywordConstant:     "#0550AE",
		chroma.KeywordNamespace:    "#6F42C1",
		chroma.LiteralString:       "#032F62",
		chroma.LiteralStringDouble: "#032F62",
		chroma.LiteralStringSingle: "#032F62",
		chroma.LiteralStringBacktick: "#032F62",
		chroma.LiteralStringChar:   "#032F62",
		chroma.LiteralStringDoc:    "#032F62",
		chroma.LiteralStringRegex:  "#032F62",
		chroma.LiteralNumber:       "#0550AE",
		chroma.LiteralNumberFloat:  "#0550AE",
		chroma.LiteralNumberHex:    "#0550AE",
		chroma.LiteralNumberOct:    "#0550AE",
		chroma.Name:                "#24292E",
		chroma.NameFunction:        "#6F42C1",
		chroma.NameBuiltin:         "#6F42C1",
		chroma.NameDecorator:       "#6F42C1",
		chroma.NameVariable:        "#E36209",
		chroma.NameClass:           "#6F42C1",
		chroma.NameNamespace:       "#6F42C1",
		chroma.NameAttribute:       "#E36209",
		chroma.NameTag:             "#0550AE",
		chroma.Operator:            "#24292E",
		chroma.Punctuation:         "#24292E",
		chroma.Error:               "#CF222E",
	})
}

// ---------- TTY256 formatter (inline, no formatters import) ----------

func tty256Formatter(w io.Writer, style *chroma.Style, iterator chroma.Iterator) error {
	for token := iterator(); token != chroma.EOF; token = iterator() {
		entry := style.Get(token.Type)
		writeANSIStyle(w, entry, token.Value)
	}
	return nil
}

func writeANSIStyle(w io.Writer, entry chroma.StyleEntry, text string) {
	var buf strings.Builder

	if entry.Colour.IsSet() {
		buf.WriteString(ansi256Fg(entry.Colour))
	}
	if entry.Background.IsSet() {
		buf.WriteString(ansi256Bg(entry.Background))
	}
	if entry.Bold == chroma.Yes {
		buf.WriteString("\033[1m")
	}
	if entry.Italic == chroma.Yes {
		buf.WriteString("\033[3m")
	}
	if entry.Underline == chroma.Yes {
		buf.WriteString("\033[4m")
	}

	ansi := buf.String()
	if ansi != "" {
		_, _ = io.WriteString(w, ansi)
	}

	crOrLf := strings.ContainsAny(text, "\r\n")
	if crOrLf {
		lines := strings.SplitAfter(text, "\n")
		for i, line := range lines {
			if line == "" {
				continue
			}
			if strings.HasSuffix(line, "\n") {
				_, _ = io.WriteString(w, line[:len(line)-1])
				if i < len(lines)-1 {
					_, _ = io.WriteString(w, "\033[0m\n")
				} else {
					_, _ = io.WriteString(w, "\n")
				}
			} else {
				_, _ = io.WriteString(w, line)
			}
			if i < len(lines)-1 && ansi != "" {
				_, _ = io.WriteString(w, ansi)
			}
		}
	} else {
		_, _ = io.WriteString(w, text)
	}

	if ansi != "" && !crOrLf {
		_, _ = io.WriteString(w, "\033[0m")
	}
}

func ansi256Fg(c chroma.Colour) string {
	r, g, b := c.Red(), c.Green(), c.Blue()
	idx := rgbToANSI256(r, g, b)
	return "\033[38;5;" + itoa(idx) + "m"
}

func ansi256Bg(c chroma.Colour) string {
	r, g, b := c.Red(), c.Green(), c.Blue()
	idx := rgbToANSI256(r, g, b)
	return "\033[48;5;" + itoa(idx) + "m"
}

func rgbToANSI256(r, g, b uint8) int {
	if abs(int(r)-int(g)) < 8 && abs(int(g)-int(b)) < 8 && abs(int(r)-int(b)) < 8 {
		gray := int(r)
		if gray > 238 {
			return 231
		}
		return 232 + gray/11
	}
	ir := int(r) / 43
	ig := int(g) / 43
	ib := int(b) / 43
	return 16 + ir*36 + ig*6 + ib
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [3]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
