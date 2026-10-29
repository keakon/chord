package agent

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

// SubAgent model-driven compaction: the worker-variant compact_context tool,
// its acceptance path, and the synchronous apply at the batch-close point.
//
// The worker keeps its own light barrier: an accepted request is validated
// before the tool-result event is built, parked on the turn, and consumed at
// the single batch-close point in handleToolResult. compact_context must be
// the only tool call in its response, so that point is the barrier — no async
// worker, evidence pack or continuation machinery is shared with the
// MainAgent.

// subModelDrivenRequest is an accepted compact_context request waiting to be
// applied at the batch-close point. Event-loop-goroutine only; it dies with
// the turn that armed it.
type subModelDrivenRequest struct {
	CallID string
	Args   tools.CompactContextArgs
}

// subModelDrivenCheckpointTailTokens is the estimated-token budget for the
// real recent messages kept verbatim alongside a model-driven checkpoint: the
// smallest tail that still gives the continuation immediate conversational
// context instead of only the checkpoint's prose.
const subModelDrivenCheckpointTailTokens = 2048

// subCheckpointArchiveRefPlaceholder stands in for the archive reference while
// estimating the checkpoint body before the archive exists. Its length is
// close to a real reference path, so the target derived from it does not
// systematically under- or over-reserve.
const subCheckpointArchiveRefPlaceholder = "context-recovery-artifacts/reference-placeholder.json"

// subModelDrivenSkipReason carries why an accepted checkpoint request was not
// applied, for the one-shot transient notice.
const (
	subModelDrivenSkipShortHistory    = "the session history is too short to compact"
	subModelDrivenSkipLowGain         = "projected savings %d tokens is below the low-gain gate (%d tokens and %d%% of the current context)"
	subModelDrivenSkipCompression     = "the history cannot be compacted to the target size"
	subModelDrivenSkipDropsPauseInput = "compression would drop inputs whose delivery is not durable yet"
	subModelDrivenSkipArchiveFailed   = "the pre-checkpoint history could not be archived"
	subModelDrivenSkipRewriteFailed   = "session history could not be rewritten durably"
	subModelDrivenApplyReason         = "model-requested context checkpoint"
)

// registerWorkerCompactContextTool registers the worker-variant
// compact_context tool on this worker when the parent's model-driven feature
// is enabled, the base registry carries the tool, and the role ruleset does
// not deny it. It must run after the ruleset and workDirState are installed
// and before buildSystemPrompt / frozenToolDefs, so the SubAgent's tool
// surface hash and frozen prompt prefix include the variant and the
// state_files resolution anchors at this worker's own checkout.
func (s *SubAgent) registerWorkerCompactContextTool(parent *MainAgent, base *tools.Registry, ruleset permission.Ruleset) {
	if s == nil || parent == nil || base == nil {
		return
	}
	if !parent.ModelDrivenCompactionEnabled() {
		return
	}
	if _, ok := base.Get(tools.NameCompactContext); !ok {
		return
	}
	if compactContextPermissionAction(ruleset) == permission.ActionDeny {
		return
	}
	s.workerCompactContextValidator = tools.CompactContextValidator{
		WorkerSurface:              true,
		ContinuationStateMaxTokens: CompactContinuationStateMaxTokens,
		// state_files spellings resolve against this worker's active checkout,
		// not the parent's: a delegated worker can run in a different
		// worktree, and the effective base dir follows its checkout switches.
		WorkDir:        s.effectiveToolBaseDir,
		EstimateTokens: s.estimateTokensForText,
	}
	s.tools.Register(tools.NewCompactContextTool(s.workerCompactContextValidator))
	s.compactContextRegistered = true
}

// estimateTokensForText exposes this worker's usage-calibrated estimator for
// plain text so the registered tool and the event-loop revalidation apply the
// same continuation-state budget with the same accounting convention as this
// worker's other context-pressure decisions.
func (s *SubAgent) estimateTokensForText(text string) int {
	return estimateMessageTokens(s.ctxMgr, message.Message{Role: message.RoleUser, Content: text})
}

// validateModelDrivenCheckpointRequest validates an executed compact_context
// call before its tool-result event is built: an active turn, healthy
// persistence, the sole-tool-call contract, and the same argument parser the
// registered tool ran. Called on the SubAgent event loop.
func (s *SubAgent) validateModelDrivenCheckpointRequest(callID, rawArgs string) (tools.CompactContextArgs, error) {
	if s.turn == nil {
		return tools.CompactContextArgs{}, fmt.Errorf("compact_context requires an active turn")
	}
	if s.PersistenceHealth().State == PersistenceDegraded {
		return tools.CompactContextArgs{}, fmt.Errorf("session persistence is degraded; compact_context cannot rewrite session history safely")
	}
	if !compactContextSoleToolCall(s.ctxMgr.Snapshot(), callID) {
		return tools.CompactContextArgs{}, fmt.Errorf("compact_context must be the only tool call in its assistant response; sibling tool calls are not allowed")
	}
	return s.workerCompactContextValidator.ParseCompactContextArgs(json.RawMessage(rawArgs))
}

