package modelcompat

import (
	"strings"

	"github.com/keakon/chord/internal/message"
)

const (
	WireFamilyUnknown         = "unknown"
	WireFamilyAnthropic       = "anthropic"
	WireFamilyOpenAIChat      = "openai-chat"
	WireFamilyOpenAIResponses = "openai-responses"
	WireFamilyGemini          = "gemini"

	ToolResultEncodingNone               = "none"
	ToolResultEncodingOpenAIToolRole     = "openai_tool_role"
	ToolResultEncodingAnthropicUserBlock = "anthropic_user_blocks"
	ToolResultEncodingGeminiUserParts    = "gemini_user_parts"
)

type TargetModel struct {
	ProviderID string
	ModelID    string
	Variant    string
	ModelRef   string

	WireFamily              string
	ThinkingReplayEnabled   bool
	ToolResultEncoding      string
	SupportsStructuredTools bool
}

type NormalizeOptions struct {
	PreserveVisibleReasoning bool
	StructuredTools          bool
}

type NormalizeReport struct {
	DroppedThinkingBlocks int
	DowngradedToolCalls   int
	DowngradedReasoning   int
	Warnings              []string
}

// NormalizeForTarget returns a wire-only deep-copied message slice suitable for
// the current target model. It never mutates the input durable transcript.
func NormalizeForTarget(msgs []message.Message, target TargetModel, opts NormalizeOptions) ([]message.Message, NormalizeReport) {
	out := deepCopyMessages(msgs)
	report := NormalizeReport{}

	if len(out) == 0 {
		return out, report
	}

	allowThinking := strings.TrimSpace(target.WireFamily) == WireFamilyAnthropic && target.ThinkingReplayEnabled
	allowStructuredTools := opts.StructuredTools && target.SupportsStructuredTools && strings.TrimSpace(target.ToolResultEncoding) != "" && strings.TrimSpace(target.ToolResultEncoding) != ToolResultEncodingNone
	toolResultsByID := collectToolResults(out)

	for i := range out {
		msg := &out[i]

		if len(msg.ThinkingBlocks) > 0 {
			kept := make([]message.ThinkingBlock, 0, len(msg.ThinkingBlocks))
			for _, block := range msg.ThinkingBlocks {
				if !allowThinking {
					report.DroppedThinkingBlocks++
					continue
				}
				if strings.TrimSpace(block.Signature) == "" {
					report.DroppedThinkingBlocks++
					continue
				}
				if !messageAllowsAnthropicThinkingReplay(*msg) {
					report.DroppedThinkingBlocks++
					report.Warnings = append(report.Warnings, "dropped thinking blocks: missing/invalid anthropic provenance")
					continue
				}
				kept = append(kept, block)
			}
			msg.ThinkingBlocks = kept
		}

		if len(msg.ToolCalls) > 0 && !toolCallsReplayAllowed(*msg, toolResultsByID, target, allowStructuredTools) {
			downgraded := downgradeAssistantToolCallsToText(*msg)
			if downgraded.Content != msg.Content || len(msg.ToolCalls) > 0 {
				out[i] = downgraded
				report.DowngradedToolCalls++
			}
		}
	}

	if !allowStructuredTools {
		for i := range out {
			if out[i].Role != "tool" {
				continue
			}
			callID := strings.TrimSpace(out[i].ToolCallID)
			content := strings.TrimSpace(out[i].Content)
			marker := content
			if callID != "" {
				marker = joinNonEmpty("[Imported tool result for "+callID+"]", content)
			}
			out[i] = message.Message{
				Role:       "assistant",
				Content:    marker,
				Provenance: cloneProvenance(out[i].Provenance),
			}
			report.DowngradedToolCalls++
		}
	}

	return compactAdjacentAssistantMessages(out), report
}

func messageAllowsAnthropicThinkingReplay(msg message.Message) bool {
	if msg.Provenance == nil {
		return false
	}
	wire := strings.TrimSpace(msg.Provenance.WireFamily)
	return wire == WireFamilyAnthropic
}

func toolCallsReplayAllowed(msg message.Message, toolResultsByID map[string]bool, target TargetModel, allowStructuredTools bool) bool {
	if !allowStructuredTools {
		return false
	}
	if msg.Provenance != nil {
		wire := strings.TrimSpace(msg.Provenance.WireFamily)
		if wire != "" && wire != WireFamilyUnknown && wire != strings.TrimSpace(target.WireFamily) {
			if strings.TrimSpace(target.WireFamily) == WireFamilyAnthropic {
				return false
			}
		}
	}
	for _, tc := range msg.ToolCalls {
		if strings.TrimSpace(tc.ID) == "" || !toolResultsByID[strings.TrimSpace(tc.ID)] {
			return false
		}
	}
	if strings.TrimSpace(target.ToolResultEncoding) == ToolResultEncodingAnthropicUserBlock && len(msg.ToolCalls) > 0 {
		if len(msg.ThinkingBlocks) == 0 && msg.Provenance != nil && strings.Contains(msg.Provenance.Source, "claude") {
			return false
		}
	}
	return true
}

