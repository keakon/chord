package agent

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
)

// TestModelSwitchOntoCrossedLineRunsRequestInParallelWithCompaction pins the
// usage-only switch contract: a running-model change invalidates the size
// observation (window and tokenization may differ), keeping only the calibration
// ratio. The round must still go out immediately — a switch never holds the
// round back — but nothing arms or starts a compaction until fresh usage on the
// new window crosses its own line; only a hard context-length rejection
// suspends a round.
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
