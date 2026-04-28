package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/keakon/chord/internal/agent/agentdiff"
	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/filelock"
	"github.com/keakon/chord/internal/hook"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/skill"
	"github.com/keakon/chord/internal/tools"
)

// ---------------------------------------------------------------------------
// SubAgent-internal types
// ---------------------------------------------------------------------------

// llmResult carries an LLM response back to the SubAgent's event loop.
// The turnID field is used for staleness detection: results from cancelled
// turns are silently discarded.
type llmResult struct {
	resp   *message.Response
	err    error
	turnID uint64
}

// toolResult carries a single tool execution result back to the event loop.
type toolResult struct {
	CallID      string
	Name        string // tool name, used for empty-args detection
	ArgsJSON    string // original args JSON string for malformed detection
	Audit       *message.ToolArgsAudit
	Result      string
	Error       error
	TurnID      uint64
	Duration    time.Duration
	Diff        string              // unified diff for Write/Edit tools; not sent to LLM
	DiffAdded   int                 // full added-line count before any diff truncation
	DiffRemoved int                 // full removed-line count before any diff truncation
	FileCreated bool                // true when Write created a file that did not previously exist
	LSPReviews  []message.LSPReview // last-review snapshot for the directly edited file only
}

// AgentResult is the completion payload sent via EventAgentDone when a
// SubAgent finishes its task (or fails).
type AgentResult struct {
	Summary  string
	Envelope *CompletionEnvelope
	Error    error
}

// inputChanCap is the in-channel buffer size for SubAgent's user-message queue.
// If producers briefly outrun the runLoop, overflow is preserved in an
// in-memory side queue instead of dropping older messages.
const inputChanCap = 64

// ---------------------------------------------------------------------------
// SubAgent
// ---------------------------------------------------------------------------

// SubAgent runs an independent event loop that executes a single task
// delegated by the MainAgent. It has its own LLM client, context manager,
// and tool registry, but shares the recovery manager and hook engine with
// its parent MainAgent.
//
// All mutable state is confined to the runLoop goroutine (single-writer);
// external user input is enqueued via InjectUserMessage / InjectUserMessageWithParts.
type SubAgent struct {
	instanceID      string // immutable, from NextInstanceID()
	taskID          string // plan task ID or "adhoc-N"
	agentDefName    string // agent definition name (e.g. "backend-coder")
	taskDesc        string // task description (from Plan or ad-hoc)
	planTaskRef     string
	semanticTaskKey string
	writeScope      tools.WriteScope
	ownerAgentID    string
	ownerTaskID     string
	depth           int
	joinToOwner     bool
	delegation      config.DelegationConfig
	color           string // optional ANSI color code from agent config for TUI display
	llmClient       *llm.Client
	ctxMgr          *ctxmgr.Manager // own context (no auto_compact)
	tools           *tools.Registry // shared base + SubAgent-specific tools
	parent          *MainAgent      // reference to parent for event forwarding
	parentCtx       context.Context
	cancel          context.CancelFunc
	recovery        *recovery.RecoveryManager // shared with MainAgent, thread-safe

	// turnMu guards concurrent CancelSubAgent vs runLoop turn creation.
	turnMu sync.Mutex
	// Turn isolation (same Turn struct as MainAgent).
	turn       *Turn
	nextTurnID uint64

	// Event channels (buffered to avoid producer blocking).
	inputCh           chan pendingUserMessage // buffered user messages from main (cap = inputChanCap)
	ctxAppendCh       chan message.Message    // context-only user lines (e.g. !shell output); no LLM call
	llmCh             chan *llmResult         // cap=1, LLM responses
	toolCh            chan *toolResult        // cap=8, tool results
	continueCh        chan continueMsg        // cap=1; signals ContinueFromContext/cancel
	inputQueueMu      sync.Mutex
	inputOverflow     []pendingUserMessage
	ctxAppendQueueMu  sync.Mutex
	ctxAppendOverflow []message.Message

	// Idle timeout: starts when LLM returns pure text (no tool_calls).
	// MainAgent auto-intervenes on timeout.
	idleTimer   *time.Timer
	idleTimeout time.Duration // default 120s

	// pendingComplete is set when Complete appears alongside other tool
	// calls in one LLM response. The other tools execute first; EventAgentDone
	// is sent once all of them complete. This prevents the last batch of file
	// edits from being silently dropped.
	pendingComplete       *AgentResult
	pendingCompleteCallID string
	pendingEscalate       string

	// Permission: merged ruleset (global + project + agent-level).
	ruleset permission.Ruleset

	// Repetition detection (SubAgent-local, single-goroutine access).
	repetition *tools.RepetitionDetector

	// System prompt components (set at construction, read-only afterward).
	workDir      string
	venvPath     string // absolute path to detected Python virtual environment, or ""
	sessionDir   string
	agentsMD     string
	loadedSkills []*skill.Meta
	modelName    string
	customPrompt string // from agent YAML body; replaces built-in role instructions if non-empty

	// cachedSessionReminderContent is the <system-reminder> meta user message content
	// carrying AGENTS.md + currentDate. Built once at construction, injected
	// once per SubAgent lifetime (session-head for SubAgent == construction).
	// Not persisted. Mirrors MainAgent.
	cachedSessionReminderContent string
	// sessionReminderInjected is true once the reminder has been injected.
	sessionReminderInjected bool

	// frozenToolDefs is the SubAgent's tool surface snapshot, computed once at
	// construction. Kept stable so the provider request prefix does not drift.
	// See docs/architecture/prompt-and-context-engineering.md §6.
	frozenToolDefs []message.ToolDefinition

	// repMu serialises access to the RepetitionDetector from concurrent
	// tool-execution goroutines.
	repMu sync.Mutex

	// semHeld is true when this SubAgent holds a slot in the MainAgent's
	// concurrency semaphore. Set by CreateSubAgent; restored agents do not
	// hold a slot (they are idle and don't count against the concurrency limit).
	semHeld bool
	// semBypassed is true when the worker was reactivated by a wake path
	// without consuming a semaphore token, to avoid deadlocking parent/owner
	// coordination. releaseSubAgentSlot must not drain a.sem in this case.
	semBypassed bool

	runtimeState subAgentRuntimeState
}

