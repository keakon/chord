package lsp

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

func waitWritePreparations(t *testing.T, m *Manager) {
	t.Helper()
	m.clientsMu.RLock()
	var tasks []chan struct{}
	for done := range m.syncTasks {
		tasks = append(tasks, done)
	}
	m.clientsMu.RUnlock()
	for _, done := range tasks {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("write preparation did not exit")
		}
	}
}

func TestColdBatchBoundsBlockingSyncAndKeepsLateResults(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m, alpha, beta := newBatchTestManager(t)
		m.admittedAt[testKey(m, "gopls")] = time.Now()
		for _, path := range []string{alpha, beta} {
			if err := os.WriteFile(path, []byte("package main\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		afterWriteDidChange = func(m *Manager, ctx context.Context, path, content string) (map[string]int32, map[clientKey]uint64, map[string]error) {
			time.Sleep(2 * time.Second)
			return m.didChangeVersions(ctx, path, content)
		}
		began := time.Now()
		results := m.AfterFileWritesToolResult(context.Background(), []FileWrite{
			{Path: alpha, Content: "package main\n", ChangeType: WatchedFileChanged},
			{Path: beta, Content: "package main\n", ChangeType: WatchedFileChanged},
		}, "")
		if elapsed := time.Since(began); elapsed != coldStartWriteBudget {
			t.Fatalf("foreground elapsed = %v, want %v", elapsed, coldStartWriteBudget)
		}
		for path, result := range results {
			if result.Status != FileWriteStatusTimeout || strings.Contains(result.Detail, "within 0s") {
				t.Fatalf("result for %s = %+v", path, result)
			}
		}
		waitWritePreparations(t, m)
		client := m.clients[testKey(m, "gopls")]
		for _, path := range []string{alpha, beta} {
			publishWarmupDiagnostic(m, client, path)
		}
		if got := m.PendingLateDiagnostics([]string{alpha, beta}, "", ""); len(got) != 2 {
			t.Fatalf("late results = %+v, want both files", got)
		}
	})
}

func TestColdBatchDeductsAllSyncTimeAndConfiguredDiagnosticLimit(t *testing.T) {
	for _, waitMS := range []int{100, 3000} {
		t.Run(time.Duration(waitMS*int(time.Millisecond)).String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				m, alpha, beta := newBatchTestManager(t)
				m.admittedAt[testKey(m, "gopls")] = time.Now()
				m.cfg.Diagnostics.LSP.WaitTimeoutMS = new(waitMS)
				afterWriteDidChange = func(m *Manager, ctx context.Context, path, content string) (map[string]int32, map[clientKey]uint64, map[string]error) {
					time.Sleep(800 * time.Millisecond)
					return m.didChangeVersions(ctx, path, content)
				}
				afterWriteAwaitWaiter = func(_ *Manager, _ context.Context, _ string, _ chan diagnosticsEvent, _ diagnosticsWaitRequest, timeout time.Duration) ([]Diagnostic, bool, uint64, map[diagnosticIdentity]struct{}) {
					want := min(time.Duration(waitMS)*time.Millisecond, 900*time.Millisecond)
					if timeout != want {
						t.Errorf("diagnostic timeout = %v, want %v", timeout, want)
					}
					time.Sleep(timeout)
					return nil, false, 0, nil
				}
				began := time.Now()
				m.AfterFileWritesToolResult(context.Background(), []FileWrite{
					{Path: alpha, Content: "package main\n", ChangeType: WatchedFileChanged},
					{Path: beta, Content: "package main\n", ChangeType: WatchedFileChanged},
				}, "")
				if elapsed := time.Since(began); elapsed > coldStartWriteBudget {
					t.Fatalf("elapsed = %v, exceeds total budget", elapsed)
				}
			})
		})
	}
}

func TestWritePreparationCancellationAndTimeoutOwnership(t *testing.T) {
	for _, timedOut := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "timeout"}[timedOut], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				m, _, _ := newBatchTestManager(t)
				ctx, cancel := context.WithCancel(context.Background())
				began := time.Now()
				release := make(chan struct{})
				taskDone := make(chan struct{})
				if !timedOut {
					go func() {
						time.Sleep(10 * time.Millisecond)
						cancel()
					}()
				}
				completed := m.runWritePreparation(ctx, began, true, func(taskCtx context.Context) {
					select {
					case <-release:
						if taskCtx.Err() != nil {
							t.Error("timeout worker inherited later turn cancellation")
						}
					case <-taskCtx.Done():
						if timedOut {
							t.Error("timeout worker was cancelled with its turn")
						}
					}
				}, func(context.Context) { close(taskDone) })
				if completed {
					t.Fatal("blocked preparation completed in foreground")
				}
				cancel()
				if timedOut {
					synctest.Wait()
					close(release)
				}
				select {
				case <-taskDone:
				case <-time.After(time.Second):
					t.Fatal("detached preparation did not finish")
				}
				waitWritePreparations(t, m)
			})
		})
	}
}

