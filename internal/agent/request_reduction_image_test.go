package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/agent/requesttrim"
	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

// imageEvictionConfig enables the stale gate at age 2 with a single tool
// result, so tests reach the image rule without padding the history with
// filler tool calls.
func imageEvictionConfig() *config.Config {
	return &config.Config{Context: config.ContextConfig{Reduction: config.ContextReductionConfig{
		StaleAgeTurns:       2,
		MinToolResultsPrune: 1,
	}}}
}

func imageResultFixture(path string) message.ContentPart {
	return message.ContentPart{
		Type:      message.ContentPartImage,
		MimeType:  "image/png",
		ImagePath: path,
		DataBytes: 4096,
		FileName:  "shot.png",
	}
}

// imageEvictionMessages builds a user → assistant(view_image) → tool(result
// with text + image parts) exchange whose tool result carries the given
// request batch, plus trailing user messages so later requests have a tail.
func imageEvictionMessages(batch uint64, parts []message.ContentPart) []message.Message {
	return []message.Message{
		{Role: message.RoleUser, Content: "inspect the ui"},
		{Role: message.RoleAssistant, RequestBatch: batch, ToolCalls: []message.ToolCall{
			{ID: "img-call", Name: tools.NameViewImage, Args: json.RawMessage(`{"path":"shot.png"}`)},
		}},
		{Role: message.RoleTool, ToolCallID: "img-call", ToolStatus: message.ToolStatusSuccess,
			Content: "Loaded image \"shot.png\" into context (800x600).", Parts: parts},
	}
}

func newImageCapableTestLLMClient() *llm.Client {
	providerCfg := llm.NewProviderConfig("test", config.ProviderConfig{
		Type: config.ProviderTypeMessages,
		Models: map[string]config.ModelConfig{
			"test-model": {
				Limit:      config.ModelLimit{Context: 8192, Output: 1024},
				Modalities: &config.ModelModalities{Input: []string{"text", "image"}},
			},
		},
	}, []string{"test-key"})
	return llm.NewClient(providerCfg, stubProvider{}, "test-model", 1024, "")
}

func hasImagePart(parts []message.ContentPart) bool {
	for i := range parts {
		if parts[i].Type == message.ContentPartImage {
			return true
		}
	}
	return false
}

// TestPrepareMessagesForLLM_EvictsOldToolResultImages pins the rule: a tool
// result's image parts are replaced by a bracketed marker text part once the
// result ages past the stale threshold, the string content stays untouched,
// and the durable input message is never mutated. Without view_image the
// marker says the image cannot be re-loaded.
func TestPrepareMessagesForLLM_EvictsOldToolResultImages(t *testing.T) {
	a := &MainAgent{projectConfig: imageEvictionConfig()}
	originalParts := []message.ContentPart{
		{Type: message.ContentPartText, Text: "Loaded image \"shot.png\" into context (800x600)."},
		imageResultFixture("/tmp/session/images/shot.png"),
	}
	msgs := imageEvictionMessages(1, originalParts)
	setTestRequestBatch(a, nil, 3)

	prepared := a.prepareMessagesForLLM(msgs)
	evicted := prepared[2]
	if hasImagePart(evicted.Parts) {
		t.Fatalf("stale image part survived eviction: %+v", evicted.Parts)
	}
	if len(evicted.Parts) != 2 || evicted.Parts[0].Type != message.ContentPartText || evicted.Parts[1].Type != message.ContentPartText {
		t.Fatalf("evicted parts = %+v, want [text content, marker text]", evicted.Parts)
	}
	if evicted.Content != msgs[2].Content {
		t.Fatalf("image eviction must not rewrite Content: %q", evicted.Content)
	}
	marker := evicted.Parts[1].Text
	if !strings.Contains(marker, "[Older image output omitted from this request to save context.") {
		t.Fatalf("marker missing base wording: %q", marker)
	}
	if !strings.Contains(marker, "not re-loadable in this session") {
		t.Fatalf("marker without view_image must state irrecoverability: %q", marker)
	}
	if strings.Contains(marker, "shot.png") {
		t.Fatalf("unrecoverable marker must not carry an address: %q", marker)
	}

	// The durable input message keeps its parts: eviction is request-face only.
	if !hasImagePart(msgs[2].Parts) || msgs[2].Parts[1].ImagePath != "/tmp/session/images/shot.png" {
		t.Fatalf("original message parts mutated: %+v", msgs[2].Parts)
	}

	stats := a.GetContextReductionStats()
	bucket, ok := stats.ByToolAndRule[tools.NameViewImage+"/"+string(requesttrim.ImageOutput)]
	if !ok || bucket.Messages != 1 || bucket.TokensSaved <= 0 {
		t.Fatalf("image eviction bucket missing or empty: %+v", stats.ByToolAndRule)
	}
	before := message.EstimateMessageTokens(msgs[2])
	after := message.EstimateMessageTokens(evicted)
	if saved := before - after; saved < message.ImagePartEstimateTokens/2 {
		t.Fatalf("request-face estimate dropped only %d tokens (before=%d after=%d)", saved, before, after)
	}
}

