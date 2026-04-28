package agent

import (
	"context"
	"fmt"
	"github.com/keakon/chord/internal/analytics"
	"github.com/keakon/chord/internal/command"
	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/filelock"
	"github.com/keakon/chord/internal/hook"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/mcp"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/ratelimit"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/skill"
	"github.com/keakon/chord/internal/tools"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// keyed by server name. Entries whose Mgr is nil are sentinels: the server
// is already connected as part of the main agent (tools live in a.tools /
// BaseTools) and SubAgents must not reconnect it.
type mcpServerEntry struct {
	Mgr   *mcp.Manager // nil for main-agent servers (sentinel)
	Tools []tools.Tool // nil for sentinel entries
}

// ---------------------------------------------------------------------------
// Turn
// ---------------------------------------------------------------------------

// Turn represents a single user-initiated interaction cycle. Each user message
// starts a new turn; starting a new turn cancels any in-flight work from the
// previous one.
type Turn struct {
	ID     uint64
	Epoch  uint64
	Ctx    context.Context
	Cancel context.CancelFunc
	// PendingToolCalls and TotalToolCalls are accessed from both the event-loop
	// goroutine (writes) and external goroutines like CancelCurrentTurn (reads),
	// so they must be accessed atomically.
	PendingToolCalls atomic.Int32 // number of tool results not yet received
	TotalToolCalls   atomic.Int32 // total tool calls in this turn (set when dispatching)
	// PendingToolMeta tracks tool calls in the current turn that have started but
	// have not yet reached a terminal UI state. It is protected by pendingToolMu
	// because external cancellation paths need to snapshot it safely.
	// Only IDs from the finalized LLM response (handleLLMResponse) are stored here
	// so persistence/cancel never sees stream-only call_ids.
	pendingToolMu   sync.Mutex
	PendingToolMeta map[string]PendingToolCall
	// streamingToolCalls holds speculative tool metadata from SSE tool_use_start
	// before the response is finalized. Used only for TUI cancel/fail bookkeeping;
	// it must never be persisted until merged into PendingToolMeta.
	streamingToolMu    sync.Mutex
	streamingToolCalls map[string]PendingToolCall
	// partialText accumulates assistant text streamed during the current LLM
	// round so it can be saved to history if the stream is interrupted before
	// a normal DeltaStop. Protected by partialTextMu because the stream
	// callback runs on a separate goroutine from the event loop.
	partialTextMu sync.Mutex
	partialText   strings.Builder
	// MalformedCount tracks consecutive LLM rounds where tool calls had
	// abnormal arguments — either the malformed sentinel (invalid JSON) or
	// empty "{}" for tools with required parameters (output truncation).
	// When this reaches maxMalformedToolCalls the turn is aborted.
	MalformedCount                     int
	LengthRecoveryCount                int
	InLengthRecovery                   bool
	LastTruncatedToolName              string
	LengthRecoveryAutoCompactAttempted bool
	malformedInBatch                   int // abnormal calls in the current LLM-response batch
	CompletedToolCalls                 []any
	ChangedFiles                       []any
	toolExecutionBatches               []toolExecutionBatch
	nextToolBatch                      int
	activeToolBatchCancel              context.CancelFunc
}

// PendingToolCall records the minimal metadata needed to close a pending tool
// card when a turn is cancelled before a normal ToolResultEvent arrives.
type PendingToolCall struct {
	CallID   string
	Name     string
	ArgsJSON string
	AgentID  string
	Audit    *message.ToolArgsAudit
}

// toolCallStageTrace tracks per-call timing markers from streaming args-end to
// finalized execution dispatch. Used for queue-latency diagnostics.
type toolCallStageTrace struct {
	CallID string
	Name   string
	Agent  string

	ToolUseEndAt           time.Time
	CallLLMReturnedAt      time.Time
	OnAfterLLMCallDoneAt   time.Time
	LLMResponseEventSentAt time.Time
	LLMResponseHandledAt   time.Time
	ExecutionRunningAt     time.Time

	PersistBlockedTotal time.Duration
	PersistBlockedCount int
}

type ToolExecutionResult struct {
	Result            string
	EffectiveArgsJSON string
	Audit             *message.ToolArgsAudit
	LSPReviews        []message.LSPReview
	PreFilePath       string
	PreContent        string
	PreExisted        bool
}

// ---------------------------------------------------------------------------
// Confirm/Question response types for the Event+Resolve interaction path.
// ---------------------------------------------------------------------------

// ErrAgentShutdown is returned when the agent is shutting down and can no
// longer process interactive requests.
var ErrAgentShutdown = fmt.Errorf("agent is shutting down")

// ConfirmResponse carries the user's response to a ConfirmRequestEvent.
type ConfirmResponse struct {
	Approved      bool
	FinalArgsJSON string
	EditSummary   string
	DenyReason    string
	RuleIntent    *ConfirmRuleIntent // nil = no new rule
}

// ConfirmRuleIntent captures the user's intent to add a permission rule.
type ConfirmRuleIntent struct {
	Pattern string
	Scope   int // 0=session, 1=project, 2=userGlobal (matches permission.RuleScope)
}

// QuestionResponse carries the user's response to a QuestionRequestEvent.
type QuestionResponse struct {
	Answers   []string
	Cancelled bool
}

// ---------------------------------------------------------------------------
// ConfirmFunc
// ---------------------------------------------------------------------------

// ConfirmFunc is the callback the agent invokes when a tool call requires user
// confirmation (permission action "ask"). The TUI (or test harness) supplies
// the implementation.
//
//   - ctx:          context for cancellation (e.g. turn cancelled while waiting)
//   - toolName:     the name of the tool being invoked (e.g. "Bash")
//   - args:         the raw JSON arguments string
//   - needsApproval: explicit paths covered by this approval prompt (Delete only)
//   - alreadyAllowed: explicit paths already allowed by rules in the same batch (Delete only)
//   - ConfirmResponse: approved decision plus the final args JSON chosen by the user
//   - err:          non-nil if the confirmation flow itself fails
type ConfirmFunc func(ctx context.Context, toolName string, args string, needsApproval []string, alreadyAllowed []string) (ConfirmResponse, error)

// ---------------------------------------------------------------------------
// SubAgentInfo
// ---------------------------------------------------------------------------

// SubAgentInfo carries read-only information about a running SubAgent for TUI
// display (sidebar listing). The fields are snapshot values safe to read from
// any goroutine.
type SubAgentInfo struct {
	InstanceID          string
	TaskID              string
	AgentDefName        string
	TaskDesc            string
	ModelName           string
	SelectedRef         string
	RunningRef          string
	State               string
	Color               string // optional ANSI color code from agent config
	LastSummary         string
	UrgentInboxCount    int
	LastArtifactRelPath string
	LastArtifactType    string
}

// ModelOption describes a model available for runtime switching.
type ModelOption struct {
	ProviderModel string // e.g. "anthropic-main/claude-opus-4.7" or "anthropic-main/claude-opus-4.7@high"
	ProviderName  string // e.g. "anthropic-main"
	ModelID       string // e.g. "claude-opus-4.7"
	ContextLimit  int
	OutputLimit   int
}

// pendingUserMessage holds a single queued user message when the agent is busy.
// When Parts is non-nil it is a multi-part message (e.g. text + images); otherwise Content is used.
type pendingUserMessage struct {
	DraftID      string
	Content      string
	Parts        []message.ContentPart
	FromUser     bool
	MailboxAckID string
	CoalesceKey  string
}

// ---------------------------------------------------------------------------
// MainAgent
// ---------------------------------------------------------------------------

// MainAgent orchestrates the LLM ↔ Tool loop. It owns an internal event bus
// (eventCh) for sequencing work and an output channel (outputCh) that the TUI
// consumes.
type MainAgent struct {
	parentCtx              context.Context
	cancel                 context.CancelFunc
	llmClient              *llm.Client
	ctxMgr                 *ctxmgr.Manager
	tools                  *tools.Registry
	hookEngine             hook.Manager
	usageTracker           *analytics.UsageTracker
	usageLedger            *analytics.UsageLedger
	usageEventSink         func(event analytics.UsageEvent)
	skillsMu               sync.RWMutex
	loadedSkills           []*skill.Meta
	invokedSkills          map[string]*skill.Meta
	repetition             *tools.RepetitionDetector
	promptMetaMu           sync.RWMutex
	sessionInitMu          sync.Mutex
	stateMu                sync.RWMutex
	sessionSummary         *SessionSummary
	startupResumePending   bool
	startupResumeSessionID string
	startupResumeLoadedAt  time.Time

	// Permission system: ruleset from active agent config with overlay support.
	globalConfig  *config.Config
	projectConfig *config.Config
	ruleset       permission.Ruleset  // merged ruleset (base + overlays)
	overlay       *permission.Overlay // layered permission rules

	// confirmFn is called when permission evaluates to "ask". It must be set
	// before Run whenever the active ruleset can yield ActionAsk.
	confirmFn ConfirmFunc

	// Internal event bus. Goroutines that perform async work (LLM calls,
	// tool execution) send results here; the single-threaded Run loop
	// processes them in order.
	eventCh chan Event

	// pendingUserMessages holds user-facing context additions received while the
	// agent is busy (turn != nil). User-authored input must not be silently
	// dropped; some system-generated entries may be tail-coalesced when that
	// preserves arrival order. Only the event-loop goroutine reads/writes.
	pendingUserMessages []pendingUserMessage
	// pausePendingUserDrainOnce suppresses the next idle-time drain of
	// pendingUserMessages. Used for explicit user interruption so queued work
	// does not auto-run immediately after cancel.
	pausePendingUserDrainOnce bool

	// Output channel consumed by the TUI or any external observer.
	outputCh     chan AgentEvent
	outputMu     sync.RWMutex
	outputClosed atomic.Bool

	turn       *Turn
	nextTurnID uint64
	turnEpoch  uint64
	eventSeq   atomic.Uint64
	// autoCompactRequested is set after an LLM round crosses the configured
	// context threshold. The next main-agent request (or the idle fallback
	// path) will honor it via the durable-compaction gate.
	autoCompactRequested atomic.Bool
	// autoCompactFailureState tracks repeated failures of usage-driven automatic
	// compaction so the proactive path can be temporarily suppressed after
	// repeated failures.
	autoCompactFailureState autoCompactionFailureState

	// Runtime evidence candidates accumulated incrementally for durable compaction.
	evidenceCandidates   []evidenceItem
	evidenceCandidateSet map[string]struct{}

	// Async durable compaction (pre-request gate): defer inbound events until commit.
	compactionState      compactionState
	sessionEpoch         uint64
	nextCompactionPlanID uint64
	compactionFileCtxMu  sync.Mutex
	compactionFileCtxSig string

	sessionDir       string
	modelName        string
	providerModelRef string // "provider/model" for unique identification
	runningModelRef  string // actual model used in latest LLM call
	instanceID       string

	// turnMu protects the turn pointer for cross-goroutine access.
	// The event-loop goroutine writes turn in newTurn(); external goroutines
	// (TUI, shutdown) read it via CancelCurrentTurn() / Shutdown().
	turnMu sync.Mutex

	// llmMu protects llmClient, modelName, and providerModelRef for
	// cross-goroutine access. The TUI goroutine reads ModelName() and
	// ProviderModelRef() from View(), while SwapLLMClient / SwitchModel
	// write these fields. callLLM snapshots under RLock at the start
	// to ensure consistent model name for hooks and usage tracking.
	llmMu                sync.RWMutex
	installedSysPrompt   string
	systemPromptOverride string

	// repMu serialises access to the RepetitionDetector, which is called
	// from concurrent tool-execution goroutines.
	repMu sync.Mutex

	// done is closed when Run exits, allowing Shutdown to wait.
	done chan struct{}

	// stoppingCh is closed just before Run exits to signal emitInteractiveToTUI
	// to stop sending. Separate from done (which signals "Run fully exited").
	stoppingCh chan struct{}

	// toolWg tracks goroutines that may call emitInteractiveToTUI (ConfirmFunc /
	// QuestionFunc). Run waits on toolWg after closing stoppingCh before closing
	// outputCh, ensuring no send-on-closed-channel.
	toolWg sync.WaitGroup

	// outputWg tracks background goroutines that may continue emitting regular
	// TUI events after the main loop has started shutting down (for example,
	// in-flight LLM response goroutines finishing cancellation/flush work).
	// Run waits on outputWg before closing outputCh.
	outputWg sync.WaitGroup

	// Confirm/Question interaction: requestID → response channel.
	confirmFlowMu  sync.Mutex
	confirmMapMu   sync.Mutex
	confirmCh      map[string]chan ConfirmResponse
	questionFlowMu sync.Mutex
	questionMapMu  sync.Mutex
	questionCh     map[string]chan QuestionResponse

	// Plan execution workflow state.
	projectRoot    string
	lastPlanPath   string
	pendingHandoff *HandoffResult // deferred Handoff action; processed after all sibling tools finish

	// Role system: MainAgent operates as one of several roles (builder, planner, etc.).
	activeConfig *config.AgentConfig            // currently active role (nil = no role set yet; defaults to builder)
	agentConfigs map[string]*config.AgentConfig // pre-loaded: built-in → global → project (highest priority)

	// Phase 2a: Multi-agent orchestration fields.
	mu                       sync.RWMutex                  // protects subAgents/taskRecords for cross-goroutine access
	subAgents                map[string]*SubAgent          // instanceID → live SubAgent
	taskRecords              map[string]*DurableTaskRecord // taskID → durable task record
	sem                      chan struct{}                 // bounded concurrency semaphore (cap = 10)
	fileTrack                *filelock.FileTracker         // file write conflict detection
	recovery                 *recovery.RecoveryManager     // session persistence and crash recovery
	sessionLock              *recovery.SessionLock         // cross-process exclusive ownership of sessionDir
	sessionArtifactsDirFn    func() string                 // active session artifacts directory for exports / dumps
	sessionTargetChangedFn   func(string)                  // notified after active sessionDir changes
	focusedAgent             atomic.Pointer[SubAgent]      // currently focused SubAgent (nil = main)
	nudgeCounts              map[string]int                // agentID → idle nudge count
	subAgentInbox            subAgentInbox
	ownedSubAgentMailboxes   map[string][]SubAgentMailboxMessage // owner agentID -> descendant mailbox waiting for owner-local delivery
	pendingSubAgentMailboxes []*SubAgentMailboxMessage
	activeSubAgentMailboxes  []*SubAgentMailboxMessage
	activeSubAgentMailbox    *SubAgentMailboxMessage
	activeSubAgentMailboxAck bool
	subAgentMailboxSeq       atomic.Uint64
	subAgentInboxSummaryMu   sync.RWMutex
	subAgentUrgentCounts     map[string]int
	subAgentStateEnteredTurn map[string]uint64
	explicitUserTurnCount    uint64

	// mcpServerCache maps server name → mcpServerEntry, ensuring each MCP
	// server is a singleton for the lifetime of the MainAgent. Main-agent
	// servers are registered as sentinels (Mgr==nil) so SubAgents never
	// reconnect them. SubAgent-exclusive servers get their own Manager.
	mcpServerCacheMu sync.Mutex
	mcpServerCache   map[string]*mcpServerEntry

	// Custom slash commands loaded from MD files / YAML config.
	customCommandsMu sync.RWMutex
	customCommands   []*command.Definition

	// Todo state (implements tools.TodoStore).
	todoItems []tools.TodoItem
	todoMu    sync.RWMutex

	// Minimal loop-controller runtime state. Phase 1 keeps this local to the
	// MainAgent and only drives post-assistant stop assessment.
	loopState loopRuntimeState
	// pendingLoopContinuation is a request-scoped continuation notice shown to
	// the user and injected into the next continued LLM request only.
	pendingLoopContinuation *LoopContinuationNote

	// pendingRecoveryPrompt is a request-scoped recovery prompt injected after
	// length-recovery auto compaction succeeds. It is consumed as a one-shot
	// turn overlay and never appended to ctxMgr durable messages.
	pendingRecoveryPrompt string
	toolTraceMu           sync.Mutex
	toolTrace             map[string]toolCallStageTrace

	// Adhoc task counter for auto-assigning "adhoc-N" IDs.
	adhocSeq atomic.Uint64

	// Optional MCP summary injected into the system prompt (set after MCP init).
	mcpServersPromptMu    sync.RWMutex
	mcpServersPrompt      string
	pendingMCPTools       []tools.Tool
	agentsMDReady         chan struct{}
	agentsMDReadyOnce     sync.Once
	skillsReady           chan struct{}
	skillsReadyOnce       sync.Once
	mcpReady              chan struct{}
	mcpReadyOnce          sync.Once
	sessionBuilt          atomic.Bool
	bugTriagePromptActive atomic.Bool

	// shuttingDown is set to true when Shutdown begins. UpdateTodos checks
	// this flag to avoid overwriting the final snapshot.
	shuttingDown atomic.Bool

	// started is set to true when Run is called. Shutdown uses this to skip
	// waiting for the event loop and persist goroutine if Run was never called.
	started atomic.Bool

	persistCloseOnce sync.Once
	persistLoopOnce  sync.Once
	compactionWg     sync.WaitGroup

	// LLM client factory for creating SubAgent LLM clients. Set via
	// SetLLMFactory after construction. If nil, CreateSubAgent returns
	// an error. The agentModels parameter is the ordered list of model
	// references from AgentConfig.Models (e.g. "provider/model" or
	// "provider/model@variant"). If agentModels is empty, the factory uses the
	// global default model.
	llmFactory func(systemPrompt string, agentModels []string, variant string) *llm.Client

	// modelSwitchFactory creates a new LLM client from a selected model
	// reference string ("provider/model" or "provider/model@variant"). Used by
	// SwitchModel to hot-swap the MainAgent's LLM at runtime. Set via
	// SetModelSwitchFactory after construction.
	modelSwitchFactory func(providerModel string) (*llm.Client, string, int, error)
	// mainModelPolicyDirty marks the current main-agent client as needing a
	// rebuild from modelSwitchFactory before the next LLM call. This is mainly a
	// startup/deferred-policy flag; role switches try to refresh the active
	// model policy immediately so the selected model-pool head, remaining pool order, and key stats
	// stay aligned with the new role.
	mainModelPolicyDirty atomic.Bool
	mainModelPolicyMu    sync.Mutex
	mainModelPolicyBuild chan struct{}
	mainModelPolicyErr   error

	// availableModelsFn returns the list of models the user can switch to.
	// Set via SetAvailableModelsFn after construction.
	availableModelsFn func() []ModelOption

	// LSP/MCP state providers for TUI sidebar display (set via SetLSPStatusFunc / SetMCPStatusFunc).
	lspServerListFn   func() []LSPServerDisplay
	mcpServerListFn   func() []MCPServerDisplay
	lspSessionResetFn func()
	lspSessionLoadFn  func([]message.Message)

	// Async persistence channel for ordered JSONL writes.
	persistCh   chan persistEntry
	persistDone chan struct{} // closed when persist loop exits

	// Cached startup values reused in buildSystemPrompt to avoid repeated syscalls/subprocesses.
	cachedWorkDir     string
	cachedGitStatus   string // populated lazily via gitStatusReady
	cachedVenvPath    string // absolute path to detected Python virtual environment, or ""
	cachedAgentsMD    string
	gitStatusReady    chan struct{} // closed when cachedGitStatus is set
	gitStatusInjected atomic.Bool   // true after git status has been prepended to the first user turn
	cachedSubMu       sync.RWMutex
	// cachedSubAgents is the sorted list of subagent-mode agents available for
	// the Delegate tool, excluding the currently active role. Rebuilt when role filters change.
	cachedSubAgents []*config.AgentConfig

	// cachedSessionReminderContent is the <system-reminder> meta user message content
	// carrying AGENTS.md + currentDate. Built once ensureSessionBuilt completes.
	// Injected before the first user message only once per session-head, then
	// suppressed until resetSessionBuildState. Not persisted to ctxMgr or jsonl.
	// See docs/architecture/prompt-and-context-engineering.md.
	cachedSessionReminderContent atomic.Pointer[string]
	// sessionReminderInjected is true after cachedSessionReminderContent has been
	// injected into an LLM call for the current session-head.
	sessionReminderInjected atomic.Bool

	// frozenToolDefs is the LLM tool surface snapshot captured at
	// ensureSessionBuilt time. Kept stable for the life of the agent instance
	// so the provider request prefix (system prompt + tools[]) does not drift
	// and prompt cache / Responses previous_response_id remain effective.
	// Cleared by resetSessionBuildState on session-head events. See
	// docs/architecture/prompt-and-context-engineering.md §6.
	frozenToolDefs atomic.Pointer[[]message.ToolDefinition]

	// rateLimitMu protects per-provider rate-limit snapshots for cross-goroutine access.
	rateLimitMu    sync.RWMutex
	rateLimitSnaps map[string]*ratelimit.KeyRateLimitSnapshot

	// Activity observer for side-band runtime reactions (e.g. power management).
	activityObserverMu sync.RWMutex
	activityObserver   ActivityObserver
}

// persistEntry is a queued persistence request for ordered JSONL writes.
type persistEntry struct {
	agentID  string
	msg      message.Message
	recovery *recovery.RecoveryManager // snapshot of a.recovery at enqueue time
	barrier  chan struct{}
}

// ---------------------------------------------------------------------------
// System prompt
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Constructor
// ---------------------------------------------------------------------------

// NewMainAgent creates a fully-initialised MainAgent. The caller must invoke
// Run in a separate goroutine to start the event loop.
//
// projectRoot is the root directory of the project (typically cwd) and is used
// to load AGENTS.md and determine git repository status for the system prompt.
//
// globalCfg is the user-level config (~/.config/chord/config.yaml). projectCfg is the
// project-level config (.chord/config.yaml); either may be nil.
func NewMainAgent(
	ctx context.Context,
	llmClient *llm.Client,
	ctxMgr *ctxmgr.Manager,
	toolRegistry *tools.Registry,
	hookEngine hook.Manager,
	sessionDir string,
	modelName string,
	projectRoot string,
	globalCfg *config.Config,
	projectCfg *config.Config,
) *MainAgent {
	parentCtx, cancel := context.WithCancel(ctx)

	workDir, _ := os.Getwd()
	if workDir == "" {
		workDir = projectRoot
	}
	gitStatusReady := make(chan struct{})

	a := &MainAgent{
		parentCtx:                parentCtx,
		cancel:                   cancel,
		llmClient:                llmClient,
		ctxMgr:                   ctxMgr,
		tools:                    toolRegistry,
		hookEngine:               hookEngine,
		usageTracker:             analytics.NewUsageTracker(),
		usageLedger:              analytics.NewUsageLedger(sessionDir, projectRoot),
		invokedSkills:            make(map[string]*skill.Meta),
		repetition:               tools.NewRepetitionDetector(),
		globalConfig:             globalCfg,
		projectConfig:            projectCfg,
		eventCh:                  make(chan Event, 256),
		outputCh:                 make(chan AgentEvent, 512),
		sessionDir:               sessionDir,
		modelName:                modelName,
		runningModelRef:          modelName,
		instanceID:               NextInstanceID("main"),
		done:                     make(chan struct{}),
		stoppingCh:               make(chan struct{}),
		confirmCh:                make(map[string]chan ConfirmResponse),
		questionCh:               make(map[string]chan QuestionResponse),
		evidenceCandidateSet:     make(map[string]struct{}),
		projectRoot:              projectRoot,
		subAgents:                make(map[string]*SubAgent),
		taskRecords:              make(map[string]*DurableTaskRecord),
		sem:                      make(chan struct{}, 10),
		fileTrack:                filelock.NewFileTracker(),
		nudgeCounts:              make(map[string]int),
		subAgentInbox:            newSubAgentInbox(),
		ownedSubAgentMailboxes:   make(map[string][]SubAgentMailboxMessage),
		subAgentUrgentCounts:     make(map[string]int),
		subAgentStateEnteredTurn: make(map[string]uint64),
		recovery:                 recovery.NewRecoveryManager(sessionDir),
		persistCh:                make(chan persistEntry, 256),
		persistDone:              make(chan struct{}),
		cachedWorkDir:            workDir,
		gitStatusReady:           gitStatusReady,
		agentsMDReady:            make(chan struct{}),
		skillsReady:              make(chan struct{}),
		mcpReady:                 make(chan struct{}),
	}
	a.refreshSessionSummary()

	// Fetch git status asynchronously; callLLM will wait for it before the
	// first LLM request so the system prompt always has accurate info.
	go func() {
		a.setCachedGitStatus(getGitStatus(workDir))
		close(gitStatusReady)
	}()

	// Detect Python virtual environment synchronously (just os.Stat, cheap).
	a.cachedVenvPath = detectVenvPath(workDir)

	// Build and install the system prompt (git status may still be in flight;
	// it will be refreshed once ready via waitGitStatus before the first call).
	a.refreshSystemPrompt()

	return a
}

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

// SetConfirmFunc sets the callback used when a tool invocation requires user
// confirmation (permission action "ask"). This must be called before Run when
// the active ruleset can yield ActionAsk.
func (a *MainAgent) SetConfirmFunc(fn ConfirmFunc) {
	a.confirmFn = fn
}

// SetSessionLock installs the ownership lock handle for the currently active session.
func (a *MainAgent) SetSessionLock(lock *recovery.SessionLock) {
	a.sessionLock = lock
	a.refreshSessionSummary()
}

// SetMCPServersPromptBlock sets the MCP section appended to the system prompt
// and refreshes the installed system prompt on the LLM client and context manager.
func (a *MainAgent) SetMCPServersPromptBlock(block string) {
	a.mcpServersPromptMu.RLock()
	current := a.mcpServersPrompt
	a.mcpServersPromptMu.RUnlock()
	if current == block {
		a.markMCPReady()
		return
	}
	a.mcpServersPromptMu.Lock()
	a.mcpServersPrompt = block
	a.mcpServersPromptMu.Unlock()
	a.markMCPReady()
	// Pre-first-turn: refresh the stable system prompt so the MCP
	// discoverability snapshot is in place when ensureSessionBuilt freezes it.
	// Post-first-turn: MCP connection changes are runtime state and must not
	// invalidate the frozen prefix; the current snapshot stays in place.
	// See docs/architecture/prompt-and-context-engineering.md §3.3, §6.3.
	if !a.sessionBuilt.Load() {
		a.refreshSystemPrompt()
	}
}

func (a *MainAgent) SetPendingMCPDiscovery(mcpTools []tools.Tool, block string) {
	a.mcpServersPromptMu.Lock()
	a.mcpServersPrompt = block
	if len(mcpTools) == 0 {
		a.pendingMCPTools = nil
	} else {
		a.pendingMCPTools = append([]tools.Tool(nil), mcpTools...)
	}
	a.mcpServersPromptMu.Unlock()
	a.markMCPReady()
}

// RegisterMainMCPServers registers the main-agent's MCP server names as
// sentinels in mcpServerCache so that SubAgents never reconnect them.
// Called after the main-agent MCP servers are connected.
func (a *MainAgent) RegisterMainMCPServers(serverNames []string) {
	if len(serverNames) == 0 {
		return
	}
	a.mcpServerCacheMu.Lock()
	defer a.mcpServerCacheMu.Unlock()
	if a.mcpServerCache == nil {
		a.mcpServerCache = make(map[string]*mcpServerEntry)
	}
	for _, name := range serverNames {
		if _, ok := a.mcpServerCache[name]; !ok {
			a.mcpServerCache[name] = &mcpServerEntry{} // sentinel: Mgr==nil
		}
	}
}

func (a *MainAgent) markAgentsMDReady() {
	a.agentsMDReadyOnce.Do(func() { close(a.agentsMDReady) })
}

func (a *MainAgent) MarkSkillsReady() {
	a.skillsReadyOnce.Do(func() {
		if a.skillsReady != nil {
			close(a.skillsReady)
		}
	})
}

func (a *MainAgent) markMCPReady() {
	a.mcpReadyOnce.Do(func() { close(a.mcpReady) })
}

func (a *MainAgent) currentActiveConfig() *config.AgentConfig {
	a.stateMu.RLock()
	defer a.stateMu.RUnlock()
	return a.activeConfig
}

func (a *MainAgent) effectiveRuleset() permission.Ruleset {
	a.stateMu.RLock()
	defer a.stateMu.RUnlock()
	if len(a.ruleset) == 0 {
		return nil
	}
	return append(permission.Ruleset(nil), a.ruleset...)
}

// switchRole switches the MainAgent's active role configuration.
// It updates the active config, rebuilds the permission ruleset, rebuilds
// the system prompt, and clears the conversation history.
// switchRole changes the MainAgent's active role.
// If clearHistory is true, the conversation history is wiped (used for
// Handoff-triggered switches where the new role starts fresh).
// If clearHistory is false, history is preserved (used for user-initiated
// Tab cycling where the context should carry over).
func (a *MainAgent) switchRole(roleName string, clearHistory bool) error {
	cfg, ok := a.agentConfigs[roleName]
	if !ok {
		return fmt.Errorf("unknown role %q", roleName)
	}

	a.stateMu.Lock()
	a.activeConfig = cfg
	a.stateMu.Unlock()
	a.clearSystemPromptOverride()

	// Rebuild permissions from active agent config.
	a.rebuildRuleset()

	// Rebuild system prompt with role-specific instructions.
	a.refreshSystemPrompt()

	if clearHistory {
		// Clear conversation history so the new role starts fresh.
		a.ctxMgr.RestoreMessages(nil)
		a.clearEvidenceCandidates()
		a.llmClient.ResetResponsesSession("role_switch")
	}

	appliedModel := false
	if nextRef := a.defaultRoleModelRef(cfg); nextRef != "" {
		if err := a.applyRoleModelRef(nextRef); err != nil {
			return fmt.Errorf("apply role %q model %q: %w", roleName, nextRef, err)
		}
		appliedModel = true
	}

	// Keep a lazy rebuild fallback when the role has no explicit model list.
	a.mainModelPolicyDirty.Store(!appliedModel)

	slog.Info("switched MainAgent role", "role", roleName, "clear_history", clearHistory, "model_ref", a.ProviderModelRef())
	// Persist the active role immediately so a later resume/startup restore does
	// not fall back to the default builder role.
	a.saveRecoverySnapshot()
	return nil
}

// rebuildRuleset reconstructs the permission ruleset from the active agent
// config. This is called whenever the active role changes.
func (a *MainAgent) rebuildRuleset() {
	a.initOverlay()
}

func (a *MainAgent) defaultRoleModelRef(cfg *config.AgentConfig) string {
	if cfg == nil || len(cfg.Models) == 0 {
		return ""
	}
	ref := strings.TrimSpace(cfg.Models[0])
	if ref == "" {
		return ""
	}
	if _, variant := config.ParseModelRef(ref); variant != "" {
		return ref
	}
	if variant := strings.TrimSpace(cfg.Variant); variant != "" {
		return ref + "@" + variant
	}
	return ref
}

func (a *MainAgent) applyRoleModelRef(providerModel string) error {
	providerModel = strings.TrimSpace(providerModel)
	if providerModel == "" {
		return nil
	}
	// Role switches should refresh the active/running model for the UI, but they
	// should not surface the generic manual-model-switch toast.
	if err := a.switchModel(providerModel, false); err != nil {
		return err
	}
	return nil
}

// CurrentRateLimitSnapshot returns the latest rate-limit snapshot for the
// active provider when that provider uses preset: codex. Otherwise it returns nil.
// Display precedence is:
//  1. current provider-scoped inline snapshot cache (cleared on key switch)
//  2. client-selected key inline snapshot
//  3. provider/account-scoped polled usage snapshot
//
// ProxyInUseForRef reports whether the given provider/model ref uses a proxy.
// ref is "providerName/modelID"; if empty, the main agent's ProviderModelRef is used.
// Used by the TUI status bar to show a proxy indicator for the current (or focused) agent.
func (a *MainAgent) ProxyInUseForRef(ref string) bool {
	if a.globalConfig == nil {
		return false
	}
	if ref == "" {
		a.llmMu.RLock()
		ref = a.providerModelRef
		a.llmMu.RUnlock()
	}
	if ref == "" {
		return false
	}
	ref = config.NormalizeModelRef(ref)
	parts := strings.SplitN(ref, "/", 2)
	if len(parts) == 0 || parts[0] == "" {
		return false
	}
	providerName := parts[0]
	prov, ok := a.globalConfig.Providers[providerName]
	if !ok {
		return false
	}
	effective := llm.ResolveEffectiveProxy(prov.Proxy, a.globalConfig.Proxy)
	return effective != "" && effective != "direct"
}

// GetTokenUsage returns cumulative token usage statistics.
func (a *MainAgent) GetTokenUsage() message.TokenUsage {
	if sub := a.focusedAgent.Load(); sub != nil {
		return sub.ctxMgr.GetStats()
	}
	return a.ctxMgr.GetStats()
}

const sessionEndHookGrace = 300 * time.Millisecond

// Shutdown cancels any in-flight work and waits for the event loop to exit
// (up to the given timeout). The caller should cancel the context passed to
// Run as well.
func (a *MainAgent) Shutdown(timeout time.Duration) error {
	slog.Info("agent shutting down", "instance", a.instanceID, "timeout", timeout)
	deadline := time.Now().Add(timeout)
	remaining := func() time.Duration {
		left := time.Until(deadline)
		if left < 0 {
			return 0
		}
		return left
	}

	if grace := remaining(); grace > 0 {
		hookBudget := min(sessionEndHookGrace, grace)
		hookCtx, cancel := context.WithTimeout(context.Background(), hookBudget)
		if _, err := a.fireHook(hookCtx, hook.OnSessionEnd, 0, map[string]any{}); err != nil {
			slog.Warn("on_session_end hook error", "error", err)
		}
		cancel()
	}

	// Mark as shutting down so UpdateTodos stops saving snapshots (the final
	// snapshot is saved below and must not be overwritten).
	a.shuttingDown.Store(true)

	// Cancel the active turn first so tool executions and LLM calls abort.
	a.turnMu.Lock()
	if a.turn != nil {
		a.turn.Cancel()
	}
	a.turnMu.Unlock()

	// Cancel all SubAgents.
	a.mu.RLock()
	for _, sub := range a.subAgents {
		tools.StopAllSpawnedForAgent(sub.instanceID, "terminated on client exit")
		sub.cancel()
	}
	a.mu.RUnlock()
	stoppedBackground := tools.StopAllSpawnedForShutdown()
	if stoppedBackground > 0 {
		slog.Info("terminated background objects for shutdown", "count", stoppedBackground, "instance", a.instanceID)
	}

	// Close SubAgent-exclusive MCP managers (sentinels with Mgr==nil are
	// main-agent servers, already closed by ac.MCPMgr.Close() in AppContext).
	a.mcpServerCacheMu.Lock()
	for name, entry := range a.mcpServerCache {
		if entry.Mgr != nil {
			slog.Info("closing subagent MCP server", "server", name)
			entry.Mgr.Close()
		}
	}
	a.mcpServerCache = nil
	a.mcpServerCacheMu.Unlock()

	// Close the persistence channel and wait for the loop to drain.
	// The persist loop may be started outside Run (tests), so don't gate the wait
	// on the main event loop start flag.
	if a.persistCh != nil {
		a.closePersistLoop()
		if wait := remaining(); wait > 0 {
			select {
			case <-a.persistDone:
			case <-time.After(wait):
				slog.Warn("persist loop did not drain within shutdown budget, continuing")
			}
		} else {
			slog.Warn("shutdown budget exhausted before persist loop drain")
		}
	}

	if wait := remaining(); wait > 0 {
		done := make(chan struct{})
		go func() {
			a.compactionWg.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(wait):
			slog.Warn("compaction workers did not drain within shutdown budget, continuing")
		}
	} else {
		slog.Warn("shutdown budget exhausted before compaction workers drain")
	}

	// Save final snapshot and close recovery manager (flush JSONL file handles).
	if a.recovery != nil {
		a.todoMu.RLock()
		todoStates := snapshotTodos(a.todoItems)
		a.todoMu.RUnlock()

		// Collect active agent snapshots.
		a.mu.RLock()
		agents := make([]recovery.AgentSnapshot, 0, len(a.subAgents))
		for _, sub := range a.subAgents {
			state := sub.State()
			summary := sub.LastSummary()
			pendingCompleteIntent, pendingCompleteSummary := sub.PendingCompleteIntent()
			agents = append(agents, recovery.AgentSnapshot{
				InstanceID:             sub.instanceID,
				TaskID:                 sub.taskID,
				AgentDefName:           sub.agentDefName,
				TaskDesc:               sub.taskDesc,
				OwnerAgentID:           sub.OwnerAgentID(),
				OwnerTaskID:            sub.OwnerTaskID(),
				Depth:                  sub.Depth(),
				JoinToOwner:            sub.joinToOwner,
				State:                  string(state),
				LastSummary:            summary,
				PendingCompleteIntent:  pendingCompleteIntent,
				PendingCompleteSummary: pendingCompleteSummary,
			})
		}
		a.mu.RUnlock()

		usageSnap := a.usageTracker.SessionStats()
		if err := a.recovery.SaveSnapshot(&recovery.SessionSnapshot{
			Todos:                   todoStates,
			ActiveAgents:            agents,
			ModelName:               a.ModelName(),
			ActiveRole:              a.CurrentRole(),
			CreatedAt:               time.Now(),
			LastInputTokens:         a.ctxMgr.LastInputTokens(),
			LastTotalContextTokens:  a.ctxMgr.LastTotalContextTokens(),
			ActiveBackgroundObjects: spawnStatesForSnapshot(),
			UsageInputTokens:        usageSnap.InputTokens,
			UsageOutputTokens:       usageSnap.OutputTokens,
			UsageCacheReadTokens:    usageSnap.CacheReadTokens,
			UsageCacheWriteTokens:   usageSnap.CacheWriteTokens,
			UsageReasoningTokens:    usageSnap.ReasoningTokens,
			UsageLLMCalls:           usageSnap.LLMCalls,
			UsageEstimatedCost:      usageSnap.EstimatedCost,
			UsageByModel:            usageSnap.ByModel,
			UsageByAgent:            usageSnap.ByAgent,
		}); err != nil {
			slog.Warn("failed to save final recovery snapshot", "error", err)
		}

		a.recovery.Close()
	}

	if a.sessionLock != nil {
		if err := a.sessionLock.Release(); err != nil {
			slog.Warn("failed to release session lock on shutdown", "error", err)
		}
	}

	// Cancel the parent context which will unblock the Run select loop.
	a.cancel()

	// If Run was never started, there is nothing to wait for.
	if !a.started.Load() {
		return nil
	}

	// Wait for Run to return within the remaining shared shutdown budget.
	if wait := remaining(); wait > 0 {
		select {
		case <-a.done:
			return nil
		case <-time.After(wait):
			return fmt.Errorf("agent shutdown timed out after %v", timeout)
		}
	}
	return fmt.Errorf("agent shutdown timed out after %v", timeout)
}

// ---------------------------------------------------------------------------
// Internal: event loop machinery
// ---------------------------------------------------------------------------

func (a *MainAgent) recordEvidenceFromMessage(msg message.Message) {
	if msg.IsCompactionSummary {
		return
	}
	if strings.TrimSpace(msg.Content) == "" && len(msg.Parts) == 0 && strings.TrimSpace(msg.ToolDiff) == "" {
		return
	}
	text := strings.TrimSpace(msg.Content)
	if len(msg.Parts) > 0 {
		normalized := normalizeMessagesForSummary([]message.Message{msg})
		if len(normalized) > 0 {
			text = strings.TrimSpace(normalized[0].Content)
		}
	}
	switch msg.Role {
	case "user":
		switch {
		case isEscalateMessage(text):
			a.addEvidenceCandidate(buildEvidenceItem(
				evidenceEscalate,
				"SubAgent requested main-agent help",
				"This unresolved intervention request may still determine the next action.",
				"runtime user message",
				compactTextSnippet(text, 700),
			))
		case isSubAgentDoneMessage(text):
			a.addEvidenceCandidate(buildEvidenceItem(
				evidenceSubAgentDone,
				"SubAgent completion summary",
				"The main agent may need this exact completion summary before continuing.",
				"runtime user message",
				compactTextSnippet(text, 700),
			))
		case looksLikeUserCorrection(text):
			a.addEvidenceCandidate(buildEvidenceItem(
				evidenceUserCorrection,
				"User correction / constraint",
				"This explicitly constrains the next code change and should be preserved verbatim.",
				"runtime user message",
				compactTextSnippet(text, 600),
			))
		}
	case "tool":
		if strings.Contains(text, "Error:") || isToolErrorContent(text) {
			a.addEvidenceCandidate(buildEvidenceItem(
				evidenceToolError,
				"Latest failing tool result",
				"This looks like a current blocker; preserving the exact error helps the next continuation avoid guessing.",
				"runtime tool result",
				compactTextSnippet(text, 800),
			))
		}
		if strings.TrimSpace(msg.ToolDiff) != "" {
			a.addEvidenceCandidate(buildEvidenceItem(
				evidenceToolDiff,
				"Recent code diff",
				"The next continuation may depend on the exact recent code change.",
				"runtime tool diff",
				compactTextSnippet(msg.ToolDiff, 700),
			))
		}
	}
}

func (a *MainAgent) resetRuntimeEvidenceFromMessages(messages []message.Message) {
	a.clearEvidenceCandidates()
	for _, msg := range messages {
		a.recordEvidenceFromMessage(msg)
	}
}

// ---------------------------------------------------------------------------
// Slash input policy (what reaches the LLM as user content)
// ---------------------------------------------------------------------------

// filterUnsupportedParts removes image/pdf parts that the current model does
// not support. When parts are removed, a toast is emitted to notify the user.
// If all non-text parts are removed, the result falls back to plain text.
func (a *MainAgent) filterUnsupportedParts(content string, parts []message.ContentPart) (string, []message.ContentPart) {
	if len(parts) == 0 {
		return content, parts
	}

	a.llmMu.RLock()
	client := a.llmClient
	a.llmMu.RUnlock()
	if client == nil {
		return content, parts
	}

	var filtered []message.ContentPart
	var dropped []string
	for _, p := range parts {
		switch p.Type {
		case "image":
			if !client.SupportsInput("image") {
				dropped = append(dropped, "image")
				continue
			}
		case "pdf":
			if !client.SupportsInput("pdf") {
				dropped = append(dropped, "pdf")
				continue
			}
		}
		filtered = append(filtered, p)
	}

	if len(dropped) == 0 {
		return content, parts
	}

	// Deduplicate dropped types for the toast message.
	seen := map[string]bool{}
	var unique []string
	for _, d := range dropped {
		if !seen[d] {
			seen[d] = true
			unique = append(unique, d)
		}
	}
	a.emitToTUI(ToastEvent{
		Message: "The current model does not support " + strings.Join(unique, "/") + " input; attachments were ignored",
		Level:   "warn",
	})

	// If only text parts remain, collapse to plain content.
	if len(filtered) == 0 {
		return content, nil
	}
	allText := true
	for _, p := range filtered {
		if p.Type != "text" {
			allText = false
			break
		}
	}
	if allText && len(filtered) == 1 {
		return filtered[0].Text, nil
	}
	return content, filtered
}

// handleLocalOnlySlashCommands runs /export and /model. These must never
// be appended to the conversation or sent to the model. Returns true if handled.
// Runs even when the agent is busy (not queued). Skipped when the message includes
// image parts so multimodal input still reaches the model.
func (a *MainAgent) handleLocalOnlySlashCommands(content string, parts []message.ContentPart) bool {
	for _, part := range parts {
		if part.Type == "image" {
			return false
		}
	}
	c := strings.TrimSpace(content)
	switch {
	case c == "/export" || strings.HasPrefix(c, "/export "):
		a.handleExportCommand(c)
		return true
	case c == "/model" || strings.HasPrefix(c, "/model "):
		a.handleModelCommand(c)
		return true
	default:
		return false
	}
}

// processPendingUserMessagesBeforeLLMInTurn appends queued user messages to the
// conversation so the next LLM call sees tool results and user input together.
// Slash commands that require idle (/loop*, /resume*, /new, /compact) are left on the
// queue for the next idle drain.
func (a *MainAgent) processPendingUserMessagesBeforeLLMInTurn() {
	if len(a.pendingUserMessages) == 0 {
		return
	}
	pending := a.pendingUserMessages
	a.pendingUserMessages = nil
	var deferred []pendingUserMessage
	type consumedPendingDraft struct {
		draftID string
		msg     message.Message
	}
	var batch []message.Message
	var consumed []consumedPendingDraft
	for _, p := range pending {
		content := pendingUserMessageText(p)
		c := strings.TrimSpace(content)
		if c == "/resume" || strings.HasPrefix(c, "/resume ") || c == "/new" || c == "/compact" || isLoopSlashCommand(c) {
			deferred = append(deferred, p)
			continue
		}
		m, ok := a.pendingUserMessageToConversationMessage(p)
		if !ok {
			continue
		}
		batch = append(batch, m)
		consumed = append(consumed, consumedPendingDraft{draftID: p.DraftID, msg: m})
	}
	// Re-queue /resume* and anything that arrived concurrently (should be rare).
	a.pendingUserMessages = append(deferred, a.pendingUserMessages...)
	if len(batch) == 0 {
		return
	}
	slog.Debug("injecting pending user messages with tool results", "count", len(batch))
	for _, item := range consumed {
		a.ctxMgr.Append(item.msg)
		a.recordEvidenceFromMessage(item.msg)
		if a.recovery != nil {
			a.persistAsync("main", item.msg)
		}
		a.emitPendingDraftConsumed(item.draftID, item.msg)
	}
	a.syncBugTriagePromptFromSnapshot()
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

// handleUserMessage processes a new user message. When the agent is busy
// (turn != nil), the message is queued and processed later when the agent
// becomes idle (with any other queued messages in one batch). When idle,
// slash commands are handled immediately; otherwise a new turn is started.
func (a *MainAgent) handleUserMessage(evt Event) {
	var content string
	var parts []message.ContentPart
	switch p := evt.Payload.(type) {
	case string:
		content = p
	case []message.ContentPart:
		parts = p
		// Extract text parts for slash-command detection.
		for _, part := range parts {
			if part.Type == "text" {
				content += part.Text
			}
		}
	default:
		slog.Error("handleUserMessage: invalid payload type",
			"payload_type", fmt.Sprintf("%T", evt.Payload),
		)
		return
	}

	slog.Debug("handling user message", "content_len", len(content))

	// /export and /model: never queue or send to the model.
	if a.handleLocalOnlySlashCommands(content, parts) {
		return
	}

	a.explicitUserTurnCount++
	a.sweepSubAgentLifecycle()

	// When busy, queue the message; it will be drained and sent in one batch when idle.
	if a.turn != nil {
		if a.tryHandleBusySlashCommand(content) {
			return
		}
		a.pendingUserMessages = enqueuePendingUserMessage(a.pendingUserMessages, pendingUserMessage{
			Content:  content,
			Parts:    parts,
			FromUser: true,
		})
		return
	}

	// Idle: session and compaction commands before starting a turn.
	if a.tryHandleSlashCommand(content) {
		return
	}

	// Start a new turn and call LLM.
	a.newTurn()
	turnID := a.turn.ID
	turnCtx := a.turn.Ctx

	outC, outP := a.expandSlashCommandForModel(content, parts)
	outC, outP = a.filterUnsupportedParts(outC, outP)
	userMsg := message.Message{
		Role:    "user",
		Content: outC,
		Parts:   outP,
	}
	a.recordCommittedUserMessage(userMsg)
	a.syncBugTriagePromptFromSnapshot()

	a.beginMainLLMAfterPreparation(turnCtx, turnID, "")
}

func (a *MainAgent) handlePendingDraftUpsert(evt Event) {
	pending, ok := evt.Payload.(pendingUserMessage)
	if !ok {
		slog.Error("handlePendingDraftUpsert: invalid payload type",
			"payload_type", fmt.Sprintf("%T", evt.Payload),
		)
		return
	}
	pending.DraftID = strings.TrimSpace(pending.DraftID)
	if pending.DraftID == "" {
		return
	}
	pending.Parts = pendingDraftParts(pending)
	pending.Content = pendingUserMessageText(pending)

	if a.turn != nil {
		if a.tryHandleBusySlashCommand(pending.Content) {
			return
		}
		a.pendingUserMessages = enqueuePendingUserMessage(a.pendingUserMessages, pending)
		return
	}

	content := pendingUserMessageText(pending)
	if a.handleLocalOnlySlashCommands(content, pending.Parts) {
		return
	}
	if a.tryHandleSlashCommand(content) {
		return
	}
	userMsg, ok := a.pendingUserMessageToConversationMessage(pending)
	if !ok {
		return
	}

	a.newTurn()
	turnID := a.turn.ID
	turnCtx := a.turn.Ctx
	a.recordCommittedUserMessage(userMsg)
	a.emitPendingDraftConsumed(pending.DraftID, userMsg)
	a.beginMainLLMAfterPreparation(turnCtx, turnID, "")
}

func (a *MainAgent) handlePendingDraftRemove(evt Event) {
	draftID, ok := evt.Payload.(string)
	if !ok {
		slog.Error("handlePendingDraftRemove: invalid payload type",
			"payload_type", fmt.Sprintf("%T", evt.Payload),
		)
		return
	}
	draftID = strings.TrimSpace(draftID)
	if draftID == "" {
		return
	}
	var removed bool
	a.pendingUserMessages, removed = removePendingDraft(a.pendingUserMessages, draftID)
	if !removed {
		slog.Debug("pending mirrored draft already absent", "draft_id", draftID)
	}
}

func (a *MainAgent) handleAppendContext(evt Event) {
	var msg message.Message
	switch p := evt.Payload.(type) {
	case message.Message:
		msg = p
	case string:
		msg = message.Message{Role: "user", Content: p}
	default:
		return
	}
	if strings.TrimSpace(msg.Content) == "" && len(msg.Parts) == 0 {
		return
	}
	msg.Role = "user"
	a.ctxMgr.Append(msg)
	a.recordEvidenceFromMessage(msg)
	if a.recovery != nil {
		persistMsg := msg
		if strings.TrimSpace(persistMsg.Content) == "" {
			persistMsg.Content = message.UserPromptPlainText(msg)
		}
		a.persistAsync("main", persistMsg)
	}
}

// handleTurnCancelled closes any still-pending tool cards in the UI and marks
// the agent idle. It intentionally does not append tool messages to ctxMgr: a
// cancelled tool has no conversation-visible result and should not be sent back
// to the model on the next turn.
func (a *MainAgent) handleTurnCancelled(evt Event) {
	// Turn isolation: ignore duplicate/stale cancel events so an old cancellation
	// cannot force a newer turn back to idle.
	if a.turn == nil || evt.TurnID == 0 || evt.TurnID != a.turn.ID {
		slog.Debug("discarding stale turn cancellation",
			"event_turn", evt.TurnID,
			"current_turn", a.currentTurnID(),
		)
		return
	}

	payload, ok := evt.Payload.(*TurnCancelledPayload)
	if !ok {
		slog.Error("handleTurnCancelled: invalid payload type",
			"payload_type", fmt.Sprintf("%T", evt.Payload),
		)
		return
	}
	if payload == nil {
		return
	}

	a.savePartialAssistantMsg()
	if payload.KeepPendingUserMessagesQueued {
		a.pausePendingUserDrainOnce = true
	}
	status := ToolResultStatusCancelled
	if payload.MarkToolCallsFailed {
		status = ToolResultStatusError
	}
	persistedResults := a.persistInterruptedToolResults(payload.Calls, status, context.Canceled)
	if persistedResults > 0 {
		slog.Info("persisted interrupted tool-call results after cancellation",
			"turn_id", evt.TurnID,
			"count", persistedResults,
		)
	}
	if payload.MarkToolCallsFailed {
		emitFailedToolResults(a.emitToTUI, payload.Calls, context.Canceled)
	} else {
		emitCancelledToolResults(a.emitToTUI, payload.Calls)
	}
	if payload.CommitPendingUserMessagesWithoutTurn {
		a.commitPendingUserMessagesWithoutTurn()
	}
	a.emitActivity("main", ActivityIdle, "")
	a.llmClient.ResetResponsesSession("turn_cancel")
	a.markActiveSubAgentMailboxAck(false)
	a.setIdleAndDrainPending()
}

// handleAgentError emits the error to the TUI and logs it. An IdleEvent is
// also sent so the TUI knows the agent is ready for new input.
//
// If SourceID is "main" or empty, this is the MainAgent's own error (existing
// Phase 1 behavior). If SourceID identifies a SubAgent, the agent is cleaned
// up: file locks released, focus switched, map entry removed, semaphore freed,
// and context cancelled.
func (a *MainAgent) handleAgentError(evt Event) {
	err, ok := evt.Payload.(error)
	if !ok {
		slog.Error("handleAgentError: invalid payload type",
			"payload_type", fmt.Sprintf("%T", evt.Payload),
		)
		return
	}

	// Guard: SourceID "main" or empty means MainAgent's own LLM/tool error —
	// no SubAgent to clean up.
	if evt.SourceID == "main" || evt.SourceID == "" {
		// Turn isolation: discard errors from cancelled/stale turns.
		if a.turn != nil && evt.TurnID != 0 && evt.TurnID != a.turn.ID {
			slog.Debug("discarding stale error",
				"event_turn", evt.TurnID,
				"current_turn", a.currentTurnID(),
			)
			return
		}

		slog.Error("agent error",
			"error", err,
			"turn_id", evt.TurnID,
			"instance", a.instanceID,
		)
		a.savePartialAssistantMsg()
		a.failPendingToolCalls(a.turn, err)
		a.fireHookBackground(a.parentCtx, hook.OnAgentError, evt.TurnID, map[string]any{
			"message":         err.Error(),
			"error_kind":      classifyAgentError(err),
			"source_agent_id": a.instanceID,
		})
		a.emitToTUI(ErrorEvent{Err: err})
		a.stopLoopAsBlocked(err.Error())
		a.markActiveSubAgentMailboxAck(false)
		a.setIdleAndDrainPending()
		return
	}

	// SubAgent error: clean up the failed agent (same as handleAgentDone minus
	// LLM review).
	slog.Error("SubAgent error",
		"error", err,
		"source", evt.SourceID,
	)

	var emitCalls, persistCalls []PendingToolCall
	a.mu.RLock()
	sub := a.subAgents[evt.SourceID]
	a.mu.RUnlock()
	if sub != nil {
		emitCalls, persistCalls = sub.drainPendingToolFailureSets(err)
	}
	if len(persistCalls) > 0 && sub != nil {
		persistedResults := sub.persistInterruptedToolResults(persistCalls, ToolResultStatusError, err)
		if persistedResults > 0 {
			slog.Info("persisted failed sub-agent tool-call results after terminal error",
				"agent", evt.SourceID,
				"count", persistedResults,
			)
		}
	}
	if len(emitCalls) > 0 {
		emitFailedToolResults(a.emitToTUI, emitCalls, err)
		a.emitActivity(evt.SourceID, ActivityIdle, "")
	}

	a.hookEngine.FireBackground(a.parentCtx, newHookEnvelope(
		hook.OnAgentError,
		a.sessionDir,
		evt.TurnID,
		evt.SourceID,
		"sub",
		a.projectRoot,
		"",
		"",
		map[string]any{
			"message":         err.Error(),
			"error_kind":      classifyAgentError(err),
			"source_agent_id": evt.SourceID,
		},
	))

	// Auto-switch focus back to Main if user was focused on the failed agent.
	if focused := a.focusedAgent.Load(); focused != nil && focused.instanceID == evt.SourceID {
		a.focusedAgent.Store(nil)
		a.emitToTUI(AgentStatusEvent{
			AgentID: evt.SourceID,
			Status:  "error",
			Message: fmt.Sprintf("SubAgent %s errored; focus switched back to main", evt.SourceID),
		})
	}

	sub2 := a.subAgentByID(evt.SourceID)
	if sub2 != nil {
		a.handleSubAgentStateChangedEvent(Event{
			Type:     EventSubAgentStateChanged,
			SourceID: evt.SourceID,
			Payload:  &SubAgentStateChangedPayload{State: SubAgentStateFailed, Summary: err.Error()},
		})
		if sub2.semHeld {
			a.releaseSubAgentSlot(sub2)
		}
		a.emitActivity(evt.SourceID, ActivityIdle, "")
		a.handleSubAgentCloseRequestedEvent(Event{
			Type:     EventSubAgentCloseRequested,
			SourceID: evt.SourceID,
			Payload: &SubAgentCloseRequestedPayload{
				Reason:       err.Error(),
				ClosedReason: "subagent failed",
				FinalState:   SubAgentStateFailed,
			},
		})
	}

	a.emitToTUI(ErrorEvent{Err: fmt.Errorf("SubAgent %s error: %w", evt.SourceID, err)})
	a.stopLoopAsBlocked(err.Error())
	a.setIdleAndDrainPending()
}

// ---------------------------------------------------------------------------
// Plan / Execute workflow
// ---------------------------------------------------------------------------

// startPlanExecution starts executing a plan document. It switches to the
// specified agent role, loads the plan, builds an execution-oriented system
// prompt, and starts the LLM loop. planPath may be empty, in which case
// lastPlanPath is used. agentName defaults to "builder" if empty.
func (a *MainAgent) startPlanExecution(planPath, agentName string) {
	if agentName == "" {
		agentName = "builder"
	}
	a.clearSystemPromptOverride()
	// Switch to the target agent role so the correct tools and permissions apply.
	if a.activeConfig == nil || a.activeConfig.Name != agentName {
		if err := a.switchRole(agentName, true); err != nil {
			a.emitToTUI(ErrorEvent{Err: fmt.Errorf("failed to switch to %s role: %w", agentName, err)})
			a.setIdleAndDrainPending()
			return
		}
	}

	if planPath == "" {
		planPath = a.lastPlanPath
	}
	if planPath == "" {
		a.emitToTUI(ErrorEvent{Err: fmt.Errorf("no plan to execute; specify a path")})
		a.setIdleAndDrainPending()
		return
	}

	// Read the plan content for injection into the prompt.
	planContent, err := os.ReadFile(planPath)
	if err != nil {
		a.emitToTUI(ErrorEvent{Err: fmt.Errorf("failed to read plan: %w", err)})
		a.setIdleAndDrainPending()
		return
	}
	if len(strings.TrimSpace(string(planContent))) == 0 {
		a.emitToTUI(ErrorEvent{Err: fmt.Errorf("plan file is empty: %s", planPath)})
		a.setIdleAndDrainPending()
		return
	}

	slog.Info("starting plan execution",
		"plan_path", planPath,
	)

	newSessionDir, err := a.createRuntimeSessionDir()
	if err != nil {
		slog.Warn("failed to create session dir for plan execution", "error", err)
		a.emitToTUI(ErrorEvent{Err: fmt.Errorf("create execution session: %w", err)})
		a.setIdleAndDrainPending()
		return
	}

	newLock, err := recovery.AcquireSessionLock(newSessionDir)
	if err != nil {
		a.emitToTUI(ErrorEvent{Err: fmt.Errorf("execution session lock: %w", err)})
		a.setIdleAndDrainPending()
		return
	}

	oldRecovery, turnCtx := a.prepareSessionSwitch()
	turnID := a.turn.ID
	if err := a.ensureSessionBuilt(turnCtx); err != nil {
		_ = newLock.Release()
		a.emitToTUI(ErrorEvent{Err: fmt.Errorf("prepare execution session: %w", err)})
		a.setIdleAndDrainPending()
		return
	}
	oldLock := a.sessionLock
	a.freezeCurrentSession(oldRecovery)
	if oldLock != nil {
		if releaseErr := oldLock.Release(); releaseErr != nil {
			slog.Warn("execution session: failed to release old session lock", "error", releaseErr)
		}
	}
	a.sessionLock = newLock
	a.resetSessionRuntimeState()
	a.llmClient.ResetResponsesSession("new_session")
	a.installSessionTarget(newSessionDir)

	// Freeze the new session's tool/system surface before installing the
	// execution-specific prompt so the first execution request sees a stable
	// session configuration.
	if err := a.ensureSessionBuilt(turnCtx); err != nil {
		a.emitToTUI(ErrorEvent{Err: fmt.Errorf("prepare execution session: %w", err)})
		a.setIdleAndDrainPending()
		return
	}
	execPrompt := a.buildExecuteSystemPrompt(planPath, string(planContent))
	a.setSystemPromptOverride(execPrompt)

	// Notify TUI to wipe the viewport so planner-phase messages are cleared.
	a.emitToTUI(SessionRestoredEvent{})

	// Add initial execution instruction that drives LLM-based dispatch.
	a.ctxMgr.Append(message.Message{
		Role: "user",
		Content: fmt.Sprintf(
			"Execute the plan at %s. Analyse the plan content, identify all tasks and their dependencies, "+
				a.executionStartInstruction()+
				" "+a.executionPacingInstruction(),
			planPath,
		),
	})
	a.recordEvidenceFromMessage(message.Message{
		Role: "user",
		Content: fmt.Sprintf(
			"Execute the plan at %s. Analyse the plan content, identify all tasks and their dependencies, "+
				a.executionStartInstruction()+
				" "+a.executionPacingInstruction(),
			planPath,
		),
	})
	if a.usageLedger != nil {
		if err := a.usageLedger.SetFirstUserMessage(fmt.Sprintf("Execute the plan at %s", planPath)); err != nil {
			slog.Warn("failed to update usage summary first user message", "error", err)
		}
		a.updateSessionSummary(func(summary *SessionSummary) {
			if summary == nil {
				return
			}
			if summary.FirstUserMessage == "" {
				summary.FirstUserMessage = fmt.Sprintf("Execute the plan at %s", planPath)
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

// buildExecuteSystemPrompt constructs a system prompt for the plan execution
// phase. It should identify the target plan and execution expectations without
// pre-committing the current main role to a specific strategy such as direct
// implementation or subagent orchestration.
func (a *MainAgent) buildExecuteSystemPrompt(planPath, planContent string) string {
	base := a.buildSystemPrompt()
	hasTodoWrite := a.hasTodoWriteAccess()

	var sb strings.Builder
	if hasTodoWrite {
		base = strings.Replace(base, a.todoWorkflowPromptBlock(), "", 1)
	}
	sb.WriteString(base)

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
6. **For implementation tasks, first dispatch all currently independent tasks
   whose write scopes are clearly disjoint.**
7. **Dispatch tasks in parallel only when their write scopes are clearly independent;
   do not run parallel SubAgents that may edit the same file or tightly coupled targets.**
8. **After dispatching the current independent implementation tasks, if there is
   no new independent task to send, stop doing implementation work in MainAgent and
   wait for runtime coordination to deliver the next decision point.**
9. **Until you receive Escalate, Complete, or a clear error/blocked signal, do not
   take over implementation just because a SubAgent is briefly quiet, has not written
   files yet, or has not produced immediate visible output.**
10. **Report real blockers**: if the current role lacks a needed capability or
   permission, explain the blocker instead of assuming hidden capabilities or
   nonexistent workers.
11. **Finish**: when everything is done, give a concise final summary.
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
4. **For implementation tasks, first dispatch all currently independent tasks
   whose write scopes are clearly disjoint.**
5. **Dispatch tasks in parallel only when their write scopes are clearly independent;
   do not run parallel SubAgents that may edit the same file or tightly coupled targets.**
6. **After dispatching the current independent implementation tasks, if there is
   no new independent task to send, stop doing implementation work in MainAgent and
   wait for runtime coordination to deliver the next decision point.**
7. **Until you receive Escalate, Complete, or a clear error/blocked signal, do not
   take over implementation just because a SubAgent is briefly quiet, has not written
   files yet, or has not produced immediate visible output.**
8. **Report real blockers**: if the current role lacks a needed capability or
   permission, explain the blocker instead of assuming hidden capabilities or
   nonexistent workers.
9. **Finish**: when everything is done, give a concise final summary.
`, planPath)
	}

	fmt.Fprintf(&sb, `

### Plan Content
%s
`, planContent)

	return sb.String()
}

// extractToolArgument returns the string used for permission pattern matching.
//
// For Bash the full command string is used (e.g. "git push origin main").
// For file tools (Read/Write/Edit) the path argument is extracted so that
// path-based rules like `Write: { "/etc/*": deny }` work correctly.
// For search tools (Grep/Glob) the pattern argument is extracted.
// All other tools fall back to "*" (whole-tool match).
// ---------------------------------------------------------------------------

// ReloadAgentsMD reloads project AGENTS.md from disk and marks the startup
// gate (agentsMDReady) so ensureSessionBuilt can proceed. The content is
// consumed the next time ensureSessionBuilt rebuilds the session-context
// reminder (on session-head events). Mid-session edits to AGENTS.md are not
// picked up until the next /new, /resume, or equivalent reset — AGENTS.md is
// treated as a session-scope snapshot (see
// docs/architecture/prompt-and-context-engineering.md §4.2).
func (a *MainAgent) ReloadAgentsMD() bool {
	content := loadAgentsMDWithWorkDir(a.projectRoot, a.cachedWorkDir)

	a.promptMetaMu.Lock()
	if content == a.cachedAgentsMD {
		a.promptMetaMu.Unlock()
		a.markAgentsMDReady()
		return false
	}
	a.cachedAgentsMD = content
	a.promptMetaMu.Unlock()
	a.markAgentsMDReady()
	return true
}

func (a *MainAgent) refreshSystemPrompt() {
	a.llmMu.RLock()
	override := a.systemPromptOverride
	a.llmMu.RUnlock()
	if override != "" {
		a.installSystemPrompt(override)
		return
	}
	a.installSystemPrompt(a.buildSystemPrompt())
}

func (a *MainAgent) setSystemPromptOverride(prompt string) {
	a.llmMu.Lock()
	a.systemPromptOverride = prompt
	a.llmMu.Unlock()
	a.installSystemPrompt(prompt)
}

func (a *MainAgent) clearSystemPromptOverride() {
	a.llmMu.Lock()
	a.systemPromptOverride = ""
	a.llmMu.Unlock()
}

func (a *MainAgent) installSystemPrompt(prompt string) {
	a.llmMu.Lock()
	if prompt == a.installedSysPrompt {
		a.llmMu.Unlock()
		return
	}
	a.installedSysPrompt = prompt
	client := a.llmClient
	a.llmMu.Unlock()

	if client != nil {
		client.SetSystemPrompt(prompt)
	}
	a.ctxMgr.SetSystemPrompt(message.Message{
		Role:    "system",
		Content: prompt,
	})
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
		slog.Error("handleExecutePlanEvent: invalid payload type",
			"payload_type", fmt.Sprintf("%T", evt.Payload),
		)
		return
	}
	a.startPlanExecution(p.PlanPath, p.AgentName)
}
