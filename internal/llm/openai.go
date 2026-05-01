package llm

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"github.com/keakon/golog/log"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	sonicjson "github.com/bytedance/sonic"

	"github.com/keakon/chord/internal/message"
)

// OpenAIProvider implements streaming completion against the OpenAI Chat Completions API.
type OpenAIProvider struct {
	provider          *ProviderConfig
	client            *http.Client
	dumpWriter        *DumpWriter // optional: when non-nil, each request/response is dumped to disk
	proxyScheme       string      // "http"/"https"/"socks5" when using proxy, "" otherwise (for request logging)
	responsesProvider *ResponsesProvider
}

// NewOpenAIProviderWithClient creates an OpenAI provider using a caller-supplied HTTP client.
func NewOpenAIProviderWithClient(provider *ProviderConfig, client *http.Client, proxyURL string) (*OpenAIProvider, error) {
	rp, err := NewResponsesProviderWithClient(provider, client, proxyURL)
	if err != nil {
		return nil, err
	}
	return &OpenAIProvider{
		provider:          provider,
		client:            client,
		proxyScheme:       ProxyScheme(proxyURL),
		responsesProvider: rp,
	}, nil
}

// NewOpenAIProvider creates a new OpenAIProvider wrapping the given ProviderConfig.
// proxyURL configures an HTTP/HTTPS/SOCKS5 proxy; empty string means no proxy (direct connect).
func NewOpenAIProvider(provider *ProviderConfig, proxyURL string) (*OpenAIProvider, error) {
	client, err := NewHTTPClientWithProxy(proxyURL, 0)
	if err != nil {
		return nil, fmt.Errorf("create HTTP client for openai provider: %w", err)
	}
	rp := &ResponsesProvider{
		provider:    provider,
		client:      client,
		proxyScheme: ProxyScheme(proxyURL),
	}
	return &OpenAIProvider{
		provider:          provider,
		client:            client,
		proxyScheme:       ProxyScheme(proxyURL),
		responsesProvider: rp,
	}, nil
}

// SetDumpWriter enables LLM request/response dumping for debugging.
func (o *OpenAIProvider) SetDumpWriter(w *DumpWriter) {
	o.dumpWriter = w
	if o.responsesProvider != nil {
		o.responsesProvider.SetDumpWriter(w)
	}
}

// SetSessionID sets the persistent session identifier for prompt caching.
func (o *OpenAIProvider) SetSessionID(sid string) {
	if o.responsesProvider != nil {
		o.responsesProvider.SetSessionID(sid)
	}
}

// openAIStreamOptions controls extra streaming behaviour.
type openAIStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// openAIRequest is the request body for the Chat Completions API.
type openAIRequest struct {
	Model               string               `json:"model"`
	Messages            []openAIMessage      `json:"messages"`
	Tools               []openAITool         `json:"tools,omitempty"`
	MaxTokens           int                  `json:"max_tokens,omitempty"`
	MaxCompletionTokens int                  `json:"max_completion_tokens,omitempty"`
	Stream              bool                 `json:"stream"`
	StreamOptions       *openAIStreamOptions `json:"stream_options,omitempty"`
	Temperature         float64              `json:"temperature,omitempty"`
	ReasoningEffort     string               `json:"reasoning_effort,omitempty"`
	Verbosity           string               `json:"verbosity,omitempty"`
}

// openAIMessage is a single message in the OpenAI API format.
type openAIMessage struct {
	Role       string           `json:"role"`
	Content    any              `json:"content"` // string or []openAIContentBlock
	Name       string           `json:"name,omitempty"`
	ToolCalls  []openAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
}

