package llm

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/keakon/golog/log"

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
	dumpWriter   atomic.Pointer[DumpWriter]
	traceWriter  atomic.Pointer[TraceWriter]
	proxyScheme  string
	dialProxyURL string
	// codexWSCompleteFn is a test seam for the Codex WebSocket path. Production
	// uses completeStreamCodexWebSocket directly.
	codexWSCompleteFn func(context.Context, string, string, string, *responsesRequest, []responsesInputItem, StreamCallback, time.Time, codexWSCompleteOptions) (*message.Response, bool, error)

	codexWSMu             sync.Mutex
	codexWSConn           *websocket.Conn
	codexWSLastKey        string
	codexWSLastAPIURL     string
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
	client, err := NewStreamingHTTPClientWithProxy(proxyURL, providerResponseHeaderTimeout(provider))
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
	r.dumpWriter.Store(w)
}

func (r *ResponsesProvider) SetTraceWriter(w *TraceWriter) {
	r.traceWriter.Store(w)
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
	Tools              []responsesTool      `json:"tools"`
	ToolChoice         string               `json:"tool_choice"`
	ParallelToolCalls  bool                 `json:"parallel_tool_calls"`
	ServiceTier        string               `json:"service_tier,omitempty"`
	Reasoning          *reasoningConfig     `json:"reasoning,omitempty"`
	Text               *textConfig          `json:"text,omitempty"`
	PreviousResponseID string               `json:"previous_response_id,omitempty"`
	Store              bool                 `json:"store"`
	Stream             bool                 `json:"stream"`
	Include            []string             `json:"include"`
	PromptCacheKey     string               `json:"prompt_cache_key,omitempty"`
	ClientMetadata     map[string]string    `json:"client_metadata,omitempty"`
}

func (r responsesRequest) MarshalJSON() ([]byte, error) {
	type alias responsesRequest
	r.Input = normalizeResponsesInput(r.Input)
	r.Tools = normalizeResponsesTools(r.Tools)
	r.Include = normalizeResponsesInclude(r.Include)
	return json.Marshal(alias(r))
}

// responsesInputItem represents an item in the Responses API input array.
// The API expects "arguments" to be a string (JSON-serialized object), not an object.
type responsesInputItem struct {
	Type      string `json:"type"` // "message", "function_call", "function_call_output"
	Role      string `json:"role,omitempty"`
	Content   any    `json:"content,omitempty"`
	Name      string `json:"name,omitempty"`
	CallID    string `json:"call_id,omitempty"`
	Output    any    `json:"output,omitempty"`    // string or []responsesContentBlock for function_call_output
	Arguments string `json:"arguments,omitempty"` // JSON object as string per API spec
}

