package lsp

import (
	"context"
	"sync"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/identity"
)

// FileWrite is one file a multi-file tool call changed, as input to the batched
// after-write pipeline.
type FileWrite struct {
	Path       string
	Content    string
	ChangeType WatchedFileChangeType
}

// AfterFileWritesToolResult runs the after-write pipeline for every file one
// multi-file tool call changed and returns one result per input path, keyed by
// the path exactly as the caller passed it. Each Output has the shape
// AfterFileWriteToolResult produces for that write alone with base ""; Status
// reports whether its diagnostics describe this write.
//
// The pipeline runs in phases. First every covered file's servers are started
// and its cold-start state recorded, before any client wait can make a still
// starting server look warm to a sibling. Then the client waits run
// concurrently so a hung launch costs one startup budget for the batch instead
// of one per file. Every file's content sync (didChange/didSave) is sent before
// any diagnostics wait starts, so a language server can coalesce the changes;
// the waits then run concurrently and overlap their settle windows instead of
// paying N serial analysis-plus-settle rounds. The protocol does not promise a
// single analysis pass, so this is a latency and startup-budget guarantee, not
// a claim about how many times the server computes. Python files keep their
// per-file backend, which runs serially on its own goroutine so it never delays
// another file's sync. Files no server covers stay skipped.
func (m *Manager) AfterFileWritesToolResult(ctx context.Context, writes []FileWrite, displayBaseDir string) map[string]FileWriteResult {
	if m == nil || len(writes) == 0 {
		return nil
	}
	// The batch's cold-start total budget counts from here, before any begin
	// or client wait: the batch is one analysis unit for latency purposes.
	began := time.Now()

	type batchWrite struct {
		// key is the caller's spelling of the path; the first spelling of a
		// file owns the result.
		key   string
		path  string
		write FileWrite
		// clientWait is how long the parallel client wait ran, kept here until
		// the prepare pass builds the prepared struct that carries it into the
		// breakdown log.
		clientWait  time.Duration
		coldStart   bool
		exited      []string
		base        string
		clientReady bool
		prepared    afterWritePrepared
		await       bool
		result      FileWriteResult
	}
	batch := make(map[string]*batchWrite, len(writes))
	var pythonEntries []*batchWrite
	for _, write := range writes {
		// Two spellings of the same file normalize to one entry, so the
		// concurrent phases stay single-writer per path.
		normalized := normalizeWaiterPath(write.Path)
		if _, dup := batch[normalized]; dup {
			continue
		}
		entry := &batchWrite{key: write.Path, path: normalized, write: write}
		batch[normalized] = entry
		if isPythonPath(normalized) && config.DiagnosticsEnabled(m.cfg) {
			pythonEntries = append(pythonEntries, entry)
			continue
		}
		// Unassociated file type: skip LSP entirely (no Start, no note).
		if !m.HasServerForPath(normalized) {
			entry.result = FileWriteResult{Status: FileWriteStatusNoServer}
			continue
		}
		entry.coldStart, entry.base, entry.exited, _ = m.beginAfterFileWrite(ctx, normalized, "")
		entry.await = true
	}

	// The Python backend is serial per file by design; running it on its own
	// goroutine keeps that order without holding up the other files' syncs.
	var pythonWG sync.WaitGroup
	if len(pythonEntries) > 0 {
		pythonWG.Go(func() {
			for _, entry := range pythonEntries {
				entry.result = m.afterWritePythonToolResult(ctx, entry.path, entry.write.Content, "", false, nil, entry.write.ChangeType, displayBaseDir)
			}
		})
	}

	// All servers were started above, so the client waits can run in parallel:
	// a launch that stays pending costs one startup budget for the whole batch
	// instead of one per file. The content syncs below stay serial so the
	// server sees the batch's changes as one unit. Cold entries draw their
	// client wait from the batch's total budget.
	bounded := false
	for _, entry := range batch {
		bounded = bounded || entry.await && (entry.coldStart || m.clientAdmittedWithin(entry.path, warmupGrace))
	}
	completed := m.runWritePreparation(ctx, began, bounded, func(ctx context.Context) {
		var clientWG sync.WaitGroup
		for _, entry := range batch {
			if !entry.await {
				continue
			}
			clientWG.Go(func() {
				clientWait := afterWriteClientWaitTimeout
				if entry.coldStart {
					if remaining := coldStartWriteBudget - time.Since(began); remaining < clientWait {
						clientWait = max(remaining, 0)
					}
				}
				waitStart := time.Now()
				_, entry.clientReady = afterWriteWaitForClient(m, ctx, entry.path, clientWait)
				entry.clientWait = time.Since(waitStart)
			})
		}
		clientWG.Wait()

		// The breakdown's batch_size counts the files that share this batch's
		// wait pipeline, not the tool call's whole file list: entries without
		// server coverage never entered it.
		batchSize := 0
		for _, entry := range batch {
			if entry.await {
				batchSize++
			}
		}
		var pending []FileWrite
		for _, entry := range batch {
			if !entry.await {
				continue
			}
			if !entry.clientReady {
				if ctx.Err() == nil {
					pending = append(pending, FileWrite{Path: entry.path, Content: entry.write.Content, ChangeType: entry.write.ChangeType})
				}
				entry.prepared = afterWritePrepared{
					path:       entry.path,
					base:       m.appendStartFailureNotes(ctx, entry.path, entry.base, entry.exited),
					began:      began,
					clientWait: entry.clientWait,
					coldStart:  entry.coldStart,
					grace:      !entry.coldStart && m.clientAdmittedWithin(entry.path, warmupGrace),
					batchSize:  batchSize,
					skipStatus: unavailableStatus(ctx, FileWriteStatusStartFailed),
				}
				continue
			}
			entry.prepared = m.prepareReadyAfterFileWrite(ctx, entry.path, entry.write.Content, entry.base, entry.write.ChangeType, entry.coldStart, began)
			entry.prepared.clientWait = entry.clientWait
			entry.prepared.batchSize = batchSize
		}
		m.registerPendingSyncs(pending, identity.AgentIDFromContext(ctx))

		// Every file's watch registers with the client generations the batch's
		// last sync produced, not the ones captured during that file's own
		// preparation: each didChange advanced the shared client's sync sequence,
		// so a watch carrying an earlier generation would be pruned at
		// registration and only the last-synced file could ever claim a late
		// publish. Sync-failed entries keep their stale state and register
		// nothing.
		finalGenerations := make(map[clientKey]uint64)
		for _, entry := range batch {
			for key, generation := range entry.prepared.sync.generations {
				finalGenerations[key] = max(finalGenerations[key], generation)
			}
		}
		for _, entry := range batch {
			if entry.await && entry.prepared.wait && entry.prepared.sync.token.confirmable {
				entry.prepared.watchGenerations = batchWatchGenerations(entry.prepared.watchGenerations, finalGenerations)
			}
		}

	}, func(ctx context.Context) {
		for _, entry := range batch {
			if entry.await {
				m.detachPreparedWrite(ctx, &entry.prepared, identity.AgentIDFromContext(ctx))
			}
		}
	})
	if !completed {
		pythonWG.Wait()
		results := make(map[string]FileWriteResult, len(batch))
		for _, entry := range batch {
			if entry.await {
				results[entry.key] = m.coldWriteBudgetResult(ctx, entry.path, entry.base)
			} else {
				results[entry.key] = entry.result
			}
		}
		return results
	}

	var wg sync.WaitGroup
	for _, entry := range batch {
		if !entry.await {
			continue
		}
		wg.Add(1)
		go func(entry *batchWrite) {
			defer wg.Done()
			entry.result = m.awaitPreparedAfterFileWrite(ctx, &entry.prepared, false, displayBaseDir)
		}(entry)
	}
	wg.Wait()
	pythonWG.Wait()

	results := make(map[string]FileWriteResult, len(batch))
	for _, entry := range batch {
		results[entry.key] = entry.result
	}
	return results
}
