package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/keakon/chord/internal/externalclaude"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

type externalPermissionRequest struct {
	cancel context.CancelFunc
}

// externalPermissionOutcome remembers the answer to a request id. Claude Code
// can repeat a control request when it did not receive the first response;
// replaying the decision keeps that retry from hanging while still refusing a
// second authorization for the same tool call.
type externalPermissionOutcome struct {
	frame externalclaude.Frame
	done  bool
}

// externalPermissionOutcomeLimit bounds the replay cache. A replayed request id
// is rare, but every recorded outcome holds a complete frame including the
// tool's full input, so an unbounded cache grows with the worker's whole tool
// history.
const externalPermissionOutcomeLimit = 64

// pruneExternalPermissionOutcomesLocked drops settled outcomes once the cache
// is over its limit. Dropping one only costs a re-evaluation if the CLI replays
// that exact id, and a re-evaluation asks again rather than reusing a decision,
// so the fallback stays fail-closed. Callers must hold permissionMu.
func pruneExternalPermissionOutcomesLocked(outcomes map[string]*externalPermissionOutcome) {
	for len(outcomes) > externalPermissionOutcomeLimit {
		dropped := false
		for id, outcome := range outcomes {
			if outcome != nil && outcome.done {
				delete(outcomes, id)
				dropped = true
				break
			}
		}
		if !dropped {
			return
		}
	}
}

type externalToolBinding struct {
	fingerprint [32]byte
	requested   bool
	started     bool
	settled     bool
}

func (w *externalWorker) bindExternalTool(id, name string, input json.RawMessage, request bool) bool {
	data, err := json.Marshal(struct {
		Name  string
		Input json.RawMessage
	}{name, input})
	if err != nil {
		return false
	}
	fingerprint := sha256.Sum256(data)
	if binding := w.toolBindings[id]; binding != nil {
		if binding.fingerprint != fingerprint || request && binding.requested {
			return false
		}
		binding.requested = binding.requested || request
		return true
	}
	w.toolBindings[id] = &externalToolBinding{fingerprint: fingerprint, requested: request}
	return true
}

func (w *externalWorker) beginExternalPermission(event externalclaude.Event) {
	w.permissionMu.Lock()
	if outcome := w.permissionOutcomes[event.RequestID]; outcome != nil {
		frame, done := outcome.frame, outcome.done
		w.permissionMu.Unlock()
		if done {
			w.redeliverExternalPermission(frame)
		}
		return
	}
	outcome := &externalPermissionOutcome{}
	w.permissionOutcomes[event.RequestID] = outcome
	ctx, cancel := context.WithCancel(w.ctx)
	w.permissionRequests[event.RequestID] = externalPermissionRequest{cancel: cancel}
	w.permissionWG.Add(1)
	w.permissionMu.Unlock()
	go func() {
		defer w.permissionWG.Done()
		frame := w.evaluateExternalPermission(ctx, event)
		w.permissionMu.Lock()
		_, active := w.permissionRequests[event.RequestID]
		if active {
			delete(w.permissionRequests, event.RequestID)
		}
		if len(w.permissionRequests) == 0 && w.State() == SubAgentStateWaitingMain {
			w.setState(SubAgentStateRunning, "external agent running")
		}
		w.permissionMu.Unlock()
		// The write can block on stdin backpressure, so it must not hold the lock
		// that result handling needs to inspect pending permissions.
		writeFailed := false
		if active && ctx.Err() == nil {
			writeCtx, writeCancel := context.WithTimeout(ctx, 5*time.Second)
			if err := w.currentSession().WriteFrameContext(writeCtx, frame); err != nil {
				writeFailed = true
			}
			writeCancel()
		}
		w.permissionMu.Lock()
		outcome.frame, outcome.done = frame, true
		pruneExternalPermissionOutcomesLocked(w.permissionOutcomes)
		w.permissionMu.Unlock()
		cancel()
		if writeFailed {
			w.teardown()
		}
	}()
}

