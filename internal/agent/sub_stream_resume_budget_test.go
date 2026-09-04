package agent

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
)

// TestSubAgentStreamResumesDoNotSpendTheTerminalNudge pins the split between
// two budgets that used to share one counter. A transport interruption is not a
// model that refuses to finish, so riding out a flaky gateway must not consume
// the single wrap-up nudge reserved for a reply that stopped at plain text.
func TestSubAgentStreamResumesDoNotSpendTheTerminalNudge(t *testing.T) {
	_, sub := newMixedBatchTestSubAgent(t)
	if sub.turn == nil {
		t.Fatal("test sub-agent has no live turn")
	}

	cause := &llm.InterruptedResponseError{StopReason: "interrupted"}
	for round := 1; round <= maxSubAgentStreamResumes; round++ {
		if !sub.recoverTerminalResponse("resume", cause) {
			t.Fatalf("resume %d refused, want it inside the stream-resume budget of %d", round, maxSubAgentStreamResumes)
		}
	}
	if sub.recoverTerminalResponse("resume", cause) {
		t.Fatalf("resume %d accepted, want the stream-resume budget exhausted", maxSubAgentStreamResumes+1)
	}
	if sub.turn.SubAgentTerminalRecoveryCount != 0 {
		t.Fatalf("terminal nudge budget spent = %d, want 0 (transport failures must not consume it)", sub.turn.SubAgentTerminalRecoveryCount)
	}
	// The wrap-up nudge is still available after all those transport resumes.
	if !sub.recoverTerminalResponse("finish coordination", nil) {
		t.Fatal("terminal nudge refused after stream resumes, want it still available")
	}
}

// TestSubAgentPreservesPartialWhenResumeBudgetIsSpent covers the contract that
// produced text is never discarded. Once the resume budget is gone the turn
// ends, but whatever the reply already streamed still belongs in history — the
// give-up path used to drop it because saving was bundled into the resume.
func TestSubAgentPreservesPartialWhenResumeBudgetIsSpent(t *testing.T) {
	_, sub := newMixedBatchTestSubAgent(t)
	if sub.turn == nil {
		t.Fatal("test sub-agent has no live turn")
	}
	sub.turn.SubAgentStreamResumeCount = maxSubAgentStreamResumes

	const streamed = "the analysis so far, worth keeping"
	sub.turn.appendPartialText(streamed)

	before := len(sub.ctxMgr.Snapshot())
	if sub.recoverTerminalResponse("resume", &llm.InterruptedResponseError{StopReason: "interrupted"}) {
		t.Fatal("resume accepted, want the budget to be exhausted for this test")
	}
	sub.preserveInterruptedPartial()

	msgs := sub.ctxMgr.Snapshot()
	if len(msgs) != before+1 {
		t.Fatalf("history grew by %d, want exactly one preserved interrupted message", len(msgs)-before)
	}
	last := msgs[len(msgs)-1]
	if last.Role != message.RoleAssistant || last.StopReason != "interrupted" {
		t.Fatalf("preserved message = role %q stop_reason %q, want an interrupted assistant message", last.Role, last.StopReason)
	}
	if !strings.Contains(last.Content, streamed) {
		t.Fatalf("preserved content = %q, want it to carry the streamed text", last.Content)
	}
	// Draining is what makes the save idempotent: a second call must not append
	// the same text again.
	sub.preserveInterruptedPartial()
	if got := len(sub.ctxMgr.Snapshot()); got != before+1 {
		t.Fatalf("history length after a second preserve = %d, want %d", got, before+1)
	}
}
