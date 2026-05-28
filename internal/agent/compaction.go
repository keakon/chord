package agent

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/keakon/chord/internal/message"
)

const (
	// Raw evidence pack: small continuation stabilizer (checkpoint-first design).
	compactEvidenceMinTokens     = 512
	compactEvidenceMaxTokens     = 2048
	compactEvidencePercentNumer  = 2 // ~2% of context window
	compactEvidencePercentDenom  = 100
	compactRecentTailMinTokens   = 768
	compactRecentTailMaxTokens   = 3072
	compactRecentTailTurns       = 2
	compactPromptOverhead        = 4096
	compactReservedOutput        = 4096
	compactPreflightBufferMin    = 1024
	compactPreflightBufferRatio  = 50 // reserve ~2% extra input budget for provider framing / hidden overhead
	compactConfirmAgeTurns       = 2
	compactErrorAgeTurns         = 3
	compactBashSuccessAgeTurns   = 2
	compactReadLikeAgeTurns      = 1
	compactStaleAgeTurns         = 4
	compactBashSuccessBytes      = 8000
	compactReadLikeOutputBytes   = 4000
	compactReadSnippetChars      = 500
	compactStaleOutputBytes      = 1500
	compactMinToolResultsPrune   = 8
	compactSummaryMinChars       = 160
	compactPromptSubAgentLimit   = 6
	compactPromptDescMaxChars    = 240
	compactPromptSummaryMaxChars = 320
	// Budget ratio for compaction input - use 1/6 of context window
	// to reduce transcript size and speed up model calls.
	compactBudgetRatio = 6
)

var compactionRequiredHeadings = []string{
	"## Current User Request",
	"## Active Objective",
	"## Background Goals",
	"## User Constraints",
	"## Progress",
	"## Key Decisions",
	"## Files and Evidence",
	"## Todo State",
	"## SubAgent State",
	"## Open Problems",
	"## Next Step",
}

// compactionDraft is produced off the event loop and applied when
// EventCompactionReady is dispatched.
type compactionDraft struct {
	Skip bool

	TooFewMessages bool
	SmallContext   bool
	InfoMessage    string
	PlanID         uint64
	Target         compactionTarget

	NewMessages        []message.Message
	HeadSplit          int // Async mode: snapshot boundary for tail preservation
	Index              int
	AbsHistoryPath     string
	AbsHistoryMetaPath string
	RelHistoryPath     string
	SummaryMode        string
	Backend            string
	Profile            string
	ModelRef           string
	SummarizeErr       error
	Manual             bool
	ArchivedCount      int
	EvidenceCount      int
	EvidenceArtifacts  int
}

const compactionSystemPrompt = `You summarize earlier coding-agent conversation history so another agent can continue work without losing important context.

Write only the summary. Do not answer the user. Do not invent facts.

First, privately think through the transcript and anchors to identify:
- the latest user request that should be answered next, including the latest Done rejected reason when it asks for more work, asks a question, changes scope, or corrects the agent
- the single active coding objective that serves that latest user request
- historical or completed goals that are background only
- user constraints and corrections that still matter
- whether existing TODO items still serve the latest user request, are completed/background, or are stale/superseded
- concrete progress already made
- important decisions and why they were made
- key files, commands, errors, and evidence needed for continuation
- unresolved blockers and the most likely next step

Then write the final summary using the exact Markdown section headings below, in order:
## Current User Request
## Active Objective
## Background Goals
## User Constraints
## Progress
## Key Decisions
## Files and Evidence
## Todo State
## SubAgent State
## Open Problems
## Next Step

Requirements:
- Every section must be present.
- The latest user request is authoritative. Identify it explicitly and prioritize it over older goals.
- Treat the most recent Done rejected reason as important user feedback/request when it asks for more work, asks a question, changes scope, or corrects the agent.
- If the user changed topics after previous todos were created, do not treat stale todos as active work.
- Separate active todos that directly serve the latest user request from historical, completed, or superseded todos.
- Do not let old implementation/debugging goals override a later meta-analysis, clarification request, or explicit correction.
- Under "Todo State", use these subgroups exactly: "Active/relevant to latest request", "Completed/background", and "Stale/superseded". If a subgroup has no items, write "(none)".
- Use concise bullet-style prose under each heading.
- Include concrete files, commands, errors, and decisions when known.
- Under "Files and Evidence", first include the archived history file path from the prompt header, then list the most important repository file paths needed for continuation as standalone bullet lines.
- The checkpoint wrapper may also list all archived history files for the full session history chain; preserve that distinction and do not imply the current compaction file is the only historical archive.
- Prefer workspace-relative file paths such as internal/... or docs/... .
- Keep each file path on its own bullet line. Do not add inline explanation text on the same line as a file path.
- Focus on durable continuation context, not narrative recap.
- Do not duplicate long verbatim excerpts already present in the evidence pack or recent tail anchor.
- If details are missing because earliest messages were omitted, say so explicitly instead of inventing facts.`

