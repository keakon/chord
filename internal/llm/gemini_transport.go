package llm

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	sonicjson "github.com/bytedance/sonic"
	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
)

type geminiFrozenRequest struct {
	overrides config.RequestOverridesConfig
	model     string
	url       string
	headers   http.Header
	body      []byte
}
type geminiHopResult struct {
	FinishReason  string
	Usage         *geminiUsageSnapshot
	UsageComplete bool
	Err           error
	Accepted      bool
}

func geminiBodyWithContinuation(base []byte, token string) ([]byte, error) {
	if token == "" {
		return base, nil
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(base, &body); err != nil {
		return nil, fmt.Errorf("decode Gemini request: %w", err)
	}
	body["continuationToken"], _ = json.Marshal(token)
	return json.Marshal(body)
}

func validateGeminiContinuationBody(body []byte, maxTokens int, enabled bool) error {
	var value map[string]json.RawMessage
	if err := json.Unmarshal(body, &value); err != nil {
		return fmt.Errorf("decode Gemini request: %w", err)
	}
	for _, key := range []string{"continuationToken", "continuation_token"} {
		if _, ok := value[key]; ok {
			return fmt.Errorf("gemini request overrides must not set %s", key)
		}
	}
	var generation struct {
		MaxOutputTokens int  `json:"maxOutputTokens"`
		CandidateCount  *int `json:"candidateCount"`
	}
	if raw, ok := value["generationConfig"]; ok {
		if err := json.Unmarshal(raw, &generation); err != nil {
			return fmt.Errorf("decode Gemini generation config: %w", err)
		}
	}
	if enabled && generation.CandidateCount != nil && *generation.CandidateCount != 1 {
		return fmt.Errorf("gemini continuation requires candidateCount=1")
	}
	if ((enabled || maxTokens > 0) && generation.MaxOutputTokens <= 0) || (maxTokens > 0 && generation.MaxOutputTokens > maxTokens) {
		return fmt.Errorf("gemini maxOutputTokens must be positive and within the effective output budget")
	}
	return nil
}

// redactGeminiChunk never persists the opaque continuation capability.
func redactGeminiChunk(data []byte) string {
	var value map[string]json.RawMessage
	if json.Unmarshal(data, &value) != nil {
		return "[invalid Gemini chunk]"
	}
	if _, ok := value["error"]; ok {
		return "[Gemini error event]"
	}
	var candidates []map[string]json.RawMessage
	if json.Unmarshal(value["candidates"], &candidates) == nil {
		for _, candidate := range candidates {
			delete(candidate, "continuationToken")
			delete(candidate, "continuation_token")
		}
		value["candidates"], _ = json.Marshal(candidates)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "[invalid Gemini chunk]"
	}
	return string(encoded)
}

func (g *GeminiProvider) streamGeminiHop(ctx context.Context, frozen geminiFrozenRequest, body []byte, state *geminiDecodeState, cb StreamCallback) (result geminiHopResult) {
	start := time.Now()
	requestToken := state.checkpoint.Token
	dumpWriter, traceWriter := g.dumpWriter.Load(), g.traceWriter.Load()
	var collector *SSECollector
	if dumpWriter != nil {
		collector = NewSSECollector()
	}
	var trace *llmTraceCollector
	if traceWriter != nil {
		trace = newLLMTraceCollector("gemini", frozen.model, cb)
	}
	originalCB := state.cb
	if trace != nil {
		state.cb = trace.Callback
		cb = trace.Callback
	}
	defer func() { state.cb = originalCB }()
	statusCode := 0
	var responseHeaders http.Header
	defer func() {
		if apiErr, ok := errors.AsType[*APIError](result.Err); ok && (result.Accepted || requestToken != "") {
			// Accepted streams may echo any earlier capability, including the
			// input token after a newer checkpoint replaced it. Keep only safe
			// classification fields; initial HTTP rejections retain their detail
			// for the ordinary key/context recovery path.
			result.Err = &APIError{StatusCode: apiErr.StatusCode, Origin: apiErr.Origin, RetryAfter: apiErr.RetryAfter, Message: "Gemini continuation request failed"}
		}
		response := &message.Response{Usage: result.Usage.tokens(), StopReason: result.FinishReason}
		persistLLMTrace(traceWriter, trace, statusCode, "http", start, response, result.Err)
		if dumpWriter != nil {
			safeBody, _ := geminiBodyWithContinuation(frozen.body, "[redacted]")
			if bytes.Equal(body, frozen.body) {
				safeBody = frozen.body
			}
			dump := &LLMDump{Timestamp: start.Format(time.RFC3339Nano), Provider: "gemini", Model: frozen.model, RequestBody: safeBody, RequestHeaders: dumpRequestHeaders(frozen.headers), HTTPStatus: statusCode, HTTPHeaders: responseHeaders, SSEChunks: collector.Chunks(), Response: DumpResponseFromResponse(response), DurationMS: time.Since(start).Milliseconds()}
			if result.Err != nil {
				dump.Error = result.Err.Error()
			}
			if err := dumpWriter.Write(dump); err != nil {
				log.Warnf("failed to write Gemini dump: %v", err)
			}
		}
	}()
	if cb != nil {
		cb(message.StreamDelta{Type: message.StreamDeltaStatus, Status: &message.StatusDelta{Type: message.StatusDeltaConnecting}})
	}
	hopCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(hopCtx, http.MethodPost, frozen.url, bytes.NewReader(body))
	if err != nil {
		result.Err = fmt.Errorf("create Gemini request: %w", err)
		return
	}
	req.Header = frozen.headers.Clone()
	req, _ = compressRequestBody(req, body, g.provider.RequestCompression())
	applyRequestHeaderOverrides(req.Header, frozen.overrides)
	response, err := doRequestUntilHeaders(g.client, req, providerResponseHeaderTimeout(g.provider))
	if err != nil {
		result.Err = err
		finishDiagnosticAttempt(ctx, nil, err, "")
		return
	}
	statusCode, responseHeaders = dumpHTTPResponseMetadata(response)
	defer response.Body.Close()
	defer func() {
		finishDiagnosticAttempt(ctx, &message.Response{Usage: result.Usage.tokens()}, result.Err, result.usageState())
	}()
	var reader io.Reader = response.Body
	if response.Header.Get(headerContentEncoding) == headerValueGzip {
		gr, gzipErr := gzip.NewReader(reader)
		if gzipErr != nil {
			result.Err = gzipErr
			return
		}
		defer gr.Close()
		reader = gr
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(reader, maxHTTPErrorBodyBytes))
		result.Err = parseGeminiHTTPErrorFromBytes(response.StatusCode, response.Header, raw)
		return
	}
	result.Accepted = true
	if cb != nil {
		cb(message.StreamDelta{Type: message.StreamDeltaStatus, Status: &message.StatusDelta{Type: message.StatusDeltaWaitingHeaders}})
	}
	cr := NewProviderChunkTimeoutReader(reader, g.provider, 20*time.Minute, cancel)
	defer cr.Stop()
	scanner := bufio.NewScanner(cr)
	scanner.Buffer(make([]byte, 0, sseInitialBufferSize), geminiContinuationSSEMaxLineBytes)
	waitingToken := false
	for scanner.Scan() {
		if !waitingToken && cb != nil {
			waitingToken = true
			cb(message.StreamDelta{Type: message.StreamDeltaStatus, Status: &message.StatusDelta{Type: message.StatusDeltaWaitingToken}})
		}
		line := scanner.Bytes()
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		data := bytes.TrimSpace(line[5:])
		if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
			continue
		}
		state.progressBytes += int64(len(line) + 1)
		state.progressEvents++
		if cb != nil {
			cb(message.StreamDelta{Progress: &message.StreamProgressDelta{Bytes: state.progressBytes, Events: state.progressEvents}})
		}
		chunk, err := decodeGeminiStreamChunk(data)
		if err != nil {
			result.Err = &GeminiContinuationError{Reason: "protocol_error"}
			break
		}
		if collector != nil {
			safe := redactGeminiChunk(data)
			if chunk.Error != nil {
				safe = "[Gemini error event]"
			}
			collector.Add(safe)
		}
		if err := state.Consume(chunk); err != nil {
			result.Err = err
			break
		}
		if state.finish != "" {
			cr.SetTerminalDrainTimeout(TerminalDrainChunkTimeout)
		}
	}
	result.FinishReason = state.finish
	if result.Err == nil && state.finish == "" {
		result.Err = scanner.Err()
		if errors.Is(result.Err, bufio.ErrTooLong) {
			result.Err = &GeminiContinuationError{Reason: "size_limit"}
		}
		if result.Err == nil {
			result.Err = io.ErrUnexpectedEOF
		}
	}
	if state.finish != "" && result.Err == nil && state.terminalUsage {
		result.Usage = mergeGeminiConfirmedUsage(state.latestUsage, state.hopCheckpointUsage)
		result.UsageComplete = state.latestUsage.complete() && !state.usageInvalid
	} else if state.hopCheckpoint {
		result.Usage = state.hopCheckpointUsage
		result.UsageComplete = result.Err != nil && state.hopCheckpointUsageComplete && !state.usageInvalid
	}
	return
}

func redactGeminiErrorMessage(value, token string) string {
	if token == "" {
		return value
	}
	return strings.ReplaceAll(value, token, "[redacted]")
}

// decodeGeminiStreamChunk centralizes decoding. Sonic's Unmarshal([]byte)
// copies the buffer internally, so decoded strings are owned regardless of the
// caller's buffer lifetime — journals retain chunk strings safely.
func decodeGeminiStreamChunk(data []byte) (geminiStreamChunk, error) {
	var chunk geminiStreamChunk
	if err := sonicjson.ConfigDefault.Unmarshal(data, &chunk); err != nil {
		return chunk, fmt.Errorf("invalid Gemini stream chunk")
	}
	return chunk, nil
}
