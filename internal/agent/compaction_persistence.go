package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/privatefs"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/session"
	"github.com/keakon/chord/internal/tools"
)

// compactionArchiveMeta is the immutable metadata an archive export needs
// from the MainAgent. Compaction workers run on their own goroutines while
// the event loop may switch sessions, so the caller captures these values
// once at the barrier/event loop and exportCompactionHistory reads only this
// bundle — it never touches live MainAgent fields.
type compactionArchiveMeta struct {
	sessionDir          string
	modelName           string
	projectRoot         string
	persistentSessionID string
	instanceID          string
}

// captureCompactionArchiveMeta snapshots the archive metadata on the event
// loop. Call before handing a compaction draft off to a worker goroutine.
func (a *MainAgent) captureCompactionArchiveMeta() compactionArchiveMeta {
	return compactionArchiveMeta{
		sessionDir:          a.SessionDir(),
		modelName:           a.ModelName(),
		projectRoot:         a.projectRoot,
		persistentSessionID: a.exportPersistentSessionID(),
		instanceID:          a.instanceID,
	}
}

func (a *MainAgent) exportCompactionHistory(messages []message.Message, index int, topics []string, meta compactionArchiveMeta) (absPath string, sourceRefs []checkpointSourceRef, sourceFingerprint string, err error) {
	// The archive, its permission root, and its status file all belong to the
	// session captured at the barrier; a session switch cannot change where
	// this draft writes.
	sessionDir := meta.sessionDir
	absPath = filepath.Join(sessionDir, fmt.Sprintf("history-%d.md", index))
	metadata := map[string]string{
		session.MetadataKeyModel:       meta.modelName,
		session.MetadataKeyProjectPath: meta.projectRoot,
		session.MetadataKeySessionID:   meta.persistentSessionID,
		session.MetadataKeyInstanceID:  meta.instanceID,
	}
	exported, err := session.Export(messages, nil, metadata)
	if err != nil {
		return "", nil, "", err
	}
	// history-N.md is the model's archive: prepend a message-segment index so
	// the checkpoint wrapper can tell the model to read the index first and
	// then only the line ranges it needs (whole-file reads truncate and
	// re-inflate the context).
	md := buildCompactionArchiveIndexedMarkdown(session.ExportToMarkdown(exported))
	if err := privatefs.WriteFile(sessionDir, absPath, []byte(md)); err != nil {
		return "", nil, "", err
	}
	generation := fmt.Sprintf("compaction-%d", index)
	sourceRefs, err = buildCheckpointSourceRefs(meta.persistentSessionID, generation, filepath.Base(absPath), messages)
	if err != nil {
		return "", nil, "", err
	}
	sourceFingerprint = checkpointSourceFingerprint(sourceRefs)
	if err := writeCompactionHistoryMeta(sessionDir, compactionHistoryMetaPath(absPath), compactionHistoryMeta{
		Version:           1,
		HistoryFile:       filepath.Base(absPath),
		Status:            compactionHistoryPending,
		ExportedAt:        time.Now(),
		SourceGeneration:  generation,
		SourceRefs:        sourceRefs,
		SourceFingerprint: sourceFingerprint,
		Topics:            append([]string(nil), topics...),
	}); err != nil {
		return "", nil, "", err
	}
	return absPath, sourceRefs, sourceFingerprint, nil
}

func compactionHistoryMetaPath(absHistoryPath string) string {
	base := strings.TrimSuffix(absHistoryPath, filepath.Ext(absHistoryPath))
	return base + ".status.json"
}

func readCompactionHistoryMeta(path string) (compactionHistoryMeta, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return compactionHistoryMeta{}, err
	}
	var meta compactionHistoryMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return compactionHistoryMeta{}, err
	}
	return meta, nil
}

func writeCompactionHistoryMeta(sessionDir, path string, meta compactionHistoryMeta) error {
	if meta.Version == 0 {
		meta.Version = 1
	}
	if meta.HistoryFile == "" {
		meta.HistoryFile = filepath.Base(strings.TrimSuffix(path, ".status.json") + ".md")
	}
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	return privatefs.WriteFile(sessionDir, path, append(data, '\n'))
}

// getAbsHistoryPathFromDraft extracts absHistoryPath from a draft if available
func getAbsHistoryPathFromDraft(draft *compactionDraft) string {
	if draft == nil || draft.AbsHistoryPath == "" {
		return ""
	}
	return draft.AbsHistoryPath
}

