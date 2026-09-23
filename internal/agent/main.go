package agent

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/analytics"
	"github.com/keakon/chord/internal/command"
	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/ctxmgr"
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
	// startupWorkDirNotice names a resume problem the session could not fix
	// before the event loop started (the recorded worktree was gone). It is
	// reported once as a toast, like the config issues above.
	startupWorkDirNotice string

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
	// pendingUserDrainSuspended parks the pendingUserMessages queue: its entries
	// wait for the next request that actually dispatches instead of auto-starting
	// a turn. Set while the agent waits on a user decision (Handoff) or when the
	// user interrupted a turn and asked for the queued work to stay put. While
	// set, the idle drain is suppressed and hasQueuedAutomaticWork ignores the
	// queue (so global idle is still reportable); the next dispatching request
	// injects the whole queue in FIFO order and clears the flag. Only the
	// event-loop goroutine reads/writes.
	pendingUserDrainSuspended bool

	// Output channel consumed by the TUI or any external observer.
	outputCh                      chan AgentEvent
	outputMu                      sync.RWMutex
	outputClosed                  atomic.Bool
	outputDropLogMu               sync.Mutex
	outputDropLogLastByType       map[string]time.Time
	outputDropLogSuppressedByType map[string]int
	stoppingOnce                  sync.Once

	// acceptedRawMessages counts raw main-agent user messages handed to the
	// event queues by SendUserMessageToTarget / SendUserMessageWithParts;
	// dispatchedRawMessages is the high-water mark the loop already consumed.
	// The gap between them is the window where a message is accepted but its
	// turn does not exist yet, which is where a cancel request has to be matched
	// against the message order instead of a turn (see CancelCurrentTurn).
	acceptedRawMessages   atomic.Int64
	dispatchedRawMessages atomic.Int64
	// cancelUpTo is the highest accepted-message order a cancel request that
	// arrived while no turn was active applies to. Only the event loop consumes
	// it, when it is about to start a turn for a covered message.
	cancelUpTo atomic.Int64

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

	// reductionMemo caches the byte-derived per-call verdicts of the reduction
	// pass (call metadata, repeat-detection digests and input keys, shell
	// invocation parses); see reductionToolCallMemo.
	reductionMemo reductionToolCallMemo

	// binaryPartCache bounds the lazily resolved attachment payloads held for
	// the wire converter; see binaryPartReadCache.
	binaryPartCache *binaryPartReadCache

	// subPersists coalesces sub-agent meta/registry writes off the event loop;
	// see subAgentPersistDebouncer.
	subPersists *subAgentPersistDebouncer

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

	// llmMu protects llmClient, modelName, providerModelRef,
	// appliedCompactionModelRef, running-model continuity, and model-run
	// cache-warmth state for cross-goroutine access. The TUI goroutine reads
	// ModelName() and ProviderModelRef() from View(), while SwapLLMClient /
	// SwitchModel write these fields. callLLM snapshots under RLock at the start
	// to ensure consistent model name for hooks and usage tracking. Model writers
	// serialize identity, budgets, and event delivery with modelUpdateMu; readers
	// only take llmMu, which is released before any blocking output delivery.
	modelUpdateMu        sync.Mutex
	llmMu                sync.RWMutex
	installedSysPrompt   string
	systemPromptOverride string
	// The event-loop goroutine owns the active request and pending model-pool
	// switch state. Pool switches requested while a main LLM request is in flight
	// are applied at the next request boundary, so they do not invalidate the
	// request currently producing output.
	mainLLMRequestInFlight atomic.Bool
	// mainRequestSeq counts the main LLM request goroutines spawned for the
	// foreground LLM slot. It is event-loop owned and doubles as the streaming
	// segment identity of the request (see StreamSegmentEndedEvent).
	mainRequestSeq uint64
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
	contentRoot string
	pathLocator *config.PathLocator // resolved startup paths; nil falls back to DefaultPathLocator
	// pathRootsResolver re-lists the repository's checkouts for policy-root
	// path evaluation. It is injected by cmd/chord (the only layer that
	// resolves git worktrees) and consulted at each turn start; pathRoots
	// caches the last immutable snapshot so tool goroutines read it without
	// blocking. A nil resolver or empty roots degrade path rules to the
	// cwd-only behavior.
	pathRootsResolver PathRootsResolver
	pathRoots         atomic.Pointer[pathRootsSnapshot]
	lastPlanPath      string
	pendingHandoff    *HandoffResult // deferred Handoff action; processed after all sibling tools finish
	// handoffWaitActive mirrors "pendingHandoff != nil" for mailbox delivery
	// paths that also run off the event loop (manual delivery, restore). While a
	// handoff user wait is open, automatic mailbox delivery is held instead of
	// starting a main turn that would abandon the wait and settle its deferred
	// result as cancelled. Only the event loop writes it; see setPendingHandoff
	// and takePendingHandoff.
	handoffWaitActive atomic.Bool
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
	// lastModelDrivenCheckpointFingerprint identifies the arguments and
	// post-apply runtime state of the last successful model-driven checkpoint.
	// It prevents an unchanged continuation from re-arming the same reset
	// when no new work or input follows the last checkpoint, including after restore.
	lastModelDrivenCheckpointFingerprint string
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
	// contextNoticesStale marks durable context-pressure notices as no longer
	// matching the live AutoCompactDecision: a model switch moved the
	// compaction/reminder line, or usage in the same window dropped back below
	// the reminder line. The event loop drops them at the next idle boundary
	// (see maybeClearStaleContextNotices); a leftover notice would otherwise
	// keep claiming pressure the current decision is not under. A fresh first
	// delivery for the live window cancels the mark: that row belongs to the
	// current line, so the audit must not sweep it away in the same turn.
	contextNoticesStale atomic.Bool
	// contextNoticesStalePressureOnly narrows an armed cleanup to the
	// reminder-class rows. A reminder line that is disabled for the current
	// model withdraws only the rows measured against that line: the grace and
	// externalization rows are measured against the compaction threshold,
	// which is still live, so they must survive the sweep. Written before
	// contextNoticesStale so a reader that observes the mark also observes its
	// scope.
	contextNoticesStalePressureOnly atomic.Bool
	// contextNoticesPersisted records that the transcript may still hold
	// durable context-pressure notice rows. Overlay delivery claims are
	// runtime memory that a restore or session switch never carries over, so
	// presence — not a delivered claim — is what tells the cleanup whether
	// there is anything to withdraw. Set when a row is appended, rebuilt from
	// the transcript at every session load/switch, and recomputed by the idle
	// cleanup scan itself.
	contextNoticesPersisted atomic.Bool
	// pendingOverlayAppends holds the mailbox-transcript / background-result
	// card events whose backing transcript write failed, so a persistence
	// recovery can re-emit them once the write path is healthy. The message is
	// already in ctxmgr, and future requests dedupe on the in-memory row, so
	// without the deferred event the TUI would keep the message's waiting row
	// until the session switches. Guarded by pendingOverlayAppendsMu; bounded
	// (see maxPendingOverlayAppends) and cleared at a session boundary.
	pendingOverlayAppendsMu sync.Mutex
	pendingOverlayAppends   []pendingOverlayAppend
	pendingOverlayReconcile bool
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
	spoolAppendMu            sync.Mutex          // serializes mailbox.jsonl append/rollback so a recorded spool-index byte span bounds one record
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
	// consecutiveIdleWakes counts back-to-back idle turns opened by mailbox
	// delivery with no user input between them; see maxConsecutiveIdleWakes.
	consecutiveIdleWakes atomic.Int32

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
	cachedWorkDir   string
	cachedGitStatus string // populated lazily via gitStatusReady
	cachedVenvPath  string // absolute path to detected Python virtual environment, or ""
	cachedAgentsMD  string
	gitStatusReady  chan struct{} // closed when cachedGitStatus is set
	// workDirState is the active checkout of this agent. A worktree switch
	// publishes a whole new state here; cachedWorkDir stays the immutable
	// directory the session started in.
	workDirState workDirBinding
	// worktreeRT carries cmd-injected worktree services (storage locator,
	// repository root, LSP rebind). Written before the agent runs turns.
	worktreeRT  WorktreeRuntime
	cachedSubMu sync.RWMutex
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
	// memoryDegraded records that background memory extraction is stalled: setup
	// failed, or a commit or extraction cannot proceed without external
	// intervention. The status bar shows it as a failed MEMORY pill instead of
	// an enabled one; a later successful commit clears it. Injection is not
	// switched off by this flag — a stalled commit keeps serving the last
	// successfully indexed memory.
	memoryDegraded atomic.Bool
	// memoryDegradedAt records when memoryDegraded last flipped to true (Unix
	// nanoseconds, 0 while healthy). A checkpoint entry extracted after that
	// instant proves project memory moved on in another process, which lets the
	// worker clear the indicator without waiting for a local commit.
	memoryDegradedAt atomic.Int64
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

