package agent

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
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
	CallID      string
	Name        string // tool name, used for empty-args detection
	ArgsJSON    string // original args JSON string for malformed detection
	Audit       *message.ToolArgsAudit
	Result      string
	Images      []message.ContentPart // image parts to inject into model context after the batch completes
	Error       error
	TurnID      uint64
	Duration    time.Duration
	Diff        string              // unified diff for Write/Edit tools; not sent to LLM
	DiffAdded   int                 // full added-line count before any diff truncation
	DiffRemoved int                 // full removed-line count before any diff truncation
	FileCreated bool                // true when Write created a file that did not previously exist
	LSPReviews  []message.LSPReview // per-file last-review snapshots for directly edited files
	FileState   *message.ToolFileState
	// RecoveryState classifies synthetic results synthesized during session
	// restore or a failed intent barrier (not_started / outcome_unknown).
	RecoveryState string
	// walltimeTarget preserves this SubAgent instance's session attribution
	// until its result is processed by the independent event loop.
	walltimeTarget *walltimeTarget
	// composed carries the display/context texts when the execution goroutine
	// already ran composition and the sync append hook.
	composed         *composedToolResultTexts
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
	writeScopeMu       sync.RWMutex
	writeScope         tools.WriteScope
	ownerMu            sync.RWMutex
	ownerAgentID       string
	ownerTaskID        string
	depth              int
	joinToOwner        bool
	delegation         config.DelegationConfig
	color              string // optional ANSI color code from agent config for TUI display
	llmMu              sync.RWMutex
	llmClient          *llm.Client
	llmRequestInFlight atomic.Bool
	// llmRequestSeq counts this agent's LLM request goroutines. It is written
	// only by the run loop before spawning one, so the goroutine can capture a
	// stable streaming-segment identity (see StreamSegmentEndedEvent).
	llmRequestSeq        uint64
	unsupportedPartToast toastGate
	persistenceHealth    agentPersistenceHealth
	startupWatchdogSeq   atomic.Uint64
	done                 chan struct{}
	doneOnce             sync.Once
	// llmWG tracks in-flight LLM-request goroutines. runLoop exits as soon as
	// parentCtx is cancelled, but an LLM goroutine finishing concurrently still
	// performs post-call synchronous writes (usage ledger, hooks); runLoop joins
	// this group before closing done so shutdown never returns while a SubAgent
	// is mid-write.
	llmWG       sync.WaitGroup
	started     atomic.Bool
	lifecycleMu sync.Mutex      // coordinates parking with external wake-and-deliver operations
	ctxMgr      *ctxmgr.Manager // own context; automatic compaction disabled
	tools       *tools.Registry // shared base + SubAgent-specific tools
	parent      *MainAgent      // reference to parent for event forwarding
	parentCtx   context.Context
	cancel      context.CancelFunc
	// recovery is the fallback manager for a parentless sub-agent (tests and
	// standalone runs). With a parent, writes resolve the manager per write
	// through recoveryManager(): compaction replaces the parent's manager
	// mid-session, and a pointer captured at spawn time would keep writing to
	// the closed one — silently, since a write after Close is not a disk fault.
	recovery *recovery.RecoveryManager
	// sessionEpoch is the parent session this sub-agent persists for. A switch
	// advances the parent's epoch, which stops this agent's in-flight writes
	// from landing in the session that replaced its own.
	sessionEpoch uint64

	// turnMu guards concurrent CancelSubAgent vs runLoop turn creation.
	turnMu sync.Mutex
	// Turn isolation (same Turn struct as MainAgent).
	turn       *Turn
	nextTurnID uint64

	// Event channels (buffered to avoid producer blocking).
	inputCh                    chan pendingUserMessage // buffered user messages from main (cap = inputChanCap)
	ctxAppendCh                chan message.Message    // context-only user lines (e.g. !shell output); no LLM call
	llmCh                      chan *llmResult         // cap=1, LLM responses
	toolCh                     chan *toolResult        // cap=8, tool results
	continueCh                 chan continueMsg        // cap=1; signals ContinueFromContext/cancel
	wakeCh                     chan struct{}           // cap=1; forces the loop to re-evaluate state-gated channels
	inputQueueMu               sync.Mutex
	inputOverflow              []pendingUserMessage
	inputQueueBytes            int
	inputQueueReservedMessages int
	inputQueueReservedBytes    int
	ctxAppendQueueMu           sync.Mutex
	ctxAppendOverflow          []message.Message
	ctxAppendBytes             int
	queueMessageLimit          int
	queueByteLimit             int
	compactUsage               float64
	reductionMu                sync.RWMutex
	reductionStats             ContextReductionStats
	promotedToolQueue          []*toolResult // event-loop-owned FIFO; avoids sending results back into the active loop
	pendingContinue            *continueMsg  // event-loop-owned restart deferred until the current LLM request exits

	// editMatchFailStreak counts repeated approximate-match failures per
	// target path for edit/apply_patch within the current turn, mirroring
	// MainAgent, so repeated drift failures also steer SubAgent retries
	// toward a fresh bounded read. Event-loop-owned; no locking needed.
	editMatchFailStreak map[string]int
	// applyPatchRetry mirrors MainAgent's unchanged-patch gate. Execution
	// goroutines consult it, so the guard synchronizes its own state.
	applyPatchRetry applyPatchRetryGuard

	startupTimeout time.Duration

	// In-flight LLM silence watchdog: bounds how long a request may produce
	// nothing before the run loop cancels it and runs bounded recovery, so a
	// lost request goroutine can never park the loop on llmCh forever. All
	// fields are owned by the runLoop goroutine; see runLoop in sub_event.go.
	llmSilenceBudget          time.Duration // 0 disables the watchdog
	llmSilenceTimer           *time.Timer
	pendingLLMSilenceRecovery string    // recovery instruction awaiting a free in-flight gate
	llmSilenceEscalatedAt     time.Time // when the current recovery wait started
	llmSilenceRecoveries      int       // watchdog restarts since the last real request boundary
	llmSilenceAbandonedTurnID uint64    // turn failed for silence; its late results are dropped

	// pendingComplete is set when Complete appears alongside other tool
	// calls in one LLM response. The other tools execute first; EventAgentDone
	// is sent once all of them complete. This prevents the last batch of file
	// edits from being silently dropped.
	pendingComplete       *AgentResult
	pendingCompleteCallID string
	// pendingRejectedCompleteCallID / pendingRejectedCompleteErr record a
	// Complete call whose arguments failed validation while other tool calls
	// in the same response were still executing. The rejection is appended as
	// a tool result once those calls settle (finalize in handleToolResult),
	// giving the model one bounded follow-up to fix the call. Unlike
	// pendingComplete it is never persisted: a crash mid-batch replays the
	// invalid Complete from the transcript instead.
	// pendingRejectedCompleteDegraded carries the delivery that survives the
	// rejection when only the typed-result group was malformed (see
	// rejectInvalidCompleteArguments); nil for every other failure.
	pendingRejectedCompleteCallID   string
	pendingRejectedCompleteErr      error
	pendingRejectedCompleteDegraded *AgentResult
	pendingEscalate                 string
	pendingEscalateRequest          *tools.AgentRequestPayload
	acceptedMailboxIDs              map[string]struct{} // guarded by inputQueueMu; de-duplicates durable deliveries

	// Permission: merged ruleset (global + project + agent-level).
	//
	// Published as an immutable snapshot behind an atomic pointer: the batch
	// scheduler and the MainAgent's event loop read it while a parallel tool
	// goroutine can replace it after a confirmation rule intent
	// (refreshRulesetAfterRuleIntent), so a plain field would be a data race.
	// Every writer stores a freshly built ruleset and never appends to a
	// published one.
	rulesetPtr atomic.Pointer[permission.Ruleset]

	// Repetition detection removed; tool execution no longer rejects repeated
	// (name, args) calls at the agent layer.

	// System prompt components (set at construction, read-only afterward).
	workDir       string
	venvPath      string // absolute path to detected Python virtual environment, or ""
	sessionDir    string
	agentsMD      string
	loadedSkills  []*skill.Meta
	skillsMu      sync.RWMutex
	invokedSkills map[string]*skill.Meta
	modelName     string
	customPrompt  string // from agent YAML body; replaces built-in role instructions if non-empty

	// cachedSessionReminderContent is the meta user message content carrying
	// environment + AGENTS.md (under "# AGENTS.md instructions" /
	// <INSTRUCTIONS>). Built once at construction (session-head for SubAgent ==
	// construction), injected into every request so the prompt prefix keeps one
	// stable shape. Not persisted. Mirrors MainAgent.
	cachedSessionReminderContent string

	// frozenToolDefs is the SubAgent's tool surface snapshot, computed once at
	// construction. Kept stable so the provider request prefix does not drift.
	frozenToolDefs            []message.ToolDefinition
	taskChangesMu             sync.Mutex
	actualChangedFiles        map[string]struct{}
	fileAttributionIncomplete bool

	// semHeld is true when this SubAgent holds a slot in the MainAgent's
	// concurrency semaphore. Set by CreateSubAgent; restored agents do not
	// hold a slot (they are idle and don't count against the concurrency limit).
	semHeld bool
	// semBorrowed marks a slot granted from the bounded borrow pool; its
	// release decrements the governor's borrow debt. semBypassed marks an
	// uncounted wake-reactivation grant issued when both pools were exhausted;
	// releasing it returns no capacity.
	semBorrowed bool
	semBypassed bool
	semMu       sync.Mutex

	runtimeState subAgentRuntimeState
}

