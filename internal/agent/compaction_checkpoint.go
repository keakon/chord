package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

// Checkpoint recent-message retention.
//
// A checkpoint replaces the archived head with a summary, moving the real
// conversation out of the live context. The newest real user messages are kept
// verbatim inside the checkpoint (bounded by a small token budget) so the
// model can pick the latest instruction boundary back up without re-reading
// the archives — codex keeps recent user messages in its compacted history for
// the same reason. A dangling interrupted assistant reply is kept too:
// compaction often lands right after a streamed reply was cut, and that
// partial text is exactly what the continuation must resume.
const (
	// retainedRecentMessagesHeading introduces the retained section inside the
	// checkpoint wrapper. It lives outside the summary body ([Context Summary]
	// .. [Context compressed]), so prior-checkpoint carry and the summary-body
	// parsers never treat the retained messages as summary content.
	retainedRecentMessagesHeading = "## Retained Recent Messages"
	// retainedRecentTruncationMarker marks a block that was cut to fit the
	// retention budget.
	retainedRecentTruncationMarker = "[truncated]"

	retainedUserLabel                 = "User"
	retainedInterruptedAssistantLabel = "Assistant (interrupted reply, partial text)"
	retainedCheckpointRequestLabel    = "Assistant (context checkpoint request, own reasoning)"
)

// retainedCheckpointBlock is one message kept verbatim inside a checkpoint.
// Blocks are collected newest first and rendered in reverse (chronological).
type retainedCheckpointBlock struct {
	label     string
	text      string
	truncated bool
}

// selectCheckpointRetainedRecentBlocks walks the head being replaced by a
// checkpoint from the newest message backwards and returns the real messages
// worth keeping verbatim, newest first. Only user-authored messages count:
// synthetic user-role messages — prior compaction checkpoints, stream-continue
// prompts, loop notices, mailbox traffic, hook feedback, background results —
// never do, so retention cannot resurrect plumbing or a previous checkpoint.
// An interrupted assistant reply (StopReason "interrupted", no tool calls) is
// additionally retained only when it is the newest assistant segment of the
// head and no real user message is newer: an already-continued or superseded
// partial would only pull the model back to an abandoned exchange. The newest
// assistant segment that declared compact_context is likewise retained when it
// carries non-empty own text: under the archival profile the reasoning that
// led to the checkpoint request lives only in that body (plus what the model
// wrote into the continuation-state arguments), so dropping it would sever the
// model from its own just-completed analysis. At most maxUserMessages user
// messages are kept; the budget counts message text only.
func selectCheckpointRetainedRecentBlocks(messages []message.Message, maxUserMessages int, budgetTokens int, estimateTokens func(text string) int) []retainedCheckpointBlock {
	if budgetTokens <= 0 || estimateTokens == nil || maxUserMessages <= 0 {
		return nil
	}
	remaining := budgetTokens
	var blocks []retainedCheckpointBlock
	users := 0
	partialKept := false
	ccKept := false
	seenAssistant := false
	for _, msg := range slices.Backward(messages) {
		switch {
		case msg.Role == message.RoleUser && !message.IsUserAuthored(msg):
			// Synthetic user-role plumbing: never a real instruction.
			continue
		case msg.Role == message.RoleUser:
			if users >= maxUserMessages {
				return blocks
			}
			text := message.UserPromptInstructionText(msg)
			if text == "" {
				continue
			}
			users++
			blocks, remaining = addCheckpointRetainedBlock(blocks, remaining, estimateTokens, retainedCheckpointBlock{label: retainedUserLabel, text: text})
			if remaining <= 0 {
				return blocks
			}
		case msg.Role == message.RoleAssistant:
			// The newest compact_context declaring segment is kept verbatim
			// when it has own text. It sits below any interrupted partial or
			// newer tool-only rounds in the scan, so retaining it cannot
			// resurrect a superseded exchange; only one such block per head.
			if !ccKept && users == 0 && assistantDeclaresCompactContext(msg) {
				if text := retainedAssistantPartialText(msg); text != "" {
					ccKept = true
					blocks, remaining = addCheckpointRetainedBlock(blocks, remaining, estimateTokens, retainedCheckpointBlock{label: retainedCheckpointRequestLabel, text: text})
					if remaining <= 0 {
						return blocks
					}
				}
			}
			if !seenAssistant && !partialKept && users == 0 && len(msg.ToolCalls) == 0 && msg.StopReason == "interrupted" {
				if text := retainedAssistantPartialText(msg); text != "" {
					partialKept = true
					blocks, remaining = addCheckpointRetainedBlock(blocks, remaining, estimateTokens, retainedCheckpointBlock{label: retainedInterruptedAssistantLabel, text: text})
					if remaining <= 0 {
						return blocks
					}
				}
			}
			seenAssistant = true
		}
	}
	return blocks
}

