package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/pathutil"
	"github.com/keakon/chord/internal/recovery"
)

// forkFixtureMessage builds a message with plain content for fixtures.
func forkFixtureMessage(role message.Role, content string) message.Message {
	return message.Message{Role: role, Content: content}
}

func forkFixtureToolResult(content string) message.Message {
	return message.Message{Role: message.RoleTool, Content: content, ToolCallID: "call-1", ToolStatus: message.ToolStatusSuccess}
}

func forkFixtureSummary(content string) message.Message {
	return message.Message{Role: message.RoleUser, Content: content, IsCompactionSummary: true}
}

// writeForkFixtureJSONL writes messages as one JSON line each into
// sessionDir/<name>.
func writeForkFixtureJSONL(t *testing.T, sessionDir, name string, msgs []message.Message) {
	t.Helper()
	var sb strings.Builder
	for _, m := range msgs {
		data, err := json.Marshal(m)
		if err != nil {
			t.Fatalf("marshal fixture message: %v", err)
		}
		sb.Write(data)
		sb.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(sessionDir, name), []byte(sb.String()), 0o600); err != nil {
		t.Fatalf("write fixture %s: %v", name, err)
	}
}

// readForkFixtureMessages decodes the message lines of sessionDir/main.jsonl.
func readForkFixtureMessages(t *testing.T, sessionDir string) []message.Message {
	t.Helper()
	lines, err := readJSONLLines(filepath.Join(sessionDir, "main.jsonl"))
	if err != nil {
		t.Fatalf("read forked main.jsonl: %v", err)
	}
	msgs := make([]message.Message, 0, len(lines))
	for _, line := range lines {
		var m message.Message
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("decode forked message: %v", err)
		}
		msgs = append(msgs, m)
	}
	return msgs
}

// forkHistoryStatusPendingApply mirrors the agent-side "pending_apply" state:
// a boundary whose apply has not completed must not be offered for forking.
const forkHistoryStatusPendingApply = "pending_apply"

