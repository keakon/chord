package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/keakon/golog/log"

	sonicjson "github.com/bytedance/sonic"

	"github.com/keakon/chord/internal/message"
)

// Event-specific types for Responses API streaming events.
// Official schema (platform.openai.com) uses output_index, content_index; index kept for backward compat.
type responseOutputItemAdded struct {
	Type        string              `json:"type"`
	Index       int                 `json:"index"`
	OutputIndex int                 `json:"output_index"`
	Item        responsesStreamItem `json:"item"`
	SequenceNum int                 `json:"sequence_number,omitempty"`
}

type responseOutputTextDelta struct {
	Index        int    `json:"index"`
	ContentIndex int    `json:"content_index"`
	OutputIndex  int    `json:"output_index"`
	Delta        string `json:"delta"`
}

type responseFunctionCallArgumentsDelta struct {
	Index       int    `json:"index"`
	OutputIndex int    `json:"output_index"`
	Delta       string `json:"delta"`
}

type responseOutputItemDone struct {
	Type        string              `json:"type"`
	Index       int                 `json:"index"`
	OutputIndex int                 `json:"output_index"`
	Item        responsesStreamItem `json:"item"`
}

// responsesStreamItem is a lightweight union used by SSE parsing so high-frequency
// item events avoid repeated json.Unmarshal into multiple temporary structs.
type responsesStreamItem struct {
	Type      string          `json:"type"`
	ID        string          `json:"id,omitempty"`
	CallID    string          `json:"call_id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// responsesCompletedPayload captures the subset of response.completed / response.incomplete
// payload we need during SSE parsing.
type responsesCompletedPayload struct {
	ID                string                 `json:"id"`
	Status            string                 `json:"status"`
	Output            []responsesOutputEntry `json:"output"`
	Usage             *responsesUsagePayload `json:"usage,omitempty"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details,omitempty"`
}

type responsesOutputEntry struct {
	Type      string                  `json:"type"`
	ID        string                  `json:"id"`
	CallID    string                  `json:"call_id"`
	Role      string                  `json:"role"`
	Name      string                  `json:"name"`
	Arguments string                  `json:"arguments"`
	Content   []responsesContentBlock `json:"content,omitempty"`
}

type responsesUsagePayload struct {
	InputTokens        int `json:"input_tokens"`
	OutputTokens       int `json:"output_tokens"`
	InputTokensDetails *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"input_tokens_details,omitempty"`
	OutputTokensDetails *struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"output_tokens_details,omitempty"`
}

// responseReasoningSummaryTextDelta is the payload for response.reasoning_summary_text.delta (Responses API).
type responseReasoningSummaryTextDelta struct {
	Delta string `json:"delta"`
}

// responseReasoningSummaryTextDone is the payload for response.reasoning_summary_text.done (Responses API).
type responseReasoningSummaryTextDone struct {
	Text string `json:"text"`
}

type responsesProviderErrorPayload struct {
	Type     string                 `json:"type"`
	Code     string                 `json:"code"`
	Message  string                 `json:"message"`
	Param    string                 `json:"param"`
	Error    responsesProviderError `json:"error"`
	Response struct {
		Status string                 `json:"status"`
		Error  responsesProviderError `json:"error"`
	} `json:"response"`
}

type responsesProviderError struct {
	Type    string `json:"type"`
	Code    string `json:"code"`
	Message string `json:"message"`
	Param   string `json:"param"`
}

type responseCompleted struct {
	Response responsesCompletedPayload `json:"response"`
}

type responseIncomplete struct {
	Response responsesCompletedPayload `json:"response"`
}

// responsesToolAccumulator tracks an in-progress tool call during streaming.
type responsesToolAccumulator struct {
	id                 string
	itemID             string
	streamID           string
	name               string
	args               strings.Builder
	streamStartEmitted bool
}

