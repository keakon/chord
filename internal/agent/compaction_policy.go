package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/session"
	"github.com/keakon/chord/internal/tools"
)

func evidencePackTokenBudget(contextLimit int) int {
	if contextLimit <= 0 {
		return compactEvidenceMaxTokens
	}
	b := contextLimit * compactEvidencePercentNumer / compactEvidencePercentDenom
	b = max(b, compactEvidenceMinTokens)
	b = min(b, compactEvidenceMaxTokens)
	return b
}

func splitMessagesForCompactionWithSelections(messages []message.Message, recentTail []message.Message, evidenceItems []evidenceItem) (head []message.Message, evidence []message.Message) {
	if len(messages) < 4 {
		return nil, nil
	}
	archiveEnd := len(messages)
	if len(recentTail) > 0 {
		archiveEnd = len(messages) - len(recentTail)
	}
	if archiveEnd <= 0 {
		return nil, nil
	}
	archiveHead := make([]message.Message, archiveEnd)
	copy(archiveHead, messages[:archiveEnd])
	if len(evidenceItems) == 0 {
		return archiveHead, nil
	}
	artifact := renderEvidenceArtifact(evidenceItems)
	return archiveHead, []message.Message{artifact}
}

func (a *MainAgent) prepareMessagesForLLM(messages []message.Message) []message.Message {
	if len(messages) == 0 {
		return nil
	}

	prepared := make([]message.Message, len(messages))
	copy(prepared, messages)

	callMeta := buildToolCallMeta(prepared)
	turnsAfter := userTurnsAfter(prepared)
	repeated := detectRepeatedToolOutputs(prepared, callMeta)
	toolResults := countToolResults(prepared)

	for i := range prepared {
		if prepared[i].Role != "tool" {
			continue
		}
		age := turnsAfter[i]
		if repeated[i] && age >= 1 {
			meta := callMeta[prepared[i].ToolCallID]
			prepared[i].Content = fmt.Sprintf("[Repeated %s output omitted; an identical call appears later.]", toolNameOrUnknown(meta.Name))
			prepared[i].ToolDiff = ""
			continue
		}
		if age >= compactErrorAgeTurns && isToolErrorContent(prepared[i].Content) {
			prepared[i].Content = "[Older tool error omitted]"
			prepared[i].ToolDiff = ""
			continue
		}
		if age >= compactConfirmAgeTurns && isConfirmationOutput(prepared[i].Content) {
			prepared[i].Content = "[Confirmed]"
			prepared[i].ToolDiff = ""
			continue
		}
		if age >= compactBashSuccessAgeTurns && len(prepared[i].Content) > compactBashSuccessBytes {
			meta := callMeta[prepared[i].ToolCallID]
			if strings.TrimSpace(meta.Name) == "Bash" {
				prepared[i].Content = "[Older Bash output omitted to save context; re-run the command if needed.]"
				prepared[i].ToolDiff = ""
				continue
			}
		}
		if age >= compactReadLikeAgeTurns && len(prepared[i].Content) > compactReadLikeOutputBytes {
			meta := callMeta[prepared[i].ToolCallID]
			if tools.IsReadLike(meta.Name) {
				prepared[i].Content = compactReadLikeOutputSummary(meta.Name, meta.Args, prepared[i].Content)
				prepared[i].ToolDiff = ""
				continue
			}
		}
		if toolResults >= compactMinToolResultsPrune &&
			age >= compactStaleAgeTurns &&
			len(prepared[i].Content) > compactStaleOutputBytes {
			meta := callMeta[prepared[i].ToolCallID]
			prepared[i].Content = fmt.Sprintf("[Older %s output omitted to save context; refer to exported history if needed.]", toolNameOrUnknown(meta.Name))
			prepared[i].ToolDiff = ""
		}
	}

	return prepared
}

func buildToolCallMeta(messages []message.Message) map[string]toolCallMeta {
	meta := make(map[string]toolCallMeta)
	for _, msg := range messages {
		for _, tc := range msg.ToolCalls {
			meta[tc.ID] = toolCallMeta{Name: tc.Name, Args: string(tc.Args)}
		}
	}
	return meta
}

