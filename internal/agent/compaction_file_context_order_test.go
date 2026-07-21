package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

// TestStableSurfaceSurvivesKeyFileInjection pins the callLLM assembly order:
// the compaction key-file overlay is injected only after the prepared surface
// has been remembered, so the remembered shapes stay aligned with the durable
// history. If the overlay leaked into the stable-prefix shapes, every request
// after the first compaction would fail prefix compatibility and fall back to
// a full reduction scan with no deferred-flush cache protection. Keep the
// mirrored sequence below in sync with callLLM if its assembly order changes.
func TestStableSurfaceSurvivesKeyFileInjection(t *testing.T) {
	projectRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectRoot, "key.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write key file: %v", err)
	}
	a := newTestMainAgent(t, projectRoot)
	a.projectConfig = &config.Config{
		Context: config.ContextConfig{Reduction: config.ContextReductionConfig{
			ReadLikeAgeTurns:     1,
			ReadLikeOutputBytes:  80,
			MinIncrementalTokens: 1 << 20,
		}},
	}
	a.runningModelRef = "p/m"
	a.recordLLMModelRun("p/m")

	summary := buildCompactionCheckpointMessage(
		"## Current User Request\n- keep going\n\n## Files and Evidence\n- Archived history: history-1.md\n- key.go\n\n## Next Step\n- continue",
		[]string{".chord/sessions/test/history-1.md"},
		"model_summary",
		nil,
	)
	readContent := "READ_RESULT lines=1-120 total=120\n" + strings.Repeat("line content for read result\n", 120)
	msgs := []message.Message{
		{Role: "user", IsCompactionSummary: true, Content: summary},
		{Role: "user", Content: "continue"},
		{Role: "assistant", RequestBatch: 1, ToolCalls: []message.ToolCall{{ID: "tc1", Name: tools.NameWebFetch, Args: json.RawMessage(`{"url":"https://example.com/a.go"}`)}}},
		{Role: "tool", ToolCallID: "tc1", Content: readContent},
		{Role: "assistant", Content: "done"},
	}
	setTestRequestBatch(a, nil, 2)

	turnCtx, turnCancel := context.WithCancel(context.Background())
	defer turnCancel()
	a.turn = &Turn{ID: 1, Ctx: turnCtx, Cancel: turnCancel}

	// Request 1, mirroring callLLM: prepare, remember the surface, then inject
	// the request-local key-file overlay.
	prepared := a.prepareMessagesForLLM(msgs)
	if prepared[3].Content == readContent {
		t.Fatal("aged read should be reduced on request 1")
	}
	a.updatePreparedLLMRequestSurface(a.currentTurnID(), prepared)
	injected, insertedAt := a.injectCompactionFileContext(prepared)
	if insertedAt != 1 || len(injected) != len(prepared)+1 {
		t.Fatalf("expected key-file injection after the checkpoint, insertedAt=%d len=%d", insertedAt, len(injected))
	}

	// Request 2: the durable history grew by one user message and never
	// contains the overlay. The remembered surface must stay prefix-compatible
	// so stable reuse engages and the reduced marker stays byte-stable.
	setTestRequestBatch(a, nil, 3)
	msgs2 := append(append([]message.Message(nil), msgs...), message.Message{Role: "user", Content: "next"})
	prepared2 := a.prepareMessagesForLLM(msgs2)
	stats := a.GetContextReductionStats()
	if !stats.ReusedStable {
		t.Fatalf("stable surface should be reused after key-file injection, stats=%+v", stats)
	}
	if prepared2[3].Content != prepared[3].Content {
		t.Fatal("frozen reduced read marker should stay byte-stable across requests")
	}
}
