package tui

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"

	"github.com/keakon/chord/internal/tools"
)

var (
	// diffHunkHeaderRe parses unified diff hunk header: @@ -oldStart,oldCount +newStart,newCount @@.
	// The ,count part is omitted by unified diff when the count is 1 (@@ -1 +1 @@),
	// so both counts are optional and only the start line numbers are captured.
	diffHunkHeaderRe = regexp.MustCompile(`^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@`)

	// diffAddBg / diffDelBg: subtle line backgrounds (opencode-style) so syntax highlighting stays readable.
	diffAddBg = currentTheme.DiffAddLineBg
	diffDelBg = currentTheme.DiffDelLineBg

	lspDiagLineRe = regexp.MustCompile(`^\s*\d+:\d+`)

	// lspSeverityRe matches "[E]", "[W]", "[I]", "[H]" prefixes in LSP diagnostic output.
	lspSeverityRe = regexp.MustCompile(`^\s*\[(E|W|I|H)\]`)

	lspDiagnosticsOmittedLineRe = regexp.MustCompile(`^\s*\.\.\. \d+ diagnostics not shown due to output limits; they may still need fixing\.$`)

	// readResultLineRe matches any READ_RESULT metadata line (including the
	// lines=none empty-range form) so it is never rendered as a source line.
	readResultLineRe = regexp.MustCompile(`^READ_RESULT\b.*\blines=(?:\d+-\d+|none)\b`)
	// readResultRangeRe extracts the 1-based start line when a concrete range
	// is present; lines=none carries no start line.
	readResultRangeRe         = regexp.MustCompile(`\blines=(\d+)-(\d+)\b`)
	readResultTotalRe         = regexp.MustCompile(`\btotal=(\d+)\b`)
	readResultTruncatedKindRe = regexp.MustCompile(`\btruncated=(\w+)\b`)
)

// maxToolCallCompactResultLines is the default visible height for generic tool output until space expands.
const maxToolCallCompactResultLines = 10

const toolResultIndent = "    "

var activeToolSpinnerSegments = [...]string{"▖", "▘", "▝", "▗"}

const queuedToolGlyph = "⏸"

// receivingToolGlyph is used while the provider is still streaming tool
// arguments. It is deliberately static: receiving arguments is not execution.
const receivingToolGlyph = "◌"

type toolCardMetrics struct {
	blockStyle   lipgloss.Style
	toolCardBg   string
	cardWidth    int
	contentWidth int
}

func newToolCardMetrics(width int) toolCardMetrics {
	// The card surface spans the full viewport (after the rail reservation);
	// contentCap only caps the wrapped content column so text does not stretch
	// across very wide terminals. apply_patch/edit/write content already clips
	// to the content width (see renderHighlightedSnippetLine /
	// renderNumberedToolPreview), so the cap never widens the surface.
	return newToolCardMetricsWithContentCap(width, maxProseWidth)
}

func newWideHeaderToolCardMetrics(width int) toolCardMetrics {
	return newToolCardMetricsForHeaderWidth(width, maxProseWidth)
}

func newToolCardMetricsWithContentCap(width, contentCap int) toolCardMetrics {
	return newToolCardMetricsForHeaderWidth(width, contentCap)
}

func newToolCardMetricsForHeaderWidth(width, contentCap int) toolCardMetrics {
	blockStyle := ToolBlockStyle
	// Reserve a column for the conversation rail (a foreground-only "│" prepended
	// outside the card width by wrapLineWithBackgroundAndRail). Without this, a
	// full-width card (e.g. wide-header read/shell cards, or any card on a narrow
	// terminal) renders one column past the terminal width and gets hard-wrapped.
	boxWidth := max((width-railWidthToReserve(blockStyle))-blockStyle.GetHorizontalMargins(), 10)
	cardWidth := max(boxWidth-blockStyle.GetHorizontalPadding()-blockStyle.GetHorizontalBorderSize(), 10)
	contentWidth := max(min(cardWidth-4, contentCap), 10)
	return toolCardMetrics{
		blockStyle:   blockStyle,
		toolCardBg:   currentTheme.ToolCallBg,
		cardWidth:    cardWidth,
		contentWidth: contentWidth,
	}
}

func newDoneToolCardMetrics(width int) toolCardMetrics {
	return newToolCardMetricsWithContentCap(width, maxProseWidth)
}

// pendingToolGlyph is used for speculative tool cards that have finished
// streaming their arguments but have not yet transitioned into an explicit
// execution-state (running/queued) event.
//
// Keep this a single-column glyph (runewidth=1) to avoid header layout drift.
const pendingToolGlyph = "⧗"

func toolUsesCompactDetailToggle(toolName string) bool {
	// Listed tools have no compact-detail layer: their dedicated renderers own
	// the whole card (Write/Read/Edit/ApplyPatch/TodoWrite/Handoff/Delete/
	// Question/Delegate), while Cancel renders its folded and expanded bodies
	// straight from b.Collapsed. Toggling ToolCallDetailExpanded would flip
	// only the header marker while the card body stayed put.
	switch toolName {
	case tools.NameWrite, tools.NameEdit, tools.NameApplyPatch, tools.NameDelete, tools.NameRead, tools.NameTodoWrite, tools.NameHandoff, tools.NameQuestion, tools.NameDelegate, tools.NameCancel:
		return false
	}
	return true
}

