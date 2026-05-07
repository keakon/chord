package sessionimport

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
)

const importReportFileName = "import-report.json"

func writeChordSession(projectSessionsDir string, requestedSessionID string, force bool, msgs []message.Message, report ImportReport, meta recovery.SessionMeta) (sid string, sessionDir string, err error) {
	if projectSessionsDir == "" {
		return "", "", fmt.Errorf("write session: project sessions dir is empty")
	}

	sid, err = validateSessionID(requestedSessionID)
	if err != nil {
		return "", "", err
	}
	if sid == "" {
		// Allocate an ID without creating the final directory yet; retry on collision.
		last := ""
		for range 1000 {
			candidate, next := allocateSessionID(last)
			last = next
			finalDir := filepath.Join(projectSessionsDir, candidate)
			if _, statErr := os.Stat(finalDir); statErr == nil {
				continue
			} else if os.IsNotExist(statErr) {
				sid = candidate
				break
			} else {
				return "", "", fmt.Errorf("write session: stat session dir: %w", statErr)
			}
		}
		if sid == "" {
			return "", "", fmt.Errorf("write session: allocate session id: too many collisions")
		}
	}

	finalDir := filepath.Join(projectSessionsDir, sid)
	if _, statErr := os.Stat(finalDir); statErr == nil {
		if !force {
			return "", "", fmt.Errorf("write session: %w: %s", errSessionIDExists, sid)
		}
		// Overwrite: delete old directory.
		if rmErr := os.RemoveAll(finalDir); rmErr != nil {
			return "", "", fmt.Errorf("write session: remove existing session dir: %w", rmErr)
		}
	}

	// Write into a temporary sibling directory then atomically rename.
	tmpDir := filepath.Join(projectSessionsDir, newImportingDirName(sid))
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return "", "", fmt.Errorf("write session: create temp dir: %w", err)
	}
	cleanupTmp := func() {
		_ = os.RemoveAll(tmpDir)
	}

	rm := recovery.NewRecoveryManager(tmpDir)
	defer rm.Close()

	for _, m := range msgs {
		if err := rm.PersistMessage("main", m); err != nil {
			cleanupTmp()
			return "", "", fmt.Errorf("write session: persist message: %w", err)
		}
	}

	// Write import report.
	report.ImportedAt = report.ImportedAt.UTC()
	if report.ImportedAt.IsZero() {
		report.ImportedAt = time.Now().UTC()
	}
	if err := writeJSONAtomic(filepath.Join(tmpDir, importReportFileName), report); err != nil {
		cleanupTmp()
		return "", "", fmt.Errorf("write session: write import report: %w", err)
	}

	// Write session meta (includes import provenance).
	if err := recovery.SaveSessionMeta(tmpDir, meta); err != nil {
		cleanupTmp()
		return "", "", fmt.Errorf("write session: write session meta: %w", err)
	}

	// Install.
	if err := os.Rename(tmpDir, finalDir); err != nil {
		cleanupTmp()
		return "", "", fmt.Errorf("write session: install session dir: %w", err)
	}
	return sid, finalDir, nil
}
