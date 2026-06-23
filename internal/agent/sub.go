package agent

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/ctxmgr"
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
	CallID           string
	Name             string // tool name, used for empty-args detection
	ArgsJSON         string // original args JSON string for malformed detection
	Audit            *message.ToolArgsAudit
	Result           string
	Images           []message.ContentPart // image parts to inject into model context after the batch completes
	Error            error
	TurnID           uint64
	Duration         time.Duration
	Diff             string              // unified diff for Write/Edit tools; not sent to LLM
	DiffAdded        int                 // full added-line count before any diff truncation
	DiffRemoved      int                 // full removed-line count before any diff truncation
	FileCreated      bool                // true when Write created a file that did not previously exist
	LSPReviews       []message.LSPReview // last-review snapshot for the directly edited file only
	FileState        *message.ToolFileState
	speculativeHooks *speculativeToolHooks
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
// Most mutable state is confined to the runLoop goroutine (single-writer);
// external user input is enqueued via InjectUserMessage / InjectUserMessageWithParts.
// Cross-goroutine lifecycle flags use atomics.
type SubAgent struct {
	instanceID         string // immutable, from NextInstanceID()
	taskID             string // plan task ID or "adhoc-N"
	agentDefName       string // agent definition name (e.g. "backend-coder")
	taskDesc           string // task description (from Plan or ad-hoc)
	planTaskRef        string
	semanticTaskKey    string
	writeScope         tools.WriteScope
	ownerAgentID       string
	ownerTaskID        string
	depth              int
	joinToOwner        bool
	delegation         config.DelegationConfig
	color              string // optional ANSI color code from agent config for TUI display
	llmMu              sync.RWMutex
	llmClient          *llm.Client
	llmRequestInFlight atomic.Bool
	ctxMgr             *ctxmgr.Manager // own context; automatic compaction disabled
	tools              *tools.Registry // shared base + SubAgent-specific tools
	parent             *MainAgent      // reference to parent for event forwarding
	parentCtx          context.Context
	cancel             context.CancelFunc
	recovery           *recovery.RecoveryManager // shared with MainAgent, thread-safe

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

	// Repetition detection removed; tool execution no longer rejects repeated
	// (name, args) calls at the agent layer.

	// System prompt components (set at construction, read-only afterward).
	workDir      string
	venvPath     string // absolute path to detected Python virtual environment, or ""
	sessionDir   string
	agentsMD     string
	loadedSkills []*skill.Meta
	modelName    string
	customPrompt string // from agent YAML body; replaces built-in role instructions if non-empty

	// cachedSessionReminderContent is the meta user message content carrying
	// AGENTS.md (under "# AGENTS.md instructions" / <INSTRUCTIONS>) +
	// currentDate. Built once at construction, injected
	// once per SubAgent lifetime (session-head for SubAgent == construction).
	// Not persisted. Mirrors MainAgent.
	cachedSessionReminderContent string
	// sessionReminderInjected is true once the reminder has been injected.
	sessionReminderInjected bool

	// frozenToolDefs is the SubAgent's tool surface snapshot, computed once at
	// construction. Kept stable so the provider request prefix does not drift.
	frozenToolDefs []message.ToolDefinition

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
	BaseTools     *tools.Registry // shared base tool registry (Read, Write, Edit, Shell, Grep, Glob, etc.)
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
	hasViewImageTool := false
	delegationEnabled := cfg.Depth < cfg.Delegation.EffectiveMaxDepth()
	delegateVisible := delegationEnabled && !cfg.Ruleset.IsDisabled(tools.NameDelegate)
	notifyVisible := !cfg.Ruleset.IsDisabled(tools.NameNotify)

	// Copy all base tools EXCEPT MainAgent-only tools that never belong in a
	// SubAgent (`TodoWrite`, `Handoff`) plus delegation/control-plane tools
	// when this instance's depth/config does not allow nested delegation.
	for _, t := range cfg.BaseTools.ListTools() {
		switch t.Name() {
		case tools.NameTodoWrite, tools.NameHandoff, tools.NameReadArtifact, tools.NameSaveArtifact:
			// Skip MainAgent-only tools.
		case tools.NameNotify:
			// SubAgents get a dedicated Notify tool so owner-notify and
			// targeted-notify availability can diverge by permission group.
		case tools.NameSkill:
			// Register a SubAgent-scoped Skill provider after the SubAgent is
			// fully constructed, so listing/visibility aligns with this
			// SubAgent's own ruleset.
			hasSkillTool = true
		case tools.NameViewImage:
			// Re-register with this SubAgent as the capability provider after it
			// is constructed, so ViewImage visibility tracks the SubAgent's own
			// model rather than the MainAgent's.
			hasViewImageTool = true
		case tools.NameDelegate, tools.NameCancel:
			if !delegateVisible {
				continue
			}
			subTools.Register(t)
		default:
			// Skip MainAgent-only tools.
			if cfg.Ruleset.IsDisabled(t.Name()) {
				continue
			}
			subTools.Register(subAgentToolWithBaseDir(t, cfg.WorkDir))
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
	subTools.Register(tools.SaveArtifactTool{})
	subTools.Register(tools.ReadArtifactTool{})
	if !cfg.Ruleset.IsDisabled(tools.NameEscalate) {
		subTools.Register(tools.NewEscalateTool(sender))
	}
	if notifyVisible || delegateVisible {
		subTools.Register(tools.NewNotifyTool(sender, cfg.Parent, notifyVisible, notifyVisible && delegateVisible))
	}

	// Build the SubAgent's own context manager; sub-agents do not auto-compact.
	ctxMgr := ctxmgr.NewManager(0, 0)

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
	if hasSkillTool && !cfg.Ruleset.IsDisabled(tools.NameSkill) {
		s.tools.Register(tools.NewSkillTool(s))
	}
	if hasViewImageTool && !cfg.Ruleset.IsDisabled(tools.NameViewImage) {
		s.tools.Register(tools.NewViewImageTool(s))
	}

	// Build and install the system prompt.
	prompt := s.buildSystemPrompt()
	cfg.LLMClient.SetSystemPrompt(prompt)
	ctxMgr.SetSystemPrompt(message.Message{
		Role:    "system",
		Content: prompt,
	})

	// Capture session-level context (AGENTS.md + currentDate) as a meta user
	// message and freeze the tool surface. Mirrors MainAgent.
	s.cachedSessionReminderContent = buildSessionContextReminder(s.agentsMD, time.Now())
	s.frozenToolDefs = append(
		[]message.ToolDefinition(nil),
		llmToolDefinitionsFromVisibleTools(s.filteredVisibleToolsForModel(s.modelName))...,
	)

	return s
}

func (s *SubAgent) setServiceTier(tier config.ServiceTier) {
	if s == nil {
		return
	}
	s.llmMu.RLock()
	client := s.llmClient
	s.llmMu.RUnlock()
	if client != nil {
		client.SetServiceTier(tier)
	}
}

// SupportsInput reports whether this SubAgent's active model accepts the given
// input modality (e.g. "image", "pdf"). It mirrors MainAgent.SupportsInput.
func (s *SubAgent) SupportsInput(modality string) bool {
	if s == nil {
		return false
	}
	s.llmMu.RLock()
	client := s.llmClient
	s.llmMu.RUnlock()
	return client != nil && client.SupportsInput(modality)
}

func (s *SubAgent) SupportsViewImageTool() bool {
	if s == nil {
		return false
	}
	s.llmMu.RLock()
	client := s.llmClient
	s.llmMu.RUnlock()
	return client != nil && client.PrimarySupportsViewImageTool()
}

func (s *SubAgent) switchModel(client *llm.Client, modelName string, contextLimit int) {
	if s == nil || client == nil {
		return
	}
	toolDefs := llmToolDefinitionsFromVisibleTools(s.filteredVisibleToolsForModel(modelName))
	s.llmMu.Lock()
	oldClient := s.llmClient
	s.llmClient = client
	s.modelName = modelName
	s.frozenToolDefs = append([]message.ToolDefinition(nil), toolDefs...)
	s.llmMu.Unlock()
	prompt := s.buildSystemPrompt()
	client.SetSystemPrompt(prompt)
	if oldClient != nil && oldClient != client {
		oldClient.InvalidateRouting("model_client_swapped")
	}
	providerRef := client.PrimaryModelRef()
	s.ctxMgr.SetTokenBudgets(contextLimit, client.InputLimitForModelRef(providerRef), 0)
	s.ctxMgr.SetSystemPrompt(message.Message{Role: "system", Content: prompt})
	runningRef := client.RunningModelRef()
	if runningRef == "" {
		runningRef = providerRef
	}
	s.parent.emitToTUI(RunningModelChangedEvent{AgentID: s.instanceID, ProviderModelRef: providerRef, RunningModelRef: runningRef})
}

func (s *SubAgent) llmSnapshot() (*llm.Client, string) {
	if s == nil {
		return nil, ""
	}
	s.llmMu.RLock()
	client := s.llmClient
	modelName := s.modelName
	s.llmMu.RUnlock()
	return client, modelName
}

func (s *SubAgent) thinkingToolcallCompat() *config.ThinkingToolcallCompatConfig {
	client, _ := s.llmSnapshot()
	if client == nil {
		return nil
	}
	return client.ThinkingToolcallCompat()
}

func subAgentToolWithBaseDir(t tools.Tool, workDir string) tools.Tool {
	switch tt := t.(type) {
	case tools.PatchTool:
		if tt.BaseDir == "" {
			tt.BaseDir = workDir
		}
		return tt
	case tools.EditTool:
		if tt.BaseDir == "" {
			tt.BaseDir = workDir
		}
		return tt
	default:
		return t
	}
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
	s.llmMu.RLock()
	toolDefs := append([]message.ToolDefinition(nil), s.frozenToolDefs...)
	s.llmMu.RUnlock()
	if toolDefs == nil {
		toolDefs = llmToolDefinitionsFromVisibleTools(s.filteredVisibleTools())
	}
	if !s.sessionReminderInjected {
		out := injectMetaUserReminder(messages, s.cachedSessionReminderContent)
		if len(out) != len(messages) {
			messages = out
			s.sessionReminderInjected = true
		}
	}
	llmClient, modelName := s.llmSnapshot()
	if llmClient == nil {
		select {
		case s.llmCh <- &llmResult{err: fmt.Errorf("SubAgent %s has no LLM client", s.instanceID), turnID: turn.ID}:
		case <-s.parentCtx.Done():
		}
		return
	}
	if filtered, dropped := filterUnsupportedBinaryPartsForModel(messages, llmClient); dropped.any() {
		log.Warnf("SubAgent dropping unsupported binary parts before LLM request agent=%v kinds=%s", s.instanceID, dropped.summary())
		s.parent.emitToTUI(ToastEvent{Level: "warn", Message: "Input dropped (unsupported): " + dropped.summary(), AgentID: s.instanceID})
		messages = filtered
	}
	compatCfg := llmClient.ThinkingToolcallCompat()
	scrubThinkingMarkers := compatCfg != nil && compatCfg.EnabledValue()

	s.llmRequestInFlight.Store(true)
	go func() {
		defer func() {
			s.llmRequestInFlight.Store(false)
			s.parent.sendEvent(Event{Type: EventSubAgentRequestBoundary, SourceID: s.instanceID})
		}()

		// Hook: on_before_llm_call (mirrors MainAgent.callLLM).
		hookResult, hookErr := s.fireHook(turn.Ctx, hook.OnBeforeLLMCall, turn.ID, map[string]any{
			"model":         modelName,
			"message_count": len(messages),
		})
		if hookErr != nil {
			log.Warnf("SubAgent on_before_llm_call hook error agent=%v error=%v", s.instanceID, hookErr)
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
				log.Warnf("SubAgent on_before_llm_call hook returned modify action; not supported, continuing agent=%v", s.instanceID)
			}
		}

		// Streaming callback: forward deltas to the parent's TUI output
		// tagged with this SubAgent's instance ID. Shared reducer logic mirrors
		// MainAgent for text/tool/status handling while preserving SubAgent's
		// historical immediate thinking-delta behavior.
		streamingPromoted := false
		promoteStreamingActivity := func(source string) {
			if streamingPromoted {
				return
			}
			streamingPromoted = true
			log.Debugf("subagent promoting streaming activity agent=%v turn_id=%v source=%v", s.instanceID, turn.ID, source)
			s.parent.emitActivity(s.instanceID, ActivityStreaming, "")
		}

		streamReducer := s.newSubLLMStreamReducer(turn, promoteStreamingActivity, scrubThinkingMarkers)

		callback := streamReducer.Handle

		s.parent.emitActivity(s.instanceID, ActivityConnecting, "")
		resp, err := llmClient.CompleteStream(turn.Ctx, messages, toolDefs, callback)
		streamReducer.Finish() // final flush: emit any remaining accumulated text
		if turn.Ctx.Err() != nil {
			return // turn cancelled
		}

		// Track usage for cost analytics (mirrors MainAgent.callLLM).
		if err == nil && resp != nil {
			callStatus := llmClient.LastCallStatus()
			selectedRef := llmClient.PrimaryModelRef()
			runningRef := callStatus.RunningModelRef
			if runningRef == "" {
				runningRef = llmClient.RunningModelRef()
			}
			// Set context limit so the info panel gauge shows correct capacity
			// when TUI focus is on this SubAgent (mirrors MainAgent.callLLM).
			if runningRef != "" {
				if lim := llmClient.ContextLimitForModelRef(runningRef); lim > 0 {
					s.ctxMgr.SetTokenBudgets(lim, llmClient.InputLimitForModelRef(runningRef), 0)
				}
			}
			s.parent.recordUsage(s.instanceID, "sub", s.agentDefName, "chat", selectedRef, runningRef, turn.ID, resp.Usage, callStatus.ServiceTier)

			// Hook: on_after_llm_call.
			subInputTok, subOutputTok := 0, 0
			if resp.Usage != nil {
				subInputTok = resp.Usage.InputTokens
				subOutputTok = resp.Usage.OutputTokens
			}
			afterResult, afterErr := s.fireHook(turn.Ctx, hook.OnAfterLLMCall, turn.ID, map[string]any{
				"model":         modelName,
				"input_tokens":  subInputTok,
				"output_tokens": subOutputTok,
				"tool_calls":    len(resp.ToolCalls),
			})
			if afterErr != nil {
				log.Warnf("SubAgent on_after_llm_call hook error agent=%v error=%v", s.instanceID, afterErr)
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
					log.Warnf("SubAgent on_after_llm_call hook returned modify action; not supported, continuing agent=%v", s.instanceID)
				}
			}
		}

		select {
		case s.llmCh <- &llmResult{resp: resp, err: err, turnID: turn.ID}:
		case <-s.parentCtx.Done():
		}
	}()
}

