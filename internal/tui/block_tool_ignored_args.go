package tui

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/mattn/go-runewidth"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func (b *Block) renderToolCardWithIgnoredArgs(style lipgloss.Style, cardWidth int, title string, body []string, bgColorNum string, railSeq string) []string {
	body = b.appendToolArgDiagnostics(body, max(cardWidth-4, 10))
	return renderPrewrappedToolCard(style, cardWidth, title, body, bgColorNum, railSeq)
}

type toolArgDiagnostic struct {
	path    string
	value   string
	ignored bool
	missing bool
}

func (b *Block) toolArgDiagnostics() []toolArgDiagnostic {
	if b == nil || b.Audit == nil {
		return nil
	}
	var diagnostics []toolArgDiagnostic
	for _, item := range b.Audit.IgnoredArgs {
		path := strings.TrimPrefix(strings.TrimSpace(item.Path), "args.")
		if path == "" {
			continue
		}
		diagnostics = append(diagnostics, toolArgDiagnostic{
			path:    sanitizeToolDisplayText(path),
			value:   b.toolArgDiagnosticValue(path, item.ValueJSON),
			ignored: true,
		})
	}
	for _, item := range b.Audit.InvalidArgs {
		path := strings.TrimPrefix(strings.TrimSpace(item.Path), "args.")
		if path == "" {
			continue
		}
		diagnostics = append(diagnostics, toolArgDiagnostic{
			path:    sanitizeToolDisplayText(path),
			value:   b.toolArgDiagnosticValue(path, item.ValueJSON),
			missing: item.Reason == message.InvalidToolArgReasonMissing,
		})
	}
	return diagnostics
}

func (b *Block) toolArgDiagnosticValue(path, valueJSON string) string {
	canonical := strings.TrimLeft(strings.TrimSpace(path), ".")
	switch b.ToolName {
	case "glob":
		if canonical == "patterns" {
			if values := paramStringList(valueJSON); len(values) > 0 {
				return truncateToolParamValue(sanitizeToolDisplayText(formatStringListParam(values)))
			}
		}
	case "grep":
		// "patterns" is the plural a model reaches for by analogy with glob.
		// Render it like the other list-valued grep arguments rather than as raw
		// JSON, so the discarded value reads the same as paths=/includes=.
		if canonical == "paths" || canonical == "includes" || canonical == "patterns" {
			if values := paramStringList(valueJSON); len(values) > 0 {
				if canonical == "paths" {
					for i, value := range values {
						values[i] = b.displayToolDir(value)
					}
				}
				return truncateToolParamValue(sanitizeToolDisplayText(formatStringListParam(values)))
			}
		}
	case "delete":
		if canonical == "paths" {
			if values := paramStringList(valueJSON); len(values) > 0 {
				for i, value := range values {
					values[i] = b.displayToolPath(value)
				}
				return truncateToolParamValue(sanitizeToolDisplayText(formatStringListParam(values)))
			}
		}
	}
	return ignoredToolArgValue(valueJSON)
}

func ignoredToolArgValue(valueJSON string) string {
	value := strings.TrimSpace(valueJSON)
	if value == "" {
		return ""
	}
	dec := json.NewDecoder(strings.NewReader(value))
	dec.UseNumber()
	var parsed any
	if err := dec.Decode(&parsed); err == nil {
		if formatted := formatParamValue(parsed); formatted != "" {
			value = formatted
		}
	}
	return truncateToolParamValue(sanitizeToolDisplayText(value))
}

// stripResultNotes drops the runtime's own note lines from a result before a
// card derives its body from it. Those notes are model-facing — they tell the
// model what the runtime did to its call — and the card surfaces the argument
// facts from the argument audit, so repainting them inside an output section
// would make a diagnostic read as command output.
//
// Matching runs line by line, not just on a trailing suffix: a failed call
// carries "Error: …" after the notes, and for shell the duration note follows
// them, so only a whole-line removal keeps the result body intact. A card
// restored from a transcript without a notes list keeps the text.
func (b *Block) stripResultNotes(content string) string {
	if b == nil || content == "" || len(b.ResultNotes) == 0 {
		return content
	}
	lines := strings.Split(content, "\n")
	out := make([]string, 0, len(lines))
	removed := false
	for _, line := range lines {
		if b.isResultNote(line) {
			removed = true
			continue
		}
		out = append(out, line)
	}
	if !removed {
		return content
	}
	return strings.TrimRight(strings.Join(out, "\n"), "\r\n")
}