func (a *responsesToolAccumulator) mergeMetadata(item responsesStreamItem) {
	if a == nil {
		return
	}
	if item.ID != "" && a.itemID == "" {
		a.itemID = item.ID
	}
	if a.streamID == "" {
		if item.CallID != "" {
			a.streamID = item.CallID
		} else if item.ID != "" {
			a.streamID = item.ID
		}
	}
	if item.CallID != "" {
		a.id = item.CallID
	} else if a.id == "" && item.ID != "" {
		a.id = item.ID
	}
	if item.Name != "" {
		a.name = item.Name
	}
}

func responsesToolCallID(item responsesStreamItem) string {
	if item.CallID != "" {
		return item.CallID
	}
	return item.ID
}

func responsesToolCallAlreadyFinalized(finalizedCalls map[string]bool, item responsesStreamItem) bool {
	if finalizedCalls == nil {
		return false
	}
	if id := responsesToolCallID(item); id != "" && finalizedCalls[id] {
		return true
	}
	return item.ID != "" && finalizedCalls[item.ID]
}

func markResponsesToolCallFinalized(finalizedCalls map[string]bool, acc *responsesToolAccumulator) {
	if finalizedCalls == nil || acc == nil {
		return
	}
	if acc.id != "" {
		finalizedCalls[acc.id] = true
	}
	if acc.itemID != "" {
		finalizedCalls[acc.itemID] = true
	}
}

func responsesToolStreamID(acc *responsesToolAccumulator) string {
	if acc == nil {
		return ""
	}
	if acc.streamID != "" {
		return acc.streamID
	}
	return acc.id
}

func maybeEmitResponsesToolStart(acc *responsesToolAccumulator, cb StreamCallback) {
	if cb == nil || acc == nil || acc.streamStartEmitted || responsesToolStreamID(acc) == "" || acc.name == "" {
		return
	}
	cb(message.StreamDelta{
		Type: message.StreamDeltaToolUseStart,
		ToolCall: &message.ToolCallDelta{
			ID:   responsesToolStreamID(acc),
			Name: acc.name,
		},
	})
	acc.streamStartEmitted = true
}

type responsesEventEnvelope struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

var responsesTerminalSSEEvents = map[string]struct{}{
	"response.completed":  {},
	"response.incomplete": {},
	"response.failed":     {},
	"error":               {},
}

type responsesPartialCompletionState struct {
	textDone        bool
	openOutputItems map[int]struct{}
}

func (s *responsesPartialCompletionState) markOutputItemAdded(index int) {
	if s == nil {
		return
	}
	if s.openOutputItems == nil {
		s.openOutputItems = make(map[int]struct{})
	}
	s.openOutputItems[index] = struct{}{}
}

func (s *responsesPartialCompletionState) markOutputItemDone(index int) {
	if s == nil || s.openOutputItems == nil {
		return
	}
	delete(s.openOutputItems, index)
}

func (s responsesPartialCompletionState) outputItemsComplete() bool {
	return len(s.openOutputItems) == 0
}

// parseResponsesSSE reads a Responses API SSE stream and calls cb for each delta.
// Supports both combined format (data line has {"type":"...","data":...}) and
// standard SSE (event type on "event:" line, payload on "data:" line).
func parseResponsesSSE(reader io.Reader, cb StreamCallback, collector *SSECollector) (*message.Response, error) {
	resp, _, err := parseResponsesSSEWithOutputItems(reader, cb, collector)
	return resp, err
}

