package conversation

import (
	"context"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
	"github.com/tim5wang/godex/internal/contracts/protocol"
)

// Both Responses providers share native replay and stream parsing. Cache
// negotiation retries only a rejected HTTP request, never a started stream.
func streamResponses(ctx context.Context, req protocol.Request, handler StreamHandler, client openai.Client,
	params responses.ResponseNewParams, origin string, explicitAllowed bool, unsupported *sync.Map,
) (finalResp *protocol.Response, finalErr error) {
	start := time.Now()
	origin += ":" + req.Model
	replayEnabled := responsesNativeReplayModel.MatchString(strings.ToLower(req.Model))
	var input responses.ResponseInputParam
	var tail string
	var replayed int
	if replayEnabled {
		input, tail, replayed = responsesInputWithReplay(req, origin)
	} else {
		// Do not change DeepSeek/unknown compatible-provider history shaping
		// or send them native OpenAI-only output items.
		input = openAIResponsesInputFromProtocol(req)
		tail = req.RuntimeTail
	}
	params.Input = responses.ResponseNewParamsInputUnion{OfInputItemList: input}
	_, knownUnsupported := unsupported.Load(req.Model)
	explicit := explicitAllowed && supportsResponsesExplicitCache(req.Model) && !knownUnsupported
	markers := 0
	if explicit {
		params.Input.OfInputItemList, markers = responsesCacheInput(input)
		explicit = markers > 0
	}
	if explicit {
		params.PromptCacheRetention = ""
		params.SetExtraFields(map[string]any{"prompt_cache_options": map[string]any{"mode": "explicit", "ttl": "30m"}})
	}
	fallback := knownUnsupported
	defer func() {
		notifyUsage(ctx, UsageEvent{Request: req, Response: finalResp, Error: finalErr, Latency: time.Since(start), Stream: true,
			RequestDiagnostics: func() *RequestDiagnostics {
				return responsesRequestDiagnostics(params, origin, len(tail), replayed, explicit, fallback)
			},
		})
	}()
	for {
		stream := client.Responses.NewStreaming(ctx, params)
		state := &codexResponsesStreamState{toolCalls: make(map[int64]codexToolCallAcc)}
		started := false
		for stream.Next() {
			if !started {
				started = true
				if handler.OnStreamStarted != nil {
					handler.OnStreamStarted()
				}
			}
			if err := applyCodexResponsesSDKEvent(state, stream.Current(), handler); err != nil {
				_ = stream.Close()
				return nil, err
			}
		}
		err := stream.Err()
		_ = stream.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err != nil {
			if explicit && !started && responsesCacheParameterRejected(err) {
				unsupported.Store(req.Model, true)
				explicit, fallback = false, true
				params.SetExtraFields(nil)
				params.Input.OfInputItemList = input
				continue
			}
			return nil, err
		}
		if state.finishReason == "" {
			return nil, io.ErrUnexpectedEOF
		}
		resp := codexStreamStateToProtocol(state)
		if replayEnabled {
			attachResponsesReplay(resp, state, origin, tail)
		}
		return resp, nil
	}
}
