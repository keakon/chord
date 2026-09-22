package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

// readMessageForLazyCheck builds a successful read result whose FileState
// records the current file hash, plus a scan with matching tool-call metadata.
func readMessageForLazyCheck(t *testing.T, path string) (message.Message, *reductionHistoryScan) {
	t.Helper()
	hash, exists, _, err := verifiedCurrentFileHash(path)
	if err != nil {
		t.Fatalf("hash %s: %v", path, err)
	}
	if !exists {
		t.Fatalf("file %s does not exist", path)
	}
	msgs := []message.Message{
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "read-1", Name: tools.NameRead}}},
		{
			Role:       message.RoleTool,
			ToolCallID: "read-1",
			Content:    "file content",
			ToolStatus: string(ToolResultStatusSuccess),
			FileState: &message.ToolFileState{Reads: []message.TrackedFileState{
				{Path: path, SHA256: hash, Exists: true},
			}},
		},
	}
	return msgs[1], newReductionHistoryScan(msgs)
}

func lazyAgentForTest() *MainAgent {
	return &MainAgent{contentRoot: "", tools: &tools.Registry{}}
}

func TestLazyExternalReadInvalidationDetectsExternalEdit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.go")
	if err := os.WriteFile(path, []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	msg, scan := readMessageForLazyCheck(t, path)
	a := lazyAgentForTest()
	a.contentRoot = dir

	// Unchanged file: not stale.
	invalidated := a.externalReadsInvalidatedLazy([]message.Message{msg}, scan)
	if len(invalidated) != 0 {
		t.Fatalf("unchanged file reported stale: %v", invalidated)
	}
	// External edit (mtime + hash change): stale.
	if err := os.WriteFile(path, []byte("v2 external\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	invalidated = a.externalReadsInvalidatedLazy([]message.Message{msg}, scan)
	if len(invalidated) != 1 || !invalidated[0] {
		t.Fatalf("externally edited file not reported stale: %v", invalidated)
	}
}

func TestLazyExternalReadInvalidationMemoSkipsUnchangedStat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "b.go")
	if err := os.WriteFile(path, []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	msg, scan := readMessageForLazyCheck(t, path)
	a := lazyAgentForTest()
	a.contentRoot = dir
	if got := a.externalReadsInvalidatedLazy([]message.Message{msg}, scan); len(got) != 0 {
		t.Fatalf("first pass stale: %v", got)
	}

	// Rewrite with identical size and restore the original mtime: the memo
	// fast path sees an unchanged stat and reuses the cached "not changed"
	// verdict without re-hashing.
	originalInfo, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, originalInfo.ModTime(), originalInfo.ModTime()); err != nil {
		t.Fatal(err)
	}
	// Same size, same mtime: memo hit, no re-hash, still not stale.
	if got := a.externalReadsInvalidatedLazy([]message.Message{msg}, scan); len(got) != 0 {
		t.Fatalf("memo fast path should reuse verdict without re-hash: %v", got)
	}
	// Touch (mtime change, size same): stat changed, re-hash, now stale.
	newTime := originalInfo.ModTime().Add(time.Second)
	if err := os.Chtimes(path, newTime, newTime); err != nil {
		t.Fatal(err)
	}
	if got := a.externalReadsInvalidatedLazy([]message.Message{msg}, scan); len(got) != 1 {
		t.Fatalf("mtime change should re-hash and report stale: %v", got)
	}
}

func TestLazyExternalReadInvalidationUnverifiableStaysUnknown(t *testing.T) {
	dir := t.TempDir()
	// A directory cannot be hashed: verification fails, so the read must stay
	// "not stale" (unknown), not asserted stale.
	sub := filepath.Join(dir, "subdir")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	msg := message.Message{
		Role:       message.RoleTool,
		ToolCallID: "read-1",
		Content:    "dir",
		ToolStatus: string(ToolResultStatusSuccess),
		FileState: &message.ToolFileState{Reads: []message.TrackedFileState{
			{Path: sub, SHA256: "expected-hash", Exists: true},
		}},
	}
	msgs := []message.Message{
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "read-1", Name: tools.NameRead}}},
		msg,
	}
	scan := newReductionHistoryScan(msgs)
	a := lazyAgentForTest()
	if got := a.externalReadsInvalidatedLazy(msgs, scan); len(got) != 0 {
		t.Fatalf("unverifiable file must stay unknown (not stale): %v", got)
	}
}

func TestLazyExternalReadInvalidationDeletedFileIsStale(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.go")
	if err := os.WriteFile(path, []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	msg, scan := readMessageForLazyCheck(t, path)
	a := lazyAgentForTest()
	a.contentRoot = dir
	if got := a.externalReadsInvalidatedLazy([]message.Message{msg}, scan); len(got) != 0 {
		t.Fatalf("unchanged file stale: %v", got)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if got := a.externalReadsInvalidatedLazy([]message.Message{msg}, scan); len(got) != 1 {
		t.Fatalf("deleted file must be stale: %v", got)
	}
}

func TestLazyExternalReadInvalidationRelativePathResolvesProjectRoot(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rel.go")
	if err := os.WriteFile(path, []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	abs, _, _, err := verifiedCurrentFileHash(path)
	if err != nil {
		t.Fatal(err)
	}
	msg := message.Message{
		Role:       message.RoleTool,
		ToolCallID: "read-1",
		Content:    "file",
		ToolStatus: string(ToolResultStatusSuccess),
		FileState: &message.ToolFileState{Reads: []message.TrackedFileState{
			{Path: "rel.go", SHA256: abs, Exists: true},
		}},
	}
	msgs := []message.Message{
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "read-1", Name: tools.NameRead}}},
		msg,
	}
	scan := newReductionHistoryScan(msgs)
	a := lazyAgentForTest()
	a.contentRoot = dir
	if got := a.externalReadsInvalidatedLazy(msgs, scan); len(got) != 0 {
		t.Fatalf("relative path resolved incorrectly: %v", got)
	}
	if err := os.WriteFile(path, []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := a.externalReadsInvalidatedLazy(msgs, scan); len(got) != 1 {
		t.Fatalf("relative path edit not detected: %v", got)
	}
}

// TestLazyReadMemoResetWhenOverflow pins the bounded memo: once the verdict
// map reaches the cap, the next verification resets it wholesale instead of
// growing without limit, and the new verdict is still recorded.
func TestLazyReadMemoResetWhenOverflow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cap.go")
	if err := os.WriteFile(path, []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	msg, scan := readMessageForLazyCheck(t, path)
	a := lazyAgentForTest()
	a.contentRoot = dir
	a.lazyReadMemo.mu.Lock()
	a.lazyReadMemo.verdicts = make(map[string]externalReadLazyVerdict, lazyReadMemoMaxEntries)
	for i := range lazyReadMemoMaxEntries {
		a.lazyReadMemo.verdicts[fmt.Sprintf("key-%d", i)] = externalReadLazyVerdict{mtimeNano: int64(i), size: 1, changed: true}
	}
	a.lazyReadMemo.mu.Unlock()
	if got := a.externalReadsInvalidatedLazy([]message.Message{msg}, scan); len(got) != 0 {
		t.Fatalf("unchanged file stale after memo reset: %v", got)
	}
	a.lazyReadMemo.mu.Lock()
	defer a.lazyReadMemo.mu.Unlock()
	if len(a.lazyReadMemo.verdicts) != 1 {
		t.Fatalf("memo after overflow reset = %d entries, want 1", len(a.lazyReadMemo.verdicts))
	}
}
