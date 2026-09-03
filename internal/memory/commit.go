package memory

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/privatefs"
)

// Manager is the project-scoped facade over the memory files. It owns the
// project layout, the in-process single-flight state, and the cross-process
// lock acquisition. It is safe for concurrent use.
type Manager struct {
	mu       sync.Mutex
	inflight map[string]*extractionFlight
	layout   *Layout
}

// CommitResult reports what a successful extraction commit changed.
type CommitResult struct {
	Added        []string // record IDs newly written and indexed
	AlreadyKnown []string // record IDs already present (idempotent re-run)
	Superseded   []string // old active IDs removed by a replacing conclusion
	Retired      []string // active IDs removed with no replacement
	Promoted     int      // promotion suggestions written for human review
	Noop         bool     // true when nothing needed to be written
	Warnings     []string // recovered machine-state problems worth logging
	// ActiveEntries is the active index size after this commit, or -1 when
	// the commit could not have changed it (covered fingerprint or empty
	// input), so callers can skip scheduling an index review.
	ActiveEntries int
}

// NewManager creates a Manager for projectRoot. Commit paths create the
// machine state directory lazily via the cross-process lock, so read/refresh
// and authoring surfaces use the same constructor. When locator is nil the
// default path locator is used; startup callers pass the already-resolved
// locator so custom paths.state_dir / paths.sessions_dir settings are honored.
func NewManager(projectRoot string, locator ...*config.PathLocator) (*Manager, error) {
	var loc *config.PathLocator
	if len(locator) > 0 {
		loc = locator[0]
	}
	layout, err := resolveLayout(projectRoot, loc)
	if err != nil {
		return nil, err
	}
	return &Manager{
		inflight: make(map[string]*extractionFlight),
		layout:   layout,
	}, nil
}

// Layout exposes the resolved paths (used by the memory worker and commands).
func (m *Manager) Layout() *Layout { return m.layout }

// LoadIndex parses MEMORY.md. Missing file yields an empty index (nil error).
func (m *Manager) LoadIndex() (*MemoryIndex, error) {
	data, err := os.ReadFile(m.layout.IndexPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &MemoryIndex{}, nil
		}
		return nil, fmt.Errorf("read MEMORY.md: %w", err)
	}
	return parseMemoryFile(string(data))
}

// BoundedSummary renders the session-head injection text from MEMORY.md.
// Returns active=false when there is nothing to inject (no Memory file, or
// empty content).
func (m *Manager) BoundedSummary() (string, bool, error) {
	idx, err := m.LoadIndex()
	if err != nil {
		return "", false, err
	}
	summary, active := BoundedSummary(idx)
	return summary, active, nil
}

// singleFlightKey returns the in-process dedup key for one extraction input.
func singleFlightKey(sessionID, fingerprint string) string {
	return sessionID + "|" + fingerprint
}

// extractionFlight tracks one in-flight commit so identical concurrent
// extractions wait for the first attempt instead of racing on the same files.
// Waiters inherit the first attempt's outcome: a failed or cancelled commit is
// reported as an error to every waiter, never disguised as a successful no-op.
type extractionFlight struct {
	done   chan struct{}
	result *CommitResult
	err    error
}

