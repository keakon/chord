package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
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
	"github.com/keakon/chord/internal/memory"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/ratelimit"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/skill"
	"github.com/keakon/chord/internal/thinkingtranslate"
	"github.com/keakon/chord/internal/tools"
)

const (
	defaultEventOverflowLimit = 4096
	defaultLoopEventLimit     = 256
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
	ID    uint64
	Epoch uint64
	// LLMResponsesState is the turn-scoped sticky-routing state for the
	// Responses/Codex transport. It is created when a turn starts, shared by
	// every LLM request in that turn (main, compaction, replay), and dropped
	// when the turn ends so a new user turn starts with clean state.
	LLMResponsesState *llm.ResponsesTurnState
	Ctx               context.Context
	Cancel            context.CancelFunc
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
	// partialResponsesOutput accumulates finalized reasoning items streamed
	// during the current LLM round so they can be saved with the partial
	// assistant message if the stream is interrupted before
	// response.completed. Without it the persisted message lacks its
	// preceding reasoning item and the next request fails the Responses API
	// pairing constraint (400). Protected by partialResponsesOutputMu because
	// the stream callback runs on a separate goroutine from the event loop,
	// like partialText.
	partialResponsesOutputMu sync.Mutex
	partialResponsesOutput   []message.ResponsesOutputItem
	// SubAgent terminal recovery is intentionally bounded to one additional
	// request so a text-only reply that never calls a coordination tool cannot
	// spin forever.
	SubAgentTerminalRecoveryCount int
	// SubAgentCompletionRecoveryCount bounds the follow-up for a rejected
	// Complete call (invalid arguments) to one request.
	// It is separate from SubAgentTerminalRecoveryCount so a text-only reply
	// that already spent the wrap-up nudge does not consume the model's one
	// chance to repair a malformed Complete, and a rejected Complete does not
	// eat the wrap-up nudge either.
	SubAgentCompletionRecoveryCount int
	// Resuming a preserved stream interruption has its own budget: it is a
	// transport failure, not a model that refuses to finish, and the client
	// already paces each restart behind a credential cooldown. Sharing the
	// terminal budget meant one network blip spent the wrap-up nudge, and a
	// second blip dropped the text the reply had already produced.
	SubAgentStreamResumeCount    int
	SubAgentContextRecoveryCount int
	// MalformedCount tracks consecutive LLM rounds where tool calls had
	// abnormal arguments — either the malformed sentinel (invalid JSON) or
	// empty "{}" for tools with required parameters (output truncation).
	// When this reaches maxMalformedToolCalls the turn is aborted.
	MalformedCount int
	// notifyProtocolStreak counts consecutive notify response-protocol
	// failures (missing/unknown/mismatched response correlation) per target
	// task in this turn. The second failure appends corrective guidance; the
	// third pauses the turn's automatic retry for an explicit correction (see
	// notify_protocol_guard.go). Event-loop-goroutine only, like
	// MalformedCount; a fresh turn starts empty (turn/session reset).
	notifyProtocolStreak    map[string]int
	notifyProtocolPause     bool
	notifyProtocolPauseTask string
	// BarrierFailureRounds tracks consecutive LLM rounds in this turn whose
	// tool dispatch was blocked by an intent-barrier persistence failure. The
	// first failure is fed back to the model as not_started tool errors; when
	// it reaches maxIntentBarrierFailureRounds the turn is aborted instead of
	// burning further LLM rounds against a broken write path. Reset to zero by
	// every successful barrier. Event-loop-goroutine only, like MalformedCount.
	BarrierFailureRounds               int
	LengthRecoveryCount                int
	thinkingReplayAttempted            bool
	InLengthRecovery                   bool
	LastTruncatedToolName              string
	LengthRecoveryAutoCompactAttempted bool
	OversizeRecoveryCount              int
	// Efficiency tracks per-turn tool usage patterns for one-shot efficiency
	// notes appended to tool results (see tool_efficiency_advisor.go).
	// Event-loop-goroutine only, like MalformedCount.
	Efficiency            toolEfficiencyState
	malformedInBatch      int // abnormal calls in the current LLM-response batch
	CompletedToolCalls    []any
	ChangedFiles          []any
	toolExecutionBatches  []toolExecutionBatch
	nextToolBatch         int
	activeToolBatchCancel context.CancelFunc
	streamingToolExec     *StreamingToolExecutor
}

// PendingToolCall records the minimal metadata needed to close a pending tool
// card when a turn is cancelled before a normal ToolResultEvent arrives.
type PendingToolCall struct {
	CallID   string
	Name     string
	ArgsJSON string
	// InputText accumulates a freeform tool input verbatim (Responses custom
	// apply_patch deltas). ArgsJSON holds the canonical {patch} object built
	// from it; InputText is kept for raw-text TUI preview rendering.
	InputText string
	AgentID   string
	Audit     *message.ToolArgsAudit

	// inputArgsStale marks ArgsJSON as older than InputText: another freeform
	// fragment arrived since the canonical {patch} envelope was last built.
	// The envelope is only consumed once the arguments are complete
	// (speculative validation, finalize, execution), so streaming marks it
	// stale instead of re-serializing the whole patch on every fragment —
	// that is O(patch²) over a streamed patch. Readers call
	// materializeStreamingToolCallArgsLocked to rebuild it.
	inputArgsStale bool
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
	OwnerAgentID     string
	OwnerTaskID      string
	Depth            int
	AgentDefName     string
	TaskDesc         string
	ModelName        string
	Persistence      PersistenceHealth
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
	Kind                string
	FromUser            bool
	MailboxAckID        string
	Mailbox             *message.MailboxMetadata
	CoalesceKey         string
	DrainContextAppends bool
}

type requestBatchState struct {
	mu           sync.Mutex
	sessionEpoch uint64
	sequence     uint64
}

func (s *requestBatchState) reserve(sessionEpoch, historyMax uint64) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessionEpoch != sessionEpoch {
		s.sessionEpoch = sessionEpoch
		s.sequence = historyMax
	} else if s.sequence < historyMax {
		s.sequence = historyMax
	}
	s.sequence++
	return s.sequence
}

func (s *requestBatchState) rollback(sessionEpoch, batch uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessionEpoch == sessionEpoch && s.sequence == batch {
		s.sequence--
	}
}

func (s *requestBatchState) current(sessionEpoch uint64) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessionEpoch != sessionEpoch {
		return 0
	}
	return s.sequence
}

