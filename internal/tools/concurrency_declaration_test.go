package tools

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestReadOnlyBatchableToolsDeclareConcurrencyPolicy pins the two-layer
// declaration contract. ConcurrencySafeReadOnly alone decides the batching
// class (and with it the started-journal skip and the speculative policy), but a
// finalized batch only merges calls whose ConcurrencyPolicy stays
// non-exclusive. A tool that declares the class without a policy therefore
// advertises parallelism it can never reach, and silently becomes a
// serialization boundary for every later call in the same message. The scan
// covers the package sources so a newly added tool cannot skip the policy.
func TestReadOnlyBatchableToolsDeclareConcurrencyPolicy(t *testing.T) {
	declared, aware := scanReadOnlyClassDeclarations(t)
	if len(declared) == 0 {
		t.Fatal("no ConcurrencySafeReadOnly implementations found; the source scan is broken")
	}
	for recv, file := range declared {
		if !aware[recv] {
			t.Errorf("%s: %s declares ConcurrencySafeReadOnly without ConcurrencyPolicy; give it a non-exclusive policy scoped to what the call touches", file, recv)
		}
	}
}

// scanReadOnlyClassDeclarations parses the package sources (test files excluded)
// and reports the receiver types declaring ConcurrencySafeReadOnly, mapped to
// the file that declares the class, plus the receiver types declaring
// ConcurrencyPolicy. Both the presence check and the coverage check in
// TestReadOnlyBatchableToolPoliciesStayNonExclusive build on it, so the two
// cannot drift into scanning different sources.
func scanReadOnlyClassDeclarations(t *testing.T) (declared map[string]string, aware map[string]bool) {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob package sources: %v", err)
	}
	declared = map[string]string{}
	aware = map[string]bool{}
	fset := token.NewFileSet()
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		for _, decl := range parsed.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || len(fn.Recv.List) == 0 {
				continue
			}
			recv := receiverTypeName(fn.Recv.List[0].Type)
			if recv == "" {
				continue
			}
			switch fn.Name.Name {
			case "ConcurrencySafeReadOnly":
				declared[recv] = file
			case "ConcurrencyPolicy":
				aware[recv] = true
			}
		}
	}
	return declared, aware
}

// TestReadOnlyBatchableToolPoliciesStayNonExclusive asserts, for every tool that
// declares the read-only batching class, that a representative invocation stays
// non-exclusive. The AST scan cannot call a policy, so the table must cover the
// whole class: an uncovered tool would keep the exclusive default and silently
// turn every message it appears in into a serialization boundary.
func TestReadOnlyBatchableToolPoliciesStayNonExclusive(t *testing.T) {
	cases := []struct {
		name string
		tool Tool
		args string
	}{
		{NameJobOutput, JobOutputTool{}, `{"job_id":"job-1"}`},
		{NameJobList, JobListTool{}, `{}`},
		{NameReadArtifact, ReadArtifactTool{}, `{"path":"artifacts/report.md"}`},
		{NameViewImage, &ViewImageTool{BaseDir: "/tmp"}, `{"path":"shot.png"}`},
		{NameSkill, SkillTool{}, `{"name":"demo"}`},
		{NameShell, NewShellTool("bash"), `{"command":"git status"}`},
		{NameRead, ReadTool{BaseDir: "/tmp"}, `{"path":"README.md"}`},
		{NameGrep, GrepTool{BaseDir: "/tmp"}, `{"pattern":"TODO","paths":["."]}`},
		{NameGlob, GlobTool{BaseDir: "/tmp"}, `{"path":"."}`},
		{NameWebFetch, WebFetchTool{}, `{"url":"https://example.invalid/page"}`},
		{NameLsp, LspTool{BaseDir: "/tmp"}, `{"path":"main.go"}`},
		{NameWorktreeList, WorktreeListTool{}, `{}`},
	}
	declared, _ := scanReadOnlyClassDeclarations(t)
	covered := make(map[string]string, len(cases)) // receiver type -> case name
	for _, tc := range cases {
		covered[concreteToolTypeName(tc.tool)] = tc.name
		t.Run(tc.name, func(t *testing.T) {
			registry := NewRegistry()
			registry.Register(tc.tool)
			policy := PolicyForTool(registry, tc.name, json.RawMessage(tc.args))
			if policy.Mode == ConcurrencyModeExclusive {
				t.Fatalf("policy = %#v, want a non-exclusive mode", policy)
			}
			if strings.TrimSpace(policy.Resource) == "" {
				t.Fatalf("policy = %#v, want a named resource", policy)
			}
		})
	}
	for recv, file := range declared {
		if _, ok := covered[recv]; !ok {
			t.Errorf("%s: %s declares ConcurrencySafeReadOnly but has no representative invocation here; add one so its policy is asserted non-exclusive", file, recv)
		}
	}
	for recv, name := range covered {
		if _, ok := declared[recv]; !ok {
			t.Errorf("%s (%s) no longer declares ConcurrencySafeReadOnly; drop this table entry", name, recv)
		}
	}
}

