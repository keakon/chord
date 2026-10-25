package main

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

func emitHeadlessLocalShellResult(out *stdoutWriter, command, output string, err error) {
	payload := map[string]any{
		"command": command,
		"output":  output,
		"failed":  err != nil,
	}
	if err != nil {
		payload["error"] = err.Error()
	}
	out.emit(headlessEnvelope{Type: "local_shell_result", Payload: payload})
}

// handleHeadlessCommand processes a single command from stdin.
func handleHeadlessCommand(cmd headlessCommand, backend headlessBackend, state *headlessState, out *stdoutWriter) {
	switch cmd.Type {
	case "mcp":
		handleHeadlessMCPCommand(cmd, state, out)
	case "image_artifact":
		handleHeadlessImageArtifact(cmd, backend, out)
	case "subscribe":
		subs := make(map[string]bool, len(cmd.Events))
		for _, ev := range cmd.Events {
			if headlessEventTypes[ev] {
				subs[ev] = true
			}
		}
		state.mu.Lock()
		state.subscriptions = subs
		state.mu.Unlock()
		out.emit(headlessEnvelope{
			Type: "subscribe_response",
			Payload: map[string]any{
				"events": cmd.Events,
			},
		})

	case "status":
		// Fill an empty role cache before the snapshot. CurrentRole can block,
		// so it stays outside state.mu. The snapshot below re-reads state.role
		// under the same lock as seq so a concurrent RoleChangedEvent cannot
		// bind an older role to a newer version.
		headlessCurrentRole(backend, state)
		workDirSnapshot := headlessWorkDirSnapshot(backend)
		questionPage, questionSnapshot, hasQuestionSnapshot := headlessCurrentQuestions(backend, agent.QuestionSnapshotQuery{BindingID: cmd.BindingID, AfterID: cmd.QuestionsAfterID, RequestIDs: cmd.QuestionIDs})
		if hasQuestionSnapshot && questionPage.Error != "" {
			out.emit(headlessEnvelope{Type: "error", Payload: map[string]string{"message": questionPage.Error}})
			return
		}
		questionSession := questionPage.SessionID
		state.mu.Lock()
		workDirChanged := workDirSnapshot != state.workDir
		if workDirChanged {
			state.workDir = workDirSnapshot
			state.updatedAt = time.Now()
		}
		currentRole := state.role
		sid := state.sessionID
		busy := state.busy
		phase := state.phase
		phaseDetail := state.phaseDetail
		pendingConfirm := state.pendingConfirm
		pendingQuestions := state.pendingQuestionSnapshots()
		var allQuestions []*headlessQuestionPayload
		if hasQuestionSnapshot && (sid == "" || sid == questionSession) {
			sid = questionSession
			allQuestions = questionSnapshot
			pendingQuestions = make([]*headlessQuestionPayload, 0)
			for _, q := range allQuestions {
				if q.Outcome == "" {
					pendingQuestions = append(pendingQuestions, q)
				}
			}
		} else {
			// Minimal backends have no durable history query. Bound their cached
			// projection as well, rather than handing the entire cache to stdout.
			allQuestions = headlessCachedQuestions(state)
			questionPage.BindingID = ""
			questionPage.NextAfterID = ""
		}

		slices.SortFunc(allQuestions, func(x, y *headlessQuestionPayload) int { return strings.Compare(x.RequestID, y.RequestID) })
		pendingHandoff := state.pendingHandoff
		lastError := state.lastError
		lastOutcome := state.lastOutcome
		updatedAt := state.updatedAt
		workDir := state.workDir
		workspace := state.workspace
		mcpSnapshot := state.mcp.Clone()
		runtimeID := state.runtimeID
		sessionEpoch := state.sessionEpoch
		inputQueue := state.inputQueue
		seq := state.stampHeadlessSeq(workDirChanged)
		state.mu.Unlock()
		out.emit(headlessEnvelope{
			Type: "status_response",
			Seq:  seq,
			Payload: map[string]any{
				"session_id":              sid,
				"workspace":               workspace,
				"runtime_id":              runtimeID,
				"session_epoch":           sessionEpoch,
				"busy":                    busy,
				"phase":                   phase,
				"phase_detail":            phaseDetail,
				"pending_confirm":         pendingConfirm,
				"pending_questions":       pendingQuestions,
				"questions":               allQuestions,
				"question_binding_id":     questionPage.BindingID,
				"questions_next_after_id": questionPage.NextAfterID,
				"pending_handoff":         pendingHandoff,
				"last_error":              lastError,
				"last_outcome":            lastOutcome,
				"current_role":            currentRole,
				"updated_at":              updatedAt.Format(time.RFC3339),
				"workdir":                 headlessWorkDirPayload(workDir),
				"mcp":                     mcpSnapshot,
				"running_jobs":            headlessRunningJobs(backend),
				"paused_inputs":           headlessPausedInputProjection(backend),
				"input_queue":             inputQueue,
			},
		})

	case "local_shell":
		command := strings.TrimSpace(cmd.Command)
		if command == "" {
			command = strings.TrimSpace(cmd.Content)
		}
		if command == "" {
			emitHeadlessLocalShellResult(out, command, "", fmt.Errorf("empty local shell command"))
			return
		}
		output, err := tools.RunLocalShellCapture(context.Background(), "", command)
		emitHeadlessLocalShellResult(out, command, output, err)

	case "input_queue":
		handleHeadlessInputQueue(cmd, backend, out)
	case "send":
		if headlessSendWithPolicy(cmd, backend, state, out) {
			return
		}
		if isHeadlessMCPCommand(cmd.Content) {
			handleHeadlessMCPCommand(cmd, state, out)
			return
		}
		content := cmd.Content
		if strings.TrimSpace(content) == "/models" {
			content = "/models status"
		}
		if strings.TrimSpace(content) == "/role" {
			content = "/role status"
		}
		if isUnsupportedHeadlessCommand(content) {
			emitHeadlessInputRejected(cmd.RequestID, "command is only available in local TUI mode", state, out)
			fields := strings.Fields(content)
			out.emit(headlessEnvelope{
				Type: "error",
				Payload: map[string]string{
					"message": fields[0] + " is only available in local TUI mode",
				},
			})
			return
		}
		if strings.TrimSpace(content) == "" {
			emitHeadlessInputRejected(cmd.RequestID, "empty input", state, out)
			return
		}
		// In headless mode, if a confirm_request, question_request, or handoff_request
		// is pending and the user sends a regular message (not /allow, /deny,
		// /answer, or handoff), auto-dismiss the pending interaction so the agent
		// can continue processing the new user message. Without this, the
		// interaction blocks forever (default timeout is 0 = infinite) and the user
		// message is queued but never consumed.
		state.mu.Lock()
		pendingConfirm := state.pendingConfirm
		pendingHandoff := state.pendingHandoff
		state.mu.Unlock()
		// Admit input before resolving any blocked interaction. Admission never
		// waits for consumption, which may itself depend on that resolution.
		if !backend.SendUserMessageWithReceipt(content, cmd.RequestID) {
			emitHeadlessInputRejected(cmd.RequestID, "agent is shutting down", state, out)
			return
		}
		if pendingConfirm != nil {
			log.Infof("headless: auto-denying pending confirm for new user message request_id=%v tool_name=%v", pendingConfirm.RequestID, pendingConfirm.ToolName)
			backend.ResolveConfirm("deny", "", "", "", pendingConfirm.RequestID)
			state.mu.Lock()
			if state.pendingConfirm != nil && state.pendingConfirm.RequestID == pendingConfirm.RequestID {
				state.pendingConfirm = nil
				state.updatedAt = time.Now()
				// No cancelled envelope exists for confirm, but the cache
				// change still needs a newer version so a status snapshot
				// copied while the request was pending cannot restore it.
				state.stampHeadlessSeq(true)
			}
			state.mu.Unlock()
		}
		if pendingHandoff != nil {
			log.Infof("headless: auto-cancelling pending handoff for new user message request_id=%v plan_path=%v", pendingHandoff.RequestID, pendingHandoff.PlanPath)
			if hb, ok := backend.(headlessHandoffBackend); ok {
				hb.ResolveHandoff(pendingHandoff.RequestID, "cancel", "", "")
			}
			// The client still holds this request as pending; tell subscribers it
			// is gone before the new message starts a fresh turn. The stamp and
			// the enqueue share one ordered section (see ordered), and the emit
			// inside it only ever waits on the channel buffer, so stdout
			// backpressure never stalls state.mu — only other push sections.
			out.ordered(func() {
				state.mu.Lock()
				cleared := false
				if state.pendingHandoff != nil && state.pendingHandoff.RequestID == pendingHandoff.RequestID {
					state.pendingHandoff = nil
					cleared = true
					state.updatedAt = time.Now()
				}
				var cancelled *headlessEnvelope
				if state.isSubscribed("handoff_cancelled") {
					cancelled = &headlessEnvelope{Type: "handoff_cancelled", Payload: map[string]string{
						"request_id": pendingHandoff.RequestID,
						"reason":     headlessHandoffCancelledReasonSuperseded,
					}}
				}
				if cleared || cancelled != nil {
					// Bump even without a cancelled envelope: the pending cache
					// changed, so a status snapshot copied earlier must not restore it.
					state.stampHeadlessSeq(true, cancelled)
				}
				state.mu.Unlock()
				if cancelled != nil {
					out.emit(cancelled)
				}
			})
		}

	case "models":
		modelsBackend, ok := backend.(headlessModelsBackend)
		if !ok {
			out.emit(headlessEnvelope{Type: "error", Payload: map[string]string{"message": "models command is not supported by this backend"}})
			return
		}
		handleHeadlessModelsCommand(cmd, modelsBackend, out)

	case "role":
		roleBackend, ok := backend.(headlessRoleBackend)
		if !ok {
			out.emit(headlessEnvelope{Type: "error", Payload: map[string]string{"message": "role command is not supported by this backend"}})
			return
		}
		handleHeadlessRoleCommand(cmd, roleBackend, state, out)

	case "handoff":
		handoffBackend, ok := backend.(headlessHandoffBackend)
		if !ok {
			out.emit(headlessEnvelope{Type: "error", Payload: map[string]string{"message": "handoff command is not supported by this backend"}})
			return
		}
		state.mu.Lock()
		pending := state.pendingHandoff
		state.mu.Unlock()
		if pending == nil || strings.TrimSpace(pending.RequestID) == "" || (cmd.RequestID != "" && cmd.RequestID != pending.RequestID) {
			out.emit(headlessEnvelope{Type: "error", Payload: map[string]string{"message": "no matching pending handoff"}})
			return
		}
		switch strings.TrimSpace(cmd.Action) {
		case "", "accept", "allow":
			agentName := strings.TrimSpace(cmd.Agent)
			if agentName == "" {
				agentName = defaultHandoffAgent(pending.Agents)
				if agentName == "" {
					out.emit(headlessEnvelope{Type: "error", Payload: map[string]string{"message": "no eligible handoff agent"}})
					return
				}
			} else if !headlessHandoffAgentAvailable(pending.Agents, agentName) {
				out.emit(headlessEnvelope{Type: "error", Payload: map[string]string{"message": "handoff target \"" + agentName + "\" is not available; choose one of the offered agents or cancel"}})
				return
			}
			if pool := strings.TrimSpace(cmd.Pool); pool != "" {
				if err := handoffBackend.SetAgentModelPool(agentName, pool); err != nil {
					out.emit(headlessEnvelope{Type: "error", Payload: map[string]string{"message": err.Error()}})
					return
				}
			}
			handoffBackend.ResolveHandoff(pending.RequestID, "approve", agentName, "")
		case "deny", "reject":
			reason := strings.TrimSpace(cmd.DenyReason)
			if reason == "" {
				reason = "Handoff rejected from headless client."
			}
			handoffBackend.ResolveHandoff(pending.RequestID, "deny", "", reason)
		case "cancel":
			handoffBackend.ResolveHandoff(pending.RequestID, "cancel", "", "")
		default:
			out.emit(headlessEnvelope{Type: "error", Payload: map[string]string{"message": "unsupported handoff action: " + cmd.Action}})
			return
		}
		state.mu.Lock()
		if state.pendingHandoff != nil && state.pendingHandoff.RequestID == pending.RequestID {
			state.pendingHandoff = nil
			state.updatedAt = time.Now()
			state.stampHeadlessSeq(true)
		}
		state.mu.Unlock()

	case "confirm":
		ruleIntent, err := parseHeadlessRuleIntent(cmd.RulePattern, cmd.RuleTool, cmd.RuleScope)
		if err != nil {
			out.emit(headlessEnvelope{
				Type: "error",
				Payload: map[string]string{
					"message": err.Error(),
				},
			})
			return
		}
		if ruleIntent != nil {
			if withRuleIntent, ok := backend.(headlessBackendWithRuleIntent); ok {
				withRuleIntent.ResolveConfirmWithRuleIntent(cmd.Action, cmd.FinalArgsJSON, cmd.EditSummary, cmd.DenyReason, cmd.RequestID, ruleIntent)
			} else {
				backend.ResolveConfirm(cmd.Action, cmd.FinalArgsJSON, cmd.EditSummary, cmd.DenyReason, cmd.RequestID)
			}
		} else {
			backend.ResolveConfirm(cmd.Action, cmd.FinalArgsJSON, cmd.EditSummary, cmd.DenyReason, cmd.RequestID)
		}
		state.mu.Lock()
		if state.pendingConfirm != nil && state.pendingConfirm.RequestID == cmd.RequestID {
			state.pendingConfirm = nil
			state.updatedAt = time.Now()
			state.stampHeadlessSeq(true)
		}
		state.mu.Unlock()

	case "question":
		handleHeadlessQuestionCommand(cmd, backend, out)

	case "cancel":
		backend.CancelCurrentTurn()
		state.mu.Lock()
		state.pendingOutcome = "cancelled"
		state.updatedAt = time.Now()
		state.stampHeadlessSeq(true)
		state.mu.Unlock()

	default:
		out.emit(headlessEnvelope{
			Type: "error",
			Payload: map[string]string{
				"message": "unknown command type: " + cmd.Type,
			},
		})
	}
}

