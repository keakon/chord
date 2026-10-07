package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/keakon/chord/internal/privatefs"
)

var ErrReviewConflict = errors.New("memory changed; refresh and review again")

// manualUndo retains one bounded operation and survives process restarts.
type manualUndo struct {
	Before  []ManagedEntry `json:"before"`
	After   []ManagedEntry `json:"after"`
	Changed []string       `json:"changed"`
	Undo    bool           `json:"undo,omitempty"`
}

func (m *Manager) manualUndoPath() string {
	return filepath.Join(m.layout.StateDir, "manual-undo.json")
}
func (m *Manager) manualPendingPath() string {
	return filepath.Join(m.layout.StateDir, "manual-pending.json")
}

func (m *Manager) saveManualPending(op *manualUndo) error {
	data, err := json.Marshal(op)
	if err != nil {
		return err
	}
	path := m.manualPendingPath()
	tmp := path + ".tmp"
	if err = privatefs.WriteFileSynced(m.layout.StateDir, tmp, data); err != nil {
		return fmt.Errorf("write memory recovery state: %w", err)
	}
	if err = os.Rename(tmp, path); err != nil {
		return err
	}
	return privatefs.SyncDir(m.layout.StateDir)
}

func loadManualUndo(path string) (*manualUndo, error) {
	raw, err := readReviewFile(path, maxReviewTotalBytes)
	if err != nil {
		return nil, err
	}
	var op manualUndo
	if err = json.Unmarshal([]byte(raw), &op); err != nil {
		return nil, fmt.Errorf("read memory undo: %w", err)
	}
	if len(op.Changed) == 0 {
		return nil, fmt.Errorf("invalid empty memory undo")
	}
	for _, id := range op.Changed {
		if !ValidateRecordID(id) {
			return nil, fmt.Errorf("invalid memory undo ID")
		}
	}
	return &op, nil
}

func changedEntriesEqual(current, expected []ManagedEntry, ids []string) bool {
	for _, id := range ids {
		var a, b *ManagedEntry
		for _, e := range current {
			if e.ID == id {
				v := e
				a = &v
			}
		}
		for _, e := range expected {
			if e.ID == id {
				v := e
				b = &v
			}
		}
		if (a == nil) != (b == nil) || a != nil && *a != *b {
			return false
		}
	}
	return true
}

