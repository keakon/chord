package ctxmgr

import (
	"testing"

	"github.com/keakon/chord/internal/message"
)

func TestCompactionStartsAtExactFixedBudgetThreshold(t *testing.T) {
	manager := NewManagerWithTokenBudgets(1050000, 986000, 922000, 0, 0.28)
	manager.UpdateFromUsage(message.TokenUsage{InputTokens: 258159})
	if manager.ShouldAutoCompact() {
		t.Fatal("compaction started below the fixed threshold")
	}
	manager.UpdateFromUsage(message.TokenUsage{InputTokens: 258160})
	if decision := manager.AutoCompactDecision(); !decision.ShouldCompact || decision.ThresholdTokens != 258160 {
		t.Fatalf("compaction must start at the documented threshold: %+v", decision)
	}
}

func TestCompactionUsesFixedBudgetAcrossRequestBudgetChanges(t *testing.T) {
	manager := NewManagerWithTokenBudgets(400000, 336000, 272000, 16000, 0.5)
	manager.UpdateFromUsage(message.TokenUsage{InputTokens: 128000})
	for _, requestBudget := range []int{336000, 391808, 272000} {
		manager.SetTokenBudgets(400000, requestBudget, 272000, 16000)
		decision := manager.AutoCompactDecision()
		if !decision.ShouldCompact || decision.ThresholdTokens != 128000 {
			t.Fatalf("request budget %d changed fixed compaction decision: %+v", requestBudget, decision)
		}
		if decision.UsableInputBudget != requestBudget-16000 || decision.UsableCompactionBudget != 256000 {
			t.Fatalf("request and compaction budgets are not separate: %+v", decision)
		}
	}
	epoch := manager.TokenBudgetsEpoch()
	manager.SetTokenBudgets(400000, 272000, 300000, 16000)
	if manager.TokenBudgetsEpoch() != epoch+1 || manager.AutoCompactDecision().ShouldCompact {
		t.Fatal("fixed budget change must update the window and re-evaluate the threshold")
	}
}
