package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/keakon/golog/log"

	"github.com/gorilla/websocket"
	"golang.org/x/net/proxy"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/ratelimit"
)

const codexResponsesWebsocketsBeta = "responses_websockets=2026-02-06"

const codexWSIdleTimeout = 90 * time.Second

var errCodexWSInvalidEventJSON = errors.New("codex ws invalid event json")

// parseCodexWebSocketErrorJSON extracts an APIError (and optional Codex rate-limit headers) from a
// WebSocket text frame with type "error", matching ChatGPT/Codex Responses WebSocket payloads.
// Returns nil, nil when the body is not a structured API error.
func parseCodexWebSocketErrorJSON(msg []byte) (*APIError, http.Header) {
	var frame struct {
		Status int `json:"status"`
		Error  struct {
			Type            string `json:"type"`
			Message         string `json:"message"`
			ResetsInSeconds *int64 `json:"resets_in_seconds"`
		} `json:"error"`
		Headers map[string]json.RawMessage `json:"headers"`
	}
	if err := json.Unmarshal(msg, &frame); err != nil {
		return nil, nil
	}
	msgText := strings.TrimSpace(frame.Error.Message)
	if frame.Status == 0 {
		if msgText == "" {
			return nil, nil
		}
		frame.Status = http.StatusInternalServerError
	}
	apiErr := &APIError{
		StatusCode: frame.Status,
		Message:    msgText,
		Code:       frame.Error.Type,
	}
	if frame.Status == http.StatusTooManyRequests && frame.Error.ResetsInSeconds != nil && *frame.Error.ResetsInSeconds > 0 {
		apiErr.RetryAfter = durationFromPositiveSecondsClamped(*frame.Error.ResetsInSeconds, 0)
	}
	if len(frame.Headers) == 0 {
		return apiErr, nil
	}
	h := make(http.Header)
	for name, raw := range frame.Headers {
		canonical := http.CanonicalHeaderKey(name)
		var s string
		if json.Unmarshal(raw, &s) == nil {
			h.Set(canonical, s)
			continue
		}
		var n json.Number
		if json.Unmarshal(raw, &n) == nil {
			h.Set(canonical, n.String())
			continue
		}
		trim := strings.TrimSpace(string(raw))
		trim = strings.Trim(trim, `"`)
		if trim != "" {
			h.Set(canonical, trim)
		}
	}
	if len(h) == 0 {
		return apiErr, nil
	}
	return apiErr, h
}

// codexWSResponseCreate is the flat JSON envelope sent as the first WebSocket text frame
// (aligned with codex-rs / ChatGPT Codex Responses WebSocket).
type codexWSResponseCreate struct {
	Type               string               `json:"type"`
	Model              string               `json:"model"`
	Instructions       *string              `json:"instructions,omitempty"`
	Input              []responsesInputItem `json:"input"`
	Tools              []responsesTool      `json:"tools,omitempty"`
	ToolChoice         string               `json:"tool_choice,omitempty"`
	ParallelToolCalls  *bool                `json:"parallel_tool_calls,omitempty"`
	Store              bool                 `json:"store,omitempty"`
	Generate           *bool                `json:"generate,omitempty"`
	Stream             bool                 `json:"stream"`
	Include            []any                `json:"include,omitempty"`
	PromptCacheKey     string               `json:"prompt_cache_key,omitempty"`
	PreviousResponseID string               `json:"previous_response_id,omitempty"`
	MaxOutputTokens    int                  `json:"max_output_tokens,omitempty"`
	Reasoning          *reasoningConfig     `json:"reasoning,omitempty"`
	Text               *textConfig          `json:"text,omitempty"`
}

func responsesHTTPSBaseToWSS(httpsBase string) (string, error) {
	parsed, err := url.Parse(httpsBase)
	if err != nil {
		return "", fmt.Errorf("parse responses URL: %w", err)
	}
	switch strings.ToLower(parsed.Scheme) {
	case "https":
		parsed.Scheme = "wss"
	case "http":
		parsed.Scheme = "ws"
	default:
		return "", fmt.Errorf("expected http(s) responses URL, got scheme %q", parsed.Scheme)
	}
	return parsed.String(), nil
}

