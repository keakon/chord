package ctxmgr

import "github.com/keakon/chord/internal/message"

// MessageBytes splits the byte accounting of a message slice into categories so
// diagnostics can attribute a number to a source instead of reporting one
// opaque total.
//
// Two different questions share this type, and mixing them up produces a
// misleading gauge:
//
//   - Context accounting (MessageBytesBreakdown) answers "how many bytes does
//     this history put on the wire". A path-only attachment still counts: the
//     resolver loads the file and the model receives the bytes.
//   - Resident accounting (MessageResidentBytesBreakdown) answers "how many
//     bytes does this history hold in the heap". A path-only attachment counts
//     zero; only Data does.
//
// Text, tool-call, thinking, responses and Gemini buckets are identical in both
// modes — those fields only ever exist in memory.
type MessageBytes struct {
	Text      int `json:"text_bytes"`
	Image     int `json:"image_bytes"`
	PDF       int `json:"pdf_bytes"`
	ToolCalls int `json:"tool_call_bytes"`
	Thinking  int `json:"thinking_bytes"`
	Responses int `json:"responses_output_bytes"`
	Gemini    int `json:"gemini_part_bytes"`
}

// Total is the sum of every bucket.
func (b MessageBytes) Total() int {
	return b.Text + b.Image + b.PDF + b.ToolCalls + b.Thinking + b.Responses + b.Gemini
}

// MessageBytesBreakdown attributes the wire/context accounting to categories.
// The total equals messageContextBytes for the same input, which is what the
// compaction and token budgets consume.
func MessageBytesBreakdown(messages []message.Message) MessageBytes {
	return messageBytesBreakdown(messages, false)
}

// MessageResidentBytesBreakdown attributes the heap accounting to categories.
// It differs from MessageBytesBreakdown in exactly one place: attachment
// payloads are billed by the inline bytes actually held, so an attachment that
// has been moved to the session directory contributes zero. Diagnostics use
// this; budget decisions must keep using MessageBytesBreakdown, because an
// image on disk still occupies context.
func MessageResidentBytesBreakdown(messages []message.Message) MessageBytes {
	return messageBytesBreakdown(messages, true)
}

func messageBytesBreakdown(messages []message.Message, resident bool) MessageBytes {
	messages = message.DirectoryInstructionAccountingMessages(messages)
	var b MessageBytes
	for _, msg := range messages {
		if msg.NativeTools != nil && len(msg.NativeTools.Items) > 0 {
			for _, item := range msg.NativeTools.Items {
				b.Text += len(item)
			}
			if resident {
				b.Image += nativeImageResidentBytes(msg.NativeTools)
			} else {
				imageBytes, _ := nativeImageAccounting(msg.NativeTools)
				b.Image += imageBytes
			}
			continue
		}
		if len(msg.Parts) > 0 {
			for _, part := range msg.Parts {
				if part.IsBinary() {
					payloadBytes := int(part.PayloadBytes())
					if resident {
						payloadBytes = len(part.Data)
					}
					if part.Type == message.ContentPartPDF {
						b.PDF += payloadBytes
					} else {
						b.Image += payloadBytes
					}
					continue
				}
				if resident {
					b.Text += len(part.Text) + len(part.Data)
				} else {
					b.Text += len(part.Text) + int(part.PayloadBytes())
				}
			}
		} else {
			b.Text += len(msg.Content)
		}
		b.Text += len(msg.ToolCallID) + len(msg.ReasoningContent)
		for _, tc := range msg.ToolCalls {
			b.ToolCalls += len(tc.ID) + len(tc.Name) + len(tc.Args)
		}
		for _, tb := range msg.ThinkingBlocks {
			b.Thinking += len(tb.Thinking) + len(tb.Signature) + len(tb.Data)
		}
		for _, item := range msg.ResponsesOutput {
			b.Responses += len(item.ID) + len(item.EncryptedContent)
			for _, summary := range item.Summary {
				b.Responses += len(summary.Text)
			}
		}
		for _, part := range msg.GeminiParts {
			b.Gemini += len(part.ThoughtSignature)
		}
	}
	return b
}

// nativeImageResidentBytes sums the inline payloads native replay items hold.
// Unlike nativeImageAccounting it does not expand to base64: the base64 form is
// what goes on the wire, not what sits in the heap.
func nativeImageResidentBytes(native *message.NativeToolHistory) int {
	images, err := native.ImageReplayItems()
	if err != nil {
		return 0
	}
	total := 0
	for _, image := range images {
		part, confirmed := image.OriginalPart()
		if !confirmed {
			continue
		}
		total += len(part.Data)
	}
	return total
}

// BytesBreakdown attributes the current history to categories using the
// wire/context accounting. It scans the live slice under the read lock; callers
// are diagnostics (debug dump, context overlay), not per-request paths.
func (m *Manager) BytesBreakdown() MessageBytes {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return MessageBytesBreakdown(m.messages)
}

// ResidentBytesBreakdown attributes the current history to categories using the
// heap accounting. Diagnostics only, for the same reason as BytesBreakdown.
func (m *Manager) ResidentBytesBreakdown() MessageBytes {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return MessageResidentBytesBreakdown(m.messages)
}
