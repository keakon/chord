package agent

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
)

func stageBoundaryTestAgent(t *testing.T) *MainAgent {
	t.Helper()
	a := newTestMainAgent(t, t.TempDir())
	enableTestCompactContext(a)
	a.ctxMgr = ctxmgr.NewManagerWithTokenBudgets(1000, 1000, 1000, 0, 0.8)
	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: strings.Repeat("a", 600)})
	a.ctxMgr.UpdateFromUsage(message.TokenUsage{InputTokens: 200})
	a.stageCompletionCandidatePending = true
	return a
}

func buildStageBoundaryTestRequest(a *MainAgent) []message.Message {
	input := snapshotLLMRequest(a.ctxMgr)
	input.stageBoundary = a.snapshotStageBoundaryRequest()
	overlays := a.buildTurnOverlayMessages()
	return a.appendStageBoundaryReminder(input, input.Messages, overlays, "sample/test-model")
}

func countStageBoundaryNotices(messages []message.Message) int {
	count := 0
	for _, msg := range messages {
		if strings.Contains(msg.Content, "Content added since the last usage reading") {
			count++
		}
	}
	return count
}

func TestStageBoundaryPressureTodoIsTransientAndDoesNotArm(t *testing.T) {
	a := stageBoundaryTestAgent(t)
	a.ctxMgr.Append(message.Message{Role: message.RoleTool, Content: strings.Repeat("b", 2400)})
	before := a.ctxMgr.MessageCount()
	overlays := buildStageBoundaryTestRequest(a)
	if countStageBoundaryNotices(overlays) != 1 {
		t.Fatalf("expected one projected reminder: %+v", overlays)
	}
	if a.ctxMgr.MessageCount() != before || a.autoCompactRequested.Load() || a.ctxMgr.AutoCompactDecision().ShouldCompact {
		t.Fatal("the advisory reminder must neither persist nor arm automatic compaction")
	}
	for _, want := range []string{
		contextPressureFinalResponseExceptionFor(contextPressureAudienceMain),
		contextCheckpointPreparation,
		"not proof that the work is verified",
		"preserve every unfinished workstream",
	} {
		if !strings.Contains(stageBoundaryPressureReminder(), want) {
			t.Fatalf("reminder missing %q", want)
		}
	}
	// No dispatch acknowledgement: cancellation loses this one-shot text.
	if overlays := buildStageBoundaryTestRequest(a); countStageBoundaryNotices(overlays) != 0 {
		t.Fatal("a consumed todo candidate must not retry its pressure overlay")
	}
}

func TestStageBoundaryPressureMailboxBatchAndRedelivery(t *testing.T) {
	a := stageBoundaryTestAgent(t)
	a.stageCompletionCandidatePending = false
	batch := []*SubAgentMailboxMessage{
		{MessageID: "completion-1", AgentID: "worker-1", Kind: SubAgentMailboxKindCompleted, Summary: strings.Repeat("b", 1800)},
		{MessageID: "completion-2", AgentID: "worker-2", Kind: SubAgentMailboxKindCompleted, Summary: "Task complete"},
	}
	a.pendingSubAgentMailboxes = batch
	if got := countStageBoundaryNotices(buildStageBoundaryTestRequest(a)); got != 1 {
		t.Fatalf("batch reminder count = %d", got)
	}
	if a.ctxMgr.MessageCount() != 3 {
		t.Fatal("both reports must be durable before projection is evaluated")
	}
	a.pendingSubAgentMailboxes = batch
	if got := countStageBoundaryNotices(buildStageBoundaryTestRequest(a)); got != 0 {
		t.Fatalf("redelivery produced %d reminders", got)
	}
}

func TestStageBoundaryPressureCompletionAndTodo(t *testing.T) {
	a := stageBoundaryTestAgent(t)
	a.ctxMgr.Append(message.Message{Role: message.RoleTool, Content: strings.Repeat("b", 1800)})
	a.pendingSubAgentMailboxes = []*SubAgentMailboxMessage{{
		MessageID: "completion-1", AgentID: "worker-1", Kind: SubAgentMailboxKindCompleted, Summary: strings.Repeat("c", 1800),
	}}
	overlays := buildStageBoundaryTestRequest(a)
	if got := countStageBoundaryNotices(overlays); got != 1 {
		t.Fatalf("completion and todo produced %d boundary reminders, want 1", got)
	}
	// The two overlays are intentionally complementary: the existing TODO
	// candidate describes the stage, the projected notice adds capacity
	// pressure and checkpoint preparation. See context-management.md.
	if !slices.ContainsFunc(overlays, func(msg message.Message) bool {
		return strings.Contains(msg.Content, a.stageCompletionCandidateReminder())
	}) {
		t.Fatalf("todo candidate explanation was dropped: %+v", overlays)
	}
}

