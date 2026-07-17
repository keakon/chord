package agent

import (
	"github.com/keakon/chord/internal/filelock"
	"github.com/keakon/chord/internal/permission"
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
	if s.parent != nil {
		fileTrack = s.parent.fileTrack
		fileBackups = s.parent.fileBackups
		eventSender = s.parent
		emit = s.parent.emitToTUI
		confirm = s.parent.confirmFn
	}
	return toolExecutionPipeline{
		agentID:       s.instanceID,
		eventAgentID:  s.instanceID,
		taskID:        s.taskID,
		sessionDir:    s.sessionDir,
		registry:      s.tools,
		fileTrack:     fileTrack,
		fileBackups:   fileBackups,
		eventSender:   eventSender,
		emit:          emit,
		guidance:      subToolOutputGuidance,
		logPrefix:     "SubAgent:",
		projectRoot:   s.parent.projectRoot,
		writeScope:    &s.writeScope,
		writeScopeDir: s.workDir,
		currentRuleset: func() permission.Ruleset {
			return s.ruleset
		},
		refreshRulesetAfterRuleIntent: func(toolName string, intent *ConfirmRuleIntent) permission.Ruleset {
			if s.parent != nil {
				s.parent.processRuleIntent(toolName, intent)
				s.ruleset = s.parent.buildSubAgentRuleset(s.parent.agentConfigs[s.agentDefName])
			}
			return s.ruleset
		},
		isInternalTool: isSubAgentInternalTool,
		confirm:        confirm,
		currentTurnID:  s.currentTurnID,
		fireHook:       s.fireHook,
		updatePending: func(call PendingToolCall) {
			if s.turn != nil {
				s.turn.updatePendingToolCall(call)
			}
		},
		visibleToolNames: s.visibleToolNames,
	}
}