// CommitExtractionCtx commits one extraction pass in the plan's ordered
// sequence:
//
//  1. in-process single-flight on (sessionID, fingerprint): concurrent
//     identical calls wait for the first attempt and inherit its exact
//     result/error;
//  2. acquire the project cross-process lock;
//  3. re-read the latest MEMORY.md and target record state;
//  4. write immutable records (exclusive create, or content-verify on
//     collision — never overwrite);
//  5. replace only the managed index section atomically, verifying the file
//     was not changed by an external editor since it was read;
//  6. atomically commit the extraction checkpoint (last);
//  7. release the lock.
//
// Any failure before step 6 leaves the checkpoint untouched so a retry with
// the same fingerprint is safe and idempotent. ctx cancellation is checked
// before the lock, after the lock, before each record write, and before the
// checkpoint: a cancelled job never advances the checkpoint, and any record
// already written before cancellation is idempotent on the next retry.
func (m *Manager) CommitExtractionCtx(ctx context.Context, sessionID, fingerprint string, projectedCount int, generation uint64, out *ExtractionOutput) (*CommitResult, error) {
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(fingerprint) == "" {
		return nil, errors.New("commit extraction: session id and fingerprint are required")
	}
	key := singleFlightKey(sessionID, fingerprint)

	m.mu.Lock()
	if flight, ok := m.inflight[key]; ok {
		m.mu.Unlock()
		select {
		case <-flight.done:
			return flight.result, flight.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	flight := &extractionFlight{done: make(chan struct{})}
	m.inflight[key] = flight
	m.mu.Unlock()

	result, err := m.commitExtraction(ctx, sessionID, fingerprint, projectedCount, generation, out)
	// Publish the outcome before closing done so waiting goroutines observe a
	// consistent flight (the channel close happens-after the field writes).
	flight.result, flight.err = result, err
	m.mu.Lock()
	delete(m.inflight, key)
	m.mu.Unlock()
	close(flight.done)
	return result, err
}

func (m *Manager) commitExtraction(ctx context.Context, sessionID, fingerprint string, projectedCount int, generation uint64, out *ExtractionOutput) (*CommitResult, error) {

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if out == nil {
		out = &ExtractionOutput{}
	}
	// Cross-process lock serializes the whole read-merge-write commit,
	// including the checkpoint (a no-op commit must not clobber another
	// process's coverage entry).
	lock, err := m.layout.AcquireLock(30 * time.Second)
	if err != nil {
		return nil, err
	}
	defer lock.Release()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// Checkpoint is only consulted to skip already-covered fingerprints; it is
	// committed last, so a stale read here is harmless.
	existing, loadErr := LoadCheckpoint(m.layout)
	if existing != nil && existing.Covered(sessionID, fingerprint) {
		return &CommitResult{Noop: true, AlreadyKnown: []string{}, ActiveEntries: -1}, nil
	}
	var commitWarnings []string
	if errors.Is(loadErr, ErrCorruptCheckpoint) {
		// SaveCheckpoint rebuilds it below; report the loss so a repeatedly
		// corrupted file does not stay invisible.
		commitWarnings = append(commitWarnings, fmt.Sprintf("discarded unreadable extraction checkpoint: %v", loadErr))
	}

	cp := &ExtractionCheckpoint{}
	cp.SetCovered(sessionID, fingerprint, projectedCount, generation)

	// Nothing to add, retire, or promote is a legal no-op that still advances the
	// checkpoint.
	if out.Empty() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := SaveCheckpoint(m.layout, cp); err != nil {
			return nil, err
		}
		return &CommitResult{Noop: true, Warnings: commitWarnings, ActiveEntries: -1}, nil
	}

	// Re-read under the lock: another process may have committed since we last
	// loaded the index.
	idx, err := m.LoadIndex()
	if err != nil {
		return nil, err
	}

	result := &CommitResult{Warnings: commitWarnings}
	var entries []ManagedEntry
	active, warnings, err := m.activeSnapshotForIndex(idx)
	if err != nil {
		return nil, err
	}
	// This re-read exists to get the locked-in view, not new diagnostics: the
	// caller already logged these warnings from its own pre-commit
	// ActiveSnapshot, so reporting them again would only duplicate. Details
	// that failed to load cannot block a commit either — the index IDs
	// collected below are what gate duplicate and supersede checks.
	_ = warnings
	activeIDs := make(map[string]bool, len(active.Entries))
	for _, entry := range active.Entries {
		activeIDs[entry.ID] = true
	}
	supersededByBatch := make(map[string]bool)
	for _, c := range out.Candidates {
		for _, id := range uniqueStrings(c.Supersedes) {
			if !activeIDs[id] {
				return nil, fmt.Errorf("candidate supersedes inactive record %q", id)
			}
			if supersededByBatch[id] {
				return nil, fmt.Errorf("multiple candidates supersede active record %q", id)
			}
			supersededByBatch[id] = true
		}
	}
	for _, c := range out.Candidates {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		supersedes := uniqueStrings(c.Supersedes)
		if id, ok := matchingActiveConclusion(active.Records, activeIDs, c, supersedes); ok {
			if len(supersedes) > 0 && !containsString(supersedes, id) {
				return nil, fmt.Errorf("candidate supersedes records but duplicates active record %q", id)
			}
			result.AlreadyKnown = append(result.AlreadyKnown, id)
			continue
		}
		rec := recordFromCandidate(sessionID, fingerprint, c)
		hash := rec.ContentHash()
		id := RecordID(rec.Summary, hash)
		rec.ID = id
		if containsString(supersedes, id) {
			return nil, fmt.Errorf("candidate does not materially change superseded record %q", id)
		}

		if err := writeRecordImmutable(m.layout, rec); err != nil {
			return nil, err
		}
		link := filepath.ToSlash(filepath.Join(ProjectLayoutDir, recordFileName(id)))
		if entry, ok := managedEntryFor(idx, id); ok && entry.Summary == rec.Summary {
			result.AlreadyKnown = append(result.AlreadyKnown, id)
		} else {
			result.Added = append(result.Added, id)
		}
		entries = append(entries, ManagedEntry{ID: id, Link: link, Summary: rec.Summary})
		for _, oldID := range supersedes {
			delete(activeIDs, oldID)
			result.Superseded = append(result.Superseded, oldID)
		}
		activeIDs[id] = true
		active.Records = append(active.Records, rec)
	}

	// Retirements run after the candidate pass so a record already replaced by a
	// superseding conclusion is simply skipped. Unlike supersedes — where a
	// dangling target would leave a duplicate conclusion indexed — a retirement
	// is a removal with no replacement, so an ineligible or already-gone target
	// is a warning rather than a failed batch: failing here would also discard
	// the valid additions, and the retry would hit the same bad item.
	recordsByID := make(map[string]*Record, len(active.Records))
	for _, rec := range active.Records {
		if rec != nil {
			recordsByID[rec.ID] = rec
		}
	}
	var retired []string
	for _, r := range out.Retire {
		if !activeIDs[r.ID] {
			result.Warnings = append(result.Warnings, fmt.Sprintf("skipped retiring %q: no longer active", r.ID))
			continue
		}
		rec := recordsByID[r.ID]
		if rec == nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("skipped retiring %q: record details unavailable", r.ID))
			continue
		}
		// What the user stated is not the model's to forget. A preference that has
		// gone stale is for the user to drop, or for a promotion to move into
		// project instructions — not for an extraction pass to delete silently.
		if rec.Confidence == ConfidenceUserStated {
			result.Warnings = append(result.Warnings, fmt.Sprintf("refused to retire user-stated record %q", r.ID))
			continue
		}
		delete(activeIDs, r.ID)
		retired = append(retired, r.ID)
	}

	// Promotions are written before the index so a failure here can never drop a
	// conclusion out of the index without its content landing somewhere: the
	// pending files are where it survives until a human reviews it.
	if len(out.Promotions) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := writePromotionFiles(m.layout, sessionID, out.Promotions); err != nil {
			return nil, fmt.Errorf("write promotions: %w", err)
		}
		result.Promoted = len(out.Promotions)
		for _, p := range out.Promotions {
			if p.SourceID == "" || !activeIDs[p.SourceID] {
				continue
			}
			// What the user stated is not the model's to unindex, even into a
			// pending review file: until a human acts on the suggestion, the
			// indexed entry is what every future session sees. The suggestion
			// itself is still written above; refusing here only keeps the
			// source injected in the meantime.
			if rec := recordsByID[p.SourceID]; rec != nil && rec.Confidence == ConfidenceUserStated {
				result.Warnings = append(result.Warnings, fmt.Sprintf("kept user-stated record %q indexed despite promotion", p.SourceID))
				continue
			}
			delete(activeIDs, p.SourceID)
			retired = append(retired, p.SourceID)
		}
	}
	result.Retired = retired
	result.ActiveEntries = len(activeIDs)

	removeIDs := append(append([]string(nil), result.Superseded...), retired...)
	if len(entries) > 0 || len(removeIDs) > 0 {
		merged, err := BuildManagedIndexReplacing(idx, entries, removeIDs)
		if err != nil {
			return nil, fmt.Errorf("merge managed index: %w", err)
		}
		if _, err := writeMemoryFileIfChanged(m.layout, idx, merged, entries, removeIDs); err != nil {
			return nil, err
		}
	} else {
		result.Noop = true
	}

	// Checkpoint last: any failure above leaves it untouched for a safe retry.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := SaveCheckpoint(m.layout, cp); err != nil {
		return nil, fmt.Errorf("commit checkpoint: %w", err)
	}
	return result, nil
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}