// maxIdleNudges is the maximum number of idle nudges before escalating to
// MainAgent as an error. Tracked in MainAgent (nudgeCounts map), not here.
const maxIdleNudges = 3

// DefaultIdleTimeout is the default duration before a SubAgent is considered
// idle after receiving a pure-text LLM response.
const DefaultIdleTimeout = 120 * time.Second

// ---------------------------------------------------------------------------
// Constructor
// ---------------------------------------------------------------------------

// SubAgentConfig holds the parameters for creating a new SubAgent.
type SubAgentConfig struct {
	InstanceID    string
	TaskID        string
	AgentDefName  string
	TaskDesc      string
	PlanTaskRef   string
	SemanticKey   string
	WriteScope    tools.WriteScope
	OwnerAgentID  string
	OwnerTaskID   string
	Depth         int
	JoinToOwner   bool
	Delegation    config.DelegationConfig
	Color         string
	SystemPrompt  string // custom role instructions from agent YAML body; empty = use built-in
	LLMClient     *llm.Client
	Recovery      *recovery.RecoveryManager
	Parent        *MainAgent
	ParentCtx     context.Context
	Cancel        context.CancelFunc
	BaseTools     *tools.Registry // shared base tool registry (Read, Write, Edit, Bash, Grep, Glob, etc.)
	ExtraMCPTools []tools.Tool    // agent-specific MCP tools
	Ruleset       permission.Ruleset
	WorkDir       string
	VenvPath      string // absolute path to detected Python virtual environment, or ""
	SessionDir    string
	AgentsMD      string
	Skills        []*skill.Meta
	ModelName     string
	IdleTimeout   time.Duration // 0 → DefaultIdleTimeout
}

// NewSubAgent creates a fully-initialised SubAgent. The caller must invoke
// runLoop in a separate goroutine to start the event loop.
func NewSubAgent(cfg SubAgentConfig) *SubAgent {
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = DefaultIdleTimeout
	}

	// Build SubAgent's tool registry: clone base tools, then replace
	// MainAgent-only tools with SubAgent-specific ones.
	subTools := tools.NewRegistry()
	hasSkillTool := false
	delegationEnabled := cfg.Depth < cfg.Delegation.EffectiveMaxDepth()
	delegateVisible := delegationEnabled && !cfg.Ruleset.IsDisabled("Delegate")
	notifyVisible := !cfg.Ruleset.IsDisabled("Notify")

	// Copy all base tools EXCEPT MainAgent-only tools that never belong in a
	// SubAgent (`TodoWrite`, `Handoff`) plus delegation/control-plane tools
	// when this instance's depth/config does not allow nested delegation.
	for _, t := range cfg.BaseTools.ListTools() {
		switch t.Name() {
		case "TodoWrite", "Handoff":
			// Skip MainAgent-only tools.
		case "Notify":
			// SubAgents get a dedicated Notify tool so owner-notify and
			// targeted-notify availability can diverge by permission group.
		case "Skill":
			// Register a SubAgent-scoped Skill provider after the SubAgent is
			// fully constructed, so listing/visibility aligns with this
			// SubAgent's own ruleset.
			hasSkillTool = true
		case "Delegate", "Cancel":
			if !delegateVisible {
				continue
			}
			subTools.Register(t)
		default:
			// Skip MainAgent-only tools.
			if cfg.Ruleset.IsDisabled(t.Name()) {
				continue
			}
			subTools.Register(t)
		}
	}

	// Append agent-specific MCP tools (not filtered; permission Evaluate checks at runtime).
	for _, t := range cfg.ExtraMCPTools {
		subTools.Register(t)
	}

	// Create an EventSender adapter that routes through the SubAgent's
	// sendEvent method. We define it here (before the struct is fully built)
	// and close over the SubAgent pointer once created.
	//
	// The sender is only called from tool Execute methods which run after
	// runLoop starts, so the pointer is always valid.
	var s *SubAgent
	sender := &subAgentEventSender{sub: func() *SubAgent { return s }}

	// Register SubAgent-specific coordination tools. Complete is always
	// available so a worker can explicitly close its lifecycle even under
	// restrictive permission rules.
	subTools.Register(tools.CompleteTool{})
	if !cfg.Ruleset.IsDisabled("Escalate") {
		subTools.Register(tools.NewEscalateTool(sender))
	}
	if notifyVisible || delegateVisible {
		subTools.Register(tools.NewNotifyTool(sender, cfg.Parent, notifyVisible, notifyVisible && delegateVisible))
	}

	// Build the SubAgent's own context manager (no auto_compress per §3.2).
	ctxMgr := ctxmgr.NewManager(0, false, 0)

	s = &SubAgent{
		instanceID:      cfg.InstanceID,
		taskID:          cfg.TaskID,
		agentDefName:    cfg.AgentDefName,
		taskDesc:        cfg.TaskDesc,
		planTaskRef:     strings.TrimSpace(cfg.PlanTaskRef),
		semanticTaskKey: strings.TrimSpace(cfg.SemanticKey),
		writeScope:      cfg.WriteScope.Normalized(),
		ownerAgentID:    strings.TrimSpace(cfg.OwnerAgentID),
		ownerTaskID:     strings.TrimSpace(cfg.OwnerTaskID),
		depth:           cfg.Depth,
		joinToOwner:     cfg.JoinToOwner,
		delegation:      cfg.Delegation,
		color:           cfg.Color,
		llmClient:       cfg.LLMClient,
		ctxMgr:          ctxMgr,
		tools:           subTools,
		parent:          cfg.Parent,
		parentCtx:       cfg.ParentCtx,
		cancel:          cfg.Cancel,
		recovery:        cfg.Recovery,
		ruleset:         cfg.Ruleset,
		repetition:      tools.NewRepetitionDetector(),
		workDir:         cfg.WorkDir,
		venvPath:        cfg.VenvPath,
		sessionDir:      cfg.SessionDir,
		agentsMD:        cfg.AgentsMD,
		loadedSkills:    cfg.Skills,
		modelName:       cfg.ModelName,
		customPrompt:    cfg.SystemPrompt,
		idleTimeout:     cfg.IdleTimeout,
		inputCh:         make(chan pendingUserMessage, inputChanCap),
		ctxAppendCh:     make(chan message.Message, 16),
		llmCh:           make(chan *llmResult, 1),
		toolCh:          make(chan *toolResult, 8),
		continueCh:      make(chan continueMsg, 1),
	}
	s.runtimeState.set(SubAgentStateRunning, "")
	if hasSkillTool && !cfg.Ruleset.IsDisabled("Skill") {
		s.tools.Register(tools.NewSkillTool(s))
	}

	// Build and install the system prompt.
	prompt := s.buildSystemPrompt()
	cfg.LLMClient.SetSystemPrompt(prompt)
	ctxMgr.SetSystemPrompt(message.Message{
		Role:    "system",
		Content: prompt,
	})

	// Capture session-level context (AGENTS.md + currentDate) as a meta user
	// message and freeze the tool surface. Mirrors MainAgent. See
	// docs/architecture/prompt-and-context-engineering.md §§4,6.
	s.cachedSessionReminderContent = buildSessionContextReminder(s.agentsMD, time.Now())
	s.frozenToolDefs = append(
		[]message.ToolDefinition(nil),
		llmToolDefinitionsFromVisibleTools(visibleLLMTools(s.tools, s.ruleset, isSubAgentInternalTool))...,
	)

	return s
}

