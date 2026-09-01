package conversation

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
	"github.com/tim5wang/godex/internal/contracts/protocol"
)

// OpenAIResponsesClient talks to any OpenAI Responses API endpoint via the
// openai-go SDK (the same wire protocol the OpenAICodexClient uses). Unlike
// the codex client it is a general-purpose provider client: it never sends
// ChatGPT-codex-specific headers, and it forwards the official automatic
// prefix caching knobs (prompt_cache_retention) instead of the codex
// deterministic prompt_cache_key.
//
// Providers that speak Responses include api.openai.com (gpt-5.x family),
// Azure OpenAI (responses preview), and compatible gateways. This client
// gives them the richer output model (structured reasoning + function_call
// output items, per-index tool-call argument deltas) without the
// chat.completions adapter layer.
type OpenAIResponsesClient struct {
	client openai.Client
}

// NewOpenAIResponsesClient creates a Responses client for an arbitrary
// base URL (defaults to the official API when empty).
func NewOpenAIResponsesClient(baseURL, apiKey string, timeout time.Duration) *OpenAIResponsesClient {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	httpClient := &http.Client{Transport: newDefaultTransport()}
	if timeout > 0 {
		httpClient.Timeout = timeout
	}
	client := openai.NewClient(
		option.WithAPIKey(apiKey),
		option.WithBaseURL(baseURL),
		option.WithHTTPClient(httpClient),
	)
	return &OpenAIResponsesClient{client: client}
}

// Call implements Caller (non-streaming path delegates to Stream, matching
// the codex client).
func (c *OpenAIResponsesClient) Call(ctx context.Context, req protocol.Request) (*protocol.Response, error) {
	return c.Stream(ctx, req, StreamHandler{})
}

// Stream implements StreamCaller.
func (c *OpenAIResponsesClient) Stream(ctx context.Context, req protocol.Request, handler StreamHandler) (*protocol.Response, error) {
	start := time.Now()
	var finalResp *protocol.Response
	var finalErr error
	defer func() {
		notifyUsage(ctx, UsageEvent{Request: req, Response: finalResp, Error: finalErr, Latency: time.Since(start), Stream: true})
	}()

	params := c.responsesParams(req)

	stream := c.client.Responses.NewStreaming(ctx, params)
	defer stream.Close()

	state := &codexResponsesStreamState{
		toolCalls: make(map[int64]codexToolCallAcc),
	}
	started := false
	for stream.Next() {
		evt := stream.Current()
		if !started {
			started = true
			if handler.OnStreamStarted != nil {
				handler.OnStreamStarted()
			}
		}
		if err := applyCodexResponsesSDKEvent(state, evt, handler); err != nil {
			finalErr = err
			return nil, err
		}
	}
	if err := stream.Err(); err != nil {
		if ctx.Err() != nil {
			finalErr = ctx.Err()
			return nil, ctx.Err()
		}
		finalErr = err
		return nil, err
	}
	response := codexStreamStateToProtocol(state)
	finalResp = response
	return response, nil
}

// responsesParams builds the Responses API request. It reuses the codex
// protocol→Responses converters (codexInputFromProtocol / codexToolsFromProtocol)
// and always uses the official-endpoint flavor: automatic prefix caching via
// prompt_cache_retention, no deterministic prompt_cache_key (all-or-nothing
// semantics are unsafe for a growing agent prefix), no codex headers.
func (c *OpenAIResponsesClient) responsesParams(req protocol.Request) responses.ResponseNewParams {
	params := responses.ResponseNewParams{
		Model: req.Model,
		Input: responses.ResponseNewParamsInputUnion{
			OfInputItemList: codexInputFromProtocol(req),
		},
		// Keep responses out of server-side storage; caching is independent.
		Store: param.NewOpt(false),
		Include: []responses.ResponseIncludable{
			responses.ResponseIncludableReasoningEncryptedContent,
		},
	}
	if req.MaxTokens > 0 {
		params.MaxOutputTokens = param.NewOpt(int64(req.MaxTokens))
	}
	if instructions := strings.TrimSpace(req.System); instructions != "" {
		params.Instructions = param.NewOpt(instructions)
	}
	if len(req.Tools) > 0 {
		params.Tools = codexToolsFromProtocol(req.Tools)
	}
	if effort := normalizeCodexReasoningEffort(req.ReasoningEffort); effort != "" {
		params.Reasoning = shared.ReasoningParam{
			Effort: shared.ReasoningEffort(effort),
			// summary:"auto" makes the API emit response.reasoning_summary_text.delta
			// events so frontends get live "thinking" output instead of encrypted
			// reasoning content only.
			Summary: shared.ReasoningSummaryAuto,
		}
	}
	if retention := strings.TrimSpace(req.PromptCacheRetention); retention != "" {
		switch retention {
		case "24h":
			params.PromptCacheRetention = responses.ResponseNewParamsPromptCacheRetention24h
		default:
			params.PromptCacheRetention = responses.ResponseNewParamsPromptCacheRetentionInMemory
		}
	}
	return params
}
