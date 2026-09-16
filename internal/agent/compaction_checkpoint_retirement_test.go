package agent

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestCheckpointProgressRetirementAcrossGenerations(t *testing.T) {
	prior := checkpointTypedState{
		Completed:  []string{"parser tested"},
		Decisions:  []string{"use option A"},
		OpenIssues: []string{"verify parser"},
		Claims:     map[string]checkpointClaim{"use option A": {Status: typedClaimStatusActive}},
	}
	request := &modelDrivenCheckpointRequest{Args: tools.CompactContextArgs{
		Completed:    []string{"renderer tested"},
		Decisions:    []string{"use option B"},
		RetiredItems: []string{"use option A", "verify parser", "absent entry"},
	}}
	merged, _, _, malformed := mergePriorTypedCheckpointState(request, typedStateSectionHeading+"\n"+renderTypedStateJSON(prior))
	if malformed || !slices.Equal(merged.Args.Completed, []string{"renderer tested", "parser tested"}) {
		t.Fatalf("completed progress lost: %+v", merged)
	}
	if len(merged.Args.OpenIssues) != 0 || !slices.Equal(merged.Args.Decisions, []string{"use option B"}) {
		t.Fatalf("retired state survived: %+v", merged.Args)
	}
	if _, exists := merged.Claims["use option A"]; exists {
		t.Fatal("retired claim survived")
	}
	next, _, _, malformed := mergePriorTypedCheckpointState(&modelDrivenCheckpointRequest{}, typedStateSectionHeading+"\n"+renderTypedCheckpointState(merged))
	if malformed || !slices.Equal(next.Args.Completed, merged.Args.Completed) || len(next.Args.OpenIssues) != 0 {
		t.Fatalf("state regressed on next generation: %+v", next)
	}
	if len(prior.Claims) != 1 || len(prior.OpenIssues) != 1 {
		t.Fatal("retirement mutated source snapshot")
	}
}

func TestModelDrivenDerivedEvidenceStillValidatesAtBarrier(t *testing.T) {
	for _, valid := range []bool{true, false} {
		t.Run(fmt.Sprint(valid), func(t *testing.T) {
			agent := newTestMainAgent(t, t.TempDir())
			agent.newTurn()
			item := evidenceItem{Kind: evidenceToolDiff, Key: "parser", Excerpt: "parser updated"}
			agent.evidence.add(item)
			ref := evidenceItemID(item)
			if !valid {
				ref = "ev-000000000000"
			}
			agent.ctxMgr.Append(message.Message{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{testToolCall("checkpoint", tools.NameCompactContext)}})
			args := fmt.Sprintf(`{"active_objective":"finish parser","next_step":"run tests","claim_kinds":{"parser updated":"observed"},"claim_evidence":{"parser updated":[%q]}}`, ref)
			_, err := agent.tryArmModelDrivenCheckpoint("checkpoint", args)
			if valid && err != nil {
				t.Fatal(err)
			}
			if !valid {
				if err == nil || !strings.Contains(err.Error(), "unknown evidence ID") {
					t.Fatalf("unknown evidence not rejected: %v", err)
				}
				// The tool folds claim_evidence into evidence_refs, so a
				// rejection has to name the claim that carried the bad ID.
				if !strings.Contains(err.Error(), `claim_evidence["parser updated"]`) {
					t.Fatalf("rejection must name the claim_evidence entry, got: %v", err)
				}
			}
		})
	}
}

func TestCheckpointClaimsRenderOnceWithStatus(t *testing.T) {
	text := strings.Repeat("verified parser behavior ", 20)
	request := &modelDrivenCheckpointRequest{Claims: map[string]checkpointClaim{
		text: {Kind: tools.CompactContextClaimObserved, Status: typedClaimStatusInvalidated, EvidenceRefs: []string{"ev-a"}},
	}}
	rendered := renderCheckpointClaims(request)
	if strings.Count(rendered, strings.TrimSpace(text)) != 1 || !strings.Contains(rendered, "status: invalidated") {
		t.Fatalf("duplicate or missing status: %s", rendered)
	}
	if len(rendered) >= 2*len(text) {
		t.Fatal("combined view did not remove repeated claim text")
	}
}

func TestCheckpointCompletedCarryIsBounded(t *testing.T) {
	prior := checkpointTypedState{Completed: []string{"older outcome"}}
	current := checkpointTypedState{Completed: make([]string, 12)}
	for index := range current.Completed {
		current.Completed[index] = string(rune('a' + index))
	}
	merged, omitted, _ := mergeCheckpointTypedStates(prior, current)
	if len(merged.Completed) != 12 || omitted != 1 {
		t.Fatalf("unbounded progress: %+v, omitted=%d", merged, omitted)
	}
}

func TestCheckpointProgressSurvivesRepeatedDurableApplies(t *testing.T) {
	agent := newTestMainAgent(t, t.TempDir())
	agent.newTurn()
	agent.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: "implement and verify the parser"})
	for round := 1; round <= 20; round++ {
		agent.ctxMgr.Append(message.Message{Role: message.RoleAssistant, Content: "working on parser"})
		request := &modelDrivenCheckpointRequest{Args: tools.CompactContextArgs{
			ActiveObjective: "implement and verify the parser",
			NextStep:        "verify remaining parser cases",
		}}
		if round == 1 {
			request.Args.Completed = []string{"parser syntax verified"}
			request.Args.OpenIssues = []string{"verify empty input"}
		}
		if round == 2 {
			request.Args.Completed = []string{"empty input verified"}
			request.Args.RetiredItems = []string{"verify empty input"}
		}
		e2eApplyModelDrivenCheckpoint(t, agent, round, len(agent.ctxMgr.Snapshot())-1, request)
		checkpoint := e2eCheckpointAt(t, agent)
		state := e2eTypedStateOf(t, checkpoint)
		if !slices.Contains(state.Completed, "parser syntax verified") {
			t.Fatalf("round %d lost completed work: %+v", round, state)
		}
		if round >= 2 && (len(state.OpenIssues) != 0 || !slices.Contains(state.Completed, "empty input verified")) {
			t.Fatalf("round %d resurrected work: %+v", round, state)
		}
		if !strings.Contains(checkpoint.Content, "implement and verify the parser") || !strings.Contains(checkpoint.Content, "verify remaining parser cases") {
			t.Fatalf("round %d lost continuation", round)
		}
		if len(state.Completed) > 2 {
			t.Fatalf("round %d duplicated completed work: %+v", round, state)
		}
	}
}