func newResponsesWebsocketDialer(proxyURL string) (*websocket.Dialer, error) {
	proxyURL = strings.TrimSpace(proxyURL)
	d := &websocket.Dialer{
		HandshakeTimeout: 45 * time.Second,
	}
	switch {
	case proxyURL == "":
		d.Proxy = http.ProxyFromEnvironment
		return d, nil
	case proxyURL == "direct":
		d.Proxy = nil
		return d, nil
	}
	u, err := url.Parse(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("parse proxy URL: %w", err)
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		d.Proxy = http.ProxyURL(u)
		return d, nil
	case "socks5":
		host := u.Hostname()
		if host == "" {
			return nil, fmt.Errorf("socks5 proxy: missing host")
		}
		port := u.Port()
		if port == "" {
			port = "1080"
		}
		addr := net.JoinHostPort(host, port)
		base := &net.Dialer{}
		socksDialer, err := proxy.SOCKS5("tcp", addr, nil, base)
		if err != nil {
			return nil, fmt.Errorf("socks5 dialer: %w", err)
		}
		if cd, ok := socksDialer.(proxy.ContextDialer); ok {
			d.NetDialContext = cd.DialContext
		} else {
			d.NetDial = socksDialer.Dial
		}
		return d, nil
	default:
		return nil, fmt.Errorf("unsupported proxy scheme for websocket: %q", u.Scheme)
	}
}

func applyCodexWebSocketHeaders(h http.Header, provider *ProviderConfig, apiKey, sessionID string) {
	h.Set("Authorization", "Bearer "+apiKey)
	h.Set("OpenAI-Beta", codexResponsesWebsocketsBeta)
	h.Set("User-Agent", openAICodexUserAgent())
	h.Set("originator", openAICodexOriginator)
	h.Set("session_id", sessionID)
	h.Set("x-client-request-id", sessionID)
	if provider != nil {
		if info := provider.oauthInfoForKey(apiKey); info != nil && info.AccountID != "" {
			h.Set("ChatGPT-Account-ID", info.AccountID)
		}
	}
}

func (r *ResponsesProvider) codexWSCloseConnUnlocked(reason string) bool {
	hadConn := r.codexWSConn != nil
	if hadConn {
		_ = r.codexWSConn.Close()
		r.codexWSConn = nil
		log.Debugf("responses codex ws: connection closed reason=%v", reason)
	}
	r.codexWSPromptCacheKey = ""
	return hadConn
}

// resetCodexWebSocketChain closes the WebSocket and clears in-connection incremental state.
// It does not clear codexWSStickyDisabled.
func (r *ResponsesProvider) resetCodexWebSocketChain(reason string) {
	r.codexWSMu.Lock()
	defer r.codexWSMu.Unlock()
	hadConn := r.codexWSCloseConnUnlocked(reason)
	hadPrevRespID := r.codexWSLastRespID != ""
	hadReqSig := r.codexWSLastReqSig != ""
	r.codexWSLastKey = ""
	r.codexWSLastModel = ""
	r.codexWSLastRespID = ""
	r.codexWSLastInpLen = 0
	r.codexWSLastInpSig = ""
	r.codexWSLastReqSig = ""
	if hadConn || hadPrevRespID || hadReqSig {
		log.Debugf("responses codex ws: chain reset reason=%v had_conn=%v had_previous_response_id=%v had_request_signature=%v", reason, hadConn, hadPrevRespID, hadReqSig)
	}
}

func isCodexWSProtocolStickyError(err error) bool {
	if err == nil {
		return false
	}
	var ce *websocket.CloseError
	if errors.As(err, &ce) && ce != nil {
		// gorilla's IsCloseError does not unwrap; classify the concrete close frame.
		return websocket.IsCloseError(ce,
			websocket.CloseProtocolError,
			websocket.CloseUnsupportedData,
			websocket.CloseInvalidFramePayloadData,
			websocket.ClosePolicyViolation,
			websocket.CloseMandatoryExtension,
			websocket.CloseMessageTooBig,
			websocket.CloseNoStatusReceived,
		)
	}
	return false
}

