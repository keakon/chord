package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

// ---------------------------------------------------------------------------
// loadAgentsMDWithWorkDir
// ---------------------------------------------------------------------------

func TestLoadAgentsMDWithWorkDir_RootOnly(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "AGENTS.md"), "root instructions")

	got := renderDirectoryInstructionsForTest(dir, "")
	if !strings.Contains(got, "<INSTRUCTIONS>\nroot instructions\n</INSTRUCTIONS>") {
		t.Fatalf("expected root instructions, got %q", got)
	}
	if !strings.Contains(got, "Scope: .") || !strings.Contains(got, "Source: ") {
		t.Fatalf("missing source/scope: %q", got)
	}
}

func TestLoadAgentsMDWithWorkDir_WorkDirIsRootUsesPlainAgentsPath(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "AGENTS.md"), "root")

	got := renderDirectoryInstructionsForTest(root, root)
	if !strings.Contains(got, "<INSTRUCTIONS>\nroot\n</INSTRUCTIONS>") || !strings.Contains(got, "Scope: .") {
		t.Fatalf("expected root-relative AGENTS.md title when workDir is root, got %q", got)
	}
}

func TestLoadAgentsMDWithWorkDir_Hierarchical(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "pkg", "foo")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "AGENTS.md"), "root")
	writeFile(t, filepath.Join(root, "pkg", "AGENTS.md"), "mid")
	writeFile(t, filepath.Join(sub, "AGENTS.md"), "deep")

	got := renderDirectoryInstructionsForTest(root, sub)
	for _, body := range []string{"root", "mid", "deep"} {
		if strings.Count(got, "<INSTRUCTIONS>\n"+body+"\n</INSTRUCTIONS>") != 1 {
			t.Fatalf("missing/duplicate body %q: %s", body, got)
		}
	}
	if strings.Index(got, "Scope: .") > strings.Index(got, "Scope: pkg ") || strings.Index(got, "Scope: pkg ") > strings.Index(got, "Scope: pkg/foo ") {
		t.Fatalf("scope ordering: %s", got)
	}
}

func TestLoadAgentsMDWithWorkDir_WorkDirOutsideRootUsesItsOwnCopy(t *testing.T) {
	contentRoot := t.TempDir()
	other := t.TempDir()
	writeFile(t, filepath.Join(contentRoot, "AGENTS.md"), "root")
	writeFile(t, filepath.Join(other, "AGENTS.md"), "other")

	// workDir outside the content root is its own checkout root (a
	// chord-managed worktree lives under the state directory), so the walk is
	// anchored there instead of at the content root, and its own copy wins.
	got := renderDirectoryInstructionsForTest(contentRoot, other)
	if !strings.Contains(got, "<INSTRUCTIONS>\nother\n</INSTRUCTIONS>") || strings.Contains(got, "<INSTRUCTIONS>\nroot\n</INSTRUCTIONS>") {
		t.Fatalf("expected only the workDir checkout copy, got %q", got)
	}
}

func TestLoadAgentsMDWithWorkDir_WorkDirOutsideRootFallsBackToContentRootLevels(t *testing.T) {
	contentRoot := newWorktreeTestRepo(t)
	other := t.TempDir()
	runAgentTestGit(t, contentRoot, "worktree", "add", "--detach", other)
	workDir := filepath.Join(other, "pkg")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(contentRoot, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A fresh checkout holds no AGENTS.md: the files are local and gitignored
	// in this project, so the content root's copy applies at each level.
	writeFile(t, filepath.Join(contentRoot, "AGENTS.md"), "root instructions")
	writeFile(t, filepath.Join(contentRoot, "pkg", "AGENTS.md"), "pkg instructions")

	got := renderDirectoryInstructionsForTest(contentRoot, workDir)
	for _, want := range []string{"root instructions", "pkg instructions"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected content-root fallback %q in %q", want, got)
		}
	}
}

func TestLoadAgentsMDWithWorkDir_MissingFiles(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	// Only root has AGENTS.md; intermediates do not
	writeFile(t, filepath.Join(root, "AGENTS.md"), "root")

	got := renderDirectoryInstructionsForTest(root, sub)
	if !strings.Contains(got, "<INSTRUCTIONS>\nroot\n</INSTRUCTIONS>") || strings.Count(got, "<INSTRUCTIONS>") != 1 {
		t.Fatalf("expected only root, got %q", got)
	}
}

// checkoutWithGitMarker creates a directory that pathutil.CheckoutRoot treats
// as a checkout root, i.e. one carrying a .git entry (a file, as linked
// worktrees have).
func checkoutWithGitMarker(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, ".git"), "gitdir: /elsewhere\n")
}

