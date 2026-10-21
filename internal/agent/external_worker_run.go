package agent

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/externalclaude"
	"github.com/keakon/chord/internal/externalsupervisor"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
)

const externalPermissionTimeout = 5 * time.Minute

func (w *externalWorker) runExternalWorker() {
	defer w.doneOnce.Do(func() { close(w.done) })
	w.promptMu.Lock()
	var input externalTaskInput
	for _, candidate := range w.inputs {
		if candidate.State == message.ToolRecoveryStateNotStarted || candidate.State == externalInputQueued {
			input = candidate
			break
		}
	}
	for i, queued := range w.promptQueue {
		if queued.ID == input.ID {
			w.promptQueue = append(w.promptQueue[:i], w.promptQueue[i+1:]...)
			break
		}
	}
	w.promptMu.Unlock()
	for {
		if w.closing.Load() || w.ctx.Err() != nil {
			if !w.closePending.Load() {
				w.finishCancelled("external agent cancelled")
			}
			return
		}
		session, err := w.startSession(input, w.resumeNext)
		if err != nil {
			w.promptMu.Lock()
			w.processEnded = w.currentSession() == nil || w.currentSession().ProcessEnded()
			_ = w.persistExternalSessionLocked()
			w.promptMu.Unlock()
			if w.closePending.Load() {
				return
			}
			if w.closing.Load() || w.ctx.Err() != nil {
				w.finishCancelled("external agent cancelled")
			} else {
				w.finishFailure(err)
			}
			return
		}
		w.setSession(session)
		stop, next := w.consumeSession(session)
		if !stop || w.closing.Load() || w.ctx.Err() != nil {
			session.Stop(externalSessionStopGrace)
		} else {
			session.Finish(externalSessionStopGrace)
		}
		// No completion or slot release may precede process-group teardown.
		for event := range session.Events() {
			if event.Type == externalclaude.TypeResult && event.ParentToolUseID == "" {
				w.setResultError(fmt.Errorf("external agent sent a duplicate result"))
			} else if event.Type == externalclaude.TypeUser {
				w.handleExternalEvent(event)
			}
		}
		w.cancelExternalPermissions("")
		w.permissionWG.Wait()
		w.promptMu.Lock()
		w.processEnded = session.ProcessEnded()
		persistErr := w.persistExternalSessionLocked()
		w.promptMu.Unlock()
		if persistErr != nil {
			w.setResultError(persistErr)
		}
		if err := session.Wait(); err != nil && !w.closing.Load() && w.ctx.Err() == nil {
			_ = w.settleUnresolvedExternalTools()
			w.finishFailure(fmt.Errorf("external agent exit: %w", err))
			return
		}
		if w.closing.Load() || w.ctx.Err() != nil {
			_ = w.settleUnresolvedExternalTools()
			if !w.closePending.Load() {
				w.finishCancelled("external agent cancelled")
			}
			return
		}
		if err := w.getResultError(); err != nil {
			_ = w.settleUnresolvedExternalTools()
			w.finishFailure(err)
			return
		}
		if next.ID == "" {
			if err := w.settleUnresolvedExternalTools(); err != nil {
				w.finishFailure(err)
				return
			}
			if w.resultSeen {
				w.finishSuccess(w.resultText)
			} else {
				w.finishFailure(fmt.Errorf("external agent exited without a result"))
			}
			return
		}
		// The next turn resumes the same external session id in a new process;
		// wait for the old one to exit so two processes never write the same
		// session transcript at once.
		input = next
		w.resumeNext = true
	}
}

func (w *externalWorker) setSession(session *externalclaude.Session) {
	w.sessionMu.Lock()
	w.session = session
	w.sessionMu.Unlock()
}