func extractReadToolPath(argsJSON string) string {
	if strings.TrimSpace(argsJSON) == "" {
		return ""
	}
	var parsed struct {
		FilePath string `json:"path"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &parsed); err != nil {
		return ""
	}
	return strings.TrimSpace(parsed.FilePath)
}

func compactReadOutputSummary(argsJSON, content string) string {
	path := extractReadToolPath(argsJSON)
	snippet := strings.TrimSpace(compactTextSnippet(content, compactReadSnippetChars))
	if snippet == "" {
		snippet = "(no preserved excerpt)"
	}
	if path == "" {
		return "[Older Read output truncated to save context]\n" + snippet + "\n\n[Re-run Read with offset/limit if needed.]"
	}
	return fmt.Sprintf("[Older Read output truncated to save context; file=%s]\n%s\n\n[Re-run Read(path=%q, offset, limit) if needed.]", path, snippet, path)
}

func compactReadLikeOutputSummary(toolName, argsJSON, content string) string {
	switch strings.TrimSpace(toolName) {
	case tools.NameRead:
		return compactReadOutputSummary(argsJSON, content)
	case tools.NameGrep:
		return compactGrepOutputSummary(argsJSON, content)
	case tools.NameGlob:
		return compactGlobOutputSummary(argsJSON, content)
	case tools.NameWebFetch:
		return compactWebFetchOutputSummary(argsJSON, content)
	default:
		return fmt.Sprintf("[Older %s output omitted to save context; re-run the tool if needed.]", toolNameOrUnknown(toolName))
	}
}

func compactGrepOutputSummary(argsJSON, content string) string {
	var parsed struct {
		Pattern  string `json:"pattern"`
		FilePath string `json:"path"`
		Glob     string `json:"glob"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &parsed)
	snippet := strings.TrimSpace(compactTextSnippet(content, compactReadSnippetChars))
	if snippet == "" {
		snippet = "(no preserved excerpt)"
	}
	filePath := strings.TrimSpace(parsed.FilePath)
	return fmt.Sprintf(
		"[Older Grep output truncated to save context; pattern=%q path=%q glob=%q]\n%s\n\n[Re-run Grep(pattern=%q, path=%q, glob=%q) if needed.]",
		strings.TrimSpace(parsed.Pattern),
		blankToDefault(filePath, "."),
		strings.TrimSpace(parsed.Glob),
		snippet,
		strings.TrimSpace(parsed.Pattern),
		blankToDefault(filePath, "."),
		strings.TrimSpace(parsed.Glob),
	)
}

func compactGlobOutputSummary(argsJSON, content string) string {
	var parsed struct {
		Pattern  string `json:"pattern"`
		FilePath string `json:"path"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &parsed)
	snippet := strings.TrimSpace(compactTextSnippet(content, compactReadSnippetChars))
	if snippet == "" {
		snippet = "(no preserved excerpt)"
	}
	filePath := strings.TrimSpace(parsed.FilePath)
	return fmt.Sprintf(
		"[Older Glob output truncated to save context; pattern=%q path=%q]\n%s\n\n[Re-run Glob(pattern=%q, path=%q) if needed.]",
		strings.TrimSpace(parsed.Pattern),
		blankToDefault(filePath, "."),
		snippet,
		strings.TrimSpace(parsed.Pattern),
		blankToDefault(filePath, "."),
	)
}

func compactWebFetchOutputSummary(argsJSON, content string) string {
	var parsed struct {
		URL     string `json:"url"`
		Raw     bool   `json:"raw"`
		Timeout int    `json:"timeout"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &parsed)
	snippetSource := stripWebFetchResultHeaders(content)
	snippet := strings.TrimSpace(compactTextSnippet(snippetSource, compactReadSnippetChars))
	if snippet == "" {
		snippet = "(no preserved excerpt)"
	}
	return fmt.Sprintf(
		"[Older WebFetch output truncated to save context; url=%q raw=%t timeout=%d]\n%s\n\n[Re-run WebFetch(url=%q, raw=%t, timeout=%d) if needed.]",
		strings.TrimSpace(parsed.URL),
		parsed.Raw,
		parsed.Timeout,
		snippet,
		strings.TrimSpace(parsed.URL),
		parsed.Raw,
		parsed.Timeout,
	)
}

func stripWebFetchResultHeaders(content string) string {
	if idx := strings.Index(content, "\n\n"); idx >= 0 {
		return content[idx+2:]
	}
	return content
}

func userTurnsAfter(messages []message.Message) []int {
	turnsAfter := make([]int, len(messages))
	seenUsers := 0
	for i := len(messages) - 1; i >= 0; i-- {
		turnsAfter[i] = seenUsers
		if messages[i].Role == "user" {
			seenUsers++
		}
	}
	return turnsAfter
}

func detectRepeatedToolOutputs(messages []message.Message, meta map[string]toolCallMeta) map[int]bool {
	repeated := make(map[int]bool)
	seen := make(map[string]bool)
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg.Role != "tool" {
			continue
		}
		call, ok := meta[msg.ToolCallID]
		if !ok {
			continue
		}
		key := call.Name + "\x00" + call.Args
		if seen[key] {
			repeated[i] = true
			continue
		}
		seen[key] = true
	}
	return repeated
}

