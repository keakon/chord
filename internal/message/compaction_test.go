package message

import "testing"

func TestIsCompactionEvidenceArtifactText(t *testing.T) {
	if !IsCompactionEvidenceArtifactText("[Context Evidence]\nhello") {
		t.Fatal("expected compaction evidence marker to be detected")
	}
	if IsCompactionEvidenceArtifactText("plain") {
		t.Fatal("did not expect plain text to be compaction evidence")
	}
}
