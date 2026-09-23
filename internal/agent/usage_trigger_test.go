package agent

import (
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
)

// waitForResponseHandled drains the TUI output channel until the response path
// has run to completion: UsageUpdatedEvent is emitted as its last step, so an
// assertion after this observes a finished response instead of racing it.
func waitForResponseHandled(t *testing.T, a *MainAgent) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case evt := <-a.outputCh:
			if _, ok := evt.(UsageUpdatedEvent); ok {
				return
			}
		case <-time.After(time.Millisecond):
		}
	}
	t.Fatal("timed out waiting for the response path to emit UsageUpdatedEvent")
}

// A successful response whose normalized prompt exceeds the local usable input
// budget must not arm auto-compaction when the user disabled it with threshold
// 0. The answer is kept and the overshoot is diagnostics only: with a positive
// threshold the crossing already reads as ShouldCompact through the ordinary
// path, so arming here could only override an explicit disable.
func TestOversizedSuccessfulResponseDoesNotArmDisabledThreshold(t *testing.T) {
	a := newReadyTestMainAgent(t)
	a.globalConfig = &config.Config{Context: config.ContextConfig{Compaction: config.CompactionConfig{Threshold: 0}}}
	a.ctxMgr = ctxmgr.NewManagerWithInputBudget(1000, 1000, 0, 0)
	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: "continue the task"})
	provider := &blockingStreamProvider{calls: []scriptedStreamCall{{
		resp: &message.Response{Content: "done", StopReason: "stop", Usage: &message.TokenUsage{InputTokens: 2_000_000}},
	}}}
	providerCfg := llm.NewProviderConfig("provider", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"current-model": {Limit: config.ModelLimit{Context: 100000, Input: 100000, Output: 4096}},
		},
	}, []string{"test-key"})
	a.llmClient = llm.NewClient(providerCfg, provider, "current-model", 4096, "sys")
	// Pin the refs so the round runs against the disabled threshold instead of
	// a model change applied at the gate.
	a.llmMu.Lock()
	a.providerModelRef = "provider/current-model"
	a.runningModelRef = "provider/current-model"
	a.llmMu.Unlock()
	a.modelUpdateMu.Lock()
	a.appliedCompactionModelRef = "provider/current-model"
	a.modelUpdateMu.Unlock()
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
	waitForResponseHandled(t, a)

	decision := a.ctxMgr.AutoCompactDecision()
	if got := decision.EffectiveInputTokens; got != 2_000_000 {
		t.Fatalf("effective input tokens = %d, want the reported 2000000", got)
	}
	if decision.ShouldCompact {
		t.Fatalf("threshold 0 must keep the decision from compacting, got %+v", decision)
	}
	if a.autoCompactRequested.Load() {
		t.Fatal("an over-budget successful response must not arm a disabled auto-compaction")
	}
	if got := a.compactionTriggerForMainLLM(); got == compactionTriggerUsageDriven {
		t.Fatalf("gate trigger = %q, want no usage-driven compaction", got)
	}
}
