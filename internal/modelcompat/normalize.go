package modelcompat

import (
	"maps"
	"strings"

	"github.com/keakon/chord/internal/message"
)

const (
	WireFamilyUnknown         = "unknown"
	WireFamilyAnthropic       = "anthropic"
	WireFamilyOpenAIChat      = "openai-chat"
	WireFamilyOpenAIResponses = "openai-responses"
	WireFamilyGemini          = "gemini"

	ReasoningContinuityNone              = "none"
	ReasoningContinuityAnthropicBlocks   = "anthropic_blocks"
	ReasoningContinuityAnthropicUnsigned = "anthropic_unsigned"
	ReasoningContinuityOpenAIVisible     = "openai_visible"

	ToolResultEncodingNone               = "none"
	ToolResultEncodingOpenAIToolRole     = "openai_tool_role"
	ToolResultEncodingAnthropicUserBlock = "anthropic_user_blocks"
	ToolResultEncodingGeminiUserParts    = "gemini_user_parts"

	importedToolCallMarkerPrefix   = "[Imported tool call"
	importedToolResultMarkerPrefix = "[Imported tool result for "
	portableReasoningMarker        = "[Previous model reasoning preserved for continuity]"
)

type TargetModel struct {
	ProviderID string
	ModelID    string
	Variant    string
	ModelRef   string

	WireFamily              string
	ReasoningContinuityMode string
	ToolResultEncoding      string
	SupportsStructuredTools bool
}

// Replay compatibility degradation ladder for provider-bound native payloads.
// Levels only ever escalate for a target after it rejects the current level,
// so the default is the most information-preserving shape the wire protocol
// admits.
const (
	// ReplayCompatNative optimistically replays provider-bound native payloads
	// (encrypted Responses items, thinking blocks, thought signatures, visible
	// reasoning) to any target speaking the producing wire protocol, regardless
	// of which configured provider entry or model version produced them.
	// Whether a backend accepts such a payload is decided by that backend and
	// cannot be derived from client-side config, so the richest
	// protocol-compatible shape is sent first; a rejection escalates the level.
	ReplayCompatNative = 0
	// ReplayCompatSynthesized falls back to strict provenance matching for
	// native payloads. Foreign native Responses items are re-synthesized as
	// plain call_id-only function_call items — the same shape used for turns
	// that never had native output. Reasoning continuity is lost but the
	// action history stays intact.
	ReplayCompatSynthesized = 1
	// ReplayCompatStrict textifies completed tool trajectories whose native
	// reasoning cannot be replayed. This is the last resort for backends that
	// reject even the synthesized structured shape; external action history
	// remains visible without replaying provider-bound payloads.
	ReplayCompatStrict = 2
)

type NormalizeOptions struct {
	StructuredTools bool
	// ReplayCompat selects the degradation level for provider-bound native
	// payload replay. The zero value is the optimistic native replay.
	ReplayCompat int
}

type NormalizeReport struct {
	DroppedThinkingBlocks int
	DowngradedToolCalls   int
	DowngradedReasoning   int
	ConvertedReasoning    int
	TextifiedReasoning    int
	DroppedToolCalls      int
	DroppedToolResults    int
	// ForeignNativeReplays counts messages whose provider-bound native payloads
	// were kept via the relaxed wire-protocol-only rule instead of strict
	// provenance matching. It is diagnostic output; retry decisions compare the
	// actual normalized request shapes because a stricter level may be identical.
	ForeignNativeReplays int
	Warnings             []string
}

func (r NormalizeReport) Changed() bool {
	return r.DroppedThinkingBlocks != 0 || r.DowngradedToolCalls != 0 || r.DowngradedReasoning != 0 ||
		r.ConvertedReasoning != 0 || r.TextifiedReasoning != 0 || r.DroppedToolCalls != 0 || r.DroppedToolResults != 0 ||
		r.ForeignNativeReplays != 0 || len(r.Warnings) != 0
}

