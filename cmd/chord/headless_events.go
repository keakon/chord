package main

import (
	"encoding/json"
	"os"
	"strings"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

// filterHeadlessEvent converts an AgentEvent to one or more headlessEnvelopes.
// Returns nil if the event should be filtered out (not subscribed).
func filterHeadlessEvent(ev agent.AgentEvent, state *headlessState, backends ...headlessBackend) []*headlessEnvelope {
	var backend headlessBackend
	if len(backends) > 0 {
		backend = backends[0]
	}

	if status, ok := ev.(agent.MCPStatusChangedEvent); ok {
		if provider, ok := backend.(headlessRoleBackend); ok && status.Role != "" && status.Role != provider.CurrentRole() {
			return nil
		}
		if current := headlessBackendSessionID(backend); current != "" && status.SessionID != "" && current != status.SessionID {
			return nil
		}
	}
	// Handoff plan file IO and agent-option lookup stay outside state.mu: both
	// can block while the event loop and command handlers contend on the lock.
	var preHandoffAgents []agent.HandoffAgentOption
	var preHandoffPlanText, preHandoffPlanError string
	if he, ok := ev.(agent.HandoffEvent); ok {
		preHandoffAgents = []agent.HandoffAgentOption{}
		if hb, ok := backend.(headlessHandoffBackend); ok {
			preHandoffAgents = hb.HandoffAgentOptions()
		}
		if preHandoffAgents == nil {
			preHandoffAgents = []agent.HandoffAgentOption{}
		}
		if b, err := os.ReadFile(he.PlanPath); err == nil {
			preHandoffPlanText = string(b)
		} else {
			preHandoffPlanError = err.Error()
		}
	}
	state.mu.Lock()
	defer state.mu.Unlock()

	var out []*headlessEnvelope
	// mutated tracks whether cached state changed: any mutation must bump seq
	// even when no envelope is emitted (unsubscribed), so later
	// status_response snapshots stay strictly newer than earlier ones.
	mutated := false
	// touch records a cached-state mutation: it marks the state dirty for the
	// seq bump above and timestamps the change.
	touch := func() {
		mutated = true
		state.updatedAt = time.Now()
	}

	switch e := ev.(type) {
	case agent.MCPStatusChangedEvent:
		if applyHeadlessMCPSnapshotLocked(state, e.Snapshot) {
			state.mcpRole = e.Role
			state.mcpSession = e.SessionID
			touch()
			if state.isSubscribed(headlessMCPStatusChanged) {
				out = append(out, &headlessEnvelope{Type: headlessMCPStatusChanged, Payload: state.mcp.Clone()})
			}
		}

	case agent.InputQueueOperationResultEvent:
		out = append(out, &headlessEnvelope{Type: "input_queue_result", Payload: e})
	case agent.ConfirmRuleIntentResultEvent:
		out = append(out, &headlessEnvelope{Type: "confirm_rule_intent_result", Payload: e})
	case agent.InputQueueChangedEvent:
		projection := inputQueueProjection(e.Snapshot)
		if projection == nil {
			break
		}
		if state.inputQueue != nil && state.inputQueue.RuntimeID == projection.RuntimeID && state.inputQueue.SessionID == projection.SessionID && state.inputQueue.SessionEpoch == projection.SessionEpoch && state.inputQueue.ViewVersion >= projection.ViewVersion {
			break
		}
		if queue, ok := backend.(headlessInputQueueBackend); ok && projection.InputTarget != queue.InputDeliveryTarget() {
			break
		}
		if projection.SessionID != headlessBackendSessionID(backend) {
			break
		}
		state.inputQueue = projection
		touch()
		if state.isSubscribed("input_queue_changed") {
			out = append(out, &headlessEnvelope{Type: "input_queue_changed", Payload: projection})
		}
	case agent.InputResultEvent:
		// Correlated command replies are always delivered, independent of
		// optional push subscriptions, and never clear an active turn.
		out = append(out, &headlessEnvelope{Type: "input_result", Payload: e})
		if e.Status == agent.InputStarted {
			state.busy = true
			touch()
		}
	case agent.AgentActivityEvent:
		if e.Type == agent.ActivityIdle {
			return nil // filtered; idle is expressed via GlobalIdleEvent
		}
		touch()
		state.busy = true
		state.phase = string(e.Type)
		state.phaseDetail = e.Detail
		if e.Type == agent.ActivityCompacting {
			// Don't modify pendingOutcome for compacting
		} else if state.pendingOutcome != "error" && state.pendingOutcome != "cancelled" {
			state.pendingOutcome = "completed"
		}
		if state.isSubscribed("activity") {
			out = append(out, &headlessEnvelope{Type: "activity", Payload: map[string]string{
				"agent_id": e.AgentID,
				"type":     string(e.Type),
				"detail":   e.Detail,
			}})
		}
	case agent.CompactionStatusEvent:
		touch()
		// The control plane observes started and terminal outcomes only;
		// internal progress telemetry stays on the TUI slot. Ready is TUI-only
		// too: the gateway resolves a compaction slot on any non-started
		// status, so a parked-draft ready marker must not reach it as a
		// terminal outcome.
		if e.Status == agent.CompactionStatusProgress || e.Status == agent.CompactionStatusReady {
			state.stampHeadlessSeq(true)
			return nil
		}
		if state.isSubscribed("compaction_status") {
			out = append(out, &headlessEnvelope{Type: "compaction_status", Payload: map[string]any{
				"status":    e.Status,
				"trigger":   e.Trigger,
				"reason":    e.Reason,
				"plan_id":   e.PlanID,
				"synthetic": e.Synthetic,
			}})
		}
	case agent.AssistantMessageEvent:
		touch()
		if strings.TrimSpace(e.Text) == "" {
			log.Warnf("headless observed empty assistant_message agent_id=%v tool_calls=%v", e.AgentID, e.ToolCalls)
		} else {
			log.Debugf("headless forwarding assistant_message agent_id=%v text_len=%v tool_calls=%v", e.AgentID, len(e.Text), e.ToolCalls)
		}
		if state.isSubscribed("assistant_message") {
			out = append(out, &headlessEnvelope{Type: "assistant_message", Payload: map[string]any{
				"agent_id":        e.AgentID,
				"task_id":         e.TaskID,
				"agent_type":      e.AgentType,
				"parent_agent_id": e.ParentAgentID,
				"text":            e.Text,
				"tool_calls":      e.ToolCalls,
			}})
		}
	case agent.AgentStartedEvent:
		touch()
		if state.isSubscribed("agent_started") {
			out = append(out, &headlessEnvelope{Type: "agent_started", Payload: map[string]string{
				"agent_id":          e.AgentID,
				"previous_agent_id": e.PreviousAgentID,
				"task_id":           e.TaskID,
				"agent_type":        e.AgentType,
				"description":       e.Description,
				"parent_agent_id":   e.ParentAgentID,
				"parent_task_id":    e.ParentTaskID,
			}})
		}
	case agent.AgentNotifyEvent:
		touch()
		if state.isSubscribed("agent_notify") {
			payload := map[string]string{
				"agent_id":        e.AgentID,
				"task_id":         e.TaskID,
				"agent_type":      e.AgentType,
				"parent_agent_id": e.ParentAgentID,
				"parent_task_id":  e.ParentTaskID,
				"target_agent_id": e.TargetAgentID,
				"target_task_id":  e.TargetTaskID,
				"kind":            e.Kind,
				"message":         e.Message,
			}
			// Subtype distinguishes an alert that already resolved (e.g. stall →
			// stall_resolved) from one still pending. The key is omitted when
			// absent so the envelope shape stays backward compatible.
			if e.Subtype != "" {
				payload["subtype"] = e.Subtype
			}
			out = append(out, &headlessEnvelope{Type: "agent_notify", Payload: payload})
		}
	case agent.IdleEvent:
		touch()
	case agent.GlobalIdleEvent:
		touch()
		state.busy = false
		state.phase = ""
		state.phaseDetail = ""
		outcome := state.pendingOutcome
		state.lastOutcome = outcome
		state.pendingOutcome = ""
		state.pendingConfirm = nil
		state.lastError = ""
		if state.isSubscribed("idle") {
			out = append(out, &headlessEnvelope{Type: "idle", Payload: map[string]any{
				"last_outcome":               outcome,
				"suppress_user_notification": e.SuppressUserNotification,
				"running_jobs":               e.RunningJobs,
			}})
		}
	case agent.RoleChangedEvent:
		// A RoleChangedEvent is emitted right after the agent commits a switch,
		// but it travels through a separate event stream and can drain after a
		// newer role set has already updated the cache, so trusting the event
		// verbatim could regress state.role to an older role. Resolve the role
		// from the backend's committed state (the event never leads the backend)
		// and push role_change when this successful switch has not been
		// announced yet. The dedup key is lastBroadcastRole — the last role
		// actually announced — not state.role, whose cache can be refreshed by a
		// role set response before any role_change envelope was emitted. Falls
		// back to the event's role when the backend is not role-capable.
		role := e.Role
		if rb, ok := backend.(headlessRoleBackend); ok {
			if current := rb.CurrentRole(); current != "" {
				role = current
			}
		}
		changed := state.lastBroadcastRole != role
		touch()
		if state.mcpRole != "" && state.mcpRole != role {
			clearHeadlessMCPStateLocked(state)
		}
		state.role = role
		if changed {
			state.lastBroadcastRole = role
		}
		if changed && state.isSubscribed("role_change") {
			out = append(out, &headlessEnvelope{Type: "role_change", Payload: map[string]string{
				"role": role,
			}})
		}
		if changed && state.mcpRole == role && state.isSubscribed(headlessMCPStatusChanged) {
			out = append(out, &headlessEnvelope{Type: headlessMCPStatusChanged, Payload: state.mcp.Clone()})
		}
	case agent.NotificationEvent:
		touch()
		if state.isSubscribed("notification") {
			out = append(out, &headlessEnvelope{Type: "notification", Payload: map[string]string{
				"reason":  e.Reason,
				"message": e.Message,
			}})
		}
	case agent.ErrorEvent:
		if e.Silent {
			// Silent retry telemetry never reaches the user: the TUI records
			// it in the error panel without settling cards or rendering an
			// error block. Promoting it here would flip last_outcome to
			// "error" for turns that recover and complete. Dropping it is
			// safe because every silent-only sequence is eventually followed
			// by a non-silent error on terminal failure; any new path that
			// emits only silent errors must also emit a non-silent terminal
			// error.
			touch()
			state.stampHeadlessSeq(true)
			return nil
		}
		touch()
		state.pendingOutcome = "error"
		if e.Err != nil {
			state.lastError = e.Err.Error()
		}
		if state.isSubscribed("error") {
			out = append(out, &headlessEnvelope{Type: "error", Payload: map[string]string{
				"message":  state.lastError,
				"agent_id": e.AgentID,
			}})
		}
	case agent.ConfirmRequestEvent:
		touch()
		doneReason, doneReport := parseHeadlessDoneArgs(e.ArgsJSON)
		if strings.TrimSpace(e.DoneReport) != "" {
			doneReport = strings.TrimSpace(e.DoneReport)
		}
		needsApprovalRuleItems := headlessRuleItems(e.NeedsApprovalRules)
		alreadyAllowedRuleItems := headlessRuleItems(e.AlreadyAllowedRules)
		state.pendingConfirm = &headlessConfirmPayload{
			ToolName: e.ToolName, ArgsJSON: e.ArgsJSON, RequestID: e.RequestID,
			TimeoutMS: e.Timeout.Milliseconds(), NeedsApproval: e.NeedsApproval,
			AlreadyAllowed:         e.AlreadyAllowed,
			NeedsApprovalRules:     permission.RulePatterns(e.NeedsApprovalRules),
			AlreadyAllowedRules:    permission.RulePatterns(e.AlreadyAllowedRules),
			NeedsApprovalRuleItems: needsApprovalRuleItems, AlreadyAllowedRuleItems: alreadyAllowedRuleItems,
			DoneReport: doneReport, DoneReason: doneReason, AgentID: e.AgentID,
		}
		if state.isSubscribed("confirm_request") {
			out = append(out, &headlessEnvelope{Type: "confirm_request", Payload: map[string]any{
				"tool_name":                  e.ToolName,
				"args_json":                  e.ArgsJSON,
				"request_id":                 e.RequestID,
				"timeout_ms":                 e.Timeout.Milliseconds(),
				"needs_approval":             e.NeedsApproval,
				"already_allowed":            e.AlreadyAllowed,
				"needs_approval_rules":       permission.RulePatterns(e.NeedsApprovalRules),
				"already_allowed_rules":      permission.RulePatterns(e.AlreadyAllowedRules),
				"needs_approval_rule_items":  needsApprovalRuleItems,
				"already_allowed_rule_items": alreadyAllowedRuleItems,
				"done_report":                doneReport,
				"done_reason":                doneReason,
				"agent_id":                   e.AgentID,
			}})
		}
	case agent.ConfirmResolvedEvent:
		if state.pendingConfirm != nil && state.pendingConfirm.RequestID == e.RequestID {
			touch()
			state.pendingConfirm = nil
		}
		if state.isSubscribed("confirm_resolved") {
			out = append(out, &headlessEnvelope{Type: "confirm_resolved", Payload: map[string]any{
				"request_id": e.RequestID,
				"tool_name":  e.ToolName,
				"agent_id":   e.AgentID,
				"outcome":    e.Outcome,
			}})
		}
	case agent.QuestionTranscriptEvent:
		var fact agent.QuestionFact
		if json.Unmarshal(e.Message.Question, &fact) == nil && fact.Operation == agent.QuestionOpRevise && fact.Result != nil && len(fact.Updates) == 1 {
			q := fact.Updates[0]
			if old := state.questions[q.ID]; old != nil && old.Version >= q.Version {
				break
			}
			touch()
			if state.questions == nil {
				state.questions = map[string]*headlessQuestionPayload{}
			}
			q.Revision = fact.Result
			payload := headlessQuestionSnapshot(q)
			state.questions[q.ID] = payload
			if state.isSubscribed("question_updated") {
				out = append(out, &headlessEnvelope{Type: "question_updated", Payload: payload})
			}
		}
	case agent.QuestionStateEvent:
		q := e.Question
		if !q.Visible && q.Outcome == "" {
			break
		}
		if state.questions == nil {
			state.questions = map[string]*headlessQuestionPayload{}
		}
		if old := state.questions[q.ID]; old != nil && old.Version >= q.Version {
			break
		}
		touch()
		old := state.questions[q.ID]
		payload := headlessQuestionSnapshot(q)
		state.questions[q.ID] = payload
		kind := "question_request"
		if q.Outcome != "" && (old == nil || old.Outcome == "") {
			kind = "question_resolved"
		} else if q.Version > 1 {
			kind = "question_updated"
		}
		if state.isSubscribed(kind) && !(e.Restored && q.Outcome != "") {
			out = append(out, &headlessEnvelope{Type: kind, Payload: payload})
		}
	case agent.HandoffEvent:
		// Offer exactly the runtime's eligible targets; an empty list means no
		// legal handoff target exists (the active role is the only main-mode
		// agent) and must reach the client as-is instead of a fabricated
		// default the runtime would reject on approval.
		// Agent options and plan file contents were loaded before acquiring
		// state.mu; only the cache update runs under the lock.
		touch()
		payload := &headlessHandoffPayload{
			RequestID: e.RequestID,
			PlanPath:  e.PlanPath,
			Agents:    preHandoffAgents,
			PlanText:  preHandoffPlanText,
			PlanError: preHandoffPlanError,
		}
		state.pendingHandoff = payload
		if state.isSubscribed("handoff_request") {
			out = append(out, &headlessEnvelope{Type: "handoff_request", Payload: payload})
		}
	case agent.HandoffCancelledEvent:
		touch()
		// A new turn or session switch discarded the pending handoff before the
		// client decided. Only clear the cached request when this cancellation
		// targets it (an empty RequestID cancels whatever is pending); a stale
		// cancellation for an older request must not drop a newer pending one.
		if state.pendingHandoff != nil && (e.RequestID == "" || state.pendingHandoff.RequestID == e.RequestID) {
			state.pendingHandoff = nil
		}
		// Forward even when it did not match the cached request: that request is
		// truly gone, and the client must stop waiting on it.
		if state.isSubscribed("handoff_cancelled") {
			out = append(out, &headlessEnvelope{Type: "handoff_cancelled", Payload: map[string]string{
				"request_id": e.RequestID,
				"reason":     e.Reason,
			}})
		}
	case agent.AgentDoneEvent:
		touch()
		if state.isSubscribed("agent_done") {
			out = append(out, &headlessEnvelope{Type: "agent_done", Payload: map[string]string{
				"agent_id":        e.AgentID,
				"task_id":         e.TaskID,
				"agent_type":      e.AgentType,
				"parent_agent_id": e.ParentAgentID,
				"parent_task_id":  e.ParentTaskID,
				"summary":         e.Summary,
			}})
		}
	case agent.InfoEvent:
		if state.isSubscribed("info") {
			out = append(out, &headlessEnvelope{Type: "info", Payload: map[string]string{"message": e.Message, "agent_id": e.AgentID}})
		}
	case agent.ToolResultEvent:
		touch()
		if e.Name == tools.NameGenerateImage && state.isSubscribed(headlessImageResult) {
			out = append(out, headlessImageResultEnvelope(e))
		}
		if strings.EqualFold(e.Name, tools.NameDone) && e.AgentID == "" {
			reason, report := parseHeadlessDoneArgs(e.ArgsJSON)
			if strings.TrimSpace(e.DoneReport) != "" {
				report = strings.TrimSpace(e.DoneReport)
			}
			if report != "" && state.isSubscribed("done_completion") {
				out = append(out, &headlessEnvelope{Type: "done_completion", Payload: map[string]any{"call_id": e.CallID, "report": report, "reason": reason, "status": string(e.Status), "agent_id": e.AgentID, "mode": "normal"}})
			}
		}
	case agent.StreamRollbackEvent:
		touch()
		if state.isSubscribed("assistant_rollback") {
			out = append(out, &headlessEnvelope{Type: "assistant_rollback", Payload: map[string]string{"reason": e.Reason, "agent_id": e.AgentID}})
		}
	case agent.TodosUpdatedEvent:
		touch()
		if state.isSubscribed("todos") {
			out = append(out, &headlessEnvelope{Type: "todos", Payload: map[string]any{"todos": e.Todos}})
		}
	case agent.ToastEvent:
		if state.isSubscribed("toast") {
			out = append(out, &headlessEnvelope{Type: "toast", Payload: map[string]string{"message": e.Message, "level": e.Level, "agent_id": e.AgentID}})
		}
	case agent.SessionRestoredEvent:
		// An in-band session switch (handoff plan execution, /resume <id>)
		// replaces the session without restarting the process, so the startup
		// session snapshot alone would leave status_response and the gateway
		// pin on the old session. Refresh the tracked id from the backend's
		// committed state and announce the change explicitly. Restores that
		// keep the session (startup replay, durable compaction rewrite) only
		// refresh the timestamp.
		touch()
		sessionID := headlessBackendSessionID(backend)
		if sessionID == "" || sessionID == state.sessionID {
			// No committed id to adopt, or the id is already the tracked one:
			// either way there is nothing new to announce.
			break
		}
		if state.mcpSession != "" && state.mcpSession != sessionID {
			clearHeadlessMCPStateLocked(state)
		}
		state.sessionID = sessionID
		if queue, ok := backend.(headlessInputQueueBackend); ok {
			target := queue.InputDeliveryTarget()
			state.runtimeID, state.sessionEpoch = target.RuntimeID, target.SessionEpoch
		}
		state.inputQueue = headlessInputQueueSnapshot(backend)
		state.questions = nil
		if state.isSubscribed("session_switched") {
			out = append(out, &headlessEnvelope{Type: "session_switched", Payload: map[string]any{
				"session_id": sessionID, "runtime_id": state.runtimeID, "session_epoch": state.sessionEpoch, "workspace": state.workspace, "input_queue": state.inputQueue,
			}})
		}
		if state.mcpSession == sessionID && state.isSubscribed(headlessMCPStatusChanged) {
			out = append(out, &headlessEnvelope{Type: headlessMCPStatusChanged, Payload: state.mcp.Clone()})
		}
	case agent.WorkDirChangedEvent:
		snapshot := headlessWorkDirSnapshot(backend)
		if snapshot == state.workDir {
			break
		}
		touch()
		state.workDir = snapshot
		if state.isSubscribed("workdir_changed") {
			out = append(out, &headlessEnvelope{Type: "workdir_changed", Payload: headlessWorkDirPayload(snapshot)})
		}
	case agent.BackgroundResultAppendedEvent:
		// A finished background job's result is durable now, and this event is
		// the only delivery channel for the JOB RESULT card: background output
		// typically lands after the turn is idle, so no later
		// assistant_message summarizes it. The durable row keeps the
		// <system-reminder> wrapper for the model; integrators receive the bare
		// result text.
		touch()
		if state.isSubscribed("background_result") {
			out = append(out, &headlessEnvelope{Type: "background_result", Payload: map[string]any{
				"session_id":      state.sessionID,
				"target_agent_id": e.TargetAgentID,
				"message_index":   e.MessageIndex,
				"content":         message.StripSystemReminderOverlay(e.Message.Content),
			}})
		}
	case agent.ContextNoticeEvent:
		// Durable context-pressure warning with no other headless channel (no
		// toast/info accompanies it). Gateway notifications are
		// fire-and-forget, so there is no card state for
		// ContextNoticeClearedEvent to retract; the cleared event stays
		// TUI-only by design.
		touch()
		if state.isSubscribed("context_notice") {
			out = append(out, &headlessEnvelope{Type: "context_notice", Payload: map[string]any{
				"session_id":    state.sessionID,
				"level":         e.Level,
				"message":       e.Message,
				"message_index": e.MessageIndex,
			}})
		}
	case agent.LSPFollowupEvent:
		// Durable late LSP diagnostics appended to the transcript at request
		// dispatch; no other headless channel carries them.
		touch()
		if state.isSubscribed("lsp_followup") {
			out = append(out, &headlessEnvelope{Type: "lsp_followup", Payload: map[string]any{
				"session_id":    state.sessionID,
				"message":       e.Message,
				"message_index": e.MessageIndex,
			}})
		}
	}
	if len(out) == 0 {
		if mutated {
			state.stampHeadlessSeq(true)
		}
		return nil
	}
	state.stampHeadlessSeq(true, out...)
	return out
}

func headlessRuleItems(rules []permission.Rule) []headlessRuleItem {
	if len(rules) == 0 {
		return nil
	}
	items := make([]headlessRuleItem, 0, len(rules))
	for _, rule := range rules {
		items = append(items, headlessRuleItem{Tool: rule.Permission, Pattern: rule.Pattern})
	}
	return items
}

func parseHeadlessDoneArgs(argsJSON string) (reason, report string) {
	if strings.TrimSpace(argsJSON) == "" {
		return "", ""
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", ""
	}
	reason, _ = args["reason"].(string)
	report, _ = args["report"].(string)
	return strings.TrimSpace(reason), strings.TrimSpace(report)
}

// isUnsupportedHeadlessCommand checks if a command is only available in TUI mode.
func isUnsupportedHeadlessCommand(content string) bool {
	fields := strings.Fields(strings.TrimSpace(content))
	if len(fields) == 0 {
		return false
	}
	switch fields[0] {
	case "/export":
		return true
	case "/resume":
		return len(fields) == 1
	case "/mcp":
		// Supported in headless mode when passed explicitly with an action.
		return len(fields) == 1
	default:
		return false
	}
}
