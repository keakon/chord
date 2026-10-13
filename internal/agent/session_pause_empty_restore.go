package agent

import (
	"path/filepath"

	"github.com/keakon/chord/internal/recovery"
)

// A paused checkpoint can carry accepted inputs even before the first model
// request. Its sealed state, rather than a nonempty transcript, proves that
// this session can be restored without starting execution.
func completePausedSessionWithoutMessages(manager *recovery.RecoveryManager, sessionPath string) bool {
	snapshot, err := manager.Recover()
	if err != nil || snapshot.Pause == nil {
		return false
	}
	pause := snapshot.Pause
	return (pause.Paused || len(pause.PendingInputs) > 0) && pause.CheckpointComplete && pause.Identity.SessionID == filepath.Base(sessionPath)
}
