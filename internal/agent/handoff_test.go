package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/tools"
)

// setupHandoffTurn wires a turn with a completed Handoff tool call and
// processes its result, mirroring the planner writing a plan then calling
// Handoff. After this the agent is idle waiting for the user's decision and
// pendingHandoff carries the deferred result.
func setupHandoffTurn(t *testing.T, a *MainAgent, planPath string) string {
	t.Helper()
	a.newTurn()
	turnID := a.turn.ID
	callID := "handoff-1"
	argsJSON := `{"plan_path":"` + planPath + `"}`
	a.ctxMgr.Append(message.Message{
		Role: "assistant",
		ToolCalls: []message.ToolCall{{
			ID:   callID,
			Name: tools.NameHandoff,
			Args: []byte(argsJSON),
		}},
	})
	a.turn.PendingToolCalls.Store(1)
	a.turn.TotalToolCalls.Store(1)
	a.turn.recordPendingToolCall(PendingToolCall{CallID: callID, Name: tools.NameHandoff, ArgsJSON: argsJSON})

	a.handleToolResult(Event{Type: EventToolResult, TurnID: turnID, Payload: &ToolResultPayload{
		CallID:   callID,
		Name:     tools.NameHandoff,
		ArgsJSON: argsJSON,
		Result:   `{"plan_path":"` + planPath + `"}`,
		TurnID:   turnID,
	}})
	return callID
}

func TestHandoffToolResultLeavesAgentIdleForUserSelection(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	setupHandoffTurn(t, a, "docs/plans/example.md")

	if a.turn != nil {
		t.Fatalf("turn after Handoff result = %#v, want nil while waiting for user selection", a.turn)
	}
	if a.lastPlanPath != "docs/plans/example.md" {
		t.Fatalf("lastPlanPath = %q, want plan path", a.lastPlanPath)
	}
	if a.pendingHandoff == nil {
		t.Fatal("pendingHandoff = nil, want deferred handoff result retained until the user decides")
	}
	if a.pendingHandoff.RequestID == "" {
		t.Fatal("pendingHandoff.RequestID empty, want a correlating request id")
	}
	if a.pendingHandoff.CallID != "handoff-1" {
		t.Fatalf("pendingHandoff.CallID = %q, want handoff-1", a.pendingHandoff.CallID)
	}
}

// writePlanFile creates a readable non-empty plan document, mirroring what the
// planner writes before calling Handoff.
func writePlanFile(t *testing.T, dir string) string {
	t.Helper()
	planPath := filepath.Join(dir, "plans", "example.md")
	if err := os.MkdirAll(filepath.Dir(planPath), 0o755); err != nil {
		t.Fatalf("mkdir plans dir: %v", err)
	}
	if err := os.WriteFile(planPath, []byte("# Plan\n\n- Do the work.\n"), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}
	return planPath
}

// prepareExecutableHandoffAgent gives the agent the minimum configuration the
// plan-execution path needs: a selectable main-mode builder role, the planner
// as the active role (mirroring the real handoff flow), and the readiness
// markers beginPlanExecution waits on.
func prepareExecutableHandoffAgent(t *testing.T, a *MainAgent) {
	t.Helper()
	a.SetAgentConfigs(map[string]*config.AgentConfig{
		"builder": {Name: "builder", Mode: config.AgentModeMain, Description: "Builder role"},
	})
	a.activeConfig = &config.AgentConfig{Name: "planner", Mode: config.AgentModeMain}
	a.markAgentsMDReady()
	a.MarkSkillsReady()
	a.markMCPReady()
}

