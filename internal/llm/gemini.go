package llm

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/httpheader"
	"github.com/keakon/chord/internal/jsonx"
	"github.com/keakon/chord/internal/llm/mediacache"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/texttruncate"
)

// GeminiProvider implements streaming completion against the Google Gemini API.
type GeminiProvider struct {
	provider    *ProviderConfig
	client      *http.Client
	dumpWriter  atomic.Pointer[DumpWriter]
	traceWriter atomic.Pointer[TraceWriter]
	proxyScheme string

	// bodyReuse caches the marshaled request body across a target's key
	// attempts; see requestBodyReuse.
	bodyReuse requestBodyReuse
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
	client, err := NewStreamingHTTPClientWithProxy(proxyURL, providerResponseHeaderTimeout(provider))
	if err != nil {
		return nil, fmt.Errorf("create HTTP client for gemini provider: %w", err)
	}
	return &GeminiProvider{provider: provider, client: client, proxyScheme: ProxyScheme(proxyURL)}, nil
}

func validateGeminiAPIURL(apiURL string) error {
	if !config.IsGeminiAPIURL(apiURL) {
		return fmt.Errorf("gemini provider requires %s", config.GeminiAPIURLRequirement)
	}
	return nil
}

// ReleaseBodyCache drops the cached request body built from this messages
// slice; see requestBodyReuse.release for why the match is slice-scoped.
func (g *GeminiProvider) ReleaseBodyCache(msgs *message.Message, msgsLen int) {
	g.bodyReuse.release(msgs, msgsLen)
}

// SetDumpWriter enables LLM request/response dumping for debugging.
func (g *GeminiProvider) SetDumpWriter(w *DumpWriter) {
	g.dumpWriter.Store(w)
}

func (g *GeminiProvider) SetTraceWriter(w *TraceWriter) {
	g.traceWriter.Store(w)
}

type geminiRequest struct {
	ContinuationToken string                  `json:"continuationToken,omitempty"`
	Contents          []geminiContent         `json:"contents"`
	SystemInstruction *geminiContent          `json:"system_instruction,omitzero"`
	Tools             []geminiTool            `json:"tools,omitempty"`
	ToolConfig        *geminiToolConfig       `json:"toolConfig,omitzero"`
	GenerationConfig  *geminiGenerationConfig `json:"generationConfig,omitzero"`
}

type geminiToolConfig struct {
	FunctionCallingConfig *geminiFunctionCallingConfig `json:"functionCallingConfig,omitzero"`
}

type geminiFunctionCallingConfig struct {
	Mode string `json:"mode,omitempty"`
}

type geminiThinkingConfig struct {
	ThinkingBudget  *int   `json:"thinkingBudget,omitzero"`
	ThinkingLevel   string `json:"thinkingLevel,omitempty"`
	IncludeThoughts *bool  `json:"includeThoughts,omitzero"`
}

// normalizeGeminiThinking collapses the Gemini thinking knobs into a shape the
// API accepts. Gemini rejects a request that carries both thinkingBudget and
// thinkingLevel with a 400, so an explicit level always wins: thinking_level is
// the Gemini 3+ control, while thinking_budget only survives for backward
// compatibility. Callers that configure both (a common copy-paste from older
// recipes) would otherwise fail every request with no local explanation.
//
// This is deliberately the single normalization point: the tuning builders and
// merge layers keep both knobs exactly as configured, and only the request
// body construction here collapses them, so a future merge path cannot ship a
// level-bearing request without passing through this shape.
func normalizeGeminiThinking(t GeminiTuning) GeminiTuning {
	if t.ThinkingLevel != "" {
		t.ThinkingBudget = nil
	}
	return t
}

// geminiThinkingActive reports whether the request lets the model think. Gemini
// models think by default, so anything short of an explicit zero budget counts
// as active; include_thoughts only controls whether the summary is visible.
func geminiThinkingActive(t GeminiTuning) bool {
	return t.ThinkingBudget == nil || *t.ThinkingBudget != 0
}

// geminiIncludeThoughts resolves the include_thoughts default for an already
// normalized thinking shape. Thought signatures are bound to the producing
// model, so the visible thought summary is the only reasoning text that
// survives a later switch to another provider or wire family. Capture it by
// default whenever thinking is active; include_thoughts: false in the model
// config opts out explicitly. Both wire shapes that carry Gemini thinking call
// this, so the native generate-content body and the OpenAI-compatible
// extra_body mapping cannot drift apart on the default.
func geminiIncludeThoughts(t GeminiTuning) *bool {
	if t.IncludeThoughts != nil {
		return t.IncludeThoughts
	}
	if (t.ThinkingBudget != nil && *t.ThinkingBudget != 0) || t.ThinkingLevel != "" {
		return new(true)
	}
	return nil
}

