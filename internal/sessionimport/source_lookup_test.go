package sessionimport

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveImportInputPath_CodexByID(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "2026", "05", "07", "rollout-1.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	data := `{"timestamp":"2026-05-09T04:43:46Z","type":"session_meta","payload":{"id":"sess-1"}}` + "\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	lookup, err := resolveImportInputPath("codex", "", "sess-1", root)
	if err != nil {
		t.Fatalf("resolveImportInputPath: %v", err)
	}
	if lookup.Path != path {
		t.Fatalf("lookup.Path=%q, want %q", lookup.Path, path)
	}
}

func TestResolveImportInputPath_CodexByID_NewPayloadSchema(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "2026", "05", "09", "rollout-1.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	data := `{"timestamp":"2026-05-09T04:43:46Z","type":"session_meta","payload":{"id":"019e0955-00ce-73a2-bc23-213802de80d6"}}` + "\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	lookup, err := resolveImportInputPath("codex", "", "019e0955-00ce-73a2-bc23-213802de80d6", root)
	if err != nil {
		t.Fatalf("resolveImportInputPath: %v", err)
	}
	if lookup.Path != path {
		t.Fatalf("lookup.Path=%q, want %q", lookup.Path, path)
	}
}

func TestResolveImportInputPath_ClaudeByID(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "proj", "sess-1.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"uuid":"u1","message":{"role":"user","content":[{"type":"text","text":"hi"}]}}`+"\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	lookup, err := resolveImportInputPath("claude", "", "sess-1", root)
	if err != nil {
		t.Fatalf("resolveImportInputPath: %v", err)
	}
	if lookup.Path != path {
		t.Fatalf("lookup.Path=%q, want %q", lookup.Path, path)
	}
}

