package lsp

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/keakon/x/powernap/pkg/lsp/protocol"
)

func TestSyncAfterWriteExcludesServersWithAnyNotificationFailure(t *testing.T) {
	for _, tc := range []struct {
		name string
		call int
	}{
		{"watched", 1},
		{"open", 2},
		{"save", 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			failure := errors.New("notification rejected")
			mgr, path, _, fakes := newAfterWriteServerPair(t, func(context.Context) error {
				calls++
				if calls == tc.call {
					return failure
				}
				return nil
			}, nil)
			fakes["gopls"].saveOptions = &protocol.SaveOptions{}
			sync := mgr.syncAfterWrite(context.Background(), path, "package main", WatchedFileChanged, true)
			if calls != 3 || !errors.Is(sync.serverErrors["gopls"], failure) {
				t.Fatalf("calls=%d errors=%v, want the selected notification failure", calls, sync.serverErrors)
			}
			if _, ok := sync.serverVersions["gopls"]; ok {
				t.Fatal("a server that missed a notification must not verify the write")
			}
			if len(sync.serverVersions) != 1 || sync.serverVersions["samplelsp"] == 0 {
				t.Fatalf("versions=%v, want only the successfully synchronized server", sync.serverVersions)
			}
		})
	}
}

func TestAfterWriteWaitRejectsFailedServerEvents(t *testing.T) {
	for _, semantic := range []bool{false, true} {
		name := "generic"
		if semantic {
			name = "semantic"
		}
		t.Run(name, func(t *testing.T) {
			for _, healthyEvent := range []bool{false, true} {
				name := "failed_only"
				if healthyEvent {
					name = "healthy_then_failed"
				}
				t.Run(name, func(t *testing.T) {
					mgr, path, _, _ := newAfterWriteServerPair(t,
						func(context.Context) error { return errors.New("notification rejected") }, nil)
					oldStart, oldAwait := afterWriteStart, afterWriteAwaitWaiter
					t.Cleanup(func() { afterWriteStart, afterWriteAwaitWaiter = oldStart, oldAwait })
					afterWriteStart = func(*Manager, context.Context, string) {}
					waited := false
					afterWriteAwaitWaiter = func(m *Manager, ctx context.Context, p string, ch chan diagnosticsEvent, req diagnosticsWaitRequest, _ time.Duration) ([]Diagnostic, bool) {
						waited = true
						if healthyEvent {
							ch <- diagnosticsEvent{serverID: "samplelsp", version: req.serverVersions["samplelsp"], receivedAt: req.after,
								diagnostics: []Diagnostic{{Message: "current diagnostic"}}}
						}
						// A versioned old publish and an unversioned late publish from
						// the failed server must neither start nor replace settlement.
						ch <- diagnosticsEvent{serverID: "gopls", version: 1, receivedAt: req.after.Add(-time.Second),
							diagnostics: []Diagnostic{{Message: "old diagnostic"}}}
						ch <- diagnosticsEvent{serverID: "gopls", receivedAt: req.after.Add(time.Second),
							diagnostics: []Diagnostic{{Message: "late diagnostic"}}}
						req.settle = time.Millisecond
						diags, notified := m.AwaitFreshWaiter(ctx, p, ch, req, 20*time.Millisecond)
						if notified != healthyEvent {
							t.Fatalf("notified=%v, want %v", notified, healthyEvent)
						}
						if healthyEvent && (len(diags) != 1 || diags[0].Message != "current diagnostic") {
							t.Fatalf("diagnostics=%v, want the healthy server's result", diags)
						}
						return diags, notified
					}
					if semantic {
						mgr.afterWriteLSPToolResult(context.Background(), path, "package main", "written", false, nil, WatchedFileChanged, "")
					} else {
						mgr.AfterFileWriteToolResult(context.Background(), path, "package main", "written", false, WatchedFileChanged, "")
					}
					if !waited || len(mgr.waiters[path]) != 0 {
						t.Fatalf("waited=%v remaining waiters=%d", waited, len(mgr.waiters[path]))
					}
				})
			}
		})
	}
}

func TestDiagnosticsEventFreshServerScope(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name     string
		versions map[string]int32
		server   string
		version  int32
		at       time.Time
		want     bool
	}{
		{"unrestricted", nil, "sample", 1, now, true},
		{"empty selection", map[string]int32{}, "sample", 1, now, false},
		{"unknown server", map[string]int32{"sample": 2}, "other", 2, now, false},
		{"unknown unversioned server", map[string]int32{"sample": 2}, "other", 0, now, false},
		{"matching version", map[string]int32{"sample": 2}, "sample", 2, now, true},
		{"old version", map[string]int32{"sample": 2}, "sample", 1, now, false},
		{"fresh unversioned", map[string]int32{"sample": 2}, "sample", 0, now, true},
		{"old unversioned", map[string]int32{"sample": 2}, "sample", 0, now.Add(-time.Second), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ev := diagnosticsEvent{serverID: tc.server, version: tc.version, receivedAt: tc.at}
			if got := diagnosticsEventFresh(ev, diagnosticsWaitRequest{serverVersions: tc.versions, after: now}); got != tc.want {
				t.Fatalf("fresh=%v, want %v", got, tc.want)
			}
		})
	}
}
