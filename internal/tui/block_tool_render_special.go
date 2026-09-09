package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mattn/go-runewidth"

	"github.com/keakon/chord/internal/tools"
)

func (b *Block) renderTaskCall(width int, spinnerFrame string) []string {
	metrics := newToolCardMetrics(width)
	blockStyle := metrics.blockStyle
	toolCardBg := metrics.toolCardBg
	cardWidth := metrics.cardWidth
	contentWidth := metrics.contentWidth

	args := parseTaskToolArgs(b.Content)
	hasResultText := strings.TrimSpace(b.ResultContent) != ""
	subType := strings.TrimSpace(args.AgentType)
	isActive := b.toolExecutionIsRunning() && spinnerFrame != ""
	prefix := b.renderToolPrefix(spinnerFrame)

	headerLine := renderToolHeaderLine(prefix, b.ToolName)
	if subType != "" {
		headerLine += " " + DimStyle.Render("("+sanitizeToolDisplayText(subType)+")")
	}
	// The delegation card is always expanded behind a bare tool-name header:
	// the description it would summarize is the body's own first section.
	headerLine = buildToolHeaderLine(headerLine, b.ToolProgress, cardWidth, b.toolExecutionIsQueued() && b.ToolQueuedByExecutionEvent, isActive)

	result := []string{headerLine}
	descLines := taskToolExpandedDescriptionLines(b.Content, contentWidth-4)
	if len(descLines) > 0 {
		result = append(result, toolFieldSection(ToolResultExpandedStyle, "Description"))
		for _, line := range descLines {
			result = append(result, "    "+line)
		}
	}
	handle, rest, handleOK := parseTaskToolHandle(b.ResultContent)
	if handleOK || (hasResultText && !b.toolResultIsError() && !b.toolResultIsCancelled()) {
		result = append(result, toolFieldSection(ToolResultExpandedStyle, "Worker"))
		switch {
		case handleOK:
			appendTaskHandleFieldRows(&result, handle)
			// The runtime often appends commentary after the JSON payload
			// (e.g. "Note: ignored unrecognized parameter(s): …"). Render it
			// as a trailing dimmed line under the section, not concatenated
			// back into the structured fields.
			if rest != "" {
				for _, line := range wrapText(sanitizeToolDisplayText(rest), contentWidth) {
					result = append(result, toolFieldBody(DimStyle, line))
				}
			}
		default:
			for _, line := range wrapText(sanitizeToolDisplayText(strings.TrimSpace(b.ResultContent)), contentWidth) {
				result = append(result, toolFieldBody(DimStyle, line))
			}
		}
	} else {
		appendToolOutcome(&result, b, contentWidth, true)
	}
	if strings.TrimSpace(b.DoneSummary) != "" {
		result = append(result, toolFieldSection(ToolResultExpandedStyle, "Completed"))
		for _, line := range toolExpandedTextLines(sanitizeToolDisplayText(b.DoneSummary), contentWidth) {
			result = append(result, "    "+line)
		}
	}
	result = appendToolElapsedToHeader(result, b, cardWidth)
	return b.renderToolCardWithIgnoredArgs(blockStyle, cardWidth, toolCardTitle("TOOL CALL", b.displayLabelID()), result, toolCardBg, railANSISeq("tool", b.Focused))
}

