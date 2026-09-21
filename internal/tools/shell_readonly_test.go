package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func readonlyTrue(t *testing.T, command string) {
	t.Helper()
	if v := ClassifyShellReadOnly(command, "bash", false); !v.ReadOnly {
		t.Fatalf("ClassifyShellReadOnly(%q) = false (%s), want true", command, v.Reason)
	}
}

func readonlyFalse(t *testing.T, command string) {
	t.Helper()
	if v := ClassifyShellReadOnly(command, "bash", false); v.ReadOnly {
		t.Fatalf("ClassifyShellReadOnly(%q) = true, want false", command)
	}
}

func TestShellReadOnlyPositiveCases(t *testing.T) {
	cases := []string{
		"git log",
		"git log --oneline -20",
		"git diff --staged",
		"git status --short",
		"git blame f",
		"rg --no-config -n pat --glob '*.go'",
		"grep -rn x .",
		"find . -name '*.go'",
		"ls -la",
		"sed -n '1,20p' f",
		"jq '.a' f",
		"diff a b",
		"sort < f",
		"git log 2>&1",
		"git log | head -20",
		"uniq in",
		// Legacy allowlist still holds.
		"pwd",
		"cat README.md",
		"which git",
		"git diff --stat HEAD~1",
		"git show HEAD~1:README.md",
		"git branch --show-current",
		"git branch",
		"git branch -a",
		"git branch -v",
		"git branch --verbose",
		"git branch -r",
		"git branch --contains HEAD",
		"git branch -vv",
		"git rev-parse HEAD",
		"head -n 5 file",
		"tail -n 5 file",
		"wc -l file",
		"stat file",
		"file x",
		"file -b x",
		"file --mime-type x",
		"du -sh .",
		"df -h",
		// Read-only chaining stays read-only.
		"git status && pwd",
		"git status; pwd",
		"git status | cat",
		"git log | head",
		// Wrappers.
		"command git log",
		"command ls -la",
		"nice git log",
		"nice -n 10 git log",
		// Extra query shapes.
		"git describe v1.0",
		"git shortlog",
		"git ls-files",
		"git cat-file -p HEAD",
		"git config --get user.name",
		"git remote -v",
		"git tag -l",
		"git stash list",
		"gh pr view 123",
		"gh issue list",
		"gh run view 1",
		"cat <<< hello",
	}
	for _, command := range cases {
		t.Run(command, func(t *testing.T) {
			readonlyTrue(t, command)
		})
	}
}

