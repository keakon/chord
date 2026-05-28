package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestNextLoopAssessmentFromAssistantMarksCompletedOnStop(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.enable()
	a.loopState.markProgress()
	a.loopState.markProgress()

	assessment := a.nextLoopAssessmentFromAssistant(message.Message{
		Role:       "assistant",
		Content:    "implemented and verified",
		StopReason: "stop",
	})
	if assessment == nil {
		t.Fatal("assessment = nil, want continue assessment")
	}
	if assessment.Action != LoopAssessmentActionContinue {
		t.Fatalf("assessment.Action = %q, want %q", assessment.Action, LoopAssessmentActionContinue)
	}
	if assessment.Message != "Loop continuing: end this round with a `Done` tool call to request loop exit." {
		t.Fatalf("assessment.Message = %q, want missing-Done guidance", assessment.Message)
	}
	if a.loopState.State != LoopStateAssessing {
		t.Fatalf("loopState.State = %q, want %q", a.loopState.State, LoopStateAssessing)
	}
	found := false
	for _, reason := range assessment.Reasons {
		if reason == "missing_done_tool" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("assessment.Reasons = %v, want missing_done_tool", assessment.Reasons)
	}
}

func TestNextLoopAssessmentFromAssistantRequiresDoneToolWhenEnabled(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.enable()
	a.loopState.markProgress()
	a.tools.Register(tools.NewDoneTool())
	a.activeConfig = &config.AgentConfig{
		Permission: parsePermissionNode(t, `
"*": deny
Done: allow
`),
	}
	a.rebuildRuleset()

	assessment := a.nextLoopAssessmentFromAssistant(message.Message{
		Role:       "assistant",
		Content:    "finished",
		StopReason: "stop",
	})
	if assessment == nil {
		t.Fatal("assessment = nil, want continue assessment")
	}
	if assessment.Action != LoopAssessmentActionContinue {
		t.Fatalf("assessment.Action = %q, want %q", assessment.Action, LoopAssessmentActionContinue)
	}
	if !strings.Contains(assessment.Message, "Done") {
		t.Fatalf("assessment.Message = %q, want Done guard", assessment.Message)
	}
	found := false
	for _, reason := range assessment.Reasons {
		if reason == "missing_done_tool" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("assessment.Reasons = %v, want missing_done_tool", assessment.Reasons)
	}
}

func TestNextLoopAssessmentFromAssistantAcceptsDoneToolWhenEnabled(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.enable()
	a.loopState.markProgress()
	a.loopState.markProgress()
	a.tools.Register(tools.NewDoneTool())
	a.activeConfig = &config.AgentConfig{
		Permission: parsePermissionNode(t, `
"*": deny
Done: allow
`),
	}
	a.rebuildRuleset()

	assessment := a.nextLoopAssessmentFromAssistant(message.Message{
		Role:       "assistant",
		Content:    "all good",
		StopReason: "stop",
	})
	if assessment == nil {
		t.Fatal("assessment = nil, want continue assessment")
	}
	if assessment.Action != LoopAssessmentActionContinue {
		t.Fatalf("assessment.Action = %q, want %q", assessment.Action, LoopAssessmentActionContinue)
	}
	if !strings.Contains(assessment.Message, "Done") {
		t.Fatalf("assessment.Message = %q, want missing-Done guidance", assessment.Message)
	}
}

func TestHandleToolResult_DoneInLoopRequestsConfirmationAndDisablesLoopOnApproval(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.tools.Register(tools.NewDoneTool())
	a.activeConfig = &config.AgentConfig{
		Permission: parsePermissionNode(t, `
"*": deny
Done: allow
`),
	}
	a.rebuildRuleset()
	a.loopState.enableWithTarget("finish current task")
	a.loopState.markProgress()
	a.newTurn()
	turn := a.turn
	callID := "done-confirm-1"
	report := "**Completion status**: done\n\n**Verification**: tested"
	a.ctxMgr.Append(message.Message{
		Role:    "assistant",
		Content: "completed and verified",
		ToolCalls: []message.ToolCall{{
			ID:   callID,
			Name: tools.NameDone,
			Args: json.RawMessage(`{"report":"**Completion status**: done\n\n**Verification**: tested"}`),
		}},
	})
	turn.PendingToolCalls.Store(1)

	handled := make(chan struct{})
	go func() {
		a.handleToolResult(Event{TurnID: turn.ID, Payload: &ToolResultPayload{
			CallID:   callID,
			Name:     tools.NameDone,
			ArgsJSON: `{"report":"**Completion status**: done\n\n**Verification**: tested"}`,
			Result:   "completed and verified",
			TurnID:   turn.ID,
		}})
		close(handled)
	}()

	deadline := time.After(2 * time.Second)
	confirmed := false
	for !confirmed {
		select {
		case evt := <-a.outputCh:
			switch payload := evt.(type) {
			case ConfirmRequestEvent:
				if payload.ToolName != tools.NameDone {
					t.Fatalf("ConfirmRequestEvent.ToolName = %q, want %q", payload.ToolName, tools.NameDone)
				}
				confirmed = true
				a.ResolveConfirm("allow", payload.ArgsJSON, "", "", payload.RequestID)
			}
		case <-deadline:
			t.Fatal("timed out waiting for Done confirmation request")
		}
	}

	select {
	case <-handled:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Done tool result handling to finish")
	}
	var doneResult *ToolResultEvent
	for len(a.outputCh) > 0 {
		evt := <-a.outputCh
		if payload, ok := evt.(ToolResultEvent); ok && payload.CallID == callID && payload.Name == tools.NameDone {
			copy := payload
			doneResult = &copy
		}
	}
	if doneResult == nil {
		t.Fatal("expected Done ToolResultEvent after approving Done confirmation")
	}
	if doneResult.Result != "Done approved" {
		t.Fatalf("Done ToolResultEvent.Result = %q, want Done approved", doneResult.Result)
	}
	if doneResult.DoneReport != report {
		t.Fatalf("Done ToolResultEvent.DoneReport = %q, want %q", doneResult.DoneReport, report)
	}
	if a.loopState.Enabled {
		t.Fatal("loop should be disabled after approving Done confirmation")
	}
	if a.turn != nil {
		t.Fatal("turn should be cleared after approving Done confirmation")
	}
}

func TestRepeatedToolCallInLoopAutoRejectsOnThirdAndFourthAttempt(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.enableWithTarget("finish current task")
	a.tools.Register(tools.ReadTool{})
	a.activeConfig = &config.AgentConfig{
		Permission: parsePermissionNode(t, `
"*": deny
Read: allow
`),
	}
	a.rebuildRuleset()
	a.newTurn()

	call := message.ToolCall{ID: "read-repeat-1", Name: tools.NameRead, Args: json.RawMessage(`{"path":"README.md","limit":10,"offset":0}`)}

	for i := 0; i < 2; i++ {
		result, err := a.executeToolCall(context.Background(), call)
		if err == nil {
			t.Fatalf("attempt %d err = nil, want file-not-found or execution error before interception", i+1)
		}
		if result.Result != "" {
			t.Fatalf("attempt %d result = %q, want empty result before interception", i+1, result.Result)
		}
	}

	result3, err := a.executeToolCall(context.Background(), call)
	if err != nil {
		t.Fatalf("third attempt err = %v, want nil auto-reject result", err)
	}
	if !strings.Contains(result3.Result, "Tool call rejected automatically") {
		t.Fatalf("third attempt result = %q, want automatic rejection", result3.Result)
	}
	if !strings.Contains(result3.Result, "detected 3 consecutive identical `Read` tool calls") {
		t.Fatalf("third attempt result = %q, want repeated-call explanation", result3.Result)
	}
	if a.loopState.Iteration != 1 {
		t.Fatalf("loop iteration after third attempt = %d, want 1", a.loopState.Iteration)
	}

	result4, err := a.executeToolCall(context.Background(), call)
	if err != nil {
		t.Fatalf("fourth attempt err = %v, want nil auto-reject result", err)
	}
	if !strings.Contains(result4.Result, "Tool call rejected automatically") {
		t.Fatalf("fourth attempt result = %q, want automatic rejection", result4.Result)
	}
	if a.loopState.Iteration != 2 {
		t.Fatalf("loop iteration after fourth attempt = %d, want 2", a.loopState.Iteration)
	}
}

func TestRepeatedToolCallInLoopRequiresConfirmationAtIterationLimit(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.enableWithTarget("finish current task")
	a.loopState.MaxIterations = 1
	a.loopState.MaxIterationsSet = true
	a.loopState.Iteration = 1
	a.newTurn()
	a.confirmFn = func(ctx context.Context, toolName string, args string, needsApproval []string, alreadyAllowed []string) (ConfirmResponse, error) {
		return a.AwaitConfirm(ctx, toolName, args, 0, needsApproval, alreadyAllowed)
	}

	call := message.ToolCall{ID: "read-repeat-confirm", Name: tools.NameRead, Args: json.RawMessage(`{"path":"README.md","limit":10,"offset":0}`)}
	for i := 0; i < 2; i++ {
		if _, ok := a.maybeInterceptRepeatedToolCall(context.Background(), call); ok {
			t.Fatalf("attempt %d intercepted early, want no intercept before third repeated call", i+1)
		}
	}

	type confirmPayload struct {
		Reason string `json:"reason"`
		Report string `json:"report"`
	}
	handled := make(chan struct{})
	var gotResult ToolExecutionResult
	var gotErr error
	go func() {
		gotResult, gotErr = a.executeToolCall(context.Background(), call)
		close(handled)
	}()

	deadline := time.After(2 * time.Second)
	confirmed := false
	for !confirmed {
		select {
		case evt := <-a.outputCh:
			req, ok := evt.(ConfirmRequestEvent)
			if !ok {
				continue
			}
			if req.ToolName != tools.NameDone {
				t.Fatalf("confirm tool = %q, want %q", req.ToolName, tools.NameDone)
			}
			var payload confirmPayload
			if err := json.Unmarshal([]byte(req.ArgsJSON), &payload); err != nil {
				t.Fatalf("unmarshal confirm args: %v", err)
			}
			if !strings.Contains(payload.Reason, "automatic loop interception limit reached (1)") {
				t.Fatalf("confirm reason = %q, want iteration-limit explanation", payload.Reason)
			}
			if !strings.Contains(payload.Report, "Tool call rejected automatically") {
				t.Fatalf("confirm report = %q, want rejection guidance", payload.Report)
			}
			if !req.ForceDenyReason {
				t.Fatal("confirmation should require denial with a reason")
			}
			if strings.Contains(payload.Reason, "Approve") || strings.Contains(payload.Reason, "continue") {
				t.Fatalf("confirm reason = %q, should not offer approve/continue", payload.Reason)
			}
			confirmed = true
			a.ResolveConfirm("deny", req.ArgsJSON, "", "I am stuck repeating the same read; inspect previous output instead.", req.RequestID)
		case <-deadline:
			t.Fatal("timed out waiting for repeated-tool-call confirmation request")
		}
	}

	select {
	case <-handled:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for repeated-tool-call handling to finish")
	}
	if gotErr != nil {
		t.Fatalf("executeToolCall err = %v, want nil after deny/continue", gotErr)
	}
	if !strings.Contains(gotResult.Result, "Tool call rejected automatically") {
		t.Fatalf("executeToolCall result = %q, want auto rejection guidance", gotResult.Result)
	}
	if a.loopState.Iteration != 0 {
		t.Fatalf("loop iteration after deny/continue = %d, want 0 reset", a.loopState.Iteration)
	}
	if len(a.loopState.RepeatedToolCallStreak) != 0 {
		t.Fatalf("repeated tool call streak after deny/continue len = %d, want 0 reset", len(a.loopState.RepeatedToolCallStreak))
	}
	if _, ok := a.maybeInterceptRepeatedToolCall(context.Background(), call); ok {
		t.Fatal("next identical tool call after deny/continue intercepted immediately, want fresh streak")
	}
}

