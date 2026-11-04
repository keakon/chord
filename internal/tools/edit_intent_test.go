package tools

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestEditIntentScannerFragments(t *testing.T) {
	tests := []struct {
		name, tool, input string
		want              []ApplyPatchDisplayTarget
	}{
		{"write", NameWrite, `{"path":"src/sample.go","content":"unfinished`, []ApplyPatchDisplayTarget{{Kind: MutationUpdate, SourcePath: "src/sample.go"}}},
		{"edit alias", NameEdit, `{"filePath":"src/sample.go","edits":[`, []ApplyPatchDisplayTarget{{Kind: MutationUpdate, SourcePath: "src/sample.go"}}},
		{"nested path ignored", NameEdit, `{"edits":[{"path":"wrong.go","new_string":"\"path\":\"wrong.go\""}],"path":"right.go"`, []ApplyPatchDisplayTarget{{Kind: MutationUpdate, SourcePath: "right.go"}}},
		{"escaped path", NameWrite, `{"path":"src/quo\"te\\file.go"`, []ApplyPatchDisplayTarget{{Kind: MutationUpdate, SourcePath: "src/quo\"te\\file.go"}}},
		{"unicode path", NameWrite, `{"path":"src/\u6d4b\u8bd5\ud83d\ude00.go"`, []ApplyPatchDisplayTarget{{Kind: MutationUpdate, SourcePath: "src/测试😀.go"}}},
		{"incomplete", NameWrite, `{"path":"sample.go`, nil},
		{"invalid escape", NameWrite, `{"path":"sample\q.go"`, nil},
		{"body only", NameWrite, `{"content":"{\"path\":\"wrong.go\"}"}`, nil},
		{"delete", NameApplyPatch, "*** Begin Patch\n*** Delete File: sample.go\n*** End Patch\n", nil},
		{"not patch", NameApplyPatch, "*** Add File: sample.go\n", nil},
		{"move", NameApplyPatch, "*** Begin Patch\n*** Update File: from.go\n*** Move to: to.go\n", []ApplyPatchDisplayTarget{{Kind: MutationMove, SourcePath: "from.go", TargetPath: "to.go"}}},
		{"indented move", NameApplyPatch, "*** Begin Patch\n*** Update File: from.go\n  *** Move to: to.go\n", []ApplyPatchDisplayTarget{{Kind: MutationMove, SourcePath: "from.go", TargetPath: "to.go"}}},
		{"update", NameApplyPatch, "*** Begin Patch\n*** Update File: sample.go\n@@\n", []ApplyPatchDisplayTarget{{Kind: MutationUpdate, SourcePath: "sample.go"}}},
		{"add", NameApplyPatch, "*** Begin Patch\r\n*** Add File: sample.go\r\n", []ApplyPatchDisplayTarget{{Kind: MutationAdd, SourcePath: "sample.go"}}},
		{"json patch", NameApplyPatch, `{"patch":"*** Begin Patch\n*** Add File: sample.go\n+`, []ApplyPatchDisplayTarget{{Kind: MutationAdd, SourcePath: "sample.go"}}},
		{"unicode newline", NameApplyPatch, `{"patch":"*** Begin Patch\u000a*** Add File: sample.go\u000A+`, []ApplyPatchDisplayTarget{{Kind: MutationAdd, SourcePath: "sample.go"}}},
		{"nested patch ignored", NameApplyPatch, `{"other":{"patch":"*** Begin Patch\n*** Add File: wrong.go\n"}}`, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for split := 0; split <= len(tt.input); split++ {
				scanner := NewEditIntentScanner(tt.tool)
				var got []ApplyPatchDisplayTarget
				emit := func(target ApplyPatchDisplayTarget) { got = append(got, target) }
				scanner.Feed(tt.input[:split], emit)
				scanner.Feed(tt.input[split:], emit)
				if !reflect.DeepEqual(got, tt.want) {
					t.Fatalf("split=%d got=%+v want=%+v", split, got, tt.want)
				}
			}
		})
	}
}

func TestEditIntentScannerLargeBody(t *testing.T) {
	patch := "*** Begin Patch\n*** Add File: first.go\n+" + strings.Repeat("x", 1<<20) + "\n*** Add File: second.go\n"
	encoded, err := json.Marshal(ApplyPatchArgs{Patch: patch})
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{patch, string(encoded), strings.ReplaceAll(string(encoded), `\n`, `\u000a`)} {
		scanner := NewEditIntentScanner(NameApplyPatch)
		var paths []string
		for i := range len(input) {
			scanner.Feed(input[i:i+1], func(target ApplyPatchDisplayTarget) { paths = append(paths, target.SourcePath) })
		}
		if !reflect.DeepEqual(paths, []string{"first.go", "second.go"}) {
			t.Fatalf("paths = %v", paths)
		}
		if len(scanner.token) > editIntentTokenLimit || len(scanner.line) > editIntentTokenLimit {
			t.Fatal("scanner retained an unbounded body")
		}
	}
}
