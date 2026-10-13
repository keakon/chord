package agent

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestDirectoryInstructionsSpeculationRechecksRetargetedParent(t *testing.T) {
	for _, name := range []string{tools.NameWrite, tools.NameApplyPatch, tools.NameRead} {
		t.Run(name, func(t *testing.T) {
			p, root := directoryPipelineFixture(t)
			a, b := filepath.Join(root, "a"), filepath.Join(root, "b")
			for _, dir := range []string{a, b} {
				writeResolverRule(t, dir, "")
				if err := os.WriteFile(filepath.Join(dir, "sample.txt"), []byte("original\n"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			writeResolverRule(t, b, "Review the module before editing.")
			link := filepath.Join(root, "active")
			if err := os.Symlink(a, link); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(link, "sample.txt")
			args := map[string]string{"path": path}
			if name == tools.NameWrite {
				args["content"] = "changed\n"
			}
			if name == tools.NameApplyPatch {
				args = map[string]string{"patch": "*** Begin Patch\n*** Update File: active/sample.txt\n@@\n-original\n+changed\n*** End Patch"}
			}
			call := directoryCall("operation", name, args)
			targets, err := tools.InstructionTargets(name, call.Args, root)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := p.directoryRuntime.Discover(t.Context(), p.directoryRuntime.binding, targets); err != nil {
				t.Fatal(err)
			}
			if !p.directoryInstructionsCovered(call) {
				t.Fatal("initial target should be eligible")
			}
			if err := os.Remove(link); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(b, link); err != nil {
				t.Fatal(err)
			}
			result, err := p.executeSpeculative(t.Context(), call)
			if !errors.Is(err, errDirectorySpeculationDeferred) || result.ExecutionStarted || !result.ExecStartedAt.IsZero() {
				t.Fatalf("speculation was not deferred before execution: %+v, %v", result, err)
			}
			if result.DirectoryDiscovery != nil || p.directoryRuntime.attempt.Load().gate.Reason() != "" {
				t.Fatal("speculation published a discovery or closed the response gate")
			}
			result, err = p.execute(t.Context(), call, false)
			if name == tools.NameRead {
				if err != nil || !result.ExecutionStarted {
					t.Fatalf("formal read failed: %v", err)
				}
			} else if err == nil || result.ExecutionStarted {
				t.Fatal("formal mutation ran before the rules were visible")
			}
			if result.ControlReason != string(directoryInstructionStopRequired) || result.DirectoryDiscovery == nil {
				t.Fatalf("formal dispatch lost discovery: %+v", result)
			}
			data, err := os.ReadFile(filepath.Join(b, "sample.txt"))
			if err != nil || string(data) != "original\n" {
				t.Fatalf("unseen directory was modified: %q, %v", data, err)
			}
		})
	}
}

func TestDirectoryInstructionRestoreRetiresChangedScopeIdentity(t *testing.T) {
	for _, change := range []string{"nested", "external", "internal", "loop"} {
		t.Run(change, func(t *testing.T) {
			_, root := newResolverFixture(t)
			dir := filepath.Join(root, "module")
			writeResolverRule(t, dir, "Original module rules.")
			old := message.DirectoryInstructionFact{CheckoutRoot: root, Scope: "module", Operation: message.DirectoryInstructionUpsert, SourcePath: filepath.Join(dir, "AGENTS.md"), Body: "Original module rules.", BodyHash: message.DirectoryInstructionBodyHash("Original module rules.")}
			history := []message.Message{directoryInstructionFactMessage([]message.DirectoryInstructionFact{old})}
			if change == "nested" {
				if err := os.Mkdir(filepath.Join(dir, ".git"), 0755); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.RemoveAll(dir); err != nil {
					t.Fatal(err)
				}
				target := dir
				if change == "external" {
					target = t.TempDir()
				}
				if change == "internal" {
					target = filepath.Join(root, "other")
					writeResolverRule(t, target, "Current module rules.")
				}
				if err := os.Symlink(target, dir); err != nil {
					t.Fatal(err)
				}
			}
			r, facts, err := prepareDirectoryInstructions(t.Context(), directoryInstructionBinding{CheckoutRoot: root, ContentRoot: root, WorkDir: root}, history, nil, "")
			if err != nil {
				t.Fatal(err)
			}
			defer r.cancel()
			var retired bool
			for _, fact := range facts {
				if fact.Scope == old.Scope {
					want := message.DirectoryInstructionRemove
					if change == "loop" {
						want = message.DirectoryInstructionBlocked
					}
					if fact.Operation != want || fact.Body != "" {
						t.Fatalf("invalid retirement: %+v", fact)
					}
					retired = true
				}
			}
			if !retired {
				t.Fatal("old scope has no durable invalidation")
			}
			history = append(history, directoryInstructionFactMessage(facts))
			v := buildDirectoryInstructionView(r.initial, history, r.binding)
			for _, fact := range v.Dynamic {
				if fact.Scope == old.Scope {
					t.Fatal("old rules survived restore")
				}
			}
			if change == "internal" && (len(v.Dynamic) != 1 || v.Dynamic[0].Scope != "other") {
				t.Fatalf("new canonical chain lost: %+v", v.Dynamic)
			}
			if message.ValidateDirectoryInstructions(history[len(history)-1]) != nil {
				t.Fatal("invalid reconciliation payload")
			}
		})
	}
}
