package retrieval

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRecallPlanContinuationSwitchAndAcknowledgment(t *testing.T) {
	topic := "修复 gopls 符号引用和重命名"
	for _, tt := range []struct {
		turns                    []string
		mode, contains, excludes string
	}{
		{[]string{topic, "好，继续下一步"}, "continuation", "gopls", ""},
		{[]string{topic, "OK，请继续"}, "continuation", "gopls", ""},
		{[]string{topic, "还有哪些优化计划"}, "continuation", "gopls", ""},
		{[]string{topic, "谢谢", "继续"}, "continuation", "gopls", ""},
		{[]string{topic, "谢谢"}, "skip", "", "gopls"},
		{[]string{topic, "请用中文简短回答"}, "skip", "", "gopls"},
		{[]string{topic, "请用中文输出四行"}, "skip", "", "gopls"},
		{[]string{topic, "以后都用中文回答", "继续"}, "continuation", "gopls", ""},
		{[]string{"继续下一步"}, "skip", "", "gopls"},
		{[]string{topic, "换个话题：PostgreSQL VACUUM 参数怎么设置"}, "direct", "PostgreSQL", "gopls"},
		{[]string{topic, "附近有什么川菜馆"}, "direct", "川菜馆", "gopls"},
		{[]string{topic, "继续研究 PostgreSQL VACUUM"}, "direct", "PostgreSQL", "gopls"},
		{[]string{topic, "取消任务", "继续"}, "skip", "", "gopls"},
		{[]string{topic, "换个话题", "继续"}, "skip", "", "gopls"},
	} {
		got := PlanRecall(tt.turns)
		if got.Mode != tt.mode || !strings.Contains(got.Query, tt.contains) || tt.excludes != "" && strings.Contains(got.Query, tt.excludes) {
			t.Errorf("%q => %+v", tt.turns, got)
		}
	}
	if got := PlanRecall([]string{topic, "好，继续下一步"}); got.Query != topic {
		t.Fatal("pure continuation added generic search terms", got)
	}
}

func TestRecallPlanBoundsAndConversationIsolation(t *testing.T) {
	a := PlanRecall([]string{"处理 gopls 引用", "继续"})
	b := PlanRecall([]string{"处理 Vision OCR", "继续"})
	if strings.Contains(a.Query, "Vision") || strings.Contains(b.Query, "gopls") {
		t.Fatal("task topic crossed conversations")
	}
	long := PlanRecall([]string{strings.Repeat("中文任务说明", 1000) + "尾部", "继续"})
	if !utf8.ValidString(long.Query) || len([]rune(long.Query)) > 384 {
		t.Fatal("unbounded or corrupt recall query")
	}
}
