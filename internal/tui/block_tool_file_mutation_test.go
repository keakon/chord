package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
	"github.com/keakon/chord/internal/tui/terminaltext"
	uitheme "github.com/keakon/chord/internal/tui/theme"
)

func TestWriteHeaderShowsRelativePathInsideWorkingDir(t *testing.T) {
	wd := filepath.Join(string(os.PathSeparator), "tmp", "workspace")
	abs := filepath.Join(wd, "demo.txt")
	block := &Block{
		ID:                1,
		Type:              BlockToolCall,
		ToolName:          "write",
		Content:           fmt.Sprintf(`{"path":%q,"content":"hello"}`, abs),
		Collapsed:         true,
		ResultDone:        true,
		displayWorkingDir: wd,
	}
	joined := terminaltext.StripANSI(strings.Join(block.Render(100, ""), "\n"))
	if !strings.Contains(joined, "write demo.txt") {
		t.Fatalf("expected Write header to show relative path; got:\n%s", joined)
	}
	if strings.Contains(joined, abs) {
		t.Fatalf("did not expect Write header to show absolute path; got:\n%s", joined)
	}
}

func TestWriteCardMultilineResultDoesNotBypassCardWrapper(t *testing.T) {
	ApplyTheme(uitheme.Default())
	wd := filepath.Join(string(os.PathSeparator), "tmp", "workspace")
	abs := filepath.Join(wd, "demo.go")
	block := &Block{
		ID:         1,
		Type:       BlockToolCall,
		ToolName:   "write",
		Content:    fmt.Sprintf(`{"path":%q,"content":"package demo\n"}`, abs),
		Collapsed:  false,
		ResultDone: true,
		ResultContent: strings.Join([]string{
			"Successfully wrote 66 lines, 1976 bytes",
			"",
			"Diagnostics:",
			"[W] 1:1 warning",
			"... 13 diagnostics not shown due to output limits; they may still need fixing.",
		}, "\n"),
		displayWorkingDir: wd,
	}

	raw := strings.Join(block.Render(120, ""), "\n")
	plain := terminaltext.StripANSI(raw)
	// The write header carries the counts inline ("66 lines · 1976 bytes") and
	// the verbatim success line is deduplicated, so the summary must still
	// appear before the diagnostics section.
	summaryIdx := strings.Index(plain, "66 lines · 1976 bytes")
	diagIdx := strings.Index(plain, "Diagnostics:")
	if summaryIdx < 0 || diagIdx < 0 || summaryIdx > diagIdx {
		t.Fatalf("expected compact write summary to render before diagnostics; got:\n%s", plain)
	}
	if !strings.Contains(plain, "Diagnostics:") {
		t.Fatalf("expected diagnostics text to render; got:\n%s", plain)
	}
	if !strings.Contains(plain, "... 13 diagnostics not shown due to output limits; they may still need fixing.") {
		t.Fatalf("expected omitted diagnostics line to render; got:\n%s", plain)
	}
	if strings.Contains(plain, "Diagnostics:\n\n") {
		t.Fatalf("expected no blank line after Diagnostics header; got:\n%s", plain)
	}
	if !strings.Contains(raw, currentStyles.DimStyle.Render("    ... 13 diagnostics not shown due to output limits; they may still need fixing.")) {
		t.Fatalf("expected omitted diagnostics line to use dim style; got:\n%s", raw)
	}
	for i, line := range strings.Split(plain, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if !strings.HasPrefix(line, "│") {
			t.Fatalf("line %d bypassed card wrapper (missing rail): %q\nfull:\n%s", i, trimmed, plain)
		}
	}
}