type geminiGenerationConfig struct {
	MaxOutputTokens int                   `json:"maxOutputTokens,omitzero"`
	ThinkingConfig  *geminiThinkingConfig `json:"thinkingConfig,omitzero"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text             string                  `json:"text,omitempty"`
	InlineData       *geminiInlineData       `json:"inlineData,omitzero"`
	FunctionCall     *geminiFunctionCall     `json:"functionCall,omitzero"`
	FunctionResponse *geminiFunctionResponse `json:"functionResponse,omitzero"`
	Thought          bool                    `json:"thought,omitzero"`
	// ThoughtSignature is an opaque signature attached by thinking models.
	// It must be preserved and replayed verbatim with the same part in
	// conversation history (Gemini 3 requires it on functionCall parts).
	ThoughtSignature string `json:"thoughtSignature,omitempty"`
	// Transient marks a part that came from a request-scoped overlay
	// (message.KindTurnOverlay) rather than from a turn the user took. The
	// native wire has no other way to tell them apart once a message is
	// converted, and ensureGeminiActiveLoopSignatures keys the active loop on
	// the last user content carrying text: an overlay counted there would push
	// the window past the turn's own function calls and disable the Gemini 3
	// thought-signature repair. Never serialized; the OpenAI-family wires carry
	// the same distinction on openAIMessage.Transient.
	Transient bool `json:"-"`
}

type geminiInlineData struct {
	MimeType    string `json:"mimeType"`
	DisplayName string `json:"displayName,omitempty"`
	Data        string `json:"data"`
}

type geminiFunctionCall struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args,omitzero"`
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
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	Parameters  toolSchema `json:"parameters,omitempty"`
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
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error,omitempty"`
	Candidates []struct {
		Content           geminiContent `json:"content"`
		FinishReason      string        `json:"finishReason"`
		ContinuationToken string        `json:"continuationToken,omitempty"`
	} `json:"candidates"`
	UsageMetadata *geminiUsageMetadata `json:"usageMetadata,omitzero"`
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
	compat := g.provider.GenerateContentCompat(model)
	if maxTokens <= 0 {
		maxTokens = DefaultOutputTokenMax
	}
	maxTokens = constrainGenerateContentOutputBudget(maxTokens, compat)
	dumpWriter := g.dumpWriter.Load()
	traceWriter := g.traceWriter.Load()
	// The trace collector only books diagnostics; without a trace writer it
	// would burn per-chunk work (event dedup, tool-input accounting) for a
	// record nobody persists, so the raw callback is wired straight through.
	var traceCollector *llmTraceCollector
	traceCB := cb
	if traceCB == nil {
		traceCB = func(message.StreamDelta) {}
	}
	if traceWriter != nil {
		traceCollector = newLLMTraceCollector("gemini", model, cb)
		traceCB = traceCollector.Callback
	}
	overrides := g.provider.RequestOverrides(model)
	// The marshaled body depends only on (system, messages, tools, maxTokens,
	// tuning) plus static provider config — not on the API key. Caching it per
	// target lets key rotation resend the identical bytes instead of
	// re-converting and re-marshaling the full history on every attempt.
	bodyIdentity := requestBodyIdentityFor(systemPrompt, messages, tools, maxTokens, strings.TrimSpace(tuning.SessionKey))
	bodyBytes, err := g.bodyReuse.body(bodyIdentity, func() ([]byte, error) {
		contents := convertMessagesToGemini(messages)
		if geminiRequiresThoughtSignature(g.provider, model) {
			ensureGeminiActiveLoopSignatures(contents)
		}
		// Tool config is only valid while tools are declared (Gemini rejects
		// toolConfig without function declarations).
		apiTools := convertToolsToGemini(tools)
		reqBody := geminiRequest{
			Contents: contents,
			Tools:    apiTools,
		}
		// Gemini rejects a request carrying both thinkingBudget and thinkingLevel,
		// so normalize first; the tool-config downgrade below reads the same
		// normalized shape the wire will carry.
		geminiThinking := normalizeGeminiThinking(tuning.Gemini)
		// suppress_in_thinking targets reject a forced call while thinking.
		// "none" is a deliberate prohibition rather than a force and ships
		// as-is; downgrading it would silently allow calls.
		choice := tuning.Gemini.ToolChoice
		if choice == "required" && forcedToolChoiceSuppressedInThinking(g.provider, model) && geminiThinkingActive(geminiThinking) {
			choice = ""
		}
		if len(apiTools) > 0 && choice != "" && !forcedToolChoiceDowngraded(g.provider, model, choice) {
			reqBody.ToolConfig = geminiToolConfigFromTuning(choice)
		}
		if systemPrompt != "" {
			reqBody.SystemInstruction = &geminiContent{Parts: []geminiPart{{Text: systemPrompt}}}
		}

		var genCfg geminiGenerationConfig
		if maxTokens > 0 {
			genCfg.MaxOutputTokens = maxTokens
		}
		if geminiThinking.ThinkingBudget != nil || geminiThinking.ThinkingLevel != "" || geminiThinking.IncludeThoughts != nil {
			// Gemini thinkingLevel values are documented as lowercase strings in the
			// public Gemini API docs (e.g. "minimal"|"low"|"medium"|"high"). Keep the
			// configured casing as-is.
			genCfg.ThinkingConfig = &geminiThinkingConfig{ThinkingBudget: geminiThinking.ThinkingBudget, ThinkingLevel: geminiThinking.ThinkingLevel, IncludeThoughts: geminiIncludeThoughts(geminiThinking)}
		}
		if genCfg.MaxOutputTokens > 0 || genCfg.ThinkingConfig != nil {
			reqBody.GenerationConfig = &genCfg
		}

		bodyBytes, err := jsonx.Marshal(reqBody)
		if err != nil {
			return nil, fmt.Errorf("marshal request body: %w", err)
		}
		bodyBytes, err = applyRequestBodyOverrides(bodyBytes, overrides)
		if err != nil {
			return nil, err
		}
		return bodyBytes, nil
	})
	if err != nil {
		return nil, err
	}
	if err := validateGeminiContinuationBody(bodyBytes, maxTokens, automaticGeminiContinuation(compat)); err != nil {
		return nil, err
	}
	recordToolChoiceRequest(ctx, bodyBytes)
	if automaticGeminiContinuation(compat) {
		return g.completeStreamWithContinuation(ctx, freezeGeminiRequest(g, model, apiKey, bodyBytes), cb)
	}
	var dumpRequestBody []byte
	if dumpWriter != nil {
		dumpRequestBody = append([]byte(nil), bodyBytes...)
	}

	streamCtx, streamCancel := context.WithCancel(ctx)
	defer streamCancel()
	req, err := http.NewRequestWithContext(streamCtx, http.MethodPost, geminiStreamURL(g.provider.APIURL(), model), bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set(headerContentType, headerValueApplicationJSON)
	req.Header.Set("x-goog-api-key", apiKey)
	setProviderLLMUserAgent(req.Header, g.provider, "")

	req, _ = g.bodyReuse.compressBody(bodyIdentity, req, bodyBytes, g.provider.RequestCompression())
	applyRequestHeaderOverrides(req.Header, overrides)

	var requestHeaders http.Header
	if dumpWriter != nil {
		requestHeaders = dumpRequestHeaders(req.Header)
	}

	log.Debugf("gemini request model=%v max_tokens=%v messages=%v tools=%v", model, maxTokens, len(messages), len(tools))

	start := time.Now()
	if g.proxyScheme != "" {
		log.Debugf("LLM request via proxy provider=%v scheme=%v", "gemini", g.proxyScheme)
	}
	traceCB(message.StreamDelta{Type: message.StreamDeltaStatus, Status: &message.StatusDelta{Type: message.StatusDeltaConnecting}})
	httpResp, err := doRequestUntilHeaders(g.client, req, providerResponseHeaderTimeout(g.provider))
	if err != nil {
		callErr := fmt.Errorf("send request: %w", err)
		persistLLMTrace(traceWriter, traceCollector, 0, "http", start, nil, callErr)
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
		if dumpWriter != nil {
			statusCode, headers := dumpHTTPResponseMetadata(httpResp)
			bodyCopy := string(append([]byte(nil), errBody...))
			go func() {
				dump := &LLMDump{Timestamp: start.Format(time.RFC3339Nano), Provider: "gemini", Model: model, RequestBody: dumpRequestBody, RequestHeaders: requestHeaders, HTTPStatus: statusCode, HTTPHeaders: headers, HTTPBody: bodyCopy, Error: apiErr.Error(), DurationMS: time.Since(start).Milliseconds()}
				if wErr := dumpWriter.Write(dump); wErr != nil {
					log.Warnf("failed to write LLM dump error=%v", wErr)
				}
			}()
		}
		persistLLMTrace(traceWriter, traceCollector, httpResp.StatusCode, "http", start, nil, apiErr)
		return nil, apiErr
	}

	var collector *SSECollector
	if dumpWriter != nil {
		collector = NewSSECollector()
	}
	cr := NewProviderChunkTimeoutReader(httpResp.Body, g.provider, DefaultChunkTimeout, streamCancel)
	defer cr.Stop()
	resp, parseErr := parseGeminiSSEStream(cr, traceCB, collector)

	if dumpWriter != nil {
		statusCode, headers := dumpHTTPResponseMetadata(httpResp)
		go func() {
			dump := &LLMDump{Timestamp: start.Format(time.RFC3339Nano), Provider: "gemini", Model: model, RequestBody: dumpRequestBody, RequestHeaders: requestHeaders, HTTPStatus: statusCode, HTTPHeaders: headers, SSEChunks: collector.Chunks(), Response: DumpResponseFromResponse(resp), DurationMS: time.Since(start).Milliseconds()}
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

func geminiStreamURL(apiURL, model string) string {
	apiURL = strings.TrimSpace(apiURL)
	model = strings.TrimLeft(model, "/")
	if parsed, err := url.Parse(apiURL); err == nil && parsed.Path != "" {
		parsed.Path = strings.TrimRight(parsed.Path, "/") + "/models/" + model + ":streamGenerateContent"
		parsed.RawPath = ""
		query := parsed.Query()
		query.Set("alt", "sse")
		parsed.RawQuery = query.Encode()
		parsed.Fragment = ""
		return parsed.String()
	}
	base := strings.TrimRight(apiURL, "/")
	separator := "?"
	if strings.Contains(base, "?") {
		separator = "&"
	}
	return base + "/models/" + model + ":streamGenerateContent" + separator + "alt=sse"
}

func convertMessagesToGemini(msgs []message.Message) []geminiContent {
	var result []geminiContent
	toolNamesByID := make(map[string]string)
	for i := 0; i < len(msgs); {
		msg := msgs[i]
		switch msg.Role {
		case "user":
			result = appendGeminiUserContent(result, geminiUserParts(msg))
			i++
		case "assistant":
			parts := geminiAssistantParts(msg, toolNamesByID)
			if len(parts) == 0 {
				log.Warn("skipping empty/reasoning-only assistant message in Gemini history")
				i++
				continue
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
				result = appendGeminiUserContent(result, parts)
			}
		default:
			log.Warnf("skipping message with unknown role role=%v", msg.Role)
			i++
		}
	}
	return result
}

func geminiAssistantParts(msg message.Message, toolNamesByID map[string]string) []geminiPart {
	toolCalls := make(map[string]message.ToolCall, len(msg.ToolCalls))
	for _, tc := range msg.ToolCalls {
		if tc.ID != "" {
			toolCalls[tc.ID] = tc
			toolNamesByID[tc.ID] = tc.Name
		}
	}
	parts := make([]geminiPart, 0, max(1, len(msg.GeminiParts)))
	usedToolCalls := make(map[string]bool, len(msg.ToolCalls))
	for _, replay := range msg.GeminiParts {
		switch replay.Type {
		case "text":
			parts = append(parts, geminiPart{Text: replay.Text, ThoughtSignature: replay.ThoughtSignature})
		case "thought":
			parts = append(parts, geminiPart{Text: replay.Text, Thought: true, ThoughtSignature: replay.ThoughtSignature})
		case "function_call":
			tc, ok := toolCalls[replay.ToolCallID]
			if !ok || tc.Name == "" {
				log.Warnf("skipping Gemini replay part with missing tool call id=%v", replay.ToolCallID)
				continue
			}
			parts = append(parts, geminiFunctionCallPart(tc, replay.ThoughtSignature))
			usedToolCalls[tc.ID] = true
		}
	}
	if len(msg.GeminiParts) == 0 {
		if contentText := assistantContentForReplay(msg); contentText != "" {
			parts = append(parts, geminiPart{Text: contentText})
		}
	}
	for _, tc := range msg.ToolCalls {
		if usedToolCalls[tc.ID] {
			continue
		}
		if tc.ID == "" || tc.Name == "" {
			log.Warnf("skipping Gemini functionCall with empty id or name in history tool=%v id=%v", tc.Name, tc.ID)
			continue
		}
		parts = append(parts, geminiFunctionCallPart(tc, tc.ThoughtSignature))
	}
	return parts
}

func geminiFunctionCallPart(tc message.ToolCall, signature string) geminiPart {
	args := sanitizeHistoryToolArgs("Gemini", tc.Name, tc.ID, tc.Args)
	return geminiPart{FunctionCall: &geminiFunctionCall{Name: tc.Name, Args: args}, ThoughtSignature: signature}
}

const geminiSkipThoughtSignatureValidator = "skip_thought_signature_validator"

func ensureGeminiActiveLoopSignatures(contents []geminiContent) {
	activeStart := 0
	for i, content := range contents {
		if content.Role != "user" {
			continue
		}
		for _, part := range content.Parts {
			// A request-scoped overlay rides the request tail as a user row so
			// the model sees it, but it is not a turn the user took: counting
			// its text as the boundary would start the active loop after the
			// current turn's function calls and leave them unrepaired.
			if part.Transient {
				continue
			}
			if part.Text != "" || part.InlineData != nil {
				activeStart = i
				break
			}
		}
	}
	for i := activeStart; i < len(contents); i++ {
		if contents[i].Role != "model" {
			continue
		}
		for j := range contents[i].Parts {
			part := &contents[i].Parts[j]
			if part.FunctionCall == nil {
				continue
			}
			if part.ThoughtSignature == "" {
				part.ThoughtSignature = geminiSkipThoughtSignatureValidator
			}
			break
		}
	}
}

// geminiRequiresThoughtSignature selects missing-signature repair on the native
// generateContent wire. Gemini 3 and 4 require the same signature contract;
// explicit none opts out, and the gemini-3 contract also enables aliases.
// Chat Completions routes select their wire shape through native_thinking.
func geminiRequiresThoughtSignature(provider *ProviderConfig, model string) bool {
	if provider == nil {
		return false
	}
	if compat := provider.ReasoningContinuityCompat(model); compat != nil {
		// Only gemini-3 selects the repair and only none opts out; any other
		// contract (a provider-wide deepseek, say) does not apply to this
		// endpoint and leaves the model-ID inference in charge.
		switch compat.ReasoningContractValue() {
		case config.ReasoningContractGemini3:
			return true
		case config.ReasoningContractNone:
			return false
		}
	}
	return config.RequiresGeminiThoughtSignature(model)
}

func appendGeminiUserContent(result []geminiContent, parts []geminiPart) []geminiContent {
	if len(result) == 0 || result[len(result)-1].Role != "user" {
		return append(result, geminiContent{Role: "user", Parts: parts})
	}
	result[len(result)-1].Parts = append(result[len(result)-1].Parts, parts...)
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
			data, mime, ok := binaryPartForWire(p)
			if !ok {
				continue
			}
			resp.Parts = append(resp.Parts, geminiPart{InlineData: &geminiInlineData{MimeType: mime, DisplayName: p.FileName, Data: mediacache.EncodeBase64(data)}})
		case "pdf":
			data, mime, ok := binaryPartForWire(p)
			if !ok {
				continue
			}
			resp.Parts = append(resp.Parts, geminiPart{InlineData: &geminiInlineData{MimeType: defaultPDFMediaType(mime), DisplayName: p.FileName, Data: mediacache.EncodeBase64(data)}})
		}
	}
	return resp
}

func geminiUserParts(msg message.Message) []geminiPart {
	if len(msg.Parts) == 0 {
		return markGeminiOverlayParts(msg, []geminiPart{{Text: wireText(msg.Content)}})
	}
	parts := make([]geminiPart, 0, len(msg.Parts))
	for _, p := range msg.Parts {
		switch p.Type {
		case "image":
			data, mime, ok := binaryPartForWire(p)
			if !ok {
				continue
			}
			parts = append(parts, geminiPart{InlineData: &geminiInlineData{MimeType: mime, Data: mediacache.EncodeBase64(data)}})
		case "pdf":
			data, mime, ok := binaryPartForWire(p)
			if !ok {
				continue
			}
			parts = append(parts, geminiPart{InlineData: &geminiInlineData{MimeType: defaultPDFMediaType(mime), Data: mediacache.EncodeBase64(data)}})
		default:
			// Fold adjacent pure-text parts into a single text part so a
			// text-only message takes one part. Insert a newline only when
			// neither side already provides one, keeping separately-authored
			// segments (git status, user input, pasted content, <file> refs)
			// from being glued together. image/pdf parts are never folded.
			if p.Text == "" {
				continue
			}
			if last := len(parts) - 1; last >= 0 && parts[last].InlineData == nil {
				parts[last].Text = joinAdjacentPartText(parts[last].Text, p.Text)
			} else {
				parts = append(parts, geminiPart{Text: p.Text})
			}
		}
	}
	if len(parts) == 0 {
		parts = []geminiPart{{Text: emptyWireTextPlaceholder}}
	}
	return markGeminiOverlayParts(msg, parts)
}

// markGeminiOverlayParts flags every part of a request-scoped overlay so the
// converted wire can still tell it apart from a turn the user took (see
// geminiPart.Transient). It runs on the finished part list because an overlay
// reaches this point through several shapes — plain content, folded text parts,
// or binary parts — and all of them are overlay-sourced. The flag has to
// survive the merge into a preceding user content that appendGeminiUserContent
// performs, which is where an overlay following tool results lands.
func markGeminiOverlayParts(msg message.Message, parts []geminiPart) []geminiPart {
	if msg.Kind != message.KindTurnOverlay {
		return parts
	}
	for i := range parts {
		parts[i].Transient = true
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

// geminiSchemaFields is the field set of the Gemini Schema proto message
// (google.ai.generativelanguage.v1beta.Schema), in the spellings proto-JSON
// accepts. Gemini parses every tool declaration as proto-JSON: a field the
// message does not have fails the whole request, not just the tool that
// carried it. Keywords outside this set are dropped rather than forwarded, so
// a new JSON Schema keyword coming from an MCP server cannot take every Gemini
// request down with it. Keep this in sync with content.proto's Schema message.
var geminiSchemaFields = map[string]bool{
	"type": true, "format": true, "title": true, "description": true,
	"nullable": true, "enum": true, "items": true,
	"maxItems": true, "minItems": true, "max_items": true, "min_items": true,
	"properties": true, "required": true,
	"minProperties": true, "maxProperties": true, "min_properties": true, "max_properties": true,
	"minimum": true, "maximum": true,
	"minLength": true, "maxLength": true, "min_length": true, "max_length": true,
	"pattern": true, "example": true,
	"anyOf": true, "any_of": true,
	"propertyOrdering": true, "property_ordering": true, "default": true,
}

// geminiSchemaTranslatedKeywords are input-only combinators. The Schema proto
// has no field for them, but the converter must see them: oneOf is lowered to
// anyOf and allOf is dropped explicitly instead of being mistaken for an
// unknown field.
var geminiSchemaTranslatedKeywords = map[string]bool{
	"oneOf": true, "one_of": true, "allOf": true, "all_of": true,
}

// geminiSilentlyDroppedKeywords are the keywords built-in tool schemas have
// always carried without a Gemini counterpart. They are dropped without a
// diagnostic so every request does not repeat the same debug line.
var geminiSilentlyDroppedKeywords = map[string]bool{
	"$schema": true, "additionalProperties": true,
	"coerceFromString": true, "coerceFromObject": true, "not": true,
}

// convertSchemaToGemini rewrites a JSON Schema into the Gemini Schema proto
// subset. Three invariants are enforced by the API rather than by proto-JSON
// parsing alone, and all three are checked by the wire tests:
//   - Every field must exist on the Schema message; an unknown field fails the
//     whole request. Hence the allowlist above.
//   - Schema.type is a single enum value and every anyOf branch is itself a
//     complete Schema. A branch carrying only "required" is valid JSON Schema
//     but not a valid Gemini Schema.
//   - "required" is validated against the SAME node's "properties": a name
//     with no matching property fails with "property is not defined", and
//     "required" outside an OBJECT fails with "only allowed for OBJECT type".
//
// Constraints we cannot express are dropped; built-in tools state them in
// their descriptions and enforce them at execution time.
func convertSchemaToGemini(schema map[string]any) map[string]any {
	if schema == nil {
		return nil
	}
	if len(schema) == 0 {
		// An empty schema is a legal "any value" JSON Schema, but Gemini's
		// Schema requires a type and the parameters root is always an object.
		// The guess is made only for schemas empty from the start: a branch
		// emptied later by pruning must stay empty so the combinator
		// conversion drops it instead of leaving a wildcard alternative.
		return map[string]any{"type": "OBJECT"}
	}
	out := make(map[string]any, len(schema))
	var oneOfBranches []map[string]any
	for k, v := range schema {
		if !geminiSchemaFields[k] && !geminiSchemaTranslatedKeywords[k] {
			if !geminiSilentlyDroppedKeywords[k] {
				log.Debugf("dropping JSON Schema keyword Gemini cannot represent keyword=%v", k)
			}
			continue
		}
		switch k {
		case "nullable", "default":
			// Both have Schema fields, but built-in tools have never sent them
			// and their constraints are enforced in descriptions / handlers.
			continue
		case "allOf", "all_of":
			// The proto has only any_of. Forwarding the keyword would be an
			// unknown field; merging allOf branches is not semantics-preserving.
			continue
		case "anyOf", "any_of":
			if converted, ok := convertSchemaListToGemini(v); ok && len(converted) > 0 {
				out["anyOf"] = append(geminiBranchList(out["anyOf"]), converted...)
			}
			continue
		case "oneOf", "one_of":
			// Lowered to anyOf so the alternatives survive. Collected separately
			// so branch order does not depend on Go map iteration order.
			if converted, ok := convertSchemaListToGemini(v); ok {
				oneOfBranches = append(oneOfBranches, converted...)
			}
			continue
		case "type":
			if normalized := normalizeGeminiSchemaType(v); normalized != "" {
				out[k] = normalized
			}
			continue
		case "enum":
			if normalized := normalizeGeminiSchemaEnum(v); len(normalized) > 0 {
				out[k] = normalized
			}
			continue
		case "properties":
			// A non-object "properties" (a boolean schema or an array) has no
			// proto-JSON representation either: drop the whole keyword.
			if props, ok := v.(map[string]any); ok {
				converted := make(map[string]any, len(props))
				for name, raw := range props {
					child, ok := raw.(map[string]any)
					if !ok {
						// Boolean schemas have no proto-JSON representation.
						log.Debugf("dropping non-schema property child=%v", name)
						continue
					}
					if normalized := convertSchemaToGemini(child); len(normalized) > 0 {
						converted[name] = normalized
					}
				}
				out[k] = converted
			}
			continue
		case "items":
			// The proto's items is a single Schema message: a tuple array or a
			// boolean schema has no representation. Drop the keyword instead of
			// handing proto-JSON a shape it cannot parse; the type inference
			// below still marks the node as an array.
			if child, ok := v.(map[string]any); ok {
				if normalized := convertSchemaToGemini(child); len(normalized) > 0 {
					out[k] = normalized
				}
			}
			continue
		}
		out[k] = v
	}
	if len(oneOfBranches) > 0 {
		out["anyOf"] = append(geminiBranchList(out["anyOf"]), oneOfBranches...)
	}
	pruneGeminiRequired(out)
	// A node with properties/items but no type fails strict backends. Infer only
	// these shapes: doing the same for a bare union would turn optional scalar
	// arguments into objects (googleapis/python-genai#3060). The inference reads
	// the source schema, so a keyword that was dropped still shapes the guess: a
	// tuple or boolean "items" marks an array, and non-object "properties" mark
	// an object. A node emptied by dropping or pruning stays type-less so the
	// combinator conversion discards it as a wildcard branch.
	_, sawProps := schema["properties"]
	_, sawItems := schema["items"]
	if _, ok := out["type"]; !ok {
		switch {
		case sawProps:
			out["type"] = "OBJECT"
		case sawItems:
			out["type"] = "ARRAY"
		}
	}
	if out["type"] == nil && len(geminiBranchList(out["anyOf"])) == 0 {
		// An annotation cannot make a pruned constraint into a valid Schema.
		return nil
	}
	return out
}

func geminiBranchList(value any) []map[string]any {
	if list, ok := value.([]map[string]any); ok {
		return list
	}
	return nil
}

// normalizeGeminiSchemaType accepts a single type or a type array (as emitted
// by Pydantic/Zod for nullable fields) and returns one proto enum value.
func normalizeGeminiSchemaType(value any) string {
	first := func(items []any) string {
		for _, item := range items {
			s, ok := item.(string)
			if !ok {
				continue
			}
			if t := strings.ToUpper(strings.TrimSpace(s)); t != "" && t != "NULL" {
				return t
			}
		}
		return ""
	}
	switch v := value.(type) {
	case string:
		return strings.ToUpper(strings.TrimSpace(v))
	case []string:
		items := make([]any, 0, len(v))
		for _, s := range v {
			items = append(items, s)
		}
		return first(items)
	case []any:
		return first(v)
	}
	return ""
}

// normalizeGeminiSchemaEnum converts enum values to strings: the proto field is
// repeated string even when the parent type is numeric or boolean.
func normalizeGeminiSchemaEnum(value any) []string {
	switch v := value.(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			switch e := item.(type) {
			case string:
				out = append(out, e)
			case bool:
				out = append(out, strconv.FormatBool(e))
			case float64:
				out = append(out, strconv.FormatFloat(e, 'f', -1, 64))
			case int:
				out = append(out, strconv.Itoa(e))
			case int64:
				out = append(out, strconv.FormatInt(e, 10))
			}
		}
		return out
	}
	return nil
}

// pruneGeminiRequired drops required names the same node does not define, and
// drops the keyword entirely when nothing survives. This is the shape MCP
// servers and combinator branches commonly emit; on Gemini it fails the entire
// request with "property is not defined". A branch emptied here is then removed
// by convertSchemaListToGemini.
func pruneGeminiRequired(schema map[string]any) {
	_, ok := schema["required"]
	if !ok {
		return
	}
	names := RequiredFields(schema)
	props, _ := schema["properties"].(map[string]any)
	kept := make([]string, 0, len(names))
	for _, name := range names {
		if _, ok := props[name]; ok {
			kept = append(kept, name)
		}
	}
	if len(kept) == 0 {
		delete(schema, "required")
		return
	}
	schema["required"] = kept
}

// convertSchemaListToGemini converts the subschemas of a combinator keyword.
// Tool definitions build these lists as []map[string]any while a schema decoded
// from JSON carries []any, and both reach this converter. A branch left empty
// by the dropped keywords is removed: it would constrain nothing while reading
// as a wildcard alternative that always matches.
func convertSchemaListToGemini(value any) ([]map[string]any, bool) {
	var raw []any
	switch list := value.(type) {
	case []map[string]any:
		raw = make([]any, 0, len(list))
		for _, child := range list {
			raw = append(raw, child)
		}
	case []any:
		raw = list
	default:
		return nil, false
	}
	converted := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		child, ok := item.(map[string]any)
		if !ok {
			return nil, false
		}
		if child := convertSchemaToGemini(child); len(child) > 0 {
			converted = append(converted, child)
		}
	}
	return converted, true
}

// geminiToolCallSeq makes synthesized Gemini tool-call IDs unique for the whole
// conversation, not just for one response. Gemini does not send IDs of its own,
// so Chord assigns them; a plain per-response counter handed out "gemini_0" to
// the first call of every turn, and consumers that key by tool-call ID (the
// reduction memos, the tool-call ID index) treat two calls sharing an ID as the
// same call. The seed keeps IDs distinct from those a previous process wrote
// into a session that is being resumed.
var geminiToolCallSeq = func() *atomic.Uint64 {
	var seq atomic.Uint64
	seq.Store(uint64(time.Now().UnixNano()))
	return &seq
}()

func parseGeminiSSEStream(reader io.Reader, cb StreamCallback, collector *SSECollector) (*message.Response, error) {
	decoder := newLLMSSEReader(reader, cb, sseMaxTokenSize, nil)
	streamToken := geminiToolCallSeq.Add(1)
	var resp message.Response
	var content strings.Builder
	var reasoning strings.Builder
	var textParts = newGeminiTextPartAccumulator(&resp)
	var inThinking bool
	var toolCalls = make(map[int]*openAIToolAccumulator)
	var nextToolIndex int
	var continuationToken string
	var sawDataLine bool
	var progressBytes int64
	var progressEvents int64
	var readErr error

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

	for {
		event, err := decoder.Next()
		if event.Data == nil {
			if err != io.EOF {
				readErr = err
			}
			break
		}
		sawDataLine = true
		data := bytes.TrimSpace(event.Data)
		if cb != nil {
			progressBytes += event.DataBytes
			progressEvents += event.DataLines
			cb(message.StreamDelta{Progress: &message.StreamProgressDelta{Bytes: progressBytes, Events: progressEvents}})
		}
		if collector != nil {
			collector.Add(redactGeminiChunk(data))
		}
		if len(data) == 0 {
			continue
		}

		chunk, err := decodeGeminiStreamChunk(data)
		if err != nil {
			return nil, err
		}
		if chunk.Error != nil {
			return nil, &APIError{
				Origin:  APIErrorOriginSSEEvent,
				Code:    strconv.Itoa(chunk.Error.Code),
				Type:    chunk.Error.Status,
				Message: redactGeminiErrorMessage(chunk.Error.Message, continuationToken),
			}
		}
		if cb != nil {
			cb(message.StreamDelta{Event: &message.StreamEventDelta{Type: "gemini.chunk"}})
		}

		for _, candidate := range chunk.Candidates {
			if candidate.ContinuationToken != "" {
				continuationToken = candidate.ContinuationToken
			}
			if candidate.FinishReason == geminiContinuationFinish {
				if cb != nil {
					cb(message.StreamDelta{Type: message.StreamDeltaRollback, Rollback: &message.RollbackDelta{Scope: message.RollbackScopeRequest}})
				}
				return nil, &GeminiContinuationError{Reason: "continuation_disabled"}
			}

		}
		for _, candidate := range chunk.Candidates {
			for _, part := range candidate.Content.Parts {
				if part.FunctionCall == nil {
					textParts.append(part)
				}
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
					id := fmt.Sprintf("gemini_%x_%d", streamToken, idx)
					name := part.FunctionCall.Name
					if name == "" {
						log.Warnf("discarding Gemini tool call with empty id or name tool=%v id=%v", name, id)
						continue
					}
					args := part.FunctionCall.Args
					if len(args) == 0 {
						args = json.RawMessage("{}")
					}
					acc := &openAIToolAccumulator{id: id, name: name, thoughtSignature: part.ThoughtSignature}
					acc.args.Write(args)
					toolCalls[idx] = acc
					textParts.Flush()
					resp.GeminiParts = append(resp.GeminiParts, message.GeminiReplayPart{Type: "function_call", ToolCallID: id, ThoughtSignature: acc.thoughtSignature})
					if cb != nil {
						cb(message.StreamDelta{Type: message.StreamDeltaToolUseStart, ToolCall: &message.ToolCallDelta{ID: id, Name: acc.name}})
						cb(message.StreamDelta{Type: message.StreamDeltaToolUseDelta, ToolCall: &message.ToolCallDelta{ID: id, Name: acc.name, Input: acc.args.String()}})
					}
				}
			}
			if candidate.FinishReason != "" {
				resp.StopReason = candidate.FinishReason
				// Truncation may remove replay parts and invalidate the open index.
				textParts.Flush()
				finalizeGeminiToolCalls(toolCalls, &resp, cb, candidate.FinishReason == "MAX_TOKENS")
			}
		}
		if usage := geminiChunkUsage(chunk); usage != nil {
			resp.Usage = usage.tokens()
		}
	}
	textParts.Flush()
	if err := readErr; err != nil {
		flushContent()
		if canRecoverPartialResponsesAfterError(err) {
			if partial := markInterruptedTextResponse(&resp); partial != nil {
				return partial, nil
			}
		}
		return nil, fmt.Errorf("reading Gemini SSE stream: %w", err)
	}
	if !sawDataLine {
		return nil, fmt.Errorf("empty SSE stream: no data lines")
	}
	finishThinking()
	flushContent()
	if resp.StopReason == "" {
		if partial := markInterruptedTextResponse(&resp); partial != nil {
			return partial, nil
		}
	}
	finalizeGeminiToolCalls(toolCalls, &resp, cb, false)
	if reasoning.Len() > 0 {
		resp.ReasoningContent = reasoning.String()
	}
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
		keptParts := resp.GeminiParts[:0]
		for _, part := range resp.GeminiParts {
			if part.Type != "function_call" {
				keptParts = append(keptParts, part)
			}
		}
		resp.GeminiParts = keptParts
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
		if acc.id == "" || acc.name == "" {
			log.Warnf("discarding Gemini tool call with empty id or name tool=%v id=%v", acc.name, acc.id)
			delete(toolCalls, idx)
			continue
		}
		args := json.RawMessage(acc.args.String())
		if len(args) == 0 {
			args = json.RawMessage("{}")
		}
		if !json.Valid(args) {
			log.Warnf("malformed Gemini tool call arguments tool=%v id=%v raw_args=%v", acc.name, acc.id, string(args))
			args = json.RawMessage(MalformedArgsSentinel)
		}
		resp.ToolCalls = append(resp.ToolCalls, message.ToolCall{ID: acc.id, Name: acc.name, Args: args, ThoughtSignature: acc.thoughtSignature})
		if cb != nil {
			// The arguments already streamed as a ToolUseDelta fragment when the
			// functionCall part arrived; the end delta carries identity only, so
			// the fragment contract holds (a second copy of the args would be
			// counted twice by consumers that sum fragment bytes).
			cb(message.StreamDelta{Type: message.StreamDeltaToolUseEnd, ToolCall: &message.ToolCallDelta{ID: acc.id, Name: acc.name}})
		}
		delete(toolCalls, idx)
	}
}

