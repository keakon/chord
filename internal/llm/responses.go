package llm

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/keakon/golog/log"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/ratelimit"
)

// ResponsesProvider implements streaming completion against the OpenAI Responses API.
// The Responses API uses a different wire format than Chat Completions:
// - Request: `input` array of items instead of `messages`
// - Response: item-based events (response.output_item.added, etc.)
// - Tool calls: `function_call` items instead of `tool_calls` in assistant messages
type ResponsesProvider struct {
	provider     *ProviderConfig
	client       *http.Client
	dumpWriter   *DumpWriter
	proxyScheme  string
	dialProxyURL string
	// codexWSCompleteFn is a test seam for the Codex WebSocket path. Production
	// uses completeStreamCodexWebSocket directly.
	codexWSCompleteFn func(context.Context, string, string, string, *responsesRequest, []responsesInputItem, StreamCallback, time.Time) (*message.Response, bool, error)

	codexWSMu             sync.Mutex
	codexWSConn           *websocket.Conn
	codexWSStickyDisabled atomic.Bool
	codexWSLastKey        string
	codexWSLastModel      string
	codexWSLastRespID     string
	codexWSLastInpLen     int
	codexWSLastInpSig     string
	codexWSLastReqSig     string
	codexWSPromptCacheKey string
	sessionID             string
	lastTransportUsed     atomic.Value // string: "websocket" | "http"
}

// NewResponsesProviderWithClient creates a new ResponsesProvider using a caller-supplied HTTP client.
func NewResponsesProviderWithClient(provider *ProviderConfig, client *http.Client, proxyURL string) (*ResponsesProvider, error) {
	return &ResponsesProvider{
		provider:     provider,
		client:       client,
		proxyScheme:  ProxyScheme(proxyURL),
		dialProxyURL: strings.TrimSpace(proxyURL),
	}, nil
}

// NewResponsesProvider creates a new ResponsesProvider.
func NewResponsesProvider(provider *ProviderConfig, proxyURL string) (*ResponsesProvider, error) {
	client, err := NewHTTPClientWithProxy(proxyURL, 0)
	if err != nil {
		return nil, fmt.Errorf("create HTTP client for responses provider: %w", err)
	}
	return &ResponsesProvider{
		provider:     provider,
		client:       client,
		proxyScheme:  ProxyScheme(proxyURL),
		dialProxyURL: strings.TrimSpace(proxyURL),
	}, nil
}

func (r *ResponsesProvider) SetDumpWriter(w *DumpWriter) {
	r.dumpWriter = w
}

