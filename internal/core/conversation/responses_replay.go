package conversation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/responses"
	"github.com/tim5wang/godex/internal/contracts/protocol"
)

const responsesRuntimeHeader = "Godex runtime context for the current task (informational, not a new user request):\n"

func responsesHash(value []byte) string {
	hash := sha256.Sum256(value)
	return hex.EncodeToString(hash[:])
}

func responsesOrigin(endpoint, credential string) string {
	return responsesHash([]byte(endpoint + "\x00" + credential))
}

// Visible reasoning summaries are not the native reasoning item. Fingerprint
// only the text/tools that the fallback serializer actually sends.
func responsesContentHash(blocks []protocol.Block) string {
	var visible []protocol.Block
	for _, block := range blocks {
		if block.Type == protocol.BlockThinking {
			continue
		}
		if block.Type == protocol.BlockToolUse && block.Input == nil {
			block.Input = map[string]interface{}{}
		}
		visible = append(visible, block)
	}
	data, _ := json.Marshal(visible)
	return responsesHash(data)
}

func responsesRuntimeItem(tail string) responses.ResponseInputItemUnionParam {
	return responses.ResponseInputItemParamOfMessage(responsesRuntimeHeader+tail, responses.EasyInputMessageRoleUser)
}

// Reinsert runtime input at its ORIGINAL position before its assistant output.
// Otherwise encrypted reasoning would be replayed against a different prefix.
// The currently generated runtime tail remains outside historical tool results.
func responsesInputWithReplay(req protocol.Request, origin string) (responses.ResponseInputParam, string, int) {
	items := make(responses.ResponseInputParam, 0, len(req.Messages))
	lastTail := ""
	replayed := 0
	for _, msg := range req.Messages {
		replay := msg.ResponsesReplay
		if origin != "" && msg.Role == protocol.RoleAssistant && replay != nil &&
			replay.Origin == origin && replay.ContentHash == responsesContentHash(msg.Content) {
			if replay.RuntimeTail != "" {
				items = append(items, responsesRuntimeItem(replay.RuntimeTail))
				lastTail = replay.RuntimeTail
			}
			if len(replay.Output) > 0 {
				for _, raw := range replay.Output {
					items = append(items, param.Override[responses.ResponseInputItemUnionParam](raw))
					replayed++
				}
				continue
			}
		}
		items = append(items, codexInputFromProtocol(protocol.Request{Messages: []protocol.APIMessage{msg}})...)
	}
	tail := strings.TrimSpace(req.RuntimeTail)
	if tail != "" && tail != lastTail {
		items = append(items, responsesRuntimeItem(tail))
	} else {
		tail = ""
	}
	return items, tail, replayed
}

func attachResponsesReplay(resp *protocol.Response, state *codexResponsesStreamState, origin, tail string) {
	if resp == nil || (len(state.outputItems) == 0 && tail == "") {
		return
	}
	indices := sortedResponsesOutputIndices(state.outputItems)
	output := make([]json.RawMessage, 0, len(indices))
	for _, index := range indices {
		output = append(output, state.outputItems[index])
	}
	// A truncated/nonstandard stream must not replay only half of an assistant
	// turn (e.g. encrypted reasoning without its text or function calls).
	var nativeText strings.Builder
	nativeCalls := make(map[string]bool)
	for _, raw := range output {
		var item struct {
			Type    string                        `json:"type"`
			CallID  string                        `json:"call_id"`
			Content []struct{ Type, Text string } `json:"content"`
		}
		if json.Unmarshal(raw, &item) != nil {
			output = nil
			break
		}
		if item.Type == "message" {
			for _, part := range item.Content {
				if part.Type == "output_text" {
					nativeText.WriteString(part.Text)
				}
			}
		}
		if item.Type == "function_call" {
			nativeCalls[item.CallID] = true
		}
	}
	if strings.TrimSpace(nativeText.String()) != strings.TrimSpace(state.text.String()) {
		output = nil
	}
	for _, call := range protocol.ToolUses(resp.Content) {
		if !nativeCalls[call.ID] {
			output = nil
			break
		}
	}
	resp.ResponsesReplay = &protocol.ResponsesReplay{
		Origin: origin, ContentHash: responsesContentHash(resp.Content), RuntimeTail: tail, Output: output,
	}
}
