package tui

import (
	"strings"

	"github.com/keakon/chord/internal/message"
)

// A compaction checkpoint carries protocol markers ("[Context Summary]",
// "[Session Anchors]", "[Context compressed]", ...) around Markdown prose.
// Rendering the whole thing as one Markdown document merges every marker into
// the paragraph that follows it, because a bare "[...]" line has no block-level
// Markdown meaning. Splitting on the markers first keeps each region a separate
// Markdown document, so the markers stay on their own lines and the regions
// keep their headings and lists.
//
// The markers are display scaffolding here, not content: the card already shows
// "CONTEXT SUMMARY", so the opening header and the anchor delimiters are dropped
// rather than printed back to the user.

// compactionSection is one renderable region of a checkpoint. An empty label
// renders the body only.
type compactionSection struct {
	label string
	body  string
}

const (
	compactionAnchorsSectionLabel  = "SESSION ANCHORS"
	compactionArchiveSectionLabel  = "ARCHIVED HISTORY"
	compactionEvidenceSectionLabel = "PRESERVED EVIDENCE"
	compactionHintSectionLabel     = "DISPLAY HINT"
)

// splitCompactionSections divides checkpoint content into labelled regions in
// the order they appear. Content that carries no markers returns a single
// unlabelled section, so non-checkpoint text renders exactly as before.
func splitCompactionSections(content string) []compactionSection {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil
	}

	rest := strings.TrimSpace(strings.TrimPrefix(content, strings.TrimRight(message.CompactionSummaryHeader, "\n")))

	var sections []compactionSection
	appendSection := func(label, body string) {
		if body = strings.TrimSpace(body); body != "" {
			sections = append(sections, compactionSection{label: label, body: body})
		}
	}

	if anchors := message.CompactionAnchorsSection(rest); anchors != "" {
		before, after := splitAroundAnchorsBlock(rest)
		appendSection("", before)
		appendSection(compactionAnchorsSectionLabel, anchors)
		rest = after
	}

	// The trailing regions appear in a fixed order, each introduced by its own
	// marker. Cut them off the end first so the leading remainder is the summary
	// body itself.
	hint := ""
	if idx := strings.Index(rest, strings.TrimSpace(message.CompactionDisplayHint)); idx >= 0 {
		hint = rest[idx+len(strings.TrimSpace(message.CompactionDisplayHint)):]
		rest = rest[:idx]
	}
	evidence := ""
	if idx := strings.Index(rest, strings.TrimRight(message.CompactionEvidenceTag, "\n")); idx >= 0 {
		evidence = rest[idx+len(strings.TrimRight(message.CompactionEvidenceTag, "\n")):]
		rest = rest[:idx]
	}
	archive := ""
	if idx := strings.Index(rest, strings.TrimSpace(message.CompactionCompressedTag)); idx >= 0 {
		archive = rest[idx+len(strings.TrimSpace(message.CompactionCompressedTag)):]
		rest = rest[:idx]
	}

	appendSection("", rest)
	appendSection(compactionArchiveSectionLabel, archive)
	appendSection(compactionEvidenceSectionLabel, evidence)
	appendSection(compactionHintSectionLabel, hint)

	if len(sections) == 0 {
		return []compactionSection{{body: content}}
	}
	return sections
}

// splitAroundAnchorsBlock returns the text before the anchors block and the text
// after it, discarding the delimiters themselves.
func splitAroundAnchorsBlock(content string) (before, after string) {
	start := strings.Index(content, message.CompactionAnchorsOpenTag)
	if start < 0 {
		return content, ""
	}
	bodyStart := start + len(message.CompactionAnchorsOpenTag)
	end := strings.Index(content[bodyStart:], message.CompactionAnchorsCloseTag)
	if end < 0 {
		return content, ""
	}
	closeEnd := bodyStart + end + len(message.CompactionAnchorsCloseTag)
	return content[:start], content[closeEnd:]
}
