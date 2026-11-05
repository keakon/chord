package message

import (
	"strings"
	"testing"
)

// compactionArchiveMapBlock renders the archive map region the way the
// compaction checkpoint writer does: a heading line, the entry lines, and the
// closing line that explains how to read an archive. The caller supplies the
// entry lines verbatim so a test can place a line that is not a "- " entry
// (the omission note) inside the region.
func compactionArchiveMapBlock(lines ...string) string {
	var sb strings.Builder
	sb.WriteString("Archived history files (read the matching file with the read tool to recover exact details; paths accept ~ shorthand):\n")
	for _, line := range lines {
		sb.WriteString(line)
		sb.WriteByte('\n')
	}
	sb.WriteString(compactionArchiveMapEndLine)
	return sb.String()
}

// compactionArchiveMapEndLine is the line that closes the archive map, copied
// from the checkpoint writer so the tests exercise the same end marker the
// production content carries.
const compactionArchiveMapEndLine = "Each archive begins with a short message-segment index (after its header): read the index first, then read only the line ranges you need — a single read is capped around 2000 lines, so slice by the index instead of reading whole files.\n"

// assertArchiveRefs compares the parsed references against the expectation.
func assertArchiveRefs(t *testing.T, got, want []CompactionArchiveRef) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("refs = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ref[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestParseCompactionArchiveRefs(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    []CompactionArchiveRef
	}{
		{
			name:    "single archive entry",
			content: compactionArchiveMapBlock("- /abs/path/history-7.md"),
			want:    []CompactionArchiveRef{{Index: 7, Path: "/abs/path/history-7.md"}},
		},
		{
			name:    "entry with topics",
			content: compactionArchiveMapBlock("- /abs/path/history-7.md: goal, constraints"),
			want:    []CompactionArchiveRef{{Index: 7, Path: "/abs/path/history-7.md", Topics: "goal, constraints"}},
		},
		{
			name: "multiple entries keep the listed order",
			content: compactionArchiveMapBlock(
				"- /abs/path/history-3.md: goal",
				"- /abs/path/history-7.md: constraints",
				"- /abs/path/history-9.md",
			),
			want: []CompactionArchiveRef{
				{Index: 3, Path: "/abs/path/history-3.md", Topics: "goal"},
				{Index: 7, Path: "/abs/path/history-7.md", Topics: "constraints"},
				{Index: 9, Path: "/abs/path/history-9.md"},
			},
		},
		{
			name:    "home shorthand path",
			content: compactionArchiveMapBlock("- ~/.chord/sessions/s-1/history-12.md"),
			want:    []CompactionArchiveRef{{Index: 12, Path: "~/.chord/sessions/s-1/history-12.md"}},
		},
		{
			name:    "omitted range is not expanded",
			content: compactionArchiveMapBlock("- (3 earlier archives omitted, from history-1.md to history-3.md in the same directory)"),
			want:    nil,
		},
		{
			name:    "single omitted archive is not expanded",
			content: compactionArchiveMapBlock("- (1 earlier archive omitted: history-1.md in the same directory)"),
			want:    nil,
		},
		{
			name: "omitted range without the bullet prefix is not expanded",
			content: compactionArchiveMapBlock(
				"(3 earlier archives omitted, from history-1.md to history-3.md in the same directory)",
				"- /abs/path/history-7.md",
			),
			want: []CompactionArchiveRef{{Index: 7, Path: "/abs/path/history-7.md"}},
		},
		{
			name: "omitted range with absolute paths is not expanded",
			content: compactionArchiveMapBlock(
				"- (3 earlier archives omitted, from /abs/path/history-1.md to /abs/path/history-3.md in the same directory)",
			),
			want: nil,
		},
		{
			name: "omission bullet ending in an archive name is still not a reference",
			content: compactionArchiveMapBlock(
				"- (3 earlier archives omitted) /abs/path/history-3.md",
			),
			want: nil,
		},
		{
			name: "summary body paths are not references",
			content: "[Context Summary]\n" +
				"## Files and Evidence\n" +
				"- docs/architecture/context-management.md\n" +
				"- /other/session/history-9.md\n" +
				"\n" +
				"[Context compressed]\n" +
				"Earlier conversation was compacted into the summary above.\n" +
				compactionArchiveMapBlock("- /abs/path/history-7.md"),
			want: []CompactionArchiveRef{{Index: 7, Path: "/abs/path/history-7.md"}},
		},
		{
			name: "entries before the heading are not references",
			content: "- /abs/path/history-5.md\n" +
				compactionArchiveMapBlock("- /abs/path/history-7.md"),
			want: []CompactionArchiveRef{{Index: 7, Path: "/abs/path/history-7.md"}},
		},
		{
			name: "entries after the map closes are not references",
			content: compactionArchiveMapBlock("- /abs/path/history-7.md") +
				"- /abs/path/history-8.md\n",
			want: []CompactionArchiveRef{{Index: 7, Path: "/abs/path/history-7.md"}},
		},
		{
			name: "non archive file names are ignored",
			content: compactionArchiveMapBlock(
				"- /abs/path/notes.md",
				"- /abs/path/history-.md",
				"- /abs/path/history-abc.md",
				"- /abs/path/history-0.md",
			),
			want: nil,
		},
		{
			name: "duplicate index keeps the first entry only",
			content: compactionArchiveMapBlock(
				"- /abs/path/history-7.md: goal",
				"- /other/path/history-7.md: constraints",
			),
			want: []CompactionArchiveRef{{Index: 7, Path: "/abs/path/history-7.md", Topics: "goal"}},
		},
		{
			name:    "empty content",
			content: "",
			want:    nil,
		},
		{
			name:    "content without an archive map",
			content: "[Context Summary]\n## Goal\n- keep the summary\n\n[Context compressed]\n",
			want:    nil,
		},
		{
			name:    "indented heading does not open the map",
			content: "  Archived history files (read the matching file):\n- /abs/path/history-7.md\n",
			want:    nil,
		},
		{
			name:    "heading mentioned mid line does not open the map",
			content: "see Archived history files (read the matching file):\n- /abs/path/history-7.md\n",
			want:    nil,
		},
		{
			name:    "legacy heading without the opening parenthesis still opens the map",
			content: "Archived history files:\n- /abs/path/history-7.md\n",
			want:    []CompactionArchiveRef{{Index: 7, Path: "/abs/path/history-7.md"}},
		},
		{
			name:    "map without the closing line still yields its entries",
			content: "Archived history files (read the matching file):\n- /abs/path/history-7.md\n",
			want:    []CompactionArchiveRef{{Index: 7, Path: "/abs/path/history-7.md"}},
		},
		{
			name:    "entry with trailing whitespace keeps the trimmed path",
			content: compactionArchiveMapBlock("-   /abs/path/history-7.md   "),
			want:    []CompactionArchiveRef{{Index: 7, Path: "/abs/path/history-7.md"}},
		},
		{
			name:    "bullet without a space is not an entry",
			content: compactionArchiveMapBlock("-/abs/path/history-7.md"),
			want:    nil,
		},
		{
			name:    "bare archive name without a bullet is not an entry",
			content: compactionArchiveMapBlock("/abs/path/history-7.md"),
			want:    nil,
		},
		{
			name:    "crlf line endings are tolerated",
			content: "Archived history files (read the matching file):\r\n- /abs/path/history-7.md: goal, constraints\r\nEach archive begins with a short index.\r\n",
			want:    []CompactionArchiveRef{{Index: 7, Path: "/abs/path/history-7.md", Topics: "goal, constraints"}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assertArchiveRefs(t, ParseCompactionArchiveRefs(tc.content), tc.want)
		})
	}
}

