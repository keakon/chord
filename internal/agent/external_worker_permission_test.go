package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
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
	// Agent and ExitPlanMode have no Chord equivalent, so evaluateExternalPermission
	// only authorizes them through a rule naming the Claude tool. Without one the
	// call can never succeed and the tool must not stay visible.
	for _, want := range []string{"Edit", "Agent", "ExitPlanMode"} {
		if !slices.Contains(got, want) {
			t.Fatalf("disallowed = %v, want %s", got, want)
		}
	}
	for _, kept := range []string{"Read", "Write", "Bash", "AskUserQuestion"} {
		if slices.Contains(got, kept) {
			t.Fatalf("disallowed = %v, must keep %s", got, kept)
		}
	}
}

func TestExternalNamedRuleKeepsUnmappedToolVisible(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	configureExternalTestRuntime(t, a, "claude", "\"*\": deny\nAgent: allow")
	w, err := a.newExternalWorker(externalWorkerConfig{agentDef: a.agentConfigs["external-worker"], instanceID: "external-1"})
	if err != nil {
		t.Fatal(err)
	}
	if got := w.derivedDisallowedTools(); slices.Contains(got, "Agent") {
		t.Fatalf("a rule naming Agent must keep it visible: %v", got)
	}
}

func TestExternalPermissionOutcomeCacheIsBounded(t *testing.T) {
	outcomes := make(map[string]*externalPermissionOutcome)
	for i := 0; i <= externalPermissionOutcomeLimit+5; i++ {
		outcomes[strconv.Itoa(i)] = &externalPermissionOutcome{done: true}
	}
	pending := &externalPermissionOutcome{}
	outcomes["pending"] = pending
	pruneExternalPermissionOutcomesLocked(outcomes)
	if len(outcomes) > externalPermissionOutcomeLimit {
		t.Fatalf("cache = %d entries, want at most %d", len(outcomes), externalPermissionOutcomeLimit)
	}
	if outcomes["pending"] != pending {
		t.Fatal("an unsettled outcome must never be pruned")
	}
}

func TestExternalTaskAliasUsesAgentRules(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	configureExternalTestRuntime(t, a, "claude", "\"*\": deny\nAgent: allow")
	w, err := a.newExternalWorker(externalWorkerConfig{
		agentDef: a.agentConfigs["external-worker"], instanceID: "external-1", taskID: "task-1",
		ctx: context.Background(), cancel: func() {},
	})
	if err != nil {
		t.Fatal(err)
	}
	frame := w.evaluateExternalPermission(context.Background(), externalclaude.Event{RequestID: "req", Request: &externalclaude.ControlRequest{
		ToolName: "Task", ToolUseID: "agent-call",
		Input: json.RawMessage(`{"description":"inspect","prompt":"Inspect the fixture.","subagent_type":"Explore"}`),
	}})
	var decision externalclaude.PermissionDecision
	if err := json.Unmarshal(frame.Response.Response, &decision); err != nil {
		t.Fatal(err)
	}
	if decision.Behavior != externalclaude.PermissionBehaviorAllow {
		t.Fatalf("Task alias did not use the Agent rule: %+v", decision)
	}
}

