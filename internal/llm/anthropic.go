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
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
)

// AnthropicProvider implements streaming completion against the Anthropic Messages API.
type AnthropicProvider struct {
	provider    *ProviderConfig
	client      *http.Client
	dumpWriter  *DumpWriter // optional: when non-nil, each request/response is dumped to disk
	proxyScheme string      // "http"/"https"/"socks5" when using proxy, "" otherwise (for request logging)
}

// NewAnthropicProviderWithClient creates an Anthropic provider using a caller-supplied HTTP client.
func NewAnthropicProviderWithClient(provider *ProviderConfig, client *http.Client, proxyURL string) (*AnthropicProvider, error) {
	return &AnthropicProvider{provider: provider, client: client, proxyScheme: ProxyScheme(proxyURL)}, nil
}

// NewAnthropicProvider creates a new AnthropicProvider wrapping the given ProviderConfig.
// proxyURL configures an HTTP/HTTPS/SOCKS5 proxy; empty string means no proxy (direct connect).
func NewAnthropicProvider(provider *ProviderConfig, proxyURL string) (*AnthropicProvider, error) {
	client, err := NewHTTPClientWithProxy(proxyURL, 0)
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
	a.dumpWriter = w
}

// --- Anthropic API request/response structures ---

// anthropicRequest is the top-level request body for the Messages API.
type anthropicRequest struct {
	Model        string                 `json:"model"`
	MaxTokens    int                    `json:"max_tokens"`
	System       []anthropicContent     `json:"system,omitempty"`
	Messages     []anthropicMessage     `json:"messages"`
	Tools        []anthropicTool        `json:"tools,omitempty"`
	Stream       bool                   `json:"stream"`
	Thinking     *anthropicThinking     `json:"thinking,omitempty"`
	OutputConfig *anthropicOutputConfig `json:"output_config,omitempty"`
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
	Content      string                `json:"content,omitempty"`       // tool_result: the result text
	Source       *anthropicImageSource `json:"source,omitempty"`        // image block
	CacheControl *anthropicCacheCtrl   `json:"cache_control,omitempty"` // prompt caching marker
}

