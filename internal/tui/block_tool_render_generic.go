package tui

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

const bashCommandPreviewMaxLines = 2

// bashCommandBlockLines renders the command lines to show in the body (full
// when expanded, preview when collapsed).
func bashCommandBlockLines(command string, expanded bool) []string {
	if expanded {
		return bashCommandLines(command)
	}
	return bashCommandPreviewLines(command, bashCommandPreviewMaxLines)
}

// renderCommandBlock renders a `Title:` labelled, indented block of lines in
// the shared dim style used by Shell transcript cards. It is the single
// renderer for the command body across collapsed preview and expanded full
// views, as well as the local `!shell` card.
func renderCommandBlock(title string, lines []string, contentWidth int) []string {
	if len(lines) == 0 {
		return nil
	}
	out := make([]string, 0, len(lines)+1)
	// Section headers carry the "↳" marker everywhere else in the card set.
	out = append(out, toolFieldSection(DimStyle, title))
	for _, line := range lines {
		line = sanitizeToolDisplayText(line)
		for _, w := range wrapIndentedText(line, contentWidth) {
			out = append(out, toolFieldBody(DimStyle, w))
		}
	}
	return out
}

func appendBashCommandBlock(result *[]string, command string, contentWidth int, expanded bool) {
	lines := bashCommandBlockLines(command, expanded)
	*result = append(*result, renderCommandBlock("Command", lines, contentWidth)...)
}

// bashMetaLines returns the wrapped, styled meta lines displayed under the
// expanded Shell command block. Only the working directory is left: it
// qualifies the command it sits under and can be a long path, while the
// description is the header's subject and a non-default timeout is already in
// the header's option group - repeating either put the same value on screen
// twice.
func bashMetaLines(vals map[string]string, contentWidth int) []string {
	var out []string
	if workdir := strings.TrimSpace(vals["workdir"]); workdir != "" {
		line := fmt.Sprintf("workdir: %s", workdir)
		for _, w := range wrapIndentedText(line, contentWidth) {
			out = append(out, DimStyle.Render("    "+w))
		}
	}
	return out
}

func appendBashCollapsedSummary(result *[]string, b *Block, vals map[string]string, contentWidth int, includeDescription bool) {
	if b == nil || !b.ResultDone {
		return
	}
	if !b.toolResultIsError() && !b.toolResultIsCancelled() {
		// A successful background start names its job id on the header line
		// (see renderCompactExpandableToolCall): it is the handle every later
		// job_output / job_kill call needs, and the collapsed card hides the
		// result body that carries it. There is no other body to add here.
		return
	}
	// The description belongs to the call, not to the outcome, so it stays on
	// its own row; the outcome then goes through the shared envelope every
	// other card uses instead of a bare "exit code 1" line with no label.
	if includeDescription {
		if desc := strings.TrimSpace(vals["description"]); desc != "" {
			for _, wrapped := range wrapIndentedText(sanitizeToolDisplayText(desc), contentWidth) {
				*result = append(*result, DimStyle.Render("    "+wrapped))
			}
		}
	}
	summary := bashCollapsedOutcomeSummary(b)
	appendToolOutcomeBody(result, toolOutcomeKindOf(b), summary, contentWidth, false)
}

// splitStyleRender splits style.Render around a single-line probe so repeated
// per-line styling can reuse the SGR prefix/suffix instead of invoking lipgloss
// once per line (which re-scans the line, re-checks border/align props and
// rebuilds the style sequence each time). It is only valid for foreground-only
// styles (no width/padding/margin/border/background or whitespace styling) and
// tab-free input; appendStyledWrappedBody falls back to Style.Render for lines
// containing tabs so lipgloss's tab-width normalization remains intact.
func splitStyleRender(style lipgloss.Style) (prefix, suffix string) {
	const probe = "X"
	rendered := style.Render(probe)
	if prefix, suffix, ok := strings.Cut(rendered, probe); ok {
		return prefix, suffix
	}
	return "", ""
}

// appendStyledWrappedBody wraps body to the given width and appends each line
// styled with the given foreground style. The SGR prefix/suffix is computed
// once and reused for every line: for wrapped plain-text lines
// prefix+indent+line+suffix is byte-identical to style.Render(indent+line).
func appendStyledWrappedBody(result *[]string, style lipgloss.Style, indent string, body string, width int) {
	lines := toolExpandedTextLines(body, width)
	if len(lines) == 0 {
		return
	}
	prefix, suffix := splitStyleRender(style)
	for _, line := range lines {
		if strings.ContainsRune(indent, '\t') || strings.ContainsRune(line, '\t') {
			*result = append(*result, style.Render(indent+line))
			continue
		}
		*result = append(*result, prefix+indent+line+suffix)
	}
}

func appendBashExpandedResult(result *[]string, b *Block, contentWidth int) {
	if b == nil {
		return
	}
	if b.toolResultIsCancelled() {
		// bashSplitResultStreams drops the body of a cancelled run, so the
		// shared envelope is the only thing that reports the cancellation.
		appendToolOutcomeBody(result, toolOutcomeCancelled, toolDisplayResultContent(b), contentWidth, true)
		return
	}
	failed, output := bashSplitResultStreams(b)
	exitLabel := bashExpandedExitLine(b)
	if exitLabel != "" {
		for _, wrapped := range wrapIndentedText(exitLabel, contentWidth) {
			*result = append(*result, toolFieldMarker(DimStyle, wrapped))
		}
	}
	// The runtime hands the card one merged stream, so the body is labelled
	// "Output" rather than claiming a stdout/stderr split it cannot make: a
	// failing command that writes its diagnostics to stdout (go test, most
	// build tools) was previously mislabelled as stderr.
	if failed != "" {
		*result = append(*result, toolFieldSection(ErrorStyle, "Output"))
		appendStyledWrappedBody(result, ErrorStyle, "    ", failed, contentWidth)
	}
	if output != "" {
		*result = append(*result, toolFieldSection(ToolResultExpandedStyle, "Output"))
		appendStyledWrappedBody(result, ToolResultExpandedStyle, "    ", output, contentWidth)
	}
}

