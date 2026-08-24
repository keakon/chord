package memory

import (
	"errors"
	"fmt"
	"strings"
)

// ManagedEntry is one line of the Chord-managed index section in MEMORY.md.
type ManagedEntry struct {
	ID      string // stable record ID, e.g. "session-switch-boundary--a1b2c3d4e5f6a7b8"
	Link    string // project-root-relative path to the record file
	Summary string // one-line preview
}

// MemoryIndex is the parsed state of MEMORY.md.
type MemoryIndex struct {
	// Head is user-owned content before the managed section.
	Head string
	// Tail is user-owned content after the managed section. Chord's own writes
	// always keep the managed section last, so Tail is normally empty; user
	// content appended after the markers is preserved verbatim.
	Tail string
	// Managed holds the ordered managed entries.
	Managed []ManagedEntry
	// HasManagedMarker reports whether a valid managed marker pair was found.
	HasManagedMarker bool
	// Raw is the raw file content ("" when no index file exists).
	Raw string
}

// ErrManagedMarkers is reported when MEMORY.md has duplicate, nested, or
// malformed managed markers. Automatic writes stop and the caller surfaces a
// product-facing error instead of guessing a repair.
var ErrManagedMarkers = errors.New("MEMORY.md has malformed Chord-managed markers")

// UserNotes returns the combined user-owned text (Head + Tail).
func (i *MemoryIndex) UserNotes() string {
	parts := []string{strings.TrimSpace(i.Head), strings.TrimSpace(i.Tail)}
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, "\n\n")
}

// managedSectionContent renders the managed section (markers + content). It
// does not include surrounding user notes. Entries render in the given order:
// BuildManagedIndexReplacing owns ordering so a manual reorder of MEMORY.md is
// preserved across automatic writes.
func managedSectionContent(entries []ManagedEntry) string {
	var sb strings.Builder
	sb.WriteString(managedStartMarker)
	sb.WriteString("\n\n## Managed Records\n\n")
	for _, e := range entries {
		sb.WriteString("- [")
		sb.WriteString(e.ID)
		sb.WriteString("](")
		sb.WriteString(e.Link)
		sb.WriteString(")\n")
		sb.WriteString("  — ")
		sb.WriteString(e.Summary)
		sb.WriteString("\n")
	}
	sb.WriteString(managedEndMarker)
	return sb.String()
}

// parseMemoryFile parses MEMORY.md content into user notes + managed entries.
// It is strict about marker shape: duplicate, nested, or unclosed markers are an
// error, never auto-repaired. Head and Tail keep the exact original bytes so a
// later managed-section write preserves user content verbatim.
func parseMemoryFile(data string) (*MemoryIndex, error) {
	if strings.TrimSpace(data) == "" {
		return &MemoryIndex{}, nil
	}
	startIdx := strings.Index(data, managedStartMarker)
	endIdx := strings.Index(data, managedEndMarker)
	switch {
	case startIdx < 0 && endIdx < 0:
		return &MemoryIndex{Head: data, Raw: data}, nil
	case startIdx < 0 && endIdx >= 0:
		return nil, fmt.Errorf("%w: closing marker without opening", ErrManagedMarkers)
	case startIdx >= 0 && endIdx < 0:
		return nil, fmt.Errorf("%w: opening marker without closing", ErrManagedMarkers)
	}
	if strings.Count(data, managedStartMarker) != 1 {
		return nil, fmt.Errorf("%w: duplicate managed-start marker", ErrManagedMarkers)
	}
	if strings.Count(data, managedEndMarker) != 1 {
		return nil, fmt.Errorf("%w: duplicate managed-end marker", ErrManagedMarkers)
	}
	if endIdx < startIdx {
		return nil, fmt.Errorf("%w: managed-end before managed-start", ErrManagedMarkers)
	}
	between := data[startIdx+len(managedStartMarker) : endIdx]
	if strings.Contains(between, managedStartMarker) {
		return nil, fmt.Errorf("%w: nested managed-start marker", ErrManagedMarkers)
	}
	head := data[:startIdx]
	tail := data[endIdx+len(managedEndMarker):]
	entries, err := parseManagedEntries(between)
	if err != nil {
		return nil, err
	}
	return &MemoryIndex{
		Head:             head,
		Tail:             tail,
		Managed:          entries,
		HasManagedMarker: true,
		Raw:              data,
	}, nil
}

