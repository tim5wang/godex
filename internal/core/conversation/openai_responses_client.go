package conversation

import (
	"context"
	"net/http"
	"strings"
	"sync"
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
// ChatGPT-codex-specific headers. Cache controls are model-aware, with a
// conservative fallback for compatible gateways that reject new fields.
//
// Providers that speak Responses include api.openai.com (gpt-5.x family),
// Azure OpenAI (responses preview), and compatible gateways. This client
// gives them the richer output model (structured reasoning + function_call
// output items, per-index tool-call argument deltas) without the
// chat.completions adapter layer.
type OpenAIResponsesClient struct {
	client           openai.Client
	origin           string
	cacheUnsupported sync.Map
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
	return &OpenAIResponsesClient{client: client, origin: responsesOrigin(baseURL, apiKey)}
}

// Call implements Caller (non-streaming path delegates to Stream, matching
// the codex client).
func (c *OpenAIResponsesClient) Call(ctx context.Context, req protocol.Request) (*protocol.Response, error) {
	return c.Stream(ctx, req, StreamHandler{})
}

// Stream implements StreamCaller.
func (c *OpenAIResponsesClient) Stream(ctx context.Context, req protocol.Request, handler StreamHandler) (*protocol.Response, error) {
	return streamResponses(ctx, req, handler, c.client, c.responsesParams(req), c.origin, true, &c.cacheUnsupported)
}

// responsesParams builds the Responses API request. It reuses the codex
// protocol→Responses converters. streamResponses adds native replay and cache
// boundaries after this common parameter setup; no codex headers are sent.
func (c *OpenAIResponsesClient) responsesParams(req protocol.Request) responses.ResponseNewParams {
	params := responses.ResponseNewParams{
		Model: req.Model,
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
	if retention := strings.TrimSpace(req.PromptCacheRetention); retention != "" && !supportsResponsesExplicitCache(req.Model) {
		switch retention {
		case "24h":
			params.PromptCacheRetention = responses.ResponseNewParamsPromptCacheRetention24h
		default:
			// Short retention is the API default. Omit it rather than sending
			// the SDK's "in-memory" enum: compatible gateways disagree on its
			// spelling (some only accept "in_memory").
		}
	}
	return params
}

// openAIResponsesInputFromProtocol keeps volatile runtime context in its own
// final message. Putting it inside a function_call_output changes that
// historical item on every request, which can prevent prompt-cache reuse
// through that message boundary. Keeping the tool result intact lets the cache
// reach its message boundary, while the changing context stays at the end.
func openAIResponsesInputFromProtocol(req protocol.Request) responses.ResponseInputParam {
	tail := strings.TrimSpace(req.RuntimeTail)
	req.RuntimeTail = ""
	items := codexInputFromProtocol(req)
	if tail == "" {
		return items
	}

	return append(items, responses.ResponseInputItemParamOfMessage(
		responsesRuntimeHeader+tail,
		responses.EasyInputMessageRoleUser,
	))
}
