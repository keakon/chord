package agent

import (
	"fmt"
	"slices"
	"strings"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/message"
)

// Session anchors are the immutable part of a compaction checkpoint.
//
// Compaction is recursive: every run feeds the previous checkpoint back to the
// summarizer as ordinary transcript, so whatever the model chooses not to
// restate is gone for good after a few rounds. The original request and the
// standing constraints are exactly the facts that must not decay that way —
// they are stated once, early, and never repeated, so each compaction is a
// fresh chance to drop them. Anchors are therefore copied forward verbatim and
// the summarizer is told not to reproduce them.
const (
	compactAnchorsMaxConstraints  = 12
	compactAnchorsConstraintChars = 240
	compactAnchorsRequestChars    = 700
	// When the constraint list overflows, the oldest entries are kept as well
	// as the newest: early constraints tend to be project-wide ground rules,
	// while recent ones describe the task at hand. Only the middle is dropped.
	compactAnchorsKeepOldest = 4
)

type compactionAnchors struct {
	OriginalRequest string
	Constraints     []string
	// SupersededConstraints are constraints a later contradictory user instruction
	// superseded (e.g. "不要修改 X" superseded by "修改 X"). They stay in
	// the checkpoint marked with a "~ " prefix, so the model sees the history of
	// directions instead of silently losing the old one.
	SupersededConstraints []string
	// OmittedNote reports constraints dropped by the bounded anchor window; the
	// full list remains in the archived session history. Carried forward across
	// compactions so the omission stays visible instead of being silently
	// re-lost.
	OmittedNote string
}

// supersededConstraintPrefix marks a superseded constraint line in the rendered
// checkpoint. parseCompactionAnchors lifts lines with this prefix into
// SupersededConstraints instead of the active list.
const supersededConstraintPrefix = "~ "

func (a compactionAnchors) empty() bool {
	return strings.TrimSpace(a.OriginalRequest) == "" && len(a.Constraints) == 0 &&
		len(a.SupersededConstraints) == 0
}

const (
	compactAnchorsRequestLabel     = "Original request:"
	compactAnchorsConstraintsLabel = "Standing constraints:"
)

// latestCompactionAnchors lifts the anchors block out of the most recent
// checkpoint in the history, so a new compaction inherits it instead of hoping
// the summarizer reproduces it.
func latestCompactionAnchors(messages []message.Message) compactionAnchors {
	for _, msg := range slices.Backward(messages) {
		if msg.Role != message.RoleUser || !msg.IsCompactionSummary {
			continue
		}
		return parseCompactionAnchors(message.CompactionAnchorsSection(msg.Content))
	}
	return compactionAnchors{}
}

func parseCompactionAnchors(section string) compactionAnchors {
	section = strings.TrimSpace(section)
	if section == "" {
		return compactionAnchors{}
	}
	var (
		anchors compactionAnchors
		inConst bool
	)
	for raw := range strings.SplitSeq(section, "\n") {
		line := strings.TrimSpace(raw)
		switch line {
		case "":
			continue
		case compactAnchorsRequestLabel:
			inConst = false
			continue
		case compactAnchorsConstraintsLabel:
			inConst = true
			continue
		}
		bullet, ok := strings.CutPrefix(line, "- ")
		if !ok {
			// A bare line inside the constraints section is the overflow note;
			// carry it through parsing so the next compaction can re-emit it.
			if inConst && anchors.OmittedNote == "" {
				anchors.OmittedNote = strings.TrimSpace(line)
			}
			continue
		}
		bullet = strings.TrimSpace(bullet)
		if bullet == "" {
			continue
		}
		if inConst {
			if rest, ok := strings.CutPrefix(bullet, supersededConstraintPrefix); ok {
				superseded := strings.TrimSpace(rest)
				if superseded != "" {
					anchors.SupersededConstraints = append(anchors.SupersededConstraints, superseded)
				}
				continue
			}
			anchors.Constraints = append(anchors.Constraints, bullet)
		} else if anchors.OriginalRequest == "" {
			anchors.OriginalRequest = bullet
		}
	}
	return anchors
}