func TestWriteCardDiagnosticsSplitBetweenSummaryAndDiagnosticsDetail(t *testing.T) {
	ApplyTheme(uitheme.Default())
	result := strings.Join([]string{
		"Successfully wrote 71 lines, 2113 bytes",
		"",
		"Diagnostics:",
		"LSP diagnostics in other files:",
		"internal/tui/block_tool_render_delete.go:",
		`[E] 9:2 [UnusedImport] "github.com/keakon/chord/internal/tools" imported and not used`,
		`[E] 24:2 [UnusedVar] declared and not used: keys`,
	}, "\n")
	block := &Block{
		ID:            821,
		Type:          BlockToolCall,
		ToolName:      tools.NameWrite,
		Content:       `{"path":"internal/tui/sample_write_target.go","content":"package tui\n"}`,
		ResultDone:    true,
		ResultContent: result,
	}

	plain := terminaltext.StripANSI(strings.Join(block.Render(120, ""), "\n"))
	if got := strings.Count(plain, "71 lines · 2113 bytes"); got != 1 {
		t.Fatalf("Write count summary = %d, want 1; got:\n%s", got, plain)
	}
	if strings.Contains(plain, "Successfully wrote 71 lines, 2113 bytes") {
		t.Fatalf("Write should render the count summary instead of the verbatim success text; got:\n%s", plain)
	}
	if got := strings.Count(plain, "Diagnostics:"); got != 1 {
		t.Fatalf("diagnostics heading count = %d, want 1; got:\n%s", got, plain)
	}
	if !strings.Contains(plain, "[E] 9:2 [UnusedImport]") {
		t.Fatalf("Write card should show LSP diagnostic details; got:\n%s", plain)
	}
	if strings.Contains(plain, "↳ Result:") {
		t.Fatalf("Write card should not render a duplicate generic result section; got:\n%s", plain)
	}
	if strings.Contains(plain, "▸") || strings.Contains(plain, "▾") {
		t.Fatalf("Write card must not show a disclosure marker; got:\n%s", plain)
	}
	if block.ToggleAtWidth(120) {
		t.Fatal("space must be a no-op on the always-expanded Write card")
	}
}

func TestWriteCardIsAlwaysExpanded(t *testing.T) {
	ApplyTheme(uitheme.Default())
	block := &Block{
		ID:         1,
		Type:       BlockToolCall,
		ToolName:   tools.NameWrite,
		Content:    `{"path":"internal/tui/sample_write_target.go","content":"package tui\n"}`,
		ResultDone: true,
		// Trailing newline: the canonical message must still be recognized.
		ResultContent: "Successfully wrote 71 lines, 2113 bytes\n",
	}

	plain := terminaltext.StripANSI(strings.Join(block.Render(120, ""), "\n"))
	want := "✓ write internal/tui/sample_write_target.go · 71 lines · 2113 bytes"
	if !strings.Contains(plain, want) {
		t.Fatalf("Write header must carry the counts, got:\n%s", plain)
	}
	if !strings.Contains(plain, "1  package tui") {
		t.Fatalf("Write card must render the content preview, got:\n%s", plain)
	}
	if strings.Contains(plain, "Successfully wrote") {
		t.Fatalf("Write header must replace the verbatim message, got:\n%s", plain)
	}
	if strings.Contains(plain, "▸") || strings.Contains(plain, "▾") {
		t.Fatalf("Write card must not show a disclosure marker, got:\n%s", plain)
	}
	if block.ToggleAtWidth(120) {
		t.Fatal("space must be a no-op on the always-expanded Write card")
	}
}

func TestSplitWriteResultRecognizesDiagnosticsWithoutSectionHeading(t *testing.T) {
	for _, result := range []string{
		"Successfully wrote 3 lines, 42 bytes\nLSP errors detected\n[E] 2:1 [TypeError] invalid assignment",
		"Successfully wrote 3 lines, 42 bytes\n[E] 2:1 [TypeError] invalid assignment",
		"Successfully wrote 3 lines, 42 bytes\n2:1 invalid assignment",
	} {
		sections := splitWriteResult(result)
		if sections.summary != "Successfully wrote 3 lines, 42 bytes" {
			t.Fatalf("splitWriteResult(%q) summary = %q", result, sections.summary)
		}
		if !strings.Contains(sections.diagnostics, "invalid assignment") {
			t.Fatalf("splitWriteResult(%q) diagnostics = %q", result, sections.diagnostics)
		}
	}
}

func TestEditSuccessWithLSPDiagnosticsRendersDiagnostics(t *testing.T) {
	ApplyTheme(uitheme.Default())
	for _, header := range []string{"Diagnostics:", "Diagnostics summary:"} {
		block := &Block{
			ID:         1,
			Type:       BlockToolCall,
			ToolName:   tools.NameEdit,
			Content:    `{"path":"cmd/chord/setup_templates.go","patch":"@@\n-old\n+new\n"}`,
			Collapsed:  false,
			ResultDone: true,
			ResultContent: strings.Join([]string{
				"Applied patch to cmd/chord/setup_templates.go (+12 -12)",
				"",
				header,
				"[E] 139:10 [UndeclaredName] undefined: config",
				"[E] 141:10 [UndeclaredName] undefined: config",
			}, "\n"),
		}

		plain := terminaltext.StripANSI(strings.Join(block.Render(120, ""), "\n"))
		if !strings.Contains(plain, "↳ Diagnostics:") {
			t.Fatalf("%s: expected edit diagnostics section to render; got:\n%s", header, plain)
		}
		if !strings.Contains(plain, "undefined: config") {
			t.Fatalf("%s: expected edit LSP error to render; got:\n%s", header, plain)
		}
		if strings.Contains(plain, "↳ Result:") {
			t.Fatalf("%s: expected edit diagnostics not to render as a generic result; got:\n%s", header, plain)
		}
		if strings.Contains(plain, "Applied patch to cmd/chord/setup_templates.go") {
			t.Fatalf("%s: expected routine edit success text to stay hidden; got:\n%s", header, plain)
		}
	}
}