// NormalizeForTarget returns a wire-only deep-copied message slice suitable for
// the current target model. It never mutates the input durable transcript.
func NormalizeForTarget(msgs []message.Message, target TargetModel, opts NormalizeOptions) ([]message.Message, NormalizeReport) {
	out := deepCopyMessages(msgs)
	report := NormalizeReport{}

	if len(out) == 0 {
		return out, report
	}

	allowThinking := reasoningContinuityAllowsAnthropicBlocks(target)
	allowUnsignedThinking := reasoningContinuityAllowsUnsignedAnthropicBlocks(target)
	allowStructuredTools := opts.StructuredTools && target.SupportsStructuredTools && strings.TrimSpace(target.ToolResultEncoding) != "" && strings.TrimSpace(target.ToolResultEncoding) != ToolResultEncodingNone
	toolResultsByID := collectToolResults(out)
	droppedNonImportedToolIDs := make(map[string]bool)
	textifiedToolResultIDs := make(map[string]bool)

	for i := range out {
		msg := &out[i]
		reasoningToolTrajectoryInvalid := false

		if len(msg.ThinkingBlocks) > 0 {
			strictProvenance := messageAllowsAnthropicThinkingReplay(*msg, target)
			foreignProvenance := !strictProvenance && opts.ReplayCompat <= ReplayCompatNative &&
				provenanceWireFamily(*msg) == WireFamilyAnthropic
			foreignKept := false
			var portableThinking []string
			kept := make([]message.ThinkingBlock, 0, len(msg.ThinkingBlocks))
			for _, block := range msg.ThinkingBlocks {
				if !allowThinking {
					report.DroppedThinkingBlocks++
					portableThinking = appendPortableText(portableThinking, block.Thinking)
					continue
				}
				if !block.Replayable() {
					if allowUnsignedThinking && opts.ReplayCompat <= ReplayCompatNative && unsignedAnthropicThinkingReplayAllowed(*msg, target, block) {
						kept = append(kept, block)
						continue
					}
					report.DroppedThinkingBlocks++
					portableThinking = appendPortableText(portableThinking, block.Thinking)
					continue
				}
				if !strictProvenance {
					if !foreignProvenance {
						report.DroppedThinkingBlocks++
						portableThinking = appendPortableText(portableThinking, block.Thinking)
						report.Warnings = append(report.Warnings, "dropped thinking blocks: missing/invalid anthropic provenance")
						continue
					}
					foreignKept = true
				}
				kept = append(kept, block)
			}
			if foreignKept {
				report.ForeignNativeReplays++
			}
			msg.ThinkingBlocks = kept
			if len(portableThinking) > 0 && len(msg.ToolCalls) > 0 {
				msg.Content = prependPortableReasoning(msg.Content, portableThinking)
				report.TextifiedReasoning++
			}
			if len(kept) == 0 && len(msg.ToolCalls) > 0 && opts.ReplayCompat >= ReplayCompatStrict {
				reasoningToolTrajectoryInvalid = true
			}
		}

		if strings.TrimSpace(msg.ReasoningContent) != "" {
			targetRequiresReasoning := targetAllowsReasoningReplay(target)
			sourceHasNativeReasoning := AllowsOpenAIVisibleReasoningReplay(*msg)
			replayable := targetRequiresReasoning && sourceHasNativeReasoning && messageProvenanceProviderMatchesTarget(*msg, target)
			if !replayable && opts.ReplayCompat <= ReplayCompatNative && targetRequiresReasoning && sourceHasNativeReasoning {
				replayable = true
				report.ForeignNativeReplays++
			}
			if !replayable {
				portableReasoning := strings.TrimSpace(msg.ReasoningContent)
				msg.ReasoningContent = ""
				convertibleToUnsignedThinking := sourceHasNativeReasoning && reasoningContinuityAllowsUnsignedAnthropicBlocks(target)
				if portableReasoning != "" && convertibleToUnsignedThinking && opts.ReplayCompat <= ReplayCompatNative {
					msg.ThinkingBlocks = append(msg.ThinkingBlocks, message.ThinkingBlock{Thinking: portableReasoning})
					report.ConvertedReasoning++
				} else {
					report.DowngradedReasoning++
					if portableReasoning != "" && len(msg.ToolCalls) > 0 && !convertibleToUnsignedThinking {
						msg.Content = prependPortableReasoning(msg.Content, []string{portableReasoning})
						report.TextifiedReasoning++
					}
				}
				if len(msg.ToolCalls) > 0 && opts.ReplayCompat >= ReplayCompatStrict {
					reasoningToolTrajectoryInvalid = true
				}
			}
		}

		if len(msg.ResponsesOutput) > 0 {
			replayable := allowsResponsesOutputReplay(*msg, target)
			if !replayable && opts.ReplayCompat <= ReplayCompatNative && allowsForeignResponsesOutputReplay(*msg, target) {
				replayable = true
				report.ForeignNativeReplays++
			}
			if !replayable {
				// Stripping the native items leaves the turn's tool calls to
				// be re-synthesized as plain call_id-only function_call items
				// — the same shape used for turns that never had native
				// output. Dropping the turn instead would erase the model's
				// own action history; that is reserved for the strict level,
				// used only after a target rejected the synthesized shape.
				if opts.ReplayCompat >= ReplayCompatStrict && len(msg.ToolCalls) > 0 {
					reasoningToolTrajectoryInvalid = true
				}
				portableSummary := responsesReasoningSummaryText(msg.ResponsesOutput)
				msg.ResponsesOutput = nil
				report.DowngradedReasoning++
				if len(portableSummary) > 0 && len(msg.ToolCalls) > 0 {
					msg.Content = prependPortableReasoning(msg.Content, portableSummary)
					report.TextifiedReasoning++
				}
			}
		}

		if !allowsGeminiThoughtSignatureReplay(*msg, target) {
			hasSignatures := len(msg.GeminiParts) > 0
			for j := range msg.ToolCalls {
				if msg.ToolCalls[j].ThoughtSignature != "" {
					hasSignatures = true
				}
			}
			switch {
			case !hasSignatures:
			case opts.ReplayCompat <= ReplayCompatNative && allowsForeignGeminiThoughtSignatureReplay(*msg, target):
				report.ForeignNativeReplays++
			default:
				portableThoughts := geminiThoughtText(msg.GeminiParts)
				msg.GeminiParts = nil
				for j := range msg.ToolCalls {
					msg.ToolCalls[j].ThoughtSignature = ""
				}
				report.DowngradedReasoning++
				if len(portableThoughts) > 0 && len(msg.ToolCalls) > 0 {
					msg.Content = prependPortableReasoning(msg.Content, portableThoughts)
					report.TextifiedReasoning++
				}
				if opts.ReplayCompat >= ReplayCompatStrict && len(msg.ToolCalls) > 0 {
					reasoningToolTrajectoryInvalid = true
				}
			}
		}

		if len(msg.ToolCalls) > 0 && (reasoningToolTrajectoryInvalid || !toolCallsReplayAllowed(*msg, toolResultsByID, target, allowStructuredTools)) {
			if completedToolTrajectory(*msg, toolResultsByID) {
				for _, tc := range msg.ToolCalls {
					if id := strings.TrimSpace(tc.ID); id != "" {
						textifiedToolResultIDs[id] = true
					}
				}
				out[i] = downgradeCompletedToolCallsToText(*msg)
				report.DowngradedToolCalls++
				report.Warnings = append(report.Warnings, "textified completed tool trajectory for request compatibility")
				continue
			}
			if canDowngradeToolCallsToText(*msg) {
				downgraded := downgradeAssistantToolCallsToText(*msg)
				if downgraded.Content != msg.Content || len(msg.ToolCalls) > 0 {
					out[i] = downgraded
					report.DowngradedToolCalls++
				}
			} else {
				droppedCount := len(msg.ToolCalls)
				for _, tc := range msg.ToolCalls {
					if id := strings.TrimSpace(tc.ID); id != "" {
						droppedNonImportedToolIDs[id] = true
					}
				}
				msg.ToolCalls = nil
				msg.ResponsesOutput = nil
				msg.GeminiParts = nil
				report.Warnings = append(report.Warnings, "dropped unreplayable non-imported tool calls from request context")
				report.DroppedToolCalls += droppedCount
			}
		}
	}

	for i := range out {
		if out[i].Role != message.RoleTool {
			continue
		}
		if textifiedToolResultIDs[strings.TrimSpace(out[i].ToolCallID)] {
			callID := strings.TrimSpace(out[i].ToolCallID)
			out[i] = message.Message{
				Role:       message.RoleAssistant,
				Content:    joinNonEmpty("[Previous tool result for "+callID+"]", out[i].Content),
				Provenance: cloneProvenance(out[i].Provenance),
			}
			report.DowngradedToolCalls++
			continue
		}
		if !allowStructuredTools {
			if !canDowngradeToolResultToText(out[i]) {
				out[i].Role = ""
				report.Warnings = append(report.Warnings, "dropped non-imported tool result from request context")
				report.DroppedToolResults++
				continue
			}
			callID := strings.TrimSpace(out[i].ToolCallID)
			content := strings.TrimSpace(out[i].Content)
			marker := content
			if callID != "" {
				marker = joinNonEmpty(importedToolResultMarkerPrefix+callID+"]", content)
			}
			out[i] = message.Message{
				Role:       message.RoleAssistant,
				Content:    marker,
				Provenance: cloneProvenance(out[i].Provenance),
			}
			report.DowngradedToolCalls++
		}
	}
	if len(droppedNonImportedToolIDs) > 0 || !allowStructuredTools {
		filtered := out[:0]
		for _, msg := range out {
			if msg.Role == "" {
				continue
			}
			if msg.Role == message.RoleTool && droppedNonImportedToolIDs[strings.TrimSpace(msg.ToolCallID)] {
				report.DroppedToolResults++
				continue
			}
			filtered = append(filtered, msg)
		}
		out = filtered
	}

	filtered := out[:0]
	for _, msg := range out {
		if msg.Role == message.RoleAssistant && strings.TrimSpace(msg.Content) == "" && len(msg.Parts) == 0 && len(msg.ToolCalls) == 0 && !message.HasReplayableThinkingBlocks(msg.ThinkingBlocks) {
			continue
		}
		filtered = append(filtered, msg)
	}
	out = filtered

	return compactAdjacentAssistantMessages(out), report
}

