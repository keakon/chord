package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/filectx"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
)

// startPlanExecution begins executing a plan document in response to an
// ExecutePlan event: it stages the execution session, commits the session
// switch, and starts the LLM loop. Failures are reported to the TUI and the
// agent returns to idle.
func (a *MainAgent) startPlanExecution(planPath, agentName string) {
	staging, err := a.beginPlanExecution(planPath, agentName)
	if err != nil {
		a.emitToTUI(ErrorEvent{Err: err})
		a.setIdleAndDrainPending()
		return
	}
	if err := a.commitPlanExecution(staging); err != nil {
		a.emitToTUI(ErrorEvent{Err: err})
		a.setIdleAndDrainPending()
		return
	}
}

// planExecutionStaging carries resources prepared before the planner session is
// frozen. The current role, history, and recovery target remain untouched until
// commitPlanExecution starts.
type planExecutionStaging struct {
	planPath      string
	targetConfig  *config.AgentConfig
	targetModel   *preparedMainModel
	newSessionDir string
	newLock       *recovery.SessionLock
}

// beginPlanExecution stages plan execution without changing the current
// session. All work that can fail is completed before the caller settles the
// deferred Handoff result or freezes the planner session.
func (a *MainAgent) beginPlanExecution(planPath, agentName string) (*planExecutionStaging, error) {
	if !a.stopCompactionForSessionSwitch() {
		return nil, fmt.Errorf("cannot start plan execution while compaction is running")
	}
	if agentName == "" {
		agentName = "builder"
	}
	if planPath == "" {
		planPath = a.lastPlanPath
	}
	if planPath == "" {
		return nil, fmt.Errorf("no plan to execute; specify a path")
	}
	// Validate the plan document before mutating any state so a missing or
	// empty plan leaves the active role and conversation intact.
	planContent, err := os.ReadFile(planPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read plan: %w", err)
	}
	if len(strings.TrimSpace(string(planContent))) == 0 {
		return nil, fmt.Errorf("plan file is empty: %s", planPath)
	}

	cfg, ok := a.agentConfigs[agentName]
	if !ok || cfg == nil {
		return nil, fmt.Errorf("unknown role %q", agentName)
	}
	var targetModel *preparedMainModel
	if nextRef := a.defaultRoleModelRef(cfg); nextRef != "" {
		var err error
		targetModel, err = a.prepareMainModelForRole(nextRef, cfg)
		if err != nil {
			return nil, fmt.Errorf("prepare %s role model: %w", agentName, err)
		}
	}
	if err := a.ensureSessionBuilt(a.parentCtx); err != nil {
		if targetModel != nil {
			targetModel.client.Close()
		}
		return nil, fmt.Errorf("prepare execution session: %w", err)
	}

	log.Infof("starting plan execution plan_path=%v", planPath)

	newSessionDir, err := a.createRuntimeSessionDir()
	if err != nil {
		log.Warnf("failed to create session dir for plan execution error=%v", err)
		if targetModel != nil {
			targetModel.client.Close()
		}
		return nil, fmt.Errorf("create execution session: %w", err)
	}
	var newLock *recovery.SessionLock
	staged := false
	defer func() {
		if staged {
			return
		}
		if newLock != nil {
			_ = newLock.Release()
		}
		if targetModel != nil {
			current, _, _, _ := a.llmSnapshot()
			if targetModel.client != current {
				targetModel.client.Close()
			}
		}
		_ = os.RemoveAll(newSessionDir)
	}()

	newLock, err = recovery.AcquireSessionLock(newSessionDir)
	if err != nil {
		return nil, fmt.Errorf("execution session lock: %w", err)
	}
	staged = true

	return &planExecutionStaging{
		planPath:      planPath,
		targetConfig:  cfg,
		targetModel:   targetModel,
		newSessionDir: newSessionDir,
		newLock:       newLock,
	}, nil
}