// toolCardAlwaysExpanded names the cards that must keep their whole body
// visible: report-style cards (done / complete / escalate / compact_context),
// the delegation card (delegate), the interactive card (question), the
// notification card (notify), and the cards whose body is content the model
// authored (write / edit / apply_patch / todo_write / handoff / delete). Cards
// that render tool output the reader consults on demand — read, grep, glob,
// shell, cancel and generic calls — fold instead, and start folded.
// A disclosure marker would only offer to hide content the header cannot
// summarize, so pressing space on them is a no-op.
//
// This is the single source of truth for the fold decision: it drives both the
// state a card is built with and whether ToggleAtWidth may change it. Keep them
// on one predicate — a card that starts expanded but can still be folded (or the
// reverse) has no coherent rendering.
func toolCardAlwaysExpanded(toolName string) bool {
	switch toolName {
	case tools.NameDone, tools.NameComplete, tools.NameEscalate,
		tools.NameCompactContext, tools.NameDelegate, tools.NameQuestion,
		tools.NameNotify, tools.NameWrite, tools.NameEdit,
		tools.NameApplyPatch, tools.NameTodoWrite, tools.NameHandoff,
		tools.NameDelete:
		return true
	}
	return false
}

// Disclosure markers for collapsible cards. renderToolDisclosurePrefix and
// renderToolPrefixForExpanded must stay in sync through these constants.
const (
	toolDisclosureCollapsed = "▸"
	toolDisclosureExpanded  = "▾"
)

func renderToolDisclosurePrefix(prefix string, expanded bool) string {
	marker := toolDisclosureCollapsed
	if expanded {
		marker = toolDisclosureExpanded
	}
	if prefix == toolDisclosureCollapsed || prefix == toolDisclosureExpanded {
		return marker
	}
	return prefix + " " + marker
}

func toolCollapsedSummaryText(s string) string {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return ""
	}
	trimmed = strings.ReplaceAll(trimmed, "\r\n", "\n")
	trimmed = strings.ReplaceAll(trimmed, "\r", "\n")
	lines := strings.Split(trimmed, "\n")
	parts := make([]string, 0, 2)
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts = append(parts, line)
		if len(parts) == 2 {
			break
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " · ")
}

// appendCollapsedSummaryLines writes the one-line body summary of a
// collapsed card. The header owns the disclosure marker, so the body carries
// only "↳": every caller reaches this helper on a card whose header already
// went through renderToolDisclosurePrefix, and repeating the ▸ read as a
// second, nested toggle.
func appendCollapsedSummaryLines(result *[]string, summary string, width int, style lipgloss.Style) {
	trimmed := strings.TrimSpace(sanitizeToolDisplayText(summary))
	if trimmed == "" {
		return
	}
	oneLine := truncateOneLine(toolCollapsedSummaryText(trimmed), width)
	if oneLine == "" {
		return
	}
	*result = append(*result, toolFieldMarker(style, oneLine))
}

// toolHeaderProseSummary folds a prose argument into the one line a header can
// carry: the first two non-empty lines joined with " · ", cut at the first
// sentence terminator. Cards whose subject is prose (a report, a reason, a
// message, an objective) put this in the header, so the body never has to
// repeat it in a summary row.
func toolHeaderProseSummary(prose string) string {
	return firstSentence(toolCollapsedSummaryText(strings.TrimSpace(prose)))
}

func toolExpandedTextLines(s string, width int) []string {
	trimmed := strings.TrimSpace(sanitizeToolDisplayText(s))
	if trimmed == "" {
		return nil
	}
	return wrapText(trimmed, width)
}

func toolCardTitle(label string, id int) string {
	return ToolLabelStyle.Render(blockLabelWithID(label, id))
}

func toolCollapsedVisibleLineCount(s string, width int) int {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return 0
	}
	return len(wrapText(trimmed, width))
}

func toolExpandedResultLines(displayResult string, width int, expanded bool) ([]string, int) {
	trimmed := strings.TrimSpace(displayResult)
	if trimmed == "" {
		return nil, 0
	}
	resLines := strings.Split(strings.TrimRight(displayResult, "\n"), "\n")
	if expanded {
		out := make([]string, 0, len(resLines))
		for _, line := range resLines {
			out = append(out, wrapText(line, width)...)
		}
		return out, 0
	}

	out := make([]string, 0, maxToolCallCompactResultLines)
	for i, line := range resLines {
		wrapped := wrapText(line, width)
		remaining := maxToolCallCompactResultLines - len(out)
		if len(wrapped) <= remaining {
			out = append(out, wrapped...)
			continue
		}
		out = append(out, wrapped[:remaining]...)
		// Avoid wrapping the entire hidden tail in collapsed mode. The current
		// logical line is already wrapped exactly; each later logical line adds
		// at least one hidden display line.
		return out, len(wrapped) - remaining + len(resLines) - i - 1
	}
	return out, 0
}

// Field rows are the one shape every tool card shares, so the ~15 renderers
// stop drifting apart — the same treatment appendToolOutcomeBody gives the
// failure surface. A row carries three visual levels:
//
//	↳        connector — quiet; says "this row is a child of the header"
//	Label:   label     — bold; says what the row is
//	value    value     — plain; the content itself
//
// Hand-rolled as literals these three levels drifted: some rows lost the
// connector, some lost the colon, and the label never gained emphasis over
// its own value, which is why a card read as one flat block of grey.
const (
	toolFieldConnector  = "↳ "
	toolFieldLead       = "  "
	toolFieldBodyIndent = toolResultIndent
)

// toolFieldLabel renders the emphasised "Label:" half of a field row. It keeps
// the caller's foreground — an error row stays red — and adds the weight that
// separates it from the value it introduces.
func toolFieldLabel(style lipgloss.Style, label string) string {
	return style.Bold(true).Render(label + ":")
}

// toolFieldSection renders a labelled row whose content follows on later
// lines: "  ↳ Message:".
func toolFieldSection(style lipgloss.Style, label string) string {
	return toolFieldLead + ToolFieldConnectorStyle.Render(toolFieldConnector) + toolFieldLabel(style, label)
}

// toolFieldInline renders a labelled row whose value shares the line:
// "  ↳ Kind: progress". An empty value degrades to a section row instead of
// printing a dangling colon. Callers sanitize value themselves.
func toolFieldInline(style lipgloss.Style, label, value string) string {
	if value == "" {
		return toolFieldSection(style, label)
	}
	return toolFieldLead + ToolFieldConnectorStyle.Render(toolFieldConnector) +
		toolFieldLabel(style, label) + " " + style.Render(value)
}