// ---------------------------------------------------------------------------
// System prompt
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Constructor
// ---------------------------------------------------------------------------

// NewMainAgent creates a fully-initialised MainAgent. The caller must invoke
// Run in a separate goroutine to start the event loop.
//
// contentRoot is the root the project's content and machine state are anchored
// to: configuration, agent definitions, skills, memory, AGENTS.md, and the
// session project key. In a linked worktree it is the main worktree root, so
// every checkout of one repository shares them.
//
// The runtime working directory (the checkout tools and shell commands run in)
// comes from the process working directory at construction time.
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
	contentRoot string,
	workDir string,
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

	if strings.TrimSpace(workDir) == "" {
		workDir = contentRoot
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
		usageLedger:             analytics.NewUsageLedger(sessionDir, contentRoot),
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
		contentRoot:             contentRoot,
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
	a.subPersists = newSubAgentPersistDebouncer(a.flushDirtySubPersists, a.SessionDir)
	a.walltime = newWalltimeRecorder(a.usageLedger, a.persist, a.stoppingCh)
	a.interaction.setSettledHook(func(target *walltimeTarget, d time.Duration) {
		if a.walltime != nil {
			a.walltime.recordTarget(target, analytics.WalltimePurposeUserWait, d)
		}
	})
	if llmClient != nil {
		llmClient.SetCandidateScorer(a.cacheAwareCandidateScore)
	}
	// Resolved lazily-read attachment payloads for the wire converter: restored
	// sessions keep blobs on disk, so requests load them on demand instead of
	// holding every attachment in memory from startup.
	a.installBinaryPartResolver()
	a.startPersistLoop()
	a.refreshSessionSummary()

	// Fetch git status asynchronously; callLLM waits for it before the first
	// LLM request of this process so every injected prefix carries the real
	// value from the start. It reads the live binding rather than the startup
	// directory: a session restored into a worktree installs that binding
	// while this goroutine may still be running, and both orders must end on
	// the restored checkout's branch.
	go func() {
		a.setCachedGitStatus(getGitStatus(a.workDir()))
		close(gitStatusReady)
	}()

	// Detect Python virtual environment synchronously (just os.Stat, cheap).
	a.setCachedVenvPath(detectVenvPath(workDir, contentRoot))

	// Wire Memory before building the system prompt so the stable prompt can
	// include the fixed Memory discipline when a MEMORY.md is present. The
	// background extraction worker starts here (project/process lifetime).
	if a.memoryMgr == nil {
		a.initMemory(contentRoot)
	}

	// Build and install the system prompt (git status is injected into the
	// first user message per request, not part of this prompt).
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
		a.installContextNoticePresence(nil)
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
	acceptedOrder := int64(0)
	switch p := evt.Payload.(type) {
	case acceptedUserMessage:
		content = p.Content
		parts = p.Parts
		acceptedOrder = p.AcceptedOrder
		a.markRawUserMessageDispatched(acceptedOrder)
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
			AcceptedOrder: acceptedOrder,
			Content:       content,
			Parts:         parts,
			FromUser:      true,
		})
		return
	}

	// Idle: session and compaction commands before starting a turn.
	if a.tryHandleSlashCommand(content) {
		return
	}

	// A parked queue waits for this action: enqueue the new message behind it and
	// dispatch the whole batch in arrival order instead of starting a turn with
	// only the new message.
	if a.pendingUserDrainSuspended {
		a.pendingUserMessages = enqueuePendingUserMessage(a.pendingUserMessages, pendingUserMessage{
			AcceptedOrder: acceptedOrder,
			Content:       content,
			Parts:         parts,
			FromUser:      true,
		})
		a.resumePendingUserDrain()
		a.drainPendingUserMessages()
		return
	}

	// Start a new turn and call LLM.
	a.tryRecoverPersistenceBeforeTurn()
	a.stageNextSubAgentMailboxBatch()
	a.newTurn()
	a.turn.originAcceptedOrder = acceptedOrder
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

	// A cancel request accepted before this message was dispatched keeps the
	// message but not the work: the turn exists, so the transcript and recovery
	// keep the usual shape of a cancelled turn, and the model is never called.
	if a.cancelCoversAcceptedOrder(acceptedOrder) {
		log.Infof("accepted user message cancelled before its first request order=%v turn_id=%v", acceptedOrder, turnID)
		a.handleTurnCancelled(a.abortTurn(a.turn))
		return
	}

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

	// A parked queue waits for this action: enqueue the submitted draft behind it
	// and dispatch the whole batch in arrival order.
	if a.pendingUserDrainSuspended {
		a.pendingUserMessages = enqueuePendingUserMessage(a.pendingUserMessages, pending)
		a.resumePendingUserDrain()
		a.drainPendingUserMessages()
		return
	}

	a.mailboxDeliveryPaused.Store(false)
	a.tryRecoverPersistenceBeforeTurn()
	a.stageNextSubAgentMailboxBatch()
	a.newTurn()
	turnID := a.turn.ID
	turnCtx := a.turn.Ctx
	// Committing this draft here makes any queue entry carrying the same DraftID
	// stale: it was mirrored there while the agent was busy (or during an MCP
	// transition), and a later drain would inject the same draft a second time.
	a.pendingUserMessages, _ = removePendingDraft(a.pendingUserMessages, pending.DraftID)
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
	// Cancelling the turn ends it: nothing resumes behind the pending
	// compaction, so a round suspended oversize on a narrower fallback must
	// release that committed identity and its budgets back to the cursor the
	// next request starts from. callLLMForRequest deliberately keeps the
	// committed target while the suspension can resume, so this is the only
	// cancel path that has to realign it; every other cancel already returns
	// through that request-side realign.
	if a.compactionState.oversizeSuspended {
		if client, _ := a.mainLLMAndRef(); client != nil {
			a.syncRunningModelRefToCursorHead(client)
		}
	}
	a.savePartialAssistantMsg()
	if payload.KeepPendingUserMessagesQueued {
		a.suspendPendingUserDrain()
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

// handleTurnCancelRequested applies a cancel request that was accepted while no
// turn existed. A message accepted at or below the request's watermark closes
// as cancelled instead of running, whether its event is still queued (the
// watermark is recorded and applied when the turn starts) or already started a
// turn (that turn is cancelled here, matched by the order of the message that
// started it). Messages accepted later outrank the watermark and are untouched.
func (a *MainAgent) handleTurnCancelRequested(evt Event) {
	payload, ok := evt.Payload.(*turnCancelRequestPayload)
	if !ok || payload == nil {
		log.Errorf("handleTurnCancelRequested: invalid payload type payload_type=%v", fmt.Sprintf("%T", evt.Payload))
		return
	}
	a.raiseCancelUpTo(payload.AcceptedUpTo)
	if a.turn == nil {
		log.Debugf("cancel request recorded without an active turn accepted_up_to=%v", payload.AcceptedUpTo)
		return
	}
	// A covered message already reached the loop and started its turn, so this
	// request cancels the turn in front of it. The turn must prove it is
	// covered: a cancel request samples the watermark before it is queued, so a
	// message accepted in between can have started the turn in front of this
	// event, and a turn no raw main-agent message started is never covered.
	if !a.cancelCoversAcceptedOrder(a.turn.originAcceptedOrder) {
		log.Debugf("cancel request outranked by active turn accepted_up_to=%v origin=%v turn_id=%v", payload.AcceptedUpTo, a.turn.originAcceptedOrder, a.turn.ID)
		return
	}
	log.Infof("cancel request applied to active turn accepted_up_to=%v turn_id=%v", payload.AcceptedUpTo, a.turn.ID)
	a.handleTurnCancelled(a.abortTurn(a.turn))
}

func (a *MainAgent) resumeTurnAfterRoutingInvalidation(turnID uint64) bool {
	if a.turn == nil || turnID == 0 || a.turn.ID != turnID {
		return false
	}
	// The routing-invalidation resume is a dispatch boundary too: stage any
	// mailbox that arrived while the turn was paused so its next request carries
	// it alongside the queued user input.
	a.mergePendingInputsForTurnContinuation()
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
		a.effectiveToolBaseDir(),
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
