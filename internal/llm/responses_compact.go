package llm

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/message"
)

// responsesCompactV2Request builds the wire body for a native remote
// compaction request: the ordinary /responses payload plus a trailing
// compaction_trigger input item. The backend rewrites history and returns the
// compacted summary as a streamed "compaction" output item.
func responsesCompactV2Request(req responsesRequest) responsesRequest {
	req.Input = append(normalizeResponsesInput(req.Input), responsesInputItem{Type: "compaction_trigger"})
	req.Tools = normalizeResponsesTools(req.Tools)
	req.Stream = true
	// Compact requests do not carry the include list: the backend rewrites
	// history and returns only the compaction summary.
	req.omitInclude = true
	return req
}

// remoteCompactionV2URL returns the native remote-compaction endpoint URL for
// the given Responses API base. The Codex endpoint is the same /v1/responses
// URL with the remote_compaction_v2 beta feature header; the summary comes
// back as a "compaction" output item in the stream.
func remoteCompactionV2URL(apiURL string) (string, error) {
	apiURL = strings.TrimSpace(apiURL)
	if apiURL == "" {
		return "", fmt.Errorf("empty responses API URL")
	}
	return strings.TrimRight(apiURL, "/"), nil
}

func compactSummaryFromResponsesOutput(output []message.ResponsesOutputItem) (string, error) {
	var compactionCount int
	var summary string
	for _, item := range output {
		if item.Type != "compaction" {
			continue
		}
		compactionCount++
		if summary == "" {
			summary = strings.TrimSpace(item.EncryptedContent)
		}
	}
	if compactionCount == 0 {
		return "", fmt.Errorf("remote compaction v2 returned no compaction output item")
	}
	if compactionCount > 1 {
		return "", fmt.Errorf("remote compaction v2 returned %d compaction output items, want exactly one", compactionCount)
	}
	if summary == "" {
		return "", fmt.Errorf("remote compaction v2 compaction output item is empty")
	}
	return summary, nil
}

