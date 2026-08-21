package recovery

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/message"
)

func TestReadOnlyTranscriptLoaderValidatesSessionID(t *testing.T) {
	if !ValidateSessionID("20260821153000123") {
		t.Fatal("valid session id rejected")
	}
	for _, bad := range []string{"", "2026", "..", "../../etc", "abc", "2026082115300012x"} {
		if ValidateSessionID(bad) {
			t.Fatalf("invalid session id accepted: %q", bad)
		}
	}
}

func TestReadOnlyTranscriptLoaderLoad(t *testing.T) {
	root := t.TempDir()
	sessions := filepath.Join(root, "sessions")
	if err := os.MkdirAll(sessions, 0o755); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(sessions, "20260821153000123")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	rm := NewRecoveryManager(dir)
	if err := rm.PersistMessage(identity.MainAgentID, message.Message{Role: message.RoleUser, Content: "hello"}); err != nil {
		t.Fatalf("persist: %v", err)
	}
	if err := rm.PersistMessage(identity.MainAgentID, message.Message{Role: message.RoleAssistant, Content: "world"}); err != nil {
		t.Fatalf("persist: %v", err)
	}
	rm.Close()

	l := NewReadOnlyTranscriptLoader(sessions)
	msgs, err := l.Load("20260821153000123")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(msgs) != 2 || msgs[0].Content != "hello" || msgs[1].Content != "world" {
		t.Fatalf("loaded = %+v", msgs)
	}
	// Unknown session.
	if _, err := l.Load("19990101000000000"); err == nil {
		t.Fatal("expected error for unknown session")
	}
}

func TestReadOnlyTranscriptLoaderRejectsPathEscape(t *testing.T) {
	root := t.TempDir()
	sessions := filepath.Join(root, "sessions")
	if err := os.MkdirAll(sessions, 0o755); err != nil {
		t.Fatal(err)
	}
	l := NewReadOnlyTranscriptLoader(sessions)
	// A directory named with ../ won't pass the session-id validation; ensure
	// an out-of-root dir passed via LoadDir is rejected too.
	outside := filepath.Join(root, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := l.LoadDir(outside); err == nil {
		t.Fatal("expected error for session dir outside the sessions root")
	}
}

func TestReadOnlyTranscriptLoaderDoesNotLoadAttachments(t *testing.T) {
	root := t.TempDir()
	sessions := filepath.Join(root, "sessions")
	if err := os.MkdirAll(sessions, 0o755); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(sessions, "20260821153000123")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	imgPath := filepath.Join(dir, "img.png")
	if err := os.WriteFile(imgPath, []byte("PNGDATA"), 0o644); err != nil {
		t.Fatal(err)
	}
	rm := NewRecoveryManager(dir)
	_ = rm.PersistMessage(identity.MainAgentID, message.Message{
		Role:    message.RoleUser,
		Content: "see image",
		Parts:   []message.ContentPart{{Type: message.ContentPartImage, MimeType: "image/png", ImagePath: imgPath}},
	})
	rm.Close()

	l := NewReadOnlyTranscriptLoader(sessions)
	msgs, err := l.Load("20260821153000123")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("loaded = %d messages", len(msgs))
	}
	for _, p := range msgs[0].Parts {
		if len(p.Data) != 0 {
			t.Fatal("attachment bytes must not be loaded into memory by the read-only loader")
		}
	}
}

func TestListRecentCandidatesSkipsLocked(t *testing.T) {
	root := t.TempDir()
	sessions := filepath.Join(root, "sessions")
	if err := os.MkdirAll(sessions, 0o755); err != nil {
		t.Fatal(err)
	}
	mkSession := func(id string) string {
		d := filepath.Join(sessions, id)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		rm := NewRecoveryManager(d)
		if err := rm.PersistMessage(identity.MainAgentID, message.Message{Role: message.RoleUser, Content: "x"}); err != nil {
			t.Fatalf("persist: %v", err)
		}
		rm.Close()
		return d
	}
	mkSession("20260821153000123")
	mkSession("20260821120000000")
	// Ensure distinct activity mtimes so ordering is deterministic.
	time.Sleep(10 * time.Millisecond)

	// Hold a live lock on the newest session so ListRecentCandidates skips it.
	lockDir := filepath.Join(sessions, "20260821153000123")
	lock, err := AcquireSessionLock(lockDir)
	if err != nil {
		t.Fatalf("acquire lock: %v", err)
	}
	defer func() { _ = lock.Release() }()

	l := NewReadOnlyTranscriptLoader(sessions)
	cands, err := l.ListRecentCandidates("", 10)
	if err != nil {
		t.Fatalf("ListRecentCandidates: %v", err)
	}
	for _, c := range cands {
		if c.Path == lockDir {
			t.Fatalf("locked session listed as a candidate: %+v", c)
		}
	}
}
