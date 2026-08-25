package agent

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/session"
	"github.com/keakon/chord/internal/tools"
)

func normalizeMessagesForSummary(messages []message.Message) []message.Message {
	normalized := make([]message.Message, len(messages))
	copy(normalized, messages)
	for i := range normalized {
		if len(normalized[i].Parts) == 0 {
			continue
		}
		var textParts []string
		imageCount := 0
		pdfCount := 0
		for _, part := range normalized[i].Parts {
			switch part.Type {
			case "text":
				if strings.TrimSpace(part.Text) != "" {
					textParts = append(textParts, part.Text)
				}
			case "image":
				imageCount++
			case "pdf":
				pdfCount++
			}
		}
		if imageCount > 0 {
			textParts = append(textParts, fmt.Sprintf("[User included %d image attachment(s)]", imageCount))
		}
		if pdfCount > 0 {
			textParts = append(textParts, fmt.Sprintf("[User included %d PDF attachment(s)]", pdfCount))
		}
		normalized[i].Parts = nil
		joined := strings.TrimSpace(strings.Join(textParts, "\n"))
		if joined != "" {
			normalized[i].Content = joined
		}
	}
	return normalized
}

func trimMessagesToBudget(messages []message.Message, targetTokens int) ([]message.Message, int) {
	if len(messages) == 0 || targetTokens <= 0 {
		return nil, len(messages)
	}
	if ctxmgr.EstimateMessagesTokens(messages) <= targetTokens {
		out := make([]message.Message, len(messages))
		copy(out, messages)
		return out, 0
	}

	start := len(messages)
	remaining := targetTokens
	for i, message := range slices.Backward(messages) {
		cost := ctxmgr.EstimateMessageTokens(message)
		if remaining-cost < 0 {
			break
		}
		remaining -= cost
		start = i
	}
	start = ctxmgr.SafeKeepBoundary(messages, start)
	if start <= 0 || start >= len(messages) {
		return nil, len(messages)
	}

	out := make([]message.Message, 0, len(messages[start:])+1)
	omitted := start
	out = append(out, message.Message{
		Role: message.RoleUser,
		Content: fmt.Sprintf(
			"[system] The earliest %d messages from the compacted history were omitted from the summary input to fit the compression model budget. The exported history file remains authoritative for those details.",
			omitted,
		),
	})
	out = append(out, messages[start:]...)
	return out, omitted
}

func compactionInputBudget(contextLimit int) int {
	if contextLimit <= 0 {
		return 0
	}
	reservedOutput := min(compactReservedOutput, contextLimit/8)
	reservedOutput = max(reservedOutput, 2048)
	preflightBuffer := max(contextLimit/compactPreflightBufferRatio, compactPreflightBufferMin)
	budget := contextLimit - compactPromptOverhead - reservedOutput - preflightBuffer
	budget = max(budget, contextLimit/compactBudgetRatio)
	return budget
}

type compactionInput struct {
	Transcript       string
	OmittedMessages  int
	EvidenceItems    []evidenceItem
	RecentTail       []message.Message
	RecentTailAnchor string
	GoalAnchor       string
	ConstraintAnchor string
	DecisionAnchor   string
	ProgressAnchor   string
}

func buildCompactionInputWithOptions(head []message.Message, contextLimit int, evidenceItems []evidenceItem, recentTail []message.Message, autoRecentTail bool) (*compactionInput, error) {
	pruned := (&MainAgent{}).prepareMessagesForLLM(head)
	normalized := normalizeMessagesForSummary(pruned)
	budget := compactionInputBudget(contextLimit)
	trimmed, omittedMessages := trimMessagesToBudget(normalized, budget)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("compaction input too large even after truncation")
	}
	exported, err := session.Export(trimmed, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("build compaction transcript: %w", err)
	}
	if len(evidenceItems) == 0 {
		evidenceItems = selectEvidenceItems(normalized, contextLimit)
	}
	if autoRecentTail && len(recentTail) == 0 {
		recentTail = selectRecentTailMessages(normalized, compactRecentTailTurns, compactRecentTailMaxTokens)
	}
	return &compactionInput{
		Transcript:       session.ExportToMarkdown(exported),
		OmittedMessages:  omittedMessages,
		EvidenceItems:    evidenceItems,
		RecentTail:       recentTail,
		RecentTailAnchor: formatRecentTailAnchor(recentTail),
		GoalAnchor:       buildGoalAnchor(normalized),
		ConstraintAnchor: buildConstraintAnchor(evidenceItems),
		DecisionAnchor:   buildDecisionAnchor(normalized),
		ProgressAnchor:   buildProgressAnchor(normalized, evidenceItems),
	}, nil
}

func trimMessagesToBudgetWithReservedTail(messages []message.Message, targetTokens int, reserveTail int) ([]message.Message, int) {
	if reserveTail <= 0 {
		return trimMessagesToBudget(messages, targetTokens)
	}
	trimmed, omitted := trimMessagesToBudget(messages, targetTokens-reserveTail)
	if len(trimmed) > 0 {
		return trimmed, omitted
	}
	return trimMessagesToBudget(messages, targetTokens)
}

