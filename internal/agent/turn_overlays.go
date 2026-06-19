package agent

import (
	"strings"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

const pendingLSPDiagnosticOverlayText = "LSP diagnostics changed after one or more recent Edit/Write tool calls. Review the affected tool results' LSPReviews, treat blocking diagnostics in directly modified files as regressions to fix before finishing unless the user explicitly asked for a partial/WIP result, and keep any cleanup small and low-risk without expanding scope to unrelated untouched files."

// buildTurnOverlayMessages assembles per-request meta user messages that
// deliver transient context — SubAgent mailbox, bug triage hint, loop
// continuation — without touching the stable system prompt or ctxMgr. Each
// returned message is wrapped as <system-reminder>...</system-reminder> and
// prepended to the real user turn by callLLM. None of them are persisted to
// the session jsonl.
func (a *MainAgent) buildTurnOverlayMessages() []message.Message {
	var overlays []message.Message

	if block := strings.TrimSpace(a.buildCoordinationSnapshotOverlay()); block != "" {
		overlays = append(overlays, message.Message{
			Role:    "user",
			Content: "<system-reminder>\n" + block + "\n</system-reminder>",
		})
	}

	if msgs := a.takePendingSubAgentMailboxes(); len(msgs) > 0 {
		parts := make([]string, 0, len(msgs))
		for _, m := range msgs {
			if m == nil {
				continue
			}
			parts = append(parts, formatSubAgentMailboxInjectionText(m))
		}
		if len(parts) > 0 {
			overlays = append(overlays, message.Message{
				Role:    "user",
				Content: "<system-reminder>\n" + strings.Join(parts, "\n\n---\n\n") + "\n</system-reminder>",
			})
		}
	}

	if block := strings.TrimSpace(a.bugTriagePromptBlock()); block != "" {
		overlays = append(overlays, message.Message{
			Role:    "user",
			Content: "<system-reminder>\n" + block + "\n</system-reminder>",
		})
	}

	if block := strings.TrimSpace(a.pendingLoopContinuationPromptBlock()); block != "" {
		overlays = append(overlays, message.Message{
			Role:    "user",
			Content: "<system-reminder>\n" + block + "\n</system-reminder>",
		})
	}

	if block := strings.TrimSpace(a.takePendingLSPDiagnosticOverlay()); block != "" {
		overlays = append(overlays, message.Message{
			Role:    "user",
			Content: "<system-reminder>\n" + block + "\n</system-reminder>",
		})
	}

	// Recovery prompt from length-recovery auto compaction: it is request-scoped
	// overlay that should not persist to durable context.
	if recoveryPrompt := a.takePendingRecoveryPrompt(); recoveryPrompt != "" {
		overlays = append(overlays, message.Message{
			Role:    "user",
			Content: "<system-reminder>\n" + recoveryPrompt + "\n</system-reminder>",
		})
	}

	// Auto-continue prompt from usage-driven / oversize-driven compaction: keep it
	// request-scoped so the durable session history remains a clean compressed
	// summary, while the next turn explicitly resumes the task.
	if block := strings.TrimSpace(a.pendingAutoContinuePrompt); block != "" {
		overlays = append(overlays, message.Message{
			Role:    "user",
			Content: "<system-reminder>\n" + block + "\n</system-reminder>",
		})
		a.pendingAutoContinuePrompt = ""
	}
	if block := strings.TrimSpace(a.pendingAutoContinueReplayPrompt); block != "" {
		overlays = append(overlays, message.Message{
			Role:    "user",
			Content: "<system-reminder>\n" + block + "\n</system-reminder>",
		})
		a.pendingAutoContinueReplayPrompt = ""
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
	if payload.Name != tools.NameEdit && payload.Name != tools.NamePatch && payload.Name != tools.NameWrite {
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
	for i := len(history) - 1; i >= 0; i-- {
		msg := history[i]
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

// injectTurnOverlays prepends the turn overlays before the first user message
// (or at the head if no user message exists). Overlays are meta — they are not
// stored in ctxMgr or persisted. Returns a new slice when overlays are
// injected, otherwise the original slice unchanged.
func injectTurnOverlays(messages []message.Message, overlays []message.Message) []message.Message {
	if len(overlays) == 0 {
		return messages
	}
	insertAt := max(firstUserMessageIndex(messages), 0)
	out := make([]message.Message, 0, len(messages)+len(overlays))
	out = append(out, messages[:insertAt]...)
	out = append(out, overlays...)
	out = append(out, messages[insertAt:]...)
	return out
}