// toolFieldNestedInline renders a field row one level in, under a section:
// "    ↳ status: delivered". The indent alone cannot carry the distinction —
// a section's own body lines sit at the same indent — so the connector comes
// along and marks the row as a field rather than as content.
func toolFieldNestedInline(style lipgloss.Style, label, value string) string {
	if value == "" {
		return toolFieldBodyIndent + ToolFieldConnectorStyle.Render(toolFieldConnector) + toolFieldLabel(style, label)
	}
	return toolFieldBodyIndent + ToolFieldConnectorStyle.Render(toolFieldConnector) +
		toolFieldLabel(style, label) + " " + style.Render(value)
}

// toolFieldMarker renders a row that opens with a mark rather than a label —
// an exit line, a headline, a "✓". It exists so every connector in the card
// set comes from one place: a marker is not a field, but it still hangs off
// the header and must not invent a colon to say so.
func toolFieldMarker(style lipgloss.Style, text string) string {
	return toolFieldLead + ToolFieldConnectorStyle.Render(toolFieldConnector) + style.Render(text)
}

// toolFieldStandalone renders a row that is its own content and takes no
// value: "  ↳ Cancelled". Outcomes ("Cancelled", "No changes") and the status
// of a finished call stand alone, so a colon would promise a value that never
// arrives — see the rule recorded on appendToolOutcomeBody.
func toolFieldStandalone(style lipgloss.Style, text string) string {
	return toolFieldLead + ToolFieldConnectorStyle.Render(toolFieldConnector) + style.Bold(true).Render(text)
}

// toolFieldBody renders one wrapped line of a field row's content, indented
// under its label.
func toolFieldBody(style lipgloss.Style, line string) string {
	return toolFieldBodyIndent + style.Render(line)
}

// toolSummaryLine renders the one-word status a finished call reports
// ("Sent", "Delivered", "Queued", "Running", "Done", ...). It is a value, not
// a section, so it carries the "Status" label: every "↳" row then reads as
// "Label: value", and the bare "↳ Sent" that looked like a section missing its
// body is gone.
func toolSummaryLine(line string) string {
	if line == "" {
		return ""
	}
	return toolFieldInline(ToolResultExpandedStyle, "Status", line)
}

func toolCancelledDetailText(result string) string {
	trimmed := strings.TrimSpace(result)
	if trimmed == "" {
		return ""
	}
	switch strings.ToLower(trimmed) {
	case "cancelled", "canceled":
		return ""
	default:
		return trimmed
	}
}

func appendCancelledResultLines(result []string, content string, width int) []string {
	appendToolOutcomeBody(&result, toolOutcomeCancelled, content, width, true)
	return result
}

// toolOutcomeKind selects the envelope appendToolOutcomeBody renders.
type toolOutcomeKind int

const (
	toolOutcomeNone toolOutcomeKind = iota
	toolOutcomeError
	toolOutcomeCancelled
)

// toolOutcomeKindOf reports which outcome envelope a finished card owes the
// user. Callers that render their own status surface (shell's exit line, the
// done card's rejection reason) resolve the kind themselves.
func toolOutcomeKindOf(b *Block) toolOutcomeKind {
	switch {
	case b == nil || !b.ResultDone:
		return toolOutcomeNone
	case b.toolResultIsError():
		return toolOutcomeError
	case b.toolResultIsCancelled():
		return toolOutcomeCancelled
	}
	return toolOutcomeNone
}

// appendToolOutcomeBody renders the one failure/cancellation surface every
// tool card shares, so the 15 renderers stop drifting apart:
//
//   - collapsed: a single "↳ Error: <summary>" / "↳ Cancelled[: <detail>]" row
//   - expanded:  "↳ Error:" / "↳ Cancelled:" followed by the wrapped body
//   - the "Error: " prefix the tool-result envelope adds is stripped, so the
//     label is never printed twice
//   - a cancellation whose whole text is "Cancelled" renders the label alone
//     instead of repeating itself, and the colon appears only when a detail
//     follows
//
// content is the already-resolved display text (callers pass
// toolDisplayResultContent or a tool-specific variant).
func appendToolOutcomeBody(result *[]string, kind toolOutcomeKind, content string, contentWidth int, expanded bool) {
	if kind == toolOutcomeNone {
		return
	}
	style := ErrorStyle
	label := "Error"
	body := strings.TrimSpace(sanitizeToolDisplayText(toolErrorDisplayContent(content)))
	if kind == toolOutcomeCancelled {
		style, label = DimStyle, "Cancelled"
		body = strings.TrimSpace(sanitizeToolDisplayText(toolCancelledDetailText(content)))
	}
	if body == "" {
		if kind == toolOutcomeCancelled {
			*result = append(*result, toolFieldStandalone(style, label))
		}
		return
	}
	if !expanded {
		// A single logical line (a rejection reason, "exit code 1", a short
		// permission denial) folds to one compact summary row. A multi-line
		// outcome — most often a "file not found" error followed by a
		// "Did you mean:" suggestion list — must keep its whole body when
		// collapsed: toolCollapsedSummaryText joins the prompt header with the
		// cause into one row and truncates the rest, hiding exactly the
		// suggestion the user needs to act on. Expanding costs only the error's
		// own (short) length, so collapsed cards no longer force a toggle just
		// to read why a call failed.
		if toolOutcomeNonEmptyLineCount(body) < 2 {
			width := max(contentWidth-len(toolFieldConnector+label+": "), 12)
			if oneLine := truncateOneLine(toolCollapsedSummaryText(body), width); oneLine != "" {
				*result = append(*result, toolFieldInline(style, label, oneLine))
			}
			return
		}
	}
	*result = append(*result, toolFieldSection(style, label))
	lines := wrapText(body, contentWidth)
	if !expanded {
		appendBoundedOutcomeLines(result, style, lines, contentWidth)
		return
	}
	for _, line := range lines {
		*result = append(*result, style.Render("    "+line))
	}
}