func compactionPromptTokenEstimate(input *compactionInput, relHistoryPath string, keyFiles []string, todos []tools.TodoItem, subAgents []SubAgentInfo, backgroundObjects []recovery.BackgroundObjectState) int {
	prompt := buildCompactionPromptWithKeyFiles(input, relHistoryPath, keyFiles, todos, subAgents, backgroundObjects)
	return max(1, len(prompt)/3)
}

func fitCompactionInputToContextLimit(head []message.Message, input *compactionInput, contextLimit int, relHistoryPath string, keyFiles []string, todos []tools.TodoItem, subAgents []SubAgentInfo, backgroundObjects []recovery.BackgroundObjectState, maxOutputTokens int) (*compactionInput, error) {
	if input == nil {
		return nil, fmt.Errorf("compaction input is nil")
	}
	if contextLimit <= 0 {
		return input, nil
	}
	preflightBuffer := max(contextLimit/compactPreflightBufferRatio, compactPreflightBufferMin)
	allowedInput := contextLimit - maxOutputTokens - preflightBuffer
	if allowedInput <= 0 {
		return nil, fmt.Errorf("compaction context limit too small after reserving output (%d)", contextLimit)
	}
	if compactionPromptTokenEstimate(input, relHistoryPath, keyFiles, todos, subAgents, backgroundObjects) <= allowedInput {
		return input, nil
	}
	pruned := (&MainAgent{}).prepareMessagesForLLM(head)
	normalized := normalizeMessagesForSummary(pruned)
	budget := compactionInputBudget(contextLimit)
	for attempts := range 6 {
		trimmed, omittedMessages := trimMessagesToBudgetWithReservedTail(normalized, budget, attempts*512)
		if len(trimmed) == 0 {
			continue
		}
		exported, err := session.Export(trimmed, nil, nil)
		if err != nil {
			return nil, fmt.Errorf("build compaction transcript during fit: %w", err)
		}
		candidate := *input
		candidate.Transcript = session.ExportToMarkdown(exported)
		candidate.OmittedMessages = omittedMessages
		if compactionPromptTokenEstimate(&candidate, relHistoryPath, keyFiles, todos, subAgents, backgroundObjects) <= allowedInput {
			return &candidate, nil
		}
		budget -= max(512, budget/8)
		if budget <= contextLimit/8 {
			break
		}
	}
	return nil, fmt.Errorf("compaction prompt still exceeds reserved context budget")
}

func buildGoalAnchor(messages []message.Message) string {
	for _, msg := range slices.Backward(messages) {

		if !message.IsUserAuthored(msg) {
			continue
		}
		text := strings.TrimSpace(msg.Content)
		if len(msg.Parts) > 0 {
			normalized := normalizeMessagesForSummary([]message.Message{msg})
			if len(normalized) > 0 {
				text = strings.TrimSpace(normalized[0].Content)
			}
		}
		if !isPlainUserRequestForCompaction(text) {
			continue
		}
		return "- " + strings.ReplaceAll(compactTextSnippet(text, 300), "\n", " ")
	}
	return "- (not confidently recoverable from retained head)"
}

func buildConstraintAnchor(items []evidenceItem) string {
	var lines []string
	for _, item := range items {
		if item.Kind != evidenceUserCorrection {
			continue
		}
		lines = append(lines, "- "+strings.ReplaceAll(compactTextSnippet(item.Excerpt, 220), "\n", " "))
	}
	if len(lines) == 0 {
		return "- (none extracted)"
	}
	return strings.Join(lines, "\n")
}

func buildDecisionAnchor(messages []message.Message) string {
	var lines []string
	for _, msg := range messages {
		if msg.Role != message.RoleAssistant {
			continue
		}
		text := strings.TrimSpace(msg.Content)
		if text == "" {
			continue
		}
		lower := strings.ToLower(text)
		if strings.Contains(lower, "decid") || strings.Contains(lower, "plan") || strings.Contains(text, "方案") || strings.Contains(text, "决定") {
			lines = append(lines, "- "+strings.ReplaceAll(compactTextSnippet(text, 220), "\n", " "))
			if len(lines) >= 3 {
				break
			}
		}
	}
	if len(lines) == 0 {
		return "- (none explicitly extracted; infer from progress and evidence)"
	}
	return strings.Join(lines, "\n")
}

