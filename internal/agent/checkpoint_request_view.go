package agent

import (
	"slices"
	"strings"

	"github.com/keakon/chord/internal/agent/checkpointfmt"
	"github.com/keakon/chord/internal/agent/requesttrim"
	"github.com/keakon/chord/internal/message"
)

// projectCheckpointRequests changes only request-local copies. The durable
// checkpoint remains the source for typed carry, resume and evidence recovery.
func projectCheckpointRequests(messages []message.Message) []message.Message {
	out := messages
	copied := false
	for i, msg := range messages {
		if msg.Role != message.RoleUser || !msg.IsCompactionSummary || len(msg.Parts) != 0 {
			continue
		}
		var body string
		if msg.CompactionSummaryMode == compactionSummaryModeModelDriven {
			body = checkpointRequestBody(msg.Content, msg.CompactionRequestSources)
		} else {
			body = deduplicateCheckpointRequest(msg.Content, msg.CompactionRequestSources)
		}
		if body == msg.Content {
			continue
		}
		if !copied {
			out = slices.Clone(messages)
			copied = true
		}
		out[i].Content = body
	}
	return out
}

func checkpointRequestBody(body string, sources map[string]string) string {
	// Generic or incomplete recovery summaries can carry facts only in JSON.
	for _, heading := range []string{checkpointfmt.ProgressHeading, checkpointfmt.KeyDecisionsHeading, checkpointfmt.OpenProblemsHeading} {
		if checkpointfmt.FindHeading(body, heading) < 0 {
			return deduplicateCheckpointRequest(body, sources)
		}
	}
	// Remove only a parseable machine payload; preserve any carry-loss notices
	// following it, and leave malformed blocks visible for recovery.
	for _, r := range slices.Backward(typedStateSectionRanges(body)) {
		section := body[r.headingStart:r.contentEnd]
		if _, found, malformed := typedStateFromBody(section); !found || malformed {
			continue
		}
		content := body[r.contentStart:r.contentEnd]
		payload := typedStateJSONLine(content)
		if payload == "" {
			continue
		}
		lines := strings.Split(content, "\n")
		for i, line := range lines {
			if strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "-")) != payload {
				continue
			}
			rest := strings.TrimSpace(strings.Join(lines[i+1:], "\n"))
			if rest != "" {
				rest = "## Checkpoint Carry Notices\n" + rest + "\n\n"
			}
			body = body[:r.headingStart] + rest + body[r.contentEnd:]
			break
		}
	}
	return deduplicateCheckpointRequest(body, sources)
}

const checkpointSameRequestPointer = "See Current User Request."

// checkpointOriginalRequestPointer names the anchors' Original request line,
// which is the canonical copy of the excerpt folded out of an evidence row.
const checkpointOriginalRequestPointer = "See Original request."

// Request-local folding removes repeated instruction blocks altogether.
// Evidence rows retain their classification and ID, with a short excerpt
// reference. Full-source provenance is required for every lossy rendering.
func deduplicateCheckpointRequest(body string, sources map[string]string) string {
	request, ok := checkpointRequestText(body)
	if !ok {
		return body
	}
	body = deduplicateRetainedRecentRequest(body, request)
	source := checkpointRequestSource(request)
	body = deduplicateAnchoredRequest(body, request, source, sources)
	body = deduplicateUserConstraintRequest(body, request, source, sources)
	body = deduplicateEvidenceRequestExcerpt(body, request, source, sources)
	return deduplicateOriginalRequestEvidenceExcerpt(body, source, sources)
}

func checkpointRequestText(body string) (string, bool) {
	section, ok := checkpointfmt.Section(body, checkpointfmt.CurrentUserRequestHeading)
	if !ok || strings.Contains(section, "\n"+truncatedRequestNotice) {
		return "", false
	}
	// An inherited complete request remains an authoritative copy; verify its
	// canonical rendering before folding any other copy into it.
	canonical := section
	const inheritedPrefix = "- " + inheritedCheckpointLabel + ": "
	if after, ok := strings.CutPrefix(canonical, inheritedPrefix); ok {
		canonical = "- " + after
	}
	const prefix = "- " + latestUserRequestLabel + ": "
	if !strings.HasPrefix(canonical, prefix) {
		return "", false
	}
	request := strings.ReplaceAll(strings.TrimPrefix(canonical, prefix), "\n  ", "\n")
	if canonical != modelDrivenCurrentUserRequestSection(fallbackAnchor{Kind: "user_request", Label: latestUserRequestLabel, Text: request}) {
		return "", false
	}
	return request, true
}