// parseResponsesSSEWithOutputItems behaves like parseResponsesSSE and also
// returns normalized output items from response.completed / response.incomplete.
// These items are used by the WebSocket incremental baseline chain.
func parseResponsesSSEWithOutputItems(reader io.Reader, cb StreamCallback, collector *SSECollector) (*message.Response, []responsesInputItem, error) {
	phaser, _ := reader.(chunkPhaser)
	br := bufio.NewReaderSize(reader, 64*1024)

	var (
		resp           message.Response
		content        strings.Builder
		toolCalls      = make(map[int]*responsesToolAccumulator) // index → accumulator
		finalizedCalls = make(map[string]bool)                   // call_id → true; dedup against proxy replays
		truncated      bool
		gotData        bool
		sawDataLine    bool
		lastEventType  string // for standard SSE: event type from preceding "event:" line
		outputItems    []responsesInputItem
		partial        responsesPartialCompletionState
		dataChunkIndex int
		eventDataParts [][]byte
		progressBytes  int64
		progressEvents int64
		providerErr    error
	)
	partial.openOutputItems = make(map[int]struct{})
	dataChunkIndex = -1
	flushContent := func() {
		if content.Len() == 0 {
			resp.Content = ""
			return
		}
		resp.Content = content.String()
	}

	flushEvent := func(readErr error) (*message.Response, []responsesInputItem, bool, error) {
		if len(eventDataParts) == 0 {
			lastEventType = ""
			return nil, nil, false, nil
		}
		data := joinSSEDataParts(eventDataParts)
		eventTypeHint := lastEventType
		eventDataParts = nil
		lastEventType = ""

		// [DONE] signals end of stream even if the trailing blank line is missing.
		if bytes.Equal(data, []byte("[DONE]")) {
			finalizeResponsesToolCalls(toolCalls, &resp, cb, truncated, finalizedCalls)
			flushContent()
			if resp.Content != "" {
				partial.textDone = true
			}
			if partialResp, partialItems, ok := finishPartialResponsesResponse(&resp, &outputItems, partial, false); ok {
				return partialResp, partialItems, true, nil
			}
			outputItems = responsesFinalizeIncrementalOutputItems(outputItems, &resp)
			return &resp, outputItems, true, nil
		}
		if len(data) == 0 {
			return nil, nil, false, nil
		}

		// Resolve event type and payload: combined {"type","data"} or use last event + raw data.
		eventType, eventData, err := parseResponsesEvent(data, eventTypeHint)
		if err != nil {
			if readErr != nil {
				logResponsesSSETruncatedEvent(reader, dataChunkIndex, eventTypeHint, data, readErr)
				return nil, nil, false, fmt.Errorf("truncated SSE event %q: %w", eventTypeHint, readErr)
			}
			logResponsesSSEDecodeFailure(reader, dataChunkIndex, "(parse_event)", data, err)
			return nil, nil, false, fmt.Errorf("parse event: %w", err)
		}
		if cb != nil && eventType != "" {
			cb(message.StreamDelta{Event: &message.StreamEventDelta{Type: eventType}})
		}

		state := responsesEventState{
			resp:           &resp,
			content:        &content,
			toolCalls:      toolCalls,
			finalizedCalls: finalizedCalls,
			truncated:      &truncated,
			outputItems:    &outputItems,
			partial:        &partial,
			cb:             cb,
			phaser:         phaser,
		}
		outResp, outItems, done, err := processResponsesEventPayload(state, eventType, eventData, flushContent)
		if err != nil {
			if readErr != nil && !errors.Is(readErr, io.EOF) {
				logResponsesSSETruncatedEvent(reader, dataChunkIndex, eventTypeHint, data, readErr)
				return nil, nil, false, fmt.Errorf("truncated SSE event %q: %w", eventTypeHint, readErr)
			}
			providerErr = err
		}
		return outResp, outItems, done, err
	}

	for {
		line, readErr := readSSELine(br)
		if readErr == nil || len(line) > 0 {
			if !gotData && cb != nil {
				cb(message.StreamDelta{Type: message.StreamDeltaStatus, Status: &message.StatusDelta{Type: "waiting_token"}})
				gotData = true
			}
			switch {
			case len(line) == 0:
				outResp, outItems, done, err := flushEvent(nil)
				if err != nil {
					return nil, nil, err
				}
				if done {
					return outResp, outItems, nil
				}
			case bytes.HasPrefix(line, []byte("event:")):
				lastEventType = string(bytes.TrimSpace(line[len("event:"):]))
			case bytes.HasPrefix(line, []byte("data:")):
				sawDataLine = true
				data := line[len("data:"):]
				if len(data) > 0 && data[0] == ' ' {
					data = data[1:]
				}
				if cb != nil {
					progressBytes += int64(len(line) + 1)
					progressEvents++
					cb(message.StreamDelta{Progress: &message.StreamProgressDelta{Bytes: progressBytes, Events: progressEvents}})
				}
				dataChunkIndex++
				eventDataParts = append(eventDataParts, append([]byte(nil), data...))
				if collector != nil {
					collector.Add(string(data))
				}
				if sseDataLineTerminatesEvent(data, lastEventType, responsesTerminalSSEEvents) {
					outResp, outItems, done, err := flushEvent(nil)
					if err != nil {
						return nil, nil, err
					}
					if done {
						return outResp, outItems, nil
					}
				}
			}
		}
		if readErr != nil {
			if len(eventDataParts) > 0 {
				outResp, outItems, done, err := flushEvent(readErr)
				if err != nil {
					flushContent()
					if canRecoverPartialResponsesAfterReadError(err, &resp) {
						if partialResp, partialItems, ok := finishPartialResponsesResponse(&resp, &outputItems, partial, true); ok {
							return partialResp, partialItems, nil
						}
					}
					return nil, nil, err
				}
				if done {
					return outResp, outItems, nil
				}
			}
			if errors.Is(readErr, io.EOF) {
				break
			}
			flushContent()
			if canRecoverPartialResponsesAfterReadError(readErr, &resp) {
				if partialResp, partialItems, ok := finishPartialResponsesResponse(&resp, &outputItems, partial, true); ok {
					return partialResp, partialItems, nil
				}
			}
			return nil, nil, fmt.Errorf("reading SSE stream: %w", readErr)
		}
	}

	if !sawDataLine {
		return nil, nil, fmt.Errorf("empty SSE stream: no data lines")
	}
	if providerErr != nil {
		return nil, nil, providerErr
	}
	flushContent()
	if partialResp, partialItems, ok := finishPartialResponsesResponse(&resp, &outputItems, partial, false); ok {
		return partialResp, partialItems, nil
	}

	return nil, nil, fmt.Errorf("incomplete SSE stream: stream closed before response.completed")
}

