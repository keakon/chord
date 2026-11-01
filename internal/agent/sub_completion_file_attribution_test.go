package agent

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestCompletionDisclosesIncompleteFileAttribution(t *testing.T) {
	t.Parallel()
	parent, sub := newMixedBatchTestSubAgent(t)
	sub.tools.Register(dummyMutatingTool{name: "OpaqueMutation"})
	sub.recordTaskToolChanges(&toolResult{Name: "OpaqueMutation", ArgsJSON: `{}`}, false)
	sub.handleLLMResponse(&llmResult{turnID: 1, resp: &message.Response{ToolCalls: convertCalls([]messageToolCall{
		mustJSONToolCall(t, "complete", tools.NameComplete, map[string]any{"summary": "Inspection complete."}),
	})}})

	result := waitForAgentEventType(t, parent.eventCh, EventAgentDone).Payload.(*AgentResult)
	if result.Envelope == nil || !result.Envelope.FileAttributionIncomplete || len(result.Envelope.ActualFilesChanged) != 0 {
		t.Fatalf("envelope = %#v, want unknown file attribution without invented paths", result.Envelope)
	}
	if sub.turn.SubAgentCompletionRecoveryCount != 0 {
		t.Fatalf("budgets = %#v, want unknown attribution alone not to reject", sub.turn)
	}
	mailbox := formatSubAgentMailboxInjectionText(&SubAgentMailboxMessage{
		MessageID: "msg-1", AgentID: sub.instanceID, TaskID: sub.taskID,
		Kind: SubAgentMailboxKindCompleted, Summary: result.Summary, Completion: result.Envelope,
	})
	parent.subs.mu.Lock()
	parent.subs.taskRecords[sub.taskID] = &DurableTaskRecord{
		TaskID: sub.taskID, State: string(SubAgentStateCompleted), ResumePolicy: taskResumePolicyNotify,
		LastSummary: result.Summary, LastCompletion: result.Envelope,
	}
	parent.subs.mu.Unlock()
	snapshot := parent.buildCoordinationSnapshotOverlayForRequest(nil)
	for surface, text := range map[string]string{"mailbox": mailbox, "snapshot": snapshot} {
		if !strings.Contains(text, "file_attribution_incomplete: true") {
			t.Fatalf("%s lost unknown attribution:\n%s", surface, text)
		}
	}
}

func TestCompletionDoesNotInventIncompleteFileAttribution(t *testing.T) {
	t.Parallel()
	for _, env := range []*CompletionEnvelope{nil, {Summary: "Inspection complete."}} {
		parent := newTestMainAgent(t, t.TempDir())
		mailbox := formatSubAgentMailboxInjectionText(&SubAgentMailboxMessage{
			MessageID: "msg-1", AgentID: "worker-1", TaskID: "task-1",
			Kind: SubAgentMailboxKindCompleted, Summary: "Inspection complete.", Completion: env,
		})
		parent.subs.taskRecords["task-1"] = &DurableTaskRecord{
			TaskID: "task-1", State: string(SubAgentStateCompleted), ResumePolicy: taskResumePolicyNotify,
			LastSummary: "Inspection complete.", LastCompletion: env,
		}
		snapshot := parent.buildCoordinationSnapshotOverlayForRequest(nil)
		for surface, text := range map[string]string{"mailbox": mailbox, "snapshot": snapshot} {
			if strings.Contains(text, "file_attribution_incomplete") {
				t.Fatalf("%s invented unknown attribution:\n%s", surface, text)
			}
		}
	}
}

