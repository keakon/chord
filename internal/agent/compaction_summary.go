package agent

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
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

func trimMessagesToBudget(mgr *ctxmgr.Manager, messages []message.Message, targetTokens int) ([]message.Message, int) {
	if len(messages) == 0 || targetTokens <= 0 {
		return nil, len(messages)
	}
	if estimateMessagesTokens(mgr, messages) <= targetTokens {
		out := make([]message.Message, len(messages))
		copy(out, messages)
		return out, 0
	}

	start := len(messages)
	remaining := targetTokens
	for i, message := range slices.Backward(messages) {
		cost := estimateMessageTokens(mgr, message)
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
	SessionAnchors   compactionAnchors
	GoalAnchor       string
	ConstraintAnchor string
	DecisionAnchor   string
	ProgressAnchor   string
	// PriorCheckpoint is the durable body of the most recent checkpoint inside
	// the archived head (see latestPriorCheckpointBody). It is surfaced to the
	// summarizer as a protected section it must fold in rather than rely on the
	// checkpoint surviving transcript trimming, so recursive compaction cannot
	// erode the previous checkpoint's structured sections one summary at a time.
	PriorCheckpoint string
}

// compactionPromptInputs carries the auxiliary sections the compaction prompt
// is built from (key-file candidates, todos, sub-agent state, background
// objects). The budget fit (fitCompactionInputToContextLimit) may degrade
// these section by section to admit a prompt; the exact lists that satisfied
// the admission gate must be the lists the prompt is actually assembled from,
// otherwise a prompt admitted only after degradation would still be sent with
// the full lists over the reserved budget.
type compactionPromptInputs struct {
	KeyFiles          []string
	Todos             []tools.TodoItem
	SubAgents         []SubAgentInfo
	BackgroundObjects []recovery.BackgroundObjectState
}

// compactionReductionScratch returns a throwaway agent carrying the reduction
// policy semantics of the live agent: the configured policy plus the read-only
// classification inputs — the tool registry (read-only shell verdicts), the
// project root (read path resolution), and immutable snapshots of the
// recall-protection sets (inputs whose outputs were already reduced and
// re-issued by the model). The compaction input is reduced before it reaches
// the summarizer, and it must follow the same reduction semantics as the main
// request: a session that raised the byte thresholds — or disabled reduction
// outright — should not have its durable summary silently built from
// default-trimmed tool output, and a read-only shell or a recalled input must
// not lose its protection either. Reduction cannot run on the live agent here,
// because prepareMessagesForLLM records the prepared request surface and would
// corrupt the incremental reduction cache of the in-flight main request; the
// scratch never shares live mutable reduction state (stable surface, turn,
// model run), and its own bookkeeping mutations stay on the copy.
func (a *MainAgent) compactionReductionScratch() *MainAgent {
	if a == nil {
		return &MainAgent{}
	}
	scratch := &MainAgent{
		globalConfig:  a.globalConfig,
		projectConfig: a.projectConfig,
		// The tool registry is immutable after startup, so sharing the pointer
		// is safe; it powers the read-only shell classification (and the
		// disk-backed read invalidation scan) during compaction-input building.
		tools:       a.tools,
		projectRoot: a.projectRoot,
		// The session dir is the reduction archive root. Without it, a
		// non-rebuildable output (job_output / delegate / notify / question) that the
		// main request archives in full would silently degrade to a lossy
		// generic marker in the durable summary input. Archive writes are
		// content-addressed, so the main request and this pass produce identical
		// bytes for the same payload.
		sessionDir: a.sessionDir,
	}
	if recalled := a.recalledReductionInputsSnapshot(); len(recalled) > 0 {
		scratch.recalledReductionInputs = recalled
	}
	if discarded := a.lastPreparedDiscardedInputsSnapshot(); len(discarded) > 0 {
		scratch.lastPreparedLLMDiscardedInputs = discarded
	}
	return scratch
}

func (a *MainAgent) buildCompactionInputWithOptions(head []message.Message, contextLimit int, evidenceItems []evidenceItem, recentTail []message.Message, sessionAnchors compactionAnchors) (*compactionInput, error) {
	pruned := a.compactionReductionScratch().prepareMessagesForLLM(head)
	normalized := normalizeMessagesForSummary(pruned)
	budget := compactionInputBudget(contextLimit)
	trimmed, omittedMessages := trimMessagesToBudget(a.ctxMgr, normalized, budget)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("compaction input too large even after truncation")
	}
	exported, err := session.Export(trimmed, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("build compaction transcript: %w", err)
	}
	if len(evidenceItems) == 0 {
		evidenceItems = selectEvidenceItems(pruned, contextLimit)
	}
	return &compactionInput{
		Transcript:       session.ExportToMarkdown(exported),
		OmittedMessages:  omittedMessages,
		EvidenceItems:    evidenceItems,
		RecentTail:       recentTail,
		RecentTailAnchor: formatRecentTailAnchor(recentTail),
		SessionAnchors:   sessionAnchors,
		PriorCheckpoint:  latestPriorCheckpointBody(head),
		// Evidence selection and the goal anchor classify what the user asked
		// for, so they read the unmerged surface: normalized folds @-injected
		// file bodies into Content, and a document's "must"/"do not" wording
		// would then be read as an instruction the user gave. The decision and
		// progress anchors read only assistant/tool messages, which carry no
		// @-injected file parts, so both surfaces are identical for them. If
		// either is ever extended to user messages, it must switch to pruned.
		GoalAnchor:       buildGoalAnchor(pruned),
		ConstraintAnchor: buildConstraintAnchor(evidenceItems),
		DecisionAnchor:   buildDecisionAnchor(normalized),
		ProgressAnchor:   buildProgressAnchor(normalized, evidenceItems),
	}, nil
}

