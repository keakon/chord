package memory

import (
	"strings"
	"unicode/utf8"

	"github.com/keakon/chord/internal/sessionview"
)

// Bounded summary budget (internal constants; not user-configurable in v1).
const (
	// maxSummaryTokens bounds the Memory reminder injected into a session head.
	maxSummaryTokens = 900
	// managedSectionMinTokens is the floor guaranteed to the managed index, not
	// a cap: long User Notes must never squeeze the index out, but a short
	// prefix leaves the rest of maxSummaryTokens to the index.
	managedSectionMinTokens = 500
	// notesTokens caps the User Notes prefix. Notes are a hand-written
	// navigation preamble, so they get a fixed reservation and the elastic
	// remainder goes to the automatically growing index.
	notesTokens = 300
	// maxSummaryBytes is a generous wire cap on the rendered summary.
	maxSummaryBytes = 8192
)

// ActiveIndexSoftLimit is roughly how many entries fit in the reminder budget.
// It is the number extraction is told to consolidate against, so the index stays
// within what actually gets injected instead of growing a tail nobody reads.
//
// It is a soft limit: exceeding it never blocks a commit or silently drops an
// entry. Deciding what to forget belongs to the model reading the content, not
// to a heuristic on age or confidence.
const ActiveIndexSoftLimit = 24

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
// managed index lines (record IDs + one-line summaries, truncated at whole
// entries only). It never renders record bodies.
//
// Notes are bounded first; the managed index then gets whatever the notes leave
// of maxSummaryTokens, never less than managedSectionMinTokens. The index is the
// part that grows automatically, so it owns the elastic remainder.
//
// Returned active is false when there is nothing meaningful to inject.
func BoundedSummary(idx *MemoryIndex) (string, bool) {
	if idx == nil {
		return "", false
	}
	notes := strings.TrimSpace(idx.UserNotes())
	notesLimited := notes
	if sessionview.EstimatedTokens(notes) > notesTokens {
		notesLimited = boundedPrefixUTF8(notes, notesTokens*4)
	}
	notesLimited = strings.TrimSpace(notesLimited)
	managedBudget := max(maxSummaryTokens-sessionview.EstimatedTokens(notesLimited), managedSectionMinTokens)
	managed := renderManagedLines(idx.Managed, managedBudget)

	var parts []string
	if notesLimited != "" {
		parts = append(parts, notesLimited)
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

// renderManagedLines renders managed index entries as "- [id](link)\n  — summary",
// adding whole entries until the token budget is reached. It never truncates
// mid-entry. The first entry is always kept so a sparse or single large entry
// still injects something.
//
// Entries render in MEMORY.md's own order, not sorted: BuildManagedIndexReplacing
// keeps existing entries in place and prepends new ones, so the most recently
// learned records inject first and manually reordering MEMORY.md takes effect
// directly. Sorting by ID would rank by slug spelling — with multi-byte IDs
// sorting last, they could never be injected at all.
func renderManagedLines(entries []ManagedEntry, budget int) string {
	var sb strings.Builder
	used := 0
	for _, e := range entries {
		line := "- [" + e.ID + "](" + e.Link + ")\n  — " + e.Summary + "\n"
		add := sessionview.EstimatedTokens(line)
		if used > 0 && used+add > budget {
			break
		}
		sb.WriteString(line)
		used += add
	}
	return strings.TrimSpace(sb.String())
}
