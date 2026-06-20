package llm

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	sonicjson "github.com/bytedance/sonic"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/message"
)

// GeminiProvider implements streaming completion against the Google Gemini API.
type GeminiProvider struct {
	provider    *ProviderConfig
	client      *http.Client
	dumpWriter  *DumpWriter
	traceWriter *TraceWriter
	proxyScheme string
}

func NewGeminiProviderWithClient(provider *ProviderConfig, client *http.Client, proxyURL string) (*GeminiProvider, error) {
	if err := validateGeminiAPIURL(provider.APIURL()); err != nil {
		return nil, err
	}
	return &GeminiProvider{provider: provider, client: client, proxyScheme: ProxyScheme(proxyURL)}, nil
}

func NewGeminiProvider(provider *ProviderConfig, proxyURL string) (*GeminiProvider, error) {
	if err := validateGeminiAPIURL(provider.APIURL()); err != nil {
		return nil, err
	}
	client, err := NewHTTPClientWithProxy(proxyURL, providerRequestTimeout(provider))
	if err != nil {
		return nil, fmt.Errorf("create HTTP client for gemini provider: %w", err)
	}
	return &GeminiProvider{provider: provider, client: client, proxyScheme: ProxyScheme(proxyURL)}, nil
}

func validateGeminiAPIURL(apiURL string) error {
	trimmed := strings.TrimSuffix(strings.TrimSpace(apiURL), "/")
	if trimmed == "" || !strings.HasSuffix(trimmed, "/models") {
		return fmt.Errorf("gemini provider requires api_url ending in /models")
	}
	return nil
}

// SetDumpWriter enables LLM request/response dumping for debugging.
func (g *GeminiProvider) SetDumpWriter(w *DumpWriter) {
	g.dumpWriter = w
}

func (g *GeminiProvider) SetTraceWriter(w *TraceWriter) {
	g.traceWriter = w
}

type geminiRequest struct {
	Contents          []geminiContent         `json:"contents"`
	SystemInstruction *geminiContent          `json:"system_instruction,omitempty"`
	Tools             []geminiTool            `json:"tools,omitempty"`
	ToolConfig        *geminiToolConfig       `json:"toolConfig,omitempty"`
	GenerationConfig  *geminiGenerationConfig `json:"generationConfig,omitempty"`
}

type geminiToolConfig struct {
	FunctionCallingConfig *geminiFunctionCallingConfig `json:"functionCallingConfig,omitempty"`
}

type geminiFunctionCallingConfig struct {
	Mode string `json:"mode,omitempty"`
}

type geminiThinkingConfig struct {
	ThinkingBudget  *int   `json:"thinkingBudget,omitempty"`
	ThinkingLevel   string `json:"thinkingLevel,omitempty"`
	IncludeThoughts *bool  `json:"includeThoughts,omitempty"`
}

type geminiGenerationConfig struct {
	MaxOutputTokens int                   `json:"maxOutputTokens,omitempty"`
	ThinkingConfig  *geminiThinkingConfig `json:"thinkingConfig,omitempty"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text             string                  `json:"text,omitempty"`
	InlineData       *geminiInlineData       `json:"inlineData,omitempty"`
	FunctionCall     *geminiFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *geminiFunctionResponse `json:"functionResponse,omitempty"`
	Thought          bool                    `json:"thought,omitempty"`
}

type geminiInlineData struct {
	MimeType    string `json:"mimeType"`
	DisplayName string `json:"displayName,omitempty"`
	Data        string `json:"data"`
}

type geminiFunctionCall struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args,omitempty"`
}

type geminiFunctionResponse struct {
	Name     string         `json:"name"`
	Response map[string]any `json:"response"`
	Parts    []geminiPart   `json:"parts,omitempty"`
}

type geminiTool struct {
	FunctionDeclarations []geminiFunctionDeclaration `json:"functionDeclarations"`
}

type geminiFunctionDeclaration struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