// consumePendingModelDrivenCompaction consumes the turn's accepted checkpoint
// request at the batch-close point, before the abort/pause/escalate early
// exits. Every outcome clears the pending state. Continue paths (including
// entering the join wait and a paused return) apply the checkpoint; a turn
// about to abort drops it — rewriting history for a turn that ends here is
// wasted work and the model gets no continuation it could act on.
func (s *SubAgent) consumePendingModelDrivenCompaction() {
	if s == nil || s.turn == nil {
		return
	}
	req := s.turn.PendingModelDrivenCompaction
	if req == nil {
		return
	}
	s.turn.PendingModelDrivenCompaction = nil
	if s.turn.MalformedCount >= maxMalformedToolCalls || s.turn.BarrierFailureRounds >= maxIntentBarrierFailureRounds {
		log.Warnf("SubAgent dropping model-driven checkpoint request on abort path agent=%v turn_id=%v", s.instanceID, s.turn.ID)
		return
	}
	s.applyModelDrivenCheckpoint(req)
}

// applyModelDrivenCheckpoint compacts this worker's history to the
// model-authored checkpoint. The target is derived from the checkpoint body's
// own estimate plus a minimal retained tail — never a fixed ratio — so the
// result always fits the checkpoint the model wrote. Skip and failure settle
// as a transient notice plus a toast; success leaves the verification
// guidance for the next request.
func (s *SubAgent) applyModelDrivenCheckpoint(req *subModelDrivenRequest) {
	messages := s.ctxMgr.Snapshot()
	if len(messages) <= 2 {
		s.settleModelDrivenSkip(subModelDrivenSkipShortHistory)
		return
	}
	estimated := ctxmgr.EstimateMessagesTokens(messages)
	checkpoint := newSubModelDrivenCheckpointContent(s, messages, req.Args, subModelDrivenApplyReason)
	checkpointTokens := ctxmgr.EstimateMessageTokens(message.Message{
		Role:    message.RoleUser,
		Content: checkpoint.build(0, subCheckpointArchiveRefPlaceholder),
	})
	target := modelDrivenCheckpointTarget(messages, req.CallID)
	projected := target + checkpointTokens
	savings := estimated - projected
	if _, skip := modelDrivenLowGainCheck(estimated, savings); skip {
		s.settleModelDrivenSkip(fmt.Sprintf(subModelDrivenSkipLowGain, savings, modelDrivenLowGainMinTokens, int(modelDrivenLowGainMinRatio*100)))
		return
	}
	if _, ok, skipReason := s.compactContextForTargetWithCheckpoint(messages, target, subModelDrivenApplyReason, checkpoint.build); !ok {
		s.settleModelDrivenSkip(skipReason)
		return
	}
	// Success: the next request carries the reading rule so the continuation
	// starts from the checkpoint instead of re-exploring.
	s.setCompactionTransient("A model-driven context checkpoint was applied; continue the delegated task on the compacted context, processing any new input or tool results first. If the task is complete, report completion without another checkpoint. " + subCheckpointVerificationGuidance)
}

// modelDrivenCheckpointTarget returns the CompressForTarget budget for a
// model-driven apply: the first message plus a retained tail of at least
// subModelDrivenCheckpointTailTokens that always covers the compact_context
// call and its result. That pair is the newest tail and is about as large as
// the checkpoint arguments (up to CompactContinuationStateMaxTokens); a fixed
// tail budget smaller than the pair leaves CompressForTarget nothing it can
// keep, so large checkpoints would always be skipped. The budget uses the raw
// estimate CompressForTarget itself applies, so calibration cannot shrink it
// below the pair.
func modelDrivenCheckpointTarget(messages []message.Message, callID string) int {
	tail := 0
	for i := len(messages) - 1; i >= 1; i-- {
		tail += ctxmgr.EstimateMessageTokens(messages[i])
		if slices.ContainsFunc(messages[i].ToolCalls, func(tc message.ToolCall) bool { return tc.ID == callID }) {
			break
		}
	}
	return ctxmgr.EstimateMessageTokens(messages[0]) + max(tail, subModelDrivenCheckpointTailTokens)
}