func (w *externalWorker) startSession(input externalTaskInput, resume bool) (*externalclaude.Session, error) {
	if w.parent == nil {
		return nil, fmt.Errorf("external worker has no parent agent")
	}
	if err := w.ensureSessionID(); err != nil {
		return nil, err
	}
	sessionID := w.currentSessionID()
	if sessionID != "" && !externalclaude.ValidSessionID(sessionID) {
		return nil, fmt.Errorf("external session id %q is not a valid UUID", sessionID)
	}
	if resume && sessionID == "" {
		return nil, fmt.Errorf("external session cannot resume without a session id")
	}
	args, err := externalclaude.BuildArgs(externalclaude.LaunchOptions{
		SessionID:       sessionID,
		Resume:          resume,
		DisallowedTools: w.derivedDisallowedTools(),
		LaunchArgs:      w.backend.LaunchArgs,
	})
	if err != nil {
		return nil, fmt.Errorf("external launch arguments: %w", err)
	}
	env := make(map[string]string, len(w.backend.Env))
	for key, value := range w.backend.Env {
		env[key] = os.ExpandEnv(value)
	}
	w.promptMu.Lock()
	// The run identity is persisted before the supervisor exists: a crash
	// between the spawn and Chord persisting the exit can then be matched to
	// exactly one receipt, and a missing receipt keeps resume refused.
	runID, err := externalsupervisor.NewRunID()
	if err != nil {
		w.promptMu.Unlock()
		return nil, err
	}
	w.runID = runID
	w.processEnded = false
	err = w.persistExternalSessionLocked()
	w.promptMu.Unlock()
	if err != nil {
		return nil, err
	}
	w.parent.pruneExternalSupervisorReceipts()
	session, err := externalclaude.Start(externalclaude.ProcessConfig{
		Command: w.backend.EffectiveCommand(),
		Args:    args,
		Env:     env,
		Dir:     w.workDir,
		OnStderr: func(line string) {
			log.Warnf("external agent stderr instance=%v line=%v", w.instanceID, externalclaude.RedactDiagnostic(line, env))
		},
		// The process runs under its own supervisor: Chord can be killed
		// mid-task, and only a process that outlives it can still tear the
		// worker's process group down.
		Supervisor: &externalclaude.SupervisorConfig{RunID: runID, ReceiptDir: w.supervisorReceiptDir()},
	})
	if err != nil {
		return nil, fmt.Errorf("start external agent: %w", err)
	}
	w.setSession(session)
	w.promptMu.Lock()
	w.processEnded = false
	err = w.persistExternalSessionLocked()
	w.promptMu.Unlock()
	initialized := false
	defer func() {
		if !initialized {
			session.Stop(externalSessionStopGrace)
			for range session.Events() {
			}
			_ = session.Wait()
		}
	}()
	if err != nil {
		return nil, err
	}
	initCtx, cancel := context.WithTimeout(w.ctx, 15*time.Second)
	defer cancel()
	systemPrompt := strings.TrimSpace(w.agentDef.SystemPrompt + "\n\n" + w.agentDef.PromptAppend)
	if block := w.externalResultContractPromptBlock(); block != "" {
		systemPrompt = strings.TrimSpace(systemPrompt + "\n\n" + block)
	}
	if err := session.Initialize(initCtx, systemPrompt); err != nil {
		session.Stop(externalSessionStopGrace)
		return nil, err
	}
	w.beginExternalStreamSegment()
	if err := w.sendExternalInput(input); err != nil {
		session.Stop(externalSessionStopGrace)
		return nil, fmt.Errorf("send external agent prompt: %w", err)
	}
	w.resultSeen = false
	w.resultText = ""
	w.setResultError(nil)
	initialized = true
	return session, nil
}

// supervisorReceiptDir is where a supervisor leaves the lifecycle receipt of
// every run. Chord never deletes them per run: a crash between the spawn and
// the exit persist is exactly when the receipt is the only proof, so the
// directory is bounded by age instead.
func (w *externalWorker) supervisorReceiptDir() string {
	if w == nil || w.parent == nil {
		return ""
	}
	return w.parent.externalSupervisorReceiptDir()
}

// externalSupervisorReceiptRetention bounds how long a settled run's receipt
// stays available to prove a crash-time teardown.
const externalSupervisorReceiptRetention = 30 * 24 * time.Hour

// pruneExternalSupervisorReceipts runs at most once per agent: receipts are
// written for every run, so without a sweep the directory grows forever.
func (a *MainAgent) pruneExternalSupervisorReceipts() {
	if a == nil {
		return
	}
	dir := a.externalSupervisorReceiptDir()
	if dir == "" {
		return
	}
	a.supervisorReceiptPruneOnce.Do(func() {
		cutoff := time.Now().Add(-externalSupervisorReceiptRetention)
		if err := externalsupervisor.PruneReceipts(dir, cutoff); err != nil {
			log.Warnf("prune external supervisor receipts: %v", err)
		}
	})
}