func collectToolResults(msgs []message.Message) map[string]bool {
	m := make(map[string]bool)
	for _, msg := range msgs {
		if msg.Role != "tool" {
			continue
		}
		id := strings.TrimSpace(msg.ToolCallID)
		if id != "" {
			m[id] = true
		}
	}
	return m
}

func downgradeAssistantToolCallsToText(msg message.Message) message.Message {
	blocks := make([]string, 0, len(msg.ToolCalls)+1)
	if strings.TrimSpace(msg.Content) != "" {
		blocks = append(blocks, strings.TrimSpace(msg.Content))
	}
	for _, tc := range msg.ToolCalls {
		marker := "[Imported tool call"
		if strings.TrimSpace(tc.Name) != "" {
			marker += ": " + strings.TrimSpace(tc.Name)
		}
		marker += "]"
		payload := strings.TrimSpace(string(tc.Args))
		blocks = append(blocks, joinNonEmpty(marker, payload))
	}
	return message.Message{
		Role:           "assistant",
		Content:        strings.TrimSpace(strings.Join(blocks, "\n\n")),
		ThinkingBlocks: msg.ThinkingBlocks,
		StopReason:     msg.StopReason,
		Usage:          cloneUsage(msg.Usage),
		Provenance:     cloneProvenance(msg.Provenance),
	}
}

func compactAdjacentAssistantMessages(msgs []message.Message) []message.Message {
	if len(msgs) == 0 {
		return msgs
	}
	out := make([]message.Message, 0, len(msgs))
	for _, msg := range msgs {
		if len(out) == 0 {
			out = append(out, msg)
			continue
		}
		last := &out[len(out)-1]
		if last.Role == "assistant" && msg.Role == "assistant" && len(last.ToolCalls) == 0 && len(msg.ToolCalls) == 0 && len(last.Parts) == 0 && len(msg.Parts) == 0 && len(last.ThinkingBlocks) == 0 && len(msg.ThinkingBlocks) == 0 {
			last.Content = joinNonEmpty(last.Content, msg.Content)
			continue
		}
		out = append(out, msg)
	}
	return out
}

func deepCopyMessages(msgs []message.Message) []message.Message {
	if len(msgs) == 0 {
		return nil
	}
	out := make([]message.Message, len(msgs))
	for i, msg := range msgs {
		out[i] = msg
		if len(msg.Parts) > 0 {
			parts := make([]message.ContentPart, len(msg.Parts))
			copy(parts, msg.Parts)
			for j := range parts {
				if parts[j].Data != nil {
					parts[j].Data = append([]byte(nil), parts[j].Data...)
				}
			}
			out[i].Parts = parts
		}
		if len(msg.ThinkingBlocks) > 0 {
			out[i].ThinkingBlocks = append([]message.ThinkingBlock(nil), msg.ThinkingBlocks...)
		}
		if len(msg.ToolCalls) > 0 {
			calls := make([]message.ToolCall, len(msg.ToolCalls))
			copy(calls, msg.ToolCalls)
			for j := range calls {
				if calls[j].Args != nil {
					calls[j].Args = append([]byte(nil), calls[j].Args...)
				}
			}
			out[i].ToolCalls = calls
		}
		if len(msg.LSPReviews) > 0 {
			out[i].LSPReviews = append([]message.LSPReview(nil), msg.LSPReviews...)
		}
		out[i].Audit = msg.Audit.Clone()
		out[i].Usage = cloneUsage(msg.Usage)
		out[i].Provenance = cloneProvenance(msg.Provenance)
	}
	return out
}

func cloneUsage(in *message.TokenUsage) *message.TokenUsage {
	if in == nil {
		return nil
	}
	copy := *in
	return &copy
}

func cloneProvenance(in *message.MessageProvenance) *message.MessageProvenance {
	if in == nil {
		return nil
	}
	copy := *in
	return &copy
}

func joinNonEmpty(parts ...string) string {
	filtered := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			filtered = append(filtered, p)
		}
	}
	return strings.Join(filtered, "\n\n")
}