// collapsedToolOutcomeMaxLines bounds the in-place body a collapsed card grants
// a multi-line failure. The bounded case that motivated showing the body at all
// — a "file not found" plus its "Did you mean:" suggestions — is a handful of
// candidates and fits inside it. Unbounded producers do exist (apply_patch
// aggregates one failure reason per file, and an MCP server may return an
// arbitrarily long multi-line error), and with no cap a card that is already
// collapsed can grow past a screenful with nothing left to fold.
//
// The budget is the collapsed card's own output budget: a failure body has no
// claim to more room than a successful result gets before the same space press.
const collapsedToolOutcomeMaxLines = maxToolCallCompactResultLines

// appendBoundedOutcomeLines emits at most collapsedToolOutcomeMaxLines body
// rows and, when it drops any, a final row naming the remainder and the key that
// reveals it. The hint is truncated to the content width like any other row.
func appendBoundedOutcomeLines(result *[]string, style lipgloss.Style, lines []string, contentWidth int) {
	hidden := 0
	if len(lines) > collapsedToolOutcomeMaxLines {
		hidden = len(lines) - collapsedToolOutcomeMaxLines
		lines = lines[:collapsedToolOutcomeMaxLines]
	}
	for _, line := range lines {
		*result = append(*result, style.Render("    "+line))
	}
	if hidden > 0 {
		hint := fmt.Sprintf("... %d more lines, press space to expand.", hidden)
		*result = append(*result, DimStyle.Render("    "+truncateOneLine(hint, max(contentWidth, 12))))
	}
}

