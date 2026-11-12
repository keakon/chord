package maintenance

import (
	"fmt"
	"os"
	"path/filepath"
)

// cleanupLockFileName is the non-blocking sweep dedup lock inside the state
// dir. The lock only de-duplicates processes sharing the same state dir: the
// sessions root can be configured independently, so cross-state-dir sweeps of
// one sessions root stay possible (and remain safe — each deletion carries its
// own session-lock protocol). The file is never removed: a persistent empty
// file avoids create/delete races between concurrent acquirers.
const cleanupLockFileName = "maintenance.lock"

// acquireCleanupLock takes the sweep lock without blocking. It returns a
// release func on success, or skipped non-empty when another process holds the
// lock and the sweep should be skipped.
func acquireCleanupLock(stateDir string) (release func(), skipped string, err error) {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, "", fmt.Errorf("create state dir: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(stateDir, cleanupLockFileName), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, "", fmt.Errorf("open %s: %w", cleanupLockFileName, err)
	}
	if err := lockCleanupFile(f); err != nil {
		_ = f.Close()
		if isCleanupWouldBlock(err) {
			return nil, "another Chord process holds the maintenance lock", nil
		}
		return nil, "", fmt.Errorf("lock %s: %w", cleanupLockFileName, err)
	}
	return func() {
		_ = unlockCleanupFile(f)
		_ = f.Close()
	}, "", nil
}
