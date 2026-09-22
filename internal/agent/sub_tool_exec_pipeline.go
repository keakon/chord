package agent

import (
	"encoding/json"
	"strings"
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
	mainAgentID := ""
	contentRoot := ""
	if s.parent != nil {
		fileTrack = s.parent.fileTrack
		fileBackups = s.parent.fileBackups
		eventSender = s.parent
		emit = s.parent.emitToTUI
		confirm = s.parent.confirmFn
		runtimeStartedAt = s.parent.runtimeStartedAt
		mainAgentID = s.parent.instanceID
		contentRoot = s.parent.ContentRoot()
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
		// A SubAgent may reach its own jobs, the main agent's jobs, and jobs
		// started by its direct owner (the worker-reads-owner's-job case).
		jobAccess:             tools.JobAccess{OwnerAgentID: s.OwnerAgentID(), MainAgentID: mainAgentID},
		guidance:              subToolOutputGuidance,
		logPrefix:             "SubAgent:",
		applyPatchRetry:       &s.applyPatchRetry,
		toolBaseDir:           s.effectiveToolBaseDir(),
		machineStateRoot:      contentRoot,
		toolBaseDirGeneration: s.workDirState.load().Generation,
		pathScope:             s.effectivePathScope(),
		preapprovedPermission: func(callID, name string, args json.RawMessage, cwd string, pctx toolPermissionContext) bool {
			return s.permissionApprovalMatches(s.currentTurn(), callID, name, string(args), cwd, pctx)
		},
		currentRuleset: s.currentRuleset,
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
			// ask relaxes to an implicit allow while the parent YOLO is on.
			// Deny decisions never reach this hook; done and compact_context
			// keep their dedicated action semantics and report false here.
			// delegate and cancel relax exactly as they do on the main agent,
			// so the mechanism tools behave the same at every delegation depth.
			return s.parent.YoloEnabled() && yoloAskDowngradeTool(name)
		},
		currentTurnID:         s.currentTurnID,
		captureWalltimeTarget: s.captureWalltimeTarget,
		fireHook:              s.fireHookInDir,
		updatePending: func(call PendingToolCall) {
			if turn := s.currentTurn(); turn != nil {
				turn.updatePendingToolCall(call)
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

// effectiveToolBaseDir resolves the base directory tools execute against,
// matching toolExecutionPipeline.effectiveToolBaseDir (the sub pipeline pins
// toolBaseDir to the sub's active checkout) without constructing the pipeline.
func (s *SubAgent) effectiveToolBaseDir() string {
	if dir := strings.TrimSpace(s.workDirState.load().Path); dir != "" {
		return dir
	}
	if strings.TrimSpace(s.workDir) != "" {
		return s.workDir
	}
	if s.parent != nil {
		return s.parent.effectiveToolBaseDir()
	}
	return ""
}
