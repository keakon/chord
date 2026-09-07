package agent

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/keakon/chord/internal/message"
)

// Checkpoint skill continuity.
//
// A skill's instructions exist only in the `skill` tool result inside the
// transcript: the system prompt carries names and descriptions, never bodies.
// Archiving the head therefore removes the instructions from the live context
// completely, and without the section below nothing in the checkpoint records
// that they were ever loaded — a continuation that is still supposed to follow
// a skill's workflow has no way to know it existed.
//
// Re-injecting the bodies is not the fix: a skill body runs to several KB, so
// restoring every skill the archived work happened to touch would spend
// exactly what compaction just reclaimed, on instructions the continuation may
// no longer need. The checkpoint records the names instead. That is enough for
// the model to re-invoke the ones that still matter and cheap enough that it
// costs one line; which skills those are is the model's decision, not the
// runtime's.
//
// The section is built from runtime facts — the successful skill calls in the
// archived head, merged with the names the previous checkpoint recorded — and
// re-merged on every compaction, so recursion cannot erode it the way it
// erodes a summarizer-written section.
const (
	checkpointSkillsHeading = "## Skills Invoked Earlier"
	// checkpointMaxSkillNames bounds the list. Overflow is reported as a count
	// and the archived history keeps the full record. Names are listed
	// alphabetically rather than by recency: the cap is far above the number of
	// skills a session realistically loads, so a stable order is worth more
	// than choosing which name to drop.
	checkpointMaxSkillNames = 12
	// checkpointSkillNameMaxLen rejects a parsed line that cannot be a skill
	// name, so the omission note and any prose a summarizer left inside the
	// section never re-enter the list as a fake skill.
	checkpointSkillNameMaxLen = 64
)

var (
	checkpointSkillsHeadingRe = regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(checkpointSkillsHeading) + `\s*$`)
	// checkpointSkillsOmittedRe parses the omission note back, so an overflow
	// stays visible across compactions instead of being silently forgotten one
	// checkpoint at a time (the same guarantee the anchors' OmittedNote holds).
	checkpointSkillsOmittedRe = regexp.MustCompile(`^\((\d+) more omitted`)
)

// collectCheckpointSkillNames returns the skills whose instructions the
// archived head loaded, merged with the ones its most recent checkpoint already
// recorded, plus the number dropped by the cap. Only the newest checkpoint is
// read: it has itself merged everything older, so walking further back would
// re-count names that are already accounted for.
func collectCheckpointSkillNames(head []message.Message) ([]string, int) {
	seen := make(map[string]struct{})
	for _, name := range invokedSkillNamesFromMessages(head) {
		seen[name] = struct{}{}
	}
	carriedOmitted := 0
	for _, msg := range slices.Backward(head) {
		if msg.Role != message.RoleUser || !msg.IsCompactionSummary {
			continue
		}
		var names []string
		names, carriedOmitted = parseCheckpointSkillNames(msg.Content)
		for _, name := range names {
			seen[name] = struct{}{}
		}
		break
	}
	if len(seen) == 0 {
		return nil, carriedOmitted
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	omitted := carriedOmitted
	if len(names) > checkpointMaxSkillNames {
		omitted += len(names) - checkpointMaxSkillNames
		names = names[:checkpointMaxSkillNames]
	}
	return names, omitted
}

// parseCheckpointSkillNames lifts the recorded skill names and the carried
// omission count out of a checkpoint message.
func parseCheckpointSkillNames(checkpointContent string) ([]string, int) {
	section, ok := checkpointSkillsSection(compactionSummaryBody(checkpointContent))
	if !ok {
		return nil, 0
	}
	var names []string
	omitted := 0
	seen := make(map[string]struct{})
	for rawLine := range strings.SplitSeq(section, "\n") {
		candidate := normalizeSummaryBulletCandidate(rawLine)
		if candidate == "" {
			continue
		}
		if match := checkpointSkillsOmittedRe.FindStringSubmatch(candidate); match != nil {
			if count, err := strconv.Atoi(match[1]); err == nil {
				omitted += count
			}
			continue
		}
		if !isCheckpointSkillName(candidate) {
			continue
		}
		if _, dup := seen[candidate]; dup {
			continue
		}
		seen[candidate] = struct{}{}
		names = append(names, candidate)
	}
	return names, omitted
}

// isCheckpointSkillName reports whether a parsed bullet can be a skill name.
// Skill names are single tokens (optionally `plugin:skill`), so anything with
// whitespace is prose that belongs to the section, not an entry in it.
func isCheckpointSkillName(candidate string) bool {
	if candidate == "" || len(candidate) > checkpointSkillNameMaxLen {
		return false
	}
	return !strings.ContainsFunc(candidate, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '('
	})
}

// checkpointSkillsSection returns the body of the skills section, without its
// heading, and whether the section exists at all.
func checkpointSkillsSection(body string) (string, bool) {
	loc := checkpointSkillsHeadingRe.FindStringIndex(body)
	if loc == nil {
		return "", false
	}
	start := loc[1]
	end := len(body)
	if next := compactionMarkdownHeadingLineRe.FindStringIndex(body[start:]); next != nil {
		end = start + next[0]
	}
	return strings.TrimSpace(body[start:end]), true
}

// stripCheckpointSkillsSection removes every skills section from a summary
// body. More than one can be present: a summarizer may have restated the
// section it saw in the archived head, and the prior-checkpoint carry can
// bring another copy along. The authoritative section is re-appended by
// ensureCheckpointSkillsSection, so all existing copies go.
func stripCheckpointSkillsSection(body string) string {
	for {
		loc := checkpointSkillsHeadingRe.FindStringIndex(body)
		if loc == nil {
			return strings.TrimSpace(body)
		}
		end := len(body)
		if next := compactionMarkdownHeadingLineRe.FindStringIndex(body[loc[1]:]); next != nil {
			end = loc[1] + next[0]
		}
		prefix := strings.TrimSpace(body[:loc[0]])
		rest := strings.TrimSpace(body[end:])
		switch {
		case prefix == "":
			body = rest
		case rest == "":
			body = prefix
		default:
			body = prefix + "\n\n" + rest
		}
	}
}

// ensureCheckpointSkillsSection replaces whatever skills section a summary
// carries with the authoritative one. Called on every checkpoint path — model
// summary, structured fallback, truncate-only and model-driven — because the
// record is a runtime fact and must not depend on what a summarizer chose to
// restate.
func ensureCheckpointSkillsSection(summary string, names []string, omitted int) string {
	summary = stripCheckpointSkillsSection(summary)
	section := renderCheckpointSkillsSection(names, omitted)
	switch {
	case section == "":
		return summary
	case summary == "":
		return section
	default:
		return summary + "\n\n" + section
	}
}

// renderCheckpointSkillsSection renders the section, or "" when the archived
// head loaded no skills. The wording has to keep the model from reading the
// list as instructions that are still in effect: the names are all that
// survived, so re-invoking is how it gets the workflow back.
func renderCheckpointSkillsSection(names []string, omitted int) string {
	if len(names) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(checkpointSkillsHeading)
	sb.WriteString("\nLoaded during the archived conversation; the instructions themselves are no longer in context, only these names. Call `skill` again with one of them when the continuation still has to follow that workflow — do not assume the content from the name.\n")
	for _, name := range names {
		sb.WriteString("- ")
		sb.WriteString(name)
		sb.WriteByte('\n')
	}
	if omitted > 0 {
		fmt.Fprintf(&sb, "- (%d more omitted; the archived history holds them)\n", omitted)
	}
	return strings.TrimRight(sb.String(), "\n")
}
