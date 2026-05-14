package sessionimport

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveImportInputPath_CodexByID(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "2026", "05", "07", "rollout-1.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"timestamp":"2026-01-01T00:00:00Z","item":{"session_id":"sess-1","role":"user","content":"hi"}}`+"\n"), 0o600); err != nil {
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
	if err := os.WriteFile(rollout, []byte(`{"timestamp":"2026-01-01T00:00:00Z","item":{"session_id":"sess-1","role":"user","content":"hi"}}`+"\n"), 0o600); err != nil {
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