func (b *Block) renderToolCall(width int, spinnerFrame string) []string {
	b.ToolName = toolNameKey(b.ToolName)
	metrics := newToolCardMetrics(width)
	blockStyle := metrics.blockStyle
	toolCardBg := metrics.toolCardBg
	cardWidth := metrics.cardWidth
	contentWidth := metrics.contentWidth

	if b.ToolName == tools.NameTodoWrite {
		return b.renderTodoCall(width, spinnerFrame)
	}
	if b.ToolName == tools.NameWrite {
		return b.renderWriteCall(width, spinnerFrame)
	}
	if b.ToolName == tools.NameEdit || b.ToolName == tools.NameApplyPatch {
		return b.renderFileDiffCall(width, spinnerFrame)
	}
	if b.ToolName == tools.NameRead {
		return b.renderReadCall(width, spinnerFrame)
	}
	if b.ToolName == tools.NameDelete {
		return b.renderDeleteCall(width, spinnerFrame)
	}
	if b.ToolName == tools.NameGrep || b.ToolName == tools.NameGlob {
		return b.renderSearchResultToolCall(width, spinnerFrame)
	}
	if b.ToolName == tools.NameHandoff {
		return b.renderHandoffCall(width, spinnerFrame)
	}
	if b.ToolName == tools.NameQuestion {
		return b.renderQuestionCall(width, spinnerFrame)
	}
	if b.ToolName == tools.NameComplete || b.ToolName == tools.NameEscalate {
		return b.renderProseControlCall(width, spinnerFrame)
	}
	if b.ToolName == tools.NameDelegate {
		return b.renderTaskCall(width, spinnerFrame)
	}
	if b.ToolName == tools.NameDone {
		return b.renderDoneCall(width, spinnerFrame)
	}
	if b.ToolName == tools.NameCancel {
		return b.renderCancelCall(width, spinnerFrame)
	}
	if b.ToolName == tools.NameNotify {
		return b.renderNotifyCall(width, spinnerFrame)
	}
	if b.ToolName == tools.NameCompactContext {
		return b.renderCompactContextCall(width, spinnerFrame)
	}
	if toolUsesCompactDetailToggle(b.ToolName) {
		return b.renderCompactExpandableToolCall(width, spinnerFrame)
	}

	keys, vals := b.toolArgsParsed()
	paramSummary, mainPart, grayPart, _, _, _, paramLines := b.toolHeaderMeta()
	if mainPart != "" || grayPart != "" {
		metrics = newWideHeaderToolCardMetrics(width)
		blockStyle = metrics.blockStyle
		toolCardBg = metrics.toolCardBg
		cardWidth = metrics.cardWidth
		contentWidth = metrics.contentWidth
	}
	if paramSummary == "" {
		paramSummary = extractToolParamsWithParsed(keys, vals, cardWidth-16)
	} else if ansi.StringWidth(paramSummary) > cardWidth-16 {
		paramSummary = ansi.Truncate(paramSummary, cardWidth-16, "…")
	}
	var result []string
	if b.Collapsed {
		prefix := b.renderToolPrefix(spinnerFrame)
		headerLine := renderToolHeaderLine(prefix, b.ToolName)
		headerLine = appendToolHeaderSummary(headerLine, mainPart, grayPart, paramSummary, cardWidth-4)
		headerLine = appendToolProgressSuffix(headerLine, b.ToolProgress, cardWidth-4)
		result = append(result, headerLine)

		if b.DoneSummary != "" {
			summary := truncateOneLine(sanitizeToolDisplayText(b.DoneSummary), cardWidth-26)
			result = append(result, toolFieldMarker(ToolResultStyle, "✓ "+summary))
		} else if kind := toolOutcomeKindOf(b); kind != toolOutcomeNone {
			appendToolOutcomeBody(&result, kind, toolDisplayResultContent(b), contentWidth, false)
		} else if b.ResultContent != "" {
			displayResult := sanitizeToolDisplayText(toolCollapsedResultContent(b.ToolName, toolDisplayResultContent(b)))
			summary := truncateOneLine(displayResult, cardWidth-26)
			result = append(result, toolFieldMarker(ToolResultStyle, summary))
		}
	} else {
		prefix := b.renderToolPrefix(spinnerFrame)
		showParamSummary := mainPart != "" || grayPart != "" || paramSummary != ""
		headerLine := renderToolHeaderLine(prefix, b.ToolName)
		if showParamSummary {
			headerLine = appendToolHeaderSummary(headerLine, mainPart, grayPart, paramSummary, cardWidth-4)
		}
		headerLine = appendToolProgressSuffix(headerLine, b.ToolProgress, cardWidth-4)
		if b.toolExecutionIsQueued() && b.ToolQueuedByExecutionEvent {
			headerLine = renderQueuedToolHeaderBadge(headerLine, cardWidth)
		}
		result = append(result, headerLine)
		if paramSummary == "" || b.ToolName == tools.NameShell {
			for _, line := range paramLines {
				for _, wrapped := range wrapText(sanitizeToolDisplayText(line), contentWidth) {
					result = append(result, DimStyle.Render("    "+wrapped))
				}
			}
		}
		if summary := formatToolResultSummaryLine(b); summary != "" && !b.toolExecutionIsQueued() {
			result = append(result, toolSummaryLine(summary))
		}
		if kind := toolOutcomeKindOf(b); kind != toolOutcomeNone {
			appendToolOutcomeBody(&result, kind, toolDisplayResultContent(b), contentWidth, true)
		} else if b.ResultContent != "" {
			lineStyle := DimStyle
			if b.ToolName == tools.NameDelete {
				lineStyle = ToolResultExpandedStyle
			}
			for _, line := range wrapText(sanitizeToolDisplayText(toolDisplayResultContent(b)), contentWidth) {
				result = append(result, lineStyle.Render("    "+line))
			}
		}
		if b.DoneSummary != "" {
			result = append(result, toolFieldSection(ToolResultExpandedStyle, "Completed"))
			for _, line := range wrapText(sanitizeToolDisplayText(b.DoneSummary), contentWidth) {
				result = append(result, DimStyle.Render("    "+line))
			}
		}
	}

	result = appendToolElapsedToHeader(result, b, cardWidth)
	return b.renderToolCardWithIgnoredArgs(blockStyle, cardWidth, toolCardTitle("TOOL CALL", b.displayLabelID()), result, toolCardBg, railANSISeq("tool", b.Focused))
}

func (b *Block) renderDoneCall(width int, spinnerFrame string) []string {
	metrics := newDoneToolCardMetrics(width)
	blockStyle := metrics.blockStyle
	toolCardBg := metrics.toolCardBg
	cardWidth := metrics.cardWidth
	contentWidth := metrics.contentWidth

	report := strings.TrimSpace(b.DoneReport)
	prefix := b.renderToolPrefix(spinnerFrame)
	// The full report renders right under the header, so the header stays a
	// bare index line: repeating the report's opening line (and the elapsed
	// suffix) only duplicated content the card already shows.
	headerLine := renderToolHeaderLine(prefix, b.ToolName)
	headerLine = buildToolHeaderLine(headerLine, b.ToolProgress, cardWidth, b.toolExecutionIsQueued() && b.ToolQueuedByExecutionEvent, b.toolExecutionIsRunning())
	result := []string{headerLine}

	if report != "" {
		result = append(result, "")
		for _, line := range renderRichMarkdownContent(report, contentWidth, &b.richMarkdownHL) {
			result = append(result, "    "+line)
		}
	}
	if b.ResultDone && strings.TrimSpace(b.ResultContent) != "" {
		// The runtime's own notes are not a status: when the result held
		// nothing else, the card shows no status section at all.
		statusText := strings.TrimSpace(b.stripResultNotes(b.ResultContent))
		if b.toolResultIsError() {
			statusText = toolErrorDisplayContent(statusText)
		}
		// A blank line only separates the status from a report above it; a
		// card whose whole body is the status must not open with one.
		separate := func() {
			if len(result) > 1 {
				result = append(result, "")
			}
		}
		switch {
		case statusText == "":
			// Nothing but runtime notes left after the strip above.
		case doneResultIsRejected(statusText):
			// A rejection is not a schema error: keep its own label, but on
			// the shared "↳ Label:" shape.
			separate()
			result = append(result, toolFieldSection(ErrorStyle, "Rejected"))
			for _, line := range wrapText(sanitizeToolDisplayText(doneRejectedReason(statusText)), contentWidth) {
				result = append(result, ErrorStyle.Render("    "+line))
			}
		case report != "":
			// The report above already carries the outcome.
		case toolOutcomeKindOf(b) != toolOutcomeNone:
			separate()
			appendToolOutcome(&result, b, contentWidth, true)
		default:
			separate()
			result = append(result, toolFieldSection(ToolResultExpandedStyle, "Status"))
			for _, line := range wrapText(sanitizeToolDisplayText(statusText), contentWidth) {
				result = append(result, DimStyle.Render("    "+line))
			}
		}
	}
	return b.renderToolCardWithIgnoredArgs(blockStyle, cardWidth, toolCardTitle("TOOL CALL", b.displayLabelID()), result, toolCardBg, railANSISeq("tool", b.Focused))
}

type proseControlArgs struct {
	Summary              string              `json:"summary"`
	Reason               string              `json:"reason"`
	FilesChanged         []string            `json:"files_changed"`
	RemainingLimitations []string            `json:"remaining_limitations"`
	KnownRisks           []string            `json:"known_risks"`
	FollowUpRecommended  []string            `json:"follow_up_recommended"`
	Artifacts            []tools.ArtifactRef `json:"artifacts"`
}

