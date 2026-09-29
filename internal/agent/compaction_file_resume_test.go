package agent

import (
	"testing"

	"github.com/keakon/chord/internal/message"
)

// Activating a loaded session is the /resume boundary: checkpoint file
// snapshots cached for the replaced session must not replay into it.
func TestActivateLoadedSessionResetsCompactionFileReplay(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.compactionFiles.checkpoint = "previous checkpoint"
	a.compactionFiles.root = "/previous-root"
	a.compactionFiles.paths = []string{"notes.md"}
	a.compactionFiles.versions = []compactionFileVersion{{before: 1, anchor: "a", message: message.Message{Role: message.RoleUser}, bytes: 1}}

	a.activateLoadedSession(&loadedSessionState{SessionPath: a.sessionDir})

	a.compactionFiles.mu.Lock()
	defer a.compactionFiles.mu.Unlock()
	if a.compactionFiles.checkpoint != "" || a.compactionFiles.root != "" || a.compactionFiles.paths != nil || a.compactionFiles.versions != nil {
		t.Fatalf("session activation kept the replaced session's file snapshots: %+v", a.compactionFiles.versions)
	}
}