// Approving a valid target settles the deferred handoff call in the planner
// session that holds it, then switches to a fresh execution session. The
// execution session must start from the plan bootstrap message alone: a tool
// result stranded there without its call breaks strict provider pairing.
func TestHandoffApproveSettlesResultBeforeSessionSwitch(t *testing.T) {
	projectRoot := t.TempDir()
	planPath := writePlanFile(t, projectRoot)
	a := newTestMainAgent(t, projectRoot)
	prepareExecutableHandoffAgent(t, a)
	setupHandoffTurn(t, a, planPath)
	reqID := a.pendingHandoff.RequestID
	plannerSessionDir := a.SessionDir()
	if err := a.recovery.PersistMessage(identity.MainAgentID, a.ctxMgr.Snapshot()[0]); err != nil {
		t.Fatalf("persist planner tool call: %v", err)
	}

	a.handleHandoffResolveEvent(Event{Type: EventHandoffResolve, Payload: &handoffResolvePayload{
		RequestID: reqID,
		Action:    handoffResolveApprove,
		AgentName: "builder",
	}})

	if a.pendingHandoff != nil {
		t.Fatalf("pendingHandoff = %#v, want nil after approval", a.pendingHandoff)
	}
	if a.activeConfig == nil || a.activeConfig.Name != "builder" {
		t.Fatalf("active role = %+v, want builder after approval", a.activeConfig)
	}
	if a.SessionDir() == plannerSessionDir {
		t.Fatal("approval must switch to a fresh execution session")
	}
	for _, msg := range a.ctxMgr.Snapshot() {
		if msg.Role == message.RoleTool && msg.ToolCallID == "handoff-1" {
			t.Fatalf("handoff tool result stranded in the execution session without its call: %+v", msg)
		}
	}
	hasBootstrap := false
	for _, msg := range a.ctxMgr.Snapshot() {
		if msg.Role == message.RoleUser && strings.Contains(msg.Content, planPath) {
			hasBootstrap = true
			break
		}
	}
	if !hasBootstrap {
		t.Fatalf("execution bootstrap message missing from the new session context: %+v", a.ctxMgr.Snapshot())
	}
	oldRecovery := recovery.NewRecoveryManager(plannerSessionDir)
	t.Cleanup(oldRecovery.Close)
	snapshot, err := oldRecovery.Recover()
	if err != nil {
		t.Fatalf("recover planner snapshot: %v", err)
	}
	if snapshot.ActiveRole != "planner" {
		t.Fatalf("planner snapshot active role = %q, want planner", snapshot.ActiveRole)
	}
	oldMessages, err := oldRecovery.LoadMessages(identity.MainAgentID)
	if err != nil {
		t.Fatalf("load planner transcript: %v", err)
	}
	callIdx, resultIdx := -1, -1
	for i := range oldMessages {
		if oldMessages[i].Role == message.RoleAssistant {
			for _, call := range oldMessages[i].ToolCalls {
				if call.ID == "handoff-1" {
					callIdx = i
				}
			}
		}
		if oldMessages[i].Role == message.RoleTool && oldMessages[i].ToolCallID == "handoff-1" {
			resultIdx = i
		}
	}
	if callIdx < 0 || resultIdx != callIdx+1 {
		t.Fatalf("planner transcript call/result = (%d, %d), want adjacent pair", callIdx, resultIdx)
	}
}

// An approval naming a role the handoff never offered (unknown role, SubAgent,
// or the current active role) cannot be executed: the deferred tool result is
// recorded as an error instead of success, and no plan execution starts.
func TestHandoffApproveUnavailableTargetFailsWithoutSuccess(t *testing.T) {
	projectRoot := t.TempDir()
	planPath := writePlanFile(t, projectRoot)
	a := newTestMainAgent(t, projectRoot)
	prepareExecutableHandoffAgent(t, a)
	setupHandoffTurn(t, a, planPath)
	reqID := a.pendingHandoff.RequestID

	a.handleHandoffResolveEvent(Event{Type: EventHandoffResolve, Payload: &handoffResolvePayload{
		RequestID: reqID,
		Action:    handoffResolveApprove,
		AgentName: "ghost",
	}})

	if a.pendingHandoff != nil {
		t.Fatalf("pendingHandoff = %#v, want cleared after the rejected decision", a.pendingHandoff)
	}
	msgs := a.ctxMgr.Snapshot()
	var toolMsg *message.Message
	for i := range msgs {
		if msgs[i].Role == message.RoleTool && msgs[i].ToolCallID == "handoff-1" {
			toolMsg = &msgs[i]
			break
		}
	}
	if toolMsg == nil {
		t.Fatal("rejected handoff must still close its tool call")
	}
	if string(toolMsg.ToolStatus) != string(ToolResultStatusError) {
		t.Fatalf("tool message status = %q, want error", toolMsg.ToolStatus)
	}
	if !strings.Contains(toolMsg.Content, "not available") {
		t.Fatalf("tool message content = %q, want the unavailable-target reason", toolMsg.Content)
	}
	for i := range msgs {
		if msgs[i].Role == message.RoleUser && strings.Contains(msgs[i].Content, "Do the work.") {
			t.Fatal("plan bootstrap message must not be appended for an unavailable target")
		}
	}
}