// externalSupervisorReceiptDir is where supervisors leave the lifecycle
// receipts of every run. Receipts are evidence for resume after a crash, so
// they live outside any one session directory.
func (a *MainAgent) externalSupervisorReceiptDir() string {
	if a == nil {
		return ""
	}
	locator := a.pathLocator
	if locator == nil {
		var err error
		if locator, err = config.DefaultPathLocator(); err != nil {
			return ""
		}
	}
	stateDir := strings.TrimSpace(locator.StateDir)
	if stateDir == "" {
		return ""
	}
	return filepath.Join(stateDir, "external-supervisor")
}

func (w *externalWorker) ensureSessionID() error {
	w.sessionMu.Lock()
	defer w.sessionMu.Unlock()
	if w.sessionID != "" {
		return nil
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return fmt.Errorf("generate external session id: %w", err)
	}
	w.sessionID = externalclaude.NewSessionID(random)
	return nil
}

func (w *externalWorker) consumeSession(session *externalclaude.Session) (stop bool, next externalTaskInput) {
	for {
		select {
		case event, ok := <-session.Events():
			if !ok {
				return w.finishExited(session)
			}
			terminal, nextPrompt := w.handleExternalEvent(event)
			if terminal {
				return true, nextPrompt
			}
		case <-w.ctx.Done():
			// A teardown already signalled the close handler and set closing;
			// re-emitting a close request here would settle the same task twice.
			// Only an external ctx cancel (session switch / shutdown) still needs
			// to close the task.
			w.finishExternalStream()
			return true, externalTaskInput{}
		}
	}
}

func (w *externalWorker) finishExited(_ *externalclaude.Session) (bool, externalTaskInput) {
	w.finishExternalStream()
	if w.finished.Load() || w.closing.Load() {
		return true, externalTaskInput{}
	}
	if w.ctx.Err() != nil {
		return true, externalTaskInput{}
	}
	w.setResultError(fmt.Errorf("external agent exited without a result"))
	return true, externalTaskInput{}
}

func (w *externalWorker) handleExternalEvent(event externalclaude.Event) (terminal bool, nextPrompt externalTaskInput) {
	switch event.Type {
	case externalclaude.TypeSystem:
		if event.Subtype == "init" {
			if event.ClaudeCodeVersion != "" {
				// Diagnostic only: the client version never gates a launch, but a
				// failure report should be able to name the release that produced it.
				log.Debugf("external agent initialized instance=%v version=%v session=%v", w.instanceID, event.ClaudeCodeVersion, event.SessionID)
			}
			for _, name := range event.Tools {
				if !externalclaude.ManagedTool(name) {
					w.setResultError(fmt.Errorf("external tool catalogue contains an unmanaged tool"))
					return true, externalTaskInput{}
				}
			}
		}
	case externalclaude.TypeStreamEvent:
		if event.ParentToolUseID != "" {
			// Text streamed by a tool's nested agent is not this worker's answer:
			// only complete events are persisted, and the top-level result is
			// the task outcome.
			return false, externalTaskInput{}
		}
		if w.handleExternalStreamEvent(event) {
			return true, externalTaskInput{}
		}
	case externalclaude.TypeAssistant:
		if event.ParentToolUseID == "" {
			if text := event.AssistantText(); text != "" {
				if err := w.persistExternalMessage(message.Message{Role: message.RoleAssistant, Content: text}); err != nil {
					w.setResultError(err)
					return true, externalTaskInput{}
				}
				// A complete assistant message ends the live segment that
				// streamed it: the card must hold exactly this message, and
				// the next message (usually after a tool card) gets its own.
				w.commitExternalStream(text)
				w.finishExternalStream()
				w.startExternalStreamSegment()
				w.noteAssistantText(text)
			}
		}
		for _, use := range event.ToolUses() {
			w.externalToolStateMu.Lock()
			w.handleExternalToolUse(use)
			w.externalToolStateMu.Unlock()
		}
	case externalclaude.TypeUser:
		if event.Message != nil {
			for _, block := range event.Message.Content {
				if block.Type == "tool_result" {
					w.externalToolStateMu.Lock()
					known := w.handleExternalToolResult(block)
					w.externalToolStateMu.Unlock()
					if !known {
						w.finishExternalStream()
						w.setResultError(fmt.Errorf("external tool result has no matching tool call: %s", strings.TrimSpace(block.ToolUseID)))
						return true, externalTaskInput{}
					}
				}
			}
		}
	case externalclaude.TypeControlRequest:
		if event.Request == nil {
			w.finishExternalStream()
			w.setResultError(fmt.Errorf("external control request is missing its request body"))
			return true, externalTaskInput{}
		}
		if event.Request.Subtype == externalclaude.ControlSubtypeCanUseTool {
			w.beginExternalPermission(event)
		} else {
			w.finishExternalStream()
			w.setResultError(fmt.Errorf("unsupported external control request subtype %q", event.Request.Subtype))
			return true, externalTaskInput{}
		}
	case externalclaude.TypeControlCancelRequest:
		w.cancelExternalPermissions(event.RequestID)
	case externalclaude.TypeResult:
		if event.ParentToolUseID != "" {
			return false, externalTaskInput{}
		}
		return w.handleExternalResult(event)
	}
	return false, externalTaskInput{}
}

