package agent

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func readValidityMessages() ([]message.Message, map[string]toolCallMeta) {
	readBody := func(start, end, total int, line string) string {
		var b strings.Builder
		b.WriteString(tools.FormatReadResultHeader(
			strconv.Itoa(start)+"-"+strconv.Itoa(end), total, "", "", ""))
		for i := start; i <= end; i++ {
			b.WriteString("\n")
			b.WriteString(line)
		}
		return b.String()
	}
	msgs := []message.Message{
		{Role: message.RoleUser, Content: "u1"},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "r1", Name: tools.NameRead, Args: json.RawMessage(`{"path":"a.go","offset":9,"limit":11}`)}}},
		{Role: message.RoleTool, ToolCallID: "r1", Content: readBody(10, 20, 100, "alpha")},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "r2", Name: tools.NameRead, Args: json.RawMessage(`{"path":"b.go"}`)}}},
		{Role: message.RoleTool, ToolCallID: "r2", Content: readBody(1, 50, 50, "beta")},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "p1", Name: tools.NameApplyPatch, Args: json.RawMessage(`{"path":"b.go","patch":"@@\n-beta\n+gamma"}`)}}},
		{Role: message.RoleTool, ToolCallID: "p1", Content: "Applied patch to b.go (+1 -1)", ToolStatus: "success"},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "r3", Name: tools.NameRead, Args: json.RawMessage(`{"path":"a.go","offset":0,"limit":100}`)}}},
		{Role: message.RoleTool, ToolCallID: "r3", Content: readBody(1, 100, 100, "alpha")},
	}
	return msgs, buildToolCallMeta(msgs)
}

func TestAnalyzeReadValidity(t *testing.T) {
	msgs, callMeta := readValidityMessages()
	validity := analyzeReadValidity(msgs, callMeta)

	// r1 (a.go lines 10-20) is covered by the later r3 (a.go lines 1-100).
	if got := validity[2]; !got.Superseded || got.Invalidated {
		t.Fatalf("r1 validity = %+v, want superseded only", got)
	}
	// r2 (b.go) is invalidated by the later successful patch to b.go.
	if got := validity[4]; !got.Invalidated || got.Superseded {
		t.Fatalf("r2 validity = %+v, want invalidated only", got)
	}
	// r3 is the newest read of a.go: neither invalidated nor superseded.
	if got, ok := validity[8]; ok {
		t.Fatalf("r3 validity = %+v, want untracked (still valid)", got)
	}
}

func TestAnalyzeReadValidityRequiresSingleCoveringRead(t *testing.T) {
	read := func(id string, start, end int) []message.Message {
		args := `{"path":"a.go","offset":` + strconv.Itoa(start-1) + `,"limit":` + strconv.Itoa(end-start+1) + `}`
		content := tools.FormatReadResultHeader(strconv.Itoa(start)+"-"+strconv.Itoa(end), 100, "", "", "") + "\nbody"
		return []message.Message{
			{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: id, Name: tools.NameRead, Args: json.RawMessage(args)}}},
			{Role: message.RoleTool, ToolCallID: id, Content: content},
		}
	}
	msgs := []message.Message{{Role: message.RoleUser, Content: "inspect"}}
	msgs = append(msgs, read("original", 10, 30)...)
	msgs = append(msgs, read("left", 1, 20)...)
	msgs = append(msgs, read("right", 20, 40)...)

	validity := analyzeReadValidity(msgs, buildToolCallMeta(msgs))
	if got := validity[2]; got.Superseded {
		t.Fatalf("two partial later reads must not jointly supersede the original, got %+v", got)
	}

	msgs = append(msgs, read("covering", 5, 35)...)
	validity = analyzeReadValidity(msgs, buildToolCallMeta(msgs))
	if got := validity[2]; !got.Superseded {
		t.Fatalf("one later covering read should supersede the original, got %+v", got)
	}
}

func TestAnalyzeReadValidityIgnoresFailedEdits(t *testing.T) {
	msgs, _ := readValidityMessages()
	msgs[6].ToolStatus = "error"
	validity := analyzeReadValidity(msgs, buildToolCallMeta(msgs))
	if got := validity[4]; got.Invalidated {
		t.Fatalf("failed patch should not invalidate r2, got %+v", got)
	}
}

