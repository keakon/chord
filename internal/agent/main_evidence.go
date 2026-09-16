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
