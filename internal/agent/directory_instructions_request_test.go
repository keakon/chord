package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
)

func directoryTestFact(scope, body string) message.DirectoryInstructionFact {
	return message.DirectoryInstructionFact{CheckoutRoot: "/repo", Scope: scope, SourcePath: "/repo/" + scope + "/AGENTS.md", Operation: message.DirectoryInstructionUpsert, Body: body, BodyHash: message.DirectoryInstructionBodyHash(body)}
}
func TestDirectoryInstructionViewInitialOwnsScopeAndRequiresExactBody(t *testing.T) {
	f := directoryTestFact("pkg", "old scope rules")
	dynamic := directoryTestFact("other", "other rules")
	history := []message.Message{directoryInstructionFactMessage([]message.DirectoryInstructionFact{f, dynamic})}
	initial := directoryInstructionSnapshot{Observations: map[string]directoryInstructionObservation{"pkg": {Status: "present", SourcePath: f.SourcePath, Body: "new scope rules", BodyHash: message.DirectoryInstructionBodyHash("new scope rules")}}}
	v := buildDirectoryInstructionView(initial, history, directoryInstructionBinding{CheckoutRoot: "/repo"})
	projected, err := projectDirectoryInstructions(history, v)
	if err != nil {
		t.Fatal(err)
	}
	projected = append(projected, directoryInstructionProjection(v.Initial))
	visible, err := validateDirectoryInstructionRequest(projected, v)
	if err != nil || len(visible) != 2 {
		t.Fatalf("visibility %v: %v", visible, err)
	}
	for _, m := range projected {
		if strings.Contains(m.Content, "old scope rules") {
			t.Fatal("initial scope rendered old body")
		}
	}
	projected[0].Content = "truncated"
	if _, err := validateDirectoryInstructionRequest(projected, v); err == nil {
		t.Fatal("refs without exact body admitted")
	}
}
func TestDirectoryInstructionCompactionNewTailWinsAndRemoveSurvives(t *testing.T) {
	old := directoryTestFact("pkg", "old")
	newFact := directoryTestFact("pkg", "new")
	remove := directoryTestFact("gone", "")
	remove.Operation = message.DirectoryInstructionRemove
	remove.BodyHash = ""
	original := []message.Message{directoryInstructionFactMessage([]message.DirectoryInstructionFact{old, remove})}
	tail := []message.Message{{Role: message.RoleUser, IsCompactionSummary: true, Content: "checkpoint"}, directoryInstructionFactMessage([]message.DirectoryInstructionFact{newFact})}
	compacted := retainDirectoryInstructionFacts(original, tail)
	twice := retainDirectoryInstructionFacts(compacted, []message.Message{{Role: message.RoleUser, IsCompactionSummary: true, Content: "checkpoint2"}})
	v := buildDirectoryInstructionView(directoryInstructionSnapshot{}, twice, directoryInstructionBinding{CheckoutRoot: "/repo"})
	if len(v.Dynamic) != 1 || v.Dynamic[0].Body != "new" {
		t.Fatalf("latest state lost: %+v", v)
	}
	count := 0
	for _, m := range twice {
		if message.IsDirectoryInstructions(m) {
			for _, f := range m.DirectoryInstructions.Facts {
				if f.Operation == message.DirectoryInstructionRemove {
					count++
				}
			}
		}
	}
	if count != 1 {
		t.Fatal("remove tombstone lost")
	}
}
func TestDirectoryInstructionPreparationRechecksRestoredAncestorChain(t *testing.T) {
	root, rootErr := filepath.EvalSymlinks(t.TempDir())
	if rootErr != nil {
		t.Fatal(rootErr)
	}
	dir := filepath.Join(root, "middle", "leaf")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "middle", "AGENTS.md"), []byte("new intermediate rules"), 0644); err != nil {
		t.Fatal(err)
	}
	old := message.DirectoryInstructionFact{CheckoutRoot: root, Scope: "middle/leaf", SourcePath: filepath.Join(dir, "AGENTS.md"), Operation: message.DirectoryInstructionUpsert, Body: "deleted leaf rules", BodyHash: message.DirectoryInstructionBodyHash("deleted leaf rules")}
	r, facts, err := prepareDirectoryInstructions(context.Background(), directoryInstructionBinding{ContentRoot: root, WorkDir: root}, []message.Message{directoryInstructionFactMessage([]message.DirectoryInstructionFact{old})}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	defer r.cancel()
	states := make(map[string]string)
	for _, f := range facts {
		states[f.Scope] = f.Operation
	}
	if states["middle"] != message.DirectoryInstructionUpsert || states["middle/leaf"] != message.DirectoryInstructionRemove {
		t.Fatalf("restored ancestor reconciliation: %+v", facts)
	}
}
