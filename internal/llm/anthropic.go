package llm

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/user"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
)

// AnthropicProvider implements streaming completion against the Anthropic Messages API.
type AnthropicProvider struct {
	provider    *ProviderConfig
	client      *http.Client
	dumpWriter  atomic.Pointer[DumpWriter] // optional: when non-nil, each request/response is dumped to disk
	traceWriter atomic.Pointer[TraceWriter]
	proxyScheme string // "http"/"https"/"socks5" when using proxy, "" otherwise (for request logging)
}

// NewAnthropicProviderWithClient creates an Anthropic provider using a caller-supplied HTTP client.
func NewAnthropicProviderWithClient(provider *ProviderConfig, client *http.Client, proxyURL string) (*AnthropicProvider, error) {
	return &AnthropicProvider{provider: provider, client: client, proxyScheme: ProxyScheme(proxyURL)}, nil
}

// NewAnthropicProvider creates a new AnthropicProvider wrapping the given ProviderConfig.
// proxyURL configures an HTTP/HTTPS/SOCKS5 proxy; empty string means no proxy (direct connect).
func NewAnthropicProvider(provider *ProviderConfig, proxyURL string) (*AnthropicProvider, error) {
	client, err := NewStreamingHTTPClientWithProxy(proxyURL, providerResponseHeaderTimeout(provider))
	if err != nil {
		return nil, fmt.Errorf("create HTTP client for anthropic provider: %w", err)
	}
	return &AnthropicProvider{
		provider:    provider,
		client:      client,
		proxyScheme: ProxyScheme(proxyURL),
	}, nil
}

// SetDumpWriter enables LLM request/response dumping for debugging.
func (a *AnthropicProvider) SetDumpWriter(w *DumpWriter) {
	a.dumpWriter.Store(w)
}

func (a *AnthropicProvider) SetTraceWriter(w *TraceWriter) {
	a.traceWriter.Store(w)
}

// --- Anthropic API request/response structures ---

// anthropicRequest is the top-level request body for the Messages API.
type anthropicRequest struct {
	Model        string                 `json:"model"`
	MaxTokens    int                    `json:"max_tokens"`
	System       []anthropicContent     `json:"system,omitempty"`
	Messages     []anthropicMessage     `json:"messages"`
	Tools        []anthropicTool        `json:"tools,omitempty"`
	ToolChoice   *anthropicToolChoice   `json:"tool_choice,omitempty"`
	Stream       bool                   `json:"stream"`
	Temperature  *float64               `json:"temperature,omitempty"`
	Thinking     *anthropicThinking     `json:"thinking,omitempty"`
	OutputConfig *anthropicOutputConfig `json:"output_config,omitempty"`
	Speed        string                 `json:"speed,omitempty"`
	CacheControl *anthropicCacheCtrl    `json:"cache_control,omitempty"`
	Metadata     *anthropicMetadata     `json:"metadata,omitempty"`
}

// anthropicThinking configures extended thinking.
type anthropicThinking struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens,omitempty"`
	Display      string `json:"display,omitempty"`
}

// anthropicOutputConfig configures output parameters such as effort.
type anthropicOutputConfig struct {
	Effort string `json:"effort,omitempty"`
}

type anthropicMetadata struct {
	UserID string `json:"user_id,omitempty"`
}

type anthropicToolChoice struct {
	Type string `json:"type"`
}

func anthropicToolChoiceFromTuning(choice string) *anthropicToolChoice {
	switch choice {
	case "auto":
		return &anthropicToolChoice{Type: "auto"}
	case "required":
		return &anthropicToolChoice{Type: "any"}
	default:
		return nil
	}
}

// anthropicMessage is a single message in the Anthropic API format.
type anthropicMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"` // string or []anthropicContent
}

// anthropicContent is a content block in the Anthropic API format.
type anthropicContent struct {
	Type         string                `json:"type"`
	Text         string                `json:"text,omitempty"`
	Thinking     string                `json:"thinking,omitempty"`      // thinking block: content
	Signature    string                `json:"signature,omitempty"`     // thinking block: signature (required for replay)
	ID           string                `json:"id,omitempty"`            // tool_use: tool call ID
	Name         string                `json:"name,omitempty"`          // tool_use: tool name
	Input        json.RawMessage       `json:"input,omitempty"`         // tool_use: tool arguments
	ToolUseID    string                `json:"tool_use_id,omitempty"`   // tool_result: corresponding tool call ID
	Content      any                   `json:"content,omitempty"`       // tool_result: string or []anthropicContent
	Source       *anthropicImageSource `json:"source,omitempty"`        // image block
	CacheControl *anthropicCacheCtrl   `json:"cache_control,omitempty"` // prompt caching marker
}

