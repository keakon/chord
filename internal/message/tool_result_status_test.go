package message

import "testing"

func TestLegacyToolErrorFallbackRequiresExplicitErrorBoundary(t *testing.T) {
	if !IsToolResultErrorMessage(Message{Role: RoleTool, Content: "command output\n\nError: exit code 1"}) {
		t.Fatal("expected the explicit appended error boundary to be recognized")
	}
	if IsToolResultErrorMessage(Message{Role: RoleTool, Content: "command output mentions Error: but succeeded"}) {
		t.Fatal("an embedded Error: substring must not be treated as a failure")
	}
}

// TestToolErrorContentDelegatesToCentralClassifier pins IsToolErrorContent to
// ClassifyToolResultContent. A second local copy of the phrase list
// previously diverged on interrupted calls, classifying them as failures in one
// package and successes in the other on status-less transcripts.
func TestToolErrorContentDelegatesToCentralClassifier(t *testing.T) {
	for _, content := range []string{
		"Error: boom",
		"command output" + ToolResultAppendedErrorSeparator + "exit code 1",
		"Model stopped before completing this tool call",
		"plain success output",
		"output mentions Error: but succeeded",
		"cancelled",
		"",
	} {
		want := ClassifyToolResultContent(content) == ToolResultClassError
		if got := IsToolErrorContent(content); got != want {
			t.Fatalf("IsToolErrorContent(%q) = %v, central classifier says %v", content, got, want)
		}
	}
}