// openAIContentBlock is a content block (text, image_url, or tool result).
type openAIContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ImageURL  *openAIImageURL `json:"image_url,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
}

// openAIImageURL holds the image URL for an image_url content block.
type openAIImageURL struct {
	URL string `json:"url"`
}

// openAIToolCall is a tool call in assistant message.
type openAIToolCall struct {
	// Index is only present in streaming response deltas (identifies which
	// tool call a chunk belongs to). It must be omitted from request bodies
	// where it is always zero and confuses strict OpenAI-compatible APIs.
	Index    int            `json:"index,omitempty"`
	ID       string         `json:"id"`
	Type     string         `json:"type"`
	Function openAIFunction `json:"function"`
}

// openAIFunction is the function definition in a tool call.
type openAIFunction struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// openAITool is a tool definition in the OpenAI API format.
type openAITool struct {
	Type     string            `json:"type"`
	Function openAIFunctionDef `json:"function"`
}

// openAIFunctionDef is the function definition.
type openAIFunctionDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type openAIStreamChoice struct {
	Index        int               `json:"index"`
	Delta        openAIStreamDelta `json:"delta"`
	FinishReason *string           `json:"finish_reason"`
}

type openAIStreamDelta struct {
	Role             string           `json:"role,omitempty"`
	Content          *string          `json:"content,omitempty"`
	ReasoningContent *string          `json:"reasoning_content,omitempty"`
	ToolCalls        []openAIToolCall `json:"tool_calls,omitempty"`
}

// openAIStreamChunk is a single SSE chunk in streaming mode.
type openAIStreamChunk struct {
	ID      string               `json:"id"`
	Model   string               `json:"model"`
	Choices []openAIStreamChoice `json:"choices"`
	Usage   *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage,omitempty"`
}

// openAIErrorResponse is returned for non-2xx responses.
type openAIErrorResponse struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error"`
}