// decodeProseControlArgsTolerant recovers prose-control fields individually
// when one wrongly typed field makes the whole args object fail to decode, so
// the Done/Escalate card keeps showing what the caller actually passed.
func decodeProseControlArgsTolerant(argsJSON string) proseControlArgs {
	_, vals := parseToolArgs(argsJSON)
	return proseControlArgs{
		Summary:              vals["summary"],
		Reason:               vals["reason"],
		FilesChanged:         paramStringList(vals["files_changed"]),
		RemainingLimitations: paramStringList(vals["remaining_limitations"]),
		KnownRisks:           paramStringList(vals["known_risks"]),
		FollowUpRecommended:  paramStringList(vals["follow_up_recommended"]),
		Artifacts:            artifactsFromDisplayValue(vals["artifacts"]),
	}
}

func artifactsFromDisplayValue(raw string) []tools.ArtifactRef {
	if raw == "" {
		return nil
	}
	var artifacts []tools.ArtifactRef
	if json.Unmarshal([]byte(raw), &artifacts) != nil {
		return nil
	}
	return artifacts
}

func (b *Block) renderProseControlCall(width int, spinnerFrame string) []string {
	metrics := newDoneToolCardMetrics(width)
	blockStyle := metrics.blockStyle
	toolCardBg := metrics.toolCardBg
	cardWidth := metrics.cardWidth
	contentWidth := metrics.contentWidth

	// Completion cards are always expanded behind a bare tool-name header:
	// the summary/reason they would summarize is the body's own prose
	// section, so there is nothing for a disclosure marker to reveal.
	prefix := b.renderToolPrefix(spinnerFrame)
	var args proseControlArgs
	argsJSON := b.RawArgs
	if strings.TrimSpace(argsJSON) == "" {
		argsJSON = b.Content
	}
	if json.Unmarshal([]byte(argsJSON), &args) != nil {
		args = decodeProseControlArgsTolerant(argsJSON)
	}
	prose := strings.TrimSpace(args.Summary)
	if b.ToolName == tools.NameEscalate {
		prose = strings.TrimSpace(args.Reason)
	}
	argsReady := b.ResultDone || strings.TrimSpace(argsJSON) != ""

	headerLine := renderToolHeaderLine(prefix, b.ToolName)
	headerLine = buildToolHeaderLine(headerLine, b.ToolProgress, cardWidth, b.toolExecutionIsQueued() && b.ToolQueuedByExecutionEvent, b.toolExecutionIsRunning())
	result := []string{headerLine}

	if argsReady && prose != "" {
		result = append(result, "")
		for _, line := range renderRichMarkdownContent(prose, contentWidth, &b.richMarkdownHL) {
			result = append(result, "    "+line)
		}
	}

	if b.ToolName == tools.NameComplete {
		appendList := func(label string, values []string) {
			if len(values) == 0 {
				return
			}
			result = append(result, "", toolFieldSection(ToolResultExpandedStyle, label))
			for _, value := range values {
				for i, line := range wrapText(sanitizeToolDisplayText(value), contentWidth-2) {
					bullet := "  "
					if i == 0 {
						bullet = "• "
					}
					result = append(result, DimStyle.Render("    "+bullet+line))
				}
			}
		}
		appendList("Files changed", args.FilesChanged)
		appendList("Remaining limitations", args.RemainingLimitations)
		appendList("Known risks", args.KnownRisks)
		appendList("Follow-up recommended", args.FollowUpRecommended)
		for _, artifact := range args.Artifacts {
			label := artifact.RelPath
			if label == "" {
				label = artifact.Path
			}
			if label == "" {
				label = artifact.ID
			}
			if label != "" {
				result = append(result, DimStyle.Render("    • Artifact: "+sanitizeToolDisplayText(label)))
			}
		}
	}

	if toolOutcomeKindOf(b) != toolOutcomeNone {
		before := len(result)
		appendToolOutcome(&result, b, contentWidth, true)
		if len(result) > before && before > 1 {
			// Separate the envelope from the report above it, without
			// opening the body with a blank line.
			result = slices.Insert(result, before, "")
		}
	}
	result = appendToolElapsedToHeader(result, b, cardWidth)
	return b.renderToolCardWithIgnoredArgs(blockStyle, cardWidth, toolCardTitle("TOOL CALL", b.displayLabelID()), result, toolCardBg, railANSISeq("tool", b.Focused))
}

// compactContextDisplaySection describes one labelled section in the
// compact_context card body. The label is rendered with the standard "↳ X:"
// header style and the value is either prose (a single wrapped paragraph)
// or a list (one wrapped bullet per item).
type compactContextDisplaySection struct {
	label  string
	prose  string
	values []string
}

// renderCompactContextCall renders compact_context cards as structured
// sections (Objective / Completed / Decisions / Open issues / Next / State
// files) instead of the generic flat "key: value" surface. The generic path
// folds the six fields into the header parameter summary, where the card
// width truncates them away entirely — the continuation state was
// effectively invisible. This path mirrors renderProseControlCall's section
// + bullet layout so six long fields stay navigable. The model-driven schema
// is unchanged.
func (b *Block) renderCompactContextCall(width int, spinnerFrame string) []string {
	metrics := newDoneToolCardMetrics(width)
	blockStyle := metrics.blockStyle
	toolCardBg := metrics.toolCardBg
	cardWidth := metrics.cardWidth
	contentWidth := metrics.contentWidth

	// The checkpoint card is always expanded behind a bare tool-name header:
	// the objective it would summarize is the body's own ↳ Objective:
	// section.
	prefix := b.renderToolPrefix(spinnerFrame)
	argsJSON := b.RawArgs
	if strings.TrimSpace(argsJSON) == "" {
		argsJSON = b.Content
	}
	// decodeCompactContextArgs resolves every field, including the array
	// fields, so the sections below consume its result directly rather than
	// re-parsing argsJSON per field.
	args, argsReady := decodeCompactContextArgs(argsJSON)
	objective := strings.TrimSpace(args.ActiveObjective)
	next := strings.TrimSpace(args.NextStep)

	headerLine := renderToolHeaderLine(prefix, b.ToolName)
	headerLine = buildToolHeaderLine(headerLine, b.ToolProgress, cardWidth, b.toolExecutionIsQueued() && b.ToolQueuedByExecutionEvent, b.toolExecutionIsRunning())
	result := []string{headerLine}

	switch {
	case !argsReady && !b.ResultDone:
		// Nothing decodable yet: the arguments are still streaming, and the
		// header already carries the "N chars received" progress. Rendering a
		// half-decoded state here would flash sections that the finished card
		// then folds away.
	case argsReady:
		appendCompactContextSections(&result, []compactContextDisplaySection{
			{label: "Objective", prose: objective},
			{label: "Completed", values: args.Completed},
			{label: "Decisions", values: args.Decisions},
			{label: "Open issues", values: args.OpenIssues},
			{label: "Next", prose: next},
			{label: "State files", values: args.StateFiles},
		}, contentWidth)
	default:
		// Expanded, but no schema field resolved: the payload nests the state
		// under an unexpected key or misspells every field name — exactly the
		// shapes the validator rejects. Fall back to the submitted keys so the
		// failure stays diagnosable instead of leaving an empty card.
		appendCompactContextRawArgs(&result, argsJSON, contentWidth)
	}

	// The result envelope closes the body, after the submitted args on
	// expanded cards. Collapsed cards keep it on one line, so the failure
	// reads at a glance without opening the card.
	switch {
	case toolOutcomeKindOf(b) != toolOutcomeNone:
		before := len(result)
		appendToolOutcome(&result, b, contentWidth, true)
		if len(result) > before && before > 1 {
			result = slices.Insert(result, before, "")
		}
	case b.ResultDone:
		// The success acknowledgement carries the "no reset has occurred
		// yet" caveat, so an expanded card must keep it: only a later
		// checkpoint event confirms the reset actually applied.
		display := strings.TrimSpace(toolDisplayResultContent(b))
		if display == "" {
			break
		}
		result = append(result, "", toolFieldSection(ToolResultExpandedStyle, "Result"))
		for _, line := range wrapText(sanitizeToolDisplayText(display), contentWidth) {
			result = append(result, DimStyle.Render("    "+line))
		}
	}

	result = appendToolElapsedToHeader(result, b, cardWidth)
	return b.renderToolCardWithIgnoredArgs(blockStyle, cardWidth, toolCardTitle("TOOL CALL", b.displayLabelID()), result, toolCardBg, railANSISeq("tool", b.Focused))
}