func canRecoverPartialResponsesAfterError(err error) bool {
	if err == nil {
		return true
	}
	if _, ok := errors.AsType[*APIError](err); ok {
		return false
	}
	return !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)
}

func canRecoverPartialResponsesAfterReadError(err error, resp *message.Response) bool {
	if !canRecoverPartialResponsesAfterError(err) {
		return false
	}
	if _, ok := errors.AsType[*ChunkTimeoutError](err); ok && (resp == nil || len(resp.ToolCalls) == 0) {
		return false
	}
	return true
}

func finishPartialResponsesResponse(resp *message.Response, outputItems *[]responsesInputItem, partial responsesPartialCompletionState, requireCompleteOutput bool) (*message.Response, []responsesInputItem, bool) {
	if resp == nil {
		return nil, nil, false
	}
	hasToolCalls := len(resp.ToolCalls) > 0
	if !partialResponsesRecoverable(hasToolCalls, partial, requireCompleteOutput) {
		return nil, nil, false
	}
	if resp.Content == "" && len(resp.ToolCalls) == 0 {
		return nil, nil, false
	}
	if resp.StopReason == "" {
		if hasToolCalls {
			resp.StopReason = "tool_calls"
		} else if partial.textDone {
			resp.StopReason = "stop"
		} else {
			resp.StopReason = "interrupted"
		}
	}
	items := responsesFinalizeIncrementalOutputItems(*outputItems, resp)
	*outputItems = items
	return resp, items, true
}

func partialResponsesRecoverable(hasToolCalls bool, partial responsesPartialCompletionState, requireCompleteOutput bool) bool {
	if requireCompleteOutput && !partial.outputItemsComplete() {
		return false
	}
	if requireCompleteOutput && !hasToolCalls && !partial.textDone {
		return false
	}
	return true
}

