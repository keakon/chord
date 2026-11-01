package agent

import (
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestCollectTaskFileEvidence(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	checkpoint := func(taskID string, paths ...string) message.Message {
		return message.Message{
			Role: message.RoleUser, IsCompactionSummary: true,
			CompactionTaskEvidence: &message.TaskEvidence{TaskID: taskID, ChangedPaths: paths},
		}
	}
	for _, tc := range []struct {
		name     string
		messages []message.Message
		want     *message.TaskEvidence
	}{
		{
			name: "no observation",
			messages: []message.Message{
				{Role: message.RoleAssistant, Content: "Changed sample.go."},
				{Role: message.RoleUser, CompactionTaskEvidence: &message.TaskEvidence{TaskID: "task-1", ChangedPaths: []string{"not-a-checkpoint.go"}}},
				checkpoint("task-2", "foreign.go"),
			},
		},
		{
			name: "latest matching checkpoint and tail",
			messages: []message.Message{
				checkpoint("task-1", "obsolete.go"),
				{Role: message.RoleTool, ToolChangedPaths: []string{"before-latest.go"}},
				checkpoint("task-1", "prefix.go"),
				checkpoint("task-2", "foreign.go"),
				{Role: message.RoleTool, ToolChangedPaths: []string{"prefix.go", " tail.go ", ""}},
			},
			want: &message.TaskEvidence{TaskID: "task-1", ChangedPaths: []string{"prefix.go", "tail.go"}},
		},
		{
			name: "file state fallback and explicit paths",
			messages: []message.Message{
				{Role: message.RoleTool, FileState: &message.ToolFileState{
					Writes:  []message.TrackedFileState{{Path: filepath.Join(root, "written.go")}},
					Deletes: []message.TrackedFileState{{Path: filepath.Join(root, "deleted.go")}},
				}},
				{Role: message.RoleTool, ToolChangedPaths: []string{"explicit.go"}, FileState: &message.ToolFileState{
					Writes: []message.TrackedFileState{{Path: filepath.Join(root, "unused.go")}},
				}},
			},
			want: &message.TaskEvidence{TaskID: "task-1", ChangedPaths: []string{"written.go", "deleted.go", "explicit.go"}},
		},
		{
			name: "unknown attribution",
			messages: []message.Message{
				{Role: message.RoleTool, FileAttributionIncomplete: true},
			},
			want: &message.TaskEvidence{TaskID: "task-1", FileAttributionIncomplete: true},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := collectTaskFileEvidence("task-1", root, tc.messages)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("evidence = %#v, want %#v", got, tc.want)
			}
			if got != nil && len(got.ChangedPaths) > 0 {
				got.ChangedPaths[0] = "clone.go"
				if again := collectTaskFileEvidence("task-1", root, tc.messages); !reflect.DeepEqual(again, tc.want) {
					t.Fatalf("collection mutated source: %#v", again)
				}
			}
		})
	}
}

