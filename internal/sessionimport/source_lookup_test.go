package sessionimport

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// codexTestUUID returns a valid UUID-shaped id for file names and meta
// payloads; n fills the final segment so distinct ids stay sortable.
func codexTestUUID(n int) string {
	return fmt.Sprintf("019e0000-0000-7000-8000-%012x", n)
}

func TestResolveImportInputPath_CodexByID(t *testing.T) {
	root := t.TempDir()
	id := codexTestUUID(1)
	path := filepath.Join(root, "2026", "05", "07", "rollout-2026-05-07T04-43-46-"+id+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	data := `{"timestamp":"2026-05-07T04:43:46Z","ordinal":0,"type":"session_meta","payload":{"id":"` + id + `"}}` + "\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	lookup, err := resolveImportInputPath("codex", "", id, root)
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

// TestResolveImportInputPath_ClaudeByID_MultiMatchListsCandidates verifies the
// multiple-match error names every matching file so the user can pick one.
func TestResolveImportInputPath_ClaudeByID_MultiMatchListsCandidates(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "proj-a", "sess-1.jsonl")
	second := filepath.Join(root, "proj-b", "sess-1.jsonl")
	for _, path := range []string{first, second} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte(`{"uuid":"u1","message":{"role":"user","content":[{"type":"text","text":"hi"}]}}`+"\n"), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	_, err := resolveImportInputPath("claude", "", "sess-1", root)
	if err == nil {
		t.Fatal("expected a multiple-match error")
	}
	for _, want := range []string{first, second} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not list candidate %q", err.Error(), want)
		}
	}
}

