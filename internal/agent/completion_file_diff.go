package agent

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	completionFileDiffLineMaxRunes = 240
	completionFileDiffMaxExamples  = 3

	completionReportedOnlyLabel = "reported_only_in_records"
	completionObservedOnlyLabel = "observed_only_in_records"

	completionFileDiffNote = "file_list_note: Differences compare retained path records only; they do not establish missing changes or false claims."
	// The incomplete variant is the same one-line note with the missing-record
	// qualifier: a truncated or partially attributable observation can itself
	// produce a difference, so the note must not read as a completeness claim.
	completionFileDiffNoteIncomplete = "file_list_note: Differences compare retained path records only; the records are incomplete, so they do not establish missing changes or false claims."
)

// completionPathRecord is one file-list entry reduced to its lexical
// comparison key. Only spelling differences that the text alone settles are
// removed; the record keeps ambiguity and shape flags so the caller can refuse
// a diff whose combinations would require guessing at the filesystem.
type completionPathRecord struct {
	key       string
	ambiguous bool
	absolute  bool
	hasParent bool
}

// completionFileRecordDiff is the retained-record difference between a
// worker's self-reported list and the paths Chord observed. Both directions are
// list differences, never claims about what the task did or did not change.
type completionFileRecordDiff struct {
	reportedOnly []string
	observedOnly []string
}

// completionFileDiffLines derives the bounded diff summary for one completion.
// Returned lines already carry linePrefix so the 240-rune bound covers the
// rendered line; no lines means the lists agree, both sides are empty, or the two
// records cannot be compared safely.
func completionFileDiffLines(env *CompletionEnvelope, linePrefix string) []string {
	diff, ok := completionFileRecordDiffFor(env)
	if !ok {
		return nil
	}
	lines := make([]string, 0, 3)
	if len(diff.reportedOnly) > 0 {
		lines = append(lines, completionOnlyRecordLine(linePrefix+completionReportedOnlyLabel, diff.reportedOnly))
	}
	if len(diff.observedOnly) > 0 {
		lines = append(lines, completionOnlyRecordLine(linePrefix+completionObservedOnlyLabel, diff.observedOnly))
	}
	if len(lines) == 0 {
		return nil
	}
	note := completionFileDiffNote
	if env.FileAttributionIncomplete {
		note = completionFileDiffNoteIncomplete
	}
	return append(lines, linePrefix+note)
}

func completionFileRecordDiffFor(env *CompletionEnvelope) (completionFileRecordDiff, bool) {
	if env == nil {
		return completionFileRecordDiff{}, false
	}
	reported := completionPathRecords(env.ReportedFilesChanged)
	observed := completionPathRecords(env.ActualFilesChanged)
	if len(reported) == 0 && len(observed) == 0 {
		return completionFileRecordDiff{}, false
	}
	// One-sided records still show which side recorded paths. They do not
	// imply the empty side was observed completely, so no matching is needed.
	if len(reported) == 0 {
		return completionFileRecordDiff{observedOnly: completionRecordKeys(observed)}, true
	}
	if len(observed) == 0 {
		return completionFileRecordDiff{reportedOnly: completionRecordKeys(reported)}, true
	}

	observedKeys := make(map[string]struct{}, len(observed))
	for _, rec := range observed {
		observedKeys[rec.key] = struct{}{}
	}
	reportedKeys := make(map[string]struct{}, len(reported))
	for _, rec := range reported {
		reportedKeys[rec.key] = struct{}{}
	}
	diff := completionFileRecordDiff{}
	for _, rec := range reported {
		if _, ok := observedKeys[rec.key]; !ok {
			diff.reportedOnly = append(diff.reportedOnly, rec.key)
		}
	}
	for _, rec := range observed {
		if _, ok := reportedKeys[rec.key]; !ok {
			diff.observedOnly = append(diff.observedOnly, rec.key)
		}
	}
	if len(diff.reportedOnly) == 0 && len(diff.observedOnly) == 0 {
		return completionFileRecordDiff{}, false
	}
	if !completionRecordsComparable(reported, observed) {
		return completionFileRecordDiff{}, false
	}
	return diff, true
}