func TestAnalyzeReadValidityIgnoresCancelledTools(t *testing.T) {
	msgs, _ := readValidityMessages()
	msgs[6].ToolStatus = string(ToolResultStatusCancelled)
	validity := analyzeReadValidity(msgs, buildToolCallMeta(msgs))
	if got := validity[4]; got.Invalidated {
		t.Fatalf("cancelled patch changed read validity: %+v", got)
	}
}

func TestAnalyzeReadValidityNormalizesLegacyPatchName(t *testing.T) {
	msgs, _ := readValidityMessages()
	msgs[5].ToolCalls[0].Name = "patch"
	validity := analyzeReadValidity(msgs, buildToolCallMeta(msgs))
	if got := validity[4]; !got.Invalidated {
		t.Fatalf("successful legacy patch should invalidate r2, got %+v", got)
	}
}

func TestAnalyzeReadValidityLocalizedEditOnlyInvalidatesOverlappingRead(t *testing.T) {
	read := func(id string, start, end int) []message.Message {
		content := tools.FormatReadResultHeader(strconv.Itoa(start)+"-"+strconv.Itoa(end), 100, "", "", "") + "\nbody"
		return []message.Message{
			{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: id, Name: tools.NameRead, Args: json.RawMessage(`{"path":"a.go"}`)}}},
			{Role: message.RoleTool, ToolCallID: id, Content: content},
		}
	}
	msgs := []message.Message{{Role: message.RoleUser, Content: "inspect"}}
	msgs = append(msgs, read("before", 1, 20)...)
	msgs = append(msgs, read("overlap", 40, 60)...)
	msgs = append(msgs,
		message.Message{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "edit", Name: tools.NameEdit, Args: json.RawMessage(`{"path":"a.go","old_string":"x","new_string":"y"}`)}}},
		message.Message{Role: message.RoleTool, ToolCallID: "edit", Content: "Replaced 1 occurrence", ToolStatus: "success", FileState: &message.ToolFileState{Writes: []message.TrackedFileState{{Path: "a.go", Exists: true, ChangedStart: 45, ChangedEnd: 47}}}},
	)

	validity := analyzeReadValidity(msgs, buildToolCallMeta(msgs))
	if got := validity[2]; got.Invalidated {
		t.Fatalf("non-overlapping read should remain current, got %+v", got)
	}
	if got := validity[4]; !got.Invalidated {
		t.Fatalf("overlapping read should be invalidated, got %+v", got)
	}
}

func TestAnalyzeReadValidityLineShiftBeforeReadInvalidatesLineNumbers(t *testing.T) {
	content := tools.FormatReadResultHeader("40-60", 100, "", "", "") + "\nbody"
	msgs := []message.Message{
		{Role: message.RoleUser, Content: "inspect"},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "read", Name: tools.NameRead, Args: json.RawMessage(`{"path":"a.go"}`)}}},
		{Role: message.RoleTool, ToolCallID: "read", Content: content},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "edit", Name: tools.NameEdit, Args: json.RawMessage(`{"path":"a.go","old_string":"x","new_string":"x\ny"}`)}}},
		{Role: message.RoleTool, ToolCallID: "edit", Content: "Replaced 1 occurrence", ToolStatus: "success", FileState: &message.ToolFileState{Writes: []message.TrackedFileState{{Path: "a.go", Exists: true, ChangedStart: 10, ChangedEnd: 10, LineDelta: 1}}}},
	}

	validity := analyzeReadValidity(msgs, buildToolCallMeta(msgs))
	if got := validity[2]; !got.Invalidated {
		t.Fatalf("line insertion before a read range must invalidate its displayed line numbers, got %+v", got)
	}
}

func TestAnalyzeReadValidityUnknownEditRangeInvalidatesWholeFile(t *testing.T) {
	msgs, _ := readValidityMessages()
	validity := analyzeReadValidity(msgs, buildToolCallMeta(msgs))
	if got := validity[4]; !got.Invalidated {
		t.Fatalf("legacy write metadata without a range must invalidate the whole file, got %+v", got)
	}
}