func buildProgressAnchor(messages []message.Message, items []evidenceItem) string {
	var lines []string
	for _, item := range items {
		if item.Kind == evidenceToolDiff || item.Kind == evidenceToolError || item.Kind == evidenceEscalate || item.Kind == evidenceSubAgentDone {
			lines = append(lines, "- "+item.Title+": "+strings.ReplaceAll(compactTextSnippet(item.Excerpt, 180), "\n", " "))
		}
		if len(lines) >= 4 {
			break
		}
	}
	if len(lines) == 0 {
		for i := len(messages) - 1; i >= 0 && len(lines) < 3; i-- {
			msg := messages[i]
			if msg.Role != message.RoleAssistant && msg.Role != message.RoleTool {
				continue
			}
			text := strings.TrimSpace(msg.Content)
			if text == "" {
				continue
			}
			lines = append(lines, "- "+strings.ReplaceAll(compactTextSnippet(text, 180), "\n", " "))
		}
	}
	if len(lines) == 0 {
		return "- (none extracted)"
	}
	return strings.Join(lines, "\n")
}

func formatCompactionAnchorsForPrompt(input *compactionInput) string {
	if input == nil {
		return "Latest user request anchor:\n- (none)\n\nConstraint anchor:\n- (none)\n\nDecision anchor:\n- (none)\n\nRecent progress anchor:\n- (none)"
	}
	return strings.Join([]string{
		"Latest user request anchor:\n" + input.GoalAnchor,
		"Constraint anchor:\n" + input.ConstraintAnchor,
		"Decision anchor:\n" + input.DecisionAnchor,
		"Recent progress anchor:\n" + input.ProgressAnchor,
	}, "\n\n")
}

func validateCompactionSummary(summary string) error {
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return fmt.Errorf("compaction model returned empty summary")
	}
	if containsThinkingTag(summary) {
		return fmt.Errorf("compaction summary contains private thinking tags")
	}
	if len([]rune(summary)) < compactSummaryMinChars {
		return fmt.Errorf("compaction summary too short (%d chars)", len([]rune(summary)))
	}
	positions := compactionHeadingPositions(summary)
	matched := len(positions)
	if matched < len(compactionRequiredHeadings) {
		return fmt.Errorf("compaction summary missing required sections (%d/%d)", matched, len(compactionRequiredHeadings))
	}
	for i := 1; i < len(positions); i++ {
		if positions[i] <= positions[i-1] {
			return fmt.Errorf("compaction summary sections out of order")
		}
	}
	if strings.TrimSpace(summary[:positions[0]]) != "" {
		return fmt.Errorf("compaction summary has content before first required section")
	}
	if err := validateCompactionNextStep(summary, positions[len(positions)-1]); err != nil {
		return err
	}
	if err := validateCompactionTodoState(summary); err != nil {
		return err
	}
	return nil
}

var (
	compactionMarkdownHeadingLineRe      = regexp.MustCompile(`(?m)^##\s+`)
	compactionRequiredHeadingLineRegexps = buildCompactionRequiredHeadingLineRegexps()
)

func buildCompactionRequiredHeadingLineRegexps() map[string]*regexp.Regexp {
	patterns := make(map[string]*regexp.Regexp, len(compactionRequiredHeadings))
	for _, heading := range compactionRequiredHeadings {
		patterns[heading] = regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(heading) + `\s*$`)
	}
	return patterns
}

func validateCompactionNextStep(summary string, nextStepPos int) error {
	if nextStepPos < 0 || nextStepPos >= len(summary) {
		return fmt.Errorf("compaction summary missing next step")
	}
	section := strings.TrimSpace(summary[nextStepPos+len("## Next Step"):])
	if section == "" {
		return fmt.Errorf("compaction summary next step is empty")
	}
	if isVagueCompactionNextStep(section) {
		return fmt.Errorf("compaction summary next step is too vague")
	}
	return nil
}

func isVagueCompactionNextStep(section string) bool {
	normalized := strings.ToLower(strings.TrimSpace(section))
	for strings.HasPrefix(normalized, "-") {
		normalized = strings.TrimSpace(strings.TrimPrefix(normalized, "-"))
	}
	normalized = strings.Trim(strings.TrimSuffix(normalized, "."), " ")
	return slices.Contains([]string{
		"continue",
		"continue working",
		"continue the task",
		"keep working",
		"proceed",
		"resume",
		"resume work",
		"carry on",
	}, normalized)
}

func validateCompactionTodoState(summary string) error {
	section, ok := markdownSection(summary, "## Todo State")
	if !ok {
		return nil
	}
	active := todoSubsectionLines(section, "Active/relevant to latest request")
	completed := todoSubsectionLines(section, "Completed/background")
	stale := todoSubsectionLines(section, "Stale/superseded")
	if len(active) == 0 {
		return nil
	}
	inactive := append(completed, stale...)
	for _, activeLine := range active {
		activeKey := normalizeTodoStateLine(activeLine)
		if activeKey == "" || activeKey == "none" {
			continue
		}
		for _, inactiveLine := range inactive {
			inactiveKey := normalizeTodoStateLine(inactiveLine)
			if inactiveKey == "" || inactiveKey == "none" {
				continue
			}
			if activeKey == inactiveKey {
				return fmt.Errorf("compaction summary marks completed or stale todo as active: %s", activeLine)
			}
		}
	}
	return nil
}