func TestRepeatedToolCallInLoopRejectsApprovalAtIterationLimit(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.enableWithTarget("finish current task")
	a.loopState.MaxIterations = 1
	a.loopState.MaxIterationsSet = true
	a.loopState.Iteration = 1
	a.newTurn()
	a.confirmFn = func(ctx context.Context, toolName string, args string, needsApproval []string, alreadyAllowed []string) (ConfirmResponse, error) {
		return a.AwaitConfirm(ctx, toolName, args, 0, needsApproval, alreadyAllowed)
	}

	call := message.ToolCall{ID: "read-repeat-confirm-approve", Name: tools.NameRead, Args: json.RawMessage(`{"path":"README.md","limit":10,"offset":0}`)}
	for i := 0; i < 2; i++ {
		if _, ok := a.maybeInterceptRepeatedToolCall(context.Background(), call); ok {
			t.Fatalf("attempt %d intercepted early, want no intercept before third repeated call", i+1)
		}
	}

	handled := make(chan struct{})
	var gotResult ToolExecutionResult
	var gotErr error
	go func() {
		gotResult, gotErr = a.executeToolCall(context.Background(), call)
		close(handled)
	}()

	deadline := time.After(2 * time.Second)
	confirmed := false
	for !confirmed {
		select {
		case evt := <-a.outputCh:
			req, ok := evt.(ConfirmRequestEvent)
			if !ok {
				continue
			}
			if !req.ForceDenyReason {
				t.Fatal("confirmation should require denial with a reason")
			}
			confirmed = true
			a.ResolveConfirm("allow", req.ArgsJSON, "", "", req.RequestID)
		case <-deadline:
			t.Fatal("timed out waiting for repeated-tool-call confirmation request")
		}
	}

	select {
	case <-handled:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for repeated-tool-call handling to finish")
	}
	if gotErr == nil {
		t.Fatal("executeToolCall err = nil, want approval rejected")
	}
	if !strings.Contains(strings.ToLower(gotErr.Error()), "rejected by user") {
		t.Fatalf("executeToolCall err = %v, want rejection error", gotErr)
	}
	if gotResult.Result != "" {
		t.Fatalf("executeToolCall result = %q, want empty result after invalid approval", gotResult.Result)
	}
	if a.loopState.Iteration != 1 {
		t.Fatalf("loop iteration after invalid approval = %d, want unchanged", a.loopState.Iteration)
	}
	if len(a.loopState.RepeatedToolCallStreak) != repeatedToolCallInterceptThreshold {
		t.Fatalf("repeated tool call streak after invalid approval len = %d, want retained threshold", len(a.loopState.RepeatedToolCallStreak))
	}
}

func TestHandleToolResult_DoneOutsideLoopEntersIdle(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.tools.Register(tools.NewDoneTool())
	a.activeConfig = &config.AgentConfig{
		Permission: parsePermissionNode(t, `
"*": deny
Done: ask
`),
	}
	a.rebuildRuleset()
	a.newTurn()
	turn := a.turn
	callID := "done-nonloop-ask-1"
	a.ctxMgr.Append(message.Message{
		Role:    "assistant",
		Content: "task complete",
		ToolCalls: []message.ToolCall{{
			ID:   callID,
			Name: tools.NameDone,
			Args: json.RawMessage(`{"report":"**Completion status**: done\n**What changed**: validated\n**Verification**: tested\n**Remaining issues**: none"}`),
		}},
	})
	turn.PendingToolCalls.Store(1)

	handled := make(chan struct{})
	go func() {
		a.handleToolResult(Event{TurnID: turn.ID, Payload: &ToolResultPayload{
			CallID:   callID,
			Name:     tools.NameDone,
			ArgsJSON: `{"report":"**Completion status**: done\n**What changed**: validated\n**Verification**: tested\n**Remaining issues**: none"}`,
			Result:   "Done requested: report received (104 chars)",
			TurnID:   turn.ID,
		}})
		close(handled)
	}()

	deadline := time.After(2 * time.Second)
	var sawToolResult bool
	for !sawToolResult {
		select {
		case evt := <-a.outputCh:
			switch payload := evt.(type) {
			case ToolResultEvent:
				if payload.Name != tools.NameDone {
					continue
				}
				sawToolResult = true
			case RequestCycleStartedEvent:
				continue
			}
		case <-deadline:
			t.Fatal("timed out waiting for non-loop Done tool result")
		}
	}

	select {
	case <-handled:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for non-loop Done handling to finish")
	}
	if a.turn != nil {
		t.Fatal("non-loop Done should clear the current turn and enter idle")
	}
	select {
	case <-a.done:
		t.Fatal("non-loop Done must not shut down the agent runtime")
	default:
	}
}

func TestHandleToolResult_DoneOutsideLoopDenyContinuesWork(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.tools.Register(tools.NewDoneTool())
	a.activeConfig = &config.AgentConfig{
		Permission: parsePermissionNode(t, `
"*": deny
Done: allow
`),
	}
	a.rebuildRuleset()
	a.newTurn()
	turn := a.turn
	callID := "done-nonloop-deny-1"
	a.ctxMgr.Append(message.Message{
		Role:    "assistant",
		Content: "task complete",
		ToolCalls: []message.ToolCall{{
			ID:   callID,
			Name: tools.NameDone,
			Args: json.RawMessage(`{"report":"**Completion status**: done\n**What changed**: validated\n**Verification**: tested\n**Remaining issues**: none"}`),
		}},
	})
	turn.PendingToolCalls.Store(1)

	handled := make(chan struct{})
	go func() {
		a.handleToolResult(Event{TurnID: turn.ID, Payload: &ToolResultPayload{
			CallID:   callID,
			Name:     tools.NameDone,
			ArgsJSON: `{"report":"**Completion status**: done\n**What changed**: validated\n**Verification**: tested\n**Remaining issues**: none"}`,
			Result:   "Done requested: report received (104 chars)",
			TurnID:   turn.ID,
		}})
		close(handled)
	}()

	deadline := time.After(2 * time.Second)
	var sawToolResult bool
	for !sawToolResult {
		select {
		case evt := <-a.outputCh:
			switch payload := evt.(type) {
			case ToolResultEvent:
				if payload.Name != tools.NameDone {
					continue
				}
				sawToolResult = true
			case RequestCycleStartedEvent:
				continue
			}
		case <-deadline:
			t.Fatal("timed out waiting for non-loop Done tool result")
		}
	}

	select {
	case <-handled:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for non-loop Done denial handling to finish")
	}
	if a.turn != nil {
		t.Fatal("non-loop Done should clear the current turn and enter idle")
	}
	select {
	case <-a.done:
		t.Fatal("non-loop Done must not shut down the agent runtime")
	default:
	}
}