// TestPrepareMessagesForLLM_ImageEvictionRecoveryAddress pins the recall branch:
// with view_image exposed to the agent, the marker carries the persisted path
// and the re-load route instead of the irrecoverability notice.
func TestPrepareMessagesForLLM_ImageEvictionRecoveryAddress(t *testing.T) {
	a := newReductionTestMainAgent(t, t.TempDir())
	a.llmClient = newImageCapableTestLLMClient()
	a.tools.Register(tools.NewViewImageTool(a))
	a.projectConfig = imageEvictionConfig()
	msgs := imageEvictionMessages(1, []message.ContentPart{
		{Type: message.ContentPartText, Text: "Loaded image \"shot.png\" into context (800x600)."},
		imageResultFixture("/tmp/session/images/shot.png"),
	})
	setTestRequestBatch(a, nil, 3)

	prepared := a.prepareMessagesForLLM(msgs)
	if hasImagePart(prepared[2].Parts) {
		t.Fatal("stale image part survived eviction with view_image available")
	}
	marker := prepared[2].Parts[1].Text
	if !strings.Contains(marker, "/tmp/session/images/shot.png") || !strings.Contains(marker, "view_image") {
		t.Fatalf("recoverable marker missing address or route: %q", marker)
	}
	if strings.Contains(marker, "not re-loadable") {
		t.Fatalf("recoverable marker must not claim irrecoverability: %q", marker)
	}
}

// TestPrepareMessagesForLLM_ImageEvictionKeepsPDFAndOrder pins the parts
// surgery: only image parts go, PDFs stay, and the marker lands where the
// first image stood with plural wording for several images.
func TestPrepareMessagesForLLM_ImageEvictionKeepsPDFAndOrder(t *testing.T) {
	a := &MainAgent{projectConfig: imageEvictionConfig()}
	parts := []message.ContentPart{
		{Type: message.ContentPartText, Text: "Loaded two images."},
		imageResultFixture("/tmp/session/images/a.png"),
		{Type: message.ContentPartPDF, MimeType: "application/pdf", ImagePath: "/tmp/session/images/doc.pdf", DataBytes: 2048},
		imageResultFixture("/tmp/session/images/b.png"),
	}
	msgs := imageEvictionMessages(1, parts)
	setTestRequestBatch(a, nil, 3)

	prepared := a.prepareMessagesForLLM(msgs)
	evicted := prepared[2].Parts
	if hasImagePart(evicted) {
		t.Fatalf("image parts survived: %+v", evicted)
	}
	if len(evicted) != 3 || evicted[1].Type != message.ContentPartText || evicted[2].Type != message.ContentPartPDF {
		t.Fatalf("evicted parts = %+v, want [text, marker, pdf] with the pdf kept in place", evicted)
	}
	if !strings.Contains(evicted[1].Text, "[Older image outputs omitted") {
		t.Fatalf("plural marker wording missing: %q", evicted[1].Text)
	}
	if !strings.Contains(evicted[1].Text, "They are not re-loadable") {
		t.Fatalf("plural irrecoverability wording missing: %q", evicted[1].Text)
	}
}