func countToolResults(messages []message.Message) int {
	total := 0
	for _, msg := range messages {
		if msg.Role == "tool" {
			total++
		}
	}
	return total
}

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
	for i := len(messages) - 1; i >= 0; i-- {
		cost := ctxmgr.EstimateMessageTokens(messages[i])
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
		Role: "user",
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
	for attempts := 0; attempts < 6; attempts++ {
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
	for _, msg := range messages {
		if msg.Role != "user" {
			continue
		}
		text := strings.TrimSpace(msg.Content)
		if text == "" {
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
		if msg.Role != "assistant" {
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
			if msg.Role != "assistant" && msg.Role != "tool" {
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
		return "Goal anchor:\n- (none)\n\nConstraint anchor:\n- (none)\n\nDecision anchor:\n- (none)\n\nRecent progress anchor:\n- (none)"
	}
	return strings.Join([]string{
		"Goal anchor:\n" + input.GoalAnchor,
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
	if len([]rune(summary)) < compactSummaryMinChars {
		return fmt.Errorf("compaction summary too short (%d chars)", len([]rune(summary)))
	}
	matched := 0
	for _, heading := range compactionRequiredHeadings {
		if strings.Contains(summary, heading) {
			matched++
		}
	}
	if matched < len(compactionRequiredHeadings)-1 {
		return fmt.Errorf("compaction summary missing required sections (%d/%d)", matched, len(compactionRequiredHeadings))
	}
	return nil
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
		for i := len(messages) - 1; i >= 0; i-- {
			start = i
			if messages[i].Role == "user" {
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
	return renderFallbackSummarySections([]fallbackSummarySection{
		{"## Goal", "- Continue the active coding task using the archived history and preserved recent context."},
		{"## User Constraints", renderEvidenceKindForFallback(input, evidenceUserCorrection, "- No preserved user constraints.")},
		{"## Progress", fallbackProgressSection(input)},
		{"## Key Decisions", "- Earlier durable decisions should be read from the archived history file if needed.\n- Preserve the recent continuation direction and evidence below."},
		{"## Files and Evidence", fallbackFilesAndEvidenceSection(relHistoryPath, input, keyFiles)},
		{"## Todo State", formatTodosAsBullets(todos)},
		{"## SubAgent State", formatSubAgentsAsBullets(subAgents)},
		{"## Open Problems", fallbackOpenProblemsSection(input, summarizeErr)},
		{"## Next Step", fallbackNextStepSection(input)},
	}, backgroundObjects)
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
	if len(input.EvidenceItems) > 0 {
		lines = append(lines, fmt.Sprintf("- Preserved %d high-priority evidence item(s).", len(input.EvidenceItems)))
	}
	return strings.Join(lines, "\n")
}

func fallbackFilesAndEvidenceSection(relHistoryPath string, input *compactionInput, keyFiles []string) string {
	lines := []string{"- Archived history: " + relHistoryPath}
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
	if input == nil || input.RecentTailAnchor == "- (none)" {
		return "- Continue from the archived history and preserved evidence."
	}
	return "- Continue from the preserved recent context below:\n" + input.RecentTailAnchor
}

func formatTodosAsBullets(todos []tools.TodoItem) string {
	if len(todos) == 0 {
		return "- (none)"
	}
	var lines []string
	for _, todo := range todos {
		lines = append(lines, fmt.Sprintf("- [%s] %s: %s", todo.Status, todo.ID, todo.Content))
	}
	return strings.Join(lines, "\n")
}

func subAgentStateNeedsPromptContext(state string) bool {
	switch strings.TrimSpace(state) {
	case string(SubAgentStateRunning), string(SubAgentStateIdle), string(SubAgentStateWaitingPrimary), string(SubAgentStateWaitingDescendant):
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
	fmt.Fprintf(&sb, "Full archived history file: %s\n", relHistoryPath)
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
	sb.WriteString("\n\nCurrent todo list:\n")
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
		{"## Goal", "- Continue the active coding task using the archived history and preserved recent context."},
		{"## User Constraints", "- Constraints may be incomplete because truncate-only fallback skipped model-generated summarization."},
		{"## Progress", "- Earlier history was compacted in truncate-only mode.\n- Use the archived history and key files below as the durable checkpoint."},
		{"## Key Decisions", "- Model-based context summarization was unavailable.\n- Continue from the archived history, key files, and preserved recent context instead of inventing missing decisions."},
		{"## Files and Evidence", fallbackFilesAndEvidenceSection(relHistoryPath, nil, keyFiles)},
		{"## Todo State", formatTodosAsBullets(todos)},
		{"## SubAgent State", formatSubAgentsAsBullets(subAgents)},
		{"## Open Problems", fallbackOpenProblemsSection(nil, summarizeErr)},
		{"## Next Step", "- Continue from the archived history and listed key files."},
	}, backgroundObjects)
}