type blockedWriteClient struct {
	fakePowernapClient
	entered chan struct{}
	killed  chan struct{}
	once    sync.Once
}

func (c *blockedWriteClient) NotifyDidOpenTextDocument(context.Context, string, string, int, string) error {
	close(c.entered)
	select {
	case <-c.killed:
		return context.Canceled
	case <-time.After(2 * time.Second):
		return context.DeadlineExceeded
	}
}

func (c *blockedWriteClient) Kill() {
	c.once.Do(func() { close(c.killed) })
}

func TestStopReleasesRegisteredPendingSync(t *testing.T) {
	m, path := newWarmupTestManager(t)
	m.registerPendingSync(path, "package main\n", WatchedFileChanged, "")
	blocked := &blockedWriteClient{entered: make(chan struct{}), killed: make(chan struct{})}
	client := pendingOwnerClient(m, "gopls")
	client.client = blocked
	m.clientsMu.Lock()
	m.clients[testKey(m, "gopls")] = client
	m.startPendingResyncLocked()
	m.clientsMu.Unlock()
	select {
	case <-blocked.entered:
	case <-time.After(time.Second):
		t.Fatal("pending sync did not enter protocol writer")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	m.Stop(ctx)
	if ctx.Err() != nil {
		t.Fatalf("Stop did not release blocked sync: %v", ctx.Err())
	}
	waitWritePreparations(t, m)
	m.lateMu.Lock()
	watches := len(m.lateWatches)
	m.lateMu.Unlock()
	if watches != 0 || m.pendingSyncCount() != 0 {
		t.Fatalf("shutdown left late watches=%d pending=%d", watches, m.pendingSyncCount())
	}
}

func TestColdBatchExternalSyncBeforeWatchInvalidatesBatch(t *testing.T) {
	m, alpha, beta := newBatchTestManager(t)
	external := beta + ".external.go"
	synced := 0
	afterWriteDidChange = func(m *Manager, ctx context.Context, path, content string) (map[string]int32, map[clientKey]uint64, map[string]error) {
		versions, generations, failures := m.didChangeVersions(ctx, path, content)
		synced++
		if synced == 2 {
			m.didChangeVersions(ctx, external, "package main\n")
		}
		return versions, generations, failures
	}
	afterWriteAwaitWaiter = func(*Manager, context.Context, string, chan diagnosticsEvent, diagnosticsWaitRequest, time.Duration) ([]Diagnostic, bool, uint64, map[diagnosticIdentity]struct{}) {
		return nil, false, 0, nil
	}
	for _, path := range []string{alpha, beta} {
		if err := os.WriteFile(path, []byte("package main\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	m.AfterFileWritesToolResult(context.Background(), []FileWrite{
		{Path: alpha, Content: "package main\n", ChangeType: WatchedFileChanged},
		{Path: beta, Content: "package main\n", ChangeType: WatchedFileChanged},
	}, "")
	client := m.clients[testKey(m, "gopls")]
	for _, path := range []string{alpha, beta} {
		publishWarmupDiagnostic(m, client, path)
	}
	if got := m.PendingLateDiagnostics([]string{alpha, beta}, "", ""); len(got) != 0 {
		t.Fatalf("external sync was absorbed into batch generation: %+v", got)
	}
}

func TestPendingSyncReplacementDoesNotInheritOldVersion(t *testing.T) {
	m, path := newWarmupTestManager(t)
	key := testKey(m, "gopls")
	old := pendingOwnerClient(m, "gopls")
	m.clientsMu.Lock()
	m.clients[key] = old
	m.clientsMu.Unlock()
	if _, err := old.DidChange(context.Background(), path, "package sample\n"); err != nil {
		t.Fatal(err)
	}
	// The ready owner schedules a re-sync right away. Holding the re-sync gate
	// keeps that goroutine from consuming the entry under the old client before
	// the replacement is installed.
	m.pendingResyncMu.Lock()
	release := sync.OnceFunc(m.pendingResyncMu.Unlock)
	t.Cleanup(release)
	m.registerPendingSync(path, "package main\n", WatchedFileChanged, "")
	replacement := pendingOwnerClient(m, "gopls")
	m.clientsMu.Lock()
	m.clients[key] = replacement
	m.clientsMu.Unlock()
	release()
	m.resyncPendingWrites(context.Background())
	if got := replacement.syncedDocumentVersion(path); got != 1 {
		t.Fatalf("replacement version=%d, want first sync", got)
	}
	replacementFake := replacement.client.(*fakePowernapClient)
	if len(replacementFake.didOpenURIs) != 1 || len(replacementFake.didChangeURIs) != 0 {
		t.Fatalf("replacement notifications=%v", replacementFake.notifyOrder)
	}
}