// cleanupOrphanCompactionFiles removes history files and status.json for a
// compaction that was cancelled or failed before apply. This is called when
// the user cancels compaction (ESC) or when startup detects orphan pending files.
func cleanupOrphanCompactionFiles(absHistoryPath string) {
	if absHistoryPath == "" {
		return
	}
	// Remove the history .md file
	if err := os.Remove(absHistoryPath); err != nil && !os.IsNotExist(err) {
		log.Warnf("failed to remove orphan history file path=%v error=%v", absHistoryPath, err)
	}
	// Remove the .status.json file
	metaPath := compactionHistoryMetaPath(absHistoryPath)
	if err := os.Remove(metaPath); err != nil && !os.IsNotExist(err) {
		log.Warnf("failed to remove orphan history meta file path=%v error=%v", metaPath, err)
	}
	log.Debugf("cleaned up orphan compaction files history_path=%v", absHistoryPath)
}

// cleanupStalePendingCompactions scans sessionDir for history-N.status.json files
// with status "pending_apply" that are older than the threshold, and removes them.
// This handles the case where a compaction was cancelled but the cleanup didn't run
// (e.g., process exit before the cancel event was processed).
func cleanupStalePendingCompactions(sessionDir string, maxAge time.Duration) {
	if sessionDir == "" {
		return
	}
	entries, err := os.ReadDir(sessionDir)
	if err != nil {
		return
	}
	now := time.Now()
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".status.json") {
			continue
		}
		metaPath := filepath.Join(sessionDir, name)
		meta, err := readCompactionHistoryMeta(metaPath)
		if err != nil {
			continue
		}
		if meta.Status != compactionHistoryPending {
			continue
		}
		age := now.Sub(meta.ExportedAt)
		if age < maxAge {
			continue
		}
		// Check if the corresponding main.pre-compress file exists
		// If it does, the compaction might still be in progress or the apply failed
		// We only clean up if there's no backup file (meaning apply never started)
		indexStr := strings.TrimSuffix(strings.TrimPrefix(name, "history-"), ".status.json")
		backupPath := filepath.Join(sessionDir, fmt.Sprintf("main.pre-compress-%s.jsonl", indexStr))
		if _, err := os.Stat(backupPath); err == nil {
			// Backup exists - this might be a failed apply, leave it for manual inspection
			log.Debugf("skipping orphan cleanup: backup file exists meta_path=%v backup_path=%v", metaPath, backupPath)
			continue
		}
		// Safe to clean up
		historyPath := filepath.Join(sessionDir, meta.HistoryFile)
		cleanupOrphanCompactionFiles(historyPath)
	}
	// Transaction manifests have no terminal-file lifecycle of their own: a
	// committed/aborted manifest is only read by the restore reconciliation
	// and the model-driven crash-window fix (both of which run before this
	// sweep on the restore path), and stale prepared ones are re-judged by
	// that same reconcile. Sweep them once they are old enough. A prepared
	// manifest whose archive still exists is left alone: its worker may
	// still be running (a draft can legitimately outlive the sweep window),
	// and only a manifest whose archive is gone is provably dead.
	txnFiles, err := listCompactionTransactionManifests(sessionDir)
	if err == nil {
		for _, file := range txnFiles {
			if file.Err != nil {
				continue
			}
			manifest := file.Manifest
			if now.Sub(manifest.UpdatedAt) < maxAge {
				continue
			}
			if manifest.Status == compactionTransactionPrepared {
				// A prepared manifest whose archive is still present may belong to
				// an in-flight worker (the archive is only removed once its draft
				// is cancelled/failed or the transaction commits), so it is left
				// alone; only a manifest whose archive is gone is provably dead.
				// The manifest's own authoritative ArchivePath is used instead of
				// re-deriving the history index from the transaction ID: every
				// production writer records the absolute archive path here, and
				// the index spelling inside the ID is not guaranteed to match the
				// archive file name. An empty ArchivePath means the archive is
				// gone.
				if strings.TrimSpace(manifest.ArchivePath) != "" {
					if _, err := os.Stat(manifest.ArchivePath); err == nil {
						continue
					}
				}
			}
			removeCompactionTransactionManifest(sessionDir, file.TransactionID)
		}
	}
}

func listHistoryReferences(sessionDir string) ([]string, error) {
	entries, err := os.ReadDir(sessionDir)
	if err != nil {
		return nil, err
	}
	type historyEntry struct {
		n   int
		abs string
	}
	var histories []historyEntry
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, "history-") || !strings.HasSuffix(name, ".md") {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(name, "history-"), ".md"))
		if err != nil {
			continue
		}
		histories = append(histories, historyEntry{n: n, abs: filepath.Join(sessionDir, name)})
	}
	sort.Slice(histories, func(i, j int) bool { return histories[i].n < histories[j].n })
	refs := make([]string, 0, len(histories))
	for _, item := range histories {
		refs = append(refs, item.abs)
	}
	return refs, nil
}

