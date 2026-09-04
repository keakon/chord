package agent

import (
	"errors"
	"fmt"
	"net"
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

// TestSubAgentPreservesPartialOnEveryTransportInterruptionRound drives the whole
// give-up path through handleLLMResponse for consecutive interruptions, instead
// of calling the recovery helper directly. It covers the round that runs out of
// budget as well as the ones that resume, for both an error the stream-level
// classifier recognizes and one only the sub-agent's transport classifier does
// (a reset socket). The give-up branch used to test the narrower of the two
// conditions, so a reset mid-reply was preserved on the rounds that resumed and
// then discarded on the round that gave up.
func TestSubAgentPreservesPartialOnEveryTransportInterruptionRound(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cause error
	}{
		{name: "connection reset", cause: &net.OpError{Op: "read", Err: errors.New("connection reset by peer")}},
		{name: "interrupted stream", cause: &llm.InterruptedResponseError{StopReason: "interrupted"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, sub := newMixedBatchTestSubAgent(t)
			if sub.turn == nil {
				t.Fatal("test sub-agent has no live turn")
			}
			before := len(sub.ctxMgr.Snapshot())

			// One round past the budget: the last one ends the turn instead of
			// resuming, and is the round that used to drop its text.
			rounds := maxSubAgentStreamResumes + 1
			for round := 1; round <= rounds; round++ {
				sub.turn.appendPartialText(fmt.Sprintf("round %d partial reply", round))
				sub.handleLLMResponse(&llmResult{err: tc.cause, turnID: sub.turn.ID})

				preserved := preservedInterruptedContents(sub.ctxMgr.Snapshot()[before:])
				if len(preserved) != round {
					t.Fatalf("round %d: history holds %d preserved partials, want %d (one per round)", round, len(preserved), round)
				}
				if want := fmt.Sprintf("round %d partial reply", round); !strings.Contains(preserved[round-1], want) {
					t.Fatalf("round %d: preserved content = %q, want it to carry %q", round, preserved[round-1], want)
				}
			}

			if got := sub.turn.SubAgentStreamResumeCount; got != maxSubAgentStreamResumes {
				t.Fatalf("stream resumes spent = %d, want the budget of %d fully spent", got, maxSubAgentStreamResumes)
			}
			if got := sub.turn.SubAgentTerminalRecoveryCount; got != 0 {
				t.Fatalf("terminal nudge budget spent = %d, want 0", got)
			}
		})
	}
}

// preservedInterruptedContents returns the bodies of the interrupted assistant
// messages in msgs, in order. Rounds that resume also append the continuation
// instruction as a user message, so counting the preserved partials is what
// isolates "the produced text was kept" from the rest of the round.
func preservedInterruptedContents(msgs []message.Message) []string {
	var contents []string
	for _, msg := range msgs {
		if msg.Role == message.RoleAssistant && msg.StopReason == "interrupted" {
			contents = append(contents, msg.Content)
		}
	}
	return contents
}