func (o *OpenAIProvider) CompleteStream(
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
	if o.provider != nil && o.provider.isOpenAIOAuthKey(apiKey) {
		return o.responsesProvider.CompleteStream(ctx, apiKey, model, systemPrompt, messages, tools, maxTokens, tuning, cb)
	}

	ot := tuning.OpenAI
	// Convert messages to OpenAI format.
	apiMessages := convertMessagesToOpenAI(systemPrompt, messages)

	// Convert tools.
	apiTools := convertToolsToOpenAI(tools)

	// Build request body.
	url := o.provider.APIURL()
	reqBody := openAIRequest{
		Model:    model,
		Messages: apiMessages,
		Tools:    apiTools,
		Stream:   true,
	}
	// Request usage stats in the final streaming chunk.
	// Without this, OpenAI-compatible APIs never populate chunk.Usage
	// and token counts remain 0.
	reqBody.StreamOptions = &openAIStreamOptions{IncludeUsage: true}

	if ot.ReasoningEffort != "" {
		// Reasoning models (o1/o3/o4-mini) require max_completion_tokens
		// instead of max_tokens, and do not support temperature.
		reqBody.ReasoningEffort = ot.ReasoningEffort
		if maxTokens > 0 {
			reqBody.MaxCompletionTokens = maxTokens
			reqBody.MaxTokens = 0
		}
	} else if maxTokens > 0 {
		reqBody.MaxTokens = maxTokens
	}

	if ot.TextVerbosity != "" {
		reqBody.Verbosity = ot.TextVerbosity
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request body: %w", err)
	}
	dumpRequestBody := append([]byte(nil), bodyBytes...)

	// Build HTTP request with a derived context for per-chunk timeout enforcement.
	streamCtx, streamCancel := context.WithCancel(ctx)
	defer streamCancel()
	req, err := http.NewRequestWithContext(streamCtx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	// Apply request body compression if configured
	req, _ = compressRequestBody(req, bodyBytes, o.provider.CompressEnabled())

	log.Debugf("openai request model=%v max_tokens=%v messages=%v tools=%v", model, maxTokens, len(messages), len(tools))

	// Send request.
	start := time.Now()
	if o.proxyScheme != "" {
		log.Debugf("LLM request via proxy provider=%v scheme=%v", "openai", o.proxyScheme)
	}
	cb(message.StreamDelta{Type: "status", Status: &message.StatusDelta{Type: "connecting"}})
	httpResp, err := o.client.Do(req)
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
		// Read up to 4KB for error logging to avoid memory issues with large responses.
		errBody, _ := io.ReadAll(io.LimitReader(httpResp.Body, 4096))
		// Discard any remaining body content to ensure clean connection reuse.
		io.Copy(io.Discard, httpResp.Body)
		log.Debugf("openai error response status=%v body_len=%v", httpResp.StatusCode, len(errBody))
		apiErr := parseOpenAIHTTPErrorFromBytes(httpResp.StatusCode, httpResp.Header, errBody)
		// Dump error response if enabled.
		if o.dumpWriter != nil {
			dumpWriter := o.dumpWriter
			go func() {
				dump := &LLMDump{
					Timestamp:   start.Format(time.RFC3339Nano),
					Provider:    "openai",
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

	// Parse SSE stream, collecting chunks for dump if enabled.
	var collector *SSECollector
	if o.dumpWriter != nil {
		collector = NewSSECollector()
	}
	cr := NewChunkTimeoutReader(httpResp.Body, DefaultChunkTimeout, streamCancel)
	defer cr.Stop()
	resp, parseErr := parseOpenAISSEStream(cr, cb, collector)

	// Write dump asynchronously (whether success or failure).
	if o.dumpWriter != nil {
		dumpWriter := o.dumpWriter
		go func() {
			dump := &LLMDump{
				Timestamp:   start.Format(time.RFC3339Nano),
				Provider:    "openai",
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

func responseHeaderBytes(resp *http.Response) int64 {
	if resp == nil {
		return 0
	}
	n := 0
	if resp.Proto != "" {
		n += len(resp.Proto) + 1
	}
	n += 3
	if resp.Status != "" {
		n += len(resp.Status)
	}
	n += 2
	for name, values := range resp.Header {
		for _, value := range values {
			n += len(name) + 2 + len(value) + 2
		}
	}
	n += 2
	return int64(n)
}

// convertMessagesToOpenAI converts internal messages to OpenAI API format.
func convertMessagesToOpenAI(systemPrompt string, msgs []message.Message) []openAIMessage {
	var result []openAIMessage

	// Add system prompt as first message.
	if systemPrompt != "" {
		result = append(result, openAIMessage{
			Role:    "system",
			Content: systemPrompt,
		})
	}

	for _, msg := range msgs {
		switch msg.Role {
		case "user":
			if len(msg.Parts) > 0 {
				// Multi-part message (may include images).
				var blocks []openAIContentBlock
				for _, p := range msg.Parts {
					switch p.Type {
					case "image":
						blocks = append(blocks, openAIContentBlock{
							Type: "image_url",
							ImageURL: &openAIImageURL{
								URL: "data:" + p.MimeType + ";base64," + encodeBase64Cached(p.Data),
							},
						})
					default: // "text"
						blocks = append(blocks, openAIContentBlock{Type: "text", Text: p.Text})
					}
				}
				result = append(result, openAIMessage{Role: "user", Content: blocks})
			} else {
				result = append(result, openAIMessage{
					Role:    "user",
					Content: msg.Content,
				})
			}

		case "assistant":
			omi := openAIMessage{
				Role: "assistant",
			}
			// OpenAI requires content to be null (not empty string) when
			// tool_calls are present; set it only when there is actual text.
			if msg.Content != "" {
				omi.Content = msg.Content
			} else if len(msg.ToolCalls) == 0 {
				// Some OpenAI-compatible APIs reject content:null when there are
				// no tool calls; use an explicit empty string instead.
				omi.Content = ""
			}
			// Convert tool calls.
			for _, tc := range msg.ToolCalls {
				// Skip tool calls with empty id or name — these are malformed
				// responses from some models (e.g. GLM) that omit these fields.
				// Sending them would cause 400 errors from the API.
				if tc.ID == "" || tc.Name == "" {
					log.Warnf("skipping tool call with empty id or name in history tool=%v id=%v", tc.Name, tc.ID)
					continue
				}
				// Sanitize tool call arguments: ensure they are valid JSON
				// before sending to the API. Malformed args (e.g. from a
				// truncated model response) would cause a 400 error from
				// the API server when it tries to parse the arguments field.
				args := tc.Args
				if len(args) == 0 || !json.Valid(args) {
					log.Warnf("sanitizing invalid tool call args in conversation history tool=%v id=%v raw_args=%v", tc.Name, tc.ID, string(args))
					args = json.RawMessage(`{"error":"malformed tool call arguments from model"}`)
				}
				// The OpenAI Chat Completions API requires function.arguments
				// to be a JSON *string* (e.g. "{\"path\":\"README.md\"}"),
				// not a raw JSON object.  tc.Args is json.RawMessage holding
				// the parsed object bytes, so we must wrap it as a JSON string.
				argsStr, err := json.Marshal(string(args))
				if err != nil {
					// Should never happen — string is always marshalable.
					argsStr = args
				}
				omi.ToolCalls = append(omi.ToolCalls, openAIToolCall{
					ID:   tc.ID,
					Type: "function",
					Function: openAIFunction{
						Name:      tc.Name,
						Arguments: json.RawMessage(argsStr),
					},
				})
			}
			// If all tool calls were skipped (e.g. all had empty id/name) and
			// there is no text content either, the assistant message would have
			// null content and no tool_calls — some APIs (e.g. Qwen) reject
			// this. Ensure content is at least an empty string.
			if omi.Content == nil && len(omi.ToolCalls) == 0 {
				omi.Content = ""
			}
			result = append(result, omi)

		case "tool":
			// Skip tool results with empty call id — they correspond to malformed
			// tool calls (e.g. from GLM) that were also skipped above.
			if msg.ToolCallID == "" {
				log.Warn("skipping tool result with empty tool_call_id in history")
				continue
			}
			result = append(result, openAIMessage{
				Role:       "tool",
				Content:    msg.Content,
				ToolCallID: msg.ToolCallID,
			})
		}
	}

	return result
}

// convertToolsToOpenAI converts internal tool definitions to OpenAI format.
func convertToolsToOpenAI(tools []message.ToolDefinition) []openAITool {
	if len(tools) == 0 {
		return nil
	}

	result := make([]openAITool, len(tools))
	for i, t := range tools {
		result[i] = openAITool{
			Type: "function",
			Function: openAIFunctionDef{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.InputSchema,
			},
		}
	}
	return result
}

// parseOpenAIHTTPErrorFromBytes parses an OpenAI error response from a status code,
// headers, and body bytes.
func parseOpenAIHTTPErrorFromBytes(statusCode int, header http.Header, body []byte) *APIError {
	apiErr := &APIError{
		StatusCode: statusCode,
	}

	// Parse Retry-After header: try integer seconds first, then HTTP-date.
	if ra := header.Get("Retry-After"); ra != "" {
		if seconds, err := strconv.Atoi(ra); err == nil {
			apiErr.RetryAfter = durationFromPositiveSecondsClamped(int64(seconds), 0)
		} else if t, err := http.ParseTime(ra); err == nil {
			apiErr.RetryAfter = time.Until(t)
			if apiErr.RetryAfter < 0 {
				apiErr.RetryAfter = 0
			}
		}
	}

	var errResp openAIErrorResponse
	if err := json.Unmarshal(body, &errResp); err == nil && errResp.Error.Message != "" {
		apiErr.Message = errResp.Error.Message
		apiErr.Code = errResp.Error.Code
		apiErr.Type = errResp.Error.Type
	} else {
		msg := string(body)
		if len(msg) > 200 {
			msg = msg[:200] + "..."
		}
		apiErr.Message = msg
	}

	return apiErr
}

// openAIToolAccumulator tracks an in-progress tool call during streaming.
// OpenAI sends tool calls incrementally: the first chunk carries id + name,
// subsequent chunks carry argument fragments, and finish_reason arrives in a
// separate final chunk with no tool_calls in the delta.
type openAIToolAccumulator struct {
	id   string
	name string
	args strings.Builder
}

var thinkingToolcallFunctionPattern = regexp.MustCompile(`functions\.[A-Za-z_][A-Za-z0-9_]*:`)

func hasThinkingToolcallMarkers(text string) bool {
	// Strict detection: require BOTH a marker AND the function pattern.
	// This avoids false positives when "functions" appears in normal text.
	// Valid markers: <|tool_call_begin|> or <|tool_calls_section_begin|>
	// Required together with: functions.ToolName:
	hasCallBegin := strings.Contains(text, "<|tool_call_begin|>")
	hasSectionBegin := strings.Contains(text, "<|tool_calls_section_begin|>")
	hasArgBegin := strings.Contains(text, "<|tool_call_argument_begin|>")
	hasMarker := (hasCallBegin || hasSectionBegin) && hasArgBegin
	hasFuncPattern := thinkingToolcallFunctionPattern.MatchString(text)
	return hasMarker && hasFuncPattern
}

// parseOpenAISSEStream reads an OpenAI SSE stream and calls cb for each delta.
// If collector is non-nil, raw SSE data lines are recorded for debug dumps.
func parseOpenAISSEStream(reader io.Reader, cb StreamCallback, collector *SSECollector) (*message.Response, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var (
		resp      message.Response
		content   strings.Builder
		toolCalls = make(map[int]*openAIToolAccumulator) // index → accumulator
		// inThinking tracks whether we are currently inside a reasoning_content
		inThinking     bool
		truncated      bool
		gotData        bool
		progressBytes  int64
		progressEvents int64
		// reasoningBuf accumulates full reasoning_content text so that when

		// pseudo tool-call markers are detected, the agent layer can parse
		// tool calls from it.
		reasoningBuf strings.Builder
		// thinkingToolcallMarkerHit marks that reasoning_content contained pseudo
		// tool-call templates. This is diagnostics/compat metadata only.
		thinkingToolcallMarkerHit bool
		// reasoningTail keeps a short suffix to detect markers spanning SSE chunks.
		reasoningTail string
		// sawDataLine tracks whether we saw at least one SSE data line.
		sawDataLine bool
	)
	flushContent := func() {
		if content.Len() == 0 {
			resp.Content = ""
			return
		}
		resp.Content = content.String()
	}

	for scanner.Scan() {
		line := scanner.Bytes()

		if !gotData && cb != nil {
			cb(message.StreamDelta{Type: "status", Status: &message.StatusDelta{Type: "waiting_token"}})
			gotData = true
		}

		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		sawDataLine = true

		data := bytes.TrimSpace(line[len("data:"):])
		if cb != nil {
			progressBytes += int64(len(line) + 1)
			progressEvents++
			cb(message.StreamDelta{Progress: &message.StreamProgressDelta{Bytes: progressBytes, Events: progressEvents}})
		}

		// Record raw SSE data for dump if collector is present.
		if collector != nil {
			collector.Add(string(data))
		}

		// [DONE] signals end of stream.
		if bytes.Equal(data, []byte("[DONE]")) {
			if inThinking && cb != nil {
				cb(message.StreamDelta{Type: "thinking_end"})
				inThinking = false
			}
			// Finalize any remaining tool calls (defensive — normally
			// finalized on finish_reason).
			finalizeToolCalls(toolCalls, &resp, cb, truncated)
			resp.ThinkingToolcallMarkerHit = thinkingToolcallMarkerHit
			if thinkingToolcallMarkerHit {
				resp.ReasoningContent = reasoningBuf.String()
			}
			flushContent()
			return &resp, nil
		}

		if len(data) == 0 {
			// Defensive: ignore empty data lines.
			continue
		}

		var chunk openAIStreamChunk
		if err := sonicjson.ConfigStd.Unmarshal(data, &chunk); err != nil {
			return nil, fmt.Errorf("parse stream chunk: %w", err)
		}

		// Process choices.
		for _, choice := range chunk.Choices {
			// Handle reasoning_content (thinking) delta — emitted by
			// DeepSeek-R1, ZhipuAI GLM and other OpenAI-compatible providers.
			if choice.Delta.ReasoningContent != nil && *choice.Delta.ReasoningContent != "" {
				thinking := *choice.Delta.ReasoningContent
				reasoningBuf.WriteString(thinking)
				if !thinkingToolcallMarkerHit {
					candidate := reasoningTail + thinking
					if hasThinkingToolcallMarkers(candidate) {
						thinkingToolcallMarkerHit = true
					}
					if len(candidate) > 128 {
						reasoningTail = candidate[len(candidate)-128:]
					} else {
						reasoningTail = candidate
					}
				}
				if !inThinking {
					inThinking = true
					if p, ok := reader.(chunkPhaser); ok {
						p.SetChunkTimeout(SlowPhaseChunkTimeout)
					}
				}
				if cb != nil {
					cb(message.StreamDelta{Type: "thinking", Text: thinking})
				}
			}

			// Handle content delta.
			if choice.Delta.Content != nil {
				text := *choice.Delta.Content
				// Only end thinking when real (non-empty) content arrives.
				// Some models (e.g. Qwen on ModelScope) send content:""
				// alongside reasoning_content during the thinking phase;
				// treating empty content as the end of thinking would
				// fragment the thinking block into many tiny parts.
				if inThinking && text != "" {
					inThinking = false
					if cb != nil {
						cb(message.StreamDelta{Type: "thinking_end"})
					}
					if p, ok := reader.(chunkPhaser); ok {
						p.SetChunkTimeout(DefaultChunkTimeout)
					}
				}
				if text != "" && cb != nil {
					cb(message.StreamDelta{
						Type: "text",
						Text: text,
					})
				}
				content.WriteString(text)
			}

			// Handle tool call deltas — accumulate across chunks.
			for _, tc := range choice.Delta.ToolCalls {
				// Close any open thinking block before the first tool call
				// chunk. Some OpenAI-compatible providers (e.g. GLM, DeepSeek)
				// interleave reasoning_content with tool_calls without emitting
				// a content field between them, so thinking_end would otherwise
				// never be sent. Without this, the agent-side thinkingActive
				// flag stays true and the TUI creates a second thinking card on
				// the next reasoning delta.
				acc, exists := toolCalls[tc.Index]
				if inThinking && !exists {
					inThinking = false
					if cb != nil {
						cb(message.StreamDelta{Type: "thinking_end"})
					}
					if p, ok := reader.(chunkPhaser); ok {
						p.SetChunkTimeout(DefaultChunkTimeout)
					}
				}
				if !exists {
					// First chunk for this tool call: carries id + name.
					acc = &openAIToolAccumulator{
						id:   tc.ID,
						name: tc.Function.Name,
					}
					toolCalls[tc.Index] = acc
					if cb != nil {
						cb(message.StreamDelta{
							Type: "tool_use_start",
							ToolCall: &message.ToolCallDelta{
								ID:   tc.ID,
								Name: tc.Function.Name,
							},
						})
					}
				}
				// Subsequent chunks carry argument fragments.
				//
				// In the OpenAI streaming format, function.arguments is a JSON string
				// whose decoded value is a fragment of the final arguments JSON object.
				// For example, the wire bytes for one chunk might be:
				//   `"{\""` — the raw JSON representation of the string fragment `{"`
				// We must decode the string value first so we accumulate raw JSON
				// bytes, not nested-quoted escaped text.
				if len(tc.Function.Arguments) > 0 {
					var frag string
					if err := sonicjson.ConfigStd.Unmarshal(tc.Function.Arguments, &frag); err == nil {
						acc.args.WriteString(frag)
					} else {
						// Fallback: some proxies send raw JSON instead of a string.
						acc.args.Write(tc.Function.Arguments)
					}
					if cb != nil {
						cb(message.StreamDelta{
							Type: "tool_use_delta",
							ToolCall: &message.ToolCallDelta{
								ID:    acc.id,
								Name:  acc.name,
								Input: acc.args.String(),
							},
						})
					}
				}
			}

			// Handle finish reason — finalize all accumulated tool calls.
			if choice.FinishReason != nil {
				resp.StopReason = *choice.FinishReason
				switch *choice.FinishReason {
				case "tool_calls":
					finalizeToolCalls(toolCalls, &resp, cb, false)
				case "length":
					// Output was truncated by max_tokens. Any in-progress tool
					// calls have incomplete arguments and must be discarded.
					truncated = true
					log.Warnf("LLM output truncated (finish_reason=length), discarding incomplete tool calls pending_tool_calls=%v", len(toolCalls))
					finalizeToolCalls(toolCalls, &resp, cb, true)
				}
			}
		}

		// Handle usage (only present when stream_options.include_usage=true).
		if chunk.Usage != nil {
			if resp.Usage == nil {
				resp.Usage = &message.TokenUsage{}
			}
			resp.Usage.InputTokens = chunk.Usage.PromptTokens
			resp.Usage.OutputTokens = chunk.Usage.CompletionTokens
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading SSE stream: %w", err)
	}
	if !sawDataLine {
		return nil, fmt.Errorf("empty SSE stream: no data lines")
	}

	// Stream ended without [DONE] — close any open thinking block.
	if inThinking && cb != nil {
		cb(message.StreamDelta{Type: "thinking_end"})
	}

	// Finalize any remaining tool calls if stream ended without [DONE].
	finalizeToolCalls(toolCalls, &resp, cb, truncated)
	resp.ThinkingToolcallMarkerHit = thinkingToolcallMarkerHit
	if thinkingToolcallMarkerHit {
		resp.ReasoningContent = reasoningBuf.String()
	}
	flushContent()
	return &resp, nil
}

// finalizeToolCalls converts all accumulated tool calls into the response and
// emits tool_use_end events. The map is cleared after finalization.
//
// If truncated is true (e.g. finish_reason == "length"), all accumulated tool
// calls are discarded because their arguments are almost certainly incomplete.
func finalizeToolCalls(
	toolCalls map[int]*openAIToolAccumulator,
	resp *message.Response,
	cb StreamCallback,
	truncated bool,
) {
	if len(toolCalls) == 0 {
		return
	}

	// When the output was truncated (e.g. max_tokens hit), the tool call
	// arguments are guaranteed to be incomplete. Discard them and log a
	// warning so the caller can surface an appropriate error message.
	if truncated {
		for idx, acc := range toolCalls {
			log.Warnf("discarding truncated tool call tool=%v id=%v partial_args=%v", acc.name, acc.id, acc.args.String())
			delete(toolCalls, idx)
		}
		return
	}

	// Process in index order for deterministic output.
	indices := make([]int, 0, len(toolCalls))
	for idx := range toolCalls {
		indices = append(indices, idx)
	}
	sort.Ints(indices)

	for _, idx := range indices {
		acc := toolCalls[idx]
		args := json.RawMessage(acc.args.String())
		if len(args) == 0 {
			args = json.RawMessage("{}")
		}
		// Safety net: if the accumulated bytes are still a JSON string literal
		// (starts with '"'), decode the string once to recover the inner JSON.
		// This handles responses where the full arguments arrive as a single
		// chunk and the fragment-decoding loop above was not reached (e.g. when
		// the proxy sends a non-streaming response disguised as streaming).
		if len(args) > 0 && args[0] == '"' {
			log.Debugf("tool call args were JSON string, unwrapping tool=%v raw_args=%v", acc.name, acc.args.String())
			var decoded string
			if err := json.Unmarshal(args, &decoded); err == nil {
				args = json.RawMessage(decoded)
			}
		}
		// Validate that the accumulated arguments are valid JSON. If not
		// (e.g. the model produced truncated output despite a normal
		// finish_reason), replace with a descriptive error object so that
		// downstream tool execution gets a clear error instead of corrupting
		// the conversation history with malformed JSON.
		if !json.Valid(args) {
			log.Warnf("tool call has invalid JSON args, replacing with error object tool=%v id=%v raw_args=%v", acc.name, acc.id, string(args))
			args = json.RawMessage(`{"error":"malformed tool call arguments from model"}`)
		}
		// Discard tool calls with empty id or name — some models (e.g. GLM) omit
		// these fields, producing history entries that cause 400 errors on
		// subsequent requests (Responses API requires a non-empty call_id).
		if acc.id == "" || acc.name == "" {
			log.Warnf("discarding tool call with empty id or name tool=%v id=%v", acc.name, acc.id)
			delete(toolCalls, idx)
			continue
		}
		log.Debugf("finalized tool call tool=%v id=%v args=%v", acc.name, acc.id, string(args))
		resp.ToolCalls = append(resp.ToolCalls, message.ToolCall{
			ID:   acc.id,
			Name: acc.name,
			Args: args,
		})
		if cb != nil {
			cb(message.StreamDelta{
				Type: "tool_use_end",
				ToolCall: &message.ToolCallDelta{
					ID:   acc.id,
					Name: acc.name,
				},
			})
		}
		delete(toolCalls, idx)
	}
}