func (r *ResponsesProvider) LastTransportUsed() string {
	if r == nil {
		return ""
	}
	if v := r.lastTransportUsed.Load(); v != nil {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// responsesRequest is the request body for the Responses API.
type responsesRequest struct {
	Model              string               `json:"model"`
	Instructions       *string              `json:"instructions,omitempty"`
	Input              []responsesInputItem `json:"input"`
	Tools              []responsesTool      `json:"tools,omitempty"`
	ParallelToolCalls  *bool                `json:"parallel_tool_calls,omitempty"`
	MaxOutputTokens    int                  `json:"max_output_tokens,omitempty"`
	Reasoning          *reasoningConfig     `json:"reasoning,omitempty"`
	Text               *textConfig          `json:"text,omitempty"`
	PreviousResponseID string               `json:"previous_response_id,omitempty"`
	Store              *bool                `json:"store,omitempty"`
	Stream             bool                 `json:"stream"`
	PromptCacheKey     string               `json:"prompt_cache_key,omitempty"`
}

// responsesInputItem represents an item in the Responses API input array.
// The API expects "arguments" to be a string (JSON-serialized object), not an object.
type responsesInputItem struct {
	Type      string  `json:"type"` // "message", "function_call", "function_call_output"
	Role      string  `json:"role,omitempty"`
	Content   any     `json:"content,omitempty"`
	Name      string  `json:"name,omitempty"`
	CallID    string  `json:"call_id,omitempty"`
	Output    *string `json:"output,omitempty"`    // pointer: nil→omitted for message/function_call; non-nil (even "")→included for function_call_output
	Arguments string  `json:"arguments,omitempty"` // JSON object as string per API spec
}

// responsesContentBlock is a content block within a message item.
type responsesContentBlock struct {
	Type     string `json:"type"` // "input_text", "output_text", "input_image"
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

// responsesTool is a tool definition for the Responses API.
// The Responses API expects "parameters" (not "params") for tool schemas.
type responsesTool struct {
	Type       string         `json:"type"`
	Name       string         `json:"name"`
	Parameters map[string]any `json:"parameters,omitempty"`
}

// reasoningConfig configures reasoning models.
type reasoningConfig struct {
	Effort  string `json:"effort,omitempty"`  // e.g. "low"|"medium"|"high"|"xhigh"
	Summary string `json:"summary,omitempty"` // "auto"|"concise"|"detailed"
}

type textConfig struct {
	Verbosity string `json:"verbosity,omitempty"` // "low"|"medium"|"high"
}

func (r *ResponsesProvider) CompleteStream(
	ctx context.Context,
	apiKey string,
	model string,
	systemPrompt string,
	messages []message.Message,
	tools []message.ToolDefinition,
	maxTokens int,
	tuning RequestTuning,
	cb StreamCallback,
) (*message.Response, error) {
	ot := tuning.OpenAI
	useOpenAIOAuth := r.provider != nil && r.provider.isOpenAIOAuthKey(apiKey)
	url := r.provider.APIURL()
	if useOpenAIOAuth {
		url = resolveOpenAIOAuthAPIURL(url)
	}

	// Validate API URL.
	// Validate API URL.
	path := strings.TrimSuffix(url, "/")
	if !strings.HasSuffix(path, "/responses") {
		log.Warnf("ResponsesProvider called with non-Responses API URL model=%v api_url=%v expected=%v", model, url, "*/responses")
	}

	// Convert messages to Responses API format.
	conversionSystemPrompt := systemPrompt
	if useOpenAIOAuth || (r.provider != nil && r.provider.IsCodexOAuthTransport()) {
		conversionSystemPrompt = ""
	}
	apiInput := convertMessagesToResponses(conversionSystemPrompt, messages)

	// Validate that we have at least one input item.
	if len(apiInput) == 0 {
		return nil, fmt.Errorf("responses API requires at least one input item (system prompt or user message), system_prompt=%q messages_len=%d", systemPrompt, len(messages))
	}

	// Debug: log input length
	log.Debugf("responses input system_prompt_len=%v messages_len=%v api_input_len=%v", len(systemPrompt), len(messages), len(apiInput))

	// Convert tools.
	apiTools := convertToolsToResponses(tools)

	// Determine effective store setting.
	var modelStore *bool
	if r.provider != nil {
		if m, ok := r.provider.GetModel(model); ok {
			modelStore = m.Store
		}
	}
	var providerStore *bool
	if r.provider != nil {
		providerStore = r.provider.StoreConfig()
	}
	effectiveStore := config.EffectiveStore(providerStore, modelStore)
	// HTTP previous_response_id reuse is intentionally disabled for current Codex
	// behavior; store remains relevant only for non-OAuth Responses-compatible
	// backends that support server-side retention.
	// Official ChatGPT/Codex OAuth Responses API returns 400 if store is true.
	// Apply suppression at provider level too: even non-OAuth keys on a Codex OAuth
	// transport provider must not send store=true.
	if (useOpenAIOAuth || (r.provider != nil && r.provider.IsCodexOAuthTransport())) && effectiveStore {
		log.Warnf("responses: Codex OAuth transport requires store=false on wire; ignoring store config model=%v", model)
		effectiveStore = false
	}

	// HTTP path is full-input only for current Codex OAuth transport behavior.
	// We do not send previous_response_id here; connection-scoped reuse belongs to WebSocket.
	fullInput := apiInput

	// Build request body.
	reqBody := responsesRequest{
		Model:  model,
		Tools:  apiTools,
		Stream: true,
	}
	reqBody.Input = fullInput
	if r.sessionID != "" {
		reqBody.PromptCacheKey = r.sessionID
	}
	if effectiveStore {
		v := true
		reqBody.Store = &v
	} else if useOpenAIOAuth || (r.provider != nil && r.provider.IsCodexOAuthTransport()) {
		// Codex OAuth HTTP endpoint treats absent store field as true; send explicit false.
		v := false
		reqBody.Store = &v
	}
	log.Debugf("responses: full model=%v store=%v input_len=%v", model, effectiveStore, len(fullInput))
	// Set instructions for Codex OAuth transport
	if useOpenAIOAuth || (r.provider != nil && r.provider.IsCodexOAuthTransport()) {
		instructions := systemPrompt
		reqBody.Instructions = &instructions
	}
	reqBody.ParallelToolCalls = cloneBoolPtr(ot.ParallelToolCalls)

	effectiveReasoningEffort := ot.ReasoningEffort
	effectiveMaxTokens := maxTokens
	useCodexTransport := r.provider != nil && r.provider.IsCodexOAuthTransport()
	if useOpenAIOAuth || useCodexTransport {
		if normalized, changed := normalizeOpenAIOAuthReasoningEffort(ot.ReasoningEffort); changed {
			if normalized == "" {
				log.Warnf("omitting unsupported reasoning effort for Codex transport requested=%v", ot.ReasoningEffort)
			} else {
				log.Warnf("normalizing reasoning effort for Codex transport requested=%v effective=%v", ot.ReasoningEffort, normalized)
			}
			effectiveReasoningEffort = normalized
		}
		if effectiveMaxTokens > 0 {
			log.Debugf("omitting max_output_tokens for Codex transport requested=%v", effectiveMaxTokens)
			effectiveMaxTokens = 0
		}
	}

	if effectiveReasoningEffort != "" {
		reqBody.Reasoning = &reasoningConfig{Effort: effectiveReasoningEffort, Summary: ot.ReasoningSummary}
	}
	if ot.TextVerbosity != "" {
		reqBody.Text = &textConfig{Verbosity: ot.TextVerbosity}
	}

	if effectiveMaxTokens > 0 {
		reqBody.MaxOutputTokens = effectiveMaxTokens
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request body: %w", err)
	}
	dumpRequestBody := append([]byte(nil), bodyBytes...)

	log.Debugf("responses request model=%v max_output_tokens=%v messages=%v tools=%v reasoning_effort=%v reasoning_summary=%v request_bytes=%v", model, effectiveMaxTokens, len(messages), len(tools), effectiveReasoningEffort, ot.ReasoningSummary, len(bodyBytes))

	start := time.Now()
	if useOpenAIOAuth && r.provider != nil && r.provider.IsCodexOAuthTransport() && r.provider.EffectiveResponsesWebsocket() && !r.codexWSStickyDisabled.Load() {
		wsComplete := r.codexWSCompleteFn
		if wsComplete == nil {
			wsComplete = r.completeStreamCodexWebSocket
		}
		wsResp, wsUsedIncremental, wsErr := wsComplete(ctx, url, apiKey, model, &reqBody, fullInput, cb, start)
		if wsErr == nil {
			r.lastTransportUsed.Store("websocket")
			return wsResp, nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			r.resetCodexWebSocketChain("context_cancel")
			return nil, fmt.Errorf("responses websocket aborted: %w", ctxErr)
		}
		if wsUsedIncremental {
			log.Warnf("responses: Codex WebSocket incremental failed, retrying full request on websocket error=%v model=%v", wsErr, model)
			r.resetCodexWebSocketChain("error_fallback")
			wsResp, _, retryErr := wsComplete(ctx, url, apiKey, model, &reqBody, fullInput, cb, start)
			if retryErr == nil {
				r.lastTransportUsed.Store("websocket")
				return wsResp, nil
			}
			wsErr = retryErr
			if ctxErr := ctx.Err(); ctxErr != nil {
				r.resetCodexWebSocketChain("context_cancel")
				return nil, fmt.Errorf("responses websocket aborted: %w", ctxErr)
			}
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			r.resetCodexWebSocketChain("context_cancel")
			return nil, fmt.Errorf("responses websocket aborted: %w", ctxErr)
		}
		if isCodexWSProtocolStickyError(wsErr) {
			r.codexWSStickyDisabled.Store(true)
			r.resetCodexWebSocketChain("ws_sticky_disabled")
			log.Warnf("responses: Codex WebSocket disabled for this process after protocol error error=%v", wsErr)
		} else {
			log.Warnf("responses: Codex WebSocket failed, falling back to HTTP error=%v", wsErr)
			r.resetCodexWebSocketChain("ws_fallback_http")
		}
	}

	r.lastTransportUsed.Store("http")
	resp, parseErr := r.sendAndParse(ctx, url, bodyBytes, dumpRequestBody, model, apiKey, useOpenAIOAuth, cb)

	// HTTP full-input path: no previous_response_id retry/rollback handling required.

	return resp, parseErr
}

// sendAndParse sends a Responses API request and parses the SSE stream.
func (r *ResponsesProvider) sendAndParse(
	ctx context.Context,
	url string,
	bodyBytes []byte,
	dumpRequestBody []byte,
	model string,
	apiKey string,
	useOpenAIOAuth bool,
	cb StreamCallback,
) (*message.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("responses request aborted: %w", err)
	}
	// Build HTTP request with a derived context for per-chunk timeout enforcement.
	streamCtx, streamCancel := context.WithCancel(ctx)
	defer streamCancel()
	req, err := http.NewRequestWithContext(streamCtx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	if useOpenAIOAuth {
		applyOpenAIOAuthHeaders(req, r.provider, apiKey)
	} else {
		req.Header.Set("Authorization", "Bearer "+apiKey)
		req.Header.Set("OpenAI-Beta", "responses=v1")
	}

	// Apply request body compression if configured
	req, _ = compressRequestBody(req, bodyBytes, r.provider.CompressEnabled())

	// Send request.
	start := time.Now()
	if r.proxyScheme != "" {
		log.Debugf("LLM request via proxy provider=%v scheme=%v", "responses", r.proxyScheme)
	}
	if cb != nil {
		cb(message.StreamDelta{Type: "status", Status: &message.StatusDelta{Type: "connecting"}})
	}
	httpResp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer httpResp.Body.Close()

	// Handle gzip response if server supports it
	if httpResp.Header.Get("Content-Encoding") == "gzip" {
		gr, err := gzip.NewReader(httpResp.Body)
		if err != nil {
			return nil, fmt.Errorf("create gzip reader: %w", err)
		}
		httpResp.Body = gr
	}

	if cb != nil {
		cb(message.StreamDelta{Type: "status", Status: &message.StatusDelta{Type: "waiting_headers"}, Progress: &message.StreamProgressDelta{Bytes: responseHeaderBytes(httpResp)}})
	}

	// Handle non-2xx responses.
	if httpResp.StatusCode != http.StatusOK {
		// Read up to 4KB for error logging to avoid memory issues with large responses.
		errBody, _ := io.ReadAll(io.LimitReader(httpResp.Body, 4096))
		// Discard any remaining body content to ensure clean connection reuse.
		io.Copy(io.Discard, httpResp.Body) //nolint:errcheck
		log.Debugf("responses error response status=%v body_len=%v", httpResp.StatusCode, len(errBody))
		// For preset: codex OAuth 429: parse x-codex-* headers on the error response so
		// markKeyCooldown can use the window reset time when Retry-After is absent.
		if useOpenAIOAuth && httpResp.StatusCode == 429 {
			if snap := ratelimit.ParseCodexHeaders(httpResp.Header); snap != nil {
				snap.Provider = r.provider.Name()
				r.provider.UpdateKeySnapshot(apiKey, snap)
				if cb != nil {
					cb(message.StreamDelta{Type: "rate_limits", RateLimit: snap})
				}
			}
		}
		apiErr := parseOpenAIHTTPErrorFromBytes(httpResp.StatusCode, httpResp.Header, errBody)
		if r.dumpWriter != nil {
			dumpWriter := r.dumpWriter
			go func() {
				dump := &LLMDump{
					Timestamp:   start.Format(time.RFC3339Nano),
					Provider:    "responses",
					Model:       model,
					RequestBody: dumpRequestBody,
					Error:       apiErr.Error(),
					DurationMS:  time.Since(start).Milliseconds(),
				}
				if wErr := dumpWriter.Write(dump); wErr != nil {
					log.Warnf("failed to write LLM dump error=%v", wErr)
				}
			}()
		}
		return nil, apiErr
	}

	// Parse OpenAI Codex OAuth rate-limit headers from the 200 response and notify via callback.
	if useOpenAIOAuth {
		if snap := ratelimit.ParseCodexHeaders(httpResp.Header); snap != nil {
			snap.Provider = r.provider.Name()
			r.provider.UpdateKeySnapshot(apiKey, snap)
			if cb != nil {
				cb(message.StreamDelta{Type: "rate_limits", RateLimit: snap})
			}
		}
	}

	// Parse SSE stream.
	var collector *SSECollector
	if r.dumpWriter != nil {
		collector = NewSSECollector()
	}
	cr := NewChunkTimeoutReader(httpResp.Body, DefaultChunkTimeout, streamCancel)
	defer cr.Stop()
	resp, parseErr := parseResponsesSSE(cr, cb, collector)
	if parseErr != nil {
		var timeoutErr *ChunkTimeoutError
		if errors.As(parseErr, &timeoutErr) {
			snap := cr.chunkTimeoutSnapshot()
			attrs := []any{
				"model", model,
				"error", parseErr,
				"chunk_timeout", snap.Timeout,
				"timed_out", snap.TimedOut,
				"timeout_read_returned", snap.TimeoutReadReturned,
				"timeout_read_bytes", snap.TimeoutReadBytes,
				"last_read_bytes", snap.LastReadBytes,
				"last_read_err", snap.LastReadErr,
				"total_bytes", snap.TotalBytes,
			}
			if !snap.LastByteAt.IsZero() {
				attrs = append(attrs, "since_last_byte_ms", time.Since(snap.LastByteAt).Milliseconds())
			}
			if !snap.TimeoutFiredAt.IsZero() {
				attrs = append(attrs, "since_timeout_ms", time.Since(snap.TimeoutFiredAt).Milliseconds())
			}
			log.Warnf("responses: SSE stream timed out %v", attrs)
		}
	}

	// Write dump asynchronously.
	if r.dumpWriter != nil {
		dumpWriter := r.dumpWriter
		go func() {
			dump := &LLMDump{
				Timestamp:   start.Format(time.RFC3339Nano),
				Provider:    "responses",
				Model:       model,
				RequestBody: dumpRequestBody,
				SSEChunks:   collector.Chunks(),
				Response:    DumpResponseFromResponse(resp),
				DurationMS:  time.Since(start).Milliseconds(),
			}
			if parseErr != nil {
				dump.Error = parseErr.Error()
			}
			if wErr := dumpWriter.Write(dump); wErr != nil {
				log.Warnf("failed to write LLM dump error=%v", wErr)
			}
		}()
	}

	return resp, parseErr
}
