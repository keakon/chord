package agent

import (
	"maps"
	"slices"
	"strings"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

// The full policy lives in the stable system prompt's "LSP diagnostic
// follow-up" section (takePendingLSPDiagnosticOverlay only fires when that
// section is injected), so this one-shot reminder just points back at it.
const pendingLSPDiagnosticOverlayText = "LSP diagnostics changed after one or more recent file-editing tool calls. Review the affected tool results' LSPReviews and apply the LSP diagnostic follow-up rules from the system prompt."

// buildTurnOverlayMessages assembles meta user messages appended after the real
// user turn by callLLM. SubAgent mailbox messages are also appended to ctxMgr
// and persisted because they are real owner-visible model input. Other runtime
// hints remain request-scoped overlays.
func (a *MainAgent) buildTurnOverlayMessages() []message.Message {
	var overlays []message.Message

	// Take the pending mailbox batch up front: together with the mailbox
	// messages already durable in the conversation it defines which mailbox
	// messages this request carries, and the coordination snapshot must not
	// repeat a terminal completion that one of them already expresses.
	//
	// The durable half needs a snapshot of the whole conversation, which copies
	// every message header in the session. Nothing below reads it unless this
	// session has SubAgents, so a session without them must not pay for it on
	// every request.
	pendingMailboxes := a.takePendingSubAgentMailboxes()
	var durableMailboxIDs map[string]struct{}
	if len(pendingMailboxes) > 0 || a.hasCoordinationTaskRecords() {
		durableMailboxIDs = conversationMailboxIDs(a.ctxMgr.Snapshot())
	}

	// Stall markers feed both the relevance filter and the rendered stall lines
	// of the coordination snapshot, so they are refreshed here at the
	// request-dispatch boundary rather than inside
	// buildCoordinationSnapshotOverlay, keeping that formatter free of state
	// writes. Because this refresh always precedes the snapshot, the stored
	// SuspectedStallReason stays a live decision input for it.
	a.updateSubAgentStallMarkers()

	if block := strings.TrimSpace(a.buildCoordinationSnapshotOverlayForRequest(requestInjectedMailboxIDs(durableMailboxIDs, pendingMailboxes))); block != "" {
		overlays = append(overlays, message.Message{
			Role:    "user",
			Kind:    message.KindTurnOverlay,
			Content: "<system-reminder>\n" + block + "\n</system-reminder>",
		})
	}

	if len(pendingMailboxes) > 0 {
		for _, mailbox := range pendingMailboxes {
			if mailbox == nil {
				continue
			}
			content := strings.TrimSpace(formatSubAgentMailboxInjectionText(mailbox))
			if content == "" {
				continue
			}
			msg := subAgentMailboxConversationMessage(mailbox, "<system-reminder>\n"+content+"\n</system-reminder>")
			if id := strings.TrimSpace(mailbox.MessageID); id == "" || !mapContains(durableMailboxIDs, id) {
				a.ctxMgr.Append(msg)
				a.persistAsync("main", msg)
				if id != "" {
					durableMailboxIDs[id] = struct{}{}
				}
			}
			overlays = append(overlays, msg)
		}
	}

	if block := strings.TrimSpace(a.bugTriagePromptBlock()); block != "" {
		overlays = append(overlays, message.Message{
			Role:    "user",
			Kind:    message.KindTurnOverlay,
			Content: "<system-reminder>\n" + block + "\n</system-reminder>",
		})
	}

	// Model-driven checkpoint outcome (skip/failure/cancel): request-scoped
	// overlay so the model learns why the checkpoint did not apply without the
	// reason becoming a durable user message that a later compaction could
	// misread as the latest request.
	if notice := strings.TrimSpace(a.pendingModelDrivenNotice); notice != "" {
		a.pendingModelDrivenNotice = ""
		overlays = append(overlays, message.Message{
			Role:    "user",
			Kind:    message.KindTurnOverlay,
			Content: "<system-reminder>\n" + notice + "\n</system-reminder>",
		})
	}
	if a.stageCompletionCandidatePending {
		a.stageCompletionCandidatePending = false
		overlays = append(overlays, message.Message{Role: "user", Kind: message.KindTurnOverlay, Content: "<system-reminder>\nA task stage has reached a terminal TODO state. Decide whether earlier exploration is still needed; if not, record verified continuation state and request a provisional context checkpoint with compact_context. Do not claim completion without evidence.\n</system-reminder>"})
	}

	// Context-pressure reminder (sticky per compaction window — full text once,
	// then the short text — until the model calls compact_context or the
	// window resets), the grace-period imminent notice (sticky per deferred
	// request during the grace window) and the usage-driven externalization
	// warning (one-shot per auto-compact request generation). They are
	// turn-tail overlays queued by beginMainLLMAfterPreparation /
	// usageDrivenCompactionGraceDefers and consumed here; the delivered claim
	// is confirmed at dispatch, so attaching here only marks deliveryPending —
	// a request cancelled before dispatch leaves the claim reusable. They
	// carry bare text and are wrapped in the same <system-reminder> runtime
	// message block as every other harness injection so the model can tell
	// them apart from user-written messages.
	if reminder := strings.TrimSpace(a.pendingContextPressureReminder); reminder != "" {
		a.pendingContextPressureReminder = ""
		a.noteContextPressureReminderAttached()
		overlays = append(overlays, message.Message{
			Role:    "user",
			Kind:    message.KindTurnOverlay,
			Content: "<system-reminder>\n" + reminder + "\n</system-reminder>",
		})
	}
	if imminent := strings.TrimSpace(a.pendingCompactionImminent); imminent != "" {
		a.pendingCompactionImminent = ""
		a.noteCompactionImminentAttached()
		overlays = append(overlays, message.Message{
			Role:    "user",
			Kind:    message.KindTurnOverlay,
			Content: "<system-reminder>\n" + imminent + "\n</system-reminder>",
		})
	}
	if warning := strings.TrimSpace(a.pendingCompactionWarning); warning != "" {
		a.pendingCompactionWarning = ""
		a.noteCompactionWarningAttached()
		overlays = append(overlays, message.Message{
			Role:    "user",
			Kind:    message.KindTurnOverlay,
			Content: "<system-reminder>\n" + warning + "\n</system-reminder>",
		})
	}

	if block := strings.TrimSpace(a.pendingLoopContinuationPromptBlock()); block != "" {
		overlays = append(overlays, message.Message{
			Role:    "user",
			Kind:    message.KindTurnOverlay,
			Content: "<system-reminder>\n" + block + "\n</system-reminder>",
		})
	}

	if block := strings.TrimSpace(a.takePendingLSPDiagnosticOverlay()); block != "" {
		overlays = append(overlays, message.Message{
			Role:    "user",
			Kind:    message.KindTurnOverlay,
			Content: "<system-reminder>\n" + block + "\n</system-reminder>",
		})
	}

	// Recovery prompt from length-recovery auto compaction: it is request-scoped
	// overlay that should not persist to durable context.
	if recoveryPrompt := a.takePendingRecoveryPrompt(); recoveryPrompt != "" {
		overlays = append(overlays, message.Message{
			Role:    "user",
			Kind:    message.KindTurnOverlay,
			Content: "<system-reminder>\n" + recoveryPrompt + "\n</system-reminder>",
		})
	}

	// Auto-continue prompt from usage-driven / oversize-driven compaction: keep it
	// request-scoped so the durable session history remains a clean compressed
	// summary, while the next turn explicitly resumes the task. The continue and
	// replay notes share one reminder because they always describe the same
	// compaction event.
	autoContinueParts := make([]string, 0, 2)
	if block := strings.TrimSpace(a.pendingAutoContinuePrompt); block != "" {
		autoContinueParts = append(autoContinueParts, block)
		a.pendingAutoContinuePrompt = ""
	}
	if block := strings.TrimSpace(a.pendingAutoContinueReplayPrompt); block != "" {
		autoContinueParts = append(autoContinueParts, block)
		a.pendingAutoContinueReplayPrompt = ""
	}
	if len(autoContinueParts) > 0 {
		overlays = append(overlays, message.Message{
			Role:    "user",
			Kind:    message.KindTurnOverlay,
			Content: "<system-reminder>\n" + strings.Join(autoContinueParts, "\n") + "\n</system-reminder>",
		})
	}

	return overlays
}

// takePendingRecoveryPrompt consumes the pending recovery prompt (one-shot).
// Returns the pending prompt if any, or empty string if none.
func (a *MainAgent) takePendingRecoveryPrompt() string {
	if a.pendingRecoveryPrompt == "" {
		return ""
	}
	prompt := a.pendingRecoveryPrompt
	a.pendingRecoveryPrompt = ""
	return prompt
}

// takePendingLSPDiagnosticOverlay consumes the pending LSP diagnostic overlay
// (one-shot). Returns the pending overlay if any, or empty string if none.
func (a *MainAgent) takePendingLSPDiagnosticOverlay() string {
	if a.pendingLSPDiagnosticOverlay == "" {
		return ""
	}
	if !a.shouldInjectLSPDiagnosticPrompt() {
		a.pendingLSPDiagnosticOverlay = ""
		return ""
	}
	prompt := a.pendingLSPDiagnosticOverlay
	a.pendingLSPDiagnosticOverlay = ""
	return prompt
}

func (a *MainAgent) queueLSPDiagnosticOverlay(history []message.Message, payload *ToolResultPayload) {
	if !shouldQueueLSPDiagnosticOverlay(history, payload) {
		return
	}
	a.pendingLSPDiagnosticOverlay = pendingLSPDiagnosticOverlayText
}

func shouldQueueLSPDiagnosticOverlay(history []message.Message, payload *ToolResultPayload) bool {
	if payload == nil {
		return false
	}
	if payload.Name != tools.NameEdit && payload.Name != tools.NameApplyPatch && payload.Name != tools.NameWrite {
		return false
	}
	if len(payload.LSPReviews) == 0 || !hasNonZeroLSPReviews(payload.LSPReviews) {
		return false
	}
	path := reviewedToolPayloadPath(payload)
	if path == "" {
		return false
	}
	prev, ok := latestLSPReviewsForPath(history, path)
	if !ok {
		return true
	}
	return !sameLSPReviews(prev, payload.LSPReviews)
}

func reviewedToolPayloadPath(payload *ToolResultPayload) string {
	if payload == nil {
		return ""
	}
	if payload.FileState != nil && len(payload.FileState.Writes) > 0 {
		return payload.FileState.Writes[0].Path
	}
	return extractHookFilePath([]byte(payload.ArgsJSON))
}

func latestLSPReviewsForPath(history []message.Message, path string) ([]message.LSPReview, bool) {
	for _, msg := range slices.Backward(history) {

		if len(msg.LSPReviews) == 0 {
			continue
		}
		if reviewedToolMessagePath(msg) != path {
			continue
		}
		return append([]message.LSPReview(nil), msg.LSPReviews...), true
	}
	return nil, false
}

func reviewedToolMessagePath(msg message.Message) string {
	if msg.FileState != nil && len(msg.FileState.Writes) > 0 {
		return msg.FileState.Writes[0].Path
	}
	return ""
}

func hasNonZeroLSPReviews(reviews []message.LSPReview) bool {
	for _, review := range reviews {
		if review.Errors > 0 || review.Warnings > 0 {
			return true
		}
	}
	return false
}

func sameLSPReviews(a, b []message.LSPReview) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// injectTurnOverlays appends transient turn overlays after the conversation
// tail. Durable mailbox messages do not use this path. Returns a new slice when
// overlays are injected, otherwise the original slice unchanged.
//
// The tail is the only cache-safe position for request-scoped content: these
// overlays vanish (or change) on the next request, so placing them anywhere
// earlier would change the prompt prefix at that point and invalidate every
// downstream prompt-cache breakpoint. Appending also puts the runtime hint
// closest to the work it applies to, and never separates an assistant tool_use
// from its tool_result.
func injectTurnOverlays(messages []message.Message, overlays []message.Message) []message.Message {
	if len(overlays) == 0 {
		return messages
	}
	out := make([]message.Message, 0, len(messages)+len(overlays))
	out = append(out, messages...)
	out = append(out, overlays...)
	return out
}

// applyTurnOverlayMessages appends durable mailbox overlays in their persisted
// conversation position, then appends the transient overlays after them. The
// second return value is the number of transient messages appended at the tail;
// callers use it to keep the newest prompt-cache breakpoint on the last durable
// message.
func applyTurnOverlayMessages(messages, overlays []message.Message) ([]message.Message, int) {
	transient := make([]message.Message, 0, len(overlays))
	for _, overlay := range overlays {
		if overlay.Kind == message.KindSubAgentMailbox {
			messageID := ""
			if overlay.Mailbox != nil {
				messageID = overlay.Mailbox.MessageID
			}
			if !containsSubAgentMailboxMessage(messages, messageID) {
				messages = append(messages, overlay)
			}
			continue
		}
		transient = append(transient, overlay)
	}
	return injectTurnOverlays(messages, transient), len(transient)
}

func containsSubAgentMailboxMessage(messages []message.Message, messageID string) bool {
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return false
	}
	for _, msg := range messages {
		if msg.Kind == message.KindSubAgentMailbox && msg.Mailbox != nil && strings.TrimSpace(msg.Mailbox.MessageID) == messageID {
			return true
		}
	}
	return false
}