// commitPlanExecution activates the staged execution session: it freezes the
// current session, installs the new one, injects the execution prompt, and
// starts the model loop. Call only after beginPlanExecution reported success
// and after any deferred tool result has been persisted into the session that
// holds its paired tool call.
func (a *MainAgent) commitPlanExecution(staging *planExecutionStaging) error {
	defer a.finishSessionSwitch()
	oldSessionDir := a.SessionDir()
	oldRecovery, turnCtx := a.prepareSessionSwitch()
	// The execution switch replaces the planner session exactly like a user
	// session switch does, so signal it: the TUI must drop the replaced
	// session's scoped state (a held mailbox message's queued waiting row for
	// example) instead of leaving it in the execution session's pending area.
	a.emitToTUI(SessionSwitchStartedEvent{Kind: sessionSwitchKindPlanExecution})
	turnID := a.turn.ID
	oldLock := a.sessionLock
	a.freezeCurrentSession(oldRecovery)
	if oldLock != nil {
		if releaseErr := oldLock.Release(); releaseErr != nil {
			log.Warnf("execution session: failed to release old session lock error=%v", releaseErr)
		}
	}
	a.sessionLock = staging.newLock
	a.resetSessionRuntimeState()
	a.installSessionTarget(staging.newSessionDir)
	a.llmClient.SetSessionID(filepath.Base(staging.newSessionDir))
	a.scheduleMemoryExtraction(oldSessionDir)

	// The target model and resource preparation were completed before the old
	// session was frozen. Install the target role only after the new recovery
	// target is active so the old snapshot retains the planner role.
	a.installPlanExecutionRole(staging.targetConfig, staging.targetModel)
	// The session surface was preflighted before the switch. Rebuild it here
	// without running another fallible resource-preparation hook.
	if err := a.ensureSessionBuiltWithoutPreparation(a.parentCtx); err != nil {
		return fmt.Errorf("prepare execution session: %w", err)
	}
	execPrompt := a.buildExecuteSystemPrompt(staging.planPath)
	a.setSystemPromptOverride(execPrompt)

	// Notify TUI to wipe the viewport so planner-phase messages are cleared.
	a.emitToTUI(SessionRestoredEvent{})
	a.finishPlanExecution(turnCtx, turnID, staging.planPath)
	return nil
}

func (a *MainAgent) installPlanExecutionRole(cfg *config.AgentConfig, prepared *preparedMainModel) {
	a.stateMu.Lock()
	a.activeConfig = cfg
	a.stateMu.Unlock()
	a.clearSystemPromptOverride()
	a.rebuildRuleset()
	a.markRuntimeSurfaceDirty()
	a.NotifyEnvStatusUpdated()
	if prepared != nil {
		a.installPreparedMainModel(prepared)
	} else {
		a.mainModelPolicyDirty.Store(true)
	}
}

// finishPlanExecution injects the plan bootstrap message and starts the model
// loop. Call only after beginPlanExecution reported success.
func (a *MainAgent) finishPlanExecution(turnCtx context.Context, turnID uint64, planPath string) {
	// Add initial execution instruction that drives LLM-based dispatch.
	executionMsg := a.buildPlanExecutionBootstrapMessage(planPath)
	a.ctxMgr.Append(executionMsg)
	a.recordEvidenceFromMessage(executionMsg)
	if a.usageLedger != nil {
		firstUserMessage := message.UserPromptPlainText(executionMsg)
		if err := a.usageLedger.SetFirstUserMessage(firstUserMessage); err != nil {
			log.Warnf("failed to update usage summary first user message error=%v", err)
		}
		a.updateSessionSummary(func(summary *SessionSummary) {
			if summary == nil {
				return
			}
			if summary.FirstUserMessage == "" {
				summary.FirstUserMessage = firstUserMessage
				summary.FirstUserMessageIsCompactionSummary = false
			}
			if summary.OriginalFirstUserMessage == "" {
				summary.OriginalFirstUserMessage = firstUserMessage
			}
		})
	}

	a.beginMainLLMAfterPreparation(turnCtx, turnID, "")
}

// ---------------------------------------------------------------------------
// Execution: agent resolution
// ---------------------------------------------------------------------------

// resolveAvailableAgents returns the agent configs available for delegate dispatch.
// All known subagent-mode agent configs are returned. The result is used to
// populate the execution system prompt so the LLM knows which agent_type values
// are valid for the Delegate tool.
func (a *MainAgent) resolveAvailableAgents() []*config.AgentConfig {
	if len(a.agentConfigs) == 0 {
		return nil
	}

	// Collect all subagent-mode agents.
	agents := make([]*config.AgentConfig, 0, len(a.agentConfigs))
	for _, cfg := range a.agentConfigs {
		if cfg.IsSubAgent() {
			agents = append(agents, cfg)
		}
	}
	return agents
}