type evidenceKind string

const (
	evidenceUserCorrection evidenceKind = "user_correction"
	evidenceDoneRejected   evidenceKind = "done_rejected"
	evidenceUserRequest    evidenceKind = "user_request"
	evidenceToolError      evidenceKind = "tool_error"
	evidenceToolDiff       evidenceKind = "tool_diff"
	evidenceEscalate       evidenceKind = "escalate"
	evidenceSubAgentDone   evidenceKind = "subagent_done"
)

type evidenceItem struct {
	Kind      evidenceKind
	Title     string
	WhyNeeded string
	Source    string
	Excerpt   string
	Priority  int
	TokenCost int
	Key       string
	Sequence  int
}

type compactionHistoryMeta struct {
	Version     int       `json:"version"`
	HistoryFile string    `json:"history_file"`
	Status      string    `json:"status"`
	ExportedAt  time.Time `json:"exported_at"`
	AppliedAt   time.Time `json:"applied_at,omitempty"`
}

const (
	compactionHistoryPending = "pending_apply"
	compactionHistoryApplied = "applied"
)

type toolCallMeta struct {
	Name string
	Args string
}

func compactTextSnippet(s string, maxChars int) string {
	s = strings.TrimSpace(s)
	if s == "" || maxChars <= 0 {
		return ""
	}
	if len(s) <= maxChars {
		return s
	}
	keepHead := maxChars * 2 / 3
	if keepHead < 1 {
		keepHead = maxChars
	}
	keepTail := maxChars - keepHead - len("\n...\n")
	if keepTail < 0 {
		keepTail = 0
	}
	if keepTail == 0 {
		return strings.TrimSpace(s[:keepHead])
	}
	return strings.TrimSpace(s[:keepHead]) + "\n...\n" + strings.TrimSpace(s[len(s)-keepTail:])
}

func evidencePriority(kind evidenceKind) int {
	switch kind {
	case evidenceUserCorrection:
		return 100
	case evidenceDoneRejected:
		// Treat a Done rejection as equal-weight user feedback: when both a
		// user correction and a Done rejection exist, the later one (by
		// message Sequence) wins as the latest request anchor, instead of
		// always preferring an older correction.
		return 100
	case evidenceToolError:
		return 95
	case evidenceToolDiff:
		return 90
	case evidenceEscalate:
		return 85
	case evidenceSubAgentDone:
		return 80
	case evidenceUserRequest:
		return 70
	default:
		return 10
	}
}

func isEscalateMessage(text string) bool {
	return strings.HasPrefix(strings.TrimSpace(text), "[SubAgent ") && strings.Contains(text, "requests intervention]")
}

func isSubAgentDoneMessage(text string) bool {
	trimmed := strings.TrimSpace(text)
	return strings.HasPrefix(trimmed, "[SubAgent ") && strings.Contains(trimmed, " completed]")
}

