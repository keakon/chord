package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/keakon/chord/internal/message"
)

func TestCompletionFileDiffShowsRetainedRecordDifferences(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		reported   []string
		observed   []string
		incomplete bool
		want       []string
	}{
		{name: "both empty"},
		{
			name:     "reported only",
			reported: []string{"a.go"},
			want: []string{
				"reported_only_in_records: 1 (a.go)",
				completionFileDiffNote,
			},
		},
		{
			name:     "observed only",
			observed: []string{"b.go"},
			want: []string{
				"observed_only_in_records: 1 (b.go)",
				completionFileDiffNote,
			},
		},
		{
			name:     "one-sided absolute record",
			reported: []string{"/tmp/a.go"},
			want: []string{
				"reported_only_in_records: 1 (/tmp/a.go)",
				completionFileDiffNote,
			},
		},
		{
			name:     "identical",
			reported: []string{"a.go", "b.go"},
			observed: []string{"b.go", "a.go"},
		},
		{
			name:     "partial overlap",
			reported: []string{"a.go", "b.go"},
			observed: []string{"b.go", "c.go"},
			want: []string{
				"reported_only_in_records: 1 (a.go)",
				"observed_only_in_records: 1 (c.go)",
				completionFileDiffNote,
			},
		},
		{
			name:     "disjoint",
			reported: []string{"a.go"},
			observed: []string{"b.go"},
			want: []string{
				"reported_only_in_records: 1 (a.go)",
				"observed_only_in_records: 1 (b.go)",
				completionFileDiffNote,
			},
		},
		{
			name:     "duplicates and order do not matter",
			reported: []string{" b.go ", "a.go", "b.go", "", "a.go"},
			observed: []string{"a.go"},
			want: []string{
				"reported_only_in_records: 1 (b.go)",
				completionFileDiffNote,
			},
		},
		{
			name:     "dot segment matches",
			reported: []string{"./a.go"},
			observed: []string{"a.go"},
		},
		{
			name:     "repeated separators and dot segments normalize",
			reported: []string{"dir//./a.go"},
			observed: []string{"dir/a.go"},
		},
		{
			name:     "path with spaces is preserved",
			reported: []string{"docs/My Notes.md"},
			observed: []string{"docs/My Notes.md"},
		},
		{
			name:     "same basename in different directories does not match",
			reported: []string{"one/x.go"},
			observed: []string{"two/x.go"},
			want: []string{
				"reported_only_in_records: 1 (one/x.go)",
				"observed_only_in_records: 1 (two/x.go)",
				completionFileDiffNote,
			},
		},
		{
			name:     "case is not folded",
			reported: []string{"A.go"},
			observed: []string{"a.go"},
			want: []string{
				"reported_only_in_records: 1 (A.go)",
				"observed_only_in_records: 1 (a.go)",
				completionFileDiffNote,
			},
		},
		{
			name:     "absolute and relative mix suppresses the diff",
			reported: []string{"a.go"},
			observed: []string{"/tmp/b.go"},
		},
		{
			name:     "parent segment suppresses the diff",
			reported: []string{"dir/../a.go"},
			observed: []string{"a.go"},
		},
		{
			name:     "identical parent segment is not a difference",
			reported: []string{"dir/../a.go"},
			observed: []string{"dir/../a.go"},
		},
		{
			name:     "mixed path styles suppress the diff",
			reported: []string{`dir\x.go`},
			observed: []string{"dir/y.go"},
		},
		{
			name:     "backslashes do not establish a shared platform",
			reported: []string{`dir\x.go`},
			observed: []string{`dir\y.go`},
		},
		{
			name:     "drive prefixes do not establish a shared platform",
			reported: []string{"C:/dir/x.go"},
			observed: []string{"C:/dir/y.go"},
		},
		{
			name:     "network roots stay ambiguous",
			reported: []string{"//server/share/x.go"},
			observed: []string{"/server/share/y.go"},
		},
		{
			name:       "incomplete records keep the qualifier",
			reported:   []string{"a.go"},
			observed:   []string{"b.go"},
			incomplete: true,
			want: []string{
				"reported_only_in_records: 1 (a.go)",
				"observed_only_in_records: 1 (b.go)",
				completionFileDiffNoteIncomplete,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			env := &CompletionEnvelope{
				ReportedFilesChanged:      tt.reported,
				ActualFilesChanged:        tt.observed,
				FileAttributionIncomplete: tt.incomplete,
			}
			got := completionFileDiffLines(env, "")
			if !slices.Equal(got, tt.want) {
				t.Fatalf("diff lines = %#v, want %#v", got, tt.want)
			}
			if env.FileAttributionIncomplete != tt.incomplete {
				t.Fatal("diff rendering changed the incomplete flag")
			}
		})
	}
}

