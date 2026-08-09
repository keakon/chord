package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/keakon/chord/internal/privatefs"
)

func persistJSONAtomically(sessionDir, path, tempPrefix string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmpPath := filepath.Join(filepath.Dir(path), fmt.Sprintf("%s.%d.json.tmp", tempPrefix, time.Now().UnixNano()))
	if err := writeFileSynced(sessionDir, tmpPath, data); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return syncDir(filepath.Dir(path))
}

// syncDir fsyncs a directory so a completed rename survives a crash. Without
// it the rename itself can be lost and the file silently reverts to its
// previous version — consistent, but stale, which re-opens the corr-N/group-N
// alias reuse these files exist to prevent. Best-effort on platforms that
// reject directory fsync.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		// Windows FlushFileBuffers does not support directory handles. Some
		// Unix filesystems likewise report EINVAL/ENOTSUP for directory fsync.
		// The file itself was already synced before rename, so these platforms
		// retain the previous best-effort durability instead of failing every
		// otherwise-successful coordination write after the rename committed.
		if runtime.GOOS == "windows" || os.IsPermission(err) || errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOTSUP) {
			return nil
		}
		return err
	}
	return nil
}

// writeFileSynced mirrors privatefs.WriteFile but fsyncs before close so the
// subsequent rename cannot be reordered ahead of the data reaching disk.
// These JSON files gate coordination state across restarts; without the sync
// a crash could leave an empty or truncated file behind the rename.
func writeFileSynced(sessionDir, path string, data []byte) (err error) {
	f, err := privatefs.OpenFile(sessionDir, path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
	}()
	if _, err = f.Write(data); err != nil {
		return err
	}
	return f.Sync()
}
