package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/keakon/golog/log"

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
	toolUseID   string
	fingerprint [32]byte
	frame       externalclaude.Frame
	done        bool
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

var (
	errExternalToolInputInvalid     = errors.New("external tool input is not valid JSON")
	errExternalToolIdentityMismatch = errors.New("external tool identity was reused with different arguments")
	errExternalToolDuplicateRequest = errors.New("external permission was already requested for this tool call")
)

// Claude Code may re-serialize the same input between tool_use and
// can_use_tool. Object order and whitespace must not change its fingerprint,
// while number literals such as 1 and 1.0 must remain distinct.
func externalToolFingerprint(name string, input json.RawMessage) ([32]byte, error) {
	canonical, err := canonicalToolCallArgs(input)
	if err != nil {
		return [32]byte{}, fmt.Errorf("%w: %w", errExternalToolInputInvalid, err)
	}
	data, err := json.Marshal(struct {
		Name  string
		Input json.RawMessage
	}{externalclaude.NormalizeToolName(name), canonical})
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(data), nil
}

func (w *externalWorker) bindExternalTool(id, name string, fingerprint [32]byte, request bool) error {
	if binding := w.toolBindings[id]; binding != nil {
		if binding.fingerprint != fingerprint {
			log.Debugf("external tool binding mismatch task=%v instance=%v tool_use_id=%v tool=%v bound=%x received=%x",
				w.taskID, w.instanceID, id, name, binding.fingerprint[:4], fingerprint[:4])
			return errExternalToolIdentityMismatch
		}
		if request && binding.requested {
			log.Debugf("external permission request repeated task=%v instance=%v tool_use_id=%v tool=%v",
				w.taskID, w.instanceID, id, name)
			return errExternalToolDuplicateRequest
		}
		binding.requested = binding.requested || request
		return nil
	}
	w.toolBindings[id] = &externalToolBinding{fingerprint: fingerprint, requested: request}
	return nil
}

func (w *externalWorker) beginExternalPermission(event externalclaude.Event) {
	request := event.Request
	if request == nil || strings.TrimSpace(event.RequestID) == "" || strings.TrimSpace(request.ToolUseID) == "" {
		// An empty request id cannot be correlated with a later replay, and an
		// empty tool id cannot bind the response to one tool call. Denying
		// without caching keeps a malformed request from becoming a reusable
		// authorization.
		w.redeliverExternalPermission(externalclaude.DenyPermission(event.RequestID, "invalid external permission request identity"))
		return
	}
	fingerprint, err := externalToolFingerprint(request.ToolName, request.Input)
	if err != nil {
		w.redeliverExternalPermission(externalclaude.DenyPermission(event.RequestID, "invalid external permission request input"))
		return
	}
	w.permissionMu.Lock()
	if outcome := w.permissionOutcomes[event.RequestID]; outcome != nil {
		matches := outcome.toolUseID == request.ToolUseID && outcome.fingerprint == fingerprint
		frame, done := outcome.frame, outcome.done
		w.permissionMu.Unlock()
		if !matches {
			// A repeated request id with a different tool or input is not a
			// retry. Replaying the first decision would authorize a different
			// tool call without evaluating it.
			w.redeliverExternalPermission(externalclaude.DenyPermission(event.RequestID, "external permission request id was reused for a different tool call"))
			return
		}
		if done {
			w.redeliverExternalPermission(frame)
		}
		return
	}
	outcome := &externalPermissionOutcome{
		toolUseID:   request.ToolUseID,
		fingerprint: fingerprint,
	}
	w.permissionOutcomes[event.RequestID] = outcome
	ctx, cancel := context.WithCancel(w.ctx)
	w.permissionRequests[event.RequestID] = externalPermissionRequest{cancel: cancel}
	w.permissionWG.Add(1)
	w.permissionMu.Unlock()
	go func() {
		defer w.permissionWG.Done()
		frame := w.evaluateExternalPermission(ctx, event, fingerprint)
		w.permissionMu.Lock()
		_, active := w.permissionRequests[event.RequestID]
		if !active || ctx.Err() != nil {
			frame = externalclaude.DenyPermission(event.RequestID, "external permission request cancelled")
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
		_, active = w.permissionRequests[event.RequestID]
		if !active || ctx.Err() != nil {
			frame = externalclaude.DenyPermission(event.RequestID, "external permission request cancelled")
		}
		delete(w.permissionRequests, event.RequestID)
		outcome.frame, outcome.done = frame, true
		pruneExternalPermissionOutcomesLocked(w.permissionOutcomes)
		resumed := false
		if len(w.permissionRequests) == 0 && w.State() == SubAgentStateWaitingMain {
			resumed = w.setState(SubAgentStateRunning, "external agent running")
		}
		w.permissionMu.Unlock()
		if resumed {
			w.persistExternalLifecycleState()
		}
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
	w.permissionWG.Go(func() {
		ctx, cancel := context.WithTimeout(w.ctx, 5*time.Second)
		defer cancel()
		_ = session.WriteFrameContext(ctx, frame)
	})
}

func (w *externalWorker) cancelExternalPermissions(requestID string) {
	w.permissionMu.Lock()
	for id, request := range w.permissionRequests {
		if requestID == "" || requestID == id {
			request.cancel()
			delete(w.permissionRequests, id)
		}
	}
	for id, outcome := range w.permissionOutcomes {
		if requestID == "" || requestID == id {
			outcome.frame = externalclaude.DenyPermission(id, "external permission request cancelled")
			outcome.done = true
		}
	}
	w.permissionMu.Unlock()
}

func (w *externalWorker) evaluateExternalPermission(ctx context.Context, event externalclaude.Event, fingerprint [32]byte) externalclaude.Frame {
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
	if request == nil || request.ToolName == "" || request.ToolUseID == "" {
		return deny("invalid external permission request")
	}
	w.externalToolStateMu.Lock()
	if err := w.bindExternalTool(request.ToolUseID, request.ToolName, fingerprint, true); err != nil {
		w.externalToolStateMu.Unlock()
		return deny(err.Error())
	}
	w.recordExternalToolUse(externalclaude.ContentBlock{ID: request.ToolUseID, Name: request.ToolName, Input: request.Input})
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
		w.persistExternalLifecycleState()
		// The 5-minute bound must cover waiting in the global confirm queue too,
		// not only the modal wait that starts after the queue admits this
		// request. Otherwise a long-running native confirm can park the worker
		// indefinitely before its own timer ever starts.
		confirmCtx, cancel := context.WithTimeout(ctx, externalPermissionTimeout)
		response, err := w.parent.AwaitConfirmWithRuleContext(confirmCtx, name, string(args), externalPermissionTimeout,
			decision.NeedsApprovalPaths, decision.AlreadyAllowedPaths, decision.NeedsApprovalRules, decision.AlreadyAllowedRules,
			externalPermissionSummary(request.ToolName))
		cancel()
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
			w.parent.processRuleIntent(evaluationName, response.RuleIntent, w.agentDefName)
			def := w.agentDef
			if current := w.parent.snapshotAgentConfigByName(w.agentDefName); current != nil {
				def = current
			}
			w.setRuleset(w.parent.buildSubAgentRuleset(def))
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
	if ctx.Err() != nil {
		w.settleExternalTool(request.ToolUseID, name, "external permission request cancelled", message.ToolStatusError, message.ToolRecoveryStateNotStarted)
		return externalclaude.DenyPermission(event.RequestID, "external permission request cancelled")
	}
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
