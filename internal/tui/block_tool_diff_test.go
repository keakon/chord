package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/lipgloss/v2"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/textdiff"
	"github.com/keakon/chord/internal/tools"
	"github.com/keakon/chord/internal/tui/syntax"
	"github.com/keakon/chord/internal/tui/terminaltext"
	uitheme "github.com/keakon/chord/internal/tui/theme"
)

func TestEditDiffHeaderRecoversPathFromInvalidArgs(t *testing.T) {
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      tools.NameEdit,
		Content:       `{"path":"src/app.go","old_string":123,"new_string":"b"}`,
		ResultDone:    true,
		ResultStatus:  message.ToolStatusError,
		ResultContent: "arguments do not match edit schema: args.old_string must be a string, got number 123",
	}
	if got := block.diffToolFilePath(); got != "src/app.go" {
		t.Fatalf("diffToolFilePath with invalid sibling arg = %q, want %q", got, "src/app.go")
	}
}

func TestEditToolCardRendersHighlightedDiffWithPath(t *testing.T) {
	patch := "@@\n-old\n+new\n"
	args, _ := json.Marshal(map[string]string{"path": "src/demo.go", "patch": patch})
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      tools.NameEdit,
		Content:       string(args),
		ResultDone:    true,
		ResultStatus:  message.ToolStatusSuccess,
		ResultContent: "Applied patch to src/demo.go (+1 -1)",
		Diff:          "--- src/demo.go\n+++ src/demo.go\n@@ -1 +1 @@\n-old\n+new\n",
	}

	rendered := strings.Join(block.Render(100, ""), "\n")
	plain := terminaltext.StripANSI(rendered)
	if !strings.Contains(plain, "edit") || !strings.Contains(plain, "src/demo.go") {
		t.Fatalf("expected edit header to show path, got:\n%s", plain)
	}
	if !strings.Contains(plain, "-old") || !strings.Contains(plain, "+new") {
		t.Fatalf("expected diff lines to render, got:\n%s", plain)
	}
	if rendered == plain {
		t.Fatal("expected diff render to include ANSI highlighting")
	}
}

func TestApplyPatchToolCardRendersNamePathAndHighlightedPreview(t *testing.T) {
	ApplyTheme(uitheme.Default())
	args, _ := json.Marshal(map[string]string{"patch": strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: src/demo.go",
		"@@",
		"-func old() {}",
		"+func main() {}",
		"*** End Patch",
	}, "\n")})
	block := &Block{
		ID:       1,
		Type:     BlockToolCall,
		ToolName: tools.NameApplyPatch,
		Content:  applyPatchToolDisplayArgs(string(args)),
		RawArgs:  string(args),
	}

	lines := block.Render(100, "●")
	plain := terminaltext.StripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "apply_patch src/demo.go") {
		t.Fatalf("expected apply_patch header with path, got:\n%s", plain)
	}
	if strings.Contains(plain, "↳ Targets:") || strings.Contains(plain, "M src/demo.go") {
		t.Fatalf("expected single-file target list to be omitted as duplicate, got:\n%s", plain)
	}
	if !strings.Contains(plain, "+func main() {}") {
		t.Fatalf("expected patch preview, got:\n%s", plain)
	}
	added := renderedLineContaining(t, lines, "func main")
	assertRenderedTextForeground(t, added, "func", renderedCellsForText(t, syntax.New("sample.go", "", "").Line("func sample() {}", ""), "func")[0].Style.Fg)
	assertRenderedTextBackground(t, added, "func", colorOfTheme(currentStyles.Palette.DiffAddLineBg))
}

func TestApplyPatchToolCardSummarizesMultiplePaths(t *testing.T) {
	args, _ := json.Marshal(map[string]string{"patch": strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: src/old.go",
		"*** Move to: src/new.go",
		"@@",
		"-package old",
		"+package renamed",
		"*** Add File: docs/new.md",
		"+# New",
		"*** Delete File: tmp/old.txt",
		"*** End Patch",
	}, "\n")})
	block := &Block{ID: 1, Type: BlockToolCall, ToolName: tools.NameApplyPatch, Content: applyPatchToolDisplayArgs(string(args)), RawArgs: string(args)}
	plain := terminaltext.StripANSI(strings.Join(block.Render(120, "●"), "\n"))
	for _, want := range []string{
		"apply_patch 3 files",
		"↳ Targets:",
		"R src/old.go → src/new.go",
		"A docs/new.md",
		"D tmp/old.txt",
	} {
		if !strings.Contains(plain, want) {
			t.Fatalf("expected multi-file apply_patch display to contain %q, got:\n%s", want, plain)
		}
	}
	// The Targets rows name every file, so the header keeps only the count: it
	// used to repeat the first target and print the file count twice.
	for _, unwanted := range []string{"src/old.go → src/new.go +2 files", "apply_patch src/old.go"} {
		if strings.Contains(plain, unwanted) {
			t.Fatalf("expected multi-file header to drop %q, got:\n%s", unwanted, plain)
		}
	}
}

func TestApplyPatchToolCardGroupsMultiFileDiffByFile(t *testing.T) {
	args, _ := json.Marshal(map[string]string{"patch": strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: src/first.go",
		"@@",
		"-const first = 1",
		"+const first = 2",
		"*** Update File: src/second.go",
		"@@",
		"-const second = 1",
		"+const second = 2",
		"*** End Patch",
	}, "\n")})
	block := &Block{
		ID:       1,
		Type:     BlockToolCall,
		ToolName: tools.NameApplyPatch,
		Content:  applyPatchToolDisplayArgs(string(args)),
		RawArgs:  string(args),
		Diff: strings.Join([]string{
			"--- src/first.go",
			"+++ src/first.go",
			"@@ -1,1 +1,1 @@",
			"-const first = 1",
			"+const first = 2",
			"--- src/second.go",
			"+++ src/second.go",
			"@@ -1,1 +1,1 @@",
			"-const second = 1",
			"+const second = 2",
		}, "\n"),
	}

	plain := terminaltext.StripANSI(strings.Join(block.Render(120, ""), "\n"))
	if strings.Contains(plain, "↳ Targets:") {
		t.Fatalf("expected per-file diff sections instead of a detached file list, got:\n%s", plain)
	}
	firstHeader := strings.Index(plain, "↳ M src/first.go")
	firstChange := strings.Index(plain, "const first = 2")
	secondHeader := strings.Index(plain, "↳ M src/second.go")
	secondChange := strings.Index(plain, "const second = 2")
	if firstHeader < 0 || firstChange < 0 || secondHeader < 0 || secondChange < 0 ||
		!(firstHeader < firstChange && firstChange < secondHeader && secondHeader < secondChange) {
		t.Fatalf("expected each file heading immediately before its own diff section, got:\n%s", plain)
	}
}

func TestApplyPatchToolCardSummarizesDeleteAndKeepsMoveUpdateDiff(t *testing.T) {
	args, _ := json.Marshal(map[string]string{"patch": strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: src/old.go",
		"*** Move to: src/new.go",
		"@@",
		"-const moved = 1",
		"+const moved = 2",
		"*** Add File: docs/new.md",
		"+# New",
		"*** Delete File: tmp/old.txt",
		"*** End Patch",
	}, "\n")})
	block := &Block{
		ID:           1,
		Type:         BlockToolCall,
		ToolName:     tools.NameApplyPatch,
		Content:      applyPatchToolDisplayArgs(string(args)),
		RawArgs:      string(args),
		ResultDone:   true,
		ResultStatus: message.ToolStatusSuccess,
		Diff: strings.Join([]string{
			"--- src/old.go",
			"+++ src/new.go",
			"@@ -1,1 +1,1 @@",
			"-const moved = 1",
			"+const moved = 2",
			"--- /dev/null",
			"+++ docs/new.md",
			"@@ -0,0 +1,1 @@",
			"+# New",
			"--- tmp/old.txt",
			"+++ /dev/null",
			"@@ -1,1 +0,0 @@",
			"-obsolete",
		}, "\n"),
	}

	plain := terminaltext.StripANSI(strings.Join(block.Render(120, ""), "\n"))
	for _, want := range []string{
		"↳ R src/old.go → src/new.go",
		"↳ A docs/new.md",
		"↳ D tmp/old.txt",
		"const moved = 2",
		"# New",
	} {
		if !strings.Contains(plain, want) {
			t.Fatalf("expected multi-file diff section %q, got:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "obsolete") {
		t.Fatalf("expected deleted file contents to be hidden, got:\n%s", plain)
	}
}

// A codex-style patch spells the target absolutely while the tool writes
// base-dir-relative diff headers. Both name the same file, so the card has to
// relativize the header path, recognise the delete section (the writer spells
// the same path on both sides instead of using /dev/null) and keep the removed
// contents out of the body — the same thing the delete tool's card does.
func TestApplyPatchDeleteCardRelativizesAbsoluteTarget(t *testing.T) {
	const workingDir = "/Users/example/project"
	args := `{"patch":"*** Begin Patch\n*** Delete File: ` + workingDir + `/.chord/notes/demo.sh\n*** End Patch"}`
	block := &Block{
		ID: 1, Type: BlockToolCall, ToolName: tools.NameApplyPatch,
		Content: applyPatchToolDisplayArgs(args), RawArgs: args,
		displayWorkingDir: workingDir,
		ResultDone:        true, ResultStatus: message.ToolStatusSuccess,
		ResultContent: "Applied patch:\nD .chord/notes/demo.sh",
		Diff:          "--- .chord/notes/demo.sh\n+++ .chord/notes/demo.sh\n@@ -1,2 +1,0 @@\n-obsolete\n-secret\n",
	}

	plain := terminaltext.StripANSI(strings.Join(block.Render(140, ""), "\n"))
	if !strings.Contains(plain, "✓ apply_patch D .chord/notes/demo.sh · 1 file · -2 lines") {
		t.Fatalf("expected a relative delete header with its line count, got:\n%s", plain)
	}
	for _, unwanted := range []string{"obsolete", "secret", workingDir, "↳ M", "↳ D"} {
		if strings.Contains(plain, unwanted) {
			t.Fatalf("expected %q to be absent from a single-file delete card, got:\n%s", unwanted, plain)
		}
	}
}

// The same-file-both-sides diff must not turn a delete into "M", and a
// multi-file patch must spell every row the way the header does.
func TestApplyPatchMultiFileRowsRelativizeAbsoluteTargets(t *testing.T) {
	const workingDir = "/Users/example/project"
	args := `{"patch":"*** Begin Patch\n*** Delete File: ` + workingDir + `/.chord/notes/old.sh\n*** Update File: ` + workingDir + `/.chord/notes/keep.sh\n@@\n-old\n+new\n*** End Patch"}`
	block := &Block{
		ID: 1, Type: BlockToolCall, ToolName: tools.NameApplyPatch,
		Content: applyPatchToolDisplayArgs(args), RawArgs: args,
		displayWorkingDir: workingDir,
		ResultDone:        true, ResultStatus: message.ToolStatusSuccess,
		ResultContent: "Applied patch:\nD .chord/notes/old.sh\nM .chord/notes/keep.sh",
		Diff: "--- .chord/notes/old.sh\n+++ .chord/notes/old.sh\n@@ -1,2 +1,0 @@\n-obsolete\n-secret\n" +
			"--- .chord/notes/keep.sh\n+++ .chord/notes/keep.sh\n@@ -1,1 +1,1 @@\n-old\n+new\n",
	}

	plain := terminaltext.StripANSI(strings.Join(block.Render(140, ""), "\n"))
	if !strings.Contains(plain, "✓ apply_patch 2 files · +1 -3 lines") {
		t.Fatalf("expected the header to count the hidden delete section, got:\n%s", plain)
	}
	for _, want := range []string{"↳ D .chord/notes/old.sh", "↳ M .chord/notes/keep.sh", "+new"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("expected %q, got:\n%s", want, plain)
		}
	}
	for _, unwanted := range []string{workingDir, "obsolete", "secret"} {
		if strings.Contains(plain, unwanted) {
			t.Fatalf("expected %q to be absent, got:\n%s", unwanted, plain)
		}
	}
}