func TestWriteCallRendersContentPreviewByDefault(t *testing.T) {
	ApplyTheme(uitheme.Default())
	content := strings.Join([]string{
		"package main",
		"",
		"func main() {",
		`\tfmt.Println("1")`,
		`\tfmt.Println("2")`,
		`\tfmt.Println("3")`,
		`\tfmt.Println("4")`,
		`\tfmt.Println("5")`,
		`\tfmt.Println("6")`,
		`\tfmt.Println("7")`,
		`\tfmt.Println("8")`,
		"}",
	}, "\n")
	args, err := json.Marshal(map[string]string{
		"path":    "cmd/demo/main.go",
		"content": content,
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      "write",
		Content:       string(args),
		ResultDone:    true,
		ResultContent: "Successfully wrote 260 lines, 157 bytes",
	}

	plain := terminaltext.StripANSI(strings.Join(block.Render(120, ""), "\n"))
	for _, want := range []string{"✓ write cmd/demo/main.go", "260 lines · 157 bytes", "1  package main", "12  }"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("expected Write preview to contain %q; got:\n%s", want, plain)
		}
	}
	// The verbatim "Successfully wrote X lines, Y bytes" message stays hidden:
	// the header carries the counts instead.
	if strings.Contains(plain, "Successfully wrote") {
		t.Fatalf("expected Write preview to hide the 'Successfully wrote' message; got:\n%s", plain)
	}
	if strings.Contains(plain, "▸") || strings.Contains(plain, "▾") {
		t.Fatalf("Write card must not show a disclosure marker; got:\n%s", plain)
	}
	if block.ToggleAtWidth(120) {
		t.Fatal("space must be a no-op on the always-expanded Write card")
	}
}

func TestWriteCallSanitizesPreviewControlCharacters(t *testing.T) {
	ApplyTheme(uitheme.Default())
	args, err := json.Marshal(map[string]string{
		"path":    "demo.txt",
		"content": "safe\x1b[31m literal\rnext",
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      "write",
		Content:       string(args),
		Collapsed:     false,
		ResultDone:    true,
		ResultContent: "Successfully wrote 1 line, 22 bytes",
	}

	plain := terminaltext.StripANSI(strings.Join(block.Render(100, ""), "\n"))
	if strings.ContainsRune(plain, '\x1b') {
		t.Fatalf("expected rendered Write preview to not contain raw ESC: %q", plain)
	}
	if !strings.Contains(plain, `safe\x1b[31m literal`) || !strings.Contains(plain, "next") {
		t.Fatalf("expected sanitized Write preview content, got:\n%s", plain)
	}
}

func TestDeleteHeaderShowsRelativePathInsideWorkingDir(t *testing.T) {
	wd := filepath.Join(string(os.PathSeparator), "tmp", "workspace")
	abs := filepath.Join(wd, "internal", "tui", "obsolete.go")
	block := &Block{
		ID:                1,
		Type:              BlockToolCall,
		ToolName:          "delete",
		Content:           fmt.Sprintf(`{"paths":[%q],"reason":"remove obsolete file"}`, abs),
		ResultContent:     "delete completed.\n\nDeleted (1):\n- internal/tui/obsolete.go",
		ResultDone:        true,
		displayWorkingDir: wd,
	}
	joined := terminaltext.StripANSI(strings.Join(block.Render(120, ""), "\n"))
	if !strings.Contains(joined, "delete internal/tui/obsolete.go (remove obsolete file)") ||
		!strings.Contains(joined, "Deleted internal/tui/obsolete.go") {
		t.Fatalf("expected the relative path in the header and below; got:\n%s", joined)
	}
	if strings.Contains(joined, abs) {
		t.Fatalf("did not expect delete header to show absolute path; got:\n%s", joined)
	}
}

func TestDeleteHeaderShowsFilePath(t *testing.T) {
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "delete",
		Content:                `{"paths":["internal/tui/obsolete.go"],"reason":"remove obsolete file"}`,
		ResultContent:          "delete completed.\n\nDeleted (1):\n- internal/tui/obsolete.go",
		ResultDone:             true,
		ToolCallDetailExpanded: false,
	}

	joined := terminaltext.StripANSI(strings.Join(block.Render(120, ""), "\n"))
	// The paths are the subject of a delete, so they lead the header and the
	// reason joins the option group beside them.
	if !strings.Contains(joined, "delete internal/tui/obsolete.go (remove obsolete file)") ||
		!strings.Contains(joined, "Deleted internal/tui/obsolete.go") {
		t.Fatalf("expected the path in the header and the per-path outcome below; got:\n%s", joined)
	}
}