func maxRequestBatch(messages []message.Message) uint64 {
	var maximum uint64
	for _, msg := range messages {
		maximum = max(maximum, msg.RequestBatch)
	}
	return maximum
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
	walltime               *walltimeRecorder
	unsupportedPartToast   toastGate
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
	// startupSkippedLockedSessions names the sessions --continue passed over
	// at startup because another live Chord process owned them. The agent
	// reports them once as a toast when the event loop starts so a fallback is
	// never silent.
	startupSkippedLockedSessions []string
	// startupConfigIssues names the config-file problems the tolerant loader
	// logged and treated as not configured at startup. The agent reports them
	// once as a toast when the event loop starts so silently dropped values
	// stay visible, pointing at `chord doctor config` for the full report.
	startupConfigIssues []string

	// Permission system: ruleset from active agent config with overlay support.
	globalConfig  *config.Config
	projectConfig *config.Config
	ruleset       permission.Ruleset  // merged ruleset (base + overlays)
	overlay       *permission.Overlay // layered permission rules
	// YOLO bypasses ordinary tools' permission checks entirely. The
	// mechanism control tools delegate/handoff/cancel stay rule-governed
	// (their deny rules still reject, and their ask relaxes to allow); done
	// and compact_context keep their dedicated actions.
	yoloEnabled atomic.Bool

	// confirmFn is called when permission evaluates to "ask". It must be set
	// before Run whenever the active ruleset can yield ActionAsk.
	confirmFn ConfirmFunc

	// Internal event bus. Goroutines that perform async work (LLM calls,
	// tool execution) send results here; the single-threaded Run loop
	// processes them in order.
	eventCh            chan Event
	eventWakeCh        chan struct{}
	eventSpaceCh       chan struct{}
	eventMu            sync.Mutex
	deferredEvents     []Event
	loopEvents         []Event
	eventOverflowPeak  atomic.Uint64
	eventCoalesced     atomic.Uint64
	eventBackpressure  atomic.Uint64
	eventOverflowLimit int
	loopEventLimit     int

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
	stoppingOnce                  sync.Once

	turn           *Turn
	nextTurnID     uint64
	turnEpoch      uint64
	eventSeq       atomic.Uint64
	requestBatches requestBatchState
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

	// shellReadMemo caches externallyInvalidatedReadsAfterMutatingShell verdicts
	// keyed by the triggering shell result, so the reduction pass hashes each
	// historically read file once per completed shell rather than once per LLM
	// request. Tracked write/edit/patch/delete invalidation stays with
	// analyzeReadValidity and is unaffected by this memo.
	shellReadMemo shellReadInvalidationMemo

	// lazyReadMemo caches the disk-verification verdict for every historical
	// current read (path + expected hash), regardless of whether a mutating
	// shell triggered the check. The cached stat (mtime/size) is compared on
	// every request, so the content hash is only recomputed when the file
	// actually changed on disk: a lazy check that catches external edits to a
	// file the model still trusts, with no filesystem watcher.
	lazyReadMemo externalReadLazyMemo

	// shellReadOnlyClass caches per-ToolCallID read-only classification of
	// shell commands for the reduction pass (see shellReadOnlyClassMemo).
	shellReadOnlyClass shellReadOnlyClassMemo

	// persistenceHealth tracks the durability of the main transcript writes.
	// Degraded means writes are failing; the intent barrier then blocks tool
	// dispatch while Q&A turns keep working until a checkpoint recovers.
	persistenceHealth agentPersistenceHealth

	// loopReductionMu protects request-shape snapshots, reduction stats, and
	// loopState fields that may be read by callLLM on a worker goroutine while
	// the event loop handles a busy /loop command.
	loopReductionMu               sync.Mutex
	lastPreparedLLMTurnID         uint64
	lastPreparedLLMRequestShape   []stableReductionMessageShape
	lastPreparedLLMRequestPrefix  []message.Message
	lastPreparedLLMReducedIndices []bool
	// lastPreparedLLMDiscardedInputs is session-scoped recall evidence: input
	// key -> ToolCallID of the call whose output was summarized away,
	// excluding repeated-collapse. Dropped with the reduction caches on
	// restore or model switch.
	lastPreparedLLMDiscardedInputs map[string]string
	lastPreparedLLMNextReviewAge   []int
	lastPreparedLLMToolResults     int
	lastPreparedReductionPolicy    contextReductionPolicy
	lastPreparedReductionStats     ContextReductionStats
	lastPreparedLLMToolDefHash     [sha256.Size]byte
	lastPreparedStablePrefixLen    int
	// lastPreparedLLMShapeSource holds shallow struct copies of the original
	// messages lastPreparedLLMRequestShape was computed from. It lets shape
	// compatibility checks use direct field equality (O(1) per unchanged
	// message thanks to shared string backing) instead of re-hashing every
	// message's content on each request. Read-only after store.
	lastPreparedLLMShapeSource              []message.Message
	wrapUpGraceTurnID                       uint64
	wrapUpGraceRemaining                    int
	stageCompletionCandidateTurnID          uint64
	stageCompletionCandidatePending         bool
	stageCompletionCandidatePromptDelivered bool
	contextReductionStats                   ContextReductionStats
	// retentionSignals aggregates per-request retention signals (rereads,
	// archive reads, evidence validity) into the current compaction-window
	// totals. It survives resetContextReductionStats on compaction applies —
	// an apply publishes the window (takeWindowRetentionSignals) and only a
	// session boundary clears it. Guarded by loopReductionMu.
	retentionSignals       retentionWindowTotals
	lastLLMRequestModelRef string
	llmModelRunLength      int

	// fallbackSurfaceRebuilds / fallbackSurfaceReuses count how fallback
	// boundaries resolved the "may this prepared surface go to another model"
	// question. Session-scoped counters, read for telemetry only.
	fallbackSurfaceRebuilds atomic.Int64
	fallbackSurfaceReuses   atomic.Int64

	// recalledReductionInputs remembers tool-input keys (normalized tool + raw args)
	// whose reduced output the model later re-fetched with an identical call —
	// direct evidence that reduction discarded content the model still needed.
	// The newest output of a recalled input is exempt from reduction for the
	// rest of the session. Derived, in-memory, session-scoped state: cleared
	// with the reduction caches and simply absent after restore. Guarded by
	// loopReductionMu.
	recalledReductionInputs map[string]struct{}

	// cacheExpectMu protects per-ref request fingerprints used to attribute
	// prompt-cache misses (chord-side prefix mutation vs provider-side loss).
	cacheExpectMu     sync.Mutex
	cacheExpectations map[string]*cacheExpectationRecord
	cacheHitTracker   *cacheHitTracker

	// toolDefHashMemo caches the tool-definition hash keyed by the frozen
	// tool-definition snapshot pointer, so per-request surface checks do not
	// re-marshal and re-hash every tool schema.
	toolDefHashMemo atomic.Pointer[toolDefHashMemoEntry]

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
	realWorkEpoch       atomic.Uint64
	lastIdleWorkEpoch   uint64
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
	mainLLMRequestInFlight atomic.Bool
	// mainSlotForeground tracks whether the shared main activity slot currently
	// shows a live foreground request/tool state. Compaction activity emissions
	// consult it so heartbeats never clobber visible main-model progress. It is
	// released by the event loop after the request result is handled, not by the
	// goroutine that only finished reading the provider response.
	mainSlotForeground atomic.Bool
	// compactionSlotActive is the cross-goroutine display-only view of the
	// compaction lifecycle. The detailed compaction state remains event-loop
	// owned; activity heartbeats and IsCompactionRunning use this atomic view so
	// they do not read that state concurrently.
	compactionSlotActive        atomic.Bool
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
	projectRoot    string
	pathLocator    *config.PathLocator // resolved startup paths; nil falls back to DefaultPathLocator
	lastPlanPath   string
	pendingHandoff *HandoffResult // deferred Handoff action; processed after all sibling tools finish
	// pendingModelDriven is the armed-but-not-yet-barriered compact_context
	// checkpoint request (the payload form of the accepted proposal). Armed by
	// handleToolResult after control-plane validation and consumed at the
	// tool-batch barrier. The proposal's lifecycle record — identity, status,
	// runtime-owned reason/time and audit args — lives in modelDrivenProposal
	// below and survives past this armed payload (see compaction_proposal.go).
	pendingModelDriven *modelDrivenCheckpointRequest
	// modelDrivenProposal is the single event-loop-owned lifecycle record of
	// the most recent model-driven compact_context attempt, from acceptance
	// through its terminal settle. All mutations funnel through
	// armModelDrivenProposal / transitionModelDrivenProposal, which persist
	// the recovery snapshot after every change.
	modelDrivenProposal modelDrivenProposalState
	// modelDrivenSkipNotice carries the low-gain skip reason from the worker
	// settle to the continuation, which surfaces it as a transient notice.
	modelDrivenSkipNotice string
	// pendingModelDrivenNotice is a one-shot transient turn overlay that tells
	// the model a model-driven checkpoint did not apply (skip/failure/cancel)
	// and why. It is consumed by buildTurnOverlayMessages and never persisted
	// to ctxMgr.
	pendingModelDrivenNotice string
	// lastModelDrivenApplyBatch is the main request batch of the last
	// successful model-driven apply, using currentRequestBatch semantics (the
	// request-batch counter, persisted with the recovery snapshot). The next
	// model-driven apply must wait minModelDrivenApplyIntervalBatches requests
	// after it. Session switch clears it so a fresh session starts
	// unthrottled; restore keeps the persisted value (the current > last
	// guard prevents uint64 underflow on resumed sessions).
	lastModelDrivenApplyBatch uint64
	// lastModelDrivenSkipBatch and lastModelDrivenSkipReason record the most
	// recent low-gain / interval skip for the same-reason skip cooldown: a
	// retry within minModelDrivenSkipCooldownBatches of the same reason
	// short-circuits without re-running preflight. Reasons are
	// "low_gain"/"interval" (see compaction_model_driven.go); a cooldown skip
	// propagates the reason it was bound to.
	lastModelDrivenSkipBatch  uint64
	lastModelDrivenSkipReason string
	// compactionGraceStartBatch / compactionGraceExhausted are the threshold
	// grace period (compaction_grace.go): the request batch at which the
	// usage-driven crossing was first deferred, and whether this compaction
	// window already spent its grace. Runtime memory only; a durable apply,
	// a session switch, or a model change clears all three.
	// compactionGraceActive tracks "a grace is running" separately because
	// batch 0 is a legitimate start batch: before any request batch has been
	// reserved in this window the gate reads 0, so a zero start batch cannot
	// double as "not started".
	compactionGraceStartBatch uint64
	compactionGraceActive     bool
	compactionGraceExhausted  bool
	// modelDrivenCompactionEnabled reflects the effective
	// context.compaction.model_driven configuration. It is set once at runtime
	// wiring; the capability is decided at construction, never toggled at
	// runtime.
	modelDrivenCompactionEnabled atomic.Bool
	// modelDrivenDenyDiagnosticOnce gates the one-time diagnostic reported
	// when model_driven is enabled but the permission rules explicitly deny
	// compact_context (the model-driven checkpoint feature is then
	// unavailable).
	modelDrivenDenyDiagnosticOnce sync.Once
	// autoCompactRequestGeneration is a monotonic id for each usage-driven
	// auto-compact request instance. It increments when the request is armed
	// (false->true) and is never reset by apply/skip/clear; it binds the
	// usage-driven externalization warning claim (auto_compact_request_id).
	autoCompactRequestGeneration atomic.Uint64
	// pendingContextPressureReminder is the turn-tail context-pressure
	// reminder overlay (context_overlays.go). It is sticky (optimization
	// 2.9): beginMainLLMAfterPreparation re-queues it for every request while
	// usage stays above the reminder line — the full text once per compaction
	// window, then a one-line short text — until the model calls
	// compact_context in the window, the usage drops back below the line, or a
	// durable apply / session switch / model change starts a fresh window. The
	// per-request queue + per-attach consume cycle keeps the field scoped to
	// one request; the claim's delivered flag is confirmed at dispatch, not at
	// attach. pendingCompactionWarning is the one-shot usage-driven
	// externalization warning (once per auto-compact request generation).
	pendingContextPressureReminder string
	pendingCompactionWarning       string
	// pendingCompactionImminent is the grace-period "compaction imminent"
	// notice. It is re-queued on every request inside the threshold grace
	// window with the true remaining countdown (compaction_grace.go).
	pendingCompactionImminent string
	// overlayClaims holds the per-window context-pressure reminder claim and
	// the per-generation externalization warning claim. Cross-goroutine: the
	// event loop queues, the main LLM goroutine confirms delivery at dispatch.
	overlayClaims overlayClaimState
	// pendingContextNotices carries the text of the context overlays attached
	// to the request being assembled so the dispatch confirmation point can
	// surface them to the user as cards (see ContextNoticeEvent). Request
	// assembly runs on the event loop and dispatch confirmation on the main
	// LLM goroutine, so the hand-off is mutex-guarded. The stash is reset at
	// the start of every assembly: a request cancelled before dispatch never
	// reaches the confirmation point, and its stashed notices must not leak
	// into the next request.
	pendingContextNotices   []contextNotice
	pendingContextNoticesMu sync.Mutex
	// compactionWindowGeneration is the in-memory monotonic compaction-window
	// id used by the reminder-class overlay claims: every durable apply
	// increments it, and a session switch / restore resets it to 0. It
	// replaces the on-disk history file count as the window key so the key
	// only changes when a checkpoint actually applies — a usage-driven worker
	// writes its history file before the summary model runs, which would
	// rotate the key mid-window and re-arm (or permanently suppress) the
	// reminder for requests racing that window. Event-loop owned: queued,
	// advanced, and reset only on the event loop, so it needs no lock.
	compactionWindowGeneration uint64
	// compactionIndexAllocs hands out history file indexes without re-scanning
	// the disk. They are keyed by captured session directory so a worker that
	// outlives a session switch cannot allocate from the new live session.
	// The map mutex protects lookup/creation; each allocator serializes its
	// own directory's worker allocations.
	compactionIndexAllocsMu sync.Mutex
	compactionIndexAllocs   map[string]*compactionIndexAllocator
	// recoverySnapshotMu serializes building + writing one recovery snapshot
	// (snapshot.json). Writers run on several goroutines — the event loop's
	// apply / TodoWrite / session-freeze paths and SubAgent persistence
	// callbacks — and SaveSnapshot replaces the whole file, so a build that
	// started before a newer state update could otherwise finish after that
	// update's write and clobber it with stale contents. Holding the lock
	// across build + write makes the last writer's snapshot reflect the
	// freshest state.
	recoverySnapshotMu sync.Mutex
	// recoveryOwner holds the live recovery manager together with the session
	// identity it writes for; see recovery_owner.go. Every read goes through
	// recoveryManager / recoveryManagerForEpoch and every replacement through
	// installRecoveryManager / clearRecoveryManagerIf, because sub-agent and
	// tool goroutines persist through it while the event loop swaps sessions.
	recoveryOwnerMu sync.RWMutex
	recoveryOwner   *recoveryOwnership
	// appliedCompactionModelRef records the model reference whose per-model
	// compaction threshold is currently applied to ctxmgr. A change re-applies
	// the threshold; not persisted, so after a restore the threshold is
	// re-applied at the first request boundary.
	appliedCompactionModelRef string
	// lastCompactionMessageCount is the compacted message count at the previous
	// compaction apply, used to report the interval (in messages) since the
	// last compaction in lifecycle analytics.
	lastCompactionMessageCount int
	pendingLoopExitResults     []*loopExitResult

	// Role system: MainAgent operates as one of several roles (builder, planner, etc.).
	activeConfig *config.AgentConfig            // currently active role (nil = no role set yet; defaults to builder)
	agentConfigs map[string]*config.AgentConfig // pre-loaded: built-in → global → project (highest priority)

	// Multi-agent orchestration. subs owns the live sub-agent maps and the
	// RWMutex that guards them (formerly inline MainAgent fields mu/subAgents/
	// taskRecords/subAgentStateEnteredTurn).
	subs                     subAgentRegistry
	orchestrationMetrics     orchestrationRuntimeMetrics
	governor                 *resourceGovernor
	waitingMainExpiry        waitingMainExpiryPolicy // resolved once; see waitingMainExpiryPolicy()
	admissionMu              sync.Mutex
	admissionEpoch           atomic.Uint64
	admissionPaused          atomic.Bool
	subAgentMetaPersistMu    sync.Mutex
	taskRegistryPersistMu    sync.Mutex
	settlementJournalMu      sync.Mutex
	agentRequestPersistMu    sync.Mutex
	taskRegistryPersistHook  func()                   // test-only barrier after snapshot, before durable write
	rehydrateCommitHook      func()                   // test-only barrier between rehydrate attempt decision and final commit
	terminalCommitGuardHook  func()                   // test-only barrier between settlement journal append and runtime CAS
	sem                      chan struct{}            // compatibility view of governor normal runtime slots
	fileTrack                *filelock.FileTracker    // file write conflict detection
	fileBackups              *fileBackupManager       // session-scoped risky write backups
	runtimeStartedAt         time.Time                // when this agent runtime started; drift warnings omit mtimes predating it
	sessionLock              *recovery.SessionLock    // cross-process exclusive ownership of sessionDir
	sessionArtifactsDirFn    func() string            // active session artifacts directory for exports / dumps
	sessionTargetChangedFn   func(string)             // notified after active sessionDir changes
	focusedAgent             atomic.Pointer[SubAgent] // currently focused SubAgent (nil = main)
	focusedTaskMu            sync.RWMutex
	focusedTaskID            string // focused durable task when its runtime is parked
	subAgentInbox            subAgentInbox
	ownedSubAgentMailboxes   map[string][]SubAgentMailboxMessage // owner agentID -> descendant mailbox waiting for owner-local delivery
	ownedMailboxSpool        map[string][]string                 // owner agentID -> durable mailbox IDs outside the memory budget
	subAgentMailboxIDsMu     sync.Mutex
	subAgentMailboxIDs       map[string]struct{} // session-scoped idempotency keys for persisted and live mailbox events
	subAgentMailboxConsumed  map[string]struct{} // consumed mailbox IDs loaded once and updated with ack writes
	mailboxDeliveryPaused    atomic.Bool         // restored sessions wait for explicit user continuation before mailbox delivery
	pendingSubAgentMailboxes []*SubAgentMailboxMessage
	activeSubAgentMailboxes  []*SubAgentMailboxMessage
	activeSubAgentMailbox    *SubAgentMailboxMessage
	activeSubAgentMailboxAck bool
	subAgentMailboxSeq       atomic.Uint64
	subAgentInboxSummaryMu   sync.RWMutex
	subAgentUrgentCounts     map[string]int
	explicitUserTurnCount    atomic.Uint64

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
	// editMatchFailStreak counts repeated approximate-match failures per
	// target path for edit/apply_patch within the current turn, so the agent
	// can advise a fresh bounded read once the model has burned retries on
	// drifted target text. Reset at every new turn; the event loop is the
	// only reader/writer.
	editMatchFailStreak map[string]int
	// applyPatchRetry blocks an unchanged patch after a match failure until
	// the model successfully reads one of its targets. Unlike the advisory
	// streak above, execution goroutines also consult this state.
	applyPatchRetry applyPatchRetryGuard

	// pendingRecoveryPrompt is a request-scoped recovery prompt injected after
	// length-recovery auto compaction succeeds. It is consumed as a one-shot
	// turn overlay and never appended to ctxMgr durable messages.
	pendingRecoveryPrompt string
	// pendingThinkingReplayPrefix holds the visible reasoning text of a response
	// whose whole output budget was spent on thinking before any visible reply.
	// It is replayed as a wire-only assistant message on the next recovery
	// request so the model continues from its truncated reasoning instead of
	// restarting. Bound to the producing model ref: if the model pool cursor
	// moved before the retry, the prefix is dropped. It never enters ctxMgr or
	// the durable history.
	pendingThinkingReplayPrefix *message.Message
	pendingThinkingReplayRef    string
	pendingThinkingReplayTurnID uint64
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
	adhocSeq        atomic.Uint64
	agentRequestSeq atomic.Uint64

	// Optional MCP summary injected into the system prompt (set after MCP init).
	mcpServersPromptMu  sync.RWMutex
	mcpServersPrompt    string
	mcpRuntimePrompt    string
	pendingMCPTools     []tools.Tool
	pendingMCPReplace   bool
	agentsMDReady       chan struct{}
	agentsMDReadyOnce   sync.Once
	skillsReady         chan struct{}
	skillsReadyOnce     sync.Once
	mcpReadyMu          sync.Mutex
	mcpReady            chan struct{}
	mcpReadyGeneration  uint64
	mcpTransitionActive atomic.Bool
	mcpControlFn        func(context.Context, MCPControlRequest) (MCPControlResult, error)
	mcpMountState       mcpToolMountState
	loopDoneLateMount   atomic.Bool
	// mcpMountFullInjectionOnly is sticky for the current session run. Once
	// set, cache-friendly dynamic MCP mounts (Responses additional_tools and
	// Kimi mcp_system_tools_message) are disabled and every MCP tool is
	// injected in the top-level tools array. It is set on boundaries where
	// prompt-cache reuse no longer applies — model switch, session resume,
	// forked history, and durable compaction — so dynamic mounts cannot be
	// mis-anchored or misread as the complete tool surface. The next
	// session-head event (resetSessionBuildState) clears it: a fresh, empty
	// session run may mount dynamically again.
	mcpMountFullInjectionOnly atomic.Bool
	sessionBuilt              atomic.Bool
	bugTriagePromptActive     atomic.Bool

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

	// modelSwitchFactory creates a new LLM client for a selected model
	// reference string ("provider/model" or "provider/model@variant"). Callers
	// supply poolRefs/poolVariant — the model chain and default variant of the
	// role the client will run under — so the factory attaches the right
	// fallback pool without reading the agent's current (possibly still old)
	// active role. Used by SwitchModel, role switches, SubAgent pool rebuilds,
	// and deferred main-model policy rebuilds. Set via SetModelSwitchFactory
	// after construction.
	modelSwitchFactory func(providerModel string, poolRefs []string, poolVariant string) (*llm.Client, string, int, error)
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
	// <INSTRUCTIONS>). Built once ensureSessionBuilt completes and refreshed
	// in place on session-head events; injected into every LLM request so the
	// prompt prefix keeps one stable shape. Not persisted to ctxMgr or jsonl.
	cachedSessionReminderContent atomic.Pointer[string]

	// Memory (cross-session project memory). See memory_runtime.go. The memory
	// block is appended to the session-context reminder under an untrusted
	// wrapper, and the fixed load discipline is added to the stable system
	// prompt only when a MEMORY.md is present.
	memoryMu       sync.Mutex
	memoryMgr      *memory.Manager
	memoryErr      error
	memoryPending  []memoryJob // frozen session dirs awaiting extraction
	memoryInflight *memoryInflight
	memoryWake     chan struct{}
	// memoryWorkerDone closes when the background extraction worker returns, so
	// Shutdown can confirm it stopped touching project files.
	memoryWorkerDone     chan struct{}
	memoryExtractEnabled atomic.Bool // effective memory.enabled (project overrides user)
	memoryActive         atomic.Bool
	// memoryReminderVersion is bumped whenever the cached Memory block changes
	// (init, background extraction commit). ensureSessionBuilt rebuilds the
	// per-request session reminder when it moves, so a background commit lands
	// on the next request even when the load activation state did not flip.
	memoryReminderVersion atomic.Int64
	// memoryReminderBuilt records the version captured when
	// cachedSessionReminderContent was last rebuilt from the memory block.
	memoryReminderBuilt  atomic.Int64
	cachedMemoryReminder atomic.Pointer[string]

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
	activityObserverMu      sync.RWMutex
	activityObserver        ActivityObserver
	busyPreparationMu       sync.RWMutex
	busyPreparationHook     func(context.Context) error
	subAgentParkBarrierHook func(*SubAgent) // test-only deterministic race barrier
}

