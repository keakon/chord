package agent

import (
	"strings"

	"github.com/keakon/chord/internal/message"
)

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
		if strings.TrimSpace(msg.ToolDiff) != "" && !notesStateOnlyToolDiff(msg) {
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

// notesStateOnlyToolDiff reports whether a completed file-editing tool result
// wrote only agent-owned note or plan documents (.chord/notes, .chord/plans).
// Those writes are task state, not code: the checkpoint re-loads the documents
// the model registered as state files, so an evidence excerpt would only
// restate content the continuation already receives, while taking the pack's
// single required diff slot — and the notes write tends to be the newest diff
// exactly when compaction fires. A call that also touched checkout content
// keeps its diff, and a result whose write targets were not recorded keeps it
// too: an unclassifiable diff stays evidence instead of being dropped on a
// guess.
func notesStateOnlyToolDiff(msg message.Message) bool {
	state := msg.FileState
	if state == nil {
		return false
	}
	matched := false
	for _, write := range state.Writes {
		if strings.TrimSpace(write.Path) == "" {
			continue
		}
		if !isNotesStatePath(write.Path) {
			return false
		}
		matched = true
	}
	for _, change := range state.Changes {
		for _, path := range []string{change.Path, change.TargetPath} {
			if strings.TrimSpace(path) == "" {
				continue
			}
			if !isNotesStatePath(path) {
				return false
			}
			matched = true
		}
	}
	return matched
}
