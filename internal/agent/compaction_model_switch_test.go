package agent

import (
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
)

// TestModelSwitchOntoCrossedLineRunsRequestInParallelWithCompaction pins the
// usage-only switch contract: a running-model change retires the size
// observation for the trigger frame (window and tokenization may differ) — the
// gauge keeps the retired reading as stale until fresh usage arrives, and only
// the calibration ratio feeds the next frozen estimate. The round must still go
// out immediately — a switch never holds the round back — but nothing arms or
// starts a compaction until fresh usage on the new window crosses its own line;
// only a hard context-length rejection suspends a round.
func TestModelSwitchOntoCrossedLineRunsRequestInParallelWithCompaction(t *testing.T) {
	a := newReadyTestMainAgent(t)
	a.globalConfig = &config.Config{Context: config.ContextConfig{Compaction: config.CompactionConfig{Threshold: 0.8}}}
	a.ctxMgr = ctxmgr.NewManagerWithInputBudget(100000, 100000, 0, 0.8)
	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: "continue the task"})
	a.ctxMgr.UpdateFromUsage(message.TokenUsage{InputTokens: 90000}) // 0.9: over the old line, invalidated by the switch
	// The running model just changed: the applied threshold still belongs to the
	// previous model, so this gate re-derives it and invalidates the stale size.
	a.appliedCompactionModelRef = "provider/previous-model"
	provider := &blockingStreamProvider{calls: []scriptedStreamCall{{
		resp: &message.Response{Content: "still running", StopReason: "stop"},
	}}}
	providerCfg := llm.NewProviderConfig("provider", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"current-model": {Limit: config.ModelLimit{Context: 100000, Input: 100000, Output: 4096}},
		},
	}, []string{"test-key"})
	a.llmClient = llm.NewClient(providerCfg, provider, "current-model", 4096, "sys")
	a.newTurn()
	a.started.Store(true)
	t.Cleanup(func() {
		if a.IsCompactionRunning() {
			a.handleCompactionCancel()
		}
		a.compactionWg.Wait()
	})

	a.beginMainLLMAfterPreparation(a.turn.Ctx, a.turn.ID, "")

	// The request must have gone out; waiting on the provider observes the
	// actual dispatch rather than the in-flight flag alone.
	waitForBlockingStreamProviderCalls(t, provider, 1)
	if !a.mainLLMRequestInFlight.Load() {
		t.Fatal("the round must be in flight after the switch")
	}
	if a.autoCompactRequested.Load() {
		t.Fatal("the switch must not arm from stale observation: size is invalidated, fresh usage required")
	}
	if a.IsCompactionRunning() {
		t.Fatal("the gate must not start a compaction from stale observation after a model switch")
	}
}

// TestReminderDoesNotStageOverPendingHigherPressureNotice pins the guard that
// keeps the reminder from stacking on a higher-pressure notice left pending by
// a dispatch that never confirmed (a threshold crossing that both re-attaches
// the reminder and arms the compaction in the same cycle). The guard lives in the
// reminder queue because it is the lowest-severity notice and cannot tell from
// its own inputs whether the gate is about to attach the countdown or the
// warning for this same request.
func TestReminderDoesNotStageOverPendingHigherPressureNotice(t *testing.T) {
	newAgent := func(t *testing.T) *MainAgent {
		a := newTestMainAgent(t, t.TempDir())
		a.ctxMgr = ctxmgr.NewManagerWithInputBudget(8192, 8192, 0, 0.9)
		a.ctxMgr.UpdateFromUsage(message.TokenUsage{InputTokens: 5000}) // above the reminder line (0.54)
		enableTestCompactContext(a)
		return a
	}

	t.Run("grace countdown", func(t *testing.T) {
		a := newAgent(t)
		a.queueCompactionImminentNotice(minCompactionGracePeriodBatches)
		if a.pendingCompactionImminent == "" {
			t.Fatal("grace must stage the countdown")
		}
		a.queueContextPressureReminder(a.ctxMgr.AutoCompactDecision())
		if a.pendingContextPressureReminder != "" {
			t.Fatalf("a pending countdown must keep the lower-pressure reminder from staging, got %q", a.pendingContextPressureReminder)
		}
		if a.pendingCompactionImminent == "" {
			t.Fatal("the countdown must survive the reminder queue")
		}
	})

	t.Run("externalization warning", func(t *testing.T) {
		a := newAgent(t)
		a.requestBatches.reserve(a.sessionEpoch, 0)
		a.armUsageDrivenAutoCompactRequest()
		a.queueCompactionWarning()
		if a.pendingCompactionWarning == "" {
			t.Fatal("the armed request must stage the warning")
		}
		a.queueContextPressureReminder(a.ctxMgr.AutoCompactDecision())
		if a.pendingContextPressureReminder != "" {
			t.Fatalf("a pending warning must keep the lower-pressure reminder from staging, got %q", a.pendingContextPressureReminder)
		}
		if a.pendingCompactionWarning == "" {
			t.Fatal("the warning must survive the reminder queue")
		}
	})
}

