package tui

import (
	"strconv"
	"strings"
	"time"

	"github.com/mattn/go-runewidth"

	"github.com/keakon/chord/internal/textclean"
	"github.com/keakon/chord/internal/tools"
	"github.com/keakon/chord/internal/tui/terminaltext"
)

const backgroundResultCardTitle = "JOB RESULT"

// backgroundResultSuccessStatus is the status line a successfully completed job
// renders. The ✓ headline already states it, so folded cards drop the line and
// keep only any extra detail it carries.
const backgroundResultSuccessStatus = "Completed successfully"

type parsedBackgroundResult struct {
	id          string
	description string
	command     string
	status      string
	residual    []string
	output      []string
	elapsed     string
	duration    string
}

// formattedBackgroundResult is the card body a JOB RESULT card renders plus
// the one fact about it the renderer must not re-derive from the text: which
// lines are job headlines. Job output is free-form and may itself open lines
// with the ✓/✗/• glyphs a headline uses (build tools log their own progress
// marks), so headline identity is recorded here at format time and the
// renderer matches lines by index.
type formattedBackgroundResult struct {
	content       string
	headlineLines []int
	headlineID    string
}

func formatBackgroundResultCard(raw string) formattedBackgroundResult {
	sections := splitBackgroundResultSections(raw)
	if len(sections) > 1 {
		contents := make([]string, 0, len(sections))
		firstID := ""
		for _, section := range sections {
			content, sectionID := formatSingleBackgroundResult(section)
			if firstID == "" {
				firstID = sectionID
			}
			if content != "" {
				contents = append(contents, content)
			}
		}
		return joinBackgroundResultSections(contents, firstID)
	}
	content, id := formatSingleBackgroundResult(raw)
	return formattedBackgroundResult{content: content, headlineLines: []int{0}, headlineID: id}
}

// joinBackgroundResultSections joins per-job section bodies with one blank
// line. Each body opens with its headline, so the section start offsets are
// the headline line indices the renderer matches on.
func joinBackgroundResultSections(contents []string, firstID string) formattedBackgroundResult {
	var b strings.Builder
	headlines := make([]int, 0, len(contents))
	lines := 0
	for i, content := range contents {
		if i > 0 {
			// The "\n\n" separator ends the previous section's last line and
			// writes the one blank line between sections, so it advances the
			// line cursor by exactly one.
			b.WriteString("\n\n")
			lines++
		}
		headlines = append(headlines, lines)
		b.WriteString(content)
		lines += strings.Count(content, "\n") + 1
	}
	return formattedBackgroundResult{content: b.String(), headlineLines: headlines, headlineID: firstID}
}

func formatSingleBackgroundResult(raw string) (string, string) {
	parsed := parseBackgroundResult(raw)
	id := parsed.id
	status := parsed.status
	command := parsed.command
	description := parsed.description
	if strings.TrimSpace(description) == "" {
		description = command
	}
	elapsed := parsed.elapsed
	if elapsed == "" {
		elapsed = parsed.duration
	}

	glyph, statusLine := backgroundResultStatusLine(status)
	if elapsed != "" {
		statusLine += " · " + elapsedGlyph + " " + elapsed
	}
	id = strings.TrimSpace(id)
	description = strings.TrimSpace(description)
	headline := glyph
	if id != "" {
		headline += " " + id
	}
	if description != "" {
		if id != "" {
			headline += " · "
		} else {
			headline += " "
		}
		headline += description
	}
	if headline == glyph {
		headline += " Background job"
	}

	lines := []string{headline, statusLine}
	for _, line := range parsed.residual {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if commandDurationNote(trimmed) != "" {
			continue
		}
		lines = append(lines, line)
	}
	if len(parsed.output) > 0 {
		lines = append(lines, "Relevant output:")
		lines = append(lines, parsed.output...)
	}
	return strings.TrimSpace(strings.Join(lines, "\n")), id
}

func splitBackgroundResultSections(raw string) []string {
	raw = textclean.NormalizeNewlines(raw)
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	var sections []string
	var current []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if len(current) > 0 && isBackgroundResultHeader(trimmed) {
			sections = append(sections, strings.TrimSpace(strings.Join(current, "\n")))
			current = nil
		}
		current = append(current, line)
	}
	if len(current) > 0 {
		sections = append(sections, strings.TrimSpace(strings.Join(current, "\n")))
	}
	if len(sections) == 0 {
		return []string{strings.TrimSpace(raw)}
	}
	return sections
}