// A move with content changes keeps its diff section, and the section header
// must still match the target when the two sides spell the paths differently.
func TestApplyPatchMoveUpdateSectionMatchesAbsoluteTarget(t *testing.T) {
	const workingDir = "/Users/example/project"
	args := `{"patch":"*** Begin Patch\n*** Update File: ` + workingDir + `/src/old.go\n*** Move to: ` + workingDir + `/src/new.go\n@@\n-const moved = 1\n+const moved = 2\n*** End Patch"}`
	block := &Block{
		ID: 1, Type: BlockToolCall, ToolName: tools.NameApplyPatch,
		Content: applyPatchToolDisplayArgs(args), RawArgs: args,
		displayWorkingDir: workingDir,
		ResultDone:        true, ResultStatus: message.ToolStatusSuccess,
		ResultContent: "Applied patch:\nR src/old.go -> src/new.go",
		Diff:          "--- src/old.go\n+++ src/new.go\n@@ -1,1 +1,1 @@\n-const moved = 1\n+const moved = 2\n",
	}

	plain := terminaltext.StripANSI(strings.Join(block.Render(140, ""), "\n"))
	for _, want := range []string{"apply_patch src/old.go → src/new.go", "+const moved = 2"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("expected %q, got:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, workingDir) {
		t.Fatalf("expected no absolute path in the card, got:\n%s", plain)
	}
}

// A patch that spells its targets through a symlinked working dir still owns
// the tool's base-dir-relative diff sections: the delete must not fall back to
// an "M" section replaying the removed lines.
func TestApplyPatchSymlinkSpelledTargetsMatchDiffSections(t *testing.T) {
	real, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	args := `{"patch":"*** Begin Patch\n*** Delete File: ` + link + `/notes/old.sh\n*** Update File: ` + link + `/notes/keep.sh\n@@\n-old\n+new\n*** End Patch"}`
	block := &Block{
		ID: 1, Type: BlockToolCall, ToolName: tools.NameApplyPatch,
		Content: applyPatchToolDisplayArgs(args), RawArgs: args,
		displayWorkingDir: real,
		ResultDone:        true, ResultStatus: message.ToolStatusSuccess,
		ResultContent: "Applied patch:\nD notes/old.sh\nM notes/keep.sh",
		Diff: "--- notes/old.sh\n+++ notes/old.sh\n@@ -1,1 +1,0 @@\n-obsolete\n" +
			"--- notes/keep.sh\n+++ notes/keep.sh\n@@ -1,1 +1,1 @@\n-old\n+new\n",
	}

	plain := terminaltext.StripANSI(strings.Join(block.Render(200, ""), "\n"))
	for _, want := range []string{"↳ D notes/old.sh", "↳ M notes/keep.sh", "+new"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("expected %q, got:\n%s", want, plain)
		}
	}
	for _, unwanted := range []string{link, "obsolete", "↳ M notes/old.sh"} {
		if strings.Contains(plain, unwanted) {
			t.Fatalf("expected %q to be absent, got:\n%s", unwanted, plain)
		}
	}
}

// An errored patch keeps the section of a delete that already landed: that
// section header is the only place the committed group is marked ✓.
func TestApplyPatchErrorCardMarksCommittedDelete(t *testing.T) {
	args := `{"patch":"*** Begin Patch\n*** Delete File: .chord/notes/old.sh\n*** Update File: .chord/notes/failed.sh\n@@\n-missing\n+replacement\n*** End Patch"}`
	block := &Block{
		ID: 1, Type: BlockToolCall, ToolName: tools.NameApplyPatch,
		Content: applyPatchToolDisplayArgs(args), RawArgs: args,
		ResultDone: true, ResultStatus: message.ToolStatusError,
		ResultContent: strings.Join([]string{
			"apply_patch partially applied: 1 change committed; 1 file group not applied.",
			"Applied patch:",
			"D .chord/notes/old.sh",
			"",
			"Not applied:",
			"- .chord/notes/failed.sh: hunk not found (1/1)",
		}, "\n"),
		Diff: "--- .chord/notes/old.sh\n+++ .chord/notes/old.sh\n@@ -1,1 +1,0 @@\n-obsolete\n",
	}

	plain := terminaltext.StripANSI(strings.Join(block.Render(120, ""), "\n"))
	for _, want := range []string{"↳ ✓ D .chord/notes/old.sh", "✗ .chord/notes/failed.sh"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("expected errored card to contain %q, got:\n%s", want, plain)
		}
	}
}

func TestApplyPatchToolCardHidesSingleDeleteDiff(t *testing.T) {
	args := `{"patch":"*** Begin Patch\n*** Delete File: tmp/old.txt\n*** End Patch"}`
	block := &Block{
		ID: 1, Type: BlockToolCall, ToolName: tools.NameApplyPatch,
		Content: applyPatchToolDisplayArgs(args), RawArgs: args,
		ResultDone: true, ResultStatus: message.ToolStatusSuccess,
		ResultContent: "Applied patch:\nD tmp/old.txt",
		Diff:          "--- tmp/old.txt\n+++ /dev/null\n@@ -1,2 +0,0 @@\n-obsolete\n-secret\n",
	}

	plain := terminaltext.StripANSI(strings.Join(block.Render(100, ""), "\n"))
	if !strings.Contains(plain, "apply_patch D tmp/old.txt") {
		t.Fatalf("expected delete summary in header, got:\n%s", plain)
	}
	for _, hidden := range []string{"obsolete", "secret", "↳ Requested patch:"} {
		if strings.Contains(plain, hidden) {
			t.Fatalf("expected %q to be hidden for file deletion, got:\n%s", hidden, plain)
		}
	}
}

func TestApplyPatchToolCardShowsNoChangesWithoutPatchPreview(t *testing.T) {
	args := `{"patch":"*** Begin Patch\n*** Update File: docs/guide.md\n@@\n section\n*** End Patch"}`
	block := &Block{
		ID: 1, Type: BlockToolCall, ToolName: tools.NameApplyPatch,
		Content: applyPatchToolDisplayArgs(args), RawArgs: args,
		ResultDone: true, ResultStatus: message.ToolStatusSuccess,
		ResultContent: "Applied patch:\nNo net file changes",
	}

	plain := terminaltext.StripANSI(strings.Join(block.Render(100, ""), "\n"))
	if !strings.Contains(plain, "No changes") {
		t.Fatalf("expected no-changes indicator, got:\n%s", plain)
	}
	if strings.Contains(plain, "*** Begin Patch") {
		t.Fatalf("expected successful no-change card to hide patch preview, got:\n%s", plain)
	}
}

func TestApplyPatchToolCardHidesPureMoveDiff(t *testing.T) {
	args := `{"patch":"*** Begin Patch\n*** Update File: src/old.go\n*** Move to: src/new.go\n*** End Patch"}`
	block := &Block{
		ID: 1, Type: BlockToolCall, ToolName: tools.NameApplyPatch,
		Content: applyPatchToolDisplayArgs(args), RawArgs: args,
		ResultDone: true, ResultStatus: message.ToolStatusSuccess,
		ResultContent: "Applied patch:\nR src/old.go -> src/new.go",
		Diff:          "--- src/old.go\n+++ src/new.go\n",
	}

	plain := terminaltext.StripANSI(strings.Join(block.Render(100, ""), "\n"))
	if !strings.Contains(plain, "apply_patch src/old.go → src/new.go") {
		t.Fatalf("expected move path summary in header, got:\n%s", plain)
	}
	if strings.Contains(plain, "↳ Requested patch:") || strings.Contains(plain, "Applied patch:") {
		t.Fatalf("expected pure move to omit patch and generic result bodies, got:\n%s", plain)
	}
}

func TestApplyPatchToolCardKeepsMoveWithUpdateDiff(t *testing.T) {
	args := `{"patch":"*** Begin Patch\n*** Update File: src/old.go\n*** Move to: src/new.go\n@@\n-old\n+new\n*** End Patch"}`
	block := &Block{
		ID: 1, Type: BlockToolCall, ToolName: tools.NameApplyPatch,
		Content: applyPatchToolDisplayArgs(args), RawArgs: args,
		ResultDone: true, ResultStatus: message.ToolStatusSuccess,
		Diff: "--- src/old.go\n+++ src/new.go\n@@ -1,1 +1,1 @@\n-old\n+new\n",
	}

	plain := terminaltext.StripANSI(strings.Join(block.Render(100, ""), "\n"))
	if !strings.Contains(plain, "apply_patch src/old.go → src/new.go") ||
		!strings.Contains(plain, "-old") || !strings.Contains(plain, "+new") {
		t.Fatalf("expected move path and content update diff, got:\n%s", plain)
	}
}

func TestLegacyPatchToolCardUsesApplyPatchDisplay(t *testing.T) {
	args := `{"patch":"*** Begin Patch\n*** Update File: src/demo.go\n@@\n-old\n+new\n*** End Patch"}`
	block := &Block{ID: 1, Type: BlockToolCall, ToolName: "patch", Content: args, RawArgs: args}
	plain := terminaltext.StripANSI(strings.Join(block.Render(100, "●"), "\n"))
	if !strings.Contains(plain, "apply_patch src/demo.go") || strings.Contains(plain, " patch ") {
		t.Fatalf("expected legacy patch card to use apply_patch display, got:\n%s", plain)
	}
}

func TestApplyPatchErrorCardKeepsHighlightedPatchPreview(t *testing.T) {
	ApplyTheme(uitheme.Default())
	args := `{"patch":"*** Begin Patch\n*** Update File: src/demo.go\n@@\n-func old() {}\n+func main() {}\n*** End Patch"}`
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      tools.NameApplyPatch,
		Content:       applyPatchToolDisplayArgs(args),
		RawArgs:       args,
		ResultContent: "hunk not found",
		ResultDone:    true,
		ResultStatus:  message.ToolStatusError,
	}
	lines := block.Render(100, "")
	plain := terminaltext.StripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "apply_patch src/demo.go") || !strings.Contains(plain, "↳ Requested patch:") || !strings.Contains(plain, "+func main() {}") || !strings.Contains(plain, "hunk not found") {
		t.Fatalf("expected error card to preserve path, patch, and error, got:\n%s", plain)
	}
	hunkHeader := renderedLineContaining(t, lines, "@@")
	if !strings.Contains(terminaltext.StripANSI(hunkHeader), "─") {
		t.Fatalf("expected failed patch preview hunk header to include an inline separator, got %q", terminaltext.StripANSI(hunkHeader))
	}
	added := renderedLineContaining(t, lines, "func main")
	assertRenderedTextForeground(t, added, "func", renderedCellsForText(t, syntax.New("sample.go", "", "").Line("func sample() {}", ""), "func")[0].Style.Fg)
	assertRenderedTextBackground(t, added, "func", colorOfTheme(currentStyles.Palette.DiffAddLineBg))
}