func (s *SubAgent) slotState() (held, bypassed bool) {
	s.semMu.Lock()
	defer s.semMu.Unlock()
	return s.semHeld, s.semBypassed
}

func (s *SubAgent) waitDone(ctx context.Context) error {
	if s == nil || s.done == nil || !s.started.Load() {
		return nil
	}
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *SubAgent) startRunLoop() {
	if s == nil || !s.started.CompareAndSwap(false, true) {
		return
	}
	go s.runLoop()
}

// recoveryManager resolves the manager this sub-agent's writes belong to. With
// a parent it is resolved per write and epoch-checked, so the agent follows a
// compaction that replaced the manager and stops writing once a session switch
// replaced its session. Without a parent (tests, standalone runs) it is the
// manager handed in at construction.
func (s *SubAgent) recoveryManager() *recovery.RecoveryManager {
	if s == nil {
		return nil
	}
	if s.parent == nil {
		return s.recovery
	}
	return s.parent.recoveryManagerForEpoch(s.sessionEpoch)
}

func (s *SubAgent) persistMessageAsync(msg message.Message, description string, after func()) {
	if s == nil {
		return
	}
	if s.parent == nil {
		if manager := s.recoveryManager(); manager != nil {
			if err := manager.PersistMessage(s.instanceID, msg); err != nil {
				log.Warnf("SubAgent: failed to persist %s agent=%v error=%v", description, s.instanceID, err)
				s.notePersistenceFailure(err)
				return
			}
		}
		if after != nil {
			after()
		}
		return
	}
	s.notePersistenceEnqueue(s.parent.persistAsyncForEpoch(s.sessionEpoch, s.instanceID, msg, func(err error) {
		if err != nil {
			s.notePersistenceFailure(err)
			return
		}
		if after != nil {
			after()
		}
	}))
}

