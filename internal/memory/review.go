package memory

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const (
	maxReviewFileBytes  = 32 << 10
	maxReviewIndexBytes = 512 << 10
	maxReviewTotalBytes = 4 << 20
)

// ReviewItem retains the original bytes so applying a preview can detect edits.
type ReviewItem struct {
	Entry   ManagedEntry
	Record  *Record
	Content string
	Path    string
	Error   string
}

type ReviewSuggestion struct {
	Title   string
	Content string
	Path    string
}

// ReviewSnapshot is a disk view, independent of the session's applied summary.
type ReviewSnapshot struct {
	ProjectRoot string
	Index       *MemoryIndex
	Items       []ReviewItem
	Suggestions []ReviewSuggestion
	CanUndo     bool
}

func readReviewFile(path string, limit int64) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	return readReviewContent(f, limit)
}

func readReviewContent(r io.Reader, limit int64) (string, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return "", err
	}
	if int64(len(data)) > limit {
		return "", fmt.Errorf("memory file exceeds %d bytes", limit)
	}
	return string(data), nil
}

func (m *Manager) loadReviewIndex() (*MemoryIndex, error) {
	raw, err := readReviewFile(m.layout.IndexPath, maxReviewIndexBytes)
	if errors.Is(err, os.ErrNotExist) {
		return &MemoryIndex{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read memory index: %w", err)
	}
	return parseMemoryFile(raw)
}

func (m *Manager) reviewItem(root *os.Root, e ManagedEntry) ReviewItem {
	item := ReviewItem{Entry: e, Path: filepath.Join(m.layout.ProjectRoot, filepath.FromSlash(e.Link))}
	expected := filepath.ToSlash(filepath.Join(ProjectLayoutDir, recordFileName(e.ID)))
	if !ValidateRecordID(e.ID) || e.Link != expected {
		item.Error = "Invalid memory record link"
		return item
	}
	f, err := root.Open(filepath.FromSlash(e.Link))
	if err != nil {
		item.Error = err.Error()
		return item
	}
	defer f.Close()
	item.Content, err = readReviewContent(f, maxReviewFileBytes)
	if err == nil {
		item.Record, err = ParseRecord([]byte(item.Content))
	}
	if err == nil && (item.Record.ID != e.ID || RecordID(item.Record.Summary, item.Record.ContentHash()) != e.ID) {
		err = fmt.Errorf("record content does not match its ID")
	}
	if err != nil {
		item.Error = err.Error()
		item.Record = nil
	}
	return item
}

// Review loads bounded content without changing files or making model calls.
func (m *Manager) Review(ctx context.Context) (*ReviewSnapshot, error) {
	lock, err := m.layout.AcquireLock(2 * time.Second)
	if err != nil {
		return nil, err
	}
	defer lock.Release()
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if err = m.recoverManualCommit(); err != nil {
		return nil, err
	}
	idx, err := m.loadReviewIndex()
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(m.layout.ProjectRoot)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	snap := &ReviewSnapshot{ProjectRoot: m.layout.ProjectRoot, Index: idx}
	total := len(idx.Raw)
	seen := map[string]bool{}
	for _, e := range idx.Managed {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		if seen[e.ID] {
			return nil, fmt.Errorf("duplicate memory ID %s", e.ID)
		}
		seen[e.ID] = true
		item := m.reviewItem(root, e)
		total += len(item.Content)
		if total > maxReviewTotalBytes {
			return nil, fmt.Errorf("memory review exceeds %d bytes", maxReviewTotalBytes)
		}
		snap.Items = append(snap.Items, item)
	}
	entries, err := os.ReadDir(m.layout.PromotionsDir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read memory suggestions: %w", err)
	}
	for _, e := range entries {
		if !e.Type().IsRegular() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		relative := filepath.Join(filepath.FromSlash(ProjectPromotionsDir), e.Name())
		f, openErr := root.Open(relative)
		if openErr != nil {
			return nil, openErr
		}
		content, readErr := readReviewContent(f, maxReviewFileBytes)
		_ = f.Close()
		if readErr != nil {
			return nil, readErr
		}
		total += len(content)
		if total > maxReviewTotalBytes {
			return nil, fmt.Errorf("memory review exceeds %d bytes", maxReviewTotalBytes)
		}
		title, _, _ := strings.Cut(content, "\n")
		title = strings.TrimPrefix(title, "# Pending promotion: ")
		snap.Suggestions = append(snap.Suggestions, ReviewSuggestion{Title: title, Content: content, Path: filepath.Join(m.layout.ProjectRoot, relative)})
	}
	_, err = os.Stat(m.manualUndoPath())
	snap.CanUndo = err == nil
	return snap, nil
}

// Select returns a view containing exactly the requested active IDs.
func (s *ReviewSnapshot) Select(ids []string) (*ReviewSnapshot, error) {
	out := *s
	out.Items = nil
	out.Suggestions = nil
	for _, id := range ids {
		if slices.ContainsFunc(out.Items, func(i ReviewItem) bool { return i.Entry.ID == id }) {
			return nil, fmt.Errorf("duplicate selected memory %s", id)
		}
		found := false
		for _, item := range s.Items {
			if item.Entry.ID == id {
				out.Items = append(out.Items, item)
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("memory %s is no longer active", id)
		}
	}
	return &out, nil
}