func TestCompletionFileSourcesReachOwnerSeparately(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		reported []string
		observed []string
	}{
		{name: "empty"},
		{name: "reported only", reported: []string{"reported.go"}},
		{name: "observed only", observed: []string{"observed.go"}},
		{name: "overlapping", reported: []string{"shared.go", "reported.go"}, observed: []string{"shared.go", "observed.go"}},
		{name: "different", reported: []string{"reported.go"}, observed: []string{"observed.go"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parent, sub := newMixedBatchTestSubAgent(t)
			sub.restoreTaskToolChanges([]message.Message{{Role: message.RoleTool, ToolChangedPaths: tc.observed}})
			sub.handleLLMResponse(&llmResult{turnID: sub.turn.ID, resp: &message.Response{ToolCalls: convertCalls([]messageToolCall{
				mustJSONToolCall(t, "complete", tools.NameComplete, map[string]any{
					"summary": "Task complete.", "files_changed": tc.reported,
					"actual_files_changed": []string{"injected.go"}, "file_attribution_incomplete": true,
				}),
			})}})
			result := waitForAgentEventType(t, parent.eventCh, EventAgentDone).Payload.(*AgentResult)
			observed := slices.Clone(tc.observed)
			slices.Sort(observed)
			if !reflect.DeepEqual(result.Envelope.ReportedFilesChanged, tc.reported) ||
				!slices.Equal(result.Envelope.ActualFilesChanged, observed) ||
				result.Envelope.FileAttributionIncomplete {
				t.Fatalf("envelope = %#v, want separate reported and runtime sources", result.Envelope)
			}
			if sub.llmRequestInFlight.Load() || sub.turn.SubAgentCompletionRecoveryCount != 0 {
				t.Fatal("file source difference triggered completion recovery")
			}
			mailbox := formatSubAgentMailboxInjectionText(&SubAgentMailboxMessage{
				MessageID: "msg-1", AgentID: sub.instanceID, TaskID: sub.taskID,
				Kind: SubAgentMailboxKindCompleted, Summary: result.Summary, Completion: result.Envelope,
			})
			parent.subs.mu.Lock()
			parent.subs.taskRecords[sub.taskID] = &DurableTaskRecord{
				TaskID: sub.taskID, State: string(SubAgentStateCompleted), ResumePolicy: taskResumePolicyNotify,
				LastSummary: result.Summary, LastCompletion: result.Envelope,
			}
			parent.subs.mu.Unlock()
			snapshot := parent.buildCoordinationSnapshotOverlayForRequest(nil)
			for surface, text := range map[string]string{"mailbox": mailbox, "snapshot": snapshot} {
				for label, paths := range map[string][]string{
					"reported_files_changed": result.Envelope.ReportedFilesChanged,
					"actual_files_changed":   result.Envelope.ActualFilesChanged,
				} {
					if len(paths) > 0 {
						if !strings.Contains(text, label+": "+strings.Join(paths, ", ")) {
							t.Fatalf("%s lost %s:\n%s", surface, label, text)
						}
					} else if strings.Contains(text, label+":") {
						t.Fatalf("%s invented %s:\n%s", surface, label, text)
					}
				}
				if strings.Contains(text, "\n- files_changed:") || strings.Contains(text, "\n  files_changed:") ||
					strings.Contains(text, "injected.go") {
					t.Fatalf("%s merged file sources or trusted injected evidence:\n%s", surface, text)
				}
			}
		})
	}
}

func TestCompletionFileSourcesCloneAndJSON(t *testing.T) {
	t.Parallel()
	env := &CompletionEnvelope{
		ReportedFilesChanged: []string{" reported.go ", "reported.go"},
		ActualFilesChanged:   []string{"observed.go"}, FileAttributionIncomplete: true,
	}
	clone := cloneAgentResult(&AgentResult{Envelope: env})
	clone.Envelope.ReportedFilesChanged[0] = "other.go"
	clone.Envelope.ActualFilesChanged[0] = "other.go"
	if env.ReportedFilesChanged[0] != " reported.go " || env.ActualFilesChanged[0] != "observed.go" {
		t.Fatal("clone changed file sources in the original")
	}
	data := marshalCompletionEnvelope(env)
	restored := unmarshalCompletionEnvelope(data)
	if !reflect.DeepEqual(restored, normalizeCompletionEnvelope(env)) {
		t.Fatalf("restored = %#v, want both sources and incomplete attribution", restored)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	if _, ok := fields["files_changed"]; ok {
		t.Fatalf("JSON kept derived file union: %s", data)
	}
	for _, source := range []*CompletionEnvelope{
		{ReportedFilesChanged: []string{"reported.go"}},
		{ActualFilesChanged: []string{"observed.go"}},
		{FileAttributionIncomplete: true},
	} {
		if !reflect.DeepEqual(unmarshalCompletionEnvelope(marshalCompletionEnvelope(source)), source) {
			t.Fatalf("source-only envelope lost during JSON round trip: %#v", source)
		}
	}
}

func TestMailboxBudgetCountsBothFileSources(t *testing.T) {
	t.Parallel()
	msg := SubAgentMailboxMessage{Completion: &CompletionEnvelope{Summary: "Complete."}}
	before := mailboxMessageBytes(msg)
	msg.Completion.ReportedFilesChanged = []string{"reported.go", "shared.go"}
	msg.Completion.ActualFilesChanged = []string{"observed.go", "shared.go"}
	want := len("reported.go") + len("observed.go") + 2*len("shared.go")
	if got := mailboxMessageBytes(msg) - before; got != want {
		t.Fatalf("file source budget = %d, want %d", got, want)
	}
}
