package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func directoryPipelineFixture(t *testing.T) (toolExecutionPipeline, string) {
	t.Helper()
	r, root := newResolverFixture(t)
	d := discoverResolverDirectory(t, r, root)
	r.initial = directoryInstructionSnapshot{Observations: d.Observations}
	view := buildDirectoryInstructionView(r.initial, nil, r.binding)
	r.view.Store(&view)
	r.attempt.Store(&directoryInstructionAttempt{request: directoryInstructionRequest{Binding: r.binding, RequestSeq: 1, AttemptSeq: 1, Visible: make(map[directoryInstructionVersion]struct{})}})
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

// Directory rules are prompt material: a read that discovers new rules must not
// change what the rest of the response is allowed to do.
func TestDirectoryInstructionsReadThenWriteStillExecutes(t *testing.T) {
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
	write, err := p.execute(context.Background(), directoryCall("write", tools.NameWrite, map[string]string{"path": path, "content": "changed"}), false)
	if err != nil || !write.ExecutionStarted {
		t.Fatalf("write=%+v err=%v", write, err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "changed" {
		t.Fatalf("file not written: %q err=%v", got, err)
	}
}

// Speculative reads commit through the same result path as ordinary reads, so
// a streamed read must discover the rules of the directory it actually read.
func TestDirectoryInstructionsSpeculativeReadDiscoversRules(t *testing.T) {
	p, root := directoryPipelineFixture(t)
	dir := filepath.Join(root, "module")
	writeResolverRule(t, dir, "Use the module workflow.")
	path := filepath.Join(dir, "sample.txt")
	if err := os.WriteFile(path, []byte("sample"), 0644); err != nil {
		t.Fatal(err)
	}
	result, err := p.executeSpeculative(context.Background(), directoryCall("read", tools.NameRead, map[string]string{"path": path}))
	if err != nil {
		t.Fatalf("speculative read failed: %v", err)
	}
	if result.DirectoryDiscovery == nil {
		t.Fatal("speculative read did not discover directory rules")
	}
	if o := result.DirectoryDiscovery.Observations["module"]; o.Status != "present" || o.Body != "Use the module workflow." {
		t.Fatalf("module rule not discovered: %+v", result.DirectoryDiscovery.Observations)
	}
}

// The streaming speculative path must use the same discovery result as a
// directly executed read; otherwise a read promoted from the stream silently
// loses its directory rules.
func TestDirectoryInstructionsStreamingReadPromotesDiscovery(t *testing.T) {
	p, root := directoryPipelineFixture(t)
	dir := filepath.Join(root, "module")
	writeResolverRule(t, dir, "Use the module workflow.")
	path := filepath.Join(dir, "sample.txt")
	if err := os.WriteFile(path, []byte("sample"), 0644); err != nil {
		t.Fatal(err)
	}
	exec := NewStreamingToolExecutor(7, t.Context(), nil, p.executeSpeculative)
	call := directoryCall("read", tools.NameRead, map[string]string{"path": path})
	if !exec.Start(call) {
		t.Fatal("Start returned false")
	}
	result, ok, drift := exec.Promote(call)
	if drift || !ok || result == nil {
		t.Fatalf("Promote result=%+v ok=%v drift=%v", result, ok, drift)
	}
	if result.DirectoryDiscovery == nil {
		t.Fatal("streamed read did not discover directory rules")
	}
	if o := result.DirectoryDiscovery.Observations["module"]; o.Status != "present" || o.Body != "Use the module workflow." {
		t.Fatalf("module rule not discovered: %+v", result.DirectoryDiscovery.Observations)
	}
}

// Rules reach the model only when a file inside a directory is read. A patch
// that creates files in two directories applies unchanged and discovers nothing.
func TestDirectoryInstructionsPatchAppliesWithoutDiscovery(t *testing.T) {
	p, root := directoryPipelineFixture(t)
	writeResolverRule(t, filepath.Join(root, "deep"), "Review changes here.")
	patch := "*** Begin Patch\n*** Add File: first.txt\n+first\n*** Add File: deep/last.txt\n+last\n*** End Patch"
	result, err := p.execute(context.Background(), directoryCall("patch", tools.NameApplyPatch, map[string]string{"patch": patch}), false)
	if err != nil || !result.ExecutionStarted {
		t.Fatalf("patch=%+v err=%v", result, err)
	}
	for path, want := range map[string]string{"first.txt": "first\n", "deep/last.txt": "last\n"} {
		got, err := os.ReadFile(filepath.Join(root, path))
		if err != nil || string(got) != want {
			t.Fatalf("target %s=%q err=%v", path, got, err)
		}
	}
	if result.DirectoryDiscovery != nil {
		t.Fatalf("non-read tool discovered directory rules: %+v", result.DirectoryDiscovery)
	}
}

// An unverified read identity cannot name the directory that was read, so it
// must not attribute rules to it — and must not disturb the read itself.
func TestDirectoryInstructionsUnstableReadIdentitySkipsDiscovery(t *testing.T) {
	p, root := directoryPipelineFixture(t)
	p = p.directoryAttemptSnapshot()
	result := ToolExecutionResult{ExecutionStarted: true, Result: "read succeeded"}
	p.discoverAfterToolSuccess(context.Background(), directoryCall("read", tools.NameRead, map[string]string{"path": filepath.Join(root, "sample.txt")}), tools.ReadObservation{Path: filepath.Join(root, "sample.txt"), SHA256: "sample"}, &result)
	if result.Result != "read succeeded" || !result.ExecutionStarted || result.DirectoryDiscovery != nil || len(result.Notes) != 0 {
		t.Fatalf("unexpected result: %+v", result)
	}
}

// A rule file that cannot be loaded never stops a write: the write neither
// reads nor discovers rules, so it cannot be affected by one.
func TestDirectoryInstructionsUnreadableRuleDoesNotBlockWrite(t *testing.T) {
	p, root := directoryPipelineFixture(t)
	dir := filepath.Join(root, "module")
	// A directory named AGENTS.md cannot be opened as a regular file.
	if err := os.MkdirAll(filepath.Join(dir, "AGENTS.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "new.txt")
	result, err := p.execute(context.Background(), directoryCall("write", tools.NameWrite, map[string]string{"path": target, "content": "sample"}), false)
	if err != nil || !result.ExecutionStarted {
		t.Fatalf("write blocked by directory rules: %+v, %v", result, err)
	}
	if got, err := os.ReadFile(target); err != nil || string(got) != "sample" {
		t.Fatalf("file=%q err=%v", got, err)
	}
	if result.DirectoryDiscovery != nil {
		t.Fatalf("write discovered directory rules: %+v", result.DirectoryDiscovery)
	}
}

// A root AGENTS.md symlinked to a shared source outside the checkout loads from
// that source, is attributed to the scope holding the link, and leaves the rest
// of the response free to run.
func TestDirectoryInstructionsExternalRuleSymlinkLoadsAndKeepsResponseOpen(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	outside, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	shared := filepath.Join(outside, "shared-AGENTS.md")
	if err := os.WriteFile(shared, []byte("shared rule"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(shared, filepath.Join(root, "AGENTS.md")); err != nil {
		t.Fatal(err)
	}
	binding := directoryInstructionBinding{CheckoutRoot: root, ContentRoot: root, WorkDir: root, FallbackAllowed: true}
	r := newDirectoryInstructionRuntime(context.Background(), binding)
	t.Cleanup(r.cancel)
	d, err := r.Discover(context.Background(), binding, []tools.InstructionTarget{{Path: root, Mode: tools.InstructionTargetDirectory}})
	if err != nil {
		t.Fatal(err)
	}
	r.initial = directoryInstructionSnapshot{Observations: d.Observations}
	view := buildDirectoryInstructionView(r.initial, nil, binding)
	r.view.Store(&view)
	r.attempt.Store(&directoryInstructionAttempt{request: directoryInstructionRequest{Binding: binding, RequestSeq: 1, AttemptSeq: 1, Visible: make(map[directoryInstructionVersion]struct{})}})
	registry := tools.NewRegistry()
	registry.Register(tools.ReadTool{})
	registry.Register(tools.WriteTool{})
	p := toolExecutionPipeline{agentID: "sample", registry: registry, toolBaseDir: root, directoryRuntime: r}
	path := filepath.Join(root, "sample.txt")
	if err := os.WriteFile(path, []byte("original"), 0644); err != nil {
		t.Fatal(err)
	}
	read, err := p.execute(context.Background(), directoryCall("read", tools.NameRead, map[string]string{"path": path}), false)
	if err != nil || !read.ExecutionStarted {
		t.Fatalf("read=%+v err=%v", read, err)
	}
	if read.DirectoryDiscovery == nil {
		t.Fatal("read did not discover directory rules")
	}
	if o := read.DirectoryDiscovery.Observations["."]; o.Status != "present" || o.Body != "shared rule" || o.SourcePath != shared {
		t.Fatalf("external rule not loaded: %+v", read.DirectoryDiscovery.Observations)
	}
	write, err := p.execute(context.Background(), directoryCall("write", tools.NameWrite, map[string]string{"path": path, "content": "changed"}), false)
	if err != nil || !write.ExecutionStarted {
		t.Fatalf("sibling write=%+v err=%v", write, err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "changed" {
		t.Fatalf("file=%q err=%v", got, err)
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
		if err != nil {
			t.Fatalf("read=%+v err=%v", result, err)
		}
		p.directoryRuntime.collectDiscovery(result.DirectoryDiscovery)
	}
	if len(p.directoryRuntime.pending) != 0 {
		t.Fatal("empty known chain published facts")
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

func TestDirectoryInstructionsGrepMissingPathKeepsResponseOpen(t *testing.T) {
	p, root := directoryPipelineFixture(t)
	p = p.directoryAttemptSnapshot()
	present := filepath.Join(root, "present.go")
	if err := os.WriteFile(present, []byte("sample"), 0644); err != nil {
		t.Fatal(err)
	}
	call := directoryCall("grep", tools.NameGrep, map[string]any{"pattern": "sample", "paths": []string{filepath.Join(root, "missing.go"), "present.go"}})
	var result ToolExecutionResult
	p.discoverAfterToolSuccess(context.Background(), call, tools.ReadObservation{}, &result)
	if len(result.Notes) != 0 {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestDirectoryInstructionsGrepExpansionFailureKeepsResponseOpen(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix HOME resolution")
	}
	p, root := directoryPipelineFixture(t)
	p.registry.Register(tools.GrepTool{})
	present := filepath.Join(root, "present.go")
	if err := os.WriteFile(present, []byte("sample"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", "")
	call := directoryCall("search", tools.NameGrep, map[string]any{"pattern": "sample", "paths": []string{"~/missing.go", present}})
	result, err := p.execute(context.Background(), call, false)
	if err != nil || !result.ExecutionStarted {
		t.Fatalf("grep=%+v err=%v", result, err)
	}
	next, err := p.execute(context.Background(), directoryCall("write", tools.NameWrite, map[string]string{"path": present, "content": "updated"}), false)
	if err != nil || !next.ExecutionStarted {
		t.Fatalf("sibling write=%+v err=%v", next, err)
	}
}