func TestExternalNotebookEditUsesPathRules(t *testing.T) {
	root := t.TempDir()
	a := newTestMainAgent(t, root)
	configureExternalTestRuntime(t, a, "claude", "\"*\": deny\nNotebookEdit:\n  \"notes/*.ipynb\": allow")
	w, err := a.newExternalWorker(externalWorkerConfig{
		agentDef: a.agentConfigs["external-worker"], instanceID: "external-1", taskID: "task-1",
		workDir: root, ctx: context.Background(), cancel: func() {},
	})
	if err != nil {
		t.Fatal(err)
	}
	evaluate := func(id, path string) externalclaude.PermissionDecision {
		t.Helper()
		frame := w.evaluateExternalPermission(context.Background(), externalclaude.Event{RequestID: id, Request: &externalclaude.ControlRequest{
			ToolName: "NotebookEdit", ToolUseID: id,
			Input: json.RawMessage(`{"notebook_path":` + strconv.Quote(path) + `,"cell_id":"cell-1","new_source":"print(1)"}`),
		}})
		var decision externalclaude.PermissionDecision
		if err := json.Unmarshal(frame.Response.Response, &decision); err != nil {
			t.Fatal(err)
		}
		return decision
	}
	if decision := evaluate("allowed", filepath.Join(root, "notes", "a.ipynb")); decision.Behavior != externalclaude.PermissionBehaviorAllow {
		t.Fatalf("notebook inside the allowed path was not approved: %+v", decision)
	}
	if decision := evaluate("denied", filepath.Join(root, "other.ipynb")); decision.Behavior != externalclaude.PermissionBehaviorDeny {
		t.Fatalf("notebook outside the allowed path was approved: %+v", decision)
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
	input := json.RawMessage(`{"questions":[
		{"header":"Options","question":"Choose options","multiSelect":true,"options":[{"label":"Alpha","description":"First"},{"label":"Beta","description":"Second"}]},
		{"header":"Text","question":"Provide a value","options":[{"label":"Default","description":"Default value"}]},
		{"header":"Level","question":"Pick a level","options":[{"label":"Low","description":"Low"},{"label":"High","description":"High"}]},
		{"header":"Notes","question":"Add notes","options":[{"label":"Default","description":"Default notes"}]}
	]}`)
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
			if !answered && len(a.questions.records) == 4 {
				// Answer in reverse order to verify that delivery order does not
				// replace the original question association.
				for _, header := range []string{"Notes", "Level", "Text", "Options"} {
					for id, q := range a.questions.records {
						if q.Item.Header != header {
							continue
						}
						op := QuestionOperation{Operation: QuestionOpAnswer, QuestionID: id, Answers: []string{"1"}}
						if header == "Options" {
							op.Answers = []string{"1", "0"}
						}
						if header == "Text" {
							op.Answers = []string{"A custom value"}
							op.Custom = true
						}
						if header == "Notes" {
							op.Answers = []string{"Extra notes"}
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
				updated.Answers["Choose options"] != "Beta, Alpha" || updated.Answers["Provide a value"] != "A custom value" ||
				updated.Answers["Pick a level"] != "High" || updated.Answers["Add notes"] != "Extra notes" ||
				len(updated.Answers) != 4 {
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

func TestExternalCancelledPermissionReplay(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	script := writeFakeClaude(t, `
IFS= read -r decision
printf '%s\n' "$decision"
`)
	configureExternalTestRuntime(t, a, script, `"*": ask`)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, err := externalclaude.Start(externalclaude.ProcessConfig{Command: script, KillGrace: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		session.Stop(20 * time.Millisecond)
		for range session.Events() {
		}
		_ = session.Wait()
	})
	if _, err := session.Initialize(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if err := session.WriteUserMessage(ctx, "Fixture."); err != nil {
		t.Fatal(err)
	}
	w, err := a.newExternalWorker(externalWorkerConfig{
		agentDef: a.agentConfigs["external-worker"], instanceID: "external-1", taskID: "task-1",
		ctx: ctx, cancel: cancel,
	})
	if err != nil {
		t.Fatal(err)
	}
	w.setSession(session)
	event := externalclaude.Event{RequestID: "request-1", Request: &externalclaude.ControlRequest{
		ToolName: "Write", ToolUseID: "call-1", Input: json.RawMessage(`{"file_path":"sample.txt","content":"Text."}`),
	}}
	w.beginExternalPermission(event)
	var confirm ConfirmRequestEvent
	for confirm.RequestID == "" {
		select {
		case out := <-a.outputCh:
			if c, ok := out.(ConfirmRequestEvent); ok {
				confirm = c
			}
		case <-ctx.Done():
			t.Fatal("confirmation did not arrive")
		}
	}
	// Hold snapshot publication after the pre-authorization context check.
	w.toolMu.Lock()
	locked := true
	defer func() {
		if locked {
			w.toolMu.Unlock()
		}
	}()
	a.ResolveConfirm("allow", confirm.ArgsJSON, "", "", confirm.RequestID)
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if !w.externalToolStateMu.TryLock() {
				goto authorizing
			}
			w.externalToolStateMu.Unlock()
		case <-ctx.Done():
			t.Fatal("authorization did not reach the final section")
		}
	}
authorizing:
	w.cancelExternalPermissions(event.RequestID)
	w.toolMu.Unlock()
	locked = false
	settled := make(chan struct{})
	go func() { w.permissionWG.Wait(); close(settled) }()
	select {
	case <-settled:
	case <-ctx.Done():
		t.Fatal("cancelled authorization did not settle")
	}
	w.beginExternalPermission(event)
	select {
	case replay := <-session.Events():
		if replay.Response == nil {
			t.Fatalf("unexpected replay: %+v", replay)
		}
		var decision externalclaude.PermissionDecision
		if err := json.Unmarshal(replay.Response.Response, &decision); err != nil {
			t.Fatal(err)
		}
		if decision.Behavior != externalclaude.PermissionBehaviorDeny {
			t.Fatalf("cancelled request replay authorized execution: %+v", decision)
		}
	case <-ctx.Done():
		t.Fatal("replay did not arrive")
	}
}