// concreteToolTypeName resolves a tool value to the bare receiver type name the
// source scan reports: *T reports T, without the package qualifier or any type
// arguments.
func concreteToolTypeName(tool Tool) string {
	name := strings.TrimPrefix(reflect.TypeOf(tool).String(), "*")
	if idx := strings.LastIndexByte(name, '.'); idx >= 0 {
		name = name[idx+1:]
	}
	if idx := strings.IndexByte(name, '['); idx >= 0 {
		name = name[:idx]
	}
	return name
}

func TestJobOutputPolicyScopesResourceByJob(t *testing.T) {
	registry := NewRegistry()
	registry.Register(JobOutputTool{})
	policy := func(jobID string) ConcurrencyPolicy {
		return PolicyForTool(registry, NameJobOutput, json.RawMessage(`{"job_id":"`+jobID+`"}`))
	}

	first := policy("job-1")
	if first.Resource != "job:job-1" || first.Mode != ConcurrencyModeRead {
		t.Fatalf("policy = %#v, want a read of job:job-1", first)
	}
	if ConcurrencyConflict(first, policy("job-2")) {
		t.Fatal("reads of different jobs must batch together")
	}
	if empty := PolicyForTool(registry, NameJobOutput, json.RawMessage(`{}`)); empty.Mode != ConcurrencyModeExclusive {
		t.Fatalf("policy without a job id = %#v, want the conservative exclusive default", empty)
	}
}

func TestShellPolicySplitsReadOnlyCommandsFromMutations(t *testing.T) {
	registry := NewRegistry()
	registry.Register(NewShellTool("bash"))
	readOnly := PolicyForTool(registry, NameShell, json.RawMessage(`{"command":"git status"}`))
	if readOnly.Resource != "process:shell" || readOnly.Mode != ConcurrencyModeRead || readOnly.AbortSiblingsOnError {
		t.Fatalf("read-only shell policy = %#v, want process:shell read without sibling aborts", readOnly)
	}
	mutating := PolicyForTool(registry, NameShell, json.RawMessage(`{"command":"go test ./..."}`))
	if mutating.Resource != "process:shell" || mutating.Mode != ConcurrencyModeExclusive || !mutating.AbortSiblingsOnError {
		t.Fatalf("mutating shell policy = %#v, want the exclusive process:shell with sibling aborts", mutating)
	}
	if !ConcurrencyConflict(readOnly, mutating) {
		t.Fatal("a mutating shell must still exclude an allowlisted read-only one")
	}
}

// receiverTypeName resolves T, *T, and generic receivers to the bare type name.
func receiverTypeName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return receiverTypeName(t.X)
	case *ast.IndexExpr:
		return receiverTypeName(t.X)
	case *ast.IndexListExpr:
		return receiverTypeName(t.X)
	default:
		return ""
	}
}