func markdownSection(summary, heading string) (string, bool) {
	pos := findMarkdownHeadingLine(summary, heading)
	if pos < 0 {
		return "", false
	}
	start := pos + len(heading)
	rest := summary[start:]
	if loc := compactionMarkdownHeadingLineRe.FindStringIndex(rest); loc != nil {
		rest = rest[:loc[0]]
	}
	return strings.TrimSpace(rest), true
}

func todoSubsectionLines(section, label string) []string {
	var lines []string
	inGroup := false
	for raw := range strings.SplitSeq(section, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		bullet := strings.TrimSpace(strings.TrimPrefix(line, "-"))
		if strings.HasPrefix(bullet, label+":") {
			inGroup = true
			rest := strings.TrimSpace(strings.TrimPrefix(bullet, label+":"))
			if rest != "" {
				lines = append(lines, rest)
			}
			continue
		}
		if strings.HasPrefix(bullet, "Active/relevant to latest request:") || strings.HasPrefix(bullet, "Completed/background:") || strings.HasPrefix(bullet, "Stale/superseded:") {
			inGroup = false
			continue
		}
		if inGroup {
			lines = append(lines, bullet)
		}
	}
	return lines
}

func normalizeTodoStateLine(line string) string {
	line = strings.ToLower(strings.TrimSpace(line))
	line = strings.TrimPrefix(line, "-")
	line = strings.TrimSpace(line)
	line = strings.Trim(line, "`*_ .")
	line = strings.Trim(line, "()")
	return line
}

func containsThinkingTag(summary string) bool {
	lower := strings.ToLower(summary)
	return strings.Contains(lower, "<think>") || strings.Contains(lower, "</think>")
}

func compactionHeadingPositions(summary string) []int {
	positions := make([]int, 0, len(compactionRequiredHeadings))
	searchStart := 0
	for _, heading := range compactionRequiredHeadings {
		pos := findMarkdownHeadingLine(summary[searchStart:], heading)
		if pos < 0 {
			break
		}
		absolute := searchStart + pos
		positions = append(positions, absolute)
		searchStart = absolute + len(heading)
	}
	return positions
}

func findMarkdownHeadingLine(summary, heading string) int {
	pattern := compactionRequiredHeadingLineRegexps[heading]
	if pattern == nil {
		return -1
	}
	loc := pattern.FindStringIndex(summary)
	if loc == nil {
		return -1
	}
	return loc[0]
}

func renderEvidenceItemsForPrompt(items []evidenceItem) string {
	if len(items) == 0 {
		return "- (none)"
	}
	var sb strings.Builder
	for _, item := range items {
		fmt.Fprintf(&sb, "- %s", item.Title)
		if item.WhyNeeded != "" {
			fmt.Fprintf(&sb, " | why: %s", item.WhyNeeded)
		}
		if item.Excerpt != "" {
			fmt.Fprintf(&sb, "\n  excerpt: %s", strings.ReplaceAll(item.Excerpt, "\n", " "))
		}
		sb.WriteByte('\n')
	}
	return strings.TrimRight(sb.String(), "\n")
}

func recentTailTokenBudget(contextLimit int) int {
	if contextLimit <= 0 {
		return compactRecentTailMinTokens
	}
	b := contextLimit / 50
	b = max(b, compactRecentTailMinTokens)
	b = min(b, compactRecentTailMaxTokens)
	return b
}

func selectRecentTailMessages(messages []message.Message, userTurns int, maxTokens int) []message.Message {
	if len(messages) == 0 || userTurns <= 0 || maxTokens <= 0 {
		return nil
	}
	for turns := userTurns; turns >= 1; turns-- {
		usersSeen := 0
		start := len(messages)
		for i, message0 := range slices.Backward(messages) {
			start = i
			if message0.Role == message.RoleUser {
				usersSeen++
				if usersSeen >= turns {
					break
				}
			}
		}
		start = ctxmgr.SafeKeepBoundary(messages, start)
		if start <= 0 || start >= len(messages) {
			continue
		}
		tail := append([]message.Message(nil), messages[start:]...)
		if ctxmgr.EstimateMessagesTokens(tail) <= maxTokens {
			return tail
		}
	}
	return nil
}

func formatRecentTailAnchor(messages []message.Message) string {
	if len(messages) == 0 {
		return "- (none)"
	}
	var sb strings.Builder
	for _, msg := range messages {
		text := strings.TrimSpace(msg.Content)
		if len(msg.Parts) > 0 {
			norm := normalizeMessagesForSummary([]message.Message{msg})
			if len(norm) > 0 {
				text = strings.TrimSpace(norm[0].Content)
			}
		}
		if text == "" {
			continue
		}
		fmt.Fprintf(&sb, "- %s: %s\n", msg.Role, compactTextSnippet(text, 220))
	}
	out := strings.TrimRight(sb.String(), "\n")
	if out == "" {
		return "- (none)"
	}
	return out
}

