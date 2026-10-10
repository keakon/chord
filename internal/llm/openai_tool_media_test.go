package llm

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/modelcompat"
)

func TestChatToolMediaFollowsCompleteBatchWithoutChangingHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "image.png")
	if err := os.WriteFile(path, tinyPNG, 0600); err != nil {
		t.Fatal(err)
	}
	SetBinaryPartResolver(func(part message.ContentPart) ([]byte, string, error) {
		data, err := os.ReadFile(part.ImagePath)
		return data, "image/png", err
	})
	t.Cleanup(func() { SetBinaryPartResolver(nil) })
	for _, bridge := range []bool{false, true} {
		for _, tail := range []message.Role{"", message.RoleAssistant, message.RoleUser} {
			msgs := []message.Message{
				{Role: message.RoleUser, Content: "Create an illustration"},
				{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "a", Name: "generate_image", Args: json.RawMessage(`{}`)}, {ID: "b", Name: "read", Args: json.RawMessage(`{}`)}}},
				{Role: message.RoleTool, ToolCallID: "a", Content: "Saved image", Parts: []message.ContentPart{{Type: message.ContentPartImage, ArtifactID: "sha256-image", ImagePath: path}}},
				{Role: message.RoleTool, ToolCallID: "b", Content: "Read document", Parts: []message.ContentPart{{Type: message.ContentPartPDF, MimeType: "application/pdf", Data: []byte("%PDF-1.4"), FileName: "document.pdf"}}},
			}
			if tail != "" {
				msgs = append(msgs, message.Message{Role: tail, Content: "Continue"})
			}
			before, _ := json.Marshal(msgs)
			provider := NewProviderConfig("sample", config.ProviderConfig{Type: config.ProviderTypeChatCompletions, Models: map[string]config.ModelConfig{"vision": {Modalities: &config.ModelModalities{Input: []string{"text", "image", "pdf"}}}}}, []string{"key"})
			normalized, _ := normalizeMessagesForPoolTargetWithOptions(msgs, FallbackModel{ProviderConfig: provider, ModelID: "vision"}, RequestTuning{}, modelcompat.ReplayCompatNative)
			out := convertMessagesToOpenAIWithOptions("", modelcompat.WireFamilyOpenAIChat, modelcompat.ReasoningContinuityNone, normalized, openAIConvertOptions{requiresAssistantAfterToolResult: bridge})
			wantRoles := []string{"user", "assistant", "tool", "tool"}
			if bridge {
				wantRoles = append(wantRoles, "assistant")
			}
			mediaIndex := len(wantRoles)
			wantRoles = append(wantRoles, "user")
			if tail != "" {
				wantRoles = append(wantRoles, string(tail))
			}
			var roles []string
			for _, msg := range out {
				roles = append(roles, msg.Role)
			}
			if !reflect.DeepEqual(roles, wantRoles) {
				t.Fatalf("bridge=%t tail=%q roles=%v want=%v", bridge, tail, roles, wantRoles)
			}
			blocks := out[mediaIndex].Content.([]openAIContentBlock)
			if !out[mediaIndex].Transient || len(blocks) != 4 || !strings.Contains(blocks[0].Text, "a:") || blocks[1].ImageURL.URL != "data:image/png;base64,"+tinyPNGBase64 || !strings.Contains(blocks[2].Text, "b:") || blocks[3].File.Filename != "document.pdf" {
				t.Fatal("tool attachment association or payload was lost")
			}
			after, _ := json.Marshal(msgs)
			if string(before) != string(after) {
				t.Fatal("wire projection modified durable tool results")
			}
		}
	}
}

func TestChatToolMediaIsOmittedForTextOnlyTarget(t *testing.T) {
	provider := NewProviderConfig("sample", config.ProviderConfig{Type: config.ProviderTypeChatCompletions, Models: map[string]config.ModelConfig{"text": {}}}, []string{"key"})
	msgs := []message.Message{{Role: message.RoleTool, ToolCallID: "a", Content: "Saved image", Parts: []message.ContentPart{{Type: message.ContentPartImage, Data: tinyPNG}}}}
	filtered := filterUnsupportedBinaryPartsForTarget(msgs, FallbackModel{ProviderConfig: provider, ModelID: "text"})
	out := convertMessagesToOpenAIWithOptions("", modelcompat.WireFamilyOpenAIChat, modelcompat.ReasoningContinuityNone, filtered, openAIConvertOptions{})
	if len(out) != 1 || out[0].Content != "Saved image" || len(msgs[0].Parts) != 1 {
		t.Fatal("text-only projection changed text or durable images")
	}
}
