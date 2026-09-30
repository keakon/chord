package tui

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/tools"
)

func TestEditBatchPreviewAndCopyIncludeEveryReplacement(t *testing.T) {
	const raw = `{"path":"sample.go","edits":[{"old_string":"firstOld","new_string":"firstNew"},{"old_string":"secondOld","new_string":"secondNew","replace_all":true}]}`
	args, ok := parseReplaceEditArgs(raw)
	if !ok {
		t.Fatal("batch arguments not recognized")
	}
	preview := stripANSI(strings.Join(appendReplaceEditPreview(nil, args, "sample.go", 100, nil), "\n"))
	copyText := fileDiffToolCallMarkdownContent(&Block{ToolName: tools.NameEdit, Content: raw})
	for _, text := range []string{"firstOld", "firstNew", "secondOld", "secondNew"} {
		if !strings.Contains(preview, text) || !strings.Contains(copyText, text) {
			t.Fatalf("replacement %q missing from preview or copy", text)
		}
	}
	if !strings.Contains(copyText, "replace_all") {
		t.Fatal("copy lost replace_all")
	}
	// Each entry sits under its own numbered section, in order, so adjacent
	// replacements cannot read as one; the entry that replaces every match
	// says so.
	first := strings.Index(preview, "Edit 1")
	second := strings.Index(preview, "Edit 2 (replace_all=true)")
	if first < 0 || second < 0 {
		t.Fatalf("preview lacks numbered entry sections:\n%s", preview)
	}
	if !(first < strings.Index(preview, "firstOld") && strings.Index(preview, "firstNew") < second && second < strings.Index(preview, "secondOld")) {
		t.Fatalf("entry sections do not separate the replacements:\n%s", preview)
	}
	if strings.Contains(preview, "Edit 1 (replace_all") {
		t.Fatalf("entry without replace_all is labelled as replace_all:\n%s", preview)
	}
}

func TestEditBatchHeaderNamesReplaceAllEntries(t *testing.T) {
	ApplyTheme(DefaultTheme())
	for _, tc := range []struct {
		raw, want string
	}{
		{`{"path":"foo.txt","edits":[{"old_string":"a","new_string":"b"},{"old_string":"c","new_string":"d","replace_all":true}]}`, "edit foo.txt (replace_all=edit 2)"},
		{`{"path":"foo.txt","edits":[{"old_string":"a","new_string":"b","replace_all":true},{"old_string":"c","new_string":"d","replace_all":true}]}`, "edit foo.txt (replace_all=edits 1,2)"},
	} {
		block := &Block{
			ID:            1,
			Type:          BlockToolCall,
			ToolName:      tools.NameEdit,
			Content:       `{"path":"foo.txt"}`,
			RawArgs:       tc.raw,
			ResultContent: "old_string not found in file",
			ResultStatus:  agent.ToolResultStatusError,
			ResultDone:    true,
		}
		plain := stripANSI(strings.Join(block.Render(100, ""), "\n"))
		if !strings.Contains(plain, tc.want) {
			t.Fatalf("header missing %q; got:\n%s", tc.want, plain)
		}
	}
	plain := stripANSI(strings.Join((&Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      tools.NameEdit,
		Content:       `{"path":"foo.txt"}`,
		RawArgs:       `{"path":"foo.txt","edits":[{"old_string":"a","new_string":"b"}]}`,
		ResultContent: "old_string not found in file",
		ResultStatus:  agent.ToolResultStatusError,
		ResultDone:    true,
	}).Render(100, ""), "\n"))
	if strings.Contains(plain, "replace_all") {
		t.Fatalf("batch without replace_all shows the option; got:\n%s", plain)
	}
}

// A failed batch card states that nothing was written and marks only the
// entries the report blames: 0-based "edits[i]" indexes map to the 1-based
// "Edit N" sections, matched-only entries stay unmarked, and no entry reads as
// applied.
func TestEditBatchFailureMarksOnlyFailedEntries(t *testing.T) {
	ApplyTheme(DefaultTheme())
	const raw = `{"path":"sample.go","edits":[{"old_string":"firstOld","new_string":"firstNew"},{"old_string":"failedOld","new_string":"failedNew"},{"old_string":"thirdOld","new_string":"thirdNew"}]}`
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      tools.NameEdit,
		Content:       `{"path":"sample.go"}`,
		RawArgs:       raw,
		ResultContent: "No changes were written (batch edits are atomic).\nedits[1]: old_string not found in the original file\nRemaining entries (edits[0,2]) passed per-entry matching but were not applied.\nFix the failing entries above and resubmit the complete batch.",
		ResultStatus:  agent.ToolResultStatusError,
		ResultDone:    true,
	}
	plain := stripANSI(strings.Join(block.Render(100, ""), "\n"))
	if !strings.Contains(plain, "Not applied: no changes were written") {
		t.Fatalf("card lacks the not-applied status:\n%s", plain)
	}
	if !strings.Contains(plain, "✗ Edit 2") {
		t.Fatalf("failing entry lacks the ✗ mark:\n%s", plain)
	}
	for _, unmarked := range []string{"✗ Edit 1", "✗ Edit 3"} {
		if strings.Contains(plain, unmarked) {
			t.Fatalf("matched-only entry marked %q:\n%s", unmarked, plain)
		}
	}
	if strings.Contains(plain, "✓") {
		t.Fatalf("batch failure card shows an applied mark:\n%s", plain)
	}
}

// An error result that is not a batch failure report carries no ✗/not-applied
// decoration: cards restored from other error shapes keep the plain preview.
func TestEditBatchUnstructuredErrorKeepsPlainPreview(t *testing.T) {
	ApplyTheme(DefaultTheme())
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      tools.NameEdit,
		Content:       `{"path":"sample.go"}`,
		RawArgs:       `{"path":"sample.go","edits":[{"old_string":"a","new_string":"b"},{"old_string":"c","new_string":"d"}]}`,
		ResultContent: "old_string not found in file, even after punctuation/whitespace tolerance.",
		ResultStatus:  agent.ToolResultStatusError,
		ResultDone:    true,
	}
	plain := stripANSI(strings.Join(block.Render(100, ""), "\n"))
	if strings.Contains(plain, "Not applied") || strings.Contains(plain, "✗ Edit") {
		t.Fatalf("unstructured error gained failure decoration:\n%s", plain)
	}
	for _, want := range []string{"Edit 1", "Edit 2"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("preview lacks %q:\n%s", want, plain)
		}
	}
}
