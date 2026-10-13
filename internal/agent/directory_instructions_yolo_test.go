package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

func TestDirectoryInstructionsMainYoloMatchesDispatch(t *testing.T) {
	for _, action := range []permission.Action{permission.ActionAllow, permission.ActionAsk, permission.ActionDeny} {
		for _, hasRules := range []bool{false, true} {
			t.Run(string(action)+map[bool]string{false: "/absent", true: "/present"}[hasRules], func(t *testing.T) {
				root, err := filepath.EvalSymlinks(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				if hasRules {
					if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("Use project conventions."), 0600); err != nil {
						t.Fatal(err)
					}
				}
				rules := permission.Ruleset{{Permission: tools.NameRead, Pattern: "*", Action: action}}
				scope := permission.PathScope{Cwd: root}
				a := &MainAgent{ruleset: rules, cachedWorkDir: root}
				a.yoloEnabled.Store(true)
				check, identity := a.directoryInstructionReadPermission()
				runtime, _, err := prepareDirectoryInstructions(t.Context(), directoryInstructionBinding{AgentID: "main", ContentRoot: root, WorkDir: root}, nil, check, identity)
				if err != nil {
					t.Fatal(err)
				}
				defer runtime.cancel()
				observation := runtime.initial.Observations["."]
				want := "absent"
				if hasRules {
					want = "present"
				}
				if observation.Status != want {
					t.Fatalf("initial rule = %+v, want %s", observation, want)
				}
				pipeline := yoloPipelineForRuleset(rules, false)
				pipeline.directoryRuntime = runtime
				pipeline.directoryRequest = &directoryInstructionRequest{Binding: runtime.binding, Visible: map[directoryInstructionVersion]struct{}{}}
				if hasRules {
					pipeline.directoryRequest.Visible[directoryInstructionFactFromObservation(runtime.binding, ".", observation).Ref()] = struct{}{}
				}
				pipeline.directoryGate = &directoryInstructionGate{}
				pipeline.toolBaseDir = runtime.binding.WorkDir
				pipeline.pathScope = scope
				_, dispatchIdentity := pipeline.directoryInstructionReadPermission()
				if dispatchIdentity != identity {
					t.Fatal("preparation and dispatch permission identities differ")
				}
				call := message.ToolCall{ID: "call-1", Name: tools.NameWrite, Args: json.RawMessage(`{"path":"sample.txt","content":"sample"}`)}
				if err := pipeline.applyPermission(t.Context(), &call, &ToolExecutionResult{}); err != nil {
					t.Fatal(err)
				}
				if err := pipeline.discoverToolDirectoryInstructions(t.Context(), call, &ToolExecutionResult{}); err != nil {
					t.Fatalf("allowed write blocked: %v", err)
				}
				if !pipeline.directoryInstructionsCovered(call) {
					t.Fatal("discovered write lost speculative coverage")
				}
				// A directory absent from the initial snapshot must use the same permission.
				nested := filepath.Join(root, "nested")
				if err := os.Mkdir(nested, 0700); err != nil {
					t.Fatal(err)
				}
				call.Args = json.RawMessage(`{"path":"nested/sample.txt","content":"sample"}`)
				if err := pipeline.discoverToolDirectoryInstructions(t.Context(), call, &ToolExecutionResult{}); err != nil {
					t.Fatalf("dynamic directory blocked: %v", err)
				}
			})
		}
	}
}

func TestDirectoryInstructionsWorkerYoloPreservesDeny(t *testing.T) {
	for _, action := range []permission.Action{permission.ActionAllow, permission.ActionAsk, permission.ActionDeny} {
		rules := permission.Ruleset{{Permission: tools.NameRead, Pattern: "*", Action: action}}
		p := toolExecutionPipeline{currentRuleset: func() permission.Ruleset { return rules }, yoloDowngradeAsk: func(string) bool { return true }, pathScope: permission.PathScope{Cwd: t.TempDir()}}
		check, _ := p.directoryInstructionReadPermission()
		err := check(t.Context(), filepath.Join(p.pathScope.Cwd, "AGENTS.md"))
		if (err == nil) != (action != permission.ActionDeny) {
			t.Fatalf("worker action %s: %v", action, err)
		}
	}
}
