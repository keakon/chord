package llm

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/keakon/chord/internal/message"
)

const geminiContinuationFinish = "CONTINUATION"
const geminiContinuationMaxBufferedBytes = 64 << 20
const geminiContinuationSSEMaxLineBytes = 16 << 20

// GeminiContinuationError marks a generation that must not be rebuilt by key rotation.
type GeminiContinuationError struct {
	Reason string
	Cause  error
}

func (e *GeminiContinuationError) Error() string { return "Gemini continuation failed: " + e.Reason }
func (e *GeminiContinuationError) Unwrap() error { return e.Cause }

type geminiCheckpoint struct {
	Token                            string
	PartCursor, PreviewCursor, Bytes int
	Thinking                         bool
	Usage                            *geminiUsageSnapshot
	TokenDigest                      [32]byte
}

type geminiDecodeState struct {
	progressBytes, progressEvents int64
	usageInvalid                  bool
	parts                         []geminiPart
	preview                       []message.StreamDelta
	checkpoint                    geminiCheckpoint
	seen                          map[[32]byte]struct{}
	thinking                      bool
	bytes                         int
	cb                            StreamCallback
	finish                        string
	latestUsage                   *geminiUsageSnapshot
	hopCheckpointUsage            *geminiUsageSnapshot
	hopCheckpointUsageComplete    bool
	hopCheckpoint                 bool
	terminalUsage                 bool
}

func (s *geminiDecodeState) emit(delta message.StreamDelta) {
	s.preview = append(s.preview, delta)
	if s.cb != nil {
		s.cb(delta)
	}
}

func (s *geminiDecodeState) Consume(chunk geminiStreamChunk) error {
	if chunk.Error != nil {
		return &APIError{StatusCode: chunk.Error.Code, Origin: APIErrorOriginSSEEvent, Code: fmt.Sprint(chunk.Error.Code), Type: chunk.Error.Status, Message: chunk.Error.Message}
	}
	if len(chunk.Candidates) > 1 {
		return &GeminiContinuationError{Reason: "protocol_error"}
	}
	if len(chunk.Candidates) == 0 {
		if chunk.UsageMetadata != nil {
			s.observeUsage(geminiChunkUsage(chunk))
			if s.finish != "" {
				s.terminalUsage = true
			}
		}
		return nil
	}
	c := chunk.Candidates[0]
	if s.finish != "" && (len(c.Content.Parts) > 0 || c.FinishReason != "" || c.ContinuationToken != "") {
		return &GeminiContinuationError{Reason: "protocol_error"}
	}
	size := s.bytes
	for _, p := range c.Content.Parts {
		size += len(p.Text) + len(p.ThoughtSignature) + 512
		if p.FunctionCall != nil {
			size += len(p.FunctionCall.Name) + len(p.FunctionCall.Args)
		}
	}
	if size+len(c.ContinuationToken)+len(s.checkpoint.Token) > geminiContinuationMaxBufferedBytes {
		return &GeminiContinuationError{Reason: "size_limit"}
	}
	terminal := c.FinishReason != "" && c.FinishReason != geminiContinuationFinish
	digest := sha256.Sum256([]byte(c.ContinuationToken))
	duplicate := false
	if c.ContinuationToken != "" && !terminal {
		if _, seen := s.seen[digest]; seen {
			duplicate = true
			if usage := geminiChunkUsage(chunk); usage != nil && (s.checkpoint.Usage == nil || *usage != *s.checkpoint.Usage) {
				return &GeminiContinuationError{Reason: "protocol_error"}
			}
			if c.ContinuationToken != s.checkpoint.Token || len(c.Content.Parts) > 0 || len(s.parts) != s.checkpoint.PartCursor {
				return &GeminiContinuationError{Reason: "no_progress"}
			}
		}
	}
	if c.FinishReason == geminiContinuationFinish && c.ContinuationToken == "" {
		return &GeminiContinuationError{Reason: "protocol_error"}
	}
	for _, p := range c.Content.Parts {
		s.parts = append(s.parts, p)
		if p.Text != "" || p.FunctionCall != nil {
			if s.thinking && (!p.Thought || p.FunctionCall != nil) {
				s.thinking = false
				s.emit(message.StreamDelta{Type: message.StreamDeltaThinkingEnd})
			}
			if p.Text != "" {
				kind := message.StreamDeltaText
				if p.Thought {
					kind = message.StreamDeltaThinking
					s.thinking = true
				}
				s.emit(message.StreamDelta{Type: kind, Text: p.Text})
			}
		}
	}
	s.bytes = size
	if chunk.UsageMetadata != nil {
		s.observeUsage(geminiChunkUsage(chunk))
	}
	if c.ContinuationToken != "" && !terminal {
		if s.seen == nil {
			s.seen = make(map[[32]byte]struct{})
		}
		s.seen[digest] = struct{}{}
		if !duplicate {
			s.checkpoint = geminiCheckpoint{Token: c.ContinuationToken, PartCursor: len(s.parts), PreviewCursor: len(s.preview), Bytes: s.bytes, Thinking: s.thinking, Usage: geminiChunkUsage(chunk), TokenDigest: digest}
			s.hopCheckpoint = true
			s.hopCheckpointUsageComplete = s.checkpoint.Usage.complete()
			s.hopCheckpointUsage = mergeGeminiConfirmedUsage(s.checkpoint.Usage, s.hopCheckpointUsage)
		}
	}
	if c.FinishReason != "" {
		s.finish = c.FinishReason
		s.terminalUsage = chunk.UsageMetadata != nil
	} else if s.finish != "" && chunk.UsageMetadata != nil {
		s.terminalUsage = true
	}
	return nil
}