func TestHandleToolResult_DoneInLoopEmitsVisibleRejectionWhenExitConditionsFail(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.tools.Register(tools.NewDoneTool())
	a.activeConfig = &config.AgentConfig{
		Permission: parsePermissionNode(t, `
"*": deny
Done: allow
`),
	}
	a.rebuildRuleset()
	a.loopState.enableWithTarget("finish current task")
	a.loopState.markProgress()
	a.todoItems = []tools.TodoItem{{ID: "1", Content: "unfinished cleanup", Status: "pending"}}
	a.newTurn()
	turn := a.turn
	callID := "done-reject-1"
	a.ctxMgr.Append(message.Message{
		Role:    "assistant",
		Content: "everything looks done",
		ToolCalls: []message.ToolCall{{
			ID:   callID,
			Name: tools.NameDone,
			Args: json.RawMessage(`{"reason":"everything looks done"}`),
		}},
	})
	turn.PendingToolCalls.Store(1)

	handled := make(chan struct{})
	go func() {
		a.handleToolResult(Event{TurnID: turn.ID, Payload: &ToolResultPayload{
			CallID:   callID,
			Name:     tools.NameDone,
			ArgsJSON: `{"reason":"everything looks done"}`,
			Result:   "everything looks done",
			TurnID:   turn.ID,
		}})
		close(handled)
	}()

	deadline := time.After(2 * time.Second)
	for {
		select {
		case evt := <-a.outputCh:
			switch payload := evt.(type) {
			case ConfirmRequestEvent:
				t.Fatal("unexpected Done confirmation request when loop exit conditions are not satisfied")
			case LoopNoticeEvent:
				if payload.Title != "LOOP CONTINUE" {
					continue
				}
				t.Fatal("unexpected LoopNoticeEvent for Done tool-call continuation; runtime must not inject user continuation after tool calls")
			case ToolResultEvent:
				if payload.CallID != callID || payload.Name != tools.NameDone {
					continue
				}
				if !strings.Contains(payload.Result, "Done rejected automatically: loop exit conditions are not satisfied yet") {
					continue
				}
				select {
				case <-handled:
				case <-time.After(2 * time.Second):
					t.Fatal("timed out waiting for Done rejection handling to finish")
				}
				if a.loopState.Iteration != 1 {
					t.Fatalf("loop iteration = %d, want 1 after automatic Done rejection", a.loopState.Iteration)
				}
				msgs := a.ctxMgr.Snapshot()
				for _, msg := range msgs {
					if msg.Role == "user" && msg.Kind == "loop_notice" && msg.Content == payload.Result {
						t.Fatalf("unexpected rejection user message persisted in context: %#v", msg)
					}
					if msg.Role == "tool" && msg.ToolCallID == callID && msg.Content != payload.Result {
						t.Fatalf("restored Done tool result content = %#v, want exact synthetic rejection result", msg)
					}
					if msg.Role == "tool" && msg.ToolCallID == "loop-exit-control" {
						t.Fatalf("found orphan loop-exit-control tool message in context: %#v", msg)
					}
				}
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for Done rejection events")
		}
	}
}

func TestAwaitLoopExitConfirmationEscapesReasonJSON(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.tools.Register(tools.NewDoneTool())
	a.activeConfig = &config.AgentConfig{
		Permission: parsePermissionNode(t, `
"*": deny
Done: ask
`),
	}
	a.rebuildRuleset()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reason := "line 1\npath C:\\\\temp\\\"quoted\\\""
	respCh := make(chan ConfirmResponse, 1)
	errCh := make(chan error, 1)
	go func() {
		resp, err := a.awaitLoopExitConfirmation(ctx, &loopExitResult{Reason: reason})
		if err != nil {
			errCh <- err
			return
		}
		respCh <- resp
	}()

	select {
	case evt := <-a.outputCh:
		req, ok := evt.(ConfirmRequestEvent)
		if !ok {
			t.Fatalf("event = %T, want ConfirmRequestEvent", evt)
		}
		var args struct {
			Reason string `json:"reason"`
		}
		if !json.Valid([]byte(req.ArgsJSON)) {
			t.Fatalf("ArgsJSON is invalid JSON: %q", req.ArgsJSON)
		}
		if err := json.Unmarshal([]byte(req.ArgsJSON), &args); err != nil {
			t.Fatalf("Unmarshal(ArgsJSON): %v", err)
		}
		if args.Reason != reason {
			t.Fatalf("reason = %q, want %q", args.Reason, reason)
		}
		a.ResolveConfirm("deny", req.ArgsJSON, "", "", req.RequestID)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Done confirmation request")
	}

	select {
	case err := <-errCh:
		t.Fatalf("awaitLoopExitConfirmation: %v", err)
	case resp := <-respCh:
		if resp.Approved {
			t.Fatal("confirmation unexpectedly approved")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Done confirmation response")
	}
}

func TestHandleToolResult_DoneInLoopUserDenialDoesNotEmitLoopContinue(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.tools.Register(tools.NewDoneTool())
	a.activeConfig = &config.AgentConfig{
		Permission: parsePermissionNode(t, `
"*": deny
Done: ask
`),
	}
	a.rebuildRuleset()
	a.loopState.enableWithTarget("finish current task")
	a.loopState.markProgress()
	a.loopState.markProgress()
	a.newTurn()
	turn := a.turn
	callID := "done-user-deny-1"
	a.ctxMgr.Append(message.Message{
		Role:    "assistant",
		Content: "completed and verified",
		ToolCalls: []message.ToolCall{{
			ID:   callID,
			Name: tools.NameDone,
			Args: json.RawMessage(`{"reason":"completed and verified"}`),
		}},
	})
	turn.PendingToolCalls.Store(1)

	handled := make(chan struct{})
	go func() {
		a.handleToolResult(Event{TurnID: turn.ID, Payload: &ToolResultPayload{
			CallID:   callID,
			Name:     tools.NameDone,
			ArgsJSON: `{"reason":"completed and verified"}`,
			Result:   "completed and verified",
			TurnID:   turn.ID,
		}})
		close(handled)
	}()

	deadline := time.After(2 * time.Second)
	var sawConfirm bool
	var sawToolResult bool
	for {
		select {
		case evt := <-a.outputCh:
			switch payload := evt.(type) {
			case ConfirmRequestEvent:
				if payload.ToolName != tools.NameDone {
					t.Fatalf("ConfirmRequestEvent.ToolName = %q, want %q", payload.ToolName, tools.NameDone)
				}
				sawConfirm = true
				a.ResolveConfirm("deny", payload.ArgsJSON, "", "need a manual final check", payload.RequestID)
			case LoopNoticeEvent:
				if payload.Title != "LOOP CONTINUE" {
					continue
				}
				t.Fatal("unexpected LoopNoticeEvent for user-denied Done tool-call continuation; runtime must not inject user continuation after tool calls")
			case ToolResultEvent:
				if payload.CallID != callID || payload.Name != tools.NameDone {
					continue
				}
				sawToolResult = true
				if payload.Result != "Done rejected: need a manual final check" {
					t.Fatalf("ToolResultEvent.Result = %q, want exact user denial reason", payload.Result)
				}
				select {
				case <-handled:
				case <-time.After(2 * time.Second):
					t.Fatal("timed out waiting for user-denied Done handling to finish")
				}
				msgs := a.ctxMgr.Snapshot()
				for _, msg := range msgs {
					if msg.Role == "user" && msg.Kind == "loop_notice" && msg.Content == payload.Result {
						t.Fatalf("unexpected persisted user denial loop_notice: %#v", msg)
					}
					if msg.Role == "tool" && msg.ToolCallID == callID && msg.Content != payload.Result {
						t.Fatalf("restored Done user-denial tool result content = %#v, want exact synthetic rejection result", msg)
					}
				}
				if !sawConfirm {
					t.Fatal("expected Done confirmation request before denial result")
				}
				return
			}
		case <-deadline:
			if !sawToolResult {
				t.Fatal("timed out waiting for Done denial tool result")
			}
			return
		}
	}
}

func TestHandleToolResult_DoneInLoopUserDenialPreservesLongReason(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.tools.Register(tools.NewDoneTool())
	a.activeConfig = &config.AgentConfig{
		Permission: parsePermissionNode(t, `
"*": deny
Done: ask
`),
	}
	a.rebuildRuleset()
	a.loopState.enableWithTarget("finish current task")
	a.loopState.markProgress()
	a.loopState.markProgress()
	a.newTurn()
	turn := a.turn
	callID := "done-user-deny-long-1"
	a.ctxMgr.Append(message.Message{
		Role:    "assistant",
		Content: "completed and verified",
		ToolCalls: []message.ToolCall{{
			ID:   callID,
			Name: tools.NameDone,
			Args: json.RawMessage(`{"reason":"completed and verified"}`),
		}},
	})
	turn.PendingToolCalls.Store(1)

	longReason := strings.Repeat("长", 250) + "\nsecond line tail that must be preserved"
	wantResult := "Done rejected: " + longReason

	handled := make(chan struct{})
	go func() {
		a.handleToolResult(Event{TurnID: turn.ID, Payload: &ToolResultPayload{
			CallID:   callID,
			Name:     tools.NameDone,
			ArgsJSON: `{"reason":"completed and verified"}`,
			Result:   "completed and verified",
			TurnID:   turn.ID,
		}})
		close(handled)
	}()

	deadline := time.After(2 * time.Second)
	for {
		select {
		case evt := <-a.outputCh:
			switch payload := evt.(type) {
			case ConfirmRequestEvent:
				a.ResolveConfirm("deny", payload.ArgsJSON, "", longReason, payload.RequestID)
			case ToolResultEvent:
				if payload.CallID != callID || payload.Name != tools.NameDone {
					continue
				}
				if payload.Result != wantResult {
					t.Fatalf("ToolResultEvent.Result length/content mismatch: got %q, want %q", payload.Result, wantResult)
				}
				if !strings.Contains(payload.Result, "second line tail that must be preserved") {
					t.Fatalf("ToolResultEvent.Result lost long deny reason tail: %q", payload.Result)
				}
				select {
				case <-handled:
				case <-time.After(2 * time.Second):
					t.Fatal("timed out waiting for long user-denied Done handling to finish")
				}
				for _, msg := range a.ctxMgr.Snapshot() {
					if msg.Role == "tool" && msg.ToolCallID == callID && msg.Content != wantResult {
						t.Fatalf("persisted Done rejection = %q, want full %q", msg.Content, wantResult)
					}
				}
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for long Done denial tool result")
		}
	}
}

func TestNextLoopAssessmentFromAssistantAllowsDoneToolRequestBeforeCompleted(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.enableWithTarget("finish current task")
	a.loopState.markProgress()
	a.loopState.markProgress()

	assessment := a.nextLoopAssessmentFromAssistant(message.Message{
		Role:       "assistant",
		Content:    "done",
		StopReason: "stop",
	})
	if assessment == nil {
		t.Fatal("assessment = nil, want continue assessment")
	}
	if assessment.Action != LoopAssessmentActionContinue {
		t.Fatalf("assessment.Action = %q, want %q", assessment.Action, LoopAssessmentActionContinue)
	}
	if assessment.Message != "Loop continuing: end this round with a `Done` tool call to request loop exit." {
		t.Fatalf("assessment.Message = %q, want missing-Done guidance", assessment.Message)
	}
}

func TestNextLoopAssessmentFromAssistantRequiresNoActiveSubAgentsBeforeCompleted(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.enableWithTarget("finish current task")
	a.loopState.markProgress()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	sub := &SubAgent{
		instanceID: "agent-1",
		parent:     a,
		parentCtx:  ctx,
		cancel:     cancel,
		inputCh:    make(chan pendingUserMessage, 1),
		recovery:   a.recovery,
		ctxMgr:     ctxmgr.NewManager(100, 0),
	}
	a.mu.Lock()
	a.subAgents["agent-1"] = sub
	a.mu.Unlock()

	assessment := a.nextLoopAssessmentFromAssistant(message.Message{
		Role:    "assistant",
		Content: "finished",
		ToolCalls: []message.ToolCall{{
			ID:   "done-subagent-guard-1",
			Name: tools.NameDone,
			Args: json.RawMessage(`{"reason":"implemented and verified"}`),
		}},
		StopReason: "tool_calls",
	})
	if assessment == nil {
		t.Fatal("assessment = nil, want continue assessment")
	}
	if assessment.Action != LoopAssessmentActionContinue {
		t.Fatalf("assessment.Action = %q, want %q", assessment.Action, LoopAssessmentActionContinue)
	}
	if !strings.Contains(assessment.Message, "active subagents must finish before completion") {
		t.Fatalf("assessment.Message = %q, want active-subagent completion guard", assessment.Message)
	}
	found := false
	for _, reason := range assessment.Reasons {
		if reason == "subagents_active" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("assessment.Reasons = %v, want subagents_active", assessment.Reasons)
	}
}

func TestNextLoopAssessmentFromAssistantRejectsDoneMixedWithOtherToolCalls(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.enable()
	a.loopState.markProgress()
	a.loopState.markProgress()
	a.tools.Register(tools.NewDoneTool())
	a.tools.Register(tools.ReadTool{})
	a.activeConfig = &config.AgentConfig{
		Permission: parsePermissionNode(t, `
"*": deny
Done: allow
Read: allow
`),
	}
	a.rebuildRuleset()

	assessment := a.nextLoopAssessmentFromAssistant(message.Message{
		Role:    "assistant",
		Content: "finished",
		ToolCalls: []message.ToolCall{
			{ID: "done-1", Name: tools.NameDone, Args: json.RawMessage(`{"report":"**Completion status**: done\n**What changed**: validated\n**Verification**: tested\n**Remaining issues**: none"}`)},
			{ID: "read-1", Name: tools.NameRead, Args: json.RawMessage(`{"path":"go.mod"}`)},
		},
		StopReason: "tool_calls",
	})
	if assessment == nil {
		t.Fatal("assessment = nil, want continue assessment")
	}
	if assessment.Action != LoopAssessmentActionContinue {
		t.Fatalf("assessment.Action = %q, want %q", assessment.Action, LoopAssessmentActionContinue)
	}
	if !strings.Contains(assessment.Message, "Done") || !strings.Contains(assessment.Message, "only tool call") {
		t.Fatalf("assessment.Message = %q, want Done mixed-batch guidance", assessment.Message)
	}
	found := false
	for _, reason := range assessment.Reasons {
		if reason == "done_mixed_with_other_tools" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("assessment.Reasons = %v, want done_mixed_with_other_tools", assessment.Reasons)
	}
}

func TestNextLoopAssessmentFromAssistantRepeatedSignatureContinues(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.enable()

	msg := message.Message{Role: "assistant", Content: "same status", StopReason: "interrupted"}
	first := a.nextLoopAssessmentFromAssistant(msg)
	if first == nil || first.Action != LoopAssessmentActionContinue {
		t.Fatalf("first assessment = %#v, want continue", first)
	}
	second := a.nextLoopAssessmentFromAssistant(msg)
	if second == nil || second.Action != LoopAssessmentActionContinue {
		t.Fatalf("second assessment = %#v, want continue", second)
	}
}

func TestNextLoopAssessmentFromAssistantResetsNoProgressAfterObservableProgress(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.enable()

	msg := message.Message{Role: "assistant", Content: "same status", StopReason: "interrupted"}
	first := a.nextLoopAssessmentFromAssistant(msg)
	if first == nil || first.Action != LoopAssessmentActionContinue {
		t.Fatalf("first assessment = %#v, want continue", first)
	}
	a.loopState.markProgress()
	second := a.nextLoopAssessmentFromAssistant(msg)
	if second == nil || second.Action != LoopAssessmentActionContinue {
		t.Fatalf("second assessment = %#v, want continue after progress", second)
	}
	if a.loopState.ConsecutiveNoProgress != 0 {
		t.Fatalf("ConsecutiveNoProgress = %d, want 0 after observable progress", a.loopState.ConsecutiveNoProgress)
	}
}

func TestStallDetectorSuspectedStallAfterTwoConsecutive(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.enable()

	msg := message.Message{Role: "assistant", Content: "no change", StopReason: "interrupted"}
	// Round 1: signature set, ConsecutiveNoProgress=1 → continue
	first := a.nextLoopAssessmentFromAssistant(msg)
	if first == nil || first.Action != LoopAssessmentActionContinue {
		t.Fatalf("first assessment = %#v, want continue", first)
	}
	// Round 2: same signature, ConsecutiveNoProgress=2 → suspected_stall
	second := a.nextLoopAssessmentFromAssistant(msg)
	if second == nil || second.Action != LoopAssessmentActionContinue {
		t.Fatalf("second assessment = %#v, want continue (suspected_stall)", second)
	}
	hasSuspectedStall := false
	for _, r := range second.Reasons {
		if r == "suspected_stall" {
			hasSuspectedStall = true
		}
	}
	if !hasSuspectedStall {
		t.Fatalf("second assessment reasons = %v, want suspected_stall", second.Reasons)
	}
}

func TestStallDetectorBudgetExhaustedAfterThreeConsecutive(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.enable()

	msg := message.Message{Role: "assistant", Content: "no change", StopReason: "interrupted"}
	// Round 1: ConsecutiveNoProgress=1
	a.nextLoopAssessmentFromAssistant(msg)
	// Round 2: ConsecutiveNoProgress=2 → suspected_stall
	a.nextLoopAssessmentFromAssistant(msg)
	// Round 3: ConsecutiveNoProgress=3 → budget_exhausted
	third := a.nextLoopAssessmentFromAssistant(msg)
	if third == nil || third.Action != LoopAssessmentActionBudgetExhausted {
		t.Fatalf("third assessment = %#v, want budget_exhausted", third)
	}
}

func TestStallDetectorResetsAfterProgress(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.enable()

	msg := message.Message{Role: "assistant", Content: "no change", StopReason: "interrupted"}
	// Round 1: ConsecutiveNoProgress=1
	a.nextLoopAssessmentFromAssistant(msg)
	// Mark hard progress
	a.loopState.markProgress()
	// Round 2: ProgressVersion != LastAssessmentVersion → resets to 0
	second := a.nextLoopAssessmentFromAssistant(msg)
	if second == nil || second.Action != LoopAssessmentActionContinue {
		t.Fatalf("second assessment = %#v, want continue", second)
	}
	if a.loopState.ConsecutiveNoProgress != 0 {
		t.Fatalf("ConsecutiveNoProgress = %d, want 0 after progress", a.loopState.ConsecutiveNoProgress)
	}
	hasSuspectedStall := false
	for _, r := range second.Reasons {
		if r == "suspected_stall" {
			hasSuspectedStall = true
		}
	}
	if hasSuspectedStall {
		t.Fatalf("second assessment should not have suspected_stall after progress, got %v", second.Reasons)
	}
}

func TestStallDetectorBudgetExhaustedWithTerminalStop(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.enable()

	msg := message.Message{Role: "assistant", Content: "no change", StopReason: "interrupted"}
	// Build up ConsecutiveNoProgress=3 (round 1 → 1, round 2 → 2, round 3 → 3)
	a.nextLoopAssessmentFromAssistant(msg)
	a.nextLoopAssessmentFromAssistant(msg)
	third := a.nextLoopAssessmentFromAssistant(msg)
	if third == nil || third.Action != LoopAssessmentActionBudgetExhausted {
		t.Fatalf("third assessment = %#v, want budget_exhausted", third)
	}
	// Now test that even a subsequent terminal stop can't override budget_exhausted
	// (this tests the ordering: stall detector runs before terminal stop check)
	msgStop := message.Message{Role: "assistant", Content: "just summarizing", StopReason: "stop"}
	a.loopState.ConsecutiveNoProgress = 3                           // Simulate already stalled state
	a.loopState.LastAssessmentVersion = a.loopState.ProgressVersion // No new progress
	fourth := a.nextLoopAssessmentFromAssistant(msgStop)
	if fourth == nil || fourth.Action != LoopAssessmentActionBudgetExhausted {
		t.Fatalf("fourth assessment = %#v, want budget_exhausted even with terminal stop", fourth)
	}
}

func TestStallDetectorContinuationNoteIncludesSuspectedStall(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.enableWithTarget("finish current task")

	note := a.buildLoopContinuationNote(&LoopAssessment{
		Action:  LoopAssessmentActionContinue,
		Reasons: []string{"suspected_stall", "context_continue"},
	})
	if note == nil {
		t.Fatal("expected continuation note")
	}
	if !strings.Contains(note.Text, "no hard progress detected") {
		t.Fatalf("continuation note = %q, want suspected_stall guidance", note.Text)
	}
	if !strings.Contains(note.Text, "WARNING: You appear to be stalling") {
		t.Fatalf("continuation note = %q, want stalling warning instruction", note.Text)
	}
}

func TestDoneRejectedContinuationNoteIncludesRejectReason(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.enableWithTarget("finish current task")

	note := a.buildLoopContinuationNote(&LoopAssessment{
		Action:  LoopAssessmentActionContinue,
		Message: "Done rejected: 分析所有会话并找出可沉淀命令",
		Reasons: []string{"done_rejected", "context_continue"},
	})
	if note == nil {
		t.Fatal("expected continuation note")
	}
	if !strings.Contains(note.Text, "Latest Done rejection reason:\n分析所有会话并找出可沉淀命令") {
		t.Fatalf("continuation note = %q, want explicit rejection reason", note.Text)
	}
}

func TestStallDetectorDifferentContentDoesNotReset(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.enable()

	msg := message.Message{Role: "assistant", Content: "status A", StopReason: "interrupted"}
	a.nextLoopAssessmentFromAssistant(msg)
	// Different content = different signature, but still NOT hard progress
	msg2 := message.Message{Role: "assistant", Content: "status B", StopReason: "interrupted"}
	second := a.nextLoopAssessmentFromAssistant(msg2)
	if second == nil || second.Action != LoopAssessmentActionContinue {
		t.Fatalf("second assessment = %#v, want continue", second)
	}
	// Counter should still be 2 because different assistant text is not hard progress
	if a.loopState.ConsecutiveNoProgress != 2 {
		t.Fatalf("ConsecutiveNoProgress = %d, want 2 (different text is not hard progress)", a.loopState.ConsecutiveNoProgress)
	}
	hasSuspectedStall := false
	for _, r := range second.Reasons {
		if r == "suspected_stall" {
			hasSuspectedStall = true
		}
	}
	if !hasSuspectedStall {
		t.Fatalf("should flag suspected_stall with ConsecutiveNoProgress=2, got reasons %v", second.Reasons)
	}
}

func TestNextLoopAssessmentFromAssistantRequiresTodoSyncBeforeCompleted(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.enable()
	a.todoItems = []tools.TodoItem{{ID: "1", Content: "ship loop", Status: "in_progress"}}
	a.loopState.markProgress()

	assessment := a.nextLoopAssessmentFromAssistant(message.Message{
		Role:       "assistant",
		Content:    "all done",
		StopReason: "stop",
	})
	if assessment == nil {
		t.Fatal("assessment = nil, want continue assessment")
	}
	if assessment.Action != LoopAssessmentActionContinue {
		t.Fatalf("assessment.Action = %q, want %q", assessment.Action, LoopAssessmentActionContinue)
	}
	if assessment.Message == "" || assessment.Message == "Loop completed: assistant reached a terminal stop after observable progress." {
		t.Fatalf("assessment.Message = %q, want todo sync guard message", assessment.Message)
	}
}

func TestNextLoopAssessmentFromAssistantRequiresDoneToolBeforeCompleted(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.enableWithTarget("finish current task")
	a.loopState.markProgress()
	a.loopState.markProgress()
	a.tools.Register(tools.NewDoneTool())
	a.activeConfig = &config.AgentConfig{
		Permission: parsePermissionNode(t, `
"*": deny
Done: allow
`),
	}
	a.rebuildRuleset()

	assessment := a.nextLoopAssessmentFromAssistant(message.Message{
		Role:       "assistant",
		Content:    "done for now",
		StopReason: "stop",
	})
	if assessment == nil {
		t.Fatal("assessment = nil, want continue assessment")
	}
	if assessment.Action != LoopAssessmentActionContinue {
		t.Fatalf("assessment.Action = %q, want %q", assessment.Action, LoopAssessmentActionContinue)
	}
	if !strings.Contains(assessment.Message, "Done") {
		t.Fatalf("assessment.Message = %q, want missing-Done guidance", assessment.Message)
	}
}

func TestNextLoopAssessmentFromAssistantReturnsBlockedForBlockedTag(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.enableWithTarget("finish current task")
	a.loopState.markProgress()

	assessment := a.nextLoopAssessmentFromAssistant(message.Message{
		Role:       "assistant",
		Content:    "<blocked>dependency_unavailable: upstream service returned 503 repeatedly</blocked>",
		StopReason: "stop",
	})
	if assessment == nil {
		t.Fatal("assessment = nil, want blocked assessment")
	}
	if assessment.Action != LoopAssessmentActionBlocked {
		t.Fatalf("assessment.Action = %q, want %q", assessment.Action, LoopAssessmentActionBlocked)
	}
	if !strings.Contains(assessment.Message, "dependency_unavailable") {
		t.Fatalf("assessment.Message = %q, want blocker category", assessment.Message)
	}
}

func TestIsVerificationLikeToolResultDetectsShellValidationOutput(t *testing.T) {
	payload := &ToolResultPayload{Name: "Shell", ArgsJSON: `{"command":"go test ./..."}`}
	if !isVerificationLikeToolResult(payload, "go test ./...\nok") {
		t.Fatal("expected go test output to be treated as verification-like progress")
	}
}

func TestIsVerificationLikeToolResultRejectsNonValidationShellOutput(t *testing.T) {
	payload := &ToolResultPayload{Name: "Shell", ArgsJSON: `{"command":"echo hello"}`}
	if isVerificationLikeToolResult(payload, "hello") {
		t.Fatal("unexpected verification classification for non-validation shell output")
	}
}

func TestIsVerificationLikeToolResultDetectsVerificationFromShellCommand(t *testing.T) {
	for _, tc := range []struct {
		name    string
		command string
		result  string
	}{
		{name: "pytest command", command: "pytest -q", result: "2 passed in 0.10s"},
		{name: "npm test command", command: "npm test -- --runInBand", result: "PASS src/app.test.ts"},
		{name: "cargo test command", command: "cargo test", result: "Finished test profile"},
	} {
		payload := &ToolResultPayload{Name: "Shell", ArgsJSON: `{"command":"` + tc.command + `"}`}
		if !isVerificationLikeToolResult(payload, tc.result) {
			t.Fatalf("%s should be treated as verification-like progress", tc.name)
		}
	}
}

func TestIsVerificationLikeToolResultDoesNotMisclassifyShortPatternSubstrings(t *testing.T) {
	cases := []struct {
		name    string
		command string
		result  string
	}{
		{name: "ava substring in java result", command: "echo hello", result: "java is available"},
		{name: "tox substring in available result", command: "echo hello", result: "environment available"},
		{name: "nox substring in innocuous result", command: "echo hello", result: "knoxville"},
		{name: "ava substring in command token", command: "java -version", result: "ok"},
	}
	for _, tc := range cases {
		payload := &ToolResultPayload{Name: "Shell", ArgsJSON: `{"command":"` + tc.command + `"}`}
		if isVerificationLikeToolResult(payload, tc.result) {
			t.Fatalf("%s should not be treated as verification-like progress", tc.name)
		}
	}
}

func TestIsVerificationLikeToolResultMatchesShortVerificationCommandsWithWordBoundaries(t *testing.T) {
	for _, command := range []string{"tox -q", "nox -s tests", "npx ava"} {
		payload := &ToolResultPayload{Name: "Shell", ArgsJSON: `{"command":"` + command + `"}`}
		if !isVerificationLikeToolResult(payload, "ok") {
			t.Fatalf("command %q should be treated as verification-like progress", command)
		}
	}
}

func TestHandleToolResult_DoneInLoopAllowsExitAfterPytestVerification(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.tools.Register(tools.NewDoneTool())
	a.activeConfig = &config.AgentConfig{
		Permission: parsePermissionNode(t, `
"*": deny
Done: allow
`),
	}
	a.rebuildRuleset()
	a.loopState.enableWithTarget("finish current task")
	a.newTurn()
	turn := a.turn

	verifyCallID := "verify-pytest-1"
	a.handleToolResult(Event{TurnID: turn.ID, Payload: &ToolResultPayload{
		CallID:   verifyCallID,
		Name:     "Shell",
		ArgsJSON: `{"command":"pytest -q"}`,
		Result:   "2 passed in 0.10s",
		TurnID:   turn.ID,
	}})
	a.newTurn()
	turn = a.turn
	callID := "done-after-pytest-1"
	a.ctxMgr.Append(message.Message{
		Role:    "assistant",
		Content: "completed and verified",
		ToolCalls: []message.ToolCall{{
			ID:   callID,
			Name: tools.NameDone,
			Args: json.RawMessage(`{"reason":"completed and verified"}`),
		}},
	})
	turn.PendingToolCalls.Store(1)

	handled := make(chan struct{})
	go func() {
		a.handleToolResult(Event{TurnID: turn.ID, Payload: &ToolResultPayload{
			CallID:   callID,
			Name:     tools.NameDone,
			ArgsJSON: `{"reason":"completed and verified"}`,
			Result:   "completed and verified",
			TurnID:   turn.ID,
		}})
		close(handled)
	}()

	deadline := time.After(2 * time.Second)
	for {
		select {
		case evt := <-a.outputCh:
			switch payload := evt.(type) {
			case ConfirmRequestEvent:
				a.ResolveConfirm("allow", payload.ArgsJSON, "", "", payload.RequestID)
				select {
				case <-handled:
				case <-time.After(2 * time.Second):
					t.Fatal("timed out waiting for Done approval handling after pytest verification")
				}
				if a.loopState.Enabled {
					t.Fatal("loop should be disabled after approving Done confirmation")
				}
				return
			case InfoEvent:
				if strings.Contains(payload.Message, "Done rejected") {
					t.Fatalf("unexpected Done rejection after pytest verification: %q", payload.Message)
				}
			}
		case <-deadline:
			t.Fatal("timed out waiting for Done confirmation request after pytest verification")
		}
	}
}

func TestChangedFileSummaryMarksObservableProgressSignal(t *testing.T) {
	payload := &ToolResultPayload{
		Name:     "Write",
		ArgsJSON: `{"path":"internal/agent/example.go","content":"package example"}`,
		Diff:     "@@ -0,0 +1 @@\n+package example",
	}
	if changed := changedFileSummary(payload); changed == nil {
		t.Fatal("changedFileSummary = nil, want changed file summary")
	}
	if _, err := json.Marshal(changedFileSummary(payload)); err != nil {
		t.Fatalf("changedFileSummary marshal: %v", err)
	}
}

func TestContinueFromContextDoesNotEnableLoopByDefault(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.ContinueFromContext()
	if a.loopState.Enabled {
		t.Fatal("ContinueFromContext() should not enable loop mode by default")
	}
}

func TestHandleUserMessageTreatsLoopOffAsBusyControlCommand(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.EnableLoopMode("finish current task")
	a.newTurn()

	a.handleUserMessage(Event{Type: EventUserMessage, Payload: "/loop off"})

	if a.loopState.Enabled {
		t.Fatal("loop should be disabled immediately while busy")
	}
	if got := len(a.pendingUserMessages); got != 0 {
		t.Fatalf("len(pendingUserMessages) = %d, want 0 for busy /loop off", got)
	}
	for _, msg := range a.ctxMgr.Snapshot() {
		if strings.Contains(msg.Content, "/loop off") {
			t.Fatalf("slash command leaked into context: %q", msg.Content)
		}
	}
}

func TestHandleUserMessageTreatsLoopOnAsBusyControlCommand(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.newTurn()

	a.handleUserMessage(Event{Type: EventUserMessage, Payload: "/loop on finish current task"})

	if a.loopState.Enabled {
		t.Fatal("loop should remain disabled when Done tool is unavailable")
	}
	if got := len(a.pendingUserMessages); got != 0 {
		t.Fatalf("len(pendingUserMessages) = %d, want 0 when /loop on is rejected", got)
	}
}

func TestHandleUserMessageBusyLoopOnDefersContinuationPromptInjection(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.tools.Register(tools.NewDoneTool())
	a.activeConfig = &config.AgentConfig{
		Permission: parsePermissionNode(t, `
"*": deny
Done: allow
`),
	}
	a.rebuildRuleset()
	a.newTurn()

	a.handleUserMessage(Event{Type: EventUserMessage, Payload: "/loop on finish current task"})

	if !a.loopState.Enabled {
		t.Fatal("loop should be enabled when Done tool is available")
	}
	if !a.loopState.DeferContinuationPromptUntilDone {
		t.Fatal("busy /loop on should defer loop continuation prompt injection")
	}
	if got := len(a.pendingUserMessages); got != 0 {
		t.Fatalf("len(pendingUserMessages) = %d, want 0 for busy /loop on control command", got)
	}
	for _, msg := range a.ctxMgr.Snapshot() {
		if msg.Kind == "loop_notice" {
			t.Fatalf("unexpected persisted loop notice on busy /loop on: %q", msg.Content)
		}
	}
	for len(a.outputCh) > 0 {
		if _, ok := (<-a.outputCh).(LoopNoticeEvent); ok {
			t.Fatal("unexpected LoopNoticeEvent on busy /loop on")
		}
	}
}

func TestShouldEmitLoopContinuationForAssessmentRespectsDeferredGate(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.enableWithTarget("finish current task")
	a.loopState.DeferContinuationPromptUntilDone = true

	if a.shouldEmitLoopContinuationForAssessment(&LoopAssessment{
		Action:            LoopAssessmentActionContinue,
		TriggerStopReason: "interrupted",
	}) {
		t.Fatal("should not emit continuation when deferred gate is active and stop reason is not done")
	}
	if !a.loopState.DeferContinuationPromptUntilDone {
		t.Fatal("deferred gate should remain active for non-done stop reason")
	}
	if a.shouldEmitLoopContinuationForAssessment(&LoopAssessment{
		Action:            LoopAssessmentActionContinue,
		TriggerStopReason: "stop",
	}) {
		t.Fatal("should not emit continuation on stop when deferred gate requires done")
	}
	if !a.loopState.DeferContinuationPromptUntilDone {
		t.Fatal("deferred gate should remain active for stop when done is required")
	}
	if !a.shouldEmitLoopContinuationForAssessment(&LoopAssessment{
		Action:            LoopAssessmentActionContinue,
		TriggerStopReason: "done",
	}) {
		t.Fatal("should emit continuation when terminal done stop reason arrives")
	}
	if a.loopState.DeferContinuationPromptUntilDone {
		t.Fatal("deferred gate should be cleared after terminal done stop reason")
	}
}

func TestLoopWorkflowPromptBlockHiddenWhenBusyLoopOnIsDeferred(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.enableWithTarget("finish current task")
	a.loopState.DeferContinuationPromptUntilDone = true
	if got := a.loopWorkflowPromptBlock(); got != "" {
		t.Fatalf("loopWorkflowPromptBlock() = %q, want empty while deferred injection gate is active", got)
	}
}

func TestHandleUserMessageRejectsLoopOnWithoutDoneTool(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.newTurn()
	a.tools.Register(tools.ReadTool{})
	a.activeConfig = &config.AgentConfig{Permission: parsePermissionNode(t, `
"*": deny
Read: allow
`)}
	a.rebuildRuleset()

	a.handleUserMessage(Event{Type: EventUserMessage, Payload: "/loop on finish current task"})

	if a.loopState.Enabled {
		t.Fatal("loop should remain disabled when Done tool is unavailable")
	}
	if got := len(a.pendingUserMessages); got != 0 {
		t.Fatalf("len(pendingUserMessages) = %d, want 0", got)
	}
}

func TestHandleUserMessageLoopOnAllowedWhenYoloEnabled(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.tools.Register(tools.NewDoneTool())
	a.activeConfig = &config.AgentConfig{
		Permission: parsePermissionNode(t, `
"*": deny
Done: allow
`),
	}
	a.rebuildRuleset()
	a.SetInitialYoloMode(true)
	a.newTurn()

	a.handleUserMessage(Event{Type: EventUserMessage, Payload: "/loop on finish current task"})

	if !a.loopState.Enabled {
		t.Fatal("loop should be enabled when YOLO mode keeps Done available")
	}
	if !a.loopState.DeferContinuationPromptUntilDone {
		t.Fatal("busy /loop on should defer loop continuation prompt injection")
	}
	if got := len(a.pendingUserMessages); got != 0 {
		t.Fatalf("len(pendingUserMessages) = %d, want 0 for busy /loop on control command", got)
	}
}

func TestSubAgentTerminalTransitionMarksLoopProgress(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.enableWithTarget("finish current task")
	msg := message.Message{Role: "assistant", Content: "no change", StopReason: "interrupted"}
	first := a.nextLoopAssessmentFromAssistant(msg)
	if first == nil || first.Action != LoopAssessmentActionContinue {
		t.Fatalf("first assessment = %#v, want continue", first)
	}
	if got := a.loopState.ConsecutiveNoProgress; got != 1 {
		t.Fatalf("ConsecutiveNoProgress = %d, want 1", got)
	}

	sub := newControllableTestSubAgent(t, a, "worker-1")
	sub.setState(SubAgentStateRunning, "working")
	a.handleSubAgentStateChangedEvent(Event{
		Type:     EventSubAgentStateChanged,
		SourceID: sub.instanceID,
		Payload:  &SubAgentStateChangedPayload{State: SubAgentStateCompleted, Summary: "done"},
	})

	second := a.nextLoopAssessmentFromAssistant(msg)
	if second == nil || second.Action != LoopAssessmentActionContinue {
		t.Fatalf("second assessment = %#v, want continue after subagent progress", second)
	}
	if got := a.loopState.ConsecutiveNoProgress; got != 0 {
		t.Fatalf("ConsecutiveNoProgress = %d, want 0 after terminal subagent evidence", got)
	}
}

func TestHandleAgentErrorStopsLoopAsBlocked(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.EnableLoopMode("finish current task")
	a.newTurn()
	turnID := a.turn.ID

	a.handleAgentError(Event{
		Type:     EventAgentError,
		SourceID: "main",
		TurnID:   turnID,
		Payload:  fmt.Errorf("permission denied: missing token"),
	})

	if a.loopState.Enabled {
		t.Fatal("loop should be disabled after terminal agent error")
	}
	var blockedInfo bool
	for len(a.outputCh) > 0 {
		evt := <-a.outputCh
		if info, ok := evt.(InfoEvent); ok && strings.Contains(info.Message, "Loop blocked") {
			blockedInfo = true
			break
		}
	}
	if !blockedInfo {
		t.Fatal("expected loop blocked info event after agent error")
	}
}

func TestNextLoopAssessmentFromAssistantReturnsNilWhenLoopDisabled(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	assessment := a.nextLoopAssessmentFromAssistant(message.Message{Role: "assistant", Content: "done", StopReason: "stop"})
	if assessment != nil {
		t.Fatalf("assessment = %#v, want nil when loop is disabled", assessment)
	}
}

func TestHandleLoopAssessmentContinueDoesNotConsumeAutoExitInterceptionBudget(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.enableWithTarget("finish current task")
	a.loopState.MaxIterations = 1
	a.handleLoopAssessment(Event{Type: EventLoopAssessment, Payload: &LoopAssessment{Action: LoopAssessmentActionContinue, Message: "Loop continuing."}})

	if !a.loopState.Enabled {
		t.Fatal("loop should remain enabled after a normal continuation assessment")
	}
	if got := a.loopState.Iteration; got != 0 {
		t.Fatalf("Iteration = %d, want 0 because only automatic Done rejections count toward the interception budget", got)
	}
	if got := a.loopState.State; got != LoopStateExecuting {
		t.Fatalf("loopState.State = %q, want %q", got, LoopStateExecuting)
	}
}

func TestSendLoopAnchorFromCommandIncludesCompletionContract(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.sendLoopAnchorFromCommand("finish current task")
	msgs := a.ctxMgr.Snapshot()
	var found *message.Message
	for i := range msgs {
		if msgs[i].Kind == "loop_notice" {
			found = &msgs[i]
			break
		}
	}
	if found == nil {
		t.Fatal("expected persisted loop notice message")
	}
	if !strings.Contains(found.Content, "Completion requirements:") || !strings.Contains(found.Content, "Final completion response requirements:") {
		t.Fatalf("loop notice content = %q, want completion contract", found.Content)
	}
	if !strings.Contains(found.Content, "Pass the complete final Markdown completion report in the `Done` tool's required `report` argument. The report must include this structure:") {
		t.Fatalf("loop notice content = %q, want Done report-argument requirement", found.Content)
	}
	if !strings.Contains(found.Content, "**Completion status**:") {
		t.Fatalf("loop notice content = %q, want completion status field", found.Content)
	}
	if !strings.Contains(found.Content, "**Verification**:") {
		t.Fatalf("loop notice content = %q, want verification field", found.Content)
	}
}

func TestSendLoopAnchorFromCommandPersistsLoopNoticeAsUser(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.sendLoopAnchorFromCommand("finish current task")
	msgs := a.ctxMgr.Snapshot()
	for i := range msgs {
		if msgs[i].Kind != "loop_notice" {
			continue
		}
		if msgs[i].Role != "user" {
			t.Fatalf("loop notice role = %q, want user", msgs[i].Role)
		}
		return
	}
	t.Fatal("expected persisted loop notice message")
}

func TestEnableLoopModeSetsExecutingStateWhenPreviouslyUnset(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.State = ""
	a.EnableLoopMode("finish current task")
	if got := a.loopState.State; got != LoopStateExecuting {
		t.Fatalf("loopState.State = %q, want %q", got, LoopStateExecuting)
	}
}

func TestSendLoopAnchorFromCommandEmitsLoopNoticeEvent(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.sendLoopAnchorFromCommand("finish current task")
	var found bool
	for len(a.outputCh) > 0 {
		evt := <-a.outputCh
		notice, ok := evt.(LoopNoticeEvent)
		if !ok {
			continue
		}
		if notice.Title == "LOOP" && strings.Contains(notice.Text, "finish current task") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected LOOP notice event when sending loop anchor from command")
	}
}

func TestSendLoopAnchorFromCommandWaitsForOutputSpace(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	for i := 0; i < cap(a.outputCh); i++ {
		a.outputCh <- StreamTextEvent{Text: "busy"}
	}

	delivered := make(chan struct{})
	go func() {
		a.sendLoopAnchorFromCommand("finish current task")
		close(delivered)
	}()

	select {
	case <-delivered:
		t.Fatal("sendLoopAnchorFromCommand returned while output channel was still full")
	case <-time.After(30 * time.Millisecond):
	}

	for i := 0; i < cap(a.outputCh); i++ {
		evt := <-a.outputCh
		if _, ok := evt.(LoopNoticeEvent); ok {
			t.Fatalf("received LoopNoticeEvent before output channel space was made available at slot %d", i)
		}
	}

	select {
	case <-delivered:
	case <-time.After(time.Second):
		t.Fatal("sendLoopAnchorFromCommand did not complete after output channel space became available")
	}

	select {
	case evt := <-a.outputCh:
		notice, ok := evt.(LoopNoticeEvent)
		if !ok {
			t.Fatalf("event type = %T, want LoopNoticeEvent", evt)
		}
		if notice.Title != "LOOP" || !strings.Contains(notice.Text, "finish current task") {
			t.Fatalf("LoopNoticeEvent = %+v, want LOOP notice with target", notice)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for LoopNoticeEvent after freeing output channel space")
	}
}

func TestLoopWorkflowPromptUsesPermissionSpecificConfirmationGuidance(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.Enabled = true
	block := a.loopWorkflowPromptBlock()
	if !strings.Contains(block, "Continue autonomously from the existing context") {
		t.Fatalf("loop workflow prompt should emphasize autonomous continuation, got %q", block)
	}
	if strings.Contains(block, "Question tool") {
		t.Fatalf("loop workflow prompt without Question should not require Question tool, got %q", block)
	}

	a.tools.Register(tools.NewQuestionTool(nil))
	a.activeConfig = &config.AgentConfig{Permission: parsePermissionNode(t, `
"*": deny
Question: allow
`)}
	a.rebuildRuleset()
	block = a.loopWorkflowPromptBlock()
	if strings.Contains(block, "call the `Question` tool") {
		t.Fatalf("loop workflow prompt should not generally require Question during loop continuation, got %q", block)
	}
	if !strings.Contains(block, "do not ask merely because the automatic Done interception budget is low") {
		t.Fatalf("loop workflow prompt should discourage premature user prompts near the interception limit, got %q", block)
	}
}

func TestLoopWorkflowPromptIncludesCompletionContract(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.Enabled = true
	block := a.loopWorkflowPromptBlock()
	if !strings.Contains(block, "A task is complete only when") || !strings.Contains(block, "explicitly mark completion in your final response") {
		t.Fatalf("loop workflow prompt = %q, want completion contract", block)
	}
	if !strings.Contains(block, "Default to making ordinary engineering decisions yourself") {
		t.Fatalf("loop workflow prompt = %q, want autonomy guidance", block)
	}
	// Without open TODOs, the prompt should not mention TodoWrite or "no open TODO items remain"
	if strings.Contains(block, "no open TODO items remain") {
		t.Fatalf("loop workflow prompt should NOT contain 'no open TODO items remain' when no TODOs exist, got: %q", block)
	}
	if strings.Contains(block, "TodoWrite") {
		t.Fatalf("loop workflow prompt should NOT contain 'TodoWrite' when no TODOs exist, got: %q", block)
	}
}

func TestLoopWorkflowPromptIncludesTodoClauseWhenOpenTodosWithTodoWrite(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.tools.Register(tools.NewTodoWriteTool(nil))
	a.loopState.Enabled = true
	a.todoItems = []tools.TodoItem{{ID: "1", Content: "ship feature", Status: "pending"}}
	block := a.loopWorkflowPromptBlock()
	if !strings.Contains(block, "no open TODO items remain") {
		t.Fatalf("loop workflow prompt should contain 'no open TODO items remain' when TODOs exist and TodoWrite is available, got: %q", block)
	}
	if !strings.Contains(block, "TodoWrite") {
		t.Fatalf("loop workflow prompt should contain 'TodoWrite' when TODOs exist and TodoWrite is available, got: %q", block)
	}
}

func TestLoopWorkflowPromptIncludesTodoClauseWhenOpenTodosWithoutTodoWrite(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	// No TodoWrite registered.
	a.loopState.Enabled = true
	a.todoItems = []tools.TodoItem{{ID: "1", Content: "ship feature", Status: "pending"}}
	block := a.loopWorkflowPromptBlock()
	if strings.Contains(block, "Mark every remaining open TODO item completed or cancelled with TodoWrite") {
		t.Fatalf("loop workflow prompt should NOT tell model to use TodoWrite when the tool is unavailable, got: %q", block)
	}
	if !strings.Contains(block, "no open TODO items remain") {
		t.Fatalf("loop workflow prompt should still contain 'no open TODO items remain' when TODOs exist even without TodoWrite, got: %q", block)
	}
	if !strings.Contains(block, "Open TODO items:") {
		t.Fatalf("loop workflow prompt should list open TODOs even without TodoWrite, got: %q", block)
	}
}

func TestHandleToolResult_DoneInLoopUserDenialDoesNotIncrementIteration(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.tools.Register(tools.NewDoneTool())
	a.activeConfig = &config.AgentConfig{
		Permission: parsePermissionNode(t, `
"*": deny
Done: ask
`),
	}
	a.rebuildRuleset()
	a.loopState.enableWithTarget("finish current task")
	a.loopState.markProgress()
	a.loopState.markProgress()
	a.newTurn()
	turn := a.turn
	callID := "done-user-deny-count-1"
	a.ctxMgr.Append(message.Message{
		Role:    "assistant",
		Content: "completed and verified",
		ToolCalls: []message.ToolCall{{
			ID:   callID,
			Name: tools.NameDone,
			Args: json.RawMessage(`{"reason":"completed and verified"}`),
		}},
	})
	turn.PendingToolCalls.Store(1)

	handled := make(chan struct{})
	go func() {
		a.handleToolResult(Event{TurnID: turn.ID, Payload: &ToolResultPayload{
			CallID:   callID,
			Name:     tools.NameDone,
			ArgsJSON: `{"reason":"completed and verified"}`,
			Result:   "completed and verified",
			TurnID:   turn.ID,
		}})
		close(handled)
	}()

	deadline := time.After(2 * time.Second)
	for {
		select {
		case evt := <-a.outputCh:
			switch payload := evt.(type) {
			case ConfirmRequestEvent:
				a.ResolveConfirm("deny", payload.ArgsJSON, "", "need a manual final check", payload.RequestID)
			case ToolResultEvent:
				if payload.CallID != callID || payload.Name != tools.NameDone {
					continue
				}
				if payload.Result != "Done rejected: need a manual final check" {
					continue
				}
				<-handled
				if a.loopState.Iteration != 0 {
					t.Fatalf("loop iteration = %d, want 0 after manual Done rejection", a.loopState.Iteration)
				}
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for manual Done rejection result")
		}
	}
}

func TestHandleToolResult_DoneInLoopAutoRejectionStopsAtMaxIterations(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.tools.Register(tools.NewDoneTool())
	a.activeConfig = &config.AgentConfig{
		Permission: parsePermissionNode(t, `
"*": deny
Done: allow
`),
	}
	a.rebuildRuleset()
	a.loopState.enableWithTarget("finish current task")
	a.todoItems = []tools.TodoItem{{ID: "1", Content: "finish current task", Status: "pending"}}
	a.loopState.MaxIterations = 1
	a.newTurn()
	turn := a.turn
	callID := "done-auto-reject-budget-stop-1"
	a.ctxMgr.Append(message.Message{
		Role:    "assistant",
		Content: "completed without verification",
		ToolCalls: []message.ToolCall{{
			ID:   callID,
			Name: tools.NameDone,
			Args: json.RawMessage(`{"reason":"completed without verification"}`),
		}},
	})
	turn.PendingToolCalls.Store(1)

	a.handleToolResult(Event{TurnID: turn.ID, Payload: &ToolResultPayload{
		CallID:   callID,
		Name:     tools.NameDone,
		ArgsJSON: `{"reason":"completed without verification"}`,
		Result:   "completed without verification",
		TurnID:   turn.ID,
	}})

	if !a.loopState.Enabled {
		t.Fatal("loop should remain enabled while awaiting an explicit user decision after automatic Done interception limit is reached")
	}
	if got := a.CurrentLoopState(); got == LoopStateBudgetExhausted {
		t.Fatalf("CurrentLoopState() = %q, should not enter budget_exhausted while awaiting explicit user decision", got)
	}

	var rejected bool
	for len(a.outputCh) > 0 {
		evt := <-a.outputCh
		switch payload := evt.(type) {
		case ToolResultEvent:
			if payload.CallID == callID && payload.Name == tools.NameDone && strings.Contains(payload.Result, "Done") {
				rejected = true
			}
		}
	}
	if !rejected {
		t.Fatal("expected Done tool result event before waiting for explicit user decision")
	}
}

func TestHandleUserMessageBusyLoopOnUpdatesTargetAndMaxIterations(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.tools.Register(tools.NewDoneTool())
	a.activeConfig = &config.AgentConfig{Permission: parsePermissionNode(t, `
"*": deny
Done: allow
`)}
	a.rebuildRuleset()
	a.newTurn()

	a.handleUserMessage(Event{Type: EventUserMessage, Payload: "/loop on revised target --max-iterations 7"})

	if !a.loopState.Enabled {
		t.Fatal("loop should be enabled")
	}
	if a.loopState.Target != "revised target" {
		t.Fatalf("loop target = %q, want revised target", a.loopState.Target)
	}
	if a.loopState.MaxIterations != 7 {
		t.Fatalf("MaxIterations = %d, want 7", a.loopState.MaxIterations)
	}
	if !a.loopState.MaxIterationsSet {
		t.Fatal("MaxIterationsSet = false, want true")
	}
	if !a.loopState.DeferContinuationPromptUntilDone {
		t.Fatal("busy /loop on should still defer continuation prompt")
	}
}

func TestEnableLoopModeEmitsUnlimitedMessageWhenMaxIterationsZero(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.MaxIterations = 0
	a.loopState.MaxIterationsSet = true
	a.EnableLoopMode("finish current task")
	var found bool
	for len(a.outputCh) > 0 {
		evt := <-a.outputCh
		if info, ok := evt.(InfoEvent); ok && strings.Contains(info.Message, "Automatic Done interceptions: unlimited") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected unlimited loop enabled info event")
	}
}

func TestLoopAnchorOmitsTodoRequirementWhenNoOpenTodos(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.sendLoopAnchorFromCommand("finish current task")
	msgs := a.ctxMgr.Snapshot()
	var found *message.Message
	for i := range msgs {
		if msgs[i].Kind == "loop_notice" {
			found = &msgs[i]
			break
		}
	}
	if found == nil {
		t.Fatal("expected persisted loop notice message")
	}
	if strings.Contains(found.Content, "No open TODO items remain") {
		t.Fatalf("loop notice should NOT contain 'No open TODO items remain' when no TODOs exist, got: %q", found.Content)
	}
	if strings.Contains(found.Content, "TodoWrite") {
		t.Fatalf("loop notice should NOT contain 'TodoWrite' when no TODOs exist, got: %q", found.Content)
	}
}

func TestLoopAnchorIncludesTodoRequirementWhenOpenTodosWithTodoWrite(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.tools.Register(tools.NewTodoWriteTool(nil))
	a.todoItems = []tools.TodoItem{{ID: "1", Content: "ship feature", Status: "pending"}}
	a.sendLoopAnchorFromCommand("finish current task")
	msgs := a.ctxMgr.Snapshot()
	var found *message.Message
	for i := range msgs {
		if msgs[i].Kind == "loop_notice" {
			found = &msgs[i]
			break
		}
	}
	if found == nil {
		t.Fatal("expected persisted loop notice message")
	}
	if !strings.Contains(found.Content, "No open TODO items remain") {
		t.Fatalf("loop notice should contain 'No open TODO items remain' when TODOs exist and TodoWrite is available, got: %q", found.Content)
	}
	if !strings.Contains(found.Content, "TodoWrite") {
		t.Fatalf("loop notice should contain 'TodoWrite' when TODOs exist and TodoWrite is available, got: %q", found.Content)
	}
	if !strings.Contains(found.Content, "Open TODO items:") {
		t.Fatalf("loop notice should list open TODOs section when TODOs exist, got: %q", found.Content)
	}
}

func TestLoopAnchorIncludesTodoRequirementWhenOpenTodosWithoutTodoWrite(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	// No TodoWrite registered — hasTodoWriteAccess() returns false.
	a.todoItems = []tools.TodoItem{{ID: "1", Content: "ship feature", Status: "pending"}}
	a.sendLoopAnchorFromCommand("finish current task")
	msgs := a.ctxMgr.Snapshot()
	var found *message.Message
	for i := range msgs {
		if msgs[i].Kind == "loop_notice" {
			found = &msgs[i]
			break
		}
	}
	if found == nil {
		t.Fatal("expected persisted loop notice message")
	}
	if strings.Contains(found.Content, "Mark every remaining open TODO item completed or cancelled with TodoWrite") {
		t.Fatalf("loop notice should NOT instruct model to use TodoWrite when the tool is unavailable, got: %q", found.Content)
	}
	if !strings.Contains(found.Content, "TodoWrite is not available") {
		t.Fatalf("loop notice should explain TodoWrite is unavailable, got: %q", found.Content)
	}
	if !strings.Contains(found.Content, "finish the remaining work") {
		t.Fatalf("loop notice should instruct to finish remaining work, got: %q", found.Content)
	}
	if strings.Contains(found.Content, "No open TODO items remain") {
		t.Fatalf("loop notice should NOT say 'No open TODO items remain' when TodoWrite is unavailable (cannot sync), got: %q", found.Content)
	}
	if !strings.Contains(found.Content, "Open TODO items:") {
		t.Fatalf("loop notice should list open TODOs section even when TodoWrite is unavailable, got: %q", found.Content)
	}
}

func TestLoopContinuationOmitsTodoRequirementWhenNoOpenTodos(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.enableWithTarget("finish current task")
	note := a.buildLoopContinuationNote(&LoopAssessment{Action: LoopAssessmentActionContinue, Reasons: []string{"target_active"}})
	if note == nil {
		t.Fatal("expected continuation note")
	}
	if strings.Contains(note.Text, "No open TODO items remain") {
		t.Fatalf("LOOP CONTINUE should NOT contain 'No open TODO items remain' when no TODOs exist, got: %q", note.Text)
	}
	if strings.Contains(note.Text, "TodoWrite") {
		t.Fatalf("LOOP CONTINUE should NOT contain 'TodoWrite' when no TODOs exist, got: %q", note.Text)
	}
}

func TestLoopContinuationIncludesTodoRequirementWhenOpenTodosWithTodoWrite(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.tools.Register(tools.NewTodoWriteTool(nil))
	a.loopState.enableWithTarget("finish current task")
	a.todoItems = []tools.TodoItem{{ID: "1", Content: "ship feature", Status: "in_progress"}}
	note := a.buildLoopContinuationNote(&LoopAssessment{Action: LoopAssessmentActionContinue, Reasons: []string{"open_todos", "target_active"}})
	if note == nil {
		t.Fatal("expected continuation note")
	}
	if !strings.Contains(note.Text, "No open TODO items remain") {
		t.Fatalf("LOOP CONTINUE should contain 'No open TODO items remain' when TODOs exist and TodoWrite is available, got: %q", note.Text)
	}
	if !strings.Contains(note.Text, "TodoWrite") {
		t.Fatalf("LOOP CONTINUE should contain 'TodoWrite' when TODOs exist and TodoWrite is available, got: %q", note.Text)
	}
}

func TestLoopContinuationIncludesTodoRequirementWhenOpenTodosWithoutTodoWrite(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	// No TodoWrite registered — hasTodoWriteAccess() returns false.
	a.loopState.enableWithTarget("finish current task")
	a.todoItems = []tools.TodoItem{{ID: "1", Content: "ship feature", Status: "in_progress"}}
	note := a.buildLoopContinuationNote(&LoopAssessment{Action: LoopAssessmentActionContinue, Reasons: []string{"open_todos", "target_active"}})
	if note == nil {
		t.Fatal("expected continuation note")
	}
	if strings.Contains(note.Text, "Mark every remaining open TODO item completed or cancelled with TodoWrite") {
		t.Fatalf("LOOP CONTINUE should NOT tell model to use TodoWrite when the tool is unavailable, got: %q", note.Text)
	}
	if !strings.Contains(note.Text, "Open TODO items:") {
		t.Fatalf("LOOP CONTINUE should list open TODOs even when TodoWrite is unavailable, got: %q", note.Text)
	}
}

func TestLoopAnchorOmitsSubAgentRequirementWhenNoActiveSubAgents(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.sendLoopAnchorFromCommand("finish current task")
	msgs := a.ctxMgr.Snapshot()
	var found *message.Message
	for i := range msgs {
		if msgs[i].Kind == "loop_notice" {
			found = &msgs[i]
			break
		}
	}
	if found == nil {
		t.Fatal("expected persisted loop notice message")
	}
	if strings.Contains(found.Content, "No active subagents remain") {
		t.Fatalf("loop notice should NOT contain 'No active subagents remain' when no active subagents exist, got: %q", found.Content)
	}
	if strings.Contains(found.Content, "Active subagents:") {
		t.Fatalf("loop notice should NOT contain 'Active subagents:' section when no active subagents exist, got: %q", found.Content)
	}
}

func TestLoopAnchorIncludesSubAgentRequirementWhenActiveSubAgents(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	sub := &SubAgent{
		instanceID: "agent-1",
		parent:     a,
		parentCtx:  ctx,
		cancel:     cancel,
		inputCh:    make(chan pendingUserMessage, 1),
		recovery:   a.recovery,
		ctxMgr:     ctxmgr.NewManager(100, 0),
	}
	a.mu.Lock()
	a.subAgents["agent-1"] = sub
	a.mu.Unlock()
	a.sendLoopAnchorFromCommand("finish current task")
	msgs := a.ctxMgr.Snapshot()
	var found *message.Message
	for i := range msgs {
		if msgs[i].Kind == "loop_notice" {
			found = &msgs[i]
			break
		}
	}
	if found == nil {
		t.Fatal("expected persisted loop notice message")
	}
	if !strings.Contains(found.Content, "No active subagents remain") {
		t.Fatalf("loop notice should contain 'No active subagents remain' when active subagents exist, got: %q", found.Content)
	}
	if !strings.Contains(found.Content, "Active subagents:") {
		t.Fatalf("loop notice should contain 'Active subagents:' section when active subagents exist, got: %q", found.Content)
	}
}

func TestLoopContinuationOmitsSubAgentRequirementWhenNoActiveSubAgents(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.enableWithTarget("finish current task")
	note := a.buildLoopContinuationNote(&LoopAssessment{Action: LoopAssessmentActionContinue, Reasons: []string{"target_active"}})
	if note == nil {
		t.Fatal("expected continuation note")
	}
	if strings.Contains(note.Text, "No active subagents remain") {
		t.Fatalf("LOOP CONTINUE should NOT contain 'No active subagents remain' when no active subagents exist, got: %q", note.Text)
	}
}

func TestLoopContinuationIncludesSubAgentRequirementWhenActiveSubAgents(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.enableWithTarget("finish current task")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	sub := &SubAgent{
		instanceID: "agent-1",
		parent:     a,
		parentCtx:  ctx,
		cancel:     cancel,
		inputCh:    make(chan pendingUserMessage, 1),
		recovery:   a.recovery,
		ctxMgr:     ctxmgr.NewManager(100, 0),
	}
	a.mu.Lock()
	a.subAgents["agent-1"] = sub
	a.mu.Unlock()
	note := a.buildLoopContinuationNote(&LoopAssessment{Action: LoopAssessmentActionContinue, Reasons: []string{"subagents_active", "target_active"}})
	if note == nil {
		t.Fatal("expected continuation note")
	}
	if !strings.Contains(note.Text, "No active subagents remain") {
		t.Fatalf("LOOP CONTINUE should contain 'No active subagents remain' when active subagents exist, got: %q", note.Text)
	}
}

func TestLoopWorkflowPromptOmitsSubAgentClauseWhenNoActiveSubAgents(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.Enabled = true
	block := a.loopWorkflowPromptBlock()
	if strings.Contains(block, "no active subagents remain") {
		t.Fatalf("loop workflow prompt should NOT contain 'no active subagents remain' when no active subagents exist, got: %q", block)
	}
	if strings.Contains(block, "Active subagents:") {
		t.Fatalf("loop workflow prompt should NOT contain 'Active subagents:' when no active subagents exist, got: %q", block)
	}
}

func TestLoopWorkflowPromptIncludesSubAgentClauseWhenActiveSubAgents(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.Enabled = true
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	sub := &SubAgent{
		instanceID: "agent-1",
		parent:     a,
		parentCtx:  ctx,
		cancel:     cancel,
		inputCh:    make(chan pendingUserMessage, 1),
		recovery:   a.recovery,
		ctxMgr:     ctxmgr.NewManager(100, 0),
	}
	a.mu.Lock()
	a.subAgents["agent-1"] = sub
	a.mu.Unlock()
	block := a.loopWorkflowPromptBlock()
	if !strings.Contains(block, "no active subagents remain") {
		t.Fatalf("loop workflow prompt should contain 'no active subagents remain' when active subagents exist, got: %q", block)
	}
	if !strings.Contains(block, "Active subagents:") {
		t.Fatalf("loop workflow prompt should contain 'Active subagents:' when active subagents exist, got: %q", block)
	}
}

func TestLoopAnchorIncludesIterationBudget(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.sendLoopAnchorFromCommand("finish current task")
	msgs := a.ctxMgr.Snapshot()
	var found *message.Message
	for i := range msgs {
		if msgs[i].Kind == "loop_notice" {
			found = &msgs[i]
			break
		}
	}
	if found == nil {
		t.Fatal("expected persisted loop notice message")
	}
	if !strings.Contains(found.Content, "Automatic Done interceptions:") {
		t.Fatalf("loop notice should contain iteration budget, got: %q", found.Content)
	}
}

func TestLoopContinuationIncludesIterationBudget(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.enableWithTarget("finish current task")
	a.loopState.MaxIterations = 100
	a.loopState.Iteration = 7
	note := a.buildLoopContinuationNote(&LoopAssessment{Action: LoopAssessmentActionContinue, Reasons: []string{"target_active"}})
	if note == nil {
		t.Fatal("expected continuation note")
	}
	if !strings.Contains(note.Text, "Automatic Done interceptions 6 of 100 (94 remaining)") {
		t.Fatalf("LOOP CONTINUE should contain iteration budget with remaining count, got: %q", note.Text)
	}
}

func TestLoopContinuationConvergenceWarningNearBudgetLimit(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.enableWithTarget("finish current task")
	a.loopState.MaxIterations = 100
	a.loopState.Iteration = 99
	note := a.buildLoopContinuationNote(&LoopAssessment{Action: LoopAssessmentActionContinue, Reasons: []string{"target_active"}})
	if note == nil {
		t.Fatal("expected continuation note")
	}
	if !strings.Contains(note.Text, "Automatic Done interception budget is nearly exhausted") {
		t.Fatalf("LOOP CONTINUE should warn when budget nearly exhausted, got: %q", note.Text)
	}
	if !strings.Contains(note.Text, "near the automatic Done interception limit") {
		t.Fatalf("LOOP CONTINUE instruction should mention iteration limit, got: %q", note.Text)
	}
}

func TestLoopContinuationGapLinesConcrete(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.enableWithTarget("fix the bug")
	note := a.buildLoopContinuationNote(&LoopAssessment{Action: LoopAssessmentActionContinue, Reasons: []string{"open_todos", "subagents_active"}})
	if note == nil {
		t.Fatal("expected continuation note")
	}
	if !strings.Contains(note.Text, "Why this loop continues:") {
		t.Fatalf("LOOP CONTINUE should have 'Why this loop continues' section, got: %q", note.Text)
	}
	if !strings.Contains(note.Text, "open TODO items remain") {
		t.Fatalf("LOOP CONTINUE gap lines should mention open TODO items, got: %q", note.Text)
	}
	if !strings.Contains(note.Text, "active subagents are still running") {
		t.Fatalf("LOOP CONTINUE gap lines should mention active subagents, got: %q", note.Text)
	}
}

func TestLoopContinuationNoVagueFallbackWhenConcreteReasonsExist(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.enableWithTarget("fix the bug")
	note := a.buildLoopContinuationNote(&LoopAssessment{Action: LoopAssessmentActionContinue, Reasons: []string{"open_todos"}})
	if note == nil {
		t.Fatal("expected continuation note")
	}
	// When concrete reasons exist, the generic "loop is not yet complete" fallback should NOT appear.
	if strings.Contains(note.Text, "loop is not yet complete") {
		t.Fatalf("LOOP CONTINUE should not use vague fallback when concrete reasons exist, got: %q", note.Text)
	}
}

func TestLoopContinuationSubAgentStuckInstruction(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.enableWithTarget("finish current task")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	sub := &SubAgent{
		instanceID: "agent-1",
		parent:     a,
		parentCtx:  ctx,
		cancel:     cancel,
		inputCh:    make(chan pendingUserMessage, 1),
		recovery:   a.recovery,
		ctxMgr:     ctxmgr.NewManager(100, 0),
	}
	a.mu.Lock()
	a.subAgents["agent-1"] = sub
	a.mu.Unlock()
	note := a.buildLoopContinuationNote(&LoopAssessment{Action: LoopAssessmentActionContinue, Reasons: []string{"subagents_active", "target_active"}})
	if note == nil {
		t.Fatal("expected continuation note")
	}
	if !strings.Contains(note.Text, "stuck or blocked") {
		t.Fatalf("LOOP CONTINUE should instruct about stuck subagents when active subagents exist, got: %q", note.Text)
	}
}

func TestCurrentLoopContinuationReasonsUsesHasActiveSubAgents(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.enableWithTarget("finish current task")
	// Add a completed subagent — it should NOT trigger subagents_active.
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	sub := &SubAgent{
		instanceID: "agent-1",
		parent:     a,
		parentCtx:  ctx,
		cancel:     cancel,
		inputCh:    make(chan pendingUserMessage, 1),
		recovery:   a.recovery,
		ctxMgr:     ctxmgr.NewManager(100, 0),
	}
	sub.setState(SubAgentStateCompleted, "")
	a.mu.Lock()
	a.subAgents["agent-1"] = sub
	a.mu.Unlock()
	reasons := a.currentLoopContinuationReasons()
	for _, r := range reasons {
		if r == "subagents_active" {
			t.Fatalf("should not report subagents_active for completed subagent, reasons: %v", reasons)
		}
	}
}