func (a *MainAgent) buildPlanExecutionBootstrapMessage(planPath string) message.Message {
	instruction := fmt.Sprintf(
		"Execute the plan at @%s. Analyse the referenced plan content, identify all tasks and their dependencies, "+
			a.executionStartInstruction()+
			" "+a.executionPacingInstruction(),
		escapePlanAtMentionPath(planPath),
	)
	parts := append([]message.ContentPart{{Type: message.ContentPartText, Text: instruction}}, filectx.BuildFileParts([]string{planPath}, func(path string) string { return path })...)
	return message.Message{Role: message.RoleUser, Content: instruction, Parts: parts}
}

func escapePlanAtMentionPath(path string) string {
	var b strings.Builder
	b.Grow(len(path))
	for _, r := range path {
		if unicode.IsSpace(r) || r == '\\' || r == '@' {
			b.WriteRune('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// buildExecuteSystemPrompt constructs a system prompt for the plan execution
// phase. It should identify the target plan and execution expectations without
// pre-committing the current main role to a specific strategy such as direct
// implementation or subagent orchestration.
func (a *MainAgent) buildExecuteSystemPrompt(planPath string) string {
	base := a.buildSystemPrompt()
	hasTodoWrite := a.hasTodoWriteAccess()

	var sb strings.Builder
	sb.WriteString(base)

	// Parallel-dispatch and wait-for-coordination rules are not restated here:
	// when this role can delegate, base already carries the "## SubAgent
	// Workflow" rules (their single source); a role without delegation has no
	// workers to dispatch, so restating them would reference unavailable
	// machinery.

	if hasTodoWrite {
		fmt.Fprintf(&sb, `

## Execution Mode — Plan Execution

You are executing a plan in the current main-agent role. Your job is to carry
out the plan using the visible tools and coordination mechanisms available in
this role.

### Plan File
Path: %s

### Execution Rules
1. **Analyse** the plan's tasks and their dependency graph.
2. **Initialise** a todo list with TodoWrite (all "pending"; order matches plan intent).
3. **Choose the execution strategy that fits this role**: use the visible tools
   and coordination mechanisms that are actually available here. Do not assume a
   hidden orchestration mode or unavailable workers.
4. **Respect dependencies**: do NOT begin a task until its dependencies are
   satisfied. For independent tasks, use a pragmatic order and keep moving.
5. **Track progress**: update TodoWrite as work progresses (statuses:
   pending, in_progress, completed, cancelled). Before your final summary, leave
   no pending/in_progress items unless you explain why.
6. **Report real blockers**: if the current role lacks a needed capability or
   permission, explain the blocker instead of assuming hidden capabilities or
   nonexistent workers.
7. **Finish**: when everything is done, give a concise final summary.
`, planPath)
	} else {
		fmt.Fprintf(&sb, `

## Execution Mode — Plan Execution

You are executing a plan in the current main-agent role. Your job is to
carry out the plan using the visible tools and coordination mechanisms available
in this role.

### Plan File
Path: %s

### Execution Rules
1. **Analyse** the plan's tasks and their dependency graph.
2. **Choose the execution strategy that fits this role**: use the visible tools
   and coordination mechanisms that are actually available here. Do not assume a
   hidden orchestration mode or unavailable workers.
3. **Respect dependencies**: do NOT begin a task until its dependencies are
   satisfied. For independent tasks, use a pragmatic order and keep moving.
4. **Report real blockers**: if the current role lacks a needed capability or
   permission, explain the blocker instead of assuming hidden capabilities or
   nonexistent workers.
5. **Finish**: when everything is done, give a concise final summary.
`, planPath)
	}

	return sb.String()
}

// executePlanPayload carries the plan path and target agent name for EventExecutePlan.
type executePlanPayload struct {
	PlanPath  string
	AgentName string // target agent role (default: "builder")
}

// handleExecutePlanEvent dispatches plan execution from an EventExecutePlan event.
func (a *MainAgent) handleExecutePlanEvent(evt Event) {
	p, ok := evt.Payload.(*executePlanPayload)
	if !ok {
		log.Errorf("handleExecutePlanEvent: invalid payload type payload_type=%v", fmt.Sprintf("%T", evt.Payload))
		return
	}
	a.startPlanExecution(p.PlanPath, p.AgentName)
}