// toolOutcomeNonEmptyLineCount counts the logical lines a tool result carries,
// ignoring blank separators, so "is this a single logical line?" is judged on
// real content. Splitting on both CR and LF covers results that arrive with
// either terminator, and dropping empty fields makes CRLF count once.
//
// Callers use it for two decisions: a collapsed card picks between a one-row
// summary and an in-place body, and the shell status line only quotes an error
// verbatim when it is the whole cause.
func toolOutcomeNonEmptyLineCount(s string) int {
	count := 0
	for line := range strings.FieldsFuncSeq(s, func(r rune) bool { return r == '\n' || r == '\r' }) {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count
}

// appendToolOutcome renders the shared envelope for a finished card using the
// block's own display result.
func appendToolOutcome(result *[]string, b *Block, contentWidth int, expanded bool) {
	kind := toolOutcomeKindOf(b)
	if kind == toolOutcomeNone {
		return
	}
	appendToolOutcomeBody(result, kind, toolDisplayResultContent(b), contentWidth, expanded)
}

// appendToolElapsedToHeader appends the tool elapsed label to the header line
// (result[0]) so the time stays in place whether the card is collapsed or
// expanded, instead of moving to the end of the body. cardWidth bounds the
// header so a near-full header is truncated rather than overflowing the card.
func appendToolElapsedToHeader(result []string, b *Block, cardWidth int) []string {
	if b == nil || !b.ResultDone {
		return result
	}
	// A Question card's clock starts when the prompt opens and stops when the
	// user answers, so the number is how long the user took to reply, not work
	// the agent did. Rendering it charges the tool for the user's own thinking
	// time, which reads as a cost the call incurred.
	if toolElapsedIsUserWaitTime(b.ToolName) {
		return result
	}
	elapsed := b.toolElapsedLabel()
	if elapsed == "" && tools.NormalizeName(b.ToolName) == tools.NameShell {
		elapsed = shellDurationNoteLabel(b.ResultContent)
	}
	if elapsed != "" && len(result) > 0 {
		result[0] = appendToolElapsedSuffix(result[0], elapsed, cardWidth-4)
	}
	return result
}

// toolElapsedIsUserWaitTime reports tools whose elapsed is measured while the
// user is the one holding the clock: the call does not run to completion on its
// own, it parks until an answer arrives. Their elapsed carries no information
// about the work performed, so the card must not show it.
func toolElapsedIsUserWaitTime(toolName string) bool {
	return tools.NormalizeName(toolName) == tools.NameQuestion
}

// appendToolElapsedSuffix appends " · ⏱ <elapsed>" to a header line, truncating
// the header with "…" when needed so the elapsed stays visible within maxWidth.
// Mirrors appendToolProgressSuffix so the header never overflows the card.
func appendToolElapsedSuffix(headerLine, elapsed string, maxWidth int) string {
	suffix := DimStyle.Render(" · ⏱ " + elapsed)
	if maxWidth <= 0 {
		return headerLine + suffix
	}
	if runewidth.StringWidth(stripANSI(headerLine+suffix)) <= maxWidth {
		return headerLine + suffix
	}
	suffixWidth := runewidth.StringWidth(stripANSI(suffix))
	if suffixWidth >= maxWidth {
		return headerLine
	}
	headerBudget := maxWidth - suffixWidth
	if headerBudget < 1 {
		return headerLine
	}
	truncatedHeader := ansi.Truncate(headerLine, headerBudget, "…")
	if runewidth.StringWidth(stripANSI(truncatedHeader+suffix)) <= maxWidth {
		return truncatedHeader + suffix
	}
	return truncateToolHeaderForSuffix(headerLine, suffix, maxWidth, headerBudget)
}

// truncateToolHeaderForSuffix shrinks a styled tool header line until
// line+suffix fits maxWidth, keeping the header's SGR sequences intact.
//
// This must stay ANSI-aware. The previous fallback stripped the sequences with
// stripANSI and returned a plain header, which silently dropped the tool-name
// colour: only the widest headers reach this branch, and mcp_* names are both
// the longest and carry the longest inline parameter summary, so they lost
// their colour while short built-in names kept it — a visible per-tool colour
// split with no styling code behind it. ansi.Truncate and runewidth disagree
// on the width of some glyphs, so instead of trusting one measurement we keep
// shrinking with the ANSI-aware truncate until the runewidth check passes.
func truncateToolHeaderForSuffix(headerLine, suffix string, maxWidth, headerBudget int) string {
	for budget := headerBudget; budget >= 1; budget-- {
		truncated := ansi.Truncate(headerLine, budget, "…")
		if runewidth.StringWidth(stripANSI(truncated+suffix)) <= maxWidth {
			return truncated + suffix
		}
	}
	return ansi.Truncate(headerLine, 1, "…") + suffix
}

func shellDurationNoteLabel(result string) string {
	matches := shellDurationNoteRE.FindStringSubmatch(result)
	if len(matches) == 2 {
		seconds, err := strconv.ParseFloat(matches[1], 64)
		if err != nil || seconds < 1 {
			return ""
		}
		return fmt.Sprintf("%ds", int(seconds))
	}
	return ""
}

func appendErrorResultLines(result []string, content string, width int) []string {
	appendToolOutcomeBody(&result, toolOutcomeError, content, width, true)
	return result
}

func bashCollapsedOutcomeSummary(b *Block) (string, bool) {
	if b == nil || !b.ResultDone {
		return "", false
	}
	if b.toolResultIsCancelled() {
		return "cancelled", false
	}
	if b.toolResultIsError() {
		// A single-line error result is the whole cause (for example a
		// rejection reason or a short permission denial), so show the line
		// verbatim instead of a lossy status the user would have to expand to
		// recover. Multi-line errors are too ambiguous to guess which line is
		// the real cause, so keep only a concise status (exit code / timeout)
		// and leave the detail to the expanded card.
		content := strings.TrimSpace(b.ResultContent)
		if toolOutcomeNonEmptyLineCount(content) == 1 {
			if line := bashFirstNonEmptyLine(sanitizeToolDisplayText(bashErrorText(content))); line != "" {
				return truncateOneLine(line, 120), true
			}
		}
		if timedOut := sanitizeToolDisplayText(bashTimeoutSummary(content)); timedOut != "" {
			return timedOut, true
		}
		if exit := bashExitCodeAnywhere(content); exit != "" {
			return exit, true
		}
		if line := bashFirstNonEmptyLine(sanitizeToolDisplayText(bashErrorText(content))); line != "" {
			return truncateOneLine(line, 120), true
		}
		return "failed", true
	}
	_, stdout := bashSplitResultStreams(b)
	if line := bashFirstNonEmptyLine(sanitizeToolDisplayText(stdout)); line != "" {
		return truncateOneLine(line, 120), false
	}
	if line := bashFirstNonEmptyLine(sanitizeToolDisplayText(strings.TrimSpace(b.ResultContent))); line != "" {
		return truncateOneLine(line, 120), false
	}
	return "completed", false
}

func bashExpandedExitLine(b *Block) string {
	if b == nil || !b.ResultDone {
		return ""
	}
	if b.toolResultIsCancelled() {
		return ""
	}
	if b.toolResultIsError() {
		if timedOut := bashTimeoutSummary(b.ResultContent); timedOut != "" {
			return "Exit: timeout"
		}
		// bashExitCodeAnywhere also matches the common
		// "<output>\n\nError: exit code N" shape, which the leading-prefix
		// form misses: the collapsed card reported the exit code while the
		// expanded card fell back to a bare "Exit: error".
		if exit := bashExitCodeFromError(b.ResultContent); exit != "" {
			return "Exit: " + strings.TrimSpace(exit)
		}
		if exit := bashExitCodeAnywhere(b.ResultContent); exit != "" {
			return "Exit: " + strings.TrimSpace(strings.TrimPrefix(exit, "exit code "))
		}
		return "Exit: error"
	}
	return "Exit: 0"
}

func bashSplitResultStreams(b *Block) (stderr, stdout string) {
	if b == nil {
		return "", ""
	}
	trimmed := strings.TrimSpace(shellDurationNoteRE.ReplaceAllString(b.ResultContent, ""))
	if trimmed == "" {
		return "", ""
	}
	if b.toolResultIsCancelled() {
		return "", ""
	}
	if b.toolResultIsError() {
		body := strings.TrimSpace(bashErrorBody(trimmed))
		if body == "" {
			body = trimmed
		}
		return body, ""
	}
	return "", trimmed
}

func bashErrorBody(content string) string {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return ""
	}
	const marker = "after output:"
	_, after, ok := strings.Cut(trimmed, marker)
	if !ok {
		return trimmed
	}
	body := strings.TrimSpace(after)
	return body
}

func bashTimeoutSummary(content string) string {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return ""
	}
	if strings.Contains(trimmed, "timed out after") {
		prefix := strings.TrimPrefix(trimmed, "command ")
		if before, _, ok := strings.Cut(prefix, " after output:"); ok {
			return before
		}
		if before, _, ok := strings.Cut(prefix, "\n"); ok {
			return before
		}
		return prefix
	}
	return ""
}

func bashExitCodeFromError(content string) string {
	trimmed := strings.TrimSpace(content)
	if !strings.HasPrefix(trimmed, "exit code ") {
		return ""
	}
	line := trimmed
	if i := strings.Index(line, "\n"); i >= 0 {
		line = line[:i]
	}
	return strings.TrimSpace(strings.TrimPrefix(line, "exit code "))
}

// bashErrorText returns the error explanation for a shell error result: the text
// after a trailing "Error:" marker when present, otherwise the whole content.
func bashErrorText(content string) string {
	trimmed := strings.TrimSpace(content)
	if _, after, ok := strings.Cut(trimmed, "Error:"); ok {
		if body := strings.TrimSpace(after); body != "" {
			return body
		}
	}
	return trimmed
}