func TestLoadAgentsMDWithWorkDir_PrefersCheckoutCopyOverContentRoot(t *testing.T) {
	contentRoot := t.TempDir()
	checkout := t.TempDir()
	checkoutWithGitMarker(t, checkout)
	workDir := filepath.Join(checkout, "pkg")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(contentRoot, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(contentRoot, "AGENTS.md"), "content root instructions")
	writeFile(t, filepath.Join(checkout, "AGENTS.md"), "checkout root instructions")
	writeFile(t, filepath.Join(contentRoot, "pkg", "AGENTS.md"), "content pkg instructions")
	writeFile(t, filepath.Join(checkout, "pkg", "AGENTS.md"), "checkout pkg instructions")

	got := renderDirectoryInstructionsForTest(contentRoot, workDir)
	for _, want := range []string{"checkout root instructions", "checkout pkg instructions"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected checkout copy %q in %q", want, got)
		}
	}
	for _, unwanted := range []string{"content root instructions", "content pkg instructions"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("content-root copy %q must not be loaded alongside the checkout copy: %q", unwanted, got)
		}
	}
}

func TestLoadAgentsMDWithWorkDir_FallsBackToContentRootCopies(t *testing.T) {
	contentRoot := newWorktreeTestRepo(t)
	checkout := t.TempDir()
	runAgentTestGit(t, contentRoot, "worktree", "add", "--detach", checkout)
	workDir := filepath.Join(checkout, "pkg")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(contentRoot, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A fresh worktree carries only tracked files, so gitignored local
	// AGENTS.md files exist in the content root alone.
	writeFile(t, filepath.Join(contentRoot, "AGENTS.md"), "content root instructions")
	writeFile(t, filepath.Join(contentRoot, "pkg", "AGENTS.md"), "content pkg instructions")

	got := renderDirectoryInstructionsForTest(contentRoot, workDir)
	for _, want := range []string{"content root instructions", "content pkg instructions"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected content-root fallback %q in %q", want, got)
		}
	}
}

func TestLoadAgentsMDWithWorkDir_AnchorsWalkAtCheckoutRoot(t *testing.T) {
	contentRoot := newWorktreeTestRepo(t)
	checkout := filepath.Join(contentRoot, ".chord", "worktrees", "runtime")
	runAgentTestGit(t, contentRoot, "worktree", "add", "--detach", checkout)
	workDir := filepath.Join(checkout, "pkg")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(contentRoot, "AGENTS.md"), "content root instructions")
	writeFile(t, filepath.Join(contentRoot, ".chord", "AGENTS.md"), "content chord instructions")

	got := renderDirectoryInstructionsForTest(contentRoot, workDir)
	if !strings.Contains(got, "content root instructions") {
		t.Fatalf("expected the content-root fallback for the checkout root level, got %q", got)
	}
	// Levels are relative to the checkout root, so content-root-only levels
	// between the content root and the checkout (here .chord/) are not walked.
	if strings.Contains(got, "content chord instructions") {
		t.Fatalf("walk must not descend content-root-only levels, got %q", got)
	}
}

// The stable system prompt never mentions AGENTS.md: the injected block names
// itself and states its own precedence, so a prompt line that depends on whether
// rules exist would only claim a block the request may not carry — and it would
// invalidate the cached prefix whenever rules appear or disappear.
func TestBuildSystemPrompt_NeverFramesAgentsMD(t *testing.T) {
	withRules := &MainAgent{tools: tools.NewRegistry()}
	setCachedAgentsMDForTest(withRules, "repo rules")
	sub := &SubAgent{tools: tools.NewRegistry(), workDir: "/tmp/project", taskDesc: "Inspect the parser."}
	sub.directoryInstructions.Store(withRules.directoryInstructions.Load())

	for name, prompt := range map[string]string{
		"main without rules": (&MainAgent{tools: tools.NewRegistry()}).buildSystemPrompt(),
		"main with rules":    withRules.buildSystemPrompt(),
		"sub without rules":  (&SubAgent{tools: tools.NewRegistry(), workDir: "/tmp/project", taskDesc: "Inspect the parser."}).buildSystemPrompt(),
		"sub with rules":     sub.buildSystemPrompt(),
	} {
		for _, unwanted := range []string{"## Workspace Instructions", "# AGENTS.md instructions"} {
			if strings.Contains(prompt, unwanted) {
				t.Fatalf("%s: system prompt mentions AGENTS.md (%q), got:\n%s", name, unwanted, prompt)
			}
		}
	}
}

// renderDirectoryInstructionsForTest loads the AGENTS.md instructions that apply to the
// agent's current checkout, from the checkout root down to workDir.
//
// Every level resolves to exactly one file: the checkout's own copy when it
// exists (a branch may carry its own tracked instructions), otherwise the
// content root's copy (where gitignored local instructions live, and which the
// worktree checkout does not contain). Levels are never loaded from both
// checkouts, so the same instructions are not repeated.
func renderDirectoryInstructionsForTest(contentRoot, workDir string) string {
	if contentRoot == "" {
		return ""
	}
	root, err := filepath.Abs(contentRoot)
	if err != nil {
		return ""
	}
	if workDir == "" {
		workDir = root
	}
	work, err := filepath.Abs(workDir)
	if err != nil {
		return ""
	}
	r, _, err := prepareDirectoryInstructions(context.Background(), directoryInstructionBinding{ContentRoot: root, WorkDir: work}, nil)
	if err != nil {
		return ""
	}
	defer r.cancel()
	return message.RenderDirectoryInstructionFacts(r.view.Load().Initial)
}
