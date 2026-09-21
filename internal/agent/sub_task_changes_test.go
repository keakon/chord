package agent

import (
	"testing"

	"github.com/keakon/chord/internal/tools"
)

func TestRecordTaskToolChangesShellReadOnlyMapping(t *testing.T) {
	// A shell the classifier admits skips file-attribution
	// bookkeeping entirely, even on error; a rejected one marks the
	// attribution incomplete when nothing proves what it touched.
	registry := tools.NewRegistry()
	registry.Register(tools.NewShellTool("bash"))
	sub := &SubAgent{tools: registry, workDir: t.TempDir()}

	readonly := &toolResult{CallID: "rg", Name: tools.NameShell, ArgsJSON: `{"command":"rg --no-config -n pat --glob '*.go'"}`}
	if files, incomplete := sub.recordTaskToolChanges(readonly, false); len(files) != 0 || incomplete {
		t.Fatalf("read-only shell success = (%v, %v), want (nil, false)", files, incomplete)
	}
	if files, incomplete := sub.recordTaskToolChanges(readonly, true); len(files) != 0 || incomplete {
		t.Fatalf("read-only shell error = (%v, %v), want (nil, false)", files, incomplete)
	}
	if sub.fileAttributionIncomplete {
		t.Fatal("read-only shells must not flag file attribution incomplete")
	}

	mutating := &toolResult{CallID: "test", Name: tools.NameShell, ArgsJSON: `{"command":"go test ./..."}`}
	if files, incomplete := sub.recordTaskToolChanges(mutating, false); len(files) != 0 || !incomplete {
		t.Fatalf("mutating shell success = (%v, %v), want (nil, true)", files, incomplete)
	}
	if files, incomplete := sub.recordTaskToolChanges(mutating, true); len(files) != 0 || !incomplete {
		t.Fatalf("mutating shell error = (%v, %v), want (nil, true)", files, incomplete)
	}
	if !sub.fileAttributionIncomplete {
		t.Fatal("unattributed mutating shell must flag file attribution incomplete")
	}
}