func finishPartialResponsesResponseWouldSucceed(resp *message.Response, partial responsesPartialCompletionState, requireCompleteOutput bool) bool {
	if resp == nil {
		return false
	}
	// Only tool-call partials may shorten the trailer wait. A Responses stream can
	// legitimately emit assistant text, pause, and then emit tool calls; treating
	// text-only output_item.done as terminal would truncate those later tools.
	return len(resp.ToolCalls) > 0 && partialResponsesRecoverable(true, partial, requireCompleteOutput)
}

type responsesEventState struct {
	resp           *message.Response
	content        *strings.Builder
	toolCalls      map[int]*responsesToolAccumulator
	finalizedCalls map[string]bool
	truncated      *bool
	outputItems    *[]responsesInputItem
	partial        *responsesPartialCompletionState
	cb             StreamCallback
	phaser         chunkPhaser
}

func processResponsesEventPayload(state responsesEventState, eventType string, eventData []byte, flushContent func()) (*message.Response, []responsesInputItem, bool, error) {
	switch eventType {
	case "response.output_item.added":
		if len(eventData) == 0 {
			log.Debug("responses: skip empty output_item.added")
			return nil, nil, false, nil
		}
		var added responseOutputItemAdded
		if err := responsesSSEUnmarshal(eventData, &added); err != nil {
			log.Debugf("responses: skip unparseable output_item.added err=%v data_len=%v", err, len(eventData))
			return nil, nil, false, nil
		}
		addedIdx := added.OutputIndex
		if addedIdx == 0 {
			addedIdx = added.Index
		}
		if state.partial != nil {
			state.partial.markOutputItemAdded(addedIdx)
		}
		switch added.Item.Type {
		case "function_call":
			if state.phaser != nil {
				state.phaser.SetChunkTimeout(SlowPhaseChunkTimeout)
			}
			toolCallID := responsesToolCallID(added.Item)
			if responsesToolCallAlreadyFinalized(state.finalizedCalls, added.Item) {
				log.Debugf("responses: skip duplicate function_call (already finalized) tool=%v call_id=%v output_index=%v", added.Item.Name, toolCallID, addedIdx)
				return nil, nil, false, nil
			}
			acc, exists := state.toolCalls[addedIdx]
			if !exists {
				acc = &responsesToolAccumulator{}
				state.toolCalls[addedIdx] = acc
			}
			acc.mergeMetadata(added.Item)
			maybeEmitResponsesToolStart(acc, state.cb)
		case "reasoning":
			if state.phaser != nil {
				state.phaser.SetChunkTimeout(SlowPhaseChunkTimeout)
			}
		}
		return nil, nil, false, nil

	case "response.output_text.delta":
		var delta responseOutputTextDelta
		if err := responsesSSEUnmarshal(eventData, &delta); err != nil {
			return nil, nil, false, fmt.Errorf("parse output_text.delta: %w", err)
		}
		if delta.Delta != "" && state.cb != nil {
			state.cb(message.StreamDelta{Type: message.StreamDeltaText, Text: delta.Delta})
		}
		state.content.WriteString(delta.Delta)
		return nil, nil, false, nil

	case "response.output_text.done":
		if state.partial != nil {
			state.partial.textDone = true
		}
		return nil, nil, false, nil

	case "response.function_call_arguments.delta":
		var delta responseFunctionCallArgumentsDelta
		if err := responsesSSEUnmarshal(eventData, &delta); err != nil {
			return nil, nil, false, fmt.Errorf("parse function_call_arguments.delta: %w", err)
		}
		deltaIdx := delta.OutputIndex
		if deltaIdx == 0 && delta.Index != 0 {
			deltaIdx = delta.Index
		}
		acc, exists := state.toolCalls[deltaIdx]
		if exists && delta.Delta != "" {
			acc.args.WriteString(delta.Delta)
			// Stream callbacks must remain paired: deltas are emitted only after
			// a start has been emitted for the same accumulator. Args still
			// accumulate so finalize can make the discard decision.
			if state.cb != nil && acc.streamStartEmitted && acc.args.Len() > 0 {
				argsStr := acc.args.String()
				if argsStr != "{}" {
					state.cb(message.StreamDelta{Type: message.StreamDeltaToolUseDelta, ToolCall: &message.ToolCallDelta{ID: responsesToolStreamID(acc), Name: acc.name, Input: argsStr}})
				}
			}
		}
		return nil, nil, false, nil

	case "response.reasoning_summary_text.delta":
		var delta responseReasoningSummaryTextDelta
		if err := responsesSSEUnmarshal(eventData, &delta); err != nil {
			log.Debugf("responses: skip unparseable reasoning_summary_text.delta err=%v", err)
			return nil, nil, false, nil
		}
		if delta.Delta != "" && state.cb != nil {
			state.cb(message.StreamDelta{Type: message.StreamDeltaThinking, Text: delta.Delta})
		}
		return nil, nil, false, nil

	case "response.reasoning_summary_text.done":
		var done responseReasoningSummaryTextDone
		if err := responsesSSEUnmarshal(eventData, &done); err != nil {
			log.Debugf("responses: skip unparseable reasoning_summary_text.done err=%v", err)
			return nil, nil, false, nil
		}
		if state.cb != nil {
			state.cb(message.StreamDelta{Type: message.StreamDeltaThinkingEnd})
		}
		if done.Text != "" {
			state.resp.ThinkingBlocks = append(state.resp.ThinkingBlocks, message.ThinkingBlock{Thinking: done.Text})
		}
		return nil, nil, false, nil

	case "response.output_item.done":
		if len(eventData) == 0 {
			return nil, nil, false, nil
		}
		var done responseOutputItemDone
		if err := responsesSSEUnmarshal(eventData, &done); err != nil {
			log.Debugf("responses: skip unparseable output_item.done err=%v", err)
			return nil, nil, false, nil
		}
		doneIdx := done.OutputIndex
		if doneIdx == 0 {
			doneIdx = done.Index
		}
		switch done.Item.Type {
		case "function_call":
			if state.phaser != nil {
				state.phaser.SetChunkTimeout(DefaultChunkTimeout)
			}
			if acc, exists := state.toolCalls[doneIdx]; exists {
				acc.mergeMetadata(done.Item)
				maybeEmitResponsesToolStart(acc, state.cb)
			}
			finalizeOneResponsesToolCall(state.toolCalls, doneIdx, state.resp, state.cb, *state.truncated, done.Item.Arguments, state.finalizedCalls)
		case "message":
			if state.partial != nil {
				state.partial.textDone = true
			}
		}
		// Mark the item done for every type that was opened by output_item.added
		// (reasoning, function_call, message, ...). markOutputItemAdded fires for
		// all types, so omitting any type here — notably "reasoning", which gpt-5
		// reasoning models always emit — leaves openOutputItems non-empty forever.
		// That stale entry makes outputItemsComplete() permanently false, which
		// (a) prevents the short terminal drain from arming after the last tool
		// call (the stream then waits a full DefaultChunkTimeout for the trailing
		// response.completed) and (b) blocks partial recovery when that trailing
		// event is truncated, discarding already-complete tool calls and forcing
		// a full retry. delete is idempotent, so re-marking function_call/message
		// here is harmless.
		if state.partial != nil {
			state.partial.markOutputItemDone(doneIdx)
		}
		if state.phaser != nil && state.partial != nil && finishPartialResponsesResponseWouldSucceed(state.resp, *state.partial, true) {
			state.phaser.SetTerminalDrainTimeout(TerminalDrainChunkTimeout)
		}
		return nil, nil, false, nil

	case "error", "response.failed":
		apiErr, err := parseResponsesProviderErrorEvent(eventType, eventData)
		if err != nil {
			return nil, nil, false, fmt.Errorf("parse %s: %w", eventType, err)
		}
		return nil, nil, false, apiErr

	case "response.completed":
		var completed responseCompleted
		if err := responsesSSEUnmarshal(eventData, &completed); err != nil {
			return nil, nil, false, fmt.Errorf("parse completed: %w", err)
		}
		respObj := completed.Response
		applyResponsesCompletionPayload(state.resp, respObj, state.truncated)
		*state.outputItems = responsesOutputToInputItems(respObj.Output)
		finalizeResponsesToolCalls(state.toolCalls, state.resp, state.cb, *state.truncated, state.finalizedCalls)
		if state.resp.StopReason == "tool_calls" && len(state.resp.ToolCalls) == 0 {
			recoverResponsesToolCallsFromOutput(state.resp, respObj.Output, state.cb)
		}
		flushContent()
		*state.outputItems = responsesFinalizeIncrementalOutputItems(*state.outputItems, state.resp)
		return state.resp, *state.outputItems, true, nil

	case "response.incomplete":
		var incomplete responseIncomplete
		if err := responsesSSEUnmarshal(eventData, &incomplete); err != nil {
			return nil, nil, false, fmt.Errorf("parse incomplete: %w", err)
		}
		respObj := incomplete.Response
		applyResponsesCompletionPayload(state.resp, respObj, state.truncated)
		*state.outputItems = responsesOutputToInputItems(respObj.Output)
		if respObj.IncompleteDetails != nil {
			state.resp.StopReason = "length"
			*state.truncated = true
		}
		finalizeResponsesToolCalls(state.toolCalls, state.resp, state.cb, *state.truncated, state.finalizedCalls)
		flushContent()
		*state.outputItems = responsesFinalizeIncrementalOutputItems(*state.outputItems, state.resp)
		return state.resp, *state.outputItems, true, nil
	}
	return nil, nil, false, nil
}

