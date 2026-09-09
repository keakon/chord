package agent

import (
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/keakon/chord/internal/message"
)

// Prior-checkpoint carry-forward.
//
// A compaction checkpoint is the newest message of the session only until the
// next compaction archives it and replaces it with a fresh summary. Recursive
// compaction is therefore a chain of re-summarizations, and every step is a
// fresh chance for the summarizer to drop a structured section the previous
// checkpoint established. The session anchors (original request + standing
// constraints) are already protected by their own verbatim carry; the rest of
// a prior checkpoint's body — the ## sections a model-driven checkpoint
// declared, or the sections an earlier usage-driven summary wrote — had no
// equivalent protection: it only survived if the summarizer happened to keep
// it inside the trim budget and chose to restate it.
//
// The carry below gives the whole prior checkpoint body the same deterministic
// treatment as the anchors. It is (1) extracted from the most recent
// checkpoint inside the archived head before the summary prompt is built,
// (2) surfaced to the summarizer as a protected section it must fold in, and
// (3) appended verbatim to the new checkpoint as a `## Previous Checkpoint`
// section, so the live context after apply always references the previous
// checkpoint's content even when the summarizer merged it imperfectly. The
// extraction strips the anchors block (carried separately) and any earlier
// carry section (so consecutive compactions never compound the carried body),
// and caps the body so a large prior checkpoint cannot bloat the new summary.

const (
	// priorCheckpointSectionHeading is the deterministic carry section appended
	// to the checkpoint body. A later compaction strips it before re-carrying,
	// which keeps the carry bounded at roughly one checkpoint body.
	priorCheckpointSectionHeading = "## Previous Checkpoint"
	// compactCheckpointCarryMaxChars bounds the carried body. The archived
	// history file remains authoritative for the full text.
	compactCheckpointCarryMaxChars = 2400
)

// latestPriorCheckpointStrippedBody returns the durable body of the most
// recent checkpoint in messages, or "" when none exists. The body is the
// checkpoint's content between the [Context Summary] header and the
// [Context compressed] footer, with the [Session Anchors] block removed
// (anchors are carried by their own mechanism), the `## Skills Invoked
// Earlier` section removed (also carried separately, by name merge) and any
// previously appended `## Previous Checkpoint` section removed (the carry must
// not compound across generations). No display truncation is applied here.
func latestPriorCheckpointStrippedBody(messages []message.Message) string {
	for _, msg := range slices.Backward(messages) {
		if msg.Role != message.RoleUser || !msg.IsCompactionSummary {
			continue
		}
		body := compactionSummaryBody(msg.Content)
		body = stripCompactionAnchorsBlock(body)
		body = stripCheckpointSkillsSection(body)
		body = stripPriorCheckpointCarrySection(body)
		return body
	}
	return ""
}

// latestPriorCheckpointBody returns the display-truncated form of
// latestPriorCheckpointStrippedBody, bounded to compactCheckpointCarryMaxChars
// for the natural-language carry sections of the summary prompt and the
// `## Previous Checkpoint` appendix. Callers that need the machine-carryable
// typed state must read the untruncated body instead: the typed JSON line
// sits late in a model-driven body and a long checkpoint would lose it before
// it could be parsed.
func latestPriorCheckpointBody(messages []message.Message) string {
	body := latestPriorCheckpointStrippedBody(messages)
	if body == "" {
		return ""
	}
	return truncateCheckpointCarryLines(body, compactCheckpointCarryMaxChars)
}

func truncateCheckpointCarryLines(body string, maxChars int) string {
	const omitted = "[Earlier checkpoint content omitted; read the archive for the complete record.]"
	body = strings.TrimSpace(body)
	if body == "" || maxChars <= 0 {
		return ""
	}
	if utf8.RuneCountInString(body) <= maxChars {
		return body
	}
	omittedChars := utf8.RuneCountInString(omitted)
	if maxChars < omittedChars {
		return ""
	}
	lines := strings.Split(body, "\n")
	kept := make([]string, 0, len(lines))
	used := 0
	for _, line := range lines {
		lineChars := utf8.RuneCountInString(line)
		separatorChars := 0
		if len(kept) > 0 {
			separatorChars = 1
		}
		const omissionSeparatorChars = 1
		if used+separatorChars+lineChars+omissionSeparatorChars+omittedChars > maxChars {
			break
		}
		kept = append(kept, line)
		used += separatorChars + lineChars
	}
	if len(kept) == 0 {
		return omitted
	}
	return strings.TrimSpace(strings.Join(kept, "\n")) + "\n" + omitted
}