func TestImport_Codex_ByID(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("CHORD_STATE_DIR", stateDir)
	t.Setenv("CHORD_SESSIONS_DIR", "")

	root := t.TempDir()
	rollout := filepath.Join(root, "2026", "05", "07", "rollout-1.jsonl")
	if err := os.MkdirAll(filepath.Dir(rollout), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	data := `{"timestamp":"2026-05-09T04:43:46Z","type":"session_meta","payload":{"id":"sess-1"}}
{"timestamp":"2026-05-09T04:43:47Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}}
`
	if err := os.WriteFile(rollout, []byte(data), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	projectRoot := t.TempDir()
	res, err := Import(context.Background(), ImportOptions{Source: "codex", SourceID: "sess-1", SourceRoot: root, ProjectRoot: projectRoot})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if res.Messages != 1 {
		t.Fatalf("Messages=%d, want 1", res.Messages)
	}
}

func TestResolveImportInputPath_CodexByID_ForkedSubagent(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "2026", "09", "08")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Parent thread: its own session_meta with id == parent.
	parent := filepath.Join(dir, "rollout-2026-09-08T19-12-39-parent.jsonl")
	parentData := `{"timestamp":"2026-09-08T19:12:39Z","type":"session_meta","payload":{"id":"parent","session_id":"parent"}}` + "\n"
	if err := os.WriteFile(parent, []byte(parentData), 0o600); err != nil {
		t.Fatalf("write parent: %v", err)
	}

	// Forked sub-agent thread: its own first session_meta carries id == child
	// but session_id/forked_from_id point back at the parent, and it then
	// replays the parent's session_meta on the next line (real codex fork
	// behavior).
	child := filepath.Join(dir, "rollout-2026-09-08T19-23-24-child.jsonl")
	childData := `{"timestamp":"2026-09-08T19:23:24Z","type":"session_meta","payload":{"id":"child","session_id":"parent","forked_from_id":"parent","thread_source":"subagent"}}
{"timestamp":"2026-09-08T19:12:39Z","type":"session_meta","payload":{"id":"parent","session_id":"parent"}}
`
	if err := os.WriteFile(child, []byte(childData), 0o600); err != nil {
		t.Fatalf("write child: %v", err)
	}

	// The parent id must resolve to only the parent file, even though the
	// child rollout also carries that id in its session_id field and in a
	// replayed session_meta line.
	lookup, err := resolveImportInputPath("codex", "", "parent", root)
	if err != nil {
		t.Fatalf("resolve parent: %v", err)
	}
	if lookup.Path != parent {
		t.Fatalf("parent lookup.Path=%q, want %q", lookup.Path, parent)
	}

	// The child id resolves to the child file only.
	lookup, err = resolveImportInputPath("codex", "", "child", root)
	if err != nil {
		t.Fatalf("resolve child: %v", err)
	}
	if lookup.Path != child {
		t.Fatalf("child lookup.Path=%q, want %q", lookup.Path, child)
	}
}

// TestResolveImportInputPath_CodexByID_UnreadableFirstMetaScansOn verifies
// that a session_meta whose payload cannot be read does not abort the lookup:
// the first line may be truncated or corrupted while a later session_meta
// still carries the rollout's id, and skipping it must not mark the file as a
// non-match. Only the first *readable* session_meta governs the fork rule, so
// a readable mismatch still stops the scan.
func TestResolveImportInputPath_CodexByID_UnreadableFirstMetaScansOn(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "2026", "09", "09", "rollout-1.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	data := `{"timestamp":"2026-09-09T01:02:03Z","type":"session_meta","payload":"truncated"}
{"timestamp":"2026-09-09T01:02:04Z","type":"session_meta","payload":{"id":"sess-2"}}
` + "\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	lookup, err := resolveImportInputPath("codex", "", "sess-2", root)
	if err != nil {
		t.Fatalf("resolveImportInputPath: %v", err)
	}
	if lookup.Path != path {
		t.Fatalf("lookup.Path=%q, want %q", lookup.Path, path)
	}

	// A payload-less first meta line must behave the same way.
	path2 := filepath.Join(root, "2026", "09", "09", "rollout-2.jsonl")
	data2 := `{"timestamp":"2026-09-09T01:02:05Z","type":"session_meta"}
{"timestamp":"2026-09-09T01:02:06Z","type":"session_meta","payload":{"id":"sess-3"}}
` + "\n"
	if err := os.WriteFile(path2, []byte(data2), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	lookup, err = resolveImportInputPath("codex", "", "sess-3", root)
	if err != nil {
		t.Fatalf("resolveImportInputPath: %v", err)
	}
	if lookup.Path != path2 {
		t.Fatalf("lookup.Path=%q, want %q", lookup.Path, path2)
	}

	// The unreadable first meta must not change the fork rule for a readable
	// one: a mismatch on the first readable meta still stops the scan, so a
	// replay line for a different id cannot cause a false match.
	path3 := filepath.Join(root, "2026", "09", "09", "rollout-3.jsonl")
	data3 := `{"timestamp":"2026-09-09T01:02:07Z","type":"session_meta","payload":{"id":"own"}}
{"timestamp":"2026-09-09T01:02:08Z","type":"session_meta","payload":{"id":"parent"}}
` + "\n"
	if err := os.WriteFile(path3, []byte(data3), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := resolveImportInputPath("codex", "", "parent", root); err == nil {
		t.Fatal("expected the readable first meta to stop the scan before the replay line")
	}
}

// TestResolveImportInputPath_CodexByID_NoMatchErrorGuides verifies that a
// failed codex id lookup names the searched root and points the user at
// `codex resume` and the explicit-file-path alternative, matching the
// guidance style of the other import errors.
func TestResolveImportInputPath_CodexByID_NoMatchErrorGuides(t *testing.T) {
	root := t.TempDir()
	_, err := resolveImportInputPath("codex", "", "missing-id", root)
	if err == nil {
		t.Fatal("expected lookup failure for an unknown codex id")
	}
	for _, want := range []string{root, "codex resume missing-id", "explicit file path"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q is missing guidance %q", err.Error(), want)
		}
	}
}