// appendGenericToolArgSections renders the arguments a generic card could not
// fit on its header: each one becomes a "↳ Label:" section, with JSON arrays
// broken into bullets the way the dedicated cards render their lists.
func appendGenericToolArgSections(result *[]string, keys []string, vals map[string]string, contentWidth int) {
	for _, k := range keys {
		value := strings.TrimSpace(vals[k])
		if value == "" {
			continue
		}
		label := toolArgSectionLabel(k)
		if label == "" {
			continue
		}
		*result = append(*result, toolFieldSection(ToolResultExpandedStyle, label))
		if items, isList := genericToolArgList(value); isList {
			for _, item := range items {
				for i, line := range wrapText(sanitizeToolDisplayText(item), contentWidth-2) {
					bullet := "  "
					if i == 0 {
						bullet = "• "
					}
					*result = append(*result, DimStyle.Render("    "+bullet+line))
				}
			}
			continue
		}
		for _, line := range wrapText(sanitizeToolDisplayText(value), contentWidth) {
			*result = append(*result, DimStyle.Render("    "+line))
		}
	}
}

// appendCompactContextSections writes a sequence of labelled sections into
// the card body: empty sections are skipped, prose sections become a single
// wrapped paragraph under the header, list sections become "•" bullets with
// per-item wrapping. Sections are separated by blank lines to give the eye
// an anchor between groups.
func appendCompactContextSections(result *[]string, sections []compactContextDisplaySection, contentWidth int) {
	wrote := false
	for _, sec := range sections {
		prose := strings.TrimSpace(sec.prose)
		values := sec.values
		if prose == "" && len(values) == 0 {
			continue
		}
		if wrote {
			*result = append(*result, "")
		}
		*result = append(*result, toolFieldSection(ToolResultExpandedStyle, sec.label))
		if prose != "" {
			for _, line := range wrapText(sanitizeToolDisplayText(prose), contentWidth) {
				*result = append(*result, toolFieldBody(DimStyle, line))
			}
		} else {
			for _, value := range values {
				for i, line := range wrapText(sanitizeToolDisplayText(value), contentWidth-2) {
					bullet := "  "
					if i == 0 {
						bullet = "• "
					}
					*result = append(*result, DimStyle.Render("    "+bullet+line))
				}
			}
		}
		wrote = true
	}
}

// compactContextRawArgMaxLines bounds the fallback dump below: a rejected
// payload can carry the whole continuation state under one wrong key, and
// the card must not grow to a hundred lines just to say "these are not the
// fields".
const compactContextRawArgMaxLines = 12

// appendCompactContextRawArgs writes the submitted payload under an
// "Arguments" header when no schema field could be resolved, so a rejected
// call still shows what the model actually sent. Recognisable JSON objects
// render on the generic "key: value" surface; anything else falls back to
// the raw text.
func appendCompactContextRawArgs(result *[]string, argsJSON string, contentWidth int) {
	trimmed := strings.TrimSpace(argsJSON)
	if trimmed == "" {
		return
	}
	var lines []string
	keys, vals := parseToolArgs(trimmed)
	if len(keys) == 0 {
		lines = wrapText(sanitizeToolDisplayText(trimmed), contentWidth)
	} else {
		for _, k := range keys {
			lines = append(lines, wrapText(sanitizeToolDisplayText(k+": "+vals[k]), contentWidth)...)
			if len(lines) > compactContextRawArgMaxLines {
				break
			}
		}
	}
	if len(lines) == 0 {
		return
	}
	hidden := 0
	if len(lines) > compactContextRawArgMaxLines {
		hidden = len(lines) - compactContextRawArgMaxLines
		lines = lines[:compactContextRawArgMaxLines]
	}
	*result = append(*result, toolFieldSection(ToolResultExpandedStyle, "Arguments"))
	for _, line := range lines {
		*result = append(*result, DimStyle.Render("    "+line))
	}
	if hidden > 0 {
		*result = append(*result, DimStyle.Render(fmt.Sprintf("    ... %d more lines hidden.", hidden)))
	}
}

// decodeCompactContextArgs parses compact_context arguments strictly when
// possible and falls back to tolerant field recovery otherwise, so a single
// wrongly-typed field (the schema is open enough to admit "everything is a
// string" mistakes) does not blank the whole continuation state card.
// Returns the parsed state plus argsReady, which reports whether at least
// one schema field resolved: a strict decode also succeeds on a payload
// that carries none of them (unknown keys are ignored), and the caller must
// fall back to the raw arguments in that case instead of rendering an empty
// set of sections.
func decodeCompactContextArgs(argsJSON string) (tools.CompactContextArgs, bool) {
	var args tools.CompactContextArgs
	trimmed := strings.TrimSpace(argsJSON)
	if trimmed == "" {
		return args, false
	}
	if json.Unmarshal([]byte(trimmed), &args) != nil {
		_, vals := parseToolArgs(trimmed)
		args.ActiveObjective = vals["active_objective"]
		args.NextStep = vals["next_step"]
		if vals["completed"] != "" {
			args.Completed = paramStringList(vals["completed"])
		}
		if vals["decisions"] != "" {
			args.Decisions = paramStringList(vals["decisions"])
		}
		if vals["open_issues"] != "" {
			args.OpenIssues = paramStringList(vals["open_issues"])
		}
		if vals["state_files"] != "" {
			args.StateFiles = paramStringList(vals["state_files"])
		}
	}
	ready := strings.TrimSpace(args.ActiveObjective) != "" || strings.TrimSpace(args.NextStep) != "" ||
		len(args.Completed) > 0 || len(args.Decisions) > 0 || len(args.OpenIssues) > 0 || len(args.StateFiles) > 0
	return args, ready
}

// firstSentence returns the first sentence of s, trimmed of whitespace.
// Used to extract a one-line summary for the collapsed compact_context
// card so a 5-line objective does not blow the width budget. Iterates by
// rune so multibyte CJK terminators are not sliced in half.
//
// An ASCII terminator ('.', '!', '?') only ends a sentence when whitespace
// or the end of the string follows it: continuation state is full of paths
// ("internal/tui/block.go"), commands ("go test ./...") and versions
// ("v1.2"), and cutting at their inner dots truncated the summary mid-token.
// Fullwidth terminators ('。', '！', '？') need no trailing whitespace —
// CJK prose does not put a space after them.
func firstSentence(s string) string {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return ""
	}
	for i, r := range trimmed {
		switch r {
		case '。', '！', '？':
		case '.', '!', '?':
			if rest := trimmed[i+len(string(r)):]; rest != "" {
				if next, _ := utf8.DecodeRuneInString(rest); !unicode.IsSpace(next) {
					continue
				}
			}
		default:
			continue
		}
		return strings.TrimSpace(trimmed[:i+len(string(r))])
	}
	return trimmed
}

func doneResultIsRejected(result string) bool {
	trimmed := strings.ToLower(strings.TrimSpace(result))
	return strings.HasPrefix(trimmed, "done rejected:") || strings.HasPrefix(trimmed, "done rejected automatically:")
}

func doneRejectedReason(result string) string {
	trimmed := strings.TrimSpace(result)
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "done rejected automatically:") {
		return strings.TrimSpace(trimmed[len("Done rejected automatically:"):])
	}
	if strings.HasPrefix(lower, "done rejected:") {
		return strings.TrimSpace(trimmed[len("Done rejected:"):])
	}
	return trimmed
}

