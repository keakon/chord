package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
