package llm

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	sonicjson "github.com/bytedance/sonic"

	"github.com/keakon/chord/internal/message"
)

type responsesCompactOutputItem struct {
	Type             string                  `json:"type"`
	Role             string                  `json:"role,omitempty"`
	EncryptedContent string                  `json:"encrypted_content,omitempty"`
	Content          []responsesContentBlock `json:"content,omitempty"`
}

type responsesCompactResponse struct {
	Output []responsesCompactOutputItem `json:"output"`
}

func resolveResponsesCompactURL(apiURL string) (string, error) {
	apiURL = strings.TrimSpace(apiURL)
	if apiURL == "" {
		return "", fmt.Errorf("empty responses API URL")
	}
	if strings.Contains(apiURL, "/responses/compact") {
		return apiURL, nil
	}
	if strings.Contains(apiURL, "/responses") {
		return strings.TrimRight(apiURL, "/") + "/compact", nil
	}
	return "", fmt.Errorf("responses compact requires /responses API URL")
}

func extractCompactSummary(resp responsesCompactResponse) string {
	for i := len(resp.Output) - 1; i >= 0; i-- {
		item := resp.Output[i]
		if item.Type == "compaction" && strings.TrimSpace(item.EncryptedContent) != "" {
			return strings.TrimSpace(item.EncryptedContent)
		}
	}
	for i := len(resp.Output) - 1; i >= 0; i-- {
		item := resp.Output[i]
		if item.Type != "message" {
			continue
		}
		var parts []string
		for _, block := range item.Content {
			if (block.Type == "output_text" || block.Type == "text") && strings.TrimSpace(block.Text) != "" {
				parts = append(parts, block.Text)
			}
		}
		if len(parts) > 0 {
			return strings.TrimSpace(strings.Join(parts, "\n"))
		}
	}
	return ""
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
	url, err := resolveResponsesCompactURL(r.provider.APIURL())
	if err != nil {
		return nil, err
	}
	ot := tuning.OpenAI
	apiInput := convertMessagesToResponses("", messages)
	if len(apiInput) == 0 {
		return nil, fmt.Errorf("responses compact requires at least one input item")
	}
	reqBody := responsesRequest{
		Model:             model,
		Input:             apiInput,
		Tools:             convertToolsToResponses(tools),
		ParallelToolCalls: cloneBoolPtr(ot.ParallelToolCalls),
	}
	if strings.TrimSpace(systemPrompt) != "" {
		reqBody.Instructions = &systemPrompt
	}
	if ot.ReasoningEffort != "" {
		reqBody.Reasoning = &reasoningConfig{Effort: ot.ReasoningEffort, Summary: ot.ReasoningSummary}
	}
	if ot.TextVerbosity != "" {
		reqBody.Text = &textConfig{Verbosity: ot.TextVerbosity}
	}
	if maxTokens > 0 {
		reqBody.MaxOutputTokens = maxTokens
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal compact request body: %w", err)
	}
	dumpRequestBody := append([]byte(nil), bodyBytes...)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create compact request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	applyOpenAIOAuthHeaders(req, r.provider, apiKey)

	// Apply request body compression if configured
	req, _ = compressRequestBody(req, bodyBytes, r.provider.CompressEnabled())

	start := time.Now()
	httpResp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send compact request: %w", err)
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

	if httpResp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(io.LimitReader(httpResp.Body, 4096))
		io.Copy(io.Discard, httpResp.Body) //nolint:errcheck
		apiErr := parseOpenAIHTTPErrorFromBytes(httpResp.StatusCode, httpResp.Header, errBody)
		if r.dumpWriter != nil {
			dumpWriter := r.dumpWriter
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
					slog.Warn("failed to write LLM dump", "error", wErr)
				}
			}()
		}
		return nil, apiErr
	}
	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("read compact response body: %w", err)
	}
	var compactResp responsesCompactResponse
	if err := sonicjson.Unmarshal(respBody, &compactResp); err != nil {
		return nil, fmt.Errorf("parse compact response: %w", err)
	}
	summary := extractCompactSummary(compactResp)
	if summary == "" {
		return nil, fmt.Errorf("compact response missing summary text")
	}
	if r.dumpWriter != nil {
		dumpWriter := r.dumpWriter
		go func() {
			dump := &LLMDump{
				Timestamp:   start.Format(time.RFC3339Nano),
				Provider:    "responses-compact",
				Model:       model,
				RequestBody: dumpRequestBody,
				Response: &DumpResponse{
					Content: summary,
				},
				DurationMS: time.Since(start).Milliseconds(),
			}
			if wErr := dumpWriter.Write(dump); wErr != nil {
				slog.Warn("failed to write LLM dump", "error", wErr)
			}
		}()
	}
	return &message.Response{Content: summary}, nil
}