// isResultNote reports whether one result line is a recorded runtime note.
// Comparison trims surrounding whitespace so CRLF results match the recorded
// notes the same way LF ones do.
func (b *Block) isResultNote(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return false
	}
	for _, note := range b.ResultNotes {
		if trimmed == strings.TrimSpace(note) {
			return true
		}
	}
	return false
}

// diagnosticBaseText renders the unprefixed text one diagnostic contributes to
// a header: "path" for a flag, "path=value" otherwise, and "path=<missing>"
// when the value never arrived. It is the single source of truth for that
// spelling, because the header restyling path locates the text already rendered
// into the header by string search; a second copy drifting from this one would
// silently stop matching and render the value twice.
func diagnosticBaseText(diagnostic toolArgDiagnostic) string {
	text := diagnostic.path
	if diagnostic.missing {
		text += "=<missing>"
	} else if diagnostic.value != "" {
		text += "=" + diagnostic.value
	}
	return text
}

// diagnosticOptionPlain renders the inline text a header carries for one
// diagnostic. An ignored value keeps an "ignored " prefix so the strikethrough
// is not the only signal that it never reached the tool: the value is something
// the model passed and the runtime dropped, not an option that took effect.
func diagnosticOptionPlain(diagnostic toolArgDiagnostic) string {
	text := diagnosticBaseText(diagnostic)
	if text == "" {
		return ""
	}
	if diagnostic.ignored {
		return "ignored " + text
	}
	return text
}

// diagnosticArgHeaderOption renders the diagnostic option for canonical with
// the same styling contract as formatDiagnosticOption: struck-through for
// ignored values, error-styled for missing ones, plain otherwise.
func (b *Block) diagnosticArgHeaderOption(canonical string) string {
	text, ignored, missing := b.diagnosticArgHeaderItem(canonical)
	if text == "" {
		return ""
	}
	if ignored {
		return DimStyle.Strikethrough(true).Render(text)
	}
	if missing {
		return ErrorStyle.Render(text)
	}
	return text
}

func (b *Block) diagnosticArgHeaderItem(canonical string) (text string, ignored, missing bool) {
	if b == nil || b.Audit == nil {
		return "", false, false
	}
	for _, item := range b.Audit.InvalidArgs {
		if canonicalDiagnosticPath(item.Path) != canonical || item.Reason != message.InvalidToolArgReasonMissing {
			continue
		}
		if text := b.diagnosticArgText(item.Path, item.ValueJSON, canonical); text != "" {
			return text, false, true
		}
	}
	for _, item := range b.Audit.InvalidArgs {
		if canonicalDiagnosticPath(item.Path) != canonical || item.Reason == message.InvalidToolArgReasonMissing {
			continue
		}
		if text := b.diagnosticArgText(item.Path, item.ValueJSON, canonical); text != "" {
			return text, false, false
		}
	}
	for _, item := range b.Audit.IgnoredArgs {
		if text := b.diagnosticArgText(item.Path, item.ValueJSON, canonical); text != "" {
			return text, true, false
		}
	}
	return "", false, false
}

func canonicalDiagnosticPath(rawPath string) string {
	path := sanitizeToolDisplayText(strings.TrimPrefix(strings.TrimSpace(rawPath), "args."))
	for strings.HasPrefix(path, ".") {
		path = strings.TrimPrefix(path, ".")
	}
	return path
}

func (b *Block) diagnosticArgText(rawPath, valueJSON, canonical string) string {
	path := sanitizeToolDisplayText(strings.TrimPrefix(strings.TrimSpace(rawPath), "args."))
	if path == "" || canonicalDiagnosticPath(rawPath) != canonical {
		return ""
	}
	value := b.toolArgDiagnosticValue(path, valueJSON)
	if value == "" && !b.diagnosticArgIsMissing(path) {
		return ""
	}
	if b.diagnosticArgUsesValueOnly(path) {
		return value
	}
	if b.diagnosticArgIsMissing(path) {
		return "<missing>"
	}
	return path + "=" + value
}

