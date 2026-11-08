package message

import (
	"encoding/base64"
	"encoding/json"
)

// MaxImageDimension and MaxImagePixels bound one image's geometry: the largest
// dimension and pixel count Chord accepts for generated and replayed images.
// Image generation validates requests against them, and token estimation uses
// them as the conservative upper bound of one image's planning allowance.
const (
	MaxImageDimension = 12000
	MaxImagePixels    = 36_000_000
)

// ImagePartEstimateTokens is the input-token allowance one image part carries in
// every estimate. Providers bill images per image (patch- or scale-based), not
// per base64 byte — byte accounting charged a 400 KB screenshot ~130K phantom
// tokens. The allowance covers the largest normalized image Chord accepts
// under the conservative Anthropic-style ceil(w*h/750) rule (2000x2000), so an
// unknown provider does not make capacity planning optimistic. PDF parts keep
// the byte-based accounting: their cost scales with the document and the part
// carries no page count to estimate.
const ImagePartEstimateTokens = (2000*2000 + 749) / 750

// NativeImageAccounting reports the two quantities native image replay
// contributes: the rehydrated base64 payload bytes and the per-image token
// allowance replacing them. Native image bytes are rehydrated into result at
// request time, so they are charged as image tokens separately from those
// base64 bytes, just as for ordinary image parts.
func NativeImageAccounting(native *NativeToolHistory) (payloadBytes, tokens int) {
	images, err := native.ImageReplayItems()
	if err != nil {
		return 0, 0
	} // Invalid receipts cannot be dispatched.
	for _, image := range images {
		part, confirmed := image.OriginalPart()
		if !confirmed {
			continue
		}
		payloadBytes += base64.StdEncoding.EncodedLen(int(part.PayloadBytes()))
		allowance := ImagePartEstimateTokens
		var summary struct {
			Images []struct {
				Width  int64 `json:"width"`
				Height int64 `json:"height"`
			} `json:"images"`
		}
		if json.Unmarshal(image.Call.Result, &summary) == nil && len(summary.Images) == 1 {
			img := summary.Images[0]
			// Native replay preserves originals instead of normalizing to 2000px.
			// This is a conservative planning allowance, not a billing formula.
			if img.Width > 0 && img.Height > 0 && img.Width <= MaxImageDimension && img.Height <= MaxImageDimension && img.Width*img.Height <= MaxImagePixels {
				allowance = max(allowance, int((img.Width*img.Height+749)/750))
			}
		}
		tokens += allowance
	}
	return payloadBytes, tokens
}

// ImagePartAccounting reports the two image-side quantities the token estimator
// keeps apart from the byte counters: the payload bytes image parts contribute
// and the per-image token allowance replacing them.
func ImagePartAccounting(messages []Message) (payloadBytes, tokens int) {
	for _, msg := range messages {
		if msg.NativeTools != nil && len(msg.NativeTools.Items) > 0 {
			imageBytes, imageTokens := NativeImageAccounting(msg.NativeTools)
			payloadBytes += imageBytes
			tokens += imageTokens
			continue
		}
		for _, part := range msg.Parts {
			if part.Type != ContentPartImage {
				continue
			}
			payloadBytes += int(part.PayloadBytes())
			tokens += ImagePartEstimateTokens
		}
	}
	return payloadBytes, tokens
}

// EstimateMessagesTokens returns the approximate input-token count for a slice
// of messages, including multipart text and attachment payloads.
func EstimateMessagesTokens(messages []Message) int {
	messages = DirectoryInstructionAccountingMessages(messages)
	total := 0
	for _, msg := range messages {
		total += EstimateMessageTokens(msg)
	}
	return total
}

// EstimateMessageTokens returns approximate token count for a single message.
func EstimateMessageTokens(msg Message) int {
	// Native adapters replay the complete raw surface instead of its display
	// mirrors (content, thinking and client calls).
	if msg.NativeTools != nil && len(msg.NativeTools.Items) > 0 {
		n := 0
		for _, item := range msg.NativeTools.Items {
			n += len(item)
		}
		_, imageTokens := NativeImageAccounting(msg.NativeTools)
		return max(1, n/3+imageTokens)
	}
	payloadBytes := len(msg.Content)
	imageTokens := 0
	if len(msg.Parts) > 0 {
		payloadBytes = 0
		for _, part := range msg.Parts {
			if part.Type == ContentPartImage {
				imageTokens += ImagePartEstimateTokens
				continue
			}
			payloadBytes += len(part.Text) + int(part.PayloadBytes())
		}
	}
	n := payloadBytes/3 + imageTokens
	n += len(msg.ToolCallID) / 3
	for _, tc := range msg.ToolCalls {
		n += len(tc.Args) / 3
	}
	for _, tb := range msg.ThinkingBlocks {
		n += (len(tb.Thinking) + len(tb.Signature) + len(tb.Data)) / 3
	}
	for _, item := range msg.ResponsesOutput {
		n += (len(item.ID) + len(item.EncryptedContent)) / 3
		for _, summary := range item.Summary {
			n += len(summary.Text) / 3
		}
	}
	for _, part := range msg.GeminiParts {
		n += len(part.ThoughtSignature) / 3
	}
	n += len(msg.ReasoningContent) / 3
	if n < 1 {
		n = 1
	}
	return n
}