// fallbackSummarySection is a heading/body pair rendered by
// renderFallbackSummarySections. The body is TrimSpaced before writing.
type fallbackSummarySection struct {
	heading string
	body    string
}

// renderFallbackSummarySections renders heading + body pairs separated by blank
// lines, then appends a preserved background-objects footer when present. Used
// by both the structured-fallback and truncate-only summary builders.
func renderFallbackSummarySections(sections []fallbackSummarySection, backgroundObjects []recovery.BackgroundObjectState) string {
	var sb strings.Builder
	for i, sec := range sections {
		if i > 0 {
			sb.WriteString("\n\n")
		}
		sb.WriteString(sec.heading)
		sb.WriteString("\n")
		sb.WriteString(strings.TrimSpace(sec.body))
	}
	if len(backgroundObjects) > 0 {
		sb.WriteString("\n\n<!-- Background objects preserved:\n")
		sb.WriteString(formatBackgroundObjectsForPrompt(backgroundObjects))
		sb.WriteString("\n-->")
	}
	return strings.TrimSpace(sb.String())
}

func buildStructuredFallbackSummary(relHistoryPath string, input *compactionInput, summarizeErr error, keyFiles []string, todos []tools.TodoItem, subAgents []SubAgentInfo, backgroundObjects []recovery.BackgroundObjectState) string {
	anchor := fallbackContinuationAnchorForInput(input)
	return renderFallbackSummarySections([]fallbackSummarySection{
		{"## Current User Request", fallbackCurrentUserRequestSection(input)},
		{"## Active Objective", fallbackActiveObjectiveSection(anchor)},
		{"## Background Goals", "- Earlier goals are background until confirmed relevant to the latest preserved user request."},
		{"## User Constraints", renderEvidenceKindForFallback(input, evidenceUserCorrection, "- No preserved user constraints.")},
		{"## Progress", fallbackProgressSection(input)},
		{"## Key Decisions", "- Earlier durable decisions should be read from the archived history file if needed.\n- Preserve the recent continuation direction and evidence below."},
		{"## Files and Evidence", fallbackFilesAndEvidenceSection(relHistoryPath, input, keyFiles)},
		{"## Todo State", formatTodosAsRelevanceBullets(todos, anchor)},
		{"## SubAgent State", formatSubAgentsAsBullets(subAgents)},
		{"## Open Problems", fallbackOpenProblemsSection(input, summarizeErr)},
		{"## Next Step", fallbackNextStepSection(input)},
	}, backgroundObjects)
}

func fallbackCurrentUserRequestSection(input *compactionInput) string {
	anchor := fallbackContinuationAnchorForInput(input)
	if anchor.Kind != "" {
		return "- " + anchor.Label + ": " + strings.ReplaceAll(anchor.Text, "\n", " ")
	}
	return "- Unknown: model summarization was unavailable and no reliable latest-request anchor was preserved. Do not infer the active task from completed or stale todos."
}

func compactionSummaryHasUnknownUserRequest(content string) bool {
	const heading = "## Current User Request"
	_, section, ok := strings.Cut(content, heading)
	if !ok {
		return false
	}
	if before, _, found := strings.Cut(section, "\n## "); found {
		section = before
	}
	return strings.HasPrefix(strings.TrimSpace(section), "- Unknown:")
}

type fallbackAnchor struct {
	Kind  string
	Label string
	Text  string
}

func fallbackContinuationAnchorForInput(input *compactionInput) fallbackAnchor {
	if reason, ok := latestDoneRejectedReason(input); ok {
		return fallbackAnchor{Kind: "done_rejected", Label: "Latest Done rejected reason", Text: reason}
	}
	if input != nil {
		if strings.TrimSpace(input.RecentTailAnchor) != "" && input.RecentTailAnchor != "- (none)" {
			return fallbackAnchor{Kind: "recent_tail", Label: "Latest preserved recent context", Text: input.RecentTailAnchor}
		}
		if usableFallbackAnchor(input.GoalAnchor) {
			return fallbackAnchor{Kind: "goal_anchor", Label: "Latest recoverable user request from durable anchors", Text: input.GoalAnchor}
		}
	}
	return fallbackAnchor{}
}

func fallbackActiveObjectiveSection(anchor fallbackAnchor) string {
	if anchor.Kind != "" {
		return "- Serve only the latest preserved request: " + fallbackAnchorSnippet(anchor) + "\n- Do not restart older completed/background or stale/superseded todos unless that request explicitly reopens them."
	}
	return "- No active objective can be recovered safely from fallback data. First identify the latest user request from preserved recent context/evidence before acting; do not restart completed/background or stale/superseded todos."
}

func fallbackAnchorSnippet(anchor fallbackAnchor) string {
	text := strings.TrimSpace(anchor.Text)
	if text == "" {
		return "unknown"
	}
	return strings.ReplaceAll(compactTextSnippet(text, 260), "\n", " ")
}