// ---------------------------------------------------------------------------
// LLM interaction
// ---------------------------------------------------------------------------

// asyncCallLLM starts an asynchronous LLM call and sends the result back
// via llmCh. Streaming deltas are forwarded to the parent's TUI output.
// Hook firing (on_before_llm_call / on_after_llm_call) and usage tracking
// mirror MainAgent.callLLM so that SubAgent calls are observable by user
// hooks and visible in cost analytics.
func (s *SubAgent) asyncCallLLM(turn *Turn, messages []message.Message) {
	toolDefs := s.frozenToolDefs
	if toolDefs == nil {
		visibleTools := visibleLLMTools(s.tools, s.ruleset, isSubAgentInternalTool)
		toolDefs = llmToolDefinitionsFromVisibleTools(visibleTools)
	}
	if !s.sessionReminderInjected {
		out := injectMetaUserReminder(messages, s.cachedSessionReminderContent)
		if len(out) != len(messages) {
			messages = out
			s.sessionReminderInjected = true
		}
	}
	compatCfg := s.llmClient.ThinkingToolcallCompat()
	scrubThinkingMarkers := compatCfg != nil && compatCfg.EnabledValue()

	go func() {
		// Hook: on_before_llm_call (mirrors MainAgent.callLLM).
		hookResult, hookErr := s.fireHook(turn.Ctx, hook.OnBeforeLLMCall, turn.ID, map[string]any{
			"model":         s.modelName,
			"message_count": len(messages),
		})
		if hookErr != nil {
			slog.Warn("SubAgent on_before_llm_call hook error",
				"agent", s.instanceID, "error", hookErr)
		} else if hookResult != nil {
			switch hookResult.Action {
			case hook.ActionBlock:
				msg := "blocked by on_before_llm_call hook"
				if hookResult.Message != "" {
					msg = hookResult.Message
				}
				select {
				case s.llmCh <- &llmResult{err: fmt.Errorf("LLM request %s", msg), turnID: turn.ID}:
				case <-s.parentCtx.Done():
				}
				return
			case hook.ActionModify:
				slog.Warn("SubAgent on_before_llm_call hook returned modify action; not supported, continuing",
					"agent", s.instanceID)
			}
		}

		// Streaming callback: forward deltas to the parent's TUI output
		// tagged with this SubAgent's instance ID.
		// Text and thinking deltas are batched to avoid flooding the output
		// channel when a proxy delivers all chunks in a burst.
		var (
			textAccum    strings.Builder
			textLastEmit time.Time

			thinkingAccum strings.Builder
		)
		const subTextFlushInterval = 20 * time.Millisecond
		streamingPromoted := false

		flushSubTextDelta := func() {
			if textAccum.Len() > 0 {
				text := textAccum.String()
				textAccum.Reset()
				s.parent.emitToTUI(StreamTextEvent{Text: text, AgentID: s.instanceID})
			}
			textLastEmit = time.Now()
		}
		promoteStreamingActivity := func(source string) {
			if streamingPromoted {
				return
			}
			streamingPromoted = true
			slog.Debug("subagent promoting streaming activity",
				"agent", s.instanceID,
				"turn_id", turn.ID,
				"source", source,
			)
			s.parent.emitActivity(s.instanceID, ActivityStreaming, "")
		}

		callback := func(delta message.StreamDelta) {
			switch delta.Type {
			case "text":
				textAccum.WriteString(delta.Text)
				if textLastEmit.IsZero() {
					flushSubTextDelta() // first delta: emit immediately
				} else if time.Since(textLastEmit) >= subTextFlushInterval {
					flushSubTextDelta()
				}
			case "thinking":
				flushSubTextDelta() // flush pending text before thinking
				thinkingAccum.WriteString(delta.Text)
				if delta.Text != "" {
					s.parent.emitToTUI(StreamThinkingDeltaEvent{Text: delta.Text, AgentID: s.instanceID})
				}
			case "thinking_end":
				if thinkingAccum.Len() > 0 {
					thinkingText := thinkingAccum.String()
					thinkingAccum.Reset()
					if scrubThinkingMarkers {
						thinkingText = scrubThinkingToolcallMarkers(thinkingText)
					}
					if strings.TrimSpace(thinkingText) != "" {
						s.parent.emitToTUI(StreamThinkingEvent{Text: thinkingText, AgentID: s.instanceID})
					}
				}
			case "tool_use_start":
				flushSubTextDelta() // flush pending text before tool call block
				promoteStreamingActivity("tool_use_start")
				if delta.ToolCall != nil {
					s.turn.recordStreamingToolCall(PendingToolCall{
						CallID:   delta.ToolCall.ID,
						Name:     delta.ToolCall.Name,
						ArgsJSON: delta.ToolCall.Input,
						AgentID:  s.instanceID,
					})
					s.parent.emitToTUI(ToolCallStartEvent{
						ID:       delta.ToolCall.ID,
						Name:     delta.ToolCall.Name,
						ArgsJSON: delta.ToolCall.Input,
						AgentID:  s.instanceID,
					})
				}
			case "tool_use_delta":
				if delta.ToolCall != nil && s.turn != nil && delta.ToolCall.ID != "" && delta.ToolCall.Input != "" {
					promoteStreamingActivity("tool_use_delta")
					accumulated := s.turn.appendStreamingToolCallInput(delta.ToolCall.ID, delta.ToolCall.Name, delta.ToolCall.Input, s.instanceID)
					if accumulated != "" {
						s.parent.emitToTUI(ToolCallUpdateEvent{
							ID:       delta.ToolCall.ID,
							Name:     delta.ToolCall.Name,
							ArgsJSON: accumulated,
							AgentID:  s.instanceID,
						})
					}
				}
			case "tool_use_end":
				if delta.ToolCall != nil && s.turn != nil && delta.ToolCall.ID != "" {
					callID := delta.ToolCall.ID
					callName := strings.TrimSpace(delta.ToolCall.Name)
					argsJSON := ""
					if call, ok := s.turn.getStreamingToolCall(callID); ok {
						if callName == "" {
							callName = call.Name
						}
						argsJSON = call.ArgsJSON
					}
					s.parent.recordToolTraceToolUseEnd(callID, callName, s.instanceID, time.Now())
					s.parent.emitToTUI(ToolCallUpdateEvent{
						ID:                callID,
						Name:              callName,
						ArgsJSON:          argsJSON,
						ArgsStreamingDone: true,
						AgentID:           s.instanceID,
					})
				}
			case "status":
				if delta.Status != nil {
					if delta.Status.Type == string(ActivityStreaming) {
						promoteStreamingActivity("llm_status")
						return
					}
					s.parent.emitActivity(s.instanceID, ActivityType(delta.Status.Type), delta.Status.Detail)
				}
			case "error":
				slog.Warn("SubAgent LLM stream error delta",
					"text", delta.Text,
					"agent", s.instanceID,
				)
			case "rollback":
				if s.turn != nil {
					s.parent.discardSpeculativeStreamToolsAndClearToolTrace(s.turn)
				}
				reason := ""
				if delta.Rollback != nil {
					reason = delta.Rollback.Reason
				}
				s.parent.emitToTUI(StreamRollbackEvent{Reason: reason, AgentID: s.instanceID})
			}
		}

		s.parent.emitActivity(s.instanceID, ActivityConnecting, "")
		resp, err := s.llmClient.CompleteStream(turn.Ctx, messages, toolDefs, callback)
		flushSubTextDelta() // final flush: emit any remaining accumulated text
		if turn.Ctx.Err() != nil {
			return // turn cancelled
		}

		// Track usage for cost analytics (mirrors MainAgent.callLLM).
		if err == nil && resp != nil {
			selectedRef := ""
			runningRef := ""
			if s.llmClient != nil {
				selectedRef = s.llmClient.PrimaryModelRef()
				runningRef = s.llmClient.RunningModelRef()
				// Set context limit so the info panel gauge shows correct capacity
				// when TUI focus is on this SubAgent (mirrors MainAgent.callLLM).
				if runningRef != "" {
					if lim := s.llmClient.ContextLimitForModelRef(runningRef); lim > 0 {
						s.ctxMgr.SetMaxTokens(lim)
					}
				}
			}
			s.parent.recordUsage(s.instanceID, "sub", s.agentDefName, "chat", selectedRef, runningRef, turn.ID, resp.Usage)

			// Hook: on_after_llm_call.
			subInputTok, subOutputTok := 0, 0
			if resp.Usage != nil {
				subInputTok = resp.Usage.InputTokens
				subOutputTok = resp.Usage.OutputTokens
			}
			afterResult, afterErr := s.fireHook(turn.Ctx, hook.OnAfterLLMCall, turn.ID, map[string]any{
				"model":         s.modelName,
				"input_tokens":  subInputTok,
				"output_tokens": subOutputTok,
				"tool_calls":    len(resp.ToolCalls),
			})
			if afterErr != nil {
				slog.Warn("SubAgent on_after_llm_call hook error",
					"agent", s.instanceID, "error", afterErr)
			} else if afterResult != nil {
				switch afterResult.Action {
				case hook.ActionBlock:
					msg := "blocked by on_after_llm_call hook"
					if afterResult.Message != "" {
						msg = afterResult.Message
					}
					select {
					case s.llmCh <- &llmResult{err: fmt.Errorf("LLM response %s", msg), turnID: turn.ID}:
					case <-s.parentCtx.Done():
					}
					return
				case hook.ActionModify:
					slog.Warn("SubAgent on_after_llm_call hook returned modify action; not supported, continuing",
						"agent", s.instanceID)
				}
			}
		}

		select {
		case s.llmCh <- &llmResult{resp: resp, err: err, turnID: turn.ID}:
		case <-s.parentCtx.Done():
		}
	}()
}