// An approved-but-unexecutable target consumes the user's decision without
// executing it, exactly like the preparation failures below it. Those settle by
// draining queued input; this branch used to return without doing so, stranding
// anything the user typed while the handoff prompt was open until some later
// idle transition happened to pick it up.
func TestHandoffApproveUnavailableTargetDrainsQueuedInput(t *testing.T) {
	projectRoot := t.TempDir()
	planPath := writePlanFile(t, projectRoot)
	a := newTestMainAgent(t, projectRoot)
	prepareExecutableHandoffAgent(t, a)
	setupHandoffTurn(t, a, planPath)
	reqID := a.pendingHandoff.RequestID

	a.pendingUserMessages = append(a.pendingUserMessages, pendingUserMessage{
		Content:  "queued while deciding",
		FromUser: true,
	})
	if got := a.PendingUserMessageCount(); got != 1 {
		t.Fatalf("PendingUserMessageCount before resolve = %d, want 1", got)
	}

	a.handleHandoffResolveEvent(Event{Type: EventHandoffResolve, Payload: &handoffResolvePayload{
		RequestID: reqID,
		Action:    handoffResolveApprove,
		AgentName: "ghost",
	}})

	if got := a.PendingUserMessageCount(); got != 0 {
		t.Fatalf("PendingUserMessageCount after unexecutable decision = %d, want the queue drained", got)
	}
}

// A valid target whose preparation fails (here: the plan file disappeared)
// must surface as an error terminal on the handoff card, never as success.
// Staging performs no irreversible mutation first: the planner session keeps
// its role, history, and the adjacent call/result pair intact.
func TestHandoffApproveFailedPreparationFailsAsError(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	prepareExecutableHandoffAgent(t, a)
	setupHandoffTurn(t, a, filepath.Join(projectRoot, "missing", "plan.md"))
	reqID := a.pendingHandoff.RequestID

	a.handleHandoffResolveEvent(Event{Type: EventHandoffResolve, Payload: &handoffResolvePayload{
		RequestID: reqID,
		Action:    handoffResolveApprove,
		AgentName: "builder",
	}})

	if a.activeConfig == nil || a.activeConfig.Name != "planner" {
		t.Fatalf("active role = %+v, want planner preserved after failed preparation", a.activeConfig)
	}
	callIdx, resultIdx := -1, -1
	msgs := a.ctxMgr.Snapshot()
	for i := range msgs {
		if msgs[i].Role == message.RoleAssistant {
			for _, call := range msgs[i].ToolCalls {
				if call.ID == "handoff-1" {
					callIdx = i
				}
			}
		}
		if msgs[i].Role == message.RoleTool && msgs[i].ToolCallID == "handoff-1" {
			resultIdx = i
		}
	}
	if callIdx < 0 || resultIdx < 0 || resultIdx != callIdx+1 {
		t.Fatalf("error terminal must pair with its call in place: call=%d result=%d", callIdx, resultIdx)
	}
	if string(msgs[resultIdx].ToolStatus) != string(ToolResultStatusError) {
		t.Fatalf("tool message status = %q, want error after failed preparation", msgs[resultIdx].ToolStatus)
	}
	if !strings.Contains(msgs[resultIdx].Content, "failed to read plan") {
		t.Fatalf("tool message content = %q, want the preparation failure", msgs[resultIdx].Content)
	}
}

func TestHandoffApproveModelPreparationFailurePreservesPlannerSession(t *testing.T) {
	projectRoot := t.TempDir()
	planPath := writePlanFile(t, projectRoot)
	a := newTestMainAgent(t, projectRoot)
	prepareExecutableHandoffAgent(t, a)
	a.agentConfigs["builder"].Models = map[string][]string{"default": {"sample/test-model"}}
	a.SetModelSwitchFactory(func(string) (*llm.Client, string, int, error) {
		return nil, "", 0, errors.New("model unavailable")
	})
	setupHandoffTurn(t, a, planPath)
	reqID := a.pendingHandoff.RequestID
	plannerSessionDir := a.SessionDir()

	a.handleHandoffResolveEvent(Event{Type: EventHandoffResolve, Payload: &handoffResolvePayload{
		RequestID: reqID,
		Action:    handoffResolveApprove,
		AgentName: "builder",
	}})

	if a.CurrentRole() != "planner" || a.SessionDir() != plannerSessionDir {
		t.Fatalf("failed model preparation changed session state: role=%q session=%q", a.CurrentRole(), a.SessionDir())
	}
	if len(a.ctxMgr.Snapshot()) != 2 {
		t.Fatalf("failed model preparation changed planner history: %+v", a.ctxMgr.Snapshot())
	}
	assertHandoffErrorResult(t, a)
}

