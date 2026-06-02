package llm

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/keakon/chord/internal/message"
)

const llmTraceFileName = "llm-trace.jsonl"

func LLMTraceFileName() string { return llmTraceFileName }

type LLMTraceRecord struct {
	Timestamp          string             `json:"timestamp"`
	Provider           string             `json:"provider"`
	Model              string             `json:"model"`
	Transport          string             `json:"transport,omitempty"`
	HTTPStatus         int                `json:"http_status,omitempty"`
	DurationMS         int64              `json:"duration_ms"`
	Error              string             `json:"error,omitempty"`
	StopReason         string             `json:"stop_reason,omitempty"`
	Statuses           []string           `json:"statuses,omitempty"`
	ProgressBytes      int64              `json:"progress_bytes,omitempty"`
	ProgressEvents     int64              `json:"progress_events,omitempty"`
	TextChunks         int                `json:"text_chunks,omitempty"`
	TextChars          int                `json:"text_chars,omitempty"`
	ThinkingChars      int                `json:"thinking_chars,omitempty"`
	FinalContentChars  int                `json:"final_content_chars,omitempty"`
	FinalThinkingChars int                `json:"final_thinking_chars,omitempty"`
	FinalToolCalls     int                `json:"final_tool_calls,omitempty"`
	ToolCalls          []LLMTraceToolCall `json:"tool_calls,omitempty"`
}

type LLMTraceToolCall struct {
	ID             string `json:"id,omitempty"`
	Name           string `json:"name,omitempty"`
	Started        bool   `json:"started,omitempty"`
	DeltaCount     int    `json:"delta_count,omitempty"`
	ArgsBytes      int    `json:"args_bytes,omitempty"`
	Ended          bool   `json:"ended,omitempty"`
	Finalized      bool   `json:"finalized,omitempty"`
	FinalJSONValid bool   `json:"final_json_valid,omitempty"`
}

type TraceWriter struct {
	mu   sync.Mutex
	path string
}

func NewTraceWriter(path string) *TraceWriter {
	return &TraceWriter{path: strings.TrimSpace(path)}
}

func (w *TraceWriter) SetPath(path string) {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.path = strings.TrimSpace(path)
	w.mu.Unlock()
}

