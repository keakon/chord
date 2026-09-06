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

// responseFunctionCallArgumentsDone is the payload of the
// response.function_call_arguments.done event. It is the provider's authoritative
// signal that a function call's arguments are fully streamed — the parser uses it
// to emit tool_use_end early (so the UI stops showing "receiving" and speculative
// execution can start) without finalizing the call; response.output_item.done
// remains the source of truth for the final arguments.
//
// Only the index is decoded. The event also carries the complete arguments, but
// binding them would copy the whole payload — tens of KB for a large patch —
// into a RawMessage nothing reads.
type responseFunctionCallArgumentsDone struct {
	Index       int `json:"index"`
	OutputIndex int `json:"output_index"`
}

// responseCustomToolCallInputDone is the payload of the
// response.custom_tool_call_input.done event, the custom-tool counterpart of
// function_call_arguments.done. Custom input events carry item_id only, and the
// complete input is deliberately left undecoded for the same reason.
type responseCustomToolCallInputDone struct {
	ItemID string `json:"item_id"`
}

// responseCustomToolCallInputDelta is the payload of the
// response.custom_tool_call_input.delta event. Custom tool input deltas carry
// item_id instead of output_index, so the streaming accumulator must locate the
// tool call by item id, unlike function_call arguments deltas.
type responseCustomToolCallInputDelta struct {
	ItemID string `json:"item_id"`
	Delta  string `json:"delta"`
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
	Type             string          `json:"type"`
	ID               string          `json:"id,omitempty"`
	CallID           string          `json:"call_id,omitempty"`
	Name             string          `json:"name,omitempty"`
	Arguments        json.RawMessage `json:"arguments,omitempty"`
	Input            string          `json:"input,omitempty"` // custom_tool_call freeform text
	EncryptedContent string          `json:"encrypted_content,omitempty"`
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
	Type      string `json:"type"`
	ID        string `json:"id"`
	CallID    string `json:"call_id"`
	Role      string `json:"role"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	// Input carries the freeform text of a custom_tool_call output item.
	Input   string                  `json:"input,omitempty"`
	Phase   string                  `json:"phase,omitempty"`
	Content []responsesContentBlock `json:"content,omitempty"`
	// Reasoning item fields (type == "reasoning").
	EncryptedContent string                             `json:"encrypted_content,omitempty"`
	Summary          []responsesReasoningSummaryPayload `json:"summary,omitempty"`
}

// responsesReasoningSummaryPayload is one summary block of a reasoning output item.
type responsesReasoningSummaryPayload struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type responsesUsagePayload struct {
	InputTokens        int `json:"input_tokens"`
	OutputTokens       int `json:"output_tokens"`
	InputTokensDetails *struct {
		CachedTokens     int `json:"cached_tokens"`
		CacheWriteTokens int `json:"cache_write_tokens"`
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

// xAI Responses uses reasoning_text events for the raw reasoning trace.
type responseReasoningTextDelta struct {
	Delta string `json:"delta"`
}

type responseReasoningTextDone struct {
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
	custom             bool // freeform custom_tool_call: args accumulate raw text
	args               strings.Builder
	streamStartEmitted bool
	endEmitted         bool // tool_use_end already sent (arguments.done early signal)
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

// emitResponsesToolArgsEnd emits the paired tool_use_end when a call's
// arguments or freeform input finished streaming (the authoritative per-call
// completion signals). The accumulator marks itself so the later
// response.output_item.done does not emit a second end.
func emitResponsesToolArgsEnd(acc *responsesToolAccumulator, cb StreamCallback) {
	if cb == nil || acc == nil {
		return
	}
	maybeEmitResponsesToolStart(acc, cb)
	if !acc.streamStartEmitted || acc.endEmitted {
		return
	}
	acc.endEmitted = true
	cb(message.StreamDelta{
		Type: message.StreamDeltaToolUseEnd,
		ToolCall: &message.ToolCallDelta{
			ID:   responsesToolStreamID(acc),
			Name: acc.name,
		},
	})
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
	resp, _, err := parseResponsesSSEWithOutputItemsAndTurnState(reader, cb, collector, nil, "", false)
	return resp, err
}

// parseResponsesSSEWithOutputItems behaves like parseResponsesSSE and also
// returns normalized output items from response.completed / response.incomplete.
// These items are used by the WebSocket incremental baseline chain.
func parseResponsesSSEWithOutputItems(reader io.Reader, cb StreamCallback, collector *SSECollector) (*message.Response, []responsesInputItem, error) {
	return parseResponsesSSEWithOutputItemsAndTurnState(reader, cb, collector, nil, "", false)
}

func parseResponsesSSEWithOutputItemsAndTurnState(reader io.Reader, cb StreamCallback, collector *SSECollector, turnState *ResponsesTurnState, turnStateID string, freeform bool) (*message.Response, []responsesInputItem, error) {
	phaser, _ := reader.(chunkPhaser)
	br := bufio.NewReaderSize(reader, sseInitialBufferSize)

	var (
		resp            message.Response
		content         strings.Builder
		toolCalls       = make(map[int]*responsesToolAccumulator) // index → accumulator
		customItemToIdx = make(map[string]int)                    // custom tool item_id → index
		finalizedCalls  = make(map[string]bool)                   // call_id → true; dedup against proxy replays
		truncated       bool
		gotData         bool
		sawDataLine     bool
		lastEventType   string // for standard SSE: event type from preceding "event:" line
		outputItems     []responsesInputItem
		partial         responsesPartialCompletionState
		dataChunkIndex  int
		eventDataParts  [][]byte
		progressBytes   int64
		progressEvents  int64
		providerErr     error
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
			if partialResp, partialItems, ok := finishPartialResponsesResponse(&resp, &outputItems, partial, false, freeform); ok {
				return partialResp, partialItems, true, nil
			}
			// Native remote compaction: the [DONE] frame after the streamed
			// compaction item ends the compact stream; the response carries
			// the collected summary already.
			if responsesOutputHasCompactionItem(resp.ResponsesOutput) {
				return &resp, outputItems, true, nil
			}
			outputItems = responsesFinalizeIncrementalOutputItems(outputItems, &resp, freeform)
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
			resp:              &resp,
			content:           &content,
			toolCalls:         toolCalls,
			customItemToIndex: customItemToIdx,
			finalizedCalls:    finalizedCalls,
			truncated:         &truncated,
			outputItems:       &outputItems,
			partial:           &partial,
			cb:                cb,
			phaser:            phaser,
			turnState:         turnState,
			turnStateID:       turnStateID,
			freeform:          freeform,
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
						if partialResp, partialItems, ok := finishPartialResponsesResponse(&resp, &outputItems, partial, true, freeform); ok {
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
				if partialResp, partialItems, ok := finishPartialResponsesResponse(&resp, &outputItems, partial, true, freeform); ok {
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
	if partialResp, partialItems, ok := finishPartialResponsesResponse(&resp, &outputItems, partial, false, freeform); ok {
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
	if _, ok := errors.AsType[*ChunkTimeoutError](err); ok {
		return responseHasText(resp) || (resp != nil && len(resp.ToolCalls) > 0)
	}
	return true
}

func finishPartialResponsesResponse(resp *message.Response, outputItems *[]responsesInputItem, partial responsesPartialCompletionState, requireCompleteOutput bool, freeform bool) (*message.Response, []responsesInputItem, bool) {
	if resp == nil {
		return nil, nil, false
	}
	hasToolCalls := len(resp.ToolCalls) > 0
	if requireCompleteOutput && !hasToolCalls && !partial.textDone {
		if partialResp := markInterruptedTextResponse(resp); partialResp != nil {
			items := responsesFinalizeIncrementalOutputItems(*outputItems, partialResp, freeform)
			*outputItems = items
			return partialResp, items, true
		}
	}
	if !partialResponsesRecoverable(hasToolCalls, partial, requireCompleteOutput) {
		return nil, nil, false
	}
	if resp.Content == "" && len(resp.ToolCalls) == 0 {
		return nil, nil, false
	}
	if resp.StopReason == "" {
		if hasToolCalls {
			resp.StopReason = "tool_calls"
		} else {
			resp.StopReason = "interrupted"
		}
	}
	items := responsesFinalizeIncrementalOutputItems(*outputItems, resp, freeform)
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
	resp              *message.Response
	content           *strings.Builder
	toolCalls         map[int]*responsesToolAccumulator
	customItemToIndex map[string]int // custom tool item_id → output index
	finalizedCalls    map[string]bool
	truncated         *bool
	outputItems       *[]responsesInputItem
	partial           *responsesPartialCompletionState
	cb                StreamCallback
	phaser            chunkPhaser
	turnState         *ResponsesTurnState
	turnStateID       string
	// freeform replays apply_patch output items in the custom_tool_call shape
	// (raw patch text) so incremental baselines match the main history replay.
	freeform bool
}

func processResponsesEventPayload(state responsesEventState, eventType string, eventData []byte, flushContent func()) (*message.Response, []responsesInputItem, bool, error) {
	switch eventType {
	case "response.metadata":
		captureResponsesTurnStateEvent(state.turnState, eventData, state.turnStateID)
		return nil, nil, false, nil

	case "response.output_item.added":
		if len(eventData) == 0 {
			log.Debug("responses: skip empty output_item.added")
			return nil, nil, false, nil
		}
		var added responseOutputItemAdded
		if err := responsesSSEUnmarshal(eventData, &added); err != nil {
			log.Debugf("responses: skip unparseable output_item.added err=%v", err)
			return nil, nil, false, nil
		}
		addedIdx := added.OutputIndex
		if addedIdx == 0 {
			addedIdx = added.Index
		}
		if state.partial != nil && added.Item.Type != "compaction" {
			state.partial.markOutputItemAdded(addedIdx)
		}
		// Compact request: collect the streamed compaction item so the
		// response.completed trailer (which carries usage) still ends the
		// stream normally.
		if added.Item.Type == "compaction" && state.resp != nil && added.Item.EncryptedContent != "" {
			state.resp.ResponsesOutput = append(state.resp.ResponsesOutput, message.ResponsesOutputItem{
				Type:             "compaction",
				ID:               added.Item.ID,
				EncryptedContent: added.Item.EncryptedContent,
			})
			state.resp.Content = added.Item.EncryptedContent
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
		case "custom_tool_call":
			if state.phaser != nil {
				state.phaser.SetChunkTimeout(SlowPhaseChunkTimeout)
			}
			toolCallID := responsesToolCallID(added.Item)
			if responsesToolCallAlreadyFinalized(state.finalizedCalls, added.Item) {
				log.Debugf("responses: skip duplicate custom_tool_call (already finalized) tool=%v call_id=%v output_index=%v", added.Item.Name, toolCallID, addedIdx)
				return nil, nil, false, nil
			}
			acc, exists := state.toolCalls[addedIdx]
			if !exists {
				// Deltas may have arrived before this added event, leaving a
				// synthetic accumulator under a negative index (see the
				// custom_tool_call_input.delta handler). Migrate it to the real
				// output index so the accumulated text is preserved.
				if state.customItemToIndex != nil {
					if syntheticIdx, ok := state.customItemToIndex[added.Item.ID]; ok && syntheticIdx < 0 {
						if synthetic, ok := state.toolCalls[syntheticIdx]; ok {
							acc = synthetic
							delete(state.toolCalls, syntheticIdx)
						}
					}
				}
				if acc == nil {
					acc = &responsesToolAccumulator{}
				}
				state.toolCalls[addedIdx] = acc
			}
			acc.mergeMetadata(added.Item)
			acc.custom = true
			if added.Item.ID != "" && state.customItemToIndex != nil {
				state.customItemToIndex[added.Item.ID] = addedIdx
			}
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

	case "response.function_call_arguments.done":
		// Authoritative per-call completion signal: the call's arguments are
		// fully streamed. Emit tool_use_end immediately so the UI leaves the
		// receiving state and speculative execution can start without waiting
		// for the rest of the response. The accumulator is kept: the final
		// arguments (and the call's validity) still come from
		// response.output_item.done, which also skips its own end emission
		// via endEmitted.
		var done responseFunctionCallArgumentsDone
		if err := responsesSSEUnmarshal(eventData, &done); err != nil {
			return nil, nil, false, fmt.Errorf("parse function_call_arguments.done: %w", err)
		}
		doneIdx := done.OutputIndex
		if doneIdx == 0 && done.Index != 0 {
			doneIdx = done.Index
		}
		if acc, exists := state.toolCalls[doneIdx]; exists {
			emitResponsesToolArgsEnd(acc, state.cb)
		}
		return nil, nil, false, nil

	case "response.custom_tool_call_input.delta":
		var delta responseCustomToolCallInputDelta
		if err := responsesSSEUnmarshal(eventData, &delta); err != nil {
			return nil, nil, false, fmt.Errorf("parse custom_tool_call_input.delta: %w", err)
		}
		if delta.Delta == "" {
			return nil, nil, false, nil
		}
		// Custom deltas carry item_id (no output_index), so locate the
		// accumulator by the mapping registered at output_item.added.
		idx, ok := -1, false
		if state.customItemToIndex != nil {
			idx, ok = state.customItemToIndex[delta.ItemID]
		}
		if !ok {
			// Delta arrived before the added event (defensive): allocate a
			// synthetic negative index that cannot collide with real output
			// indexes, create the accumulator so the text is not dropped, and
			// register the mapping so later deltas and the done event find the
			// same accumulator. The added event migrates it to the real index.
			idx = -1 - len(state.toolCalls)
			if state.customItemToIndex != nil && delta.ItemID != "" {
				state.customItemToIndex[delta.ItemID] = idx
			}
			if _, exists := state.toolCalls[idx]; !exists {
				state.toolCalls[idx] = &responsesToolAccumulator{}
			}
		}
		if acc, exists := state.toolCalls[idx]; exists {
			acc.args.WriteString(delta.Delta)
			if state.cb != nil && acc.streamStartEmitted && acc.args.Len() > 0 {
				// Keep the full input in the accumulator for finalization, but
				// emit only this fragment because the agent accumulates callbacks.
				state.cb(message.StreamDelta{Type: message.StreamDeltaToolUseDelta, ToolCall: &message.ToolCallDelta{ID: responsesToolStreamID(acc), Name: acc.name, InputText: delta.Delta}})
			}
		}
		return nil, nil, false, nil

	case "response.custom_tool_call_input.done":
		// Custom-tool counterpart of function_call_arguments.done: the input is
		// fully streamed, so emit tool_use_end early. Final arguments still come
		// from response.output_item.done.
		var done responseCustomToolCallInputDone
		if err := responsesSSEUnmarshal(eventData, &done); err != nil {
			return nil, nil, false, fmt.Errorf("parse custom_tool_call_input.done: %w", err)
		}
		if done.ItemID == "" {
			return nil, nil, false, nil
		}
		// Look the item up only when the added event (or a delta before it)
		// actually registered the id. Defaulting to a synthetic index here
		// would alias the delta-before-added accumulator, which belongs to a
		// different item. Finalization does not depend on this event:
		// response.output_item.done carries the complete input.
		idx, registered := 0, false
		if state.customItemToIndex != nil {
			idx, registered = state.customItemToIndex[done.ItemID]
		}
		if !registered {
			return nil, nil, false, nil
		}
		if acc, exists := state.toolCalls[idx]; exists {
			emitResponsesToolArgsEnd(acc, state.cb)
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

	case "response.reasoning_text.delta":
		var delta responseReasoningTextDelta
		if err := responsesSSEUnmarshal(eventData, &delta); err != nil {
			log.Debugf("responses: skip unparseable reasoning_text.delta err=%v", err)
			return nil, nil, false, nil
		}
		if delta.Delta != "" {
			state.resp.ReasoningContent += delta.Delta
			if state.cb != nil {
				state.cb(message.StreamDelta{Type: message.StreamDeltaThinking, Text: delta.Delta})
			}
		}
		return nil, nil, false, nil

	case "response.reasoning_text.done":
		var done responseReasoningTextDone
		if err := responsesSSEUnmarshal(eventData, &done); err != nil {
			log.Debugf("responses: skip unparseable reasoning_text.done err=%v", err)
			return nil, nil, false, nil
		}
		if state.resp.ReasoningContent == "" && done.Text != "" {
			state.resp.ReasoningContent = done.Text
		}
		if state.cb != nil {
			state.cb(message.StreamDelta{Type: message.StreamDeltaThinkingEnd})
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
		if done.Item.Type == "compaction" {
			// Native remote compaction returns exactly one "compaction" output
			// item whose encrypted_content is the replacement history. The v2
			// compact request collects it from resp.ResponsesOutput; ordinary
			// requests never see this type. The response.completed trailer
			// carries usage and ends the stream.
			if state.resp != nil && done.Item.EncryptedContent != "" && !responsesOutputHasCompactionItem(state.resp.ResponsesOutput) {
				state.resp.ResponsesOutput = append(state.resp.ResponsesOutput, message.ResponsesOutputItem{
					Type:             "compaction",
					ID:               done.Item.ID,
					EncryptedContent: done.Item.EncryptedContent,
				})
				state.resp.Content = done.Item.EncryptedContent
			}
			if state.cb != nil {
				state.cb(message.StreamDelta{Type: message.StreamDeltaStatus, Status: &message.StatusDelta{Type: "compacting"}})
			}
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
		case "custom_tool_call":
			if state.phaser != nil {
				state.phaser.SetChunkTimeout(DefaultChunkTimeout)
			}
			acc, exists := state.toolCalls[doneIdx]
			if !exists && state.customItemToIndex != nil {
				// Added event was missed: locate by item id.
				if altIdx, ok := state.customItemToIndex[done.Item.ID]; ok {
					doneIdx = altIdx
					acc, exists = state.toolCalls[doneIdx]
				}
			}
			if !exists {
				// The added event was missed entirely (truncated stream): the
				// done payload carries the complete input, so create the
				// accumulator from it instead of dropping the call.
				acc = &responsesToolAccumulator{}
				state.toolCalls[doneIdx] = acc
			}
			acc.mergeMetadata(done.Item)
			acc.custom = true
			maybeEmitResponsesToolStart(acc, state.cb)
			// done.Item.Input carries the complete freeform text; the
			// accumulator wraps it into the canonical {patch} object at
			// finalize (see canonicalApplyPatchArgs).
			finalizeOneResponsesToolCall(state.toolCalls, doneIdx, state.resp, state.cb, *state.truncated, json.RawMessage(done.Item.Input), state.finalizedCalls)
		case "message":
			if state.partial != nil {
				state.partial.textDone = true
			}
		case "reasoning":
			// A finalized reasoning item carries its encrypted_content and id.
			// Streaming does not fold reasoning into resp.ResponsesOutput (that
			// only happens at response.completed/incomplete via
			// collectResponsesOutput), so without capturing it here an
			// interrupted turn loses the reasoning entirely. Persisting the
			// message without its preceding reasoning item then violates the
			// Responses API pairing constraint on the next request (400).
			// Only an encrypted item is worth carrying. The completed path also
			// copies reasoning_text and summary, which this event does not even
			// parse, but those are human-readable annotations that replay
			// ignores — the interrupted message keeps its prose in Content. An
			// item with no encrypted payload would replay as a bare id
			// referencing state the target may never have stored, which is a
			// fresh 400 rather than the one this preserves against.
			if state.cb != nil && done.Item.ID != "" && done.Item.EncryptedContent != "" {
				item := message.ResponsesOutputItem{
					Type:             "reasoning",
					ID:               done.Item.ID,
					EncryptedContent: done.Item.EncryptedContent,
				}
				state.cb(message.StreamDelta{
					Type:          message.StreamDeltaReasoningItem,
					ReasoningItem: &item,
				})
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
		// Native remote compaction collects the streamed compaction output item
		// from the added/done events. applyResponsesCompletionPayload resets
		// ResponsesOutput from the completed payload, so preserve the streamed
		// compaction item and restore it afterwards. The compact callers then
		// see the collected summary plus the trailer's usage.
		var streamedCompaction []message.ResponsesOutputItem
		for _, item := range state.resp.ResponsesOutput {
			if item.Type == "compaction" {
				streamedCompaction = append(streamedCompaction, item)
			}
		}
		if len(streamedCompaction) > 0 {
			summary := streamedCompaction[0].EncryptedContent
			state.resp.ResponsesOutput = nil
			applyResponsesCompletionPayload(state.resp, respObj, state.truncated)
			state.resp.ResponsesOutput = append([]message.ResponsesOutputItem(nil), streamedCompaction...)
			if strings.TrimSpace(state.resp.Content) == "" {
				state.resp.Content = summary
			}
		} else {
			applyResponsesCompletionPayload(state.resp, respObj, state.truncated)
			collectResponsesOutput(state.resp, respObj.Output)
		}
		// applyResponsesCompletionPayload already stored the trailer's usage on
		// state.resp.Usage when the completed payload carries it; the compact
		// caller returns that usage alongside the collected summary.
		*state.outputItems = responsesOutputToInputItems(respObj.Output, state.freeform)
		finalizeResponsesToolCalls(state.toolCalls, state.resp, state.cb, *state.truncated, state.finalizedCalls)
		if state.resp.StopReason == "tool_calls" && len(state.resp.ToolCalls) == 0 {
			recoverResponsesToolCallsFromOutput(state.resp, respObj.Output, state.cb)
		}
		flushContent()
		*state.outputItems = responsesFinalizeIncrementalOutputItems(*state.outputItems, state.resp, state.freeform)
		return state.resp, *state.outputItems, true, nil

	case "response.incomplete":
		var incomplete responseIncomplete
		if err := responsesSSEUnmarshal(eventData, &incomplete); err != nil {
			return nil, nil, false, fmt.Errorf("parse incomplete: %w", err)
		}
		respObj := incomplete.Response
		applyResponsesCompletionPayload(state.resp, respObj, state.truncated)
		*state.outputItems = responsesOutputToInputItems(respObj.Output, state.freeform)
		if respObj.IncompleteDetails != nil {
			state.resp.StopReason = "length"
			*state.truncated = true
		}
		finalizeResponsesToolCalls(state.toolCalls, state.resp, state.cb, *state.truncated, state.finalizedCalls)
		flushContent()
		*state.outputItems = responsesFinalizeIncrementalOutputItems(*state.outputItems, state.resp, state.freeform)
		return state.resp, *state.outputItems, true, nil
	}
	return nil, nil, false, nil
}

// responsesOutputHasCompactionItem reports whether the collected output items
// already include a native remote compaction item (streamed via
// output_item.done). The completed payload then keeps the streamed item
// instead of re-applying the completed output list.
func responsesOutputHasCompactionItem(output []message.ResponsesOutputItem) bool {
	for _, item := range output {
		if item.Type == "compaction" {
			return true
		}
	}
	return false
}

// responsesSSEUnmarshal is a thin wrapper around the SSE payload decoder used
// by the Responses stream parser.
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
	param := strings.TrimSpace(errObj.Param)
	if param == "" {
		param = strings.TrimSpace(payload.Param)
	}
	if msg == "" {
		msg = strings.TrimSpace(string(eventData))
	}
	return &APIError{Origin: APIErrorOriginSSEEvent, Code: code, Type: typ, Param: param, Message: msg}, nil
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
		return parts[0]
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
