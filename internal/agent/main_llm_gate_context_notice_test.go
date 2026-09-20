package agent

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
)

// TestGateKeepsStickyReminderWhenTheWarningClaimIsSpent pins the sticky
// reminder across the requests of one armed generation. The externalization
// warning is claimed once, by the request that starts the compaction, so a
// later request behind the same generation carries no higher-pressure notice
// and must still carry the reminder like any other request above the reminder
// line.
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

	// The request that started the compaction already delivered the warning for
	// this armed generation, so the next gate's claim declines.
	a.armUsageDrivenAutoCompactRequest()
	a.overlayClaims.mu.Lock()
	a.overlayClaims.warning = warningOverlayClaim{
		requestID: a.autoCompactRequestGeneration.Load(),
		batch:     a.currentRequestBatch(a.ctxMgr.Snapshot()),
		delivered: true,
	}
	a.overlayClaims.mu.Unlock()

	a.beginMainLLMAfterPreparation(a.turn.Ctx, a.turn.ID, "")
	waitForBlockingStreamProviderCalls(t, provider, 1)

	requests, _ := provider.snapshot()
	if len(requests) == 0 {
		t.Fatal("the round must reach the provider")
	}
	dispatched := ""
	for _, msg := range requests[0] {
		dispatched += msg.Content + "\n"
	}
	if !strings.Contains(dispatched, "approaching the configured automatic-compaction threshold") {
		t.Fatalf("a request behind the armed generation must carry the sticky reminder; dispatched=%q", dispatched)
	}
	if strings.Contains(dispatched, "has reached the automatic-compaction threshold") {
		t.Fatalf("the spent warning claim must not re-attach the warning; dispatched=%q", dispatched)
	}
}
