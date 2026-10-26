package lsp

import (
	"context"
	"sync"

	"github.com/keakon/chord/internal/config"
)

// FileWrite is one file a multi-file tool call changed, as input to the batched
// after-write pipeline.
type FileWrite struct {
	Path       string
	Content    string
	ChangeType WatchedFileChangeType
}

// AfterFileWritesToolResult runs the after-write pipeline for every file one
// multi-file tool call changed and returns one result string per input path,
// keyed by the path exactly as the caller passed it. Each string has the shape
// AfterFileWriteToolResult produces for that write alone with base "".
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
func (m *Manager) AfterFileWritesToolResult(ctx context.Context, writes []FileWrite, displayBaseDir string) map[string]string {
	if m == nil || len(writes) == 0 {
		return nil
	}

	type batchWrite struct {
		// key is the caller's spelling of the path; the first spelling of a
		// file owns the result.
		key         string
		path        string
		write       FileWrite
		coldStart   bool
		exited      []string
		base        string
		clientReady bool
		prepared    afterWritePrepared
		await       bool
		result      string
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
			continue
		}
		entry.coldStart, entry.base, entry.exited = m.beginAfterFileWrite(ctx, normalized, "")
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
	// server sees the batch's changes as one unit.
	var clientWG sync.WaitGroup
	for _, entry := range batch {
		if !entry.await {
			continue
		}
		clientWG.Go(func() {
			_, entry.clientReady = afterWriteWaitForClient(m, ctx, entry.path, afterWriteClientWaitTimeout)
		})
	}
	clientWG.Wait()

	for _, entry := range batch {
		if !entry.await {
			continue
		}
		if !entry.clientReady {
			entry.prepared = afterWritePrepared{path: entry.path, base: m.appendStartFailureNotes(ctx, entry.path, entry.base, entry.exited)}
			continue
		}
		entry.prepared = m.prepareReadyAfterFileWrite(ctx, entry.path, entry.write.Content, entry.base, entry.write.ChangeType, entry.coldStart)
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

	results := make(map[string]string, len(batch))
	for _, entry := range batch {
		results[entry.key] = entry.result
	}
	return results
}