// ---------------------------------------------------------------------------
// Tool execution
// ---------------------------------------------------------------------------

// executeToolCall runs a single tool invocation with permission checks,
// repetition detection, hook interception, and output truncation.
// It uses the SubAgent's own ruleset but the parent's hookEngine and confirmFn.
func (s *SubAgent) executeToolCall(ctx context.Context, tc message.ToolCall) (ToolExecutionResult, error) {
	execResult := ToolExecutionResult{
		EffectiveArgsJSON: string(tc.Args),
	}
	// ----- Permission check -----
	if len(s.ruleset) > 0 && !isSubAgentInternalTool(tc.Name) {
		decision := evaluateToolPermission(s.ruleset, tc.Name, tc.Args)

		switch decision.Action {
		case permission.ActionDeny:
			slog.Warn("SubAgent: tool call denied by permission",
				"agent", s.instanceID, "tool", tc.Name, "argument", decision.MatchArgument)
			return execResult, wrapToolPermissionDenied(tc.Name)

		case permission.ActionAsk:
			if s.parent.confirmFn == nil {
				return execResult, wrapToolRequiresConfirmation(tc.Name)
			}
			// Tool goroutines serialize naturally on the cap=1 confirmCh;
			// the channel send can be interrupted by context cancellation.
			resp, err := s.parent.confirmFn(ctx, tc.Name, string(tc.Args), decision.NeedsApprovalPaths, decision.AlreadyAllowedPaths)
			if err != nil {
				return execResult, wrapToolConfirmationFailed(tc.Name, err)
			}
			if !resp.Approved {
				denyReason := normalizeDenyReason(resp.DenyReason)
				slog.Info("SubAgent: tool call rejected by user",
					"agent", s.instanceID, "tool", tc.Name, "argument", decision.MatchArgument,
					"deny_reason", denyReason)
				return execResult, wrapToolRejectedByUser(tc.Name, denyReason)
			}
			if resp.RuleIntent != nil {
				s.parent.processRuleIntent(tc.Name, resp.RuleIntent)
				s.ruleset = s.parent.effectiveRuleset()
			}
			originalArgs := append(json.RawMessage(nil), tc.Args...)
			editedArgs, err := applyConfirmedArgsEdits(s.tools, s.ruleset, tc.Name, tc.Args, resp.FinalArgsJSON)
			if err != nil {
				return execResult, err
			}
			tc.Args = editedArgs
			execResult.EffectiveArgsJSON = string(tc.Args)
			execResult.Audit = buildToolArgsAudit(originalArgs, tc.Args, resp.EditSummary)
			if s.turn != nil {
				s.turn.updatePendingToolCall(PendingToolCall{CallID: tc.ID, Name: tc.Name, AgentID: s.instanceID, ArgsJSON: execResult.EffectiveArgsJSON, Audit: execResult.Audit})
			}
			s.parent.emitToTUI(ToolCallUpdateEvent{ID: tc.ID, Name: tc.Name, ArgsJSON: execResult.EffectiveArgsJSON, AgentID: s.instanceID})

		case permission.ActionAllow:
			// Execute directly.
		}
	}

	// ----- Hook: on_tool_call -----
	hookResult, hookErr := s.fireHook(ctx, hook.OnToolCall, s.currentTurnID(), buildToolHookData(tc))
	if hookErr == nil && hookResult != nil {
		switch hookResult.Action {
		case hook.ActionBlock:
			msg := "blocked by hook"
			if hookResult.Message != "" {
				msg = hookResult.Message
			}
			return execResult, fmt.Errorf("tool %q %s", tc.Name, msg)
		case hook.ActionModify:
			if modified, ok := hookResult.Data.(map[string]any); ok {
				if newArgs, ok := modified["args"]; ok {
					if raw, err := json.Marshal(newArgs); err == nil {
						tc.Args = raw
						execResult.EffectiveArgsJSON = string(tc.Args)
						execResult.Audit = syncAuditEffectiveArgs(execResult.Audit, tc.Args)
						if s.turn != nil {
							s.turn.updatePendingToolCall(PendingToolCall{CallID: tc.ID, Name: tc.Name, AgentID: s.instanceID, ArgsJSON: execResult.EffectiveArgsJSON, Audit: execResult.Audit})
						}
						s.parent.emitToTUI(ToolCallUpdateEvent{ID: tc.ID, Name: tc.Name, ArgsJSON: execResult.EffectiveArgsJSON, AgentID: s.instanceID})
					}
				}
			}
		}
	}

	// ----- Repetition guard -----
	s.repMu.Lock()
	allowed := s.repetition.Check(tc.Name, tc.Args)
	s.repMu.Unlock()

	if !allowed {
		return execResult, fmt.Errorf(
			"tool %q called too many times with the same arguments (loop detected)",
			tc.Name,
		)
	}

	// ----- Malformed args guard (improvement 1) -----
	// Detect the sentinel set by the streaming parser when the LLM produces
	// invalid JSON, and return a guiding error instead of letting the tool
	// fail with an opaque "field required" message.
	if llm.IsMalformedArgs(tc.Args) {
		slog.Warn("SubAgent: tool call has malformed args, returning guidance error",
			"agent", s.instanceID, "tool", tc.Name,
		)
		return execResult, fmt.Errorf(
			"tool %q was called with malformed arguments (likely due to output "+
				"truncation at max_tokens). Please reduce the number of parallel "+
				"tool calls and retry with properly structured JSON arguments "+
				"matching the tool's input schema",
			tc.Name,
		)
	}

	// ----- Empty args guard (improvement 4) -----
	// Mirrors the MainAgent check: catch empty "{}" args for tools with
	// required parameters before execution, providing a diagnostic message.
	if llm.IsEmptyArgs(tc.Args) {
		if tool, ok := s.tools.Get(tc.Name); ok {
			if req := llm.RequiredFields(tool.Parameters()); len(req) > 0 {
				slog.Warn("SubAgent: tool call has empty args but tool requires parameters",
					"agent", s.instanceID, "tool", tc.Name, "required", req,
				)
				return execResult, fmt.Errorf(
					"tool %q was called with empty arguments {}. This typically "+
						"happens when the model's output was truncated at max_tokens. "+
						"Please reduce the number of parallel tool calls and retry "+
						"with the complete required parameters: %v",
					tc.Name, req,
				)
			}
		}
	}
	if err := validateToolArgsAgainstSchema(s.tools, tc.Name, tc.Args); err != nil {
		return execResult, err
	}
	execResult.PreFilePath, execResult.PreContent, execResult.PreExisted = agentdiff.CapturePreWriteState(tc)

	// Attach execution context metadata so tools can identify the invoking
	// agent and optionally report structured progress.
	agentCtx := buildToolExecContext(ctx, tc, s.instanceID, s.taskID, s.sessionDir, s.parent, s.parent.emitToTUI)
	artifactKey := tc.ID
	if strings.TrimSpace(artifactKey) == "" {
		artifactKey = tc.Name + "-anonymous"
	}

	// ----- FileTracker integration -----
	var (
		trackedFilePath string
		deleteLocks     *deleteLockSet
	)
	if tc.Name == "Read" || tc.Name == "Write" || tc.Name == "Edit" || tc.Name == "Delete" {
		if tc.Name == "Delete" {
			locks, err := acquireDeleteLocks(s.parent.fileTrack, s.instanceID, tc.Args)
			if err != nil {
				var ext *filelock.ExternalModificationError
				if errors.As(err, &ext) {
					return execResult, err
				}
				return execResult, fmt.Errorf("file conflict: %w", err)
			}
			deleteLocks = locks
			if deleteLocks != nil {
				defer deleteLocks.Release()
			}
		} else {
			var parsed struct {
				Path string `json:"path"`
			}
			if json.Unmarshal(tc.Args, &parsed) == nil {
				trackedFilePath = parsed.Path
			}
		}
	}

	// Write/Edit: acquire write lock before execution, release after.
	// Uses the parent MainAgent's FileTracker (shared, goroutine-safe).
	if trackedFilePath != "" && (tc.Name == "Write" || tc.Name == "Edit") {
		currentHash := computeFileHash(trackedFilePath)
		if err := s.parent.fileTrack.AcquireWrite(trackedFilePath, s.instanceID, currentHash); err != nil {
			var ext *filelock.ExternalModificationError
			if errors.As(err, &ext) {
				return execResult, err
			}
			return execResult, fmt.Errorf("file conflict: %w", err)
		}
		defer func() {
			newHash := computeFileHash(trackedFilePath)
			s.parent.fileTrack.ReleaseWrite(trackedFilePath, s.instanceID, newHash)
		}()
	}

	args := llm.UnwrapToolArgs(tc.Args)
	result, err := s.tools.Execute(agentCtx, tc.Name, args)
	if err != nil {
		// Preserve tool output even on error for debugging.
		if result != "" {
			truncated := tools.TruncateOutputWithOptions(result, s.sessionDir, tools.TruncateOptions{ArtifactKey: artifactKey})
			content := tools.NormalizeEmptySuccessOutput(tc.Name, truncated.Content, err)
			content = tools.AppendArtifactGuidance(content, truncated,
				"Use Grep to search the full content or Read with offset/limit to view specific sections.")
			execResult.Result = content
			return execResult, err
		}
		return execResult, err
	}
	if deleteLocks != nil {
		deleteLocks.Commit(result)
	}

	// Read: track content hash after successful execution for optimistic locking.
	if tc.Name == "Read" && trackedFilePath != "" {
		hash := computeFileHash(trackedFilePath)
		s.parent.fileTrack.TrackRead(trackedFilePath, s.instanceID, hash)
	}

	if (tc.Name == "Write" || tc.Name == "Edit") && trackedFilePath != "" {
		if tool, ok := s.tools.Get(tc.Name); ok {
			switch t := tool.(type) {
			case tools.WriteTool:
				if t.LSP != nil {
					execResult.LSPReviews = t.LSP.CurrentReviewSnapshots(trackedFilePath)
				}
			case tools.EditTool:
				if t.LSP != nil {
					execResult.LSPReviews = t.LSP.CurrentReviewSnapshots(trackedFilePath)
				}
			}
		}
	}

	// Apply output truncation.
	truncated := tools.TruncateOutputWithOptions(result, s.sessionDir, tools.TruncateOptions{ArtifactKey: artifactKey})
	content := tools.NormalizeEmptySuccessOutput(tc.Name, truncated.Content, nil)
	content = tools.AppendArtifactGuidance(content, truncated,
		"Use Grep to search the full content or Read with offset/limit to view specific sections.")

	execResult.Result = content
	return execResult, nil
}

