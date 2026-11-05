package agent

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestSubInterruptedToolResultPreservesFileAttribution(t *testing.T) {
	for _, interrupt := range []string{"cancel", "replace"} {
		for _, tc := range []struct {
			name       string
			toolName   string
			args       string
			diff       string
			fileState  *message.ToolFileState
			err        error
			wantPaths  []string
			incomplete bool
		}{
			{name: "opaque success", toolName: "sample_mutation", args: `{}`, incomplete: true},
			{name: "opaque failure", toolName: "sample_mutation", args: `{}`, err: errors.New("sample failure"), incomplete: true},
			{name: "diff paths", toolName: tools.NameWrite, args: `{"path":"sample.go"}`, diff: "+sample", wantPaths: []string{"sample.go"}},
			{name: "file state", toolName: "sample_mutation", args: `{}`, fileState: &message.ToolFileState{
				Writes: []message.TrackedFileState{{Path: "sample.go"}},
			}, wantPaths: []string{"sample.go"}},
		} {
			t.Run(interrupt+"/"+tc.name, func(t *testing.T) {
				parent := newTestMainAgent(t, t.TempDir())
				sub := newControllableTestSubAgent(t, parent, "sample-task")
				sub.sessionEpoch = parent.recoverySessionEpoch()
				args := tc.args
				fileState := tc.fileState.Clone()
				if tc.diff != "" {
					raw, err := json.Marshal(map[string]string{"path": filepath.Join(sub.effectiveToolBaseDir(), "sample.go")})
					if err != nil {
						t.Fatal(err)
					}
					args = string(raw)
				}
				if fileState != nil {
					fileState.Writes[0].Path = filepath.Join(sub.effectiveToolBaseDir(), "sample.go")
				}
				turn := sub.newTurn()
				sub.tools.Register(&barrierRecordingTool{name: tc.toolName})
				call := message.ToolCall{ID: "sample-call", Name: tc.toolName, Args: []byte(args)}
				assistant := message.Message{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{call}}
				sub.ctxMgr.Append(assistant)
				sub.persistMessageAsync(assistant, "assistant", nil)
				turn.recordPendingToolCall(PendingToolCall{CallID: call.ID, Name: call.Name, ArgsJSON: args})
				if !turn.admitToolExecution(call.ID) || !turn.registerReadyToolResult(call.ID, &toolResult{
					CallID: call.ID, Name: call.Name, ArgsJSON: args, Result: "completed",
					TurnID: turn.ID, Diff: tc.diff, FileState: fileState, Error: tc.err,
				}) {
					t.Fatal("could not register completed execution")
				}
				if interrupt == "cancel" {
					sub.cancelCurrentTurnFromLoop()
				} else {
					sub.newTurn()
				}
				parent.flushPersist()
				if paths, incomplete := sub.taskChangeSnapshot(); !slices.Equal(paths, tc.wantPaths) || incomplete != tc.incomplete {
					t.Fatalf("runtime attribution = (%v, %t), want (%v, %t)", paths, incomplete, tc.wantPaths, tc.incomplete)
				}
				durable, err := sub.recoveryManager().LoadMessages(sub.instanceID)
				if err != nil {
					t.Fatal(err)
				}
				for source, messages := range map[string][]message.Message{"memory": sub.ctxMgr.Snapshot(), "persisted": durable} {
					t.Run(source, func(t *testing.T) {
						results := 0
						for _, msg := range messages {
							if msg.Role != message.RoleTool || msg.ToolCallID != call.ID {
								continue
							}
							results++
							if !slices.Equal(msg.ToolChangedPaths, tc.wantPaths) || msg.FileAttributionIncomplete != tc.incomplete {
								t.Errorf("message attribution = (%v, %t), want (%v, %t)", msg.ToolChangedPaths, msg.FileAttributionIncomplete, tc.wantPaths, tc.incomplete)
							}
						}
						if results != 1 {
							t.Fatalf("completed messages = %d, want 1", results)
						}
						want := &message.TaskEvidence{TaskID: sub.taskID, ChangedPaths: tc.wantPaths, FileAttributionIncomplete: tc.incomplete}
						if carry := buildTaskEvidenceCarry(sub.taskID, sub.effectiveToolBaseDir(), messages); !reflect.DeepEqual(carry, want) {
							t.Errorf("compaction evidence = %#v, want %#v", carry, want)
						}
						restored := &SubAgent{taskID: sub.taskID, workDir: sub.effectiveToolBaseDir()}
						restored.restoreTaskToolChanges(messages)
						if paths, incomplete := restored.taskChangeSnapshot(); !slices.Equal(paths, tc.wantPaths) || incomplete != tc.incomplete {
							t.Errorf("restored attribution = (%v, %t), want (%v, %t)", paths, incomplete, tc.wantPaths, tc.incomplete)
						}
					})
				}
			})
		}
	}
}
