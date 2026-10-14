package agent

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
)

// A tool result's image parts must survive the commit even when the current
// model cannot see them: the request wire strips unsupported modalities per
// target (filterUnsupportedBinaryPartsForTarget), so dropping them at commit
// time would lose the image for good — including after a later switch back to
// a vision model. The current model only decides whether the user is warned.
func TestToolResultImagePartsSurviveTextOnlyModelCommit(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	drainAgentEvents(a.Events())

	textOnly := llm.NewProviderConfig("text-only", config.ProviderConfig{
		Type: config.ProviderTypeMessages,
		Models: map[string]config.ModelConfig{
			"text-model": {Limit: config.ModelLimit{Context: 8192, Output: 1024}},
		},
	}, []string{"test-key"})
	a.llmClient = llm.NewClient(textOnly, stubProvider{}, "text-model", 1024, "")
	a.modelName = "text-model"

	image := message.ContentPart{Type: "image", MimeType: "image/png", Data: []byte("png-bytes"), FileName: "shot.png"}
	text := "Loaded image \"shot.png\" into context (100x50)."

	parts := a.toolResultParts(text, []message.ContentPart{image})
	if len(parts) != 2 || parts[0].Type != "text" || parts[1].Type != "image" || string(parts[1].Data) != "png-bytes" {
		t.Fatalf("tool result parts = %#v, want the image retained for a text-only model", parts)
	}

	toolMsg := a.buildToolResultMessage(&ToolResultPayload{CallID: "call-1", Name: "view_image"}, text, parts, false, nil)
	if len(toolMsg.Parts) != 2 || toolMsg.Parts[1].FileName != "shot.png" {
		t.Fatalf("durable tool message parts = %#v, want the image retained", toolMsg.Parts)
	}

	var warned bool
	for _, evt := range drainAgentEvents(a.Events()) {
		if toast, ok := evt.(ToastEvent); ok && strings.Contains(toast.Message, "tool-result attachments") {
			warned = true
		}
	}
	if !warned {
		t.Fatal("missing the warn toast telling the user this model will not see the image")
	}

	// A later switch to a vision model still replays the retained image, and
	// no new warning fires because the modality is supported again.
	vision := llm.NewProviderConfig("vision", config.ProviderConfig{
		Type: config.ProviderTypeResponses,
		Models: map[string]config.ModelConfig{
			"vision-model": {
				Limit:      config.ModelLimit{Context: 8192, Output: 1024},
				Modalities: &config.ModelModalities{Input: []string{"text", "image"}},
			},
		},
	}, []string{"test-key"})
	a.llmClient = llm.NewClient(vision, stubProvider{}, "vision-model", 1024, "")
	a.modelName = "vision-model"

	if parts := a.toolResultParts(text, []message.ContentPart{image}); len(parts) != 2 || parts[1].Type != "image" {
		t.Fatalf("tool result parts = %#v, want the image retained for a vision model", parts)
	}
	for _, evt := range drainAgentEvents(a.Events()) {
		if toast, ok := evt.(ToastEvent); ok && strings.Contains(toast.Message, "tool-result attachments") {
			t.Fatalf("unexpected unsupported-attachment toast for a vision model: %q", toast.Message)
		}
	}
}
