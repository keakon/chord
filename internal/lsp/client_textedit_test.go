package lsp

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/charmbracelet/x/powernap/pkg/lsp/protocol"
)

func TestSplitLinesAndUTF16Offsets(t *testing.T) {
	lines := splitLines("a\r\nb😀c\nlast")
	if len(lines) != 3 || lines[0] != "a\r" || lines[1] != "b😀c" || lines[2] != "last" {
		t.Fatalf("splitLines = %#v", lines)
	}
	if got := utf16CharToByteOffset("b😀c", 2); got != len("b😀") {
		t.Fatalf("utf16 offset inside surrogate = %d, want %d", got, len("b😀"))
	}
	if got := utf16CharToByteOffset("b😀c", 3); got != len("b😀") {
		t.Fatalf("utf16 offset after surrogate = %d, want %d", got, len("b😀"))
	}
	if got := lineCharToByte(lines, 1, 3); got != len("a\r\n")+len("b😀") {
		t.Fatalf("lineCharToByte = %d, want %d", got, len("a\r\n")+len("b😀"))
	}
}

func TestApplyTextEdit(t *testing.T) {
	content := "hello\nworld\n"
	got := applyTextEdit(content, protocol.TextEdit{
		Range: protocol.Range{
			Start: protocol.Position{Line: 0, Character: 1},
			End:   protocol.Position{Line: 1, Character: 3},
		},
		NewText: "i there\nw",
	})
	if got != "hi there\nwld\n" {
		t.Fatalf("applyTextEdit = %q", got)
	}
	appended := applyTextEdit("one", protocol.TextEdit{
		Range:   protocol.Range{Start: protocol.Position{Line: 10, Character: 0}, End: protocol.Position{Line: 10, Character: 0}},
		NewText: " two",
	})
	if appended != "one two" {
		t.Fatalf("append edit = %q", appended)
	}
}

func TestApplyWorkspaceEditSortsReverse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.go")
	if err := os.WriteFile(path, []byte("abcdef"), 0644); err != nil {
		t.Fatal(err)
	}
	uri := protocol.DocumentURI("file://" + path)
	err := applyWorkspaceEdit(protocol.WorkspaceEdit{Changes: map[protocol.DocumentURI][]protocol.TextEdit{
		uri: {
			{Range: protocol.Range{Start: protocol.Position{Line: 0, Character: 1}, End: protocol.Position{Line: 0, Character: 2}}, NewText: "B"},
			{Range: protocol.Range{Start: protocol.Position{Line: 0, Character: 4}, End: protocol.Position{Line: 0, Character: 5}}, NewText: "E"},
		},
	}})
	if err != nil {
		t.Fatalf("applyWorkspaceEdit: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "aBcdEf" {
		t.Fatalf("edited content = %q", data)
	}
}

func TestLocationConversions(t *testing.T) {
	uri := protocol.DocumentURI("file:///tmp/example.go")
	loc := protocol.Location{URI: uri, Range: protocol.Range{Start: protocol.Position{Line: 3, Character: 7}}}
	got := locationToRefLocation(loc)
	if got.Path != "/tmp/example.go" || got.Line != 3 || got.Col != 7 {
		t.Fatalf("locationToRefLocation = %+v", got)
	}
	links := definitionLinksToRefLocations([]protocol.DefinitionLink{{
		TargetURI:            uri,
		TargetSelectionRange: protocol.Range{Start: protocol.Position{Line: 4, Character: 2}},
	}})
	if len(links) != 1 || links[0].Line != 4 || links[0].Col != 2 {
		t.Fatalf("definitionLinksToRefLocations = %+v", links)
	}
}