func TestDeleteCardShowsDiagnosticPathsLikeNormalHeader(t *testing.T) {
	wd := filepath.Join(string(os.PathSeparator), "tmp", "workspace")
	block := &Block{
		ID:                1,
		Type:              BlockToolCall,
		ToolName:          tools.NameDelete,
		Content:           `{".paths":["/tmp/workspace/internal/tui/obsolete.go","/tmp/workspace/cmd/old.go"],"reason":"cleanup generated files"}`,
		ResultDone:        true,
		ResultStatus:      message.ToolStatusError,
		ResultContent:     "arguments do not match delete schema: args.paths is required",
		displayWorkingDir: wd,
		Audit: &message.ToolArgsAudit{
			OriginalArgsJSON: `{".paths":["/tmp/workspace/internal/tui/obsolete.go","/tmp/workspace/cmd/old.go"],"reason":"cleanup generated files"}`,
			InvalidArgs: []message.InvalidToolArg{{
				Path:   "args.paths",
				Reason: message.InvalidToolArgReasonMissing,
			}},
			IgnoredArgs: []message.IgnoredToolArg{{
				Path:      "args..paths",
				ValueJSON: `["/tmp/workspace/internal/tui/obsolete.go","/tmp/workspace/cmd/old.go"]`,
				Reason:    message.IgnoredToolArgReasonUnrecognized,
			}},
		},
	}
	rendered := strings.Join(block.Render(120, ""), "\n")
	joined := terminaltext.StripANSI(rendered)
	if !strings.Contains(joined, "delete cleanup generated files (ignored .paths=internal/tui/obsolete.go,cmd/old.go, paths=<missing>)") {
		t.Fatalf("expected delete diagnostic header to keep the ignored paths beside the reason; got:\n%s", joined)
	}
	if !strings.Contains(rendered, ";9m") {
		t.Fatalf("ignored delete paths are not struck through: %q", rendered)
	}
	if strings.Contains(joined, `["/tmp/workspace/internal/tui/obsolete.go","/tmp/workspace/cmd/old.go"]`) {
		t.Fatalf("did not expect delete diagnostic header to keep JSON array formatting; got:\n%s", joined)
	}
}

