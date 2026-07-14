package agent

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/analytics"
	"github.com/keakon/chord/internal/command"
	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/filectx"
	"github.com/keakon/chord/internal/filelock"
	"github.com/keakon/chord/internal/hook"
	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/mcp"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/ratelimit"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/skill"
	"github.com/keakon/chord/internal/thinkingtranslate"
	"github.com/keakon/chord/internal/tools"
)

// Keyed by MCP scope and server name. Entries whose Mgr is nil are sentinels
// for servers inherited from top-level config. Agent-private entries are scoped
// by agent definition so instances of one agent reuse a connection without
// conflating same-named servers owned by different agents.
type mcpServerEntry struct {
	Mgr   *mcp.Manager // nil for main-agent servers (sentinel)
	Tools []tools.Tool // nil for sentinel entries
}

func mainMCPServerCacheKey(serverName string) string {
	return "main\x00" + serverName
}

func agentMCPServerCacheKey(agentName, serverName string) string {
	return "agent\x00" + agentName + "\x00" + serverName
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
	pendingToolMu        sync.Mutex
	PendingToolMeta      map[string]PendingToolCall
	completedToolCallIDs map[string]struct{}
	// streamingToolCalls holds speculative tool metadata from SSE tool_use_start
	// before the response is finalized. Used only for TUI cancel/fail bookkeeping;
	// it must never be persisted until merged into PendingToolMeta.
	streamingToolMu    sync.Mutex
	streamingToolCalls map[string]PendingToolCall
	streamingToolOrder []string
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
	OversizeRecoveryCount              int
	malformedInBatch                   int // abnormal calls in the current LLM-response batch
	CompletedToolCalls                 []any
	ChangedFiles                       []any
	toolExecutionBatches               []toolExecutionBatch
	nextToolBatch                      int
	activeToolBatchCancel              context.CancelFunc
	streamingToolExec                  *StreamingToolExecutor
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
	SpeculativeStartAt     time.Time
	FirstVisibleResultAt   time.Time
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
	ModelContextNote  string
	Images            []message.ContentPart // image/binary parts produced by the tool (ViewImage, MCP image results)
	EffectiveArgsJSON string
	Audit             *message.ToolArgsAudit
	LSPReviews        []message.LSPReview
	FileState         *message.ToolFileState
	PreFilePath       string
	PreContent        string
	PreExisted        bool
	speculativeHooks  *speculativeToolHooks
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
	Patterns []string
	Scope    int // 0=session, 1=project, 2=userGlobal (matches permission.RuleScope)
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
//   - toolName:     the name of the tool being invoked (e.g. "Shell")
//   - args:         the raw JSON arguments string
//   - needsApproval: explicit arguments covered by this approval prompt
//   - alreadyAllowed: explicit arguments already allowed by rules in the same batch
//   - needsApprovalRules: rule patterns that matched ask items in this prompt
//   - alreadyAllowedRules: rule patterns that matched allowed items in the same batch
//   - ConfirmResponse: approved decision plus the final args JSON chosen by the user
//   - err:          non-nil if the confirmation flow itself fails
type ConfirmFunc func(ctx context.Context, toolName string, args string, needsApproval []string, alreadyAllowed []string, needsApprovalRules []string, alreadyAllowedRules []string) (ConfirmResponse, error)

// ---------------------------------------------------------------------------
// SubAgentInfo
// ---------------------------------------------------------------------------

// SubAgentInfo carries read-only information about a running SubAgent for TUI
// display (sidebar listing). The fields are snapshot values safe to read from
// any goroutine.
type SubAgentInfo struct {
	InstanceID       string
	TaskID           string
	AgentDefName     string
	TaskDesc         string
	ModelName        string
	SelectedRef      string
	RunningRef       string
	State            string
	Color            string // optional ANSI color code from agent config
	LastSummary      string
	UrgentInboxCount int
	LastArtifact     tools.ArtifactRef
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
	DraftID             string
	Content             string
	Parts               []message.ContentPart
	FromUser            bool
	MailboxAckID        string
	CoalesceKey         string
	DrainContextAppends bool
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
	yoloEnabled   atomic.Bool         // temporary main-agent permission bypass; Handoff/Delegate remain governed by rules

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
	outputCh                      chan AgentEvent
	outputMu                      sync.RWMutex
	outputClosed                  atomic.Bool
	outputDropLogMu               sync.Mutex
	outputDropLogLastByType       map[string]time.Time
	outputDropLogSuppressedByType map[string]int

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
	evidence evidenceCandidateTracker

	// loopReductionMu protects request-shape snapshots, reduction stats, and
	// loopState fields that may be read by callLLM on a worker goroutine while
	// the event loop handles a busy /loop command.
	loopReductionMu               sync.Mutex
	lastPreparedLLMTurnID         uint64
	lastPreparedLLMRequestShape   []stableReductionMessageShape
	lastPreparedLLMRequestPrefix  []message.Message
	lastPreparedLLMReducedIndices []bool
	lastPreparedReductionStats    ContextReductionStats
	lastPreparedLLMToolDefHash    [sha256.Size]byte
	lastPreparedStablePrefixLen   int
	wrapUpGraceTurnID             uint64
	wrapUpGraceRemaining          int
	contextReductionStats         ContextReductionStats
	contextSurfaceRefreshAllowed  atomic.Bool
	lastLLMRequestModelRef        string
	llmModelRunLength             int

	// Async durable compaction (pre-request gate): defer inbound events until commit.
	compactionState      compactionState
	sessionEpoch         uint64
	nextCompactionPlanID uint64

	thinkingTranslateMu   sync.Mutex
	thinkingTranslateSvc  *thinkingtranslate.Service
	thinkingTranslateSeen map[string]struct{}

	sessionDir          string
	modelName           string
	providerModelRef    string // "provider/model" for unique identification
	runningModelRef     string // actual model used in latest LLM call
	previousLLMModelRef string
	instanceID          string
	mcpClientInfo       mcp.ClientInfo
	globalIdle          atomic.Bool
	lastIdleTurnID      atomic.Uint64

	// turnMu protects the turn pointer for cross-goroutine access.
	// The event-loop goroutine writes turn in newTurn(); external goroutines
	// (TUI, shutdown) read it via CancelCurrentTurn() / Shutdown().
	turnMu sync.Mutex

	// llmMu protects llmClient, modelName, providerModelRef, running-model
	// continuity, and model-run cache-warmth state for
	// cross-goroutine access. The TUI goroutine reads ModelName() and
	// ProviderModelRef() from View(), while SwapLLMClient / SwitchModel
	// write these fields. callLLM snapshots under RLock at the start
	// to ensure consistent model name for hooks and usage tracking.
	llmMu                sync.RWMutex
	installedSysPrompt   string
	systemPromptOverride string
	// The event-loop goroutine owns the active request and pending model-pool
	// switch state. Pool switches requested while a main LLM request is in flight
	// are applied at the next request boundary, so they do not invalidate the
	// request currently producing output.
	mainLLMRequestInFlight      atomic.Bool
	pendingMainModelPoolSwitch  bool
	pendingAgentModelPoolSwitch map[string]struct{}
	pendingModelPoolRollback    *modelPoolSelectionSnapshot

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

	// Confirm/Question interaction: owns the requestID→response-channel
	// plumbing for the single-modal confirm and question flows. Wired in
	// NewMainAgent once stoppingCh exists.
	interaction *interactionBroker

	// Plan execution workflow state.
	projectRoot            string
	lastPlanPath           string
	pendingHandoff         *HandoffResult // deferred Handoff action; processed after all sibling tools finish
	pendingLoopExitResults []*loopExitResult

	// Role system: MainAgent operates as one of several roles (builder, planner, etc.).
	activeConfig *config.AgentConfig            // currently active role (nil = no role set yet; defaults to builder)
	agentConfigs map[string]*config.AgentConfig // pre-loaded: built-in → global → project (highest priority)

	// Multi-agent orchestration. subs owns the live sub-agent maps and the
	// RWMutex that guards them (formerly inline MainAgent fields mu/subAgents/
	// taskRecords/nudgeCounts/subAgentStateEnteredTurn).
	subs                     subAgentRegistry
	sem                      chan struct{}             // bounded concurrency semaphore (cap = 10)
	fileTrack                *filelock.FileTracker     // file write conflict detection
	fileBackups              *fileBackupManager        // session-scoped risky write backups
	recovery                 *recovery.RecoveryManager // session persistence and crash recovery
	sessionLock              *recovery.SessionLock     // cross-process exclusive ownership of sessionDir
	sessionArtifactsDirFn    func() string             // active session artifacts directory for exports / dumps
	sessionTargetChangedFn   func(string)              // notified after active sessionDir changes
	focusedAgent             atomic.Pointer[SubAgent]  // currently focused SubAgent (nil = main)
	subAgentInbox            subAgentInbox
	ownedSubAgentMailboxes   map[string][]SubAgentMailboxMessage // owner agentID -> descendant mailbox waiting for owner-local delivery
	subAgentMailboxIDsMu     sync.Mutex
	subAgentMailboxIDs       map[string]struct{} // session-scoped idempotency keys for persisted and live mailbox events
	mailboxDeliveryPaused    atomic.Bool         // restored sessions wait for explicit user continuation before mailbox delivery
	pendingSubAgentMailboxes []*SubAgentMailboxMessage
	activeSubAgentMailboxes  []*SubAgentMailboxMessage
	activeSubAgentMailbox    *SubAgentMailboxMessage
	activeSubAgentMailboxAck bool
	subAgentMailboxSeq       atomic.Uint64
	subAgentInboxSummaryMu   sync.RWMutex
	subAgentUrgentCounts     map[string]int
	explicitUserTurnCount    uint64

	// mcpServerCache maps scoped server keys to connections. Main-agent servers
	// are registered as sentinels (Mgr==nil); SubAgent-exclusive servers are
	// isolated by agent definition and shared by instances of that definition.
	mcpServerCacheMu sync.Mutex
	mcpServerCache   map[string]*mcpServerEntry

	// Custom slash commands loaded from MD files / YAML config.
	customCommandsMu sync.RWMutex
	customCommands   []*command.Definition

	// Todo state (implements tools.TodoStore).
	todoItems []tools.TodoItem
	todoMu    sync.RWMutex

	// Minimal loop-controller runtime state for post-assistant stop assessment.
	loopState loopRuntimeState
	// pendingLoopContinuation is a request-scoped continuation note surfaced via
	// turn overlays for the next LLM request. It must not be re-persisted as a
	// synthetic user message after assistant turns that already emitted tool
	// calls; only terminal assistant stops without tool calls may inject a new
	// runtime user continuation message.
	pendingLoopContinuation *LoopContinuationNote
	// pendingLSPDiagnosticOverlay is a one-shot generic reminder injected into the next
	// LLM request after a write/edit changes LSP diagnostics on a directly
	// modified file. The concrete diagnostics stay attached to each tool result's
	// LSPReviews; this overlay only reminds the model to check them. It is
	// request-scoped and never persisted to durable context.
	pendingLSPDiagnosticOverlay string

	// pendingRecoveryPrompt is a request-scoped recovery prompt injected after
	// length-recovery auto compaction succeeds. It is consumed as a one-shot
	// turn overlay and never appended to ctxMgr durable messages.
	pendingRecoveryPrompt string
	// pendingAutoContinuePrompt is a request-scoped continuation hint injected
	// after usage-driven or oversize-driven compaction succeeds, so the next
	// automatically resumed turn continues the active task without persisting an
	// extra durable message.
	pendingAutoContinuePrompt string
	// pendingAutoContinueReplayPrompt is a one-shot request-scoped reminder that
	// replays the most recent real user intent after compaction, without
	// persisting another durable user message or replaying prior tool side effects.
	pendingAutoContinueReplayPrompt string
	// pendingCompactionResume keeps the durable recovery intent for a compaction-
	// driven continuation. It is rebuilt into one-shot request overlays when the
	// session resumes and the user continues from context, without persisting an
	// extra user message.
	pendingCompactionResume *recovery.PendingCompactionResume
	// newTurnOversizeRecoveryCount carries durable oversize retry state across
	// /continue or auto-continue boundaries so retry limits remain effective
	// after restore/restart.
	newTurnOversizeRecoveryCount int
	toolTraceMu                  sync.Mutex
	toolTrace                    map[string]toolCallStageTrace

	// Adhoc task counter for auto-assigning "adhoc-N" IDs.
	adhocSeq atomic.Uint64

	// Optional MCP summary injected into the system prompt (set after MCP init).
	mcpServersPromptMu    sync.RWMutex
	mcpServersPrompt      string
	pendingMCPTools       []tools.Tool
	pendingMCPReplace     bool
	agentsMDReady         chan struct{}
	agentsMDReadyOnce     sync.Once
	skillsReady           chan struct{}
	skillsReadyOnce       sync.Once
	mcpReadyMu            sync.Mutex
	mcpReady              chan struct{}
	mcpTransitionActive   atomic.Bool
	mcpControlFn          func(context.Context, MCPControlRequest) (MCPControlResult, error)
	sessionBuilt          atomic.Bool
	bugTriagePromptActive atomic.Bool

	// shuttingDown is set to true when Shutdown begins. UpdateTodos checks
	// this flag to avoid overwriting the final snapshot.
	shuttingDown atomic.Bool

	// started is set to true when Run is called. Shutdown uses this to skip
	// waiting for the event loop and persist goroutine if Run was never called.
	started atomic.Bool

	compactionWg sync.WaitGroup

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

	// modelPoolPolicy manages runtime model pool selection for the current main
	// role plus explicit per-agent overrides. Set via SetModelPoolPolicy after
	// construction.
	modelPoolPolicy *RuntimeModelPoolPolicy

	// modelPoolStatePath is the per-project file path for persisting pool state.
	modelPoolStatePath string

	// LSP/MCP state providers for TUI sidebar display (set via SetLSPStatusFunc / SetMCPStatusFunc).
	lspServerListFn     func() []LSPServerDisplay
	mcpServerListFn     func() []MCPServerDisplay
	mcpKnownToolNamesFn func(string) []string
	lspSessionResetFn   func()
	lspSessionLoadFn    func([]message.Message)

	// Async persistence pump for ordered JSONL writes.
	persist *persistencePump

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

	// cachedSessionReminderContent is the meta user message content carrying
	// environment + AGENTS.md (under "# AGENTS.md instructions" /
	// <INSTRUCTIONS>). Built once ensureSessionBuilt completes.
	// Injected before the first user message only once per session-head, then
	// suppressed until resetSessionBuildState. Not persisted to ctxMgr or jsonl.
	cachedSessionReminderContent atomic.Pointer[string]
	// sessionReminderInjected is true after cachedSessionReminderContent has been
	// injected into an LLM call for the current session-head.
	sessionReminderInjected atomic.Bool

	// frozenToolDefs is the LLM tool surface snapshot captured at
	// ensureSessionBuilt time. Kept stable for the life of the agent instance
	// so the provider request prefix (system prompt + tools[]) does not drift
	// and prompt cache / Responses previous_response_id remain effective.
	// Cleared by resetSessionBuildState on session-head events.
	frozenToolDefs atomic.Pointer[[]message.ToolDefinition]
	// surfaceDirty asks the next request to compare the current runtime
	// permission/MCP surface with the frozen request surface before rebuilding it.
	surfaceDirty atomic.Bool

	// rateLimitMu protects per-provider rate-limit snapshots for cross-goroutine access.
	rateLimitMu    sync.RWMutex
	rateLimitSnaps map[string]*ratelimit.KeyRateLimitSnapshot

	// Activity observer for side-band runtime reactions (e.g. power management).
	activityObserverMu  sync.RWMutex
	activityObserver    ActivityObserver
	busyPreparationMu   sync.RWMutex
	busyPreparationHook func(context.Context) error
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
	mcpClientInfo mcp.ClientInfo,
) *MainAgent {
	parentCtx, cancel := context.WithCancel(ctx)

	workDir, _ := os.Getwd()
	if workDir == "" {
		workDir = projectRoot
	}
	gitStatusReady := make(chan struct{})

	a := &MainAgent{
		parentCtx:              parentCtx,
		cancel:                 cancel,
		llmClient:              llmClient,
		ctxMgr:                 ctxMgr,
		tools:                  toolRegistry,
		hookEngine:             hookEngine,
		usageTracker:           analytics.NewUsageTracker(),
		usageLedger:            analytics.NewUsageLedger(sessionDir, projectRoot),
		invokedSkills:          make(map[string]*skill.Meta),
		globalConfig:           globalCfg,
		projectConfig:          projectCfg,
		eventCh:                make(chan Event, 256),
		outputCh:               make(chan AgentEvent, 512),
		sessionDir:             sessionDir,
		modelName:              modelName,
		runningModelRef:        modelName,
		instanceID:             NextInstanceID(identity.MainAgentID),
		mcpClientInfo:          mcpClientInfo,
		done:                   make(chan struct{}),
		stoppingCh:             make(chan struct{}),
		evidence:               evidenceCandidateTracker{seen: make(map[string]struct{})},
		projectRoot:            projectRoot,
		subs:                   newSubAgentRegistry(),
		sem:                    make(chan struct{}, 10),
		fileTrack:              filelock.NewFileTracker(),
		fileBackups:            newFileBackupManager(sessionDir),
		subAgentInbox:          newSubAgentInbox(),
		ownedSubAgentMailboxes: make(map[string][]SubAgentMailboxMessage),
		subAgentMailboxIDs:     make(map[string]struct{}),
		subAgentUrgentCounts:   make(map[string]int),
		recovery:               recovery.NewRecoveryManager(sessionDir),
		persist:                newPersistencePump(256),
		cachedWorkDir:          workDir,
		gitStatusReady:         gitStatusReady,
		agentsMDReady:          make(chan struct{}),
		skillsReady:            make(chan struct{}),
		mcpReadyMu:             sync.Mutex{},
		mcpReady:               make(chan struct{}),
	}
	a.interaction = newInteractionBroker(a.stoppingCh)
	a.refreshSessionSummary()

	// Fetch git status asynchronously; callLLM will wait for it before the
	// first LLM request so the system prompt always has accurate info.
	go func() {
		a.setCachedGitStatus(getGitStatus(workDir))
		close(gitStatusReady)
	}()

	// Detect Python virtual environment synchronously (just os.Stat, cheap).
	a.cachedVenvPath = detectVenvPath(workDir, projectRoot)

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

// SetBusyPreparationHook installs a callback that runs before the main request
// surface is built for a new busy cycle. It is used by runtime resource
// managers to restore idle-unloaded dependencies before the next request.
func (a *MainAgent) SetBusyPreparationHook(fn func(context.Context) error) {
	a.busyPreparationMu.Lock()
	defer a.busyPreparationMu.Unlock()
	a.busyPreparationHook = fn
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
	if !a.sessionBuilt.Load() {
		a.refreshSystemPrompt()
	}
}

func (a *MainAgent) SetPendingMCPDiscovery(mcpTools []tools.Tool, block string) {
	a.mcpServersPromptMu.Lock()
	a.mcpServersPrompt = block
	a.pendingMCPReplace = false
	if len(mcpTools) == 0 {
		a.pendingMCPTools = nil
	} else {
		a.pendingMCPTools = append([]tools.Tool(nil), mcpTools...)
	}
	a.mcpServersPromptMu.Unlock()
	a.markMCPReady()
}

// SetRuntimeMCPDiscovery stages a full runtime MCP surface replacement for the
// next request and marks the frozen LLM-facing surface for re-evaluation.
func (a *MainAgent) SetRuntimeMCPDiscovery(mcpTools []tools.Tool, block string) {
	a.mcpServersPromptMu.Lock()
	a.mcpServersPrompt = block
	a.pendingMCPReplace = true
	if len(mcpTools) == 0 {
		a.pendingMCPTools = nil
	} else {
		a.pendingMCPTools = append([]tools.Tool(nil), mcpTools...)
	}
	a.mcpServersPromptMu.Unlock()
	a.markRuntimeSurfaceDirty()
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
		key := mainMCPServerCacheKey(name)
		if _, ok := a.mcpServerCache[key]; !ok {
			a.mcpServerCache[key] = &mcpServerEntry{} // sentinel: Mgr==nil
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
	a.mcpReadyMu.Lock()
	ch := a.mcpReady
	a.mcpReadyMu.Unlock()
	if ch == nil {
		return
	}
	select {
	case <-ch:
		// already closed
	default:
		close(ch)
	}
}

func (a *MainAgent) currentActiveConfig() *config.AgentConfig {
	a.stateMu.RLock()
	defer a.stateMu.RUnlock()
	return a.activeConfig
}

// snapshotRuleset returns a defensive copy of the main agent's merged ruleset,
// without any YOLO-mode filtering. Use this when the caller needs the same
// rules the user configured, regardless of the temporary YOLO bypass.
func (a *MainAgent) snapshotRuleset() permission.Ruleset {
	a.stateMu.RLock()
	defer a.stateMu.RUnlock()
	if len(a.ruleset) == 0 {
		return nil
	}
	return append(permission.Ruleset(nil), a.ruleset...)
}

// effectiveRuleset returns the ruleset that should drive the main agent's own
// LLM-facing surface (system prompt, tool visibility) and rule evaluation.
// Under YOLO it returns only the protected-tool rules so the visible surface
// matches what bypassPermission actually enforces. SubAgents must use
// subAgentBaseRuleset instead.
func (a *MainAgent) effectiveRuleset() permission.Ruleset {
	ruleset := a.snapshotRuleset()
	if a.yoloEnabled.Load() {
		return yoloRuleset(ruleset)
	}
	return ruleset
}

// subAgentBaseRuleset returns the unfiltered ruleset SubAgents should inherit
// when they are created or refreshed. YOLO is intentionally a main-agent-only
// relaxation; subagents continue to evaluate the user's full rule set.
func (a *MainAgent) subAgentBaseRuleset() permission.Ruleset {
	return a.snapshotRuleset()
}

// buildSubAgentRuleset returns the ruleset a freshly created or restored
// SubAgent should evaluate tool permissions against: the unfiltered main-agent
// ruleset merged with the SubAgent's own permission config.
func (a *MainAgent) buildSubAgentRuleset(agentDef *config.AgentConfig) permission.Ruleset {
	ruleset := a.subAgentBaseRuleset()
	if agentDef != nil && agentDef.Permission.Kind != 0 {
		ruleset = permission.Merge(ruleset, permission.ParsePermission(&agentDef.Permission))
	}
	return ruleset
}

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

	// Prompt and tool visibility depend on the role's permissions. Rebuild them
	// together at the next request boundary so the model sees one coherent surface.
	a.markRuntimeSurfaceDirty()
	a.NotifyEnvStatusUpdated()

	if clearHistory {
		// Clear conversation history so the new role starts fresh.
		a.ctxMgr.RestoreMessages(nil)
		a.clearEvidenceCandidates()
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

	log.Infof("switched MainAgent role role=%v clear_history=%v model_ref=%v", roleName, clearHistory, a.ProviderModelRef())
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
	if a.modelPoolPolicy != nil {
		return a.modelPoolPolicy.ResolveInitialModelRef(cfg.Name, cfg)
	}
	poolNames := cfg.PoolNames()
	if len(poolNames) == 0 {
		return ""
	}
	refs := cfg.PoolModels(poolNames[0])
	if len(refs) == 0 {
		return ""
	}
	ref := strings.TrimSpace(refs[0])
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
	log.Infof("agent shutting down instance=%v timeout=%v", a.instanceID, timeout)
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
		// Run shutdown hooks under the remaining shutdown budget rather than the
		// already-cancelled run context, so on_session_end can perform best-effort
		// cleanup without hanging process exit.
		hookCtx, cancel := context.WithTimeout(context.Background(), hookBudget)
		if _, err := a.fireHook(hookCtx, hook.OnSessionEnd, 0, map[string]any{}); err != nil {
			log.Warnf("on_session_end hook error error=%v", err)
		}
		cancel()
	}

	// Mark as shutting down so UpdateTodos stops saving snapshots (the final
	// snapshot is saved below and must not be overwritten).
	a.shuttingDown.Store(true)

	a.cancelActiveWork()
	a.closeSubAgentMCPServers()

	// Close the persistence channel and wait for the loop to drain.
	// The persist loop may be started outside Run (tests), so don't gate the wait
	// on the main event loop start flag.
	if a.persist.ch != nil {
		a.closePersistLoop()
		if wait := remaining(); wait > 0 {
			select {
			case <-a.persist.done:
			case <-time.After(wait):
				log.Warn("persist loop did not drain within shutdown budget, continuing")
			}
		} else {
			log.Warn("shutdown budget exhausted before persist loop drain")
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
			log.Warn("compaction workers did not drain within shutdown budget, continuing")
		}
	} else {
		log.Warn("shutdown budget exhausted before compaction workers drain")
	}

	// Save final snapshot and close recovery manager (flush JSONL file handles).
	if a.recovery != nil {
		if err := a.recovery.SaveSnapshot(a.buildShutdownSnapshot()); err != nil {
			log.Warnf("failed to save final recovery snapshot error=%v", err)
		}

		a.recovery.Close()
	}

	if a.sessionLock != nil {
		if err := a.sessionLock.Release(); err != nil {
			log.Warnf("failed to release session lock on shutdown error=%v", err)
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

// cancelActiveWork aborts the active turn (if any), cancels every live
// SubAgent, and stops orphaned background objects (Shell spawns, etc.). It is
// the first phase of [MainAgent.Shutdown] and runs synchronously so tool
// executions and LLM calls observe cancellation before snapshot/persist work
// begins.
func (a *MainAgent) cancelActiveWork() {
	a.turnMu.Lock()
	if a.turn != nil {
		a.turn.Cancel()
	}
	a.turnMu.Unlock()

	a.subs.mu.RLock()
	for _, sub := range a.subs.subAgents {
		tools.StopAllSpawnedForAgent(sub.instanceID, "terminated on client exit")
		sub.cancel()
	}
	a.subs.mu.RUnlock()

	if stoppedBackground := tools.StopAllSpawnedForShutdown(); stoppedBackground > 0 {
		log.Infof("terminated background objects for shutdown count=%v instance=%v", stoppedBackground, a.instanceID)
	}
}

// closeSubAgentMCPServers tears down SubAgent-exclusive MCP managers. Sentinel
// entries (Mgr==nil) point at main-agent servers which are owned by AppContext
// and closed elsewhere. Resets the cache so post-shutdown lookups fail
// explicitly.
func (a *MainAgent) closeSubAgentMCPServers() {
	a.mcpServerCacheMu.Lock()
	defer a.mcpServerCacheMu.Unlock()
	for name, entry := range a.mcpServerCache {
		if entry.Mgr != nil {
			log.Infof("closing subagent MCP server server=%v", name)
			entry.Mgr.Close()
		}
	}
	a.mcpServerCache = nil
}

// buildShutdownSnapshot collects todos, sub-agent states, and current usage
// totals into a [recovery.SessionSnapshot] suitable for the final shutdown
// snapshot.
func (a *MainAgent) buildShutdownSnapshot() *recovery.SessionSnapshot {
	return a.buildRecoverySnapshot()
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
	case message.RoleUser:
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
			item := buildEvidenceItem(
				evidenceUserCorrection,
				"User correction / constraint",
				"This explicitly constrains the next code change and should be preserved verbatim.",
				"runtime user message",
				compactTextSnippet(text, 600),
			)
			item.Sequence = a.evidence.len() + 1
			a.addEvidenceCandidate(item)
		case isPlainUserRequestForCompaction(text):
			item := buildLatestUserRequestEvidence("runtime user message", text)
			item.Sequence = a.evidence.len() + 1
			a.addEvidenceCandidate(item)
		}
	case message.RoleTool:
		if reason, ok := extractDoneRejectedReason(text); ok {
			item := buildDoneRejectedEvidence("runtime tool result", reason)
			item.Sequence = a.evidence.len() + 1
			a.addEvidenceCandidate(item)
		} else if reason, ok := extractToolRejectedByUserReason(text); ok && isPlainUserRequestForCompaction(reason) {
			item := buildLatestUserRequestEvidence("runtime tool rejection reason", reason)
			item.Sequence = a.evidence.len() + 1
			a.addEvidenceCandidate(item)
		}
		if isToolResultErrorMessage(msg) {
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

// SupportsInput reports whether the active main-agent model accepts the given
// input modality (e.g. "image", "pdf").
func (a *MainAgent) SupportsInput(modality string) bool {
	if a == nil {
		return false
	}
	a.llmMu.RLock()
	client := a.llmClient
	a.llmMu.RUnlock()
	return client != nil && client.SupportsInput(modality)
}

// SupportsViewImageTool reports whether the stable primary model for this agent
// can expose view_image. It intentionally follows the model-pool primary rather
// than the current fallback cursor so the tool surface does not change as
// fallback routing moves between candidates.
func (a *MainAgent) SupportsViewImageTool() bool {
	if a == nil {
		return false
	}
	a.llmMu.RLock()
	client := a.llmClient
	a.llmMu.RUnlock()
	return client != nil && client.PrimarySupportsViewImageTool()
}

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
		case message.ContentPartImage:
			if !client.SupportsInput("image") {
				dropped = append(dropped, "image")
				continue
			}
		case message.ContentPartPDF:
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
		if p.Type != message.ContentPartText {
			allText = false
			break
		}
	}
	if allText && len(filtered) == 1 {
		return filtered[0].Text, nil
	}
	return content, filtered
}

// handleLocalOnlySlashCommands runs local-only slash commands that must never
// be appended to the conversation or sent to the model. Returns true if
// handled. busy reports whether an active turn is in flight (a.turn != nil)
// so handlers can avoid clearing turn state mid-retry. Runs even when the
// agent is busy (not queued), including when the submitted message carries
// image parts.
func (a *MainAgent) handleLocalOnlySlashCommands(content string, parts []message.ContentPart, busy bool) bool {
	return a.executeLocalOnlySlashCommand(content, parts, busy)
}

// processPendingUserMessagesBeforeLLMInTurn appends queued user messages to the
// conversation so the next LLM call sees tool results and user input together.
// Slash commands that require idle (/loop*, /resume*, /new, /mcp*) are left on the
// queue for the next idle drain. /compact is local-only and schedules background
// compaction immediately, even while a turn is active.
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
	manualInputConsumed := false
	for _, p := range pending {
		content := pendingUserMessageText(p)
		c := strings.TrimSpace(content)
		if c == "/resume" || strings.HasPrefix(c, "/resume ") || c == "/new" || c == "/mcp" || strings.HasPrefix(c, "/mcp ") || isLoopSlashCommand(c) {
			deferred = append(deferred, p)
			continue
		}
		m, ok := a.pendingUserMessageToConversationMessage(p)
		if !ok {
			continue
		}
		batch = append(batch, m)
		consumed = append(consumed, consumedPendingDraft{draftID: p.DraftID, msg: m})
		manualInputConsumed = manualInputConsumed || p.FromUser
	}
	// Re-queue /resume* and anything that arrived concurrently (should be rare).
	a.pendingUserMessages = append(deferred, a.pendingUserMessages...)
	if len(batch) == 0 {
		return
	}
	if manualInputConsumed {
		a.stageNextSubAgentMailboxBatch()
	}
	log.Debugf("injecting pending user messages with tool results count=%v", len(batch))
	for _, item := range consumed {
		a.ctxMgr.Append(item.msg)
		a.recordEvidenceFromMessage(item.msg)
		if a.recovery != nil {
			a.persistAsync(identity.MainAgentID, item.msg)
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
			if part.Type == message.ContentPartText {
				content += part.Text
			}
		}
	default:
		log.Errorf("handleUserMessage: invalid payload type payload_type=%v", fmt.Sprintf("%T", evt.Payload))
		return
	}

	log.Debugf("handling user message content_len=%v", len(content))

	// /export, /models, /tier, and /compact are local-only: never queue or send to the model.
	// Pass busy = (a.turn != nil) so handlers skip setIdleAndDrainPending and
	// don't clobber the active turn while it's mid-retry.
	if a.handleLocalOnlySlashCommands(content, parts, a.turn != nil) {
		return
	}
	a.mailboxDeliveryPaused.Store(false)

	a.explicitUserTurnCount++
	a.sweepSubAgentLifecycle()

	trimmedContent := strings.TrimSpace(content)
	isMCPCommand := trimmedContent == "/mcp" || strings.HasPrefix(trimmedContent, "/mcp ")

	// When busy (turn != nil) or an MCP transition is in flight, queue the message;
	// it will be drained and sent in one batch when idle.
	if a.turn != nil || a.mcpTransitionActive.Load() {
		if a.turn != nil {
			if a.tryHandleBusySlashCommand(content) {
				return
			}
		}
		if a.mcpTransitionActive.Load() && isMCPCommand {
			a.emitToTUI(ToastEvent{Message: "MCP change already in progress", Level: "warn"})
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
	a.stageNextSubAgentMailboxBatch()
	a.newTurn()
	turnID := a.turn.ID
	turnCtx := a.turn.Ctx

	outC, outP := a.expandSlashCommandForModel(content, parts)
	outC, outP = a.filterUnsupportedParts(outC, outP)
	userMsg := message.Message{
		Role:    message.RoleUser,
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
		log.Errorf("handlePendingDraftUpsert: invalid payload type payload_type=%v", fmt.Sprintf("%T", evt.Payload))
		return
	}
	pending.DraftID = strings.TrimSpace(pending.DraftID)
	if pending.DraftID == "" {
		return
	}
	pending.Parts = pendingDraftParts(pending)
	pending.Content = pendingUserMessageText(pending)

	if a.turn != nil || a.mcpTransitionActive.Load() {
		if a.turn != nil {
			if a.tryHandleBusySlashCommand(pending.Content) {
				return
			}
		}
		a.pendingUserMessages = enqueuePendingUserMessage(a.pendingUserMessages, pending)
		return
	}

	content := pendingUserMessageText(pending)
	if a.handleLocalOnlySlashCommands(content, pending.Parts, false) {
		return
	}
	if a.tryHandleSlashCommand(content) {
		return
	}
	userMsg, ok := a.pendingUserMessageToConversationMessage(pending)
	if !ok {
		return
	}

	a.mailboxDeliveryPaused.Store(false)
	a.stageNextSubAgentMailboxBatch()
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
		log.Errorf("handlePendingDraftRemove: invalid payload type payload_type=%v", fmt.Sprintf("%T", evt.Payload))
		return
	}
	draftID = strings.TrimSpace(draftID)
	if draftID == "" {
		return
	}
	var removed bool
	a.pendingUserMessages, removed = removePendingDraft(a.pendingUserMessages, draftID)
	if !removed {
		log.Debugf("pending mirrored draft already absent draft_id=%v", draftID)
	}
}

func (a *MainAgent) handleAppendContext(evt Event) {
	var msg message.Message
	switch p := evt.Payload.(type) {
	case message.Message:
		msg = p
	case string:
		msg = message.Message{Role: message.RoleUser, Content: p}
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
		a.persistAsync(identity.MainAgentID, persistMsg)
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
		log.Debugf("discarding stale turn cancellation event_turn=%v current_turn=%v", evt.TurnID, a.currentTurnID())
		return
	}

	payload, ok := evt.Payload.(*TurnCancelledPayload)
	if !ok {
		log.Errorf("handleTurnCancelled: invalid payload type payload_type=%v", fmt.Sprintf("%T", evt.Payload))
		return
	}
	if payload == nil {
		return
	}

	a.mainLLMRequestInFlight.Store(false)
	a.savePartialAssistantMsg()
	if payload.KeepPendingUserMessagesQueued {
		a.pausePendingUserDrainOnce = true
	}

	// Extract completed speculative tool results before marking as failed
	var completedResults map[string]*ToolResultPayload
	if a.turn != nil && a.turn.streamingToolExec != nil {
		completedResults = a.turn.streamingToolExec.DrainCompletedResults()
	}

	// Separate tools into completed vs truly cancelled
	var reallyCancelled []PendingToolCall
	completedCount := 0
	for _, call := range a.turn.filterCompletedToolCalls(payload.Calls) {
		if result, ok := completedResults[call.CallID]; ok {
			if a.handleCompletedInterruptedToolResult(call, result, "not_in_context") {
				completedCount++
			}
		} else {
			// Tool was truly cancelled
			reallyCancelled = append(reallyCancelled, call)
		}
	}

	status := ToolResultStatusCancelled
	if payload.MarkToolCallsFailed {
		status = ToolResultStatusError
	}

	if len(reallyCancelled) > 0 {
		persistedResults := finalizeInterruptedToolCalls(a.ctxMgr, a.emitToTUI, a.persistInterruptedToolResults, reallyCancelled, status, context.Canceled)
		if persistedResults > 0 {
			log.Infof("persisted interrupted tool-call results after cancellation turn_id=%v interrupted=%v completed=%v", evt.TurnID, persistedResults, completedCount)
		}
	} else if completedCount > 0 {
		log.Infof("preserved completed tool results after cancellation turn_id=%v completed=%v", evt.TurnID, completedCount)
	}

	if payload.CommitPendingUserMessagesWithoutTurn {
		a.commitPendingUserMessagesWithoutTurn()
	}
	a.applyPendingModelPoolSwitchesAtRequestBoundary()
	a.emitActivity(identity.MainAgentID, ActivityIdle, "")
	a.markActiveSubAgentMailboxAck(false)
	a.setIdleAndDrainPending()
}

func (a *MainAgent) resumeTurnAfterRoutingInvalidation(turnID uint64) bool {
	if a.turn == nil || turnID == 0 || a.turn.ID != turnID {
		return false
	}
	a.processPendingUserMessagesBeforeLLMInTurn()
	a.syncBugTriagePromptFromSnapshot()
	turnCtx := a.turn.Ctx
	a.beginMainLLMAfterPreparation(turnCtx, turnID, "")
	return true
}

// handleAgentError emits the error to the TUI and logs it. An IdleEvent is
// also sent so the TUI knows the agent is ready for new input.
//
// If SourceID is "main" or empty, this is the MainAgent's own error.
// If SourceID identifies a SubAgent, its active resources are released and the
// failed instance is retained so the user can explicitly continue it later.
func (a *MainAgent) handleAgentError(evt Event) {
	err, ok := evt.Payload.(error)
	if !ok {
		log.Errorf("handleAgentError: invalid payload type payload_type=%v", fmt.Sprintf("%T", evt.Payload))
		return
	}

	// Guard: SourceID "main" or empty means MainAgent's own LLM/tool error —
	// no SubAgent to clean up.
	if evt.SourceID == identity.MainAgentID || evt.SourceID == "" {
		// Turn isolation: discard errors from cancelled/stale turns.
		if a.turn != nil && evt.TurnID != 0 && evt.TurnID != a.turn.ID {
			log.Debugf("discarding stale error event_turn=%v current_turn=%v", evt.TurnID, a.currentTurnID())
			return
		}
		a.mainLLMRequestInFlight.Store(false)

		if llm.IsRoutingInvalidated(err) {
			log.Infof("routing invalidated during active turn; restarting request turn_id=%v instance=%v", evt.TurnID, a.instanceID)
			a.applyPendingModelPoolSwitchesAtRequestBoundary()
			if a.resumeTurnAfterRoutingInvalidation(evt.TurnID) {
				return
			}
		}

		log.Errorf("agent error error=%v turn_id=%v instance=%v", err, evt.TurnID, a.instanceID)
		a.savePartialAssistantMsg()
		a.failPendingToolCalls(a.turn, err)
		a.applyPendingModelPoolSwitchesAtRequestBoundary()
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
	log.Errorf("SubAgent error error=%v source=%v", err, evt.SourceID)

	var emitCalls, persistCalls, discardCalls []PendingToolCall
	a.subs.mu.RLock()
	sub := a.subs.subAgents[evt.SourceID]
	a.subs.mu.RUnlock()
	if sub != nil {
		emitCalls, persistCalls = sub.drainPendingToolFailureSets(err)
		emitCalls, discardCalls = splitPendingCallsByDeclaredTools(sub.ctxMgr, emitCalls)
	}
	if len(persistCalls) > 0 && sub != nil {
		persistedResults := sub.persistInterruptedToolResults(persistCalls, ToolResultStatusError, err)
		if persistedResults > 0 {
			log.Infof("persisted failed sub-agent tool-call results after terminal error agent=%v count=%v", evt.SourceID, persistedResults)
		}
	}
	if len(emitCalls) > 0 {
		emitFailedToolResults(a.emitToTUI, emitCalls, err)
		a.emitActivity(evt.SourceID, ActivityIdle, "")
	}
	if len(discardCalls) > 0 {
		emitToolCallDiscards(a.emitToTUI, discardCalls, "not_in_context")
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

	log.Infof("starting plan execution plan_path=%v", planPath)

	newSessionDir, err := a.createRuntimeSessionDir()
	if err != nil {
		log.Warnf("failed to create session dir for plan execution error=%v", err)
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
			log.Warnf("execution session: failed to release old session lock error=%v", releaseErr)
		}
	}
	a.sessionLock = newLock
	a.resetSessionRuntimeState()
	a.installSessionTarget(newSessionDir)
	a.llmClient.SetSessionID(filepath.Base(newSessionDir))

	// Freeze the new session's tool/system surface before installing the
	// execution-specific prompt so the first execution request sees a stable
	// session configuration.
	if err := a.ensureSessionBuilt(turnCtx); err != nil {
		a.emitToTUI(ErrorEvent{Err: fmt.Errorf("prepare execution session: %w", err)})
		a.setIdleAndDrainPending()
		return
	}
	execPrompt := a.buildExecuteSystemPrompt(planPath)
	a.setSystemPromptOverride(execPrompt)

	// Notify TUI to wipe the viewport so planner-phase messages are cleared.
	a.emitToTUI(SessionRestoredEvent{})

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

	return sb.String()
}

// extractToolArgument returns the string used for permission pattern matching.
//
// For Shell the full command string is used (e.g. "git push origin main").
// For file tools (Read/Write/Edit) the path argument is extracted so that
// path-based rules like `Write: { "/etc/*": deny }` work correctly.
// For search tools (Grep/Glob) the pattern argument is extracted.
// All other tools fall back to "*" (whole-tool match).
// ---------------------------------------------------------------------------

// ReloadAgentsMD reloads project AGENTS.md from disk and marks the startup
// gate (agentsMDReady) so ensureSessionBuilt can proceed. The content is
// consumed the next time ensureSessionBuilt rebuilds the session-context
// reminder (on session-head events). Mid-session edits to AGENTS.md are not
// picked up until the next /new, /resume, or equivalent reset; AGENTS.md is
// treated as a session-scope snapshot.
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
		Role:    message.RoleSystem,
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
		log.Errorf("handleExecutePlanEvent: invalid payload type payload_type=%v", fmt.Sprintf("%T", evt.Payload))
		return
	}
	a.startPlanExecution(p.PlanPath, p.AgentName)
}