// persistEntry is a queued persistence request for ordered JSONL writes.
type persistEntry struct {
	agentID        string
	msg            message.Message
	recovery       *recovery.RecoveryManager // manager resolved at enqueue time; nil means the write's session is gone
	after          func(error)
	walltimeLedger *analytics.UsageLedger
	walltimeEvent  *analytics.UsageEvent
	barrier        chan struct{}
	stop           bool
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
//
// pathLocators optionally carries the startup-resolved config.PathLocator so
// Memory state files and session scanning honor custom paths.state_dir /
// paths.sessions_dir settings. When omitted the default path locator is used
// (tests construct the agent directly and rely on that).
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
	pathLocators ...*config.PathLocator,
) *MainAgent {
	var pathLocator *config.PathLocator
	if len(pathLocators) > 0 {
		pathLocator = pathLocators[0]
	}
	parentCtx, cancel := context.WithCancel(ctx)

	workDir, _ := os.Getwd()
	if workDir == "" {
		workDir = projectRoot
	}
	gitStatusReady := make(chan struct{})
	orchestrationCfg := effectiveOrchestrationConfig(globalCfg, projectCfg)
	governor := newResourceGovernor(orchestrationCfg)

	a := &MainAgent{
		parentCtx:               parentCtx,
		cancel:                  cancel,
		llmClient:               llmClient,
		ctxMgr:                  ctxMgr,
		tools:                   toolRegistry,
		hookEngine:              hookEngine,
		usageTracker:            analytics.NewUsageTracker(),
		usageLedger:             analytics.NewUsageLedger(sessionDir, projectRoot),
		invokedSkills:           make(map[string]*skill.Meta),
		globalConfig:            globalCfg,
		projectConfig:           projectCfg,
		eventCh:                 make(chan Event, 256),
		eventWakeCh:             make(chan struct{}, 1),
		eventSpaceCh:            make(chan struct{}, 1),
		eventOverflowLimit:      defaultEventOverflowLimit,
		loopEventLimit:          defaultLoopEventLimit,
		outputCh:                make(chan AgentEvent, 512),
		sessionDir:              sessionDir,
		modelName:               modelName,
		runningModelRef:         modelName,
		instanceID:              NextInstanceID(identity.MainAgentID),
		mcpClientInfo:           mcpClientInfo,
		done:                    make(chan struct{}),
		stoppingCh:              make(chan struct{}),
		evidence:                evidenceCandidateTracker{seen: make(map[string]int)},
		cacheHitTracker:         newCacheHitTracker(),
		projectRoot:             projectRoot,
		pathLocator:             pathLocator,
		subs:                    newSubAgentRegistry(),
		governor:                governor,
		waitingMainExpiry:       resolveWaitingMainExpiryPolicy(orchestrationCfg),
		sem:                     governor.runtimeSlots,
		fileTrack:               filelock.NewFileTracker(),
		fileBackups:             newFileBackupManager(sessionDir),
		runtimeStartedAt:        time.Now(),
		subAgentInbox:           newSubAgentInbox(),
		ownedSubAgentMailboxes:  make(map[string][]SubAgentMailboxMessage),
		ownedMailboxSpool:       make(map[string][]string),
		subAgentMailboxIDs:      make(map[string]struct{}),
		subAgentMailboxConsumed: make(map[string]struct{}),
		subAgentUrgentCounts:    make(map[string]int),
		recoveryOwner:           &recoveryOwnership{manager: recovery.NewRecoveryManager(sessionDir), dir: sessionDir},
		persist:                 newPersistencePump(256),
		cachedWorkDir:           workDir,
		gitStatusReady:          gitStatusReady,
		agentsMDReady:           make(chan struct{}),
		skillsReady:             make(chan struct{}),
		mcpReadyMu:              sync.Mutex{},
		mcpReady:                make(chan struct{}),
	}
	a.interaction = newInteractionBroker(a.stoppingCh)
	a.walltime = newWalltimeRecorder(a.usageLedger, a.persist, a.stoppingCh)
	a.interaction.setSettledHook(func(target *walltimeTarget, d time.Duration) {
		if a.walltime != nil {
			a.walltime.recordTarget(target, analytics.WalltimePurposeUserWait, d)
		}
	})
	if llmClient != nil {
		llmClient.SetCandidateScorer(a.cacheAwareCandidateScore)
	}
	a.startPersistLoop()
	a.refreshSessionSummary()

	// Fetch git status asynchronously; callLLM will wait for it before the
	// first LLM request so the system prompt always has accurate info.
	go func() {
		a.setCachedGitStatus(getGitStatus(workDir))
		close(gitStatusReady)
	}()

	// Detect Python virtual environment synchronously (just os.Stat, cheap).
	a.cachedVenvPath = detectVenvPath(workDir, projectRoot)

	// Wire Memory before building the system prompt so the stable prompt can
	// include the fixed Memory discipline when a MEMORY.md is present. The
	// background extraction worker starts here (project/process lifetime).
	if a.memoryMgr == nil {
		a.initMemory(projectRoot)
	}

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
		a.mcpServersPromptMu.Lock()
		a.mcpRuntimePrompt = block
		a.mcpServersPromptMu.Unlock()
		a.markMCPReady()
		return
	}
	a.mcpServersPromptMu.Lock()
	a.mcpServersPrompt = block
	a.mcpRuntimePrompt = block
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
	a.mcpRuntimePrompt = block
	a.pendingMCPReplace = false
	if len(mcpTools) == 0 {
		a.pendingMCPTools = nil
	} else {
		a.pendingMCPTools = append([]tools.Tool(nil), mcpTools...)
	}
	a.mcpServersPromptMu.Unlock()
	a.markMCPReady()
}