func looksLikeUserCorrection(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	if lower == "" {
		return false
	}
	for _, marker := range []string{
		"don't ", "do not ", "不要", "别", "instead", "only ", "只", "不要改", "不要再", "rather than", "must ", "必须",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func extractDoneRejectedReason(text string) (string, bool) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return "", false
	}
	prefixes := []string{"Done rejected:", "Done rejected automatically:"}
	for _, prefix := range prefixes {
		if strings.HasPrefix(trimmed, prefix) {
			reason := strings.TrimSpace(strings.TrimPrefix(trimmed, prefix))
			return reason, reason != ""
		}
	}
	return "", false
}

func extractToolRejectedByUserReason(text string) (string, bool) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return "", false
	}
	marker := "rejected by user:"
	idx := strings.Index(strings.ToLower(trimmed), marker)
	if idx < 0 {
		return "", false
	}
	reason := strings.TrimSpace(trimmed[idx+len(marker):])
	return reason, reason != ""
}

func buildDoneRejectedEvidence(source, reason string) evidenceItem {
	return buildEvidenceItem(
		evidenceDoneRejected,
		"Latest Done rejection",
		"The rejection reason is recent user feedback/request and may supersede older todos.",
		source,
		compactTextSnippet(reason, 700),
	)
}

func buildLatestUserRequestEvidence(source, text string) evidenceItem {
	return buildEvidenceItem(
		evidenceUserRequest,
		"Latest user request",
		"This is the latest ordinary user request and should anchor the current objective unless superseded by a later correction or Done rejection.",
		source,
		compactTextSnippet(text, 700),
	)
}

func isCompactionSummaryText(text string) bool {
	return strings.HasPrefix(strings.TrimSpace(text), "[Context Summary]")
}

func isNonActionUserUtterance(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	lower = strings.Trim(lower, ".!。！~～ ")
	switch lower {
	case "thanks", "thank you", "thx", "ty", "ok", "okay", "好的", "好", "谢谢", "多谢", "收到", "明白", "了解":
		return true
	}
	return false
}

func isPlainUserRequestForCompaction(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" || isCompactionSummaryText(text) || isNonActionUserUtterance(text) || isEscalateMessage(text) || isSubAgentDoneMessage(text) {
		return false
	}
	return true
}

func buildEvidenceItem(kind evidenceKind, title, whyNeeded, source, excerpt string) evidenceItem {
	excerpt = strings.TrimSpace(excerpt)
	key := string(kind) + "\x00" + excerpt
	if excerpt == "" {
		key = string(kind) + "\x00" + strings.TrimSpace(source)
	}
	return evidenceItem{
		Kind:      kind,
		Title:     title,
		WhyNeeded: whyNeeded,
		Source:    source,
		Excerpt:   excerpt,
		Priority:  evidencePriority(kind),
		TokenCost: max(1, len(title)/3+len(whyNeeded)/3+len(source)/3+len(excerpt)/3),
		Key:       key,
	}
}

func renderEvidenceArtifact(items []evidenceItem) message.Message {
	return message.Message{Role: "user", Content: renderEvidenceArtifactContent(items)}
}

func (a *MainAgent) clearEvidenceCandidates() {
	a.evidenceCandidates = nil
	if a.evidenceCandidateSet == nil {
		a.evidenceCandidateSet = make(map[string]struct{})
		return
	}
	for key := range a.evidenceCandidateSet {
		delete(a.evidenceCandidateSet, key)
	}
}