func (w *externalWorker) redeliverExternalPermission(frame externalclaude.Frame) {
	session := w.currentSession()
	if session == nil {
		return
	}
	w.permissionWG.Add(1)
	go func() {
		defer w.permissionWG.Done()
		ctx, cancel := context.WithTimeout(w.ctx, 5*time.Second)
		defer cancel()
		_ = session.WriteFrameContext(ctx, frame)
	}()
}

func (w *externalWorker) cancelExternalPermissions(requestID string) {
	w.permissionMu.Lock()
	for id, request := range w.permissionRequests {
		if requestID == "" || requestID == id {
			request.cancel()
			delete(w.permissionRequests, id)
		}
	}
	w.permissionMu.Unlock()
}

func (w *externalWorker) evaluateExternalPermission(ctx context.Context, event externalclaude.Event) externalclaude.Frame {
	request := event.Request
	registered := false
	deny := func(reason string) externalclaude.Frame {
		if registered {
			w.externalToolStateMu.Lock()
			w.settleExternalTool(request.ToolUseID, w.toolName(request.ToolUseID), reason, message.ToolStatusError, message.ToolRecoveryStateNotStarted)
			w.externalToolStateMu.Unlock()
		}
		return externalclaude.DenyPermission(event.RequestID, reason)
	}
	if request == nil || request.ToolName == "" || request.ToolUseID == "" || !json.Valid(request.Input) {
		return deny("invalid external permission request")
	}
	w.externalToolStateMu.Lock()
	if !w.bindExternalTool(request.ToolUseID, request.ToolName, request.Input, true) {
		w.externalToolStateMu.Unlock()
		return deny("external tool identity was reused")
	}
	w.handleExternalToolUse(externalclaude.ContentBlock{ID: request.ToolUseID, Name: request.ToolName, Input: request.Input})
	registered = true
	w.externalToolStateMu.Unlock()
	if !externalclaude.ManagedTool(request.ToolName) {
		return deny("external tool is not part of the managed tool set")
	}
	var executionFlags struct {
		Background bool   `json:"run_in_background"`
		Isolation  string `json:"isolation"`
	}
	_ = json.Unmarshal(request.Input, &executionFlags)
	if executionFlags.Background || executionFlags.Isolation != "" {
		return deny("external execution must remain in the managed foreground checkout")
	}
	if w.PersistenceHealth().State != PersistenceHealthy {
		return deny("external worker persistence requires recovery")
	}
	mapping, chordInput, mapped := externalclaude.MapTool(request.ToolName, request.Input)
	name, args := request.ToolName, request.Input
	evaluationName := externalclaude.NormalizeToolName(request.ToolName)
	if mapped {
		name, args = mapping.ChordName, chordInput
		evaluationName = mapping.ChordName
	}
	decision := evaluateToolPermissionInDir(w.currentRuleset(), evaluationName, args, w.pathScope())
	action := normalizeToolPermissionAction(evaluationName, decision.Action)
	if !mapped && !w.currentRuleset().LastSpecificToolMatch(evaluationName, decision.MatchArgument).Found {
		return deny("external tool requires an explicit role rule")
	}
	if action == permission.ActionDeny {
		return deny("denied by the worker role's permission rules")
	}
	ctx = withRulePathScope(tools.WithTaskID(tools.WithAgentID(ctx, w.instanceID), w.taskID), w.pathScope())
	ctx = tools.WithToolCallID(ctx, request.ToolUseID)
	if action == permission.ActionAsk || evaluationName == "ExitPlanMode" {
		w.setState(SubAgentStateWaitingMain, externalPermissionSummary(request.ToolName))
		response, err := w.parent.AwaitConfirmWithRuleContext(ctx, name, string(args), externalPermissionTimeout,
			decision.NeedsApprovalPaths, decision.AlreadyAllowedPaths, decision.NeedsApprovalRules, decision.AlreadyAllowedRules,
			externalPermissionSummary(request.ToolName))
		if err != nil {
			return deny("external permission request cancelled")
		}
		if !response.Approved {
			return deny("denied by the user")
		}
		if response.FinalArgsJSON != "" && response.FinalArgsJSON != string(args) {
			return deny("edited external arguments require a new tool request")
		}
		if response.RuleIntent != nil {
			w.parent.processRuleIntent(name, response.RuleIntent, w.agentDefName)
			w.setRuleset(w.parent.buildSubAgentRuleset(w.agentDef))
		}
	}
	updatedInput := request.Input
	if evaluationName == tools.NameQuestion {
		w.setState(SubAgentStateWaitingMain, externalPermissionSummary(request.ToolName))
		var err error
		updatedInput, err = w.answerExternalQuestion(ctx, request.Input)
		if err != nil {
			return deny("external question cancelled or unavailable")
		}
	}
	if ctx.Err() != nil || evaluateToolPermissionInDir(w.currentRuleset(), evaluationName, args, w.pathScope()).Action == permission.ActionDeny {
		return deny("external permission no longer valid")
	}
	w.externalToolStateMu.Lock()
	defer w.externalToolStateMu.Unlock()
	if w.PersistenceHealth().State != PersistenceHealthy {
		return externalclaude.DenyPermission(event.RequestID, "external tool intent could not be persisted")
	}
	if err := w.persistExternalToolStarted(request.ToolUseID, name, &message.ToolArgsAudit{
		OriginalArgsJSON: string(request.Input), EffectiveArgsJSON: string(updatedInput),
	}); err != nil {
		w.setResultError(err)
		w.settleExternalTool(request.ToolUseID, name, "external authorization could not be persisted", message.ToolStatusError, message.ToolRecoveryStateNotStarted)
		return externalclaude.DenyPermission(event.RequestID, "external authorization could not be persisted")
	}
	w.toolBindings[request.ToolUseID].started = true
	w.captureToolPreWrite(request.ToolUseID, name, args)
	return externalclaude.AllowPermission(event.RequestID, updatedInput)
}

