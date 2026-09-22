package agent

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
)

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
	// originAcceptedOrder is the arrival order of the raw main-agent user
	// message that started this turn, or 0 when something else started it
	// (slash commands, queued mailbox deliveries, restores). The event-loop
	// goroutine alone reads and writes it, so a cancel request queued while no
	// turn existed can tell whether it covers the turn in front of it.
	originAcceptedOrder int64
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
	// streamingToolEmitAt records the last TUI args-update flush per call ID so
	// per-fragment ToolCallUpdateEvents coalesce to the flush cadence.
	// Protected by streamingToolMu.
	streamingToolEmitAt map[string]time.Time
	// permissionApprovals caches non-interactive allow decisions recorded by
	// the speculative-reuse prefilter so the finalize path skips a second
	// evaluation with identical inputs. Guarded by permissionApprovalsMu;
	// entries live for the turn.
	permissionApprovalsMu sync.Mutex
	permissionApprovals   map[string]permApprovalRecord
	// partialText accumulates assistant text streamed during the current LLM
	// round so it can be saved to history if the stream is interrupted before
	// a normal DeltaStop. Protected by partialTextMu because the stream
	// callback runs on a separate goroutine from the event loop.
	partialTextMu sync.Mutex
	partialText   strings.Builder
	// partialProducingRef records the provider/model ref confirmed to have
	// produced visible output for the current streaming round (key_confirmed).
	// It follows partialText's lifecycle and is deliberately independent of the
	// sidebar identity, which realigns to the sticky cursor when a request ends
	// without a confirmed switch: an interrupted partial reply keeps the model
	// that actually wrote it.
	partialProducingRef string
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
	// SubAgentResultContractRecoveryCount bounds the follow-up for a completion
	// rejected by the task's declared result contract to one request. It is
	// separate from the invalid-arguments budget so a JSON-level mistake cannot
	// eat the model's one chance to correct the delivered shape, and a shape
	// mistake cannot eat the chance to correct the arguments: the two failure
	// classes are independent, and a shared budget would strand whichever came
	// second without a repair.
	SubAgentResultContractRecoveryCount int
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

	// argsFragBuf accumulates streamed JSON argument fragments (Anthropic
	// input_json_delta, OpenAI function.arguments, Responses
	// function_call_arguments.delta). The builder keeps per-fragment cost
	// amortized O(1); ArgsJSON materializes from it on demand as an owned copy
	// (see materializeStreamingToolCallArgsLocked) because the builder keeps
	// growing on the streaming goroutine and its buffer must not escape as a
	// string. argsLenAtMaterialize is the builder length at the last
	// materialization, so repeated reads between fragments skip the re-clone.
	argsFragBuf          *strings.Builder
	argsLenAtMaterialize int

	// inputTextBuf accumulates freeform input fragments (Responses custom
	// apply_patch deltas) under the same contract as argsFragBuf: InputText
	// materializes on demand as an owned copy, and inputTextLenAtMaterialize
	// skips the re-clone between fragments.
	inputTextBuf              *strings.Builder
	inputTextLenAtMaterialize int
}

// pendingUserMessage holds a single queued user message when the agent is busy.
// When Parts is non-nil it is a multi-part message (e.g. text + images); otherwise Content is used.
type pendingUserMessage struct {
	// AcceptedOrder is the arrival order of the raw main-agent user message this
	// entry queued, or 0 for entries that are not raw user messages. A drain
	// matches it against the cancel watermark so a cancel also covers a message
	// that was queued before its turn could start.
	AcceptedOrder       int64
	DraftID             string
	Content             string
	Parts               []message.ContentPart
	Kind                string
	FromUser            bool
	MailboxAckID        string
	Mailbox             *message.MailboxMetadata
	DrainContextAppends bool
}
