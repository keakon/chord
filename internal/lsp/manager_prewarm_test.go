package lsp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
)

func newPrewarmTestManager(t *testing.T) (*Manager, string) {
	t.Helper()
	root := resolvedTempDir(t)
	return NewManager(&config.Config{LSP: config.LSPConfig{
		"gopls": {Command: "chord-no-such-language-server", FileTypes: []string{".go"}},
	}}, root, nil), filepath.Join(root, "sample.go")
}

func waitPrewarmTask(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("prewarm task did not settle")
	}
}

func TestPrewarmNonblockingAndBounded(t *testing.T) {
	m, path := newPrewarmTestManager(t)
	defer stopManager(m)
	release := make(chan struct{})
	defer close(release)
	entered := make(chan struct{}, maxPrewarmTasks)
	authorize := func() bool {
		entered <- struct{}{}
		select {
		case <-release:
		case <-time.After(5 * time.Second):
		}
		return false
	}
	for range maxPrewarmTasks {
		m.Prewarm(context.Background(), path, authorize)
		waitPrewarmTask(t, entered)
	}
	m.Prewarm(context.Background(), path, authorize)
	m.clientsMu.Lock()
	defer m.clientsMu.Unlock()
	if len(m.syncTasks) != maxPrewarmTasks || m.prewarmTasks != maxPrewarmTasks {
		t.Errorf("tasks = %d/%d", len(m.syncTasks), m.prewarmTasks)
	}
	// The callback must also return while the manager lock is held.
	returned := make(chan struct{})
	go func() {
		m.Prewarm(context.Background(), path, authorize)
		close(returned)
	}()
	waitPrewarmTask(t, returned)
}

func TestPrewarmStopAndCallerCancelBeforeLaunch(t *testing.T) {
	for _, stop := range []bool{false, true} {
		t.Run(map[bool]string{false: "caller", true: "manager"}[stop], func(t *testing.T) {
			m, path := newPrewarmTestManager(t)
			defer stopManager(m)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			entered, release := make(chan struct{}), make(chan struct{})
			m.Prewarm(ctx, path, func() bool {
				close(entered)
				select {
				case <-release:
				case <-time.After(5 * time.Second):
				}
				return true
			})
			waitPrewarmTask(t, entered)
			m.clientsMu.Lock()
			var done chan struct{}
			for task := range m.syncTasks {
				done = task
			}
			m.clientsMu.Unlock()
			if stop {
				stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
				m.Stop(stopCtx)
				stopCancel()
			} else {
				cancel()
			}
			close(release)
			waitPrewarmTask(t, done)
			m.clientsMu.Lock()
			defer m.clientsMu.Unlock()
			if len(m.launches) != 0 || len(m.clients) != 0 || m.prewarmTasks != 0 {
				t.Fatal("cancelled prewarm started a server")
			}
			if stop && m.startEra != nil {
				t.Fatal("old prewarm recreated a stopped era")
			}
		})
	}
}

func TestPrewarmRequiresGoAndAuthorization(t *testing.T) {
	m, path := newPrewarmTestManager(t)
	defer stopManager(m)
	m.Prewarm(context.Background(), strings.TrimSuffix(path, ".go")+".txt", func() bool {
		t.Error("non-Go path reached authorization")
		return true
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m.Prewarm(ctx, path, func() bool {
		t.Error("cancelled context reached authorization")
		return true
	})
	authorized := make(chan struct{})
	m.Prewarm(context.Background(), path, func() bool {
		close(authorized)
		return false
	})
	waitPrewarmTask(t, authorized)
	stopManager(m)
	if len(m.startFail) != 0 {
		t.Fatal("denied prewarm attempted to launch a server")
	}
	m.cfg.Diagnostics.Enabled = new(false)
	m.Prewarm(context.Background(), path, func() bool {
		t.Error("disabled diagnostics reached authorization")
		return true
	})
}

func TestRealGoplsPrewarmReusedAfterCallerCancellation(t *testing.T) {
	gopls := requireRealGopls(t)
	root := resolvedTempDir(t)
	path := writeBrokenGoCheckout(t, root, "example.com/prewarm", "missingSampleSymbol")
	m := NewManager(&config.Config{LSP: config.LSPConfig{
		"gopls": {Command: gopls, FileTypes: []string{".go"}, RootMarkers: []string{"go.mod"}},
	}}, root, nil)
	defer stopManager(m)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	began := time.Now()
	m.Prewarm(ctx, path, func() bool {
		close(entered)
		select {
		case <-release:
		case <-time.After(5 * time.Second):
		}
		return true
	})
	waitPrewarmTask(t, entered)
	m.clientsMu.Lock()
	var taskDone chan struct{}
	for done := range m.syncTasks {
		taskDone = done
	}
	m.clientsMu.Unlock()
	close(release)
	waitPrewarmTask(t, taskDone)
	key := testKey(m, "gopls")
	m.clientsMu.RLock()
	entry, client := m.launches[key], m.clients[key]
	m.clientsMu.RUnlock()
	if entry == nil && client == nil {
		t.Fatalf("prewarm did not launch gopls: %v", formattedStartFailures(m, path))
	}
	cancel()
	// The original launch must survive cancellation; do not call Start or a
	// wait helper that could silently replace it.
	if entry != nil {
		select {
		case <-entry.ready:
		case <-time.After(30 * time.Second):
			t.Fatal("prewarmed server did not become ready")
		}
	}
	m.clientsMu.RLock()
	client = m.clients[key]
	m.clientsMu.RUnlock()
	if client == nil {
		t.Fatalf("prewarm launch was lost on caller cancellation: %v", formattedStartFailures(m, path))
	}
	if version := client.syncedDocumentVersion(path); version != 0 {
		t.Fatalf("prewarm opened a document: version=%d", version)
	}
	if len(m.TouchedPaths()) != 0 || len(m.ReviewedPaths()) != 0 {
		t.Fatal("prewarm marked a file touched or reviewed")
	}
	lead := time.Since(began)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	writeCtx, writeCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer writeCancel()
	wrote := time.Now()
	out := m.AfterFileWriteToolResult(writeCtx, path, string(content), "wrote sample.go", true, WatchedFileChanged, "")
	if !strings.Contains(out, "missingSampleSymbol") {
		t.Fatalf("prewarmed write missed diagnostics: %q", out)
	}
	m.clientsMu.RLock()
	sameClient := m.clients[key] == client
	m.clientsMu.RUnlock()
	if !sameClient {
		t.Fatal("write did not reuse the prewarmed client")
	}
	t.Logf("prewarm lead=%v; write diagnostics=%v", lead, time.Since(wrote))
}
