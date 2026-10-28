package agent

import (
	"reflect"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
)

func TestDirectoryCandidateBudgetIncludesLateLSPWithoutConsuming(t *testing.T) {
	for _, worker := range []bool{false, true} {
		name := "main"
		if worker {
			name = "worker"
		}
		t.Run(name, func(t *testing.T) {
			a := newTestMainAgent(t, t.TempDir())
			appendWriteResult(a, "call-1", "/repo/main.go")
			acked := 0
			source := &recordingSubLSPFollowupSource{items: []LSPFollowupItem{{
				DisplayPath: "main.go",
				Lines:       strings.Repeat("[W] sample diagnostic\n", 200),
				Ack:         func() { acked++ },
			}}}
			a.SetLSPFollowupSource(source)
			s := &SubAgent{parent: a, instanceID: "worker-1", ctxMgr: a.ctxMgr, workDir: a.workDir(), lspFollowupSource: source}
			history := a.ctxMgr.Snapshot()
			budgetInputs := func() directoryCandidateBudgetInputs {
				if worker {
					return s.directoryCandidateInputs(history)
				}
				return a.directoryCandidateInputs(history)
			}
			in := budgetInputs()
			overlay, ok := findLSPFollowupOverlay(in.overlays)
			if !ok {
				t.Fatal("late diagnostics are missing from the candidate budget")
			}
			if acked != 0 || a.stagedLSPFollowup != nil || s.stagedLSPFollowup != nil {
				t.Fatal("budget calculation consumed or staged the diagnostics")
			}
			r := &directoryInstructionRuntime{
				binding: directoryInstructionBinding{CheckoutRoot: "/repo", ContentRoot: "/repo", WorkDir: "/repo"},
				pending: map[string]directoryInstructionObservation{
					"pkg": {Status: "present", SourcePath: "/repo/pkg/AGENTS.md", Body: "Package rules.", BodyHash: message.DirectoryInstructionBodyHash("Package rules.")},
				},
			}
			var withoutLSP []message.Message
			for _, msg := range in.overlays {
				if msg.Content != overlay.Content {
					withoutLSP = append(withoutLSP, msg)
				}
			}
			// Candidate admission reserves 4096 tokens beyond the priced input.
			budget := llm.EstimateRequestInputTokens(in.system, append(append([]message.Message(nil), history...), withoutLSP...), in.defs) + 4096 + 500
			for _, tc := range []struct {
				overlays []message.Message
				want     string
			}{
				{withoutLSP, message.DirectoryInstructionUpsert},
				{in.overlays, message.DirectoryInstructionBlocked},
			} {
				candidate, err := buildDirectoryCandidateMessage(r, history, in.system, in.defs, budget, tc.overlays)
				if err != nil || candidate == nil || len(candidate.DirectoryInstructions.Facts) != 1 {
					t.Fatalf("candidate = %+v, %v", candidate, err)
				}
				if got := candidate.DirectoryInstructions.Facts[0].Operation; got != tc.want {
					t.Fatalf("candidate operation = %q, want %q", got, tc.want)
				}
			}
			var delivered message.Message
			if worker {
				delivered, ok = s.buildLSPFollowupOverlay()
			} else {
				delivered, ok = a.buildLSPFollowupOverlay()
			}
			if !ok || !reflect.DeepEqual(overlay, delivered) {
				t.Fatalf("budget overlay differs from the request: %+v / %+v", overlay, delivered)
			}
			// Pricing again must not replace a request that was already staged.
			mainStaged, subStaged := a.stagedLSPFollowup, s.stagedLSPFollowup
			_ = budgetInputs()
			if a.stagedLSPFollowup != mainStaged || s.stagedLSPFollowup != subStaged || acked != 0 {
				t.Fatal("budget calculation changed a staged request")
			}
		})
	}
}