// TestParseCompactionArchiveRefsReadsRealCheckpointShape parses a checkpoint
// in the exact shape the compaction writer emits: the summary body, the
// wrapper, the archive map and the closing line.
func TestParseCompactionArchiveRefsReadsRealCheckpointShape(t *testing.T) {
	content := "[Context Summary]\n" +
		"## Goal\n" +
		"- keep the importer\n" +
		"\n" +
		"## Files and Evidence\n" +
		"- docs/architecture/context-management.md\n" +
		"\n" +
		"[Context compressed]\n" +
		"Earlier conversation was compacted into the summary above.\n" +
		"Archived history files (read the matching file with the read tool to recover exact details; paths accept ~ shorthand):\n" +
		"- ~/.chord/sessions/s-1/history-4.md: goal, constraints\n" +
		"- ~/.chord/sessions/s-1/history-6.md: progress\n" +
		"Each archive begins with a short message-segment index (after its header): read the index first, then read only the line ranges you need — a single read is capped around 2000 lines, so slice by the index instead of reading whole files.\n"

	assertArchiveRefs(t, ParseCompactionArchiveRefs(content), []CompactionArchiveRef{
		{Index: 4, Path: "~/.chord/sessions/s-1/history-4.md", Topics: "goal, constraints"},
		{Index: 6, Path: "~/.chord/sessions/s-1/history-6.md", Topics: "progress"},
	})
}

func TestCompactionArchiveIndex(t *testing.T) {
	tests := []struct {
		name      string
		path      string
		wantIndex int
		wantOK    bool
	}{
		{name: "bare file name", path: "history-7.md", wantIndex: 7, wantOK: true},
		{name: "absolute path", path: "/a/b/history-12.md", wantIndex: 12, wantOK: true},
		{name: "home shorthand path", path: "~/x/history-3.md", wantIndex: 3, wantOK: true},
		{name: "windows separator path", path: `C:\sessions\history-5.md`, wantIndex: 5, wantOK: true},
		{name: "wrong suffix", path: "history-7.jsonl"},
		{name: "missing number", path: "history-.md"},
		{name: "non numeric number", path: "history-abc.md"},
		{name: "zero index", path: "history-0.md"},
		{name: "leading zero index is not a writer-generated file", path: "history-007.md"},
		{name: "empty path", path: ""},
		{name: "unrelated file name", path: "notes.md"},
		{name: "overflowing number does not panic", path: "history-99999999999999999999.md"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			index, ok := CompactionArchiveIndex(tc.path)
			if ok != tc.wantOK {
				t.Fatalf("CompactionArchiveIndex(%q) ok = %t, want %t", tc.path, ok, tc.wantOK)
			}
			if index != tc.wantIndex {
				t.Fatalf("CompactionArchiveIndex(%q) index = %d, want %d", tc.path, index, tc.wantIndex)
			}
		})
	}
}