func compactToolHiddenResultLines(b *Block, contentWidth int) int {
	if b == nil || !toolUsesCompactDetailToggle(b.ToolName) {
		return 0
	}
	if strings.TrimSpace(b.ResultContent) == "" || (b.toolResultIsCancelled() && toolCancelledDetailText(b.ResultContent) == "") {
		return 0
	}
	displayResult := b.stripResultNotes(toolExpandedResultContent(b.ToolName, b.ResultContent))
	if b.ToolName == tools.NameLsp && !b.toolResultIsError() && !b.toolResultIsCancelled() {
		displayResult = toolDisplayResultContent(b)
	}
	if b.ToolName == tools.NameDelete {
		displayResult = sanitizeToolDisplayText(displayResult)
		nonEmpty := 0
		for line := range strings.SplitSeq(strings.TrimRight(displayResult, "\n"), "\n") {
			if strings.TrimSpace(line) != "" {
				nonEmpty++
			}
		}
		if nonEmpty > 1 {
			return 1
		}
		return 0
	}
	resLines := strings.Split(strings.TrimRight(displayResult, "\n"), "\n")
	if len(resLines) > maxToolCallCompactResultLines+1 {
		return len(resLines) - maxToolCallCompactResultLines
	}
	_, hidden := toolExpandedResultLines(displayResult, contentWidth, false)
	return hidden
}

func compactToolHiddenParamLines(toolName string, keys []string, vals map[string]string, mainPart string, contentWidth int, expanded bool) int {
	if expanded || mainPart != "" || len(keys) == 0 || toolName == tools.NameSkill {
		return 0
	}
	// In compact mode we show only the first param line; Complete shows no param
	// lines until expanded.
	start := 1
	if toolName == tools.NameComplete {
		start = 0
	}
	if start >= len(keys) {
		return 0
	}
	hidden := 0
	for _, k := range keys[start:] {
		line := fmt.Sprintf("%s: %s", k, vals[k])
		hidden += len(wrapIndentedText(line, contentWidth))
	}
	return hidden
}

func compactToolHiddenDetailLines(b *Block, keys []string, vals map[string]string, mainPart string, contentWidth int, expanded bool) int {
	if b == nil || contentWidth <= 0 || expanded || !toolUsesCompactDetailToggle(b.ToolName) {
		return 0
	}
	hidden := 0
	hidden += compactToolHiddenParamLines(b.ToolName, keys, vals, mainPart, contentWidth, expanded)

	// Result differences.
	if strings.TrimSpace(b.ResultContent) != "" && !(b.toolResultIsCancelled() && toolCancelledDetailText(b.ResultContent) == "") {
		switch b.ToolName {
		case tools.NameShell:
			// Shell collapsed mode hides the command body and all output, so the
			// disclosure state is represented by the header glyph instead of a
			// body-level line-count hint.
			hidden++
		case tools.NameSkill:
			if !b.toolResultIsError() && !b.toolResultIsCancelled() {
				displayResult := sanitizeToolDisplayText(toolDisplayResultContent(b))
				hidden += toolCollapsedVisibleLineCount(displayResult, contentWidth)
			} else {
				hidden += compactToolHiddenResultLines(b, contentWidth)
			}
		case tools.NameComplete:
			if !b.toolResultIsError() && !b.toolResultIsCancelled() {
				if more := toolCollapsedVisibleLineCount(b.stripResultNotes(b.ResultContent), contentWidth) - 2; more > 0 {
					hidden += more
				}
			} else {
				hidden += compactToolHiddenResultLines(b, contentWidth)
			}
		default:
			hidden += compactToolHiddenResultLines(b, contentWidth)
		}
	}
	return hidden
}

func (b *Block) compactToolResultForceExpanded(contentWidth int) bool {
	if b == nil {
		return false
	}
	switch b.ToolName {
	case tools.NameGrep, tools.NameGlob, tools.NameShell, tools.NameLsp:
		// Search cards have their own count-based summaries; the generic
		// "only one hidden line" heuristic must not force them expanded, or
		// Space could never collapse them again (the toggle guard below).
		return false
	}
	keys, vals := b.toolArgsParsed()
	_, mainPart, _, _, _, _, _ := b.toolHeaderMeta()
	hidden := compactToolHiddenDetailLines(b, keys, vals, mainPart, contentWidth, false)
	if hidden == 1 {
		return true
	}
	// An error/cancelled card renders its outcome in the collapsed body too, so
	// when no other line is hidden and the envelope has nothing left to reveal
	// the toggle would only restructure the same text. Keep the compact row and
	// deny the marker.
	if toolOutcomeKindOf(b) != toolOutcomeNone {
		return hidden == 0 && !toolOutcomeFoldable(b, contentWidth)
	}
	return false
}

func compactToolContentWidthForRenderWidth(width int) int {
	return newToolCardMetrics(width).contentWidth
}

func (b *Block) compactToolResultForceExpandedForRenderWidth(width int) bool {
	if width <= 0 {
		return false
	}
	return b.compactToolResultForceExpanded(compactToolContentWidthForRenderWidth(width))
}

// compactToolHeaderResultSummary reports the one-line result summary a compact
// card carries on its header instead of in a body row: the search hit count
// joins the query like grep's match count, job_kill's stop acknowledgment and
// job_output's read summary join the job id. All are the fact the collapsed
// body would have shown, so the folded card stays a single line. Sharing the
// header with the primary argument is also what keeps that argument on a
// narrow card: the job id is the handle the next job_output / job_kill call
// needs, so the summary yields first. ok is true whenever the card owns a
// header summary — with an empty summary it renders no body row either.
func compactToolHeaderResultSummary(b *Block) (summary string, ok bool) {
	if b == nil {
		return "", false
	}
	switch toolNameKey(b.ToolName) {
	case tools.NameLsp, tools.NameJobKill:
		if b.toolExecutionIsQueued() {
			return "", true
		}
		return formatToolResultSummaryLine(b), true
	case tools.NameJobOutput:
		if !b.ResultDone || b.toolResultIsError() || b.toolResultIsCancelled() {
			return "", true
		}
		return jobOutputSummaryLine(b.stripResultNotes(b.ResultContent)), true
	}
	return "", false
}

