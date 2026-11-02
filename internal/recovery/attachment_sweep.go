package recovery

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// AttachmentSweepResult reports what a startup orphan sweep did.
type AttachmentSweepResult struct {
	// Removed names the attachment files deleted, relative to images/.
	Removed []string
	// KeptNew counts files left alone only because they are newer than the
	// grace period: they may belong to an input that has not been committed.
	KeptNew int
	// Bytes is the total size of Removed.
	Bytes int64
}

// attachmentCandidate is an attachment file old enough to be judged.
type attachmentCandidate struct {
	name string
	size int64
}

// sweepReferenceExtensions are the durable file kinds that can carry an
// attachment reference. The JSONL transcript, the snapshot, the pending-input
// queue, compacted checkpoints and image operation manifests all qualify.
var sweepReferenceExtensions = map[string]struct{}{
	".json":  {},
	".jsonl": {},
	".md":    {},
}

// SweepOrphanAttachments deletes attachment files under images/ that no durable
// record in sessionDir refers to.
//
// This is a startup-only repair, not a runtime GC. It is safe there because at
// startup every in-memory message was loaded from a durable record, so
// "referenced by no file" really does mean orphaned. Running it while inputs
// are in flight would race with an attachment that has been written but whose
// message is not committed yet — hence the caller must own the session lock and
// pass a grace period that comfortably exceeds that window.
//
// A file is kept when its base name appears verbatim in a durable
// .json/.jsonl/.md file in the session directory. Diagnostic-only directories
// (dumps, traces, job logs) are skipped because they cannot own an attachment.
// Attachment names are nanosecond timestamps plus a part index, so a textual
// match is unambiguous and does not depend on the schema of each record kind.
// Over-matching only keeps a file, which is the safe direction.
func SweepOrphanAttachments(sessionDir string, now time.Time, minAge time.Duration) (AttachmentSweepResult, error) {
	var res AttachmentSweepResult
	sessionDir = strings.TrimSpace(sessionDir)
	if sessionDir == "" {
		return res, fmt.Errorf("session dir is required")
	}
	if err := requireSessionLockOwnership(sessionDir); err != nil {
		return res, err
	}
	imagesDir := filepath.Join(sessionDir, "images")
	entries, err := os.ReadDir(imagesDir)
	if err != nil {
		if os.IsNotExist(err) {
			return res, nil
		}
		return res, fmt.Errorf("read attachment dir: %w", err)
	}
	if len(entries) == 0 {
		return res, nil
	}
	if now.IsZero() {
		now = time.Now()
	}

	// Candidates are the regular files old enough to be judged. Anything newer
	// stays regardless of references.
	candidates := make([]attachmentCandidate, 0, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return res, fmt.Errorf("stat attachment %s: %w", entry.Name(), err)
		}
		// Symlinks and other non-regular entries are never payloads we wrote;
		// leave them alone rather than following them out of images/.
		if !info.Mode().IsRegular() {
			continue
		}
		if minAge > 0 && now.Sub(info.ModTime()) < minAge {
			res.KeptNew++
			continue
		}
		candidates = append(candidates, attachmentCandidate{name: entry.Name(), size: info.Size()})
	}
	if len(candidates) == 0 {
		return res, nil
	}

	referenced, err := collectAttachmentReferences(sessionDir, imagesDir, candidates)
	if err != nil {
		return res, err
	}
	for _, cand := range candidates {
		if _, ok := referenced[cand.name]; ok {
			continue
		}
		if err := os.Remove(filepath.Join(imagesDir, cand.name)); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return res, fmt.Errorf("remove orphan attachment %s: %w", cand.name, err)
		}
		res.Removed = append(res.Removed, cand.name)
		res.Bytes += cand.size
	}
	return res, nil
}

// requireSessionLockOwnership refuses to sweep unless this process owns the
// session lock. Comparing the recorded owner is the only way to tell "we hold
// it" from "another live Chord process holds it" — the kernel guard lock alone
// reports both as held.
func requireSessionLockOwnership(sessionDir string) error {
	info, err := readLockFile(filepath.Join(sessionDir, sessionLockFile))
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("session is not locked")
		}
		return fmt.Errorf("read session lock: %w", err)
	}
	if info.PID != os.Getpid() {
		return fmt.Errorf("session is owned by another process")
	}
	if hostname, hostErr := os.Hostname(); hostErr == nil && info.Hostname != "" && info.Hostname != hostname {
		return fmt.Errorf("session is owned by a process on %s", info.Hostname)
	}
	return nil
}

// collectAttachmentReferences reports which candidate attachment names some
// durable record mentions. An unreadable record is an error rather than a
// silent "unreferenced": treating a read failure as "no reference" would delete
// live attachments.
func collectAttachmentReferences(sessionDir, imagesDir string, candidates []attachmentCandidate) (map[string]struct{}, error) {
	referenced := make(map[string]struct{}, len(candidates))
	remaining := make([]attachmentCandidate, len(candidates))
	copy(remaining, candidates)
	err := filepath.WalkDir(sessionDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if nonDurableSessionDir(sessionDir, path) {
				return filepath.SkipDir
			}
			if filepath.Dir(path) == imagesDir && filepath.Base(path) != "operations" {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Dir(path) == imagesDir {
			return nil
		}
		// WalkDir reports a symlink as its own type; a linked record is not a
		// record we wrote, and reading it could escape the session directory.
		if !d.Type().IsRegular() {
			return nil
		}
		if _, ok := sweepReferenceExtensions[filepath.Ext(path)]; !ok {
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("read session record %s: %w", filepath.Base(path), err)
		}
		remaining, err = scanAttachmentReferences(file, remaining, referenced)
		closeErr := file.Close()
		if err != nil {
			return fmt.Errorf("read session record %s: %w", filepath.Base(path), err)
		}
		if closeErr != nil {
			return fmt.Errorf("close session record %s: %w", filepath.Base(path), closeErr)
		}
		if len(remaining) == 0 {
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return referenced, nil
}

func scanAttachmentReferences(reader io.Reader, remaining []attachmentCandidate, referenced map[string]struct{}) ([]attachmentCandidate, error) {
	longest := 0
	for _, cand := range remaining {
		longest = max(longest, len(cand.name))
	}
	buffer := make([]byte, (64<<10)+longest)
	overlap := 0
	for len(remaining) > 0 {
		n, err := reader.Read(buffer[overlap:])
		window := buffer[:overlap+n]
		kept := remaining[:0]
		for _, cand := range remaining {
			if bytes.Contains(window, []byte(cand.name)) {
				referenced[cand.name] = struct{}{}
			} else {
				kept = append(kept, cand)
			}
		}
		remaining = kept
		if err != nil {
			if err == io.EOF {
				return remaining, nil
			}
			return remaining, err
		}
		// A filename can straddle reads, including in a JSONL record whose
		// single line is much larger than the buffer.
		overlap = min(longest-1, len(window))
		copy(buffer, window[len(window)-overlap:])
	}
	return remaining, nil
}