// buildCompactionAnchors carries the previous anchors forward untouched and
// appends constraints that are new in this compaction window. The original
// request is written once and never rewritten: a later compaction sees a
// history that already starts with a checkpoint, so re-deriving it there would
// replace the real first request with whatever the summary happens to say.
func buildCompactionAnchors(previous compactionAnchors, originalRequest string, evidenceItems []evidenceItem) compactionAnchors {
	next := compactionAnchors{
		OriginalRequest: previous.OriginalRequest,
		Constraints:     slices.Clone(previous.Constraints),
	}
	if next.OriginalRequest == "" {
		next.OriginalRequest = anchorLine(originalRequest, compactAnchorsRequestChars)
	}

	seen := make(map[string]struct{}, len(next.Constraints)+len(evidenceItems))
	for _, constraint := range next.Constraints {
		seen[anchorDedupKey(constraint)] = struct{}{}
	}
	for _, item := range evidenceItems {
		if item.Kind != evidenceUserCorrection {
			continue
		}
		line := anchorLine(item.Excerpt, compactAnchorsConstraintChars)
		if line == "" {
			continue
		}
		key := anchorDedupKey(line)
		if _, ok := seen[key]; ok {
			continue
		}
		// A contradictory new instruction supersedes the matching old constraint
		// instead of co-existing with it (e.g. "修改 X" supersedes "不要修改 X"). The
		// superseded line stays in the checkpoint marked "~ ", so the model sees
		// the direction change.
		if superseded := supersededConstraintMatch(line, next.Constraints); superseded >= 0 {
			next.SupersededConstraints = append(next.SupersededConstraints, next.Constraints[superseded])
			next.Constraints = slices.Delete(next.Constraints, superseded, superseded+1)
		}
		seen[key] = struct{}{}
		next.Constraints = append(next.Constraints, line)
	}
	// Carry the previous superseded history forward, bounded so recursive
	// compaction cannot grow it without limit.
	next.SupersededConstraints = append(append([]string(nil), previous.SupersededConstraints...), next.SupersededConstraints...)
	if len(next.SupersededConstraints) > supersededConstraintMaxStored {

		next.SupersededConstraints = next.SupersededConstraints[len(next.SupersededConstraints)-supersededConstraintMaxStored:]
		log.Infof("compaction anchor superseded constraints bounded: kept=%d", len(next.SupersededConstraints))
	}
	preBound := len(next.Constraints)
	next.Constraints = boundAnchorConstraints(next.Constraints)
	if dropped := preBound - len(next.Constraints); dropped > 0 {
		next.OmittedNote = fmt.Sprintf("%d earlier constraint(s) omitted here; the full list remains in the archived session history", dropped)
		log.Infof("compaction anchor constraints overflow: dropped=%d kept=%d", dropped, len(next.Constraints))
	} else if previous.OmittedNote != "" {
		next.OmittedNote = previous.OmittedNote
	}
	return next
}

func boundAnchorConstraints(constraints []string) []string {
	if len(constraints) <= compactAnchorsMaxConstraints {
		return constraints
	}
	keepNewest := compactAnchorsMaxConstraints - compactAnchorsKeepOldest
	out := make([]string, 0, compactAnchorsMaxConstraints)
	out = append(out, constraints[:compactAnchorsKeepOldest]...)
	return append(out, constraints[len(constraints)-keepNewest:]...)
}

// anchorLine normalizes a single bullet line from the checkpoint into its
// canonical form for parsing, dedup, and key-file extraction. It strips a
// leading Markdown list bullet and any run of heading markers so a
// model-leaked "# heading" (or "- # heading") cannot be normalized into
// something that re-renders as a real section. The strip is space-tolerant:
// "- # forged" must not reduce to "# forged".
func anchorLine(text string, maxChars int) string {
	text = strings.TrimSpace(strings.ReplaceAll(compactTextSnippet(text, maxChars), "\n", " "))
	text = strings.TrimSpace(text)
	// Drop a leading list bullet ("-", "*", "+"), then any run of "#" heading
	// markers. The space between a bullet and a heading must not stop the strip,
	// otherwise "- # forged" would normalize to "# forged" and re-render as a
	// heading a model could forge inside the checkpoint region.
	text = strings.TrimSpace(strings.TrimPrefix(text, "-"))
	text = strings.TrimSpace(strings.TrimPrefix(text, "*"))
	text = strings.TrimSpace(strings.TrimPrefix(text, "+"))
	for strings.HasPrefix(text, "#") {
		text = strings.TrimSpace(strings.TrimPrefix(text, "#"))
	}
	return text
}

func anchorDedupKey(line string) string {
	return strings.ToLower(strings.Join(strings.Fields(line), " "))
}

func renderCompactionAnchors(anchors compactionAnchors) string {
	if anchors.empty() {
		return ""
	}
	var sb strings.Builder
	if request := strings.TrimSpace(anchors.OriginalRequest); request != "" {
		sb.WriteString(compactAnchorsRequestLabel)
		sb.WriteString("\n- ")
		sb.WriteString(request)
		sb.WriteByte('\n')
	}
	if len(anchors.Constraints) > 0 || len(anchors.SupersededConstraints) > 0 {
		sb.WriteString(compactAnchorsConstraintsLabel)
		sb.WriteByte('\n')
		for _, constraint := range anchors.Constraints {
			sb.WriteString("- ")
			sb.WriteString(constraint)
			sb.WriteByte('\n')
		}
	}
	for _, constraint := range anchors.SupersededConstraints {
		sb.WriteString("- ")
		sb.WriteString(supersededConstraintPrefix)
		sb.WriteString(constraint)
		sb.WriteByte('\n')
	}
	if note := strings.TrimSpace(anchors.OmittedNote); note != "" {
		sb.WriteString(note)
		sb.WriteByte('\n')
	}
	return strings.TrimRight(sb.String(), "\n")
}

