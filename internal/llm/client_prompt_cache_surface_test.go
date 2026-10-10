package llm

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
)

func TestResponsesCacheDiagnosticMatchesMultimodalWireEndpoint(t *testing.T) {
	cfg := NewProviderConfig("sample", config.ProviderConfig{Type: config.ProviderTypeResponses, Models: map[string]config.ModelConfig{"gpt-6.1-sol": {}}}, nil)
	client := NewClient(cfg, nil, "gpt-6.1-sol", 4096, "")
	defer client.Close()
	text := message.ContentPart{Type: message.ContentPartText, Text: "Sample text"}
	image := message.ContentPart{Type: message.ContentPartImage, MimeType: "image/png", Data: tinyPNG}
	pdf := message.ContentPart{Type: message.ContentPartPDF, MimeType: "application/pdf", Data: []byte("%PDF-sample")}
	for _, role := range []message.Role{message.RoleUser, message.RoleTool} {
		for _, tc := range []struct {
			name      string
			parts     []message.ContentPart
			wantParts int
		}{
			{"text-image", []message.ContentPart{text, image}, 1},
			{"text-pdf", []message.ContentPart{text, pdf}, 1},
			{"image-text", []message.ContentPart{image, text}, 2},
			{"text-image-text", []message.ContentPart{text, image, text}, 3},
			{"text-many-attachments", []message.ContentPart{text, image, pdf}, 1},
			{"empty-tail", []message.ContentPart{text, {Type: message.ContentPartText}}, 1},
			{"image-only", []message.ContentPart{image}, -1},
			{"empty", []message.ContentPart{{Type: message.ContentPartText}}, 0},
			{"invalid-attachment", []message.ContentPart{{Type: message.ContentPartImage}}, 0},
		} {
			t.Run(string(role)+"/"+tc.name, func(t *testing.T) {
				history := []message.Message{
					{Role: message.RoleUser, Kind: message.KindTurnOverlay, Content: "snapshot"},
					{Role: role, Content: "Fallback text", Parts: tc.parts},
					{Role: message.RoleAssistant, Content: "Sample response"},
					{Role: message.RoleUser, Kind: message.KindTurnOverlay, Content: "Sample hint"},
				}
				if role == message.RoleTool {
					history[1].ToolCallID = "call-1"
				}
				before, _ := json.Marshal(history)
				projection := client.PromptCacheMessagesForModelRef("sample/gpt-6.1-sol", history, 1)
				wire := convertMessagesToResponsesWithItemIDs("", history, false, false, true)
				content := wire[1].Content
				if role == message.RoleTool {
					content = wire[1].Output
				}
				blocks := content.([]responsesContentBlock)
				endpoint := -1
				for i, block := range blocks {
					if block.PromptCacheBreakpoint != nil {
						endpoint = i
					}
				}
				if tc.wantParts < 0 {
					if len(projection) != 0 || endpoint >= 0 {
						t.Fatal("attachment-only input has no explicit text endpoint")
					}
					return
				}
				if len(projection) != 2 || len(projection[1].Parts) != tc.wantParts || endpoint < 0 {
					t.Fatalf("projection=%+v endpoint=%d", projection, endpoint)
				}
				projectedWire := convertMessagesToResponsesWithItemIDs("", projection, false, false, true)
				projectedContent := projectedWire[1].Content
				if role == message.RoleTool {
					projectedContent = projectedWire[1].Output
				}
				if !reflect.DeepEqual(projectedContent, blocks[:endpoint+1]) {
					t.Fatalf("diagnostic surface differs from marked wire prefix: %+v vs %+v", projectedContent, blocks[:endpoint+1])
				}
				after, _ := json.Marshal(history)
				if string(before) != string(after) {
					t.Fatal("cache projection mutated canonical history")
				}
			})
		}
	}
}

func TestAnthropicCacheDiagnosticExcludesFoldedReminderResult(t *testing.T) {
	cfg := NewProviderConfig("sample", config.ProviderConfig{Type: config.ProviderTypeMessages, Models: map[string]config.ModelConfig{"test-model": {PromptCache: &config.PromptCacheConfig{Mode: "explicit"}}}}, nil)
	impl, err := NewAnthropicProvider(cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	client := NewClient(cfg, impl, "test-model", 4096, "")
	defer client.Close()
	history := pendingAnthropicNativeHistory()
	if got := client.PromptCacheMessagesForModelRef("sample/test-model", history, 0); len(got) != 3 {
		t.Fatalf("durable result excluded without reminders: %d", len(got))
	}
	history = append(history, message.Message{Role: message.RoleUser, Kind: message.KindTurnOverlay, Content: "Sample hint"})
	if got := client.PromptCacheMessagesForModelRef("sample/test-model", history, 1); len(got) != 2 {
		t.Fatalf("transient tool result included in cache diagnostics: %d", len(got))
	}
}