// TestPrepareMessagesForLLM_ImageEvictionStablePrefixReview pins the age
// contract: the review that guards the stable prefix (a RoleTool gate)
// protects the image result until the stale frontier and must actually evict
// the parts when the frontier is crossed.
func TestPrepareMessagesForLLM_ImageEvictionStablePrefixReview(t *testing.T) {
	a := newReductionTestMainAgent(t, t.TempDir())
	a.llmClient = newImageCapableTestLLMClient()
	a.tools.Register(tools.NewViewImageTool(a))
	a.projectConfig = imageEvictionConfig()
	msgs := imageEvictionMessages(1, []message.ContentPart{
		{Type: message.ContentPartText, Text: "Loaded image \"shot.png\" into context (800x600)."},
		imageResultFixture("/tmp/session/images/shot.png"),
	})
	setTestRequestBatch(a, nil, 1)
	turnCtx, turnCancel := context.WithCancel(context.Background())
	t.Cleanup(turnCancel)
	a.turn = &Turn{ID: 1, Ctx: turnCtx, Cancel: turnCancel}

	first := a.prepareMessagesForLLM(msgs)
	if !hasImagePart(first[2].Parts) {
		t.Fatal("fresh image part evicted before the stale frontier")
	}

	setTestRequestBatch(a, nil, 2)
	second := a.prepareMessagesForLLM(msgs)
	if !hasImagePart(second[2].Parts) {
		t.Fatal("age-1 image part evicted before the stale frontier")
	}
	if a.GetContextReductionStats().SkippedByReason[contextReductionSkipDeferredReview] == 0 {
		t.Fatal("image result did not ride the deferred-review frontier")
	}

	setTestRequestBatch(a, nil, 3)
	third := a.prepareMessagesForLLM(msgs)
	if hasImagePart(third[2].Parts) {
		t.Fatal("stale image part not evicted when the frontier was crossed")
	}
	if !strings.Contains(third[2].Parts[1].Text, "view_image") {
		t.Fatalf("test agent exposes view_image, marker must carry the recall route: %q", third[2].Parts[1].Text)
	}
}

// TestPrepareMessagesForLLM_ImageEvictionAmortizesBoundaryFlush pins the
// accounting fix with parallel scenarios over the same cached tail: a single
// image inside the previously sent prefix defers (its savings do not amortize
// the rewrite), three images accumulate their planning allowance and flush —
// under Content-only accounting both would score zero and always defer.
func TestPrepareMessagesForLLM_ImageEvictionAmortizesBoundaryFlush(t *testing.T) {
	run := func(t *testing.T, imageCount int, wantFlush bool) {
		t.Helper()
		a := newReductionTestMainAgent(t, t.TempDir())
		a.projectConfig = &config.Config{
			Context: config.ContextConfig{Reduction: config.ContextReductionConfig{
				StaleAgeTurns:        2,
				MinToolResultsPrune:  1,
				MinIncrementalTokens: 1 << 20,
			}},
		}
		a.runningModelRef = "p/m"
		a.recordLLMModelRun("p/m")
		a.recordLLMModelRun("p/m")
		turnCtx, turnCancel := context.WithCancel(context.Background())
		t.Cleanup(turnCancel)
		a.turn = &Turn{ID: 1, Ctx: turnCtx, Cancel: turnCancel}

		parts := []message.ContentPart{{Type: message.ContentPartText, Text: "Loaded image into context."}}
		for i := range imageCount {
			parts = append(parts, imageResultFixture("/tmp/session/images/shot"+string(rune('a'+i))+".png"))
		}
		msgs := imageEvictionMessages(1, parts)
		msgs = append(msgs,
			message.Message{Role: message.RoleAssistant, Content: strings.Repeat("stable assistant filler ", 3000)},
			message.Message{Role: message.RoleUser, Content: "u2"},
		)
		setTestRequestBatch(a, msgs, 1)
		if prepared := a.prepareMessagesForLLM(msgs); !hasImagePart(prepared[2].Parts) {
			t.Fatal("fresh image evicted before establishing the cache")
		}

		setTestRequestBatch(a, nil, 2)
		second := a.prepareMessagesForLLM(msgs)
		if !hasImagePart(second[2].Parts) {
			t.Fatal("age-1 image evicted before the stale frontier")
		}

		setTestRequestBatch(a, nil, 3)
		third := a.prepareMessagesForLLM(msgs)
		if flushed := !hasImagePart(third[2].Parts); flushed != wantFlush {
			t.Fatalf("%d images: flushed=%v, want %v", imageCount, flushed, wantFlush)
		}
		if !wantFlush {
			if a.GetContextReductionStats().SkippedByReason[contextReductionSkipDeferredCache] == 0 {
				t.Fatal("single image deferral did not record the cache-deferred skip")
			}
			return
		}
		stats := a.GetContextReductionStats()
		bucket, ok := stats.ByToolAndRule[tools.NameViewImage+"/"+string(requesttrim.ImageOutput)]
		if !ok || bucket.Messages != 1 {
			t.Fatalf("flushed eviction bucket missing: %+v", stats.ByToolAndRule)
		}

		setTestRequestBatch(a, nil, 4)
		fourth := a.prepareMessagesForLLM(msgs)
		if hasImagePart(fourth[2].Parts) {
			t.Fatal("flushed eviction did not freeze into the stable surface")
		}
		if marker := fourth[2].Parts[len(fourth[2].Parts)-1].Text; marker != third[2].Parts[len(third[2].Parts)-1].Text {
			t.Fatal("frozen marker drifted between requests, breaking cache reuse")
		}
	}
	t.Run("single image defers", func(t *testing.T) { run(t, 1, false) })
	t.Run("three images flush", func(t *testing.T) { run(t, 3, true) })
}