func TestPartiallyAppliedPatchShowsOnlyAppliedDiff(t *testing.T) {
	args := `{"patch":"*** Begin Patch\n*** Update File: committed.go\n@@\n-old\n+new\n*** Update File: failed.go\n@@\n-missing\n+replacement\n*** End Patch"}`
	longReasonTail := "failure-reason-tail-must-not-wrap"
	block := &Block{
		ID: 1, Type: BlockToolCall, ToolName: tools.NameApplyPatch,
		Content: applyPatchToolDisplayArgs(args), RawArgs: args,
		ResultDone: true, ResultStatus: message.ToolStatusError,
		ResultContent: strings.Join([]string{
			"apply_patch partially applied: 1 change committed; 1 file group not applied.",
			"Applied patch:",
			"M committed.go",
			"",
			"Diagnostics:",
			"committed.go (1 new, 0 resolved):",
			"[E] 1:1 [MissingFieldOrMethod] missing field",
			"",
			"LSP diagnostics in other files:",
			"other.go:",
			"[I] 2:1 [default] informational diagnostic",
			"",
			"Not applied:",
			"- failed.go: hunk not found; " + strings.Repeat("long explanation ", 8) + longReasonTail,
			"Changes under \"Applied patch\" are already on disk; resolve each cause above and resubmit only the failed file groups rebuilt from current file contents.",
		}, "\n"),
		Diff: "--- committed.go\n+++ committed.go\n@@ -1,1 +1,1 @@\n-old\n+new\n",
	}

	plain := terminaltext.StripANSI(strings.Join(block.Render(80, ""), "\n"))
	for _, want := range []string{"↳ Applied changes:", "↳ ✓ M committed.go", "✗ failed.go: hunk not found", "+new", "↳ Error:", "Not applied:", "↳ Diagnostics:", "missing field", "informational diagnostic"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("expected partially applied patch to contain %q, got:\n%s", want, plain)
		}
	}
	// The args-derived target list states no status, so an errored card drops
	// it instead of drawing the failed target unmarked beside the ✓/✗ marks.
	for _, duplicate := range []string{"↳ Requested patch:", "Applied patch:", "*** Begin Patch", "↳ Targets:"} {
		if strings.Contains(plain, duplicate) {
			t.Fatalf("expected partially applied patch to omit requested patch content %q, got:\n%s", duplicate, plain)
		}
	}
	committedHeader := renderedLineContaining(t, block.Render(80, ""), "↳ ✓ M committed.go")
	assertRenderedTextForeground(t, committedHeader, "✓", colorOfTheme(currentStyles.Palette.InfoPanelSuccessFg))
	if strings.Count(plain, "M committed.go") != 1 {
		t.Fatalf("expected committed target to appear once, got:\n%s", plain)
	}
	errorAt := strings.Index(plain, "↳ Error:")
	notAppliedAt := strings.Index(plain, "Not applied:")
	diagnosticsAt := strings.Index(plain, "↳ Diagnostics:")
	diagnosticAt := strings.Index(plain, "missing field")
	if errorAt < 0 || notAppliedAt < errorAt || diagnosticsAt < notAppliedAt || diagnosticAt < diagnosticsAt {
		t.Fatalf("expected failure and diagnostics to render as separate ordered sections, got:\n%s", plain)
	}
	// Plain error text wraps to multiple lines so the full diagnostic (including
	// the tail of the long failure reason) is visible in the rendered card,
	// rather than truncating with "…". The copy path preserves the full content
	// independently (see the toolCallMarkdownContent check below).
	if !strings.Contains(plain, longReasonTail) {
		t.Fatalf("expected wrapped failure lines to surface the full tail %q in the rendered card, got:\n%s", longReasonTail, plain)
	}
	failureLine := renderedLineContaining(t, block.Render(80, ""), "✗ failed.go: hunk not found")
	assertRenderedTextForeground(t, failureLine, "✗", colorOfTheme(currentStyles.Palette.ErrorFg))
	if strings.Contains(terminaltext.StripANSI(failureLine), "…") {
		t.Fatalf("expected long failure line to wrap instead of truncate, got %q", terminaltext.StripANSI(failureLine))
	}
	copyContent := toolCallMarkdownContent(block)
	for _, preserved := range []string{longReasonTail} {
		if !strings.Contains(copyContent, preserved) {
			t.Fatalf("expected copied tool content to preserve %q, got:\n%s", preserved, copyContent)
		}
	}
}

// TestApplyPatchFullFailureWrapsLongDiagnostic guards the "plain error text
// wraps, not truncates" contract for a full apply_patch failure (no partial
// application). The "Not applied:" diagnostic must wrap to multiple lines so
// the actionable tail is fully visible, instead of being clipped with "…" and
// hiding the very information the user needs to fix the patch. Mirrors the
// wrap behavior of renderLSPDiagnosticsLines (Edit errors, diagnostics) and
// the collapsed error path, both of which already wrap.
func TestApplyPatchFullFailureWrapsLongDiagnostic(t *testing.T) {
	tail := "actionable-tail-must-be-visible"
	longLine := "- internal/agent/main_llm_gate.go: hunk not found (1/1); first expected complete line: `\t\tif isFoo(err) {`; the file currently has a different guard; " + tail
	content := strings.Join([]string{
		"Error: apply_patch failed: no changes were committed.",
		"",
		"Not applied:",
		longLine,
	}, "\n")
	patchArgs := `{"patch":"*** Begin Patch\n*** Update File: internal/agent/main_llm_gate.go\n@@\n-if isBar(err) {\n+if isFoo(err) {\n*** End Patch"}`
	block := &Block{
		ID: 829, Type: BlockToolCall, ToolName: tools.NameApplyPatch,
		Content:       applyPatchToolDisplayArgs(patchArgs),
		RawArgs:       patchArgs,
		ResultDone:    true,
		ResultStatus:  message.ToolStatusError,
		ResultContent: content,
	}

	for _, width := range []int{80, 120, 160, 200} {
		t.Run(fmt.Sprintf("width=%d", width), func(t *testing.T) {
			plain := terminaltext.StripANSI(strings.Join(block.Render(width, ""), "\n"))

			for _, want := range []string{"↳ Error:", "apply_patch failed", "Not applied:"} {
				if !strings.Contains(plain, want) {
					t.Fatalf("expected full-failure card to contain %q, got:\n%s", want, plain)
				}
			}
			// The full diagnostic tail is visible in the rendered card (wrap,
			// not truncation).
			if !strings.Contains(plain, tail) {
				t.Fatalf("expected wrapped diagnostic to surface the tail %q in the rendered card, got:\n%s", tail, plain)
			}
			// No truncation marker on the first wrapped line of the diagnostic.
			firstWrapped := renderedLineContaining(t, block.Render(width, ""), "hunk not found")
			if strings.Contains(terminaltext.StripANSI(firstWrapped), "…") {
				t.Fatalf("expected diagnostic to wrap instead of truncate, got %q", terminaltext.StripANSI(firstWrapped))
			}
			// The long diagnostic spans multiple wrapped lines at every tested
			// width (the line is well over any card content budget), proving
			// wrap occurred instead of single-line truncation.
			wrappedLines := 0
			for _, ln := range block.Render(width, "") {
				p := terminaltext.StripANSI(ln)
				if strings.Contains(p, "hunk not found") || strings.Contains(p, "the file currently") || strings.Contains(p, tail) {
					wrappedLines++
				}
			}
			if wrappedLines < 2 {
				t.Fatalf("expected the long diagnostic to wrap to >=2 rendered lines, got %d matching lines", wrappedLines)
			}
		})
	}
}

// A partially applied patch must say which file groups landed and which did
// not: every diff section is a committed group, so its header carries ✓, and
// each "Not applied:" entry carries ✗.
func TestApplyPatchErrorCardMarksCommittedAndFailedFiles(t *testing.T) {
	ApplyTheme(uitheme.Default())
	args := `{"patch":"*** Begin Patch\n*** Update File: src/first.go\n@@\n-old\n+new\n*** Update File: src/second.go\n@@\n-old\n+new\n*** Update File: src/failed.go\n@@\n-missing\n+replacement\n*** End Patch"}`
	block := &Block{
		ID: 1, Type: BlockToolCall, ToolName: tools.NameApplyPatch,
		Content: applyPatchToolDisplayArgs(args), RawArgs: args,
		ResultDone: true, ResultStatus: message.ToolStatusError,
		ResultContent: strings.Join([]string{
			"apply_patch partially applied: 2 changes committed; 1 file group not applied.",
			"Applied patch:",
			"M src/first.go",
			"M src/second.go",
			"",
			"Not applied:",
			"- src/failed.go: hunk not found (1/1)",
			`Changes under "Applied patch" are already on disk; resolve each cause above and resubmit only the failed file groups rebuilt from current file contents.`,
		}, "\n"),
		Diff: "--- src/first.go\n+++ src/first.go\n@@ -1,1 +1,1 @@\n-old\n+new\n" +
			"--- src/second.go\n+++ src/second.go\n@@ -1,1 +1,1 @@\n-old\n+new\n",
	}
	lines := block.Render(100, "")
	plain := terminaltext.StripANSI(strings.Join(lines, "\n"))
	for _, want := range []string{"↳ ✓ M src/first.go", "↳ ✓ M src/second.go", "✗ src/failed.go: hunk not found (1/1)"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("expected errored card to contain %q, got:\n%s", want, plain)
		}
	}
	for _, committed := range []string{"↳ ✓ M src/first.go", "↳ ✓ M src/second.go"} {
		header := renderedLineContaining(t, lines, committed)
		assertRenderedTextForeground(t, header, "✓", colorOfTheme(currentStyles.Palette.InfoPanelSuccessFg))
	}
	failed := renderedLineContaining(t, lines, "✗ src/failed.go")
	assertRenderedTextForeground(t, failed, "✗", colorOfTheme(currentStyles.Palette.ErrorFg))
}

// Committed groups whose diff is unavailable still have to read as applied, so
// the "Applied patch:" list in the error body is marked ✓ as well.
func TestApplyPatchErrorCardMarksAppliedListWithoutDiff(t *testing.T) {
	ApplyTheme(uitheme.Default())
	args := `{"patch":"*** Begin Patch\n*** Update File: src/first.go\n@@\n-old\n+new\n*** Update File: src/failed.go\n@@\n-missing\n+replacement\n*** End Patch"}`
	block := &Block{
		ID: 1, Type: BlockToolCall, ToolName: tools.NameApplyPatch,
		Content: applyPatchToolDisplayArgs(args), RawArgs: args,
		ResultDone: true, ResultStatus: message.ToolStatusError,
		ResultContent: strings.Join([]string{
			"apply_patch partially applied: 1 change committed; 1 file group not applied.",
			"Applied patch:",
			"M src/first.go",
			"",
			"Not applied:",
			"- src/failed.go: hunk not found (1/1)",
		}, "\n"),
	}
	plain := terminaltext.StripANSI(strings.Join(block.Render(100, ""), "\n"))
	for _, want := range []string{"↳ Applied changes:", "✓ M src/first.go", "✗ src/failed.go: hunk not found (1/1)"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("expected errored card to contain %q, got:\n%s", want, plain)
		}
	}
}