// compactionIndexAllocator holds the monotonic history index for one session
// directory.
type compactionIndexAllocator struct {
	mu     sync.Mutex
	next   int
	seeded bool
}

// nextCompactionIndex scans the session dir for the highest history /
// pre-compress index on disk.
func nextCompactionIndex(sessionDir string) (int, error) {
	entries, err := os.ReadDir(sessionDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 1, nil
		}
		return 0, err
	}
	maxIndex := 0
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, "history-") && strings.HasSuffix(name, ".md") {
			if n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(name, "history-"), ".md")); err == nil && n > maxIndex {
				maxIndex = n
			}
		}
		if strings.HasPrefix(name, "main.pre-compress-") && strings.HasSuffix(name, ".jsonl") {
			if n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(name, "main.pre-compress-"), ".jsonl")); err == nil && n > maxIndex {
				maxIndex = n
			}
		}
	}
	return maxIndex + 1, nil
}

// nextCompactionIndexForAgent allocates the next history index for sessionDir
// through an in-memory monotonic allocator seeded from that directory's
// on-disk maximum. Allocators are kept per directory because a worker may
// outlive a watchdog failure and a later session switch; the late worker must
// continue allocating against its captured target instead of the new live
// session. Once an index is handed out it is never reused, so deferred orphan
// cleanup can only remove files written for that index by this process.
func (a *MainAgent) nextCompactionIndexForAgent(sessionDir string) (int, error) {
	sessionDir = filepath.Clean(sessionDir)
	a.compactionIndexAllocsMu.Lock()
	if a.compactionIndexAllocs == nil {
		a.compactionIndexAllocs = make(map[string]*compactionIndexAllocator)
	}
	alloc := a.compactionIndexAllocs[sessionDir]
	if alloc == nil {
		alloc = &compactionIndexAllocator{}
		a.compactionIndexAllocs[sessionDir] = alloc
	}
	a.compactionIndexAllocsMu.Unlock()

	alloc.mu.Lock()
	defer alloc.mu.Unlock()
	if !alloc.seeded {
		next, err := nextCompactionIndex(sessionDir)
		if err != nil {
			return 0, err
		}
		alloc.next = next
		alloc.seeded = true
	}
	alloc.next++
	return alloc.next - 1, nil
}

// reseedCompactionIndexAllocator raises the in-memory floor to the maximum
// index currently visible on disk without lowering indexes already handed
// out. Session activation calls this because another process may have written
// an archive while this session was not active.
func (a *MainAgent) reseedCompactionIndexAllocator(sessionDir string) error {
	sessionDir = filepath.Clean(sessionDir)
	a.compactionIndexAllocsMu.Lock()
	if a.compactionIndexAllocs == nil {
		a.compactionIndexAllocs = make(map[string]*compactionIndexAllocator)
	}
	alloc := a.compactionIndexAllocs[sessionDir]
	if alloc == nil {
		alloc = &compactionIndexAllocator{}
		a.compactionIndexAllocs[sessionDir] = alloc
	}
	a.compactionIndexAllocsMu.Unlock()

	next, err := nextCompactionIndex(sessionDir)
	if err != nil {
		return err
	}
	alloc.mu.Lock()
	defer alloc.mu.Unlock()
	if !alloc.seeded || next > alloc.next {
		alloc.next = next
		alloc.seeded = true
	}
	return nil
}

// pruneCompactionIndexAllocators drops per-directory allocators that the
// process no longer needs, so a long-lived session that switches between many
// session directories cannot accumulate one map entry per directory forever.
// Call on session activation (switch / restore) with the directory that just
// became active.
//
// keepDir is always kept. Any other entry is dropped only once every index it
// handed out is already visible on disk (the on-disk floor has caught up with
// the in-memory one): re-creating that allocator later re-seeds from the same
// or a higher floor, so a late worker still holding the directory can never be
// handed an index a previous worker already used. An entry whose allocations
// have not landed yet simply stays until a later activation.
func (a *MainAgent) pruneCompactionIndexAllocators(keepDir string) {
	keepDir = filepath.Clean(keepDir)
	a.compactionIndexAllocsMu.Lock()
	candidates := make([]string, 0, len(a.compactionIndexAllocs))
	for dir := range a.compactionIndexAllocs {
		if dir != keepDir {
			candidates = append(candidates, dir)
		}
	}
	a.compactionIndexAllocsMu.Unlock()

	for _, dir := range candidates {
		diskNext, err := nextCompactionIndex(dir)
		if err != nil {
			continue
		}
		a.compactionIndexAllocsMu.Lock()
		alloc := a.compactionIndexAllocs[dir]
		if alloc != nil {
			alloc.mu.Lock()
			settled := !alloc.seeded || diskNext >= alloc.next
			alloc.mu.Unlock()
			if settled {
				delete(a.compactionIndexAllocs, dir)
			}
		}
		a.compactionIndexAllocsMu.Unlock()
	}
}

