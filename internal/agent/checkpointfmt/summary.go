// Package checkpointfmt defines the canonical checkpoint summary format and
// its section readers. It owns only immutable schema data and synchronized
// regexp caches; model calls, checkpoint application and persistence stay with callers.
package checkpointfmt

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
)

// ValidateSummary checks model-authored checkpoint summaries against the canonical
// heading order and content rules. Runtime-authored continuation layouts are validated
// by their own builders and must not be passed here. Input text is never rewritten.
func ValidateSummary(summary string) error {
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return fmt.Errorf("compaction model returned empty summary")
	}
	if containsThinkingTag(summary) {
		return fmt.Errorf("compaction summary contains private thinking tags")
	}
	if len([]rune(summary)) < compactSummaryMinChars {
		return fmt.Errorf("compaction summary too short (%d chars)", len([]rune(summary)))
	}
	positions := compactionHeadingPositions(summary)
	if missing := missingCompactionHeadings(summary); len(missing) > 0 {
		return &MissingSectionsError{Missing: missing, Total: len(compactionRequiredHeadings)}
	}
	// compactionHeadingPositions scans headings sequentially, so a short result
	// means an earlier heading appeared after a later one rather than a missing
	// section.
	if len(positions) != len(compactionRequiredHeadings) {
		return fmt.Errorf("compaction summary sections out of order")
	}
	if strings.TrimSpace(summary[:positions[0]]) != "" {
		return fmt.Errorf("compaction summary has content before first required section")
	}
	if err := validateCompactionNextStep(summary, positions[len(positions)-1]); err != nil {
		return err
	}
	if err := validateCompactionTodoState(summary); err != nil {
		return err
	}
	return nil
}

// MissingSectionsError names the required headings a summary
// omitted so repair prompts and diagnostics can act on the exact sections
// instead of only a match ratio.
type MissingSectionsError struct {
	Missing []string
	Total   int
}

func (e *MissingSectionsError) Error() string {
	return fmt.Sprintf("compaction summary missing required sections (%d/%d): %s",
		e.Total-len(e.Missing), e.Total, strings.Join(e.Missing, ", "))
}

var (
	compactionMarkdownHeadingLineRe      = regexp.MustCompile(`(?m)^##\s+`)
	compactionRequiredHeadingLineRegexps = buildCompactionRequiredHeadingLineRegexps()
	// extraHeadingLineRegexps caches compiled full-line heading patterns for
	// headings outside compactionRequiredHeadings (for example the typed
	// checkpoint state section). The pattern is compiled once per heading and
	// reused, so parsing a typed block does not allocate a fresh regexp on
	// every checkpoint read.
	extraHeadingLineRegexpsMu sync.Mutex
	extraHeadingLineRegexps   = map[string]*regexp.Regexp{}
)

func buildCompactionRequiredHeadingLineRegexps() map[string]*regexp.Regexp {
	patterns := make(map[string]*regexp.Regexp, len(compactionRequiredHeadings))
	for _, heading := range compactionRequiredHeadings {
		patterns[heading] = regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(heading) + `\s*$`)
	}
	return patterns
}

func validateCompactionNextStep(summary string, nextStepPos int) error {
	if nextStepPos < 0 || nextStepPos >= len(summary) {
		return fmt.Errorf("compaction summary missing next step")
	}
	section := strings.TrimSpace(summary[nextStepPos+len("## Next Step"):])
	if section == "" {
		return fmt.Errorf("compaction summary next step is empty")
	}
	if isVagueCompactionNextStep(section) {
		return fmt.Errorf("compaction summary next step is too vague")
	}
	return nil
}

func isVagueCompactionNextStep(section string) bool {
	normalized := strings.ToLower(strings.TrimSpace(section))
	for strings.HasPrefix(normalized, "-") {
		normalized = strings.TrimSpace(strings.TrimPrefix(normalized, "-"))
	}
	normalized = strings.Trim(strings.TrimSuffix(normalized, "."), " ")
	switch normalized {
	case "continue",
		"continue working",
		"continue the task",
		"keep working",
		"proceed",
		"resume",
		"resume work",
		"carry on":
		return true
	default:
		return false
	}
}

func validateCompactionTodoState(summary string) error {
	section, ok := Section(summary, "## Todo State")
	if !ok {
		return nil
	}
	active := todoSubsectionLines(section, TodoSubgroupActive)
	completed := todoSubsectionLines(section, TodoSubgroupCompleted)
	stale := todoSubsectionLines(section, TodoSubgroupStale)
	if len(active) == 0 {
		return nil
	}
	inactive := append(completed, stale...)
	for _, activeLine := range active {
		activeKey := normalizeTodoStateLine(activeLine)
		if activeKey == "" || activeKey == "none" {
			continue
		}
		for _, inactiveLine := range inactive {
			inactiveKey := normalizeTodoStateLine(inactiveLine)
			if inactiveKey == "" || inactiveKey == "none" {
				continue
			}
			if activeKey == inactiveKey {
				return fmt.Errorf("compaction summary marks completed or stale todo as active: %s", activeLine)
			}
		}
	}
	return nil
}

