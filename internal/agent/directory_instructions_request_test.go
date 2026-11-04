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

func TestDirectoryInstructionMessagesKeepMechanismGuidanceInSystemPrompt(t *testing.T) {
	root := directoryTestFact(".", "Use repository conventions.")
	module := directoryTestFact("module", "Preserve schema field names.")
	leaf := directoryTestFact("module/leaf", "Keep identifiers unchanged.")
	history := []message.Message{
		{Role: message.RoleUser, Content: "Inspect the module."},
		directoryInstructionFactMessage([]message.DirectoryInstructionFact{module}),
		directoryInstructionFactMessage([]message.DirectoryInstructionFact{leaf}),
	}
	r := &directoryInstructionRuntime{
		binding: directoryInstructionBinding{CheckoutRoot: "/repo"},
		initial: directoryInstructionSnapshot{Observations: map[string]directoryInstructionObservation{
			".": {Status: "present", SourcePath: root.SourcePath, Body: root.Body, BodyHash: root.BodyHash},
		}},
	}
	v := buildDirectoryInstructionView(r.initial, history, r.binding)
	r.view.Store(&v)

	for name, system := range map[string]string{
		"main": (&MainAgent{}).buildSystemPrompt(),
		"sub":  (&SubAgent{}).buildSystemPrompt(),
	} {
		t.Run(name, func(t *testing.T) {
			for range 2 {
				projected, err := projectDirectoryInstructions(history, v)
				if err != nil {
					t.Fatal(err)
				}
				request := injectDirectoryInstructionReminder(projected, "", r)
				if _, err := validateDirectoryInstructionRequest(request, v); err != nil {
					t.Fatal(err)
				}
				text := system
				blocks := 0
				for _, m := range request {
					text += "\n" + m.Content
					blocks += strings.Count(m.Content, "# AGENTS.md")
					for _, unwanted := range []string{"The runtime automatically discovers AGENTS.md files", "Do not search for or read AGENTS.md files", "Deeper AGENTS.md instructions override"} {
						if strings.Contains(m.Content, unwanted) {
							t.Fatalf("injected body repeated mechanism guidance %q", unwanted)
						}
					}
				}
				if blocks != 3 {
					t.Fatalf("AGENTS.md blocks = %d, want 3", blocks)
				}
				if count := strings.Count(text, "The runtime automatically discovers AGENTS.md files"); count != 1 {
					t.Fatalf("mechanism guidance count = %d, want 1", count)
				}
				for _, f := range []message.DirectoryInstructionFact{root, module, leaf} {
					if count := strings.Count(text, f.Body); count != 1 {
						t.Fatalf("body %q count = %d, want 1", f.Body, count)
					}
				}
			}
		})
	}
}
func TestDirectoryInstructionUnknownInputBudgetDoesNotBlock(t *testing.T) {
	binding := directoryInstructionBinding{CheckoutRoot: "/repo", ContentRoot: "/repo", WorkDir: "/repo"}
	body := "package rules"
	observations := map[string]directoryInstructionObservation{
		"pkg": {Status: "present", SourcePath: "/repo/pkg/AGENTS.md", Body: body, BodyHash: message.DirectoryInstructionBodyHash(body)},
	}
	for _, budget := range []int{0, -1} {
		facts := directoryInstructionCandidateFacts(binding, observations, nil, budget, 1<<30)
		if len(facts) != 1 || facts[0].Operation != message.DirectoryInstructionUpsert || facts[0].Body != body {
			t.Fatalf("budget %d: facts = %+v, want the rule admitted", budget, facts)
		}
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
	r, facts, err := prepareDirectoryInstructions(context.Background(), directoryInstructionBinding{ContentRoot: root, WorkDir: root}, []message.Message{directoryInstructionFactMessage([]message.DirectoryInstructionFact{old})}, nil)
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
