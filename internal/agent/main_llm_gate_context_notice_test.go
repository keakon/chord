package agent

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
)

// TestGateKeepsStickyReminderWhenTheWarningClaimIsSpent pins the one-shot
// externalization warning across the requests of one armed generation. The
// request that starts the compaction delivers the warning and its durable
// row; a later request behind the same generation — the compaction already
// running in parallel — must not attach a fresh warning overlay, and the
// injection ladder keeps the lower-priority reminder quiet while the warning
// row is retained: the notice content reaches the model only through the
// durable row the transcript replays.
func TestGateKeepsStickyReminderWhenTheWarningClaimIsSpent(t *testing.T) {
	a := newReadyTestMainAgent(t)
	a.globalConfig = &config.Config{Context: config.ContextConfig{Compaction: config.CompactionConfig{Threshold: 0.8}}}
	a.ctxMgr = ctxmgr.NewManagerWithInputBudget(100000, 100000, 0, 0.8)
	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: "continue the task"})
	a.ctxMgr.UpdateFromUsage(message.TokenUsage{InputTokens: 90000}) // 0.9: above the reminder line and the threshold
	enableTestCompactContext(a)
	// The window's grace is already spent, so this crossing starts the
	// compaction instead of deferring it with the countdown notice.
	a.compactionGraceExhausted = true
	provider := &blockingStreamProvider{calls: []scriptedStreamCall{
		{resp: &message.Response{Content: "still running", StopReason: "stop"}},
		{resp: &message.Response{Content: "still running", StopReason: "stop"}},
	}}
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
	// The crossing starts the compaction, and it stays running in parallel
	// with the requests below; the registered state keeps both gate entries on
	// the spawn-only path so the test drives the notice machinery, not a
	// compaction worker.
	a.beginCompactionState(1, compactionTarget{sessionEpoch: a.sessionEpoch}, compactionTriggerUsageDriven, continuationPlan{kind: compactionResumeMainLLM, turnID: a.turn.ID}, 0, nil)

	// First request of the armed generation: the gate queues the warning, the
	// request dispatches, and the delivery writes the durable rows.
	a.armUsageDrivenAutoCompactRequest()
	a.beginMainLLMAfterPreparation(a.turn.Ctx, a.turn.ID, "")
	waitForBlockingStreamProviderCalls(t, provider, 1)

	// Second request behind the same armed generation: the spent claim must
	// not re-attach the warning, the retained warning row keeps replaying,
	// and the ladder keeps the undelivered reminder quiet.
	a.beginMainLLMAfterPreparation(a.turn.Ctx, a.turn.ID, "")
	waitForBlockingStreamProviderCalls(t, provider, 2)

	requests, _ := provider.snapshot()
	if len(requests) < 2 {
		t.Fatalf("provider calls = %d, want 2", len(requests))
	}
	var first, second strings.Builder
	for _, msg := range requests[0] {
		first.WriteString(msg.Content)
		first.WriteByte('\n')
	}
	for _, msg := range requests[1] {
		second.WriteString(msg.Content)
		second.WriteByte('\n')
	}
	if !strings.Contains(first.String(), "has reached the automatic-compaction threshold") {
		t.Fatalf("the request that starts the compaction must deliver the warning; dispatched=%q", first.String())
	}
	if strings.Contains(second.String(), "approaching the configured automatic-compaction threshold") {
		t.Fatalf("the retained warning row must keep the lower-priority reminder quiet; dispatched=%q", second.String())
	}
	if n := strings.Count(second.String(), "has reached the automatic-compaction threshold"); n != 1 {
		t.Fatalf("the spent warning claim must not re-attach the warning; occurrences=%d dispatched=%q", n, second.String())
	}
}