func codexWSParseFailureReason(err error) string {
	if err == nil {
		return "parse_failed"
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr != nil {
		if apiErr.StatusCode > 0 {
			return fmt.Sprintf("api_error_%d", apiErr.StatusCode)
		}
		return "api_error"
	}
	if isTimeoutLikeError(err) {
		return "stream_timeout"
	}
	if errors.Is(err, errCodexWSInvalidEventJSON) {
		return "invalid_event_json"
	}
	return "parse_failed"
}

// codexWSProgressTracker keeps request-scoped transport progress in the same
// cumulative shape used by SSE paths:
// - Bytes starts with waiting_headers baseline (non-zero approximation).
// - Each subsequent WS event message contributes one event and its frame bytes.
type codexWSProgressTracker struct {
	bytes  int64
	events int64
}

func newCodexWSProgressTracker(headerBytes int64) codexWSProgressTracker {
	if headerBytes < 0 {
		headerBytes = 0
	}
	return codexWSProgressTracker{bytes: headerBytes}
}

func (p *codexWSProgressTracker) addMessageFrame(msg []byte) message.StreamProgressDelta {
	if p == nil {
		return message.StreamProgressDelta{}
	}
	// Keep parity with SSE accounting that includes per-line separator bytes.
	p.bytes += int64(len(msg) + 1)
	p.events++
	return message.StreamProgressDelta{Bytes: p.bytes, Events: p.events}
}

func codexWSBuildBaseline(fullInput []responsesInputItem, outputItems []responsesInputItem) ([]responsesInputItem, int, string) {
	baseline := make([]responsesInputItem, 0, len(fullInput)+len(outputItems))
	baseline = append(baseline, fullInput...)
	baseline = append(baseline, outputItems...)
	return baseline, len(baseline), responsesInputSignature(baseline)
}

func (r *ResponsesProvider) codexWSCanUseIncrementalLocked(
	apiKey string,
	model string,
	reqSig string,
	fullInput []responsesInputItem,
	allowEmptyDelta bool,
) (bool, int, string) {
	if r.codexWSConn == nil {
		return false, 0, "no_connection"
	}
	if r.codexWSLastRespID == "" {
		return false, 0, "no_previous_response_id"
	}
	if r.codexWSLastReqSig == "" {
		return false, 0, "no_previous_request_signature"
	}
	if apiKey != r.codexWSLastKey || model != r.codexWSLastModel {
		return false, 0, "key_or_model_changed"
	}
	if reqSig == "" || reqSig != r.codexWSLastReqSig {
		return false, 0, "request_signature_changed"
	}
	n := r.codexWSLastInpLen
	if n < 0 || len(fullInput) < n {
		return false, 0, "input_shortened"
	}
	if !allowEmptyDelta && len(fullInput) == n {
		return false, 0, "empty_delta_not_allowed"
	}
	if n == 0 {
		return true, 0, "ok"
	}
	pref := responsesInputPrefixSignature(fullInput, n)
	if pref == "" || pref != r.codexWSLastInpSig {
		return false, 0, "input_prefix_mismatch"
	}
	return true, n, "ok"
}

func (r *ResponsesProvider) codexWSExecuteRequestLocked(
	ctx context.Context,
	apiKey string,
	model string,
	env codexWSResponseCreate,
	cb StreamCallback,
	collectDump bool,
	start time.Time,
	reusingConn bool,
) (*message.Response, []responsesInputItem, error) {
	payload, err := json.Marshal(env)
	if err != nil {
		return nil, nil, fmt.Errorf("codex ws marshal: %w", err)
	}

	streamCtx, streamCancel := context.WithCancel(ctx)
	defer streamCancel()

	// Only emit "connecting" when establishing a new WebSocket; on a reused
	// connection the write is immediate — skip the misleading state.
	// Note: for the completeStreamCodexWebSocket path, "connecting" is emitted
	// before dial, so reusingConn should always be true for the real-request
	// call to avoid a duplicate.
	if cb != nil && !reusingConn {
		cb(message.StreamDelta{Type: "status", Status: &message.StatusDelta{Type: "connecting"}})
	}

	if err := r.codexWSConn.SetWriteDeadline(time.Now().Add(60 * time.Second)); err != nil {
		r.codexWSCloseConnUnlocked("write_deadline")
		return nil, nil, fmt.Errorf("codex ws write deadline: %w", err)
	}
	if err := r.codexWSConn.WriteMessage(websocket.TextMessage, payload); err != nil {
		r.codexWSCloseConnUnlocked("write_failed")
		return nil, nil, fmt.Errorf("codex ws write: %w", err)
	}

	waitingHeaderBytes := responseHeaderBytes(&http.Response{Header: http.Header{"Upgrade": []string{"websocket"}}})
	if cb != nil {
		cb(message.StreamDelta{
			Type:   "status",
			Status: &message.StatusDelta{Type: "waiting_headers"},
			Progress: &message.StreamProgressDelta{
				Bytes: waitingHeaderBytes,
			},
		})
	}

	resp, outputItems, parseErr := r.codexWSReadResponseLocked(streamCtx, apiKey, cb, newCodexWSProgressTracker(waitingHeaderBytes))
	streamCancel()

	if collectDump && r.dumpWriter != nil {
		dumpWriter := r.dumpWriter
		go func() {
			dump := &LLMDump{
				Timestamp:   start.Format(time.RFC3339Nano),
				Provider:    "responses_ws",
				Model:       model,
				RequestBody: payload,
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
		r.codexWSCloseConnUnlocked(codexWSParseFailureReason(parseErr))
		return nil, nil, parseErr
	}
	return resp, outputItems, nil
}

func (r *ResponsesProvider) codexWSReadResponseLocked(
	streamCtx context.Context,
	apiKey string,
	cb StreamCallback,
	progress codexWSProgressTracker,
) (*message.Response, []responsesInputItem, error) {
	var (
		resp           message.Response
		content        strings.Builder
		toolCalls      = make(map[int]*responsesToolAccumulator)
		finalizedCalls = make(map[string]bool)
		truncated      bool
		outputItems    []responsesInputItem
		gotData        bool
	)
	flushContent := func() {
		if content.Len() == 0 {
			resp.Content = ""
			return
		}
		resp.Content = content.String()
	}
	for {
		select {
		case <-streamCtx.Done():
			if err := streamCtx.Err(); err != nil {
				return nil, nil, err
			}
			return nil, nil, context.Canceled
		default:
		}
		msg, err := r.codexWSReadMessageWithIdleTimeoutLocked(streamCtx)
		if err != nil {
			return nil, nil, fmt.Errorf("reading websocket stream: %w", err)
		}
		if cb != nil {
			snap := progress.addMessageFrame(msg)
			cb(message.StreamDelta{Progress: &snap})
		}
		if !gotData {
			if cb != nil {
				cb(message.StreamDelta{Type: "status", Status: &message.StatusDelta{Type: "waiting_token"}})
			}
			gotData = true
		}
		var hdr struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(msg, &hdr); err != nil {
			return nil, nil, fmt.Errorf("%w: %v", errCodexWSInvalidEventJSON, err)
		}
		if hdr.Type == "error" {
			if apiErr, errHdr := parseCodexWebSocketErrorJSON(msg); apiErr != nil {
				if len(errHdr) > 0 {
					if snap := ratelimit.ParseCodexHeaders(errHdr); snap != nil && r.provider != nil {
						snap.Provider = r.provider.Name()
						r.provider.UpdateKeySnapshot(apiKey, snap)
						if cb != nil {
							cb(message.StreamDelta{Type: "rate_limits", RateLimit: snap})
						}
					}
				}
				return nil, nil, apiErr
			}
			return nil, nil, fmt.Errorf("codex ws api error: %s", string(msg))
		}
		if hdr.Type == "codex.rate_limits" {
			if snap := ratelimit.ParseCodexRateLimitWebSocketEvent(msg); snap != nil && r.provider != nil {
				snap.Provider = r.provider.Name()
				r.provider.UpdateKeySnapshot(apiKey, snap)
				if cb != nil {
					cb(message.StreamDelta{Type: "rate_limits", RateLimit: snap})
				}
			}
			continue
		}
		eventType, eventData, err := parseResponsesEvent(msg, "")
		if err != nil {
			return nil, nil, fmt.Errorf("parse event: %w", err)
		}
		state := responsesEventState{
			resp:           &resp,
			content:        &content,
			toolCalls:      toolCalls,
			finalizedCalls: finalizedCalls,
			truncated:      &truncated,
			outputItems:    &outputItems,
			cb:             cb,
		}
		outResp, outItems, done, err := processResponsesEventPayload(state, eventType, eventData, flushContent)
		if err != nil {
			return nil, nil, err
		}
		if done {
			return outResp, outItems, nil
		}
	}
}

func (r *ResponsesProvider) codexWSReadMessageWithIdleTimeoutLocked(streamCtx context.Context) ([]byte, error) {
	type readResult struct {
		msg []byte
		err error
	}
	resultCh := make(chan readResult, 1)
	go func() {
		_, msg, err := r.codexWSConn.ReadMessage()
		resultCh <- readResult{msg: msg, err: err}
	}()

	timer := time.NewTimer(codexWSIdleTimeout)
	defer timer.Stop()

	select {
	case <-streamCtx.Done():
		return nil, streamCtx.Err()
	case <-timer.C:
		_ = r.codexWSConn.SetReadDeadline(time.Now())
		return nil, fmt.Errorf("idle timeout waiting for websocket message")
	case result := <-resultCh:
		return result.msg, result.err
	}
}

// completeStreamCodexWebSocket streams a turn over an existing or new Codex WebSocket.
// The caller must not hold codexWSMu.
func (r *ResponsesProvider) completeStreamCodexWebSocket(
	ctx context.Context,
	httpsURL string,
	apiKey string,
	model string,
	req *responsesRequest,
	fullInput []responsesInputItem,
	cb StreamCallback,
	start time.Time,
) (*message.Response, bool, error) {
	r.codexWSMu.Lock()
	defer r.codexWSMu.Unlock()

	wsURL, err := responsesHTTPSBaseToWSS(httpsURL)
	if err != nil {
		return nil, false, err
	}
	dialer, err := newResponsesWebsocketDialer(r.dialProxyURL)
	if err != nil {
		return nil, false, err
	}

	if r.codexWSConn != nil && (apiKey != r.codexWSLastKey || model != r.codexWSLastModel) {
		r.codexWSCloseConnUnlocked("key_or_model_changed")
		r.codexWSLastRespID = ""
		r.codexWSLastInpLen = 0
		r.codexWSLastInpSig = ""
		r.codexWSLastReqSig = ""
	}

	reqSig := responsesRequestSignature(req)

	newConnection := false
	if r.codexWSConn == nil {
		// Emit "connecting" before the potentially slow dial (DNS + TCP + TLS + WS
		// upgrade) so the TUI transitions from "Switching key" immediately, rather
		// than staying on that status for the entire dial + prewarm duration.
		// After this point the connection is considered established, so the
		// real-request call below passes reusingConn=true to avoid a duplicate.
		if cb != nil {
			cb(message.StreamDelta{Type: "status", Status: &message.StatusDelta{Type: "connecting"}})
		}
		sess := r.sessionID
		if sess == "" {
			sess = newOpenAIOAuthSessionID()
		}
		hdr := http.Header{}
		applyCodexWebSocketHeaders(hdr, r.provider, apiKey, sess)
		conn, httpResp, dialErr := dialer.DialContext(ctx, wsURL, hdr)
		if httpResp != nil && httpResp.Body != nil {
			defer httpResp.Body.Close()
		}
		if dialErr != nil {
			if httpResp != nil && httpResp.StatusCode >= 400 {
				errBody, _ := io.ReadAll(io.LimitReader(httpResp.Body, 4096))
				return nil, false, &APIError{
					StatusCode: httpResp.StatusCode,
					Message:    strings.TrimSpace(string(errBody)),
				}
			}
			return nil, false, fmt.Errorf("codex ws dial: %w", dialErr)
		}
		if r.provider != nil && httpResp != nil {
			if snap := ratelimit.ParseCodexHeaders(httpResp.Header); snap != nil {
				snap.Provider = r.provider.Name()
				r.provider.UpdateKeySnapshot(apiKey, snap)
				if cb != nil {
					cb(message.StreamDelta{Type: "rate_limits", RateLimit: snap})
				}
			}
		}
		r.codexWSConn = conn
		r.codexWSPromptCacheKey = sess
		newConnection = true
	}

	// Match Codex behavior: prewarm the first request on a fresh websocket so the
	// following real request can reuse previous_response_id with an empty delta.
	if newConnection {
		generate := false
		prewarmEnv := codexWSResponseCreate{
			Type:              "response.create",
			Model:             req.Model,
			Instructions:      req.Instructions,
			Input:             fullInput,
			Tools:             req.Tools,
			ToolChoice:        "auto",
			ParallelToolCalls: cloneBoolPtr(req.ParallelToolCalls),
			Generate:          &generate,
			Stream:            true,
			Include:           []any{},
			PromptCacheKey:    r.codexWSPromptCacheKey,
			MaxOutputTokens:   req.MaxOutputTokens,
			Reasoning:         req.Reasoning,
			Text:              req.Text,
		}
		prewarmResp, prewarmOutputItems, prewarmErr := r.codexWSExecuteRequestLocked(
			ctx, apiKey, model, prewarmEnv, nil, false, start, false,
		)
		if prewarmErr != nil {
			return nil, false, prewarmErr
		}
		_, baselineLen, baselineSig := codexWSBuildBaseline(fullInput, prewarmOutputItems)
		r.codexWSLastKey = apiKey
		r.codexWSLastModel = model
		r.codexWSLastReqSig = reqSig
		r.codexWSLastInpLen = baselineLen
		r.codexWSLastInpSig = baselineSig
		if prewarmResp != nil {
			r.codexWSLastRespID = prewarmResp.ProviderResponseID
		} else {
			r.codexWSLastRespID = ""
		}
	}

	useIncremental := false
	var wireInput []responsesInputItem
	var prevID string
	if ok, baselineLen, reason := r.codexWSCanUseIncrementalLocked(apiKey, model, reqSig, fullInput, true); ok {
		useIncremental = true
		wireInput = fullInput[baselineLen:]
		prevID = r.codexWSLastRespID
		log.Debugf("responses codex ws incremental decision result=%v reason=%v baseline_len=%v delta_len=%v model=%v", "hit", reason, baselineLen, len(wireInput), model)
	} else {
		wireInput = fullInput
		log.Debugf("responses codex ws incremental decision result=%v reason=%v input_len=%v model=%v", "miss", reason, len(fullInput), model)
	}

	env := codexWSResponseCreate{
		Type:               "response.create",
		Model:              req.Model,
		Instructions:       req.Instructions,
		Input:              wireInput,
		Tools:              req.Tools,
		ToolChoice:         "auto",
		ParallelToolCalls:  cloneBoolPtr(req.ParallelToolCalls),
		Stream:             true,
		Include:            []any{},
		PromptCacheKey:     r.codexWSPromptCacheKey,
		PreviousResponseID: prevID,
		MaxOutputTokens:    req.MaxOutputTokens,
		Reasoning:          req.Reasoning,
		Text:               req.Text,
	}
	// reusingConn is always true here: if we just dialed+prewarmed, the WS
	// connection is already established; if we reused an existing connection,
	// it was already live. Either way, "connecting" has already been emitted
	// (either above for new connections, or was emitted on the original dial).
	resp, outputItems, reqErr := r.codexWSExecuteRequestLocked(
		ctx, apiKey, model, env, cb, true, start, true,
	)
	if reqErr != nil {
		return nil, useIncremental, reqErr
	}

	_, baselineLen, baselineSig := codexWSBuildBaseline(fullInput, outputItems)
	r.codexWSLastKey = apiKey
	r.codexWSLastModel = model
	r.codexWSLastReqSig = reqSig
	r.codexWSLastInpLen = baselineLen
	r.codexWSLastInpSig = baselineSig
	if resp != nil && resp.ProviderResponseID != "" {
		r.codexWSLastRespID = resp.ProviderResponseID
	} else {
		r.codexWSLastRespID = ""
		log.Debugf("responses: codex ws completed without response id; incremental chain not advanced model=%v", model)
	}
	log.Debugf("responses codex ws baseline updated model=%v baseline_len=%v output_items=%v request_signature_set=%v", model, baselineLen, len(outputItems), r.codexWSLastReqSig != "")

	return resp, useIncremental, nil
}
