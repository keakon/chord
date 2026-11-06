package agent

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestCheckpointRequestProjectionOmitsFileHistory(t *testing.T) {
	for _, mode := range []string{compactionSummaryModeModelDriven, message.CompactionSummaryModeTruncateOnly} {
		t.Run(mode, func(t *testing.T) {
			msg := requestViewFixture()
			msg.CompactionSummaryMode = mode
			msg.Content = ensureCheckpointWorklogSection(msg.Content, renderCheckpointWorklog(checkpointWorklog{
				files: []checkpointWorklogFile{
					{Verb: tools.NameWrite, Path: "/workspace/scratch.go"},
					{Verb: tools.NameDelete, Path: "/workspace/scratch.go"},
					{Verb: tools.NameWrite, Path: "/workspace/parser.go"},
				},
				commits:  []string{"[main 1234567] fix: preserve empty input"},
				commands: []string{"[success] make test-fast", "[outcome_unknown] make build"},
			}))
			msg.Content = strings.Replace(msg.Content, "- add test", "- Validate /workspace/parser.go before committing.", 1)
			msg.Content = strings.Replace(msg.Content, "Preserve evidence.", "Preserve evidence.\n### Files touched\n- write /workspace/evidence.go", 1)
			before, err := json.Marshal(msg)
			if err != nil {
				t.Fatal(err)
			}
			projected := projectCheckpointRequests([]message.Message{msg})
			got := projected[0].Content
			for _, unwanted := range []string{"/workspace/scratch.go", "- write /workspace/parser.go"} {
				if strings.Contains(got, unwanted) {
					t.Fatalf("file history retained: %s", unwanted)
				}
			}
			for _, want := range []string{
				"Validate /workspace/parser.go before committing.",
				"[main 1234567] fix: preserve empty input",
				"[success] make test-fast", "[outcome_unknown] make build",
				"Shell tool success does not prove that a background process has finished.",
				"### Files touched\n- write /workspace/evidence.go",
			} {
				if !strings.Contains(got, want) {
					t.Fatalf("lost continuation information: %s", want)
				}
			}
			after, err := json.Marshal(msg)
			if err != nil || string(after) != string(before) {
				t.Fatal("durable checkpoint changed")
			}
			if !reflect.DeepEqual(projectCheckpointRequests(projected), projected) {
				t.Fatal("projection not idempotent")
			}
			var restored message.Message
			if err := json.Unmarshal(before, &restored); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(projectCheckpointRequests([]message.Message{restored}), projected) {
				t.Fatal("resume changed request projection")
			}
		})
	}
}

func TestCheckpointRequestProjectionFileOnlyWorklog(t *testing.T) {
	for _, count := range []int{1, 100} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			var worklog checkpointWorklog
			for i := range count {
				worklog.files = append(worklog.files, checkpointWorklogFile{Verb: tools.NameWrite, Path: fmt.Sprintf("/workspace/file-%d.go", i)})
			}
			section := boundedCheckpointWorklog(worklog, compactWorklogMaxTokens, checkpointWorklogEstimateTokens)
			wrapper := message.CompactionCompressedTag + "\nArchived history: history-1.md\n" + message.CompactionEvidenceTag + "\nKeep recovery evidence."
			msg := message.Message{Role: message.RoleUser, IsCompactionSummary: true, Content: "## Next Step\nRead notes.md.\n\n" + section + wrapper}
			got := projectCheckpointRequests([]message.Message{msg})[0].Content
			if strings.Contains(got, checkpointWorklogHeading) || strings.Contains(got, "/workspace/") {
				t.Fatalf("file-only worklog retained:\n%s", got)
			}
			if !strings.HasPrefix(got, "## Next Step\nRead notes.md.") || !strings.HasSuffix(got, wrapper) {
				t.Fatalf("lost recovery information:\n%s", got)
			}
		})
	}
}

func TestCheckpointRequestProjectionPreservesWorklogOmission(t *testing.T) {
	worklog := checkpointWorklog{
		commits: []string{"[main 1234567] fix: preserve input"},
		files:   []checkpointWorklogFile{{Verb: tools.NameWrite, Path: "/workspace/parser.go"}},
	}
	msg := message.Message{Role: message.RoleUser, IsCompactionSummary: true, Content: renderCheckpointWorklog(worklog) + "\n\n" + checkpointWorklogOmittedNote}
	got := projectCheckpointRequests([]message.Message{msg})[0].Content
	if strings.Contains(got, "/workspace/parser.go") || !strings.Contains(got, worklog.commits[0]) || !strings.Contains(got, checkpointWorklogOmittedNote) {
		t.Fatalf("unexpected projected worklog:\n%s", got)
	}
}

func TestCheckpointRequestProjectionKeepsIndependentFileLists(t *testing.T) {
	worklog := renderCheckpointWorklog(checkpointWorklog{files: []checkpointWorklogFile{{Verb: tools.NameWrite, Path: "/workspace/parser.go"}}})
	for _, tc := range []struct {
		name string
		msg  message.Message
	}{
		{"ordinary message", message.Message{Role: message.RoleUser, Content: worklog}},
		{"assistant message", message.Message{Role: message.RoleAssistant, IsCompactionSummary: true, Content: worklog}},
		{"retained message", message.Message{Role: message.RoleUser, IsCompactionSummary: true, Content: retainedRecentMessagesHeading + "\nUser:\n" + worklog}},
		{"evidence excerpt", message.Message{Role: message.RoleUser, IsCompactionSummary: true, Content: message.CompactionEvidenceTag + "\n" + worklog}},
		{"independent section", message.Message{Role: message.RoleUser, IsCompactionSummary: true, Content: "## Next Step\n### Files touched\n- parser.go"}},
		{"custom worklog", message.Message{Role: message.RoleUser, IsCompactionSummary: true, Content: checkpointWorklogHeading + "\nUnfinished edits:\n### Files touched\n- parser.go: add tests"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := projectCheckpointRequests([]message.Message{tc.msg})[0]; !reflect.DeepEqual(got, tc.msg) {
				t.Fatal("independent content changed")
			}
		})
	}
}

func TestCheckpointRequestProjectionOmitsOnlyEmptyRuntimeSections(t *testing.T) {
	for _, populated := range []bool{false, true} {
		msg := requestViewFixture()
		files := checkpointArchivePointer
		workers := formatSubAgentsAsBullets(nil)
		if populated {
			files = "- parser.go"
			workers = "- worker-1 | state=running"
		}
		sections := "\n\n## Files and Evidence\n" + files + "\n\n## SubAgent State\n" + workers + "\n\n"
		msg.Content = strings.Replace(msg.Content, "\n\n"+retainedRecentMessagesHeading, sections+retainedRecentMessagesHeading, 1)
		got := projectCheckpointRequests([]message.Message{msg})[0].Content
		for _, heading := range []string{"## Files and Evidence", "## SubAgent State"} {
			if strings.Contains(got, heading) != populated {
				t.Fatalf("populated=%v: unexpected section %s:\n%s", populated, heading, got)
			}
		}
	}
}