func geminiToolConfigFromTuning(choice string) *geminiToolConfig {
	switch choice {
	case "auto":
		return &geminiToolConfig{FunctionCallingConfig: &geminiFunctionCallingConfig{Mode: "AUTO"}}
	case "required":
		return &geminiToolConfig{FunctionCallingConfig: &geminiFunctionCallingConfig{Mode: "ANY"}}
	case "none":
		return &geminiToolConfig{FunctionCallingConfig: &geminiFunctionCallingConfig{Mode: "NONE"}}
	default:
		return nil
	}
}

type geminiStreamChunk struct {
	Candidates []struct {
		Content      geminiContent `json:"content"`
		FinishReason string        `json:"finishReason"`
	} `json:"candidates"`
	UsageMetadata *struct {
		PromptTokenCount        int `json:"promptTokenCount"`
		CandidatesTokenCount    int `json:"candidatesTokenCount"`
		CachedContentTokenCount int `json:"cachedContentTokenCount"`
		TotalTokenCount         int `json:"totalTokenCount"`
		ThoughtsTokenCount      int `json:"thoughtsTokenCount"`
	} `json:"usageMetadata,omitempty"`
}

type geminiErrorResponse struct {
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error"`
}

func (g *GeminiProvider) CompleteStream(
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
	traceCollector := newLLMTraceCollector("gemini", model, cb)
	traceCB := traceCollector.Callback
	contents := convertMessagesToGemini(messages)
	reqBody := geminiRequest{
		Contents: contents,
		Tools:    convertToolsToGemini(tools),
	}
	if tuning.Gemini.ToolChoice != "" {
		reqBody.ToolConfig = geminiToolConfigFromTuning(tuning.Gemini.ToolChoice)
	}
	if systemPrompt != "" {
		reqBody.SystemInstruction = &geminiContent{Parts: []geminiPart{{Text: systemPrompt}}}
	}

	var genCfg geminiGenerationConfig
	if maxTokens > 0 {
		genCfg.MaxOutputTokens = maxTokens
	}
	if tuning.Gemini.ThinkingBudget != nil || tuning.Gemini.ThinkingLevel != "" || tuning.Gemini.IncludeThoughts != nil {
		// Gemini thinkingLevel values are documented as lowercase strings in the
		// public Gemini API docs (e.g. "minimal"|"low"|"medium"|"high"). Keep the
		// configured casing as-is.
		genCfg.ThinkingConfig = &geminiThinkingConfig{ThinkingBudget: tuning.Gemini.ThinkingBudget, ThinkingLevel: tuning.Gemini.ThinkingLevel, IncludeThoughts: tuning.Gemini.IncludeThoughts}
	}
	if genCfg.MaxOutputTokens > 0 || genCfg.ThinkingConfig != nil {
		reqBody.GenerationConfig = &genCfg
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request body: %w", err)
	}
	dumpRequestBody := append([]byte(nil), bodyBytes...)

	streamCtx, streamCancel := context.WithCancel(ctx)
	defer streamCancel()
	req, err := http.NewRequestWithContext(streamCtx, http.MethodPost, geminiStreamURL(g.provider.APIURL(), model), bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set(headerContentType, headerValueApplicationJSON)
	req.Header.Set("x-goog-api-key", apiKey)
	setProviderLLMUserAgent(req.Header, g.provider)

	req, _ = compressRequestBody(req, bodyBytes, g.provider.CompressEnabled())

	log.Debugf("gemini request model=%v max_tokens=%v messages=%v tools=%v", model, maxTokens, len(messages), len(tools))

	start := time.Now()
	if g.proxyScheme != "" {
		log.Debugf("LLM request via proxy provider=%v scheme=%v", "gemini", g.proxyScheme)
	}
	traceCB(message.StreamDelta{Type: message.StreamDeltaStatus, Status: &message.StatusDelta{Type: "connecting"}})
	httpResp, err := g.client.Do(req)
	if err != nil {
		callErr := fmt.Errorf("send request: %w", err)
		persistLLMTrace(g.traceWriter, traceCollector, 0, "http", start, nil, callErr)
		return nil, callErr
	}
	defer httpResp.Body.Close()

	if httpResp.Header.Get(headerContentEncoding) == headerValueGzip {
		gr, err := gzip.NewReader(httpResp.Body)
		if err != nil {
			return nil, fmt.Errorf("create gzip reader: %w", err)
		}
		httpResp.Body = gr
	}

	traceCB(message.StreamDelta{Type: message.StreamDeltaStatus, Status: &message.StatusDelta{Type: message.StatusDeltaWaitingHeaders}, Progress: &message.StreamProgressDelta{Bytes: responseHeaderBytes(httpResp)}})

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		errBody, _ := io.ReadAll(io.LimitReader(httpResp.Body, maxHTTPErrorBodyBytes))
		io.Copy(io.Discard, httpResp.Body)
		apiErr := parseGeminiHTTPErrorFromBytes(httpResp.StatusCode, httpResp.Header, errBody)
		if g.dumpWriter != nil {
			dumpWriter := g.dumpWriter
			statusCode, headers := dumpHTTPResponseMetadata(httpResp)
			bodyCopy := string(append([]byte(nil), errBody...))
			go func() {
				dump := &LLMDump{Timestamp: start.Format(time.RFC3339Nano), Provider: "gemini", Model: model, RequestBody: dumpRequestBody, HTTPStatus: statusCode, HTTPHeaders: headers, HTTPBody: bodyCopy, Error: apiErr.Error(), DurationMS: time.Since(start).Milliseconds()}
				if wErr := dumpWriter.Write(dump); wErr != nil {
					log.Warnf("failed to write LLM dump error=%v", wErr)
				}
			}()
		}
		persistLLMTrace(g.traceWriter, traceCollector, httpResp.StatusCode, "http", start, nil, apiErr)
		return nil, apiErr
	}

	var collector *SSECollector
	if g.dumpWriter != nil {
		collector = NewSSECollector()
	}
	cr := NewProviderChunkTimeoutReader(httpResp.Body, g.provider, DefaultChunkTimeout, streamCancel)
	defer cr.Stop()
	resp, parseErr := parseGeminiSSEStream(cr, traceCB, collector)

	if g.dumpWriter != nil {
		dumpWriter := g.dumpWriter
		statusCode, headers := dumpHTTPResponseMetadata(httpResp)
		go func() {
			dump := &LLMDump{Timestamp: start.Format(time.RFC3339Nano), Provider: "gemini", Model: model, RequestBody: dumpRequestBody, HTTPStatus: statusCode, HTTPHeaders: headers, SSEChunks: collector.Chunks(), Response: DumpResponseFromResponse(resp), DurationMS: time.Since(start).Milliseconds()}
			if parseErr != nil {
				dump.Error = parseErr.Error()
			}
			if wErr := dumpWriter.Write(dump); wErr != nil {
				log.Warnf("failed to write LLM dump error=%v", wErr)
			}
		}()
	}
	persistLLMTrace(g.traceWriter, traceCollector, httpResp.StatusCode, "http", start, resp, parseErr)
	return resp, parseErr
}

