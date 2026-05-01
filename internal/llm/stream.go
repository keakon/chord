package llm

import (
	"bufio"
	"encoding/json"
	"fmt"
	"github.com/keakon/golog/log"
	"io"
	"strings"

	"github.com/keakon/chord/internal/message"
)

// StreamCallback is the function signature for receiving streaming deltas.
type StreamCallback func(delta message.StreamDelta)

// --- SSE JSON structures for Anthropic streaming ---

// sseMessageStart represents the "message_start" SSE event payload.
type sseMessageStart struct {
	Type    string `json:"type"`
	Message struct {
		ID    string `json:"id"`
		Role  string `json:"role"`
		Model string `json:"model"`
		Usage struct {
			InputTokens              int `json:"input_tokens"`
			OutputTokens             int `json:"output_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
			CacheCreation            *struct {
				Ephemeral5mInputTokens int `json:"ephemeral_5m_input_tokens"`
				Ephemeral1hInputTokens int `json:"ephemeral_1h_input_tokens"`
			} `json:"cache_creation,omitempty"`
		} `json:"usage"`
	} `json:"message"`
}

// sseContentBlockStart represents the "content_block_start" SSE event payload.
type sseContentBlockStart struct {
	Type         string `json:"type"`
	Index        int    `json:"index"`
	ContentBlock struct {
		Type  string          `json:"type"`            // "text" or "tool_use"
		Text  string          `json:"text,omitempty"`  // for text blocks
		ID    string          `json:"id,omitempty"`    // for tool_use blocks
		Name  string          `json:"name,omitempty"`  // for tool_use blocks
		Input json.RawMessage `json:"input,omitempty"` // for tool_use blocks (usually {})
	} `json:"content_block"`
}

// sseContentBlockDelta represents the "content_block_delta" SSE event payload.
type sseContentBlockDelta struct {
	Type  string `json:"type"`
	Index int    `json:"index"`
	Delta struct {
		Type        string `json:"type"`                   // "text_delta", "input_json_delta", "thinking_delta", "signature_delta"
		Text        string `json:"text,omitempty"`         // for text_delta
		PartialJSON string `json:"partial_json,omitempty"` // for input_json_delta
		Thinking    string `json:"thinking,omitempty"`     // for thinking_delta
		Signature   string `json:"signature,omitempty"`    // for signature_delta
	} `json:"delta"`
}

// sseContentBlockStop represents the "content_block_stop" SSE event payload.
type sseContentBlockStop struct {
	Type  string `json:"type"`
	Index int    `json:"index"`
}

// sseMessageDelta represents the "message_delta" SSE event payload.
type sseMessageDelta struct {
	Type  string `json:"type"`
	Delta struct {
		StopReason string `json:"stop_reason"`
	} `json:"delta"`
	Usage struct {
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// sseError represents an SSE error event payload.
type sseError struct {
	Type  string `json:"type"`
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// contentBlock tracks an in-progress content block during streaming.
type contentBlock struct {
	blockType string // "text", "tool_use", or "thinking"
	text      strings.Builder
	toolID    string
	toolName  string
	toolInput strings.Builder
	thinking  strings.Builder // accumulated thinking text (thinking blocks only)
	signature string          // thinking block signature (returned by signature_delta)
}

// parseSSEStream reads an Anthropic SSE stream from reader and calls cb for
// each incremental delta. If collector is non-nil, raw SSE data lines are
// recorded for debug dumps. It returns the fully assembled Response when the
// stream completes, or an error if the stream fails.
func parseSSEStream(reader io.Reader, cb StreamCallback, collector *SSECollector) (*message.Response, error) {
	scanner := bufio.NewScanner(reader)
	// Allow lines up to 1MB for large JSON payloads.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var (
		eventType      string
		resp           message.Response
		blocks         = make(map[int]*contentBlock)
		gotData        bool
		progressBytes  int64
		progressEvents int64
	)

	for scanner.Scan() {
		line := scanner.Text()

		if !gotData && cb != nil {
			cb(message.StreamDelta{Type: "status", Status: &message.StatusDelta{Type: "waiting_token"}})
			gotData = true
		}

		// Event type line.
		if strings.HasPrefix(line, "event: ") {
			eventType = strings.TrimPrefix(line, "event: ")
			continue
		}

		// Data line.
		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")

			if cb != nil {
				progressBytes += int64(len(line) + 1)
				progressEvents++
				cb(message.StreamDelta{Progress: &message.StreamProgressDelta{Bytes: progressBytes, Events: progressEvents}})
			}

			// Record raw SSE data for dump if collector is present.
			if collector != nil {
				collector.Add(fmt.Sprintf("%s: %s", eventType, data))
			}

			switch eventType {
			case "message_start":
				var ev sseMessageStart
				if err := json.Unmarshal([]byte(data), &ev); err != nil {
					return nil, fmt.Errorf("parse message_start: %w", err)
				}
				if resp.Usage == nil {
					resp.Usage = &message.TokenUsage{}
				}
				resp.Usage.InputTokens = ev.Message.Usage.InputTokens
				resp.Usage.CacheReadTokens = ev.Message.Usage.CacheReadInputTokens
				cacheWrite := ev.Message.Usage.CacheCreationInputTokens
				if ev.Message.Usage.CacheCreation != nil {
					cacheWrite = ev.Message.Usage.CacheCreation.Ephemeral5mInputTokens + ev.Message.Usage.CacheCreation.Ephemeral1hInputTokens
				}
				resp.Usage.CacheWriteTokens = cacheWrite

			case "content_block_start":
				var ev sseContentBlockStart
				if err := json.Unmarshal([]byte(data), &ev); err != nil {
					return nil, fmt.Errorf("parse content_block_start: %w", err)
				}
				block := &contentBlock{
					blockType: ev.ContentBlock.Type,
				}
				switch ev.ContentBlock.Type {
				case "tool_use":
					block.toolID = ev.ContentBlock.ID
					block.toolName = ev.ContentBlock.Name
					if cb != nil {
						cb(message.StreamDelta{
							Type: "tool_use_start",
							ToolCall: &message.ToolCallDelta{
								ID:   ev.ContentBlock.ID,
								Name: ev.ContentBlock.Name,
							},
						})
					}
					if p, ok := reader.(chunkPhaser); ok {
						p.SetChunkTimeout(SlowPhaseChunkTimeout)
					}
				case "thinking":
					// thinking block started; no delta emitted yet (content arrives via thinking_delta)
					if p, ok := reader.(chunkPhaser); ok {
						p.SetChunkTimeout(SlowPhaseChunkTimeout)
					}
				}
				blocks[ev.Index] = block

			case "content_block_delta":
				var ev sseContentBlockDelta
				if err := json.Unmarshal([]byte(data), &ev); err != nil {
					return nil, fmt.Errorf("parse content_block_delta: %w", err)
				}
				block, ok := blocks[ev.Index]
				if !ok {
					continue
				}
				switch ev.Delta.Type {
				case "text_delta":
					block.text.WriteString(ev.Delta.Text)
					if cb != nil {
						cb(message.StreamDelta{
							Type: "text",
							Text: ev.Delta.Text,
						})
					}
				case "input_json_delta":
					block.toolInput.WriteString(ev.Delta.PartialJSON)
					if cb != nil {
						cb(message.StreamDelta{
							Type: "tool_use_delta",
							ToolCall: &message.ToolCallDelta{
								ID:    block.toolID,
								Name:  block.toolName,
								Input: block.toolInput.String(),
							},
						})
					}
				case "thinking_delta":
					block.thinking.WriteString(ev.Delta.Thinking)
					if cb != nil {
						cb(message.StreamDelta{
							Type: "thinking",
							Text: ev.Delta.Thinking,
						})
					}
				case "signature_delta":
					// Signature is stored for replay; not shown to the user.
					block.signature = ev.Delta.Signature
				}

			case "content_block_stop":
				var ev sseContentBlockStop
				if err := json.Unmarshal([]byte(data), &ev); err != nil {
					return nil, fmt.Errorf("parse content_block_stop: %w", err)
				}
				if p, ok := reader.(chunkPhaser); ok {
					p.SetChunkTimeout(DefaultChunkTimeout)
				}
				block, ok := blocks[ev.Index]
				if !ok {
					continue
				}
				switch block.blockType {
				case "text":
					text := block.text.String()
					if text != "" {
						if resp.Content != "" {
							resp.Content += "\n"
						}
						resp.Content += text
					}
				case "tool_use":
					tc := message.ToolCall{
						ID:   block.toolID,
						Name: block.toolName,
						Args: json.RawMessage(block.toolInput.String()),
					}
					// Default to empty object if no input was received.
					if len(tc.Args) == 0 {
						tc.Args = json.RawMessage("{}")
					}
					// Validate that the accumulated arguments are valid JSON.
					// Even in the normal content_block_stop path, the model
					// could produce malformed JSON (rare but possible).
					if !json.Valid(tc.Args) {
						log.Warnf("Anthropic tool call has invalid JSON args at content_block_stop tool=%v id=%v raw_args=%v", block.toolName, block.toolID, string(tc.Args))
						tc.Args = json.RawMessage(`{"error":"malformed tool call arguments from model"}`)
					}
					resp.ToolCalls = append(resp.ToolCalls, tc)
					if cb != nil {
						cb(message.StreamDelta{
							Type: "tool_use_end",
							ToolCall: &message.ToolCallDelta{
								ID:   block.toolID,
								Name: block.toolName,
							},
						})
					}
				case "thinking":
					resp.ThinkingBlocks = append(resp.ThinkingBlocks, message.ThinkingBlock{
						Thinking:  block.thinking.String(),
						Signature: block.signature,
					})
					if cb != nil {
						cb(message.StreamDelta{Type: "thinking_end"})
					}
				}
				delete(blocks, ev.Index)

			case "message_delta":
				var ev sseMessageDelta
				if err := json.Unmarshal([]byte(data), &ev); err != nil {
					return nil, fmt.Errorf("parse message_delta: %w", err)
				}
				resp.StopReason = ev.Delta.StopReason
				if resp.Usage == nil {
					resp.Usage = &message.TokenUsage{}
				}
				resp.Usage.OutputTokens = ev.Usage.OutputTokens

			case "message_stop":
				// Stream complete. Return the assembled response.
				return &resp, nil

			case "error":
				var ev sseError
				if err := json.Unmarshal([]byte(data), &ev); err != nil {
					return nil, fmt.Errorf("parse error event: %w", err)
				}
				// Some proxies embed the HTTP status in the message (e.g. "HTTP 429 - ...").
				// Parse it out so retry/fallback logic can classify the error correctly.
				statusCode := 0
				if i := strings.Index(ev.Error.Message, "HTTP "); i >= 0 {
					fmt.Sscanf(ev.Error.Message[i+5:], "%d", &statusCode)
				}
				return nil, &APIError{
					StatusCode: statusCode,
					Message:    ev.Error.Message,
				}

			case "ping":
				// Keep-alive; ignore.
			}

			// Reset event type after processing data.
			eventType = ""
			continue
		}

		// Empty lines are event separators; just continue.
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading SSE stream: %w", err)
	}

	// If we reach EOF without a message_stop event, finalize any in-progress
	// content blocks. This can happen if the connection drops or the model
	// hits max_tokens (stop_reason == "max_tokens").
	truncated := resp.StopReason == "max_tokens"
	for idx, block := range blocks {
		switch block.blockType {
		case "text":
			text := block.text.String()
			if text != "" {
				if resp.Content != "" {
					resp.Content += "\n"
				}
				resp.Content += text
			}
		case "tool_use":
			if truncated {
				// Output was truncated; tool call arguments are incomplete.
				log.Warnf("discarding truncated Anthropic tool call tool=%v id=%v partial_input=%v", block.toolName, block.toolID, block.toolInput.String())
			} else {
				args := json.RawMessage(block.toolInput.String())
				if len(args) == 0 {
					args = json.RawMessage("{}")
				}
				// Validate JSON — stream may have been interrupted mid-argument.
				if !json.Valid(args) {
					log.Warnf("Anthropic tool call has invalid JSON args, replacing with error object tool=%v id=%v raw_args=%v", block.toolName, block.toolID, string(args))
					args = json.RawMessage(`{"error":"malformed tool call arguments from model"}`)
				}
				resp.ToolCalls = append(resp.ToolCalls, message.ToolCall{
					ID:   block.toolID,
					Name: block.toolName,
					Args: args,
				})
			}
			if cb != nil {
				cb(message.StreamDelta{
					Type: "tool_use_end",
					ToolCall: &message.ToolCallDelta{
						ID:   block.toolID,
						Name: block.toolName,
					},
				})
			}
		case "thinking":
			resp.ThinkingBlocks = append(resp.ThinkingBlocks, message.ThinkingBlock{
				Thinking:  block.thinking.String(),
				Signature: block.signature,
			})
			if cb != nil {
				cb(message.StreamDelta{Type: "thinking_end"})
			}
		}
		delete(blocks, idx)
	}

	if resp.Content != "" || len(resp.ToolCalls) > 0 {
		return &resp, nil
	}
	return nil, fmt.Errorf("SSE stream ended without message_stop event")
}