func (r *ResponsesProvider) Compact(
	ctx context.Context,
	apiKey string,
	model string,
	systemPrompt string,
	messages []message.Message,
	tools []message.ToolDefinition,
	maxTokens int,
	tuning RequestTuning,
) (*message.Response, error) {
	if r.provider == nil || !r.provider.IsCodexOAuthTransport() {
		return nil, fmt.Errorf("responses compact endpoint requires provider preset codex")
	}
	url, err := remoteCompactionV2URL(r.provider.APIURL())
	if err != nil {
		return nil, err
	}
	// Native remote compaction v2 rides the ordinary /responses streaming wire.
	ot := tuning.OpenAI
	apiInput := convertMessagesToResponses("", messages)
	if len(apiInput) == 0 {
		return nil, fmt.Errorf("responses compact requires at least one input item")
	}
	reqBody := responsesCompactV2Request(responsesRequest{
		Model:             model,
		Instructions:      nil,
		Input:             apiInput,
		Tools:             convertToolsToResponses(tools),
		ParallelToolCalls: false,
	})
	reqBody.Include = nil
	if strings.TrimSpace(systemPrompt) != "" {
		reqBody.Instructions = &systemPrompt
	}
	if ot.ParallelToolCalls != nil {
		reqBody.ParallelToolCalls = *ot.ParallelToolCalls
	}
	if ot.ServiceTier != "" {
		reqBody.ServiceTier = ot.ServiceTier
	}
	if r.sessionID != "" {
		reqBody.PromptCacheKey = r.sessionID
	}
	// Fingerprint convergence for cache locality: compact traffic must carry
	// the same client_metadata identity as the main Responses path (body) so
	// it does not surface as a different account/session upstream.
	if r.sessionID != "" {
		reqBody.ClientMetadata = responsesClientMetadata(r.sessionID, time.Now())
	}
	effectiveReasoningEffort, effectiveReasoningSummary := resolveResponsesReasoningFields(ot.EffectiveReasoningEffort(), ot.ReasoningSummary)
	if effectiveReasoningEffort != "" || effectiveReasoningSummary != "" {
		reqBody.Reasoning = &reasoningConfig{Effort: effectiveReasoningEffort, Summary: effectiveReasoningSummary}
	}
	if ot.TextVerbosity != "" {
		reqBody.Text = &textConfig{Verbosity: ot.TextVerbosity}
	}
	if maxTokens > 0 {
		log.Debugf("omitting max_output_tokens for Responses compact request requested=%v", maxTokens)
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal compact request body: %w", err)
	}
	dumpRequestBody := append([]byte(nil), bodyBytes...)
	dumpWriter := r.dumpWriter.Load()
	streamCtx, streamCancel := context.WithCancel(ctx)
	defer streamCancel()
	req, err := http.NewRequestWithContext(streamCtx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create compact request: %w", err)
	}
	req.Header.Set(headerContentType, headerValueApplicationJSON)
	applyOpenAIOAuthHeaders(req, r.provider, apiKey, true)
	// Keep the compact wire identity convergent with the main Responses path:
	// the same installation/session/thread/window metadata is echoed into the
	// headers (see sendAndParse) so compact traffic does not perturb the
	// session identity or cache locality.
	applyResponsesMetadataHeaders(req.Header, reqBody.ClientMetadata)
	turnState := ResponsesTurnStateFromContext(ctx)
	turnStateIdentity := responsesTurnStateIdentity(r.provider, apiKey)
	applyResponsesTurnStateHeader(req.Header, turnState, turnStateIdentity)

	// Apply request body compression if configured
	req, _ = compressRequestBody(req, bodyBytes, r.provider.CompressEnabled())
	// Native remote compaction v2 rides the ordinary /responses streaming wire;
	// advertise the session-level beta feature like the real Codex client.
	req.Header.Set(headerCodexBetaFeatures, headerValueRemoteCompactV2)

	start := time.Now()
	httpResp, err := doRequestUntilHeaders(r.client, req, providerResponseHeaderTimeout(r.provider))
	if err != nil {
		return nil, fmt.Errorf("send compact request: %w", err)
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

	if httpResp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(io.LimitReader(httpResp.Body, maxHTTPErrorBodyBytes))
		io.Copy(io.Discard, httpResp.Body) //nolint:errcheck
		apiErr := parseOpenAIHTTPErrorFromBytes(httpResp.StatusCode, httpResp.Header, errBody)
		if dumpWriter != nil {
			go func() {
				dump := &LLMDump{
					Timestamp:   start.Format(time.RFC3339Nano),
					Provider:    "responses-compact",
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
	captureResponsesTurnState(turnState, httpResp.Header, turnStateIdentity)

	cr := NewProviderChunkTimeoutReader(httpResp.Body, r.provider, DefaultChunkTimeout, streamCancel)
	defer cr.Stop()
	collector := NewSSECollector()
	resp, _, parseErr := parseResponsesSSEWithOutputItemsAndTurnState(cr, nil, collector, turnState, turnStateIdentity)
	if dumpWriter != nil {
		go func() {
			dump := &LLMDump{
				Timestamp:   start.Format(time.RFC3339Nano),
				Provider:    "responses-compact-v2",
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
	if parseErr != nil {
		return nil, fmt.Errorf("parse compact SSE stream: %w", parseErr)
	}
	if resp == nil {
		return nil, fmt.Errorf("compact SSE stream produced no response")
	}
	summary, err := compactSummaryFromResponsesOutput(resp.ResponsesOutput)
	if err != nil && resp.Content != "" {
		// The compaction summary may arrive through resp.Content when the
		// backend streams the item with content only on the done event;
		// fall back to it before failing.
		summary = strings.TrimSpace(resp.Content)
		err = nil
	}
	if err != nil {
		return nil, err
	}
	// The Codex backend emits the compaction output item, then the
	// response.completed trailer with usage and ends the stream; compact
	// callers consume the summary plus the trailer's usage.
	if resp.Usage != nil {
		log.Debugf("responses compact v2 usage input=%v output=%v cache_read=%v cache_write=%v", resp.Usage.InputTokens, resp.Usage.OutputTokens, resp.Usage.CacheReadTokens, resp.Usage.CacheWriteTokens)
	}
	return &message.Response{Content: summary, Usage: resp.Usage}, nil
}