// writeForkHistoryStatus writes the agent-shaped history-N.status.json record
// for compaction boundary index so the fork scanner can classify the boundary.
func writeForkHistoryStatus(t *testing.T, sessionDir string, index int, status string) {
	t.Helper()
	meta := struct {
		Version     int    `json:"version"`
		HistoryFile string `json:"history_file"`
		Status      string `json:"status"`
	}{Version: 1, HistoryFile: fmt.Sprintf("history-%d.md", index), Status: status}
	data, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, fmt.Sprintf("history-%d.status.json", index)), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestScanForkHistoryBoundaries(t *testing.T) {
	dir := t.TempDir()
	writeForkFixtureJSONL(t, dir, "main.pre-compress-1.jsonl", []message.Message{forkFixtureMessage(message.RoleUser, "a")})
	writeForkFixtureJSONL(t, dir, "main.pre-compress-2.jsonl", []message.Message{forkFixtureMessage(message.RoleUser, "b")})
	writeForkHistoryStatus(t, dir, 1, compactionHistoryStatusApplied)
	writeForkHistoryStatus(t, dir, 2, compactionHistoryStatusApplied)
	if err := os.WriteFile(filepath.Join(dir, "main.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "unrelated.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	bounds, err := scanForkHistoryBoundaries(dir)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(bounds) != 2 || bounds[0].index != 1 || bounds[1].index != 2 {
		t.Fatalf("boundaries = %+v, want [1 2]", bounds)
	}

	// A snapshot without an applied status record is not a forkable boundary:
	// a crash between the pre-compress rename and the applied-status write
	// leaves main.pre-compress-N.jsonl in place while history-N.status.json is
	// missing or still pending.
	writeForkFixtureJSONL(t, dir, "main.pre-compress-3.jsonl", []message.Message{forkFixtureMessage(message.RoleUser, "c")})
	writeForkHistoryStatus(t, dir, 3, forkHistoryStatusPendingApply)
	writeForkFixtureJSONL(t, dir, "main.pre-compress-4.jsonl", []message.Message{forkFixtureMessage(message.RoleUser, "d")})
	if err := os.WriteFile(filepath.Join(dir, "history-4.status.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	bounds, err = scanForkHistoryBoundaries(dir)
	if err != nil {
		t.Fatalf("scan with pending snapshots: %v", err)
	}
	if len(bounds) != 2 || bounds[0].index != 1 || bounds[1].index != 2 {
		t.Fatalf("boundaries with pending/corrupt snapshots = %+v, want [1 2]", bounds)
	}

	if err := os.Remove(filepath.Join(dir, "main.pre-compress-1.jsonl")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "main.pre-compress-2.jsonl")); err != nil {
		t.Fatal(err)
	}
	empty, err := scanForkHistoryBoundaries(dir)
	if err != nil {
		t.Fatalf("scan empty: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("empty scan = %+v, want none", empty)
	}
}

func TestForkBoundaryFromFlag(t *testing.T) {
	for value, want := range map[string]int{
		"":       0,
		"latest": 0,
		"auto":   0,
		" 2 ":    2,
	} {
		got, err := forkBoundaryFromFlag(value)
		if err != nil || got != want {
			t.Errorf("forkBoundaryFromFlag(%q) = %d, %v; want %d", value, got, err, want)
		}
	}
	for _, bad := range []string{"abc", "0", "-1", "2.5"} {
		if _, err := forkBoundaryFromFlag(bad); err == nil {
			t.Errorf("forkBoundaryFromFlag(%q) succeeded, want error", bad)
		}
	}
}

// writeForkSourceFixture creates a two-generation source session:
//
//	gen 1 (pre-compress-1): u1, a1, t1, u2, a2, t2     (no summary card)
//	gen 2 (pre-compress-2): summary card (of gen 1), a2, t2, u3, a3
//
// Both compaction archives history-1.md and history-2.md exist on disk (the
// archive of gen 2 was produced when compaction 2 applied, so it is present
// even though pre-compress-2 predates it).
func writeForkSourceFixture(t *testing.T, sessionDir string) {
	t.Helper()
	writeForkFixtureJSONL(t, sessionDir, "main.pre-compress-1.jsonl", []message.Message{
		forkFixtureMessage(message.RoleUser, "u1"),
		forkFixtureMessage(message.RoleAssistant, "a1"),
		forkFixtureToolResult("t1"),
		forkFixtureMessage(message.RoleUser, "u2"),
		forkFixtureMessage(message.RoleAssistant, "a2"),
		forkFixtureToolResult("t2"),
	})
	writeForkFixtureJSONL(t, sessionDir, "main.pre-compress-2.jsonl", []message.Message{
		forkFixtureSummary("[Context Summary]\nOriginal request: u1\nHistory map: history-1.md"),
		forkFixtureMessage(message.RoleAssistant, "a2"),
		forkFixtureToolResult("t2"),
		forkFixtureMessage(message.RoleUser, "u3"),
		forkFixtureMessage(message.RoleAssistant, "a3"),
	})
	if err := os.WriteFile(filepath.Join(sessionDir, "history-1.md"), []byte("# Index\n- 1: User: u1\n... archived gen 1 ...\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "history-2.md"), []byte("# Index\n- 1: User: [Context Summary]\n... archived gen 2 ...\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeForkHistoryStatus(t, sessionDir, 1, compactionHistoryStatusApplied)
	writeForkHistoryStatus(t, sessionDir, 2, compactionHistoryStatusApplied)
	if err := os.WriteFile(filepath.Join(sessionDir, "main.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func forkContentSequence(msgs []message.Message) string {
	var sb strings.Builder
	for _, m := range msgs {
		sb.WriteString(m.Content)
		sb.WriteString("|")
	}
	return sb.String()
}

// forkDirNames returns the file names in dir, sorted.
func forkDirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	// ReadDir already sorts by name.
	return names
}

func TestForkSessionAtHistoryDefaultLatest(t *testing.T) {
	src := filepath.Join(t.TempDir(), "20260901000000001")
	if err := os.MkdirAll(src, 0o700); err != nil {
		t.Fatal(err)
	}
	writeForkSourceFixture(t, src)

	newDir, chosen, seeded, err := forkSessionAtHistory(src, filepath.Dir(src), 0)
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	if chosen != 2 {
		t.Errorf("chosen = %d, want 2", chosen)
	}
	msgs := readForkFixtureMessages(t, newDir)
	if seeded != len(msgs) {
		t.Fatalf("seeded = %d, forked messages = %d", seeded, len(msgs))
	}
	// Fork 2 copies pre-compress-2 verbatim: the gen-1 summary card is part of
	// that generation's state and must be preserved; earlier raw messages live
	// in history-1.md instead of being stitched into the transcript.
	want := "[Context Summary]\nOriginal request: u1\nHistory map: history-1.md|a2|t2|u3|a3|"
	if got := forkContentSequence(msgs); got != want {
		t.Fatalf("forked sequence = %q, want %q", got, want)
	}
	if !msgs[0].IsCompactionSummary {
		t.Error("fork 2 lost the leading compaction summary card of pre-compress-2")
	}

	// The transcript file itself is copied record-for-record: no re-encoding
	// pass may reorder fields, drop omitempty keys, or reformat numbers.
	gotTranscript, err := os.ReadFile(filepath.Join(newDir, "main.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	wantTranscript, err := os.ReadFile(filepath.Join(src, "main.pre-compress-2.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if string(gotTranscript) != string(wantTranscript) {
		t.Fatalf("fork main.jsonl differs from main.pre-compress-2.jsonl")
	}

	// Archives with an index below the boundary are copied alongside for the
	// history map to read; history-2.md is not — pre-compress-2 still carries
	// that generation's messages inline, so copying it would duplicate content
	// already in the fork's transcript. No other runtime state is carried.
	// session.lock.guard is the residue of the fork-time session lock, as in
	// any real session directory.
	names := forkDirNames(t, newDir)
	wantNames := []string{"history-1.md", "main.jsonl", "session-meta.json", "session.lock.guard"}
	if len(names) != len(wantNames) {
		t.Fatalf("fork dir entries = %v, want %v", names, wantNames)
	}
	for i := range wantNames {
		if names[i] != wantNames[i] {
			t.Fatalf("fork dir entries = %v, want %v", names, wantNames)
		}
	}
	gotArchive, err := os.ReadFile(filepath.Join(newDir, "history-1.md"))
	if err != nil {
		t.Fatal(err)
	}
	wantArchive := "# Index\n- 1: User: u1\n... archived gen 1 ...\n"
	if string(gotArchive) != wantArchive {
		t.Fatalf("history-1.md content = %q, want %q", gotArchive, wantArchive)
	}

	meta, err := recovery.LoadSessionMeta(newDir)
	if err != nil || meta == nil {
		t.Fatalf("load fork meta: meta=%v err=%v", meta, err)
	}
	if meta.ForkedFrom != filepath.Base(src) {
		t.Errorf("ForkedFrom = %q, want %q", meta.ForkedFrom, filepath.Base(src))
	}
}

func TestForkSessionAtHistoryExplicitBoundary(t *testing.T) {
	src := filepath.Join(t.TempDir(), "20260901000000002")
	if err := os.MkdirAll(src, 0o700); err != nil {
		t.Fatal(err)
	}
	writeForkSourceFixture(t, src)

	newDir, chosen, seeded, err := forkSessionAtHistory(src, filepath.Dir(src), 1)
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	if chosen != 1 {
		t.Errorf("chosen = %d, want 1", chosen)
	}
	msgs := readForkFixtureMessages(t, newDir)
	if seeded != 6 {
		t.Fatalf("seeded = %d, want 6", seeded)
	}
	if got := forkContentSequence(msgs); got != "u1|a1|t1|u2|a2|t2|" {
		t.Fatalf("forked sequence = %q, want the pre-compress-1 messages only", got)
	}
	// Boundary 1 predates any checkpoint card, so no archive is copied:
	// pre-compress-1 is the full raw transcript with no earlier generation to
	// look up, and history-1.md would only duplicate content that is already
	// inline in the fork's main.jsonl.
	names := forkDirNames(t, newDir)
	wantNames := []string{"main.jsonl", "session-meta.json", "session.lock.guard"}
	if len(names) != len(wantNames) {
		t.Fatalf("fork dir entries = %v, want %v", names, wantNames)
	}
	for i := range wantNames {
		if names[i] != wantNames[i] {
			t.Fatalf("fork dir entries = %v, want %v", names, wantNames)
		}
	}
}

func TestForkSessionAtHistoryErrors(t *testing.T) {
	base := t.TempDir()

	// No compaction history at all.
	noHist := filepath.Join(base, "20260901000000003")
	if err := os.MkdirAll(noHist, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(noHist, "main.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := forkSessionAtHistory(noHist, base, 0); err == nil || !strings.Contains(err.Error(), "no applied compaction history") {
		t.Fatalf("no-history error = %v, want 'no applied compaction history'", err)
	}

	// Out-of-range boundary.
	src := filepath.Join(base, "20260901000000004")
	if err := os.MkdirAll(src, 0o700); err != nil {
		t.Fatal(err)
	}
	writeForkFixtureJSONL(t, src, "main.pre-compress-1.jsonl", []message.Message{forkFixtureMessage(message.RoleUser, "u1")})
	writeForkHistoryStatus(t, src, 1, compactionHistoryStatusApplied)
	if err := os.WriteFile(filepath.Join(src, "main.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, _, err := forkSessionAtHistory(src, base, 3)
	if err == nil || !strings.Contains(err.Error(), "applied boundary history-3 does not exist") || !strings.Contains(err.Error(), "history-1") {
		t.Fatalf("out-of-range error = %v, want range listing", err)
	}
}

func TestForkSessionMetaCarriesWorktreeAndMCPOnly(t *testing.T) {
	src := filepath.Join(t.TempDir(), "20260901000000005")
	if err := os.MkdirAll(src, 0o700); err != nil {
		t.Fatal(err)
	}
	sourceMeta := recovery.SessionMeta{
		ForkedFrom:        "grandparent",
		Title:             "keep me",
		RepoID:            "repo-1",
		RepoRoot:          "/main/repo",
		WorktreeName:      "feat",
		WorktreeBranch:    "chord/feat",
		WorktreePath:      "/main/repo/feat",
		IsMainWorktree:    true,
		MCPEnabledServers: []string{"b", "a"},
		ImportedFrom:      &recovery.ImportMeta{Source: "codex", SourcePath: "/tmp/x.jsonl", ImportedAt: time.Now()},
	}
	if err := recovery.SaveSessionMeta(src, sourceMeta); err != nil {
		t.Fatal(err)
	}
	writeForkFixtureJSONL(t, src, "main.pre-compress-1.jsonl", []message.Message{forkFixtureMessage(message.RoleUser, "u1")})

	meta, err := forkSessionMeta(src)
	if err != nil {
		t.Fatalf("forkSessionMeta: %v", err)
	}
	if meta.ForkedFrom != filepath.Base(src) {
		t.Errorf("ForkedFrom = %q, want the direct source sid", meta.ForkedFrom)
	}
	if meta.Title != "" || meta.ImportedFrom != nil {
		t.Errorf("fork carried title/import state: %+v", meta)
	}
	if meta.RepoID != "repo-1" || meta.WorktreeName != "feat" || !meta.IsMainWorktree {
		t.Errorf("worktree provenance not carried: %+v", meta)
	}
	if len(meta.MCPEnabledServers) != 2 || meta.MCPEnabledServers[0] != "a" || meta.MCPEnabledServers[1] != "b" {
		t.Errorf("MCP intent not carried/normalized: %+v", meta.MCPEnabledServers)
	}
}

func TestForkResumeSessionByCurrentProject(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("CHORD_STATE_DIR", stateDir)
	project := t.TempDir()
	t.Chdir(project)

	pl, err := startupPathLocator()
	if err != nil {
		t.Fatalf("startupPathLocator: %v", err)
	}
	projectPL, err := pl.LocateProject(project)
	if err != nil {
		t.Fatalf("LocateProject: %v", err)
	}
	src := filepath.Join(projectPL.ProjectSessionsDir, "20260901000000007")
	if err := os.MkdirAll(src, 0o700); err != nil {
		t.Fatal(err)
	}
	writeForkSourceFixture(t, src)

	newSID, chosen, seeded, err := forkResumeSessionByCurrentProject("20260901000000007", 0)
	if err != nil {
		t.Fatalf("forkResumeSessionByCurrentProject: %v", err)
	}
	if chosen != 2 {
		t.Errorf("chosen = %d, want 2", chosen)
	}
	if seeded != 5 {
		t.Errorf("seeded = %d, want 5 (pre-compress-2 incl. its summary card)", seeded)
	}
	newDir := filepath.Join(projectPL.ProjectSessionsDir, newSID)
	msgs := readForkFixtureMessages(t, newDir)
	if got := forkContentSequence(msgs); got != "[Context Summary]\nOriginal request: u1\nHistory map: history-1.md|a2|t2|u3|a3|" {
		t.Fatalf("forked sequence = %q", got)
	}
	meta, err := recovery.LoadSessionMeta(newDir)
	if err != nil || meta == nil {
		t.Fatalf("load fork meta: meta=%v err=%v", meta, err)
	}
	if meta.ForkedFrom != filepath.Base(src) {
		t.Errorf("ForkedFrom = %q, want %q", meta.ForkedFrom, filepath.Base(src))
	}

	// A session that does not belong to the current project fails cleanly.
	if _, _, _, err := forkResumeSessionByCurrentProject("20260901000000099", 0); err == nil {
		t.Fatal("fork of an unknown session succeeded, want error")
	}
}

func TestForkSessionRelocatesImageAttachment(t *testing.T) {
	src := filepath.Join(t.TempDir(), "20260901000000006")
	if err := os.MkdirAll(filepath.Join(src, "images"), 0o700); err != nil {
		t.Fatal(err)
	}
	imageBytes := []byte("fake-png-bytes")
	imgPath := filepath.Join(src, "images", "1-0.png")
	if err := os.WriteFile(imgPath, imageBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	user := message.Message{
		Role:    message.RoleUser,
		Content: "see the image",
		Parts: []message.ContentPart{
			{Type: message.ContentPartText, Text: "see the image"},
			{Type: message.ContentPartImage, MimeType: "image/png", ImagePath: imgPath, FileName: "1-0.png"},
		},
	}
	writeForkFixtureJSONL(t, src, "main.pre-compress-1.jsonl", []message.Message{user})
	writeForkHistoryStatus(t, src, 1, compactionHistoryStatusApplied)
	if err := os.WriteFile(filepath.Join(src, "main.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	newDir, _, _, err := forkSessionAtHistory(src, filepath.Dir(src), 0)
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	msgs := readForkFixtureMessages(t, newDir)
	if len(msgs) != 1 {
		t.Fatalf("forked messages = %d, want 1", len(msgs))
	}
	var foundImage bool
	for _, p := range msgs[0].Parts {
		if !p.IsBinary() {
			continue
		}
		foundImage = true
		if p.ImagePath == "" || p.ImagePath == imgPath || !strings.HasPrefix(p.ImagePath, newDir) {
			t.Fatalf("image path not relocated into fork: %q", p.ImagePath)
		}
		got, err := os.ReadFile(p.ImagePath)
		if err != nil {
			t.Fatalf("read forked image: %v", err)
		}
		if string(got) != string(imageBytes) {
			t.Fatalf("forked image bytes = %q, want %q", got, imageBytes)
		}
	}
	if !foundImage {
		t.Fatal("forked message lost its image part")
	}
	if msgs[0].Content != "see the image" {
		t.Fatalf("message content changed: %q", msgs[0].Content)
	}
}

func TestForkSessionLatestSkipsPendingBoundary(t *testing.T) {
	src := filepath.Join(t.TempDir(), "20260901000000008")
	if err := os.MkdirAll(src, 0o700); err != nil {
		t.Fatal(err)
	}
	writeForkFixtureJSONL(t, src, "main.pre-compress-1.jsonl", []message.Message{forkFixtureMessage(message.RoleUser, "gen1")})
	writeForkHistoryStatus(t, src, 1, compactionHistoryStatusApplied)
	writeForkFixtureJSONL(t, src, "main.pre-compress-2.jsonl", []message.Message{forkFixtureMessage(message.RoleUser, "gen2-not-yet-applied")})
	writeForkHistoryStatus(t, src, 2, forkHistoryStatusPendingApply)
	if err := os.WriteFile(filepath.Join(src, "main.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Latest *applied* wins: the newer snapshot whose apply never completed is
	// not offered for forking.
	newDir, chosen, _, err := forkSessionAtHistory(src, filepath.Dir(src), 0)
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	if chosen != 1 {
		t.Fatalf("chosen = %d, want 1 (the pending boundary 2 must be skipped)", chosen)
	}
	if got := forkContentSequence(readForkFixtureMessages(t, newDir)); got != "gen1|" {
		t.Fatalf("forked sequence = %q, want %q", got, "gen1|")
	}

	// Explicitly asking for the not-yet-applied boundary fails instead of
	// forking a snapshot whose apply never completed.
	if _, _, _, err := forkSessionAtHistory(src, filepath.Dir(src), 2); err == nil || !strings.Contains(err.Error(), "applied boundary history-2 does not exist") {
		t.Fatalf("pending boundary error = %v, want an applied-boundary range error", err)
	}
}

func TestForkSessionMissingAttachmentFails(t *testing.T) {
	src := filepath.Join(t.TempDir(), "20260901000000009")
	if err := os.MkdirAll(filepath.Join(src, "images"), 0o700); err != nil {
		t.Fatal(err)
	}
	// The archived record references an image file the source session no
	// longer has; the fork must fail loudly instead of silently producing a
	// fork whose image would be missing on restore.
	missingPath := filepath.Join(src, "images", "gone.png")
	user := message.Message{
		Role:    message.RoleUser,
		Content: "see the image",
		Parts: []message.ContentPart{
			{Type: message.ContentPartImage, MimeType: "image/png", ImagePath: missingPath},
		},
	}
	writeForkFixtureJSONL(t, src, "main.pre-compress-1.jsonl", []message.Message{user})
	writeForkHistoryStatus(t, src, 1, compactionHistoryStatusApplied)
	if err := os.WriteFile(filepath.Join(src, "main.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, _, _, err := forkSessionAtHistory(src, filepath.Dir(src), 0)
	if err == nil || !strings.Contains(err.Error(), missingPath) || !strings.Contains(err.Error(), "no longer readable") {
		t.Fatalf("missing-attachment error = %v, want a readable error naming %s", err, missingPath)
	}
	// The partial fork directory is removed again.
	entries, readErr := os.ReadDir(filepath.Dir(src))
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, e := range entries {
		if e.Name() != filepath.Base(src) {
			t.Fatalf("fork failure left a session directory behind: %s", e.Name())
		}
	}
}

// forkCheckpointSummary builds a compaction-summary card whose archived-history
// map lists each archive by the exact abbreviated absolute path the compaction
// pipeline writes (AbbreviateHome(sessionDir/history-N.md)). This is the shape
// that survives into main.pre-compress-N.jsonl and that a fork must repoint at
// its own session directory so it stays self-contained.
func forkCheckpointSummary(t *testing.T, sessionDir string, indexes ...int) message.Message {
	t.Helper()
	var sb strings.Builder
	sb.WriteString("[Context Summary]\nOriginal request: u1\nArchived history files (read the matching file with the read tool to recover exact details; paths accept ~ shorthand):\n")
	for _, n := range indexes {
		ref := filepath.Join(sessionDir, fmt.Sprintf("history-%d.md", n))
		fmt.Fprintf(&sb, "- %s\n", pathutil.AbbreviateHome(ref))
	}
	return message.Message{Role: message.RoleUser, Content: sb.String(), IsCompactionSummary: true}
}

// TestForkSessionRewritesCheckpointHistoryMap exercises the P1 fix: a fork at
// boundary 2 must repoint the checkpoint summary card's history-map paths from
// the source session's directory to the fork's own copy. The fork copies the
// archives, but the map's paths still reference the source until rewritten, so
// deleting the source session must not break reading the archive the map points
// at. HOME is pointed at a temp dir so the paths take the ~/... abbreviated form
// the real pipeline emits under the default sessions root.
func TestForkSessionRewritesCheckpointHistoryMap(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	src := filepath.Join(home, ".local", "state", "chord", "sessions", "proj", "20260901000000010")
	if err := os.MkdirAll(src, 0o700); err != nil {
		t.Fatal(err)
	}
	// gen 2's summary card references history-1.md by the source session's
	// abbreviated path — the real shape the compaction pipeline writes.
	summary := forkCheckpointSummary(t, src, 1)
	srcPrefix := pathutil.AbbreviateHome(src)
	ordinaryText := "Original request: inspect " + srcPrefix + "/notes.txt"
	summary.Content = strings.Replace(summary.Content, "Original request: u1", ordinaryText, 1)
	summary.Content = strings.Replace(summary.Content, "/history-1.md\n", "/history-1.md: first archive\n", 1)
	writeForkFixtureJSONL(t, src, "main.pre-compress-1.jsonl", []message.Message{
		forkFixtureMessage(message.RoleUser, "u1"),
	})
	writeForkFixtureJSONL(t, src, "main.pre-compress-2.jsonl", []message.Message{
		summary,
		forkFixtureMessage(message.RoleAssistant, "a3"),
	})
	if err := os.WriteFile(filepath.Join(src, "history-1.md"), []byte("# Index\n- 1: User: u1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeForkHistoryStatus(t, src, 1, compactionHistoryStatusApplied)
	writeForkHistoryStatus(t, src, 2, compactionHistoryStatusApplied)
	if err := os.WriteFile(filepath.Join(src, "main.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	newDir, chosen, _, err := forkSessionAtHistory(src, filepath.Dir(src), 2)
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	if chosen != 2 {
		t.Fatalf("chosen = %d, want 2", chosen)
	}
	msgs := readForkFixtureMessages(t, newDir)
	if len(msgs) != 2 || !msgs[0].IsCompactionSummary {
		t.Fatalf("forked messages = %+v, want summary card + a3", msgs)
	}
	newPrefix := pathutil.AbbreviateHome(newDir)
	if srcPrefix == newPrefix {
		t.Fatalf("test setup degenerate: source and fork prefixes match")
	}
	if strings.Contains(msgs[0].Content, "- "+srcPrefix+"/history-") {
		t.Fatalf("fork summary still references source session path:\n%s", msgs[0].Content)
	}
	if !strings.Contains(msgs[0].Content, ordinaryText) {
		t.Fatalf("fork summary rewrote ordinary text outside the history map:\n%s", msgs[0].Content)
	}
	wantRef := newPrefix + "/history-1.md"
	if !strings.Contains(msgs[0].Content, wantRef) {
		t.Fatalf("fork summary does not reference fork's own history-1.md (%s):\n%s", wantRef, msgs[0].Content)
	}
	// The fork's own archive was copied alongside.
	if _, err := os.Stat(filepath.Join(newDir, "history-1.md")); err != nil {
		t.Fatalf("fork did not copy history-1.md: %v", err)
	}

	// The fork must be self-contained: removing the source session must not
	// break reading the archive the rewritten map points at. Resolve through
	// the same ExpandTilde path the read tool uses.
	if err := os.RemoveAll(src); err != nil {
		t.Fatal(err)
	}
	resolved, err := pathutil.ResolveInDir(wantRef, "")
	if err != nil {
		t.Fatalf("resolve rewritten ref %s: %v", wantRef, err)
	}
	got, err := os.ReadFile(resolved)
	if err != nil {
		t.Fatalf("read rewritten ref %s (resolved %s) after source removal: %v", wantRef, resolved, err)
	}
	if string(got) != "# Index\n- 1: User: u1\n" {
		t.Fatalf("fork's history-1.md = %q", got)
	}
}
