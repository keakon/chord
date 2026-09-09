package agent

import (
	"time"

	"github.com/keakon/chord/internal/filelock"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/tools"
)

func (s *SubAgent) toolExecutionPipeline() toolExecutionPipeline {
	var (
		fileTrack   *filelock.FileTracker
		fileBackups *fileBackupManager
		eventSender tools.EventSender
		emit        func(AgentEvent)
		confirm     ConfirmFunc
	)
	runtimeStartedAt := time.Time{}
	if s.parent != nil {
		fileTrack = s.parent.fileTrack
		fileBackups = s.parent.fileBackups
		eventSender = s.parent
		emit = s.parent.emitToTUI
		confirm = s.parent.confirmFn
		runtimeStartedAt = s.parent.runtimeStartedAt
	}
	return toolExecutionPipeline{
		agentID:          s.instanceID,
		journalAgentID:   s.instanceID,
		eventAgentID:     s.instanceID,
		taskID:           s.taskID,
		sessionDir:       s.sessionDir,
		registry:         s.tools,
		governor:         s.parent.governor,
		fileTrack:        fileTrack,
		fileBackups:      fileBackups,
		runtimeStartedAt: runtimeStartedAt,
		eventSender:      eventSender,
		emit:             emit,
		guidance:         subToolOutputGuidance,
		logPrefix:        "SubAgent:",
		applyPatchRetry:  &s.applyPatchRetry,
		projectRoot:      s.parent.projectRoot,
		toolBaseDir:      s.workDir,
		currentRuleset:   s.currentRuleset,
		refreshRulesetAfterRuleIntent: func(toolName string, intent *ConfirmRuleIntent) permission.Ruleset {
			if s.parent != nil {
				s.parent.processRuleIntent(toolName, intent, s.agentDefName)
				s.setRuleset(s.parent.buildSubAgentRuleset(s.parent.agentConfigs[s.agentDefName]))
			}
			return s.currentRuleset()
		},
		isInternalTool: isSubAgentInternalTool,
		confirm:        confirm,
		yoloDowngradeAsk: func(name string) bool {
			// SubAgents inherit the main agent's YOLO mode at each decision:
			// ask degrades to an implicit allow while the parent YOLO is on.
			// Deny decisions never reach this hook and the protected control
			// tools are excluded, so both stay enforced.
			return s.parent.YoloEnabled() && !yoloProtectedPermissionTool(name)
		},
		currentTurnID:         s.currentTurnID,
		captureWalltimeTarget: s.captureWalltimeTarget,
		fireHook:              s.fireHook,
		updatePending: func(call PendingToolCall) {
			if s.turn != nil {
				s.turn.updatePendingToolCall(call)
			}
		},
		visibleToolNames: s.visibleToolNames,
		appendToolActivity: func(rec recovery.ToolActivityRecord) error {
			// Resolved per record, not captured with the pipeline: compaction
			// replaces the manager and a switch retires it entirely.
			manager := s.recoveryManager()
			if manager == nil {
				return nil
			}
			return manager.AppendToolActivity(rec)
		},
	}
}

func (s *SubAgent) captureWalltimeTarget() *walltimeTarget {
	if s == nil || s.parent == nil || s.parent.walltime == nil {
		return nil
	}
	return s.parent.walltime.captureAt(s.instanceID, s.agentDefName, s.currentTurnID())
}