// A successful card marks no per-file status: everything it shows already
// landed, so ✓ on each section header would only add noise.
func TestSuccessfulApplyPatchCardOmitsPerFileStatusMarks(t *testing.T) {
	ApplyTheme(uitheme.Default())
	args := `{"patch":"*** Begin Patch\n*** Update File: src/first.go\n@@\n-old\n+new\n*** Update File: src/second.go\n@@\n-old\n+new\n*** End Patch"}`
	block := &Block{
		ID: 1, Type: BlockToolCall, ToolName: tools.NameApplyPatch,
		Content: applyPatchToolDisplayArgs(args), RawArgs: args,
		ResultDone: true, ResultStatus: message.ToolStatusSuccess,
		ResultContent: "Applied patch:\nM src/first.go\nM src/second.go",
		Diff: "--- src/first.go\n+++ src/first.go\n@@ -1,1 +1,1 @@\n-old\n+new\n" +
			"--- src/second.go\n+++ src/second.go\n@@ -1,1 +1,1 @@\n-old\n+new\n",
	}
	plain := terminaltext.StripANSI(strings.Join(block.Render(100, ""), "\n"))
	if !strings.Contains(plain, "↳ M src/first.go") {
		t.Fatalf("expected the grouped success card to render per-file sections, got:\n%s", plain)
	}
	for _, mark := range []string{"↳ ✓", "↳ ✗", "✗"} {
		if strings.Contains(plain, mark) {
			t.Fatalf("expected a successful card to omit %q, got:\n%s", mark, plain)
		}
	}
}

// TestEditPatchPreviewLinesFullContent guards the "never line-clip patch
// previews" decision: apply_patch and edit previews must show all lines, not a
// 20-line window with a truncation marker.
func TestEditPatchPreviewLinesFullContent(t *testing.T) {
	longPatch := make([]string, 0, 60)
	for i := range 60 {
		longPatch = append(longPatch, fmt.Sprintf(" line %d", i))
	}
	lines := editPatchPreviewLines(strings.Join(longPatch, "\n"))
	if len(lines) != 60 {
		t.Fatalf("editPatchPreviewLines returned %d lines, want all 60", len(lines))
	}
	for _, truncated := range []string{"... (patch truncated)", "... (text truncated)"} {
		if strings.Contains(strings.Join(lines, "\n"), truncated) {
			t.Fatalf("preview unexpectedly truncated with %q", truncated)
		}
	}

	replaceLines := replaceEditPreviewLines(strings.Join(longPatch, "\n"))
	if len(replaceLines) != 60 {
		t.Fatalf("replaceEditPreviewLines returned %d lines, want all 60", len(replaceLines))
	}
}

func TestApplyPatchPreviewTruncatesLongLinesWithoutWrapping(t *testing.T) {
	ApplyTheme(uitheme.Default())
	longLine := `const message = "` + strings.Repeat("x", 100) + `tail"`
	args, _ := json.Marshal(map[string]string{"patch": strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: src/demo.go",
		"@@",
		"+" + longLine,
		"*** End Patch",
	}, "\n")})
	block := &Block{
		ID: 1, Type: BlockToolCall, ToolName: tools.NameApplyPatch,
		Content: applyPatchToolDisplayArgs(string(args)), RawArgs: string(args),
	}

	lines := block.Render(60, "")
	plain := terminaltext.StripANSI(strings.Join(lines, "\n"))
	if strings.Count(plain, "const message") != 1 || strings.Contains(plain, "tail") {
		t.Fatalf("expected long patch line to be truncated once without a continuation, got:\n%s", plain)
	}
	line := renderedLineContaining(t, lines, "const message")
	if !strings.Contains(terminaltext.StripANSI(line), "…") {
		t.Fatalf("expected truncated patch line to show an ellipsis, got %q", terminaltext.StripANSI(line))
	}
	assertRenderedTextBackground(t, line, "…", colorOfTheme(currentStyles.Palette.DiffAddLineBg))
}

func TestEditAndApplyPatchToolCardsKeepAddedAndDeletedLineBackgrounds(t *testing.T) {
	ApplyTheme(uitheme.Default())
	args, _ := json.Marshal(map[string]string{"path": "src/demo.go"})
	diff := "@@ -1,1 +1,1 @@\n-package old\n+package main\n"
	for _, toolName := range []string{tools.NameEdit, tools.NameApplyPatch} {
		t.Run(toolName, func(t *testing.T) {
			block := &Block{ID: 1, Type: BlockToolCall, ToolName: toolName, Content: string(args), Diff: diff, ResultDone: true}
			lines := block.Render(80, "")
			deleted := renderedLineContaining(t, lines, "package old")
			added := renderedLineContaining(t, lines, "package main")
			assertRenderedTextBackground(t, deleted, "package", colorOfTheme(currentStyles.Palette.DiffDelLineBg))
			assertRenderedTextBackground(t, added, "package", colorOfTheme(currentStyles.Palette.DiffAddLineBg))
		})
	}
}

func TestEditAndApplyPatchToolCardsHighlightContextLines(t *testing.T) {
	ApplyTheme(uitheme.Default())
	args, _ := json.Marshal(map[string]string{"path": "src/demo.go"})
	diff := "@@ -1,5 +1,6 @@\n func demo() error {\n+\tvalue := 1\n if value != 0 {\n\treturn fmt.Errorf(\"bad: %d\", value)\n }\n return nil\n"
	wantKeyword := renderedCellsForText(t, syntax.New("sample.go", "", "").Line("func sample() {}", ""), "func")[0].Style.Fg
	for _, toolName := range []string{tools.NameEdit, tools.NameApplyPatch} {
		t.Run(toolName, func(t *testing.T) {
			block := &Block{ID: 1, Type: BlockToolCall, ToolName: toolName, Content: string(args), Diff: diff, ResultDone: true}
			contextLine := renderedLineContaining(t, block.Render(100, ""), "if value")
			assertRenderedTextForeground(t, contextLine, "if", wantKeyword)
			assertRenderedTextBackground(t, contextLine, "if", colorOfTheme(currentStyles.Palette.ToolCallBg))
		})
	}
}

func TestEditToolCardRendersDiagnosticsSummaryWithDiff(t *testing.T) {
	block := &Block{
		ID:           1,
		Type:         BlockToolCall,
		ToolName:     tools.NameEdit,
		Content:      `{"path":"internal/config/config_project_test.go","patch":"@@\n-old\n+new\n"}`,
		ResultDone:   true,
		ResultStatus: message.ToolStatusSuccess,
		ResultContent: strings.Join([]string{
			"Applied patch to internal/config/config_project_test.go (+3 -2)",
			"",
			"Diagnostics summary:",
			"[E] 34:36 [UndeclaredName] undefined: DefaultContextReductionConfig",
		}, "\n"),
		Diff: "--- internal/config/config_project_test.go\n+++ internal/config/config_project_test.go\n@@ -1 +1 @@\n-old\n+new\n",
	}

	plain := terminaltext.StripANSI(strings.Join(block.Render(120, ""), "\n"))
	if !strings.Contains(plain, "↳ Diagnostics:") {
		t.Fatalf("expected diagnostics section to render with edit diff, got:\n%s", plain)
	}
	if !strings.Contains(plain, "undefined: DefaultContextReductionConfig") {
		t.Fatalf("expected diagnostic detail to render with edit diff, got:\n%s", plain)
	}
	if strings.Contains(plain, "Diagnostics summary:") || strings.Contains(plain, "Applied patch to internal/config/config_project_test.go") {
		t.Fatalf("expected only diagnostics detail in edit success diagnostics section, got:\n%s", plain)
	}
}

func TestEditLiveArgsWithoutCompletePathDoNotRenderDot(t *testing.T) {
	displayArgs := streamingToolDisplayArgs(tools.NameEdit, `{"patch":"*** Begin Patch\n*** Update File:`, "")
	if displayArgs != "" {
		t.Fatalf("display args = %q, want empty until path is parsed", displayArgs)
	}
	block := &Block{
		ID:       1,
		Type:     BlockToolCall,
		ToolName: tools.NameEdit,
		Content:  displayArgs,
	}
	plain := terminaltext.StripANSI(strings.Join(block.Render(100, ""), "\n"))
	if strings.Contains(plain, "edit .") {
		t.Fatalf("expected incomplete Edit args not to render dot path, got:\n%s", plain)
	}
}

func TestLexerForFilePathKeepsBackgroundForUnknownExtension(t *testing.T) {
	ApplyTheme(uitheme.Default())
	backgrounds := []struct {
		name  string
		value string
	}{
		{name: "tool", value: currentStyles.Palette.ToolCallBg},
		{name: "diff add", value: currentStyles.Palette.DiffAddLineBg},
		{name: "diff delete", value: currentStyles.Palette.DiffDelLineBg},
	}
	for _, background := range backgrounds {
		t.Run(background.name, func(t *testing.T) {
			hl := syntax.New("notes.unknownext", "package tools\n", "")
			rendered := hl.Line("package tools", background.value)
			if plain := terminaltext.StripANSI(rendered); plain != "package tools" {
				t.Fatalf("expected unknown extension background fallback to preserve text, got %q", plain)
			}
			wantBg := syntax.ColorSequence(lipgloss.Color(background.value), false)
			if wantBg == "" {
				t.Fatal("expected background color to produce an ANSI sequence")
			}
			if !strings.Contains(rendered, wantBg) {
				t.Fatalf("expected unknown extension fallback to keep background %q, got %q", wantBg, rendered)
			}
			spaceRendered := hl.Line("    ", background.value)
			if plain := terminaltext.StripANSI(spaceRendered); plain != "    " {
				t.Fatalf("expected whitespace-only fallback to preserve spaces, got %q", plain)
			}
			if !strings.Contains(spaceRendered, wantBg) {
				t.Fatalf("expected whitespace-only fallback to keep background %q, got %q", wantBg, spaceRendered)
			}
		})
	}
}

func TestRenderInlineDiffLineKeepsSingleTokenInsertionSingleLine(t *testing.T) {
	lines := renderInlineDiffLine("myVariable", "myHTTPVariable", 40, nil)
	if len(lines) != 1 {
		t.Fatalf("expected single-line inline diff, got %d lines: %#v", len(lines), lines)
	}
	plain := terminaltext.StripANSI(lines[0])
	if !strings.HasPrefix(plain, "+") {
		t.Fatalf("expected insertion line, got %q", plain)
	}
	if !strings.Contains(plain, "myHTTPVariable") {
		t.Fatalf("expected inserted token to remain visible, got %q", plain)
	}
}