// Recovery distinguishes a prepared operation from its committed index state.
func (m *Manager) recoverManualCommit() error {
	op, err := loadManualUndo(m.manualPendingPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	idx, err := m.loadReviewIndex()
	if err != nil {
		return err
	}
	if changedEntriesEqual(idx.Managed, op.After, op.Changed) {
		if op.Undo {
			if err = os.Remove(m.manualUndoPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			err = os.Remove(m.manualPendingPath())
		} else {
			err = os.Rename(m.manualPendingPath(), m.manualUndoPath())
		}
	} else if changedEntriesEqual(idx.Managed, op.Before, op.Changed) {
		err = os.Remove(m.manualPendingPath())
	} else {
		return fmt.Errorf("%w: interrupted manual commit", ErrReviewConflict)
	}
	if err != nil {
		return err
	}
	return privatefs.SyncDir(m.layout.StateDir)
}

// ApplyManual commits only an explicitly accepted preview, under the project lock.
func (m *Manager) ApplyManual(ctx context.Context, d *ManualDraft) error {
	if err := d.validate(); err != nil {
		return err
	}
	if d.Empty() {
		return nil
	}
	if d.Base.ProjectRoot != m.layout.ProjectRoot {
		return ErrReviewConflict
	}
	lock, err := m.layout.AcquireLock(2 * time.Second)
	if err != nil {
		return err
	}
	defer lock.Release()
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = m.recoverManualCommit(); err != nil {
		return err
	}
	idx, err := m.loadReviewIndex()
	if err != nil {
		return err
	}
	if d.All && !slices.Equal(idx.Managed, d.Base.Index.Managed) {
		return ErrReviewConflict
	}
	root, err := os.OpenRoot(m.layout.ProjectRoot)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, base := range d.Base.Items {
		current, ok := findReviewEntry(idx.Managed, base.Entry.ID)
		if !ok || current != base.Entry {
			return ErrReviewConflict
		}
		item := m.reviewItem(root, current)
		if item.Content != base.Content || item.Error != base.Error {
			return ErrReviewConflict
		}
	}
	entries := make([]ManagedEntry, 0, len(d.Records))
	removed := d.changedIDs()
	for _, r := range d.Records {
		if slices.ContainsFunc(idx.Managed, func(e ManagedEntry) bool { return e.ID == r.ID }) {
			return fmt.Errorf("replacement already active; refresh preview")
		}
		created, writeErr := writeRecordImmutable(m.layout, r)
		if writeErr != nil {
			return writeErr
		}
		if !created {
			existing, loadErr := loadRecord(recordPath(m.layout.RecordsDir, r.ID))
			if loadErr != nil {
				return loadErr
			}
			if !slices.Equal(existing.Supersedes, r.Supersedes) || existing.SourceFingerprint != r.SourceFingerprint {
				return fmt.Errorf("replacement already exists with different sources; regenerate the preview")
			}
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		entries = append(entries, ManagedEntry{ID: r.ID, Link: filepath.ToSlash(filepath.Join(ProjectLayoutDir, recordFileName(r.ID))), Summary: r.Summary})
	}
	merged, err := BuildManagedIndexReplacing(idx, entries, removed)
	if err != nil {
		return err
	}
	after, err := parseMemoryFile(merged)
	if err != nil {
		return err
	}
	changed := append([]string(nil), removed...)
	for _, e := range entries {
		changed = append(changed, e.ID)
	}
	op := &manualUndo{Before: idx.Managed, After: after.Managed, Changed: changed}
	return m.commitManualIndex(ctx, idx, merged, op)
}

func findReviewEntry(entries []ManagedEntry, id string) (ManagedEntry, bool) {
	for _, e := range entries {
		if e.ID == id {
			return e, true
		}
	}
	return ManagedEntry{}, false
}

func (m *Manager) commitManualIndex(ctx context.Context, idx *MemoryIndex, merged string, op *manualUndo) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := m.saveManualPending(op); err != nil {
		return err
	}
	// External editors do not take our lock; do not rebase an accepted preview silently.
	fresh, err := m.loadReviewIndex()
	if err != nil {
		return err
	}
	if fresh.Raw != idx.Raw {
		_ = os.Remove(m.manualPendingPath())
		return ErrReviewConflict
	}
	if err = writeMemoryFileAtomic(m.layout, merged); err != nil {
		return fmt.Errorf("apply memory index (recovery state retained): %w", err)
	}
	return m.recoverManualCommit()
}

// UndoManual reverses just the latest manual change, keeping unrelated new entries.
func (m *Manager) UndoManual(ctx context.Context) error {
	lock, err := m.layout.AcquireLock(2 * time.Second)
	if err != nil {
		return err
	}
	defer lock.Release()
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = m.recoverManualCommit(); err != nil {
		return err
	}
	op, err := loadManualUndo(m.manualUndoPath())
	if err != nil {
		return err
	}
	idx, err := m.loadReviewIndex()
	if err != nil {
		return err
	}
	if !changedEntriesEqual(idx.Managed, op.After, op.Changed) {
		return ErrReviewConflict
	}
	root, err := os.OpenRoot(m.layout.ProjectRoot)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, e := range op.Before {
		if slices.Contains(op.Changed, e.ID) && m.reviewItem(root, e).Error != "" {
			return fmt.Errorf("%w: original record %s is unreadable", ErrReviewConflict, e.ID)
		}
	}
	kept := make([]ManagedEntry, 0, len(idx.Managed))
	for _, e := range idx.Managed {
		if !slices.Contains(op.Changed, e.ID) {
			kept = append(kept, e)
		}
	}
	for pos, e := range op.Before {
		if !slices.Contains(op.Changed, e.ID) {
			continue
		}
		insert := len(kept)
		for _, next := range op.Before[pos+1:] {
			if p := slices.IndexFunc(kept, func(k ManagedEntry) bool { return k.ID == next.ID }); p >= 0 {
				insert = p
				break
			}
		}
		kept = slices.Insert(kept, insert, e)
	}
	restored := *idx
	restored.Managed = kept
	reverse := &manualUndo{Before: idx.Managed, After: kept, Changed: op.Changed, Undo: true}
	return m.commitManualIndex(ctx, idx, renderManagedMarkdown(&restored), reverse)
}