// TestModelSwitchDoesNotAttachStalePressureNoticeToRequest is the end-to-end
// guard for the reported bug: a model switch that crossed the new window's
// threshold in the same cycle used to queue the sticky reminder together with a
// higher-pressure notice, so the model received two prompts describing one
// pressure. Under usage-only triggering the switch invalidates the previous
// window's size observation, so the request that goes out carries no pressure
// prompt at all: the crossing is judged again from fresh usage against the new
// window's own line.
func TestModelSwitchDoesNotAttachStalePressureNoticeToRequest(t *testing.T) {
	a := newReadyTestMainAgent(t)
	a.globalConfig = &config.Config{Context: config.ContextConfig{Compaction: config.CompactionConfig{Threshold: 0.8}}}
	a.ctxMgr = ctxmgr.NewManagerWithInputBudget(100000, 100000, 0, 0.8)
	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: "continue the task"})
	a.ctxMgr.UpdateFromUsage(message.TokenUsage{InputTokens: 90000}) // 0.9: above both the reminder line and the threshold
	// The running model just changed: the applied threshold still belongs to the
	// previous model, so this gate re-derives it and reads the crossing.
	a.appliedCompactionModelRef = "provider/previous-model"
	enableTestCompactContext(a)
	provider := &blockingStreamProvider{calls: []scriptedStreamCall{{
		resp: &message.Response{Content: "still running", StopReason: "stop"},
	}}}
	providerCfg := llm.NewProviderConfig("provider", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"current-model": {Limit: config.ModelLimit{Context: 100000, Input: 100000, Output: 4096}},
		},
	}, []string{"test-key"})
	a.llmClient = llm.NewClient(providerCfg, provider, "current-model", 4096, "sys")
	a.newTurn()
	a.started.Store(true)
	t.Cleanup(func() {
		if a.IsCompactionRunning() {
			a.handleCompactionCancel()
		}
		a.compactionWg.Wait()
	})

	a.beginMainLLMAfterPreparation(a.turn.Ctx, a.turn.ID, "")

	waitForBlockingStreamProviderCalls(t, provider, 1)
	requests, _ := provider.snapshot()
	for _, msg := range requests[0] {
		// The reminder and the countdown share the checkpoint-pressure action
		// text; the externalization warning has its own wording.
		if strings.Contains(msg.Content, contextCheckpointPressureAction) || strings.Contains(msg.Content, compactionWarningText) {
			t.Fatalf("a model switch must not warn from the previous window's observation, got %q", msg.Content)
		}
	}
}

// waitForFailedRoundSettled drains the TUI output channel until the request
// goroutine posts its segment end: tests drive the request path without the
// event loop, and the segment end is the last thing that goroutine emits, so an
// assertion after this observes a finished failed round instead of racing it.
func waitForFailedRoundSettled(t *testing.T, a *MainAgent) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case evt := <-a.outputCh:
			if _, ok := evt.(StreamSegmentEndedEvent); ok {
				return
			}
		case <-time.After(time.Millisecond):
		}
	}
	t.Fatal("timed out waiting for the failed round to post its segment end")
}

// TestModelSwitchFailedRoundKeepsRetiredGaugeReading pins the display half of
// the switch contract: a round that fails after the switch carries no usage, so
// the gauge must stay on the reading the switch retired (marked stale) instead
// of blanking to 0 until the new window reports usage of its own.
func TestModelSwitchFailedRoundKeepsRetiredGaugeReading(t *testing.T) {
	a := newReadyTestMainAgent(t)
	a.globalConfig = &config.Config{Context: config.ContextConfig{Compaction: config.CompactionConfig{Threshold: 0.8}}}
	a.ctxMgr = ctxmgr.NewManagerWithInputBudget(100000, 100000, 0, 0.8)
	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: "continue the task"})
	a.ctxMgr.UpdateFromUsage(message.TokenUsage{InputTokens: 90000}) // 0.9: measured against the previous window
	// The running model just changed: the applied threshold still belongs to the
	// previous model, so this gate retires the previous window's reading.
	a.appliedCompactionModelRef = "provider/previous-model"
	provider := &blockingStreamProvider{calls: []scriptedStreamCall{{
		err: &llm.APIError{StatusCode: 503, Message: "upstream unavailable"},
	}}}
	providerCfg := llm.NewProviderConfig("provider", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"current-model": {Limit: config.ModelLimit{Context: 100000, Input: 100000, Output: 4096}},
		},
	}, []string{"test-key"})
	client := llm.NewClient(providerCfg, provider, "current-model", 4096, "sys")
	client.SetStreamRetryRounds(1)
	a.llmClient = client
	a.llmMu.Lock()
	a.providerModelRef = "provider/current-model"
	a.runningModelRef = "provider/current-model"
	a.llmMu.Unlock()
	a.newTurn()
	a.started.Store(true)
	t.Cleanup(func() {
		if a.IsCompactionRunning() {
			a.handleCompactionCancel()
		}
		a.compactionWg.Wait()
	})

	a.beginMainLLMAfterPreparation(a.turn.Ctx, a.turn.ID, "")
	waitForBlockingStreamProviderCalls(t, provider, 1)
	waitForFailedRoundSettled(t, a)

	current, limit := a.GetContextStats()
	if current != 90000 || limit != 100000 {
		t.Fatalf("GetContextStats() after the failed round = (%d, %d), want the retired reading (90000, 100000)", current, limit)
	}
	if got := a.GetContextUsageState(); got != ctxmgr.ContextUsageStale {
		t.Fatalf("GetContextUsageState() = %v, want stale", got)
	}
	if a.ctxMgr.AutoCompactDecision().ShouldCompact {
		t.Fatal("the failed round must not arm automatic compaction from the retired reading")
	}
	if a.autoCompactRequested.Load() {
		t.Fatal("the failed round must not arm the usage-driven compaction request")
	}
}