func TestCompletionFileDiffNilEnvelopeHasNoLines(t *testing.T) {
	t.Parallel()
	if lines := completionFileDiffLines(nil, "- "); len(lines) != 0 {
		t.Fatalf("nil envelope produced lines: %#v", lines)
	}
}

func TestCompletionFileDiffPreservesAmbiguousOneSidedRecords(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name  string
		paths []string
		want  string
	}{
		{
			name:  "backslash is not a slash",
			paths: []string{`dir\item.go`, "dir/item.go", `dir\item.go`},
			want:  `observed_only_in_records: 2 (dir/item.go, dir\\item.go)`,
		},
		{
			name:  "drive spelling is preserved",
			paths: []string{"C:/dir//item.go", "C:/dir/item.go"},
			want:  "observed_only_in_records: 2 (C:/dir//item.go, C:/dir/item.go)",
		},
		{
			name:  "network root is preserved",
			paths: []string{"//server/share/item.go", "/server/share/item.go"},
			want:  "observed_only_in_records: 2 (//server/share/item.go, /server/share/item.go)",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			lines := completionFileDiffLines(&CompletionEnvelope{ActualFilesChanged: tt.paths}, "")
			if !slices.Equal(lines, []string{tt.want, completionFileDiffNote}) {
				t.Fatalf("one-sided records = %#v, want %q and note", lines, tt.want)
			}
		})
	}
}

func TestCompletionFileDiffAmbiguityDoesNotDependOnOrder(t *testing.T) {
	t.Parallel()
	for _, paths := range [][]string{
		{`dir\item.go`, "dir/item.go"},
		{"C:item.go", "./C:item.go"},
		{"//server/share/item.go", "/server/share/item.go"},
	} {
		for range 2 {
			for _, env := range []*CompletionEnvelope{
				{ReportedFilesChanged: paths, ActualFilesChanged: []string{"other.go"}},
				{ReportedFilesChanged: []string{"other.go"}, ActualFilesChanged: paths},
			} {
				if lines := completionFileDiffLines(env, ""); len(lines) != 0 {
					t.Fatalf("ambiguous records %#v produced a diff: %#v", env, lines)
				}
			}
			slices.Reverse(paths)
		}
	}
}

func TestCompletionFileDiffPreservesDistinctPOSIXFiles(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("backslash is a path separator on Windows")
	}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "dir"), 0o700); err != nil {
		t.Fatal(err)
	}
	state := &message.ToolFileState{}
	for _, name := range []string{`dir\item.go`, "dir/item.go"} {
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
		state.Writes = append(state.Writes, message.TrackedFileState{Path: path, Exists: true})
	}
	sub := &SubAgent{workDir: root}
	sub.recordTaskToolChanges(&toolResult{FileState: state}, false)
	result := sub.enrichCompletionResult(&AgentResult{Summary: "Complete."})
	lines := completionFileDiffLines(result.Envelope, "")
	want := []string{
		`observed_only_in_records: 2 (dir/item.go, dir\\item.go)`,
		completionFileDiffNote,
	}
	if !slices.Equal(lines, want) {
		t.Fatalf("distinct observed files = %#v, want %#v", lines, want)
	}
}