func TestChangedPreEditLineRange(t *testing.T) {
	tests := []struct {
		name   string
		before string
		after  string
		start  int
		end    int
		delta  int
	}{
		{name: "replace", before: "a\nb\nc\n", after: "a\nx\nc\n", start: 2, end: 2, delta: 0},
		{name: "delete", before: "a\nb\nc\n", after: "a\nc\n", start: 2, end: 2, delta: -1},
		{name: "insert", before: "a\nc\n", after: "a\nb\nc\n", start: 2, end: 2, delta: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start, end, delta := changedPreEditLineRange(tt.before, tt.after)
			if start != tt.start || end != tt.end || delta != tt.delta {
				t.Fatalf("range = %d-%d delta=%d, want %d-%d delta=%d", start, end, delta, tt.start, tt.end, tt.delta)
			}
		})
	}
}

func TestAnalyzeReadValidityMatchesPathSuffix(t *testing.T) {
	content := tools.FormatReadResultHeader("1-30", 30, "", "", "") + "\n" + strings.Repeat("body\n", 30)
	msgs := []message.Message{
		{Role: message.RoleUser, Content: "u1"},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "r1", Name: tools.NameRead, Args: json.RawMessage(`{"path":"/tmp/worktree/internal/agent/main.go"}`)}}},
		{Role: message.RoleTool, ToolCallID: "r1", Content: content},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "p1", Name: tools.NameApplyPatch, Args: json.RawMessage(`{"path":"internal/agent/main.go","patch":"@@\n-a\n+b"}`)}}},
		{Role: message.RoleTool, ToolCallID: "p1", Content: "Applied patch to internal/agent/main.go (+1 -1)", ToolStatus: "success"},
	}
	validity := analyzeReadValidity(msgs, buildToolCallMeta(msgs))
	if got := validity[2]; !got.Invalidated {
		t.Fatalf("relative-path edit should invalidate absolute-path read, got %+v", got)
	}
}

func TestAnalyzeReadValidityDoesNotSuffixMatchDistinctCanonicalPaths(t *testing.T) {
	content := tools.FormatReadResultHeader("1-30", 30, "", "", "") + "\n" + strings.Repeat("body\n", 30)
	readPath := "/tmp/worktree-a/internal/agent/main.go"
	editPath := "/tmp/worktree-b/internal/agent/main.go"
	msgs := []message.Message{
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "r1", Name: tools.NameRead, Args: json.RawMessage(`{"path":"` + readPath + `"}`)}}},
		{Role: message.RoleTool, ToolCallID: "r1", Content: content, FileState: &message.ToolFileState{Reads: []message.TrackedFileState{{Path: readPath, SHA256: "read", Exists: true}}}},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "p1", Name: tools.NameApplyPatch, Args: json.RawMessage(`{"path":"` + editPath + `","patch":"@@\n-a\n+b"}`)}}},
		{Role: message.RoleTool, ToolCallID: "p1", Content: "Applied patch", ToolStatus: "success", FileState: &message.ToolFileState{Writes: []message.TrackedFileState{{Path: editPath, SHA256: "write", Exists: true}}}},
	}
	validity := analyzeReadValidity(msgs, buildToolCallMeta(msgs))
	if got := validity[1]; got.Invalidated {
		t.Fatalf("distinct canonical worktree path should stay valid, got %+v", got)
	}
}

func TestAnalyzeReadValidityUsesCommittedFilesFromPartialPatchError(t *testing.T) {
	content := tools.FormatReadResultHeader("1-10", 10, "", "", "") + "\n" + strings.Repeat("body\n", 10)
	msgs := []message.Message{
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "r1", Name: tools.NameRead, Args: json.RawMessage(`{"path":"a.go"}`)}}},
		{Role: message.RoleTool, ToolCallID: "r1", Content: content, ToolStatus: message.ToolStatusSuccess, FileState: &message.ToolFileState{Reads: []message.TrackedFileState{{Path: "a.go", SHA256: "old", Exists: true}}}},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "p1", Name: tools.NameApplyPatch, Args: json.RawMessage(`{"patch":"*** Begin Patch\\n*** End Patch"}`)}}},
		{Role: message.RoleTool, ToolCallID: "p1", Content: "partially applied\n\nError: one file failed", ToolStatus: message.ToolStatusError, FileState: &message.ToolFileState{Writes: []message.TrackedFileState{{Path: "a.go", SHA256: "new", Exists: true}}}},
	}
	validity := analyzeReadValidity(msgs, buildToolCallMeta(msgs))
	if got := validity[1]; !got.Invalidated {
		t.Fatalf("partial patch committed write should invalidate the read, got %+v", got)
	}
}