// SetRuntimeMCPDiscoveryForGeneration applies a background MCP restore only
// when no newer restore has replaced its readiness barrier. The generation's
// own wait channel is always closed so a superseded waiter cannot leak.
func (a *MainAgent) SetRuntimeMCPDiscoveryForGeneration(generation MCPReadyGeneration, mcpTools []tools.Tool, block string) bool {
	a.mcpReadyMu.Lock()
	defer a.mcpReadyMu.Unlock()
	if generation.id != a.mcpReadyGeneration || generation.ready != a.mcpReady {
		closeReadyChannel(generation.ready)
		return false
	}
	a.stageRuntimeMCPDiscovery(mcpTools, block)
	a.markRuntimeSurfaceDirty()
	a.closeMCPReadyLocked()
	return true
}

// applyMCPControlSurfaceForGeneration applies a completed runtime MCP control
// result only when no newer restore has replaced its readiness barrier,
// mirroring SetRuntimeMCPDiscoveryForGeneration. apply runs and the current
// barrier is released only for the owning generation; a superseded request has
// its own wait channel closed so a waiter that grabbed it cannot leak.
func (a *MainAgent) applyMCPControlSurfaceForGeneration(generation MCPReadyGeneration, apply func()) bool {
	a.mcpReadyMu.Lock()
	defer a.mcpReadyMu.Unlock()
	if generation.id != a.mcpReadyGeneration || generation.ready != a.mcpReady {
		closeReadyChannel(generation.ready)
		return false
	}
	apply()
	a.closeMCPReadyLocked()
	return true
}