func containsString(values []string, want string) bool {
	return slices.Contains(values, want)
}

func matchingActiveConclusion(records []*Record, activeIDs map[string]bool, candidate Candidate, excluded []string) (string, bool) {
	excludedIDs := make(map[string]bool, len(excluded))
	for _, id := range excluded {
		excludedIDs[id] = true
	}
	statement := strings.Join(strings.Fields(strings.ToLower(candidate.Statement)), " ")
	for _, record := range records {
		if record == nil || !activeIDs[record.ID] || excludedIDs[record.ID] || record.Type != candidate.Type {
			continue
		}
		if strings.Join(strings.Fields(strings.ToLower(record.Statement)), " ") == statement {
			return record.ID, true
		}
	}
	return "", false
}

// managedEntryFor returns the existing managed entry for id.
func managedEntryFor(idx *MemoryIndex, id string) (ManagedEntry, bool) {
	for _, e := range idx.Managed {
		if e.ID == id {
			return e, true
		}
	}
	return ManagedEntry{}, false
}

// recordFromCandidate builds a Record from an extraction candidate that
// already passed ParseExtractionOutput's structural and droppable validation.
func recordFromCandidate(sessionID, fingerprint string, c Candidate) *Record {
	rec := &Record{
		Type:              c.Type,
		Created:           time.Now().UTC(),
		OriginSessionID:   sessionID,
		SourceFingerprint: fingerprint,
		Confidence:        c.Confidence,
		Outcome:           c.Outcome,
		Supersedes:        append([]string(nil), c.Supersedes...),
		Summary:           c.Summary,
		Statement:         c.Statement,
		Rationale:         c.Rationale,
		Application:       c.Application,
		ProjectPaths:      append([]string(nil), c.ProjectPaths...),
	}
	if len(rec.Supersedes) == 0 {
		rec.Supersedes = nil
	}
	if len(rec.ProjectPaths) == 0 {
		rec.ProjectPaths = nil
	}
	return rec
}

