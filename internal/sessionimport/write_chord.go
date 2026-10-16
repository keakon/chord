package sessionimport

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/privatefs"
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
	if err := privatefs.EnsureDir(tmpDir, tmpDir); err != nil {
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
	if err := writeJSONAtomic(tmpDir, filepath.Join(tmpDir, importReportFileName), report); err != nil {
		cleanupTmp()
		return "", "", fmt.Errorf("write session: write import report: %w", err)
	}

	// Write session meta (includes import provenance).
	if err := recovery.SaveSessionMeta(tmpDir, meta); err != nil {
		cleanupTmp()
		return "", "", fmt.Errorf("write session: write session meta: %w", err)
	}

	// Attachment files were materialized while the session was still the staging
	// directory, so the transcript names them there; the recorded paths have to
	// name where they will be after installation or every imported attachment
	// resolves to a directory that no longer exists. The rewrite happens inside
	// the staging directory, before the rename, so a failure leaves nothing
	// installed and the install itself stays atomic.
	if err := rewriteStagedAttachmentPaths(tmpDir, tmpDir, finalDir); err != nil {
		cleanupTmp()
		return "", "", fmt.Errorf("write session: rewrite attachment paths: %w", err)
	}

	// Install.
	if err := os.Rename(tmpDir, finalDir); err != nil {
		cleanupTmp()
		return "", "", fmt.Errorf("write session: install session dir: %w", err)
	}
	return sid, finalDir, nil
}

// rewriteStagedAttachmentPaths points attachment paths recorded under dir at the
// directory the session will be installed as. The recovery manager materializes
// every image/PDF part as <sessionDir>/images/<file>, so only that prefix needs
// rewriting: matching the staging directory itself would also rewrite transcript
// text that happens to name the temporary path. Sessions without attachments
// never mention the path, so the common case is a read that finds nothing to do.
//
// dir is the directory holding the transcript to rewrite; stagedDir and
// sessionDir are the two spellings to replace between. Rewriting while the
// transcript still lives in the staging directory keeps a crash mid-write from
// installing a truncated session: the file is renamed into place only after the
// complete rewritten bytes are on disk.
func rewriteStagedAttachmentPaths(dir, stagedDir, sessionDir string) error {
	if stagedDir == sessionDir {
		return nil
	}
	path := filepath.Join(dir, identity.MainSessionLogFilename)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	// The transcript spells paths as JSON strings, so on Windows each separator
	// reaches the file doubled; match the spelling the log actually carries.
	stagedImages := jsonPathBytes(filepath.Join(stagedDir, "images"))
	if !bytes.Contains(data, stagedImages) {
		return nil
	}
	// Sync the rewritten bytes before the rename installs them: the rename then
	// publishes a complete file, and a crash before it leaves only the staging
	// directory behind.
	return privatefs.WriteFileSynced(dir, path, bytes.ReplaceAll(data, stagedImages, jsonPathBytes(filepath.Join(sessionDir, "images"))))
}

// jsonPathBytes renders p the way a JSON string in the transcript spells it.
func jsonPathBytes(p string) []byte {
	return []byte(strings.ReplaceAll(p, `\`, `\\`))
}