func (b *Block) renderCompactExpandableToolCall(width int, spinnerFrame string) []string {
	metrics := newWideHeaderToolCardMetrics(width)
	blockStyle := metrics.blockStyle
	toolCardBg := metrics.toolCardBg
	cardWidth := metrics.cardWidth
	contentWidth := compactToolContentWidthForRenderWidth(width)

	forceExpanded := b.compactToolResultForceExpanded(contentWidth)
	expanded := b.ToolCallDetailExpanded || forceExpanded
	// Argument-level facts that would otherwise cost a body row: the handle a
	// later job_output / job_kill call needs (a just-promoted background shell
	// job) and the count job_list leaves behind its fold. job_output's read
	// summary rides the header next to the id it read, so its id survives a
	// narrow card (compactToolHeaderResultSummary).
	headerSuffix := ""
	switch {
	case b.ToolName == tools.NameShell:
		if !expanded && !b.toolResultIsError() && !b.toolResultIsCancelled() {
			headerSuffix = parseJobResultID(b.ResultContent)
		}
	case b.ToolName == tools.NameJobList:
		if b.ResultDone && !b.toolResultIsError() && !b.toolResultIsCancelled() {
			headerSuffix = jobListSummaryLine(b.ResultContent)
		}
	}
	keys, vals := b.toolArgsParsed()
	paramSummary, mainPart, grayPart, collapsedMain, collapsedGray, collapsedOK, _ := b.toolHeaderMeta()
	isActive := b.toolExecutionIsRunning() && spinnerFrame != ""
	if b.ToolName == tools.NameShell && !expanded && collapsedOK {
		mainPart, grayPart = collapsedMain, collapsedGray
	}
	// Tools with no dedicated header entry (artifact/result/task helpers, and
	// any MCP tool) get the shared shape from the generic rule instead of a
	// body full of "key: value" lines.
	var genericBodyKeys []string
	genericArgs := false
	if mainPart == "" && grayPart == "" && paramSummary == "" && len(keys) > 0 && len(b.toolArgDiagnostics()) == 0 {
		mainPart, grayPart, genericBodyKeys = genericToolHeaderParts(keys, vals)
		genericArgs = mainPart != "" || grayPart != "" || len(genericBodyKeys) > 0
	}

	result := make([]string, 0, 16)
	prefix := b.renderToolPrefixForExpanded(spinnerFrame, expanded)
	// The marker states what the toggle can do, not how much text is hidden:
	// a collapsed card with nothing hidden still opens on space, so it earns
	// a ▸ like every other toggleable card. Force-expanded cards are stuck
	// and must not claim a marker the toggle cannot honour. shell never
	// force-expands (compactToolResultForceExpanded returns false for it), so
	// it always keeps its marker.
	if b.ResultDone && !forceExpanded {
		prefix = renderToolDisclosurePrefix(prefix, expanded)
	}
	var elapsedOnHeader bool
	headerSummary, summaryOnHeader := compactToolHeaderResultSummary(b)
	toolHeaderLine := renderToolHeaderLine(prefix, b.ToolName)
	if summaryOnHeader {
		if b.ToolName == tools.NameJobOutput && !b.toolResultIsError() && !b.toolResultIsCancelled() {
			toolHeaderLine = appendJobOutputHeaderDetails(toolHeaderLine, mainPart, grayPart, headerSummary, toolHeaderElapsedLabel(b), cardWidth-4)
			elapsedOnHeader = true
		} else {
			toolHeaderLine = appendSearchHeaderSummary(toolHeaderLine, mainPart, grayPart, headerSummary, cardWidth-4)
		}
	} else {
		toolHeaderLine = appendToolHeaderSummary(toolHeaderLine, mainPart, grayPart, paramSummary, cardWidth-4)
	}
	toolHeaderLine = buildToolHeaderLine(toolHeaderLine, b.ToolProgress, cardWidth, b.toolExecutionIsQueued() && b.ToolQueuedByExecutionEvent, isActive)
	result = append(result, toolHeaderLine)

	if b.ToolName == tools.NameShell {
		if expanded {
			appendBashCommandBlock(&result, vals["command"], contentWidth, true)
			result = append(result, bashMetaLines(cloneToolValsWithDisplayDirs(b, vals), contentWidth)...)
		} else {
			appendBashCollapsedSummary(&result, b, vals, contentWidth, !collapsedOK)
		}
	} else if genericArgs {
		// The header carries the subject and the short options; only the long
		// or structured arguments are left, and only an expanded card shows
		// them.
		if expanded && len(genericBodyKeys) > 0 {
			appendGenericToolArgSections(&result, genericBodyKeys, vals, contentWidth)
			// The result body below is unlabelled on generic cards; with
			// argument sections above it, it needs its own header or it reads
			// as part of the last section.
			if strings.TrimSpace(toolDisplayResultContent(b)) != "" && toolOutcomeKindOf(b) == toolOutcomeNone {
				result = append(result, toolFieldSection(ToolResultExpandedStyle, "Result"))
			}
		}
	} else if mainPart == "" && paramSummary == "" && len(keys) > 0 {
		// Keep schema-invalid generic calls on the same inline parameter surface
		// as successful calls. The diagnostic pass below applies the per-field
		// style without hiding valid fields from the original request.
		paramSummary = genericToolParamSummary(b.headerParamSummaryKeys(keys, vals), vals)
		result[0] = appendToolHeaderSummary(toolHeaderLine, mainPart, grayPart, paramSummary, cardWidth-4)
	}

	if b.ResultContent != "" || b.DoneSummary != "" || b.toolExecutionIsQueued() {
		// A summary that already rides the header must not print again as a
		// body row.
		if !summaryOnHeader {
			if summary := formatToolResultSummaryLine(b); summary != "" && !b.toolExecutionIsQueued() && !(b.ToolName == tools.NameShell && !expanded) {
				result = append(result, toolSummaryLine(summary))
			}
		}
		if b.ToolName == tools.NameShell {
			if expanded {
				appendBashExpandedResult(&result, b, contentWidth)
			}
		} else if kind := toolOutcomeKindOf(b); kind != toolOutcomeNone {
			appendToolOutcomeBody(&result, kind, toolDisplayResultContent(b), contentWidth, expanded)
		} else if expanded {
			if displayResult := strings.TrimSpace(toolDisplayResultContent(b)); displayResult != "" {
				for _, line := range toolExpandedTextLines(displayResult, contentWidth) {
					result = append(result, DimStyle.Render(toolResultIndent+line))
				}
			}
		}
		if strings.TrimSpace(b.DoneSummary) != "" {
			if expanded {
				result = append(result, toolFieldSection(ToolResultExpandedStyle, "Completed"))
				for _, line := range toolExpandedTextLines(sanitizeToolDisplayText(b.DoneSummary), contentWidth) {
					result = append(result, "    "+line)
				}
			} else {
				appendCollapsedSummaryLines(&result, b.DoneSummary, cardWidth-10, ToolResultStyle)
			}
		}
	}

	if len(b.ImageParts) > 0 {
		imagesRendered := b.appendImagePreviewLines(&result, contentWidth, toolCardBg, blockStyle.GetPaddingTop(), len(result) > 0)
		if !imagesRendered {
			label := "  📎"
			if len(b.ImageParts) > 1 {
				label = fmt.Sprintf("  📎 %d", len(b.ImageParts))
			}
			result = append(result, DimStyle.Render(label))
		}
	}

	if !elapsedOnHeader {
		result = appendToolElapsedToHeader(result, b, cardWidth)
	}
	// The handle/count is appended after the elapsed suffix so a tight card
	// drops the timestamp rather than the identifier a follow-up call needs.
	if headerSuffix != "" && len(result) > 0 {
		result[0] = appendToolHeaderSuffix(result[0], DimStyle.Render(" · "+headerSuffix), cardWidth-4)
	}
	return b.renderToolCardWithIgnoredArgs(blockStyle, cardWidth, toolCardTitle("TOOL CALL", b.displayLabelID()), result, toolCardBg, railANSISeq("tool", b.Focused))
}

// renderToolPrefix returns a concise status indicator.
func (b *Block) renderToolPrefix(spinnerFrame string) string {
	return b.renderToolPrefixForExpanded(spinnerFrame, b.ToolCallDetailExpanded)
}

func styleToolStatusPrefix(prefix string) string {
	if prefix == "" {
		return ""
	}
	if strings.HasPrefix(prefix, "✓") {
		return ToolStatusSuccessStyle.Render("✓") + prefix[len("✓"):]
	}
	if strings.HasPrefix(prefix, "✗") {
		return ToolStatusErrorStyle.Render("✗") + prefix[len("✗"):]
	}
	for _, marker := range []string{receivingToolGlyph, queuedToolGlyph, pendingToolGlyph} {
		if strings.HasPrefix(prefix, marker) {
			return ToolStatusNeutralStyle.Render(marker) + prefix[len(marker):]
		}
	}
	return prefix
}

func renderToolHeaderLine(prefix, toolName string) string {
	return "  " + styleToolStatusPrefix(prefix) + " " + ToolCallStyle.Render(toolName)
}

func renderAnimatedToolPrefixGlyph(spinnerFrame string) string {
	iconColor := NeonAccentColor(1800 * time.Millisecond)
	styled := lipgloss.NewStyle().Foreground(lipgloss.Color(iconColor)).Render(spinnerFrame)
	if strings.HasSuffix(styled, "\x1b[0m") {
		return styled[:len(styled)-len("\x1b[0m")] + "\x1b[39m"
	}
	if strings.HasSuffix(styled, "\x1b[m") {
		return styled[:len(styled)-len("\x1b[m")] + "\x1b[39m"
	}
	return styled
}