func targetAllowsReasoningReplay(target TargetModel) bool {
	return strings.TrimSpace(target.ReasoningContinuityMode) == ReasoningContinuityOpenAIVisible && strings.TrimSpace(target.WireFamily) == WireFamilyOpenAIChat
}

// allowsResponsesOutputReplay reports whether msg's native Responses API
// output items can be replayed to the current target. The encrypted payload
// is bound to the producing model, so replay requires an openai-responses
// target and matching provenance (same wire family and model); anything else
// risks a 400 reasoning-replay rejection.
func allowsResponsesOutputReplay(msg message.Message, target TargetModel) bool {
	if strings.TrimSpace(target.WireFamily) != WireFamilyOpenAIResponses {
		return false
	}
	if msg.Provenance == nil {
		return false
	}
	if strings.TrimSpace(msg.Provenance.WireFamily) != WireFamilyOpenAIResponses {
		return false
	}
	return messageProvenanceMatchesTarget(msg, target)
}

// allowsForeignResponsesOutputReplay is the relaxed native-replay rule for
// ReplayCompatNative: only the wire protocol must match. Encrypted reasoning
// payloads are opaque to the client and decrypted by the producing backend
// platform, which is identified by neither the configured provider entry nor
// the model version string; a backend that cannot decrypt them rejects the
// request and the caller escalates the replay compatibility level.
func allowsForeignResponsesOutputReplay(msg message.Message, target TargetModel) bool {
	return strings.TrimSpace(target.WireFamily) == WireFamilyOpenAIResponses &&
		provenanceWireFamily(msg) == WireFamilyOpenAIResponses
}

