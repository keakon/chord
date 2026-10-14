package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestShellReadOnlyViaToolArgs(t *testing.T) {
	tool := NewShellTool("bash")
	mustArgs := func(v any) json.RawMessage {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		return raw
	}
	if !tool.ConcurrencySafeReadOnly(mustArgs(map[string]any{"command": "git log | head -20"})) {
		t.Fatal("pipeline of reads must be batch-safe")
	}
	if policy := tool.ConcurrencyPolicy(mustArgs(map[string]any{"command": "git log | head -20"})); policy.Mode != ConcurrencyModeRead || policy.Resource != "process:shell" || policy.AbortSiblingsOnError {
		t.Fatalf("read-only policy = %#v, want process:shell read without sibling aborts", policy)
	}
	if tool.ConcurrencySafeReadOnly(mustArgs(map[string]any{"command": "git log && rm -rf x"})) {
		t.Fatal("pipeline with a write must not be batch-safe")
	}
	if policy := tool.ConcurrencyPolicy(mustArgs(map[string]any{"command": "git log && rm -rf x"})); policy.Mode != ConcurrencyModeExclusive || !policy.AbortSiblingsOnError {
		t.Fatalf("mutating policy = %#v, want exclusive with sibling aborts", policy)
	}
	// Malformed args fail closed.
	if tool.ConcurrencySafeReadOnly(json.RawMessage(`{`)) {
		t.Fatal("malformed args must not be batch-safe")
	}
	if got := shellReadOnlyArgsVerdict(json.RawMessage(`{`), "bash"); got.ReadOnly {
		t.Fatal("malformed args verdict must be false")
	}
}

func TestShellReadOnlyLeaseMatrix(t *testing.T) {
	registry := NewRegistry()
	registry.Register(NewShellTool("bash"))
	registry.Register(ReadTool{BaseDir: "/tmp"})
	policy := func(name, args string) ConcurrencyPolicy {
		tool, ok := registry.Get(name)
		if !ok {
			t.Fatalf("missing policy fixture: %s", name)
		}
		return PolicyForInstance(tool, name, json.RawMessage(args))
	}
	readShell := policy(NameShell, `{"command":"git log --oneline -20"}`)
	mutatingShell := policy(NameShell, `{"command":"go test ./..."}`)
	fileRead := policy(NameRead, `{"path":"README.md"}`)
	fileWrite := ConcurrencyPolicy{Resource: "file:README.md", Mode: ConcurrencyModeWrite}

	if readShell.Mode != ConcurrencyModeRead || readShell.Resource != "process:shell" {
		t.Fatalf("read-only shell policy = %#v, want process:shell read", readShell)
	}
	// Read-only shells batch with file reads and stay clear of file writes
	// on the batch path; across agents the lease still serializes a read
	// against a mutating shell on the same process resource.
	if ConcurrencyConflict(readShell, fileRead) {
		t.Fatal("process-read vs file-read must not conflict")
	}
	if ConcurrencyConflict(readShell, fileWrite) {
		t.Fatal("process-read vs file-write must not conflict on the batch path")
	}
	if !ConcurrencyConflict(readShell, mutatingShell) {
		t.Fatal("read-only shell must still conflict with a mutating shell")
	}
	if !WorkspaceLeaseConflict(readShell, mutatingShell) {
		t.Fatal("read-only shell lease must conflict with a mutating shell")
	}
	if WorkspaceLeaseConflict(readShell, fileRead) {
		t.Fatal("shell lease must not conflict with an unrelated file read")
	}
}

func TestShellReadOnlyFailureIsRealErrorWithoutSiblingAbort(t *testing.T) {
	// Read-only is not successful: grep with no match exits 1 and surfaces a
	// real terminal error. The batch still must not arm sibling cancellation
	// for it, which is exactly what AbortSiblingsOnError=false promises the
	// batch executor below (see TestBuildToolExecutionBatchesMergesRg...).
	resetJobRegistryOnlyForTest(t)
	t.Cleanup(func() { StopAllJobsForShutdown() })
	path := filepath.Join(t.TempDir(), "haystack.txt")
	if err := os.WriteFile(path, []byte("hay\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	command := "grep -r chord-will-not-match-anything " + path
	tool := NewShellTool("bash")
	args := json.RawMessage(`{"command":` + strconv.Quote(command) + `}`)
	if !tool.ConcurrencySafeReadOnly(args) {
		t.Fatalf("%q must classify read-only", command)
	}
	if policy := tool.ConcurrencyPolicy(args); policy.Mode != ConcurrencyModeRead || policy.AbortSiblingsOnError {
		t.Fatalf("read-only failure policy = %#v, want read without sibling aborts", policy)
	}
	if _, err := tool.Execute(context.Background(), args); err == nil {
		t.Fatalf("%q must exit nonzero (no match), got nil error", command)
	}
}