func (w *TraceWriter) Path() string {
	if w == nil {
		return ""
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.path
}

func (w *TraceWriter) Write(rec *LLMTraceRecord) error {
	if w == nil || rec == nil {
		return nil
	}
	w.mu.Lock()
	path := w.path
	w.mu.Unlock()
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("marshal llm trace: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create llm trace dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open llm trace: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("append llm trace: %w", err)
	}
	return nil
}

type llmTraceCollector struct {
	cb           StreamCallback
	record       LLMTraceRecord
	statusesSeen map[string]bool
	tools        map[string]*toolTraceState
	toolOrder    []string
	nextAnonTool int
}

type toolTraceState struct {
	trace     LLMTraceToolCall
	lastInput string
}

func newLLMTraceCollector(provider, model string, cb StreamCallback) *llmTraceCollector {
	return &llmTraceCollector{
		cb: cb,
		record: LLMTraceRecord{
			Timestamp: time.Now().Format(time.RFC3339Nano),
			Provider:  strings.TrimSpace(provider),
			Model:     strings.TrimSpace(model),
		},
		statusesSeen: make(map[string]bool),
		tools:        make(map[string]*toolTraceState),
	}
}

func (c *llmTraceCollector) Callback(delta message.StreamDelta) {
	if c == nil {
		return
	}
	if delta.Status != nil {
		status := strings.TrimSpace(delta.Status.Type)
		if status != "" && !c.statusesSeen[status] {
			c.statusesSeen[status] = true
			c.record.Statuses = append(c.record.Statuses, status)
		}
	}
	if delta.Progress != nil {
		c.record.ProgressBytes = delta.Progress.Bytes
		c.record.ProgressEvents = delta.Progress.Events
	}
	switch delta.Type {
	case message.StreamDeltaText:
		c.record.TextChunks++
		c.record.TextChars += len(delta.Text)
	case message.StreamDeltaThinking:
		c.record.ThinkingChars += len(delta.Text)
	case message.StreamDeltaToolUseStart:
		state := c.toolState(delta.ToolCall)
		state.trace.Started = true
	case message.StreamDeltaToolUseDelta:
		state := c.toolState(delta.ToolCall)
		state.trace.Started = true
		state.trace.DeltaCount++
		c.addToolInputBytes(state, delta.ToolCall)
	case message.StreamDeltaToolUseEnd:
		state := c.toolState(delta.ToolCall)
		state.trace.Started = true
		state.trace.Ended = true
		c.addToolInputBytes(state, delta.ToolCall)
	}
	if c.cb != nil {
		c.cb(delta)
	}
}

func (c *llmTraceCollector) Finish(httpStatus int, transport string, resp *message.Response, err error) *LLMTraceRecord {
	if c == nil {
		return nil
	}
	c.record.HTTPStatus = httpStatus
	c.record.Transport = strings.TrimSpace(transport)
	if err != nil {
		c.record.Error = err.Error()
	}
	if resp != nil {
		c.record.StopReason = strings.TrimSpace(resp.StopReason)
		c.record.FinalContentChars = len(resp.Content)
		c.record.FinalThinkingChars = len(resp.ReasoningContent)
		if c.record.FinalThinkingChars == 0 && len(resp.ThinkingBlocks) > 0 {
			for _, tb := range resp.ThinkingBlocks {
				c.record.FinalThinkingChars += len(tb.Thinking)
			}
		}
		c.record.FinalToolCalls = len(resp.ToolCalls)
		for _, tc := range resp.ToolCalls {
			state := c.toolState(&message.ToolCallDelta{ID: tc.ID, Name: tc.Name})
			state.trace.Finalized = true
			state.trace.FinalJSONValid = json.Valid(tc.Args) && string(tc.Args) != MalformedArgsSentinel
			if state.trace.ArgsBytes == 0 {
				state.trace.ArgsBytes = len(tc.Args)
			}
		}
	}
	c.record.ToolCalls = make([]LLMTraceToolCall, 0, len(c.toolOrder))
	for _, key := range c.toolOrder {
		state := c.tools[key]
		if state == nil {
			continue
		}
		c.record.ToolCalls = append(c.record.ToolCalls, state.trace)
	}
	return &c.record
}

func (c *llmTraceCollector) toolState(tc *message.ToolCallDelta) *toolTraceState {
	key, id, name, consumeAnon := c.toolKey(tc)
	if state, ok := c.tools[key]; ok {
		if state.trace.ID == "" {
			state.trace.ID = id
		}
		if state.trace.Name == "" {
			state.trace.Name = name
		}
		return state
	}
	if consumeAnon {
		c.nextAnonTool++
	}
	state := &toolTraceState{trace: LLMTraceToolCall{ID: id, Name: name}}
	c.tools[key] = state
	c.toolOrder = append(c.toolOrder, key)
	return state
}

func (c *llmTraceCollector) toolKey(tc *message.ToolCallDelta) (key, id, name string, consumeAnon bool) {
	if tc != nil {
		id = strings.TrimSpace(tc.ID)
		name = strings.TrimSpace(tc.Name)
	}
	if id != "" {
		return "id:" + id, id, name, false
	}
	if name != "" {
		return fmt.Sprintf("name:%s#%d", name, c.nextAnonTool), id, name, true
	}
	return fmt.Sprintf("anon:%d", c.nextAnonTool), id, name, true
}

func (c *llmTraceCollector) addToolInputBytes(state *toolTraceState, tc *message.ToolCallDelta) {
	if state == nil || tc == nil {
		return
	}
	input := tc.Input
	if input == "" {
		return
	}
	if strings.HasPrefix(input, state.lastInput) {
		state.trace.ArgsBytes += len(input) - len(state.lastInput)
	} else {
		state.trace.ArgsBytes += len(input)
	}
	state.lastInput = input
}

func persistLLMTrace(writer *TraceWriter, collector *llmTraceCollector, httpStatus int, transport string, startedAt time.Time, resp *message.Response, err error) {
	if writer == nil || collector == nil {
		return
	}
	rec := collector.Finish(httpStatus, transport, resp, err)
	if rec == nil {
		return
	}
	rec.DurationMS = time.Since(startedAt).Milliseconds()
	_ = writer.Write(rec)
}

func SetProviderTraceWriter(p Provider, w *TraceWriter) {
	switch impl := p.(type) {
	case *OpenAIProvider:
		impl.SetTraceWriter(w)
	case *AnthropicProvider:
		impl.SetTraceWriter(w)
	case *GeminiProvider:
		impl.SetTraceWriter(w)
	case *ResponsesProvider:
		impl.SetTraceWriter(w)
	}
}
