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
	if err := privatefs.WriteFile(sessionDir, tmpPath, data); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}