func TestHandoffApproveBusyPreparationFailurePreservesPlannerSession(t *testing.T) {
	projectRoot := t.TempDir()
	planPath := writePlanFile(t, projectRoot)
	a := newTestMainAgent(t, projectRoot)
	prepareExecutableHandoffAgent(t, a)
	a.SetBusyPreparationHook(func(context.Context) error { return errors.New("resources unavailable") })
	setupHandoffTurn(t, a, planPath)
	reqID := a.pendingHandoff.RequestID
	plannerSessionDir := a.SessionDir()

	a.handleHandoffResolveEvent(Event{Type: EventHandoffResolve, Payload: &handoffResolvePayload{
		RequestID: reqID,
		Action:    handoffResolveApprove,
		AgentName: "builder",
	}})

	if a.CurrentRole() != "planner" || a.SessionDir() != plannerSessionDir {
		t.Fatalf("failed resource preparation changed session state: role=%q session=%q", a.CurrentRole(), a.SessionDir())
	}
	if len(a.ctxMgr.Snapshot()) != 2 {
		t.Fatalf("failed resource preparation changed planner history: %+v", a.ctxMgr.Snapshot())
	}
	assertHandoffErrorResult(t, a)
}

func assertHandoffErrorResult(t *testing.T, a *MainAgent) {
	t.Helper()
	for _, msg := range a.ctxMgr.Snapshot() {
		if msg.Role == message.RoleTool && msg.ToolCallID == "handoff-1" {
			if msg.ToolStatus != string(ToolResultStatusError) {
				t.Fatalf("handoff result status = %q, want error", msg.ToolStatus)
			}
			return
		}
	}
	t.Fatal("handoff error result missing")
}

// AvailableAgents offers only main-mode roles other than the active one. When
// the active role is the only configured main-mode agent the list is empty:
// handing off to the current role is never offered or silently substituted.
func TestAvailableAgentsExcludesCurrentRole(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.SetAgentConfigs(map[string]*config.AgentConfig{
		"builder": {Name: "builder", Mode: config.AgentModeMain},
		"planner": {Name: "planner", Mode: config.AgentModeMain},
		"coder":   {Name: "coder", Mode: config.AgentModeSubAgent},
	})
	a.activeConfig = a.agentConfigs["planner"]

	got := a.AvailableAgents()
	if len(got) != 1 || got[0] != "builder" {
		t.Fatalf("AvailableAgents() = %v, want [builder] with planner active", got)
	}
	if !a.validHandoffTarget("builder") || a.validHandoffTarget("planner") || a.validHandoffTarget("coder") || a.validHandoffTarget("") {
		t.Fatal("validHandoffTarget must accept only other main-mode roles")
	}

	// Only the active role remains: no eligible target exists at all.
	a.agentConfigs = map[string]*config.AgentConfig{"planner": {Name: "planner", Mode: config.AgentModeMain}}
	if got := a.AvailableAgents(); len(got) != 0 {
		t.Fatalf("AvailableAgents() = %v, want empty when only the active role exists", got)
	}
	if a.validHandoffTarget("planner") {
		t.Fatal("the current role must never be a valid handoff target")
	}
}

func TestHandoffDenyEmitsRejectionAndAppendsUserMessage(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	setupHandoffTurn(t, a, "docs/plans/example.md")
	reqID := a.pendingHandoff.RequestID

	a.handleHandoffResolveEvent(Event{Type: EventHandoffResolve, Payload: &handoffResolvePayload{
		RequestID:  reqID,
		Action:     handoffResolveDeny,
		DenyReason: "use reviewer first",
	}})

	if a.pendingHandoff != nil {
		t.Fatalf("pendingHandoff = %#v, want nil after rejection", a.pendingHandoff)
	}
	msgs := a.ctxMgr.Snapshot()
	var toolMsg, userMsg *message.Message
	for i := range msgs {
		if msgs[i].Role == message.RoleTool && msgs[i].ToolCallID == "handoff-1" {
			toolMsg = &msgs[i]
		}
		if msgs[i].Role == message.RoleUser && strings.HasPrefix(msgs[i].Content, "Handoff rejected:") {
			userMsg = &msgs[i]
		}
	}
	if toolMsg == nil || !strings.Contains(toolMsg.Content, "Handoff rejected: use reviewer first") {
		t.Fatalf("handoff tool message = %+v, want rejection text", toolMsg)
	}
	if userMsg == nil {
		t.Fatal("rejection user message not appended to context")
	}
	if !strings.Contains(userMsg.Content, "Plan path: docs/plans/example.md") {
		t.Fatalf("rejection user message = %q, want plan path anchor", userMsg.Content)
	}
}