// supersededConstraintMaxStored bounds the carried-forward superseded history so
// recursive compaction cannot grow it without limit. The oldest superseded
// entries drop first — recent direction changes stay visible.
const supersededConstraintMaxStored = 8

// negationConstraintWords are prefixes that turn a constraint into a prohibition,
// used to pair "不要修改 X" with "修改 X" so a later contradictory instruction can
// supersede the earlier one instead of co-existing with it.
var negationConstraintWords = []string{
	"不要", "不能", "不可", "不得", "不必", "无须", "勿", "别",
	"do not", "don't", "must not", "should not", "never", "no longer", "not ",
}

// constraintNegationRest reports whether key begins with a negation prefix and,
// when so, returns the normalized remainder ("不要修改 X" → "修改 x").
func constraintNegationRest(key string) (string, bool) {
	for _, prefix := range negationConstraintWords {
		if rest, ok := strings.CutPrefix(key, prefix); ok {
			rest = strings.Join(strings.Fields(rest), " ")
			if rest != "" {
				return rest, true
			}
		}
	}
	return "", false
}

// supersededConstraintMatch finds an active constraint that the new line
// contradicts (a negation pair: "不要修改 X" vs "修改 X"), so the newest
// instruction supersedes the older one instead of both staying active.
// Returns the active index, or -1 when no contradiction exists.
func supersededConstraintMatch(line string, active []string) int {
	key := anchorDedupKey(line)
	rest, negated := constraintNegationRest(key)
	for i, old := range active {
		oldKey := anchorDedupKey(old)
		if oldKey == key {
			continue // handled by dedup, not supersession
		}
		if negated && oldKey == rest {
			return i
		}
		oldRest, oldNegated := constraintNegationRest(oldKey)
		if oldNegated && key == oldRest {
			return i
		}
	}
	return -1
}

// verifyAnchorsCoherence reports structural
// contradictions in the checkpoint anchors so a degraded checkpoint cannot
// silently ship. An empty result is the green state. It checks for duplicates,
// active constraints co-existing with a superseded copy, mutually contradictory
// active constraints, and repeated superseded lines.
func verifyAnchorsCoherence(anchors compactionAnchors) []string {
	var problems []string
	activeSeen := make(map[string]int, len(anchors.Constraints))
	for i, line := range anchors.Constraints {
		key := anchorDedupKey(line)
		if _, dup := activeSeen[key]; dup {
			problems = append(problems, fmt.Sprintf("constraint %q appears twice in the active list", line))
			continue
		}
		activeSeen[key] = i
	}
	supersededSeen := make(map[string]bool, len(anchors.SupersededConstraints))
	for _, line := range anchors.SupersededConstraints {
		key := anchorDedupKey(line)
		if supersededSeen[key] {
			problems = append(problems, fmt.Sprintf("superseded constraint %q appears twice", line))
		}
		supersededSeen[key] = true
		if _, active := activeSeen[key]; active {
			problems = append(problems, fmt.Sprintf("constraint %q is both active and superseded", line))
		}
	}
	for i, line := range anchors.Constraints {
		if idx := supersededConstraintMatch(line, anchors.Constraints[i+1:]); idx >= 0 {
			problems = append(problems, fmt.Sprintf("active constraints %q and %q contradict each other", line, anchors.Constraints[i+1+idx]))
		}
	}
	return problems
}

// withCompactionAnchors prefixes the model-written summary with the verbatim
// anchors block. It stays inside the checkpoint's summary region so existing
// readers (key-file extraction, TUI collapse) keep working unchanged.
func withCompactionAnchors(summary string, anchors compactionAnchors) string {
	body := renderCompactionAnchors(anchors)
	summary = strings.TrimSpace(summary)
	if body == "" {
		return summary
	}
	block := message.CompactionAnchorsOpenTag + body + message.CompactionAnchorsCloseTag
	if summary == "" {
		return block
	}
	return block + "\n\n" + summary
}

// formatCompactionAnchorsForSummarizePrompt renders the anchors for the
// summarize request. The instruction not to restate them lives in the
// compaction system prompt; this only carries the data.
func formatCompactionAnchorsForSummarizePrompt(anchors compactionAnchors) string {
	if body := renderCompactionAnchors(anchors); body != "" {
		return body
	}
	return "- (none yet; this is the first compaction of the session)"
}