func (b *Block) renderToolPrefixForExpanded(spinnerFrame string, compactExpanded bool) string {
	if b.toolArgumentsAreReceiving() {
		return receivingToolGlyph
	}
	if b.toolExecutionIsRunning() && spinnerFrame != "" {
		return renderAnimatedToolPrefixGlyph(spinnerFrame)
	}
	if b.toolExecutionIsQueued() {
		if b.ToolQueuedByExecutionEvent {
			return queuedToolGlyph
		}
		return pendingToolGlyph
	}
	if b.ToolName == tools.NameDelegate {
		if b.toolResultIsError() {
			return "✗"
		}
		if b.toolResultIsCancelled() {
			return "◌"
		}
		if b.ResultDone || strings.TrimSpace(b.ResultContent) != "" {
			return "✓"
		}
		// The delegation card is always expanded and carries no expand/collapse
		// toggle, so it must never show a disclosure marker. While the worker
		// is still spinning up (not running, not yet done, no result yet) it is
		// simply pending.
		return pendingToolGlyph
	}
	if b.ToolName == tools.NameHandoff {
		// The handoff card is always expanded and has no detail toggle, so it
		// must never show a disclosure marker: pending until the user decides,
		// then the terminal state.
		if b.ResultDone || strings.TrimSpace(b.ResultContent) != "" {
			if b.toolResultIsError() || handoffRejectedReason(b.ResultContent) != "" {
				return "✗"
			}
			if b.toolResultIsCancelled() {
				return "◌"
			}
			return "✓"
		}
		return pendingToolGlyph
	}
	if toolUsesCompactDetailToggle(b.ToolName) {
		if !b.ResultDone {
			if compactExpanded {
				return toolDisclosureExpanded
			}
			return toolDisclosureCollapsed
		}
		if b.toolResultIsError() {
			return "✗"
		}
		if b.ToolName == tools.NameDone && doneResultIsRejected(b.ResultContent) {
			return "✗"
		}
		if b.toolResultIsCancelled() {
			if b.ToolName == tools.NameDone {
				return "✗"
			}
			return "◌"
		}
		return "✓"
	}
	if b.ResultDone || b.ResultContent != "" {
		if b.toolResultIsError() {
			return "✗"
		}
		if b.toolResultIsCancelled() {
			return "◌"
		}
		return "✓"
	}
	if toolCardAlwaysExpanded(b.ToolName) {
		// Always-expanded cards have no fold state to advertise.
		return pendingToolGlyph
	}
	if b.Collapsed {
		return toolDisclosureCollapsed
	}
	return toolDisclosureExpanded
}

// appendToolHeaderSummary appends the main parameter, gray options and the
// parameter summary to a styled tool header line as one prioritized layout
// decision. Widths use the same metric the card box is padded with
// (ansi.StringWidth, matching lipgloss): it also counts grapheme clusters
// (for example a base glyph plus VS16) the way the box does, where runewidth
// counts them narrower — a budget measured with the wrong ruler either
// overflows the card or drops a suffix that still fits.
func appendToolHeaderSummary(headerLine, mainPart, grayPart, paramSummary string, maxWidth int) string {
	baseWidth := ansi.StringWidth(headerLine)
	if baseWidth >= maxWidth {
		return ansi.Truncate(headerLine, maxWidth, "…")
	}
	budget := maxWidth - baseWidth - 1
	if budget <= 0 {
		return headerLine
	}

	mainPart = sanitizeToolDisplayText(mainPart)
	grayPart = sanitizeDisplayTextKeepingSGR(grayPart)
	paramSummary = sanitizeToolDisplayText(paramSummary)
	if mainPart == "" && grayPart == "" {
		if paramSummary == "" {
			return headerLine
		}
		return headerLine + " " + DimStyle.Render(truncateToolHeaderTail(paramSummary, budget))
	}
	if mainPart == "" {
		return headerLine + " " + DimStyle.Render(truncateToolHeaderGray(grayPart, budget))
	}

	mainWidth := ansi.StringWidth(mainPart)
	grayWidth := ansi.StringWidth(grayPart)
	if grayPart == "" || mainWidth >= budget {
		return headerLine + " " + truncateToolHeaderTail(mainPart, budget)
	}

	remaining := budget - mainWidth - 1
	if remaining < 5 {
		return headerLine + " " + mainPart
	}
	if grayWidth > remaining {
		grayPart = truncateToolHeaderGray(grayPart, remaining)
	}
	return headerLine + " " + mainPart + " " + DimStyle.Render(grayPart)
}

// appendJobOutputHeaderDetails appends a job_output read summary, an optional
// ignored/invalid argument group, and the elapsed label to the header line as
// one prioritized layout decision. The id is the handle every later
// job_output / job_kill call needs, so it truncates last: the elapsed label
// yields first, then the read summary, and only then is the id narrowed to
// the remaining header width.
func appendJobOutputHeaderDetails(headerLine, pattern, grayPart, summary, elapsed string, maxWidth int) string {
	// Measure with ansi, not runewidth: lipgloss pads the card box with the
	// same metric, which counts grapheme clusters (for example a base glyph
	// plus VS16) the way the box does. runewidth counts them narrower and
	// would spend more budget than the box actually gives away.
	baseWidth := ansi.StringWidth(headerLine)
	const sep = " · "
	sepWidth := ansi.StringWidth(sep)
	if baseWidth >= maxWidth {
		return ansi.Truncate(headerLine, maxWidth, "…")
	}
	// The pattern joins the header with a single space; everything after it
	// joins through sep. budget is the width the pattern and its suffix group
	// share, so the fit checks below subtract sepWidth themselves — taking it
	// out of budget too would reserve the separator twice and drop a suffix
	// that still fits.
	budget := maxWidth - baseWidth - 1
	if budget <= 0 {
		return headerLine
	}
	pattern = sanitizeToolDisplayText(pattern)
	summary = sanitizeToolDisplayText(summary)
	elapsed = strings.TrimSpace(elapsed)

	var suffix string
	var suffixWidth int
	var summaryWidth int
	if summary != "" {
		suffix = summary
		summaryWidth = ansi.StringWidth(summary)
		suffixWidth = summaryWidth
	}
	if grayPart != "" {
		// The ignored/invalid-argument group rides the same summary: when the
		// summary cannot fit, the option group still has one word of its own.
		optionSuffix := grayPart
		widthDelta := ansi.StringWidth(optionSuffix)
		if suffix != "" {
			widthDelta += sepWidth + suffixWidth
			suffix = optionSuffix + sep + suffix
		} else {
			suffix = optionSuffix
		}
		suffixWidth = widthDelta
	}
	if elapsed != "" {
		timerSuffix := elapsedGlyph + " " + elapsed
		timerWidth := ansi.StringWidth(timerSuffix)
		if suffix != "" {
			suffixWidth += sepWidth + timerWidth
			suffix += sep + timerSuffix
		} else {
			suffix, suffixWidth = timerSuffix, timerWidth
		}
	}
	patternWidth := ansi.StringWidth(pattern)
	if patternWidth > budget {
		pattern = truncateToolHeaderMiddle(pattern, budget)
		patternWidth = ansi.StringWidth(pattern)
	}
	// summary is never empty from the production caller (jobOutputSummaryLine
	// always returns a sentence), but a defensive empty must not print a
	// dangling separator: fall back to the id alone instead.
	if suffix == "" || summary == "" && grayPart == "" && elapsed == "" {
		return headerLine + " " + pattern
	}
	if suffixWidth <= budget-patternWidth-sepWidth {
		return headerLine + " " + pattern + sep + DimStyle.Render(suffix)
	}
	// The suffix does not fit whole: the elapsed label is dropped first, then
	// the optional argument group, and the id is the only fact left to keep.
	if summaryWidth <= budget-patternWidth-sepWidth {
		return headerLine + " " + pattern + sep + DimStyle.Render(summary)
	}
	return headerLine + " " + pattern
}