func responsesSSEUnmarshal(data []byte, v any) error {
	return sonicjson.ConfigDefault.Unmarshal(data, v)
}

func parseResponsesProviderErrorEvent(eventType string, eventData []byte) (*APIError, error) {
	var payload responsesProviderErrorPayload
	if err := responsesSSEUnmarshal(eventData, &payload); err != nil {
		return nil, fmt.Errorf("parse %s: %w", eventType, err)
	}
	errObj := payload.Error
	if eventType == "response.failed" {
		errObj = payload.Response.Error
	}
	code := strings.TrimSpace(errObj.Code)
	msg := strings.TrimSpace(errObj.Message)
	typ := strings.TrimSpace(errObj.Type)
	if code == "" {
		code = strings.TrimSpace(payload.Code)
	}
	if msg == "" {
		msg = strings.TrimSpace(payload.Message)
	}
	if typ == "" {
		typ = strings.TrimSpace(payload.Type)
	}
	if msg == "" {
		msg = strings.TrimSpace(string(eventData))
	}
	return &APIError{StatusCode: 400, Code: code, Type: typ, Message: msg}, nil
}

func readSSELine(r *bufio.Reader) ([]byte, error) {
	line, err := r.ReadBytes('\n')
	line = bytes.TrimSuffix(line, []byte("\n"))
	line = bytes.TrimSuffix(line, []byte("\r"))
	return line, err
}