func (s *SubAgent) notePersistenceEnqueue(enqueued bool) bool {
	if s != nil && !enqueued {
		s.notePersistenceFailure(errPersistenceQueueUnavailable)
	}
	return enqueued
}

// persistMessageBarrier persists msg and returns a channel that receives the
// write result exactly once. When the write path is synchronous (no parent
// pump) or recovery is nil, the result is already buffered before the channel
// is returned. The returned enqueued flag is false only when the parent pump
// rejected the entry (shutdown); the channel still carries a non-nil error so
// callers can wait uniformly.
func (s *SubAgent) persistMessageBarrier(msg message.Message, description string) (<-chan error, bool) {
	barrier := make(chan error, 1)
	manager := s.recoveryManager()
	if s == nil || manager == nil {
		barrier <- nil
		return barrier, true
	}
	if s.parent == nil {
		var err error
		if writeErr := manager.PersistMessage(s.instanceID, msg); writeErr != nil {
			log.Warnf("SubAgent: failed to persist %s agent=%v error=%v", description, s.instanceID, writeErr)
			err = writeErr
		}
		if err != nil {
			s.notePersistenceFailure(err)
		}
		barrier <- err
		return barrier, true
	}
	enqueued := s.parent.persistAsyncForEpoch(s.sessionEpoch, s.instanceID, msg, func(err error) {
		if err != nil {
			s.notePersistenceFailure(err)
		}
		barrier <- err
	})
	if !enqueued {
		s.notePersistenceFailure(errPersistenceQueueUnavailable)
		barrier <- errPersistenceQueueUnavailable
	}
	return barrier, enqueued
}

// waitPersistBarrier waits for a persistence barrier result, failing closed on
// shutdown, pump-stop, or the agent's own cancellation.
func (s *SubAgent) waitPersistBarrier(barrier <-chan error, enqueued bool) error {
	var stop <-chan struct{}
	if s.parent != nil {
		stop = s.parent.stoppingCh
	} else if s.turn != nil {
		stop = s.turn.Ctx.Done()
	}
	return waitPersistBarrierOn(barrier, enqueued, stop)
}

func (s *SubAgent) transcriptPersistenceHealthy() bool {
	return s != nil && s.PersistenceHealth().State == PersistenceHealthy
}

func (s *SubAgent) PersistenceHealth() PersistenceHealth {
	if s == nil {
		return PersistenceHealth{State: PersistenceHealthy}
	}
	return s.persistenceHealth.snapshot()
}

func (s *SubAgent) notePersistenceFailure(err error) {
	if s == nil || err == nil {
		return
	}
	// See MainAgent.notePersistenceFailure: writes racing an intentional
	// Close are not disk failures and must not mint a degraded SubAgent.
	if errors.Is(err, recovery.ErrClosed) {
		log.Debugf("SubAgent persistence write after close ignored agent=%v error=%v", s.instanceID, err)
		return
	}
	if !s.persistenceHealth.markDegraded(err) {
		return
	}
	log.Warnf("SubAgent persistence degraded agent=%v task_id=%v error=%v", s.instanceID, s.taskID, err)
	if s.parent != nil {
		_ = s.parent.persistSubAgentMeta(s)
		s.parent.updateTaskRecordFromSub(s, "")
		_ = s.parent.persistTaskRegistry()
		s.parent.saveRecoverySnapshot()
	}
}

func (s *SubAgent) checkpointTranscript() error {
	manager := s.recoveryManager()
	if s == nil || manager == nil || !s.persistenceHealth.beginRecovery() {
		return nil
	}
	if s.parent != nil {
		_ = s.parent.persistSubAgentMeta(s)
		s.parent.updateTaskRecordFromSub(s, "")
	}
	if err := manager.RewriteLog(s.instanceID, s.ctxMgr.Snapshot()); err != nil {
		s.notePersistenceFailure(err)
		return err
	}
	s.persistenceHealth.markRecovered()
	if s.parent != nil {
		if err := s.parent.persistSubAgentMeta(s); err != nil {
			s.notePersistenceFailure(err)
			return err
		}
		s.parent.updateTaskRecordFromSub(s, "")
		if err := s.parent.persistTaskRegistry(); err != nil {
			s.notePersistenceFailure(err)
			return err
		}
		s.parent.saveRecoverySnapshot()
	}
	log.Infof("SubAgent persistence recovered after transcript checkpoint agent=%v task_id=%v", s.instanceID, s.taskID)
	return nil
}

// DefaultSubAgentStartupTimeout bounds how long a running worker may retain
// queued input without creating a turn. It catches lost wakeups that the
// in-flight silence watchdog cannot see (no request was ever started).
const DefaultSubAgentStartupTimeout = 15 * time.Second

// defaultSubAgentLLMSilenceBudget bounds how long an in-flight LLM request may
// produce nothing (no stream delta, no completion) before the run-loop
// watchdog intervenes. Healthy transports already abort silence far sooner
// through the client's per-chunk idle timeouts, so this only catches the
// residual gap (a request wedged before chunk-level enforcement starts, or a
// transport that stops erroring entirely). It is deliberately generous — well
// above slow-phase thinking windows and retry cooldowns — yet below the
// coordination stall threshold so a wedged request self-heals before the
// main-side stall sweep would flag the worker.
const defaultSubAgentLLMSilenceBudget = 6 * time.Minute

