package agent

import (
	"slices"
	"testing"
)

func TestOpenIssuesPreserveSignificantSpelling(t *testing.T) {
	issues := []string{"Fix `Foo`", "Fix `foo`", "Handle `a b`", "Handle `a  b`"}
	merged, _, _, _ := mergeCheckpointTypedStates(checkpointTypedState{}, checkpointTypedState{OpenIssues: issues})
	if !slices.Equal(merged.OpenIssues, issues) {
		t.Fatalf("distinct issues merged: %v", merged.OpenIssues)
	}
	merged, _, _, _ = mergeCheckpointTypedStates(merged, checkpointTypedState{OpenIssuesComplete: true})
	retired := retireCheckpointItems(merged, []string{" Fix `Foo` "})
	remaining := append([]string(nil), retired.CarriedOpenIssues...)
	if !slices.Equal(remaining, issues[1:]) {
		t.Fatalf("retirement removed a different issue: %v", remaining)
	}
}