// writeRecordImmutable writes a record file with exclusive-create semantics:
// same ID + identical canonical content is an idempotent success; same ID with
// different content is a conflict error; the file is never overwritten.
func writeRecordImmutable(l *Layout, rec *Record) error {
	if err := validateRecordBounds(rec); err != nil {
		return err
	}
	if err := os.MkdirAll(l.RecordsDir, 0o755); err != nil {
		return fmt.Errorf("create records dir: %w", err)
	}
	path := recordPath(l.RecordsDir, rec.ID)
	data, err := MarshalRecord(rec)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err == nil {
		if _, werr := f.Write(data); werr != nil {
			_ = f.Close()
			_ = os.Remove(path)
			return fmt.Errorf("write record %s: %w", rec.ID, werr)
		}
		// Records are immutable and the index/checkpoint are committed after
		// them; sync the file so a crash cannot leave an empty or partial
		// record behind a completed index write.
		if serr := f.Sync(); serr != nil {
			_ = f.Close()
			_ = os.Remove(path)
			return fmt.Errorf("sync record %s: %w", rec.ID, serr)
		}
		if cerr := f.Close(); cerr != nil {
			_ = os.Remove(path)
			return fmt.Errorf("close record %s: %w", rec.ID, cerr)
		}
		// The directory entry must be durable before the index/checkpoint that
		// reference it, or a crash could leave a checkpointed index pointing at
		// a record that never materialized.
		if serr := privatefs.SyncDir(l.RecordsDir); serr != nil {
			return fmt.Errorf("sync records dir: %w", serr)
		}
		return nil
	}
	if !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("create record %s: %w", rec.ID, err)
	}
	// Existing file: compare canonical content.
	existing, err := loadRecord(path)
	if err != nil {
		return fmt.Errorf("record %s exists but cannot be parsed: %w", rec.ID, err)
	}
	existing.ID = rec.ID
	if existing.ContentHash() != rec.ContentHash() {
		return fmt.Errorf("record id %s already exists with different content", rec.ID)
	}
	return nil
}