// maxSubAgentLLMSilentRecoveries bounds how many times the silence watchdog
// restarts a wedged request before it abandons the turn with
// EventAgentError (which surfaces a risk alert to the owner). One bounded
// recovery keeps the worst-case silent window at roughly twice the budget.
const maxSubAgentLLMSilentRecoveries = 1

// ---------------------------------------------------------------------------
// Constructor
// ---------------------------------------------------------------------------

// SubAgentConfig holds the parameters for creating a new SubAgent.
type SubAgentConfig struct {
	InstanceID     string
	TaskID         string
	AgentDefName   string
	TaskDesc       string
	PlanTaskRef    string
	SemanticKey    string
	WriteScope     tools.WriteScope
	OwnerAgentID   string
	OwnerTaskID    string
	Depth          int
	JoinToOwner    bool
	Delegation     config.DelegationConfig
	Color          string
	SystemPrompt   string // custom role instructions from agent YAML body; empty = use built-in
	LLMClient      *llm.Client
	Recovery       *recovery.RecoveryManager
	SessionEpoch   uint64 // parent session epoch this agent's writes belong to
	Parent         *MainAgent
	ParentCtx      context.Context
	Cancel         context.CancelFunc
	BaseTools      *tools.Registry // shared base tool registry (Read, Write, Edit, Shell, Grep, Glob, etc.)
	ExtraMCPTools  []tools.Tool    // agent-specific MCP tools
	Ruleset        permission.Ruleset
	WorkDir        string
	VenvPath       string // absolute path to detected Python virtual environment, or ""
	SessionDir     string
	AgentsMD       string
	Skills         []*skill.Meta
	ModelName      string
	StartupTimeout time.Duration // 0 → DefaultSubAgentStartupTimeout
	Orchestration  config.OrchestrationConfig
}