func reasoningContinuityAllowsAnthropicBlocks(target TargetModel) bool {
	if strings.TrimSpace(target.WireFamily) != WireFamilyAnthropic {
		return false
	}
	mode := strings.TrimSpace(target.ReasoningContinuityMode)
	return mode == ReasoningContinuityAnthropicBlocks || mode == ReasoningContinuityAnthropicUnsigned
}

func reasoningContinuityAllowsUnsignedAnthropicBlocks(target TargetModel) bool {
	return strings.TrimSpace(target.WireFamily) == WireFamilyAnthropic && strings.TrimSpace(target.ReasoningContinuityMode) == ReasoningContinuityAnthropicUnsigned
}

func unsignedAnthropicThinkingReplayAllowed(msg message.Message, target TargetModel, block message.ThinkingBlock) bool {
	if strings.TrimSpace(block.Thinking) == "" || strings.TrimSpace(block.Signature) != "" || strings.TrimSpace(block.Data) != "" {
		return false
	}
	return messageProvenanceMatchesTarget(msg, target) && provenanceWireFamily(msg) == WireFamilyAnthropic
}

// AllowsOpenAIVisibleReasoningReplay reports whether msg carries
// OpenAI Chat-native visible reasoning state that can be safely replayed as
// reasoning_content for an OpenAI-compatible chat-completions target.
func AllowsOpenAIVisibleReasoningReplay(msg message.Message) bool {
	if strings.TrimSpace(msg.ReasoningContent) == "" || msg.Provenance == nil {
		return false
	}
	return strings.TrimSpace(msg.Provenance.WireFamily) == WireFamilyOpenAIChat
}

