package requesttrim

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/tools"
)

// benchmarkProtectedShellContext is the shape the classification chain is most
// expensive on: a large shell result that every protection branch has to look
// at before one of them keeps it complete.
func benchmarkProtectedShellContext(sizeBytes int) Context {
	body := strings.Repeat("step failed while processing a record\n", sizeBytes/38+1)
	return Context{
		ToolName:   tools.NameShell,
		Meta:       ToolCall{Name: tools.NameShell, Args: `{"command":"git log --oneline -30"}`},
		Content:    body,
		ToolStatus: "success",
		Age:        0,
		Policy:     DefaultPolicy(),
	}
}

// BenchmarkClassifyProtectedShellOutput measures one classification of a
// protected result.
func BenchmarkClassifyProtectedShellOutput(b *testing.B) {
	ctx := benchmarkProtectedShellContext(100 * 1024)
	b.ReportAllocs()
	b.SetBytes(int64(len(ctx.Content)))
	for b.Loop() {
		if class := Classify(ctx).Class; class != None {
			b.Fatalf("class = %q, want the protected class", class)
		}
	}
}

// BenchmarkProtectedShellRetentionDecision measures classification plus the
// retention decision built from it — the pair the request pass runs for every
// tool result it keeps complete.
func BenchmarkProtectedShellRetentionDecision(b *testing.B) {
	ctx := benchmarkProtectedShellContext(100 * 1024)
	b.ReportAllocs()
	b.SetBytes(int64(len(ctx.Content)))
	for b.Loop() {
		verdict := Classify(ctx)
		if d := RetentionDecision(ctx, verdict, "", ""); d.Level != RetentionFull {
			b.Fatalf("level = %q, want full", d.Level)
		}
	}
}