// anthropicImageSource is the source object for an image content block.
type anthropicImageSource struct {
	Type      string `json:"type"`       // always "base64"
	MediaType string `json:"media_type"` // e.g. "image/png"
	Data      string `json:"data"`       // base64-encoded image bytes
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
	at := tuning.Anthropic
	at, err := validateAnthropicTuning(at)
	if err != nil {
		return nil, fmt.Errorf("validate anthropic tuning: %w", err)
	}
	transportCompat := a.provider.AnthropicTransportCompat()
	if transportCompat != nil && transportCompat.SystemPrefix != "" {
		systemPrompt = transportCompat.SystemPrefix + systemPrompt
	}

	// Build system content blocks.
	systemBlocks := buildSystemBlocks(systemPrompt)

	// Convert internal messages to Anthropic API format.
	apiMessages := convertMessages(messages)

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
	if userID := stableAnthropicMetadataUserID(a.provider, transportCompat); userID != "" {
		reqBody.Metadata = &anthropicMetadata{UserID: userID}
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request body: %w", err)
	}
	dumpRequestBody := append([]byte(nil), bodyBytes...)

	// Build the HTTP request with a derived context for per-chunk timeout enforcement.
	streamCtx, streamCancel := context.WithCancel(ctx)
	defer streamCancel()
	req, err := http.NewRequestWithContext(streamCtx, http.MethodPost, a.provider.APIURL(), bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	if betaHeader := anthropicBetaHeader(at, transportCompat); betaHeader != "" {
		req.Header.Set("anthropic-beta", betaHeader)
	}
	if transportCompat != nil && transportCompat.UserAgent != "" {
		req.Header.Set("User-Agent", transportCompat.UserAgent)
	}

	log.Debugf("anthropic request model=%v max_tokens=%v thinking_type=%v thinking_budget=%v messages=%v tools=%v", model, maxTokens, at.ThinkingType, at.ThinkingBudget, len(messages), len(tools))

	// Apply request body compression if configured
	req, _ = compressRequestBody(req, bodyBytes, a.provider.CompressEnabled())

	// Send the request.
	start := time.Now()
	if a.proxyScheme != "" {
		log.Debugf("LLM request via proxy provider=%v scheme=%v", "anthropic", a.proxyScheme)
	}
	cb(message.StreamDelta{Type: "status", Status: &message.StatusDelta{Type: "connecting"}})
	httpResp, err := a.client.Do(req)
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

	cb(message.StreamDelta{Type: "status", Status: &message.StatusDelta{Type: "waiting_headers"}, Progress: &message.StreamProgressDelta{Bytes: responseHeaderBytes(httpResp)}})

	// Handle non-2xx responses.
	if httpResp.StatusCode != http.StatusOK {
		apiErr := parseHTTPError(httpResp)
		// Dump error response if enabled.
		if a.dumpWriter != nil {
			dumpWriter := a.dumpWriter
			go func() {
				dump := &LLMDump{
					Timestamp:   start.Format(time.RFC3339Nano),
					Provider:    "anthropic",
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

	// Parse the SSE stream, collecting chunks for dump if enabled.
	var collector *SSECollector
	if a.dumpWriter != nil {
		collector = NewSSECollector()
	}
	cr := NewChunkTimeoutReader(httpResp.Body, DefaultChunkTimeout, streamCancel)
	defer cr.Stop()
	resp, parseErr := parseSSEStream(cr, cb, collector)

	// Write dump asynchronously.
	if a.dumpWriter != nil {
		dumpWriter := a.dumpWriter
		go func() {
			dump := &LLMDump{
				Timestamp:   start.Format(time.RFC3339Nano),
				Provider:    "anthropic",
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

// parseHTTPError converts a non-200 HTTP response into an APIError.
func parseHTTPError(resp *http.Response) *APIError {
	apiErr := &APIError{
		StatusCode: resp.StatusCode,
	}

	// Parse Retry-After header if present.
	if ra := resp.Header.Get("Retry-After"); ra != "" {
		if seconds, err := strconv.Atoi(ra); err == nil {
			apiErr.RetryAfter = durationFromPositiveSecondsClamped(int64(seconds), 0)
		} else if t, err := http.ParseTime(ra); err == nil {
			apiErr.RetryAfter = time.Until(t)
			if apiErr.RetryAfter < 0 {
				apiErr.RetryAfter = 0
			}
		}
	}

	// Try to parse JSON error body. Read up to 4KB to avoid memory issues
	// with large error responses, then discard the rest to ensure the body
	// is fully consumed before Close().
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		apiErr.Message = fmt.Sprintf("HTTP %d (failed to read body: %v)", resp.StatusCode, err)
		return apiErr
	}
	// Discard any remaining body content to ensure clean connection reuse.
	io.Copy(io.Discard, resp.Body)

	var errResp anthropicErrorResponse
	if err := json.Unmarshal(body, &errResp); err == nil && errResp.Error.Message != "" {
		apiErr.Message = errResp.Error.Message
	} else {
		// Fallback: use raw body (truncated).
		msg := string(body)
		if len(msg) > 200 {
			msg = msg[:200] + "..."
		}
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
	mode, err := normalizeAnthropicPromptCacheMode(tuning.PromptCacheMode)
	if err != nil {
		return tuning, err
	}
	tuning.PromptCacheMode = mode
	return tuning, nil
}

func anthropicBetaHeader(tuning AnthropicTuning, compat *config.AnthropicTransportCompatConfig) string {
	var betas []string
	if effectiveAnthropicThinkingType(tuning) == "enabled" && tuning.ThinkingBudget > 0 {
		betas = append(betas, "interleaved-thinking-2025-05-14")
	}
	if compat != nil && len(compat.ExtraBeta) > 0 {
		betas = append(betas, compat.ExtraBeta...)
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

func stableAnthropicMetadataUserID(provider *ProviderConfig, compat *config.AnthropicTransportCompatConfig) string {
	if compat == nil || !compat.MetadataUserID || provider == nil {
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

	raw := strings.Join([]string{
		"chord-anthropic-user",
		provider.Name(),
		configHome,
		username,
	}, "|")
	if raw == "" {
		return ""
	}

	sum := sha256.Sum256([]byte(raw))
	return "chord_" + hex.EncodeToString(sum[:8])
}

// convertMessages converts internal Message slices to Anthropic API format.
// Adjacent tool results (Role="tool") are merged into a single user message
// with multiple tool_result content blocks.
func convertMessages(msgs []message.Message) []anthropicMessage {
	var result []anthropicMessage

	i := 0
	for i < len(msgs) {
		msg := msgs[i]

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
					default: // "text"
						blocks = append(blocks, anthropicContent{Type: "text", Text: p.Text})
					}
				}
				result = append(result, anthropicMessage{Role: "user", Content: blocks})
			} else {
				// Plain text user message.
				result = append(result, anthropicMessage{
					Role:    "user",
					Content: []anthropicContent{{Type: "text", Text: msg.Content}},
				})
			}
			i++

		case "tool":
			// Collect adjacent tool results into a single user message.
			var toolResults []anthropicContent
			for i < len(msgs) && msgs[i].Role == "tool" {
				// Skip tool results with empty id — they correspond to malformed
				// tool calls (e.g. from GLM) that were also skipped above.
				if msgs[i].ToolCallID == "" {
					log.Warn("skipping tool_result with empty tool_use_id in history")
					i++
					continue
				}
				toolResults = append(toolResults, anthropicContent{
					Type:      "tool_result",
					ToolUseID: msgs[i].ToolCallID,
					Content:   msgs[i].Content,
				})
				i++
			}
			if len(toolResults) == 0 {
				continue
			}
			result = append(result, anthropicMessage{
				Role:    "user",
				Content: toolResults,
			})

		case "assistant":
			var content []anthropicContent

			// Thinking blocks must come before text/tool_use in the assistant message.
			// Anthropic requires them to be replayed verbatim (including signature).
			for _, tb := range msg.ThinkingBlocks {
				content = append(content, anthropicContent{
					Type:      "thinking",
					Thinking:  tb.Thinking,
					Signature: tb.Signature,
				})
			}

			// Add text content if present.
			if msg.Content != "" {
				content = append(content, anthropicContent{
					Type: "text",
					Text: msg.Content,
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
					args = json.RawMessage(`{"error":"malformed tool call arguments from model"}`)
				}
				content = append(content, anthropicContent{
					Type:  "tool_use",
					ID:    tc.ID,
					Name:  tc.Name,
					Input: args,
				})
			}

			// Ensure at least one content block.
			if len(content) == 0 {
				content = append(content, anthropicContent{
					Type: "text",
					Text: "",
				})
			}

			result = append(result, anthropicMessage{
				Role:    "assistant",
				Content: content,
			})
			i++

		default:
			// Unknown role; skip.
			log.Warnf("skipping message with unknown role role=%v", msg.Role)
			i++
		}
	}

	return result
}

// applyCacheBreakpoints applies the 4-breakpoint cache_control strategy:
// 1. system[0]: first system block
// 2. system[-1]: last system block (if different from first)
// 3. Last user message's last content block
// 4. Last assistant message's last non-thinking content block
func applyCacheBreakpoints(system []anthropicContent, messages []anthropicMessage) {
	ephemeral := &anthropicCacheCtrl{Type: "ephemeral"}

	// Breakpoint 1: system[0]
	if len(system) > 0 {
		system[0].CacheControl = ephemeral
	}

	// Breakpoint 2: system[-1] (only if different from system[0])
	if len(system) > 1 {
		system[len(system)-1].CacheControl = ephemeral
	}

	// Find last user and last assistant messages.
	lastUserIdx := -1
	lastAssistantIdx := -1
	for i := len(messages) - 1; i >= 0; i-- {
		if lastUserIdx < 0 && messages[i].Role == "user" {
			lastUserIdx = i
		}
		if lastAssistantIdx < 0 && messages[i].Role == "assistant" {
			lastAssistantIdx = i
		}
		if lastUserIdx >= 0 && lastAssistantIdx >= 0 {
			break
		}
	}

	// Breakpoint 3: last user message's last content block.
	if lastUserIdx >= 0 {
		if blocks, ok := messages[lastUserIdx].Content.([]anthropicContent); ok && len(blocks) > 0 {
			blocks[len(blocks)-1].CacheControl = ephemeral
			messages[lastUserIdx].Content = blocks
		}
	}

	// Breakpoint 4: last assistant message's last non-thinking content block.
	if lastAssistantIdx >= 0 {
		if blocks, ok := messages[lastAssistantIdx].Content.([]anthropicContent); ok && len(blocks) > 0 {
			// Find the last non-thinking block.
			for j := len(blocks) - 1; j >= 0; j-- {
				if blocks[j].Type != "thinking" {
					blocks[j].CacheControl = ephemeral
					break
				}
			}
			messages[lastAssistantIdx].Content = blocks
		}
	}
}

// convertToolsWithCache converts tool definitions; marks the last tool with a
// cache_control breakpoint when CacheTools is enabled (explicit mode).
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
		applyCacheBreakpoints(req.System, req.Messages)
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