func (s *SubAgent) newSubLLMStreamReducer(turn *Turn, promoteStreamingActivity func(string), scrubThinkingMarkers bool) *llmStreamReducer {
	streamReducer := &llmStreamReducer{}
	streamReducer.content = streamContentReducer{
		agentID:               s.instanceID,
		emit:                  s.parent.emitToTUI,
		scrubThinkingDelta:    false,
		scrubThinkingFinal:    scrubThinkingMarkers,
		thinkingCommitMode:    streamContentCommitFullText,
		textFlushInterval:     defaultStreamTextFlushInterval,
		thinkingFlushInterval: 0,
	}
	streamReducer.tool = streamToolDeltaReducer{
		agentID:                  s.instanceID,
		turn:                     turn,
		registry:                 s.tools,
		ruleset:                  func() permission.Ruleset { return s.ruleset },
		visibleToolNames:         s.visibleToolNames,
		emit:                     s.parent.emitToTUI,
		flushBeforeTool:          streamReducer.content.flushTextDelta,
		promoteStreamingActivity: promoteStreamingActivity,
		recordToolUseEnd:         s.parent.recordToolTraceToolUseEnd,
		discardSpeculativeOnRollback: func(turn *Turn, reason string) {
			s.parent.discardSpeculativeStreamToolsAndClearToolTrace(turn, reason)
		},
	}
	streamReducer.emitActivity = func(activity ActivityType, detail string) {
		s.parent.emitActivity(s.instanceID, activity, detail)
	}
	streamReducer.promoteStreamingActivity = promoteStreamingActivity
	streamReducer.onRetryError = func(err error, provider, model, keySuffix string) {
		s.parent.emitToTUI(ErrorEvent{
			Err:      err,
			AgentID:  s.instanceID,
			Silent:   true,
			Provider: provider,
			Model:    model,
			Key:      keySuffix,
		})
	}
	streamReducer.onError = func(text string) {
		log.Warnf("SubAgent LLM stream error delta text=%v agent=%v", text, s.instanceID)
	}
	return streamReducer
}