func (w *externalWorker) beginExternalStreamSegment() {
	w.lastAssistantMu.Lock()
	w.lastAssistant = ""
	w.lastAssistantMu.Unlock()
	w.startExternalStreamSegment()
}

// startExternalStreamSegment opens a fresh live segment without dropping the
// last complete assistant message: the closing result may still repeat it.
func (w *externalWorker) startExternalStreamSegment() {
	w.streamRequestSeq++
	w.streamActive = false
}

func (w *externalWorker) streamIdentity() (uint64, uint64) {
	return 0, w.streamRequestSeq
}

func (w *externalWorker) handleExternalStreamEvent(event externalclaude.Event) bool {
	stream := event.StreamEvent
	if stream == nil {
		w.finishExternalStream()
		w.setResultError(fmt.Errorf("external stream event is missing its event body"))
		return true
	}
	switch stream.Type {
	case "content_block_delta":
		if stream.Delta == nil {
			w.finishExternalStream()
			w.setResultError(fmt.Errorf("external stream event %q is missing its delta", stream.Type))
			return true
		}
		switch stream.Delta.Type {
		case "text_delta":
			if stream.Delta.Text == "" {
				return false
			}
			w.streamActive = true
			turnID, requestSeq := w.streamIdentity()
			w.parent.emitToTUI(StreamTextEvent{
				Text: stream.Delta.Text, AgentID: w.instanceID,
				TurnID: turnID, RequestSeq: requestSeq,
			})
		case "input_json_delta", "thinking_delta", "signature_delta":
			// Tool input and Claude's reasoning are not visible answer text.
		default:
			w.finishExternalStream()
			w.setResultError(fmt.Errorf("unsupported external stream delta type %q", stream.Delta.Type))
			return true
		}
	case "content_block_start", "content_block_stop", "message_delta", "message_start", "message_stop", "ping":
		// These events carry framing or metadata; complete assistant/result
		// events remain the source of persisted content and settlement.
	default:
		w.finishExternalStream()
		w.setResultError(fmt.Errorf("unsupported external stream event type %q", stream.Type))
		return true
	}
	return false
}

func (w *externalWorker) commitExternalStream(text string) {
	if text == "" {
		return
	}
	// A commit without streamed deltas (a complete assistant message or a
	// distinct closing result) still opens a card, so the segment stays active
	// until its end event settles that card.
	w.streamActive = true
	turnID, requestSeq := w.streamIdentity()
	w.parent.emitToTUI(StreamTextCommitEvent{
		Text: text, AgentID: w.instanceID,
		TurnID: turnID, RequestSeq: requestSeq,
	})
}

func (w *externalWorker) finishExternalStream() {
	if !w.streamActive {
		return
	}
	w.streamActive = false
	turnID, requestSeq := w.streamIdentity()
	w.parent.emitToTUI(StreamSegmentEndedEvent{
		AgentID: w.instanceID, TurnID: turnID, RequestSeq: requestSeq,
	})
}