// responsesContentBlock is a content block within a message item.
type responsesContentBlock struct {
	Type     string `json:"type"` // "input_text", "output_text", "input_image", "input_file"
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
	Detail   string `json:"detail,omitempty"`
	Filename string `json:"filename,omitempty"`  // input_file: display filename
	FileData string `json:"file_data,omitempty"` // input_file: data URL (data:application/pdf;base64,...)
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

const responsesEncryptedReasoningInclude = "reasoning.encrypted_content"

const (
	responsesClientMetadataInstallationID = "x-codex-installation-id"
	responsesClientMetadataSessionID      = "session_id"
	responsesClientMetadataThreadID       = "thread_id"
	responsesClientMetadataTurnID         = "turn_id"
	responsesClientMetadataTurnMetadata   = "x-codex-turn-metadata"
	responsesClientMetadataWindowID       = "x-codex-window-id"
)

func responsesIncludeForReasoning(reasoning *reasoningConfig) []string {
	if reasoning == nil {
		return []string{}
	}
	return []string{responsesEncryptedReasoningInclude}
}

func normalizeResponsesInput(input []responsesInputItem) []responsesInputItem {
	if input == nil {
		return []responsesInputItem{}
	}
	return input
}

func normalizeResponsesTools(tools []responsesTool) []responsesTool {
	if tools == nil {
		return []responsesTool{}
	}
	return tools
}

func normalizeResponsesInclude(include []string) []string {
	if include == nil {
		return []string{}
	}
	return include
}

func responsesConfiguredStore(provider *ProviderConfig, model string) bool {
	var modelStore *bool
	if provider != nil {
		if m, ok := provider.GetModel(model); ok {
			modelStore = m.Store
		}
	}
	var providerStore *bool
	if provider != nil {
		providerStore = provider.StoreConfig()
	}
	return config.EffectiveStore(providerStore, modelStore)
}

type responsesTurnMetadataPayload struct {
	InstallationID         string `json:"installation_id,omitempty"`
	SessionID              string `json:"session_id,omitempty"`
	ThreadID               string `json:"thread_id,omitempty"`
	ThreadSource           string `json:"thread_source,omitempty"`
	TurnID                 string `json:"turn_id,omitempty"`
	WindowID               string `json:"window_id,omitempty"`
	Sandbox                string `json:"sandbox,omitempty"`
	RequestKind            string `json:"request_kind,omitempty"`
	TurnStartedAtUnixMilli int64  `json:"turn_started_at_unix_ms,omitempty"`
}

func responsesWindowID(sessionID string) string {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return ""
	}
	return sessionID + ":0"
}