func (a *MainAgent) addEvidenceCandidate(item evidenceItem) {
	if strings.TrimSpace(item.Excerpt) == "" {
		return
	}
	if a.evidenceCandidateSet == nil {
		a.evidenceCandidateSet = make(map[string]struct{})
	}
	key := item.Key
	if key == "" {
		key = string(item.Kind) + "\x00" + item.Excerpt
		item.Key = key
	}
	if _, ok := a.evidenceCandidateSet[key]; ok {
		return
	}
	a.evidenceCandidateSet[key] = struct{}{}
	a.evidenceCandidates = append(a.evidenceCandidates, item)
	if len(a.evidenceCandidates) > 48 {
		drop := len(a.evidenceCandidates) - 48
		for i := 0; i < drop; i++ {
			delete(a.evidenceCandidateSet, a.evidenceCandidates[i].Key)
		}
		a.evidenceCandidates = append([]evidenceItem(nil), a.evidenceCandidates[drop:]...)
	}
}

func evidenceItemsFromCandidates(candidates []evidenceItem, contextLimit int) []evidenceItem {
	if len(candidates) == 0 {
		return nil
	}
	latestDoneRejected := -1
	latestDoneRejectedSeq := -1
	latestUserRequest := -1
	latestUserRequestSeq := -1
	for i, item := range candidates {
		seq := item.Sequence
		if seq == 0 {
			seq = i + 1
		}
		switch item.Kind {
		case evidenceDoneRejected:
			if seq > latestDoneRejectedSeq {
				latestDoneRejectedSeq = seq
				latestDoneRejected = i
			}
		case evidenceUserRequest:
			if seq > latestUserRequestSeq {
				latestUserRequestSeq = seq
				latestUserRequest = i
			}
		}
	}
	items := make([]evidenceItem, 0, len(candidates))
	for i, item := range candidates {
		if item.Kind == evidenceDoneRejected && i != latestDoneRejected {
			continue
		}
		if item.Kind == evidenceUserRequest && i != latestUserRequest {
			continue
		}
		items = append(items, item)
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Priority != items[j].Priority {
			return items[i].Priority > items[j].Priority
		}
		if items[i].Sequence != items[j].Sequence {
			return items[i].Sequence > items[j].Sequence
		}
		return items[i].TokenCost < items[j].TokenCost
	})
	budget := evidencePackTokenBudget(contextLimit)
	selected := make([]evidenceItem, 0, len(items))
	used := 0
	var haveCorrection, haveDoneRejected, haveUserRequest, haveError, haveDiff bool
	for _, item := range items {
		required := false
		switch item.Kind {
		case evidenceUserCorrection:
			required = !haveCorrection
		case evidenceDoneRejected:
			required = !haveDoneRejected
		case evidenceUserRequest:
			required = !haveUserRequest
		case evidenceToolError:
			required = !haveError
		case evidenceToolDiff:
			required = !haveDiff
		}
		if used > 0 && used+item.TokenCost > budget && !required {
			continue
		}
		selected = append(selected, item)
		used += item.TokenCost
		switch item.Kind {
		case evidenceUserCorrection:
			haveCorrection = true
		case evidenceDoneRejected:
			haveDoneRejected = true
		case evidenceUserRequest:
			haveUserRequest = true
		case evidenceToolError:
			haveError = true
		case evidenceToolDiff:
			haveDiff = true
		}
	}
	if len(selected) == 0 {
		return nil
	}
	sort.SliceStable(selected, func(i, j int) bool {
		if selected[i].Priority != selected[j].Priority {
			return selected[i].Priority > selected[j].Priority
		}
		return selected[i].Sequence > selected[j].Sequence
	})
	return selected
}

