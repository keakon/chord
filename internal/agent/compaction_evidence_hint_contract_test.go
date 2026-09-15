package agent

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestCheckpointHintMatchesObservedValidation(t *testing.T) {
	agent := newTestMainAgent(t, t.TempDir())
	positive := buildEvidenceItem(evidenceToolDiff, "positive", "needed", "tool", "diff")
	unclassified := buildEvidenceItem(evidenceToolDiff, "unclassified", "needed", "tool", "unclassified diff")
	stale := buildEvidenceItem(evidenceToolDiff, "stale", "needed", "tool", "stale diff")
	for _, item := range []evidenceItem{positive, unclassified, stale} {
		pack := buildCompactionCheckpointMessage("summary", nil, compactionSummaryModeModelDriven, []evidenceItem{item})
		if item.Key == unclassified.Key {
			pack = strings.ReplaceAll(pack, "Evidence Kind: "+string(item.Kind)+"\n", "")
		}
		agent.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: pack, IsCompactionSummary: true})
	}
	stale.Validity = evidenceValidityInvalidated
	agent.evidence.add(stale)
	ids, _ := agent.resolvableClaimEvidenceIDs(0)
	if !slices.Equal(ids, []string{evidenceItemID(positive)}) {
		t.Fatalf("unsafe hint candidates: %v", ids)
	}
	for _, ref := range ids {
		args := tools.CompactContextArgs{EvidenceRefs: []string{ref}, ClaimKinds: map[string]string{"updated": claimKindObserved}, ClaimEvidence: map[string][]string{"updated": {ref}}}
		if err := agent.validateObservedClaimEvidence(args); err != nil {
			t.Fatal(err)
		}
		if err := agent.validateModelDrivenEvidenceRefs(args.EvidenceRefs); err != nil {
			t.Fatal(err)
		}
	}
	if err := agent.validateModelDrivenEvidenceRefs([]string{evidenceItemID(unclassified)}); err != nil {
		t.Fatalf("provenance-only reference rejected: %v", err)
	}
}

func TestCheckpointWorkerUsesCapturedRequestIdentity(t *testing.T) {
	agent := newTestMainAgent(t, t.TempDir())
	agent.newTurn()
	request := &modelDrivenCheckpointRequest{ToolCallID: "captured-request"}
	agent.modelDrivenProposal.requestID = "different-live-request"
	bundle := agent.captureModelDrivenBarrierSnapshot(nil)
	agent.startModelDrivenCompactionAsync(bundle, 1, compactionTarget{sessionEpoch: agent.sessionEpoch}, continuationPlan{}, request)
	defer agent.compactionWg.Wait()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case event := <-agent.eventCh:
			if event.Type != EventCompactionReady {
				continue
			}
			draft := event.Payload.(*compactionDraft)
			if draft.ModelDrivenRequestID != request.ToolCallID {
				t.Fatalf("request identity = %q", draft.ModelDrivenRequestID)
			}
			return
		case <-timer.C:
			t.Fatal("checkpoint worker did not finish")
		}
	}
}

func TestCheckpointClaimOmitsEmptyMetadata(t *testing.T) {
	request := &modelDrivenCheckpointRequest{Claims: map[string]checkpointClaim{"finding": {EvidenceRefs: []string{"ev-a"}}}}
	if got := renderCheckpointClaims(request); got != "- finding | evidence: ev-a" {
		t.Fatalf("unexpected claim rendering: %q", got)
	}
}

func TestCheckpointRetirementPreservesProvenance(t *testing.T) {
	prior := checkpointTypedState{Claims: map[string]checkpointClaim{"finding": {EvidenceRefs: []string{"ev-a"}}}, EvidenceRefs: []string{"ev-a"}}
	retired := retireCheckpointItems(prior, []string{"finding"})
	if len(retired.Claims) != 0 || !slices.Equal(retired.EvidenceRefs, prior.EvidenceRefs) {
		t.Fatalf("unexpected retirement: %+v", retired)
	}
}