func (w *externalWorker) handleExternalResult(event externalclaude.Event) (bool, externalTaskInput) {
	if w.resultSeen {
		w.setResultError(fmt.Errorf("external agent sent a duplicate result"))
		return true, externalTaskInput{}
	}
	w.resultSeen = true
	text := strings.TrimSpace(event.Result)
	if text == "" {
		text = w.lastAssistantText()
	}
	lastAssistant := w.lastAssistantText()
	// A complete assistant message was already committed when it arrived. Only
	// uncommitted deltas or a distinct closing text need a commit here; repeating
	// the last message would open a duplicate card.
	if text != "" && (w.streamActive || text != lastAssistant) {
		w.commitExternalStream(text)
	}
	w.finishExternalStream()
	if event.IsError {
		reason := text
		if reason == "" {
			reason = event.TerminalReason
		}
		if reason == "" {
			reason = "external agent returned an error result"
		}
		if text != "" && text != lastAssistant {
			if err := w.persistExternalMessage(message.Message{Role: message.RoleAssistant, Content: text}); err != nil {
				w.setResultError(err)
				return true, externalTaskInput{}
			}
		}
		// An error result is a definitive outcome for this input: the failure is
		// carried by the task state, not by an unknown input that would block a
		// targeted follow-up.
		if err := w.finishExternalInput(); err != nil {
			w.setResultError(err)
			return true, externalTaskInput{}
		}
		w.setResultError(fmt.Errorf("external agent failed: %s", reason))
		return true, externalTaskInput{}
	}
	w.resultText = text
	w.permissionMu.Lock()
	pendingPermissions := len(w.permissionRequests)
	w.permissionMu.Unlock()
	if pendingPermissions != 0 {
		w.cancelExternalPermissions("")
		w.setResultError(fmt.Errorf("external result arrived before permission requests settled"))
		return true, externalTaskInput{}
	}
	w.externalToolStateMu.Lock()
	unresolved := w.settleUnresolvedExternalTools()
	w.externalToolStateMu.Unlock()
	if unresolved != nil {
		w.setResultError(unresolved)
		return true, externalTaskInput{}
	}
	if text != "" && text != lastAssistant {
		if err := w.persistExternalMessage(message.Message{Role: message.RoleAssistant, Content: text}); err != nil {
			w.setResultError(err)
			return true, externalTaskInput{}
		}
	}
	// The closing result is the task's delivered result, so the contract is
	// enforced on it before the input completes. A violation is a definitive
	// outcome for this input (the task fails), so it does not become unknown and
	// block a targeted follow-up.
	if !w.hasQueuedExternalInput() {
		if err := w.validateExternalResultContract(w.resultText); err != nil {
			if inputErr := w.finishExternalInput(); inputErr != nil {
				w.setResultError(inputErr)
				return true, externalTaskInput{}
			}
			w.setResultError(err)
			return true, externalTaskInput{}
		}
	}
	if err := w.finishExternalInput(); err != nil {
		w.setResultError(err)
		return true, externalTaskInput{}
	}
	w.promptMu.Lock()
	if len(w.promptQueue) > 0 {
		next := w.promptQueue[0]
		w.promptQueue = w.promptQueue[1:]
		w.promptMu.Unlock()
		return true, next
	}
	w.inputClosed = true
	w.promptMu.Unlock()
	return true, externalTaskInput{}
}

