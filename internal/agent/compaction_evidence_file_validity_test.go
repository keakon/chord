package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestAddToolEvidenceCandidateBindsToolStateAndRevision(t *testing.T) {
	a := &MainAgent{}
	a.addToolEvidenceCandidate(buildEvidenceItem(evidenceToolDiff, "diff", "needed", "tool", "changed"), message.Message{
		ToolCallID: "call-1",
		ToolStatus: "success",
		FileState:  &message.ToolFileState{Writes: []message.TrackedFileState{{Path: "main.go", SHA256: "abc", Exists: true}}},
	})
	items := a.evidence.snapshot()
	if len(items) != 1 || items[0].SourceID != "call-1" || items[0].ToolState != "success" || items[0].Revisions["main.go"] != "abc" {
		t.Fatalf("tool evidence = %#v", items)
	}
}

func TestRefreshEvidenceValidityInvalidatesChangedToolFile(t *testing.T) {
	projectRoot := t.TempDir()
	path := filepath.Join(projectRoot, "main.go")
	if err := os.WriteFile(path, []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := &MainAgent{contentRoot: projectRoot, tools: tools.NewRegistry(), ctxMgr: ctxmgr.NewManager(10000, 1000)}
	a.evidence.add(evidenceItem{Kind: evidenceToolDiff, SourceID: "call-1", Excerpt: "diff", Revisions: map[string]string{path: "bad-revision"}, Validity: evidenceValidityValid})
	a.refreshEvidenceValidity()
	if got := a.evidence.snapshot()[0].Validity; got != evidenceValidityInvalidated {
		t.Fatalf("evidence validity = %q, want invalidated", got)
	}
}

// TestRefreshEvidenceValidityRehashesOnlyWhenFileChanges pins the hash cache
// behind the event-loop validity refresh: two consecutive passes over an
// unchanged tracked file must not re-read it (the content hash is reused from
// the mtime/size key), and a real change must still be detected on the next
// pass. Rewriting the file with identical content and restoring the original
// mtime leaves an unchanged stat, which is exactly the fast path the memo
// skips — a re-hash would see the new write only through stat or content.
func TestRefreshEvidenceValidityRehashesOnlyWhenFileChanges(t *testing.T) {
	projectRoot := t.TempDir()
	path := filepath.Join(projectRoot, "main.go")
	if err := os.WriteFile(path, []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	hash, _, _, err := verifiedCurrentFileHash(path)
	if err != nil {
		t.Fatalf("hash %s: %v", path, err)
	}
	a := &MainAgent{contentRoot: projectRoot, tools: tools.NewRegistry(), ctxMgr: ctxmgr.NewManager(10000, 1000)}
	a.evidence.add(evidenceItem{Kind: evidenceToolDiff, SourceID: "call-1", Excerpt: "diff", Revisions: map[string]string{path: hash}, Validity: evidenceValidityValid})

	a.refreshEvidenceValidity()
	if got := a.evidence.snapshot()[0].Validity; got != evidenceValidityValid {
		t.Fatalf("first refresh must keep the unchanged file valid, got %q", got)
	}

	// Rewrite with different content but restore the original mtime and keep
	// the size identical: only a content re-hash could detect the change. The
	// memo reuses the cached hash on the unchanged stat, mirroring the lazy
	// external-read memo semantics.
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
	a.refreshEvidenceValidity()
	if got := a.evidence.snapshot()[0].Validity; got != evidenceValidityValid {
		t.Fatalf("unchanged-stat file must reuse the cached hash without re-hashing, got %q", got)
	}

	// A real stat change (mtime bump) re-hashes and detects the content drift.
	newTime := originalInfo.ModTime().Add(time.Second)
	if err := os.Chtimes(path, newTime, newTime); err != nil {
		t.Fatal(err)
	}
	a.refreshEvidenceValidity()
	if got := a.evidence.snapshot()[0].Validity; got != evidenceValidityInvalidated {
		t.Fatalf("mtime bump must re-hash and invalidate the drifted file, got %q", got)
	}
}

// TestEvidenceFileRevisionMemoReusesHashOnUnchangedStat tests the memo
// directly: the same file yields the cached content hash while the stat is
// unchanged, and a fresh hash once the mtime/size differs.
func TestEvidenceFileRevisionMemoReusesHashOnUnchangedStat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.go")
	if err := os.WriteFile(path, []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	hash, exists, err := evidenceFileRevisionMemoGlobal.verifiedHash(path)
	if err != nil || !exists {
		t.Fatalf("first hash = %q exists=%v err=%v", hash, exists, err)
	}
	second, exists, err := evidenceFileRevisionMemoGlobal.verifiedHash(path)
	if err != nil || !exists || second != hash {
		t.Fatalf("memo must reuse the cached hash on an unchanged stat: %q vs %q", second, hash)
	}
	// Deleted files are forgotten and reported as not existing.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, exists, err := evidenceFileRevisionMemoGlobal.verifiedHash(path); err != nil || exists {
		t.Fatalf("deleted file must be forgotten exists=%v err=%v", exists, err)
	}
}

// TestEvidenceFileRevisionMemoFollowsSymlinkTarget pins the target-following
// stat semantics of the evidence hash memo: a write/edit evidence path that an
// external process replaced with a symlink pointing at content identical at
// replacement time must still invalidate when the symlink's target is later
// edited or removed. The memo key follows the target like the hash it guards
// (os.Stat, matching computeFileHash's open): caching under the link's own
// mtime/size would pin the verdict to metadata that never changes while the
// target does, and the replacement hash would stay current forever.
func TestEvidenceFileRevisionMemoFollowsSymlinkTarget(t *testing.T) {
	dir := t.TempDir()
	targetPath := filepath.Join(dir, "target.go")
	path := filepath.Join(dir, "evidence.go")
	if err := os.WriteFile(targetPath, []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(targetPath, path); err != nil {
		t.Fatal(err)
	}
	first, exists, err := evidenceFileRevisionMemoGlobal.verifiedHash(path)
	if err != nil || !exists {
		t.Fatalf("hash through symlink = %q exists=%v err=%v", first, exists, err)
	}

	// The target is edited; the symlink itself is untouched. The memo must
	// re-hash (the target stat changed) and report the new content.
	if err := os.WriteFile(targetPath, []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(targetPath, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	second, exists, err := evidenceFileRevisionMemoGlobal.verifiedHash(path)
	if err != nil || !exists {
		t.Fatalf("hash after target edit = %q exists=%v err=%v", second, exists, err)
	}
	if second == first {
		t.Fatal("target edit must invalidate the memoized hash instead of reusing the stale one")
	}

	// Deleting the target makes the whole path unresolvable even though the
	// link itself is untouched: the recorded revision cannot be current.
	if err := os.Remove(targetPath); err != nil {
		t.Fatal(err)
	}
	if _, exists, err := evidenceFileRevisionMemoGlobal.verifiedHash(path); err != nil || exists {
		t.Fatalf("deleted symlink target must read as not existing exists=%v err=%v", exists, err)
	}
}

// TestRefreshEvidenceValidityInvalidatesReplacedSymlinkTarget drives the
// reported scenario end to end at the validity level: a write/edit evidence
// path is externally replaced by a symlink to content identical at replacement
// time (so the evidence stays valid), and a later edit or removal of the
// symlink's target — while the link itself never changes — must invalidate the
// evidence instead of reusing a hash the link's own metadata still matches.
func TestRefreshEvidenceValidityInvalidatesReplacedSymlinkTarget(t *testing.T) {
	projectRoot := t.TempDir()
	tracked := filepath.Join(projectRoot, "tracked.go")
	alias := filepath.Join(projectRoot, "alias.go")
	if err := os.WriteFile(tracked, []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := &MainAgent{contentRoot: projectRoot, tools: tools.NewRegistry(), ctxMgr: ctxmgr.NewManager(10000, 1000)}
	hash, _, _, err := verifiedCurrentFileHash(tracked)
	if err != nil {
		t.Fatalf("hash %s: %v", tracked, err)
	}
	a.evidence.add(evidenceItem{Kind: evidenceToolDiff, SourceID: "call-1", Excerpt: "diff", Revisions: map[string]string{tracked: hash}, Validity: evidenceValidityValid})
	a.refreshEvidenceValidity()
	if got := a.evidence.snapshot()[0].Validity; got != evidenceValidityValid {
		t.Fatalf("real-file evidence must start valid, got %q", got)
	}

	// External replacement: the tracked path becomes a symlink to a different
	// file carrying the same content. The evidence stays valid.
	if err := os.Remove(tracked); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(alias, []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(alias, tracked); err != nil {
		t.Fatal(err)
	}
	a.refreshEvidenceValidity()
	if got := a.evidence.snapshot()[0].Validity; got != evidenceValidityValid {
		t.Fatalf("same-content symlink replacement must keep the evidence valid, got %q", got)
	}

	// Editing only the symlink's target invalidates the evidence.
	if err := os.WriteFile(alias, []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(alias, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	a.refreshEvidenceValidity()
	if got := a.evidence.snapshot()[0].Validity; got != evidenceValidityInvalidated {
		t.Fatalf("edited symlink target must invalidate the evidence, got %q", got)
	}

	// Deleting the target (the link remains) also invalidates.
	item := &a.evidence.items[0]
	item.Validity = evidenceValidityValid
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	a.refreshEvidenceValidity()
	if got := a.evidence.snapshot()[0].Validity; got != evidenceValidityInvalidated {
		t.Fatalf("deleted symlink target must invalidate the evidence, got %q", got)
	}
}