func (b *Block) diagnosticArgIsMissing(path string) bool {
	if b == nil || b.Audit == nil {
		return false
	}
	canonical := strings.TrimLeft(strings.TrimSpace(path), ".")
	for _, item := range b.Audit.InvalidArgs {
		if canonicalDiagnosticPath(item.Path) != canonical {
			continue
		}
		return item.Reason == message.InvalidToolArgReasonMissing
	}
	return false
}

func (b *Block) diagnosticArgUsesValueOnly(path string) bool {
	canonical := strings.TrimLeft(strings.TrimSpace(path), ".")
	switch b.ToolName {
	case "glob":
		return canonical == "patterns"
	case "grep":
		return canonical == "pattern"
	default:
		return false
	}
}

// globDiagnosticHeaderParts keeps schema-broken glob calls on the same header
// shape as successful ones: the ignored or invalid patterns value takes the
// pattern slot and path stays relativized, so only the diagnostic styling
// differs from a valid call.
func (b *Block) globDiagnosticHeaderParts(vals map[string]string) (mainPart, grayPart string) {
	mainPart = b.diagnosticPrimaryText("patterns")
	if mainPart == "" {
		return "", ""
	}
	var opts []string
	if dir := b.displayToolDir(vals["path"]); dir != "" && dir != "." {
		opts = append(opts, "path="+dir)
	}
	if ignored := b.firstIgnoredDiagnostic("patterns"); ignored != nil && ignored.value != "" {
		opts = append(opts, b.formatDiagnosticOption(ignored))
	}
	if len(opts) == 0 {
		return mainPart, ""
	}
	return mainPart, "(" + strings.Join(opts, ", ") + ")"
}

// grepDiagnosticHeaderParts keeps schema-broken grep calls on the same header
// shape as successful ones: the pattern slot shows the missing marker or the
// effective pattern, while every ignored or invalid argument joins the
// parenthesized option group instead of trailing the header line.
func (b *Block) grepDiagnosticHeaderParts(vals map[string]string) (mainPart, grayPart string) {
	pattern := b.diagnosticPrimaryText("pattern")
	if pattern == "" {
		pattern = strings.TrimSpace(vals["pattern"])
	}
	if pattern == "" {
		return "", ""
	}
	var opts []string
	if paths := nonCurrentDirToolPaths(vals["paths"]); len(paths) > 0 {
		opts = append(opts, "paths="+formatStringListParam(paths))
	}
	if paths := b.diagnosticArgHeaderOption("paths"); paths != "" {
		opts = append(opts, paths)
	}
	if includes := paramStringList(vals["includes"]); len(includes) > 0 {
		opts = append(opts, "includes="+formatStringListParam(includes))
	}
	if includes := b.diagnosticArgHeaderOption("includes"); includes != "" {
		opts = append(opts, includes)
	}
	if path := strings.TrimSpace(vals["path"]); path != "" && path != "." {
		opts = append(opts, "path="+b.displayToolDir(path))
	}
	if ignored := b.firstIgnoredDiagnostic("pattern"); ignored != nil && ignored.value != "" {
		opts = append(opts, b.formatDiagnosticOption(ignored))
	}
	// Models routinely pluralize the singular "pattern" schema field the way
	// glob spells it. Without this the discarded value trails the header behind
	// a " · " separator, so a broken call no longer looks like a valid one.
	if ignored := b.firstIgnoredDiagnostic("patterns"); ignored != nil && ignored.value != "" {
		opts = append(opts, b.formatDiagnosticOption(ignored))
	}
	if len(opts) == 0 {
		return pattern, ""
	}
	return pattern, "(" + strings.Join(opts, ", ") + ")"
}

func (b *Block) diagnosticPrimaryText(canonical string) string {
	if b == nil || b.Audit == nil {
		return ""
	}
	for _, item := range b.Audit.InvalidArgs {
		if canonicalDiagnosticPath(item.Path) != canonical || item.Reason != message.InvalidToolArgReasonMissing {
			continue
		}
		return "<missing>"
	}
	return ""
}

func (b *Block) firstIgnoredDiagnostic(canonical string) *toolArgDiagnostic {
	if b == nil || b.Audit == nil {
		return nil
	}
	for _, item := range b.Audit.IgnoredArgs {
		if canonicalDiagnosticPath(item.Path) != canonical {
			continue
		}
		path := sanitizeToolDisplayText(strings.TrimPrefix(strings.TrimSpace(item.Path), "args."))
		if path == "" {
			continue
		}
		return &toolArgDiagnostic{path: path, value: b.toolArgDiagnosticValue(path, item.ValueJSON), ignored: true}
	}
	return nil
}

