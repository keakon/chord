package modelcompat

import (
	"testing"

	"github.com/keakon/chord/internal/message"
)

func TestDiagnosticSourceMergeKeepsMixedAttribution(t *testing.T) {
	msgs := []message.Message{{Role: message.RoleAssistant, Content: "first", DiagnosticSource: message.DiagnosticSourceInstructions}, {Role: message.RoleAssistant, Content: "second"}}
	out := compactAdjacentAssistantMessages(msgs)
	if len(out) != 1 || out[0].DiagnosticSource != message.DiagnosticSourceMixed {
		t.Fatal("mixed sources not retained", out)
	}
	if msgs[0].DiagnosticSource != message.DiagnosticSourceInstructions {
		t.Fatal("source mutation")
	}
}