// geminiTextPartAccumulator merges consecutive unsigned streaming text parts
// into single GeminiReplayPart entries without re-copying the accumulated text
// on every delta. The open part's bytes live in a strings.Builder and are
// materialized into resp.GeminiParts only when the part closes, so per-delta
// cost stays linear in the new bytes. The open part is always the last element
// of resp.GeminiParts; call Flush before appending a function_call part,
// removing replay parts, and after the last text part.
type geminiTextPartAccumulator struct {
	resp     *message.Response
	open     int // index in resp.GeminiParts of the unsigned open part; -1 when closed
	partType string
	text     strings.Builder
}

func newGeminiTextPartAccumulator(resp *message.Response) *geminiTextPartAccumulator {
	return &geminiTextPartAccumulator{resp: resp, open: -1}
}

// append merges one streaming part. Semantics match the previous last-element
// merge: consecutive same-type parts merge while unsigned; a signature-bearing
// part merges its text into the open part and closes it; anything else starts
// a new part. Gemini signatures are positional and must remain on their
// original part boundary, so merging never crosses a signed part.
func (a *geminiTextPartAccumulator) append(part geminiPart) {
	if part.Text == "" && part.ThoughtSignature == "" {
		return
	}
	partType := "text"
	if part.Thought {
		partType = "thought"
	}
	if a.open >= 0 && a.partType == partType {
		a.text.WriteString(part.Text)
		if part.ThoughtSignature == "" {
			return
		}
		a.close(part.ThoughtSignature)
		return
	}
	a.Flush()
	a.open = len(a.resp.GeminiParts)
	a.partType = partType
	a.resp.GeminiParts = append(a.resp.GeminiParts, message.GeminiReplayPart{Type: partType})
	a.text.WriteString(part.Text)
	if part.ThoughtSignature != "" {
		a.close(part.ThoughtSignature)
	}
}

