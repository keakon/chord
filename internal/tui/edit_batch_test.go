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
	preview := stripANSI(strings.Join(appendReplaceEditPreview(nil, args, "sample.go", 100), "\n"))
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