func (a *MainAgent) stageRuntimeMCPDiscovery(mcpTools []tools.Tool, block string) {
	a.mcpServersPromptMu.Lock()
	a.mcpServersPrompt = block
	a.mcpRuntimePrompt = block
	a.pendingMCPReplace = true
	if len(mcpTools) == 0 {
		a.pendingMCPTools = nil
	} else {
		a.pendingMCPTools = append([]tools.Tool(nil), mcpTools...)
	}
	a.mcpServersPromptMu.Unlock()
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
	defer a.mcpReadyMu.Unlock()
	a.closeMCPReadyLocked()
}

func (a *MainAgent) closeMCPReadyLocked() {
	closeReadyChannel(a.mcpReady)
}

func closeReadyChannel(ch chan struct{}) {
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
// Under YOLO it returns the yoloRuleset view of the user's rules: unprotected
// rules drop out (those tools bypass), while delegate/handoff/cancel/done/
// compact_context rules survive together with the mechanism tools' mirrored
// defaults, so the visible surface matches what the execution gate still
// decides. SubAgents must use subAgentBaseRuleset instead.
func (a *MainAgent) effectiveRuleset() permission.Ruleset {
	ruleset := a.snapshotRuleset()
	if a.yoloEnabled.Load() {
		return yoloRuleset(ruleset)
	}
	return ruleset
}

// subAgentBaseRuleset returns the unfiltered ruleset SubAgents should inherit
// when they are created or refreshed. SubAgents evaluate the user's full rule
// set; the only YOLO effect they inherit is the ask→allow downgrade the
// SubAgent execution pipeline applies while the main agent's YOLO mode is on.
func (a *MainAgent) subAgentBaseRuleset() permission.Ruleset {
	return a.snapshotRuleset()
}

// buildSubAgentRuleset returns the ruleset a freshly created or restored
// SubAgent should evaluate tool permissions against: the unfiltered main-agent
// ruleset merged with the SubAgent's own session-rule bucket (so rules the
// SubAgent triggered persist across MainAgent role switches) and the
// SubAgent's own agent-definition permission config.
func (a *MainAgent) buildSubAgentRuleset(agentDef *config.AgentConfig) permission.Ruleset {
	ruleset := a.subAgentBaseRuleset()
	if agentDef != nil {
		if role := strings.TrimSpace(agentDef.Name); role != "" && a.overlay != nil {
			if session := a.overlay.SessionRulesForRole(role); len(session) > 0 {
				ruleset = permission.Merge(ruleset, session)
			}
		}
		if agentDef.Permission.Kind != 0 {
			ruleset = permission.Merge(ruleset, permission.ParsePermission(&agentDef.Permission))
		}
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

	// Resolve and validate the role's model before committing any role state,
	// so a model failure leaves the agent fully unchanged (role, history, and
	// model policy) instead of half-switched with an error. The prepared model
	// is installed only after the role state is committed below; installing
	// cannot fail, so the role and its model always change together.
	var prepared *preparedMainModel
	if nextRef := a.defaultRoleModelRef(cfg); nextRef != "" {
		var err error
		prepared, err = a.prepareMainModelForRole(nextRef, cfg)
		if err != nil {
			return fmt.Errorf("apply role %q model %q: %w", roleName, nextRef, err)
		}
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

	if prepared != nil {
		a.installPreparedMainModel(prepared)
	}

	// Keep a lazy rebuild fallback when the role has no explicit model list.
	a.mainModelPolicyDirty.Store(prepared == nil)

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

// GetTokenUsage returns cumulative token usage for the TUI-focused agent, in
// the same frame as GetSidebarUsageStats and the usage panel: the billing
// split, where InputTokens is the whole prompt side including the cached
// prefix.
//
// It reads the usage ledger rather than the focused agent's context manager
// even for a live agent. A context manager accumulates the provider's raw
// numbers, whose input field includes the cache-read prefix on some providers
// and excludes it on others, so the same session showed one prompt-side total
// while an agent was live and a different one after it parked (the parked path
// has always read the ledger). The ledger normalizes that split once, at
// record time, for every agent.
func (a *MainAgent) GetTokenUsage() message.TokenUsage {
	return tokenUsageFromSessionStats(a.GetSidebarUsageStats())
}

const sessionEndHookGrace = 300 * time.Millisecond

// Shutdown cancels any in-flight work and waits for the event loop to exit
// (up to the given timeout). The caller should cancel the context passed to
// Run as well.
func (a *MainAgent) Shutdown(timeout time.Duration) error {
	log.Infof("agent shutting down instance=%v timeout=%v", a.instanceID, timeout)
	// Cancel in-flight memory extraction and flush the usage ledger. Shutdown
	// never starts new extraction and never waits on an in-flight one.
	a.shutdownMemoryWorker()
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
	a.admissionMu.Lock()
	a.shuttingDown.Store(true)
	// Unblock reliable/interactive TUI sends immediately. Waiting until Run's
	// defer is too late when the event-loop goroutine itself is blocked on a
	// full output channel: it cannot reach that defer until Shutdown releases it.
	a.signalStopping()
	a.admissionEpoch.Add(1)
	a.cancelSubAgentAdmissions()
	a.admissionMu.Unlock()

	// Stop the event loop before taking the final snapshot. signalStopping
	// releases any output-channel backpressure, while cancelling parentCtx
	// makes nextEvent return as soon as the current handler completes.
	a.cancel()
	a.cancelActiveWork()
	a.waitForSubAgents(remaining)

	// Defer SubAgent MCP cleanup across every subsequent shutdown return. The
	// helper waits for Run to finish before closing transports, so timeout paths
	// do not race an event-loop MCP call or leave cleanup behind if Run exits
	// shortly after Shutdown's budget expires.
	defer a.closeSubAgentMCPServersAfterRun()

	// Close the persistence channel and wait for the loop to drain.
	// The persist loop may be started outside Run (tests), so don't gate the wait
	// on the main event loop start flag. Closing the channel is itself an
	// ordering barrier: the drain loop processes every already-enqueued entry
	// (including walltime segments settled during cancellation) in FIFO order
	// before it exits, so no separate pre-close flush is needed.
	persistDrained := true
	if a.persist.ch != nil {
		a.closePersistLoop()
		if wait := remaining(); wait > 0 {
			select {
			case <-a.persist.done:
			case <-time.After(wait):
				persistDrained = false
			}
		} else {
			persistDrained = false
		}
	}
	if !persistDrained {
		return a.shutdownTimeoutError(timeout)
	}

	compactionDrained := true
	if wait := remaining(); wait > 0 {
		done := make(chan struct{})
		go func() {
			a.compactionWg.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(wait):
			compactionDrained = false
		}
	} else {
		compactionDrained = false
	}
	if !compactionDrained {
		return a.shutdownTimeoutError(timeout)
	}

	// The extraction worker was cancelled at the top of Shutdown; confirm it
	// actually returned before the session files are closed below.
	if !a.waitMemoryWorkerStopped(remaining()) {
		return a.shutdownTimeoutError(timeout)
	}

	// If Run was started, wait until its current handler and output producers
	// have fully stopped before reading mutable loop state or closing recovery.
	if a.started.Load() {
		if wait := remaining(); wait > 0 {
			select {
			case <-a.done:
			case <-time.After(wait):
				return a.shutdownTimeoutError(timeout)
			}
		} else {
			return a.shutdownTimeoutError(timeout)
		}
	}

	// The event loop has fully exited: settle any handoff the user never
	// decided so the transcript does not end on an unresolved tool call.
	a.settlePendingHandoffAtShutdown()

	if failed := a.checkpointDegradedSubAgents(); len(failed) > 0 {
		log.Warnf("shutdown leaving SubAgents with degraded persistence agent_ids=%v", failed)
	}

	// Save final snapshot and close recovery manager (flush JSONL file handles).
	if manager := a.recoveryManager(); manager != nil {
		if err := a.persistSnapshotLocked(a.buildShutdownSnapshot); err != nil {
			log.Warnf("failed to save final recovery snapshot error=%v", err)
		}

		// Unpublish before closing so a straggling worker resolves nil instead
		// of a closed manager it would report as a persistence failure.
		a.clearRecoveryManagerIf(manager)
		manager.Close()
	}

	if a.sessionLock != nil {
		if err := a.sessionLock.Release(); err != nil {
			log.Warnf("failed to release session lock on shutdown error=%v", err)
		}
	}

	return nil
}

// shutdownTimeoutError persists a best-effort snapshot before Shutdown aborts
// with workers still live. persistSnapshotLocked serializes the snapshot build
// and write against the concurrent TodoWrite / SubAgent persistence writers,
// and SaveSnapshot writes atomically via temp+rename, so racing a wedged loop
// can only yield a slightly stale snapshot — never a corrupt one. The recovery
// manager stays open: the stuck persist loop may still be writing JSONL, and
// Close would turn those writes into silent no-ops.
func (a *MainAgent) shutdownTimeoutError(timeout time.Duration) error {
	if a.recoveryManager() != nil {
		if err := a.persistSnapshotLocked(a.buildShutdownSnapshot); err != nil {
			log.Warnf("failed to save best-effort recovery snapshot on shutdown timeout error=%v", err)
		}
	}
	return fmt.Errorf("agent shutdown timed out after %v", timeout)
}

func (a *MainAgent) waitForSubAgents(remaining func() time.Duration) {
	subs := a.subs.snapshotSubAgents()
	for _, sub := range subs {
		wait := remaining()
		if wait <= 0 {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), wait)
		if err := sub.waitDone(ctx); err != nil {
			log.Warnf("SubAgent did not stop within shutdown budget agent_id=%v error=%v", sub.instanceID, err)
		}
		cancel()
	}
}

func (a *MainAgent) checkpointDegradedSubAgents() []string {
	if a == nil {
		return nil
	}
	var failed []string
	for _, sub := range a.subs.snapshotSubAgents() {
		if sub == nil || sub.transcriptPersistenceHealthy() {
			continue
		}
		if err := sub.checkpointTranscript(); err != nil {
			failed = append(failed, sub.instanceID)
		}
	}
	return failed
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
	a.llmMu.RLock()
	mainClient := a.llmClient
	a.llmMu.RUnlock()
	if mainClient != nil {
		mainClient.Close()
	}

	a.subs.mu.RLock()
	for _, sub := range a.subs.subAgents {
		tools.StopAllSpawnedForAgent(sub.instanceID, "terminated on client exit")
		sub.cancel()
		sub.closeLLMClient()
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

func (a *MainAgent) closeSubAgentMCPServersAfterRun() {
	if !a.started.Load() {
		a.closeSubAgentMCPServers()
		return
	}
	select {
	case <-a.done:
		a.closeSubAgentMCPServers()
	default:
		go func() {
			<-a.done
			a.closeSubAgentMCPServers()
		}()
	}
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
	if item, ok := subAgentMailboxEvidence(msg, "runtime SubAgent mailbox"); ok {
		a.addEvidenceCandidate(item)
		return
	}
	if msg.Role == message.RoleUser && !message.IsUserAuthored(msg) {
		return
	}
	if strings.TrimSpace(msg.Content) == "" && len(msg.Parts) == 0 && strings.TrimSpace(msg.ToolDiff) == "" {
		return
	}
	text := message.UserPromptInstructionText(msg)
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
			a.addEvidenceCandidate(buildEvidenceItem(
				evidenceUserCorrection,
				"User correction / constraint",
				"This explicitly constrains the next code change and should be preserved verbatim.",
				"runtime user message",
				compactTextSnippet(text, 600),
			))
		case looksLikeStatedConstraint(text):
			a.addEvidenceCandidate(buildStatedConstraintEvidence("runtime user message", text))
		case isPlainUserRequestForCompaction(text):
			a.addEvidenceCandidate(buildLatestUserRequestEvidence("runtime user message", text))
		}
	case message.RoleTool:
		if reason, ok := extractDoneRejectedReason(text); ok {
			a.addEvidenceCandidate(buildDoneRejectedEvidence("runtime tool result", reason))
		} else if reason, ok := extractToolRejectedByUserReason(text); ok && isPlainUserRequestForCompaction(reason) {
			a.addEvidenceCandidate(buildLatestUserRequestEvidence("runtime tool rejection reason", reason))
		}
		if isToolResultErrorMessage(msg) {
			item := buildEvidenceItem(
				evidenceToolError,
				"Latest failing tool result",
				"This looks like a current blocker; preserving the exact error helps the next continuation avoid guessing.",
				"runtime tool result",
				compactTextSnippet(text, 800),
			)
			a.addToolEvidenceCandidate(item, msg)
		}
		if strings.TrimSpace(msg.ToolDiff) != "" {
			item := buildEvidenceItem(
				evidenceToolDiff,
				"Recent code diff",
				"The next continuation may depend on the exact recent code change.",
				"runtime tool diff",
				compactTextSnippet(msg.ToolDiff, 700),
			)
			a.addToolEvidenceCandidate(item, msg)
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
	modelName := a.modelName
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
	if a.unsupportedPartToast.first(modelName, toastCategoryInput, droppedSummary(unique)) {
		a.emitToTUI(ToastEvent{
			Message: "The current model does not support " + strings.Join(unique, "/") + " input; attachments were ignored",
			Level:   "warn",
		})
	}

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
	a.consumePendingUserMessagesForRequest(nil, 0)
}

func (a *MainAgent) consumePendingUserMessagesForRequest(messages []message.Message, tailOverlayCount int) []message.Message {
	if len(a.pendingUserMessages) == 0 {
		return messages
	}
	pending := a.pendingUserMessages
	a.pendingUserMessages = nil
	var deferred []pendingUserMessage
	type consumedPendingDraft struct {
		draftID string
		msg     message.Message
	}
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
		consumed = append(consumed, consumedPendingDraft{draftID: p.DraftID, msg: m})
		manualInputConsumed = manualInputConsumed || p.FromUser
	}
	// Re-queue /resume* and anything that arrived concurrently (should be rare).
	a.pendingUserMessages = append(deferred, a.pendingUserMessages...)
	if len(consumed) == 0 {
		return messages
	}
	if manualInputConsumed {
		a.stageNextSubAgentMailboxBatch()
	}
	log.Debugf("injecting pending user messages with tool results count=%v", len(consumed))
	tailOverlayCount = min(max(tailOverlayCount, 0), len(messages))
	insertionAt := len(messages) - tailOverlayCount
	requestMessages := make([]message.Message, 0, len(messages)+len(consumed))
	requestMessages = append(requestMessages, messages[:insertionAt]...)
	for _, item := range consumed {
		a.ctxMgr.Append(item.msg)
		requestMessages = append(requestMessages, item.msg)
		a.recordEvidenceFromMessage(item.msg)
		if a.recoveryManager() != nil {
			a.persistAsync(identity.MainAgentID, item.msg)
		}
		a.emitPendingDraftConsumed(item.draftID, item.msg)
	}
	requestMessages = append(requestMessages, messages[insertionAt:]...)
	a.syncBugTriagePromptFromSnapshot()
	return requestMessages
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

	a.explicitUserTurnCount.Add(1)
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
	a.tryRecoverPersistenceBeforeTurn()
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
	a.tryRecoverPersistenceBeforeTurn()
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
	if a.recoveryManager() != nil {
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

	// A model-driven checkpoint armed by this turn must not outlive it: the
	// user just aborted exactly the work the checkpoint was meant to continue.
	a.cancelCompactionForTurnCancellation(evt.TurnID)

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

// streamContinueMessageText is the durable user-role continuation message
// appended after a preserved stream interruption for pools whose models cannot
// resume from a trailing assistant turn. It is real transcript content: the
// same text is sent to the model in this and every later request and rendered
// as the card the user sees, so what the UI shows always equals what the model
// was told. The preserved partial reply sits directly above it in history, so
// it names what to continue instead of restating the task.
const streamContinueMessageText = "Your previous reply was interrupted before it completed. The text already written is preserved above; continue directly from the interruption point without apologizing or restating what was written."

// resumeAfterPreservedStreamInterruption saves the turn's streamed partial
// text as an interrupted assistant message and restarts the main LLM request so
// the model continues from where it stopped. The restart runs the full retry
// rotation (key switch, fallback models, cooling waits): the client cooled the
// interrupted key before escalating, so a transport that keeps truncating
// mid-stream rotates across keys and fallback models and waits out cooldowns
// instead of restarting back-to-back. Continuation is not capped: the restart
// repeats until the reply completes, an interruption carries no new visible
// text, the turn is cancelled or goes stale, or a non-resumable error ends the
// turn normally. It reports false when the turn is stale or no body text was
// streamed, letting the caller fall through to ordinary error handling.
// Event-loop-goroutine only.
func (a *MainAgent) resumeAfterPreservedStreamInterruption(turnID uint64, cause error) bool {
	if a.turn == nil || turnID == 0 || a.turn.ID != turnID {
		return false
	}
	if !a.savePartialAssistantMsgForTurn(a.turn) {
		// Nothing visible was streamed before the interruption, so there is no
		// partial reply to resume; fall through to ordinary error handling.
		return false
	}
	log.Infof("resuming turn after preserved stream interruption turn_id=%v error=%v", turnID, cause)
	a.prepareStreamContinuation()
	a.resumeTurnAfterRoutingInvalidation(turnID)
	return true
}

// prepareStreamContinuation readies the restart of a preserved interruption.
// The saved partial reply is already the last durable message, so a model pool
// that can resume from a trailing assistant turn needs nothing else: the next
// request simply ends with the interrupted reply and the model picks it up. A
// pool that needs a user turn instead gets a durable KindStreamContinue
// message appended right after the partial, so the transcript and every later
// request carry exactly what the model was sent. The StreamContinueEvent
// mirrors that appended message (empty when nothing was appended) so the TUI
// settles the interrupted card and, when a message exists, renders it.
func (a *MainAgent) prepareStreamContinuation() {
	if a.llmClient != nil && a.llmClient.AllPoolTargetsSupportAssistantPrefillContinuation() {
		log.Debugf("resuming preserved stream via trailing assistant turn turn_id=%v", a.turn.ID)
		a.emitToTUI(StreamContinueEvent{Text: ""})
		return
	}
	msg := message.Message{Role: message.RoleUser, Content: streamContinueMessageText, Kind: message.KindStreamContinue}
	a.ctxMgr.Append(msg)
	if a.recoveryManager() != nil {
		a.persistAsync(identity.MainAgentID, msg)
	}
	log.Debugf("persisted stream continuation message turn_id=%v", a.turn.ID)
	a.emitToTUI(StreamContinueEvent{Text: streamContinueMessageText})
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
		// Settle the request before the routing-invalidation restart:
		// applyPendingModelPoolSwitchesAtRequestBoundary is a no-op while a main
		// request is still marked in flight, and this is exactly the boundary a
		// pool switch that invalidated the routing must be applied at.
		a.mainLLMRequestInFlight.Store(false)
		if llm.IsRoutingInvalidated(err) {
			log.Infof("routing invalidated during active turn; restarting request turn_id=%v instance=%v", evt.TurnID, a.instanceID)
			a.applyPendingModelPoolSwitchesAtRequestBoundary()
			// The restart can land on a different model or backend, and a
			// finalized reasoning item only replays to the one that produced
			// it. The partial text is provider-neutral and stays.
			a.turn.drainPartialResponsesOutput()
			if a.resumeTurnAfterRoutingInvalidation(evt.TurnID) {
				return
			}
		}

		// A stream interruption that carried already-streamed assistant text is
		// resumable: the partial reply is saved to history (never discarded),
		// followed by a durable continuation message for pools that need one,
		// and the turn restarts the request so the model continues from where
		// it stopped. The restart runs the full retry rotation (key switch,
		// fallback models, cooling waits) without a round cap, so a
		// persistently failing transport behaves like any other error without
		// ever throwing away produced text; the client cools the interrupted
		// key before escalating so repeated interruptions rotate keys and
		// fallback models and wait out cooldowns rather than restarting
		// back-to-back. When nothing visible was streamed, this falls through
		// to ordinary error handling below — which then discards nothing,
		// since the partial text has already been drained and saved.
		if a.turn != nil && llm.IsPreservableStreamInterruption(err) {
			if a.resumeAfterPreservedStreamInterruption(evt.TurnID, err) {
				return
			}
		}

		log.Errorf("agent error error=%v turn_id=%v instance=%v", err, evt.TurnID, a.instanceID)
		a.discardPartialAssistantMsg()
		a.failPendingToolCalls(a.turn, err)
		a.applyPendingModelPoolSwitchesAtRequestBoundary()
		a.fireHookBackground(a.parentCtx, hook.OnAgentError, evt.TurnID, map[string]any{
			"message":         err.Error(),
			"error_kind":      classifyAgentError(err),
			"source_agent_id": a.instanceID,
		})
		a.emitToTUI(StreamRollbackEvent{Reason: err.Error()})
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
			// The SubAgent is about to enter its terminal close path. Ensure the
			// synthetic tool results queued above are durable before restore,
			// export, or cleanup can observe the failed instance.
			a.flushPersist()
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

	sub2 := a.subAgentByID(evt.SourceID)
	if sub2 != nil {
		errorKind := classifyAgentError(err)
		failureSummary := fmt.Sprintf("SubAgent failed (%s): %s", errorKind, err.Error())
		a.releaseSubAgentSlot(sub2)
		a.emitActivity(evt.SourceID, ActivityIdle, "")
		mailbox := &SubAgentMailboxMessage{
			AgentID:      evt.SourceID,
			TaskID:       sub2.taskID,
			OwnerAgentID: sub2.OwnerAgentID(),
			OwnerTaskID:  sub2.OwnerTaskID(),
			InReplyTo:    firstReplyMessageID(sub2),
			Kind:         SubAgentMailboxKindRiskAlert,
			Subtype:      agentMessageSubtypeTaskFailure,
			Priority:     SubAgentMailboxPriorityInterrupt,
			Summary:      failureSummary,
			Payload: fmt.Sprintf(
				"SubAgent task terminated without completion.\n- task_id: %s\n- agent_id: %s\n- error_kind: %s\n- error: %s\n- required_action: inspect the failure and retry, reassign, or report the blocker; do not treat the task as completed.",
				sub2.taskID, evt.SourceID, errorKind, err.Error(),
			),
			RequiresAck: false,
		}
		// Terminal-commit ordering (mirror of the completion path in
		// handleAgentDone): persist and apply the risk_alert mailbox before the
		// close handler below commits the task Failed, so a crash after the
		// terminal commit can no longer lose the failure notification; the
		// queued mailbox event then only delivers the already-durable message.
		// Persistence stays best-effort: a failure is retried through the
		// queued event's own persist path and must never block the terminal
		// commit.
		if err := a.prepareSubAgentMailboxMessage(mailbox); err != nil {
			log.Warnf("risk_alert mailbox durability degraded task_id=%v agent_id=%v error=%v (will retry through the mailbox queue)", sub2.taskID, evt.SourceID, err)
		} else if messageID := strings.TrimSpace(mailbox.MessageID); messageID != "" {
			// Already durably recorded and applied here; the mailbox event must
			// only deliver it, not write or apply it a second time.
			a.markSubAgentMailboxSeen(messageID)
		}
		a.queueLoopEvent(Event{Type: EventSubAgentMailbox, SourceID: evt.SourceID, Payload: mailbox})
		a.emitToTUI(AgentNotifyEvent{
			AgentID:       evt.SourceID,
			TaskID:        sub2.taskID,
			AgentType:     sub2.agentDefName,
			ParentAgentID: controlPlaneAgentID(sub2.OwnerAgentID()),
			ParentTaskID:  sub2.OwnerTaskID(),
			TargetAgentID: controlPlaneAgentID(sub2.OwnerAgentID()),
			TargetTaskID:  sub2.OwnerTaskID(),
			Kind:          string(SubAgentMailboxKindRiskAlert),
			Message:       failureSummary,
			MessageID:     mailbox.MessageID,
		})
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
}

// ---------------------------------------------------------------------------
// Plan / Execute workflow
// ---------------------------------------------------------------------------

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