func TestCompletionFileDiffLinesStayBounded(t *testing.T) {
	t.Parallel()
	longA := strings.Repeat("a", 116) + ".go"
	longB := strings.Repeat("b", 116) + ".go"
	longC := strings.Repeat("c", 116) + ".go"
	env := &CompletionEnvelope{
		ReportedFilesChanged: []string{longA, longB, longC, "d.go", "e.go"},
	}
	lines := completionFileDiffLines(env, "  ")
	if len(lines) != 2 {
		t.Fatalf("lines = %#v, want one direction and the note", lines)
	}
	bounded := lines[0]
	if !strings.HasPrefix(bounded, "  reported_only_in_records: 5 (") {
		t.Fatalf("bounded line lost its label or count: %q", bounded)
	}
	if !strings.HasSuffix(bounded, ", ...4 more)") {
		t.Fatalf("bounded line has an unexpected omission marker: %q", bounded)
	}
	if !strings.HasPrefix(lines[1], "  "+completionFileDiffNote) {
		t.Fatalf("note line = %q", lines[1])
	}
	for _, line := range lines {
		if !utf8.ValidString(line) {
			t.Fatalf("line is not valid UTF-8: %q", line)
		}
		if n := utf8.RuneCountInString(line); n > completionFileDiffLineMaxRunes {
			t.Fatalf("line has %d runes, want at most %d: %q", n, completionFileDiffLineMaxRunes, line)
		}
	}
}

func TestCompletionFileDiffExampleBoundary(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		items []string
		want  string
	}{
		{
			name:  "three examples fit",
			items: []string{"a.go", "b.go", "c.go"},
			want:  "reported_only_in_records: 3 (a.go, b.go, c.go)",
		},
		{
			name:  "fourth item is omitted",
			items: []string{"a.go", "b.go", "c.go", "d.go"},
			want:  "reported_only_in_records: 4 (a.go, b.go, c.go, ...1 more)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			lines := completionFileDiffLines(&CompletionEnvelope{ReportedFilesChanged: tt.items}, "")
			if len(lines) == 0 || lines[0] != tt.want {
				t.Fatalf("lines = %#v, want first line %q", lines, tt.want)
			}
		})
	}
}

func TestCompletionFileDiffEscapesControlCharactersAndKeepsUnicode(t *testing.T) {
	t.Parallel()
	env := &CompletionEnvelope{
		ReportedFilesChanged: []string{"line\nbreak.go", "tab\tname.go", "esc\x1bname.go"},
	}
	lines := completionFileDiffLines(env, "")
	if len(lines) == 0 {
		t.Fatal("expected a diff line")
	}
	line := lines[0]
	if strings.ContainsAny(line, "\n\r\t\x1b") {
		t.Fatalf("control characters leaked into one line: %q", line)
	}
	for _, want := range []string{`line\nbreak.go`, `tab\tname.go`, `esc\u001bname.go`} {
		if !strings.Contains(line, want) {
			t.Fatalf("escaped line missing %q: %q", want, line)
		}
	}
	if !utf8.ValidString(line) {
		t.Fatalf("line is not valid UTF-8: %q", line)
	}

	unicodeLines := completionFileDiffLines(&CompletionEnvelope{ReportedFilesChanged: []string{"文档/说明.md"}}, "")
	if len(unicodeLines) == 0 || !strings.Contains(unicodeLines[0], "文档/说明.md") {
		t.Fatalf("unicode path was not preserved: %#v", unicodeLines)
	}
}

