package agent

import "testing"

// newReductionTestMainAgent keeps request reduction on a real message manager
// and turn, without unrelated session persistence, memory extraction, or startup
// discovery. It must not be used by restore, provider dispatch, or durable apply
// tests, which exercise the complete runtime fixture.
func newReductionTestMainAgent(t *testing.T, root string) *MainAgent {
	t.Helper()
	a := newContextPolicyTestAgent(root)
	a.parentCtx = t.Context()
	a.llmClient = newTestLLMClient()
	t.Cleanup(func() {
		if turn := a.currentTurn(); turn != nil {
			turn.Cancel()
		}
	})
	return a
}