// anthropicImageSource is the base64 source block shared by image blocks and
// document (PDF) blocks; both use the same type/media_type/data shape.
type anthropicImageSource struct {
	Type      string `json:"type"`       // always "base64"
	MediaType string `json:"media_type"` // e.g. "image/png" or "application/pdf"
	Data      string `json:"data"`       // base64-encoded bytes
}

// anthropicCacheCtrl is the cache_control block for prompt caching.
type anthropicCacheCtrl struct {
	Type string `json:"type"` // always "ephemeral"
	TTL  string `json:"ttl,omitempty"`
}

// anthropicTool is a tool definition in the Anthropic API format.
type anthropicTool struct {
	Name         string              `json:"name"`
	Description  string              `json:"description"`
	InputSchema  map[string]any      `json:"input_schema"`
	CacheControl *anthropicCacheCtrl `json:"cache_control,omitempty"`
}

// anthropicErrorResponse is returned in the HTTP body for non-streaming errors.
type anthropicErrorResponse struct {
	Type  string `json:"type"`
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

func (a *AnthropicProvider) CompleteStream(
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
	dumpWriter := a.dumpWriter.Load()
	traceWriter := a.traceWriter.Load()
	traceCollector := newLLMTraceCollector("anthropic", model, cb)
	traceCB := traceCollector.Callback
	at := tuning.Anthropic
	at, err := validateAnthropicTuning(at)
	if err != nil {
		return nil, fmt.Errorf("validate anthropic tuning: %w", err)
	}

	// Build system content blocks.
	systemBlocks := buildSystemBlocks(systemPrompt)

	// Convert internal messages to Anthropic API format.
	apiMessages, messageMap := convertMessagesWithMap(messages)
	at.CacheBoundary = resolveAnthropicCacheBoundary(at.CacheBoundary, messageMap)

	// Convert tool definitions with optional cache markers.
	apiTools := convertToolsWithCache(tools, at)

	// Build the request body.
	reqBody := anthropicRequest{
		Model:     model,
		MaxTokens: maxTokens,
		System:    systemBlocks,
		Messages:  apiMessages,
		Tools:     apiTools,
		Stream:    true,
	}
	if at.ToolChoice != "" {
		// Extended thinking is incompatible with forced tool use: Anthropic
		// returns 400 for tool_choice "any"/"tool" when thinking is
		// enabled/adaptive. "auto" stays valid, so only the forced choice is
		// suppressed here (the loop exit-control path requests "required",
		// which maps to "any").
		thinkingActive := at.ThinkingType == "enabled" || at.ThinkingType == "adaptive"
		if tc := anthropicToolChoiceFromTuning(at.ToolChoice); tc != nil && !(thinkingActive && tc.Type == "any") {
			reqBody.ToolChoice = tc
		}
	}
	if at.Temperature != nil && at.ThinkingType != "enabled" && at.ThinkingType != "adaptive" {
		reqBody.Temperature = at.Temperature
	}

	// Apply prompt caching strategy.
	if err := applyPromptCaching(at, &reqBody); err != nil {
		return nil, fmt.Errorf("apply anthropic prompt caching: %w", err)
	}

	// Configure thinking.
	if thinking := buildAnthropicThinking(at); thinking != nil {
		reqBody.Thinking = thinking
	}
	if oc := buildAnthropicOutputConfig(at); oc != nil {
		reqBody.OutputConfig = oc
	}
	if at.ServiceTier != "" {
		reqBody.Speed = at.ServiceTier
	}
	if userIDPayload := stableAnthropicMetadataUserIDPayload(a.provider); userIDPayload != "" {
		reqBody.Metadata = &anthropicMetadata{UserID: userIDPayload}
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request body: %w", err)
	}
	overrides := a.provider.RequestOverrides(model)
	bodyBytes, err = applyRequestBodyOverrides(bodyBytes, overrides)
	if err != nil {
		return nil, err
	}
	dumpRequestBody := append([]byte(nil), bodyBytes...)

	// Build the HTTP request with a derived context for per-chunk timeout enforcement.
	streamCtx, streamCancel := context.WithCancel(ctx)
	defer streamCancel()
	req, err := http.NewRequestWithContext(streamCtx, http.MethodPost, a.provider.APIURL(), bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set(headerContentType, headerValueApplicationJSON)
	applyProviderAuthHeader(req.Header, a.provider.AuthScheme(), apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("x-app", "cli")

	if betaHeader := anthropicBetaHeader(at, a.effectiveContextTokens(model)); betaHeader != "" {
		req.Header.Set("anthropic-beta", betaHeader)
	}
	setProviderLLMUserAgent(req.Header, a.provider)

	log.Debugf("anthropic request model=%v max_tokens=%v thinking_type=%v thinking_budget=%v messages=%v tools=%v", model, maxTokens, at.ThinkingType, at.ThinkingBudget, len(messages), len(tools))

	// Apply request body compression if configured
	req, _ = compressRequestBody(req, bodyBytes, a.provider.CompressEnabled())
	applyRequestHeaderOverrides(req.Header, overrides)

	// Send the request.
	start := time.Now()
	if a.proxyScheme != "" {
		log.Debugf("LLM request via proxy provider=%v scheme=%v", "anthropic", a.proxyScheme)
	}
	traceCB(message.StreamDelta{Type: message.StreamDeltaStatus, Status: &message.StatusDelta{Type: "connecting"}})
	httpResp, err := doRequestUntilHeaders(a.client, req, providerResponseHeaderTimeout(a.provider))
	if err != nil {
		callErr := fmt.Errorf("send request: %w", err)
		persistLLMTrace(traceWriter, traceCollector, 0, "http", start, nil, callErr)
		return nil, callErr
	}
	defer httpResp.Body.Close()

	// Handle gzip response if server supports it
	if httpResp.Header.Get(headerContentEncoding) == headerValueGzip {
		gr, err := gzip.NewReader(httpResp.Body)
		if err != nil {
			return nil, fmt.Errorf("create gzip reader: %w", err)
		}
		httpResp.Body = gr
	}

	traceCB(message.StreamDelta{Type: message.StreamDeltaStatus, Status: &message.StatusDelta{Type: message.StatusDeltaWaitingHeaders}, Progress: &message.StreamProgressDelta{Bytes: responseHeaderBytes(httpResp)}})

	// Handle non-2xx responses.
	if httpResp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(io.LimitReader(httpResp.Body, maxHTTPErrorBodyBytes))
		io.Copy(io.Discard, httpResp.Body)
		apiErr := parseHTTPErrorFromBytes(httpResp.StatusCode, httpResp.Header, errBody)
		// Dump error response if enabled.
		if dumpWriter != nil {
			statusCode, headers := dumpHTTPResponseMetadata(httpResp)
			bodyCopy := string(append([]byte(nil), errBody...))
			go func() {
				dump := &LLMDump{
					Timestamp:   start.Format(time.RFC3339Nano),
					Provider:    "anthropic",
					Model:       model,
					RequestBody: dumpRequestBody,
					HTTPStatus:  statusCode,
					HTTPHeaders: headers,
					HTTPBody:    bodyCopy,
					Error:       apiErr.Error(),
					DurationMS:  time.Since(start).Milliseconds(),
				}
				if wErr := dumpWriter.Write(dump); wErr != nil {
					log.Warnf("failed to write LLM dump error=%v", wErr)
				}
			}()
		}
		persistLLMTrace(traceWriter, traceCollector, httpResp.StatusCode, "http", start, nil, apiErr)
		return nil, apiErr
	}

	// Parse the SSE stream, collecting chunks for dump if enabled.
	var collector *SSECollector
	if dumpWriter != nil {
		collector = NewSSECollector()
	}
	cr := NewProviderChunkTimeoutReader(httpResp.Body, a.provider, DefaultChunkTimeout, streamCancel)
	defer cr.Stop()
	resp, parseErr := parseSSEStream(cr, traceCB, collector)

	// Write dump asynchronously.
	if dumpWriter != nil {
		statusCode, headers := dumpHTTPResponseMetadata(httpResp)
		go func() {
			dump := &LLMDump{
				Timestamp:   start.Format(time.RFC3339Nano),
				Provider:    "anthropic",
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
			}
			if wErr := dumpWriter.Write(dump); wErr != nil {
				log.Warnf("failed to write LLM dump error=%v", wErr)
			}
		}()
	}

	persistLLMTrace(traceWriter, traceCollector, httpResp.StatusCode, "http", start, resp, parseErr)
	return resp, parseErr
}

// parseHTTPError converts a non-200 HTTP response into an APIError.
func parseHTTPErrorFromBytes(statusCode int, header http.Header, body []byte) *APIError {
	apiErr := &APIError{
		StatusCode: statusCode,
	}

	// Parse Retry-After header if present.
	if ra := header.Get("Retry-After"); ra != "" {
		if seconds, err := strconv.Atoi(ra); err == nil {
			apiErr.RetryAfter = durationFromPositiveSecondsClamped(int64(seconds), 0)
		} else if t, err := http.ParseTime(ra); err == nil {
			apiErr.RetryAfter = max(time.Until(t), 0)
		}
	}

	// Try to parse JSON error body.
	if len(body) == 0 {
		apiErr.Message = fmt.Sprintf("HTTP %d (empty error body)", statusCode)
		return apiErr
	}
	var errResp anthropicErrorResponse
	if err := json.Unmarshal(body, &errResp); err == nil && errResp.Error.Message != "" {
		apiErr.Message = errResp.Error.Message
		apiErr.Type = errResp.Error.Type
	} else {
		// Fallback: use raw body (truncated).
		msg := TruncateStringRunes(string(body), 200, "...")
		apiErr.Message = msg
	}

	return apiErr
}

// buildSystemBlocks converts a system prompt string into Anthropic content blocks.
func buildSystemBlocks(systemPrompt string) []anthropicContent {
	if systemPrompt == "" {
		return nil
	}
	return []anthropicContent{
		{
			Type: "text",
			Text: systemPrompt,
		},
	}
}

func effectiveAnthropicThinkingType(tuning AnthropicTuning) string {
	return tuning.ThinkingType
}

func normalizeAnthropicPromptCacheMode(mode string) (string, error) {
	mode = strings.TrimSpace(mode)
	switch mode {
	case "", "explicit":
		return "explicit", nil
	case "off", "auto":
		return mode, nil
	default:
		return "", fmt.Errorf("unsupported anthropic prompt_cache.mode %q", mode)
	}
}

func validateAnthropicTuning(tuning AnthropicTuning) (AnthropicTuning, error) {
	tuning.ThinkingType = effectiveAnthropicThinkingType(tuning)
	switch tuning.ThinkingType {
	case "":
		// valid as-is
	case "enabled":
		if tuning.ThinkingBudget <= 0 {
			return tuning, fmt.Errorf("anthropic thinking.type %q requires thinking.budget > 0", tuning.ThinkingType)
		}
		if tuning.ThinkingEffort != "" {
			return tuning, fmt.Errorf("anthropic thinking.type %q does not support thinking.effort", tuning.ThinkingType)
		}
	case "adaptive":
		if tuning.ThinkingBudget > 0 {
			return tuning, fmt.Errorf("anthropic thinking.type %q does not support thinking.budget", tuning.ThinkingType)
		}
	case "disabled":
		if tuning.ThinkingBudget > 0 {
			return tuning, fmt.Errorf("anthropic thinking.type %q does not support thinking.budget", tuning.ThinkingType)
		}
		if tuning.ThinkingEffort != "" {
			return tuning, fmt.Errorf("anthropic thinking.type %q does not support thinking.effort", tuning.ThinkingType)
		}
		if tuning.ThinkingDisplay != "" {
			return tuning, fmt.Errorf("anthropic thinking.type %q does not support thinking.display", tuning.ThinkingType)
		}
	default:
		return tuning, fmt.Errorf("unsupported anthropic thinking.type %q", tuning.ThinkingType)
	}
	if tuning.ServiceTier != "" && tuning.ServiceTier != "fast" {
		return tuning, fmt.Errorf("unsupported anthropic service tier %q", tuning.ServiceTier)
	}
	mode, err := normalizeAnthropicPromptCacheMode(tuning.PromptCacheMode)
	if err != nil {
		return tuning, err
	}
	tuning.PromptCacheMode = mode
	return tuning, nil
}

// effectiveContextTokens reports the model's declared context window in tokens,
// preferring the configured input budget and falling back to the total context
// limit. It returns 0 when the model is unknown or no limit is declared.
func (a *AnthropicProvider) effectiveContextTokens(model string) int {
	if a.provider == nil {
		return 0
	}
	m, ok := a.provider.GetModel(model)
	if !ok {
		return 0
	}
	if m.Limit.Input > 0 {
		return m.Limit.Input
	}
	return m.Limit.Context
}

// anthropicBetaHeader builds the anthropic-beta header value. effectiveContext
// is the model's effective context window (input budget if declared, else total
// context) in tokens; context-1m is only injected for models the caller declares
// as having a 1M-token window, because that beta actually opts the request into
// 1M context behavior on the official API (tier gated, distinct pricing above
// 200K, and an error on unsupported models) rather than being a no-op.
func anthropicBetaHeader(tuning AnthropicTuning, effectiveContext int) string {
	var betas []string

	// claude-code-20250219 is the Claude Code agentic client identifier the
	// official Anthropic API recognizes; sending it improves cache/routing
	// affinity. The anthropic-beta header is server-validated (the official API
	// rejects unknown or inapplicable values with HTTP 400), so every other flag
	// below is emitted only when it actually applies to the model or tuning.
	betas = append(betas, "claude-code-20250219")

	// context-1m-2025-08-07 is a real, enforced beta on the official Anthropic
	// API (tier-4 gated, 2x/1.5x long-context pricing above 200K, and an error
	// on models that lack 1M support). Only opt in for models the caller
	// declares as having a 1M-token window.
	if effectiveContext >= 1000000 {
		betas = append(betas, "context-1m-2025-08-07")
	}

	if effectiveAnthropicThinkingType(tuning) == "enabled" && tuning.ThinkingBudget > 0 {
		betas = append(betas, "interleaved-thinking-2025-05-14")
	}
	// effort-2025-11-24 only takes effect paired with output_config.effort, so
	// send it only when an effort level is configured (see
	// buildAnthropicOutputConfig). Sending it bare can 400 on models that do not
	// support effort.
	if tuning.ThinkingEffort != "" {
		betas = append(betas, "effort-2025-11-24")
	}
	if tuning.ServiceTier == "fast" {
		betas = append(betas, "fast-mode-2026-02-01")
	}
	if len(betas) == 0 {
		return ""
	}

	seen := make(map[string]struct{}, len(betas))
	merged := make([]string, 0, len(betas))
	for _, beta := range betas {
		beta = strings.TrimSpace(beta)
		if beta == "" {
			continue
		}
		if _, ok := seen[beta]; ok {
			continue
		}
		seen[beta] = struct{}{}
		merged = append(merged, beta)
	}
	return strings.Join(merged, ",")
}

func stableAnthropicMetadataUserIDPayload(provider *ProviderConfig) string {
	if provider == nil {
		return ""
	}

	configHome, err := config.ConfigHomeDir()
	if err != nil {
		configHome = ""
	}

	username := os.Getenv("USER")
	if username == "" {
		username = os.Getenv("USERNAME")
	}
	if username == "" {
		if cur, err := user.Current(); err == nil {
			username = cur.Username
		}
	}

	// Add hostname for better request routing to same backend instance
	hostname, _ := os.Hostname()

	raw := strings.Join([]string{
		"chord-anthropic-user",
		provider.Name(),
		configHome,
		username,
		hostname,
	}, "|")
	if raw == "" {
		return ""
	}

	sum := sha256.Sum256([]byte(raw))
	deviceID := "chord_" + hex.EncodeToString(sum[:8])

	// Claude Code-compatible proxies expect a stable session_id-shaped routing key.
	sessionRaw := strings.Join([]string{
		"chord-session",
		provider.Name(),
		configHome,
		username,
		hostname,
	}, "|")
	sessionSum := sha256.Sum256([]byte(sessionRaw))
	sessionID := hex.EncodeToString(sessionSum[:16])

	// Always return JSON format for better cache routing with third-party proxies.
	// Official Anthropic API accepts this value in metadata.user_id.
	return fmt.Sprintf(`{"device_id":"%s","account_uuid":"","session_id":"%s"}`, deviceID, sessionID)
}

// convertMessages converts internal Message slices to Anthropic API format.
// Adjacent tool results (Role="tool") are merged into a single user message
// with multiple tool_result content blocks.
func convertMessages(msgs []message.Message) []anthropicMessage {
	result, _ := convertMessagesWithMap(msgs)
	return result
}

type anthropicMessageMapEntry struct {
	MessageIndex int
	BlockIndex   int
	Valid        bool
}

func convertMessagesWithMap(msgs []message.Message) ([]anthropicMessage, []anthropicMessageMapEntry) {
	var result []anthropicMessage
	messageMap := make([]anthropicMessageMapEntry, len(msgs))

	i := 0
	for i < len(msgs) {
		msg := msgs[i]
		sourceIndex := i

		switch msg.Role {
		case "user":
			if len(msg.Parts) > 0 {
				// Multi-part message (may include images).
				var blocks []anthropicContent
				for _, p := range msg.Parts {
					switch p.Type {
					case "image":
						blocks = append(blocks, anthropicContent{
							Type: "image",
							Source: &anthropicImageSource{
								Type:      "base64",
								MediaType: p.MimeType,
								Data:      encodeBase64Cached(p.Data),
							},
						})
					case "pdf":
						blocks = append(blocks, anthropicContent{
							Type: "document",
							Source: &anthropicImageSource{
								Type:      "base64",
								MediaType: defaultPDFMediaType(p.MimeType),
								Data:      encodeBase64Cached(p.Data),
							},
						})
					default: // "text"
						blocks = append(blocks, anthropicContent{Type: "text", Text: p.Text})
					}
				}
				result, messageMap[sourceIndex] = appendAnthropicUserMessage(result, blocks)
			} else {
				// Plain text user message.
				result, messageMap[sourceIndex] = appendAnthropicUserMessage(result, []anthropicContent{{Type: "text", Text: msg.Content}})
			}
			i++

		case "tool":
			// Collect adjacent tool results into a single user message.
			var toolResults []anthropicContent
			var toolSourceIndices []int
			for i < len(msgs) && msgs[i].Role == "tool" {
				sourceIndex := i
				// Skip tool results with empty id — they correspond to malformed
				// tool calls (e.g. from GLM) that were also skipped above.
				if msgs[i].ToolCallID == "" {
					log.Warn("skipping tool_result with empty tool_use_id in history")
					i++
					continue
				}
				blockIndex := len(toolResults)
				toolResults = append(toolResults, anthropicContent{
					Type:      "tool_result",
					ToolUseID: msgs[i].ToolCallID,
					Content:   anthropicToolResultContent(msgs[i]),
				})
				messageMap[sourceIndex] = anthropicMessageMapEntry{BlockIndex: blockIndex, Valid: true}
				toolSourceIndices = append(toolSourceIndices, sourceIndex)
				i++
			}
			if len(toolResults) == 0 {
				continue
			}
			var appended anthropicMessageMapEntry
			result, appended = appendAnthropicUserMessage(result, toolResults)
			baseBlock := appended.BlockIndex - len(toolResults) + 1
			for _, sourceIndex := range toolSourceIndices {
				messageMap[sourceIndex].MessageIndex = appended.MessageIndex
				messageMap[sourceIndex].BlockIndex += baseBlock
			}

		case "assistant":
			var content []anthropicContent

			// Thinking blocks must come before text/tool_use in the assistant message.
			// Anthropic requires them to be replayed verbatim (including signature).
			for _, tb := range msg.ThinkingBlocks {
				if !tb.Replayable() {
					log.Warnf("skipping unreplayable thinking block in Anthropic history")
					continue
				}
				content = append(content, anthropicContent{
					Type:      "thinking",
					Thinking:  tb.Thinking,
					Signature: tb.Signature,
				})
			}

			// Add text content if present.
			contentText := assistantContentForReplay(msg)
			if contentText != "" {
				content = append(content, anthropicContent{
					Type: "text",
					Text: contentText,
				})
			}

			// Add tool_use blocks.
			for _, tc := range msg.ToolCalls {
				// Skip tool calls with empty id or name — malformed responses from
				// some models (e.g. GLM) that omit these fields cause 400 errors
				// (Anthropic API requires non-empty tool_use id and name).
				if tc.ID == "" || tc.Name == "" {
					log.Warnf("skipping tool_use with empty id or name in history tool=%v id=%v", tc.Name, tc.ID)
					continue
				}
				args := tc.Args
				// Sanitize tool call arguments: ensure valid JSON before
				// sending to the API. Malformed args from a truncated model
				// response would cause the API to reject the request.
				if len(args) == 0 || !json.Valid(args) {
					log.Warnf("sanitizing invalid tool call args in Anthropic conversation history tool=%v id=%v raw_args=%v", tc.Name, tc.ID, string(args))
					args = json.RawMessage(MalformedArgsSentinel)
				}
				content = append(content, anthropicContent{
					Type:  "tool_use",
					ID:    tc.ID,
					Name:  tc.Name,
					Input: args,
				})
			}

			if len(content) == 0 {
				log.Warnf("skipping empty assistant message in Anthropic history after normalization")
				i++
				continue
			}

			result = append(result, anthropicMessage{
				Role:    "assistant",
				Content: content,
			})
			messageMap[sourceIndex] = anthropicMessageMapEntry{MessageIndex: len(result) - 1, BlockIndex: len(content) - 1, Valid: true}
			i++

		default:
			// Unknown role; skip.
			log.Warnf("skipping message with unknown role role=%v", msg.Role)
			i++
		}
	}

	return result, messageMap
}

func appendAnthropicUserMessage(result []anthropicMessage, blocks []anthropicContent) ([]anthropicMessage, anthropicMessageMapEntry) {
	if len(result) == 0 || result[len(result)-1].Role != "user" {
		result = append(result, anthropicMessage{Role: "user", Content: blocks})
		return result, anthropicMessageMapEntry{MessageIndex: len(result) - 1, BlockIndex: len(blocks) - 1, Valid: len(blocks) > 0}
	}
	prevBlocks, ok := result[len(result)-1].Content.([]anthropicContent)
	if !ok {
		result = append(result, anthropicMessage{Role: "user", Content: blocks})
		return result, anthropicMessageMapEntry{MessageIndex: len(result) - 1, BlockIndex: len(blocks) - 1, Valid: len(blocks) > 0}
	}
	start := len(prevBlocks)
	result[len(result)-1].Content = append(prevBlocks, blocks...)
	return result, anthropicMessageMapEntry{MessageIndex: len(result) - 1, BlockIndex: start + len(blocks) - 1, Valid: len(blocks) > 0}
}

func resolveAnthropicCacheBoundary(boundary AnthropicCacheBoundary, messageMap []anthropicMessageMapEntry) AnthropicCacheBoundary {
	if !boundary.Valid || boundary.MessageIndex < 0 || boundary.MessageIndex >= len(messageMap) {
		return AnthropicCacheBoundary{}
	}
	entry := messageMap[boundary.MessageIndex]
	if !entry.Valid {
		return AnthropicCacheBoundary{}
	}
	return AnthropicCacheBoundary{MessageIndex: entry.MessageIndex, BlockIndex: entry.BlockIndex, Valid: true}
}

func anthropicToolResultContent(msg message.Message) any {
	if len(msg.Parts) == 0 {
		return msg.Content
	}
	blocks := make([]anthropicContent, 0, len(msg.Parts))
	for _, p := range msg.Parts {
		switch p.Type {
		case "image":
			blocks = append(blocks, anthropicContent{
				Type: "image",
				Source: &anthropicImageSource{
					Type:      "base64",
					MediaType: p.MimeType,
					Data:      encodeBase64Cached(p.Data),
				},
			})
		case "pdf":
			blocks = append(blocks, anthropicContent{
				Type: "document",
				Source: &anthropicImageSource{
					Type:      "base64",
					MediaType: defaultPDFMediaType(p.MimeType),
					Data:      encodeBase64Cached(p.Data),
				},
			})
		default:
			if p.Text == "" {
				continue
			}
			blocks = append(blocks, anthropicContent{Type: "text", Text: p.Text})
		}
	}
	if len(blocks) == 0 {
		return msg.Content
	}
	return blocks
}

// applyCacheBreakpoints applies up to four Anthropic cache_control breakpoints.
// Existing tool breakpoints count toward the Anthropic limit. The stable
// reduced-prefix boundary is prioritized over the weaker tail assistant marker
// so long tool loops can reuse the frozen historical surface.
func applyCacheBreakpoints(system []anthropicContent, messages []anthropicMessage, boundary AnthropicCacheBoundary, existing int) {
	remaining := 4 - existing
	if remaining <= 0 {
		return
	}

	markSystem := func(i int) bool {
		if remaining <= 0 || i < 0 || i >= len(system) || system[i].CacheControl != nil {
			return false
		}
		system[i].CacheControl = &anthropicCacheCtrl{Type: "ephemeral"}
		remaining--
		return true
	}
	markMessageBlock := func(i, preferredBlock int, skipThinking bool) bool {
		if remaining <= 0 || i < 0 || i >= len(messages) {
			return false
		}
		blocks, ok := messages[i].Content.([]anthropicContent)
		if !ok || len(blocks) == 0 {
			return false
		}
		start := len(blocks) - 1
		if preferredBlock >= 0 {
			if preferredBlock >= len(blocks) {
				return false
			}
			start = preferredBlock
		}
		for j := start; j >= 0; j-- {
			if skipThinking && blocks[j].Type == "thinking" {
				if preferredBlock >= 0 {
					return false
				}
				continue
			}
			if blocks[j].CacheControl != nil {
				return false
			}
			blocks[j].CacheControl = &anthropicCacheCtrl{Type: "ephemeral"}
			messages[i].Content = blocks
			remaining--
			return true
		}
		return false
	}

	markSystem(len(system) - 1)
	if boundary.Valid {
		markMessageBlock(boundary.MessageIndex, boundary.BlockIndex, true)
	}
	if i := lastAnthropicMessageIndex(messages, "user"); i >= 0 {
		markMessageBlock(i, -1, false)
	}
	if i := lastAnthropicMessageIndex(messages, "assistant"); i >= 0 {
		markMessageBlock(i, -1, true)
	}
	if len(system) > 1 {
		markSystem(0)
	}
}

func lastAnthropicMessageIndex(messages []anthropicMessage, role string) int {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == role {
			return i
		}
	}
	return -1
}

func countAnthropicToolCacheBreakpoints(tools []anthropicTool) int {
	count := 0
	for _, tool := range tools {
		if tool.CacheControl != nil {
			count++
		}
	}
	return count
}

// convertToolsWithCache converts tool definitions; marks the last tool with a
// cache_control breakpoint when CacheTools is enabled (explicit mode).
// Tools are expected to be in a stable order from Registry.ListDefinitions().
func convertToolsWithCache(tools []message.ToolDefinition, at AnthropicTuning) []anthropicTool {
	if len(tools) == 0 {
		return nil
	}
	result := make([]anthropicTool, len(tools))
	for i, t := range tools {
		result[i] = anthropicTool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
		}
	}
	if at.CacheTools && at.PromptCacheMode != "off" && at.PromptCacheMode != "auto" {
		ttl := at.PromptCacheTTL
		result[len(result)-1].CacheControl = &anthropicCacheCtrl{Type: "ephemeral", TTL: ttl}
	}
	return result
}