func collectEvidenceItems(messages []message.Message) []evidenceItem {
	items := make([]evidenceItem, 0, 8)
	seen := make(map[string]bool)
	capturedLatestUserRequest := false
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		sourceSeq := i + 1
		switch msg.Role {
		case "user":
			text := strings.TrimSpace(msg.Content)
			if len(msg.Parts) > 0 {
				text = normalizeMessagesForSummary([]message.Message{msg})[0].Content
			}
			if text == "" {
				continue
			}
			var item evidenceItem
			switch {
			case isEscalateMessage(text):
				item = buildEvidenceItem(
					evidenceEscalate,
					"SubAgent requested main-agent help",
					"This unresolved intervention request may still determine the next action.",
					fmt.Sprintf("message %d (user)", i+1),
					compactTextSnippet(text, 700),
				)
			case isSubAgentDoneMessage(text):
				item = buildEvidenceItem(
					evidenceSubAgentDone,
					"SubAgent completion summary",
					"The main agent may need this exact completion summary before continuing.",
					fmt.Sprintf("message %d (user)", i+1),
					compactTextSnippet(text, 700),
				)
			case looksLikeUserCorrection(text):
				item = buildEvidenceItem(
					evidenceUserCorrection,
					"User correction / constraint",
					"This explicitly constrains the next code change and should be preserved verbatim.",
					fmt.Sprintf("message %d (user)", i+1),
					compactTextSnippet(text, 600),
				)
			case isPlainUserRequestForCompaction(text) && !capturedLatestUserRequest:
				item = buildLatestUserRequestEvidence(fmt.Sprintf("message %d (user)", i+1), text)
				capturedLatestUserRequest = true
			default:
				continue
			}
			item.Sequence = sourceSeq
			if seen[item.Key] {
				continue
			}
			seen[item.Key] = true
			items = append(items, item)
		case "tool":
			text := strings.TrimSpace(msg.Content)
			if reason, ok := extractDoneRejectedReason(text); ok {
				item := buildDoneRejectedEvidence(fmt.Sprintf("message %d (tool result)", i+1), reason)
				item.Sequence = sourceSeq
				if !seen[item.Key] {
					seen[item.Key] = true
					items = append(items, item)
				}
			}
			if text == "" && strings.TrimSpace(msg.ToolDiff) == "" {
				continue
			}
			if strings.Contains(text, "Error:") || isToolErrorContent(text) {
				item := buildEvidenceItem(
					evidenceToolError,
					"Latest failing tool result",
					"This looks like a current blocker; preserving the exact error helps the next continuation avoid guessing.",
					fmt.Sprintf("message %d (tool result)", i+1),
					compactTextSnippet(text, 800),
				)
				item.Sequence = sourceSeq
				if !seen[item.Key] {
					seen[item.Key] = true
					items = append(items, item)
				}
			}
			if strings.TrimSpace(msg.ToolDiff) != "" {
				item := buildEvidenceItem(
					evidenceToolDiff,
					"Recent code diff",
					"The next continuation may depend on the exact recent code change.",
					fmt.Sprintf("message %d (tool diff)", i+1),
					compactTextSnippet(msg.ToolDiff, 700),
				)
				item.Sequence = sourceSeq
				if !seen[item.Key] {
					seen[item.Key] = true
					items = append(items, item)
				}
			}
		}
	}
	return items
}

func selectEvidenceItems(messages []message.Message, contextLimit int) []evidenceItem {
	items := collectEvidenceItems(messages)
	return evidenceItemsFromCandidates(items, contextLimit)
}

func (a *MainAgent) evidenceItemsForCompaction(_ []message.Message, contextLimit int) []evidenceItem {
	return evidenceItemsFromCandidates(a.evidenceCandidates, contextLimit)
}

func isToolErrorContent(content string) bool {
	return strings.HasPrefix(strings.TrimSpace(content), "Error:")
}

func isConfirmationOutput(content string) bool {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" || len(trimmed) > 180 {
		return false
	}
	if strings.Count(trimmed, "\n") > 2 {
		return false
	}
	lower := strings.ToLower(trimmed)
	for _, phrase := range []string{
		"applied successfully",
		"completed successfully",
		"written successfully",
		"updated successfully",
		"created successfully",
		"removed successfully",
		"saved successfully",
		"background task started",
		"background task completed",
		"done",
	} {
		if strings.Contains(lower, phrase) {
			return true
		}
	}
	return false
}

func toolNameOrUnknown(name string) string {
	if name == "" {
		return "tool"
	}
	return name
}