// hasCoordinationTaskRecords reports whether this session has any SubAgent task
// record. It is the cheap precondition for the conversation snapshot the
// coordination overlay needs: with no records the overlay renders nothing, so
// the mailbox-dedupe set it would consume cannot change the request.
func (a *MainAgent) hasCoordinationTaskRecords() bool {
	if a == nil {
		return false
	}
	a.subs.mu.RLock()
	defer a.subs.mu.RUnlock()
	return len(a.subs.taskRecords) > 0
}

// conversationMailboxIDs collects the message IDs of the SubAgent mailbox
// messages already durable in the conversation (delivered on earlier requests).
// The set answers "was this mailbox already appended" in one pass instead of
// one scan of the conversation per pending mailbox.
func conversationMailboxIDs(conversation []message.Message) map[string]struct{} {
	ids := make(map[string]struct{})
	for _, msg := range conversation {
		if msg.Kind != message.KindSubAgentMailbox || msg.Mailbox == nil {
			continue
		}
		if id := strings.TrimSpace(msg.Mailbox.MessageID); id != "" {
			ids[id] = struct{}{}
		}
	}
	return ids
}

func mapContains(set map[string]struct{}, key string) bool {
	_, ok := set[key]
	return ok
}

// requestInjectedMailboxIDs collects the message IDs of every SubAgent mailbox
// already part of the request being assembled: the durable ones plus the
// pending batch about to be appended now. buildCoordinationSnapshotOverlay uses
// this set to skip terminal completions whose completed-mailbox text is in the
// same request.
func requestInjectedMailboxIDs(durableMailboxIDs map[string]struct{}, pendingMailboxes []*SubAgentMailboxMessage) map[string]struct{} {
	if len(durableMailboxIDs) == 0 && len(pendingMailboxes) == 0 {
		return nil
	}
	injected := make(map[string]struct{}, len(durableMailboxIDs)+len(pendingMailboxes))
	maps.Copy(injected, durableMailboxIDs)
	for _, mailbox := range pendingMailboxes {
		if mailbox == nil {
			continue
		}
		if id := strings.TrimSpace(mailbox.MessageID); id != "" {
			injected[id] = struct{}{}
		}
	}
	return injected
}
