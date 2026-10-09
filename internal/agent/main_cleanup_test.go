package agent

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
)

func registerTestMainAgentCleanup(t *testing.T, a *MainAgent) {
	t.Helper()
	t.Cleanup(func() {
		// Tests that do not start Run must release blocked output producers and
		// join them explicitly before shutting down the remaining workers.
		a.signalStopping()
		a.cancel()
		a.outputWg.Wait()
		// Direct handler tests can launch requests without starting the worker
		// event loop. Shutdown waits on that loop's done channel only when it
		// started, so join these request goroutines explicitly.
		for _, sub := range a.subs.snapshotSubAgents() {
			if !sub.started.Load() {
				sub.cancel()
				sub.llmWG.Wait()
			}
		}
		// Some tests set started without invoking Run. Run-owning tests join Run
		// before cleanup, so neither case needs to wait on the main loop here.
		a.started.Store(false)
		if err := a.Shutdown(2 * time.Second); err != nil {
			t.Errorf("Shutdown() during test cleanup: %v", err)
		}
	})
}

func TestRestoreTestAgentCleanupDrainsWorkerWrites(t *testing.T) {
	t.Run("started_run_loop", func(t *testing.T) {
		testRestoreCleanupDrainsWorkerWrites(t, true)
	})
	t.Run("direct_handler", func(t *testing.T) {
		testRestoreCleanupDrainsWorkerWrites(t, false)
	})
}

func testRestoreCleanupDrainsWorkerWrites(t *testing.T, startRunLoop bool) {
	t.Helper()
	projectRoot := t.TempDir()
	sessionDir := filepath.Join(projectRoot, "session")
	manager := recovery.NewRecoveryManager(sessionDir)
	defer manager.Close()
	writeResult := make(chan error, 1)
	releaseWrite := make(chan struct{})
	cleanupReturned := make(chan struct{})
	returnedEarly := make(chan bool, 1)
	var a *MainAgent
	var sub *SubAgent

	t.Run("worker", func(t *testing.T) {
		a = newTestMainAgentForRestore(t, projectRoot, sessionDir)
		a.installRecoveryManager(manager)
		sub = newControllableTestSubAgent(t, a, "task-1")
		// Model the final write of a request goroutine released by cancellation.
		// SubAgent.done must join this goroutine before recovery is closed.
		sub.llmWG.Go(func() {
			<-sub.parentCtx.Done()
			<-releaseWrite
			writeResult <- manager.PersistMessage(sub.instanceID,
				message.Message{Role: "assistant", Content: "Final worker message"})
		})
		go func() {
			<-sub.parentCtx.Done()
			// Give cleanup an opportunity to return while the final write is
			// blocked. A joined worker must keep cleanup waiting for release.
			select {
			case <-cleanupReturned:
				returnedEarly <- true
			case <-time.After(50 * time.Millisecond):
				returnedEarly <- false
			}
			close(releaseWrite)
		}()
		if startRunLoop {
			sub.startRunLoop()
		}
	})
	close(cleanupReturned)
	if <-returnedEarly {
		t.Error("cleanup returned while the worker's final write was still blocked")
	}

	if a.recoveryManager() != nil {
		t.Error("cleanup left a closed recovery manager available to workers")
	}
	select {
	case err := <-writeResult:
		if err != nil {
			t.Fatalf("final worker write: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cleanup did not drain the final worker write")
	}
	if startRunLoop {
		select {
		case <-sub.done:
		default:
			t.Fatal("cleanup returned before the worker stopped")
		}
	}
	msgs, err := manager.LoadMessages(sub.instanceID)
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Content != "Final worker message" {
		t.Fatalf("persisted messages = %#v, want the final worker message", msgs)
	}
}