// bashExitCodeAnywhere reports an "exit code N" status found on any line of an
// error result, trimming any colon/period/dot-prefixed explanation that follows
// (for example "exit code 1: non-interactive shell failure: ..."). Lines that
// carry the trailing "Error: " marker prefix are stripped first so the exit
// code inside them is still found.
func bashExitCodeAnywhere(content string) string {
	for line := range strings.SplitSeq(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "Error: "))
		if !strings.HasPrefix(line, "exit code ") {
			continue
		}
		body := strings.TrimSpace(strings.TrimPrefix(line, "exit code "))
		if i := strings.IndexAny(body, ": ."); i >= 0 {
			body = body[:i]
		}
		if _, err := strconv.Atoi(body); err == nil {
			return "exit code " + body
		}
	}
	return ""
}

func bashFirstNonEmptyLine(content string) string {
	for line := range strings.SplitSeq(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return ""
}

// toolSummarySuppressesErrors reports whether the tool's one-line result summary
// is suppressed for error results because the ↳ Error block already carries the
// detail.
func toolSummarySuppressesErrors(name string) bool {
	switch name {
	case tools.NameJobList, tools.NameJobKill, tools.NameDelegate, tools.NameGrep,
		tools.NameGlob, tools.NameLsp, tools.NameCancel, tools.NameNotify:
		return true
	}
	return false
}

// jobResultIDMaxWidth caps the ID shown in the collapsed summary so a
// malformed result cannot push the one-line summary past the card width. Real
// IDs are short opaque tokens (e.g. "job-64"). It bounds display columns, not
// rune count, so a wide-glyph ID is capped by what it actually occupies.
const jobResultIDMaxWidth = 24

// parseJobResultID extracts the background job ID from a shell result, e.g.
// "[background job job-64] ...". The job id is the handle for every later
// job_output / job_kill call, so the collapsed card can name it without being
// expanded. Returns "" when the result is not in that shape.
func parseJobResultID(result string) string {
	for line := range strings.SplitSeq(result, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		rest, ok := strings.CutPrefix(line, "[background job ")
		if !ok {
			return ""
		}
		id, _, ok := strings.Cut(rest, "]")
		if !ok {
			return ""
		}
		id = strings.TrimSpace(id)
		if id == "" || strings.ContainsFunc(id, unicode.IsSpace) {
			return ""
		}
		id = sanitizeToolDisplayText(id)
		if runewidth.StringWidth(id) > jobResultIDMaxWidth {
			id = runewidth.Truncate(id, jobResultIDMaxWidth, "…")
		}
		return id
	}
	return ""
}

// formatToolResultSummaryLine returns the one-line state summary under the
// tool header. Error and cancelled results render their detail in the shared
// ↳ Error / ↳ Cancelled envelope, so they return "" instead of a redundant
// label like "Search failed" or a second "Cancelled" row above the envelope.
func formatToolResultSummaryLine(b *Block) string {
	if b == nil {
		return ""
	}
	b.ToolName = toolNameKey(b.ToolName)
	if b.toolResultIsCancelled() {
		return ""
	}
	if !b.ResultDone {
		return ""
	}
	trimmed := strings.TrimSpace(b.ResultContent)
	// Unlisted tools fall through to the default branch, whose audit note stays
	// meaningful on error results too.
	if b.toolResultIsError() && toolSummarySuppressesErrors(b.ToolName) {
		return ""
	}
	switch b.ToolName {
	case tools.NameShell:
		// Shell expands with explicit exit-code detail, so avoid a redundant summary like "Passed".
		return ""
	case tools.NameJobOutput, tools.NameJobList:
		// The body already carries the incremental output and the status line.
		return ""
	case tools.NameJobKill:
		return "Stop requested"
	case tools.NameDelegate:
		if b.DoneSummary != "" {
			return "Done"
		}
		if id := parseTaskResultInstanceID(trimmed); id != "" {
			return fmt.Sprintf("Spawned · %s", id)
		}
		if trimmed != "" {
			return "Spawned"
		}
		return "Running"
	case tools.NameGrep:
		if trimmed == "No matches found." {
			return ""
		}
		count := 0
		for line := range strings.SplitSeq(strings.TrimRight(trimmed, "\n"), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "(showing first ") {
				continue
			}
			count++
		}
		if count <= 1 {
			return ""
		}
		return fmt.Sprintf("%d matches", count)
	case tools.NameGlob:
		if trimmed == "No files matched the pattern." {
			return ""
		}
		count := 0
		for line := range strings.SplitSeq(strings.TrimRight(trimmed, "\n"), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "(showing first ") {
				continue
			}
			count++
		}
		if count <= 1 {
			return ""
		}
		return fmt.Sprintf("%d files", count)
	case tools.NameLsp:
		return lspResultSummary(b.Content, trimmed)
	case tools.NameCancel:
		handle, _, ok := parseTaskToolHandle(trimmed)
		if ok && handle.Status != "" {
			switch handle.Status {
			case "stopped":
				return "Stopped"
			case "cancelled":
				return "Cancelled"
			default:
				return "Stopped"
			}
		}
		return "Stopped"
	case tools.NameNotify:
		handle, _, ok := parseTaskToolHandle(trimmed)
		if ok && handle.Status != "" {
			switch handle.Status {
			case "delivered":
				return "Delivered"
			case "queued":
				return "Queued"
			case "rehydrated":
				return "Rehydrated"
			default:
				return "Sent"
			}
		}
		if strings.TrimSpace(trimmed) != "" {
			return "Sent"
		}
		return "Sent"
	default:
		if b.Audit != nil && b.Audit.UserModified {
			return "edited before approval"
		}
		if !b.toolResultIsError() {
			if summary := toolSuccessfulFileOpSummary(b); summary != "" {
				return summary
			}
		}
		return ""
	}
}