func TestImport_Codex_ByID(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("CHORD_STATE_DIR", stateDir)
	t.Setenv("CHORD_SESSIONS_DIR", "")

	root := t.TempDir()
	id := codexTestUUID(1)
	rollout := filepath.Join(root, "2026", "05", "07", "rollout-2026-05-07T04-43-46-"+id+".jsonl")
	if err := os.MkdirAll(filepath.Dir(rollout), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	data := `{"timestamp":"2026-05-07T04:43:46Z","ordinal":0,"type":"session_meta","payload":{"id":"` + id + `"}}
{"timestamp":"2026-05-07T04:43:47Z","ordinal":1,"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}}
`
	if err := os.WriteFile(rollout, []byte(data), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	projectRoot := t.TempDir()
	res, err := Import(context.Background(), ImportOptions{Source: "codex", SourceID: id, SourceRoot: root, ProjectRoot: projectRoot})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if res.Messages != 1 {
		t.Fatalf("Messages=%d, want 1", res.Messages)
	}
}

// TestFindCodexRolloutByID_NewestFileWins verifies the codex --id rule for
// threads whose id spans several rollout pages: the newest file name
// timestamp wins, ties break by the greater rollout id, and a bare rollout id
// is not an --id target (only thread ids are, matching codex resume).
func TestFindCodexRolloutByID_NewestFileWins(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "2026", "09", "08")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	threadID := codexTestUUID(1)
	older := filepath.Join(dir, "rollout-2026-09-08T19-12-39-"+threadID+".jsonl")
	sameSecondLower := filepath.Join(dir, "rollout-2026-09-08T19-23-24-"+threadID+".jsonl")
	sameSecondHigher := filepath.Join(dir, "rollout-2026-09-08T19-23-24-"+threadID+"_"+codexTestUUID(2)+".jsonl")
	for _, path := range []string{older, sameSecondLower, sameSecondHigher} {
		if err := os.WriteFile(path, []byte(""), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	path, err := findCodexRolloutByID(root, threadID)
	if err != nil {
		t.Fatalf("findCodexRolloutByID: %v", err)
	}
	if path != sameSecondHigher {
		t.Fatalf("path=%q, want the newest page %q (same-second tie broken by rollout id)", path, sameSecondHigher)
	}

	// A rollout id is not an --id target: codex resume resolves thread ids.
	if _, err := findCodexRolloutByID(root, codexTestUUID(2)); err == nil {
		t.Fatal("expected a rollout id not to match any --id lookup")
	}
}

// TestFindCodexRolloutByID_CompressedNewestRejected verifies a compressed
// newest page is rejected instead of silently falling back to an older page.
func TestFindCodexRolloutByID_CompressedNewestRejected(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "2026", "09", "08")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	threadID := codexTestUUID(1)
	older := filepath.Join(dir, "rollout-2026-09-08T19-12-39-"+threadID+".jsonl")
	if err := os.WriteFile(older, []byte(""), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	compressed := filepath.Join(dir, "rollout-2026-09-08T19-23-24-"+threadID+".jsonl.zst")
	if err := os.WriteFile(compressed, []byte(""), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if _, err := findCodexRolloutByID(root, threadID); err == nil {
		t.Fatal("expected the compressed newest page to be rejected")
	} else if !strings.Contains(err.Error(), "compressed") {
		t.Fatalf("error %q does not mention compression", err.Error())
	}
}

// TestFindCodexRolloutByID_PlainPreferredOverCompressedTie verifies the same
// page present in both forms resolves to the plain file (codex removes the
// .zst sidecar when it materializes a rollout, so a tie is transient).
func TestFindCodexRolloutByID_PlainPreferredOverCompressedTie(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "2026", "09", "08")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	threadID := codexTestUUID(1)
	base := "rollout-2026-09-08T19-12-39-" + threadID
	plain := filepath.Join(dir, base+".jsonl")
	if err := os.WriteFile(plain, []byte(""), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, base+".jsonl.zst"), []byte(""), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	path, err := findCodexRolloutByID(root, threadID)
	if err != nil {
		t.Fatalf("findCodexRolloutByID: %v", err)
	}
	if path != plain {
		t.Fatalf("path=%q, want the plain file %q", path, plain)
	}
}

func TestParseCodexRolloutFileName(t *testing.T) {
	threadID := codexTestUUID(1)
	rolloutID := codexTestUUID(2)
	cases := []struct {
		name      string
		want      bool
		thread    string
		rollout   string
		timestamp string
		compress  bool
	}{
		{name: "rollout-2026-09-24T02-43-16-" + threadID + ".jsonl", want: true, thread: threadID, rollout: threadID, timestamp: "2026-09-24T02-43-16"},
		{name: "rollout-2026-09-24T02-43-16-" + threadID + "_" + rolloutID + ".jsonl", want: true, thread: threadID, rollout: rolloutID, timestamp: "2026-09-24T02-43-16"},
		{name: "rollout-2026-09-24T02-43-16-" + threadID + "_" + rolloutID + ".jsonl.zst", want: true, thread: threadID, rollout: rolloutID, timestamp: "2026-09-24T02-43-16", compress: true},
		{name: "rollout-1.jsonl"},
		{name: "rollout-2026-09-24T02-43-16-not-a-uuid.jsonl"},
		{name: "rollout-2026-99-99T99-99-99-" + threadID + ".jsonl"},
		{name: "rollout-2026-09-24T02-43-16-" + threadID + "-extra.jsonl"},
		{name: "rollout-2026-09-24T02-43-16-" + threadID + "_.jsonl"},
		{name: "rollout-2026-09-24T02-43-16-" + threadID + "_" + rolloutID},
		{name: "other-2026-09-24T02-43-16-" + threadID + ".jsonl"},
	}
	for _, tc := range cases {
		parsed, ok := parseCodexRolloutFileName(tc.name)
		if ok != tc.want {
			t.Fatalf("parseCodexRolloutFileName(%q) ok=%v, want %v", tc.name, ok, tc.want)
		}
		if !ok {
			continue
		}
		if parsed.ThreadID != tc.thread || parsed.RolloutID != tc.rollout || parsed.Timestamp != tc.timestamp || parsed.Compressed != tc.compress {
			t.Fatalf("parseCodexRolloutFileName(%q) = %+v, want thread=%q rollout=%q timestamp=%q compressed=%v", tc.name, parsed, tc.thread, tc.rollout, tc.timestamp, tc.compress)
		}
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
