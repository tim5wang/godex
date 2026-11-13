package retrieval

import (
	"strings"
	"unicode"
)

// RecallPlan is derived only from this conversation's original user messages.
// It is a search hint, never an instruction or a shared desktop goal.
type RecallPlan struct {
	Query string `json:"query"`
	Mode  string `json:"mode"` // direct, continuation, skip
}

// PlanRecall bounds both history and query size. Independent requests replace
// the topic; only explicit anaphora/continuations inherit it. Nothing is taken
// from assistant replies, tool outputs, summaries or a desktop's stored goal.
func PlanRecall(userTexts []string) RecallPlan {
	topic := ""
	plan := RecallPlan{Mode: "skip"}
	for _, raw := range userTexts[max(0, len(userTexts)-24):] {
		text := strings.TrimSpace(raw)
		key := compactRecallText(text)
		if key == "" {
			continue
		}
		plan = RecallPlan{Mode: "skip"}
		if recallStop[key] {
			topic = ""
			continue
		}
		if recallAcknowledgments[key] {
			continue
		}
		if responseOnlyRecall(text) {
			continue
		}
		switched := false
		for _, prefix := range []string{"换个话题", "换个任务", "另外问一下", "另一个问题", "new topic", "switch topic"} {
			if strings.HasPrefix(strings.ToLower(text), prefix) {
				topic = ""
				text = strings.TrimLeft(text[len(prefix):], " ，,。.:：!?！？\n")
				switched = true
				break
			}
		}
		if !switched && recallContinuation(text, topic) {
			if topic != "" {
				query := topic
				if continuationDetail.Replace(key) != "" {
					query += " " + text
				}
				plan = RecallPlan{Query: boundedRecallText(query, 384), Mode: "continuation"}
			}
			continue
		}
		if len(Terms(text)) == 0 {
			continue
		}
		topic = boundedRecallText(text, 256)
		plan = RecallPlan{Query: topic, Mode: "direct"}
	}
	return plan
}

var recallAcknowledgments = map[string]bool{
	"好": true, "好的": true, "嗯": true, "嗯嗯": true, "ok": true, "okay": true,
	"收到": true, "明白": true, "知道了": true, "了解了": true, "谢谢": true, "谢谢你": true,
	"thanks": true, "thankyou": true, "可以": true, "没问题": true,
	"你好": true, "早上好": true, "晚上好": true, "hello": true, "hi": true,
}
var recallStop = map[string]bool{"取消": true, "取消任务": true, "停止": true, "先这样": true, "不用了": true, "stop": true, "cancel": true}

var continuationDetail = strings.NewReplacer(
	"好的", "", "嗯嗯", "", "按这个顺序", "", "按这个", "", "照这个", "", "按计划", "", "照计划", "",
	"下一步", "", "下一批", "", "还有哪些", "", "还有什么", "", "剩下的", "", "优化计划", "",
	"continue", "", "please", "", "nextstep", "", "ok", "", "继续", "", "开始", "", "实施", "", "执行", "", "优化", "", "方案", "", "计划", "", "好", "", "请", "", "吧", "",
)

func responseOnlyRecall(text string) bool {
	if !strings.Contains(text, "用中文") && !strings.Contains(text, "简短回答") {
		return false
	}
	rest := compactRecallText(queryBoilerplate.Replace(strings.ToLower(text)))
	rest = strings.NewReplacer("以后都", "", "以后", "", "请", "", "回复", "", "回答", "", "四行", "", "简短", "", "一点", "").Replace(rest)
	return rest == ""
}

func recallContinuation(text, topic string) bool {
	key := compactRecallText(text)
	follow := false
	for _, cue := range []string{"继续", "下一步", "下一批", "还有哪些", "还有什么", "剩下的", "按这个", "照这个", "按计划", "照计划", "这个方案", "这个顺序", "这部分", "这个改动", "它的", "它会", "那性能", "那怎么", "那什么时候", "再试", "重试", "continue", "nextstep", "tryagain"} {
		follow = follow || strings.Contains(key, cue)
	}
	if !follow {
		return false
	}
	// A newly named technical object is a new query, even after "continue".
	for _, token := range entityToken.FindAllString(text, -1) {
		entities, _ := queryEntities(token)
		if len(entities) > 0 && !recallAcknowledgments[compactRecallText(token)] && !EntityNameMatches(topic, token) {
			return false
		}
	}
	return true
}

func compactRecallText(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, text)
}

func boundedRecallText(text string, limit int) string {
	runes := []rune(strings.TrimSpace(text))
	if len(runes) <= limit {
		return string(runes)
	}
	// Do not turn a truncated code identifier into a different strong anchor.
	for limit > 0 && runes[limit-1] < 128 && !unicode.IsSpace(runes[limit-1]) {
		limit--
	}
	return strings.TrimSpace(string(runes[:limit]))
}
