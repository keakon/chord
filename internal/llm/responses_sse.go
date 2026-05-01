package llm

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/keakon/golog/log"
	"io"
	"strconv"
	"strings"
	"time"

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

type responseCompleted struct {
	Response responsesCompletedPayload `json:"response"`
}

type responseIncomplete struct {
	Response responsesCompletedPayload `json:"response"`
}

// responsesToolAccumulator tracks an in-progress tool call during streaming.
type responsesToolAccumulator struct {
	id   string
	name string
	args strings.Builder
}

type responsesEventEnvelope struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
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
		dataChunkIndex int
		eventDataParts [][]byte
		progressBytes  int64
		progressEvents int64
	)
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
			finalizeResponsesToolCalls(toolCalls, &resp, cb, truncated)
			flushContent()
			outputItems = responsesFinalizeIncrementalOutputItems(outputItems, &resp)
			return &resp, outputItems, true, nil
		}
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			logResponsesSSETruncatedEvent(reader, dataChunkIndex, eventTypeHint, data, readErr)
			return nil, nil, false, fmt.Errorf("truncated SSE event %q: %w", eventTypeHint, readErr)
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

		state := responsesEventState{
			resp:           &resp,
			content:        &content,
			toolCalls:      toolCalls,
			finalizedCalls: finalizedCalls,
			truncated:      &truncated,
			outputItems:    &outputItems,
			cb:             cb,
			phaser:         phaser,
		}
		return processResponsesEventPayload(state, eventType, eventData, flushContent)
	}

	for {
		line, readErr := readSSELine(br)
		if readErr == nil || len(line) > 0 {
			if !gotData && cb != nil {
				cb(message.StreamDelta{Type: "status", Status: &message.StatusDelta{Type: "waiting_token"}})
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
			}
		}
		if readErr != nil {
			if len(eventDataParts) > 0 {
				outResp, outItems, done, err := flushEvent(readErr)
				if err != nil {
					return nil, nil, err
				}
				if done {
					return outResp, outItems, nil
				}
			}
			if errors.Is(readErr, io.EOF) {
				break
			}
			return nil, nil, fmt.Errorf("reading SSE stream: %w", readErr)
		}
	}

	if !sawDataLine {
		return nil, nil, fmt.Errorf("empty SSE stream: no data lines")
	}

	return nil, nil, fmt.Errorf("incomplete SSE stream: stream closed before response.completed")
}

type responsesEventState struct {
	resp           *message.Response
	content        *strings.Builder
	toolCalls      map[int]*responsesToolAccumulator
	finalizedCalls map[string]bool
	truncated      *bool
	outputItems    *[]responsesInputItem
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
		switch added.Item.Type {
		case "function_call":
			if state.phaser != nil {
				state.phaser.SetChunkTimeout(SlowPhaseChunkTimeout)
			}
			toolCallID := added.Item.CallID
			if toolCallID == "" {
				toolCallID = added.Item.ID
			}
			if state.finalizedCalls[toolCallID] {
				log.Debugf("responses: skip duplicate function_call (already finalized) tool=%v call_id=%v output_index=%v", added.Item.Name, toolCallID, addedIdx)
				return nil, nil, false, nil
			}
			state.toolCalls[addedIdx] = &responsesToolAccumulator{id: toolCallID, name: added.Item.Name}
			if state.cb != nil {
				state.cb(message.StreamDelta{Type: "tool_use_start", ToolCall: &message.ToolCallDelta{ID: toolCallID, Name: added.Item.Name}})
			}
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
			state.cb(message.StreamDelta{Type: "text", Text: delta.Delta})
		}
		state.content.WriteString(delta.Delta)
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
			if state.cb != nil && acc.args.Len() > 0 {
				argsStr := acc.args.String()
				if argsStr != "{}" {
					state.cb(message.StreamDelta{Type: "tool_use_delta", ToolCall: &message.ToolCallDelta{ID: acc.id, Name: acc.name, Input: argsStr}})
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
			state.cb(message.StreamDelta{Type: "thinking", Text: delta.Delta})
		}
		return nil, nil, false, nil

	case "response.reasoning_summary_text.done":
		var done responseReasoningSummaryTextDone
		if err := responsesSSEUnmarshal(eventData, &done); err != nil {
			log.Debugf("responses: skip unparseable reasoning_summary_text.done err=%v", err)
			return nil, nil, false, nil
		}
		if state.cb != nil {
			state.cb(message.StreamDelta{Type: "thinking_end"})
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
		if done.Item.Type == "function_call" {
			if state.phaser != nil {
				state.phaser.SetChunkTimeout(DefaultChunkTimeout)
			}
			doneIdx := done.OutputIndex
			if doneIdx == 0 {
				doneIdx = done.Index
			}
			finalizeOneResponsesToolCall(state.toolCalls, doneIdx, state.resp, state.cb, *state.truncated, done.Item.Arguments, state.finalizedCalls)
		}
		return nil, nil, false, nil

	case "response.completed":
		var completed responseCompleted
		if err := responsesSSEUnmarshal(eventData, &completed); err != nil {
			return nil, nil, false, fmt.Errorf("parse completed: %w", err)
		}
		respObj := completed.Response
		applyResponsesCompletionPayload(state.resp, respObj, state.truncated)
		*state.outputItems = responsesOutputToInputItems(respObj.Output)
		finalizeResponsesToolCalls(state.toolCalls, state.resp, state.cb, *state.truncated)
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
		finalizeResponsesToolCalls(state.toolCalls, state.resp, state.cb, *state.truncated)
		flushContent()
		*state.outputItems = responsesFinalizeIncrementalOutputItems(*state.outputItems, state.resp)
		return state.resp, *state.outputItems, true, nil
	}
	return nil, nil, false, nil
}

func responsesSSEUnmarshal(data []byte, v any) error {
	return sonicjson.ConfigStd.Unmarshal(data, v)
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
	log.Warnf("responses: failed to decode SSE payload attrs=%v", "<missing>")
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
	log.Warnf("responses: SSE event ended before delimiter attrs=%v", "<missing>")
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
