package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/tools"
)

type ToolExecutionResult struct {
	Result string
	// Payload is the tool's raw output before any diagnostic note is appended.
	// Only set when toolPayloadIsStructured(toolName): for ordinary free-text
	// output Content already holds everything, so a second copy would just
	// double large results. Keeping it clean is what lets the UI parse the
	// user's actual answer instead of the answer-with-notes the model is shown.
	Payload string
	// Notes are the diagnostic lines appended after the payload for the model,
	// in order. They describe the call, never its output, so they are recorded
	// beside the payload rather than inside it.
	Notes                     []string
	Images                    []message.ContentPart // image/binary parts produced by the tool (ViewImage, MCP image results)
	EffectiveArgsJSON         string
	originalArgsForValidation json.RawMessage
	Audit                     *message.ToolArgsAudit
	LSPReviews                []message.LSPReview
	FileState                 *message.ToolFileState
	Diff                      tools.DiffSummary
	PreFilePath               string
	PreContent                string
	PreExisted                bool
	// ExecStartedAt is set by the execution pipeline immediately before the
	// tool's real action runs, after permission confirmation, hooks, and
	// argument validation have all passed. Duration consumers (tool result
	// events, tool card footer, persisted tool_duration_ms) compute elapsed
	// time from this anchor so ask / question / done confirmation waits are
	// never counted as tool execution time.
	ExecStartedAt time.Time
	// walltimeTarget pins tool time to the agent, turn, and session active at
	// ExecStartedAt so delayed results cannot leak into another agent/session.
	walltimeTarget   *walltimeTarget
	speculativeHooks *speculativeToolHooks
}

// ---------------------------------------------------------------------------
// Tool execution
// ---------------------------------------------------------------------------

// executeToolCall runs a single tool invocation with permission checks,
// output truncation.
func (a *MainAgent) executeToolCall(ctx context.Context, tc message.ToolCall) (ToolExecutionResult, error) {
	if intercept, ok := a.maybeInterceptRepeatedToolCall(ctx, tc); ok {
		execResult := ToolExecutionResult{
			EffectiveArgsJSON: string(tc.Args),
			Result:            intercept.toolResult,
			// No tool actually ran: anchor the (near-zero) execution duration at
			// the intercept decision point so the repeated-call confirmation wait
			// is never counted as tool execution time.
			ExecStartedAt:  time.Now(),
			walltimeTarget: a.captureMainWalltimeTarget(),
		}
		return execResult, intercept.confirmErr
	}
	return a.toolExecutionPipeline().execute(ctx, tc, true)
}

// executeToolCallSpeculative runs a tool without firing hooks,
// or irreversible finalize-only side effects. Results are UI-only until the
// finalized call promotes them through the normal handleToolResult path.
func (a *MainAgent) executeToolCallSpeculative(ctx context.Context, tc message.ToolCall) (ToolExecutionResult, error) {
	return a.toolExecutionPipeline().executeSpeculative(ctx, tc)
}

func (a *MainAgent) captureMainWalltimeTarget() *walltimeTarget {
	if a == nil || a.walltime == nil {
		return nil
	}
	return a.walltime.captureAt(identity.MainAgentID, a.currentAgentName(), a.currentTurnID())
}

func (a *MainAgent) toolExecutionPipeline() toolExecutionPipeline {
	return toolExecutionPipeline{
		agentID:          a.instanceID,
		journalAgentID:   identity.MainAgentID,
		eventAgentID:     "",
		sessionDir:       a.sessionDir,
		registry:         a.tools,
		governor:         a.governor,
		fileTrack:        a.fileTrack,
		fileBackups:      a.fileBackups,
		runtimeStartedAt: a.runtimeStartedAt,
		eventSender:      a,
		emit:             a.emitToTUI,
		// MainAgent owns every job; its own instance id both stamps its jobs and
		// lets it reach jobs started by any SubAgent.
		jobAccess:       tools.JobAccess{MainAgentID: a.instanceID},
		guidance:        mainToolOutputGuidance,
		applyPatchRetry: &a.applyPatchRetry,
		currentRuleset: func() permission.Ruleset {
			return a.effectiveRuleset()
		},
		toolBaseDir:           a.effectiveToolBaseDir(),
		machineStateRoot:      a.ContentRoot(),
		toolBaseDirGeneration: a.workDirState.load().Generation,
		pathScope:             a.effectivePathScope,
		refreshRulesetAfterRuleIntent: func(toolName string, intent *ConfirmRuleIntent) permission.Ruleset {
			a.processRuleIntent(toolName, intent, a.currentAgentName())
			return a.effectiveRuleset()
		},
		isInternalTool:        isInternalControlTool,
		confirm:               a.confirmFn,
		currentTurnID:         a.currentTurnID,
		captureWalltimeTarget: a.captureMainWalltimeTarget,
		fireHook:              a.fireHook,
		updatePending: func(call PendingToolCall) {
			if turn := a.currentTurn(); turn != nil {
				turn.updatePendingToolCall(call)
			}
		},
		reservedToolError: func(name string) error {
			if isMainAgentReservedTool(name) {
				return fmt.Errorf("tool %q is reserved for SubAgents and unavailable to MainAgent", name)
			}
			return nil
		},
		bypassPermission: func(name string) bool {
			return a.YoloEnabled() && !yoloProtectedPermissionTool(name)
		},
		yoloDowngradeAsk: func(name string) bool {
			// Under YOLO the mechanism control tools' ask rules stop raising
			// the shared confirmation dialog while their deny rules still
			// reject; ordinary tools are already skipped by bypassPermission
			// above, and done/compact_context report false through
			// yoloAskDowngradeTool.
			return a.YoloEnabled() && yoloAskDowngradeTool(name)
		},
		loopExitAuthorized: a.loopExitAuthorized,
		preapprovedPermission: func(callID, name string, args json.RawMessage, cwd string, pctx toolPermissionContext) bool {
			return a.permissionApprovalMatches(callID, name, string(args), cwd, pctx)
		},
		visibleToolNames: a.mainVisibleLLMToolNames,
		appendToolActivity: func(rec recovery.ToolActivityRecord) error {
			// Tool goroutines outlive a session switch, so the manager is
			// resolved per record rather than captured with the pipeline.
			manager := a.recoveryManager()
			if manager == nil {
				return nil
			}
			return manager.AppendToolActivity(rec)
		},
	}
}

// normalizeDenyReason trims surrounding whitespace in a deny reason while preserving
// the user's full text, including internal newlines, for display and model context.
func normalizeDenyReason(reason string) string {
	reason = strings.TrimSpace(reason)
	return reason
}

// workDir returns the checkout the agent's tools and shell commands run in.
// It is the single runtime source for tool path resolution: worktree switches
// publish the active checkout into workDirState, and cachedWorkDir only holds
// the directory the session started in.
func (a *MainAgent) workDir() string {
	if a == nil {
		return ""
	}
	if dir := strings.TrimSpace(a.workDirState.load().Path); dir != "" {
		return dir
	}
	if dir := strings.TrimSpace(a.cachedWorkDir); dir != "" {
		return dir
	}
	return a.contentRoot
}

// effectiveToolBaseDir resolves the base directory tools execute against,
// matching toolExecutionPipeline.effectiveToolBaseDir without constructing the
// pipeline (some per-result paths only need the directory).
func (a *MainAgent) effectiveToolBaseDir() string {
	return a.workDir()
}