func usableFallbackAnchor(anchor string) bool {
	anchor = strings.TrimSpace(anchor)
	return anchor != "" && anchor != "- (none)" && !strings.Contains(anchor, "not confidently recoverable")
}

func latestDoneRejectedReason(input *compactionInput) (string, bool) {
	if input == nil {
		return "", false
	}
	for _, item := range input.EvidenceItems {
		if item.Kind == evidenceDoneRejected && strings.TrimSpace(item.Excerpt) != "" {
			return item.Excerpt, true
		}
	}
	return "", false
}

func renderEvidenceKindForFallback(input *compactionInput, kind evidenceKind, empty string) string {
	if input == nil || len(input.EvidenceItems) == 0 {
		return empty
	}
	var lines []string
	for _, item := range input.EvidenceItems {
		if item.Kind != kind {
			continue
		}
		lines = append(lines, "- "+strings.ReplaceAll(item.Excerpt, "\n", " "))
	}
	if len(lines) == 0 {
		return empty
	}
	return strings.Join(lines, "\n")
}

func fallbackProgressSection(input *compactionInput) string {
	if input == nil {
		return "- Archived history was compacted."
	}
	lines := []string{"- Archived history was compacted into a durable checkpoint."}
	if input.RecentTailAnchor != "- (none)" {
		lines = append(lines, "- Recent continuation context was preserved.")
	}
	if usableFallbackAnchor(input.GoalAnchor) {
		lines = append(lines, "- Latest recoverable user request was preserved in durable anchors.")
	}
	if len(input.EvidenceItems) > 0 {
		lines = append(lines, fmt.Sprintf("- Preserved %d high-priority evidence item(s).", len(input.EvidenceItems)))
	}
	return strings.Join(lines, "\n")
}

func fallbackFilesAndEvidenceSection(relHistoryPath string, input *compactionInput, keyFiles []string) string {
	lines := []string{
		"- Archived history for this compaction: " + relHistoryPath,
		"- Checkpoint wrapper may list additional archived history files for the full session history chain.",
	}
	for _, path := range keyFiles {
		lines = append(lines, "- "+path)
	}
	if input != nil {
		for _, item := range input.EvidenceItems {
			if item.Kind == evidenceToolDiff || item.Kind == evidenceToolError {
				lines = append(lines, "- "+item.Title+": "+strings.ReplaceAll(compactTextSnippet(item.Excerpt, 160), "\n", " "))
			}
		}
	}
	return strings.Join(lines, "\n")
}

func fallbackOpenProblemsSection(input *compactionInput, summarizeErr error) string {
	var lines []string
	if summarizeErr != nil {
		lines = append(lines, "- Summary quality fallback reason: "+summarizeErr.Error())
	}
	if input != nil {
		for _, item := range input.EvidenceItems {
			if item.Kind == evidenceToolError || item.Kind == evidenceEscalate {
				lines = append(lines, "- "+strings.ReplaceAll(compactTextSnippet(item.Excerpt, 180), "\n", " "))
			}
		}
	}
	if len(lines) == 0 {
		return "- Read the archived history if additional unresolved issues are needed."
	}
	return strings.Join(lines, "\n")
}

func fallbackNextStepSection(input *compactionInput) string {
	anchor := fallbackContinuationAnchorForInput(input)
	if anchor.Kind != "" {
		return "- Choose the immediate next action only from this latest preserved request: " + fallbackAnchorSnippet(anchor) + "\n- Ignore completed/background and stale/superseded todos unless that request explicitly reopens them."
	}
	return "- Before modifying files or continuing old work, recover or ask for the latest user request; do not act on completed/background or stale/superseded todos."
}

func formatTodosAsRelevanceBullets(todos []tools.TodoItem, anchor fallbackAnchor) string {
	lines := []string{
		"- Active/relevant to latest request:",
		"  - (none reliably classified by fallback)",
		"- Completed/background:",
		"  - (none classified by fallback)",
		"- Stale/superseded:",
		"  - (none classified by fallback)",
	}
	if strings.TrimSpace(anchor.Text) != "" {
		lines = []string{
			"- Active/relevant to latest request:",
			"  - " + anchor.Label + ": " + strings.ReplaceAll(anchor.Text, "\n", " "),
			"- Completed/background:",
			"  - (none classified by fallback)",
			"- Stale/superseded:",
		}
		if len(todos) == 0 {
			lines = append(lines, "  - (none)")
			return strings.Join(lines, "\n")
		}
		for _, todo := range todos {
			lines = append(lines, fmt.Sprintf("  - [%s] %s: %s", todo.Status, todo.ID, todo.Content))
		}
		return strings.Join(lines, "\n")
	}
	if len(todos) == 0 {
		return strings.Join(lines, "\n")
	}
	for _, todo := range todos {
		lines = append(lines, fmt.Sprintf("  - [%s] %s: %s", todo.Status, todo.ID, todo.Content))
	}
	return strings.Join(lines, "\n")
}

