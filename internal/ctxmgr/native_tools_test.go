package ctxmgr

import (
	"encoding/json"
	"testing"

	"github.com/keakon/chord/internal/message"
)

func TestNativeReplayAccountingExcludesDisplayMirrors(t *testing.T) {
	raw := json.RawMessage(`{"type":"text","text":"Sample native content"}`)
	base := message.Message{Role: message.RoleAssistant, NativeTools: &message.NativeToolHistory{Items: []json.RawMessage{raw}}}
	mirrored := base.Clone()
	mirrored.Content = "Sample native content"
	mirrored.ThinkingBlocks = []message.ThinkingBlock{{Thinking: "Sample reasoning", Signature: "sample-signature"}}
	mirrored.ToolCalls = []message.ToolCall{{ID: "call-1", Name: "sample_lookup", Args: json.RawMessage(`{"query":"sample"}`)}}
	mirrored.ResponsesOutput = []message.ResponsesOutputItem{{ID: "item-1", EncryptedContent: "sample-payload"}}
	for _, msg := range []message.Message{base, mirrored} {
		if got := EstimateMessageTokens(msg); got != len(raw)/3 {
			t.Fatalf("tokens=%d, want %d", got, len(raw)/3)
		}
		if got := EstimateMessagesBytes([]message.Message{msg}); got != len(raw) {
			t.Fatalf("bytes=%d, want %d", got, len(raw))
		}
	}
	mirrored.NativeTools.Items = nil
	if EstimateMessageTokens(mirrored) <= 1 || EstimateMessagesBytes([]message.Message{mirrored}) == 0 {
		t.Fatal("receipt without raw items lost ordinary content")
	}
}

func TestNativeImageReplayAccountingSeparatesImagePayload(t *testing.T) {
	raw := json.RawMessage(`{"type":"image_generation_call","id":"image-1","status":"completed"}`)
	makeMessage := func(size int64) message.Message {
		return message.Message{Role: message.RoleAssistant, Content: "Display summary", Parts: []message.ContentPart{{Type: message.ContentPartImage, DataBytes: size}}, NativeTools: &message.NativeToolHistory{
			Items: []json.RawMessage{raw},
			Calls: []message.HostedCall{{ID: "image-1", Kind: message.HostedCallKindImageGeneration, Status: message.HostedCallStatusCompleted, Result: json.RawMessage(`{"images":[{"width":1024,"height":1024}]}`), Parts: []message.ContentPart{{Type: message.ContentPartImage, DataBytes: size}}}},
		}}
	}
	small, large := makeMessage(1024), makeMessage(10<<20)
	if smallTokens, largeTokens := EstimateMessageTokens(small), EstimateMessageTokens(large); smallTokens != largeTokens || smallTokens <= len(raw)/3 {
		t.Fatalf("native image tokens are missing or scaled by payload bytes: small=%d large=%d", smallTokens, largeTokens)
	}
	if EstimateMessagesBytes([]message.Message{small}) >= EstimateMessagesBytes([]message.Message{large}) {
		t.Fatal("byte budget ignores rehydrated native originals")
	}
	for _, msg := range []message.Message{small, large} {
		textBytes, tokens := messageEstimateBytesAndImages([]message.Message{msg})
		if textBytes != len(raw) || tokens != imagePartEstimateTokens {
			t.Fatalf("calibrated byte ratio includes image bytes or display mirrors: bytes=%d tokens=%d", textBytes, tokens)
		}
	}
}

func TestNativeImageAccountingUsesOnlyReplayedSuccessfulItems(t *testing.T) {
	part := message.ContentPart{Type: message.ContentPartImage, DataBytes: 1024}
	native := &message.NativeToolHistory{
		Items: []json.RawMessage{json.RawMessage(`{"type":"image_generation_call","id":"current","status":"completed"}`), json.RawMessage(`{"type":"image_generation_call","id":"failed","status":"failed"}`)},
		Calls: []message.HostedCall{
			{ID: "earlier", Kind: message.HostedCallKindImageGeneration, Parts: []message.ContentPart{part}},
			{ID: "current", Kind: message.HostedCallKindImageGeneration, Status: message.HostedCallStatusCompleted, Parts: []message.ContentPart{part}, Result: json.RawMessage(`{"images":[{"width":6000,"height":6000}]}`)},
			{ID: "failed", Kind: message.HostedCallKindImageGeneration, Status: message.HostedCallStatusFailed, Error: "generation failed"},
		},
	}
	bytes, tokens := nativeImageAccounting(native)
	if bytes != 1368 || tokens != 48000 {
		t.Fatalf("wrong surface or original allowance: bytes=%d tokens=%d", bytes, tokens)
	}
	native.Items = native.Items[1:]
	if bytes, tokens := nativeImageAccounting(native); bytes != 0 || tokens != 0 {
		t.Fatalf("failed item charged for an image: %d, %d", bytes, tokens)
	}
}