func TestCompletionFileDiffPreservesSourceRecords(t *testing.T) {
	t.Parallel()
	env := &CompletionEnvelope{
		Summary:                   "done",
		ReportedFilesChanged:      []string{"./a.go", " b.go "},
		ActualFilesChanged:        []string{"c.go"},
		FileAttributionIncomplete: true,
	}
	reportedBefore := slices.Clone(env.ReportedFilesChanged)
	observedBefore := slices.Clone(env.ActualFilesChanged)
	first := completionFileDiffLines(env, "")
	second := completionFileDiffLines(env, "")
	if !slices.Equal(first, second) {
		t.Fatalf("repeated rendering differs: %#v vs %#v", first, second)
	}
	if !slices.Equal(env.ReportedFilesChanged, reportedBefore) || !slices.Equal(env.ActualFilesChanged, observedBefore) {
		t.Fatalf("rendering mutated the source records: %#v", env)
	}

	data := marshalCompletionEnvelope(env)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	for _, derived := range []string{"reported_only_in_records", "observed_only_in_records", "file_list_note"} {
		if _, ok := fields[derived]; ok {
			t.Fatalf("persisted envelope gained derived field %q: %s", derived, data)
		}
	}
	restored := unmarshalCompletionEnvelope(data)
	if restored == nil {
		t.Fatal("restored envelope is nil")
	}
	if got := completionFileDiffLines(restored, ""); !slices.Equal(got, first) {
		t.Fatalf("restored rendering = %#v, want %#v", got, first)
	}
}

func TestCompletionFileDiffDisclosesCheckpointTruncation(t *testing.T) {
	t.Parallel()
	paths := make([]string, message.MaxTaskEvidencePaths+2)
	for i := range paths {
		paths[i] = fmt.Sprintf("file-%03d.go", i)
	}
	carry := buildTaskEvidenceCarry("task-1", t.TempDir(), []message.Message{{
		Role: message.RoleTool, ToolChangedPaths: paths,
	}})
	sub := &SubAgent{taskID: "task-1", workDir: t.TempDir()}
	sub.restoreTaskToolChanges([]message.Message{
		{Role: message.RoleUser, IsCompactionSummary: true, CompactionTaskEvidence: carry},
		{Role: message.RoleTool, ToolChangedPaths: []string{"tail.go"}},
	})
	result := sub.enrichCompletionResult(&AgentResult{Summary: "restored", Envelope: &CompletionEnvelope{
		Summary:              "restored",
		ReportedFilesChanged: paths[:message.MaxTaskEvidencePaths],
	}})
	if result.Envelope == nil || !result.Envelope.FileAttributionIncomplete {
		t.Fatalf("envelope = %#v, want the retained incomplete flag", result.Envelope)
	}
	lines := completionFileDiffLines(result.Envelope, "")
	if len(lines) != 2 || lines[0] != "observed_only_in_records: 1 (tail.go)" || lines[1] != completionFileDiffNoteIncomplete {
		t.Fatalf("lines = %#v, want the retained tail diff with the incomplete note", lines)
	}
}

func TestMailboxAndSnapshotShareCompletionFileDiff(t *testing.T) {
	t.Parallel()
	env := &CompletionEnvelope{
		Summary:              "done",
		ReportedFilesChanged: []string{"internal/a.go", "internal/b.go"},
		ActualFilesChanged:   []string{"internal/b.go", "internal/c.go"},
	}
	mailbox := formatSubAgentMailboxInjectionText(&SubAgentMailboxMessage{
		MessageID: "msg-1", AgentID: "worker-1", TaskID: "task-1",
		Kind: SubAgentMailboxKindCompleted, Summary: "done", Completion: env,
	})
	a := newTestMainAgent(t, t.TempDir())
	a.explicitUserTurnCount.Store(1)
	a.subs.taskRecords["task-1"] = &DurableTaskRecord{
		TaskID: "task-1", State: string(SubAgentStateCompleted), ResumePolicy: taskResumePolicyNotify,
		LastSummary: "done", LastUpdatedTurn: 1, LastCompletion: env,
	}
	snapshot := a.buildCoordinationSnapshotOverlayForRequest(nil)
	for _, want := range []string{
		"reported_only_in_records: 1 (internal/a.go)",
		"observed_only_in_records: 1 (internal/c.go)",
		completionFileDiffNote,
	} {
		if !strings.Contains(mailbox, "\n- "+want) {
			t.Fatalf("mailbox missing diff line %q:\n%s", want, mailbox)
		}
		if !strings.Contains(snapshot, "\n  "+want) {
			t.Fatalf("snapshot missing diff line %q:\n%s", want, snapshot)
		}
	}
}