func geminiStreamURL(apiURL, model string) string {
	base := strings.TrimRight(apiURL, "/")
	model = strings.TrimLeft(model, "/")
	return base + "/" + model + ":streamGenerateContent?alt=sse"
}

func convertMessagesToGemini(msgs []message.Message) []geminiContent {
	var result []geminiContent
	toolNamesByID := make(map[string]string)
	for i := 0; i < len(msgs); {
		msg := msgs[i]
		switch msg.Role {
		case "user":
			result = append(result, geminiContent{Role: "user", Parts: geminiUserParts(msg)})
			i++
		case "assistant":
			parts := make([]geminiPart, 0, 1+len(msg.ToolCalls))
			if msg.Content != "" {
				parts = append(parts, geminiPart{Text: msg.Content})
			}
			for _, tc := range msg.ToolCalls {
				if tc.ID == "" || tc.Name == "" {
					log.Warnf("skipping Gemini functionCall with empty id or name in history tool=%v id=%v", tc.Name, tc.ID)
					continue
				}
				args := tc.Args
				if len(args) == 0 || !json.Valid(args) {
					log.Warnf("sanitizing invalid tool call args in Gemini conversation history tool=%v id=%v raw_args=%v", tc.Name, tc.ID, string(args))
					args = json.RawMessage(MalformedArgsSentinel)
				}
				toolNamesByID[tc.ID] = tc.Name
				parts = append(parts, geminiPart{FunctionCall: &geminiFunctionCall{Name: tc.Name, Args: args}})
			}
			if len(parts) == 0 {
				parts = append(parts, geminiPart{Text: ""})
			}
			result = append(result, geminiContent{Role: "model", Parts: parts})
			i++
		case "tool":
			var parts []geminiPart
			for i < len(msgs) && msgs[i].Role == "tool" {
				toolMsg := msgs[i]
				name := toolNamesByID[toolMsg.ToolCallID]
				if name == "" {
					name = toolMsg.ToolCallID
				}
				if name == "" {
					log.Warn("skipping Gemini functionResponse with empty tool_call_id in history")
					i++
					continue
				}
				parts = append(parts, geminiPart{FunctionResponse: geminiToolFunctionResponse(name, toolMsg)})
				i++
			}
			if len(parts) > 0 {
				result = append(result, geminiContent{Role: "user", Parts: parts})
			}
		default:
			log.Warnf("skipping message with unknown role role=%v", msg.Role)
			i++
		}
	}
	return result
}