func TestRenderInlineDiffLineKeepsSyntaxHighlighting(t *testing.T) {
	hl := syntax.New("example.go", "func demo() {\n\treturn 1\n}\n", "")
	lines := renderInlineDiffLine("return 1", "return 10", 80, hl)
	if len(lines) != 1 {
		t.Fatalf("expected single-line inline diff, got %#v", lines)
	}
	if !strings.Contains(terminaltext.StripANSI(lines[0]), "+return 10") {
		t.Fatalf("expected changed line to stay visible, got %q", terminaltext.StripANSI(lines[0]))
	}
	keywordColour := renderedCellsForText(t, syntax.New("sample.go", "", "").Line("func sample() {}", ""), "func")[0].Style.Fg
	assertRenderedTextForeground(t, lines[0], "return", keywordColour)
	assertRenderedTextBackground(t, lines[0], "0", colorOfTheme(currentStyles.Palette.DiffAddInlineBg))
}

func TestEditToolCardKeepsFinalInlineDiffBackgrounds(t *testing.T) {
	ApplyTheme(uitheme.Default())
	longPrefix := strings.Repeat("prefix", 12)
	longSuffix := strings.Repeat("suffix", 12)
	tests := []struct {
		name              string
		oldLine           string
		newLine           string
		lineText          string
		unchangedText     string
		changedText       string
		changedBackground string
		wantEllipsis      bool
	}{
		{
			name:              "short insertion",
			oldLine:           "return 1",
			newLine:           "return 10",
			lineText:          "+return 10",
			unchangedText:     "return",
			changedText:       "0",
			changedBackground: currentStyles.Palette.DiffAddInlineBg,
		},
		{
			name:              "short deletion",
			oldLine:           "return 10",
			newLine:           "return 1",
			lineText:          "-return 10",
			unchangedText:     "return",
			changedText:       "0",
			changedBackground: currentStyles.Palette.DiffDelInlineBg,
		},
		{
			name:              "long insertion window",
			oldLine:           longPrefix + " value " + longSuffix,
			newLine:           longPrefix + " valueHTTP " + longSuffix,
			lineText:          "valueHTTP",
			unchangedText:     "suffix",
			changedText:       "HTTP",
			changedBackground: currentStyles.Palette.DiffAddInlineBg,
			wantEllipsis:      true,
		},
	}

	args, _ := json.Marshal(map[string]string{"path": "example.go"})
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			block := &Block{
				ID:         1,
				Type:       BlockToolCall,
				ToolName:   tools.NameEdit,
				Content:    string(args),
				Diff:       "@@ -1,1 +1,1 @@\n-" + tt.oldLine + "\n+" + tt.newLine + "\n",
				ResultDone: true,
			}
			line := renderedLineContaining(t, block.Render(60, ""), tt.lineText)
			assertRenderedTextBackground(t, line, tt.unchangedText, colorOfTheme(currentStyles.Palette.ToolCallBg))
			assertRenderedTextBackground(t, line, tt.changedText, colorOfTheme(tt.changedBackground))
			if tt.wantEllipsis && !strings.Contains(terminaltext.StripANSI(line), "…") {
				t.Fatalf("expected long inline diff to use a snippet window, got %q", terminaltext.StripANSI(line))
			}
		})
	}
}

func TestRenderInlineDiffLineKeepsTabIndentedDeletionAligned(t *testing.T) {
	oldLine := "\tcase tools.NameGrep, tools.NameGlob, tools.NameShell, tools.NameJobOutput, tools.NameViewImage:"
	newLine := "\tcase tools.NameGrep, tools.NameGlob, tools.NameShell, tools.NameJobOutput:"
	hl := syntax.New("example.go", "package tui\n\nfunc example() {\n"+oldLine+"\n}\n", "")

	lines := renderInlineDiffLine(oldLine, newLine, 120, hl)
	if len(lines) != 1 {
		t.Fatalf("expected single-line inline diff, got %#v", lines)
	}
	wantLine := "-" + terminaltext.ExpandTabs(oldLine, terminaltext.TabWidth)
	if got := terminaltext.StripANSI(lines[0]); got != wantLine {
		t.Fatalf("rendered line = %q, want %q", got, wantLine)
	}
	want := currentStyles.DiffDelInlineStyle.Render(", tools.NameViewImage")
	if !strings.Contains(lines[0], want) {
		t.Fatalf("expected deletion style to cover only %q, got %q", ", tools.NameViewImage", lines[0])
	}
}

func TestDiffTextWidthMatchesGraphemeRendererWithTabs(t *testing.T) {
	for _, text := range []string{
		"界",
		"👨‍👩‍👧‍👦",
		"👍🏽",
		"e\u0301",
		"\t👨‍👩‍👧‍👦",
		"a\t👍🏽",
	} {
		expanded := terminaltext.ExpandTabs(text, terminaltext.TabWidth)
		if got, want := diffTextWidth(text), terminaltext.Width(expanded); got != want {
			t.Fatalf("diffTextWidth(%q) = %d, want rendered width %d", text, got, want)
		}
	}
}

func TestRenderInlineDiffLineUsesGraphemeWidthLimit(t *testing.T) {
	prefix := strings.Repeat("👨‍👩‍👧‍👦", 40)
	oldLine := prefix
	newLine := prefix + "x"
	if got := terminaltext.Width(oldLine); got > singleLineDiffColumnsLimit {
		t.Fatalf("fixture rendered width = %d, want <= %d", got, singleLineDiffColumnsLimit)
	}

	lines := renderInlineDiffLine(oldLine, newLine, 120, nil)
	if len(lines) != 1 {
		t.Fatalf("grapheme-width eligible inline diff = %#v, want one line", lines)
	}
	if got := terminaltext.StripANSI(lines[0]); got != "+"+newLine {
		t.Fatalf("rendered line = %q, want %q", got, "+"+newLine)
	}
}

func TestRenderInlineDiffLineFallsBackForMixedTokenRewrite(t *testing.T) {
	lines := renderInlineDiffLine("prefixSuffix", "preFIXmidSUFsuffix", 80, nil)
	if lines != nil {
		t.Fatalf("expected mixed token rewrite to fall back to two-line diff, got %#v", lines)
	}
}

func TestRenderInlineDiffLineFallsBackForPureInsertionWithMultipleRunsInOneToken(t *testing.T) {
	lines := renderInlineDiffLine("myVariable", "myHVariableTTPX", 80, nil)
	if lines != nil {
		t.Fatalf("expected fragmented same-token insertion to fall back to two-line diff, got %#v", lines)
	}
}

func TestRenderInlineDiffLineKeepsSingleTokenDeletionSingleLine(t *testing.T) {
	lines := renderInlineDiffLine("github.com/org/service/internal/api", "github.com/org/service/api", 60, nil)
	if len(lines) != 1 {
		t.Fatalf("expected single-line deletion diff, got %#v", lines)
	}
	plain := terminaltext.StripANSI(lines[0])
	if !strings.HasPrefix(plain, "-") {
		t.Fatalf("expected deletion line, got %q", plain)
	}
	if !strings.Contains(plain, "internal/") {
		t.Fatalf("expected deleted path segment to remain visible, got %q", plain)
	}
}

func TestRenderInlineDiffLineFunctionArgumentExpansionFallsBackToTwoLineDiff(t *testing.T) {
	oldLine := strings.Repeat("prefix", 6) + " foo(bar, baz) " + strings.Repeat("suffix", 6)
	newLine := strings.Repeat("prefix", 6) + " foo(longBar, baz) " + strings.Repeat("suffix", 6)
	lines := renderInlineDiffLine(oldLine, newLine, 28, nil)
	if lines != nil {
		t.Fatalf("expected argument expansion with token rewrite to fall back to two-line diff, got %#v", lines)
	}
}

func TestRenderInlineDiffLineFallsBackForMultiTokenMixedRewrite(t *testing.T) {
	lines := renderInlineDiffLine("foo(bar, baz)", "foo(longBar, qux)", 80, nil)
	if lines != nil {
		t.Fatalf("expected multi-token mixed rewrite to use two-line diff, got %#v", lines)
	}
}

func TestRenderInlineDiffLineFallsBackBeyondHardColumnLimit(t *testing.T) {
	oldLine := strings.Repeat("a", 201)
	newLine := oldLine + "HTTP"
	lines := renderInlineDiffLine(oldLine, newLine, 120, nil)
	if lines != nil {
		t.Fatalf("expected >200-column line to force two-line diff, got %#v", lines)
	}
}

func TestRenderInlineDiffLineFallsBackBeyondConfiguredColumnLimit(t *testing.T) {
	oldLimit := singleLineDiffColumnsLimit
	SetSingleLineDiffColumnsLimit(20)
	defer SetSingleLineDiffColumnsLimit(oldLimit)

	oldLine := "012345678901234567890"
	newLine := oldLine + "HTTP"
	lines := renderInlineDiffLine(oldLine, newLine, 80, nil)
	if lines != nil {
		t.Fatalf("expected configured width limit to force two-line diff, got %#v", lines)
	}
}

func TestSetSingleLineDiffColumnsLimitResetsOnInvalidValue(t *testing.T) {
	oldLimit := singleLineDiffColumnsLimit
	defer SetSingleLineDiffColumnsLimit(oldLimit)

	SetSingleLineDiffColumnsLimit(123)
	if singleLineDiffColumnsLimit != 123 {
		t.Fatalf("singleLineDiffColumnsLimit = %d, want 123", singleLineDiffColumnsLimit)
	}
	SetSingleLineDiffColumnsLimit(0)
	if singleLineDiffColumnsLimit != defaultSingleLineDiffColumns {
		t.Fatalf("singleLineDiffColumnsLimit = %d, want default %d", singleLineDiffColumnsLimit, defaultSingleLineDiffColumns)
	}
}

func TestRenderInlineDiffLineLongLineUsesChangeSnippet(t *testing.T) {
	oldLine := strings.Repeat("prefix", 8) + " myVariable " + strings.Repeat("suffix", 8)
	newLine := strings.Repeat("prefix", 8) + " myHTTPVariable " + strings.Repeat("suffix", 8)
	lines := renderInlineDiffLine(oldLine, newLine, 24, nil)
	if len(lines) != 1 {
		t.Fatalf("expected single-line snippet diff, got %#v", lines)
	}
	plain := terminaltext.StripANSI(lines[0])
	if !strings.Contains(plain, "myHTTPVariable") {
		t.Fatalf("expected snippet to keep changed region visible, got %q", plain)
	}
	if !strings.Contains(plain, "…") {
		t.Fatalf("expected snippet ellipsis for long line, got %q", plain)
	}
}

func TestRenderFileDiffCallHeaderShowsRelativePathInsideWorkingDir(t *testing.T) {
	wd := filepath.Join(string(os.PathSeparator), "tmp", "workspace")
	abs := filepath.Join(wd, "internal", "tui", "example.go")
	block := &Block{
		ID:                1,
		Type:              BlockToolCall,
		ToolName:          tools.NameEdit,
		Content:           fmt.Sprintf(`{"path":"%s","patch":"@@\n-old\n+new\n"}`, filepath.Join("internal", "tui", "example.go")),
		Diff:              "--- example.go\n+++ example.go\n@@ -1,1 +1,1 @@\n-old\n+new\n",
		ResultDone:        true,
		ResultStatus:      message.ToolStatusSuccess,
		displayWorkingDir: wd,
	}
	joined := terminaltext.StripANSI(strings.Join(block.Render(120, ""), "\n"))
	want := filepath.Join("internal", "tui", "example.go")
	if !strings.Contains(joined, "edit") || !strings.Contains(joined, want) {
		t.Fatalf("expected edit header to show relative path; got:\n%s", joined)
	}
	_ = abs
}

