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
		// Some tests set started without invoking Run. Run-owning tests join Run
		// before cleanup, so neither case needs to wait on the main loop here.
		a.started.Store(false)
		if err := a.Shutdown(2 * time.Second); err != nil {
			t.Errorf("Shutdown() during test cleanup: %v", err)
		}
	})
}

func TestRestoreTestAgentCleanupDrainsWorkerWrites(t *testing.T) {
	projectRoot := t.TempDir()
	sessionDir := filepath.Join(projectRoot, "session")
	manager := recovery.NewRecoveryManager(sessionDir)
	defer manager.Close()
	writeResult := make(chan error, 1)
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
			writeResult <- manager.PersistMessage(sub.instanceID,
				message.Message{Role: "assistant", Content: "Final worker message"})
		})
		sub.startRunLoop()
	})

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
	select {
	case <-sub.done:
	default:
		t.Fatal("cleanup returned before the worker stopped")
	}
	msgs, err := manager.LoadMessages(sub.instanceID)
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Content != "Final worker message" {
		t.Fatalf("persisted messages = %#v, want the final worker message", msgs)
	}
}