// settleModelDrivenSkip queues the one-shot "not applied" transient and toasts
// the reason, mirroring the MainAgent's continuation notice: the model learns
// why the checkpoint did not apply without the reason becoming a durable user
// message a later compaction could misread as the latest request.
func (s *SubAgent) settleModelDrivenSkip(reason string) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = subModelDrivenSkipCompression
	}
	if !strings.HasSuffix(reason, ".") {
		reason += "."
	}
	s.setCompactionTransient("Context checkpoint not applied: " + reason + " The session continues on the previous context.")
	if s.parent != nil {
		s.parent.emitToTUI(ToastEvent{Message: "Context checkpoint not applied: " + strings.TrimSuffix(reason, "."), Level: "info", AgentID: s.instanceID})
	}
}

// setCompactionTransient stores the one-shot request overlay; the next request
// assembly consumes it.
func (s *SubAgent) setCompactionTransient(text string) {
	s.pendingCompactionTransientMu.Lock()
	s.pendingCompactionTransient = text
	s.pendingCompactionTransientMu.Unlock()
}

// takeCompactionTransient consumes the pending checkpoint-outcome overlay.
func (s *SubAgent) takeCompactionTransient() string {
	s.pendingCompactionTransientMu.Lock()
	defer s.pendingCompactionTransientMu.Unlock()
	text := s.pendingCompactionTransient
	s.pendingCompactionTransient = ""
	return text
}

// subCheckpointContentBuilder renders the checkpoint body for one compaction.
// removed is the number of messages the compaction drops and archiveRef the
// address of the archived history; both are only known mid-compaction.
type subCheckpointContentBuilder func(removed int, archiveRef string) string

type subModelDrivenCheckpointContent struct {
	args   tools.CompactContextArgs
	reason string
	facts  subAgentCheckpointRuntimeFacts
}

func newSubModelDrivenCheckpointContent(s *SubAgent, messages []message.Message, args tools.CompactContextArgs, reason string) subModelDrivenCheckpointContent {
	if s != nil {
		messages = projectSkillDiscoveryMessages(messages, s.skillActivationView())
	}
	return subModelDrivenCheckpointContent{
		args:   args,
		reason: reason,
		facts:  buildSubAgentCheckpointRuntimeFacts(s, messages),
	}
}

// build renders the checkpoint body with the stable contract prefix, the
// model's continuation state, the skills line, shared runtime facts, and the
// archive trailer.
func (c subModelDrivenCheckpointContent) build(removed int, archiveRef string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[system] SubAgent context checkpoint: %d earlier messages were removed for %s.\n", removed, c.reason)
	b.WriteString("Model-reported continuation state:\n")
	b.WriteString("- Active objective: ")
	b.WriteString(strings.TrimSpace(c.args.ActiveObjective))
	b.WriteByte('\n')
	b.WriteString("- Next step: ")
	b.WriteString(strings.TrimSpace(c.args.NextStep))
	b.WriteByte('\n')
	writeCheckpointList(&b, "- Completed:", c.args.Completed)
	writeCheckpointList(&b, "- Decisions:", c.args.Decisions)
	writeCheckpointList(&b, "- Open problems:", c.args.OpenIssues)
	b.WriteString("- State files: ")
	if len(c.args.StateFiles) == 0 {
		b.WriteString("none")
	} else {
		b.WriteString(strings.Join(c.args.StateFiles, ", "))
		b.WriteString(" (read these before relying on their contents)")
	}
	b.WriteByte('\n')
	b.WriteString(subAgentCheckpointSkillsPrefix)
	b.WriteString(c.facts.skills)
	b.WriteByte('\n')
	b.WriteString("Runtime facts:\n")
	b.WriteString("- Task: ")
	b.WriteString(c.facts.task)
	b.WriteByte('\n')
	b.WriteString("- Owner: ")
	b.WriteString(c.facts.owner)
	b.WriteByte('\n')
	b.WriteString("- Latest owner/user instruction: ")
	b.WriteString(c.facts.latestInstruction)
	b.WriteByte('\n')
	b.WriteString("- Files read or changed: ")
	b.WriteString(c.facts.files)
	b.WriteByte('\n')
	b.WriteString("- Known failures: ")
	b.WriteString(c.facts.failures)
	b.WriteByte('\n')
	b.WriteString("- Open blocker: ")
	b.WriteString(c.facts.blocker)
	b.WriteByte('\n')
	fmt.Fprintf(&b, "Full pre-checkpoint history: %s.", archiveRef)
	return b.String()
}

// writeCheckpointList renders one model-authored list field as a heading with
// per-item lines, or a single "none" heading when the model left it empty.
func writeCheckpointList(b *strings.Builder, heading string, items []string) {
	b.WriteString(heading)
	if len(items) == 0 {
		b.WriteString(" none\n")
		return
	}
	b.WriteByte('\n')
	for _, item := range items {
		b.WriteString("  - ")
		b.WriteString(strings.TrimSpace(item))
		b.WriteByte('\n')
	}
}