func TestRenderFileDiffCallInsertionContextUsesNewLineNumbers(t *testing.T) {
	block := &Block{
		ID:       1,
		Type:     BlockToolCall,
		ToolName: tools.NameEdit,
		Content:  `{"path":"example.py","patch":"@@\n-old\n+new\n"}`,
		Diff: "--- a/example.py\n+++ b/example.py\n@@ -8,4 +8,5 @@\n" +
			" def build_items():\n" +
			"     items = [\n" +
			"+        \"added\",\n" +
			"         \"existing\",\n",
		ResultDone:   true,
		ResultStatus: message.ToolStatusSuccess,
	}
	plain := terminaltext.StripANSI(strings.Join(block.Render(120, ""), "\n"))
	if !strings.Contains(plain, "  10 +        \"added\",") {
		t.Fatalf("expected inserted line to use new line number 10, got:\n%s", plain)
	}
	if !strings.Contains(plain, "  11          \"existing\",") {
		t.Fatalf("expected following context line to use new line number 11, got:\n%s", plain)
	}
}

func TestRenderFileDiffCallDeletionContextDoesNotDecreaseLineNumbers(t *testing.T) {
	block := &Block{
		ID:       1,
		Type:     BlockToolCall,
		ToolName: tools.NameEdit,
		Content:  `{"path":"example.py","patch":"@@\n-old\n+new\n"}`,
		Diff: "--- a/example.py\n+++ b/example.py\n@@ -8,5 +8,4 @@\n" +
			" def build_items():\n" +
			"     items = [\n" +
			"-        \"removed\",\n" +
			"         \"existing\",\n",
		ResultDone:   true,
		ResultStatus: message.ToolStatusSuccess,
	}
	plain := terminaltext.StripANSI(strings.Join(block.Render(120, ""), "\n"))
	if !strings.Contains(plain, "  10 -        \"removed\",") {
		t.Fatalf("expected deleted line to use old line number 10, got:\n%s", plain)
	}
	if !strings.Contains(plain, "  11          \"existing\",") {
		t.Fatalf("expected following context line to avoid decreasing from deleted line number 10, got:\n%s", plain)
	}
}

func TestRenderFileDiffCallGroupedMinusPlusBlockUsesInlineOneSidedPairs(t *testing.T) {
	old := "\t\t// separator(1) + content(lines) + bottom margin(1) + extra bars\n\t\treturn lines + 2 + extraBars\n"
	new := "\t\t// separator(1) + content(lines) + bottom margin(1)\n\t\treturn lines + 2\n"
	diff := textdiff.Unified(old, new, "example.go").Text
	if diff == "" {
		t.Fatal("expected non-empty unified diff")
	}
	block := &Block{
		ID:           1,
		Type:         BlockToolCall,
		ToolName:     tools.NameEdit,
		Content:      `{"path":"example.go","patch":"@@\n-old\n+new\n"}`,
		Diff:         diff,
		ResultDone:   true,
		ResultStatus: message.ToolStatusSuccess,
	}
	lines := block.Render(120, "")
	plain := terminaltext.StripANSI(strings.Join(lines, "\n"))
	if strings.Contains(plain, "   1 +") || strings.Contains(plain, "   2 +") {
		t.Fatalf("expected grouped pure deletions to render as inline '-' lines only, got:\n%s", plain)
	}
	if !strings.Contains(plain, "extra bars") || !strings.Contains(plain, "extraBars") {
		t.Fatalf("expected deleted fragments to remain visible in inline diff, got:\n%s", plain)
	}
}

func TestRenderFileDiffCallUnequalMinusPlusBlocksUseWholeLineBackground(t *testing.T) {
	tests := []struct {
		name            string
		oldLines        []string
		newLines        []string
		wantLineNumbers bool
	}{
		{
			name:     "two deletions one addition",
			oldLines: []string{"func demo() {", "    value := 1"},
			newLines: []string{"func demo() {    value := 1"},
		},
		{
			name:            "one deletion two additions",
			oldLines:        []string{"old line"},
			newLines:        []string{"new line one", "new line two"},
			wantLineNumbers: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ApplyTheme(uitheme.Default())
			old := strings.Join(tt.oldLines, "\n") + "\n"
			new := strings.Join(tt.newLines, "\n") + "\n"
			diff := textdiff.Unified(old, new, "example.go").Text
			if diff == "" {
				t.Fatal("expected non-empty unified diff")
			}
			block := &Block{
				ID:         1,
				Type:       BlockToolCall,
				ToolName:   tools.NameEdit,
				Content:    `{"path":"example.go","patch":"@@\n-old\n+new\n"}`,
				Diff:       diff,
				ResultDone: true,
			}

			lines := block.Render(120, "")
			plain := terminaltext.StripANSI(strings.Join(lines, "\n"))
			for _, oldLine := range tt.oldLines {
				deleted := renderedLineContaining(t, lines, oldLine)
				assertRenderedTextBackground(t, deleted, oldLine, colorOfTheme(currentStyles.Palette.DiffDelLineBg))
			}
			for _, newLine := range tt.newLines {
				added := renderedLineContaining(t, lines, newLine)
				assertRenderedTextBackground(t, added, newLine, colorOfTheme(currentStyles.Palette.DiffAddLineBg))
			}
			if tt.wantLineNumbers {
				for _, want := range []string{"  1 -old line", "  1 +new line one", "  2 +new line two"} {
					if !strings.Contains(plain, want) {
						t.Fatalf("expected line-numbered uneven diff to contain %q, got:\n%s", want, plain)
					}
				}
			}
		})
	}
}

func TestRenderFileDiffCallExpandedShowsAllLinesPastFormerLimit(t *testing.T) {
	ApplyTheme(uitheme.Default())
	diffLines := []string{
		"--- example.go",
		"+++ example.go",
		fmt.Sprintf("@@ -1,%d +1,%d @@", 250, 250),
	}
	for i := 1; i < 250; i++ {
		diffLines = append(diffLines, fmt.Sprintf(" context line %d", i))
	}
	diffLines = append(diffLines, "-old value", "+new value")
	block := &Block{
		ID:         1,
		Type:       BlockToolCall,
		ToolName:   tools.NameEdit,
		Content:    `{"path":"example.go","patch":"@@\n-old\n+new\n"}`,
		Diff:       strings.Join(diffLines, "\n") + "\n",
		ResultDone: true,
	}

	plain := terminaltext.StripANSI(strings.Join(block.Render(120, ""), "\n"))
	for _, want := range []string{"old value", "new value", "edit example.go"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("expected expanded diff to contain %q, got:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "... (diff truncated)") {
		t.Fatalf("did not expect expanded diff truncation marker, got:\n%s", plain)
	}
}

func TestRenderFileDiffCallPureDeletionLongLineUsesSnippets(t *testing.T) {
	oldLine := strings.Repeat("prefix", 7) + " github.com/org/service/internal/api " + strings.Repeat("suffix", 7)
	newLine := strings.Repeat("prefix", 7) + " github.com/org/service/api " + strings.Repeat("suffix", 7)
	block := &Block{
		ID:           1,
		Type:         BlockToolCall,
		ToolName:     tools.NameEdit,
		Content:      `{"path":"example.go","patch":"@@\n-old\n+new\n"}`,
		Diff:         fmt.Sprintf("--- example.go\n+++ example.go\n@@ -1,1 +1,1 @@\n-%s\n+%s\n", oldLine, newLine),
		ResultDone:   true,
		ResultStatus: message.ToolStatusSuccess,
	}
	lines := block.Render(46, "")
	plain := terminaltext.StripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "service/internal/api") {
		t.Fatalf("expected snippet to keep deleted path region visible, got:\n%s", plain)
	}
	if !strings.Contains(plain, "…") {
		t.Fatalf("expected snippet to use ellipsis, got:\n%s", plain)
	}
}

func TestRenderFileDiffCallOverHardColumnLimitUsesTwoLines(t *testing.T) {
	oldLine := strings.Repeat("a", 201)
	newLine := oldLine + "HTTP"
	block := &Block{
		ID:           1,
		Type:         BlockToolCall,
		ToolName:     tools.NameEdit,
		Content:      `{"path":"example.go","patch":"@@\n-old\n+new\n"}`,
		Diff:         fmt.Sprintf("--- example.go\n+++ example.go\n@@ -1,1 +1,1 @@\n-%s\n+%s\n", oldLine, newLine),
		ResultDone:   true,
		ResultStatus: message.ToolStatusSuccess,
	}
	lines := block.Render(120, "")
	plain := terminaltext.StripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "   1 -") {
		t.Fatalf("expected old line in two-line diff, got:\n%s", plain)
	}
	if !strings.Contains(plain, "   1 +") {
		t.Fatalf("expected new line in two-line diff, got:\n%s", plain)
	}
}

func TestRenderFileDiffCallMixedLongLinesUseTwoLineSnippets(t *testing.T) {
	oldLine := strings.Repeat("prefix", 8) + " myVariable " + strings.Repeat("suffix", 8)
	newLine := strings.Repeat("prefix", 8) + " otherHTTPValue " + strings.Repeat("suffix", 8)
	block := &Block{
		ID:           1,
		Type:         BlockToolCall,
		ToolName:     tools.NameEdit,
		Content:      `{"path":"example.go","patch":"@@\n-old\n+new\n"}`,
		Diff:         fmt.Sprintf("--- example.go\n+++ example.go\n@@ -1,1 +1,1 @@\n-%s\n+%s\n", oldLine, newLine),
		ResultDone:   true,
		ResultStatus: message.ToolStatusSuccess,
	}
	lines := block.Render(38, "")
	plain := terminaltext.StripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "   1 -") || !strings.Contains(plain, "   1 +") {
		t.Fatalf("expected mixed rewrite to render as two lines, got:\n%s", plain)
	}
	if !strings.Contains(plain, "myVariable") {
		t.Fatalf("expected old snippet to preserve changed token, got:\n%s", plain)
	}
	if !strings.Contains(plain, "otherHTTPValue") {
		t.Fatalf("expected new snippet to preserve changed token, got:\n%s", plain)
	}
	if !strings.Contains(plain, "…") {
		t.Fatalf("expected long two-line diff to use ellipsis snippets, got:\n%s", plain)
	}
}

func TestRenderHighlightedSnippetLineShowsHiddenClusterSummary(t *testing.T) {
	hl := syntax.New("example.go", "", "")
	line := "alpha ONE beta TWO gamma THREE delta"
	spans := []diffSegmentSpan{
		{StartCol: strings.Index(line, "ONE"), EndCol: strings.Index(line, "ONE") + len("ONE")},
		{StartCol: strings.Index(line, "TWO"), EndCol: strings.Index(line, "TWO") + len("TWO")},
		{StartCol: strings.Index(line, "THREE"), EndCol: strings.Index(line, "THREE") + len("THREE")},
	}
	rendered := renderHighlightedSnippetLine(line, spans, 26, hl, currentStyles.Palette.ToolCallBg)
	plain := terminaltext.StripANSI(rendered)
	if !strings.Contains(plain, "(+1)") {
		t.Fatalf("expected hidden cluster summary, got %q", plain)
	}
	if !strings.Contains(plain, "ONE") || !strings.Contains(plain, "TWO") {
		t.Fatalf("expected visible clusters to remain in snippet, got %q", plain)
	}
}

