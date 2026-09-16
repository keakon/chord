package modelcompat

import (
	"strings"

	"github.com/keakon/chord/internal/message"
)

// Provider-bound thinking state — Anthropic thinking blocks, Gemini thought
// signatures, Responses reasoning items — is opaque to Chord and validated by
// the backend that produced it. Which wire Chord speaks to that backend is a
// local detail: the same Gemini backend can be reached through a
// Messages-compatible gateway, a Chat Completions gateway, or the native
// generateContent API, and all of them accept the same signature. Replay is
// therefore gated on the *native family* (which upstream produced and can
// validate the blob) rather than on the wire family, which only selects the
// carrier shape a request uses.
//
// A family that cannot be resolved on either side keeps the blob stripped:
// replaying a signature the target is not known to validate only produces a
// rejection, and the retry ladder cannot recover the continuity it dropped.

const (
	NativeFamilyUnknown   = ""
	NativeFamilyAnthropic = "anthropic"
	NativeFamilyGemini    = "gemini"
	NativeFamilyOpenAI    = "openai"
)

// ModelNativeFamily infers the upstream family from a model ID. Vendor prefixes
// and aliases are common, so the match stays deliberately loose; an unknown
// name falls back to the family implied by the wire.
func ModelNativeFamily(modelID string) string {
	m := strings.ToLower(strings.TrimSpace(modelID))
	if slash := strings.LastIndex(m, "/"); slash >= 0 {
		m = m[slash+1:]
	}
	switch {
	case m == "":
		return NativeFamilyUnknown
	case strings.Contains(m, "gemini"), strings.Contains(m, "vertex"):
		return NativeFamilyGemini
	case strings.Contains(m, "claude"), strings.Contains(m, "anthropic"):
		return NativeFamilyAnthropic
	case strings.Contains(m, "gpt"), strings.Contains(m, "codex"), strings.Contains(m, "chatgpt"):
		return NativeFamilyOpenAI
	}
	return NativeFamilyUnknown
}

// WireNativeFamily maps a wire family onto the upstream family it implies when
// the model name says nothing. The Chat Completions wire implies nothing (it
// fronts Claude and Gemini backends alike), and Responses items are the only
// OpenAI-family replay blob.
func WireNativeFamily(wireFamily string) string {
	switch strings.TrimSpace(wireFamily) {
	case WireFamilyAnthropic:
		return NativeFamilyAnthropic
	case WireFamilyGemini:
		return NativeFamilyGemini
	case WireFamilyOpenAIResponses:
		return NativeFamilyOpenAI
	default:
		return NativeFamilyUnknown
	}
}

// ToolCallsThoughtSignature returns the first provider-bound thought signature
// carried by a tool-call step, which is also the value a wire with a single
// step-level carrier (Gemini's first functionCall) has to serialize.
func ToolCallsThoughtSignature(toolCalls []message.ToolCall) string {
	for _, tc := range toolCalls {
		if signature := strings.TrimSpace(tc.ThoughtSignature); signature != "" {
			return signature
		}
	}
	return ""
}

// MessageNativeFamily resolves the family that produced msg's thinking state.
func MessageNativeFamily(msg message.Message) string {
	if msg.Provenance != nil {
		if family := strings.TrimSpace(msg.Provenance.NativeFamily); family != NativeFamilyUnknown {
			return family
		}
		if family := ModelNativeFamily(msg.Provenance.ModelID); family != NativeFamilyUnknown {
			return family
		}
		if family := WireNativeFamily(msg.Provenance.WireFamily); family != NativeFamilyUnknown {
			return family
		}
	}
	return NativeFamilyUnknown
}

// targetNativeFamily is the family the current request reaches. It is resolved
// by the caller from the target model and its compat overrides, because only
// the LLM layer knows how a chat dialect maps back onto a model family.
func targetNativeFamily(target TargetModel) string {
	if family := strings.TrimSpace(target.NativeFamily); family != NativeFamilyUnknown {
		return family
	}
	return WireNativeFamily(target.WireFamily)
}

// geminiSignatureFamily resolves the family that owns a Gemini-shaped
// signature. The carrier fields are Gemini-specific, so a message without a
// resolvable model keeps the gemini family rather than falling back to the
// wire.
func geminiSignatureFamily(msg message.Message) string {
	if msg.Provenance != nil {
		if family := strings.TrimSpace(msg.Provenance.NativeFamily); family != NativeFamilyUnknown {
			return family
		}
		if family := ModelNativeFamily(msg.Provenance.ModelID); family != NativeFamilyUnknown {
			return family
		}
	}
	return NativeFamilyGemini
}