func trimMessagesToBudgetWithReservedTail(mgr *ctxmgr.Manager, messages []message.Message, targetTokens int, reserveTail int) ([]message.Message, int) {
	if reserveTail <= 0 {
		return trimMessagesToBudget(mgr, messages, targetTokens)
	}
	trimmed, omitted := trimMessagesToBudget(mgr, messages, targetTokens-reserveTail)
	if len(trimmed) > 0 {
		return trimmed, omitted
	}
	return trimMessagesToBudget(mgr, messages, targetTokens)
}

// compactionPromptTokenEstimate sizes the assembled compaction prompt for
// fitCompactionInputToContextLimit. It deliberately stays on bytes/3 instead of
// the usage-calibrated estimator: this is the admission gate that decides
// whether a prompt may be sent, and bytes/3 approximates the densest realistic
// token/byte ratio, so it errs high. A calibrated ratio can read low (its
// denominator is the full history while its numerator comes from the reduced
// request surface), which here would admit an oversized prompt and turn a local
// trim decision into a provider rejection plus fallback.
func compactionPromptTokenEstimate(input *compactionInput, historyPath string, keyFiles []string, todos []tools.TodoItem, subAgents []SubAgentInfo, backgroundObjects []recovery.BackgroundObjectState) int {
	prompt := buildCompactionPromptWithKeyFiles(input, historyPath, keyFiles, todos, subAgents, backgroundObjects)
	return max(1, len(prompt)/3)
}

func (a *MainAgent) fitCompactionInputToContextLimit(head []message.Message, input *compactionInput, contextLimit int, historyPath string, keyFiles []string, todos []tools.TodoItem, subAgents []SubAgentInfo, backgroundObjects []recovery.BackgroundObjectState, maxOutputTokens int) (*compactionInput, *compactionPromptInputs, error) {
	if input == nil {
		return nil, nil, fmt.Errorf("compaction input is nil")
	}
	if contextLimit <= 0 {
		return nil, nil, fmt.Errorf("compaction context limit is unavailable; refusing unbounded admission")
	}
	fullInputs := &compactionPromptInputs{
		KeyFiles:          keyFiles,
		Todos:             todos,
		SubAgents:         subAgents,
		BackgroundObjects: backgroundObjects,
	}
	preflightBuffer := max(contextLimit/compactPreflightBufferRatio, compactPreflightBufferMin)
	allowedInput := contextLimit - maxOutputTokens - preflightBuffer
	if allowedInput <= 0 {
		return nil, nil, fmt.Errorf("compaction context limit too small after reserving output (%d)", contextLimit)
	}
	if compactionPromptTokenEstimate(input, historyPath, keyFiles, todos, subAgents, backgroundObjects) <= allowedInput {
		return input, fullInputs, nil
	}
	pruned := a.compactionReductionScratch().prepareMessagesForLLM(head)
	normalized := normalizeMessagesForSummary(pruned)
	budget := compactionInputBudget(contextLimit)
	for attempts := range 6 {
		candidateKeyFiles, candidateTodos, candidateSubAgents, candidateBackground := compactionPromptInputsForAttempt(
			keyFiles, todos, subAgents, backgroundObjects, attempts,
		)
		trimmed, omittedMessages := trimMessagesToBudgetWithReservedTail(a.ctxMgr, normalized, budget, attempts*512)
		if len(trimmed) == 0 {
			continue
		}
		exported, err := session.Export(trimmed, nil, nil)
		if err != nil {
			return nil, nil, fmt.Errorf("build compaction transcript during fit: %w", err)
		}
		candidate := *input
		candidate.Transcript = session.ExportToMarkdown(exported)
		candidate.OmittedMessages = omittedMessages
		if compactionPromptTokenEstimate(&candidate, historyPath, candidateKeyFiles, candidateTodos, candidateSubAgents, candidateBackground) <= allowedInput {
			return &candidate, &compactionPromptInputs{
				KeyFiles:          candidateKeyFiles,
				Todos:             candidateTodos,
				SubAgents:         candidateSubAgents,
				BackgroundObjects: candidateBackground,
			}, nil
		}
		budget -= max(512, budget/8)
		if budget <= contextLimit/8 {
			break
		}
	}
	return nil, nil, fmt.Errorf("compaction prompt still exceeds reserved context budget")
}