func (s *geminiDecodeState) RestoreCheckpoint(cb StreamCallback) {
	cp := s.checkpoint
	changed := len(s.preview) > cp.PreviewCursor
	clear(s.parts[cp.PartCursor:])
	s.parts = s.parts[:cp.PartCursor]
	clear(s.preview[cp.PreviewCursor:])
	s.preview = s.preview[:cp.PreviewCursor]
	s.thinking, s.bytes = cp.Thinking, cp.Bytes
	if changed && cb != nil {
		cb(message.StreamDelta{Type: message.StreamDeltaRollback, Rollback: &message.RollbackDelta{Scope: message.RollbackScopeRequest}})
		for _, delta := range s.preview {
			cb(delta)
		}
	}
}

func (s *geminiDecodeState) Materialize() (*message.Response, error) {
	resp := &message.Response{StopReason: s.finish}
	var text, thought strings.Builder
	calls := make(map[int]*openAIToolAccumulator)
	seq := geminiToolCallSeq.Add(1)
	var replay strings.Builder
	var replayThought bool
	flushReplay := func(signature string) {
		appendGeminiResponseTextPart(resp, geminiPart{Text: replay.String(), Thought: replayThought, ThoughtSignature: signature})
		replay.Reset()
	}
	for _, p := range s.parts {
		if p.FunctionCall == nil {
			if p.Text == "" && p.ThoughtSignature == "" {
				continue
			}
			if replayThought != p.Thought {
				flushReplay("")
			}
			replayThought = p.Thought
			replay.WriteString(p.Text)
			if p.ThoughtSignature != "" {
				flushReplay(p.ThoughtSignature)
			}
			if p.Thought {
				thought.WriteString(p.Text)
			} else {
				text.WriteString(p.Text)
			}
			continue
		}
		if p.FunctionCall.Name == "" {
			continue
		}
		flushReplay("")
		index := len(calls)
		id := fmt.Sprintf("gemini_%x_%d", seq, index)
		acc := &openAIToolAccumulator{id: id, name: p.FunctionCall.Name, thoughtSignature: p.ThoughtSignature}
		args := p.FunctionCall.Args
		if len(args) == 0 {
			args = json.RawMessage("{}")
		}
		acc.args.Write(args)
		calls[index] = acc
		resp.GeminiParts = append(resp.GeminiParts, message.GeminiReplayPart{Type: "function_call", ToolCallID: id, ThoughtSignature: p.ThoughtSignature})
	}
	flushReplay("")
	finalizeGeminiToolCalls(calls, resp, nil, s.finish == "MAX_TOKENS")
	resp.Content, resp.ReasoningContent = text.String(), thought.String()
	if resp.Content == "" && resp.ReasoningContent == "" && len(resp.ToolCalls) == 0 {
		if s.finish == "MAX_TOKENS" {
			return resp, &EmptyTruncationError{}
		}
		return resp, &EmptyResponseError{}
	}
	return resp, nil
}

func publishGeminiFinalTools(resp *message.Response, cb StreamCallback) {
	if cb == nil {
		return
	}
	for _, call := range resp.ToolCalls {
		for _, kind := range []string{message.StreamDeltaToolUseStart, message.StreamDeltaToolUseDelta, message.StreamDeltaToolUseEnd} {
			d := &message.ToolCallDelta{ID: call.ID, Name: call.Name, DeferExecution: true}
			if kind == message.StreamDeltaToolUseDelta {
				d.Input = string(call.Args)
			}
			cb(message.StreamDelta{Type: kind, ToolCall: d})
		}
	}
}

func (s *geminiDecodeState) observeUsage(usage *geminiUsageSnapshot) {
	if usage == nil {
		return
	}
	if usage.InputTokens < 0 || usage.OutputTokens < 0 || usage.ReasoningTokens < 0 || usage.CacheReadTokens < 0 {
		s.usageInvalid = true
	}
	if previous := s.latestUsage; previous != nil && ((usage.promptKnown && previous.promptKnown && usage.InputTokens < previous.InputTokens) || (usage.outputKnown && previous.outputKnown && usage.OutputTokens < previous.OutputTokens) || usage.ReasoningTokens < previous.ReasoningTokens || usage.CacheReadTokens < previous.CacheReadTokens) {
		s.usageInvalid = true
	}
	s.latestUsage = usage
}