// isSubAgentInternalTool reports whether a tool is required for SubAgent
// control flow and must not be blocked by user permission rules.
func isSubAgentInternalTool(toolName string) bool {
	switch toolName {
	case "Complete":
		return true
	default:
		return false
	}
}

// ---------------------------------------------------------------------------
// Turn management
// ---------------------------------------------------------------------------

// newTurn cancels any in-flight work and creates a fresh Turn.
func (s *SubAgent) newTurn() *Turn {
	s.turnMu.Lock()
	defer s.turnMu.Unlock()
	if s.turn != nil {
		cancelledExec := s.turn.cancelPendingToolCalls()
		cancelledStream := s.turn.drainStreamingToolCalls()
		merged := mergePendingToolCalls(cancelledExec, cancelledStream)
		if len(merged) > 0 {
			persistedResults := s.persistInterruptedToolResults(merged, ToolResultStatusCancelled, context.Canceled)
			if persistedResults > 0 {
				slog.Info("SubAgent: persisted interrupted tool-call results before starting new turn",
					"agent", s.instanceID, "count", persistedResults)
			}
			emitCancelledToolResults(s.parent.emitToTUI, merged)
			s.parent.emitActivity(s.instanceID, ActivityIdle, "")
		}
		s.turn.PendingToolCalls.Store(0)
		s.turn.TotalToolCalls.Store(0)
		s.turn.toolExecutionBatches = nil
		s.turn.nextToolBatch = 0
		if s.turn.activeToolBatchCancel != nil {
			s.turn.activeToolBatchCancel()
			s.turn.activeToolBatchCancel = nil
		}
		s.turn.Cancel()
	}
	s.nextTurnID++
	ctx, cancel := context.WithCancel(s.parentCtx)
	s.turn = &Turn{
		ID:                    s.nextTurnID,
		Ctx:                   ctx,
		Cancel:                cancel,
		PendingToolMeta:       make(map[string]PendingToolCall),
		toolExecutionBatches:  nil,
		nextToolBatch:         0,
		activeToolBatchCancel: nil,
	}
	slog.Debug("SubAgent: new turn created",
		"agent", s.instanceID, "turn_id", s.turn.ID)
	return s.turn
}

