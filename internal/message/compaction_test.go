package message

import (
	"strings"
	"testing"
)

func TestIsCompactionEvidenceArtifactText(t *testing.T) {
	if !IsCompactionEvidenceArtifactText("[Context Evidence]\nhello") {
		t.Fatal("expected compaction evidence marker to be detected")
	}
	if IsCompactionEvidenceArtifactText("plain") {
		t.Fatal("did not expect plain text to be compaction evidence")
	}
}

func TestMergeCompactionSummaryAndEvidenceInsertsBeforeDisplayHint(t *testing.T) {
	summary := "[Context Summary]\nsummary\n\n[Context compressed]\narchived\n\n[Context display hint]\nPress toggle-collapse."
	evidence := "[Context Evidence]\n1. Latest failing tool result\nExcerpt:\nError: boom"
	got := MergeCompactionSummaryAndEvidence(summary, evidence)
	if !strings.Contains(got, evidence+"\n\n[Context display hint]") {
		t.Fatalf("merged content = %q, want evidence before display hint", got)
	}
}
