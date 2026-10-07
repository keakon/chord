package agent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/memory"
	"github.com/keakon/chord/internal/message"
)

func seedReviewMemory(t *testing.T, a *MainAgent) {
	t.Helper()
	c := memory.Candidate{Type: memory.TypePreference, Statement: "Prefer concise project reports.", Rationale: "A recurring project preference.", Application: "Use when preparing reports.", Summary: "Report style", SourceRole: memory.SourceRoleUser, Confidence: memory.ConfidenceUserStated, Outcome: memory.OutcomeSuccess}
	if _, err := a.memoryMgr.CommitExtractionCtx(context.Background(), "source", "source-report", 1, 0, &memory.ExtractionOutput{Candidates: []memory.Candidate{c}}); err != nil {
		t.Fatal(err)
	}
	a.loadMemorySummary()
	a.applyLoadedMemoryAtCacheBreak()
}

func TestManualMemoryControlRefreshesAppliedSummaryWithoutTurn(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	seedReviewMemory(t, a)
	view, err := a.ReviewMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(view.Applied, "Report style") {
		t.Fatal("applied snapshot missing original memory")
	}
	beforeMessages := len(a.GetMessages())
	r := &memoryControlRequest{ctx: context.Background(), kind: "apply", draft: memory.NewRemovalDraft(view.Snapshot), reply: make(chan memoryControlResult, 1)}
	a.handleMemoryControl(r)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	evt, err := a.nextEvent(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if evt.Type != EventMemoryControlDone {
		t.Fatalf("event = %s", evt.Type)
	}
	a.dispatch(evt)
	res := <-r.reply
	if res.err != nil {
		t.Fatal(res.err)
	}
	a.outputWg.Wait()
	if a.currentTurn() != nil || len(a.GetMessages()) != beforeMessages {
		t.Fatal("manual memory operation created a turn/message")
	}
	view, err = a.ReviewMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(view.Applied, "Report style") || view.Pending {
		t.Fatal("manual change left the old applied summary")
	}
	if a.memoryManual.request != nil {
		t.Fatal("operation admission did not reset")
	}
}

func TestManualMemoryControlRejectsBusyOrCanceledRequest(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	seedReviewMemory(t, a)
	view, err := a.ReviewMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	a.turnMu.Lock()
	a.turn = &Turn{ID: 1}
	a.turnMu.Unlock()
	r := &memoryControlRequest{ctx: context.Background(), kind: "organize", base: view.Snapshot, reply: make(chan memoryControlResult, 1)}
	a.handleMemoryControl(r)
	if res := <-r.reply; res.err == nil || !strings.Contains(res.err.Error(), "idle") {
		t.Fatal("busy organization was admitted")
	}
	a.turnMu.Lock()
	a.turn = nil
	a.turnMu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r.ctx = ctx
	a.handleMemoryControl(r)
	if res := <-r.reply; res.err == nil {
		t.Fatal("canceled organization was admitted")
	}
}

func TestManualMemoryDoneDoesNotRefreshAnotherSession(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	seedReviewMemory(t, a)
	original := a.memoryApplied.Load()
	r := &memoryControlRequest{reply: make(chan memoryControlResult, 1)}
	a.handleMemoryControlDone(memoryControlResult{request: r, epoch: a.sessionEpoch + 1, snapshot: &memorySnapshot{}})
	res := <-r.reply
	if res.err == nil || a.memoryApplied.Load() != original {
		t.Fatal("stale result changed the active session")
	}
}

func TestReviewMemoryReportsDiskPendingSeparately(t *testing.T) {
	root := t.TempDir()
	a := newTestMainAgent(t, root)
	writeProjectMemory(t, root, "A user-owned memory note.")
	view, err := a.ReviewMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !view.Pending || view.Applied != "" {
		t.Fatal("disk content was presented as applied")
	}
	if a.MemoryEnabled() {
		t.Fatal("reading memory enabled automatic extraction")
	}
}

func TestGenerateMemoryOrganizationIsReadOnlyAndBudgeted(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	seedReviewMemory(t, a)
	view, err := a.ReviewMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	before, _ := a.memoryMgr.LoadIndex()
	raw := `{"candidates":[],"retire":[],"issues":["Two statements need clarification."]}`
	client := llm.NewClient(a.llmClient.ProviderConfig(), stubProvider{response: &message.Response{Content: raw, StopReason: "stop"}}, "test-model", 1024, "")
	d, err := a.generateMemoryOrganization(context.Background(), client, view.Snapshot, true, "Keep conditions.", "review-session")
	if err != nil {
		t.Fatal(err)
	}
	if !d.Empty() || len(d.Issues) != 1 {
		t.Fatal("clarifications should not turn into writes")
	}
	after, _ := a.memoryMgr.LoadIndex()
	if before.Raw != after.Raw {
		t.Fatal("generation changed index")
	}
	truncated := llm.NewClient(a.llmClient.ProviderConfig(), stubProvider{response: &message.Response{Content: raw, StopReason: "max_tokens"}}, "test-model", 1024, "")
	if _, err = a.generateMemoryOrganization(context.Background(), truncated, view.Snapshot, true, "", "review-session"); err == nil {
		t.Fatal("complete-looking JSON from a truncated response was accepted")
	}
	large := *view.Snapshot
	large.Items = append([]memory.ReviewItem(nil), large.Items...)
	large.Items[0].Content = strings.Repeat("memory input ", 30000)
	if _, err = a.generateMemoryOrganization(context.Background(), client, &large, true, "", "review-session"); err == nil || !strings.Contains(err.Error(), "budget") {
		t.Fatal("oversized full organization was not rejected")
	}
}

func TestMemoryOrganizationKeepsPoolCursorAndTraversesKeys(t *testing.T) {
	var order []string
	pool := make([]llm.FallbackModel, 3)
	for i := range pool {
		model := fmt.Sprintf("model-%d", i)
		cfg := config.ProviderConfig{Type: "stub", KeyOrder: config.KeyOrderSequential, Models: map[string]config.ModelConfig{model: {Limit: config.ModelLimit{Context: 32768, Output: 16384}}}}
		provider := &compactionPoolTraceProvider{err: &llm.APIError{StatusCode: http.StatusInternalServerError, Message: "temporary failure"}, order: &order}
		if i == 0 {
			provider.err = nil
			provider.response = &message.Response{Content: `{"candidates":[],"retire":[],"issues":[]}`, StopReason: "stop"}
		}
		pool[i] = llm.FallbackModel{ProviderConfig: llm.NewProviderConfig(fmt.Sprintf("provider-%d", i), cfg, []string{"key-1", "key-2"}), ProviderImpl: provider, ModelID: model, MaxTokens: 16384}
	}
	a := newTestMainAgent(t, t.TempDir())
	seedReviewMemory(t, a)
	view, err := a.ReviewMemory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	a.llmClient = newAuxClientFromPool(pool, 1, 0, "")
	_, originalCursor := a.llmClient.ModelPoolSnapshot()
	client, err := a.newMemoryExtractionClient(0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.generateMemoryOrganization(t.Context(), client, view.Snapshot, true, "", "review-session"); err != nil {
		t.Fatal(err)
	}
	want := []string{"model-1:key-1", "model-1:key-2", "model-2:key-1", "model-2:key-2", "model-0:key-1"}
	if !slices.Equal(order, want) {
		t.Fatalf("fallback order = %v, want %v", order, want)
	}
	if _, cursor := a.llmClient.ModelPoolSnapshot(); cursor != originalCursor || a.llmClient.ModelID() != "model-1" {
		t.Fatal("organization changed main cursor")
	}
}

func TestManualOrganizationSingleFlightAndForegroundCancellation(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	seedReviewMemory(t, a)
	view, err := a.ReviewMemory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	p := &blockingStreamProvider{calls: []scriptedStreamCall{{holdAfterStreams: true}}, streamedCh: make(chan struct{}), releaseCh: make(chan struct{})}
	a.llmClient = llm.NewClient(a.llmClient.ProviderConfig(), p, "test-model", 1024, "")
	r := &memoryControlRequest{ctx: t.Context(), kind: "organize", base: view.Snapshot, reply: make(chan memoryControlResult, 1)}
	a.handleMemoryControl(r)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	select {
	case <-p.streamedCh:
	case <-ctx.Done():
		t.Fatal("organization did not start")
	}
	second := &memoryControlRequest{ctx: t.Context(), kind: "undo", reply: make(chan memoryControlResult, 1)}
	a.handleMemoryControl(second)
	if res := <-second.reply; res.err == nil {
		t.Fatal("concurrent operation admitted")
	}
	a.preemptMemoryExtractionForTurn()
	evt, err := a.nextEvent(ctx)
	if err != nil {
		t.Fatal(err)
	}
	a.dispatch(evt)
	if res := <-r.reply; !errors.Is(res.err, context.Canceled) {
		t.Fatalf("cancel result = %v", res.err)
	}
	a.outputWg.Wait()
	if a.memoryManual.active.Load() {
		t.Fatal("manual admission stayed active")
	}
	idx, _ := a.memoryMgr.LoadIndex()
	if idx.Raw != view.Snapshot.Index.Raw {
		t.Fatal("canceled generation changed index")
	}
}