func joinSSEDataParts(parts [][]byte) []byte {
	if len(parts) == 0 {
		return nil
	}
	if len(parts) == 1 {
		return append([]byte(nil), parts[0]...)
	}
	total := 0
	for _, part := range parts {
		total += len(part)
	}
	total += len(parts) - 1
	out := make([]byte, 0, total)
	for i, part := range parts {
		if i > 0 {
			out = append(out, '\n')
		}
		out = append(out, part...)
	}
	return out
}

// parseResponsesEvent parses an SSE event into type and data.
// Supports: (1) {"type":"event.name","data":{...}} — payload in "data";
// (2) {"type":"event.name","item":...,"output_index":0} — official format, payload siblings of "type" (no "data" key);
// (3) standard SSE: data line is payload only, type from preceding "event:" line.
func parseResponsesEvent(data []byte, eventTypeFromLine string) (eventType string, eventData json.RawMessage, err error) {
	if eventTypeFromLine != "" {
		return eventTypeFromLine, json.RawMessage(data), nil
	}
	if len(data) == 0 || data[0] != '{' {
		return eventTypeFromLine, json.RawMessage(data), nil
	}
	var raw responsesEventEnvelope
	if err := responsesSSEUnmarshal(data, &raw); err != nil {
		return "", nil, err
	}
	if raw.Type != "" {
		if len(raw.Data) == 0 {
			return raw.Type, json.RawMessage(data), nil
		}
		return raw.Type, raw.Data, nil
	}
	return eventTypeFromLine, json.RawMessage(data), nil
}