func renderQueuedToolHeaderBadge(line string, width int) string {
	const badgeText = "Queued"
	const rightPadding = 1
	trimmed := strings.TrimRight(line, " ")
	badge := DimStyle.Render(badgeText)
	badgeWidth := runewidth.StringWidth(badgeText)
	lineWidth := ansi.StringWidth(trimmed)
	if lineWidth == 0 {
		return badge + strings.Repeat(" ", rightPadding)
	}
	if width <= 0 {
		width = lineWidth + 1 + badgeWidth + rightPadding
	}
	availableGap := width - lineWidth - badgeWidth - rightPadding
	if availableGap < 2 {
		return trimmed
	}
	return trimmed + strings.Repeat(" ", availableGap) + badge + strings.Repeat(" ", rightPadding)
}

func ensureCodeHighlighter(slot **codeHighlighter, filePath, sample string) *codeHighlighter {
	return ensureCodeHighlighterWithLanguage(slot, filePath, sample, "")
}

func ensureCodeHighlighterWithLanguage(slot **codeHighlighter, filePath, sample, language string) *codeHighlighter {
	if *slot == nil {
		*slot = newCodeHighlighterWithLanguage(filePath, sample, language)
		return *slot
	}
	(*slot).updateContext(filePath, sample)
	(*slot).updateLanguage(language)
	return *slot
}

func highlightCodeLines(h *codeHighlighter, lines []string, bgTerm string) []string {
	if len(lines) == 0 {
		return nil
	}
	source := strings.Join(lines, "\n")
	if source != "" {
		source += "\n"
	}
	highlighted := h.highlightSnippet(source, bgTerm)
	highlighted = strings.TrimSuffix(highlighted, "\n")
	highlightedLines := strings.Split(highlighted, "\n")
	if len(highlightedLines) == len(lines) {
		return highlightedLines
	}

	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, h.highlightLine(line, bgTerm))
	}
	return out
}

func sanitizeToolDisplayText(s string) string {
	return sanitizeDisplayText(s)
}

func toolErrorDisplayContent(content string) string {
	trimmed := strings.TrimSpace(content)
	if after, ok := strings.CutPrefix(trimmed, "Error: "); ok {
		return strings.TrimSpace(after)
	}
	if strings.HasPrefix(trimmed, "Error:\n") {
		return strings.TrimSpace(strings.TrimPrefix(trimmed, "Error:"))
	}
	return content
}

func parseReadDisplayLines(result string, startLine int) ([]readDisplayLine, string) {
	if result == "" {
		return nil, ""
	}
	if startLine < 1 {
		startLine = 1
	}
	rawLines := strings.Split(strings.TrimRight(result, "\n"), "\n")
	if len(rawLines) >= 3 {
		last := len(rawLines) - 1
		if rawLines[last-1] == "" && isReadArtifactFooter(rawLines[last]) {
			rawLines = rawLines[:last-1]
		}
	}
	rows := make([]readDisplayLine, 0, len(rawLines))
	codeLines := make([]string, 0, len(rawLines))
	sourceLineNo := startLine

	for i, line := range rawLines {
		line = strings.TrimSuffix(line, "\r")
		content := sanitizeToolDisplayText(line)
		if i == 0 {
			// The READ_RESULT metadata line is for the model only; never render
			// it in the card. Use its 1-based start line to align code numbering
			// (it is authoritative when the model's offset and the returned range
			// differ, e.g. after a budget truncation).
			if readResultLineRe.MatchString(content) {
				if m := readResultRangeRe.FindStringSubmatch(content); len(m) == 3 {
					if n, err := strconv.Atoi(m[1]); err == nil && n > 0 {
						sourceLineNo = n
					}
				}
				continue
			}
		}
		rows = append(rows, readDisplayLine{IsCode: true, LineNo: fmt.Sprintf("%d", sourceLineNo), Content: content})
		codeLines = append(codeLines, content)
		sourceLineNo++
	}

	return rows, strings.Join(codeLines, "\n")
}

func isReadArtifactFooter(line string) bool {
	refs := tools.ExtractArtifactReferences(line)
	return len(refs) == 1 && strings.TrimSpace(line) == refs[0]
}

type readResultMeta struct {
	StartLine     int
	EndLine       int
	Total         int
	Truncated     bool
	TruncatedKind string // "budget", "stale" or "superseded"; empty when only legacy text hints at truncation
	RangeField    string // raw lines= field, set when it carries multiple segments
}

type grepResultMeta struct {
	Matches     int
	Files       int
	Skipped     int
	Notes       int
	Fallback    bool
	Truncated   bool
	HasDetails  bool
	EmptyResult bool
	NoMatches   bool
}

type globResultMeta struct {
	Files      int
	Truncated  bool
	Artifact   string
	HasDetails bool
}

type diffResultMeta struct {
	Files   int
	Added   int
	Removed int
}

func parseReadResultMeta(result string) (readResultMeta, bool) {
	trimmed := strings.TrimSpace(result)
	if trimmed == "" {
		return readResultMeta{}, false
	}
	first := trimmed
	if i := strings.IndexByte(first, '\n'); i >= 0 {
		first = first[:i]
	}
	first = sanitizeToolDisplayText(strings.TrimSpace(first))
	if !readResultLineRe.MatchString(first) {
		return readResultMeta{}, false
	}
	meta := readResultMeta{}
	if m := readResultRangeRe.FindStringSubmatch(first); len(m) == 3 {
		meta.StartLine, _ = strconv.Atoi(m[1])
		meta.EndLine, _ = strconv.Atoi(m[2])
	}
	if field := readResultLinesField(first); field != "" && strings.Contains(field, ",") {
		meta.RangeField = field
	}
	if m := readResultTotalRe.FindStringSubmatch(first); len(m) == 2 {
		meta.Total, _ = strconv.Atoi(m[1])
	}
	if strings.Contains(first, "truncated=") || strings.Contains(strings.ToLower(first), "output truncated") {
		meta.Truncated = true
		if m := readResultTruncatedKindRe.FindStringSubmatch(first); len(m) == 2 {
			meta.TruncatedKind = m[1]
		}
	}
	return meta, true
}