// writeMemoryFileIfChanged writes the merged MEMORY.md only when the managed
// section actually changed, and never clobbers content an external editor wrote
// after our read. It re-reads under the lock, re-merges against the freshest
// content, and retries a bounded number of times on conflict. A final
// read-after-write comparison drops the write if the file changed again right
// before, rather than overwriting a user edit.
//
// Returns (changed bool, err). changed=false means the file was already in the
// desired state (either identical, or no new entries to add).
func writeMemoryFileIfChanged(l *Layout, idx *MemoryIndex, merged string, entries []ManagedEntry, removeIDs []string) (bool, error) {
	if !memoryFileDiffers(idx.Raw, merged) {
		return false, nil
	}
	const maxAttempts = 3
	for attempt := range maxAttempts {
		// Re-read the file we merged from. If it no longer matches the index we
		// derived `merged` from, refresh and re-merge before writing.
		current, err := os.ReadFile(l.IndexPath)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return false, fmt.Errorf("re-read MEMORY.md: %w", err)
		}
		if !bytes.Equal(current, []byte(idx.Raw)) {
			newIdx, perr := parseMemoryFile(string(current))
			if perr != nil {
				return false, fmt.Errorf("MEMORY.md changed concurrently and cannot be parsed: %w", perr)
			}
			remerged, mErr := BuildManagedIndexReplacing(newIdx, entries, removeIDs)
			if mErr != nil {
				return false, mErr
			}
			idx = newIdx
			merged = remerged
			if !memoryFileDiffers(idx.Raw, merged) {
				return false, nil
			}
		}
		if err := writeMemoryFileAtomic(l, merged); err != nil {
			if attempt == maxAttempts-1 {
				return false, err
			}
			continue
		}
		// Verify the rename stuck: an external editor that writes between our
		// read and rename can still lose its content to the rename (the editor
		// does not take Chord's lock), so compare the file right after the
		// write. If it no longer holds our merge, an external writer replaced
		// it; re-merge from their content, or fail on the last attempt so the
		// checkpoint is not advanced for an index that lost our entries.
		after, rerr := os.ReadFile(l.IndexPath)
		if rerr != nil {
			return false, fmt.Errorf("re-read after write: %w", rerr)
		}
		if !bytes.Equal(after, []byte(merged)) {
			if attempt < maxAttempts-1 {
				continue
			}
			return false, fmt.Errorf("MEMORY.md changed concurrently during commit")
		}
		return true, nil
	}
	return false, nil
}

// memoryFileDiffers reports whether the raw file differs from the desired
// managed content.
func memoryFileDiffers(raw, desired string) bool {
	return raw != desired
}

// writeMemoryFileAtomic writes MEMORY.md via temp file + fsync + rename +
// directory sync (mirrors persistJSONAtomically in internal/agent).
//
// MEMORY.md is a normal project file, so privatefs helpers are deliberately
// avoided here: they restrict the whole root directory tree (which would
// chmod the project root) and force private file modes. An existing MEMORY.md
// keeps its current permissions; a new one uses the standard project file
// mode.
func writeMemoryFileAtomic(l *Layout, content string) error {
	dir := filepath.Dir(l.IndexPath)
	mode := os.FileMode(0o644)
	if info, err := os.Stat(l.IndexPath); err == nil {
		mode = info.Mode().Perm()
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat MEMORY.md: %w", err)
	}
	tmpPath := filepath.Join(dir, fmt.Sprintf(".MEMORY.md.%d.tmp", time.Now().UnixNano()))
	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("create MEMORY.md tmp: %w", err)
	}
	if _, err := f.Write([]byte(content)); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("write MEMORY.md tmp: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("sync MEMORY.md tmp: %w", err)
	}
	if err := f.Chmod(mode); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("chmod MEMORY.md tmp: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close MEMORY.md tmp: %w", err)
	}
	if err := os.Rename(tmpPath, l.IndexPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("install MEMORY.md: %w", err)
	}
	return privatefs.SyncDir(dir)
}

// writePromotionFiles writes each promotion suggestion as one immutable,
// content-addressed file under .chord/memory/promotions/. Exclusive-create
// semantics make retries after a failed or cancelled commit idempotent: the
// same suggestion always maps to the same file name, so a re-run can never
// duplicate an entry. An existing file is left untouched, including when the
// user edited it after creation.
//
// Chord never edits AGENTS.md or project docs itself: those are human-owned,
// and a wrong automatic edit to a file that steers every future session is far
// more costly than a suggestion the user ignores.
func writePromotionFiles(l *Layout, sessionID string, promotions []Promotion) error {
	if err := os.MkdirAll(l.PromotionsDir, 0o755); err != nil {
		return fmt.Errorf("create promotions dir: %w", err)
	}
	created := false
	for _, p := range promotions {
		path := filepath.Join(l.PromotionsDir, promotionFileName(p))
		if _, err := os.Stat(path); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("stat promotion: %w", err)
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err != nil {
			if errors.Is(err, os.ErrExist) {
				continue
			}
			return fmt.Errorf("create promotion file: %w", err)
		}
		if _, err := f.Write(promotionFileBody(sessionID, p)); err != nil {
			_ = f.Close()
			_ = os.Remove(path)
			return fmt.Errorf("write promotion file: %w", err)
		}
		if err := f.Sync(); err != nil {
			_ = f.Close()
			_ = os.Remove(path)
			return fmt.Errorf("sync promotion file: %w", err)
		}
		if err := f.Close(); err != nil {
			_ = os.Remove(path)
			return fmt.Errorf("close promotion file: %w", err)
		}
		created = true
	}
	if created {
		if err := privatefs.SyncDir(l.PromotionsDir); err != nil {
			return fmt.Errorf("sync promotions dir: %w", err)
		}
	}
	return nil
}

