package agent

import (
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
)

// TestModelSwitchOntoCrossedLineRunsRequestInParallelWithCompaction pins the
// switch contract: a running-model change onto a window whose line the current
// context already crosses must not hold the round back. The pre-request gate
// starts the usage-driven compaction and spawns the request in parallel with
// it, so the round reaches the provider instead of waiting for the draft; only
// a hard context-length rejection suspends a round.
func TestModelSwitchOntoCrossedLineRunsRequestInParallelWithCompaction(t *testing.T) {
	a := newReadyTestMainAgent(t)
	a.globalConfig = &config.Config{Context: config.ContextConfig{Compaction: config.CompactionConfig{Threshold: 0.8}}}
	a.ctxMgr = ctxmgr.NewManagerWithInputBudget(100000, 100000, 0, 0.8)
	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: "continue the task"})
	a.ctxMgr.UpdateFromUsage(message.TokenUsage{InputTokens: 90000}) // 0.9: over the line the switch lands on
	// The running model just changed: the applied threshold still belongs to the
	// previous model, so this gate re-derives it and reads the crossing.
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
		t.Fatal("the round must be in flight in parallel with the compaction")
	}
	if !a.autoCompactRequested.Load() {
		t.Fatal("the switch onto a crossed line must arm the usage-driven compaction")
	}
	// compact_context is not visible in this test, so the gate starts the
	// compaction immediately instead of deferring its start across the grace
	// window (the request goes out either way).
	if !a.IsCompactionRunning() {
		t.Fatal("the gate must start the usage-driven compaction for the switched model")
	}
	if got := a.compactionState.trigger; got != compactionTriggerUsageDriven {
		t.Fatalf("compaction trigger = %q, want %q", got, compactionTriggerUsageDriven)
	}
}

// TestReminderDoesNotStageOverPendingHigherPressureNotice pins the guard that
// keeps the reminder from stacking on a higher-pressure notice left pending by
// a dispatch that never confirmed (a model switch that both re-attaches the
// reminder and arms the compaction in the same cycle). The guard lives in the
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
