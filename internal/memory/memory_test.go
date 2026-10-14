package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
)

func writeProjectFile(t *testing.T, projectRoot, rel, content string) {
	t.Helper()
	path := filepath.Join(projectRoot, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func testCandidate(kind Type, statement, summary string) Candidate {
	return Candidate{
		Type:        kind,
		Statement:   statement,
		Rationale:   "This learning is not otherwise visible from the repository.",
		Application: "Apply it when the same situation appears in a future session.",
		Summary:     summary,
		SourceRole:  SourceRoleUser,
		Confidence:  ConfidenceUserStated,
		Outcome:     OutcomeSuccess,
	}
}

// extractionOf wraps candidates as a commit payload for tests that only exercise
// the addition path.
func extractionOf(candidates ...Candidate) *ExtractionOutput {
	return &ExtractionOutput{Candidates: candidates}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("mode(%s) = %04o, want %04o", path, got, want)
	}
}

// The project root and an existing MEMORY.md are normal project files: an
// automatic commit must never chmod the project root or overwrite the file's
// existing permissions. Only the new record file gets the default project mode.
// A caller-provided path locator must drive the machine state directory so
// custom paths.state_dir / paths.sessions_dir settings are honored instead of
// re-deriving the default paths.
func TestNewManagerUsesProvidedPathLocator(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(t.TempDir(), "custom-state")
	loc := &config.PathLocator{
		ConfigHome:   filepath.Join(t.TempDir(), "config"),
		StateDir:     stateDir,
		SessionsRoot: filepath.Join(stateDir, "sessions"),
		CacheDir:     filepath.Join(stateDir, "cache"),
		LogsDir:      filepath.Join(stateDir, "logs"),
		ExportsDir:   filepath.Join(stateDir, "exports"),
	}
	m, err := NewManager(root, loc)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if !strings.HasPrefix(m.layout.StateDir, stateDir) {
		t.Fatalf("state dir = %q, want under %q", m.layout.StateDir, stateDir)
	}
	// The default-locator path still works for callers that do not pass one.
	m2, err := NewManager(root)
	if err != nil {
		t.Fatalf("NewManager default: %v", err)
	}
	if m2.layout.StateDir == m.layout.StateDir {
		t.Fatalf("default state dir unexpectedly equals the provided one: %q", m2.layout.StateDir)
	}
}

// outParts unpacks the addition-path fields most parser assertions care about,
// tolerating the nil output a failed parse returns.
func outParts(out *ExtractionOutput) ([]Candidate, []string) {
	if out == nil {
		return nil, nil
	}
	return out.Candidates, out.Dropped
}
