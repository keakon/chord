package agent

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
)

func TestSummarizeCompactionHeadRetriesInvalidSummaryOnce(t *testing.T) {
	t.Parallel()
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.SetProviderModelRef("sample/compact-model")

	providerCfg := llm.NewProviderConfig("sample", config.ProviderConfig{
		Type: "stub",
		Models: map[string]config.ModelConfig{
			"compact-model": {
				Limit: config.ModelLimit{Context: 16384, Output: 2048},
			},
		},
	}, []string{"test-key"})
	provider := &countingCompactionProvider{
		progress: []message.StreamProgressDelta{{Bytes: 128, Events: 2}},
		responses: []*message.Response{
			{Content: "too short"},
			{Content: validCompactionSummaryForTest("history-1.md")},
		},
	}
	client := llm.NewClient(providerCfg, provider, "compact-model", 2048, "")
	a.llmClient = client

	head := []message.Message{
		{Role: "user", Content: "Please continue working on the current task."},
		{Role: "assistant", Content: "I will inspect the current implementation and summarize next steps."},
	}
	summary, _, err := summarizeCompactionHeadForTest(a, head, "history-1.md")
	if err != nil {
		t.Fatalf("summarizeCompactionHead error: %v", err)
	}
	if provider.calls != 2 {
		t.Fatalf("provider Complete calls = %d, want 2", provider.calls)
	}
	if !strings.Contains(summary, "history-1.md") {
		t.Fatalf("summary should contain archive reference, got:\n%s", summary)
	}
	var progress []CompactionStatusEvent
	for _, event := range drainAgentEvents(a.outputCh) {
		if event, ok := event.(CompactionStatusEvent); ok && event.Status == "progress" {
			progress = append(progress, event)
		}
	}
	if len(progress) == 0 {
		t.Fatal("repair emitted no compaction progress events")
	}
	last := progress[len(progress)-1]
	if last.Bytes != 256 || last.Events != 4 {
		t.Fatalf("repair progress = %#v, want cumulative bytes=256 events=4", last)
	}
}

func TestSummarizeCompactionHeadDoesNotRepairTransportError(t *testing.T) {
	t.Parallel()
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.SetProviderModelRef("sample/compact-model")

	providerCfg := llm.NewProviderConfig("sample", config.ProviderConfig{
		Type: "stub",
		Models: map[string]config.ModelConfig{
			"compact-model": {
				Limit: config.ModelLimit{Context: 16384, Output: 2048},
			},
		},
	}, []string{"test-key"})
	provider := &countingCompactionProvider{err: fmt.Errorf("network unavailable")}
	client := llm.NewClient(providerCfg, provider, "compact-model", 2048, "")
	a.llmClient = client

	head := []message.Message{
		{Role: "user", Content: "Please continue working on the current task."},
		{Role: "assistant", Content: "I will inspect the current implementation and summarize next steps."},
	}
	_, _, err := summarizeCompactionHeadForTest(a, head, "history-1.md")
	if err == nil {
		t.Fatal("expected transport error")
	}
	if provider.calls != 1 {
		t.Fatalf("provider Complete calls = %d, want 1", provider.calls)
	}
}

// TestSummarizeCompactionHeadWalksPoolAndKeysAfterFailure documents that the
// compaction summarize request keeps the main client's model-pool semantics:
// a retriable failure walks every key of the current target before falling
// back to the next pool target, and the successful fallback model is the one
// recorded on the checkpoint.
func TestSummarizeCompactionHeadWalksPoolAndKeysAfterFailure(t *testing.T) {
	t.Parallel()
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.SetProviderModelRef("sample/compact-model")

	primaryCfg := llm.NewProviderConfig("sample", config.ProviderConfig{
		Type: "stub",
		Models: map[string]config.ModelConfig{
			"compact-model": {Limit: config.ModelLimit{Context: 16384, Output: 2048}},
		},
	}, []string{"key-1", "key-2"})
	fallbackCfg := llm.NewProviderConfig("fallback", config.ProviderConfig{
		Type: "stub",
		Models: map[string]config.ModelConfig{
			"fallback-model": {Limit: config.ModelLimit{Context: 16384, Output: 2048}},
		},
	}, []string{"fallback-key"})
	primary := &countingCompactionProvider{err: &llm.APIError{StatusCode: 500, Message: "temporary failure"}}
	fallback := &countingCompactionProvider{
		response: &message.Response{Content: validCompactionSummaryForTest("history-1.md"), StopReason: "stop"},
	}
	client := llm.NewClient(primaryCfg, primary, "compact-model", 2048, "")
	client.SetFallbackModels([]llm.FallbackModel{{
		ProviderConfig: fallbackCfg,
		ProviderImpl:   fallback,
		ModelID:        "fallback-model",
		MaxTokens:      2048,
	}})
	a.llmClient = client

	head := []message.Message{
		{Role: "user", Content: "Please continue working on the current task."},
		{Role: "assistant", Content: "I will inspect the current implementation and summarize next steps."},
	}
	summary, modelRef, err := summarizeCompactionHeadForTest(a, head, "history-1.md")
	if err != nil {
		t.Fatalf("summarizeCompactionHead error: %v", err)
	}
	if primary.calls != 2 {
		t.Fatalf("primary Complete calls = %d, want 2 (one per key)", primary.calls)
	}
	if fallback.calls != 1 {
		t.Fatalf("fallback Complete calls = %d, want 1", fallback.calls)
	}
	if !slices.Equal(primary.keys, []string{"key-1", "key-2"}) || !slices.Equal(fallback.keys, []string{"fallback-key"}) {
		t.Fatalf("key order = %v / %v, want [key-1 key-2] / [fallback-key]", primary.keys, fallback.keys)
	}
	if !slices.Equal(primary.models, []string{"compact-model", "compact-model"}) || !slices.Equal(fallback.models, []string{"fallback-model"}) {
		t.Fatalf("model order = %v / %v", primary.models, fallback.models)
	}
	if !strings.Contains(summary, "history-1.md") {
		t.Fatalf("summary should contain archive reference, got:\n%s", summary)
	}
	if modelRef != "fallback/fallback-model" {
		t.Fatalf("model ref = %q, want fallback/fallback-model", modelRef)
	}
}