// assistantDeclaresCompactContext reports whether the assistant message's
// declaring response is exactly one compact_context tool call (the only shape
// that passes the barrier validation).
func assistantDeclaresCompactContext(msg message.Message) bool {
	if len(msg.ToolCalls) != 1 {
		return false
	}
	return tools.NormalizeName(msg.ToolCalls[0].Name) == tools.NameCompactContext
}

// addCheckpointRetainedBlock appends block when its text fits the remaining
// budget; otherwise it keeps the largest fitting prefix and marks the block
// truncated. Blocks are added newest first, so the block that does not fit is
// the oldest kept one — truncating exactly there (and stopping) keeps every
// newer message whole.
func addCheckpointRetainedBlock(blocks []retainedCheckpointBlock, remaining int, estimateTokens func(text string) int, block retainedCheckpointBlock) ([]retainedCheckpointBlock, int) {
	cost := estimateTokens(block.text)
	if cost <= remaining {
		return append(blocks, block), remaining - cost
	}
	block.text = truncateRetainedTextToBudget(block.text, remaining, estimateTokens)
	if block.text == "" {
		return blocks, 0
	}
	block.truncated = true
	return append(blocks, block), 0
}

// truncateRetainedTextToBudget returns the longest prefix of text whose
// estimated cost fits budget, cut at a UTF-8 rune boundary. The estimator is
// monotone in prefix length, so a binary search finds the fit point.
func truncateRetainedTextToBudget(text string, budget int, estimateTokens func(text string) int) string {
	if text == "" || budget <= 0 {
		return ""
	}
	if estimateTokens(text) <= budget {
		return text
	}
	lo, hi := 0, len(text)
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if estimateTokens(text[:mid]) <= budget {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	for lo > 0 && !utf8.RuneStart(text[lo]) {
		lo--
	}
	return text[:lo]
}

// retainedAssistantPartialText returns the text of an interrupted assistant
// message for retention. Content is used verbatim; part-based messages fall
// back to the same text normalization the recent-tail anchor echo uses.
func retainedAssistantPartialText(msg message.Message) string {
	if text := strings.TrimSpace(msg.Content); text != "" {
		return text
	}
	if len(msg.Parts) == 0 {
		return ""
	}
	norm := normalizeMessagesForSummary([]message.Message{msg})
	if len(norm) == 0 {
		return ""
	}
	return strings.TrimSpace(norm[0].Content)
}

// renderCheckpointRetainedRecentMessages builds the `## Retained Recent
// Messages` section embedded in a checkpoint message, with the retained
// messages in chronological order (oldest first) as labelled blockquotes so
// they read as real conversation and cannot be mistaken for checkpoint
// structure. Returns "" when nothing was retained.
func renderCheckpointRetainedRecentMessages(messages []message.Message, maxUserMessages int, budgetTokens int, estimateTokens func(text string) int) string {
	blocks := selectCheckpointRetainedRecentBlocks(messages, maxUserMessages, budgetTokens, estimateTokens)
	if len(blocks) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(retainedRecentMessagesHeading)
	sb.WriteString("\nReal messages kept verbatim from just before the checkpoint so the conversation continues on the actual work boundary; everything older lives in the summarized sections above and the archived history files.\n")
	for _, block := range slices.Backward(blocks) {
		sb.WriteByte('\n')
		sb.WriteString(block.label)
		sb.WriteString(":\n")
		for line := range strings.SplitSeq(block.text, "\n") {
			sb.WriteString("> ")
			sb.WriteString(line)
			sb.WriteByte('\n')
		}
		if block.truncated {
			sb.WriteString("> ")
			sb.WriteString(retainedRecentTruncationMarker)
			sb.WriteByte('\n')
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}

func renderEvidenceArtifactContent(items []evidenceItem) string {
	if len(items) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(message.CompactionEvidenceTag)
	sb.WriteString("Verbatim excerpts preserved for the immediate continuation.\n")
	for i, item := range items {
		fmt.Fprintf(&sb, "\n%d. %s\n", i+1, item.Title)
		if item.Source != "" {
			fmt.Fprintf(&sb, "Source: %s\n", item.Source)
		}
		if item.WhyNeeded != "" {
			fmt.Fprintf(&sb, "Why it matters: %s\n", item.WhyNeeded)
		}
		if item.Excerpt != "" {
			sb.WriteString("Excerpt:\n")
			sb.WriteString(item.Excerpt)
			sb.WriteByte('\n')
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}

// buildCompactionCheckpointMessage renders the checkpoint message: the summary
// body between the [Context Summary] / [Context compressed] markers, then the
// wrapper (mode note, archived-history map, retained recent messages, evidence
// artifact, display hint). retainedRecent, when non-empty, is the rendered
// `## Retained Recent Messages` section; the variadic form keeps the many
// direct callers (tests, carry helpers) free of an always-empty argument.
func buildCompactionCheckpointMessage(summary string, historyRefs []string, mode string, evidenceItems []evidenceItem, retainedRecent ...string) string {
	var sb strings.Builder
	sb.WriteString(message.CompactionSummaryHeader)
	sb.WriteString(strings.TrimSpace(summary))
	sb.WriteString(message.CompactionCompressedTag)
	sb.WriteString("\n")
	switch mode {
	case message.CompactionSummaryModeTruncateOnly:
		sb.WriteString("Earlier conversation was compacted without a model-generated summary.\n")
	case message.CompactionSummaryModeStructuredFallback:
		sb.WriteString("Earlier conversation was compacted using a structured fallback summary after model summarization was weak or unavailable.\n")
	case compactionSummaryModeModelDriven:
		sb.WriteString("Earlier conversation was compacted into this model-driven context checkpoint. The checkpoint was built deterministically from runtime facts (current request, todos, subagents, background objects, anchors, and the newest real messages retained below) and the continuation state the model submitted through the compact_context tool; no summarization model was called. The archived history files below remain the authoritative record.\n")
	default:
		sb.WriteString("Earlier conversation was compacted into the summary above.\n")
	}
	sb.WriteString("Archived history files (read the matching file with the read tool to recover exact details; paths accept ~ shorthand):\n")
	for _, ref := range historyRefs {
		sb.WriteString("- ")
		sb.WriteString(ref)
		sb.WriteByte('\n')
	}
	sb.WriteString("Each archive begins with a short message-segment index (after its header): read the index first, then read only the line ranges you need — a single read is capped around 2000 lines, so slice by the index instead of reading whole files.\n")
	if retained := firstRetainedRecentSection(retainedRecent); retained != "" {
		sb.WriteString("\n")
		sb.WriteString(retained)
		sb.WriteByte('\n')
	}
	if evidence := renderEvidenceArtifactContent(evidenceItems); evidence != "" {
		sb.WriteString("\n")
		sb.WriteString(evidence)
		sb.WriteByte('\n')
	}
	sb.WriteString(message.CompactionDisplayHint)
	sb.WriteString("Press toggle-collapse to expand and inspect the full preserved context message.\n")
	return strings.TrimRight(sb.String(), "\n")
}

func firstRetainedRecentSection(retainedRecent []string) string {
	if len(retainedRecent) == 0 {
		return ""
	}
	return strings.TrimSpace(retainedRecent[0])
}

func formatKeyFileCandidatesForPrompt(paths []string) string {
	if len(paths) == 0 {
		return "- (none confidently extracted)"
	}
	var sb strings.Builder
	for _, path := range paths {
		sb.WriteString("- ")
		sb.WriteString(path)
		sb.WriteByte('\n')
	}
	return strings.TrimRight(sb.String(), "\n")
}

func ensureCompactionSummaryKeyFiles(summary string, keyFiles []string) string {
	summary = strings.TrimSpace(summary)
	if summary == "" || len(keyFiles) == 0 {
		return summary
	}
	const heading = "## Files and Evidence"
	start := strings.Index(summary, heading)
	if start < 0 {
		return summary
	}
	searchStart := start + len(heading)
	relEnd := strings.Index(summary[searchStart:], "\n## ")
	end := len(summary)
	if relEnd >= 0 {
		end = searchStart + relEnd
	}
	section := summary[start:end]
	existing := make(map[string]bool)
	for line := range strings.SplitSeq(section, "\n") {
		if path := normalizeSummaryBulletCandidate(line); path != "" {
			existing[path] = true
		}
	}
	var lines []string
	for _, keyFile := range keyFiles {
		if existing[keyFile] {
			continue
		}
		lines = append(lines, "- "+keyFile)
	}
	if len(lines) == 0 {
		return summary
	}
	var b strings.Builder
	b.WriteString(strings.TrimRight(summary[:end], "\n"))
	b.WriteByte('\n')
	for _, line := range lines {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	if end < len(summary) {
		b.WriteString(strings.TrimLeft(summary[end:], "\n"))
	}
	return strings.TrimRight(b.String(), "\n")
}

func normalizeSummaryBulletCandidate(line string) string {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "- ") {
		return ""
	}
	line = strings.TrimSpace(strings.TrimPrefix(line, "- "))
	line = strings.Trim(line, "`")
	line = strings.TrimRight(line, ".,;:!?)]}>\"'，。；：！？）】》」』’”")
	line = strings.TrimPrefix(line, "@")
	return strings.TrimSpace(line)
}

func compactionSummaryBody(content string) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return ""
	}
	start := strings.Index(content, message.CompactionSummaryHeader)
	if start < 0 {
		return content
	}
	start += len(message.CompactionSummaryHeader)
	end := strings.Index(content[start:], message.CompactionCompressedTag)
	if end < 0 {
		return strings.TrimSpace(content[start:])
	}
	return strings.TrimSpace(content[start : start+end])
}

func compactionFilesAndEvidenceSection(summaryContent string) string {
	body := compactionSummaryBody(summaryContent)
	if body == "" {
		return ""
	}
	const heading = "## Files and Evidence"
	start := strings.Index(body, heading)
	if start < 0 {
		return ""
	}
	searchStart := start + len(heading)
	relEnd := strings.Index(body[searchStart:], "\n## ")
	if relEnd < 0 {
		return strings.TrimSpace(body[searchStart:])
	}
	return strings.TrimSpace(body[searchStart : searchStart+relEnd])
}

func extractCompactionKeyFiles(summaryContent, projectRoot string) []string {
	section := compactionFilesAndEvidenceSection(summaryContent)
	if strings.TrimSpace(section) == "" {
		return nil
	}
	seen := make(map[string]bool)
	var out []string
	for rawLine := range strings.SplitSeq(section, "\n") {
		line := strings.TrimSpace(rawLine)
		if !strings.HasPrefix(line, "- ") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "- "))
		line = strings.Trim(line, "`")
		line = strings.TrimRight(line, ".,;:!?)]}>\"'，。；：！？）】》」』’”")
		line = strings.TrimPrefix(line, "@")
		path := normalizeCheckpointFilePath(line, projectRoot)
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		out = append(out, path)
	}
	return out
}

func normalizeCheckpointFilePath(path, projectRoot string) string {
	path = strings.TrimSpace(path)
	if path == "" || projectRoot == "" {
		return ""
	}
	if strings.Contains(path, ": ") || strings.HasPrefix(strings.ToLower(path), "archived history") {
		return ""
	}
	candidate := filepath.FromSlash(path)
	if filepath.IsAbs(candidate) {
		rel, err := filepath.Rel(projectRoot, candidate)
		if err != nil {
			return ""
		}
		candidate = rel
	}
	candidate = filepath.Clean(candidate)
	if candidate == "." || candidate == "" || strings.HasPrefix(candidate, ".."+string(filepath.Separator)) || candidate == ".." {
		return ""
	}
	rel := filepath.ToSlash(candidate)
	if strings.HasPrefix(rel, ".chord/") {
		return ""
	}
	info, err := os.Stat(filepath.Join(projectRoot, candidate))
	if err != nil || info.IsDir() {
		return ""
	}
	return rel
}

func extractCompactionKeyFileCandidates(messages []message.Message, projectRoot string, limit int) []string {
	if limit <= 0 || projectRoot == "" {
		return nil
	}
	seen := make(map[string]bool, limit)
	out := make([]string, 0, limit)
	add := func(candidate string) {
		if len(out) >= limit {
			return
		}
		normalized := normalizeCheckpointFilePath(candidate, projectRoot)
		if normalized == "" || seen[normalized] {
			return
		}
		seen[normalized] = true
		out = append(out, normalized)
	}

	for i := len(messages) - 1; i >= 0 && len(out) < limit; i-- {
		msg := messages[i]
		for _, tc := range msg.ToolCalls {
			for _, path := range extractToolArgFilePaths(tc.Args) {
				add(path)
			}
		}
		for _, path := range extractUserFileRefPaths(msg) {
			add(path)
		}
	}
	return out
}

func extractToolArgFilePaths(args json.RawMessage) []string {
	if len(args) == 0 || !json.Valid(args) {
		return nil
	}
	var payload any
	if err := json.Unmarshal(args, &payload); err != nil {
		return nil
	}
	seen := make(map[string]bool)
	var out []string
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for key, child := range x {
				switch key {
				case "path":
					if s, ok := child.(string); ok && strings.TrimSpace(s) != "" && !seen[s] {
						seen[s] = true
						out = append(out, s)
					}
				case "paths":
					if arr, ok := child.([]any); ok {
						for _, item := range arr {
							if s, ok := item.(string); ok && strings.TrimSpace(s) != "" && !seen[s] {
								seen[s] = true
								out = append(out, s)
							}
						}
					}
				default:
					walk(child)
				}
			}
		case []any:
			for _, item := range x {
				walk(item)
			}
		}
	}
	walk(payload)
	return out
}

func extractUserFileRefPaths(msg message.Message) []string {
	var refs []string
	seen := make(map[string]bool)
	addFromText := func(text string) {
		for _, path := range message.FileRefPaths(text) {
			if path == "" || seen[path] {
				continue
			}
			seen[path] = true
			refs = append(refs, path)
		}
	}
	for _, part := range msg.Parts {
		if part.Type != "text" {
			continue
		}
		addFromText(part.Text)
	}
	if len(msg.Parts) == 0 {
		addFromText(msg.Content)
	}
	return refs
}
