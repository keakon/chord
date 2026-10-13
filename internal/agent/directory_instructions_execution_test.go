package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/keakon/chord/internal/hook"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

func directoryPipelineFixture(t *testing.T) (toolExecutionPipeline, string) {
	t.Helper()
	r, root := newResolverFixture(t)
	d := discoverResolverDirectory(t, r, root)
	r.initial = directoryInstructionSnapshot{Observations: d.Observations}
	view := buildDirectoryInstructionView(r.initial, nil, r.binding)
	r.view.Store(&view)
	r.attempt.Store(&directoryInstructionAttempt{request: directoryInstructionRequest{Binding: r.binding, RequestSeq: 1, AttemptSeq: 1, Visible: make(map[directoryInstructionVersion]struct{})}, gate: &directoryInstructionGate{}})
	registry := tools.NewRegistry()
	registry.Register(tools.ReadTool{})
	registry.Register(tools.WriteTool{})
	registry.Register(tools.ApplyPatchTool{})
	return toolExecutionPipeline{agentID: "sample", registry: registry, toolBaseDir: root, directoryRuntime: r}, root
}
func directoryCall(id, name string, args any) message.ToolCall {
	data, _ := json.Marshal(args)
	return message.ToolCall{ID: id, Name: name, Args: data}
}
func TestDirectoryInstructionsReadThenWriteRequiresNewRequest(t *testing.T) {
	p, root := directoryPipelineFixture(t)
	dir := filepath.Join(root, "module")
	writeResolverRule(t, dir, "Use the module workflow.")
	path := filepath.Join(dir, "sample.txt")
	if err := os.WriteFile(path, []byte("original"), 0644); err != nil {
		t.Fatal(err)
	}
	read, err := p.execute(context.Background(), directoryCall("read", tools.NameRead, map[string]string{"path": path}), false)
	if err != nil || !read.ExecutionStarted || read.DirectoryDiscovery == nil {
		t.Fatalf("read=%+v err=%v", read, err)
	}
	if read.ControlReason != string(directoryInstructionStopRequired) {
		t.Fatalf("reason=%s", read.ControlReason)
	}
	write, err := p.execute(context.Background(), directoryCall("write", tools.NameWrite, map[string]string{"path": path, "content": "changed"}), false)
	if err == nil || write.ExecutionStarted || write.ControlReason != string(directoryInstructionStopReplan) {
		t.Fatalf("write=%+v err=%v", write, err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "original" {
		t.Fatalf("file changed: %s", got)
	}
	// Committing a discovered fact does not amend the producing request's rights.
	p.directoryRuntime.collectDiscovery(read.DirectoryDiscovery)
	visible := make(map[directoryInstructionVersion]struct{})
	for scope, o := range read.DirectoryDiscovery.Observations {
		if o.Status == "present" {
			visible[directoryInstructionFactFromObservation(p.directoryRuntime.binding, scope, o).Ref()] = struct{}{}
		}
	}
	p.directoryRuntime.attempt.Store(&directoryInstructionAttempt{request: directoryInstructionRequest{Binding: p.directoryRuntime.binding, RequestSeq: 2, Visible: visible}, gate: &directoryInstructionGate{}})
	write, err = p.execute(context.Background(), directoryCall("write-next", tools.NameWrite, map[string]string{"path": path, "content": "changed"}), false)
	if err != nil || !write.ExecutionStarted {
		t.Fatalf("new request write=%+v err=%v", write, err)
	}
}
func TestDirectoryInstructionsPatchPreflightsLastTargetBeforeAnyMutation(t *testing.T) {
	p, root := directoryPipelineFixture(t)
	writeResolverRule(t, filepath.Join(root, "deep"), "Review changes here.")
	patch := "*** Begin Patch\n*** Add File: first.txt\n+first\n*** Add File: deep/last.txt\n+last\n*** End Patch"
	result, err := p.execute(context.Background(), directoryCall("patch", tools.NameApplyPatch, map[string]string{"patch": patch}), false)
	if err == nil || result.ExecutionStarted || !result.ExecStartedAt.IsZero() {
		t.Fatalf("patch=%+v err=%v", result, err)
	}
	for _, path := range []string{"first.txt", "deep/last.txt"} {
		if _, err := os.Stat(filepath.Join(root, path)); !os.IsNotExist(err) {
			t.Fatalf("target %s changed: %v", path, err)
		}
	}
}

func TestDirectoryInstructionsClosedGateSkipsPermissionAndHook(t *testing.T) {
	p, root := directoryPipelineFixture(t)
	p.directoryRuntime.attempt.Load().gate.Close(directoryInstructionStopRequired)
	permissionChecked, hookCalled := false, false
	p.currentRuleset = func() permission.Ruleset { permissionChecked = true; return nil }
	p.fireHook = func(context.Context, string, uint64, string, map[string]any) (*hook.Result, error) {
		hookCalled = true
		return nil, nil
	}
	result, err := p.execute(context.Background(), directoryCall("write", tools.NameWrite, map[string]string{"path": filepath.Join(root, "sample.txt"), "content": "sample"}), true)
	if err == nil || result.ExecutionStarted || permissionChecked || hookCalled || result.ControlReason != string(directoryInstructionStopReplan) {
		t.Fatalf("result=%+v err=%v permission=%v hook=%v", result, err, permissionChecked, hookCalled)
	}
}
func TestDirectoryInstructionsUnstableReadIdentityPreservesSuccess(t *testing.T) {
	p, root := directoryPipelineFixture(t)
	p = p.directoryAttemptSnapshot()
	result := ToolExecutionResult{ExecutionStarted: true, Result: "read succeeded"}
	p.discoverAfterSuccessfulRead(context.Background(), directoryCall("read", tools.NameRead, map[string]string{"path": filepath.Join(root, "sample.txt")}), tools.ReadObservation{Path: filepath.Join(root, "sample.txt"), SHA256: "sample"}, &result)
	if result.Result != "read succeeded" || !result.ExecutionStarted || result.ControlReason != string(directoryInstructionStopBlocked) || p.directoryGate.Reason() != directoryInstructionStopBlocked || result.DirectoryDiscovery == nil || result.DirectoryDiscovery.Complete {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestDirectoryInstructionsCurrentPermissionOverridesFrozenBody(t *testing.T) {
	p, root := directoryPipelineFixture(t)
	dir := filepath.Join(root, "module")
	writeResolverRule(t, dir, "module rule")
	d := discoverResolverDirectory(t, p.directoryRuntime, dir)
	visible := map[directoryInstructionVersion]struct{}{}
	for scope, o := range d.Observations {
		if o.Status == "present" {
			visible[directoryInstructionFactFromObservation(p.directoryRuntime.binding, scope, o).Ref()] = struct{}{}
		}
	}
	p.directoryRuntime.attempt.Store(&directoryInstructionAttempt{request: directoryInstructionRequest{Binding: p.directoryRuntime.binding, RequestSeq: 2, Visible: visible}, gate: &directoryInstructionGate{}})
	p.currentRuleset = func() permission.Ruleset {
		return permission.Ruleset{{Permission: "*", Pattern: "*", Action: permission.ActionAllow}, {Permission: tools.NameRead, Pattern: "*AGENTS.md", Action: permission.ActionDeny}}
	}
	target := filepath.Join(dir, "new.txt")
	result, err := p.execute(context.Background(), directoryCall("write", tools.NameWrite, map[string]string{"path": target, "content": "sample"}), false)
	if err == nil || result.ExecutionStarted || result.ControlReason != string(directoryInstructionStopBlocked) {
		t.Fatalf("cached body bypassed changed deny: %+v, %v", result, err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("file changed: %v", err)
	}
}

func TestDirectoryInstructionsKnownEmptyChainKeepsResponseOpen(t *testing.T) {
	p, root := directoryPipelineFixture(t)
	path := filepath.Join(root, "sample.txt")
	if err := os.WriteFile(path, []byte("sample"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"read-one", "read-two"} {
		result, err := p.execute(context.Background(), directoryCall(id, tools.NameRead, map[string]string{"path": path}), false)
		if err != nil || result.ControlReason != "" {
			t.Fatalf("read=%+v err=%v", result, err)
		}
		p.directoryRuntime.collectDiscovery(result.DirectoryDiscovery)
	}
	if p.directoryRuntime.attempt.Load().gate.Reason() != "" || len(p.directoryRuntime.pending) != 0 {
		t.Fatal("empty known chain triggered replanning")
	}
}

func TestDirectoryInstructionCandidateBudgetBlocksWholeNewSet(t *testing.T) {
	p, root := directoryPipelineFixture(t)
	writeResolverRule(t, filepath.Join(root, "one"), "First directory rules.")
	writeResolverRule(t, filepath.Join(root, "two"), "Second directory rules.")
	for _, dir := range []string{"one", "two"} {
		d := discoverResolverDirectory(t, p.directoryRuntime, filepath.Join(root, dir))
		d.RequestSeq = 1
		d.AttemptSeq = 1
		p.directoryRuntime.collectDiscovery(&d)
	}
	msg, err := buildDirectoryCandidateMessage(p.directoryRuntime, nil, "system", nil, 16)
	if err != nil || msg == nil || len(msg.DirectoryInstructions.Facts) != 2 {
		t.Fatalf("candidate=%+v err=%v", msg, err)
	}
	for _, f := range msg.DirectoryInstructions.Facts {
		if f.Operation != message.DirectoryInstructionBlocked || f.Reason != message.DirectoryInstructionReasonInputBudget || f.Body == "" {
			t.Fatalf("partial or missing candidate: %+v", f)
		}
	}
}
