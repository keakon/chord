package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/agent/requesttrim"
	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

func newImageReductionTestAgent(t *testing.T) *MainAgent {
	t.Helper()
	a := newReductionTestMainAgent(t, t.TempDir())
	a.llmClient = newImageCapableTestLLMClient()
	a.projectConfig = imageEvictionConfig()
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	a.turn = &Turn{ID: 1, Ctx: ctx, Cancel: cancel}
	a.tools.Register(tools.NewViewImageTool(a))
	return a
}

func TestImageEvictionAfterFrozenTextReduction(t *testing.T) {
	a := newImageReductionTestAgent(t)
	a.projectConfig.Context.Reduction.StaleAgeTurns = 3
	content := "Loaded image \"" + strings.Repeat("sample-", 20) + ".png\" into context (800x600)."
	msgs := imageEvictionMessages(1, toolResultParts(content, []message.ContentPart{imageResultFixture("/tmp/session/images/shot.png")}))
	msgs[2].Content = content
	later := imageEvictionMessages(2, msgs[2].Parts)
	later[1].ToolCalls[0].ID = "img-call-2"
	later[2].ToolCallID = "img-call-2"
	later[2].Content = content
	msgs = append(msgs, later[1:]...)
	setTestRequestBatch(a, nil, 2)
	first := a.prepareMessagesForLLM(msgs)
	if first[2].Content == content || !hasImagePart(first[2].Parts) {
		t.Fatalf("expected text-only reduction before image age: %+v", first[2])
	}
	for _, batch := range []uint64{4, 10} {
		setTestRequestBatch(a, nil, batch)
		prepared := a.prepareMessagesForLLM(msgs)
		if hasImagePart(prepared[2].Parts) {
			t.Fatalf("batch %d: frozen text prevented image eviction", batch)
		}
		if prepared[2].Content != first[2].Content {
			t.Fatalf("batch %d: image eviction rewrote the frozen text", batch)
		}
	}
	if !hasImagePart(msgs[2].Parts) || msgs[2].Content != content {
		t.Fatal("reduction changed durable input")
	}
}

func TestTextReductionAfterFrozenImageEviction(t *testing.T) {
	a := newImageReductionTestAgent(t)
	a.projectConfig.Context.Reduction.ReadLikeAgeTurns = 4
	a.projectConfig.Context.Reduction.ReadLikeOutputBytes = 100
	a.projectConfig.Context.Reduction.StaleOutputBytes = 1 << 20
	content := strings.Repeat("Sample page content.\n", 400)
	msgs := imageEvictionMessages(1, toolResultParts(content, []message.ContentPart{imageResultFixture("/tmp/session/images/shot.png")}))
	msgs[1].ToolCalls[0].Name = tools.NameWebFetch
	msgs[2].Content = content
	setTestRequestBatch(a, nil, 3)
	first := a.prepareMessagesForLLM(msgs)
	if hasImagePart(first[2].Parts) || first[2].Content != content {
		t.Fatal("expected image-only reduction before the text age")
	}
	setTestRequestBatch(a, nil, 5)
	second := a.prepareMessagesForLLM(msgs)
	if second[2].Content == content || hasImagePart(second[2].Parts) {
		t.Fatal("image-only reduction prevented later text reduction")
	}
	if second[2].Parts[1].Text != first[2].Parts[1].Text {
		t.Fatal("text reduction changed the frozen image marker")
	}
}

func TestImageEvictionHonorsToolVisibility(t *testing.T) {
	for _, registered := range []bool{false, true} {
		t.Run(map[bool]string{false: "unregistered", true: "denied"}[registered], func(t *testing.T) {
			a := newImageReductionTestAgent(t)
			if registered {
				a.ruleset = permission.Ruleset{{Permission: tools.NameViewImage, Pattern: "*", Action: permission.ActionDeny}}
			} else {
				a.tools = tools.NewRegistry()
			}
			if a.MainToolVisible(tools.NameViewImage) || !a.SupportsViewImageTool() {
				t.Fatal("expected a vision model without a visible view_image tool")
			}
			msgs := imageEvictionMessages(1, toolResultParts("Loaded image.", []message.ContentPart{imageResultFixture("/tmp/session/images/shot.png")}))
			setTestRequestBatch(a, nil, 3)
			marker := a.prepareMessagesForLLM(msgs)[2].Parts[1].Text
			if strings.Contains(marker, tools.NameViewImage) || !strings.Contains(marker, "not re-loadable") {
				t.Fatalf("marker must respect the actual tool surface: %s", marker)
			}
		})
	}
}