// compactionPromptInputsForAttempt drops only reconstructable or lower-authority
// auxiliary sections when transcript trimming alone cannot fit the prompt. The
// durable checkpoint still carries the transcript and authoritative anchors;
// these sections are presentation aids that can be rebuilt on the next pass.
func compactionPromptInputsForAttempt(keyFiles []string, todos []tools.TodoItem, subAgents []SubAgentInfo, backgroundObjects []recovery.BackgroundObjectState, attempt int) ([]string, []tools.TodoItem, []SubAgentInfo, []recovery.BackgroundObjectState) {
	switch attempt {
	case 0:
		return keyFiles, todos, subAgents, backgroundObjects
	case 1:
		return keyFiles, todos, subAgents, nil
	case 2:
		return nil, todos, subAgents, nil
	case 3:
		return nil, todos, nil, nil
	default:
		return nil, nil, nil, nil
	}
}

func buildGoalAnchor(messages []message.Message) string {
	for _, msg := range slices.Backward(messages) {

		if !message.IsUserAuthored(msg) {
			continue
		}
		text := message.UserPromptInstructionText(msg)
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
	// extraHeadingLineRegexps caches compiled full-line heading patterns for
	// headings outside compactionRequiredHeadings (for example the typed
	// checkpoint state section). The pattern is compiled once per heading and
	// reused, so parsing a typed block does not allocate a fresh regexp on
	// every checkpoint read.
	extraHeadingLineRegexpsMu sync.Mutex
	extraHeadingLineRegexps   = map[string]*regexp.Regexp{}
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

// markdownSectionBounds locates a summary section's body: start is the offset
// just past the heading, end is where the next "## " heading begins or the end
// of the summary. Readers and writers of a section share it so there is one
// definition of where a section stops.
func markdownSectionBounds(summary, heading string) (start, end int, ok bool) {
	pos := findMarkdownHeadingLine(summary, heading)
	if pos < 0 {
		return 0, 0, false
	}
	start = pos + len(heading)
	end = len(summary)
	if loc := compactionMarkdownHeadingLineRe.FindStringIndex(summary[start:]); loc != nil {
		end = start + loc[0]
	}
	return start, end, true
}

func markdownSection(summary, heading string) (string, bool) {
	start, end, ok := markdownSectionBounds(summary, heading)
	if !ok {
		return "", false
	}
	return strings.TrimSpace(summary[start:end]), true
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
		if strings.HasPrefix(line, "#") {
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
		// A heading outside the required section set (for example the typed
		// checkpoint state section) matches the same full-line rule: the
		// heading must start a line at column zero and be the line's entire
		// content. A mid-prose occurrence of the heading text can therefore
		// never be mistaken for the section. The pattern is cached, not
		// recompiled per lookup.
		extraHeadingLineRegexpsMu.Lock()
		pattern = extraHeadingLineRegexps[heading]
		if pattern == nil {
			pattern = regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(heading) + `\s*$`)
			extraHeadingLineRegexps[heading] = pattern
		}
		extraHeadingLineRegexpsMu.Unlock()
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
		fmt.Fprintf(&sb, "- [evidence:%s] %s", evidenceItemID(item), item.Title)
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
	b := contextLimit / compactRecentTailBudgetRatio
	b = max(b, compactRecentTailMinTokens)
	b = min(b, compactRecentTailMaxTokens)
	return b
}

// selectRecentTailMessages picks the raw tail kept verbatim after a
// continuation checkpoint. It prefers whole user turns (newest first) so the
// tail reads as complete exchanges. When even a single user turn exceeds the
// budget — the common case once that turn carries a real tool loop, which is
// exactly when compaction fires — it degrades to the longest safe suffix that
// fits instead of returning nothing, because an empty tail silently turns
// continuation compaction into summary-only compaction.
func selectRecentTailMessages(mgr *ctxmgr.Manager, messages []message.Message, userTurns int, maxTokens int) []message.Message {
	if len(messages) == 0 || maxTokens <= 0 {
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
		if estimateMessagesTokens(mgr, tail) <= maxTokens {
			return tail
		}
	}
	return longestSafeTailWithinBudget(mgr, messages, maxTokens)
}

// longestSafeTailWithinBudget returns the longest suffix that fits maxTokens and
// starts at an index SafeKeepBoundary already considers safe — so the tail never
// opens with an orphaned tool result and never splits a pending tool call. Only
// self-stable indices are accepted: a boundary SafeKeepBoundary would pull
// backwards could reintroduce messages the budget scan already rejected.
func longestSafeTailWithinBudget(mgr *ctxmgr.Manager, messages []message.Message, maxTokens int) []message.Message {
	best := -1
	cost := 0
	for i := len(messages) - 1; i > 0; i-- {
		cost += estimateMessageTokens(mgr, messages[i])
		if cost > maxTokens {
			break
		}
		if ctxmgr.SafeKeepBoundary(messages, i) == i {
			best = i
		}
	}
	if best <= 0 {
		return nil
	}
	return append([]message.Message(nil), messages[best:]...)
}

func formatRecentTailAnchor(messages []message.Message) string {
	if len(messages) == 0 {
		return "- (none)"
	}
	omitted := 0
	if len(messages) > compactRecentTailAnchorMessages {
		omitted = len(messages) - compactRecentTailAnchorMessages
		messages = messages[omitted:]
	}
	var sb strings.Builder
	if omitted > 0 {
		fmt.Fprintf(&sb, "- (+%d older preserved message(s) omitted here; still verbatim in context)\n", omitted)
	}
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

func buildStructuredFallbackSummary(historyPath string, input *compactionInput, summarizeErr error, keyFiles []string, todos []tools.TodoItem, subAgents []SubAgentInfo, backgroundObjects []recovery.BackgroundObjectState) string {
	anchor := fallbackContinuationAnchorForInput(input)
	return renderFallbackSummarySections([]fallbackSummarySection{
		{"## Current User Request", fallbackCurrentUserRequestSection(input)},
		{"## Active Objective", fallbackActiveObjectiveSection(anchor)},
		{"## Background Goals", "- Earlier goals are background until confirmed relevant to the latest preserved user request."},
		{"## User Constraints", renderEvidenceKindForFallback(input, evidenceUserCorrection, "- No preserved user constraints.")},
		{"## Progress", fallbackProgressSection(input)},
		{"## Key Decisions", "- Earlier durable decisions should be read from the archived history file if needed.\n- Preserve the recent continuation direction and evidence below."},
		{"## Files and Evidence", fallbackFilesAndEvidenceSection(historyPath, input, keyFiles)},
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
	// The latest Done-rejected reason and the latest ordinary user request are
	// compared by evidence Sequence (higher = more recent): a user request that
	// arrived after a Done rejection supersedes it, mirroring
	// resolveLatestUserRequestAnchor's message-order semantics.
	if anchor, ok := latestRequestOrDoneRejectedAnchor(input); ok {
		return anchor
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

// latestRequestOrDoneRejectedAnchor resolves the authoritative latest request
// from evidence by Sequence: a plain user request and a Done-rejected reason
// are both latest-request candidates, and whichever was recorded later wins
// (mirroring resolveLatestUserRequestAnchor's message-order comparison). This
// prevents an old rejection from shadowing a newer user request, and vice
// versa. Evidence selection keeps at most one item of each kind, so the max
// Sequence per kind is the newest of that kind.
func latestRequestOrDoneRejectedAnchor(input *compactionInput) (fallbackAnchor, bool) {
	if input == nil {
		return fallbackAnchor{}, false
	}
	doneSeq, doneText := -1, ""
	userSeq, userText := -1, ""
	for _, item := range input.EvidenceItems {
		switch item.Kind {
		case evidenceDoneRejected:
			if text := strings.TrimSpace(item.Excerpt); text != "" && item.Sequence > doneSeq {
				doneSeq, doneText = item.Sequence, text
			}
		case evidenceUserRequest:
			if text := strings.TrimSpace(item.Excerpt); text != "" && item.Sequence > userSeq {
				userSeq, userText = item.Sequence, text
			}
		}
	}
	switch {
	case doneText != "" && doneSeq >= userSeq:
		return fallbackAnchor{Kind: "done_rejected", Label: "Latest Done rejected reason", Text: doneText}, true
	case userText != "":
		return fallbackAnchor{Kind: "user_request", Label: "Latest user request", Text: userText}, true
	}
	return fallbackAnchor{}, false
}

// resolveLatestUserRequestAnchor is the authoritative latest-request resolver
// used by the deterministic model-driven checkpoint builder. It scans the
// transcript in reverse message order, considering only messages after the
// most recent compaction checkpoint (the raw tail): the latest ordinary
// user-authored request and the latest Done-rejected reason are both
// candidates, and whichever appears later in the transcript wins — a stale
// rejection can never shadow a newer user request, and vice versa. When the
// raw tail has no authoritative request, the `## Current User Request` section
// of the most recent checkpoint is inherited, so consecutive archival resets
// keep the current request alive. Returns an empty anchor when nothing
// authoritative exists.
func resolveLatestUserRequestAnchor(messages []message.Message) fallbackAnchor {
	lastCheckpointIdx := -1
	for i, message := range slices.Backward(messages) {
		if message.IsCompactionSummary {
			lastCheckpointIdx = i
			break
		}
	}

	userIdx, userText := -1, ""
	doneIdx, doneText := -1, ""
	for i := len(messages) - 1; i >= lastCheckpointIdx+1; i-- {
		msg := messages[i]
		if userIdx < 0 && isAuthoritativeUserRequest(msg) {
			userIdx = i
			userText = strings.TrimSpace(message.UserPromptInstructionText(msg))
		}
		if doneIdx < 0 && msg.Role == message.RoleTool {
			if reason, ok := doneRejectedToolResult(messages, i); ok && strings.TrimSpace(reason) != "" {
				doneIdx = i
				doneText = strings.TrimSpace(reason)
			}
		}
		if userIdx >= 0 && doneIdx >= 0 {
			break
		}
	}
	if userIdx >= 0 || doneIdx >= 0 {
		if doneIdx > userIdx {
			return fallbackAnchor{Kind: "done_rejected", Label: "Latest Done rejected reason", Text: doneText}
		}
		return fallbackAnchor{Kind: "user_request", Label: "Latest user request", Text: userText}
	}
	if lastCheckpointIdx >= 0 {
		if section, ok := compactionCurrentUserRequestSection(messages[lastCheckpointIdx].Content); ok {
			return fallbackAnchor{Kind: "inherited_checkpoint", Label: inheritedCheckpointLabel, Text: section}
		}
	}
	return fallbackAnchor{}
}

// isAuthoritativeUserRequest reports whether a message is an ordinary
// user-authored request (not a compaction summary, session reminder, system
// overlay, or non-user mailbox message).
func isAuthoritativeUserRequest(msg message.Message) bool {
	if !message.IsUserAuthored(msg) {
		return false
	}
	return isPlainUserRequestForCompaction(strings.TrimSpace(message.UserPromptInstructionText(msg)))
}

// compactionCurrentUserRequestSection extracts the body of the
// `## Current User Request` section from a checkpoint message, skipping
// "Unknown" placeholders.
func compactionCurrentUserRequestSection(content string) (string, bool) {
	const heading = "## Current User Request"
	_, section, ok := strings.Cut(content, heading)
	if !ok {
		return "", false
	}
	if before, _, found := strings.Cut(section, "\n## "); found {
		section = before
	}
	section = strings.TrimSpace(section)
	if section == "" || strings.HasPrefix(section, "- Unknown:") {
		return "", false
	}
	return section, true
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

func fallbackFilesAndEvidenceSection(historyPath string, input *compactionInput, keyFiles []string) string {
	lines := []string{
		"- Archived history for this compaction: " + historyPath,
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

// formatTodosAsRelevanceBullets renders the Todo State section of a
// fallback-generated checkpoint. Only a Done rejection demotes the runtime
// todos into the stale bucket — the user refused the previous completion, so
// the old targets are stale and restore drops them. Any other anchor (a plain
// user request) or no anchor must not label runtime todos stale: the complete
// list is carried by ensureCompactionTodoSnapshot as the `### Runtime TODO
// snapshot` block, and restore only drops todos it sees in the stale bucket.
// Line-escaping keeps a todo containing newlines or headings from escaping
// its section or forging a top-level heading.
func formatTodosAsRelevanceBullets(todos []tools.TodoItem, anchor fallbackAnchor) string {
	if strings.TrimSpace(anchor.Text) != "" {
		lines := []string{
			"- Active/relevant to latest request:",
			"  - " + anchor.Label + ": " + strings.ReplaceAll(anchor.Text, "\n", " "),
			"- Completed/background:",
			"  - (none classified by fallback)",
			"- Stale/superseded:",
		}
		if anchor.Kind == "done_rejected" {
			if len(todos) == 0 {
				lines = append(lines, "  - (none)")
			}
			for _, todo := range todos {
				lines = append(lines, "  - ["+todo.Status+"] "+escapeTodoContentLine(todo.ID)+": "+escapeTodoContentLine(todo.Content))
			}
		} else {
			lines = append(lines, "  - (none classified by fallback)")
		}
		return strings.Join(lines, "\n")
	}
	return strings.Join([]string{
		"- Active/relevant to latest request:",
		"  - (none reliably classified by fallback)",
		"- Completed/background:",
		"  - (none classified by fallback)",
		"- Stale/superseded:",
		"  - (none classified by fallback)",
	}, "\n")
}

// escapeTodoContentLine flattens a model-supplied todo field onto one line:
// newlines become the "    > " continuation used by the runtime snapshot, so
// content can never reach column zero and cannot forge a heading like "## ".
func escapeTodoContentLine(s string) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\r", "")
	return strings.ReplaceAll(s, "\n", "\n    > ")
}

// ensureCompactionTodoSnapshot keeps the runtime-owned todo list available
// even when the summarizer classifies it as background or omits it. The model
// still supplies the relevance classification above this snapshot, but the
// complete pre-compaction state must not depend on model wording.
func ensureCompactionTodoSnapshot(summary string, todos []tools.TodoItem) string {
	if strings.TrimSpace(summary) == "" || len(todos) == 0 {
		return summary
	}
	sectionStart, sectionEnd, ok := markdownSectionBounds(summary, "## Todo State")
	if !ok || strings.Contains(summary[sectionStart:sectionEnd], "### Runtime TODO snapshot") {
		return summary
	}
	var snapshot strings.Builder
	snapshot.WriteString("### Runtime TODO snapshot\n")
	snapshot.WriteString("- Complete pre-compaction runtime state; classify against the latest user request and reconcile with ## Progress above before acting:\n")
	kept, omitted := boundedTodoSnapshotItems(todos)
	for _, todo := range kept {
		content := escapeTodoContentLine(compactTextSnippet(todo.Content, compactTodoSnapshotItemChars))
		fmt.Fprintf(&snapshot, "  - [%s] %s: %s", todo.Status, todo.ID, content)
		if activeForm := strings.TrimSpace(todo.ActiveForm); activeForm != "" {
			fmt.Fprintf(&snapshot, " | active: %s", escapeTodoContentLine(compactTextSnippet(activeForm, compactTodoSnapshotItemChars)))
		}
		snapshot.WriteByte('\n')
	}
	if omitted > 0 {
		fmt.Fprintf(&snapshot, "  - (%d earlier completed/cancelled todos omitted; the archived history holds them)\n", omitted)
	}
	snapshotText := strings.TrimRight(snapshot.String(), "\n")
	tail := strings.TrimLeft(summary[sectionEnd:], "\n")
	out := strings.TrimRight(summary[:sectionEnd], "\n") + "\n" + snapshotText
	if tail == "" {
		return out
	}
	return out + "\n\n" + tail
}

// boundedTodoSnapshotItems caps the snapshot while preferring the items that
// still describe outstanding work. Unfinished todos are what a continuation
// acts on, so they claim the budget first, in list order so the ones nearest
// the current position survive; finished ones backfill any remaining room,
// newest first. The cap binds unconditionally — a list that is all unfinished
// is exactly the case where an unbounded section would keep growing.
func boundedTodoSnapshotItems(todos []tools.TodoItem) ([]tools.TodoItem, int) {
	if len(todos) <= compactTodoSnapshotMaxItems {
		return todos, 0
	}
	// Marked by position, not by ID: todo IDs carry no uniqueness guarantee,
	// and the snapshot has to come back out in the list's original order.
	keep := make([]bool, len(todos))
	kept := 0
	for i, todo := range todos {
		if kept >= compactTodoSnapshotMaxItems {
			break
		}
		if !isFinishedTodoStatus(todo.Status) {
			keep[i] = true
			kept++
		}
	}
	// Backfill with the newest finished todos: they carry the most recent
	// progress, which is what a reader reconciles against ## Progress.
	for i := len(todos) - 1; i >= 0 && kept < compactTodoSnapshotMaxItems; i-- {
		if !keep[i] && isFinishedTodoStatus(todos[i].Status) {
			keep[i] = true
			kept++
		}
	}
	out := make([]tools.TodoItem, 0, kept)
	for i, todo := range todos {
		if keep[i] {
			out = append(out, todo)
		}
	}
	return out, len(todos) - len(out)
}

func isFinishedTodoStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case tools.TodoStatusCompleted, tools.TodoStatusCancelled:
		return true
	}
	return false
}

func subAgentStateNeedsPromptContext(state string) bool {
	switch strings.TrimSpace(state) {
	case string(SubAgentStateRunning), string(SubAgentStateIdle), string(SubAgentStateWaitingMain), string(SubAgentStateWaitingDescendant):
		return true
	default:
		return false
	}
}

func subAgentPromptPriority(state string) int {
	switch strings.TrimSpace(state) {
	case string(SubAgentStateWaitingMain):
		return 0
	case string(SubAgentStateWaitingDescendant):
		return 1
	case string(SubAgentStateRunning):
		return 2
	case string(SubAgentStateIdle):
		return 3
	default:
		return 4 // terminal / historical / unknown: never prompt-visible
	}
}

// sortSubAgentsForPrompt orders a sub-agent snapshot deterministically —
// workers waiting on the main agent first, then running ones, then task ID —
// so the bounded prompt keeps a stable, priority-aware subset across
// captures. The registry is a map, so without the sort the choice of which
// workers fit compactPromptSubAgentLimit would follow Go's random iteration
// order and drift between two compactions of identical state.
func sortSubAgentsForPrompt(subAgents []SubAgentInfo) {
	slices.SortStableFunc(subAgents, func(a, b SubAgentInfo) int {
		if pa, pb := subAgentPromptPriority(a.State), subAgentPromptPriority(b.State); pa != pb {
			return pa - pb
		}
		if c := strings.Compare(a.TaskID, b.TaskID); c != 0 {
			return c
		}
		return strings.Compare(a.InstanceID, b.InstanceID)
	})
}

func subAgentsForCompactionPrompt(subAgents []SubAgentInfo) (visible []SubAgentInfo, omittedActive, omittedInactive int) {
	if len(subAgents) == 0 {
		return nil, 0, 0
	}
	sorted := append([]SubAgentInfo(nil), subAgents...)
	sortSubAgentsForPrompt(sorted)
	visible = make([]SubAgentInfo, 0, min(len(sorted), compactPromptSubAgentLimit))
	for _, sub := range sorted {
		if !subAgentStateNeedsPromptContext(sub.State) {
			omittedInactive++
			continue
		}
		if len(visible) >= compactPromptSubAgentLimit {
			omittedActive++
			continue
		}
		copySub := sub
		copySub.TaskDesc = strings.ReplaceAll(compactTextSnippet(strings.TrimSpace(copySub.TaskDesc), compactPromptDescMaxChars), "\n", " ")
		copySub.LastSummary = strings.ReplaceAll(compactTextSnippet(strings.TrimSpace(copySub.LastSummary), compactPromptSummaryMaxChars), "\n", " ")
		visible = append(visible, copySub)
	}
	return visible, omittedActive, omittedInactive
}

func formatSubAgentInfoLine(sub SubAgentInfo) string {
	running := sub.RunningRef
	if running == "" {
		running = sub.SelectedRef
	}
	line := fmt.Sprintf("- %s | task=%s", sub.InstanceID, sub.TaskID)
	if parent := formatSubAgentParent(sub); parent != "" {
		line += " | parent=" + parent
	}
	line += fmt.Sprintf(" | state=%s | agent=%s | model=%s | desc=%s", blankToDefault(sub.State, "unknown"), sub.AgentDefName, running, sub.TaskDesc)
	if strings.TrimSpace(sub.LastSummary) != "" {
		line += " | summary=" + sub.LastSummary
	}
	return line
}

// formatSubAgentParent renders a worker's owning task as "<agent>/<task>", or
// "" for a worker owned directly by the main agent — its parent is whoever
// reads this checkpoint, so naming it adds nothing.
func formatSubAgentParent(sub SubAgentInfo) string {
	owner := strings.TrimSpace(sub.OwnerAgentID)
	if owner == "" {
		return ""
	}
	if task := strings.TrimSpace(sub.OwnerTaskID); task != "" {
		return owner + "/" + task
	}
	return owner
}

// formatSubAgentPromptLines renders the active worker rows plus omission
// footers shared by the summarizer input ("Current sub-agent state") and the
// checkpoint's authoritative "## SubAgent State" section. Omitted workers are
// split by why they were omitted: an active worker beyond the prompt limit is
// still running and will report back through mailbox events, which is not the
// same fact as a settled historical task.
func formatSubAgentPromptLines(subAgents []SubAgentInfo) string {
	visible, omittedActive, omittedInactive := subAgentsForCompactionPrompt(subAgents)
	if len(visible) == 0 {
		if omittedInactive > 0 {
			return fmt.Sprintf("- (none active; %d historical or completed task(s) omitted)", omittedInactive)
		}
		return "- (none active)"
	}
	lines := make([]string, 0, len(visible)+2)
	for _, sub := range visible {
		lines = append(lines, formatSubAgentInfoLine(sub))
	}
	if omittedActive > 0 {
		lines = append(lines, fmt.Sprintf("- (%d active task(s) beyond the prompt limit omitted; they keep running and report back through mailbox events)", omittedActive))
	}
	if omittedInactive > 0 {
		lines = append(lines, fmt.Sprintf("- (%d historical or completed task(s) omitted from compaction prompt)", omittedInactive))
	}
	return strings.Join(lines, "\n")
}

func formatSubAgentsAsBullets(subAgents []SubAgentInfo) string {
	return formatSubAgentPromptLines(subAgents)
}

// ensureCompactionSubAgentSnapshot replaces whatever "## SubAgent State"
// section a model-authored summary carries with the authoritative runtime
// rendering. Unlike todos — which the model legitimately classifies by
// relevance — the set of active workers is a runtime fact whose only
// legitimate source is the snapshot handed to the summarizer; a restatement
// can silently drop a delegated task (the failure the todo and skills
// snapshots already guard against), and anything it adds beyond that snapshot
// is at best an echo of archived history that stays recoverable from the
// archive files. The rendering is deterministic and position-preserving, so
// the structured-fallback and truncate-only summaries pass through unchanged
// and the required-section ordering validation still applies afterwards.
func ensureCompactionSubAgentSnapshot(summary string, subAgents []SubAgentInfo) string {
	if strings.TrimSpace(summary) == "" {
		return summary
	}
	pos := findMarkdownHeadingLine(summary, "## SubAgent State")
	if pos < 0 {
		return summary
	}
	_, end, ok := markdownSectionBounds(summary, "## SubAgent State")
	if !ok {
		return summary
	}
	rendered := formatSubAgentsAsBullets(subAgents)
	prefix := strings.TrimRight(summary[:pos], "\n")
	tail := strings.TrimLeft(summary[end:], "\n")
	var b strings.Builder
	b.WriteString(prefix)
	b.WriteString("\n## SubAgent State\n")
	b.WriteString(rendered)
	if tail != "" {
		b.WriteString("\n\n")
		b.WriteString(tail)
	}
	return b.String()
}

func formatBackgroundObjectsForPrompt(jobs []recovery.BackgroundObjectState) string {
	if len(jobs) == 0 {
		return "- (none)"
	}
	var sb strings.Builder
	for _, job := range jobs {
		fmt.Fprintf(&sb, "- %s | agent=%s | status=%s | started=%s | desc=%s", job.ID, backgroundObjectPromptAgent(job.AgentID), job.Status, job.StartedAt.Format(time.DateTime), backgroundObjectPromptDescription(job.Description, job.Command))
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

func backgroundObjectPromptDescription(description, fallbackCommand string) string {
	if strings.TrimSpace(description) != "" {
		return description
	}
	return fallbackCommand
}

func buildCompactionPromptWithKeyFiles(input *compactionInput, historyPath string, keyFiles []string, todos []tools.TodoItem, subAgents []SubAgentInfo, backgroundObjects []recovery.BackgroundObjectState) string {
	var sb strings.Builder
	sb.WriteString("Summarize the earlier conversation transcript below so the main coding agent can continue work.\n")
	sb.WriteString("Treat this as a durable checkpoint for the next coding turn, not as a narrative recap. Focus on current objective, constraints, decisions, progress, blockers, and concrete next steps.\n")
	sb.WriteString("A small raw evidence pack and recent raw tail may be kept after this summary, so focus on durable context from the archived head rather than duplicating those verbatim excerpts.\n\n")
	fmt.Fprintf(&sb, "Full archived history file for this compaction: %s\n", historyPath)
	sb.WriteString("If this is not the first compaction, the checkpoint wrapper also lists all archived history files for the full session history chain.\n")
	if input != nil && input.OmittedMessages > 0 {
		fmt.Fprintf(&sb, "Compression note: the earliest %d archived message(s) were omitted from the summary input to fit the utility model budget. The archived history file is authoritative for those details.\n", input.OmittedMessages)
	}
	sb.WriteString("\nDurable session anchors (carried forward verbatim in the checkpoint):\n")
	if input != nil {
		sb.WriteString(formatCompactionAnchorsForSummarizePrompt(input.SessionAnchors))
	} else {
		sb.WriteString(formatCompactionAnchorsForSummarizePrompt(compactionAnchors{}))
	}
	sb.WriteString("\n\nDurable anchors extracted before summarization:\n")
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
	sb.WriteString(formatSubAgentsAsBullets(subAgents))
	sb.WriteString("\n\nCurrent background objects:\n")
	sb.WriteString(formatBackgroundObjectsForPrompt(backgroundObjects))
	if input != nil && strings.TrimSpace(input.PriorCheckpoint) != "" {
		sb.WriteString("\n\nPrior durable checkpoint from an earlier compaction of this session — always present, independent of transcript trimming:\n")
		sb.WriteString("Fold its still-accurate content into your summary. Do not silently drop or contradict the sections it established. The session anchors are carried forward verbatim separately; do not restate them.\n\n")
		sb.WriteString(formatPriorCheckpointCarryForPrompt(input.PriorCheckpoint))
		sb.WriteByte('\n')
	}
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

func blankToDefault(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func buildTruncateOnlySummary(historyPath string, summarizeErr error, keyFiles []string, todos []tools.TodoItem, subAgents []SubAgentInfo, backgroundObjects []recovery.BackgroundObjectState) string {
	return renderFallbackSummarySections([]fallbackSummarySection{
		{"## Current User Request", "- Latest request was not relevance-filtered because model summarization was unavailable; read the preserved recent context and archived history before acting."},
		{"## Active Objective", "- Continue from the latest preserved user request; do not assume older todos remain active without checking relevance."},
		{"## Background Goals", "- Earlier goals are background until confirmed relevant to the latest preserved user request."},
		{"## User Constraints", "- Constraints may be incomplete because truncate-only fallback skipped model-generated summarization."},
		{"## Progress", "- Earlier history was compacted in truncate-only mode.\n- Use the archived history and key files below as the durable checkpoint."},
		{"## Key Decisions", "- Model-based context summarization was unavailable.\n- Continue from the archived history, key files, and preserved recent context instead of inventing missing decisions."},
		{"## Files and Evidence", fallbackFilesAndEvidenceSection(historyPath, nil, keyFiles)},
		{"## Todo State", formatTodosAsRelevanceBullets(todos, fallbackAnchor{})},
		{"## SubAgent State", formatSubAgentsAsBullets(subAgents)},
		{"## Open Problems", fallbackOpenProblemsSection(nil, summarizeErr)},
		{"## Next Step", "- Continue from the latest preserved user request, archived history, and listed key files."},
	}, backgroundObjects)
}