// appendSearchHeaderSummary appends the pattern, optional search parameters and
// the result summary to a search tool header as a single line. Priority goes
// to the pattern (the primary argument) and to the result summary (count
// facts): the parameters are compressed or dropped before either yields. When
// a result summary exists, parameters only share the line with an intact
// pattern + summary pair; otherwise they use the space left by the pattern.
func appendSearchHeaderSummary(headerLine, mainPart, grayPart, summary string, maxWidth int) string {
	baseWidth := ansi.StringWidth(headerLine)
	if baseWidth >= maxWidth {
		return ansi.Truncate(headerLine, maxWidth, "…")
	}
	budget := maxWidth - baseWidth - 1
	if budget <= 0 {
		return headerLine
	}
	mainPart = sanitizeToolDisplayText(mainPart)
	summary = sanitizeToolDisplayText(summary)
	grayPart = sanitizeDisplayTextKeepingSGR(grayPart)

	const minGrayCols = 8
	if summary == "" {
		if mainPart == "" {
			if grayPart == "" {
				return headerLine
			}
			return headerLine + " " + DimStyle.Render(truncateToolHeaderGray(grayPart, budget))
		}
		mainWidth := ansi.StringWidth(mainPart)
		if grayPart == "" || mainWidth >= budget {
			return headerLine + " " + truncateToolHeaderMiddle(mainPart, budget)
		}
		remaining := budget - mainWidth - 1
		if remaining < minGrayCols {
			return headerLine + " " + mainPart
		}
		if grayWidth := ansi.StringWidth(grayPart); grayWidth > remaining {
			grayPart = truncateToolHeaderGray(grayPart, remaining)
		}
		return headerLine + " " + mainPart + " " + DimStyle.Render(grayPart)
	}
	if mainPart == "" {
		return headerLine + " " + DimStyle.Render(truncateToolHeaderGray(summary, budget))
	}

	// The pattern is the primary argument and the count summary is the
	// result fact: they share the line first. The parameters only join an
	// intact pattern + summary pair and are compressed or dropped before the
	// pattern is ever middle-truncated.
	const minPatternCols = 8
	const sep = " · "
	sepWidth := ansi.StringWidth(sep)
	suffixW := ansi.StringWidth(summary)
	patternBudget := budget - suffixW - sepWidth
	if patternBudget < minPatternCols {
		patternBudget = minPatternCols
		if avail := budget - patternBudget - sepWidth; avail > 0 {
			summary = truncateToolHeaderGray(summary, avail)
		} else {
			summary = ""
		}
		suffixW = ansi.StringWidth(summary)
		patternBudget = budget - suffixW - sepWidth
	}
	pattern := mainPart
	if ansi.StringWidth(mainPart) > patternBudget {
		pattern = truncateToolHeaderMiddle(mainPart, patternBudget)
	}
	if summary == "" {
		return headerLine + " " + pattern
	}
	if grayPart == "" {
		return headerLine + " " + pattern + sep + DimStyle.Render(summary)
	}
	// Pattern and summary are intact; the parameters follow the pattern
	// glued with a space, compressed to the remaining width when they do not
	// fit whole, and dropped when only a meaningless fragment would remain.
	remaining := budget - ansi.StringWidth(pattern) - sepWidth - suffixW
	if grayWidth := ansi.StringWidth(grayPart); remaining-1 >= grayWidth {
		return headerLine + " " + pattern + " " + DimStyle.Render(grayPart) + " · " + DimStyle.Render(summary)
	}
	if remaining-1 >= minGrayCols {
		grayPart = truncateToolHeaderGray(grayPart, remaining-1)
		return headerLine + " " + pattern + " " + DimStyle.Render(grayPart) + " · " + DimStyle.Render(summary)
	}
	return headerLine + " " + pattern + sep + DimStyle.Render(summary)
}

// truncateToolHeaderMiddle cuts a plain-text header segment to a width measured
// with the card-box metric (see appendToolHeaderSummary): callers pass text
// whose budget was computed with ansi.StringWidth, and gray segments are
// measured the same way before they reach truncateToolHeaderGray.

// truncateToolHeaderGray shortens a gray header tail that may embed ANSI
// styled diagnostic options; rune-level middle cuts would split escape
// sequences and leak their styles into the rest of the header line.
func truncateToolHeaderGray(s string, maxWidth int) string {
	if maxWidth <= 0 {
		return ""
	}
	if strings.Contains(s, "\x1b") {
		return ansi.Truncate(s, maxWidth, "…")
	}
	return truncateToolHeaderMiddle(s, maxWidth)
}

func truncateToolHeaderTail(s string, maxWidth int) string {
	if maxWidth <= 0 {
		return ""
	}
	if ansi.StringWidth(s) <= maxWidth {
		return s
	}
	if maxWidth == 1 {
		return "…"
	}
	return ansi.Truncate(s, maxWidth, "…")
}

func truncateToolHeaderMiddle(s string, maxWidth int) string {
	if maxWidth <= 0 {
		return ""
	}
	if ansi.StringWidth(s) <= maxWidth {
		return s
	}
	if maxWidth <= 1 {
		return "…"
	}
	if strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")") {
		if maxWidth <= 3 {
			return truncateToolHeaderTail(s, maxWidth)
		}
		inner := strings.TrimSuffix(strings.TrimPrefix(s, "("), ")")
		return "(" + truncateToolHeaderMiddle(inner, maxWidth-2) + ")"
	}
	if maxWidth <= 3 {
		return truncateToolHeaderTail(s, maxWidth)
	}
	leftWidth := (maxWidth - 1) / 2
	rightWidth := maxWidth - 1 - leftWidth
	return tuiCut(s, 0, leftWidth) + "…" + tuiCutRight(s, rightWidth)
}

func tuiCutRight(s string, maxWidth int) string {
	if maxWidth <= 0 {
		return ""
	}
	total := ansi.StringWidth(s)
	if total <= maxWidth {
		return s
	}
	return tuiCut(s, total-maxWidth, total)
}

func (b *Block) renderToolResult(width int) []string {
	contentLines := strings.Split(b.Content, "\n")
	lineCount := len(contentLines)
	metrics := newToolCardMetrics(width)
	style := metrics.blockStyle
	toolCardBg := metrics.toolCardBg
	cardWidth := metrics.cardWidth
	contentWidth := metrics.contentWidth
	if b.Collapsed {
		prefix := "✓"
		if b.IsError || b.toolResultIsError() {
			prefix = "✗"
		} else if b.toolResultIsCancelled() {
			prefix = "◌"
		}
		if b.RecoveryState == message.ToolRecoveryStateOutcomeUnknown {
			prefix = "!"
		}
		if lineCount > maxToolCallCompactResultLines {
			prefix = renderToolDisclosurePrefix(prefix, !b.Collapsed)
		}
		var body []string
		body = append(body, renderToolHeaderLine(prefix, b.ToolName))
		lim := min(lineCount, maxToolCallCompactResultLines)
		for i := range lim {
			for _, w := range wrapText(sanitizeToolDisplayText(contentLines[i]), contentWidth) {
				body = append(body, DimStyle.Render(toolResultIndent+w))
			}
		}
		b.appendImagePreviewLines(&body, contentWidth, toolCardBg, style.GetPaddingTop(), len(body) > 0)
		return b.renderToolCardWithIgnoredArgs(style, cardWidth, toolCardTitle("TOOL RESULT", b.displayLabelID()), body, toolCardBg, railANSISeq("tool", b.Focused))
	}
	headerStyle := ToolResultExpandedStyle
	renderBody := func(s string) string { return s }
	if b.IsError || b.toolResultIsError() {
		headerStyle = ErrorStyle
		renderBody = func(s string) string { return ErrorStyle.Render(s) }
	}
	if b.RecoveryState == message.ToolRecoveryStateOutcomeUnknown {
		headerStyle = LSPWarnStyle
	}
	// The recovery card names the tool it is reporting on, so the label is the
	// whole row and the tool name stays part of it rather than trailing after a
	// hand-built "↳ " prefix.
	headerLabel := "Result from " + b.ToolName
	if b.toolResultIsCancelled() {
		headerLabel = "Cancelled " + b.ToolName
	}
	if b.RecoveryState == message.ToolRecoveryStateOutcomeUnknown {
		headerLabel = "Result unknown " + b.ToolName
	}
	header := toolFieldSection(headerStyle, headerLabel)
	result := []string{header}
	for _, line := range wrapText(sanitizeToolDisplayText(b.Content), contentWidth) {
		result = append(result, "    "+renderBody(line))
	}
	b.appendImagePreviewLines(&result, contentWidth, toolCardBg, style.GetPaddingTop(), len(result) > 0)
	return b.renderToolCardWithIgnoredArgs(style, cardWidth, toolCardTitle("TOOL RESULT", b.displayLabelID()), result, toolCardBg, railANSISeq("tool", b.Focused))
}
