package conversation

import (
	"errors"
	"regexp"
	"strconv"
	"strings"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
)

var responsesGPTVersion = regexp.MustCompile(`(?:^|--|/)gpt-(\d+)(?:\.(\d+))?(?:$|[-/])`)
var responsesNativeReplayModel = regexp.MustCompile(`(?:^|--|/)(?:gpt-[0-9]|o[134](?:-|$)|codex-)`)

// Older GPTs and non-OpenAI models retain their existing wire format. The
// compatible-gateway rejection fallback is scoped to a client and model.
func supportsResponsesExplicitCache(model string) bool {
	version := responsesGPTVersion.FindStringSubmatch(strings.ToLower(model))
	if version == nil {
		return false
	}
	major, _ := strconv.Atoi(version[1])
	minor, _ := strconv.Atoi(version[2])
	return major > 5 || (major == 5 && minor >= 6)
}

// Keep markers at earlier historical boundaries as well: moving a single
// marker to the newest result loses the previous cache lookup boundary.
// Runtime messages were not marked when first sent, so never mark them later.
func responsesCacheInput(items responses.ResponseInputParam) (responses.ResponseInputParam, int) {
	marked := append(responses.ResponseInputParam(nil), items...)
	count := 0
	for i, item := range items {
		// Native output items are opaque overrides: do not decode/re-encode
		// them, or scan potentially large encrypted reasoning strings.
		switch {
		case item.OfFunctionCallOutput != nil:
			output := *item.OfFunctionCallOutput
			text := output.Output.OfString.Value
			block := responses.ResponseInputTextContentParam{Text: text}
			block.SetExtraFields(map[string]any{"prompt_cache_breakpoint": map[string]any{"mode": "explicit"}})
			output.Output = responses.ResponseInputItemFunctionCallOutputOutputUnionParam{
				OfResponseFunctionCallOutputItemArray: responses.ResponseFunctionCallOutputItemListParam{{OfInputText: &block}},
			}
			marked[i] = responses.ResponseInputItemUnionParam{OfFunctionCallOutput: &output}
		case item.OfMessage != nil && (item.OfMessage.Role == "user" || item.OfMessage.Role == "developer"):
			message := *item.OfMessage
			content := message.Content.OfInputItemContentList
			if len(content) == 0 {
				text := message.Content.OfString.Value
				if text == "" || strings.HasPrefix(text, responsesRuntimeHeader) {
					continue
				}
				content = responses.ResponseInputMessageContentListParam{responses.ResponseInputContentParamOfInputText(text)}
			} else {
				if first := content[0].OfInputText; first != nil && strings.HasPrefix(first.Text, responsesRuntimeHeader) {
					continue
				}
				// The unmarked input is reused for compatibility retries. Clone
				// both the list and marked block instead of mutating that input.
				content = append(responses.ResponseInputMessageContentListParam(nil), content...)
			}
			last := &content[len(content)-1]
			switch {
			case last.OfInputText != nil:
				block := *last.OfInputText
				block.SetExtraFields(map[string]any{"prompt_cache_breakpoint": map[string]any{"mode": "explicit"}})
				last.OfInputText = &block
			case last.OfInputImage != nil:
				block := *last.OfInputImage
				block.SetExtraFields(map[string]any{"prompt_cache_breakpoint": map[string]any{"mode": "explicit"}})
				last.OfInputImage = &block
			default:
				continue
			}
			message.Content = responses.EasyInputMessageContentUnionParam{
				OfInputItemContentList: content,
			}
			marked[i] = responses.ResponseInputItemUnionParam{OfMessage: &message}
		default:
			continue
		}
		count++
	}
	return marked, count
}

func responsesCacheParameterRejected(err error) bool {
	var apiErr *openai.Error
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 400 {
		return false
	}
	message := strings.ToLower(apiErr.Param + " " + apiErr.Message)
	cacheField := strings.Contains(message, "prompt_cache_options") || strings.Contains(message, "prompt_cache_breakpoint")
	unsupported := strings.Contains(message, "unsupported") || strings.Contains(message, "unknown") ||
		strings.Contains(message, "unrecognized") || strings.Contains(message, "not supported") || strings.Contains(message, "extra inputs")
	return cacheField && unsupported
}