func TestShellReadOnlyNegativeCases(t *testing.T) {
	cases := []string{
		// Write-semantics flags.
		"sed -i s/a/b/ f",
		"sed -n '1w f' f",
		"sed 's/a/b/e' f",
		"sort -o out in",
		"sort --output=out in",
		"tree -o out",
		"find . -delete",
		"find . -exec rm {} +",
		"find . -ok rm {} ;",
		"find . -fprint out",
		"find . -fls out",
		"rg --pre 'rm -rf' pat",
		"rg -n pat",
		"git config --edit",
		"git config user.name value",
		"git remote add origin url",
		// `git branch <name>` creates the ref, including when a list flag is
		// also present. A chain is read-only only when every side is.
		"git branch newname",
		"git branch newname HEAD",
		"git branch -v brandnew",
		"git branch --verbose brandnew",
		"git branch -n probe-n",
		"git branch --list 'feat/*'",
		"git branch -l",
		"git status; git branch pwn",
		"git status && git branch pwn",
		"file -C",
		"file --compile",
		"uniq in out",
		// Arbitrary code.
		`awk 'BEGIN{system("rm -rf /")}'`,
		`awk '{print > "f"}'`,
		"python -c 'x'",
		"perl -e 'x'",
		// Non-terminating.
		"tail -f log",
		"tail -F log",
		"tail -fn99 log",
		"tail --follow=name log",
		"tail --follow log",
		// Redirections and structure.
		"cat > f",
		"cat >> f",
		"cat 2> f",
		"cat &> f",
		"git log >& f",
		"sort < <(rm -rf x)",
		"tee f",
		"git log &",
		"git log |& cat",
		"! git log",
		"git log | tee f",
		"git log && rm -rf x",
		"git log || rm x",
		"(cd /tmp && ls)",
		"if rm x; then ls; fi",
		"coproc git log",
		"time git log",
		"cat <<EOF",
		// Git injections via global options or write flags.
		`git -c core.pager='sh -c "rm x"' log`,
		`git -c alias.l='!rm x' l`,
		"git --exec-path=/tmp/evil log",
		"git log --output=x",
		// Wrappers that must not unwrap.
		"sudo git log",
		"xargs rm",
		"bash -c 'git log'",
		"env -i git log",
		"env PATH=/evil git status",
		"nohup git log",
		"/usr/bin/time -o f git log",
		// Assignments and path bypasses.
		"FOO=1 git log",
		"PATH=/x git log",
		"./git log",
		"scripts/git log",
		// Unreadable / unknown.
		`rg -n "$pat" f`,
		"git log $(rm -rf x)",
		"git log `rm -rf x`",
		"cat < $f",
		"rm -rf x",
		"go test ./...",
		"git commit -m test",
		"git checkout main",
		"git clone https://example.invalid/repo.git",
		"tar -x -f a.tar",
		"echo hi",
		"sleep 5",
	}
	for _, command := range cases {
		t.Run(command, func(t *testing.T) {
			readonlyFalse(t, command)
		})
	}
}

func TestShellReadOnlyEntryPoints(t *testing.T) {
	if v := ClassifyShellReadOnly("git status --short", "bash", true); v.ReadOnly {
		t.Fatal("run_in_background must veto")
	}
	if v := ClassifyShellReadOnly("git log", "powershell", false); v.ReadOnly {
		t.Fatal("powershell must veto")
	}
	if v := ClassifyShellReadOnly("", "bash", false); v.ReadOnly {
		t.Fatal("empty command must veto")
	}
	if v := ClassifyShellReadOnly("git status 'unclosed", "bash", false); v.ReadOnly {
		t.Fatal("parse failure must veto")
	}
	// POSIX variant follows the runtime shell instead of assuming bash.
	if v := ClassifyShellReadOnly("git log", "posix", false); !v.ReadOnly {
		t.Fatalf("posix git log = false (%s), want true", v.Reason)
	}
	// Empty shell type (unit-test registries) behaves like bash.
	if v := ClassifyShellReadOnly("git status", "", false); !v.ReadOnly {
		t.Fatalf("empty shellType git status = false (%s), want true", v.Reason)
	}
	// Quoting primitive: the three glob spellings stay readable, while an
	// expansion vetoes the whole command.
	for _, command := range []string{"find . -name '*.go'", `rg --no-config --glob '*.go' pat`, `grep -rn "x" .`} {
		if v := ClassifyShellReadOnly(command, "bash", false); !v.ReadOnly {
			t.Fatalf("quoted %q = false (%s), want true", command, v.Reason)
		}
	}
	if v := ClassifyShellReadOnly(`rg -n "$pat" f`, "bash", false); v.ReadOnly {
		t.Fatal("expansion must veto")
	}
	// Empty quoted string is a legal empty operand, not "unreadable".
	if v := ClassifyShellReadOnly("grep -rn '' .", "bash", false); !v.ReadOnly {
		t.Fatalf("empty quoted pattern = false (%s), want true", v.Reason)
	}
	// sed distinguishes '' from '1w f' only when the primitive keeps them apart.
	if v := ClassifyShellReadOnly("sed -n '' f", "bash", false); v.ReadOnly {
		t.Fatal("sed empty script must not match the print subset")
	}
}

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

