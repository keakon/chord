package recovery

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/keakon/chord/internal/analytics"
	"github.com/keakon/chord/internal/identity"
)

// SessionActivityTime reports the freshest activity signal and whether a
// non-empty transcript or input queue exists. Unreadable or non-regular
// activity files return an error; only missing files count as absent.
func SessionActivityTime(dir string) (time.Time, bool, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("open session activity directory: %w", err)
	}
	defer root.Close()
	return sessionActivityAt(root)
}

func sessionActivityAt(root *os.Root) (time.Time, bool, error) {
	var files [3]os.FileInfo
	for i, name := range []string{identity.MainSessionLogFilename, InputDeliveryFile, analytics.SessionUsageSummaryFileName} {
		info, err := root.Lstat(name)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return time.Time{}, false, fmt.Errorf("stat session activity %s: %w", name, err)
		}
		if !info.Mode().IsRegular() {
			return time.Time{}, false, fmt.Errorf("session activity %s is not a regular file", name)
		}
		files[i] = info
	}
	lastActive, present := sessionActivityFromFiles(files[0], files[1], files[2])
	return lastActive, present, nil
}

func sessionActivityFromFiles(mainInfo, queueInfo, summaryInfo os.FileInfo) (time.Time, bool) {
	if mainInfo == nil || mainInfo.Size() == 0 {
		mainInfo = queueInfo
		if mainInfo == nil || mainInfo.Size() == 0 {
			return time.Time{}, false
		}
	}
	lastActive := mainInfo.ModTime()
	for _, info := range []os.FileInfo{queueInfo, summaryInfo} {
		if info != nil && info.ModTime().After(lastActive) {
			lastActive = info.ModTime()
		}
	}
	return lastActive, true
}