// SectionBounds locates a summary section's body: start is the offset
// just past the heading, end is where the next "## " heading begins or the end
// of the summary. Readers and writers of a section share it so there is one
// definition of where a section stops.
func SectionBounds(summary, heading string) (start, end int, ok bool) {
	pos := FindHeading(summary, heading)
	if pos < 0 {
		return 0, 0, false
	}
	start = pos + len(heading)
	end = SectionEnd(summary, start)
	return start, end, true
}

// Section returns the trimmed body of the first matching summary section.
func Section(summary, heading string) (string, bool) {
	start, end, ok := SectionBounds(summary, heading)
	if !ok {
		return "", false
	}
	return strings.TrimSpace(summary[start:end]), true
}

func todoSubsectionLines(section, label string) []string {
	var lines []string
	inGroup := false
	for raw := range strings.SplitSeq(section, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		bullet := strings.TrimSpace(strings.TrimPrefix(line, "-"))
		if strings.HasPrefix(bullet, label+":") {
			inGroup = true
			rest := strings.TrimSpace(strings.TrimPrefix(bullet, label+":"))
			if rest != "" {
				lines = append(lines, rest)
			}
			continue
		}
		if strings.HasPrefix(bullet, TodoSubgroupActive+":") || strings.HasPrefix(bullet, TodoSubgroupCompleted+":") || strings.HasPrefix(bullet, TodoSubgroupStale+":") {
			inGroup = false
			continue
		}
		if strings.HasPrefix(line, "#") {
			inGroup = false
			continue
		}
		if inGroup {
			lines = append(lines, bullet)
		}
	}
	return lines
}

func normalizeTodoStateLine(line string) string {
	line = strings.ToLower(strings.TrimSpace(line))
	line = strings.TrimPrefix(line, "-")
	line = strings.TrimSpace(line)
	line = strings.Trim(line, "`*_ .")
	line = strings.Trim(line, "()")
	return line
}

func containsThinkingTag(summary string) bool {
	lower := strings.ToLower(summary)
	return strings.Contains(lower, "<think>") || strings.Contains(lower, "</think>")
}

func missingCompactionHeadings(summary string) []string {
	missing := make([]string, 0, len(compactionRequiredHeadings))
	for _, heading := range compactionRequiredHeadings {
		if FindHeading(summary, heading) < 0 {
			missing = append(missing, heading)
		}
	}
	return missing
}

func compactionHeadingPositions(summary string) []int {
	positions := make([]int, 0, len(compactionRequiredHeadings))
	searchStart := 0
	for _, heading := range compactionRequiredHeadings {
		pos := FindHeading(summary[searchStart:], heading)
		if pos < 0 {
			break
		}
		absolute := searchStart + pos
		positions = append(positions, absolute)
		searchStart = absolute + len(heading)
	}
	return positions
}

// FindHeading returns the byte offset of an exact full-line heading, or -1.
// Compiled patterns are shared safely across readers; callers cannot mutate the cache.
func FindHeading(summary, heading string) int {
	pattern := compactionRequiredHeadingLineRegexps[heading]
	if pattern == nil {
		// A heading outside the required section set (for example the typed
		// checkpoint state section) matches the same full-line rule: the
		// heading must start a line at column zero and be the line's entire
		// content. A mid-prose occurrence of the heading text can therefore
		// never be mistaken for the section. The pattern is cached, not
		// recompiled per lookup.
		extraHeadingLineRegexpsMu.Lock()
		pattern = extraHeadingLineRegexps[heading]
		if pattern == nil {
			pattern = regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(heading) + `\s*$`)
			extraHeadingLineRegexps[heading] = pattern
		}
		extraHeadingLineRegexpsMu.Unlock()
	}
	loc := pattern.FindStringIndex(summary)
	if loc == nil {
		return -1
	}
	return loc[0]
}

// SectionEnd returns the next level-two heading offset at or after bodyStart,
// or len(summary) if none follows. bodyStart must be a valid byte offset.
func SectionEnd(summary string, bodyStart int) int {
	if loc := compactionMarkdownHeadingLineRe.FindStringIndex(summary[bodyStart:]); loc != nil {
		return bodyStart + loc[0]
	}
	return len(summary)
}

// todoSubgroupIDLineRe matches a classified todo bullet regardless of the
// language or wording of its status marker: "- [status] id: text". The status
// text is deliberately unconstrained so a translated summary still yields the
// stable todo ID the restore path needs.
var todoSubgroupIDLineRe = regexp.MustCompile(`^\s*(?:-\s*)?\[[^\]]*\]\s*([^:\n]+):`)

// TodoSubgroupIDs returns the todo IDs listed in a Todo State subgroup body.
// Parsing stops at the next Markdown heading so a nested runtime snapshot does
// not contribute IDs to the enclosing subgroup. A subgroup with no parseable
// IDs returns nil; callers must treat that as "no classified todos", not as an
// empty todo list.
func TodoSubgroupIDs(section string) []string {
	var ids []string
	for _, line := range strings.Split(section, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			break
		}
		match := todoSubgroupIDLineRe.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		if id := strings.TrimSpace(match[1]); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}