func parseBackgroundResult(raw string) parsedBackgroundResult {
	raw = textclean.NormalizeNewlines(raw)
	var parsed parsedBackgroundResult
	inOutput := false
	headerChecked := false
	for line := range strings.SplitSeq(strings.TrimSpace(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if duration := commandDurationNote(trimmed); duration != "" {
			parsed.duration = duration
			continue
		}
		if !headerChecked && trimmed != "" {
			headerChecked = true
			if isBackgroundResultHeader(trimmed) {
				parsed.id = backgroundResultIDFromHeader(trimmed)
				continue
			}
		}
		if inOutput {
			parsed.output = append(parsed.output, line)
			continue
		}
		if value, ok := cutBackgroundResultField(trimmed, "Purpose:"); ok {
			parsed.description = value
			continue
		}
		// Description and Kind are the removed spawn tool's field names. The
		// header matcher no longer recognizes a legacy headline, so such a
		// record loses its id, but its fields still parse: a session archived
		// before the job registry landed keeps folding to a useful summary
		// instead of a raw "Background job" line.
		if value, ok := cutBackgroundResultField(trimmed, "Description:"); ok {
			parsed.description = value
			continue
		}
		if value, ok := cutBackgroundResultField(trimmed, "Command:"); ok {
			parsed.command = value
			continue
		}
		if _, ok := cutBackgroundResultField(trimmed, "Kind:"); ok {
			continue
		}
		if value, ok := cutBackgroundResultField(trimmed, "Status:"); ok {
			parsed.status = value
			continue
		}
		if value, ok := cutBackgroundResultField(trimmed, "Elapsed:"); ok {
			parsed.elapsed = value
			continue
		}
		// Quiet is parsed and dropped, not rendered. It measures how long a
		// *running* job has been silent; this card only reports terminal
		// states, where it is ~0 whenever the job printed to the end and
		// says nothing the ✓/✗ glyph does not already say. job_list and the
		// JOBS overlay keep showing it for jobs that are still running.
		if _, ok := cutBackgroundResultField(trimmed, "Quiet:"); ok {
			continue
		}
		if strings.EqualFold(trimmed, "Relevant output:") {
			inOutput = true
			continue
		}
		if trimmed != "" {
			parsed.residual = append(parsed.residual, line)
		}
	}
	for len(parsed.output) > 0 && strings.TrimSpace(parsed.output[len(parsed.output)-1]) == "" {
		parsed.output = parsed.output[:len(parsed.output)-1]
	}
	return parsed
}

func commandDurationNote(line string) string {
	const prefix = "(command took "
	if !strings.HasPrefix(line, prefix) || !strings.HasSuffix(line, "s)") {
		return ""
	}
	secondsText := strings.TrimSuffix(strings.TrimPrefix(line, prefix), "s)")
	seconds, err := strconv.ParseFloat(secondsText, 64)
	// The shell tool only emits the note for elapsed >= 1s (see
	// appendShellDurationNote), so seconds < 1 is unreachable from the
	// current producer; the check stays as a guard aligned with that
	// predicate rather than as live filtering.
	if err != nil || seconds < 1 {
		return ""
	}
	return tools.FormatElapsed(time.Duration(seconds * float64(time.Second)))
}

func cutBackgroundResultField(line, field string) (string, bool) {
	if len(line) < len(field) || !strings.EqualFold(line[:len(field)], field) {
		return "", false
	}
	return strings.TrimSpace(line[len(field):]), true
}

// isBackgroundResultHeader matches the headline the job registry writes: a
// single "[Background job <id> finished]" line. Anything else is ordinary
// body text, so a card can only derive its background object id from the
// current format.
func isBackgroundResultHeader(line string) bool {
	lower := strings.ToLower(strings.TrimSpace(line))
	return strings.HasPrefix(lower, "[background job ") && strings.HasSuffix(lower, " finished]")
}

func backgroundResultIDFromHeader(line string) string {
	for field := range strings.FieldsSeq(strings.Trim(strings.TrimSpace(line), "[]")) {
		candidate := strings.Trim(field, " :,;")
		if strings.HasPrefix(candidate, "job-") {
			return candidate
		}
	}
	return ""
}

func backgroundResultStatusLine(status string) (string, string) {
	status = strings.TrimSpace(status)
	lower := strings.ToLower(status)
	if strings.Contains(lower, "cancel") {
		return "•", "Cancelled"
	}
	if code, ok := backgroundResultExitCode(lower); ok && code == 0 {
		return "✓", backgroundResultSuccessStatus
	}
	if strings.Contains(lower, "error") || strings.Contains(lower, "failed") || strings.Contains(lower, "timed out") || strings.Contains(lower, "exit status") || strings.Contains(lower, "exit code") {
		detail := backgroundResultErrorDetail(status)
		if detail == "" {
			detail = "Background command failed"
		}
		return "✗", "Error: " + detail
	}
	if strings.Contains(lower, "exit 0") || strings.Contains(lower, "success") || strings.Contains(lower, "completed") || lower == "finished" {
		return "✓", backgroundResultSuccessStatus
	}
	if status == "" {
		return "•", "Finished"
	}
	return "•", status
}

// backgroundResultExitCode extracts the exit code from a status line such as
// "completed (exit code 0)" or "failed (exit status 7)". Success text contains
// the same "exit code" marker as the failure branch, so the code must be
// parsed and checked before generic error-substring matching.
func backgroundResultExitCode(lower string) (int, bool) {
	for _, prefix := range []string{"exit code ", "exit status ", "exit "} {
		_, rest, found := strings.Cut(lower, prefix)
		if !found {
			continue
		}
		end := 0
		for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
			end++
		}
		if end == 0 {
			continue
		}
		if code, err := strconv.Atoi(rest[:end]); err == nil {
			return code, true
		}
	}
	return 0, false
}