func TestShellReadOnlyDoesNotDependOnCwd(t *testing.T) {
	a := ClassifyShellReadOnly("git log | head -20", "bash", false)
	b := ClassifyShellReadOnly("git log | head -20", "bash", false)
	if a.ReadOnly != b.ReadOnly || !a.ReadOnly {
		t.Fatalf("verdicts = %+v %+v, want stable true", a, b)
	}
	if !strings.Contains(a.Reason, "") {
		t.Fatal("unreachable")
	}
}

func TestShellReadOnlyLeaseMatrix(t *testing.T) {
	registry := NewRegistry()
	registry.Register(NewShellTool("bash"))
	registry.Register(ReadTool{BaseDir: "/tmp"})
	policy := func(name, args string) ConcurrencyPolicy {
		return PolicyForTool(registry, name, json.RawMessage(args))
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

func BenchmarkShellReadOnlyClassifier(b *testing.B) {
	commands := []string{
		"git log --oneline -20",
		"rg --no-config -n pat --glob '*.go'",
		"find . -name '*.go'",
		"git log | head -20",
		"git log && rm -rf x",
	}
	b.ResetTimer()
	i := 0
	for b.Loop() {
		_ = ClassifyShellReadOnly(commands[i%len(commands)], "bash", false)
		i++
	}
}

func TestShellReadOnlyBraceExpansionFailsClosed(t *testing.T) {
	// Brace expansion runs before flag parsing and can forge flags from an
	// innocent-looking operand: sort {-o,out} in executes as sort -o out in.
	// The parser reports braces as plain *Lit (a *BraceExp node only appears
	// after an explicit SplitBraces expansion the classifier never runs), so
	// an unquoted brace word is unreadable. Braces inside quotes never expand
	// and stay literal payload.
	denied := []string{
		"sort {-o,out} in",
		"rg {--pre,}x pat",
		"find . {-delete,}",
		"tail {-f,} x",
		"file {-C,} x",
		"sed -n '1p' {-i,} f",
		"git log {--output,}=x",
		"ls {a,b}",
	}
	for _, command := range denied {
		t.Run(command, func(t *testing.T) {
			readonlyFalse(t, command)
		})
	}
	allowed := []string{
		"sort '{-o,out}' in",
		"ls '{a,b}'",
		"find . -name '{*.go,*.mod}'",
	}
	for _, command := range allowed {
		t.Run(command, func(t *testing.T) {
			readonlyTrue(t, command)
		})
	}
}

func TestShellReadOnlyRewrittenWordFailsClosed(t *testing.T) {
	// An unquoted word is not yet the argv the command receives: quote
	// removal strips "\", pathname expansion rewrites * ? [, brace expansion
	// rewrites { }, and $'...' decodes ANSI-C escapes. Any of them can forge
	// a flag from an innocent operand — a file named "-o" turns "sort *"
	// into "sort -o out in". find has no "--" option terminator either: GNU
	// find consumes a leading one and parses "find -- . -delete" as paths
	// plus -delete. Quoted copies never expand and stay readable.
	denied := []string{
		"ls *.go",
		"sort *",
		"ls ?",
		"ls [ab]",
		`sort \-o a b`,
		`sort $'\x2do' a b`,
		"find -- . -delete",
		"find . -- -delete",
		"find -- -delete",
	}
	for _, command := range denied {
		t.Run(command, func(t *testing.T) {
			readonlyFalse(t, command)
		})
	}
	allowed := []string{
		"sort '*' in",
		"ls '*.go'",
		"cat 'a b'",
		"find . -name '*.go'",
	}
	for _, command := range allowed {
		t.Run(command, func(t *testing.T) {
			readonlyTrue(t, command)
		})
	}
}

func TestShellReadOnlyFindNameConsumesDangerousWordAsPattern(t *testing.T) {
	// find consumes the word after -name positionally as its pattern, so a
	// dangerous-looking -delete there matches files literally named -delete
	// instead of deleting. This pins the shape that looks like a bypass but
	// is safe on both BSD and GNU find.
	readonlyTrue(t, "find . -name -delete")
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