// captureOriginalFirstUserHint returns the best-known original first user
// message. It must be called BEFORE the on-disk main.jsonl has been replaced
// (otherwise FirstUserMessageFromFile would read the new compacted content).
// It is also safe to call without holding the ctxmgr write lock — Snapshot()
// is RLock-only.
//
// Order of preference:
//  1. ledger's already-set OriginalFirstUserMessage (cheapest, authoritative)
//  2. usage-summary.json's OriginalFirstUserMessage
//  3. read pre-rewrite main.jsonl directly (skips IsCompactionSummary)
//  4. scan in-memory ctxMgr snapshot (skip IsCompactionSummary)
//  5. usage-summary.json's FirstUserMessage as a last resort for older sessions
//     whose summary predates OriginalFirstUserMessage persistence
//
// Returns "" if no candidate is found; the caller may then fall back further.
func (a *MainAgent) captureOriginalFirstUserHint() string {
	if a == nil {
		return ""
	}
	var usageSummaryFirstUser string
	if a.usageLedger != nil {
		if v := strings.TrimSpace(a.usageLedger.OriginalFirstUserMessage()); v != "" {
			return v
		}
		if usageSummary, err := a.usageLedger.Summary(); err == nil && usageSummary != nil {
			if v := strings.TrimSpace(usageSummary.OriginalFirstUserMessage); v != "" {
				return v
			}
			usageSummaryFirstUser = strings.TrimSpace(usageSummary.FirstUserMessage)
		}
	}
	mainPath := filepath.Join(a.sessionDir, identity.MainSessionLogFilename)
	if info, err := os.Stat(mainPath); err == nil && info.Size() > 0 {
		if first, err := recovery.FirstUserMessageFromFile(mainPath); err == nil {
			if v := strings.TrimSpace(first); v != "" {
				return v
			}
		}
	}
	for _, msg := range a.ctxMgr.Snapshot() {
		if !message.IsUserAuthored(msg) {
			continue
		}
		candidate := strings.TrimSpace(message.UserPromptPlainText(msg))
		if candidate != "" {
			return candidate
		}
	}
	return usageSummaryFirstUser
}

