package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestHandleToolResult_DoneInLoopRequestsConfirmationAndDisablesLoopOnApproval(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.tools.Register(tools.NewDoneTool())
	a.activeConfig = &config.AgentConfig{
		Permission: parsePermissionNode(t, `
"*": deny
done: allow
`),
	}
	a.rebuildRuleset()
	a.loopState.enableWithTarget("finish current task")
	a.loopState.markProgress()
	a.newTurn("")
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
	restored := normalizeRestoredMessages(a.ctxMgr.Snapshot(), nil, identity.MainAgentID)
	var restoredTool *message.Message
	for i := range restored {
		if restored[i].Role == "tool" && restored[i].ToolCallID == callID {
			restoredTool = &restored[i]
			break
		}
	}
	if restoredTool == nil {
		t.Fatal("approved Done should persist a tool result for session resume")
	}
	if restoredTool.Content != "Done approved" {
		t.Fatalf("persisted Done content = %q, want Done approved", restoredTool.Content)
	}
	if restoredTool.ToolStatus != message.ToolStatusSuccess {
		t.Fatalf("persisted Done status = %q, want %q", restoredTool.ToolStatus, message.ToolStatusSuccess)
	}
	if a.loopState.Enabled {
		t.Fatal("loop should be disabled after approving Done confirmation")
	}
	if a.turn != nil {
		t.Fatal("turn should be cleared after approving Done confirmation")
	}
}

