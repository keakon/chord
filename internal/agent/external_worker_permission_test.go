package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/externalclaude"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/tools"
)

func TestExternalPermissionBindsToolIdentity(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	configureExternalTestRuntime(t, a, "claude", `"*": allow`)
	w, err := a.newExternalWorker(externalWorkerConfig{
		agentDef: a.agentConfigs["external-worker"], instanceID: "external-1", taskID: "task-1",
		ctx: context.Background(), cancel: func() {},
	})
	if err != nil {
		t.Fatal(err)
	}
	first := externalclaude.Event{RequestID: "request-1", Request: &externalclaude.ControlRequest{
		ToolName: "Read", ToolUseID: "tool-1", Input: json.RawMessage(`{"file_path":"sample.txt"}`),
	}}
	frame := w.evaluateExternalPermission(context.Background(), first)
	if string(frame.Response.Response) == "" {
		t.Fatal("missing permission decision")
	}
	for _, input := range []string{`{"file_path":"sample.txt"}`, `{"file_path":"other.txt"}`} {
		next := first
		next.RequestID = "request-2"
		request := *first.Request
		request.Input = json.RawMessage(input)
		next.Request = &request
		decision := w.evaluateExternalPermission(context.Background(), next)
		var response externalclaude.PermissionDecision
		if err := json.Unmarshal(decision.Response.Response, &response); err != nil {
			t.Fatal(err)
		}
		if response.Behavior != externalclaude.PermissionBehaviorDeny {
			t.Fatal("tool identity reuse was approved")
		}
	}
	history := w.messageSnapshot()
	calls := 0
	for _, msg := range history {
		calls += len(msg.ToolCalls)
	}
	if calls != 1 {
		t.Fatalf("duplicate tool intent persisted: %d", calls)
	}
}

func TestExternalDeniedToolAlwaysHasTerminalHistory(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	configureExternalTestRuntime(t, a, "claude", `"*": deny`)
	w, err := a.newExternalWorker(externalWorkerConfig{
		agentDef: a.agentConfigs["external-worker"], instanceID: "external-1", taskID: "task-1",
		ctx: context.Background(), cancel: func() {},
	})
	if err != nil {
		t.Fatal(err)
	}
	w.evaluateExternalPermission(context.Background(), externalclaude.Event{RequestID: "req", Request: &externalclaude.ControlRequest{
		ToolName: "Write", ToolUseID: "call", Input: json.RawMessage(`{"file_path":"sample.txt","content":"Text."}`),
	}})
	history := w.messageSnapshot()
	if len(history) != 2 || history[1].Role != message.RoleTool || history[1].ToolStatus != message.ToolStatusError ||
		history[1].ToolRecoveryState != message.ToolRecoveryStateNotStarted {
		t.Fatalf("denied tool history = %+v", history)
	}
}

func TestExternalToolValidationErrorDoesNotFailTask(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	configureExternalTestRuntime(t, a, "claude", `"*": allow`)
	w, err := a.newExternalWorker(externalWorkerConfig{agentDef: a.agentConfigs["external-worker"], instanceID: "external-1"})
	if err != nil {
		t.Fatal(err)
	}
	w.handleExternalToolUse(externalclaude.ContentBlock{ID: "call", Name: "Edit", Input: json.RawMessage(`{"file_path":"sample.txt"}`)})
	w.handleExternalToolResult(externalclaude.ContentBlock{ToolUseID: "call", Content: json.RawMessage(`"Invalid arguments."`), IsError: true})
	if err := w.getResultError(); err != nil {
		t.Fatalf("tool validation failed the task: %v", err)
	}
	history := w.messageSnapshot()
	if len(history) != 2 || history[1].ToolRecoveryState != message.ToolRecoveryStateNotStarted {
		t.Fatalf("tool validation history = %+v", history)
	}
}

func TestExternalToolDenyIsIndependent(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	configureExternalTestRuntime(t, a, "claude", "\"*\": allow\nedit: deny")
	w, err := a.newExternalWorker(externalWorkerConfig{agentDef: a.agentConfigs["external-worker"], instanceID: "external-1"})
	if err != nil {
		t.Fatal(err)
	}
	got := w.derivedDisallowedTools()
	if len(got) != 1 || got[0] != "Edit" {
		t.Fatalf("disallowed = %v", got)
	}
}