func (a *MainAgent) rewriteSessionAfterCompaction(index int, messages []message.Message, originalFirstUserHint string) (string, error) {
	a.flushPersist()

	mainPath := filepath.Join(a.sessionDir, identity.MainSessionLogFilename)
	backupPath := filepath.Join(a.sessionDir, fmt.Sprintf("main.pre-compress-%d.jsonl", index))
	hadMain := false
	if info, err := os.Stat(mainPath); err == nil && info.Size() > 0 {
		hadMain = true
	}

	// Use the hint captured before main.jsonl was rewritten (see
	// applyCompactionDraftAsync). If the hint is empty for whatever reason,
	// retry from the ledger / pre-rewrite file as a defence in depth — but
	// note that we are inside ReplacePrefixAtomic's callback (write-locked
	// against ctxmgr), so we MUST NOT call ctxMgr.Snapshot() here.
	originalFirstUser := strings.TrimSpace(originalFirstUserHint)
	if originalFirstUser == "" && a.usageLedger != nil {
		if v := strings.TrimSpace(a.usageLedger.OriginalFirstUserMessage()); v != "" {
			originalFirstUser = v
		} else if usageSummary, err := a.usageLedger.Summary(); err == nil && usageSummary != nil {
			if v := strings.TrimSpace(usageSummary.OriginalFirstUserMessage); v != "" {
				originalFirstUser = v
			}
		}
	}
	if originalFirstUser == "" && hadMain {
		if first, err := recovery.FirstUserMessageFromFile(mainPath); err == nil {
			originalFirstUser = strings.TrimSpace(first)
		}
	}
	if originalFirstUser == "" {
		for _, msg := range messages {
			if !message.IsUserAuthored(msg) {
				continue
			}
			if v := strings.TrimSpace(message.UserPromptPlainText(msg)); v != "" {
				originalFirstUser = v
				break
			}
		}
	}
	if originalFirstUser == "" && a.usageLedger != nil {
		if usageSummary, err := a.usageLedger.Summary(); err == nil && usageSummary != nil {
			if v := strings.TrimSpace(usageSummary.FirstUserMessage); v != "" {
				originalFirstUser = v
			}
		}
	}

	// The rewrite replaces the manager within the same session, so sub-agents
	// and tool goroutines must follow it: they resolve the manager per write
	// through the ownership rather than caching one, and see nil for the span
	// where the transcript file is being swapped underneath them.
	if manager := a.recoveryManager(); manager != nil {
		a.clearRecoveryManagerIf(manager)
		manager.Close()
	}

	if hadMain {
		if err := os.Rename(mainPath, backupPath); err != nil {
			return "", err
		}
	}

	rm := recovery.NewRecoveryManager(a.sessionDir)
	for _, msg := range messages {
		if err := rm.PersistMessage(identity.MainAgentID, msg); err != nil {
			rm.Close()
			_ = os.Remove(mainPath)
			if hadMain {
				_ = os.Rename(backupPath, mainPath)
			}
			a.installRecoveryManager(recovery.NewRecoveryManager(a.sessionDir))
			return "", err
		}
	}
	a.installRecoveryManager(rm)
	if a.usageLedger != nil {
		firstUser := ""
		for _, msg := range messages {
			if msg.Role == message.RoleUser {
				firstUser = message.UserPromptPlainText(msg)
				if strings.TrimSpace(firstUser) != "" {
					break
				}
			}
		}
		if err := a.usageLedger.RewriteFirstUserMessageWithOriginalForCompaction(firstUser, originalFirstUser); err != nil {
			log.Warnf("failed to rewrite usage summary first user message after compaction error=%v", err)
		} else {
			summaryOriginal := originalFirstUser
			if usageSummary, sumErr := a.usageLedger.Summary(); sumErr == nil && usageSummary != nil {
				if v := strings.TrimSpace(usageSummary.OriginalFirstUserMessage); v != "" {
					summaryOriginal = v
				}
			}
			a.updateSessionSummary(func(summary *SessionSummary) {
				if summary == nil {
					return
				}
				summary.FirstUserMessage = strings.TrimSpace(firstUser)
				summary.FirstUserMessageIsCompactionSummary = true
				if summaryOriginal != "" {
					summary.OriginalFirstUserMessage = summaryOriginal
				}
			})
		}
	}
	if !hadMain {
		backupPath = "(none)"
	}
	return backupPath, nil
}

func nextHistoryIndexMinusOne(sessionDir string) int {
	next, err := nextCompactionIndex(sessionDir)
	if err != nil || next <= 1 {
		return 0
	}
	return next - 1
}

func spawnStatesForSnapshot() []recovery.BackgroundObjectState {
	jobs := tools.SnapshotSpawnedProcesses()
	if len(jobs) == 0 {
		return nil
	}
	states := make([]recovery.BackgroundObjectState, 0, len(jobs))
	for _, job := range jobs {
		states = append(states, recovery.BackgroundObjectState{
			ID:            job.ID,
			AgentID:       job.AgentID,
			Kind:          job.Kind,
			Description:   job.Description,
			Command:       job.Command,
			StartedAt:     job.StartedAt,
			MaxRuntimeSec: job.MaxRuntimeSec,
			Status:        job.Status,
			FinishedAt:    job.FinishedAt,
		})
	}
	return states
}

// persistSnapshotLocked builds and writes one recovery snapshot atomically:
// the whole build + SaveSnapshot runs under recoverySnapshotMu so concurrent
// writers (the event loop's apply/TodoWrite/freeze paths and SubAgent
// persistence callbacks) can never interleave a stale build after a fresher
// write. The recovery manager is resolved inside the same guard that
// installRecoveryManager and clearRecoveryManagerIf take, so a session switch
// really cannot swap it between the guard and the write: the snapshot is built
// from, and written through, one session's state.
func (a *MainAgent) persistSnapshotLocked(build func() *recovery.SessionSnapshot) error {
	if a == nil {
		return nil
	}
	a.recoverySnapshotMu.Lock()
	defer a.recoverySnapshotMu.Unlock()
	manager := a.recoveryManager()
	if manager == nil {
		return nil
	}
	return manager.SaveSnapshot(build())
}

func (a *MainAgent) saveRecoverySnapshot() {
	if a.shuttingDown.Load() {
		return
	}
	if err := a.persistSnapshotLocked(a.buildRecoverySnapshot); err != nil {
		log.Warnf("failed to save recovery snapshot error=%v", err)
	}
}