func TestHandleToolResult_DoneOutsideLoopEntersIdle(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.tools.Register(tools.NewDoneTool())
	a.activeConfig = &config.AgentConfig{
		Permission: parsePermissionNode(t, `
"*": deny
done: ask
`),
	}
	a.rebuildRuleset()
	a.newTurn("")
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
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.tools.Register(tools.NewDoneTool())
	a.activeConfig = &config.AgentConfig{
		Permission: parsePermissionNode(t, `
"*": deny
done: allow
`),
	}
	a.rebuildRuleset()
	a.newTurn("")
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
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.tools.Register(tools.NewDoneTool())
	a.activeConfig = &config.AgentConfig{
		Permission: parsePermissionNode(t, `
"*": deny
done: allow
`),
	}
	a.rebuildRuleset()
	a.loopState.enableWithTarget("finish current task")
	a.loopState.markProgress()
	a.todoItems = []tools.TodoItem{{ID: "1", Content: "unfinished cleanup", Status: "pending"}}
	a.newTurn("")
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
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.tools.Register(tools.NewDoneTool())
	a.activeConfig = &config.AgentConfig{
		Permission: parsePermissionNode(t, `
"*": deny
done: ask
`),
	}
	a.rebuildRuleset()
	ctx := t.Context()
	reason := "line 1\npath C:\\\\temp\\\"quoted\\\""
	respCh := make(chan ConfirmResponse, 1)
	errCh := make(chan error, 1)
	go func() {
		resp, err := a.awaitDoneConfirmation(ctx, reason, "", "")
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
		t.Fatalf("awaitDoneConfirmation: %v", err)
	case resp := <-respCh:
		if resp.Approved {
			t.Fatal("confirmation unexpectedly approved")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Done confirmation response")
	}
}

func TestHandleToolResult_MultipleDoneCallsRejectEarlierOnesWithActionableReason(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.tools.Register(tools.NewDoneTool())
	a.activeConfig = &config.AgentConfig{
		Permission: parsePermissionNode(t, `
"*": deny
done: allow
`),
	}
	a.rebuildRuleset()
	a.loopState.enableWithTarget("finish current task")
	a.todoItems = []tools.TodoItem{{ID: "1", Content: "remaining work", Status: "in_progress"}}
	a.newTurn("")
	turn := a.turn
	firstCallID := "done-batch-1"
	secondCallID := "done-batch-2"
	a.ctxMgr.Append(message.Message{
		Role:    "assistant",
		Content: "completed and verified",
		ToolCalls: []message.ToolCall{{
			ID:   firstCallID,
			Name: tools.NameDone,
			Args: json.RawMessage(`{"report":"first"}`),
		}, {
			ID:   secondCallID,
			Name: tools.NameDone,
			Args: json.RawMessage(`{"report":"second"}`),
		}},
	})
	turn.PendingToolCalls.Store(2)

	a.handleToolResult(Event{TurnID: turn.ID, Payload: &ToolResultPayload{
		CallID:   firstCallID,
		Name:     tools.NameDone,
		ArgsJSON: `{"report":"first"}`,
		Result:   "first",
		TurnID:   turn.ID,
	}})
	a.handleToolResult(Event{TurnID: turn.ID, Payload: &ToolResultPayload{
		CallID:   secondCallID,
		Name:     tools.NameDone,
		ArgsJSON: `{"report":"second"}`,
		Result:   "second",
		TurnID:   turn.ID,
	}})

	deadline := time.After(2 * time.Second)
	for {
		select {
		case evt := <-a.outputCh:
			result, ok := evt.(ToolResultEvent)
			if !ok || result.CallID != firstCallID || result.Name != tools.NameDone {
				continue
			}
			want := "Done rejected: only one `done` call can be handled in a batch; keep a single final `done` call after the remaining tool work is complete."
			if result.Result != want {
				t.Fatalf("ToolResultEvent.Result = %q, want %q", result.Result, want)
			}
			msgs := a.ctxMgr.Snapshot()
			for _, msg := range msgs {
				if msg.Role == "tool" && msg.ToolCallID == firstCallID && msg.Content != want {
					t.Fatalf("persisted earlier Done rejection = %#v, want exact actionable reason", msg)
				}
			}
			return
		case <-deadline:
			t.Fatal("timed out waiting for earlier Done rejection in multi-Done batch")
		}
	}
}

func TestHandleToolResult_DoneInLoopUserDenialDoesNotEmitLoopContinue(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.tools.Register(tools.NewDoneTool())
	a.activeConfig = &config.AgentConfig{
		Permission: parsePermissionNode(t, `
"*": deny
done: ask
`),
	}
	a.rebuildRuleset()
	a.loopState.enableWithTarget("finish current task")
	a.loopState.markProgress()
	a.loopState.markProgress()
	a.newTurn("")
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
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.tools.Register(tools.NewDoneTool())
	a.activeConfig = &config.AgentConfig{
		Permission: parsePermissionNode(t, `
"*": deny
done: ask
`),
	}
	a.rebuildRuleset()
	a.loopState.enableWithTarget("finish current task")
	a.loopState.markProgress()
	a.loopState.markProgress()
	a.newTurn("")
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

func TestHandleToolResult_DoneInLoopAllowsExitAfterPytestVerification(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.tools.Register(tools.NewDoneTool())
	a.activeConfig = &config.AgentConfig{
		Permission: parsePermissionNode(t, `
"*": deny
done: allow
`),
	}
	a.rebuildRuleset()
	a.loopState.enableWithTarget("finish current task")
	a.newTurn("")
	turn := a.turn

	verifyCallID := "verify-pytest-1"
	a.handleToolResult(Event{TurnID: turn.ID, Payload: &ToolResultPayload{
		CallID:   verifyCallID,
		Name:     "shell",
		ArgsJSON: `{"command":"pytest -q"}`,
		Result:   "2 passed in 0.10s",
		TurnID:   turn.ID,
	}})
	a.newTurn("")
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

func TestHandleToolResult_DoneInLoopUserDenialDoesNotIncrementIteration(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.tools.Register(tools.NewDoneTool())
	a.activeConfig = &config.AgentConfig{
		Permission: parsePermissionNode(t, `
"*": deny
done: ask
`),
	}
	a.rebuildRuleset()
	a.loopState.enableWithTarget("finish current task")
	a.loopState.markProgress()
	a.loopState.markProgress()
	a.newTurn("")
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
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.tools.Register(tools.NewDoneTool())
	a.activeConfig = &config.AgentConfig{
		Permission: parsePermissionNode(t, `
"*": deny
done: allow
`),
	}
	a.rebuildRuleset()
	a.loopState.enableWithTarget("finish current task")
	a.todoItems = []tools.TodoItem{{ID: "1", Content: "finish current task", Status: "pending"}}
	a.loopState.MaxIterations = 1
	a.newTurn("")
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

func TestLoopExitDecisionRequiredEmitsUserInputNotification(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.MaxIterations = 1
	a.markLoopExitDecisionRequired()

	var notified bool
	for len(a.outputCh) > 0 {
		if evt, ok := (<-a.outputCh).(NotificationEvent); ok && evt.Reason == NotificationReasonUserInputRequired {
			notified = true
		}
	}
	if !notified {
		t.Fatal("expected user-input notification while loop waits for Done decision")
	}
}

func TestLoopExitRejectionToolResultUsesHumanReadableReasons(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.loopState.enableWithTarget("fix the bug")
	a.todoItems = []tools.TodoItem{{ID: "1", Content: "ship feature", Status: "in_progress"}}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	sub := &SubAgent{
		instanceID: "agent-1",
		parent:     a,
		parentCtx:  ctx,
		cancel:     cancel,
		inputCh:    make(chan pendingUserMessage, 1),
		recovery:   a.recoveryManager(),
		ctxMgr:     ctxmgr.NewManager(100, 0),
	}
	a.subs.mu.Lock()
	a.subs.subAgents["agent-1"] = sub
	a.subs.mu.Unlock()

	got := a.loopExitRejectionToolResult()
	for _, want := range []string{
		"Done rejected automatically: loop exit conditions are not satisfied yet:",
		"open TODO items remain",
		"active subagents are still running",
		"Finish the remaining work before calling `done` again.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("loopExitRejectionToolResult() = %q, want substring %q", got, want)
		}
	}
	for _, unwanted := range []string{"open_todos", "subagents_active", "Continue working toward the current loop target."} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("loopExitRejectionToolResult() = %q, should not contain %q", got, unwanted)
		}
	}
}