// parseManagedEntries parses the text between the markers into entries.
// Line format:
//
//	## Managed Records
//	- [id](.chord/memory/records/id.md)
//	  — one-line summary
//
// A truncated or malformed entry is an error rather than a silent drop, keeping
// the index trustworthy.
func parseManagedEntries(text string) ([]ManagedEntry, error) {
	var entries []ManagedEntry
	raw := strings.Split(text, "\n")
	for i := 0; i < len(raw); i++ {
		ln := strings.TrimSpace(raw[i])
		if ln == "" || strings.HasPrefix(ln, "## ") {
			continue
		}
		if !strings.HasPrefix(ln, "- [") {
			return nil, fmt.Errorf("%w: unexpected managed line %q", ErrManagedMarkers, ln)
		}
		entryText := ln[len("- ["):]
		id, after, ok := strings.Cut(entryText, "]")
		if !ok {
			return nil, fmt.Errorf("%w: malformed entry %q", ErrManagedMarkers, entryText)
		}
		if !strings.HasPrefix(after, "(") || !strings.HasSuffix(after, ")") {
			return nil, fmt.Errorf("%w: malformed entry link %q", ErrManagedMarkers, after)
		}
		link := after[1 : len(after)-1]
		e := ManagedEntry{ID: id, Link: link}
		if i+1 < len(raw) {
			next := strings.TrimSpace(raw[i+1])
			next = strings.TrimPrefix(next, "—")
			next = strings.TrimPrefix(next, "-")
			if trimmed := strings.TrimSpace(next); trimmed != "" && !strings.HasPrefix(trimmed, "- [") {
				e.Summary = trimmed
				i++
			}
		}
		entries = append(entries, e)
	}
	return entries, nil
}

// renderManagedMarkdown rebuilds MEMORY.md content preserving user notes
// (Head + Tail) verbatim and replacing the managed section. When the file had
// no managed section, the managed section is appended after existing notes.
// A single trailing newline is ensured so the file stays git-friendly.
func renderManagedMarkdown(idx *MemoryIndex) string {
	managed := managedSectionContent(idx.Managed)
	var b strings.Builder
	if strings.TrimSpace(idx.Head) != "" {
		b.WriteString(idx.Head)
		if !strings.HasSuffix(idx.Head, "\n") {
			b.WriteByte('\n')
		}
	}
	b.WriteString(managed)
	if strings.TrimSpace(idx.Tail) != "" {
		if !strings.HasSuffix(managed, "\n") {
			b.WriteByte('\n')
		}
		b.WriteString(idx.Tail)
	}
	out := b.String()
	if out != "" && !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return out
}

// BuildManagedIndexReplacing applies additions and removals to the active
// managed view. Record files remain immutable; removing an entry only makes
// the old record an orphan so provenance is retained without continuing to
// inject a retired or superseded conclusion.
//
// Ordering is meaningful because the reminder budget truncates the tail: existing
// entries keep their position (so a manual reorder of MEMORY.md survives) and
// genuinely new entries are prepended, which keeps the most recent learning
// inside the injected prefix. An entry restated in this batch is updated in
// place rather than moved to the front.
func BuildManagedIndexReplacing(existing *MemoryIndex, entries []ManagedEntry, removeIDs []string) (string, error) {
	removed := make(map[string]bool, len(removeIDs))
	for _, id := range removeIDs {
		if !ValidateRecordID(id) {
			return "", fmt.Errorf("invalid managed removal id=%q", id)
		}
		removed[id] = true
	}
	incoming := make(map[string]ManagedEntry, len(entries))
	incomingOrder := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.ID == "" || !ValidateRecordID(e.ID) || e.Link == "" {
			return "", fmt.Errorf("invalid managed entry id=%q link=%q", e.ID, e.Link)
		}
		if _, dup := incoming[e.ID]; !dup {
			incomingOrder = append(incomingOrder, e.ID)
		}
		incoming[e.ID] = e
	}
	placed := make(map[string]bool, len(existing.Managed)+len(entries))
	kept := make([]ManagedEntry, 0, len(existing.Managed))
	for _, e := range existing.Managed {
		if placed[e.ID] {
			continue
		}
		placed[e.ID] = true
		if replacement, ok := incoming[e.ID]; ok {
			// Restated in this batch: update in place. An addition wins over a
			// removal of the same ID.
			kept = append(kept, replacement)
			continue
		}
		if removed[e.ID] {
			continue
		}
		kept = append(kept, e)
	}
	fresh := make([]ManagedEntry, 0, len(incomingOrder))
	for _, id := range incomingOrder {
		if placed[id] {
			continue
		}
		placed[id] = true
		fresh = append(fresh, incoming[id])
	}
	idx := &MemoryIndex{
		Head:             existing.Head,
		Tail:             existing.Tail,
		Managed:          append(fresh, kept...),
		HasManagedMarker: true,
	}
	return renderManagedMarkdown(idx), nil
}