// stripCompactionAnchorsBlock removes the verbatim [Session Anchors] block from
// a checkpoint body. Anchors survive recursion through buildCompactionAnchors /
// withCompactionAnchors, so re-carrying them inside the body would duplicate
// the block in the new checkpoint.
func stripCompactionAnchorsBlock(body string) string {
	start := strings.Index(body, message.CompactionAnchorsOpenTag)
	if start < 0 {
		return strings.TrimSpace(body)
	}
	end := strings.Index(body[start:], message.CompactionAnchorsCloseTag)
	if end < 0 {
		return strings.TrimSpace(body)
	}
	end += start + len(message.CompactionAnchorsCloseTag)
	prefix := strings.TrimSpace(body[:start])
	rest := strings.TrimSpace(body[end:])
	switch {
	case prefix == "":
		return rest
	case rest == "":
		return prefix
	default:
		return prefix + "\n\n" + rest
	}
}

// stripPriorCheckpointCarrySection removes a `## Previous Checkpoint` section
// previously appended to a checkpoint body, so the carry stays bounded at one
// checkpoint body instead of compounding the whole history of carried blocks.
func stripPriorCheckpointCarrySection(body string) string {
	idx := strings.LastIndex(body, "\n"+priorCheckpointSectionHeading+"\n")
	if idx < 0 {
		return strings.TrimSpace(body)
	}
	return strings.TrimSpace(body[:idx])
}

// appendPriorCheckpointCarry appends the carried body as the checkpoint's
// final section. It is a no-op when there is nothing to carry or the body
// already carries the section (the summarizer produced one, or the append ran
// twice on the same draft path).
func appendPriorCheckpointCarry(summary, carry string) string {
	carry = strings.TrimSpace(carry)
	summary = strings.TrimSpace(summary)
	if carry == "" || summary == "" {
		return summary
	}
	if strings.Contains(summary, "\n"+priorCheckpointSectionHeading+"\n") {
		return summary
	}
	return summary + "\n\n" + priorCheckpointSectionHeading + "\n" + carry
}

// formatPriorCheckpointCarryForPrompt renders the carried body for the
// summarizer prompt. The fold instruction itself lives in
// buildCompactionPromptWithKeyFiles; this only carries the data.
func formatPriorCheckpointCarryForPrompt(carry string) string {
	if strings.TrimSpace(carry) == "" {
		return "- (none)"
	}
	return carry
}

// latestPriorTypedCheckpointBody returns the body of the most recent
// checkpoint in messages that carries a parseable typed state block — the
// nearest generation that can actually contribute machine-carryable state —
// or "" when no checkpoint does. Usage-driven and truncate-only summaries
// declare no typed state of their own, yet the model-driven checkpoint they
// replaced can still be the typed carry source: it survives as an older
// checkpoint message while an intermediate compaction left it inside the
// archived head, and its typed JSON line can also survive inside the newer
// summary's `## Previous Checkpoint` appendix. The scan therefore walks
// backward over checkpoint messages and parses each candidate's full body —
// the stripped form first, then the raw body when the typed line only
// survives inside a carried appendix.
//
// unreadable reports that some checkpoint body carried a typed block that
// could not be parsed while no parseable block was found: callers must
// disclose the unreadable carry (typedStateUnreadableNote) instead of reading
// it as an empty one.
func latestPriorTypedCheckpointBody(messages []message.Message) (body string, unreadable bool) {
	var broken bool
	for _, msg := range slices.Backward(messages) {
		if msg.Role != message.RoleUser || !msg.IsCompactionSummary {
			continue
		}
		raw := compactionSummaryBody(msg.Content)
		if raw == "" {
			continue
		}
		stripped := stripCompactionAnchorsBlock(stripCheckpointSkillsSection(stripPriorCheckpointCarrySection(raw)))
		for _, candidate := range []string{stripped, raw} {
			if _, found, malformed := typedStateFromBody(candidate); found {
				if malformed {
					broken = true
					continue
				}
				return candidate, false
			}
		}
	}
	return "", broken
}