func backgroundResultErrorDetail(status string) string {
	detail := strings.TrimSpace(status)
	lower := strings.ToLower(detail)
	const finishedError = "finished (error:"
	if strings.HasPrefix(lower, finishedError) {
		detail = strings.TrimSpace(detail[len(finishedError):])
		return strings.TrimSpace(strings.TrimSuffix(detail, ")"))
	}
	const errorPrefix = "error:"
	if strings.HasPrefix(lower, errorPrefix) {
		return strings.TrimSpace(detail[len(errorPrefix):])
	}
	// Terminal status text is "failed (...)" or "killed (...)"; the headline
	// glyph already says the job did not succeed, so show only the detail.
	for _, verb := range []string{"failed (", "killed ("} {
		if strings.HasPrefix(lower, verb) && strings.HasSuffix(detail, ")") {
			return strings.TrimSpace(detail[len(verb) : len(detail)-1])
		}
	}
	return detail
}

// isBackgroundResultCard reports whether this BlockStatus is a JOB RESULT card.
// These cards carry a durable background object id or the JOB RESULT badge, are
// collapsible, and route to renderBackgroundResult. Every other status card
// (loop notices, info cards, context-pressure notices, mailbox cards) keeps the
// generic rendering and is not collapsible.
func (b *Block) isBackgroundResultCard() bool {
	if b == nil || b.Type != BlockStatus {
		return false
	}
	return b.BackgroundObjectID != "" || b.StatusTitle == backgroundResultCardTitle
}

// backgroundResultContentLines splits the card body the way every reader of a
// JOB RESULT card's Content does. The headline indices stored on the block are
// offsets into exactly this line sequence, so the formatter and the renderer
// must share one splitting — a divergent split would point every index at the
// wrong line.
func backgroundResultContentLines(content string) []string {
	return strings.Split(strings.TrimSpace(terminaltext.Sanitize(content)), "\n")
}

// applyBackgroundResultContent stores a formatted result on the block: the card
// body, the headline indices that travel with it, and the bare copy text.
// Every writer of a JOB RESULT card's Content goes through here so the indices
// cannot drift from the text they point into.
func (b *Block) applyBackgroundResultContent(formatted formattedBackgroundResult, raw string) {
	b.Content = formatted.content
	b.BackgroundHeadlineLines = formatted.headlineLines
	b.BackgroundCopyContent = raw
}