func subAgentStateNeedsPromptContext(state string) bool {
	switch strings.TrimSpace(state) {
	case string(SubAgentStateRunning), string(SubAgentStateIdle), string(SubAgentStateWaitingMain), string(SubAgentStateWaitingDescendant):
		return true
	default:
		return false
	}
}

func subAgentsForCompactionPrompt(subAgents []SubAgentInfo) (visible []SubAgentInfo, omitted int) {
	if len(subAgents) == 0 {
		return nil, 0
	}
	visible = make([]SubAgentInfo, 0, min(len(subAgents), compactPromptSubAgentLimit))
	for _, sub := range subAgents {
		if !subAgentStateNeedsPromptContext(sub.State) {
			omitted++
			continue
		}
		if len(visible) >= compactPromptSubAgentLimit {
			omitted++
			continue
		}
		copySub := sub
		copySub.TaskDesc = strings.ReplaceAll(compactTextSnippet(strings.TrimSpace(copySub.TaskDesc), compactPromptDescMaxChars), "\n", " ")
		copySub.LastSummary = strings.ReplaceAll(compactTextSnippet(strings.TrimSpace(copySub.LastSummary), compactPromptSummaryMaxChars), "\n", " ")
		visible = append(visible, copySub)
	}
	return visible, omitted
}

func formatSubAgentsAsBullets(subAgents []SubAgentInfo) string {
	visible, omitted := subAgentsForCompactionPrompt(subAgents)
	if len(visible) == 0 {
		if omitted > 0 {
			return fmt.Sprintf("- (none active; %d historical or completed task(s) omitted)", omitted)
		}
		return "- (none active)"
	}
	var lines []string
	for _, sub := range visible {
		running := sub.RunningRef
		if running == "" {
			running = sub.SelectedRef
		}
		line := fmt.Sprintf("- %s | task=%s | state=%s | agent=%s | model=%s | desc=%s", sub.InstanceID, sub.TaskID, blankToDefault(sub.State, "unknown"), sub.AgentDefName, running, sub.TaskDesc)
		if strings.TrimSpace(sub.LastSummary) != "" {
			line += " | summary=" + sub.LastSummary
		}
		lines = append(lines, line)
	}
	if omitted > 0 {
		lines = append(lines, fmt.Sprintf("- (%d historical or completed task(s) omitted from compaction prompt)", omitted))
	}
	return strings.Join(lines, "\n")
}

func formatBackgroundObjectsForPrompt(jobs []recovery.BackgroundObjectState) string {
	if len(jobs) == 0 {
		return "- (none)"
	}
	var sb strings.Builder
	for _, job := range jobs {
		fmt.Fprintf(&sb, "- %s | agent=%s | kind=%s | status=%s | started=%s | desc=%s", job.ID, backgroundObjectPromptAgent(job.AgentID), backgroundObjectPromptKind(job.Kind), job.Status, job.StartedAt.Format(time.DateTime), backgroundObjectPromptDescription(job.Description, job.Command))
		if job.MaxRuntimeSec > 0 {
			fmt.Fprintf(&sb, " | max_runtime=%ds", job.MaxRuntimeSec)
		}
		if !job.FinishedAt.IsZero() {
			fmt.Fprintf(&sb, " | finished=%s", job.FinishedAt.Format(time.DateTime))
		}
		sb.WriteByte('\n')
	}
	return strings.TrimRight(sb.String(), "\n")
}

func backgroundObjectPromptAgent(agentID string) string {
	if strings.TrimSpace(agentID) == "" {
		return "main"
	}
	return agentID
}

func backgroundObjectPromptKind(kind string) string {
	if strings.TrimSpace(kind) == "" {
		return "job"
	}
	return kind
}

func backgroundObjectPromptDescription(description, fallbackCommand string) string {
	if strings.TrimSpace(description) != "" {
		return description
	}
	return fallbackCommand
}