// headlessHandoffAgentAvailable reports whether name is one of the agents the
// pending handoff offered. External clients may only pick from the offered
// list; anything else (unknown role, SubAgent, or the current active role) is
// rejected before the runtime resolves the handoff.
func headlessHandoffAgentAvailable(options []agent.HandoffAgentOption, name string) bool {
	for _, opt := range options {
		if strings.TrimSpace(opt.Name) == name {
			return true
		}
	}
	return false
}

func defaultHandoffAgent(options []agent.HandoffAgentOption) string {
	for _, opt := range options {
		if opt.Default && strings.TrimSpace(opt.Name) != "" {
			return strings.TrimSpace(opt.Name)
		}
	}
	for _, opt := range options {
		if strings.TrimSpace(opt.Name) != "" {
			return strings.TrimSpace(opt.Name)
		}
	}
	// No eligible agent was offered: returning an empty name forces the caller
	// to report an error instead of inventing a target.
	return ""
}

func parseHeadlessRuleIntent(pattern, tool, scope string) (*agent.ConfirmRuleIntent, error) {
	pattern = strings.TrimSpace(pattern)
	tool = strings.TrimSpace(tool)
	scope = strings.TrimSpace(scope)
	if pattern == "" && tool == "" && scope == "" {
		return nil, nil
	}
	if pattern == "" {
		return nil, fmt.Errorf("confirm rule intent requires non-empty rule_pattern")
	}
	if scope == "" {
		scope = "session"
	}
	var ruleScope int
	switch strings.ToLower(scope) {
	case "session":
		ruleScope = int(permission.ScopeSession)
	case "project":
		ruleScope = int(permission.ScopeProject)
	case "user_global", "user-global", "userglobal":
		ruleScope = int(permission.ScopeUserGlobal)
	default:
		return nil, fmt.Errorf("invalid rule_scope %q (expected session|project|user_global)", scope)
	}
	return &agent.ConfirmRuleIntent{
		Rules: []agent.ConfirmRule{{Tool: tool, Pattern: pattern}},
		Scope: ruleScope,
	}, nil
}