func (b *Block) renderBackgroundResult(width int) []string {
	metrics := newToolCardMetrics(width)
	body := make([]string, 0, 8)
	// A JOB RESULT card drives every job from its headline: the glyph names the
	// outcome, the row carries the measured duration the way a tool card header
	// does, and only status text the glyph cannot spell out gets a row of its
	// own. Folded, the residual lines and the relevant-output block stay hidden.
	collapsed := b.Collapsed
	expectStatus := false
	skippingOutput := false
	headlineIdx := -1
	headlineTailIdx := -1
	contentLines := backgroundResultContentLines(b.Content)
	nextHeadline := 0
	for i := range len(contentLines) {
		line := contentLines[i]
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			if !skippingOutput && len(body) > 0 && body[len(body)-1] != "" {
				body = append(body, "")
			}
			continue
		}
		if nextHeadline < len(b.BackgroundHeadlineLines) && b.BackgroundHeadlineLines[nextHeadline] == i {
			nextHeadline++
			skippingOutput = false
			if len(body) > 0 && body[len(body)-1] != "" {
				body = append(body, "")
			}
			headlineIdx = len(body)
			// The status summary follows the headline, so its measured part is
			// known before the headline is laid out. Every headline line spends
			// backgroundResultHeadlineIndent columns before its text, and the
			// tail is appended within the same content width, so the wrap has to
			// leave both back. Folded, the row truncates around the tail;
			// expanded, the headline re-wraps so the tail lands at the end of
			// the last line instead of cutting text out of it.
			wrapWidth := metrics.contentWidth
			if elapsed := backgroundResultElapsedAfter(contentLines, i); elapsed != "" {
				room := backgroundResultElapsedSuffixWidth(elapsed) + backgroundResultHeadlineIndent
				if collapsed {
					if budget := max(wrapWidth-room, 10); runewidth.StringWidth(trimmed) > budget {
						trimmed = runewidth.Truncate(trimmed, budget, "…")
					}
				} else if lines := terminaltext.Wrap(trimmed, wrapWidth); runewidth.StringWidth(lines[len(lines)-1])+room > wrapWidth {
					wrapWidth = max(wrapWidth-room, 10)
				}
			}
			wrapped := terminaltext.Wrap(trimmed, wrapWidth)
			for i, part := range wrapped {
				prefix := "    "
				if i == 0 {
					prefix = "  "
					part = styleBackgroundResultHeadline(part, collapsed)
				}
				body = append(body, prefix+part)
			}
			headlineTailIdx = len(body) - 1
			expectStatus = true
			continue
		}
		if skippingOutput {
			continue
		}
		if strings.EqualFold(trimmed, "Relevant output:") {
			if collapsed {
				// Output is the tail of its section; skip it but keep scanning
				// so a multi-job card still lists every job's headline.
				skippingOutput = true
				continue
			}
			if len(body) > 0 && body[len(body)-1] != "" {
				body = append(body, "")
			}
			body = append(body, toolFieldSection(currentStyles.ToolResultExpandedStyle, "Relevant output"))
			// Output is the tail of its section: it ends where the next job's
			// headline begins, not at the end of the card — a ✓/✗ line inside
			// the output belongs to the output, and the next job still gets its
			// own headline row.
			outputEnd := len(contentLines)
			if nextHeadline < len(b.BackgroundHeadlineLines) {
				outputEnd = b.BackgroundHeadlineLines[nextHeadline]
			}
			output := strings.Join(contentLines[i+1:outputEnd], "\n")
			if backgroundResultHasCodeFence(output) {
				codeLines, _, _ := renderAssistantMarkdownContent(output, output, metrics.contentWidth, 0, &b.codeHL)
				for _, codeLine := range codeLines {
					body = append(body, "    "+codeLine)
				}
				skippingOutput = true
				continue
			}
			for outputLine := range strings.SplitSeq(output, "\n") {
				if strings.TrimSpace(outputLine) == "" {
					if len(body) > 0 && body[len(body)-1] != "" {
						body = append(body, "")
					}
					continue
				}
				for j, part := range terminaltext.Wrap(strings.TrimSpace(outputLine), metrics.contentWidth) {
					prefix := "    "
					if j == 0 {
						prefix = "    "
					}
					body = append(body, currentStyles.DimStyle.Render(prefix+part))
				}
			}
			skippingOutput = true
			continue
		}
		if expectStatus {
			// This summary row contributes two things and nothing else: the
			// duration rides the headline (a tool card keeps its elapsed on
			// the header whether the card is folded or open, so the time does
			// not move when the fold changes), and whatever status text the
			// ✓/✗ glyph cannot spell out keeps a row. A successful job's
			// summary duplicates the glyph, so once the duration is lifted the
			// row is empty and the card reads as headline plus output.
			expectStatus = false
			elapsed, detail := splitBackgroundResultStatusTail(backgroundResultStatusDetail(trimmed))
			if elapsed != "" {
				targetIdx := headlineIdx
				if !collapsed {
					targetIdx = headlineTailIdx
				}
				if targetIdx >= 0 {
					body[targetIdx] = appendToolElapsedSuffix(body[targetIdx], elapsed, metrics.cardWidth-4)
				}
			}
			if detail == "" {
				continue
			}
			trimmed = detail
		} else if collapsed {
			continue
		}
		expectStatus = false
		style := currentStyles.ToolResultExpandedStyle
		if strings.HasPrefix(strings.ToLower(trimmed), "error:") {
			style = currentStyles.ErrorStyle
		}
		for i, part := range terminaltext.Wrap(trimmed, metrics.contentWidth) {
			if i > 0 {
				body = append(body, toolFieldBody(style, part))
				continue
			}
			body = append(body, toolFieldMarker(style, part))
		}
	}
	return renderPrewrappedToolCard(metrics.blockStyle, metrics.cardWidth, toolCardTitle(backgroundResultCardTitle, b.displayLabelID()), body, metrics.toolCardBg, cardRailPrefix("tool", b.Focused))
}