// allowsGeminiThoughtSignatureReplay reports whether msg's Gemini thought
// signatures can be replayed to the current target. Signatures are bound to
// the producing model, so replay requires a gemini target with matching
// provenance; anything else strips them (other wire formats never serialize
// them, but stale signatures must not survive a model switch back to gemini).
func allowsGeminiThoughtSignatureReplay(msg message.Message, target TargetModel) bool {
	if strings.TrimSpace(target.WireFamily) != WireFamilyGemini {
		return false
	}
	if provenanceWireFamily(msg) != WireFamilyGemini {
		return false
	}
	return messageProvenanceMatchesTarget(msg, target)
}

// allowsForeignGeminiThoughtSignatureReplay is the relaxed variant for
// ReplayCompatNative: signatures produced through the gemini wire protocol are
// replayed to any gemini target, since the backend — not client-side config —
// decides whether it can validate them; a rejection escalates the replay
// compatibility level. Non-gemini targets always strip, so stale signatures
// never survive a switch away from the gemini wire format.
func allowsForeignGeminiThoughtSignatureReplay(msg message.Message, target TargetModel) bool {
	return strings.TrimSpace(target.WireFamily) == WireFamilyGemini &&
		provenanceWireFamily(msg) == WireFamilyGemini
}

func provenanceWireFamily(msg message.Message) string {
	if msg.Provenance == nil {
		return ""
	}
	return strings.TrimSpace(msg.Provenance.WireFamily)
}

func messageAllowsAnthropicThinkingReplay(msg message.Message, target TargetModel) bool {
	if msg.Provenance == nil {
		return false
	}
	wire := strings.TrimSpace(msg.Provenance.WireFamily)
	providerID := strings.TrimSpace(msg.Provenance.ProviderID)
	return wire == WireFamilyAnthropic && (providerID == "" || providerID == strings.TrimSpace(target.ProviderID))
}

func messageProvenanceMatchesTarget(msg message.Message, target TargetModel) bool {
	if msg.Provenance == nil {
		return false
	}
	return strings.TrimSpace(msg.Provenance.ProviderID) == strings.TrimSpace(target.ProviderID) &&
		strings.TrimSpace(msg.Provenance.ModelID) == strings.TrimSpace(target.ModelID)
}

func messageProvenanceProviderMatchesTarget(msg message.Message, target TargetModel) bool {
	if msg.Provenance == nil {
		return false
	}
	return strings.TrimSpace(msg.Provenance.ProviderID) == strings.TrimSpace(target.ProviderID)
}

