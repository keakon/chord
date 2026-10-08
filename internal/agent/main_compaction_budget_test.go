package agent

import (
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
)

func TestRunningModelUsesFixedCompactionBudgetForTriggerAndDisplay(t *testing.T) {
	provider := llm.NewProviderConfig("sample", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"model": {Limit: config.ModelLimit{Context: 400000, Output: 128000}},
		},
	}, []string{"key"})
	client := llm.NewClient(provider, &blockingStreamProvider{}, "model", 128000, "")
	t.Cleanup(client.Close)
	instance := newReadyTestMainAgent(t)
	instance.ctxMgr = ctxmgr.NewManager(400000, 0.5)
	instance.swapLLMClientWithRef(client, "model", 400000, "sample/model")
	for _, outputCap := range []int{0, 8192, 128000} {
		client.SetOutputTokenMax(outputCap)
		instance.applyRunningModelRefIfCurrent(client, "sample/model", 0, 0)
		instance.ctxMgr.UpdateFromUsage(message.TokenUsage{InputTokens: 136000})
		decision := instance.ctxMgr.AutoCompactDecision()
		current, limit := instance.GetContextStats()
		if !decision.ShouldCompact || decision.ThresholdTokens != 136000 || limit != 272000 || current != 136000 {
			t.Fatalf("output cap %d changed trigger/display: %+v, current/limit=%d/%d", outputCap, decision, current, limit)
		}
		if got, want := instance.ctxMgr.GetInputBudget(), client.InputLimitForModelRef("sample/model"); got != want {
			t.Fatalf("request input budget = %d, want %d", got, want)
		}
	}
}

func TestPressureNoticeAgreesWithExactFixedCompactionThreshold(t *testing.T) {
	instance := noticeRequestAgent(t)
	instance.globalConfig.Context.Compaction.Threshold = 0.28
	instance.globalConfig.Context.Compaction.Reminder = 0.2
	instance.ctxMgr.SetTokenBudgets(1050000, 986000, 922000, 0)
	instance.ctxMgr.SetThreshold(0.28)
	for _, tokens := range []int{258159, 258160} {
		instance.ctxMgr.UpdateFromUsage(message.TokenUsage{InputTokens: tokens})
		pressure, compaction := instance.pressureNoticeValidity(nil, "provider/model", 922000, true)
		if !pressure || compaction != (tokens == 258160) || compaction != instance.ctxMgr.ShouldAutoCompact() {
			t.Fatalf("tokens %d: pressure/compaction = %v/%v, decision = %+v", tokens, pressure, compaction, instance.ctxMgr.AutoCompactDecision())
		}
	}
}
