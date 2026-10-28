package agent

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/message"
)

// The Responses prompt_cache_key / client_metadata pair is part of the Codex
// client contract for several relays, and the identity behind it lives on the
// llm.Client. Every path that installs a client must therefore re-apply it:
// a freshly built client would otherwise send the first request of a session
// without the pair and be rejected with a 400 no key rotation can fix.
func TestEnsureLLMSessionIdentityPinsIdentityOnFreshClient(t *testing.T) {
	t.Parallel()
	a := &MainAgent{sessionDir: filepath.Join("/sessions", "proj", "session-a")}
	client := newTestLLMClient()
	a.ensureLLMSessionIdentity(client)
	if got := client.SessionKey(); got != "session-a" {
		t.Fatalf("SessionKey = %q, want the session directory base", got)
	}
}

// A session directory that is not usable as an identity must leave the client
// untouched rather than pinning a placeholder like "." on it.
func TestEnsureLLMSessionIdentityIgnoresUnusableSessionDir(t *testing.T) {
	t.Parallel()
	a := &MainAgent{}
	client := newTestLLMClient()
	a.ensureLLMSessionIdentity(client)
	if got := client.SessionKey(); got != "" {
		t.Fatalf("SessionKey = %q, want empty without a session directory", got)
	}
	a.ensureLLMSessionIdentity(nil)
}

func TestMainSessionIdentityWindowTracksCompactionCheckpoints(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	sessionDir := a.sessionDir
	if id := a.mainSessionIdentity(sessionDir); id.WindowNumber != 0 {
		t.Fatalf("window number = %d, want 0 before any compaction", id.WindowNumber)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "history-3.md"), []byte("summary"), 0o600); err != nil {
		t.Fatal(err)
	}
	if id := a.mainSessionIdentity(sessionDir); id.WindowNumber != 0 {
		t.Fatalf("uncommitted archive advanced the window: %d", id.WindowNumber)
	}
	a.ctxMgr.RestoreMessages([]message.Message{{
		Role: message.RoleUser, Content: "checkpoint", IsCompactionSummary: true, CompactionWindowNumber: 2,
	}})
	id := a.mainSessionIdentity(sessionDir)
	if id.Key != filepath.Base(sessionDir) {
		t.Fatalf("session key = %q, want the session directory base", id.Key)
	}
	if id.WindowNumber != 2 {
		t.Fatalf("window number = %d, want restored checkpoint window 2", id.WindowNumber)
	}
}

func TestClientSessionBindingDoesNotHoldStateLockWhileReadingHistory(t *testing.T) {
	for _, auxiliary := range []bool{false, true} {
		t.Run(map[bool]string{false: "main", true: "auxiliary"}[auxiliary], func(t *testing.T) {
			a := &MainAgent{sessionDir: "/sessions/session-a", ctxMgr: ctxmgr.NewManager(8192, 0.8)}
			client := newTestLLMClient()
			locked := make(chan struct{})
			release := make(chan struct{})
			unlockHistory := sync.OnceFunc(func() { close(release) })
			rewritten := make(chan error, 1)
			go func() {
				rewritten <- a.ctxMgr.ReplacePrefixAtomic(0, nil, func([]message.Message) ([]message.Message, error) {
					close(locked)
					select {
					case <-release:
					case <-time.After(5 * time.Second):
						return nil, context.DeadlineExceeded
					}
					return nil, nil
				})
			}()
			defer unlockHistory()
			select {
			case <-locked:
			case <-time.After(time.Second):
				t.Fatal("history transaction did not start")
			}
			bound := make(chan struct{})
			go func() {
				if auxiliary {
					a.bindAuxClientSession(client)
				} else {
					a.ensureLLMSessionIdentity(client)
				}
				close(bound)
			}()
			select {
			case <-bound:
				t.Fatal("binding did not wait for the history transaction")
			case <-time.After(20 * time.Millisecond):
			}
			if !a.stateMu.TryLock() {
				t.Fatal("binding held stateMu while waiting for history")
			}
			a.sessionDir = "/sessions/session-b"
			a.stateMu.Unlock()
			unlockHistory()
			select {
			case err := <-rewritten:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("history transaction did not finish")
			}
			select {
			case <-bound:
			case <-time.After(time.Second):
				t.Fatal("binding did not finish")
			}
			if got := client.SessionKey(); got != "session-b" {
				t.Fatalf("binding used a stale session: %q", got)
			}
		})
	}
}
