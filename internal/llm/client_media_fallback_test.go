package llm

import (
	"context"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
)

func TestPrimarySupportsViewImageToolUsesFirstPoolModel(t *testing.T) {
	responsesCfg := NewProviderConfig("responses", config.ProviderConfig{
		Type: config.ProviderTypeResponses,
		Models: map[string]config.ModelConfig{
			"vision": {Modalities: &config.ModelModalities{Input: []string{"text", "image"}}},
		},
	}, []string{"key"})
	chatCfg := NewProviderConfig("chat", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"vision": {Modalities: &config.ModelModalities{Input: []string{"text", "image"}}},
		},
	}, []string{"key"})

	client := NewClient(chatCfg, &scriptedProvider{}, "vision", 1024, "")
	client.SetModelPool([]FallbackModel{
		{ProviderConfig: responsesCfg, ProviderImpl: &scriptedProvider{}, ModelID: "vision", MaxTokens: 1024},
		{ProviderConfig: chatCfg, ProviderImpl: &scriptedProvider{}, ModelID: "vision", MaxTokens: 1024},
	}, 1)
	if !client.PrimarySupportsViewImageTool() {
		t.Fatal("PrimarySupportsViewImageTool() = false, want true from first model-pool entry")
	}

	client.SetModelPool([]FallbackModel{
		{ProviderConfig: chatCfg, ProviderImpl: &scriptedProvider{}, ModelID: "vision", MaxTokens: 1024},
		{ProviderConfig: responsesCfg, ProviderImpl: &scriptedProvider{}, ModelID: "vision", MaxTokens: 1024},
	}, 1)
	if !client.PrimarySupportsViewImageTool() {
		t.Fatal("PrimarySupportsViewImageTool() = false for an image-capable Chat Completions primary")
	}

	textOnlyCfg := NewProviderConfig("text-only", config.ProviderConfig{
		Type: config.ProviderTypeResponses,
		Models: map[string]config.ModelConfig{
			"text": {},
		},
	}, []string{"key"})
	client.SetModelPool([]FallbackModel{{ProviderConfig: textOnlyCfg, ProviderImpl: &scriptedProvider{}, ModelID: "text", MaxTokens: 1024}}, 0)
	if client.PrimarySupportsViewImageTool() {
		t.Fatal("PrimarySupportsViewImageTool() = true, want false when primary model defaults to text-only input")
	}
}

func TestCompleteStreamDropsUnsupportedImageToolResultPartsForFallback(t *testing.T) {
	responsesCfg := NewProviderConfig("responses", config.ProviderConfig{
		Type: config.ProviderTypeResponses,
		Models: map[string]config.ModelConfig{
			"vision": {Modalities: &config.ModelModalities{Input: []string{"text", "image"}}},
		},
	}, []string{"key"})
	textCfg := NewProviderConfig("text", config.ProviderConfig{
		Type: config.ProviderTypeResponses,
		Models: map[string]config.ModelConfig{
			"text": {Modalities: &config.ModelModalities{Input: []string{"text"}}},
		},
	}, []string{"key"})
	startProvider := &scriptedProvider{calls: []scriptedCall{{err: &APIError{StatusCode: 502, Message: "bad gateway"}}}}
	textProvider := &rejectBinaryPartsProvider{resp: &message.Response{Content: "fallback ok"}}
	client := NewClient(responsesCfg, startProvider, "vision", 1024, "")
	messages := []message.Message{{
		Role:       "tool",
		ToolCallID: "call_1",
		Content:    "Loaded image",
		Parts: []message.ContentPart{
			{Type: "text", Text: "Loaded image"},
			{Type: "image", MimeType: "image/png", Data: tinyPNG},
		},
	}}

	_, err := callCompleteStreamWithRetryForTest(
		client,
		context.Background(),
		responsesCfg,
		startProvider,
		"vision",
		1024,
		RequestTuning{},
		"",
		messages,
		nil,
		nil,
		true,
		[]FallbackModel{{ProviderConfig: textCfg, ProviderImpl: textProvider, ModelID: "text", MaxTokens: 1024}},
		1,
		&CallStatus{},
	)
	if err != nil {
		t.Fatalf("CompleteStream error = %v, want nil", err)
	}
	if got := textProvider.CallCount(); got != 1 {
		t.Fatalf("text fallback calls = %d, want 1", got)
	}
	replayed := textProvider.LastMessages()
	if len(replayed) != 1 || len(replayed[0].Parts) != 1 || replayed[0].Parts[0].Type != message.ContentPartText {
		t.Fatalf("fallback messages = %#v, want text-only tool result", replayed)
	}
}

