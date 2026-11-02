package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/tools"
)

func TestExternalWorkerCompletionRequiresReport(t *testing.T) {
	const missingReport = "external agent finished without a completion report"
	const assistantReport = `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Sample assistant report"}]}}`
	for _, tc := range []struct {
		name        string
		events      string
		wantState   SubAgentState
		wantSummary string
	}{
		{
			name: "missing result text", events: `{"type":"result","subtype":"success"}`,
			wantState: SubAgentStateFailed, wantSummary: missingReport,
		},
		{
			name: "empty result text", events: `{"type":"result","subtype":"success","result":""}`,
			wantState: SubAgentStateFailed, wantSummary: missingReport,
		},
		{
			name: "whitespace result text", events: `{"type":"result","subtype":"success","result":" \t\n "}`,
			wantState: SubAgentStateFailed, wantSummary: missingReport,
		},
		{
			name: "whitespace assistant text",
			events: `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":" \t\n "}]}}
{"type":"result","subtype":"success","result":""}`,
			wantState: SubAgentStateFailed, wantSummary: missingReport,
		},
		{
			name: "only partial assistant text",
			events: `{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Sample partial text"}}}
{"type":"result","subtype":"success","result":""}`,
			wantState: SubAgentStateFailed, wantSummary: missingReport,
		},
		{
			name: "result text", events: `{"type":"result","subtype":"success","result":" Sample result \n"}`,
			wantState: SubAgentStateCompleted, wantSummary: "Sample result",
		},
		{
			name:      "result text takes precedence",
			events:    assistantReport + "\n" + `{"type":"result","subtype":"success","result":"Sample result"}`,
			wantState: SubAgentStateCompleted, wantSummary: "Sample result",
		},
		{
			name:      "assistant fallback for missing result",
			events:    assistantReport + "\n" + `{"type":"result","subtype":"success"}`,
			wantState: SubAgentStateCompleted, wantSummary: "Sample assistant report",
		},
		{
			name:      "assistant fallback for empty result",
			events:    assistantReport + "\n" + `{"type":"result","subtype":"success","result":""}`,
			wantState: SubAgentStateCompleted, wantSummary: "Sample assistant report",
		},
		{
			name:      "assistant fallback for whitespace result",
			events:    assistantReport + "\n" + `{"type":"result","subtype":"success","result":" \t\n "}`,
			wantState: SubAgentStateCompleted, wantSummary: "Sample assistant report",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newTestMainAgent(t, t.TempDir())
			script := writeFakeClaude(t, "\nprintf '%s\\n' '"+tc.events+"'\n")
			configureExternalTestRuntime(t, a, script, `"*": allow`)
			a.agentConfigs["external-worker"].Backend.LaunchArgs = []string{"--include-partial-messages"}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			handle, err := a.CreateSubAgent(ctx, tools.SubAgentRequest{
				Description: "Report the sample task result", AgentType: "external-worker",
			})
			if err != nil {
				t.Fatal(err)
			}
			worker := a.subs.runtime(handle.AgentID).(*externalWorker)
			if waitErr := worker.waitDone(ctx); waitErr != nil {
				t.Fatalf("wait for external worker: %v", waitErr)
			}
			settlements, err := loadTaskSettlements(a.SessionDir())
			if err != nil {
				t.Fatal(err)
			}
			settlement := settlements[taskAttemptKey{TaskID: handle.TaskID, Attempt: 1}]
			if settlement == nil || settlement.Outcome != string(tc.wantState) || settlement.Summary != tc.wantSummary {
				t.Fatalf("autonomous settlement = %+v, want %s with %q", settlement, tc.wantState, tc.wantSummary)
			}
			completed := tc.wantState == SubAgentStateCompleted
			if (settlement.Completion != nil) != completed {
				t.Fatalf("settlement completion = %+v, completed=%t", settlement.Completion, completed)
			}

			record := driveExternalTask(t, a, handle.TaskID)
			if record.State != string(tc.wantState) || record.LastSummary != tc.wantSummary || !record.SettlementDurable {
				t.Fatalf("terminal record = %+v, want durable %s with %q", record, tc.wantState, tc.wantSummary)
			}
			if (record.LastCompletion != nil) != completed {
				t.Fatalf("last completion = %+v, completed=%t", record.LastCompletion, completed)
			}
			if record.ExternalSession == nil || !record.ExternalSession.ProcessEnded || len(record.ExternalSession.Inputs) != 0 {
				t.Fatalf("terminal result left a process or undelivered input: %+v", record.ExternalSession)
			}
			records, err := loadDurableTaskRecords(a.SessionDir())
			if err != nil {
				t.Fatal(err)
			}
			if stored := records[handle.TaskID]; stored == nil || stored.State != record.State ||
				stored.ExternalSession == nil || !stored.ExternalSession.ProcessEnded || len(stored.ExternalSession.Inputs) != 0 {
				t.Fatalf("durable task record lost the terminal outcome: %+v", stored)
			}

			messages, err := loadSubAgentMailboxMessages(a.SessionDir())
			if err != nil {
				t.Fatal(err)
			}
			var completions, failures int
			for _, msg := range messages {
				if msg.TaskID != handle.TaskID {
					continue
				}
				if msg.Kind == SubAgentMailboxKindCompleted {
					completions++
				}
				if msg.Kind == SubAgentMailboxKindRiskAlert && msg.Subtype == agentMessageSubtypeTaskFailure {
					failures++
					if msg.Priority != SubAgentMailboxPriorityInterrupt || !strings.Contains(msg.Payload, "do not treat the task as completed") {
						t.Fatalf("failure mailbox lacks the explicit interruption: %+v", msg)
					}
				}
			}
			if completed && (completions != 1 || failures != 0) {
				t.Fatalf("completed task mailbox: completions=%d failures=%d", completions, failures)
			}
			if !completed && (completions != 0 || failures != 1) {
				t.Fatalf("failed task mailbox: completions=%d failures=%d", completions, failures)
			}
		})
	}
}
