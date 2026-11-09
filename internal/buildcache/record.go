//go:build unix

package buildcache

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type userRecord struct {
	BootID string `json:"boot_id,omitempty"`
}

// reapUsers removes records that provably describe dead owners. A record is
// allowed to survive process death: the supervised command may have escaped
// its process group, and a descendant in another session can keep writing
// cache entries after its parent is gone. Only a different boot proves that
// every owner recorded before it is gone. It returns how many records may
// still belong to live owners and how many stale records were removed.
func (c *Cache) reapUsers(bootID string) (live, reaped int, err error) {
	dir := filepath.Join(c.root, "users")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, 0, fmt.Errorf("read build cache users: %w", err)
	}
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		record, ok := readUserRecord(path)
		// Age and process death cannot exclude an escaped descendant.
		if !ok || bootID == "" || record.BootID == bootID {
			live++
			continue
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return live, reaped, fmt.Errorf("remove stale build cache user: %w", err)
		}
		reaped++
	}
	return live, reaped, nil
}

func readUserRecord(path string) (userRecord, bool) {
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return userRecord{}, false
	}
	var record userRecord
	if json.Unmarshal(data, &record) != nil || record.BootID == "" {
		return userRecord{}, false
	}
	return record, true
}
