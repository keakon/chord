package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/keakon/chord/internal/agent/requesttrim"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

// readMessageForLazyCheck builds a successful read result whose FileState
// records the current file hash, plus a scan with matching tool-call metadata.
func readMessageForLazyCheck(t *testing.T, path string) (message.Message, *requesttrim.Scan) {
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
			ToolStatus: message.ToolStatusSuccess,
			FileState: &message.ToolFileState{Reads: []message.TrackedFileState{
				{Path: path, SHA256: hash, Exists: true},
			}},
		},
	}
	return msgs[1], requesttrim.NewScan(msgs, nil)
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
		ToolStatus: message.ToolStatusSuccess,
		FileState: &message.ToolFileState{Reads: []message.TrackedFileState{
			{Path: sub, SHA256: "expected-hash", Exists: true},
		}},
	}
	msgs := []message.Message{
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "read-1", Name: tools.NameRead}}},
		msg,
	}
	scan := requesttrim.NewScan(msgs, nil)
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
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if got := a.externalReadsInvalidatedLazy([]message.Message{msg}, scan); len(got) != 1 {
		t.Fatalf("deleted file must be stale: %v", got)
	}
	if err := os.WriteFile(path, []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if got := a.externalReadsInvalidatedLazy([]message.Message{msg}, scan); !got[0] {
		t.Fatal("recreated file reused the deleted file's verdict")
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
		ToolStatus: message.ToolStatusSuccess,
		FileState: &message.ToolFileState{Reads: []message.TrackedFileState{
			{Path: "rel.go", SHA256: abs, Exists: true},
		}},
	}
	msgs := []message.Message{
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "read-1", Name: tools.NameRead}}},
		msg,
	}
	scan := requesttrim.NewScan(msgs, nil)
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

func TestLazyReadMemoAtCapacityDoesNotEvict(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cap.go")
	if err := os.WriteFile(path, []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	msg, scan := readMessageForLazyCheck(t, path)
	a := lazyAgentForTest()
	a.contentRoot = dir
	a.lazyReadMemo.verdicts = make(map[string]externalReadLazyVerdict, lazyReadMemoMaxEntries)
	key := path + "\x00" + msg.FileState.Reads[0].SHA256
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	a.lazyReadMemo.verdicts[key] = externalReadLazyVerdict{
		mtimeNano: info.ModTime().UnixNano(),
		size:      info.Size(),
	}
	for i := range lazyReadMemoMaxEntries - 1 {
		a.lazyReadMemo.verdicts[fmt.Sprintf("key-%d", i)] = externalReadLazyVerdict{mtimeNano: int64(i), size: 1, changed: true}
	}
	if got := a.externalReadsInvalidatedLazy([]message.Message{msg}, scan); len(got) != 0 {
		t.Fatalf("unchanged file stale at capacity: %v", got)
	}
	if len(a.lazyReadMemo.verdicts) != lazyReadMemoMaxEntries {
		t.Fatal("existing cached verdict was evicted")
	}
	if err := os.WriteFile(path, []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if stale := a.externalReadsInvalidatedLazy([]message.Message{msg}, scan); !stale[0] ||
		!a.lazyReadMemo.verdicts[key].changed || len(a.lazyReadMemo.verdicts) != lazyReadMemoMaxEntries {
		t.Fatal("a changed cached file should update its verdict without evicting other files")
	}
	newPath := filepath.Join(dir, "new.go")
	if err := os.WriteFile(newPath, []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	newMsg, newScan := readMessageForLazyCheck(t, newPath)
	if got := a.externalReadsInvalidatedLazy([]message.Message{newMsg}, newScan); len(got) != 0 {
		t.Fatalf("unchanged uncached file stale: %v", got)
	}
	newKey := newPath + "\x00" + newMsg.FileState.Reads[0].SHA256
	if _, ok := a.lazyReadMemo.verdicts[newKey]; ok || len(a.lazyReadMemo.verdicts) != lazyReadMemoMaxEntries {
		t.Fatal("cache miss at capacity must not evict or add a key")
	}
}

func TestLazyReadMemoCanCacheNewFilesAfterSessionSwitch(t *testing.T) {
	for _, name := range []string{"new", "fork", "resume"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			initial := testProjectSessionDir(t, root, "initial")
			a := newTestMainAgentForRestore(t, root, initial)
			a.markAgentsMDReady()
			a.MarkSkillsReady()
			a.markMCPReady()
			a.lazyReadMemo.verdicts = make(map[string]externalReadLazyVerdict, lazyReadMemoMaxEntries)
			for i := range lazyReadMemoMaxEntries {
				a.lazyReadMemo.verdicts[fmt.Sprintf("old-%d", i)] = externalReadLazyVerdict{}
			}

			switch name {
			case "new":
				a.handleNewSessionCommand()
			case "fork":
				a.ctxMgr.RestoreMessages([]message.Message{
					{Role: message.RoleUser, Content: "prompt"},
					{Role: message.RoleAssistant, Content: "reply"},
				})
				a.handleForkSessionCommand(0)
			case "resume":
				target := testProjectSessionDir(t, root, "resumed")
				if err := os.MkdirAll(target, 0o755); err != nil {
					t.Fatal(err)
				}
				a.activateLoadedSession(&loadedSessionState{SessionPath: target})
			}
			if a.SessionDir() == initial {
				t.Fatal("session did not switch")
			}

			path := filepath.Join(root, "current.txt")
			if err := os.WriteFile(path, []byte("unchanged\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			msg, scan := readMessageForLazyCheck(t, path)
			if got := a.externalReadsInvalidatedLazy([]message.Message{msg}, scan); len(got) != 0 {
				t.Fatalf("unchanged file reported stale: %v", got)
			}
			key := path + "\x00" + msg.FileState.Reads[0].SHA256
			a.lazyReadMemo.mu.Lock()
			_, ok := a.lazyReadMemo.verdicts[key]
			count := len(a.lazyReadMemo.verdicts)
			a.lazyReadMemo.mu.Unlock()
			if !ok || count != 1 {
				t.Fatalf("memo has %d entries, new file cached=%v; want only the current file", count, ok)
			}
		})
	}
}

func TestLazyReadMemoRejectsWritesFromPreviousScan(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.txt")
	if err := os.WriteFile(path, []byte("unchanged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	msg, _ := readMessageForLazyCheck(t, path)
	expected := msg.FileState.Reads[0].SHA256
	var memo externalReadLazyMemo
	oldGeneration := memo.currentGeneration()
	memo.reset()
	if memo.check(oldGeneration, path, expected) || len(memo.verdicts) != 0 {
		t.Fatal("previous scan should verify the file without populating the new memo")
	}
	if memo.check(memo.currentGeneration(), path, expected) || len(memo.verdicts) != 1 {
		t.Fatal("current scan should cache the same file")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if !memo.check(oldGeneration, path, expected) || len(memo.verdicts) != 1 {
		t.Fatal("previous scan should report the missing file without deleting the new verdict")
	}
	if !memo.check(memo.currentGeneration(), path, expected) || len(memo.verdicts) != 0 {
		t.Fatal("current scan should forget the missing file")
	}
}
