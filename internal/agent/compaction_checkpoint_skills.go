package agent

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/skill"
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
// erodes a summarizer-written section. Names the current model-facing catalog
// does not expose — a manual-only skill, one the ruleset denies, or one that
// left the catalog — are marked instead of being offered to a `skill` call the
// execution path refuses; the archived history and the user remain their
// recovery paths.
const (
	checkpointSkillsHeading = "## Skills Invoked Earlier"
	// checkpointSkillsFact and checkpointSkillsReloadHint introduce the names
	// the model can still load; the reload hint is dropped when none of them is
	// loadable, so the section never asks for a call that would be refused.
	checkpointSkillsFact       = "Loaded during the archived conversation; the instructions themselves are no longer in context, only these names."
	checkpointSkillsReloadHint = "Call `skill` again with one of them when the continuation still has to follow that workflow — do not assume the content from the name."
	// checkpointSkillsNotLoadableLabel introduces the names the model cannot
	// load now, for the same three reasons the model face excludes a skill. They
	// stay in the record — the continuation may still be following their
	// workflow — but a reload attempt would fail, so the recovery paths are the
	// archived history and the user.
	checkpointSkillsNotLoadableLabel = "Not loadable by you — do not call `skill` for these; read the archived history if their workflow must continue, or ask the user to load one again:"
	// checkpointMaxSkillNames bounds the list. Overflow is reported as a count
	// and the archived history keeps the full record. Names are listed
	// alphabetically within each group rather than by recency: the cap is far
	// above the number of skills a session realistically loads, so a stable
	// order is worth more than choosing which name to drop.
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
// restate. modelLoadable answers whether the model can still call `skill` for
// a recorded name.
func ensureCheckpointSkillsSection(summary string, names []string, omitted int, modelLoadable func(string) bool) string {
	summary = stripCheckpointSkillsSection(summary)
	section := renderCheckpointSkillsSection(names, omitted, modelLoadable)
	switch {
	case section == "":
		return summary
	case summary == "":
		return section
	default:
		return summary + "\n\n" + section
	}
}

// checkpointModelLoadable reports whether the model can still load a recorded
// skill by name. The model-facing catalog is the only authority: a manual-only
// skill, a name the ruleset now denies, and a skill that left the catalog all
// fail the `skill` tool the same way, so the section must not send the model
// into a call that is refused.
func checkpointModelLoadable(skills []*skill.Meta) func(string) bool {
	visible := make(map[string]struct{}, len(skills))
	for _, meta := range skills {
		if meta != nil && meta.Name != "" {
			visible[meta.Name] = struct{}{}
		}
	}
	return func(name string) bool {
		_, ok := visible[name]
		return ok
	}
}

// renderCheckpointSkillsSection renders the section, or "" when the archived
// head loaded no skills. The wording has to keep the model from reading the
// list as instructions that are still in effect: the instructions themselves
// are gone, so re-invoking is how it gets a workflow back — and a name the
// model cannot load is listed apart, with its own recovery paths, instead of
// inviting the refused call.
func renderCheckpointSkillsSection(names []string, omitted int, modelLoadable func(string) bool) string {
	if len(names) == 0 {
		return ""
	}
	loadable := make([]string, 0, len(names))
	notLoadable := make([]string, 0, len(names))
	for _, name := range names {
		if modelLoadable(name) {
			loadable = append(loadable, name)
			continue
		}
		notLoadable = append(notLoadable, name)
	}
	var sb strings.Builder
	sb.WriteString(checkpointSkillsHeading)
	sb.WriteString("\n")
	sb.WriteString(checkpointSkillsFact)
	if len(loadable) > 0 {
		sb.WriteString(" ")
		sb.WriteString(checkpointSkillsReloadHint)
		for _, name := range loadable {
			sb.WriteString("\n- ")
			sb.WriteString(name)
		}
	}
	if len(notLoadable) > 0 {
		sb.WriteString("\n")
		sb.WriteString(checkpointSkillsNotLoadableLabel)
		for _, name := range notLoadable {
			sb.WriteString("\n- ")
			sb.WriteString(name)
		}
	}
	if omitted > 0 {
		fmt.Fprintf(&sb, "\n- (%d more omitted; the archived history holds them)", omitted)
	}
	return sb.String()
}