func TestDeleteAlwaysShowsAllPartialResultsAndCannotCollapse(t *testing.T) {
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      tools.NameDelete,
		Content:       `{"paths":["a.go","b.go","c.go"],"reason":"remove obsolete generated files"}`,
		ResultContent: "Delete stopped after an execution error.\n\nDeleted (1):\n- a.go\n\nFailed (1):\n- b.go — permission denied\n\nNot attempted (1):\n- c.go",
		ResultDone:    true,
		ResultStatus:  message.ToolStatusError,
	}

	before := terminaltext.StripANSI(strings.Join(block.Render(120, ""), "\n"))
	for _, want := range []string{
		"delete 3 files (remove obsolete generated files)",
		"Stopped after an execution error",
		"Deleted a.go",
		"Failed b.go — permission denied",
		"Not attempted c.go",
	} {
		if !strings.Contains(before, want) {
			t.Fatalf("expected Delete card to contain %q; got:\n%s", want, before)
		}
	}
	if strings.Contains(before, "[space]") || strings.Contains(before, "more lines") {
		t.Fatalf("Delete result paths must never be folded behind a hint; got:\n%s", before)
	}

	block.ToggleAtWidth(120)
	after := terminaltext.StripANSI(strings.Join(block.Render(120, ""), "\n"))
	if after != before {
		t.Fatalf("Delete card should ignore collapse toggles\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestEditAndApplyPatchShowDiagnosticsDetail(t *testing.T) {
	tests := []struct {
		name          string
		toolName      string
		resultContent string
		wantDetail    string
	}{
		{
			name:          "edit with LSP diagnostics",
			toolName:      "edit",
			resultContent: "Successfully edited internal/tui/example.go\n\nDiagnostics:\n[E] 3:4 [E1] undefined name\n[W] 5:1 [W1] unused variable",
			wantDetail:    "undefined name",
		},
		{
			name:          "edit diagnostics block without severity lines",
			toolName:      "edit",
			resultContent: "Successfully edited internal/tui/example.go\n\nDiagnostics:\nPython diagnostics skipped: no configured checker available.",
			wantDetail:    "Python diagnostics skipped",
		},
		{
			name:          "apply_patch with LSP diagnostics",
			toolName:      "apply_patch",
			resultContent: "Applied patch:\ninternal/tui/example.go: +2 -1 lines\n\nDiagnostics:\n[E] 3:4 [E1] undefined name",
			wantDetail:    "undefined name",
		},
		{
			name:          "edit without diagnostics",
			toolName:      "edit",
			resultContent: "Successfully edited internal/tui/example.go",
			wantDetail:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			block := &Block{
				ID:            1,
				Type:          BlockToolCall,
				ToolName:      tt.toolName,
				Content:       `{"path":"internal/tui/example.go","old_string":"a","new_string":"b"}`,
				Diff:          "--- internal/tui/example.go\n+++ internal/tui/example.go\n@@ -1,2 +1,2 @@\n-a\n+b",
				ResultContent: tt.resultContent,
				ResultDone:    true,
			}

			joined := terminaltext.StripANSI(strings.Join(block.Render(120, ""), "\n"))
			if tt.wantDetail == "" {
				if strings.Contains(strings.ToLower(joined), "diagnostics") {
					t.Fatalf("expected no diagnostics section for %s, got:\n%s", tt.toolName, joined)
				}
				return
			}
			if !strings.Contains(joined, "↳ Diagnostics:") {
				t.Fatalf("expected a diagnostics section for %s, got:\n%s", tt.toolName, joined)
			}
			if !strings.Contains(joined, tt.wantDetail) {
				t.Fatalf("expected diagnostics detail %q for %s, got:\n%s", tt.wantDetail, tt.toolName, joined)
			}
		})
	}
}

func TestCancelledWriteCallSuppressesEmptyFilePreviewAndDuplicateCancelledText(t *testing.T) {
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      "write",
		Content:       `{"path":"foo.txt","content":""}`,
		ResultContent: "Cancelled",
		ResultStatus:  message.ToolStatusCancelled,
		ResultDone:    true,
	}

	plain := terminaltext.StripANSI(strings.Join(block.Render(80, ""), "\n"))
	if !strings.Contains(plain, "write foo.txt") {
		t.Fatalf("expected Write header to include file path, got:\n%s", plain)
	}
	if !strings.Contains(plain, "↳ Cancelled") {
		t.Fatalf("expected cancelled summary line, got:\n%s", plain)
	}
	if strings.Contains(plain, "(empty file)") {
		t.Fatalf("expected cancelled Write card to suppress empty file preview, got:\n%s", plain)
	}
	if strings.Contains(plain, "↳ Cancelled:") {
		t.Fatalf("expected cancelled Write card to omit duplicate cancelled detail header, got:\n%s", plain)
	}
	if strings.Count(plain, "Cancelled") != 1 {
		t.Fatalf("expected exactly one visible Cancelled marker, got:\n%s", plain)
	}
}

func TestEditErrorPreservesExampleBlockIndentation(t *testing.T) {
	block := &Block{
		ID:       1,
		Type:     BlockToolCall,
		ToolName: tools.NameEdit,
		Content:  `{"patch":"*** Begin Patch\n*** Update File: internal/tui/sidebar_render.go\n@@\n-for _, fe := range files {\n-baseName := filepath.Base(fe.Path)\n-var parts string\n*** End Patch\n"}`,
		ResultContent: strings.Join([]string{
			"hunk not found. Indentation mismatch? A unique match exists if leading whitespace is ignored. Example block:",
			"\t\t\tfor _, fe := range files {",
			"\t\t\t\tbaseName := filepath.Base(fe.Path)",
			"\t\t\t\tvar parts string",
		}, "\n"),
		ResultStatus: message.ToolStatusError,
		ResultDone:   true,
	}

	plain := terminaltext.StripANSI(strings.Join(block.Render(120, ""), "\n"))
	if !strings.Contains(plain, "            for _, fe := range files {") {
		t.Fatalf("expected first example line indentation to be preserved; got:\n%s", plain)
	}
	if !strings.Contains(plain, "                baseName := filepath.Base(fe.Path)") {
		t.Fatalf("expected nested example line indentation to be preserved; got:\n%s", plain)
	}
	if strings.Contains(plain, "    for _, fe := range files {") && !strings.Contains(plain, "            for _, fe := range files {") {
		t.Fatalf("example block indentation appears trimmed; got:\n%s", plain)
	}
}