// sameNativeFamily reports whether a blob produced by blobFamily may be
// replayed to a request reaching targetFamily.
func sameNativeFamily(blobFamily, targetFamily string) bool {
	return blobFamily != NativeFamilyUnknown && blobFamily == targetFamily
}

// targetCarriesAnthropicBlocks reports whether NormalizeForTarget must keep
// Anthropic-shaped thinking blocks for this target, i.e. whether the target
// wire has a carrier for them.
func targetCarriesAnthropicBlocks(target TargetModel) bool {
	switch strings.TrimSpace(target.WireFamily) {
	case WireFamilyAnthropic:
		mode := strings.TrimSpace(target.ReasoningContinuityMode)
		if mode == ReasoningContinuityAnthropicBlocks || mode == ReasoningContinuityAnthropicUnsigned {
			return true
		}
		// A Messages-wire gateway fronting a Gemini model replays the signed
		// blocks the same backend produced even when this request does not
		// enable thinking: Gemini 3 validates function-call history regardless
		// of the request's thinking setting.
		return targetNativeFamily(target) == NativeFamilyGemini
	case WireFamilyOpenAIChat:
		family := targetNativeFamily(target)
		return family == NativeFamilyAnthropic || family == NativeFamilyGemini
	case WireFamilyGemini:
		// The request serializes the signature as a thought part.
		return targetNativeFamily(target) == NativeFamilyGemini
	default:
		return false
	}
}

// targetCarriesGeminiSignatures reports whether the target wire has a carrier
// for a Gemini thought signature.
func targetCarriesGeminiSignatures(target TargetModel) bool {
	if targetNativeFamily(target) != NativeFamilyGemini {
		return false
	}
	switch strings.TrimSpace(target.WireFamily) {
	case WireFamilyGemini, WireFamilyOpenAIChat, WireFamilyAnthropic:
		return true
	default:
		return false
	}
}

// adaptThinkingStateCarrier rewrites msg's thinking-state carrier into the
// shape the target wire serializes, and returns how many blobs were rewritten.
// It never converts across families: callers only reach it after establishing
// that the blob's family matches the target, so a Claude thinking block is
// never re-labelled as a Gemini signature.
func adaptThinkingStateCarrier(msg *message.Message, target TargetModel) int {
	converted := 0
	switch strings.TrimSpace(target.WireFamily) {
	case WireFamilyOpenAIChat:
		// Gemini keeps one signature per step, on its first function call.
		if ToolCallsThoughtSignature(msg.ToolCalls) != "" {
			return 0
		}
		for _, part := range msg.GeminiParts {
			if part.Type != "function_call" || strings.TrimSpace(part.ThoughtSignature) == "" || len(msg.ToolCalls) == 0 {
				continue
			}
			msg.ToolCalls[0].ThoughtSignature = part.ThoughtSignature
			converted++
			break
		}
		if targetNativeFamily(target) == NativeFamilyGemini && ToolCallsThoughtSignature(msg.ToolCalls) == "" && len(msg.ToolCalls) > 0 {
			for _, block := range msg.ThinkingBlocks {
				if block.Data == "" && strings.TrimSpace(block.Signature) != "" {
					msg.ToolCalls[0].ThoughtSignature = block.Signature
					converted++
					break
				}
			}
		}
	case WireFamilyAnthropic:
		// Anthropic has no per-call signature slot, so only the thought parts
		// (which carry their text) map onto a thinking block.
		if len(msg.ThinkingBlocks) > 0 {
			return 0
		}
		for _, part := range msg.GeminiParts {
			if part.Type != "thought" || strings.TrimSpace(part.ThoughtSignature) == "" {
				continue
			}
			msg.ThinkingBlocks = append(msg.ThinkingBlocks, message.ThinkingBlock{
				Thinking:  part.Text,
				Signature: part.ThoughtSignature,
			})
			converted++
		}
	case WireFamilyGemini:
		// The native wire serializes signatures as thought parts; a redacted
		// block has no native counterpart.
		if len(msg.GeminiParts) > 0 {
			return 0
		}
		for _, block := range msg.ThinkingBlocks {
			if block.Data != "" || strings.TrimSpace(block.Signature) == "" {
				continue
			}
			msg.GeminiParts = append(msg.GeminiParts, message.GeminiReplayPart{
				Type:             "thought",
				Text:             block.Thinking,
				ThoughtSignature: block.Signature,
			})
			converted++
		}
	}
	return converted
}