func TestStageBoundaryPressureConditions(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*MainAgent)
	}{
		{"no_growth", func(a *MainAgent) { a.ctxMgr.UpdateFromUsage(message.TokenUsage{InputTokens: 200}) }},
		{"no_boundary", func(a *MainAgent) { a.stageCompletionCandidatePending = false }},
		{"unknown", func(a *MainAgent) { a.ctxMgr.ClearLastTokenUsage() }},
		{"stale", func(a *MainAgent) { a.ctxMgr.InvalidateSizeObservation() }},
		{"invisible", func(a *MainAgent) { a.modelDrivenCompactionEnabled.Store(false) }},
		{"manual", func(a *MainAgent) { a.armManualCompactionIntent(1) }},
		{"running", func(a *MainAgent) { a.compactionSlotActive.Store(true) }},
		{"no_budget", func(a *MainAgent) {
			a.ctxMgr = ctxmgr.NewManagerWithTokenBudgets(1000, 1000, 1000, 1000, 0.8)
			a.ctxMgr.UpdateFromUsage(message.TokenUsage{InputTokens: 200})
		}},
		{"threshold_disabled", func(a *MainAgent) { a.ctxMgr.SetThreshold(0) }},
		{"reminder_disabled", func(a *MainAgent) {
			a.globalConfig = &config.Config{Context: config.ContextConfig{Compaction: config.CompactionConfig{Reminder: config.CompactionReminderDisabled}}}
		}},
		{"reminder_at_threshold", func(a *MainAgent) {
			a.globalConfig = &config.Config{Context: config.ContextConfig{Compaction: config.CompactionConfig{Reminder: 0.8}}}
		}},
		{"already_observed", func(a *MainAgent) { a.ctxMgr.UpdateFromUsage(message.TokenUsage{InputTokens: 700}) }},
		{"checkpoint_pending", func(a *MainAgent) { a.pendingModelDriven = &modelDrivenCheckpointRequest{} }},
		{"checkpoint_outcome", func(a *MainAgent) { a.pendingModelDrivenNotice = "The checkpoint was skipped." }},
		{"apply_interval", func(a *MainAgent) {
			a.requestBatches.reserve(a.sessionEpoch, 0)
			a.lastModelDrivenApplyBatch = a.currentRequestBatch(nil)
		}},
		{"skip_cooldown", func(a *MainAgent) {
			a.requestBatches.reserve(a.sessionEpoch, 0)
			a.lastModelDrivenSkipBatch = a.currentRequestBatch(nil)
			a.lastModelDrivenSkipReason = modelDrivenSkipReasonLowGain
			a.requestBatches.reserve(a.sessionEpoch, 1)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := stageBoundaryTestAgent(t)
			a.ctxMgr.Append(message.Message{Role: message.RoleTool, Content: strings.Repeat("b", 1800)})
			tc.setup(a)
			if got := countStageBoundaryNotices(buildStageBoundaryTestRequest(a)); got != 0 {
				t.Fatalf("unexpected %d boundary reminders", got)
			}
		})
	}
}

func TestStageBoundaryPressureDefersToExistingNotice(t *testing.T) {
	for _, level := range []string{contextNoticePressure, contextNoticeImminent, contextNoticeWarning, contextNoticeManual} {
		t.Run(level, func(t *testing.T) {
			a := stageBoundaryTestAgent(t)
			a.ctxMgr.Append(message.Message{Role: message.RoleTool, Content: strings.Repeat("b", 1800)})
			a.ctxMgr.Append(message.Message{Role: message.RoleUser, Kind: message.KindContextNotice, NoticeLevel: level, Content: "A pressure notice."})
			if got := countStageBoundaryNotices(buildStageBoundaryTestRequest(a)); got != 0 {
				t.Fatalf("stacked %d boundary reminders on %s", got, level)
			}
		})
	}
}

func TestStageBoundaryPressureIgnoresProgressMailbox(t *testing.T) {
	a := stageBoundaryTestAgent(t)
	a.stageCompletionCandidatePending = false
	a.pendingSubAgentMailboxes = []*SubAgentMailboxMessage{{
		MessageID: "progress-1", Kind: SubAgentMailboxKindProgress, Summary: strings.Repeat("b", 2400),
	}}
	if got := countStageBoundaryNotices(buildStageBoundaryTestRequest(a)); got != 0 {
		t.Fatalf("progress produced %d boundary reminders", got)
	}
}

func TestStageBoundaryPressureReachesProviderRequest(t *testing.T) {
	a := newReadyTestMainAgent(t)
	enableTestCompactContext(a)
	cfg := llm.NewProviderConfig("sample", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"test-model": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
		},
	}, []string{"sample-key"})
	provider := &requestCaptureProvider{}
	a.swapLLMClientWithRef(llm.NewClient(cfg, provider, "test-model", 4096, "system"), "test-model", 128000, "sample/test-model")
	a.newTurn("")
	a.ctxMgr = ctxmgr.NewManagerWithTokenBudgets(10000, 10000, 10000, 0, 0.8)
	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: strings.Repeat("a", 6000)})
	a.ctxMgr.UpdateFromUsage(message.TokenUsage{InputTokens: 2000})
	a.pendingSubAgentMailboxes = []*SubAgentMailboxMessage{{
		MessageID: "completion-1", Kind: SubAgentMailboxKindCompleted, Summary: "Completed sample work\n" + strings.Repeat("b", 24000),
	}}
	ctx, cancel := context.WithTimeout(a.turn.Ctx, 5*time.Second)
	defer cancel()
	a.spawnMainLLMResponseGoroutine(ctx, a.turn.ID, snapshotLLMRequest(a.ctxMgr), "")
	done := make(chan struct{})
	go func() {
		a.outputWg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("request assembly did not finish before the deadline")
	}
	provider.mu.Lock()
	count := len(provider.requests)
	provider.mu.Unlock()
	if count != 1 {
		t.Fatalf("provider requests = %d, want 1", count)
	}
	text := provider.requestText(0)
	if !strings.Contains(text, stageBoundaryPressureReminder()) || !strings.Contains(text, "Completed sample work") {
		t.Fatal("provider request did not carry both the completion and its projected reminder")
	}
	if got := countStageBoundaryNotices(a.ctxMgr.Snapshot()); got != 0 {
		t.Fatal("request-only reminder became durable")
	}
	if a.ctxMgr.AutoCompactDecision().ShouldCompact || a.autoCompactRequested.Load() {
		t.Fatal("a projection beyond the threshold must not arm automatic compaction")
	}
}