func (w *externalWorker) answerExternalQuestion(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var input struct {
		Questions []struct {
			Question, Header string
			Options          []struct{ Label, Description string }
			Multiple         bool `json:"multiSelect"`
		}
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil, fmt.Errorf("decode external question: %w", err)
	}
	args := tools.QuestionArgs{}
	for _, q := range input.Questions {
		item := tools.QuestionItem{Question: q.Question, Header: q.Header, Multiple: q.Multiple, ResponsePolicy: tools.QuestionPolicyUserRequired}
		for i, option := range q.Options {
			item.Options = append(item.Options, tools.QuestionOption{ID: fmt.Sprint(i), Label: option.Label, Description: option.Description})
		}
		args.Questions = append(args.Questions, item)
	}
	result, err := w.parent.ExecuteQuestion(ctx, args)
	if err != nil {
		return nil, err
	}
	if len(result.Answers) != len(input.Questions) {
		return nil, fmt.Errorf("external question has unanswered items")
	}
	answers := map[string]string{}
	for i, answer := range result.Answers {
		if answer.Outcome == tools.QuestionOutcomeAnswered {
			answers[input.Questions[i].Question] = strings.Join(answer.Selected, ", ")
		}
	}
	if len(answers) != len(input.Questions) {
		return nil, fmt.Errorf("external question has unanswered items")
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	fields["answers"], _ = json.Marshal(answers)
	return json.Marshal(fields)
}

func externalPermissionSummary(toolName string) string {
	switch toolName {
	case "AskUserQuestion":
		return "External agent is waiting for your answer"
	case "ExitPlanMode":
		return "External agent requests plan approval"
	default:
		return "External agent requests permission: " + toolName
	}
}