func TestTaskEvidenceCarryBoundsOnlyCheckpoint(t *testing.T) {
	t.Parallel()
	paths := make([]string, message.MaxTaskEvidencePaths+2)
	for i := range paths {
		paths[i] = fmt.Sprintf("file-%03d.go", i)
	}
	messages := []message.Message{{Role: message.RoleTool, ToolChangedPaths: paths}}
	root := t.TempDir()
	exact := buildTaskEvidenceCarry("task-1", root, []message.Message{{
		Role: message.RoleTool, ToolChangedPaths: paths[:message.MaxTaskEvidencePaths],
	}})
	if exact.FileAttributionIncomplete || len(exact.ChangedPaths) != message.MaxTaskEvidencePaths {
		t.Fatalf("exact bound = %#v, want complete attribution", exact)
	}
	carry := buildTaskEvidenceCarry("task-1", root, messages)
	if carry == nil || len(carry.ChangedPaths) != message.MaxTaskEvidencePaths || !carry.FileAttributionIncomplete {
		t.Fatalf("carry = %#v, want bounded paths and incomplete attribution", carry)
	}
	sub := &SubAgent{taskID: "task-1", workDir: root}
	sub.restoreTaskToolChanges(messages)
	if len(sub.actualChangedFiles) != len(paths) || sub.fileAttributionIncomplete {
		t.Fatalf("restore = (%d, %v), want all live paths without truncation", len(sub.actualChangedFiles), sub.fileAttributionIncomplete)
	}
	messages = []message.Message{
		{Role: message.RoleUser, IsCompactionSummary: true, CompactionTaskEvidence: carry},
		{Role: message.RoleTool, ToolChangedPaths: []string{"tail.go"}},
	}
	repeated := buildTaskEvidenceCarry("task-1", root, messages)
	if len(repeated.ChangedPaths) != message.MaxTaskEvidencePaths || !repeated.FileAttributionIncomplete {
		t.Fatalf("repeated carry = %#v, want loss disclosed", repeated)
	}
	sub = &SubAgent{taskID: "task-1", workDir: root}
	sub.restoreTaskToolChanges(messages)
	if len(sub.actualChangedFiles) != message.MaxTaskEvidencePaths+1 || !sub.fileAttributionIncomplete {
		t.Fatalf("restore = (%d, %v), want carry plus tail and incomplete attribution", len(sub.actualChangedFiles), sub.fileAttributionIncomplete)
	}
}

func TestTaskFileEvidenceSurvivesDurableCompaction(t *testing.T) {
	t.Parallel()
	parent, sub := newMixedBatchTestSubAgent(t)
	messages := []message.Message{{Role: message.RoleUser, Content: "Inspect the workspace."}}
	for i := range 24 {
		callID := fmt.Sprintf("call-%d", i)
		messages = append(messages,
			message.Message{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: callID, Name: tools.NameWrite}}},
			message.Message{
				Role: message.RoleTool, ToolCallID: callID, Content: strings.Repeat("output ", 200),
				ToolChangedPaths: []string{fmt.Sprintf("file-%02d.go", i)}, FileAttributionIncomplete: i == 0,
			},
		)
	}
	sub.ctxMgr.RestoreMessages(messages)
	manager := sub.recoveryManager()
	if manager == nil {
		t.Fatal("missing durable recovery manager")
	}
	want := collectTaskFileEvidence(sub.taskID, sub.workDir, messages)
	for pass := range 2 {
		target := estimateMessagesTokens(sub.ctxMgr, messages) * 2 / 3
		compressed, ok := sub.compactContextForTarget(messages, target, "context budget protection")
		if !ok {
			t.Fatalf("compaction %d did not apply", pass)
		}
		if index := latestTaskEvidenceIndex(sub.taskID, compressed); index < 0 || len(compressed[index].CompactionTaskEvidence.ChangedPaths) == 0 {
			t.Fatalf("compaction %d lost prefix observations", pass)
		}
		parent.flushPersist()
		durable, err := manager.LoadMessages(sub.instanceID)
		if err != nil {
			t.Fatal(err)
		}
		if got := collectTaskFileEvidence(sub.taskID, sub.workDir, durable); !reflect.DeepEqual(got, want) {
			t.Fatalf("durable evidence = %#v, want %#v", got, want)
		}
		restored := &SubAgent{taskID: sub.taskID, workDir: sub.workDir}
		restored.restoreTaskToolChanges(durable)
		actual := make([]string, 0, len(restored.actualChangedFiles))
		for path := range restored.actualChangedFiles {
			actual = append(actual, path)
		}
		slices.Sort(actual)
		expected := slices.Clone(want.ChangedPaths)
		slices.Sort(expected)
		if !slices.Equal(actual, expected) || !restored.fileAttributionIncomplete {
			t.Fatalf("restored evidence = (%v, %v), want (%v, true)", actual, restored.fileAttributionIncomplete, expected)
		}
		messages = compressed
	}
}