func TestRenderHighlightedSnippetLineOmitsSummaryWhenTooNarrow(t *testing.T) {
	hl := syntax.New("example.go", "", "")
	line := "alpha ONE beta TWO gamma THREE delta"
	spans := []diffSegmentSpan{
		{StartCol: strings.Index(line, "ONE"), EndCol: strings.Index(line, "ONE") + len("ONE")},
		{StartCol: strings.Index(line, "TWO"), EndCol: strings.Index(line, "TWO") + len("TWO")},
		{StartCol: strings.Index(line, "THREE"), EndCol: strings.Index(line, "THREE") + len("THREE")},
	}
	rendered := renderHighlightedSnippetLine(line, spans, 12, hl, currentStyles.Palette.ToolCallBg)
	plain := terminaltext.StripANSI(rendered)
	if strings.Contains(plain, "(+1)") {
		t.Fatalf("expected hidden cluster summary to be omitted when width is too narrow, got %q", plain)
	}
}

// TestEditDiffHeaderPathIsCwdRelative guards against regressing to an absolute
// edit path in the diff header. Before the session publishes its working dir
// the card falls back to the process one, so a long absolute prefix (deep tree,
// long $HOME, git worktree) cannot push the file name out of the width-clipped
// header.
func TestEditDiffHeaderPathIsCwdRelative(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	args, _ := json.Marshal(map[string]string{"path": filepath.Join(wd, "src", "demo.go"), "patch": "@@\n-old\n+new\n"})
	block := &Block{
		ID:       1,
		Type:     BlockToolCall,
		ToolName: tools.NameEdit,
		Content:  string(args),
	}

	got := block.diffToolFilePath()
	if filepath.IsAbs(got) {
		t.Fatalf("diffToolFilePath returned absolute path %q, want cwd-relative", got)
	}
	if got != "src/demo.go" {
		t.Fatalf("diffToolFilePath = %q, want %q", got, "src/demo.go")
	}

	// The rendered header keeps the file name even at a width far smaller than
	// the absolute path length.
	block.ResultDone = true
	block.ResultStatus = message.ToolStatusSuccess
	block.Diff = "--- src/demo.go\n+++ src/demo.go\n@@ -1 +1 @@\n-old\n+new\n"
	plain := terminaltext.StripANSI(strings.Join(block.Render(60, ""), "\n"))
	if !strings.Contains(plain, "demo.go") {
		t.Fatalf("expected file name to survive header truncation, got:\n%s", plain)
	}
}

// The session may work in a checkout other than the process one (a worktree).
// The edit header has to name files relative to the session working dir, and a
// file from the process checkout must keep its absolute path instead of reading
// as if it lived in the session's.
func TestEditDiffHeaderPathIsSessionRelative(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	sessionDir := t.TempDir()
	for _, tc := range []struct {
		name, path, want string
	}{
		{"inside the session dir", filepath.Join(sessionDir, "src", "app.go"), filepath.Join("src", "app.go")},
		{"inside the process dir only", filepath.Join(wd, "src", "app.go"), filepath.Join(wd, "src", "app.go")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args, _ := json.Marshal(map[string]string{"path": tc.path, "old_string": "a", "new_string": "b"})
			block := &Block{
				ID: 1, Type: BlockToolCall, ToolName: tools.NameEdit,
				Content: string(args), RawArgs: string(args), displayWorkingDir: sessionDir,
				ResultDone: true, ResultStatus: message.ToolStatusSuccess,
				ResultContent: "Replaced 1 occurrence",
				Diff:          "--- src/app.go\n+++ src/app.go\n@@ -1,1 +1,1 @@\n-a\n+b\n",
			}
			if got := block.diffToolFilePath(); got != tc.want {
				t.Fatalf("diffToolFilePath = %q, want %q", got, tc.want)
			}
			plain := terminaltext.StripANSI(strings.Join(block.Render(400, ""), "\n"))
			if !strings.Contains(plain, "edit "+tc.want+" ·") {
				t.Fatalf("expected header to name %q, got:\n%s", tc.want, plain)
			}
		})
	}
}

// Collapsed edit/apply_patch success is a single header line like
// read/grep/glob/write: path, parameters and the +/- line count summary merge
// into the header instead of a second "↳" body line.

func TestEditCardShowsDiffSummaryAndBodyWithoutFolding(t *testing.T) {
	ApplyTheme(uitheme.Default())
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      tools.NameEdit,
		Content:       `{"path":"src/demo.go","patch":"@@\n-old\n+new\n"}`,
		ResultDone:    true,
		ResultStatus:  message.ToolStatusSuccess,
		ResultContent: "Applied patch to src/demo.go (+1 -1)",
		Diff:          "--- src/demo.go\n+++ src/demo.go\n@@ -1,1 +1,1 @@\n-old\n+new\n",
	}

	plain := terminaltext.StripANSI(strings.Join(block.Render(120, ""), "\n"))
	if !strings.Contains(plain, "✓ edit src/demo.go · +1 -1 lines") {
		t.Fatalf("edit header must carry the diff summary; got:\n%s", plain)
	}
	if !strings.Contains(plain, "-old") || !strings.Contains(plain, "+new") {
		t.Fatalf("edit card must show the diff body; got:\n%s", plain)
	}
	if strings.Contains(plain, "▸") || strings.Contains(plain, "▾") {
		t.Fatalf("edit card must not show a disclosure marker; got:\n%s", plain)
	}
	if block.ToggleAtWidth(120) {
		t.Fatal("space must be a no-op on the always-expanded edit card")
	}
}

func TestApplyPatchCardShowsFileAndDiffSummaryWithoutFolding(t *testing.T) {
	ApplyTheme(uitheme.Default())
	args := `{"patch":"*** Begin Patch\n*** Update File: src/demo.go\n@@\n-old\n+new\n*** End Patch"}`
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      tools.NameApplyPatch,
		Content:       applyPatchToolDisplayArgs(args),
		RawArgs:       args,
		ResultDone:    true,
		ResultStatus:  message.ToolStatusSuccess,
		ResultContent: "Applied patch to src/demo.go (+1 -1)",
		Diff:          "--- src/demo.go\n+++ src/demo.go\n@@ -1,1 +1,1 @@\n-old\n+new\n",
	}

	plain := terminaltext.StripANSI(strings.Join(block.Render(120, ""), "\n"))
	if !strings.Contains(plain, "✓ apply_patch src/demo.go · 1 file · +1 -1 lines") {
		t.Fatalf("apply_patch header must merge file/diff counts; got:\n%s", plain)
	}
	if !strings.Contains(plain, "-old") || !strings.Contains(plain, "+new") {
		t.Fatalf("apply_patch card must show the diff body; got:\n%s", plain)
	}
	if strings.Contains(plain, "▸") || strings.Contains(plain, "▾") {
		t.Fatalf("apply_patch card must not show a disclosure marker; got:\n%s", plain)
	}
	if block.ToggleAtWidth(120) {
		t.Fatal("space must be a no-op on the always-expanded apply_patch card")
	}
}

func TestEditDiffSummaryOmitsZeroDirection(t *testing.T) {
	ApplyTheme(uitheme.Default())
	cases := []struct {
		name string
		diff string
		want string
	}{
		{
			name: "pure additions",
			diff: "--- src/demo.go\n+++ src/demo.go\n@@ -1,1 +1,3 @@\n old\n+new\n+more\n",
			want: "✓ edit src/demo.go · +2 lines",
		},
		{
			name: "pure deletions",
			diff: "--- src/demo.go\n+++ src/demo.go\n@@ -1,3 +1,1 @@\n-old\n-gone\n new\n",
			want: "✓ edit src/demo.go · -2 lines",
		},
		{
			name: "single addition",
			diff: "--- src/demo.go\n+++ src/demo.go\n@@ -1,1 +1,2 @@\n old\n+new\n",
			want: "✓ edit src/demo.go · +1 line",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			block := &Block{
				ID:            1,
				Type:          BlockToolCall,
				ToolName:      tools.NameEdit,
				Content:       `{"path":"src/demo.go","patch":"@@\n-old\n+new\n"}`,
				ResultDone:    true,
				ResultStatus:  message.ToolStatusSuccess,
				ResultContent: "Applied patch to src/demo.go",
				Diff:          tc.diff,
			}
			plain := terminaltext.StripANSI(strings.Join(block.Render(120, ""), "\n"))
			if !strings.Contains(plain, tc.want) {
				t.Fatalf("diff summary must omit the zero direction, want %q; got:\n%s", tc.want, plain)
			}
		})
	}
}

func TestEditCollapsedErrorKeepsErrorBlockWithoutHeaderSummary(t *testing.T) {
	ApplyTheme(uitheme.Default())
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      tools.NameEdit,
		Content:       `{"path":"src/demo.go","patch":"@@\n-old\n+new\n"}`,
		Collapsed:     true,
		ResultDone:    true,
		ResultStatus:  message.ToolStatusError,
		ResultContent: "hunk not found",
		Diff:          "--- src/demo.go\n+++ src/demo.go\n@@ -1,1 +1,1 @@\n-old\n+new\n",
	}

	plain := terminaltext.StripANSI(strings.Join(block.Render(120, ""), "\n"))
	if !strings.Contains(plain, "↳ Error:") || !strings.Contains(plain, "hunk not found") {
		t.Fatalf("expected the error block to render; got:\n%s", plain)
	}
	if strings.Contains(plain, "· error") {
		t.Fatalf("collapsed error header must not repeat an error summary; got:\n%s", plain)
	}
}

// TestDiffGutterNumbersCountlessHunkHeader pins the gutter numbers for the
// `@@ -1 +1 @@` form: unified diff omits `,count` when the count is 1, and a
// header the regexp cannot parse used to leave the previous hunk's counters in
// place, numbering every following line confidently wrong.
func TestDiffGutterNumbersCountlessHunkHeader(t *testing.T) {
	args, _ := json.Marshal(map[string]string{"path": "src/demo.go", "patch": "@@\n-old\n+new\n"})
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      tools.NameEdit,
		Content:       string(args),
		ResultDone:    true,
		ResultStatus:  message.ToolStatusSuccess,
		ResultContent: "Applied patch to src/demo.go (+1 -1)",
		Diff:          "--- src/demo.go\n+++ src/demo.go\n@@ -12 +12 @@\n-old\n+new\n",
	}

	plain := terminaltext.StripANSI(strings.Join(block.Render(100, ""), "\n"))
	for _, want := range []string{"12 -old", "12 +new"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("expected gutter %q from a countless hunk header, got:\n%s", want, plain)
		}
	}
}