func TestEditToolCallCopyIncludesPathAndPatchWhenDiffMissing(t *testing.T) {
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      tools.NameEdit,
		Content:       `{"path":"foo.txt","patch":"@@\n-old\n+new\n"}`,
		ResultContent: "hunk not found. No files were modified",
		ResultStatus:  message.ToolStatusError,
		ResultDone:    true,
	}

	got := toolCallMarkdownContent(block)
	for _, want := range []string{
		"# Tool call: edit",
		"foo.txt",
		"## Result\n\nhunk not found. No files were modified",
		"## Diff\n\n```diff\n@@\n-old\n+new\n```",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("copied edit block missing %q; got:\n%s", want, got)
		}
	}
}

func TestApplyPatchToolCallCopyPreservesToolName(t *testing.T) {
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      tools.NameApplyPatch,
		Content:       `{"path":"foo.txt"}`,
		RawArgs:       `{"path":"foo.txt","patch":"@@\n-old\n+new\n"}`,
		ResultContent: "Applied patch to foo.txt (+1 -1)",
		ResultStatus:  message.ToolStatusSuccess,
		ResultDone:    true,
	}

	got := toolCallMarkdownContent(block)
	if !strings.Contains(got, "# Tool call: apply_patch") {
		t.Fatalf("copied ApplyPatch block should preserve its tool name; got:\n%s", got)
	}
	if strings.Contains(got, "# Tool call: edit") {
		t.Fatalf("copied Patch block should not be labeled as Edit; got:\n%s", got)
	}
}

func TestApplyPatchToolCallCopyOmitsPathAndPlacesDiffBeforeResult(t *testing.T) {
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      tools.NameApplyPatch,
		Content:       `{"path":"foo.txt"}`,
		RawArgs:       `{"path":"foo.txt","patch":"@@\n-old\n+new\n"}`,
		Diff:          "@@ -1 +1 @@\n-old\n+new",
		ResultContent: "Applied patch",
		ResultStatus:  message.ToolStatusSuccess,
		ResultDone:    true,
	}

	got := toolCallMarkdownContent(block)
	if strings.Contains(got, "## Path") {
		t.Fatalf("ApplyPatch copy should omit derived path section; got:\n%s", got)
	}
	diffIndex := strings.Index(got, "## Diff")
	resultIndex := strings.Index(got, "## Result")
	if diffIndex < 0 || resultIndex < 0 || diffIndex > resultIndex {
		t.Fatalf("ApplyPatch copy should place Diff before Result; got:\n%s", got)
	}
}

func TestReplaceEditToolCallErrorCopyShowsOldAndNewStrings(t *testing.T) {
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      tools.NameEdit,
		Content:       `{"path":"foo.txt"}`,
		RawArgs:       `{"path":"foo.txt","old_string":"old line\n","new_string":"new line\n","replace_all":true}`,
		ResultContent: "old_string not found in file",
		ResultStatus:  message.ToolStatusError,
		ResultDone:    true,
	}

	got := toolCallMarkdownContent(block)
	for _, want := range []string{
		"# Tool call: edit",
		"## Path\n\nfoo.txt",
		"## old_string\n\n```\nold line\n```",
		"## new_string\n\n```\nnew line\n```",
		"## replace_all\n\ntrue",
		"## Result\n\nold_string not found in file",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("copied replace edit block missing %q; got:\n%s", want, got)
		}
	}
	if strings.Contains(got, "## Arguments") || strings.Contains(got, "## Diff") {
		t.Fatalf("copied replace edit block should use old_string/new_string sections instead of JSON Arguments or diff; got:\n%s", got)
	}
}

func TestReplaceEditToolCallCopyOmitsFalseReplaceAll(t *testing.T) {
	block := &Block{
		ID:       1,
		Type:     BlockToolCall,
		ToolName: tools.NameEdit,
		RawArgs:  `{"path":"foo.txt","old_string":"old","new_string":"new","replace_all":false}`,
	}

	got := toolCallMarkdownContent(block)
	if strings.Contains(got, "## replace_all") {
		t.Fatalf("copied edit block should omit false replace_all; got:\n%s", got)
	}
}