func deduplicateRetainedRecentRequest(body, request string) string {
	start := checkpointfmt.FindHeading(body, retainedRecentMessagesHeading)
	if start < 0 {
		return body
	}
	end := len(body)
	if n := strings.Index(body[start:], "\n"+message.CompactionEvidenceTag); n >= 0 {
		end = start + n
	}
	retained := body[start:end]
	quote := "\n" + retainedUserLabel + ":\n> " + strings.ReplaceAll(request, "\n", "\n> ")
	for offset := 0; offset < len(retained); {
		pos := strings.Index(retained[offset:], quote)
		if pos < 0 {
			break
		}
		pos += offset
		after := pos + len(quote)
		suffix := retained[after:]
		if suffix != "" && !strings.HasPrefix(suffix, "\n\n") && suffix != "\n" {
			offset = after
			continue
		}
		retained = retained[:pos] + suffix
		offset = pos
	}
	if strings.TrimSpace(strings.TrimPrefix(retained, retainedRecentMessagesHeading)) == "" {
		return body[:start] + body[end:]
	}
	return body[:start] + retained + body[end:]
}

func deduplicateAnchoredRequest(body, request, source string, sources map[string]string) string {
	open := strings.Index(body, message.CompactionAnchorsOpenTag)
	if open < 0 {
		return body
	}
	start := open + len(message.CompactionAnchorsOpenTag)
	close := strings.Index(body[start:], message.CompactionAnchorsCloseTag)
	if close < 0 {
		return body
	}
	end := start + close
	anchors := parseCompactionAnchors(body[start:end])
	changed := false
	if sources[checkpointOriginalRequestSource] == source && anchors.OriginalRequest == message.CompactionAnchorLine(request, compactAnchorsRequestChars) {
		anchors.OriginalRequest = ""
		changed = true
	}
	line := message.CompactionAnchorLine(requesttrim.TextSnippet(request, compactUserConstraintExcerptChars), compactAnchorsConstraintChars)
	anchors.Constraints = slices.DeleteFunc(anchors.Constraints, func(constraint string) bool {
		if constraint != line || sources[checkpointConstraintSourceKey(constraint)] != source {
			return false
		}
		changed = true
		return true
	})
	if !changed {
		return body
	}
	if anchors.empty() && anchors.OmittedNote == "" {
		return body[:open] + body[end+len(message.CompactionAnchorsCloseTag):]
	}
	return body[:start] + renderCompactionAnchors(anchors) + body[end:]
}

func deduplicateUserConstraintRequest(body, request, source string, sources map[string]string) string {
	start, end, ok := checkpointfmt.SectionBounds(body, checkpointfmt.UserConstraintsHeading)
	if !ok {
		return body
	}
	for _, marker := range []string{message.CompactionCompressedTag, message.CompactionEvidenceTag} {
		if pos := strings.Index(body[start:end], marker); pos >= 0 {
			end = start + pos
		}
	}
	excerpt := strings.ReplaceAll(requesttrim.TextSnippet(request, compactUserConstraintExcerptChars), "\n", " ")
	if sources[checkpointUserConstraintSourceKey(excerpt)] != source {
		return body
	}
	lines := strings.Split(body[start:end], "\n")
	lines = slices.DeleteFunc(lines, func(line string) bool { return strings.TrimSpace(line) == "- "+excerpt })
	content := strings.Join(lines, "\n")
	if strings.TrimSpace(content) == "" {
		start = checkpointfmt.FindHeading(body, checkpointfmt.UserConstraintsHeading)
	}
	return body[:start] + content + body[end:]
}

// Evidence rewriting is confined to complete, runtime-rendered instruction
// rows in the evidence region. Matching quoted text elsewhere is untouched.
func deduplicateEvidenceRequestExcerpt(body, request, source string, sources map[string]string) string {
	var excerpt strings.Builder
	excerpt.WriteString("Excerpt:\n")
	writeEvidenceExcerptBlock(&excerpt, requesttrim.TextSnippet(request, compactUserConstraintExcerptChars))
	rendered := strings.TrimSuffix(excerpt.String(), "\n")
	return rewriteCheckpointEvidenceRows(body, func(row, id string) (string, bool) {
		if sources[checkpointEvidenceSourcePrefix+id] != source || !instructionEvidenceRow(row) {
			return row, false
		}
		p := strings.Index(row, "\n"+rendered)
		if p < 0 {
			return row, false
		}
		after := p + 1 + len(rendered)
		if after != len(row) && row[after] != '\n' {
			return row, false
		}
		replacement := "Excerpt: " + checkpointSameRequestPointer
		if len(replacement) >= len(rendered) {
			return row, false
		}
		return row[:p+1] + replacement + row[after:], true
	})
}

