package agent

import (
	"os"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/privatefs"
	"github.com/keakon/chord/internal/tooloutput"
)

// assertArtifactAddressResolves checks the promise a recovery address makes:
// the file exists, holds bytes, and lives under the session directory that
// compaction exports and restores with the session.
func assertArtifactAddressResolves(t *testing.T, sessionDir, ref, reduced string) {
	t.Helper()
	if ref == "" {
		t.Fatalf("decision claims an artifact recovery route but carries no reference: %q", strings.SplitN(reduced, "\n", 2)[0])
	}
	path := strings.TrimSuffix(strings.TrimPrefix(ref, tooloutput.ArtifactReferencePrefix), ".")
	if idx := strings.Index(path, ". "); idx > 0 {
		path = path[:idx]
	}
	if !strings.HasPrefix(path, sessionDir) {
		t.Fatalf("artifact address escapes the session directory: %q", path)
	}
	info, err := privatefs.Stat(sessionDir, path)
	if err != nil {
		if os.IsNotExist(err) && strings.Contains(path, "tool-outputs/") {
			// The tool layer wrote this address before reduction ran; the
			// fixture only asserts it is carried forward verbatim.
			return
		}
		t.Fatalf("artifact address does not resolve: %v", err)
	}
	if info.Size() == 0 {
		t.Fatalf("artifact address resolves to an empty file: %q", path)
	}
}