func TestApplyPatchStreamingPreviewExtractsTextFromPartialArgs(t *testing.T) {
	complete := `{"patch":"*** Begin Patch\n*** Update File: src/demo.go\n@@\n-old\n+new\n*** End Patch"}`
	if got := applyPatchStreamingPreview(complete); got != "*** Begin Patch\n*** Update File: src/demo.go\n@@\n-old\n+new\n*** End Patch" {
		t.Fatalf("applyPatchStreamingPreview(complete) = %q", got)
	}
	// Mid-stream fragment without the closing quote/brace returns the text so far.
	fragment := `{"patch":"*** Begin Patch\n*** Update File: src/demo.go\n@@\n-old`
	if got := applyPatchStreamingPreview(fragment); got != "*** Begin Patch\n*** Update File: src/demo.go\n@@\n-old" {
		t.Fatalf("applyPatchStreamingPreview(fragment) = %q", got)
	}
	// Freeform custom-tool shape: bare patch text (no JSON envelope) is shown
	// as-is, including mid-stream fragments.
	bare := "*** Begin Patch\n*** Update File: src/demo.go\n@@\n-old"
	if got := applyPatchStreamingPreview(bare); got != bare {
		t.Fatalf("applyPatchStreamingPreview(bare freeform) = %q", got)
	}
	if got := applyPatchStreamingPreview("*** Begin Patch\n*** End Patch"); got != "*** Begin Patch\n*** End Patch" {
		t.Fatalf("applyPatchStreamingPreview(bare complete) = %q", got)
	}
	// A payload that is neither JSON-apply_patch nor bare patch text (e.g. a
	// gateway-lowered {"input":...} object) stays hidden.
	if got := applyPatchStreamingPreview(`{"input":"*** Begin Patch\n*** End Patch"}`); got != "" {
		t.Fatalf("applyPatchStreamingPreview(gateway-lowered) = %q, want empty", got)
	}
	// A trailing lone backslash is an incomplete escape and is dropped.
	if got := applyPatchStreamingPreview(`{"patch":"*** Begin Patch\`); got != "*** Begin Patch" {
		t.Fatalf("applyPatchStreamingPreview(trailing escape) = %q", got)
	}
	// Escaped quotes and backslashes decode to their literals; the escaped
	// quote must not be mistaken for the closing quote.
	if got := applyPatchStreamingPreview(`{"patch":"say \"hi\" \\o"`); got != `say "hi" \o` {
		t.Fatalf("applyPatchStreamingPreview(escapes) = %q", got)
	}
	// \uXXXX escapes decode to the rune.
	if got := applyPatchStreamingPreview(`{"patch":"\u65e7\u6587\u672c"`); got != "旧文本" {
		t.Fatalf("applyPatchStreamingPreview(unicode escape) = %q", got)
	}
	// Leading whitespace and a space after the colon are tolerated.
	if got := applyPatchStreamingPreview("  {\"patch\": \"*** Begin Patch\""); got != "*** Begin Patch" {
		t.Fatalf("applyPatchStreamingPreview(loose spacing) = %q", got)
	}
	// Control characters that survive the raw decode are sanitized for display.
	if got := applyPatchStreamingPreview(`{"patch":"a\u0000b"`); got != "a\\x00b" {
		t.Fatalf("applyPatchStreamingPreview(control char) = %q", got)
	}
}

func TestApplyPatchStreamingPreviewRejectsNonPatchArgs(t *testing.T) {
	for _, args := range []string{
		``,
		`{`,
		`{"patc`,
		`{"path":"src/demo.go"`,
		`{"patch":`,
		`{"patch":123}`,
		`{"patch":""}`,
		`{"patch":"   "}`,
	} {
		if got := applyPatchStreamingPreview(args); got != "" {
			t.Fatalf("applyPatchStreamingPreview(%q) = %q, want empty", args, got)
		}
	}
}

func TestStreamingApplyPatchDisplayArgsPreferPatchTextOverRawJSON(t *testing.T) {
	fragment := `{"patch":"*** Begin Patch\n*** Update File: src/demo.go`
	want := "*** Begin Patch\n*** Update File: src/demo.go"
	if got := streamingToolDisplayArgs(tools.NameApplyPatch, fragment, ""); got != want {
		t.Fatalf("streamingToolDisplayArgs = %q, want patch text %q", got, want)
	}
	// The legacy "patch" alias is normalized the same way.
	if got := streamingToolDisplayArgs("patch", fragment, ""); got != want {
		t.Fatalf("streamingToolDisplayArgs(patch) = %q, want patch text %q", got, want)
	}
	// With no patch text the display stays empty instead of raw JSON.
	if got := streamingToolDisplayArgs(tools.NameApplyPatch, `{"`, ""); got != "" {
		t.Fatalf("streamingToolDisplayArgs(partial JSON) = %q, want empty", got)
	}
	// Complete args still use the stable targets display.
	complete := `{"patch":"*** Begin Patch\n*** Update File: src/demo.go\n@@\n-old\n+new\n*** End Patch"}`
	if got := streamingToolDisplayArgs(tools.NameApplyPatch, complete, ""); got != `{"paths":["src/demo.go"]}` {
		t.Fatalf("streamingToolDisplayArgs(complete) = %q, want paths display", got)
	}
}

func TestApplyPatchStreamingPreviewRendersHighlightedWithoutFullParse(t *testing.T) {
	ApplyTheme(uitheme.Default())
	block := &Block{
		ID:       1,
		Type:     BlockToolCall,
		ToolName: tools.NameApplyPatch,
		// Content holds the best-effort preview text (not JSON) while the
		// args are still streaming.
		Content: "*** Begin Patch\n*** Update File: src/demo.go\n@@\n-func old() {}\n+func main() {}",
		RawArgs: `{"patch":"*** Begin Patch\n*** Update File: src/demo.go\n@@\n-func old() {}\n+func main() {}`,
	}
	lines := block.Render(100, "●")
	plain := terminaltext.StripANSI(strings.Join(lines, "\n"))
	for _, want := range []string{"*** Begin Patch", "*** Update File: src/demo.go", "-func old() {}", "+func main() {}"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("expected streaming preview to contain %q, got:\n%s", want, plain)
		}
	}
	hunkHeader := renderedLineContaining(t, lines, "@@")
	if !strings.Contains(terminaltext.StripANSI(hunkHeader), "─") {
		t.Fatalf("expected streaming patch preview hunk header to include an inline separator, got %q", terminaltext.StripANSI(hunkHeader))
	}
	added := renderedLineContaining(t, lines, "func main")
	assertRenderedTextBackground(t, added, "func", colorOfTheme(currentStyles.Palette.DiffAddLineBg))
}

// TestApplyPatchPreviewIncrementalRenderEqualsCold confirms the line-level
// render memo (appendApplyPatchPreviewLines) and the resolved highlighter
// produce the same card as a cold full render, and that streaming a growing
// patch reuses the rendered prefix instead of diverging. The patch is large
// enough to engage the adaptive coalesce window.
func TestApplyPatchPreviewIncrementalRenderEqualsCold(t *testing.T) {
	ApplyTheme(uitheme.Default())
	const width = 100
	var sb strings.Builder
	sb.WriteString("*** Begin Patch\n")
	sb.WriteString("*** Update File: internal/tui/streamed_preview.go\n")
	sb.WriteString("@@ import (\n")
	for i := range 700 {
		switch i % 3 {
		case 0:
			sb.WriteString(fmt.Sprintf("+\tif err := runStep%d(ctx, value); err != nil {\n", i))
		case 1:
			sb.WriteString(fmt.Sprintf("-\t\treturn nil, fmt.Errorf(\"step %d: %%w\", err)\n", i))
		default:
			sb.WriteString(fmt.Sprintf(" \t\tlogger.Info(\"completed\", zap.Int(\"idx\", %d))\n", i))
		}
	}
	sb.WriteString("*** End Patch")
	patch := sb.String()
	if len(patch) < 16<<10 {
		t.Fatalf("test patch too small (%d bytes) to engage adaptive window", len(patch))
	}
	fullArgs, _ := json.Marshal(map[string]string{"patch": patch})

	stream := &Block{ID: 1, Type: BlockToolCall, ToolName: tools.NameApplyPatch}
	for frac := 1; frac <= 100; frac++ {
		args := string(fullArgs[:len(fullArgs)*frac/100])
		stream.RawArgs = args
		if d := stream.cachedApplyPatchStreamingArgs(args, false); d != "" {
			stream.Content = d
		}
		stream.InvalidateCache()
		_ = stream.Render(width, "")
	}
	// argsStreamingDone finalises the card with the complete args.
	stream.RawArgs = string(fullArgs)
	stream.Content = applyPatchToolDisplayArgs(string(fullArgs))
	stream.InvalidateCache()
	incremental := strings.Join(stream.Render(width, ""), "\n")

	cold := &Block{ID: 1, Type: BlockToolCall, ToolName: tools.NameApplyPatch,
		RawArgs: string(fullArgs), Content: applyPatchToolDisplayArgs(string(fullArgs))}
	coldLines := strings.Join(cold.Render(width, ""), "\n")

	if incremental != coldLines {
		t.Fatalf("incremental render diverged from cold render\n--- cold ---\n%s\n--- incremental ---\n%s", coldLines, incremental)
	}
	if !strings.Contains(incremental, "runStep0") || !strings.Contains(incremental, "Errorf") {
		t.Fatalf("preview missing expected patch lines:\n%s", incremental)
	}
}

// TestApplyPatchPreviewMemoDropsLinesFromAStaleHighlighter pins the memo key:
// the rendered-prefix memo is keyed by the highlighter that produced it, not
// only by the patch text and the width. A multi-file patch (or a first coalesced
// frame whose path has not been parsed out yet) resolves a new file path
// mid-stream, which rebuilds the lexer in place — reusing the memo then left the
// preview in two colour schemes for the rest of the stream, with nothing to heal
// it until the args finished and the memo was cleared.
func TestApplyPatchPreviewMemoDropsLinesFromAStaleHighlighter(t *testing.T) {
	ApplyTheme(uitheme.Default())
	const width = 80

	firstFile := "*** Begin Patch\n*** Update File: demo.py\n@@\n+def main(value):\n+    return {\"key\": value}\n"
	bothFiles := firstFile + "*** Update File: demo.go\n@@\n+func main(value int) map[string]int {\n+\treturn map[string]int{\"key\": value}\n+}\n"

	streamed := &Block{ID: 1, Type: BlockToolCall, ToolName: tools.NameApplyPatch}
	// Frame 1: only the Python file has been streamed, so that is the path the
	// preview highlighter resolves.
	_ = streamed.appendApplyPatchPreviewLines(firstFile, width, streamed.applyPatchPreviewHighlighter("demo.py", firstFile))
	// Frame 2: the stream reaches the second file and the path flips to Go.
	got := streamed.appendApplyPatchPreviewLines(bothFiles, width, streamed.applyPatchPreviewHighlighter("demo.go", bothFiles))

	cold := &Block{ID: 2, Type: BlockToolCall, ToolName: tools.NameApplyPatch}
	want := cold.appendApplyPatchPreviewLines(bothFiles, width, cold.applyPatchPreviewHighlighter("demo.go", bothFiles))

	if len(got) != len(want) {
		t.Fatalf("streamed preview has %d lines, cold render has %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("line %d kept the highlighting of the previous file\n got=%q\nwant=%q", i, got[i], want[i])
		}
	}
}