// currentTurnID is a small helper for log messages.
func (s *SubAgent) currentTurnID() uint64 {
	if s.turn == nil {
		return 0
	}
	return s.turn.ID
}

// ---------------------------------------------------------------------------
// Idle timer
// ---------------------------------------------------------------------------

// resetIdleTimer stops the idle timer and notifies MainAgent to reset
// the nudge counter for this SubAgent.
func (s *SubAgent) resetIdleTimer() {
	if s.idleTimer != nil {
		s.idleTimer.Stop()
		s.idleTimer = nil
	}
	// idleNudges is in MainAgent.nudgeCounts; reset via dedicated event.
	s.parent.sendEvent(Event{
		Type:     EventResetNudge,
		SourceID: s.instanceID,
	})
}

// ---------------------------------------------------------------------------
// Event forwarding
// ---------------------------------------------------------------------------

// sendEvent sends an event to the MainAgent, tagging it with this SubAgent's
// instance ID as the SourceID.
func (s *SubAgent) sendEvent(evt Event) {
	evt.SourceID = s.instanceID
	s.parent.sendEvent(evt)
}

// ---------------------------------------------------------------------------
func (s *SubAgent) visibleToolNames() map[string]struct{} {
	visibleTools := visibleLLMTools(s.tools, s.ruleset, isSubAgentInternalTool)
	return toolNamesFromVisibleTools(visibleTools)
}