// TestPrepareMessagesForLLM_ImageEvictionUntouchedSurfaces pins the
// exclusions: user-message images, PDF-only results and native replay messages
// keep exactly the surface they had before.
func TestPrepareMessagesForLLM_ImageEvictionUntouchedSurfaces(t *testing.T) {
	a := &MainAgent{projectConfig: imageEvictionConfig()}
	userImageMsg := message.Message{
		Role: message.RoleUser, Content: "see attachment",
		Parts: []message.ContentPart{imageResultFixture("/tmp/session/images/upload.png")},
	}
	pdfToolMsg := message.Message{
		Role: message.RoleTool, ToolCallID: "pdf-call", ToolStatus: message.ToolStatusSuccess, Content: "doc",
		Parts: []message.ContentPart{{Type: message.ContentPartPDF, MimeType: "application/pdf", ImagePath: "/tmp/session/images/doc.pdf", DataBytes: 2048}},
	}
	nativeMsg := message.Message{
		Role: message.RoleTool, ToolCallID: "native-call", ToolStatus: message.ToolStatusSuccess, Content: "generated",
		NativeTools: &message.NativeToolHistory{},
		Parts:       []message.ContentPart{imageResultFixture("/tmp/session/images/gen.png")},
	}
	msgs := []message.Message{
		{Role: message.RoleUser, Content: "u1"},
		{Role: message.RoleAssistant, RequestBatch: 1, ToolCalls: []message.ToolCall{
			{ID: "native-call", Name: tools.NameGenerateImage, Args: json.RawMessage(`{"prompt":"art"}`)},
		}},
		nativeMsg,
		userImageMsg,
		{Role: message.RoleAssistant, RequestBatch: 1, ToolCalls: []message.ToolCall{
			{ID: "pdf-call", Name: tools.NameWebFetch, Args: json.RawMessage(`{"url":"https://example.invalid/doc.pdf"}`)},
		}},
		pdfToolMsg,
	}
	setTestRequestBatch(a, nil, 3)

	prepared := a.prepareMessagesForLLM(msgs)
	if !hasImagePart(prepared[3].Parts) {
		t.Fatal("user-message image was evicted")
	}
	if len(prepared[5].Parts) != 1 || prepared[5].Parts[0].Type != message.ContentPartPDF {
		t.Fatalf("pdf part did not survive untouched: %+v", prepared[5].Parts)
	}
	if !hasImagePart(prepared[2].Parts) {
		t.Fatal("native replay message image was evicted")
	}
}

// TestPrepareMessagesForLLM_StringReductionSyncsPartsText pins the apply-pass
// contract for parts-bearing results: when a string rule and the image rule
// claim the same message, the wire-visible text part carries the reduced
// content and the image becomes the marker.
func TestPrepareMessagesForLLM_StringReductionSyncsPartsText(t *testing.T) {
	a := &MainAgent{projectConfig: &config.Config{Context: config.ContextConfig{Reduction: config.ContextReductionConfig{
		StaleAgeTurns:       2,
		MinToolResultsPrune: 1,
		StaleOutputBytes:    100,
	}}}}
	parts := []message.ContentPart{
		{Type: message.ContentPartText, Text: strings.Repeat("stale tool output line\n", 80)},
		imageResultFixture("/tmp/session/images/shot.png"),
	}
	msgs := imageEvictionMessages(1, parts)
	msgs[2].Content = strings.Repeat("stale tool output line\n", 80)
	setTestRequestBatch(a, nil, 3)

	prepared := a.prepareMessagesForLLM(msgs)
	reduced := prepared[2]
	if reduced.Content == msgs[2].Content {
		t.Fatal("stale string content was not reduced")
	}
	if hasImagePart(reduced.Parts) {
		t.Fatal("image part was not evicted alongside the string reduction")
	}
	if reduced.Parts[0].Text != reduced.Content {
		t.Fatalf("wire text part not synced to the reduced content: %q vs %q", reduced.Parts[0].Text, reduced.Content)
	}
	if !strings.Contains(reduced.Parts[1].Text, "[Older image output omitted") {
		t.Fatalf("image marker missing: %q", reduced.Parts[1].Text)
	}
}