// deduplicateOriginalRequestEvidenceExcerpt folds the verbatim excerpt of the
// session's original request into a pointer when the request is already carried
// as the anchors' Original request line. The row keeps its ID, classification,
// and recovery pointers; only the second truncated copy of the same message is
// dropped. Rows from the current request are left to
// deduplicateEvidenceRequestExcerpt, whose pointer names the section that
// actually survives (the anchors' Original request line is folded away when the
// current request is the same message).
func deduplicateOriginalRequestEvidenceExcerpt(body, currentSource string, sources map[string]string) string {
	originalSource := sources[checkpointOriginalRequestSource]
	if originalSource == "" || originalSource == currentSource {
		return body
	}
	return rewriteCheckpointEvidenceRows(body, func(row, id string) (string, bool) {
		if sources[checkpointEvidenceSourcePrefix+id] != originalSource || !instructionEvidenceRow(row) {
			return row, false
		}
		return foldFencedEvidenceExcerpt(row, "Excerpt: "+checkpointOriginalRequestPointer)
	})
}

// rewriteCheckpointEvidenceRows rewrites each evidence row of the
// [Context Evidence] region. rewrite reports false to leave a row untouched.
func rewriteCheckpointEvidenceRows(body string, rewrite func(row, id string) (string, bool)) string {
	start := strings.Index(body, message.CompactionEvidenceTag)
	if start < 0 {
		return body
	}
	region := body[start:]
	var rows []int
	for offset := 0; offset < len(region); {
		pos := strings.Index(region[offset:], "\nEvidence ID: ")
		if pos < 0 {
			break
		}
		offset += pos + 1
		rows = append(rows, offset)
	}
	if len(rows) == 0 {
		return body
	}
	var out strings.Builder
	out.WriteString(region[:rows[0]])
	for i, pos := range rows {
		end := len(region)
		if i+1 < len(rows) {
			end = rows[i+1]
		}
		row := region[pos:end]
		idLine, _, _ := strings.Cut(row, "\n")
		id := strings.TrimPrefix(idLine, "Evidence ID: ")
		if rewritten, changed := rewrite(row, id); changed {
			row = rewritten
		}
		out.WriteString(row)
	}
	return body[:start] + out.String()
}

// foldFencedEvidenceExcerpt replaces the fenced excerpt block that ends an
// evidence row with a shorter pointer. Every excerpt is rendered as the row's
// last field with a column-0 backtick fence chosen longer than any run inside
// it, so the first line carrying the same fence closes the block; whatever
// follows belongs to later rows and is preserved.
func foldFencedEvidenceExcerpt(row, pointer string) (string, bool) {
	const prefix = "\nExcerpt:\n"
	start := strings.Index(row, prefix)
	if start < 0 {
		return row, false
	}
	block := row[start+len(prefix):]
	lineEnd := strings.Index(block, "\n")
	if lineEnd < 0 {
		return row, false
	}
	fence := strings.TrimSuffix(block[:lineEnd], "text")
	if fence == "" || strings.Trim(fence, "`") != "" {
		return row, false
	}
	closeAt := strings.Index(block[lineEnd+1:], "\n"+fence+"\n")
	if closeAt < 0 {
		return row, false
	}
	after := lineEnd + 1 + closeAt + len("\n"+fence+"\n")
	if len(pointer) >= len(prefix)+after {
		return row, false
	}
	return row[:start+1] + pointer + row[start+len(prefix)+after:], true
}

func instructionEvidenceRow(row string) bool {
	metadata, _, _ := strings.Cut(row, "\nExcerpt:")
	for line := range strings.SplitSeq(metadata, "\n") {
		if kind, ok := strings.CutPrefix(line, "Evidence Kind: "); ok {
			return isAnchorConstraintKind(evidenceKind(kind))
		}
	}
	return false
}