func (s *SubAgent) hasVisibleTool(name string) bool {
	visible := s.visibleToolNames()
	_, ok := visible[name]
	return ok
}

func (s *SubAgent) subAgentCoordinationPromptBlock() string {
	visible := s.visibleToolNames()
	lines := []string{"## SubAgent Coordination"}
	if hasVisibleTool(visible, "Notify") {
		lines = append(lines, "- Use `Notify` to surface progress, clarifications, or intermediate results that the owner agent should know before the task is finished")
	} else {
		lines = append(lines, "- `Notify` is unavailable in this role; do not assume you can send non-blocking progress updates to the owner agent")
	}
	if hasVisibleTool(visible, "Escalate") {
		lines = append(lines, "- Call `Escalate` when owner-agent intervention, a cross-task dependency, or a decision is required")
	} else if hasVisibleTool(visible, "Notify") {
		lines = append(lines, "- `Escalate` is unavailable in this role; use `Notify` to surface blockers or owner-agent decisions when you cannot proceed independently")
	} else {
		lines = append(lines, "- `Escalate` is unavailable in this role; if you cannot proceed independently, explain the blocker clearly in assistant text and wait for owner follow-up")
	}
	lines = append(lines, "- Call `Complete` when the task is done; plain text alone does not mark the task complete")
	return strings.Join(lines, "\n")
}