func TestExternalQuestionAuthorizationRecordsEffectiveInput(t *testing.T) {
	a := newQuestionTestAgent(t)
	configureExternalTestRuntime(t, a, "claude", `"*": allow`)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	w, err := a.newExternalWorker(externalWorkerConfig{
		agentDef: a.agentConfigs["external-worker"], instanceID: "external-1", taskID: "task-1",
		ctx: ctx, cancel: cancel,
	})
	if err != nil {
		t.Fatal(err)
	}
	input := json.RawMessage(`{"questions":[{"header":"Choice","question":"Choose an option","options":[{"label":"Alpha","description":"First option"},{"label":"Beta","description":"Second option"}]}]}`)
	done := make(chan externalclaude.Frame, 1)
	go func() {
		done <- w.evaluateExternalPermission(ctx, externalclaude.Event{RequestID: "req", Request: &externalclaude.ControlRequest{
			ToolName: "AskUserQuestion", ToolUseID: "call", Input: input,
		}})
	}()
	answered := false
	for {
		select {
		case event := <-a.eventCh:
			a.dispatch(event)
			if !answered {
				for id, q := range a.questions.records {
					if q.pending() {
						receipt := applyTestQuestion(t, a, QuestionOperation{Operation: QuestionOpAnswer, QuestionID: id, Answers: []string{"0"}}, time.Time{})
						if !receipt.Accepted {
							t.Fatalf("answer rejected: %+v", receipt)
						}
						answered = true
						break
					}
				}
			}
		case frame := <-done:
			var decision externalclaude.PermissionDecision
			if err := json.Unmarshal(frame.Response.Response, &decision); err != nil {
				t.Fatal(err)
			}
			if !answered || decision.Behavior != externalclaude.PermissionBehaviorAllow ||
				!strings.Contains(string(decision.UpdatedInput), `"Choose an option":"Alpha"`) {
				t.Fatalf("question permission = %+v", decision)
			}
			data, err := os.ReadFile(filepath.Join(a.currentRecoveryOwnership().dir, recovery.ToolActivityFilename))
			if err != nil {
				t.Fatal(err)
			}
			var entry recovery.ToolActivityRecord
			if err := json.Unmarshal(data, &entry); err != nil {
				t.Fatal(err)
			}
			if entry.Audit == nil || entry.Audit.OriginalArgsJSON != string(input) ||
				entry.Audit.EffectiveArgsJSON != string(decision.UpdatedInput) {
				t.Fatalf("missing effective authorization arguments: %+v", entry)
			}
			return
		case <-ctx.Done():
			t.Fatal("external question did not settle")
		}
	}
}

func TestExternalQuestionMultipleAndCustomAnswers(t *testing.T) {
	a := newQuestionTestAgent(t)
	configureExternalTestRuntime(t, a, "claude", `"*": allow`)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	w, err := a.newExternalWorker(externalWorkerConfig{
		agentDef: a.agentConfigs["external-worker"], instanceID: "external-1", taskID: "task-1",
		ctx: ctx, cancel: cancel,
	})
	if err != nil {
		t.Fatal(err)
	}
	input := json.RawMessage(`{"questions":[{"header":"Options","question":"Choose options","multiSelect":true,"options":[{"label":"Alpha","description":"First"},{"label":"Beta","description":"Second"}]},{"header":"Text","question":"Provide a value","options":[{"label":"Default","description":"Default value"}]}]}`)
	done := make(chan externalclaude.Frame, 1)
	go func() {
		done <- w.evaluateExternalPermission(ctx, externalclaude.Event{RequestID: "req", Request: &externalclaude.ControlRequest{
			ToolName: "AskUserQuestion", ToolUseID: "call", Input: input,
		}})
	}()
	answered := false
	for {
		select {
		case event := <-a.eventCh:
			a.dispatch(event)
			if !answered && len(a.questions.records) == 2 {
				// Answer the second question first to verify that delivery
				// order does not replace the original question association.
				for _, header := range []string{"Text", "Options"} {
					for id, q := range a.questions.records {
						if q.Item.Header != header {
							continue
						}
						op := QuestionOperation{Operation: QuestionOpAnswer, QuestionID: id, Answers: []string{"1", "0"}}
						if header == "Text" {
							op.Answers = []string{"A custom value"}
							op.Custom = true
						}
						if receipt := applyTestQuestion(t, a, op, time.Time{}); !receipt.Accepted {
							t.Fatalf("answer rejected: %+v", receipt)
						}
					}
				}
				answered = true
			}
		case frame := <-done:
			var decision externalclaude.PermissionDecision
			if err := json.Unmarshal(frame.Response.Response, &decision); err != nil {
				t.Fatal(err)
			}
			var updated struct {
				Answers map[string]string `json:"answers"`
			}
			if err := json.Unmarshal(decision.UpdatedInput, &updated); err != nil {
				t.Fatal(err)
			}
			if !answered || decision.Behavior != externalclaude.PermissionBehaviorAllow ||
				updated.Answers["Choose options"] != "Beta, Alpha" || updated.Answers["Provide a value"] != "A custom value" {
				t.Fatalf("answers were not associated with their questions: %+v %+v", decision, updated)
			}
			data, err := os.ReadFile(filepath.Join(a.currentRecoveryOwnership().dir, recovery.ToolActivityFilename))
			if err != nil {
				t.Fatal(err)
			}
			var entry recovery.ToolActivityRecord
			if err := json.Unmarshal(data, &entry); err != nil {
				t.Fatal(err)
			}
			if entry.Audit == nil || entry.Audit.EffectiveArgsJSON != string(decision.UpdatedInput) {
				t.Fatal("answers were not durably recorded before authorization")
			}
			return
		case <-ctx.Done():
			t.Fatal("multiple external questions did not settle")
		}
	}
}

