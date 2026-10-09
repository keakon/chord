package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
)

func TestNativeReceiptPersistenceRequiresDirectorySync(t *testing.T) {
	for _, kind := range []string{"main", "sub", "standalone", "standalone_async"} {
		for _, unknown := range []bool{false, true} {
			t.Run(kind+map[bool]string{false: "/completed", true: "/unknown"}[unknown], func(t *testing.T) {
				dir := filepath.Join(t.TempDir(), "session")
				if err := os.Mkdir(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				manager := recovery.NewRecoveryManager(dir)
				t.Cleanup(manager.Close)
				agentID := "worker-1"
				if kind == "main" {
					agentID = identity.MainAgentID
				}
				// Keep an open transcript handle, then remove only its directory
				// path. Writes still succeed, but synchronizing that path fails.
				if err := manager.PersistMessage(agentID, message.Message{Role: message.RoleUser, Content: "Sample input"}); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(dir, dir+"-moved"); err != nil {
					t.Fatal(err)
				}
				msg := message.Message{Role: message.RoleAssistant, NativeTools: &message.NativeToolHistory{OutcomeUnknown: unknown}}
				var err error
				if kind == "standalone" || kind == "standalone_async" {
					sub := &SubAgent{recovery: manager, instanceID: agentID}
					if kind == "standalone_async" {
						called := false
						sub.persistMessageAsync(msg, "receipt", func() { called = true })
						if called || sub.PersistenceHealth().State != PersistenceDegraded {
							t.Fatal("async receipt bypassed the durable write failure")
						}
						return
					}
					barrier, _ := sub.persistMessageBarrier(msg, "receipt")
					err = <-barrier
				} else {
					a := &MainAgent{persist: newPersistencePump(4), recoveryOwner: &recoveryOwnership{manager: manager}}
					a.startPersistLoop()
					t.Cleanup(func() { a.persist.closeUntil(nil); <-a.persist.done })
					if kind == "main" {
						barrier := make(chan error, 1)
						if !a.persistAsyncAfter(agentID, msg, func(err error) { barrier <- err }) {
							t.Fatal("receipt was not enqueued")
						}
						err = <-barrier
					} else {
						// Sub-agent writes enter the same pump through the epoch gate.
						barrier := make(chan error, 1)
						if !a.persistAsyncForEpoch(0, agentID, msg, func(err error) { barrier <- err }) {
							t.Fatal("sub receipt was not enqueued")
						}
						err = <-barrier
					}
				}
				if err == nil || !strings.Contains(err.Error(), "sync message directory") {
					t.Fatalf("receipt persistence = %v, want directory sync failure", err)
				}
			})
		}
	}
}