// applyPromptCaching applies the caching strategy to the request body.
// auto: set a top-level cache_control on the request.
// explicit (default): apply 4-breakpoint strategy to system/messages.
// off: no caching.
func applyPromptCaching(at AnthropicTuning, req *anthropicRequest) error {
	mode, err := normalizeAnthropicPromptCacheMode(at.PromptCacheMode)
	if err != nil {
		return err
	}
	switch mode {
	case "off":
		// no caching
	case "auto":
		req.CacheControl = &anthropicCacheCtrl{Type: "ephemeral", TTL: at.PromptCacheTTL}
	case "explicit":
		applyCacheBreakpoints(req.System, req.Messages, at.CacheBoundary, countAnthropicToolCacheBreakpoints(req.Tools))
	}
	return nil
}

// buildAnthropicThinking builds the thinking config from tuning.
func buildAnthropicThinking(t AnthropicTuning) *anthropicThinking {
	effectiveType := effectiveAnthropicThinkingType(t)
	switch effectiveType {
	case "enabled":
		if t.ThinkingBudget <= 0 {
			return nil
		}
		th := &anthropicThinking{Type: "enabled", BudgetTokens: t.ThinkingBudget, Display: t.ThinkingDisplay}
		return th
	case "adaptive":
		return &anthropicThinking{Type: "adaptive", Display: t.ThinkingDisplay}
	case "disabled":
		return &anthropicThinking{Type: "disabled"}
	default:
		return nil
	}
}

// buildAnthropicOutputConfig builds output_config from tuning effort.
func buildAnthropicOutputConfig(t AnthropicTuning) *anthropicOutputConfig {
	if t.ThinkingEffort == "" {
		return nil
	}
	return &anthropicOutputConfig{Effort: t.ThinkingEffort}
}