// NewSubAgent creates a fully-initialised SubAgent. The caller must invoke
// runLoop in a separate goroutine to start the event loop.
func NewSubAgent(cfg SubAgentConfig) *SubAgent {
	if cfg.StartupTimeout <= 0 {
		cfg.StartupTimeout = DefaultSubAgentStartupTimeout
	}

	// Build SubAgent's tool registry: clone base tools, then replace
	// MainAgent-only tools with SubAgent-specific ones.
	subTools := tools.NewRegistry()
	hasSkillTool := false
	hasViewImageTool := false
	var s *SubAgent
	delegationEnabled := cfg.Depth < cfg.Delegation.EffectiveMaxDepth()
	delegateCreator := subAgentDelegateCreator{
		parent: cfg.Parent,
		ruleset: func() permission.Ruleset {
			if s != nil {
				return s.currentRuleset()
			}
			return cfg.Ruleset
		},
	}
	delegateVisible := delegationEnabled && cfg.Parent != nil && len(delegateCreator.AvailableSubAgents()) > 0
	notifyVisible := !cfg.Ruleset.IsDisabled(tools.NameNotify)

	// Copy all base tools EXCEPT MainAgent-only tools that never belong in a
	// SubAgent (`TodoWrite`, `Handoff`) plus delegation/control-plane tools
	// when this instance's depth/config does not allow nested delegation.
	for _, t := range cfg.BaseTools.ListTools() {
		switch t.Name() {
		case tools.NameTodoWrite, tools.NameHandoff, tools.NameReadArtifact, tools.NameSaveArtifact, tools.NameCompactContext:
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
		case tools.NameDelegate:
			if !delegateVisible {
				continue
			}
			subTools.Register(tools.NewDelegateTool(delegateCreator))
		case tools.NameCancel:
			if !delegateVisible {
				continue
			}
			subTools.Register(t)
		default:
			// A task's write scope never removes command tools from the
			// worker: shell side effects cannot be path-validated, so their
			// availability follows the role's permission rules alone (a
			// wildcard deny keeps them out of the registry).
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
		instanceID:        cfg.InstanceID,
		done:              make(chan struct{}),
		taskID:            cfg.TaskID,
		agentDefName:      cfg.AgentDefName,
		taskDesc:          cfg.TaskDesc,
		planTaskRef:       strings.TrimSpace(cfg.PlanTaskRef),
		semanticTaskKey:   strings.TrimSpace(cfg.SemanticKey),
		writeScope:        cfg.WriteScope.Normalized(),
		ownerAgentID:      strings.TrimSpace(cfg.OwnerAgentID),
		ownerTaskID:       strings.TrimSpace(cfg.OwnerTaskID),
		depth:             cfg.Depth,
		joinToOwner:       cfg.JoinToOwner,
		delegation:        cfg.Delegation,
		color:             cfg.Color,
		llmClient:         cfg.LLMClient,
		ctxMgr:            ctxMgr,
		tools:             subTools,
		parent:            cfg.Parent,
		parentCtx:         cfg.ParentCtx,
		cancel:            cfg.Cancel,
		recovery:          cfg.Recovery,
		sessionEpoch:      cfg.SessionEpoch,
		workDir:           cfg.WorkDir,
		venvPath:          cfg.VenvPath,
		sessionDir:        cfg.SessionDir,
		agentsMD:          cfg.AgentsMD,
		loadedSkills:      cfg.Skills,
		invokedSkills:     make(map[string]*skill.Meta),
		modelName:         cfg.ModelName,
		queueMessageLimit: cfg.Orchestration.EffectiveSubAgentQueueMessages(),
		queueByteLimit:    cfg.Orchestration.EffectiveSubAgentQueueBytes(),
		compactUsage:      cfg.Orchestration.EffectiveSubAgentCompactUsage(),
		customPrompt:      cfg.SystemPrompt,
		startupTimeout:    cfg.StartupTimeout,
		llmSilenceBudget:  defaultSubAgentLLMSilenceBudget,
		inputCh:           make(chan pendingUserMessage, inputChanCap),
		ctxAppendCh:       make(chan message.Message, 16),
		llmCh:             make(chan *llmResult, 1),
		toolCh:            make(chan *toolResult, 8),
		continueCh:        make(chan continueMsg, 1),
		wakeCh:            make(chan struct{}, 1),
	}
	s.setRuleset(cfg.Ruleset)
	if !s.setState(SubAgentStateRunning, "") {
		panic(fmt.Sprintf("new SubAgent %s rejected initial running state", s.instanceID))
	}
	if hasSkillTool && !cfg.Ruleset.IsDisabled(tools.NameSkill) {
		s.tools.Register(tools.NewSkillTool(s))
	}
	if hasViewImageTool && !cfg.Ruleset.IsDisabled(tools.NameViewImage) {
		tool := tools.NewViewImageTool(s)
		tool.BaseDir = cfg.WorkDir
		s.tools.Register(tool)
	}

	// Build and install the system prompt.
	prompt := s.buildSystemPrompt()
	cfg.LLMClient.SetSystemPrompt(prompt)
	s.setSessionID(cfg.LLMClient)
	ctxMgr.SetSystemPrompt(message.Message{
		Role:    "system",
		Content: prompt,
	})

	// Capture session-level context (environment + AGENTS.md) as
	// a meta user message and freeze the tool surface. Mirrors MainAgent.
	venvRel := ""
	if s.venvPath != "" && s.workDir != "" {
		venvRel = displayPathFromWorkDir(s.workDir, s.venvPath)
	}
	env := SessionEnvSnapshot{
		WorkDir:  s.workDir,
		Platform: runtime.GOOS + "/" + runtime.GOARCH,
		VenvRel:  venvRel,
		Date:     time.Now().Format("Mon Jan 2 2006"),
	}
	s.cachedSessionReminderContent = buildSessionContextReminder(env, s.agentsMD)
	s.frozenToolDefs = append(
		[]message.ToolDefinition(nil),
		llmToolDefinitionsFromVisibleTools(s.filteredVisibleToolsForModel(s.modelName, s.llmClient))...,
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
	toolDefs := llmToolDefinitionsFromVisibleTools(s.filteredVisibleToolsForModel(modelName, client))
	s.llmMu.Lock()
	oldClient := s.llmClient
	s.llmClient = client
	s.modelName = modelName
	s.frozenToolDefs = append([]message.ToolDefinition(nil), toolDefs...)
	s.llmMu.Unlock()
	prompt := s.buildSystemPrompt()
	client.SetSystemPrompt(prompt)
	s.setSessionID(client)
	if oldClient != nil && oldClient != client {
		oldClient.Close()
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

func (s *SubAgent) closeLLMClient() {
	if s == nil {
		return
	}
	s.llmMu.RLock()
	client := s.llmClient
	s.llmMu.RUnlock()
	if client != nil {
		client.Close()
	}
}

// sessionCacheKey returns the stable per-instance session identity used for
// provider-side prompt-cache routing. It extends the MainAgent's key (the
// session directory base name) with ":sub:" + the instance id, so SubAgent
// requests never collide with MainAgent requests or with a sibling SubAgent
// instance that shares the same provider impl. The model name is deliberately
// excluded: the key must stay stable across model switches to keep cache
// affinity, and the model already participates in the request signature.
func (s *SubAgent) sessionCacheKey() string {
	base := strings.TrimSpace(filepath.Base(s.sessionDir))
	if base == "" || base == "." || base == "/" || base == string(filepath.Separator) {
		return ""
	}
	return base + ":sub:" + s.instanceID
}

// setSessionID applies the SubAgent's cache identity to an LLM client. It must
// be called on every client the SubAgent installs (construction and model
// switches), because the identity lives on the Client, not on the shared
// provider impl.
func (s *SubAgent) setSessionID(client *llm.Client) {
	if client == nil {
		return
	}
	if key := s.sessionCacheKey(); key != "" {
		client.SetSessionID(key)
	}
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
	if tt, ok := t.(tools.BaseDirTool); ok {
		return tt.WithBaseDir(workDir)
	}
	return t
}

// ---------------------------------------------------------------------------
// LLM interaction
// ---------------------------------------------------------------------------

func (s *SubAgent) asyncCallLLMWithFlightMarked(turn *Turn, messages []message.Message) {
	// Issuing a request is real activity: refresh the coordination heartbeat so
	// a long single request is not mistaken for a stalled worker.
	s.markActivity()
	// Arm the in-flight gate up front (idempotent). Relying on every caller to
	// Store(true) after finishLLMRequest cleared the gate has repeatedly left
	// the gate down, letting runLoop treat a busy sub-agent as idle and consume
	// queued input or park it. Callers that must arm earlier — atomically with
	// draining the input queue under inputQueueMu — still do so themselves.
	s.llmRequestInFlight.Store(true)
	defer func() {
		if recoverValue := recover(); recoverValue != nil {
			s.llmRequestInFlight.Store(false)
			panic(recoverValue)
		}
	}()
	s.llmMu.RLock()
	toolDefs := append([]message.ToolDefinition(nil), s.frozenToolDefs...)
	s.llmMu.RUnlock()
	if toolDefs == nil {
		toolDefs = llmToolDefinitionsFromVisibleTools(s.filteredVisibleTools())
	}
	messages = s.injectSessionContextReminder(messages)
	llmClient, modelName := s.llmSnapshot()
	if llmClient == nil {
		select {
		case s.llmCh <- &llmResult{err: fmt.Errorf("SubAgent %s has no LLM client", s.instanceID), turnID: turn.ID}:
		case <-s.parentCtx.Done():
			s.llmRequestInFlight.Store(false)
		}
		return
	}
	if providerSupportsRequiredToolChoice(llmClient.ProviderConfig()) {
		llmClient.MergeNextRequestTuningOverride(requiredToolChoiceTuning(llm.RequestTuning{}))
	}
	if filtered, dropped := filterUnsupportedBinaryPartsForModel(messages, llmClient); dropped.any() {
		log.Warnf("SubAgent dropping unsupported binary parts before LLM request agent=%v kinds=%s", s.instanceID, dropped.summary())
		if s.unsupportedPartToast.first(modelName, toastCategoryInput, dropped.summary()) {
			s.parent.emitToTUI(ToastEvent{Level: "warn", Message: "Input dropped (unsupported): " + dropped.summary(), AgentID: s.instanceID})
		}
		messages = filtered
	}
	compatCfg := llmClient.ThinkingToolcallCompat()
	scrubThinkingMarkers := compatCfg != nil && compatCfg.EnabledValue()

	s.llmRequestSeq++
	requestSeq := s.llmRequestSeq
	s.llmWG.Go(func() {
		// Report the segment end last: this goroutine emits this request's last
		// text delta in streamReducer.Finish below, and the TUI settles the
		// streaming card on this event (see StreamSegmentEndedEvent).
		defer s.parent.emitToTUI(StreamSegmentEndedEvent{AgentID: s.instanceID, TurnID: turn.ID, RequestSeq: requestSeq})
		resultQueued := false
		defer func() {
			// A queued result still belongs to the active request until runLoop
			// consumes it. Clearing the gate here would let queued user input
			// create a newer turn and make the valid result look stale.
			if !resultQueued {
				s.llmRequestInFlight.Store(false)
				s.parent.sendEvent(Event{Type: EventSubAgentRequestBoundary, SourceID: s.instanceID})
				// The request was aborted without queuing a result (cancelled
				// turn or no client). Wake the run loop so a pending silence
				// recovery is issued as soon as the gate clears instead of
				// waiting out the watchdog grace window.
				s.signalWake()
			}
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
					resultQueued = true
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

		streamState := &subLLMStreamState{}
		streamReducer := s.newSubLLMStreamReducer(turn, promoteStreamingActivity, scrubThinkingMarkers, streamState, requestSeq)

		callback := streamReducer.Handle

		s.parent.emitActivity(s.instanceID, ActivityConnecting, "")
		releaseLLM, err := s.parent.governor.acquireLLM(turn.Ctx, llmClient.PrimaryModelRef())
		if err != nil {
			select {
			case s.llmCh <- &llmResult{err: fmt.Errorf("acquire LLM request capacity: %w", err), turnID: turn.ID}:
				resultQueued = true
			case <-s.parentCtx.Done():
			}
			return
		}
		defer releaseLLM()
		wallReq := s.parent.walltime.startRequestAt(s.instanceID, s.agentDefName, turn.ID)
		if wallReq != nil {
			defer wallReq.finish()
			wallReq.wireStreamReducer(streamReducer, func(activity ActivityType, detail string) {
				s.parent.emitActivity(s.instanceID, activity, detail)
			})
		}
		requestCtx := llm.WithResponsesTurnState(turn.Ctx, turn.LLMResponsesState)
		resp, err := llmClient.CompleteStream(requestCtx, messages, toolDefs, callback)
		// Settle the walltime segment before the flush events so the TUI
		// re-render they trigger sees the updated TIME buckets; finishing at
		// return would leave the sidebar stale until the next unrelated
		// event. finish() is idempotent, so the deferred call is a no-op.
		if wallReq != nil {
			wallReq.finish()
		}
		streamReducer.Finish() // final flush: emit any remaining accumulated text
		s.parent.emitToTUI(RequestProgressEvent{AgentID: s.instanceID, Bytes: streamState.requestProgressBytes, Events: streamState.requestProgressEvents, Done: true})
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
			s.parent.recordUsage(s.instanceID, "sub", s.agentDefName, "chat", selectedRef, runningRef, turn.ID, resp.Usage, callStatus.ServiceTier, nil)

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
						resultQueued = true
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
			resultQueued = true
		case <-s.parentCtx.Done():
		}
	})
}

type subLLMStreamState struct {
	requestProgressBytes  int64
	requestProgressEvents int64
}

func (s *SubAgent) newSubLLMStreamReducer(turn *Turn, promoteStreamingActivity func(string), scrubThinkingMarkers bool, state *subLLMStreamState, requestSeq uint64) *llmStreamReducer {
	if state == nil {
		state = &subLLMStreamState{}
	}
	streamReducer := &llmStreamReducer{}
	updateRunningModelRef := func(status *message.StatusDelta) {
		if status == nil {
			return
		}
		runningRef := strings.TrimSpace(status.ModelRef)
		if runningRef == "" {
			return
		}
		client, _ := s.llmSnapshot()
		if client == nil {
			return
		}
		prev := strings.TrimSpace(client.RunningModelRef())
		client.NoteRunningModelRef(runningRef)
		if runningRef == prev {
			return
		}
		s.parent.emitToTUI(RunningModelChangedEvent{AgentID: s.instanceID, ProviderModelRef: client.PrimaryModelRef(), RunningModelRef: runningRef})
	}
	streamReducer.content = streamContentReducer{
		agentID:                      s.instanceID,
		turnID:                       streamTurnID(turn),
		requestSeq:                   requestSeq,
		emit:                         s.parent.emitToTUI,
		appendPartialText:            turn.appendPartialText,
		appendPartialResponsesOutput: turn.appendPartialResponsesOutput,
		scrubThinkingDelta:           false,
		scrubThinkingFinal:           scrubThinkingMarkers,
		thinkingCommitMode:           streamContentCommitFullText,
		textFlushInterval:            defaultStreamTextFlushInterval,
		thinkingFlushInterval:        defaultStreamThinkingFlushInterval,
	}
	streamReducer.tool = streamToolDeltaReducer{
		agentID:          s.instanceID,
		syncHookGate:     s.syncToolHooksConfigured,
		turn:             turn,
		registry:         s.tools,
		ruleset:          func() permission.Ruleset { return s.currentRuleset() },
		toolBaseDir:      s.workDir,
		visibleToolNames: s.visibleToolNames,
		emit:             s.parent.emitToTUI,
		flushBeforeTool: func() {
			streamReducer.content.flushThinkingDelta()
			streamReducer.content.flushTextDelta()
		},
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
	var lastProgressEmitAt time.Time
	var lastProgressEmitBytes int64
	var lastProgressEmitEvents int64
	streamReducer.onProgress = func(progress *message.StreamProgressDelta) {
		// Transport-level progress (bytes/events flowing) is the worker's
		// heartbeat: a request that keeps streaming — even a long single
		// generation — must not look stalled.
		s.markActivity()
		state.requestProgressBytes = progress.Bytes
		state.requestProgressEvents = progress.Events
		now := time.Now()
		if shouldEmitRequestProgress(now, lastProgressEmitAt, state.requestProgressBytes, state.requestProgressEvents, lastProgressEmitBytes, lastProgressEmitEvents) {
			lastProgressEmitAt = now
			lastProgressEmitBytes = state.requestProgressBytes
			lastProgressEmitEvents = state.requestProgressEvents
			s.parent.emitToTUI(RequestProgressEvent{AgentID: s.instanceID, Bytes: state.requestProgressBytes, Events: state.requestProgressEvents})
		}
	}
	streamReducer.beforeStatus = updateRunningModelRef
	streamReducer.onKeyConfirmed = updateRunningModelRef
	streamReducer.onRetryError = func(err error, provider, model, maskedKey, accountID, email string) {
		s.parent.emitToTUI(ErrorEvent{
			Err:       err,
			AgentID:   s.instanceID,
			Silent:    true,
			Provider:  provider,
			Model:     model,
			Key:       maskedKey,
			AccountID: accountID,
			Email:     email,
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

// currentTurn reads the active turn under turnMu. Tool goroutines run
// concurrently with newTurn, so they must not read s.turn directly — the
// MainAgent side reads its own through the same kind of guarded accessor.
func (s *SubAgent) currentTurn() *Turn {
	if s == nil {
		return nil
	}
	s.turnMu.Lock()
	defer s.turnMu.Unlock()
	return s.turn
}

// newTurn cancels any in-flight work and creates a fresh Turn.
func (s *SubAgent) newTurn() *Turn {
	if s.parent != nil {
		s.parent.markRealWorkStarted()
	}
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
	// A fresh turn must not inherit the previous turn's edit-match failure
	// history: a new mailbox delivery targets new work, so stale streaks
	// would mis-advise the model.
	s.editMatchFailStreak = nil
	s.applyPatchRetry.reset()
	s.nextTurnID++
	ctx, cancel := context.WithCancel(s.parentCtx)
	s.turn = &Turn{
		ID:                    s.nextTurnID,
		Ctx:                   ctx,
		Cancel:                cancel,
		LLMResponsesState:     llm.NewResponsesTurnState(),
		PendingToolMeta:       make(map[string]PendingToolCall),
		toolExecutionBatches:  nil,
		nextToolBatch:         0,
		activeToolBatchCancel: nil,
	}
	s.turn.streamingToolExec = NewStreamingToolExecutor(s.turn.ID, ctx, s.parent.emitToTUI, s.executeToolCallSpeculative)
	s.turn.streamingToolExec.SetProjectRoot(s.effectiveToolBaseDir())
	s.turn.streamingToolExec.SetTraceCallbacks(s.parent.recordToolTraceSpeculativeStart, s.parent.recordToolTraceFirstVisibleResult, s.parent.recordToolTraceSpeculativeDiscard)
	log.Debugf("SubAgent: new turn created agent=%v turn_id=%v", s.instanceID, s.turn.ID)
	return s.turn
}

// currentTurnID is a small helper for log messages.
func (s *SubAgent) currentTurnID() uint64 {
	turn := s.currentTurn()
	if turn == nil {
		return 0
	}
	return turn.ID
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
	client, modelName := s.llmSnapshot()
	return s.filteredVisibleToolsForModel(modelName, client)
}

func (s *SubAgent) filteredVisibleToolsForModel(modelName string, client *llm.Client) []tools.Tool {
	// A zero context is correct for SubAgents: loop mode is a main-agent
	// workflow, and neither done nor compact_context is ever registered here.
	visibleTools := visibleLLMTools(s.tools, s.currentRuleset(), isSubAgentInternalTool, toolPermissionContext{})
	var patchSurfaceDecision *bool
	if client != nil {
		// The client resolves compat + primary model inference (stable tool
		// surface); name-based inference stays the fallback when unbound.
		patchSurfaceDecision = new(client.UsesApplyPatchSurface())
	}
	return filterEditToolsByModel(visibleTools, modelName, s.currentRuleset(), patchSurfaceDecision)
}

// subAgentCoordinationPromptText is the single source for which control tool a
// SubAgent uses to report progress, escalate, and close its task; the closure
// block points here instead of restating the routing.
func subAgentCoordinationPromptText(visible map[string]struct{}) string {
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
	// complete needs no visibility branch: isSubAgentInternalTool keeps it out
	// of ruleset filtering and every SubAgent registers it, so a worker can
	// always close its own lifecycle.
	lines = append(lines, "- Call "+toolPromptName(tools.NameComplete)+" when the task is done; plain text alone does not mark the task complete")
	return strings.Join(lines, "\n")
}

// taskCompletionInstruction closes the "## Your Task" section. The escalation
// path is resolved from the same visibility snapshot the coordination block
// used, so the task instruction never tells a worker to call a control tool
// this role does not expose. complete is exempt: it is always registered and
// never ruleset-filtered (isSubAgentInternalTool).
func taskCompletionInstruction(visible map[string]struct{}) string {
	// When to call complete is not restated here: the SubAgent Coordination
	// section and the Complete tool description are its single source.
	base := "Focus only on this task."
	switch {
	case hasVisibleTool(visible, tools.NameEscalate):
		return base + " Call " + toolPromptName(tools.NameEscalate) + " if you are blocked."
	case hasVisibleTool(visible, tools.NameNotify):
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

	// One visibility snapshot feeds every block that names a control tool
	// (coordination, capabilities, the task instruction). Separate snapshots
	// could disagree and reintroduce "`notify` is unavailable in this role"
	// next to "use `notify`"; the closure block names no control tool at all
	// and points back at the coordination section instead.
	visible := s.visibleToolNames()
	parts = append(parts, subAgentIdentityPrompt, sharedAgentValuesPrompt, subAgentCodingGuidelinesPrompt, sharedContentTrustPrompt, sharedReasoningDisciplinePrompt, subAgentCoordinationPromptText(visible), subAgentResponseClosurePrompt)
	if s.customPrompt != "" {
		parts = append(parts, s.customPrompt)
	}
	if block := s.delegationPromptBlock(visible); block != "" {
		parts = append(parts, block)
	}
	if block := s.capabilityPromptBlock(visible); block != "" {
		parts = append(parts, block)
	}

	// Dynamic environment info (working directory, platform, date, venv) is
	// injected via the SubAgent's session-context reminder before the first
	// user message (mirrors MainAgent) to keep this system prompt static and
	// prefix-cacheable.

	// Task description (core difference from MainAgent).
	parts = append(parts, fmt.Sprintf("## Your Task\n\n%s\n\n%s", s.taskDesc, taskCompletionInstruction(visible)))

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

// capabilityPromptBlock takes the caller's visibility snapshot so every block
// in one system prompt describes the same tool surface.
func (s *SubAgent) capabilityPromptBlock(visible map[string]struct{}) string {
	return buildDynamicCapabilityPromptBlock(visible, s.currentRuleset(), capabilityPromptAudienceSub)
}

func (s *SubAgent) delegationPromptBlock(visible map[string]struct{}) string {
	if s == nil || s.parent == nil {
		return ""
	}
	if !hasVisibleTool(visible, tools.NameDelegate) {
		return ""
	}
	agents := s.parent.availableSubAgentsForRuleset(s.currentRuleset(), "")
	if len(agents) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("## Nested Delegation\n")
	sb.WriteString("- You may delegate child work only when the sub-problem is clearly independent and within your configured delegation depth.\n")
	sb.WriteString(delegationStrategyPromptLines(visible))
	sb.WriteString("- Child workers are owned by you directly; do not assume higher-level ancestors can message or stop them for you.\n")
	sb.WriteString("- When `child_join` is enabled, do not consider your task complete until all joined child tasks have finished or been explicitly stopped.\n")
	sb.WriteString("- If you need to finish early, explicitly stop the child task first; do not assume a later ancestor will clean it up for you.\n")
	sb.WriteString("- Use child control tools only for your own direct children.\n")
	// The child role catalogue is not re-listed here: the delegate tool's
	// agent_type parameter (name, description, empty-scope rule, filtered to
	// this role's allowed targets) is the single source for which child
	// agent types exist.
	return strings.TrimSpace(sb.String())
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// truncateString returns the first n runes of s, appending "..." if
// truncated. It never splits a multi-byte UTF-8 rune.
func truncateString(s string, n int) string {
	return llm.TruncateStringRunes(s, n, "...")
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
		if !s.setState(SubAgentStateWaitingMain, reason) {
			log.Warnf("sub-agent escalation state transition rejected agent=%v", s.instanceID)
		}
	case EventAgentNotify:
		msg, _ := payload.(string)
		if strings.TrimSpace(msg) != "" {
			if !s.updateProgress(msg) {
				log.Warnf("sub-agent notification progress rejected agent=%v", s.instanceID)
			}
		}
	}
	s.parent.sendEvent(Event{
		Type:     eventType,
		SourceID: sourceID,
		Payload:  payload,
	})
}
