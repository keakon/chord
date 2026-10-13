package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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
