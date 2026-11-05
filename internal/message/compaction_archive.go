package message

import (
	"strconv"
	"strings"
)

// The archive map is the one region of a compaction checkpoint that names the
// archived history files the checkpoint points at. Its shape is fixed by the
// compaction writer:
//
//	Archived history files (read the matching file with the read tool to recover exact details; paths accept ~ shorthand):
//	- /path/to/session/history-7.md: goal, constraints
//	Each archive begins with a short message-segment index (after its header): ...
//
// The map is also the only place a reader may learn which archive a checkpoint
// refers to: the summary body may mention paths for other reasons, and the
// omission note may name a range of files that were not listed at all.
const (
	// CompactionArchiveMapHeading introduces the archive map inside a
	// compaction checkpoint. The current writer follows it with a parenthesized
	// note; checkpoints written before that note was added end the heading at
	// the colon instead. The prefix is deliberately shared with fork's path
	// relocation so both readers recognise the same region of a restored
	// session. It must still begin at column 0.
	CompactionArchiveMapHeading = "Archived history files"
	// CompactionArchiveMapEnd closes the archive map: the line after the
	// entries explains how to read an archive. Older checkpoints have no such
	// line; then the map runs to the end of the checkpoint, but only entries
	// that name an archive are accepted.
	CompactionArchiveMapEnd = "Each archive begins"
	// CompactionArchivePrefix and CompactionArchiveSuffix name one archived
	// history file: history-<number>.md.
	CompactionArchivePrefix = "history-"
	CompactionArchiveSuffix = ".md"
	// CompactionArchiveOmissionPrefix introduces the note that names the
	// archives a bounded map left out. It is written as a "- " entry too, so
	// the parser must skip it explicitly rather than rely on its file name
	// failing to match.
	CompactionArchiveOmissionPrefix = "("
)

// CompactionArchiveRef is one archived history file that a compaction
// checkpoint explicitly listed in its archive map.
type CompactionArchiveRef struct {
	// Index is the numeric id of the archive: the file is history-<Index>.md.
	// The number is the compaction attempt number, not the count of successful
	// compactions, so it must never be used as a position in a snapshot list.
	Index int
	// Path is the path exactly as the checkpoint listed it.
	Path string
	// Topics is the optional topic list the checkpoint appended to the path.
	Topics string
}

// ParseCompactionArchiveRefs returns the archive references a compaction
// checkpoint explicitly lists in its archive map.
//
// Only the "- " entries of the map count. The map may also carry an omission
// note such as "(3 earlier archives omitted, from history-1.md to history-3.md
// in the same directory)": those files were not listed, so they are not
// references and their numbers are never filled in. Text outside the map —
// including a path that merely appears in the summary body — is ignored, so a
// checkpoint cannot make a reader follow a location it did not list.
//
// Duplicate references to the same archive are collapsed: the map lists one
// entry per archive, and a repeated entry names the same archive.
func ParseCompactionArchiveRefs(content string) []CompactionArchiveRef {
	if content == "" {
		return nil
	}
	var refs []CompactionArchiveRef
	seen := make(map[int]bool)
	inMap := false
	for line := range strings.SplitSeq(content, "\n") {
		if !inMap {
			if strings.HasPrefix(line, CompactionArchiveMapHeading) {
				inMap = true
			}
			continue
		}
		if strings.HasPrefix(line, CompactionArchiveMapEnd) {
			break
		}
		entry, ok := strings.CutPrefix(line, "- ")
		if !ok {
			// The omission note and the blank lines around the entries are
			// not references.
			continue
		}
		ref, ok := parseCompactionArchiveRef(entry)
		if !ok || seen[ref.Index] {
			continue
		}
		seen[ref.Index] = true
		refs = append(refs, ref)
	}
	return refs
}

// parseCompactionArchiveRef reads one archive map entry. The entry is a path
// with an optional ": topics" suffix; only a path whose file name is
// history-<number>.md names an archive.
func parseCompactionArchiveRef(entry string) (CompactionArchiveRef, bool) {
	entry = strings.TrimSpace(entry)
	if entry == "" {
		return CompactionArchiveRef{}, false
	}
	// A bounded map writes the omission note as a bullet too, for example
	// "- (3 earlier archives omitted, from history-1.md to history-3.md ...)".
	// Those files were not listed, so the note is never a reference and its
	// numbers are never filled in — even if its wording later ends in an
	// archive-looking file name.
	if strings.HasPrefix(entry, CompactionArchiveOmissionPrefix) {
		return CompactionArchiveRef{}, false
	}
	path := entry
	topics := ""
	if before, after, found := strings.Cut(entry, ": "); found {
		path = strings.TrimSpace(before)
		topics = strings.TrimSpace(after)
	}
	index, ok := CompactionArchiveIndex(path)
	if !ok {
		return CompactionArchiveRef{}, false
	}
	return CompactionArchiveRef{Index: index, Path: path, Topics: topics}, true
}

// CompactionArchiveIndex returns the numeric id of the archive a path names.
// The path is matched on its file name only: the map may abbreviate the home
// directory, and a reader resolves the archive against its own session
// directory rather than following the path.
func CompactionArchiveIndex(path string) (int, bool) {
	base := path
	if i := strings.LastIndexAny(path, "/\\"); i >= 0 {
		base = path[i+1:]
	}
	rest, ok := strings.CutPrefix(base, CompactionArchivePrefix)
	if !ok {
		return 0, false
	}
	digits, ok := strings.CutSuffix(rest, CompactionArchiveSuffix)
	if !ok || digits == "" {
		return 0, false
	}
	// The writer numbers archives with %d, so a leading zero is not a file
	// this session could have written. Accepting it would make history-007.md
	// match snapshot 7 and quietly misreport the source.
	if digits[0] == '0' {
		return 0, false
	}
	for _, r := range digits {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	index, err := strconv.Atoi(digits)
	if err != nil || index < 1 {
		return 0, false
	}
	return index, true
}
