package recovery

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/keakon/chord/internal/privatefs"
)

// SyncMessages establishes the transcript side of the delivery barrier without
// appending a second copy. The caller must first drain its persistence pump.
func (r *RecoveryManager) SyncMessages(agentID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return ErrClosed
	}
	f := r.handles[agentID]
	if f == nil {
		var err error
		f, err = privatefs.OpenFile(r.sessionDir, r.messageLogPath(agentID), os.O_RDWR)
		if err != nil {
			return fmt.Errorf("open transcript barrier: %w", err)
		}
		defer f.Close()
	}
	syncFile := r.messageSync
	if syncFile == nil {
		syncFile = (*os.File).Sync
	}
	if err := syncFile(f); err != nil {
		return fmt.Errorf("sync transcript barrier: %w", err)
	}
	if err := privatefs.SyncDir(filepath.Dir(r.messageLogPath(agentID))); err != nil {
		return fmt.Errorf("sync transcript directory: %w", err)
	}
	return nil
}
