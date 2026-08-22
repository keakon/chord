package memory

import (
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/keakon/chord/internal/sessionview"
)

// Bounded summary budget (internal constants; not user-configurable in v1).
const (
	// maxSummaryTokens bounds the Memory reminder injected into a session head.
	maxSummaryTokens = 900
	// managedSectionTokens reserves the budget for full managed index lines.
	managedSectionTokens = 500
	// notesTokens caps the User Notes prefix when there are no managed entries.
	notesTokens = 300
	// maxSummaryBytes is a generous wire cap on the rendered summary.
	maxSummaryBytes = 8192
)

// boundedPrefixUTF8 returns a UTF-8-safe prefix of s truncated to byteLimit,
// never cutting in the middle of a rune.
func boundedPrefixUTF8(s string, byteLimit int) string {
	if len(s) <= byteLimit {
		return s
	}
	cut := s[:byteLimit]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut
}

// BoundedSummary renders a deterministic, bounded summary of MEMORY.md for the
// session-head reminder. It reads only the User Notes prefix (bounded) and the
// full managed index lines (record IDs + one-line summaries, truncated at whole
// entries only). It never renders record bodies.
//
// Returned active is false when there is nothing meaningful to inject.
func BoundedSummary(idx *MemoryIndex) (string, bool) {
	if idx == nil {
		return "", false
	}
	managed := renderManagedLines(idx.Managed)
	managedUsed := sessionview.EstimatedTokens(managed)

	notes := strings.TrimSpace(idx.UserNotes())
	notesLimited := notes
	if managed == "" {
		// No index yet: give Notes the full budget on their own.
		if sessionview.EstimatedTokens(notes) > notesTokens {
			notesLimited = boundedPrefixUTF8(notes, notesTokens*4)
		}
	} else {
		// Index present: Notes get only the budget left after the managed lines.
		remaining := max(maxSummaryTokens-managedUsed, 0)
		if sessionview.EstimatedTokens(notes) > remaining {
			notesLimited = boundedPrefixUTF8(notes, remaining*4)
		}
	}

	var parts []string
	if strings.TrimSpace(notesLimited) != "" {
		parts = append(parts, strings.TrimSpace(notesLimited))
	}
	if managed != "" {
		parts = append(parts, managed)
	}
	out := strings.TrimSpace(strings.Join(parts, "\n\n"))
	if out == "" {
		return "", false
	}
	if len(out) > maxSummaryBytes {
		out = boundedPrefixUTF8(out, maxSummaryBytes)
	}
	if strings.TrimSpace(out) == "" {
		return "", false
	}
	return out, true
}

// renderManagedLines renders managed index entries (stable sort by ID) as
// "- [id](link)\n  — summary", adding whole entries until the section token
// budget is reached. It never truncates mid-entry. The first entry is always
// kept so a sparse or single large entry still injects something.
func renderManagedLines(entries []ManagedEntry) string {
	sorted := append([]ManagedEntry(nil), entries...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	var sb strings.Builder
	used := 0
	for _, e := range sorted {
		line := "- [" + e.ID + "](" + e.Link + ")\n  — " + e.Summary + "\n"
		add := sessionview.EstimatedTokens(line)
		if used > 0 && used+add > managedSectionTokens {
			break
		}
		sb.WriteString(line)
		used += add
	}
	return strings.TrimSpace(sb.String())
}