func responsesInstallationID(sessionID string) string {
	sum := sha256.Sum256([]byte("chord-responses-installation:" + strings.TrimSpace(sessionID)))
	sum[6] = (sum[6] & 0x0f) | 0x40
	sum[8] = (sum[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", sum[0:4], sum[4:6], sum[6:8], sum[8:10], sum[10:16])
}

func responsesClientMetadata(sessionID string, startedAt time.Time) map[string]string {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	windowID := responsesWindowID(sessionID)
	turnID := newOpenAIOAuthSessionID()
	installationID := responsesInstallationID(sessionID)
	metadata := map[string]string{
		responsesClientMetadataInstallationID: installationID,
		responsesClientMetadataSessionID:      sessionID,
		responsesClientMetadataThreadID:       sessionID,
		responsesClientMetadataTurnID:         turnID,
		responsesClientMetadataWindowID:       windowID,
	}
	turnMetadata := responsesTurnMetadataPayload{
		InstallationID:         installationID,
		SessionID:              sessionID,
		ThreadID:               sessionID,
		ThreadSource:           "user",
		TurnID:                 turnID,
		WindowID:               windowID,
		Sandbox:                "none",
		RequestKind:            "turn",
		TurnStartedAtUnixMilli: startedAt.UnixMilli(),
	}
	if data, err := json.Marshal(turnMetadata); err == nil {
		metadata[responsesClientMetadataTurnMetadata] = string(data)
	}
	return metadata
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
	dumpWriter := r.dumpWriter.Load()
	traceWriter := r.traceWriter.Load()
	traceCollector := newLLMTraceCollector("responses", model, cb)
	traceCB := traceCollector.Callback
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

	// Convert messages to Responses API format. System/developer instructions are
	// sent through the top-level instructions field (matching Codex) instead of as
	// a system-role input message; some Responses-compatible backends reject typed
	// system messages in input.
	apiInput := convertMessagesToResponses("", providerWireFamily(r.provider), messages)

	// Validate that we have at least one input item.
	if len(apiInput) == 0 {
		return nil, fmt.Errorf("responses API requires at least one input item (system prompt or user message), system_prompt=%q messages_len=%d", systemPrompt, len(messages))
	}

	// Debug: log input length
	log.Debugf("responses input system_prompt_len=%v messages_len=%v api_input_len=%v", len(systemPrompt), len(messages), len(apiInput))

	// Convert tools.
	apiTools := convertToolsToResponses(tools)

	// HTTP path is full-input only. We do not send previous_response_id here;
	// connection-scoped reuse belongs to the Codex WebSocket transport.
	fullInput := apiInput
	requestStartedAt := time.Now()

	// Keep the Responses HTTP request shape aligned with codex-rs for every
	// Responses provider, not only preset:codex. Some relay endpoints validate
	// these fields as the Responses client contract and reject narrower OpenAI
	// samples that omit them.
	reqBody := responsesRequest{
		Model:      model,
		Tools:      apiTools,
		ToolChoice: "auto",
		Store:      responsesConfiguredStore(r.provider, model),
		Stream:     true,
		Include:    []string{},
	}
	if ot.ToolChoice != "" {
		reqBody.ToolChoice = ot.ToolChoice
	}
	reqBody.Input = fullInput
	if r.sessionID != "" {
		reqBody.PromptCacheKey = r.sessionID
		reqBody.ClientMetadata = responsesClientMetadata(r.sessionID, requestStartedAt)
	}
	if ot.ServiceTier != "" {
		reqBody.ServiceTier = ot.ServiceTier
	}
	log.Debugf("responses: full model=%v store=%v input_len=%v", model, reqBody.Store, len(fullInput))
	// Set instructions separately from input messages, matching Codex's Responses
	// request shape and avoiding system-role input items on compatible backends.
	if systemPrompt != "" {
		instructions := systemPrompt
		reqBody.Instructions = &instructions
	}
	if ot.ParallelToolCalls != nil {
		reqBody.ParallelToolCalls = *ot.ParallelToolCalls
	}

	// The official Codex backend only accepts low/medium/high/xhigh; other
	// Responses-compatible gateways (e.g. GLM-5.2 relays) support a wider set
	// including max/minimal/none, which must pass through verbatim.
	firstPartyCodex := useOpenAIOAuth && r.provider != nil && r.provider.IsCodexOAuthTransport()
	effectiveReasoningEffort, _ := resolveResponsesReasoningEffort(ot.ReasoningEffort, firstPartyCodex)
	if maxTokens > 0 {
		log.Debugf("omitting max_output_tokens for Responses request requested=%v", maxTokens)
	}

	// Responses reasoning is emitted whenever effort or summary is configured. Codex's
	// request builder emits the block for any reasoning-capable model even when effort
	// is empty (effort omitted, summary carried), so gating on effort alone dropped it.
	if effectiveReasoningEffort != "" || ot.ReasoningSummary != "" {
		reqBody.Reasoning = &reasoningConfig{Effort: effectiveReasoningEffort, Summary: ot.ReasoningSummary}
	}
	if ot.TextVerbosity != "" {
		reqBody.Text = &textConfig{Verbosity: ot.TextVerbosity}
	}
	reqBody.Include = responsesIncludeForReasoning(reqBody.Reasoning)

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request body: %w", err)
	}
	dumpRequestBody := append([]byte(nil), bodyBytes...)

	log.Debugf("responses request model=%v max_output_tokens=%v messages=%v tools=%v reasoning_effort=%v reasoning_summary=%v request_bytes=%v", model, 0, len(messages), len(tools), effectiveReasoningEffort, ot.ReasoningSummary, len(bodyBytes))

	start := requestStartedAt
	if useOpenAIOAuth && r.provider != nil && r.provider.IsCodexOAuthTransport() && r.provider.EffectiveResponsesWebsocket() {
		wsComplete := r.codexWSCompleteFn
		if wsComplete == nil {
			wsComplete = r.completeStreamCodexWebSocket
		}
		wsResp, wsUsedIncremental, wsErr := wsComplete(ctx, url, apiKey, model, &reqBody, fullInput, traceCB, start, codexWSCompleteOptions{})
		if wsErr == nil {
			r.lastTransportUsed.Store("websocket")
			persistLLMTrace(traceWriter, traceCollector, 0, "websocket", start, wsResp, nil)
			return wsResp, nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			r.resetCodexWebSocketChain("context_cancel")
			callErr := fmt.Errorf("responses websocket aborted: %w", ctxErr)
			persistLLMTrace(traceWriter, traceCollector, 0, "websocket", start, nil, callErr)
			return nil, callErr
		}
		if isCodexWSChainStateMismatch(wsErr) {
			log.Warnf("responses: Codex WebSocket chain-state mismatch, retrying full request without previous_response_id error=%v model=%v", wsErr, model)
			r.resetCodexWebSocketChain("ws_chain_state_mismatch")
			retryResp, retryUsedIncremental, retryErr := wsComplete(ctx, url, apiKey, model, &reqBody, fullInput, traceCB, start, codexWSCompleteOptions{SkipPrewarm: true})
			if retryErr == nil {
				r.lastTransportUsed.Store("websocket")
				persistLLMTrace(traceWriter, traceCollector, 0, "websocket", start, retryResp, nil)
				return retryResp, nil
			}
			wsErr = retryErr
			wsUsedIncremental = retryUsedIncremental
			if ctxErr := ctx.Err(); ctxErr != nil {
				r.resetCodexWebSocketChain("context_cancel")
				callErr := fmt.Errorf("responses websocket aborted: %w", ctxErr)
				persistLLMTrace(traceWriter, traceCollector, 0, "websocket", start, nil, callErr)
				return nil, callErr
			}
		}
		if shouldStopCodexWSHTTPFallback(wsErr) {
			log.Warnf("responses: Codex WebSocket returned terminal API error, skipping HTTP fallback error=%v", wsErr)
			r.resetCodexWebSocketChain("ws_terminal_api_error")
			persistLLMTrace(traceWriter, traceCollector, apiErrorStatusCode(wsErr), "websocket", start, nil, wsErr)
			return nil, wsErr
		}
		if wsUsedIncremental {
			log.Warnf("responses: Codex WebSocket incremental failed, retrying full request on websocket error=%v model=%v", wsErr, model)
			r.resetCodexWebSocketChain("error_fallback")
			wsResp, _, retryErr := wsComplete(ctx, url, apiKey, model, &reqBody, fullInput, traceCB, start, codexWSCompleteOptions{SkipPrewarm: true})
			if retryErr == nil {
				r.lastTransportUsed.Store("websocket")
				persistLLMTrace(traceWriter, traceCollector, 0, "websocket", start, wsResp, nil)
				return wsResp, nil
			}
			wsErr = retryErr
			if ctxErr := ctx.Err(); ctxErr != nil {
				r.resetCodexWebSocketChain("context_cancel")
				callErr := fmt.Errorf("responses websocket aborted: %w", ctxErr)
				persistLLMTrace(traceWriter, traceCollector, 0, "websocket", start, nil, callErr)
				return nil, callErr
			}
			if shouldStopCodexWSHTTPFallback(wsErr) {
				log.Warnf("responses: Codex WebSocket retry returned terminal API error, skipping HTTP fallback error=%v", wsErr)
				r.resetCodexWebSocketChain("ws_terminal_api_error")
				persistLLMTrace(traceWriter, traceCollector, apiErrorStatusCode(wsErr), "websocket", start, nil, wsErr)
				return nil, wsErr
			}
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			r.resetCodexWebSocketChain("context_cancel")
			callErr := fmt.Errorf("responses websocket aborted: %w", ctxErr)
			persistLLMTrace(traceWriter, traceCollector, 0, "websocket", start, nil, callErr)
			return nil, callErr
		}
		log.Warnf("responses: Codex WebSocket failed, falling back to HTTP error=%v", wsErr)
		r.resetCodexWebSocketChain("ws_fallback_http")
	}

	r.lastTransportUsed.Store("http")
	resp, httpStatus, parseErr := r.sendAndParse(ctx, url, bodyBytes, dumpRequestBody, dumpWriter, model, apiKey, useOpenAIOAuth, reqBody.ClientMetadata, traceCB)

	// HTTP full-input path: no previous_response_id retry/rollback handling required.

	persistLLMTrace(traceWriter, traceCollector, httpStatus, "http", start, resp, parseErr)
	return resp, parseErr
}

func apiErrorStatusCode(err error) int {
	if apiErr, ok := errors.AsType[*APIError](err); ok && apiErr != nil {
		return apiErr.StatusCode
	}
	return 0
}

func shouldStopCodexWSHTTPFallback(err error) bool {
	apiErr, ok := errors.AsType[*APIError](err)
	if !ok || apiErr == nil {
		return false
	}

	code := strings.ToLower(strings.TrimSpace(apiErr.Code))
	typ := strings.ToLower(strings.TrimSpace(apiErr.Type))
	if code == "websocket_connection_limit_reached" {
		return false
	}
	if code == "rate_limit_exceeded" || typ == "rate_limit_exceeded" {
		return false
	}

	switch apiErr.StatusCode {
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden:
		return true
	}

	if code == "account_invalidated" || code == "account_deactivated" || confirmedCodexUsageLimitError(apiErr) {
		return true
	}
	return false
}

// isCodexWSChainStateMismatch reports whether a 400 indicates the server-side
// incremental conversation state (keyed by previous_response_id) has diverged
// from the input we sent. Recovering only requires clearing previous_response_id
// and re-sending the full input over the same WebSocket; if that also fails,
// the input itself is malformed and HTTP would fail identically.
func isCodexWSChainStateMismatch(err error) bool {
	apiErr, ok := errors.AsType[*APIError](err)
	if !ok || apiErr == nil {
		return false
	}
	if apiErr.StatusCode != http.StatusBadRequest {
		return false
	}
	return apiErrMessageContainsAny(apiErr, "no tool call found for function call output", "no tool call found for custom tool call output", "previous_response_id")
}

// sendAndParse sends a Responses API request and parses the SSE stream.
func (r *ResponsesProvider) sendAndParse(
	ctx context.Context,
	url string,
	bodyBytes []byte,
	dumpRequestBody []byte,
	dumpWriter *DumpWriter,
	model string,
	apiKey string,
	useOpenAIOAuth bool,
	clientMetadata map[string]string,
	cb StreamCallback,
) (*message.Response, int, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, fmt.Errorf("responses request aborted: %w", err)
	}
	// Build HTTP request with a derived context for per-chunk timeout enforcement.
	streamCtx, streamCancel := context.WithCancel(ctx)
	defer streamCancel()
	req, err := http.NewRequestWithContext(streamCtx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, 0, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set(headerContentType, headerValueApplicationJSON)
	if useOpenAIOAuth {
		applyOpenAIOAuthHeaders(req, r.provider, apiKey, true)
	} else {
		req.Header.Set("Authorization", "Bearer "+apiKey)
		applyResponsesStreamingHeaders(req.Header, r.provider)
	}
	applySessionIDHeaders(req.Header, r.sessionID)
	applyResponsesMetadataHeaders(req.Header, clientMetadata)

	// Apply request body compression if configured
	req, _ = compressRequestBody(req, bodyBytes, r.provider.CompressEnabled())

	// Send request.
	start := time.Now()
	if r.proxyScheme != "" {
		log.Debugf("LLM request via proxy provider=%v scheme=%v", "responses", r.proxyScheme)
	}
	if cb != nil {
		cb(message.StreamDelta{Type: message.StreamDeltaStatus, Status: &message.StatusDelta{Type: "connecting"}})
	}
	httpResp, err := r.client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("send request: %w", err)
	}
	defer httpResp.Body.Close()

	// Handle gzip response if server supports it
	if httpResp.Header.Get(headerContentEncoding) == headerValueGzip {
		gr, err := gzip.NewReader(httpResp.Body)
		if err != nil {
			return nil, 0, fmt.Errorf("create gzip reader: %w", err)
		}
		httpResp.Body = gr
	}

	if cb != nil {
		cb(message.StreamDelta{Type: message.StreamDeltaStatus, Status: &message.StatusDelta{Type: message.StatusDeltaWaitingHeaders}, Progress: &message.StreamProgressDelta{Bytes: responseHeaderBytes(httpResp)}})
	}

	// Handle non-2xx responses.
	if httpResp.StatusCode != http.StatusOK {
		// Read up to 4KB for error logging to avoid memory issues with large responses.
		errBody, _ := io.ReadAll(io.LimitReader(httpResp.Body, maxHTTPErrorBodyBytes))
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
					cb(message.StreamDelta{Type: message.StreamDeltaRateLimits, RateLimit: snap})
				}
			}
		}
		apiErr := parseOpenAIHTTPErrorFromBytes(httpResp.StatusCode, httpResp.Header, errBody)
		if dumpWriter != nil {
			statusCode, headers := dumpHTTPResponseMetadata(httpResp)
			bodyCopy := string(append([]byte(nil), errBody...))
			go func() {
				dump := &LLMDump{
					Timestamp:   start.Format(time.RFC3339Nano),
					Provider:    "responses",
					Model:       model,
					RequestBody: dumpRequestBody,
					HTTPStatus:  statusCode,
					HTTPHeaders: headers,
					HTTPBody:    bodyCopy,
					Error:       apiErr.Error(),
					DurationMS:  time.Since(start).Milliseconds(),
				}
				if IsContextLengthExceeded(apiErr) {
					dump.Recovery = &DumpRecovery{
						Reason: "context_length_exceeded",
						Stage:  "http_error",
						Action: "return_error",
						Code:   strings.TrimSpace(apiErr.Code),
					}
				}
				if wErr := dumpWriter.Write(dump); wErr != nil {
					log.Warnf("failed to write LLM dump error=%v", wErr)
				}
			}()
		}
		return nil, httpResp.StatusCode, apiErr
	}

	// Parse OpenAI Codex OAuth rate-limit headers from the 200 response and notify via callback.
	if useOpenAIOAuth {
		if snap := ratelimit.ParseCodexHeaders(httpResp.Header); snap != nil {
			snap.Provider = r.provider.Name()
			r.provider.UpdateKeySnapshot(apiKey, snap)
			if cb != nil {
				cb(message.StreamDelta{Type: message.StreamDeltaRateLimits, RateLimit: snap})
			}
		}
	}

	// Parse SSE stream.
	var collector *SSECollector
	if dumpWriter != nil {
		collector = NewSSECollector()
	}
	cr := NewProviderChunkTimeoutReader(httpResp.Body, r.provider, DefaultChunkTimeout, streamCancel)
	defer cr.Stop()
	resp, parseErr := parseResponsesSSE(cr, cb, collector)
	if parseErr != nil {
		if _, ok := errors.AsType[*ChunkTimeoutError](parseErr); ok {
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
	if dumpWriter != nil {
		statusCode, headers := dumpHTTPResponseMetadata(httpResp)
		go func() {
			dump := &LLMDump{
				Timestamp:   start.Format(time.RFC3339Nano),
				Provider:    "responses",
				Model:       model,
				RequestBody: dumpRequestBody,
				HTTPStatus:  statusCode,
				HTTPHeaders: headers,
				SSEChunks:   collector.Chunks(),
				Response:    DumpResponseFromResponse(resp),
				DurationMS:  time.Since(start).Milliseconds(),
			}
			if parseErr != nil {
				dump.Error = parseErr.Error()
				if apiErr, ok := errors.AsType[*APIError](parseErr); ok && IsContextLengthExceeded(apiErr) {
					dump.Recovery = &DumpRecovery{
						Reason: "context_length_exceeded",
						Stage:  "sse_parse",
						Action: "return_error",
						Code:   strings.TrimSpace(apiErr.Code),
					}
				}
			}
			if wErr := dumpWriter.Write(dump); wErr != nil {
				log.Warnf("failed to write LLM dump error=%v", wErr)
			}
		}()
	}

	return resp, httpResp.StatusCode, parseErr
}
