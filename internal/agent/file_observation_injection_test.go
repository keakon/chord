package agent

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/filectx"
)

// The new coverage attributes must not change which injected parts count as a
// complete, disk-identical read: raw CRLF bytes still qualify, while an
// explicit range or a budget-truncated body never does.
func TestTrackObservedFilePartsWithLineMetadata(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	crlf := filepath.Join(root, "crlf.txt")
	ranged := filepath.Join(root, "ranged.txt")
	big := filepath.Join(root, "big.txt")
	writeTestFile(t, crlf, "one\r\ntwo\r\n")
	writeTestFile(t, ranged, "one\ntwo\nthree\n")
	writeTestFile(t, big, strings.Repeat("abcdefghij\n", 64))

	a := newTestMainAgent(t, root)
	resolve := func(path string) string { return filepath.Join(root, path) }
	parts := filectx.BuildFileRefParts([]filectx.FileRef{
		{Path: "crlf.txt"},
		{Path: "ranged.txt", Lines: filectx.LineRange{Start: 2, End: 3}},
	}, resolve)
	if len(parts) != 2 {
		t.Fatalf("parts = %d, want 2", len(parts))
	}
	if !strings.Contains(parts[0].Text, `covered_lines="1-2" total_lines="2"`) {
		t.Fatalf("CRLF reference = %q", parts[0].Text)
	}
	truncated := filectx.BuildFileRefPartsWithOptions(
		[]filectx.FileRef{{Path: "big.txt"}}, resolve,
		filectx.BuildFilePartsOptions{MaxFileBytes: 40},
	)
	if len(truncated.Parts) != 1 || !strings.Contains(truncated.Parts[0].Text, `truncated="true"`) {
		t.Fatalf("truncated reference = %#v", truncated.Parts)
	}
	a.trackObservedFileParts(append(parts, truncated.Parts...))

	if observation := a.fileTrack.Observation(crlf, a.instanceID, computeFileHash(crlf)); !observation.Observed || !observation.Current {
		t.Fatalf("complete CRLF body lost its full-read observation: %+v", observation)
	}
	for _, path := range []string{ranged, big} {
		if observation := a.fileTrack.Observation(path, a.instanceID, computeFileHash(path)); observation.Observed {
			t.Fatalf("partial body %q was recorded as a complete observation: %+v", path, observation)
		}
	}
}