func (a *geminiTextPartAccumulator) close(signature string) {
	a.resp.GeminiParts[a.open].Text = a.text.String()
	a.resp.GeminiParts[a.open].ThoughtSignature = signature
	a.text.Reset()
	a.open = -1
}

// Flush materializes the open part, if any. Nothing reads resp.GeminiParts
// mid-parse, so deferring the text write until the part closes is safe.
func (a *geminiTextPartAccumulator) Flush() {
	if a.open < 0 {
		return
	}
	a.resp.GeminiParts[a.open].Text = a.text.String()
	a.text.Reset()
	a.open = -1
}

func parseGeminiHTTPErrorFromBytes(statusCode int, header http.Header, body []byte) *APIError {
	apiErr := &APIError{StatusCode: statusCode, Origin: APIErrorOriginHTTPResponse}
	apiErr.RetryAfter, _ = httpheader.ParseRetryAfter(header.Get("Retry-After"))
	var errResp geminiErrorResponse
	if err := json.Unmarshal(body, &errResp); err == nil && errResp.Error.Message != "" {
		apiErr.Message = errResp.Error.Message
		apiErr.Code = errResp.Error.Status
		apiErr.Type = errResp.Error.Status
		return apiErr
	}
	msg := texttruncate.Runes(string(body), 200, "...")
	apiErr.Message = msg
	return apiErr
}