func (b *Block) formatDiagnosticOption(diagnostic *toolArgDiagnostic) string {
	if diagnostic == nil {
		return ""
	}
	text := diagnosticOptionPlain(*diagnostic)
	if text == "" {
		return ""
	}
	if diagnostic.ignored {
		return DimStyle.Strikethrough(true).Render(text)
	}
	if diagnostic.missing {
		return ErrorStyle.Render(text)
	}
	return text
}

// deleteDiagnosticHeaderParts keeps schema-broken Delete calls on the same
// header shape as successful ones: the missing-paths marker takes the path
// slot while every ignored or invalid argument joins the reason in the
// parenthesized option group, so no diagnostic is silently dropped.
func (b *Block) deleteDiagnosticHeaderParts(vals map[string]string) (mainPart, grayPart string) {
	missing := b.diagnosticPrimaryText("paths")
	diagnostics := b.diagnosticHeaderOptions("paths")
	if missing == "" && len(diagnostics) == 0 {
		return "", ""
	}
	if missing != "" {
		mainPart = missing
	} else if filePaths := parseDeleteHeaderPaths(vals); len(filePaths) == 1 {
		mainPart = filePaths[0]
	} else if len(filePaths) > 1 {
		mainPart = fmt.Sprintf("%d files", len(filePaths))
	} else {
		return "", ""
	}
	return mainPart, mergeHeaderOptions(deleteReasonHeaderGray(vals), diagnostics)
}

// bashDiagnosticHeaderParts keeps schema-broken Shell calls on the same header
// shape as successful ones: ignored or invalid extra arguments join the same
// parenthesized option group as timeout, the way glob/grep keep diagnostic
// options beside the normal ones.
func (b *Block) bashDiagnosticHeaderParts(vals map[string]string) (mainPart, grayPart string) {
	if b == nil || b.Audit == nil {
		return "", ""
	}
	if len(b.toolArgDiagnostics()) == 0 {
		return "", ""
	}
	mainPart = bashDescriptionSummary(vals)
	if mainPart == "" {
		mainPart = firstDisplayLine(vals["command"])
	}
	return mainPart, mergeHeaderOptions(bashHeaderGrayPart(vals), b.diagnosticHeaderOptions())
}

// diagnosticHeaderOptions renders every ignored or invalid argument as a
// styled name=value option so tools can fold diagnostics into their header
// option group instead of appending them after the header line. Skip lists
// canonical names whose missing marker the tool already shows in the main
// header slot.
func (b *Block) diagnosticHeaderOptions(skip ...string) []string {
	diagnostics := b.toolArgDiagnostics()
	if len(diagnostics) == 0 {
		return nil
	}
	opts := make([]string, 0, len(diagnostics))
	for i := range diagnostics {
		if slices.Contains(skip, diagnostics[i].path) {
			continue
		}
		if option := b.formatDiagnosticOption(&diagnostics[i]); option != "" {
			opts = append(opts, option)
		}
	}
	return opts
}

// mergeHeaderOptions folds extra styled options into an existing
// parenthesized option group, creating the group when grayPart has none.
func mergeHeaderOptions(grayPart string, extra []string) string {
	if len(extra) == 0 {
		return grayPart
	}
	inner := strings.TrimSuffix(strings.TrimPrefix(grayPart, "("), ")")
	opts := make([]string, 0, len(extra)+1)
	if inner != "" {
		opts = append(opts, inner)
	}
	opts = append(opts, extra...)
	return "(" + strings.Join(opts, ", ") + ")"
}

// headerParamSummaryKeys drops generic-summary keys already covered by an arg
// diagnostic, unless the summary text matches the diagnostic text verbatim
// and will be restyled in place; compacted forms like "[2 items]" never match,
// so without this the full value would render a second time.
func (b *Block) headerParamSummaryKeys(keys []string, vals map[string]string) []string {
	diagnostics := b.toolArgDiagnostics()
	if len(diagnostics) == 0 {
		return keys
	}
	plainByKey := make(map[string]string, len(diagnostics))
	for _, diagnostic := range diagnostics {
		if b.diagnosticArgOccupiesHeader(diagnostic) {
			continue
		}
		plainByKey[diagnostic.path] = diagnosticBaseText(diagnostic)
	}
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		if plain, covered := plainByKey[key]; covered && key+"="+genericToolParamValue(vals[key]) != plain {
			continue
		}
		out = append(out, key)
	}
	return out
}