func TestCompleteStreamDropsUnsupportedUserImagePartsForFallback(t *testing.T) {
	responsesCfg := NewProviderConfig("responses", config.ProviderConfig{
		Type: config.ProviderTypeResponses,
		Models: map[string]config.ModelConfig{
			"vision": {Modalities: &config.ModelModalities{Input: []string{"text", "image"}}},
		},
	}, []string{"key"})
	textCfg := NewProviderConfig("text", config.ProviderConfig{
		Type: config.ProviderTypeResponses,
		Models: map[string]config.ModelConfig{
			"text": {Modalities: &config.ModelModalities{Input: []string{"text"}}},
		},
	}, []string{"key"})
	startProvider := &scriptedProvider{calls: []scriptedCall{{err: &APIError{StatusCode: 502, Message: "bad gateway"}}}}
	textProvider := &rejectBinaryPartsProvider{resp: &message.Response{Content: "fallback ok"}}
	client := NewClient(responsesCfg, startProvider, "vision", 1024, "")
	messages := []message.Message{{
		Role: "user",
		Parts: []message.ContentPart{
			{Type: "text", Text: "what is in this image?"},
			{Type: "image", MimeType: "image/png", Data: tinyPNG},
		},
	}}

	_, err := callCompleteStreamWithRetryForTest(
		client,
		context.Background(),
		responsesCfg,
		startProvider,
		"vision",
		1024,
		RequestTuning{},
		"",
		messages,
		nil,
		nil,
		true,
		[]FallbackModel{{ProviderConfig: textCfg, ProviderImpl: textProvider, ModelID: "text", MaxTokens: 1024}},
		1,
		&CallStatus{},
	)
	if err != nil {
		t.Fatalf("CompleteStream error = %v, want nil", err)
	}
	if got := textProvider.CallCount(); got != 1 {
		t.Fatalf("text fallback calls = %d, want 1", got)
	}
	replayed := textProvider.LastMessages()
	if len(replayed) != 1 || len(replayed[0].Parts) != 1 || replayed[0].Parts[0].Type != message.ContentPartText {
		t.Fatalf("fallback messages = %#v, want text-only user message", replayed)
	}
}

func TestCompleteStreamDropsUnsupportedBinaryPartsForFallbackWithMissingModelConfig(t *testing.T) {
	responsesCfg := NewProviderConfig("responses", config.ProviderConfig{
		Type: config.ProviderTypeResponses,
		Models: map[string]config.ModelConfig{
			"vision": {Modalities: &config.ModelModalities{Input: []string{"text", "image"}}},
		},
	}, []string{"key"})
	unknownModelCfg := NewProviderConfig("unknown-model", config.ProviderConfig{
		Type:   config.ProviderTypeResponses,
		Models: map[string]config.ModelConfig{},
	}, []string{"key"})
	startProvider := &scriptedProvider{calls: []scriptedCall{{err: &APIError{StatusCode: 502, Message: "bad gateway"}}}}
	textProvider := &rejectBinaryPartsProvider{resp: &message.Response{Content: "fallback ok"}}
	client := NewClient(responsesCfg, startProvider, "vision", 1024, "")
	messages := []message.Message{{
		Role: "user",
		Parts: []message.ContentPart{
			{Type: message.ContentPartText, Text: "what is in this image?"},
			{Type: message.ContentPartImage, MimeType: "image/png", Data: tinyPNG},
		},
	}}

	_, err := callCompleteStreamWithRetryForTest(
		client,
		context.Background(),
		responsesCfg,
		startProvider,
		"vision",
		1024,
		RequestTuning{},
		"",
		messages,
		nil,
		nil,
		true,
		[]FallbackModel{{ProviderConfig: unknownModelCfg, ProviderImpl: textProvider, ModelID: "not-configured", MaxTokens: 1024}},
		1,
		&CallStatus{},
	)
	if err != nil {
		t.Fatalf("CompleteStream error = %v, want nil", err)
	}
	if got := textProvider.CallCount(); got != 1 {
		t.Fatalf("fallback calls = %d, want 1", got)
	}
	replayed := textProvider.LastMessages()
	if len(replayed) != 1 || len(replayed[0].Parts) != 1 || replayed[0].Parts[0].Type != message.ContentPartText {
		t.Fatalf("fallback messages = %#v, want text-only user message for missing model config", replayed)
	}
}

func TestCompleteStreamDropsUnsupportedPDFToolResultPartsForFallback(t *testing.T) {
	responsesCfg := NewProviderConfig("responses", config.ProviderConfig{
		Type: config.ProviderTypeResponses,
		Models: map[string]config.ModelConfig{
			"document": {Modalities: &config.ModelModalities{Input: []string{"text", "pdf"}}},
		},
	}, []string{"key"})
	imageOnlyCfg := NewProviderConfig("image-only", config.ProviderConfig{
		Type: config.ProviderTypeResponses,
		Models: map[string]config.ModelConfig{
			"vision": {Modalities: &config.ModelModalities{Input: []string{"text", "image"}}},
		},
	}, []string{"key"})
	startProvider := &scriptedProvider{calls: []scriptedCall{{err: &APIError{StatusCode: 502, Message: "bad gateway"}}}}
	imageOnlyProvider := &rejectBinaryPartsProvider{resp: &message.Response{Content: "fallback ok"}}
	client := NewClient(responsesCfg, startProvider, "document", 1024, "")
	messages := []message.Message{{
		Role:       "tool",
		ToolCallID: "call_1",
		Content:    "Loaded PDF",
		Parts: []message.ContentPart{
			{Type: "text", Text: "Loaded PDF"},
			{Type: "pdf", MimeType: "application/pdf", Data: []byte("%PDF-1.7")},
		},
	}}

	_, err := callCompleteStreamWithRetryForTest(
		client,
		context.Background(),
		responsesCfg,
		startProvider,
		"document",
		1024,
		RequestTuning{},
		"",
		messages,
		nil,
		nil,
		true,
		[]FallbackModel{{ProviderConfig: imageOnlyCfg, ProviderImpl: imageOnlyProvider, ModelID: "vision", MaxTokens: 1024}},
		1,
		&CallStatus{},
	)
	if err != nil {
		t.Fatalf("CompleteStream error = %v, want nil", err)
	}
	if got := imageOnlyProvider.CallCount(); got != 1 {
		t.Fatalf("image-only fallback calls = %d, want 1", got)
	}
	replayed := imageOnlyProvider.LastMessages()
	if len(replayed) != 1 || len(replayed[0].Parts) != 1 || replayed[0].Parts[0].Type != message.ContentPartText {
		t.Fatalf("fallback messages = %#v, want text-only tool result", replayed)
	}
}
