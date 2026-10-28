package agent

import (
	"fmt"
	"strings"

	"github.com/keakon/chord/internal/message"
)

// Budget inputs are captured by the owner; workers only render the candidate
// rule set and price this immutable surface. In particular, this must not
// consume one-shot notices or append mailbox messages to the transcript.
type directoryCandidateBudgetInputs struct {
	system   string
	defs     []message.ToolDefinition
	budget   int
	overlays []message.Message
}

func (a *MainAgent) directoryCandidateInputs(history []message.Message) directoryCandidateBudgetInputs {
	a.llmMu.RLock()
	system := a.installedSysPrompt
	a.llmMu.RUnlock()
	in := directoryCandidateBudgetInputs{system: system, defs: a.mainLLMToolDefinitions(), budget: a.ctxMgr.GetInputBudget()}
	names := make(map[string]bool, len(in.defs))
	for _, d := range in.defs {
		names[d.Name] = true
	}
	for _, d := range a.effectiveRuntimeMCPDeclarations() {
		if !names[d.Name] {
			in.defs = append(in.defs, d)
			names[d.Name] = true
		}
	}
	if p := a.cachedSessionReminderContent.Load(); p != nil && *p != "" {
		in.overlays = append(in.overlays, message.Message{Role: message.RoleUser, Content: *p})
	}
	add := func(text string) {
		if text = strings.TrimSpace(text); text != "" {
			in.overlays = append(in.overlays, message.Message{Role: message.RoleUser, Kind: message.KindTurnOverlay, Content: message.SystemReminderOverlayText(text)})
		}
	}
	durableIDs := conversationMailboxIDs(history)
	for _, m := range a.pendingSubAgentMailboxes {
		if m == nil {
			continue
		}
		if m.MessageID != "" && mapContains(durableIDs, m.MessageID) {
			continue
		}
		if m.Kind == SubAgentMailboxKindBackgroundResult {
			add(m.Summary)
		} else {
			add(formatSubAgentMailboxInjectionText(m))
		}
	}
	add(a.buildCoordinationSnapshotOverlayForRequest(requestInjectedMailboxIDs(durableIDs, a.pendingSubAgentMailboxes)))
	for _, text := range []string{a.pendingModelDrivenNotice, a.pendingLSPDiagnosticOverlay, a.pendingRecoveryPrompt, a.pendingAutoContinuePrompt, a.pendingAutoContinueReplayPrompt} {
		add(text)
	}
	if a.stageCompletionCandidatePending && !a.stageCompletionCandidatePromptDelivered {
		add(a.stageCompletionCandidateReminder())
	}
	a.overlayClaims.mu.Lock()
	for _, text := range []string{a.pendingContextPressureReminder, a.pendingCompactionImminent, a.pendingCompactionWarning, a.pendingCompactionManual} {
		add(text)
	}
	a.overlayClaims.mu.Unlock()
	a.loopReductionMu.Lock()
	if note := a.pendingLoopContinuation; note != nil {
		add("## " + note.Title + "\n\n" + note.Text)
	}
	a.loopReductionMu.Unlock()
	if a.pendingThinkingReplayPrefix != nil {
		in.overlays = append(in.overlays, a.pendingThinkingReplayPrefix.Clone())
	}
	if followup := pendingLSPFollowup(a.lspFollowupSource, a.ctxMgr.ScanBackward, a.workDir(), a.instanceID); followup != nil {
		in.overlays = append(in.overlays, followup.overlay())
	}
	// The request may refresh checkpoint files immediately before dispatch.
	// Reserve its entire existing reinjection quota without performing a second
	// file read merely to decide candidate activation.
	if i, _, _ := a.latestCompactionSummarySignature(history); i >= 0 {
		plan := a.compactionInjectedFileBudgets(history)
		if plan.maxTotalBytes > 0 {
			in.overlays = append(in.overlays, message.Message{Role: message.RoleUser, Content: strings.Repeat("x", plan.maxTotalBytes) + fmt.Sprintf("\ncheckpoint file context (%d bytes)", plan.maxTotalBytes)})
		}
	}
	return in
}
func (s *SubAgent) directoryCandidateInputs(_ []message.Message) directoryCandidateBudgetInputs {
	in := directoryCandidateBudgetInputs{system: s.buildSystemPrompt(), defs: llmToolDefinitionsFromVisibleTools(s.filteredVisibleTools()), budget: s.ctxMgr.GetInputBudget()}
	if p := s.cachedSessionReminderContent.Load(); p != nil && *p != "" {
		in.overlays = append(in.overlays, message.Message{Role: message.RoleUser, Content: *p})
	}
	if followup := pendingLSPFollowup(s.lspFollowupSource, s.ctxMgr.ScanBackward, s.effectiveToolBaseDir(), s.instanceID); followup != nil {
		in.overlays = append(in.overlays, followup.overlay())
	}
	return in
}