// readResultLinesField extracts the raw lines= value ("a-b", "a-b,c-d" or
// "none") from a READ_RESULT header line.
func readResultLinesField(first string) string {
	for field := range strings.FieldsSeq(first) {
		if value, ok := strings.CutPrefix(field, "lines="); ok {
			return value
		}
	}
	return ""
}

func parseGrepResultMeta(result string) grepResultMeta {
	meta := grepResultMeta{}
	seenFiles := map[string]struct{}{}
	trimmed := strings.TrimSpace(result)
	if trimmed == "" {
		meta.EmptyResult = true
		return meta
	}
	for line := range strings.SplitSeq(strings.ReplaceAll(trimmed, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)
		switch {
		case strings.HasPrefix(line, "Note:"):
			meta.HasDetails = true
			meta.Notes++
			if strings.Contains(lower, "literal") && (strings.Contains(lower, "fallback") || strings.Contains(lower, "searched as literal")) {
				meta.Fallback = true
			}
		case strings.HasPrefix(line, "grep: skipped path:"):
			meta.HasDetails = true
			meta.Skipped++
		case strings.HasPrefix(line, "(showing first "):
			meta.HasDetails = true
			meta.Truncated = true
		case line == "No matches found." || strings.HasPrefix(line, "No matches found."):
			// The zero-match message may carry a parenthetical about literal
			// fallback on the same line, so match the prefix conservatively.
			meta.HasDetails = true
			meta.NoMatches = true
			if strings.Contains(lower, "invalid regex") && strings.Contains(lower, "literal") {
				meta.Fallback = true
			}
		default:
			meta.HasDetails = true
			meta.Matches++
			if idx := strings.Index(line, ":"); idx > 0 {
				path := line[:idx]
				if _, ok := seenFiles[path]; !ok {
					seenFiles[path] = struct{}{}
					meta.Files++
				}
			}
		}
	}
	return meta
}

func parseGlobResultMeta(result string) globResultMeta {
	meta := globResultMeta{}
	trimmed := strings.TrimSpace(result)
	if trimmed == "" {
		return meta
	}
	for line := range strings.SplitSeq(strings.ReplaceAll(trimmed, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		meta.HasDetails = true
		switch {
		case strings.HasPrefix(line, "Note:"):
			// Coercion notes are metadata, not result entries.
		case strings.HasPrefix(line, "(showing first "):
			meta.Truncated = true
			if idx := strings.LastIndex(line, "full results saved to "); idx >= 0 {
				rest := line[idx+len("full results saved to "):]
				if end := strings.Index(rest, ";"); end >= 0 {
					rest = rest[:end]
				}
				meta.Artifact = strings.TrimSpace(rest)
			}
		case strings.HasPrefix(line, "No files matched the pattern."):
			// The empty result message is not a path entry.
		default:
			meta.Files++
		}
	}
	return meta
}

func parseDiffResultMeta(diff string) diffResultMeta {
	meta := diffResultMeta{}
	if strings.TrimSpace(diff) == "" {
		return meta
	}
	seenFiles := map[string]struct{}{}
	for line := range strings.SplitSeq(strings.ReplaceAll(diff, "\r\n", "\n"), "\n") {
		switch {
		case strings.HasPrefix(line, "--- "):
			path := strings.TrimSpace(strings.TrimPrefix(line, "--- "))
			if path != "" && path != "/dev/null" {
				if _, ok := seenFiles[path]; !ok {
					seenFiles[path] = struct{}{}
					meta.Files++
				}
			}
		case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
			meta.Added++
		case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
			meta.Removed++
		}
	}
	return meta
}

func diffContentSample(diff string) string {
	const maxSampleLines = 64
	if diff == "" {
		return ""
	}
	rawLines := strings.Split(strings.TrimRight(diff, "\n"), "\n")
	sample := make([]string, 0, min(len(rawLines), maxSampleLines))
	for _, line := range rawLines {
		switch {
		case line == "":
			continue
		case strings.HasPrefix(line, "@@"), strings.HasPrefix(line, "---"), strings.HasPrefix(line, "+++"):
			continue
		case strings.HasPrefix(line, " "), strings.HasPrefix(line, "+"), strings.HasPrefix(line, "-"):
			sample = append(sample, line[1:])
		}
		if len(sample) >= maxSampleLines {
			break
		}
	}
	return strings.Join(sample, "\n")
}

func renderLSPDiagnosticsLines(content, indent string, width int) []string {
	var out []string
	for line := range strings.SplitSeq(strings.TrimRight(content, "\n"), "\n") {
		var st lipgloss.Style
		if m := lspSeverityRe.FindStringSubmatch(line); len(m) == 2 {
			switch m[1] {
			case "E":
				st = LSPErrorStyle
			case "W":
				st = LSPWarnStyle
			case "I":
				st = LSPInfoStyle
			default:
				st = LSPHintStyle
			}
		} else if lspDiagnosticsOmittedLineRe.MatchString(line) {
			st = DimStyle
		} else if strings.Contains(line, "LSP:") || strings.Contains(line, "LSP errors detected") || strings.Contains(line, "<diagnostics") || lspDiagLineRe.MatchString(strings.TrimSpace(line)) {
			st = LSPErrorStyle
		} else {
			st = ToolResultExpandedStyle
		}
		displayLine := sanitizeToolDisplayText(strings.TrimSuffix(line, "\r"))
		displayLine = expandTabsForDisplay(displayLine, preformattedTabWidth)
		for _, w := range wrapText(displayLine, width) {
			out = append(out, st.Render(indent+w))
		}
	}
	return out
}

type readDisplayLine struct {
	IsCode  bool
	LineNo  string
	Content string
}