func (b *Block) appendToolArgDiagnostics(body []string, contentWidth int) []string {
	if len(body) == 0 {
		return body
	}
	diagnostics := b.toolArgDiagnostics()
	if len(diagnostics) == 0 {
		return body
	}
	header := body[0]
	baseWidth := tuiStringWidth(stripANSI(header))
	remaining := max(contentWidth-baseWidth-1, 0)
	appended := 0
	for _, diagnostic := range diagnostics {
		if b.diagnosticArgOccupiesHeader(diagnostic) {
			continue
		}
		plain := diagnosticBaseText(diagnostic)
		text := diagnosticOptionPlain(diagnostic)
		if text == "" {
			continue
		}
		if !diagnostic.missing && strings.Contains(stripANSI(header), plain) {
			style := diagnosticOptionStyle(diagnostic)
			header = strings.Replace(header, plain, style.Render(text), 1)
			continue
		}
		separator := " · "
		partWidth := runewidth.StringWidth(separator + text)
		if partWidth > remaining {
			if appended == 0 && remaining > runewidth.StringWidth(separator)+1 {
				text = runewidth.Truncate(text, remaining-runewidth.StringWidth(separator), "…")
			} else {
				break
			}
		}
		style := diagnosticOptionStyle(diagnostic)
		if appended == 0 && remaining <= 0 {
			break
		}
		header += separator + style.Render(text)
		remaining -= runewidth.StringWidth(separator + text)
		appended++
		if remaining <= 0 {
			break
		}
	}
	body[0] = header
	return body
}

// diagnosticOptionStyle picks the style the header audit slot renders one
// diagnostic in: an ignored value is struck through, because the runtime dropped
// it and it should read as absent rather than wrong, and everything else is an
// error on the call. Callers must already have routed diagnostic.missing
// elsewhere — that branch has its own text and never reaches this one.
//
// It deliberately differs from formatDiagnosticOption, which renders a
// neither-ignored-nor-missing diagnostic unstyled; both keep their behavior.
func diagnosticOptionStyle(diagnostic toolArgDiagnostic) lipgloss.Style {
	if diagnostic.ignored {
		return DimStyle.Strikethrough(true)
	}
	return ErrorStyle
}

// diagnosticArgOccupiesHeader reports whether the tool already accounts for
// this diagnostic somewhere other than the shared header-suffix slot, so
// appendToolArgDiagnostics must not append it a second time.
func (b *Block) diagnosticArgOccupiesHeader(diagnostic toolArgDiagnostic) bool {
	path := diagnostic.path
	canonical := strings.TrimLeft(strings.TrimSpace(path), ".")
	switch b.ToolName {
	case tools.NameGlob:
		return canonical == "patterns"
	case tools.NameGrep:
		// "patterns" is the plural a model reaches for by analogy with glob;
		// grepDiagnosticHeaderParts folds it into the option group, so the
		// shared header-suffix slot must not repeat it.
		return canonical == "pattern" || canonical == "patterns" ||
			canonical == "paths" || canonical == "includes"
	case tools.NameQuestion:
		// A Question card is all body: it renders every parameter it was given
		// below the header ("▸ <header>", the question text, the option list),
		// and a rejected call carries the runtime's schema message in the
		// ↳ Error block, which already names the offending field and the value
		// it rejected ("args.questions[0].header must be a string, got number
		// 7"). Echoing the argument onto the header line therefore adds nothing
		// while putting a second, competing "header" on screen with no way to
		// tell it apart from the body's. Keep both ignored and invalid args
		// below, where the note or error section explains them.
		return true
	case tools.NameDelete, tools.NameRead, tools.NameWrite, tools.NameEdit, tools.NameApplyPatch,
		tools.NameTodoWrite, tools.NameShell, tools.NameJobOutput, tools.NameJobKill, tools.NameJobList,
		tools.NameWebFetch, tools.NameSkill:
		return true
	default:
		return false
	}
}