func (w *externalWorker) handleExternalToolUse(use externalclaude.ContentBlock) {
	if strings.TrimSpace(use.ID) == "" {
		return
	}
	if !w.bindExternalTool(use.ID, use.Name, use.Input, false) {
		w.setResultError(fmt.Errorf("external tool identity was reused with different arguments"))
		w.teardown()
		return
	}
	if w.toolSeen[use.ID] {
		return
	}
	w.toolSeen[use.ID] = true
	mapping, chordInput, ok := externalclaude.MapTool(use.Name, use.Input)
	name := mapping.ChordName
	args := chordInput
	if !ok {
		name = use.Name
		args = use.Input
	}
	if err := w.persistExternalMessage(message.Message{
		Role:      message.RoleAssistant,
		ToolCalls: []message.ToolCall{{ID: use.ID, Name: name, Args: args}},
		Audit:     &message.ToolArgsAudit{OriginalArgsJSON: string(use.Input)},
	}); err != nil {
		w.setResultError(err)
		w.teardown()
		return
	}
	w.rememberToolName(use.ID, name)
	if w.parent != nil {
		w.parent.emitToTUI(ToolCallStartEvent{
			ID:       use.ID,
			Name:     name,
			ArgsJSON: string(args),
			AgentID:  w.instanceID,
		})
	}
	summary := name
	if use.Name != "" && use.Name != name {
		summary = fmt.Sprintf("%s (%s)", name, use.Name)
	}
	w.updateProgress("External tool: " + summary)
	w.emitProgress("External tool: "+summary, "tool")
}

func (w *externalWorker) handleExternalToolResult(block externalclaude.ContentBlock) bool {
	id := strings.TrimSpace(block.ToolUseID)
	name := w.toolName(id)
	if name == "" {
		binding := w.toolBindings[id]
		return binding != nil && binding.settled
	}
	text := externalToolResultText(block.Content)
	status := message.ToolResultStatus(message.ToolStatusSuccess)
	if block.IsError {
		status = message.ToolResultStatus(message.ToolStatusError)
	}
	recoveryState := ""
	if binding := w.toolBindings[id]; binding == nil || !binding.started {
		status = message.ToolResultStatus(message.ToolStatusError)
		recoveryState = message.ToolRecoveryStateNotStarted
		if !block.IsError {
			w.setResultError(fmt.Errorf("external tool returned a result without host authorization"))
		}
	}
	w.settleExternalTool(id, name, text, string(status), recoveryState)
	w.updateProgress("External tool finished: " + name)
	return true
}

func externalToolResultText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err == nil {
		var builder strings.Builder
		for _, block := range blocks {
			if block.Type == "text" {
				builder.WriteString(block.Text)
			}
		}
		if builder.Len() > 0 {
			return builder.String()
		}
	}
	return string(raw)
}

func (w *externalWorker) currentRuleset() permission.Ruleset {
	w.rulesetMu.RLock()
	defer w.rulesetMu.RUnlock()
	return w.ruleset
}

func (w *externalWorker) setRuleset(ruleset permission.Ruleset) {
	w.rulesetMu.Lock()
	w.ruleset = ruleset
	w.rulesetMu.Unlock()
}

func (w *externalWorker) pathScope() permission.PathScope {
	scope := permission.PathScope{Cwd: w.workDir}
	if w.parent != nil {
		if snapshot := w.parent.pathRoots.Load(); snapshot != nil {
			scope.Roots = snapshot.roots
			scope.Containers = snapshot.containers
		}
	}
	return scope
}

// derivedDisallowedTools narrows the external process's visible tools to the
// ones the role ruleset keeps enabled. Permission prompts still gate every
// call; this only avoids offering tools the role can never use.
func (w *externalWorker) derivedDisallowedTools() []string {
	ruleset := w.currentRuleset()
	if len(ruleset) == 0 {
		return nil
	}
	var disallowed []string
	for _, name := range externalclaude.ManagedToolNames() {
		mapping, _, mapped := externalclaude.MapTool(name, json.RawMessage(`{}`))
		if !mapped {
			// Chord has no equivalent, so evaluateExternalPermission only
			// authorizes it through a rule that names the Claude tool itself;
			// a wildcard allow is not enough. Without such a rule every call is
			// denied, so leaving the tool visible only invites the model to
			// retry a call that can never succeed.
			if !externalToolHasNamedRule(ruleset, name) {
				disallowed = append(disallowed, name)
			}
			continue
		}
		if ruleset.IsDisabled(mapping.ChordName) {
			disallowed = append(disallowed, name)
		}
	}
	return disallowed
}

// externalToolHasNamedRule reports whether the ruleset contains a rule that
// names the tool, ignoring the wildcard default.
func externalToolHasNamedRule(ruleset permission.Ruleset, name string) bool {
	for _, rule := range ruleset {
		if permissionRuleTargetsTool(rule, name) {
			return true
		}
	}
	return false
}