func (s *SubAgent) taskCompletionInstruction() string {
	base := "Focus only on this task. Call `Complete` when done."
	switch {
	case s.hasVisibleTool("Escalate"):
		return base + " Call `Escalate` if you are blocked."
	case s.hasVisibleTool("Notify"):
		return base + " Use `Notify` if you are blocked or need owner-agent input because `Escalate` is unavailable in this role."
	default:
		return base + " If you are blocked and no control tool is available, explain the blocker clearly in assistant text and wait for owner follow-up."
	}
}

// System prompt
// ---------------------------------------------------------------------------

// buildSystemPrompt constructs the SubAgent's system prompt. Unlike
// MainAgent, it does NOT include git status (SubAgent focuses on a single
// task, not repository-wide status) and includes a dedicated "Your Task"
// section.
func (s *SubAgent) buildSystemPrompt() string {
	var parts []string

	parts = append(parts, subAgentIdentityPrompt, sharedAgentValuesPrompt, sharedCodingGuidelinesPrompt, s.subAgentCoordinationPromptBlock(), subAgentResponseClosurePrompt)
	if s.customPrompt != "" {
		parts = append(parts, s.customPrompt)
	}
	if block := s.delegationPromptBlock(); block != "" {
		parts = append(parts, block)
	}
	if block := s.capabilityPromptBlock(); block != "" {
		parts = append(parts, block)
	}

	// 2. Dynamic environment information (no git status per §6.6).
	venvLine := ""
	if s.venvPath != "" {
		venvLine = fmt.Sprintf("\n  Python virtual environment: %s\n  When running Python commands, prefer the interpreter from this virtual environment.", s.venvPath)
	}
	parts = append(parts, fmt.Sprintf(`<env>
  Working directory: %s
  Platform: %s
  Today's date: %s%s
</env>`, s.workDir, runtime.GOOS, time.Now().Format("Mon Jan 2 2006"), venvLine))

	// 3. Task description (core difference from MainAgent).
	parts = append(parts, fmt.Sprintf("## Your Task\n\n%s\n\n%s", s.taskDesc, s.taskCompletionInstruction()))

	// 4. AGENTS.md is delivered as a <system-reminder> meta user message via
	//    cachedSessionReminder (mirrors MainAgent). It does not belong in the
	//    stable system prompt. See docs/architecture/prompt-and-context-engineering.md §4.

	if block := s.availableSkillsPromptBlock(); block != "" {
		parts = append(parts, block)
	}

	return strings.Join(parts, "\n\n---\n\n")
}

func (s *SubAgent) capabilityPromptBlock() string {
	visibleTools := visibleLLMTools(s.tools, s.ruleset, isSubAgentInternalTool)
	visible := toolNamesFromVisibleTools(visibleTools)
	return buildDynamicCapabilityPromptBlock(visible, s.ruleset, capabilityPromptAudienceSub)
}

func (s *SubAgent) delegationPromptBlock() string {
	if s == nil || s.parent == nil {
		return ""
	}
	if _, ok := s.tools.Get("Delegate"); !ok {
		return ""
	}
	agents := s.parent.availableSubAgentsForPrompt()
	if len(agents) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("## Nested Delegation\n")
	sb.WriteString("- You may delegate child work only when the sub-problem is clearly independent and within your configured delegation depth.\n")
	sb.WriteString("- Child workers are owned by you directly; do not assume higher-level ancestors can message or stop them for you.\n")
	sb.WriteString("- When `child_join` is enabled, do not consider your task complete until all joined child tasks have finished or been explicitly stopped.\n")
	sb.WriteString("- If you need to finish early, explicitly stop the child task first; do not assume a later ancestor will clean it up for you.\n")
	sb.WriteString("- Use child control tools only for your own direct children.\n")
	sb.WriteString("\n### Available Child Agent Types\n")
	for _, ac := range agents {
		desc := ac.Description
		if desc == "" {
			desc = "(no description)"
		}
		fmt.Fprintf(&sb, "- **%s**: %s\n", ac.Name, desc)
	}
	return strings.TrimSpace(sb.String())
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// truncateString returns the first n characters of s, appending "..." if
// truncated.
func truncateString(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// ---------------------------------------------------------------------------
// subAgentEventSender adapts SubAgent → tools.EventSender interface
// ---------------------------------------------------------------------------

// subAgentEventSender implements tools.EventSender by forwarding events
// through the SubAgent's parent MainAgent event bus.
type subAgentEventSender struct {
	sub func() *SubAgent // deferred resolution (SubAgent not yet constructed)
}

func (e *subAgentEventSender) SendAgentEvent(eventType, sourceID string, payload any) {
	s := e.sub()
	if s == nil {
		return
	}
	switch eventType {
	case EventEscalate:
		reason, _ := payload.(string)
		s.setState(SubAgentStateWaitingPrimary, reason)
	case EventAgentNotify:
		msg, _ := payload.(string)
		if strings.TrimSpace(msg) != "" {
			s.setState(SubAgentStateRunning, msg)
		}
	}
	if eventType == EventSpawnFinished || eventType == "background_object_finished" {
		s.parent.sendEvent(Event{
			Type:     EventSpawnFinished,
			SourceID: sourceID,
			Payload:  payload,
		})
		return
	}
	s.parent.sendEvent(Event{
		Type:     eventType,
		SourceID: sourceID,
		Payload:  payload,
	})
}