func TestExternalQuestionCancellationDoesNotAuthorize(t *testing.T) {
	a := newQuestionTestAgent(t)
	configureExternalTestRuntime(t, a, "claude", `"*": allow`)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	requestCtx, cancelRequest := context.WithCancel(ctx)
	defer cancelRequest()
	w, err := a.newExternalWorker(externalWorkerConfig{
		agentDef: a.agentConfigs["external-worker"], instanceID: "external-1", taskID: "task-1",
		ctx: ctx, cancel: cancel,
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan externalclaude.Frame, 1)
	go func() {
		done <- w.evaluateExternalPermission(requestCtx, externalclaude.Event{RequestID: "req", Request: &externalclaude.ControlRequest{
			ToolName: "AskUserQuestion", ToolUseID: "call",
			Input: json.RawMessage(`{"questions":[{"header":"Choice","question":"Choose an option","options":[{"label":"Alpha","description":"First"}]}]}`),
		}})
	}()
	cancelled := false
	for {
		select {
		case event := <-a.eventCh:
			a.dispatch(event)
			if !cancelled && len(a.questions.records) == 1 {
				cancelRequest()
				cancelled = true
			}
		case frame := <-done:
			var decision externalclaude.PermissionDecision
			if err := json.Unmarshal(frame.Response.Response, &decision); err != nil {
				t.Fatal(err)
			}
			if !cancelled || decision.Behavior != externalclaude.PermissionBehaviorDeny {
				t.Fatalf("cancelled question was authorized: %+v", decision)
			}
			journal, err := a.recoveryManager().LoadToolActivity()
			if err != nil || len(journal) != 0 {
				t.Fatalf("cancelled question entered started journal: %+v %v", journal, err)
			}
			history := w.messageSnapshot()
			if len(history) != 2 || history[1].ToolStatus != message.ToolStatusError ||
				history[1].ToolRecoveryState != message.ToolRecoveryStateNotStarted {
				t.Fatalf("cancelled question did not settle not_started: %+v", history)
			}
			for _, q := range a.questions.records {
				if q.Outcome == tools.QuestionOutcomeAnswered {
					t.Fatal("cancellation fabricated an answer")
				}
			}
			return
		case <-ctx.Done():
			t.Fatal("cancelled external question did not settle")
		}
	}
}

func TestExternalPermissionPersistenceFailuresFailClosed(t *testing.T) {
	for _, stage := range []string{"intent", "started", "result"} {
		t.Run(stage, func(t *testing.T) {
			a := newTestMainAgent(t, t.TempDir())
			configureExternalTestRuntime(t, a, "claude", `"*": allow`)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			w, err := a.newExternalWorker(externalWorkerConfig{
				agentDef: a.agentConfigs["external-worker"], instanceID: "external-1", taskID: "task-1",
				ctx: ctx, cancel: cancel,
			})
			if err != nil {
				t.Fatal(err)
			}
			input := json.RawMessage(`{"file_path":"sample.txt"}`)
			if stage != "intent" {
				w.handleExternalToolUse(externalclaude.ContentBlock{ID: "call", Name: "Read", Input: input})
			}
			var blocked string
			switch stage {
			case "intent":
				blocked = filepath.Join(a.currentRecoveryOwnership().dir, "agents", w.instanceID+".jsonl")
			case "started":
				blocked = filepath.Join(a.currentRecoveryOwnership().dir, recovery.ToolActivityFilename)
			case "result":
				if err := a.recoveryManager().Close(); err != nil {
					t.Fatal(err)
				}
			}
			if blocked != "" {
				if err := os.MkdirAll(blocked, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if stage == "result" {
				w.toolBindings["call"].started = true
				w.handleExternalToolResult(externalclaude.ContentBlock{ToolUseID: "call", Content: json.RawMessage(`"Contents."`)})
			} else {
				frame := w.evaluateExternalPermission(ctx, externalclaude.Event{RequestID: "req", Request: &externalclaude.ControlRequest{
					ToolName: "Read", ToolUseID: "call", Input: input,
				}})
				var decision externalclaude.PermissionDecision
				if err := json.Unmarshal(frame.Response.Response, &decision); err != nil {
					t.Fatal(err)
				}
				if decision.Behavior != externalclaude.PermissionBehaviorDeny || w.toolBindings["call"].started {
					t.Fatalf("failed %s persistence authorized execution: %+v", stage, decision)
				}
			}
			if w.PersistenceHealth().State == PersistenceHealthy || w.getResultError() == nil {
				t.Fatalf("%s persistence failure was not surfaced", stage)
			}
			history := w.messageSnapshot()
			if stage == "result" && len(history) != 1 {
				t.Fatal("failed result was added to durable live history")
			}
			if stage == "started" && (len(history) != 2 || history[1].ToolRecoveryState != message.ToolRecoveryStateNotStarted) {
				t.Fatalf("failed authorization did not settle not_started: %+v", history)
			}
			terminal := false
			for len(a.outputCh) > 0 {
				if event, ok := (<-a.outputCh).(ToolResultEvent); ok {
					terminal = true
					if event.Status != message.ToolResultStatus(message.ToolStatusError) {
						t.Fatalf("failed persistence emitted success: %+v", event)
					}
				}
			}
			if stage != "intent" && !terminal {
				t.Fatal("started tool card did not reach an error terminal state")
			}
		})
	}
}

func TestExternalConcurrentPermissionCancellation(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	configureExternalTestRuntime(t, a, "claude", `"*": ask`)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	w, err := a.newExternalWorker(externalWorkerConfig{
		agentDef: a.agentConfigs["external-worker"], instanceID: "external-1", taskID: "task-1",
		ctx: ctx, cancel: cancel,
	})
	if err != nil {
		t.Fatal(err)
	}
	evaluate := func(ctx context.Context, id string) <-chan externalclaude.Frame {
		done := make(chan externalclaude.Frame, 1)
		go func() {
			done <- w.evaluateExternalPermission(ctx, externalclaude.Event{RequestID: id, Request: &externalclaude.ControlRequest{
				ToolName: "Read", ToolUseID: id, Input: json.RawMessage(`{"file_path":"sample.txt"}`),
			}})
		}()
		return done
	}
	first := evaluate(ctx, "first")
	var confirm ConfirmRequestEvent
	for confirm.RequestID == "" {
		select {
		case event := <-a.outputCh:
			if request, ok := event.(ConfirmRequestEvent); ok {
				confirm = request
			}
		case <-ctx.Done():
			t.Fatal("first permission did not request confirmation")
		}
	}
	secondCtx, cancelSecond := context.WithCancel(ctx)
	defer cancelSecond()
	second := evaluate(secondCtx, "second")
	secondStarted := false
	for !secondStarted {
		select {
		case event := <-a.outputCh:
			switch e := event.(type) {
			case ConfirmRequestEvent:
				t.Fatalf("concurrent modal was displayed: %+v", e)
			case ToolCallStartEvent:
				secondStarted = e.ID == "second"
			}
		case <-ctx.Done():
			t.Fatal("second permission did not persist its intent")
		}
	}
	cancelSecond()
	select {
	case frame := <-second:
		if !strings.Contains(string(frame.Response.Response), `"behavior":"deny"`) {
			t.Fatal("cancelled queued permission was allowed")
		}
	case <-ctx.Done():
		t.Fatal("queued cancellation blocked")
	}
	a.ResolveConfirm("allow", confirm.ArgsJSON, "", "", confirm.RequestID)
	select {
	case frame := <-first:
		if !strings.Contains(string(frame.Response.Response), `"behavior":"allow"`) {
			t.Fatal("first permission was not approved")
		}
	case <-ctx.Done():
		t.Fatal("first permission did not settle")
	}
	journal, err := a.recoveryManager().LoadToolActivity()
	if err != nil {
		t.Fatal(err)
	}
	_, firstStarted := journal[recovery.ToolActivityKey{AgentID: w.instanceID, CallID: "first"}]
	if len(journal) != 1 || !firstStarted {
		t.Fatalf("cancelled permission entered started journal: %+v", journal)
	}
	for len(a.outputCh) > 0 {
		if event, ok := (<-a.outputCh).(ConfirmRequestEvent); ok {
			t.Fatalf("cancelled queued modal was displayed: %+v", event)
		}
	}
}