func TestHandoffCancelEmitsCancelledResult(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	setupHandoffTurn(t, a, "docs/plans/example.md")
	reqID := a.pendingHandoff.RequestID

	a.handleHandoffResolveEvent(Event{Type: EventHandoffResolve, Payload: &handoffResolvePayload{
		RequestID: reqID,
		Action:    handoffResolveCancel,
	}})

	if a.pendingHandoff != nil {
		t.Fatalf("pendingHandoff = %#v, want nil after cancel", a.pendingHandoff)
	}
	msgs := a.ctxMgr.Snapshot()
	var toolMsg *message.Message
	for i := range msgs {
		if msgs[i].Role == message.RoleTool && msgs[i].ToolCallID == "handoff-1" {
			toolMsg = &msgs[i]
			break
		}
	}
	if toolMsg == nil {
		t.Fatal("handoff cancelled tool message not appended to context")
	}
	if string(toolMsg.ToolStatus) != string(ToolResultStatusCancelled) {
		t.Fatalf("tool message status = %q, want cancelled", toolMsg.ToolStatus)
	}
}

// TestHandoffStaleResolutionIgnored verifies a decision whose request id no
// longer matches the pending handoff (e.g. after a new turn started) is
// dropped instead of settling a wrong wait or executing the plan.
func TestHandoffStaleResolutionIgnored(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	setupHandoffTurn(t, a, "docs/plans/example.md")

	a.handleHandoffResolveEvent(Event{Type: EventHandoffResolve, Payload: &handoffResolvePayload{
		RequestID: "stale-request",
		Action:    handoffResolveApprove,
		AgentName: "builder",
	}})

	if a.pendingHandoff == nil {
		t.Fatal("pendingHandoff cleared by a stale decision, want it retained")
	}
}

// TestHandoffUserWaitSettled verifies the handoff selector wait is recorded as
// User wait (not tool time) once the user cancels the handoff.
func TestHandoffUserWaitSettled(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	setupHandoffTurn(t, a, "docs/plans/example.md")
	reqID := a.pendingHandoff.RequestID

	time.Sleep(30 * time.Millisecond)
	waitBefore := a.walltime.statsForAgent(identity.MainAgentID).UserWait

	a.handleHandoffResolveEvent(Event{Type: EventHandoffResolve, Payload: &handoffResolvePayload{
		RequestID: reqID,
		Action:    handoffResolveCancel,
	}})

	stats := a.walltime.statsForAgent(identity.MainAgentID)
	if stats.UserWait <= waitBefore {
		t.Fatalf("main walltime user wait = %v, want > %v after the handoff wait", stats.UserWait, waitBefore)
	}
}

// TestHandoffAbandonEmitsCancelledResult verifies that abandoning a pending
// handoff (new turn / session switch) emits the deferred tool result as
// cancelled and persists it, so the transcript never keeps an unresolved
// handoff tool call that would break strict provider pairing on the next
// request.
func TestHandoffAbandonEmitsCancelledResult(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	setupHandoffTurn(t, a, "docs/plans/example.md")

	a.newTurn()

	if a.pendingHandoff != nil {
		t.Fatalf("pendingHandoff = %#v, want nil after newTurn", a.pendingHandoff)
	}
	msgs := a.ctxMgr.Snapshot()
	var toolMsg *message.Message
	for i := range msgs {
		if msgs[i].Role == message.RoleTool && msgs[i].ToolCallID == "handoff-1" {
			toolMsg = &msgs[i]
			break
		}
	}
	if toolMsg == nil {
		t.Fatal("handoff cancelled tool message not appended after abandon")
	}
	if string(toolMsg.ToolStatus) != string(ToolResultStatusCancelled) {
		t.Fatalf("tool message status = %q, want cancelled", toolMsg.ToolStatus)
	}
}

func TestHandoffShutdownSettlesPendingResult(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	setupHandoffTurn(t, a, "docs/plans/example.md")

	if err := a.Shutdown(5 * time.Second); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	if a.pendingHandoff != nil {
		t.Fatalf("pendingHandoff = %#v, want nil after shutdown", a.pendingHandoff)
	}
	msgs := a.ctxMgr.Snapshot()
	var toolMsg *message.Message
	for i := range msgs {
		if msgs[i].Role == message.RoleTool && msgs[i].ToolCallID == "handoff-1" {
			toolMsg = &msgs[i]
			break
		}
	}
	if toolMsg == nil {
		t.Fatal("handoff cancelled tool message not appended at shutdown")
	}
	if string(toolMsg.ToolStatus) != string(ToolResultStatusCancelled) {
		t.Fatalf("tool message status = %q, want cancelled", toolMsg.ToolStatus)
	}
}
