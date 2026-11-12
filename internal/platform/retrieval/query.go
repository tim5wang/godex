// Package retrieval provides bounded query terms shared by durable and desktop memory.
package retrieval

import (
	"strings"
	"unicode"
)

var queryBoilerplate = strings.NewReplacer(
	"不要调用任何工具", " ", "不调用任何工具", " ", "仅根据本次注入的上下文", " ",
	"缺少的信息写", " ", "不要猜测", " ", "用中文输出", " ", "用中文回复", " ",
	"用中文", " ", "简短回答", " ", "保留已有精度", " ", "当前本地时刻", " ",
	"please", " ", "do not call tools", " ", "reply in chinese", " ",
)

var genericTerms = map[string]bool{
	"agent": true, "godex": true, "工具": true, "调用": true, "中文": true, "输出": true,
	"当前": true, "本次": true, "上下文": true, "提供": true, "未提供": true, "信息": true,
	"使用": true, "应该": true, "应使用": true, "四行": true, "文件位置": true,
}

// Terms strips response mechanics and splits Chinese phrases into overlapping
// bigrams/trigrams. A project name can scope a query without manufacturing a
// relevance hit. No query-rewrite model call is needed.
func Terms(query string) []string {
	query = queryBoilerplate.Replace(strings.ToLower(query))
	seen := map[string]bool{}
	out := []string{}
	add := func(s string) {
		if len([]rune(s)) >= 2 && !seen[s] && !genericTerms[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	var run []rune
	var han bool
	flush := func() {
		if len(run) == 0 {
			return
		}
		add(string(run))
		if han && len(run) > 3 {
			for _, n := range []int{3, 2} {
				for i := 0; i+n <= len(run); i++ {
					add(string(run[i : i+n]))
				}
			}
		}
		run = nil
	}
	for _, r := range query {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' {
			flush()
			continue
		}
		isHan := unicode.Is(unicode.Han, r)
		if len(run) > 0 && han != isHan {
			flush()
		}
		han = isHan
		run = append(run, r)
	}
	flush()
	if len(out) > 64 {
		out = out[len(out)-64:]
	}
	return out
}
