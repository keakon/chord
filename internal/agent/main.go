package agent

import (
	"context"
	"crypto/sha256"
	"sync"
	"sync/atomic"
	"time"

	"github.com/keakon/chord/internal/agent/promptcache"
	"github.com/keakon/chord/internal/agent/requestshape"
	"github.com/keakon/chord/internal/agent/requesttrim"
	"github.com/keakon/chord/internal/analytics"
	"github.com/keakon/chord/internal/command"
	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/executiongate"
	"github.com/keakon/chord/internal/filelock"
	"github.com/keakon/chord/internal/hook"
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
	contextDiagnostics    contextDiagnosticsRuntime
	mcpStatus             mcpStatusState
	directoryInstructions atomic.Pointer[directoryInstructionRuntime]
	directoryPreparation  *directoryInstructionPreparation

	skillPublisher              skillDiscoveryPublisher
	delivery                    inputDeliveryRuntime
	pause                       sessionPauseRuntime
	stagedMainInputs            []stagedSessionInput // owned by the event loop
	requestStarts               atomic.Uint64
	executionGate               *executiongate.Gate // guarded by stateMu; replaced with the session
	questions                   questionRuntime
	questionWaiting             atomic.Bool
	questionCompletionPending   atomic.Int64
	compactionFiles             compactionFileReplay
	pendingNativeContextAppends []message.Message // owned by the main event loop

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
	// startupCatalogAdvisories names the catalog freshness hints collected at
	// startup: catalog references a newer verified entry likely supersedes.
	// They load as written and never fail anything; the agent reports their
	// count once as an info toast so an upgrade-induced staleness is not
	// silently missed. Pointing at `chord doctor config` for the full report.
	startupCatalogAdvisories []string
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

	turn                          *Turn
	nextTurnID                    uint64
	turnEpoch                     uint64
	compactionContinuationStalled bool
	eventSeq                      atomic.Uint64
	requestBatches                requestBatchState
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
	// invocation parses); see requesttrim.Memo.
	reductionMemo requesttrim.Memo

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
	nativeReceipt     atomic.Pointer[nativeReceiptPersistence]

	// loopReductionMu protects request-shape snapshots, reduction stats, and
	// loopState fields that may be read by callLLM on a worker goroutine while
	// the event loop handles a busy /loop command.
	loopReductionMu               sync.Mutex
	lastPreparedLLMTurnID         uint64
	lastPreparedLLMRequestShape   []requestshape.Shape
	lastPreparedLLMRequestPrefix  []message.Message
	lastPreparedLLMReducedIndices []bool
	// lastPreparedLLMDiscardedInputs is session-scoped recall evidence: input
	// key -> ToolCallID of the call whose output was summarized away,
	// excluding repeated-collapse. Dropped with the reduction caches on
	// restore or model switch.
	lastPreparedLLMDiscardedInputs map[string]string
	lastPreparedLLMNextReviewAge   []int
	lastPreparedLLMToolResults     int
	lastPreparedReductionPolicy    requesttrim.Policy
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

	cacheExpectations promptcache.Expectations
	cacheHitTracker   *promptcache.HitTracker

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
	// usageObservationModelRef records which model's window produced the current
	// ctxmgr size observation; empty means no observation, or one restored
	// without a model identity. A model change only invalidates an observation
	// measured against another window, so a fallback response's own usage
	// survives the next config application and can arm against its line.
	usageObservationModelRef string
	instanceID               string
	mcpClientInfo            mcp.ClientInfo
	globalIdle               atomic.Bool
	realWorkEpoch            atomic.Uint64
	lastIdleWorkEpoch        uint64
	lastIdleTurnID           atomic.Uint64

	// turnMu protects the turn pointer for cross-goroutine access.
	// The event-loop goroutine writes turn in newTurn(); external goroutines
	// (TUI, shutdown) read it via CancelCurrentTurn() / Shutdown().
	turnMu sync.Mutex

	// llmMu protects llmClient, modelName, providerModelRef,
	// appliedCompactionModelRef, usageObservationModelRef, running-model
	// continuity, and model-run cache-warmth state for cross-goroutine access.
	// The TUI goroutine reads ModelName() and ProviderModelRef() from View(),
	// while SwapLLMClient / SwitchModel write these fields. callLLM snapshots
	// under RLock at the start to ensure consistent model name for hooks and
	// usage tracking. Model writers
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
	compactionSlotActive atomic.Bool
	// Pending pool writes hold stateMu so display snapshots can read them.
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
	pathRootsResolver    PathRootsResolver
	pathRootsInvalidator func()
	pathRoots            atomic.Pointer[pathRootsSnapshot]
	lastPlanPath         string
	pendingHandoff       *HandoffResult // deferred Handoff action; processed after all sibling tools finish
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
	// All three fields are written by the event-loop queue paths and cleared
	// or restaged by the request assembly on the main LLM goroutine, so they
	// are guarded by overlayClaims.mu (see context_overlays.go).
	pendingContextPressureReminder string
	pendingCompactionWarning       string
	// pendingCompactionImminent is the grace-period "compaction imminent"
	// notice. It is re-queued on every request inside the threshold grace
	// window with the true remaining countdown (compaction_grace.go).
	pendingCompactionImminent string
	// pendingCompactionManual is the manual /compact imperative notice staged
	// by the compaction gate while the armed intent's worker is still running
	// (compaction_manual_intent.go).
	pendingCompactionManual string
	// manualCompactArmed records that a busy /compact armed a manual compaction
	// intent while compact_context was visible: the model may submit its
	// checkpoint before the summarize worker applies. The flag lives from the
	// command until an apply, a manual worker terminal settle, a turn
	// cancellation, or a session switch/restore; usage-driven resets (usage
	// falling back, the failure breaker, a model switch) never clear it. It
	// only ever means "the user's compaction request has no result yet".
	// Written on the event loop, read from the main LLM goroutine
	// (reconcile/stage paths), hence the atomics.
	manualCompactArmed atomic.Bool
	// manualCompactArmedPlanID is the plan id of the worker the armed /compact
	// started. Only a running compaction with exactly this plan id may write
	// the manual notice row; restarted workers keep the intent alive but
	// permanently lose injection rights.
	manualCompactArmedPlanID atomic.Uint64
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
	// contextNoticesStale marks notices withdrawn after prefix reduction,
	// model changes, or disabling the notification contract. Request filtering
	// is immediate; durable removal waits for an idle boundary.
	contextNoticesStale atomic.Bool
	// contextNoticeWithdrawalScope selects which threshold rows are withdrawn.
	contextNoticeWithdrawalScope atomic.Uint32
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
	mailboxDeliveryPaused    atomic.Bool         // restored sessions and user-cancelled turns hold mailbox delivery until an explicit user action
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
	mcpServerCacheMu     sync.Mutex
	mcpServerCache       map[string]*mcpServerEntry
	mcpRuntimeMain       *mcp.Manager
	mcpRuntimeAuth       *mcp.AuthService
	mcpRuntimeTargets    []mcp.Target
	mcpRuntimeConfigHome string
	mcpRuntimeGeneration uint64

	// Custom slash commands loaded from MD files / YAML config.
	customCommandsMu sync.RWMutex
	customCommands   []*command.Definition

	// Todo state (implements tools.TodoStore).
	todoItems []tools.TodoItem
	todoMu    sync.RWMutex

	// Minimal loop-controller runtime state for post-assistant stop assessment.
	loopState loopRuntimeState
	// pendingLoopContinuation is a request-scoped continuation note surfaced via
	// turn overlays for the next LLM request and consumed by that request's
	// overlay assembly. It must not be re-persisted as a synthetic user message
	// after assistant turns that already emitted tool calls; only terminal
	// assistant stops without tool calls may inject a new runtime user
	// continuation message. Guarded by loopReductionMu.
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
	mcpAuthMu           sync.Mutex
	mcpAuthWg           sync.WaitGroup
	mcpAuthClosed       bool
	mcpAuthControlFn    func(context.Context, MCPAuthRequest, func(mcp.LoginProgress)) error
	mcpAuthOperations   map[mcp.ServerRef]*mcpAuthOperation
	mcpAuthOperationSeq uint64
	mcpPreparationHook  func(context.Context) error
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
	// prompt-cache reuse no longer applies and the restored history could
	// mis-anchor the dynamic declarations — session resume, forked history,
	// and durable compaction. The next session-head event
	// (resetSessionBuildState) clears it: a fresh, empty session run may mount
	// dynamically again.
	mcpMountFullInjectionOnly atomic.Bool
	sessionBuilt              atomic.Bool

	// shuttingDown is set to true when Shutdown begins. UpdateTodos checks
	// this flag to avoid overwriting the final snapshot.
	shuttingDown atomic.Bool

	// runMu orders Run admission against Shutdown. Once runClosed is set,
	// started cannot transition from false to true.
	runMu     sync.Mutex
	runClosed bool
	// started records an admitted Run; Shutdown waits for its done signal
	// before reading loop-owned state.
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

	// hostedBackend routes hosted tool sub-requests. Set once during startup
	// when the hosted catalog is registered; nil when no hosted tools exist.
	// Reads happen after startup (tool dispatch and SubAgent spawn), so the
	// plain write is ordered before any reader.
	hostedBackend tools.HostedToolBackend
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
	cachedGitStatus string        // populated lazily via gitStatusReady
	cachedVenvPath  string        // absolute path to detected Python virtual environment, or ""
	gitStatusReady  chan struct{} // closed when cachedGitStatus is set
	// workDirState is the active checkout of this agent. A checkout switch
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
	memoryManual   memoryManualState
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
	// memoryLoaded is the latest summary read from disk (init, background
	// commit); memoryApplied is the one memoryActive and cachedMemoryReminder
	// currently reflect. They differ while a commit waits for the next
	// prompt-cache break (see applyLoadedMemory).
	memoryLoaded         atomic.Pointer[memorySnapshot]
	memoryApplied        atomic.Pointer[memorySnapshot]
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