// completionRecordsComparable reports whether leftover unmatched entries can be
// read as real list differences. Parent segments, mixed absolute/relative
// spellings, and ambiguous path syntax each let two spellings name the same file
// without the recorded lists revealing it, so a diff would be a guess.
func completionRecordsComparable(groups ...[]completionPathRecord) bool {
	var absolute, relative bool
	for _, group := range groups {
		for _, rec := range group {
			if rec.ambiguous || rec.hasParent {
				return false
			}
			if rec.absolute {
				absolute = true
			} else {
				relative = true
			}
		}
	}
	return !(absolute && relative)
}

func completionPathRecords(items []string) []completionPathRecord {
	if len(items) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(items))
	records := make([]completionPathRecord, 0, len(items))
	for _, item := range items {
		rec, ok := parseCompletionPathRecord(item)
		if !ok {
			continue
		}
		if _, ok := seen[rec.key]; ok {
			continue
		}
		seen[rec.key] = struct{}{}
		records = append(records, rec)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].key < records[j].key })
	return records
}

func completionRecordKeys(records []completionPathRecord) []string {
	keys := make([]string, len(records))
	for i, rec := range records {
		keys[i] = rec.key
	}
	return keys
}

// parseCompletionPathRecord normalizes one recorded spelling without touching
// the filesystem. Records do not carry their originating platform, so Windows
// path syntax must stay opaque: backslashes and drive prefixes can also be
// ordinary POSIX filename characters. Leading "//" can identify a network root.
func parseCompletionPathRecord(raw string) (completionPathRecord, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return completionPathRecord{}, false
	}
	if strings.ContainsRune(raw, '\\') || hasWindowsDrivePrefix(raw) || strings.HasPrefix(raw, "//") {
		return completionPathRecord{key: raw, ambiguous: true}, true
	}
	rec := completionPathRecord{absolute: strings.HasPrefix(raw, "/")}
	segments := make([]string, 0, 4)
	for segment := range strings.SplitSeq(raw, "/") {
		switch segment {
		case "", ".":
			continue
		case "..":
			rec.hasParent = true
			segments = append(segments, segment)
		default:
			segments = append(segments, segment)
		}
	}
	joined := strings.Join(segments, "/")
	switch {
	case rec.absolute:
		rec.key = "/" + joined
	case joined == "":
		rec.key = "."
	default:
		rec.key = joined
	}
	// Dropping a leading "./" can expose a drive-like prefix. Equal keys
	// must retain the same ambiguity regardless of which spelling came first.
	rec.ambiguous = hasWindowsDrivePrefix(rec.key)
	return rec, true
}

func hasWindowsDrivePrefix(path string) bool {
	if len(path) < 2 || path[1] != ':' {
		return false
	}
	c := path[0]
	return ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z')
}

// completionOnlyRecordLine renders one direction under the rune budget. It
// keeps the label and total count, then drops examples until the line fits;
// the omitted count covers examples dropped both by the example cap and by the
// budget, so the "...N more" marker stays truthful.
func completionOnlyRecordLine(label string, items []string) string {
	total := len(items)
	shown := min(total, completionFileDiffMaxExamples)
	for {
		line := buildCompletionOnlyRecordLine(label, total, items[:shown])
		if shown == 0 || utf8.RuneCountInString(line) <= completionFileDiffLineMaxRunes {
			return line
		}
		shown--
	}
}

func buildCompletionOnlyRecordLine(label string, total int, shown []string) string {
	var b strings.Builder
	b.WriteString(label)
	b.WriteString(": ")
	b.WriteString(strconv.Itoa(total))
	b.WriteString(" (")
	for i, item := range shown {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(escapeCompletionRecordText(item))
	}
	if omitted := total - len(shown); omitted > 0 {
		if len(shown) > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "...%d more", omitted)
	}
	b.WriteString(")")
	return b.String()
}

// escapeCompletionRecordText keeps a recorded path on one display line. Paths
// are worker-supplied text, so newlines and other control characters must not
// break the mailbox/snapshot line structure or emit terminal control codes.
func escapeCompletionRecordText(path string) string {
	var b strings.Builder
	for _, r := range path {
		switch {
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case unicode.IsControl(r):
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