// An edit that touches Markdown carries backticks in its payload. The fence has
// to outgrow them, or the pasted section closes inside the code and the rest of
// the card renders as prose.
func TestEditToolCallCopyFencesPayloadContainingCodeFence(t *testing.T) {
	block := &Block{
		ID:       1,
		Type:     BlockToolCall,
		ToolName: tools.NameEdit,
		RawArgs:  `{"path":"README.md","old_string":"` + "```" + `go\ncode\n` + "```" + `","new_string":"  indented"}`,
	}

	got := toolCallMarkdownContent(block)
	if !strings.Contains(got, "## old_string\n\n````\n```go\ncode\n```\n````") {
		t.Fatalf("copied edit block did not grow the fence past the payload; got:\n%s", got)
	}
	if !strings.Contains(got, "## new_string\n\n```\n  indented\n```") {
		t.Fatalf("copied edit block did not fence the indented payload; got:\n%s", got)
	}
}

func TestEditToolCallCopyShowsOldAndNewStringsInsteadOfDiff(t *testing.T) {
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      tools.NameEdit,
		Content:       `{"path":"src/demo.go","old_string":"old\n","new_string":"new\n"}`,
		Diff:          "@@ -1 +1 @@\n-old\n+new",
		ResultContent: "Diagnostics:\nnone",
		ResultStatus:  message.ToolStatusSuccess,
		ResultDone:    true,
	}

	got := toolCallMarkdownContent(block)
	for _, want := range []string{
		"# Tool call: edit",
		"## Path\n\nsrc/demo.go",
		"## old_string\n\n```\nold\n```",
		"## new_string\n\n```\nnew\n```",
		"## Result\n\nDiagnostics:\nnone",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("copied edit block missing %q; got:\n%s", want, got)
		}
	}
	if strings.Contains(got, "## Diff") {
		t.Fatalf("copied edit block should show old_string/new_string instead of a unified diff; got:\n%s", got)
	}
}

func TestReplaceEditToolCallErrorRenderShowsOldAndNewLines(t *testing.T) {
	ApplyTheme(uitheme.Default())
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      tools.NameEdit,
		Content:       `{"path":"foo.txt"}`,
		RawArgs:       `{"path":"foo.txt","old_string":"func old() {\n\treturn 1\n}\n","new_string":"func current() {\n\treturn 2\n}\n","replace_all":true}`,
		ResultContent: "old_string not found in file",
		ResultStatus:  message.ToolStatusError,
		ResultDone:    true,
	}

	lines := block.Render(100, "")
	plain := terminaltext.StripANSI(strings.Join(lines, "\n"))
	for _, want := range []string{
		"edit foo.txt (replace_all=true)",
		"- func old() {",
		"+ func current() {",
		"-     return 1",
		"+     return 2",
		"↳ Error:",
		"old_string not found in file",
	} {
		if !strings.Contains(plain, want) {
			t.Fatalf("rendered replace edit block missing %q; got:\n%s", want, plain)
		}
	}
	for _, unwanted := range []string{`"old_string"`, `"new_string"`, `\n`, "Requested replacement:", "@@ ", "--- foo.txt", "+++ foo.txt", "replace_all: true"} {
		if strings.Contains(plain, unwanted) {
			t.Fatalf("rendered replace edit block should not show %q; got:\n%s", unwanted, plain)
		}
	}
	assertRenderedTextBackground(t, renderedLineContaining(t, lines, "func old"), "func", colorOfTheme(currentStyles.Palette.DiffDelLineBg))
	assertRenderedTextBackground(t, renderedLineContaining(t, lines, "func current"), "func", colorOfTheme(currentStyles.Palette.DiffAddLineBg))
}

func TestReplaceEditToolCallErrorRenderTruncatesLongLines(t *testing.T) {
	oldText := "old-prefix-" + strings.Repeat("a", 100) + "-old-suffix"
	newText := "new-prefix-" + strings.Repeat("b", 100) + "-new-suffix"
	args, err := json.Marshal(map[string]any{
		"path":       "foo.txt",
		"old_string": oldText,
		"new_string": newText,
	})
	if err != nil {
		t.Fatalf("marshal edit args: %v", err)
	}
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      tools.NameEdit,
		RawArgs:       string(args),
		ResultContent: "old_string not found in file",
		ResultStatus:  message.ToolStatusError,
		ResultDone:    true,
	}

	const width = 48
	lines := block.Render(width, "")
	plain := terminaltext.StripANSI(strings.Join(lines, "\n"))
	for _, want := range []string{"- old-prefix-", "+ new-prefix-", "…"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("rendered long replace edit line missing %q; got:\n%s", want, plain)
		}
	}
	for _, hidden := range []string{"old-suffix", "new-suffix"} {
		if strings.Contains(plain, hidden) {
			t.Fatalf("rendered long replace edit line should truncate %q; got:\n%s", hidden, plain)
		}
	}
	metrics := newToolCardMetrics(width)
	style := metrics.blockStyle
	maxLineWidth := style.GetMarginLeft() + style.GetPaddingLeft() + metrics.cardWidth + style.GetPaddingRight() + style.GetMarginRight()
	if cardRailPrefix("tool", false) != "" {
		maxLineWidth++
	}
	for i, line := range lines {
		if got := terminaltext.Width(line); got > maxLineWidth {
			t.Fatalf("rendered line %d width = %d, want <= %d: %q", i, got, maxLineWidth, line)
		}
	}
}