func buildCompactionPromptWithKeyFiles(input *compactionInput, relHistoryPath string, keyFiles []string, todos []tools.TodoItem, subAgents []SubAgentInfo, backgroundObjects []recovery.BackgroundObjectState) string {
	var sb strings.Builder
	sb.WriteString("Summarize the earlier conversation transcript below so the main coding agent can continue work.\n")
	sb.WriteString("Treat this as a durable checkpoint for the next coding turn, not as a narrative recap. Focus on current objective, constraints, decisions, progress, blockers, and concrete next steps.\n")
	sb.WriteString("A small raw evidence pack and recent raw tail may be kept after this summary, so focus on durable context from the archived head rather than duplicating those verbatim excerpts.\n\n")
	fmt.Fprintf(&sb, "Full archived history file for this compaction: %s\n", relHistoryPath)
	sb.WriteString("If this is not the first compaction, the checkpoint wrapper also lists all archived history files for the full session history chain.\n")
	if input != nil && input.OmittedMessages > 0 {
		fmt.Fprintf(&sb, "Compression note: the earliest %d archived message(s) were omitted from the summary input to fit the utility model budget. The archived history file is authoritative for those details.\n", input.OmittedMessages)
	}
	sb.WriteString("\nDurable anchors extracted before summarization:\n")
	sb.WriteString(formatCompactionAnchorsForPrompt(input))
	sb.WriteString("\n\nKey file candidates:\n")
	sb.WriteString(formatKeyFileCandidatesForPrompt(keyFiles))
	sb.WriteString("\n\nHigh-priority extracted evidence:\n")
	if input != nil {
		sb.WriteString(renderEvidenceItemsForPrompt(input.EvidenceItems))
	} else {
		sb.WriteString("- (none)")
	}
	sb.WriteString("\n\nPreserved recent continuation anchor:\n")
	if input != nil {
		sb.WriteString(input.RecentTailAnchor)
	} else {
		sb.WriteString("- (none)")
	}
	sb.WriteString("\n\nCurrent todo list from the pre-compaction agent. These todos are not automatically authoritative after compaction. Evaluate each item against the latest user request and classify it as active/relevant, completed/background, or stale/superseded in the summary:\n")
	sb.WriteString(formatTodosForPrompt(todos))
	sb.WriteString("\n\nCurrent sub-agent state:\n")
	sb.WriteString(formatSubAgentsForPrompt(subAgents))
	sb.WriteString("\n\nCurrent background objects:\n")
	sb.WriteString(formatBackgroundObjectsForPrompt(backgroundObjects))
	sb.WriteString("\n\nConversation transcript to summarize:\n\n")
	if input != nil {
		sb.WriteString(input.Transcript)
	}
	return sb.String()
}

func formatTodosForPrompt(todos []tools.TodoItem) string {
	if len(todos) == 0 {
		return "- (none)"
	}
	var sb strings.Builder
	for _, todo := range todos {
		line := fmt.Sprintf("- [%s] %s: %s", todo.Status, todo.ID, todo.Content)
		if todo.ActiveForm != "" {
			line += " | active: " + todo.ActiveForm
		}
		sb.WriteString(line)
		sb.WriteByte('\n')
	}
	return strings.TrimRight(sb.String(), "\n")
}

func formatSubAgentsForPrompt(subAgents []SubAgentInfo) string {
	visible, omitted := subAgentsForCompactionPrompt(subAgents)
	if len(visible) == 0 {
		if omitted > 0 {
			return fmt.Sprintf("- (none active; %d historical or completed task(s) omitted)", omitted)
		}
		return "- (none active)"
	}
	var sb strings.Builder
	for _, sub := range visible {
		running := sub.RunningRef
		if running == "" {
			running = sub.SelectedRef
		}
		fmt.Fprintf(&sb, "- %s | task=%s | state=%s | agent=%s | model=%s | desc=%s",
			sub.InstanceID, sub.TaskID, blankToDefault(sub.State, "unknown"), sub.AgentDefName, running, sub.TaskDesc)
		if strings.TrimSpace(sub.LastSummary) != "" {
			fmt.Fprintf(&sb, " | summary=%s", sub.LastSummary)
		}
		sb.WriteByte('\n')
	}
	if omitted > 0 {
		fmt.Fprintf(&sb, "- (%d historical or completed task(s) omitted from compaction prompt)\n", omitted)
	}
	return strings.TrimRight(sb.String(), "\n")
}

func blankToDefault(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func buildTruncateOnlySummary(relHistoryPath string, summarizeErr error, keyFiles []string, todos []tools.TodoItem, subAgents []SubAgentInfo, backgroundObjects []recovery.BackgroundObjectState) string {
	return renderFallbackSummarySections([]fallbackSummarySection{
		{"## Current User Request", "- Latest request was not relevance-filtered because model summarization was unavailable; read the preserved recent context and archived history before acting."},
		{"## Active Objective", "- Continue from the latest preserved user request; do not assume older todos remain active without checking relevance."},
		{"## Background Goals", "- Earlier goals are background until confirmed relevant to the latest preserved user request."},
		{"## User Constraints", "- Constraints may be incomplete because truncate-only fallback skipped model-generated summarization."},
		{"## Progress", "- Earlier history was compacted in truncate-only mode.\n- Use the archived history and key files below as the durable checkpoint."},
		{"## Key Decisions", "- Model-based context summarization was unavailable.\n- Continue from the archived history, key files, and preserved recent context instead of inventing missing decisions."},
		{"## Files and Evidence", fallbackFilesAndEvidenceSection(relHistoryPath, nil, keyFiles)},
		{"## Todo State", formatTodosAsRelevanceBullets(todos, fallbackAnchor{})},
		{"## SubAgent State", formatSubAgentsAsBullets(subAgents)},
		{"## Open Problems", fallbackOpenProblemsSection(nil, summarizeErr)},
		{"## Next Step", "- Continue from the latest preserved user request, archived history, and listed key files."},
	}, backgroundObjects)
}