func TestImageReductionSavingsAreDisjoint(t *testing.T) {
	for _, combined := range []bool{false, true} {
		t.Run(map[bool]string{false: "image only", true: "text and image"}[combined], func(t *testing.T) {
			a := newImageReductionTestAgent(t)
			a.projectConfig.Context.Reduction.StaleOutputBytes = 100
			content := "Loaded image into context."
			if combined {
				content = strings.Repeat("stale tool output line\n", 80)
			}
			msgs := imageEvictionMessages(1, toolResultParts(content, []message.ContentPart{imageResultFixture("/tmp/session/images/shot.png")}))
			msgs[2].Content = content
			setTestRequestBatch(a, nil, 3)
			prepared := a.prepareMessagesForLLM(msgs)
			stats := a.GetContextReductionStats()
			actual := stats.TokensBefore - stats.TokensAfter
			sum := 0
			for _, bucket := range stats.ByToolAndRule {
				sum += bucket.TokensSaved
			}
			if stats.TokensSaved != actual || sum != actual {
				t.Fatalf("savings overlap: global=%d buckets=%d before-after=%d", stats.TokensSaved, sum, actual)
			}
			if stats.Messages != 1 || stats.Bytes <= 0 {
				t.Fatalf("image reduction missing from totals: %+v", stats)
			}
			if want := ctxmgr.EstimateMessagesBytes(msgs) - ctxmgr.EstimateMessagesBytes(prepared); stats.Bytes != want {
				t.Fatalf("saved bytes=%d, want the wire byte delta %d", stats.Bytes, want)
			}
			imageBucket := stats.ByToolAndRule[tools.NameViewImage+"/"+string(requesttrim.ImageOutput)]
			if imageBucket.Messages != 1 || imageBucket.TokensSaved <= 0 || combined && imageBucket.TokensSaved >= actual {
				t.Fatalf("image bucket must exclude text savings: %+v total=%d", imageBucket, actual)
			}
			if hasImagePart(prepared[2].Parts) {
				t.Fatal("image was not evicted")
			}
			next := a.prepareMessagesForLLM(msgs)
			nextStats := a.GetContextReductionStats()
			if !message.ContentPartsEqual(next[2].Parts, prepared[2].Parts) || nextStats.TokensSaved != actual || nextStats.Messages != 1 {
				t.Fatalf("stable reuse changed savings: %+v", nextStats)
			}
		})
	}
}

func TestImageEvictionKeepsAllRecallPaths(t *testing.T) {
	a := newImageReductionTestAgent(t)
	paths := []string{"/tmp/session/images/first.png", "/tmp/session/images/second.png"}
	msgs := imageEvictionMessages(1, toolResultParts("Loaded images.", []message.ContentPart{imageResultFixture(paths[0]), imageResultFixture(paths[1])}))
	setTestRequestBatch(a, nil, 3)
	marker := a.prepareMessagesForLLM(msgs)[2].Parts[1].Text
	for _, path := range paths {
		if !strings.Contains(marker, path) {
			t.Fatalf("marker lost recall address %q: %s", path, marker)
		}
	}
}

func TestImageOnlyPartsRetainEvictionMarker(t *testing.T) {
	a := &MainAgent{projectConfig: &config.Config{Context: config.ContextConfig{Reduction: config.ContextReductionConfig{StaleAgeTurns: 2}}}}
	msgs := imageEvictionMessages(1, []message.ContentPart{imageResultFixture("/tmp/session/images/shot.png")})
	setTestRequestBatch(a, nil, 3)
	parts := a.prepareMessagesForLLM(msgs)[2].Parts
	if len(parts) != 1 || !strings.Contains(parts[0].Text, "[Older image output omitted") {
		t.Fatalf("image-only parts lost the eviction marker: %+v", parts)
	}
}

func TestImageEvictionDeferredCacheRemainsReviewable(t *testing.T) {
	a := newImageReductionTestAgent(t)
	a.projectConfig.Context.Reduction.MinIncrementalTokens = 1 << 20
	a.runningModelRef = "p/m"
	a.recordLLMModelRun("p/m")
	a.recordLLMModelRun("p/m")
	content := "Loaded image into context."
	msgs := imageEvictionMessages(1, toolResultParts(content, []message.ContentPart{imageResultFixture("/tmp/session/images/shot.png")}))
	msgs[2].Content = content
	later := imageEvictionMessages(2, toolResultParts("More images.", []message.ContentPart{
		imageResultFixture("/tmp/session/images/next-1.png"),
		imageResultFixture("/tmp/session/images/next-2.png"),
		imageResultFixture("/tmp/session/images/next-3.png"),
	}))
	later[1].ToolCalls[0].ID = "img-call-2"
	later[1].ToolCalls[0].Args = []byte(`{"path":"next.png"}`)
	later[2].ToolCallID = "img-call-2"
	msgs = append(msgs, later[1:]...)
	msgs = append(msgs, message.Message{Role: message.RoleAssistant, Content: strings.Repeat("stable assistant filler ", 3000)})
	setTestRequestBatch(a, nil, 2)
	a.prepareMessagesForLLM(msgs)
	setTestRequestBatch(a, nil, 3)
	deferred := a.prepareMessagesForLLM(msgs)
	if !hasImagePart(deferred[2].Parts) || a.GetContextReductionStats().SkippedByReason[contextReductionSkipDeferredCache] != 1 {
		t.Fatalf("expected cache deferral, got %+v", a.GetContextReductionStats())
	}
	if a.lastPreparedLLMNextReviewAge[2] <= 0 {
		t.Fatal("deferred image lost its review frontier")
	}
	// The younger images join the savings once they also cross the frontier.
	setTestRequestBatch(a, nil, 4)
	prepared := a.prepareMessagesForLLM(msgs)
	if hasImagePart(prepared[2].Parts) || hasImagePart(prepared[4].Parts) {
		t.Fatal("deferred image was not re-evaluated when the batch justified flushing")
	}
}