// renderTodoCall renders a TodoWrite tool call as a todo list with status markers.
func (b *Block) renderTodoCall(width int, spinnerFrame string) []string {
	metrics := newToolCardMetrics(width)
	blockStyle := metrics.blockStyle
	toolCardBg := metrics.toolCardBg
	cardWidth := metrics.cardWidth
	contentWidth := max(cardWidth-6, 10)

	// Schema-invalid args must still surface what the caller actually passed,
	// so decode the list item by item and keep every well-formed entry.
	todos := decodeTodoCallItems(b.Content)

	prefix := b.renderToolPrefix(spinnerFrame)
	headerLine := renderToolHeaderLine(prefix, b.ToolName)
	headerLine = appendToolHeaderSummary(headerLine, "", mergeHeaderOptions("", b.diagnosticHeaderOptions()), "", cardWidth-4)
	headerLine = buildToolHeaderLine(headerLine, b.ToolProgress, cardWidth, b.toolExecutionIsQueued() && b.ToolQueuedByExecutionEvent, b.toolExecutionIsRunning())

	var result []string
	result = append(result, headerLine)

	// Even when schema validation failed, keep every well-formed entry visible
	// so the card shows what the caller actually passed.
	if len(todos) > 0 && !b.toolResultIsCancelled() {
		if len(todos) > 1 {
			result = append(result, QuestionSeparatorStyle.Render(fmt.Sprintf("  ▸ %d tasks", len(todos))))
		}
		for _, item := range todos {
			appendTodoCallItemLines(&result, item, contentWidth)
		}
	}

	if b.toolResultIsError() && b.ResultContent != "" {
		result = appendErrorResultLines(result, b.ResultContent, contentWidth)
	} else if b.toolResultIsCancelled() && b.ResultContent != "" {
		result = appendCancelledResultLines(result, b.ResultContent, contentWidth)
	}
	// Empty list: don't show "(no items)" prominently; just omit the list body

	result = appendToolElapsedToHeader(result, b, cardWidth)
	return b.renderToolCardWithIgnoredArgs(blockStyle, cardWidth, toolCardTitle("TOOL CALL", b.displayLabelID()), result, toolCardBg, railANSISeq("tool", b.Focused))
}

// decodeTodoCallItems decodes the todos array item by item so one malformed
// entry cannot hide the remaining well-formed entries on the card.
func decodeTodoCallItems(argsJSON string) []todoCallArgItem {
	var envelope struct {
		Todos []json.RawMessage `json:"todos"`
	}
	if json.Unmarshal([]byte(argsJSON), &envelope) != nil {
		return nil
	}
	items := make([]todoCallArgItem, 0, len(envelope.Todos))
	for _, raw := range envelope.Todos {
		var item todoCallArgItem
		if json.Unmarshal(raw, &item) != nil {
			continue
		}
		items = append(items, item)
	}
	return items
}