func geminiToolFunctionResponse(name string, msg message.Message) *geminiFunctionResponse {
	resp := &geminiFunctionResponse{Name: name, Response: map[string]any{"result": msg.Content}}
	if len(msg.Parts) == 0 {
		return resp
	}
	// The textual result already rides in Response["result"]; parts only needs to
	// carry the binary media (image/PDF) that the response object cannot express.
	resp.Parts = make([]geminiPart, 0, len(msg.Parts))
	for _, p := range msg.Parts {
		switch p.Type {
		case "image":
			resp.Parts = append(resp.Parts, geminiPart{InlineData: &geminiInlineData{MimeType: p.MimeType, DisplayName: p.FileName, Data: encodeBase64Cached(p.Data)}})
		case "pdf":
			resp.Parts = append(resp.Parts, geminiPart{InlineData: &geminiInlineData{MimeType: defaultPDFMediaType(p.MimeType), DisplayName: p.FileName, Data: encodeBase64Cached(p.Data)}})
		}
	}
	return resp
}

func geminiUserParts(msg message.Message) []geminiPart {
	if len(msg.Parts) == 0 {
		return []geminiPart{{Text: msg.Content}}
	}
	parts := make([]geminiPart, 0, len(msg.Parts))
	for _, p := range msg.Parts {
		switch p.Type {
		case "image":
			parts = append(parts, geminiPart{InlineData: &geminiInlineData{MimeType: p.MimeType, Data: encodeBase64Cached(p.Data)}})
		case "pdf":
			parts = append(parts, geminiPart{InlineData: &geminiInlineData{MimeType: defaultPDFMediaType(p.MimeType), Data: encodeBase64Cached(p.Data)}})
		default:
			parts = append(parts, geminiPart{Text: p.Text})
		}
	}
	return parts
}

// convertToolsToGemini converts tool definitions to Gemini format.
// Tools are expected to be in a stable order from Registry.ListDefinitions().
func convertToolsToGemini(tools []message.ToolDefinition) []geminiTool {
	if len(tools) == 0 {
		return nil
	}
	decls := make([]geminiFunctionDeclaration, 0, len(tools))
	for _, t := range tools {
		decls = append(decls, geminiFunctionDeclaration{Name: t.Name, Description: t.Description, Parameters: convertSchemaToGemini(t.InputSchema)})
	}
	return []geminiTool{{FunctionDeclarations: decls}}
}