// promotionFileName derives the stable, human-readable file name for a
// promotion suggestion: a slug of the summary plus the content hash that makes
// identical suggestions collide onto the same idempotent file.
func promotionFileName(p Promotion) string {
	var b strings.Builder
	fmt.Fprintf(&b, "target=%s\n", p.Target)
	b.WriteString("summary=")
	b.WriteString(p.Summary)
	b.WriteString("\nlocation=")
	b.WriteString(p.SuggestedLocation)
	b.WriteString("\nsource=")
	b.WriteString(p.SourceID)
	b.WriteString("\nreason=")
	b.WriteString(p.Reason)
	b.WriteString("\ndraft=")
	b.WriteString(strings.TrimSpace(p.DraftText))
	b.WriteString("\n")
	sum := sha256.Sum256([]byte(b.String()))
	return RecordID(p.Summary, hex.EncodeToString(sum[:])[:hashHexLen]) + ".md"
}

// promotionFileBody renders one pending promotion suggestion as standalone
// Markdown a user can review without any surrounding index.
func promotionFileBody(sessionID string, p Promotion) []byte {
	var b strings.Builder
	b.WriteString("# Pending promotion: ")
	b.WriteString(p.Summary)
	b.WriteString("\n\n")
	b.WriteString("Chord judged this conclusion to belong in project instructions or project docs\n")
	b.WriteString("rather than in per-turn memory. Chord only writes suggestions here; move what\n")
	b.WriteString("you agree with into the authoritative file yourself, then delete this one.\n\n")
	b.WriteString("- Target: ")
	b.WriteString(string(p.Target))
	if p.SuggestedLocation != "" {
		b.WriteString("\n- Suggested location: ")
		b.WriteString(p.SuggestedLocation)
	}
	if p.SourceID != "" {
		b.WriteString("\n- Source record: ")
		b.WriteString(p.SourceID)
	}
	b.WriteString("\n- Session: ")
	b.WriteString(sessionID)
	b.WriteString("\n- Reason: ")
	b.WriteString(p.Reason)
	b.WriteString("\n\n")
	b.WriteString(p.DraftText)
	b.WriteString("\n")
	return []byte(b.String())
}

// PendingPromotionSummaries returns the one-line titles of pending promotion
// suggestions, newest first, capped at max. Extraction passes read the pending
// queue only to avoid suggesting the same conclusion twice across sessions;
// Chord never consumes the queue itself. A missing directory is an empty view,
// and a file deleted between listing and reading (a human reviewing the queue)
// is skipped rather than an error.
func (m *Manager) PendingPromotionSummaries(max int) ([]string, error) {
	if max <= 0 {
		return nil, nil
	}
	entries, err := os.ReadDir(m.layout.PromotionsDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read pending promotions: %w", err)
	}
	type pendingItem struct {
		title string
		mod   time.Time
		name  string
	}
	const promotionTitlePrefix = "# Pending promotion: "
	var items []pendingItem
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join(m.layout.PromotionsDir, e.Name()))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("read pending promotion %s: %w", e.Name(), err)
		}
		first, _, _ := strings.Cut(string(data), "\n")
		title, ok := strings.CutPrefix(first, promotionTitlePrefix)
		if !ok {
			continue
		}
		title = strings.Join(strings.Fields(title), " ")
		if r := []rune(title); len(r) > 160 {
			title = string(r[:160])
		}
		items = append(items, pendingItem{title: title, mod: info.ModTime(), name: e.Name()})
	}
	slices.SortFunc(items, func(a, b pendingItem) int {
		if cmp := b.mod.Compare(a.mod); cmp != 0 {
			return cmp
		}
		return strings.Compare(a.name, b.name)
	})
	titles := make([]string, 0, min(len(items), max))
	for i, it := range items {
		if i >= max {
			break
		}
		titles = append(titles, it.title)
	}
	return titles, nil
}