// decodeQuestionItemsForDisplay mirrors tools.DecodeQuestionItems but salvages
// every well-formed entry of a partially malformed questions array. The shared
// decoder doubles as the Question tool's execution parser and must stay strict,
// so display gets its own tolerant variant instead.
func decodeQuestionItemsForDisplay(argsJSON string) []tools.QuestionItem {
	var envelope struct {
		Questions []json.RawMessage `json:"questions"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &envelope); err != nil {
		var single struct {
			Questions tools.QuestionItem `json:"questions"`
		}
		if err2 := json.Unmarshal([]byte(argsJSON), &single); err2 == nil && (single.Questions.Question != "" || single.Questions.Header != "") {
			return []tools.QuestionItem{single.Questions}
		}
		return nil
	}
	items := make([]tools.QuestionItem, 0, len(envelope.Questions))
	for _, raw := range envelope.Questions {
		var item tools.QuestionItem
		if json.Unmarshal(raw, &item) != nil {
			continue
		}
		items = append(items, item)
	}
	return items
}

// decodeQuestionAnswers parses the answers array from a Question tool's own
// output. It must be given the clean payload, never the model-visible result:
// the runtime appends diagnostic notes after the payload, and a parser that
// tolerates them has to guess where the JSON ends. Transcripts written before
// the payload/notes split have no clean copy, so tolerate trailing prose there
// rather than dropping the user's answers.
func decodeQuestionAnswers(payload string) (answers []tools.QuestionAnswer, rest string, ok bool) {
	trimmed := strings.TrimSpace(payload)
	if trimmed == "" || (trimmed[0] != '[' && trimmed[0] != '{') {
		return nil, trimmed, false
	}
	dec := json.NewDecoder(strings.NewReader(trimmed))
	if err := dec.Decode(&answers); err != nil || len(answers) == 0 {
		return nil, trimmed, false
	}
	if offset := dec.InputOffset(); offset >= 0 && offset < int64(len(trimmed)) {
		rest = strings.TrimSpace(trimmed[offset:])
	}
	return answers, rest, true
}

// renderQuestionCall renders a Question tool call showing the question text and options.
func (b *Block) renderQuestionCall(width int, spinnerFrame string) []string {
	metrics := newToolCardMetrics(width)
	blockStyle := metrics.blockStyle
	toolCardBg := metrics.toolCardBg
	cardWidth := metrics.cardWidth
	contentWidth := max(cardWidth-6, 10)

	questions := decodeQuestionItemsForDisplay(b.Content)

	var answers []tools.QuestionAnswer
	hasStructuredAnswers := false
	resultNotes := append([]string(nil), b.ResultNotes...)
	if !b.toolResultIsError() && !b.toolResultIsCancelled() {
		// Parse the tool's own output, not the model-visible result: the result
		// has diagnostic notes appended and parsing it means guessing where the
		// payload ended. Transcripts predating the split keep only the combined
		// text, so fall back to it and recover the notes from the tail.
		payload := b.ResultPayload
		if strings.TrimSpace(payload) == "" {
			payload = b.ResultContent
		}
		if parsed, rest, ok := decodeQuestionAnswers(payload); ok {
			answers = parsed
			if rest != "" && len(resultNotes) == 0 {
				resultNotes = []string{rest}
			}
			hasStructuredAnswers = len(questions) > 0
		}
	}

	prefix := b.renderToolPrefix(spinnerFrame)
	// The card always renders every question in full, so the header stays a
	// bare tool-name line: naming the first question there only repeated the
	// first body section.
	headerLine := renderToolHeaderLine(prefix, b.ToolName)
	headerLine = buildToolHeaderLine(headerLine, b.ToolProgress, cardWidth, b.toolExecutionIsQueued() && b.ToolQueuedByExecutionEvent, b.toolExecutionIsRunning())

	var result []string
	result = append(result, headerLine)

	for i, q := range questions {
		answer, hasAnswer := questionAnswerForRender(answers, i, q.Header)
		selectedOptions, customAnswers := splitQuestionSelections(q, answer)
		if q.Header != "" {
			// "↳ Label:" like every other card section: ▸ now means "this card
			// toggles", so it cannot double as a section separator.
			result = append(result, toolFieldSection(QuestionSeparatorStyle, sanitizeToolDisplayText(q.Header)))
		}
		if q.Question != "" {
			qText := sanitizeToolDisplayText(strings.ReplaceAll(q.Question, "<br>", "\n"))
			for line := range strings.SplitSeq(qText, "\n") {
				for _, wl := range wrapText("    "+line, contentWidth) {
					result = append(result, paramValStyle.Render(wl))
				}
			}
		}
		if len(q.Options) > 0 {
			mode := "Single-select"
			if q.Multiple {
				mode = "Multi-select"
			}
			// The numbered list below is self-evidently the options, so the
			// mode carries the only label this section needs.
			result = append(result, DimStyle.Render("    Mode: "+mode))
			for i, opt := range q.Options {
				displayLabel := sanitizeToolDisplayText(opt.Label)
				_, isSelected := selectedOptions[displayLabel]
				marker := " "
				if isSelected {
					marker = "✓"
				}
				optPrefix := fmt.Sprintf("      %s %d. %s", marker, i+1, displayLabel)
				optLine := optPrefix
				if opt.Description != "" {
					descWidth := max(contentWidth-runewidth.StringWidth(optPrefix), 0)
					optLine += " — " + truncateOneLine(sanitizeToolDisplayText(opt.Description), descWidth)
				}
				// A bare check mark is easy to miss in a long list whose
				// descriptions wrap, so the picked option is emphasised and the
				// alternatives dim into the background.
				if isSelected {
					optLine = questionSelectedOptionStyle.Render(optLine)
				} else {
					optLine = DimStyle.Render(optLine)
				}
				result = append(result, optLine)
			}
		}
		switch {
		case hasAnswer && len(q.Options) == 0:
			for _, sel := range answer.Selected {
				result = appendQuestionAnswerLines(result, sel, "    Answer: ", "            ", contentWidth)
			}
		case len(customAnswers) == 1:
			result = appendQuestionAnswerLines(result, customAnswers[0], "    Custom: ", "            ", contentWidth)
		case len(customAnswers) > 1:
			result = append(result, DimStyle.Render("    Custom:"))
			for _, sel := range customAnswers {
				result = appendQuestionAnswerLines(result, sel, "      • ", "        ", contentWidth)
			}
		case len(q.Options) > 0 && !hasStructuredAnswers:
			result = append(result, DimStyle.Render("    Custom: Enabled"))
		}
	}

	switch {
	case b.toolResultIsError() && b.ResultContent != "":
		result = appendErrorResultLines(result, b.ResultContent, contentWidth)
	case b.toolResultIsCancelled() && b.ResultContent != "":
		result = appendCancelledResultLines(result, b.ResultContent, contentWidth)
	case hasStructuredAnswers:
		// The option list already carries the selection inline, so the answers
		// payload itself must not be echoed back. What is left is the runtime's
		// own commentary about the call, which is worth showing.
		if len(resultNotes) > 0 {
			result = append(result, toolFieldMarker(ToolResultStyle, "✓"))
			for _, note := range resultNotes {
				for _, line := range wrapText(sanitizeToolDisplayText(note), contentWidth) {
					result = append(result, ToolResultStyle.Render("    "+line))
				}
			}
		}
	case strings.TrimSpace(b.ResultContent) != "":
		result = append(result, toolFieldMarker(ToolResultStyle, "✓"))
		for _, line := range wrapText(sanitizeToolDisplayText(b.ResultContent), contentWidth) {
			result = append(result, ToolResultStyle.Render("    "+line))
		}
	}
	result = appendToolElapsedToHeader(result, b, cardWidth)
	return b.renderToolCardWithIgnoredArgs(blockStyle, cardWidth, toolCardTitle("TOOL CALL", b.displayLabelID()), result, toolCardBg, railANSISeq("tool", b.Focused))
}

func questionAnswerForRender(answers []tools.QuestionAnswer, index int, header string) (tools.QuestionAnswer, bool) {
	if index >= 0 && index < len(answers) {
		return answers[index], true
	}
	if strings.TrimSpace(header) == "" {
		return tools.QuestionAnswer{}, false
	}
	for _, answer := range answers {
		if answer.Header == header {
			return answer, true
		}
	}
	return tools.QuestionAnswer{}, false
}

func splitQuestionSelections(question tools.QuestionItem, answer tools.QuestionAnswer) (map[string]struct{}, []string) {
	selectedOptions := make(map[string]struct{}, len(answer.Selected))
	if len(answer.Selected) == 0 {
		return selectedOptions, nil
	}

	labels := make(map[string]struct{}, len(question.Options))
	for _, opt := range question.Options {
		labels[sanitizeToolDisplayText(opt.Label)] = struct{}{}
	}

	customAnswers := make([]string, 0, len(answer.Selected))
	for _, sel := range answer.Selected {
		displaySel := sanitizeToolDisplayText(sel)
		if _, ok := labels[displaySel]; ok {
			selectedOptions[displaySel] = struct{}{}
			continue
		}
		customAnswers = append(customAnswers, displaySel)
	}
	return selectedOptions, customAnswers
}

// renderCancelCall renders a Cancel tool call with semantic display.
// Collapsed view shows the target in its readable short form, the reason (if
// any) and the result status; expanded view renders structured detail rows
// that avoid raw JSON but echo the handle's machine-readable values in full.
func (b *Block) renderCancelCall(width int, spinnerFrame string) []string {
	metrics := newToolCardMetrics(width)
	blockStyle := metrics.blockStyle
	toolCardBg := metrics.toolCardBg
	cardWidth := metrics.cardWidth
	contentWidth := metrics.contentWidth

	args := parseCancelToolArgs(b.Content)
	prefix := b.renderToolPrefix(spinnerFrame)

	// Two-layer task-handle contract shared by the task cards. Context titles
	// and collapsed summaries name a task in its readable short form
	// (extractReadableTarget semantics: "adhoc-7" renders as "#7"; a bare
	// plan-task reference stays a bare number) so a compact line never
	// exposes the internal handle. Expanded field rows echo the
	// machine-readable values in full, task_id included. The header is this
	// card's context title — the cancelled task in that short form plus the
	// reason's first sentence, the same shape delete uses for its own reason.
	target := extractReadableTarget(args.TargetTaskID)
	if target == "" {
		target = "unknown"
	}

	var result []string
	headerLine := renderToolHeaderLine(prefix, b.ToolName) + " " + target
	if reason := toolHeaderProseSummary(args.Reason); reason != "" {
		headerLine = appendToolHeaderSummary(headerLine, "", "("+reason+")", "", cardWidth-4)
	}
	headerLine = buildToolHeaderLine(headerLine, b.ToolProgress, cardWidth, b.toolExecutionIsQueued() && b.ToolQueuedByExecutionEvent, b.toolExecutionIsRunning())
	result = append(result, headerLine)

	if b.Collapsed {
		if summary := formatToolResultSummaryLine(b); summary != "" {
			result = append(result, toolSummaryLine(summary))
		}
		appendToolOutcome(&result, b, contentWidth, false)
	} else {
		if args.Reason != "" {
			result = append(result, toolFieldSection(ToolResultExpandedStyle, "Reason"))
			for _, line := range wrapText(sanitizeToolDisplayText(args.Reason), contentWidth) {
				result = append(result, DimStyle.Render("    "+line))
			}
		}
		var handle tools.TaskHandle
		handleOK := false
		if b.ResultContent != "" {
			handle, _, handleOK = parseTaskToolHandle(b.ResultContent)
		}
		// The top-level "↳ Status:" summary is the compact status; when the
		// Result section below already renders the handle's own status field,
		// showing both would print the state twice. Only an expanded card
		// without a structured handle keeps the summary row.
		if !handleOK || handle.Status == "" {
			if summary := formatToolResultSummaryLine(b); summary != "" {
				result = append(result, toolSummaryLine(summary))
			}
		}
		if handleOK {
			// Expanded field layer: echo the machine-readable handle values in
			// full. The task_id row keeps its internal "adhoc-" prefix — the
			// readable "#N" form is reserved for the context-title layer (see
			// the header comment above).
			result = append(result, toolFieldSection(ToolResultExpandedStyle, "Result"))
			if handle.Status != "" {
				result = append(result, toolFieldNestedInline(DimStyle, toolArgSectionLabel("status"), sanitizeToolDisplayText(handle.Status)))
			}
			if handle.TaskID != "" {
				result = append(result, toolFieldNestedInline(DimStyle, toolArgSectionLabel("task_id"), sanitizeToolDisplayText(handle.TaskID)))
			}
			if handle.AgentID != "" {
				result = append(result, toolFieldNestedInline(DimStyle, toolArgSectionLabel("agent_id"), sanitizeToolDisplayText(handle.AgentID)))
			}
			if handle.Message != "" {
				result = append(result, toolFieldNestedInline(DimStyle, toolArgSectionLabel("message"), sanitizeToolDisplayText(handle.Message)))
			}
		} else if !b.toolResultIsError() && !b.toolResultIsCancelled() {
			result = append(result, toolFieldSection(ToolResultExpandedStyle, "Result"))
			for _, line := range wrapText(sanitizeToolDisplayText(strings.TrimSpace(b.ResultContent)), contentWidth) {
				result = append(result, DimStyle.Render("    "+line))
			}
		}
		appendToolOutcome(&result, b, contentWidth, true)
		if strings.TrimSpace(b.DoneSummary) != "" {
			result = append(result, toolFieldSection(ToolResultExpandedStyle, "Completed"))
			for _, line := range wrapText(sanitizeToolDisplayText(b.DoneSummary), contentWidth) {
				result = append(result, DimStyle.Render("    "+line))
			}
		}
	}
	result = appendToolElapsedToHeader(result, b, cardWidth)
	return b.renderToolCardWithIgnoredArgs(blockStyle, cardWidth, toolCardTitle("TOOL CALL", b.displayLabelID()), result, toolCardBg, railANSISeq("tool", b.Focused))
}

// renderNotifyCall renders a Notify tool call with semantic display.
// Collapsed view shows: target (readable), kind (if any), message summary, result status.
// Expanded view shows more structured details but avoids raw JSON.
func (b *Block) renderNotifyCall(width int, spinnerFrame string) []string {
	metrics := newToolCardMetrics(width)
	blockStyle := metrics.blockStyle
	toolCardBg := metrics.toolCardBg
	cardWidth := metrics.cardWidth
	contentWidth := metrics.contentWidth

	args := parseNotifyToolArgs(b.Content)
	prefix := b.renderToolPrefix(spinnerFrame)
	isActive := b.toolExecutionIsRunning() && spinnerFrame != ""

	target := extractReadableTarget(args.TargetTaskID)

	var result []string
	// The notification card is always expanded behind a bare tool-name
	// header: the message it would summarize is the body's ↳ Message:
	// section, and a target/kind prefix would be the one fact the body
	// never repeats.
	headerLine := renderToolHeaderLine(prefix, b.ToolName)
	headerLine = buildToolHeaderLine(headerLine, b.ToolProgress, cardWidth, b.toolExecutionIsQueued() && b.ToolQueuedByExecutionEvent, isActive)
	result = append(result, headerLine)

	// Target names the referenced task in its readable short form
	// (extractReadableTarget) — the context-title layer of the two-layer
	// contract, the same role a Cancel header plays. Raw handle fields
	// rendered below carry their full machine-readable values.
	if target != "" {
		result = append(result, toolFieldInline(ToolResultExpandedStyle, "Target", sanitizeToolDisplayText(target)))
	}
	if args.Kind != "" {
		result = append(result, toolFieldInline(ToolResultExpandedStyle, "Kind", sanitizeToolDisplayText(args.Kind)))
	}
	if args.Message != "" {
		result = append(result, toolFieldSection(ToolResultExpandedStyle, "Message"))
		for _, line := range wrapText(sanitizeToolDisplayText(args.Message), contentWidth) {
			result = append(result, toolFieldBody(DimStyle, line))
		}
	}
	if summary := formatToolResultSummaryLine(b); summary != "" {
		result = append(result, toolSummaryLine(summary))
	}
	if b.ResultContent != "" {
		handle, _, ok := parseTaskToolHandle(b.ResultContent)
		if ok {
			result = append(result, toolFieldSection(ToolResultExpandedStyle, "Result"))
			if handle.Status != "" {
				result = append(result, toolFieldNestedInline(DimStyle, toolArgSectionLabel("status"), sanitizeToolDisplayText(handle.Status)))
			}
			// The Target row above already names the task in its readable
			// short form, so echoing the handle's own task_id under Result
			// would only repeat it. The rows that do render carry full
			// machine-readable values, matching the other expanded handle
			// fields.
			if handle.AgentID != "" {
				result = append(result, toolFieldNestedInline(DimStyle, toolArgSectionLabel("agent_id"), sanitizeToolDisplayText(handle.AgentID)))
			}
			if handle.Message != "" {
				result = append(result, toolFieldNestedInline(DimStyle, toolArgSectionLabel("message"), sanitizeToolDisplayText(handle.Message)))
			}
		} else if !b.toolResultIsError() && !b.toolResultIsCancelled() {
			result = append(result, toolFieldSection(ToolResultExpandedStyle, "Result"))
			for _, line := range wrapText(sanitizeToolDisplayText(strings.TrimSpace(b.ResultContent)), contentWidth) {
				result = append(result, toolFieldBody(DimStyle, line))
			}
		}
	}
	appendToolOutcome(&result, b, contentWidth, true)
	if strings.TrimSpace(b.DoneSummary) != "" {
		result = append(result, toolFieldSection(ToolResultExpandedStyle, "Completed"))
		for _, line := range wrapText(sanitizeToolDisplayText(b.DoneSummary), contentWidth) {
			result = append(result, toolFieldBody(DimStyle, line))
		}
	}
	result = appendToolElapsedToHeader(result, b, cardWidth)
	return b.renderToolCardWithIgnoredArgs(blockStyle, cardWidth, toolCardTitle("TOOL CALL", b.displayLabelID()), result, toolCardBg, railANSISeq("tool", b.Focused))
}

// appendTaskHandleFieldRows renders a parsed delegate/task handle as a
// column of nested field rows under a "Worker" section header. Every
// non-empty field gets its own row, ordered for scannability: identity
// (status/agent_id/task_id), resumption hints (previous_agent_id/
// rehydrated), plan metadata, write scope, the runtime message, and
// finally any conflict/duplicate warnings with their suggested fix.
func appendTaskHandleFieldRows(out *[]string, h tools.TaskHandle) {
	add := func(key, value string) {
		if value == "" {
			return
		}
		*out = append(*out, toolFieldNestedInline(DimStyle, toolArgSectionLabel(key), sanitizeToolDisplayText(value)))
	}
	add("status", h.Status)
	add("agent_id", h.AgentID)
	if h.TaskID != "" {
		// Expanded field layer: the machine-readable task_id renders in full,
		// adhoc- prefix included. The readable "#N" short form belongs to
		// context titles and compact summaries, not to handle echo rows.
		add("task_id", h.TaskID)
	}
	add("previous_agent_id", h.PreviousAgentID)
	if h.Rehydrated {
		*out = append(*out, toolFieldNestedInline(DimStyle, toolArgSectionLabel("rehydrated"), "true"))
	}
	add("plan_task_ref", h.PlanTaskRef)
	add("semantic_task_key", h.SemanticTaskKey)
	if summary := formatWriteScopeSummary(h.ExpectedWriteScope); summary != "" {
		*out = append(*out, toolFieldNestedInline(DimStyle, toolArgSectionLabel("expected_write_scope"), summary))
	}
	add("message", h.Message)
	if h.ScopeConflict {
		*out = append(*out, toolFieldNestedInline(DimStyle, toolArgSectionLabel("scope_conflict"), "true"))
	}
	if h.DuplicateDetected {
		*out = append(*out, toolFieldNestedInline(DimStyle, toolArgSectionLabel("duplicate_detected"), "true"))
	}
	add("suggested_task_id", h.SuggestedTaskID)
	add("suggested_agent_id", h.SuggestedAgentID)
	add("suggested_action", h.SuggestedAction)
}

// formatWriteScopeSummary condenses a WriteScope declaration into a single
// compact value for one field row, e.g.
// "files=[a.go], path_prefix=[internal/agent]". An all-empty scope returns ""
// (the caller filters that case out before calling).
func formatWriteScopeSummary(s tools.WriteScope) string {
	var parts []string
	if len(s.Files) > 0 {
		parts = append(parts, "files=["+strings.Join(s.Files, ", ")+"]")
	}
	if len(s.PathPrefix) > 0 {
		parts = append(parts, "path_prefix=["+strings.Join(s.PathPrefix, ", ")+"]")
	}
	if len(s.Modules) > 0 {
		parts = append(parts, "modules=["+strings.Join(s.Modules, ", ")+"]")
	}
	return strings.Join(parts, ", ")
}
