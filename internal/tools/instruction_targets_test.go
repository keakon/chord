package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestInstructionTargetsDecoders(t *testing.T) {
	root := t.TempDir()
	for _, test := range []struct {
		name, args, path string
		mode             InstructionTargetMode
	}{{NameRead, `{"path":"file"}`, "file", InstructionTargetContent}, {NameWrite, `{"path":"file","content":"data"}`, "file", InstructionTargetEntry}, {NameEdit, `{"filePath":"file","old_string":"a","new_string":"b"}`, "file", InstructionTargetEntry}, {NameGlob, `{"pattern":"**/*"}`, ".", InstructionTargetDirectory}, {NameGrep, `{"pattern":"sample","path":"."}`, ".", InstructionTargetDirectory}, {NameShell, `{"command":"pwd"}`, ".", InstructionTargetDirectory}, {NameDelete, `{"paths":["file"],"reason":"cleanup"}`, "file", InstructionTargetEntry}} {
		t.Run(test.name, func(t *testing.T) {
			targets, err := InstructionTargets(test.name, json.RawMessage(test.args), root)
			if err != nil {
				t.Fatal(err)
			}
			if len(targets) != 1 || targets[0].Path != filepath.Join(root, test.path) || targets[0].Mode != test.mode {
				t.Fatalf("unexpected targets: %+v", targets)
			}
		})
	}
	for _, name := range []string{NameRead, NameWrite, NameEdit, NameGlob, NameGrep, NameShell, NameDelete, NameApplyPatch} {
		if _, err := InstructionTargets(name, json.RawMessage(`{"path":false}`), root); err == nil {
			t.Fatalf("%s accepted invalid args", name)
		}
	}
}
func TestInstructionTargetsPatchIncludesMoveSourceAndDestination(t *testing.T) {
	root := t.TempDir()
	raw, _ := json.Marshal(map[string]string{"patch": "*** Begin Patch\n*** Update File: a\n*** Move to: b\n@@\n-old\n+new\n*** End Patch"})
	targets, err := InstructionTargets(NameApplyPatch, raw, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 || targets[0].Path != filepath.Join(root, "a") || targets[1].Path != filepath.Join(root, "b") {
		t.Fatalf("targets: %+v", targets)
	}
}
func TestReadObservationActivationPathFollowsSymlink(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "target")
	if err := os.WriteFile(path, []byte("sample"), 0644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	collector := &ReadObservationCollector{}
	_, err := (ReadTool{BaseDir: root}).Execute(WithReadObservationSink(context.Background(), collector), json.RawMessage(`{"path":"link"}`))
	if err != nil {
		t.Fatal(err)
	}
	o, ok := collector.Observation()
	real, _ := filepath.EvalSymlinks(path)
	if !ok || o.Path != link || o.ActivationPath != real {
		t.Fatalf("observation: %+v", o)
	}
}

func TestInstructionTargetsGrepSkipsUnresolvableSearchPaths(t *testing.T) {
	root := t.TempDir()
	present := filepath.Join(root, "present.go")
	if err := os.WriteFile(present, []byte("sample"), 0644); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"pattern": "sample", "paths": []string{"missing.go", "present.go"}})
	targets, err := InstructionTargets(NameGrep, raw, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || targets[0].Path != present || targets[0].Mode != InstructionTargetContent {
		t.Fatalf("targets: %+v", targets)
	}
	raw, _ = json.Marshal(map[string]any{"pattern": "sample", "paths": []string{"missing.go"}})
	targets, err = InstructionTargets(NameGrep, raw, root)
	if err != nil || len(targets) != 0 {
		t.Fatalf("missing-only targets=%+v err=%v", targets, err)
	}
}

func TestInstructionTargetsGrepSkipsPathExpansionFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix HOME resolution")
	}
	root := t.TempDir()
	present := filepath.Join(root, "present.go")
	if err := os.WriteFile(present, []byte("sample"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", "")
	raw, _ := json.Marshal(map[string]any{"pattern": "sample", "paths": []string{"~/missing.go", present}})
	output, err := (GrepTool{BaseDir: root}).Execute(context.Background(), raw)
	if err != nil || !strings.Contains(output, "present.go") {
		t.Fatalf("grep output=%q err=%v; want partial success", output, err)
	}
	targets, err := InstructionTargets(NameGrep, raw, root)
	if err != nil || len(targets) != 1 || targets[0].Path != present || targets[0].Mode != InstructionTargetContent {
		t.Fatalf("targets=%+v err=%v; want only the searched path", targets, err)
	}
	if _, err := InstructionTargetsWithoutIO(NameGrep, raw, root); err == nil {
		t.Fatal("stream admission accepted an unresolved path")
	}
	if _, err := InstructionTargets(NameRead, json.RawMessage(`{"path":"~/missing.go"}`), root); err == nil {
		t.Fatal("read accepted an unresolved path")
	}
}