func logResponsesSSEDecodeFailure(reader io.Reader, chunkIndex int, eventType string, data []byte, err error) {
	attrs := []any{
		"event_type", eventType,
		"chunk_index", chunkIndex,
		"data_len", len(data),
		"data_tail", quoteASCIIBytesTail(data, 96),
		"data_tail_hex", hexBytesTail(data, 48),
		"error", err,
	}
	if diag, ok := reader.(chunkTimeoutDiagnostics); ok {
		snap := diag.chunkTimeoutSnapshot()
		attrs = append(attrs,
			"chunk_timeout", snap.Timeout,
			"timed_out", snap.TimedOut,
			"timeout_read_returned", snap.TimeoutReadReturned,
			"timeout_read_bytes", snap.TimeoutReadBytes,
			"last_read_bytes", snap.LastReadBytes,
			"last_read_err", snap.LastReadErr,
			"total_bytes", snap.TotalBytes,
		)
		if !snap.LastByteAt.IsZero() {
			attrs = append(attrs, "since_last_byte_ms", time.Since(snap.LastByteAt).Milliseconds())
		}
		if !snap.TimeoutFiredAt.IsZero() {
			attrs = append(attrs, "since_timeout_ms", time.Since(snap.TimeoutFiredAt).Milliseconds())
		}
	}
	log.Warnf("responses: failed to decode SSE payload %v", attrs)
}

func logResponsesSSETruncatedEvent(reader io.Reader, chunkIndex int, eventType string, data []byte, readErr error) {
	attrs := []any{
		"event_type", eventType,
		"chunk_index", chunkIndex,
		"data_len", len(data),
		"data_tail", quoteASCIIBytesTail(data, 96),
		"data_tail_hex", hexBytesTail(data, 48),
		"read_error", readErr,
	}
	if diag, ok := reader.(chunkTimeoutDiagnostics); ok {
		snap := diag.chunkTimeoutSnapshot()
		attrs = append(attrs,
			"chunk_timeout", snap.Timeout,
			"timed_out", snap.TimedOut,
			"timeout_read_returned", snap.TimeoutReadReturned,
			"timeout_read_bytes", snap.TimeoutReadBytes,
			"last_read_bytes", snap.LastReadBytes,
			"last_read_err", snap.LastReadErr,
			"total_bytes", snap.TotalBytes,
		)
		if !snap.LastByteAt.IsZero() {
			attrs = append(attrs, "since_last_byte_ms", time.Since(snap.LastByteAt).Milliseconds())
		}
		if !snap.TimeoutFiredAt.IsZero() {
			attrs = append(attrs, "since_timeout_ms", time.Since(snap.TimeoutFiredAt).Milliseconds())
		}
	}
	log.Warnf("responses: SSE event ended before delimiter %v", attrs)
}

func quoteASCIIBytesTail(data []byte, limit int) string {
	if limit > 0 && len(data) > limit {
		data = data[len(data)-limit:]
	}
	return strconv.QuoteToASCII(string(data))
}

func hexBytesTail(data []byte, limit int) string {
	if limit > 0 && len(data) > limit {
		data = data[len(data)-limit:]
	}
	return hex.EncodeToString(data)
}