func toolCallsReplayAllowed(msg message.Message, toolResultsByID map[string]bool, target TargetModel, allowStructuredTools bool) bool {
	if !allowStructuredTools {
		return false
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

func completedToolTrajectory(msg message.Message, toolResultsByID map[string]bool) bool {
	if len(msg.ToolCalls) == 0 {
		return false
	}
	for _, tc := range msg.ToolCalls {
		id := strings.TrimSpace(tc.ID)
		if id == "" || !toolResultsByID[id] {
			return false
		}
	}
	return true
}

func collectToolResults(msgs []message.Message) map[string]bool {
	m := make(map[string]bool)
	for _, msg := range msgs {
		if msg.Role != message.RoleTool {
			continue
		}
		id := strings.TrimSpace(msg.ToolCallID)
		if id != "" {
			m[id] = true
		}
	}
	return m
}

func canDowngradeToolCallsToText(msg message.Message) bool {
	return msg.Provenance != nil && msg.Provenance.Imported
}

func canDowngradeToolResultToText(msg message.Message) bool {
	return msg.Provenance != nil && msg.Provenance.Imported
}

func downgradeAssistantToolCallsToText(msg message.Message) message.Message {
	blocks := make([]string, 0, len(msg.ToolCalls)+1)
	if strings.TrimSpace(msg.Content) != "" {
		blocks = append(blocks, strings.TrimSpace(msg.Content))
	}
	for _, tc := range msg.ToolCalls {
		marker := importedToolCallMarkerPrefix
		if strings.TrimSpace(tc.Name) != "" {
			marker += ": " + strings.TrimSpace(tc.Name)
		}
		marker += "]"
		payload := strings.TrimSpace(string(tc.Args))
		blocks = append(blocks, joinNonEmpty(marker, payload))
	}
	return message.Message{
		Role:             message.RoleAssistant,
		Content:          strings.TrimSpace(strings.Join(blocks, "\n\n")),
		ThinkingBlocks:   msg.ThinkingBlocks,
		ReasoningContent: msg.ReasoningContent,
		StopReason:       msg.StopReason,
		Usage:            cloneUsage(msg.Usage),
		Provenance:       cloneProvenance(msg.Provenance),
	}
}

func appendPortableText(items []string, text string) []string {
	if text = strings.TrimSpace(text); text != "" {
		return append(items, text)
	}
	return items
}

func prependPortableReasoning(content string, reasoning []string) string {
	if len(reasoning) == 0 {
		return content
	}
	parts := []string{portableReasoningMarker, strings.Join(reasoning, "\n")}
	if strings.TrimSpace(content) != "" {
		parts = append(parts, content)
	}
	return strings.Join(parts, "\n\n")
}

func responsesReasoningSummaryText(items []message.ResponsesOutputItem) []string {
	var out []string
	for _, item := range items {
		if item.Type != "reasoning" {
			continue
		}
		for _, summary := range item.Summary {
			out = appendPortableText(out, summary.Text)
		}
	}
	return out
}

func geminiThoughtText(parts []message.GeminiReplayPart) []string {
	var out []string
	for _, part := range parts {
		if part.Type == "thought" {
			out = appendPortableText(out, part.Text)
		}
	}
	return out
}

func downgradeCompletedToolCallsToText(msg message.Message) message.Message {
	blocks := make([]string, 0, len(msg.ToolCalls)+1)
	if strings.TrimSpace(msg.Content) != "" {
		blocks = append(blocks, strings.TrimSpace(msg.Content))
	}
	for _, tc := range msg.ToolCalls {
		marker := "[Previous tool call"
		if strings.TrimSpace(tc.Name) != "" {
			marker += ": " + strings.TrimSpace(tc.Name)
		}
		marker += "]"
		blocks = append(blocks, joinNonEmpty(marker, strings.TrimSpace(string(tc.Args))))
	}
	return message.Message{
		Role:             message.RoleAssistant,
		Content:          strings.TrimSpace(strings.Join(blocks, "\n\n")),
		ReasoningContent: msg.ReasoningContent,
		StopReason:       msg.StopReason,
		Usage:            cloneUsage(msg.Usage),
		Provenance:       cloneProvenance(msg.Provenance),
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
		if last.Role == message.RoleAssistant && msg.Role == message.RoleAssistant && len(last.ToolCalls) == 0 && len(msg.ToolCalls) == 0 && len(last.Parts) == 0 && len(msg.Parts) == 0 && len(last.ThinkingBlocks) == 0 && len(msg.ThinkingBlocks) == 0 && len(last.ResponsesOutput) == 0 && len(msg.ResponsesOutput) == 0 && len(last.GeminiParts) == 0 && len(msg.GeminiParts) == 0 && strings.TrimSpace(last.ReasoningContent) == "" && strings.TrimSpace(msg.ReasoningContent) == "" {
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
		if len(msg.ResponsesOutput) > 0 {
			items := make([]message.ResponsesOutputItem, len(msg.ResponsesOutput))
			copy(items, msg.ResponsesOutput)
			for j := range items {
				items[j].Content = append([]message.ResponsesOutputContent(nil), items[j].Content...)
				if len(items[j].Summary) > 0 {
					items[j].Summary = append([]message.ResponsesReasoningSummary(nil), items[j].Summary...)
				}
			}
			out[i].ResponsesOutput = items
		}
		if len(msg.GeminiParts) > 0 {
			out[i].GeminiParts = append([]message.GeminiReplayPart(nil), msg.GeminiParts...)
		}
		if len(msg.CompactionFileRevisions) > 0 {
			out[i].CompactionFileRevisions = make(map[string]string, len(msg.CompactionFileRevisions))
			maps.Copy(out[i].CompactionFileRevisions, msg.CompactionFileRevisions)
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