func convertSchemaToGemini(schema map[string]any) map[string]any {
	if schema == nil {
		return nil
	}
	out := make(map[string]any, len(schema))
	for k, v := range schema {
		switch k {
		case "nullable", "default", "$schema", "additionalProperties", "coerceFromString", "coerceFromObject":
			continue
		case "type":
			if s, ok := v.(string); ok {
				out[k] = strings.ToUpper(s)
				continue
			}
		case "properties":
			if props, ok := v.(map[string]any); ok {
				converted := make(map[string]any, len(props))
				for name, raw := range props {
					if child, ok := raw.(map[string]any); ok {
						converted[name] = convertSchemaToGemini(child)
					} else {
						converted[name] = raw
					}
				}
				out[k] = converted
				continue
			}
		case "items":
			if child, ok := v.(map[string]any); ok {
				out[k] = convertSchemaToGemini(child)
				continue
			}
		}
		out[k] = v
	}
	return out
}

func parseGeminiSSEStream(reader io.Reader, cb StreamCallback, collector *SSECollector) (*message.Response, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var resp message.Response
	var content strings.Builder
	var reasoning strings.Builder
	var inThinking bool
	var toolCalls = make(map[int]*openAIToolAccumulator)
	var nextToolIndex int
	var sawDataLine bool
	var gotData bool
	var progressBytes int64
	var progressEvents int64

	flushContent := func() { resp.Content = content.String() }
	finishThinking := func() {
		if inThinking {
			inThinking = false
			if cb != nil {
				cb(message.StreamDelta{Type: message.StreamDeltaThinkingEnd})
			}
			if p, ok := reader.(chunkPhaser); ok {
				p.SetChunkTimeout(DefaultChunkTimeout)
			}
		}
	}

	for scanner.Scan() {
		line := scanner.Bytes()
		if !gotData && cb != nil {
			cb(message.StreamDelta{Type: message.StreamDeltaStatus, Status: &message.StatusDelta{Type: "waiting_token"}})
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
		if collector != nil {
			collector.Add(string(data))
		}
		if len(data) == 0 {
			continue
		}

		var chunk geminiStreamChunk
		if err := sonicjson.ConfigDefault.Unmarshal(data, &chunk); err != nil {
			return nil, fmt.Errorf("parse Gemini stream chunk: %w", err)
		}

		for _, candidate := range chunk.Candidates {
			for _, part := range candidate.Content.Parts {
				if part.Text != "" {
					if part.Thought {
						reasoning.WriteString(part.Text)
						if !inThinking {
							inThinking = true
							if p, ok := reader.(chunkPhaser); ok {
								p.SetChunkTimeout(SlowPhaseChunkTimeout)
							}
						}
						if cb != nil {
							cb(message.StreamDelta{Type: message.StreamDeltaThinking, Text: part.Text})
						}
					} else {
						finishThinking()
						content.WriteString(part.Text)
						if cb != nil {
							cb(message.StreamDelta{Type: message.StreamDeltaText, Text: part.Text})
						}
					}
				}
				if part.FunctionCall != nil {
					finishThinking()
					idx := nextToolIndex
					nextToolIndex++
					id := fmt.Sprintf("gemini_%d", idx)
					args := part.FunctionCall.Args
					if len(args) == 0 {
						args = json.RawMessage("{}")
					}
					acc := &openAIToolAccumulator{id: id, name: part.FunctionCall.Name}
					acc.args.Write(args)
					toolCalls[idx] = acc
					if cb != nil {
						cb(message.StreamDelta{Type: message.StreamDeltaToolUseStart, ToolCall: &message.ToolCallDelta{ID: id, Name: acc.name}})
						cb(message.StreamDelta{Type: message.StreamDeltaToolUseDelta, ToolCall: &message.ToolCallDelta{ID: id, Name: acc.name, Input: acc.args.String()}})
					}
				}
			}
			if candidate.FinishReason != "" {
				resp.StopReason = cloneLongLivedLLMString(candidate.FinishReason)
				finalizeGeminiToolCalls(toolCalls, &resp, cb, candidate.FinishReason == "MAX_TOKENS")
			}
		}
		if chunk.UsageMetadata != nil {
			if resp.Usage == nil {
				resp.Usage = &message.TokenUsage{}
			}
			resp.Usage.InputTokens = chunk.UsageMetadata.PromptTokenCount
			resp.Usage.OutputTokens = chunk.UsageMetadata.CandidatesTokenCount
			resp.Usage.CacheReadTokens = chunk.UsageMetadata.CachedContentTokenCount
			resp.Usage.ReasoningTokens = chunk.UsageMetadata.ThoughtsTokenCount
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading Gemini SSE stream: %w", err)
	}
	if !sawDataLine {
		return nil, fmt.Errorf("empty SSE stream: no data lines")
	}
	finishThinking()
	finalizeGeminiToolCalls(toolCalls, &resp, cb, false)
	if reasoning.Len() > 0 {
		resp.ReasoningContent = reasoning.String()
	}
	flushContent()
	if resp.StopReason == "MAX_TOKENS" && resp.Content == "" && len(resp.ToolCalls) == 0 && resp.ReasoningContent == "" {
		return &resp, &EmptyTruncationError{}
	}
	if resp.StopReason != "" && resp.Content == "" && len(resp.ToolCalls) == 0 && resp.ReasoningContent == "" {
		return &resp, &EmptyResponseError{}
	}
	return &resp, nil
}

func finalizeGeminiToolCalls(toolCalls map[int]*openAIToolAccumulator, resp *message.Response, cb StreamCallback, truncated bool) {
	if len(toolCalls) == 0 {
		return
	}
	if truncated {
		for idx, acc := range toolCalls {
			log.Warnf("discarding truncated Gemini tool call tool=%v id=%v partial_args=%v", acc.name, acc.id, acc.args.String())
			delete(toolCalls, idx)
		}
		return
	}
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
		if !json.Valid(args) {
			log.Warnf("malformed Gemini tool call arguments tool=%v id=%v raw_args=%v", acc.name, acc.id, string(args))
			args = json.RawMessage(MalformedArgsSentinel)
		}
		resp.ToolCalls = append(resp.ToolCalls, message.ToolCall{ID: cloneLongLivedLLMString(acc.id), Name: cloneLongLivedLLMString(acc.name), Args: args})
		if cb != nil {
			cb(message.StreamDelta{Type: message.StreamDeltaToolUseEnd, ToolCall: &message.ToolCallDelta{ID: acc.id, Name: acc.name, Input: string(args)}})
		}
		delete(toolCalls, idx)
	}
}

func parseGeminiHTTPErrorFromBytes(statusCode int, header http.Header, body []byte) *APIError {
	apiErr := &APIError{StatusCode: statusCode}
	if ra := header.Get("Retry-After"); ra != "" {
		if seconds, err := strconv.Atoi(ra); err == nil {
			apiErr.RetryAfter = durationFromPositiveSecondsClamped(int64(seconds), 0)
		} else if t, err := http.ParseTime(ra); err == nil {
			apiErr.RetryAfter = max(time.Until(t), 0)
		}
	}
	var errResp geminiErrorResponse
	if err := json.Unmarshal(body, &errResp); err == nil && errResp.Error.Message != "" {
		apiErr.Message = errResp.Error.Message
		apiErr.Code = errResp.Error.Status
		apiErr.Type = errResp.Error.Status
		return apiErr
	}
	msg := TruncateStringRunes(string(body), 200, "...")
	apiErr.Message = msg
	return apiErr
}