func TestReplaceEditToolCallHeaderOmitsDisabledReplaceAll(t *testing.T) {
	for _, rawArgs := range []string{
		`{"path":"foo.txt","old_string":"old","new_string":"new"}`,
		`{"path":"foo.txt","old_string":"old","new_string":"new","replace_all":false}`,
	} {
		block := &Block{
			ID:            1,
			Type:          BlockToolCall,
			ToolName:      tools.NameEdit,
			RawArgs:       rawArgs,
			ResultContent: "old_string not found in file",
			ResultStatus:  message.ToolStatusError,
			ResultDone:    true,
		}

		plain := terminaltext.StripANSI(strings.Join(block.Render(100, ""), "\n"))
		if strings.Contains(plain, "replace_all") {
			t.Fatalf("disabled replace_all should be omitted from Edit card; got:\n%s", plain)
		}
	}
}

func TestEditToolCallCopyUsesRawArgsWhenContentTrimmed(t *testing.T) {
	// On a failed edit, Content is display-trimmed (patch stripped) and the full
	// patch lives only in RawArgs, mirroring the card render path.
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      tools.NameEdit,
		Content:       `{"path":"foo.txt"}`,
		RawArgs:       `{"path":"foo.txt","patch":"@@\n-old\n+new\n"}`,
		ResultContent: "hunk not found. No files were modified",
		ResultStatus:  message.ToolStatusError,
		ResultDone:    true,
	}

	got := toolCallMarkdownContent(block)
	if !strings.Contains(got, "## Diff\n\n```diff\n@@\n-old\n+new\n```") {
		t.Fatalf("copied edit block should include patch from RawArgs; got:\n%s", got)
	}
}

func TestCancelledEditCallSuppressesDiffPreviewAndDuplicateCancelledText(t *testing.T) {
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      tools.NameEdit,
		Content:       `{"path":"foo.txt","patch":"@@\n-a\n+b\n"}`,
		Diff:          "@@ -1,1 +1,1 @@\n-a\n+b\n",
		ResultContent: "Cancelled",
		ResultStatus:  message.ToolStatusCancelled,
		ResultDone:    true,
	}

	plain := terminaltext.StripANSI(strings.Join(block.Render(80, ""), "\n"))
	if !strings.Contains(plain, "edit") || !strings.Contains(plain, "foo.txt") {
		t.Fatalf("expected edit header to include file path, got:\n%s", plain)
	}
	if !strings.Contains(plain, "↳ Cancelled") {
		t.Fatalf("expected cancelled summary line, got:\n%s", plain)
	}
	if strings.Contains(plain, "-a") || strings.Contains(plain, "+b") {
		t.Fatalf("expected cancelled Edit card to suppress diff preview, got:\n%s", plain)
	}
	if strings.Contains(plain, "↳ Cancelled:") {
		t.Fatalf("expected cancelled Edit card to omit duplicate cancelled detail header, got:\n%s", plain)
	}
	if strings.Count(plain, "Cancelled") != 1 {
		t.Fatalf("expected exactly one visible Cancelled marker, got:\n%s", plain)
	}
}

func TestWriteCallSuccessHeaderOmitsContent(t *testing.T) {
	block := &Block{
		ID:       3,
		Type:     BlockToolCall,
		ToolName: "write",
		Content:  `{"path":"docs/notes.md","content":"hello world"}`,
	}

	lines := block.renderWriteCall(120, "")
	header := ""
	for _, line := range lines {
		if plain := terminaltext.StripANSI(line); strings.Contains(plain, "docs/notes.md") {
			header = plain
			break
		}
	}
	if header == "" {
		t.Fatalf("expected Write header to keep the path, got:\n%s", terminaltext.StripANSI(strings.Join(lines, "\n")))
	}
	if strings.Contains(header, "content=") {
		t.Fatalf("valid Write args should not repeat the content in the header, got:\n%s", header)
	}
	if plain := terminaltext.StripANSI(strings.Join(lines, "\n")); !strings.Contains(plain, "hello world") {
		t.Fatalf("expected the content preview to render the written text, got:\n%s", plain)
	}
}