// ---------------------------------------------------------------------------
// Tool execution
// ---------------------------------------------------------------------------

// executeToolCall runs a single tool invocation with permission checks,
// repetition detection, hook interception, and output truncation.
// It uses the SubAgent's own ruleset but the parent's hookEngine and confirmFn.
func (s *SubAgent) executeToolCall(ctx context.Context, tc message.ToolCall) (ToolExecutionResult, error) {
	return s.toolExecutionPipeline().execute(ctx, tc, true)
}

// isSubAgentInternalTool reports whether a tool is required for SubAgent
// control flow and must not be blocked by user permission rules.
func isSubAgentInternalTool(toolName string) bool {
	switch tools.NormalizeName(toolName) {
	case tools.NameComplete:
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
		merged = s.turn.filterCompletedToolCalls(merged)
		if len(merged) > 0 {
			persistedResults := finalizeInterruptedToolCalls(s.ctxMgr, s.parent.emitToTUI, s.persistInterruptedToolResults, merged, ToolResultStatusCancelled, context.Canceled)
			if persistedResults > 0 {
				log.Infof("SubAgent: persisted interrupted tool-call results before starting new turn agent=%v count=%v", s.instanceID, persistedResults)
			}
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
	s.turn.streamingToolExec = NewStreamingToolExecutor(s.turn.ID, ctx, s.parent.emitToTUI, s.executeToolCallSpeculative)
	s.turn.streamingToolExec.SetProjectRoot(s.parent.projectRoot)
	s.turn.streamingToolExec.SetTraceCallbacks(s.parent.recordToolTraceSpeculativeStart, s.parent.recordToolTraceFirstVisibleResult, s.parent.recordToolTraceSpeculativeDiscard)
	log.Debugf("SubAgent: new turn created agent=%v turn_id=%v", s.instanceID, s.turn.ID)
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
	// idleNudges is in MainAgent.subs.nudgeCounts; reset via dedicated event.
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
	return toolNamesFromVisibleTools(s.filteredVisibleTools())
}

func (s *SubAgent) filteredVisibleTools() []tools.Tool {
	_, modelName := s.llmSnapshot()
	return s.filteredVisibleToolsForModel(modelName)
}

func (s *SubAgent) filteredVisibleToolsForModel(modelName string) []tools.Tool {
	visibleTools := visibleLLMTools(s.tools, s.ruleset, isSubAgentInternalTool)
	return filterEditToolsByModel(visibleTools, modelName, s.ruleset)
}

func (s *SubAgent) hasVisibleTool(name string) bool {
	visible := s.visibleToolNames()
	_, ok := visible[name]
	return ok
}

func (s *SubAgent) subAgentCoordinationPromptBlock() string {
	visible := s.visibleToolNames()
	lines := []string{"## SubAgent Coordination"}
	if hasVisibleTool(visible, tools.NameNotify) {
		lines = append(lines, "- Use "+toolPromptName(tools.NameNotify)+" to surface progress, clarifications, or intermediate results that the owner agent should know before the task is finished")
	} else {
		lines = append(lines, "- "+toolPromptName(tools.NameNotify)+" is unavailable in this role; do not assume you can send non-blocking progress updates to the owner agent")
	}
	if hasVisibleTool(visible, tools.NameEscalate) {
		lines = append(lines, "- Call "+toolPromptName(tools.NameEscalate)+" when owner-agent intervention, a cross-task dependency, or a decision is required")
	} else if hasVisibleTool(visible, tools.NameNotify) {
		lines = append(lines, "- "+toolPromptName(tools.NameEscalate)+" is unavailable in this role; use "+toolPromptName(tools.NameNotify)+" to surface blockers or owner-agent decisions when you cannot proceed independently")
	} else {
		lines = append(lines, "- "+toolPromptName(tools.NameEscalate)+" is unavailable in this role; if you cannot proceed independently, explain the blocker clearly in assistant text and wait for owner follow-up")
	}
	lines = append(lines, "- Call "+toolPromptName(tools.NameComplete)+" when the task is done; plain text alone does not mark the task complete")
	return strings.Join(lines, "\n")
}

func (s *SubAgent) taskCompletionInstruction() string {
	base := "Focus only on this task. Call " + toolPromptName(tools.NameComplete) + " when done."
	switch {
	case s.hasVisibleTool(tools.NameEscalate):
		return base + " Call " + toolPromptName(tools.NameEscalate) + " if you are blocked."
	case s.hasVisibleTool(tools.NameNotify):
		return base + " Use " + toolPromptName(tools.NameNotify) + " if you are blocked or need owner-agent input because " + toolPromptName(tools.NameEscalate) + " is unavailable in this role."
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

	// Dynamic environment information (no git status for sub-agents).
	venvLine := ""
	if s.venvPath != "" {
		venvLine = fmt.Sprintf("\n  Python virtual environment: %s\n  When running Python commands, prefer the interpreter from this virtual environment.", displayPathFromWorkDir(s.workDir, s.venvPath))
	}
	parts = append(parts, fmt.Sprintf(`<env>
  Working directory: %s
  Platform: %s
  Today's date: %s%s
</env>`, s.workDir, runtime.GOOS+"/"+runtime.GOARCH, time.Now().Format("Mon Jan 2 2006"), venvLine))

	// Task description (core difference from MainAgent).
	parts = append(parts, fmt.Sprintf("## Your Task\n\n%s\n\n%s", s.taskDesc, s.taskCompletionInstruction()))

	if block := agentsMDReminderFramingPromptBlock(s.agentsMD); block != "" {
		parts = append(parts, block)
	}
	// AGENTS.md is delivered as a meta user message under a
	// "# AGENTS.md instructions" / <INSTRUCTIONS> self-identifying block via
	// cachedSessionReminder (mirrors MainAgent). It does not belong in the
	// stable system prompt.

	if block := s.availableSkillsPromptBlock(); block != "" {
		parts = append(parts, block)
	}

	return strings.Join(parts, "\n\n---\n\n")
}

func (s *SubAgent) capabilityPromptBlock() string {
	visible := toolNamesFromVisibleTools(s.filteredVisibleTools())
	return buildDynamicCapabilityPromptBlock(visible, s.ruleset, capabilityPromptAudienceSub)
}

func (s *SubAgent) delegationPromptBlock() string {
	if s == nil || s.parent == nil {
		return ""
	}
	if _, ok := s.tools.Get(tools.NameDelegate); !ok {
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
		s.setState(SubAgentStateWaitingMain, reason)
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