// TestValidReadsNeverReducedRegardlessOfAgeOrVolume locks the core retention
// invariant: a read output that is still the model's current view of the file
// is never reduced, no matter how old it is or how many sibling reads share
// the same request batch. Trimming valid reads forces re-reads or guesswork.
func TestValidReadsNeverReducedRegardlessOfAgeOrVolume(t *testing.T) {
	policy := defaultContextReductionPolicy()

	for _, age := range []int{policy.ReadLikeAgeTurns, 8, 50, 1000} {
		ctx := newReadReductionContext(largeReadContent(), age)
		if class := classifyRequestReductionToolOutput(ctx); class != requestReductionNone {
			t.Fatalf("still-valid read at age %d should be retained, got %q", age, class)
		}
	}
}

// TestSameBatchReadsShareRetentionDecision guards the batching incentive: all
// reads issued in one request batch have the same age, so classification must
// treat them identically instead of letting later siblings crowd out earlier
// ones (the removed shared byte budget did exactly that).
func TestSameBatchReadsShareRetentionDecision(t *testing.T) {
	var classes []requestReductionClass
	for range 5 {
		ctx := newReadReductionContext(largeReadContent(), 6)
		classes = append(classes, classifyRequestReductionToolOutput(ctx))
	}
	for i, class := range classes {
		if class != classes[0] {
			t.Fatalf("read %d classified %q, sibling classified %q; same-batch reads must share one decision", i, class, classes[0])
		}
	}
}

func newReadReductionContext(content string, age int) requestReductionContext {
	return requestReductionContext{
		ToolName:    tools.NameRead,
		Meta:        toolCallMeta{Name: tools.NameRead, Args: `{"path":"a.go"}`},
		Content:     content,
		ToolStatus:  "success",
		Age:         age,
		Policy:      defaultContextReductionPolicy(),
		ToolResults: compactMinToolResultsPrune,
	}
}

func largeReadContent() string {
	body := strings.Repeat("plain content line\n", 200)
	return "READ_RESULT lines=1-200 total=400\n" + body
}

func TestClassifyInvalidatedOrSupersededReadReduces(t *testing.T) {
	policy := defaultContextReductionPolicy()

	invalidated := newReadReductionContext(largeReadContent(), policy.ReadLikeAgeTurns)
	invalidated.ReadInvalidated = true
	if class := classifyRequestReductionToolOutput(invalidated); class != requestReductionReadLike {
		t.Fatalf("invalidated read at base age should reduce, got %q", class)
	}

	superseded := newReadReductionContext(largeReadContent(), policy.ReadLikeAgeTurns)
	superseded.ReadSuperseded = true
	if class := classifyRequestReductionToolOutput(superseded); class != requestReductionReadLike {
		t.Fatalf("superseded read at base age should reduce, got %q", class)
	}
}

func TestReduceReadOutputSummaryMarkers(t *testing.T) {
	content := "READ_RESULT lines=1-40 total=80\n" +
		strings.Repeat("padding line of content\n", 30) +
		"func TrimmedHelper() {\n" +
		strings.Repeat("padding line of content\n", 9) +
		"Full output saved to /tmp/read-result-abc.txt.\n"

	base := newReadReductionContext(content, 10)
	base.FileState = &message.ToolFileState{Reads: []message.TrackedFileState{{Path: "/workspace/internal/demo.go", SHA256: "abc123", Exists: true}}}

	superseded := base
	superseded.ReadSuperseded = true
	out := reduceReadOutputSummary(superseded)
	if !strings.Contains(out, "truncated="+tools.ReadTruncatedSuperseded) || !strings.Contains(out, "newer read of this range") {
		t.Fatalf("superseded summary wrong: %q", out)
	}
	if strings.Contains(out, "READ_RECOVERY") || strings.Contains(out, "Outline of trimmed lines:") {
		t.Fatalf("superseded summary should not carry recovery metadata or outline: %q", out)
	}

	invalidated := base
	invalidated.ReadInvalidated = true
	out = reduceReadOutputSummary(invalidated)
	if !strings.Contains(out, "truncated="+tools.ReadTruncatedStale) || !strings.Contains(out, "File modified after this read") {
		t.Fatalf("invalidated summary wrong: %q", out)
	}
	if strings.Contains(out, "READ_RECOVERY") || strings.Contains(out, "Outline of trimmed lines:") {
		t.Fatalf("invalidated summary should not carry recovery metadata or outline: %q", out)
	}
}