// backgroundResultStatusDetail returns the status text a JOB RESULT card owes
// one job. A successful job's summary already sits in the ✓ headline, so it is
// dropped in either fold state — repeating it below the glyph says nothing the
// card did not already say. A duration it carried rides the headline instead,
// and failure, cancellation, or unknown statuses read as-is because the glyph
// alone does not name them.
func backgroundResultStatusDetail(line string) string {
	trimmed := strings.TrimSpace(line)
	if trimmed == backgroundResultSuccessStatus {
		return ""
	}
	if rest, ok := strings.CutPrefix(trimmed, backgroundResultSuccessStatus+" · "); ok {
		return rest
	}
	return line
}

// splitBackgroundResultStatusTail splits a status line into the duration it
// carries and the status text left once that duration is lifted away. The
// formatter joins the parts with " · ", so each segment is classified on its
// own and whatever survives is rejoined with the same separator. The quiet
// segment no longer exists on this card: it belongs to the surfaces that watch
// a running job, so a card that never carried it has nothing to strip.
func splitBackgroundResultStatusTail(line string) (elapsed, detail string) {
	var kept []string
	for segment := range strings.SplitSeq(line, " · ") {
		trimmed := strings.TrimSpace(segment)
		if trimmed == "" {
			continue
		}
		if value, ok := strings.CutPrefix(trimmed, elapsedGlyph+" "); ok {
			elapsed = strings.TrimSpace(value)
			continue
		}
		kept = append(kept, trimmed)
	}
	return elapsed, strings.Join(kept, " · ")
}

// backgroundResultElapsedAfter returns the duration a card lifts from the
// status line the formatter writes directly under the headline at index i, or
// "" when that line carries no measured part. The formatter always emits the
// summary line at headline+1, so the lookup is positional.
func backgroundResultElapsedAfter(lines []string, headline int) string {
	if headline+1 >= len(lines) {
		return ""
	}
	trimmed := strings.TrimSpace(lines[headline+1])
	elapsed, _ := splitBackgroundResultStatusTail(backgroundResultStatusDetail(trimmed))
	return elapsed
}

// backgroundResultElapsedSuffixWidth is the room the elapsed tail of a
// headline needs. It measures the same text the renderer appends, so the wrap
// reserves it instead of restating the format here and drifting from it, or
// discovering the overflow after the headline is already wrapped.
func backgroundResultElapsedSuffixWidth(elapsed string) int {
	return runewidth.StringWidth(toolElapsedSuffixText(elapsed))
}

func backgroundResultHasCodeFence(content string) bool {
	for line := range strings.SplitSeq(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			return true
		}
	}
	return false
}

// backgroundResultHeadlineIndent is the width every headline line spends before
// its text: a two-space indent plus the two-cell disclosure marker on the first
// line, or a four-space indent on continuation lines. The tail is appended to a
// line that already carries it, so a wrap budget or a reservation that forgets
// the indent loses exactly that much text to the truncation.
const backgroundResultHeadlineIndent = 4

// styleBackgroundResultHeadline styles the ✓/✗/• headline and appends the
// disclosure marker a collapsible card carries, so the folded state is visible
// at a glance and the space/enter toggle reads as the same affordance across
// card families.
func styleBackgroundResultHeadline(line string, collapsed bool) string {
	marker := toolDisclosureExpanded
	if collapsed {
		marker = toolDisclosureCollapsed
	}
	switch {
	case strings.HasPrefix(line, "✓"):
		return currentStyles.ToolStatusSuccessStyle.Render("✓") + " " + marker + line[len("✓"):]
	case strings.HasPrefix(line, "✗"):
		return currentStyles.ToolStatusErrorStyle.Render("✗") + " " + marker + line[len("✗"):]
	case strings.HasPrefix(line, "•"):
		return currentStyles.ToolStatusNeutralStyle.Render("•") + " " + marker + line[len("•"):]
	default:
		return line
	}
}
