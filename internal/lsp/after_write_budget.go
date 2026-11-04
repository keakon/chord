package lsp

import (
	"context"
	"time"
)

// runWritePreparation hands completed preparation to the caller, or transfers
// its cleanup to the worker when a cold write exhausts the foreground budget.
// Transport writes cannot be interrupted by a context deadline.
func (m *Manager) runWritePreparation(ctx context.Context, began time.Time, bounded bool, prepare func(context.Context), detached func(context.Context)) bool {
	if !bounded {
		prepare(ctx)
		return true
	}
	if ctx.Err() != nil {
		return false
	}
	m.clientsMu.Lock()
	taskCtx, cancel, finish := m.newSyncTaskLocked(context.WithoutCancel(ctx))
	stopTurn := context.AfterFunc(ctx, cancel)
	m.clientsMu.Unlock()

	handoff := make(chan struct{})
	abandoned := make(chan struct{})
	go func() {
		defer func() {
			stopTurn()
			finish()
		}()
		prepare(taskCtx)
		select {
		case handoff <- struct{}{}:
		case <-abandoned:
			detached(taskCtx)
		}
	}()
	timer := time.NewTimer(max(coldStartWriteBudget-time.Since(began), 0))
	defer timer.Stop()
	select {
	case <-handoff:
		return true
	case <-timer.C:
		stopTurn()
	case <-ctx.Done():
	}
	close(abandoned)
	return false
}

func (m *Manager) ensureStartEraLocked() {
	if m.startEra == nil {
		m.startEra, m.cancelStartEra = context.WithCancel(context.Background())
	}
}

// Registration shares the client lock with Stop, so shutdown cannot miss a
// worker already allowed to use its clients.
func (m *Manager) newSyncTaskLocked(ctx context.Context) (context.Context, context.CancelFunc, func()) {
	m.ensureStartEraLocked()
	taskCtx, cancel := context.WithCancel(ctx)
	stopEra := context.AfterFunc(m.startEra, cancel)
	done := make(chan struct{})
	if m.syncTasks == nil {
		m.syncTasks = make(map[chan struct{}]context.CancelFunc)
	}
	m.syncTasks[done] = cancel
	return taskCtx, cancel, func() {
		cancel()
		stopEra()
		m.clientsMu.Lock()
		delete(m.syncTasks, done)
		close(done)
		m.clientsMu.Unlock()
	}
}

func (m *Manager) detachPreparedWrite(ctx context.Context, prepared *afterWritePrepared, writerAgentID string) {
	if prepared.waiter != nil {
		m.waitersMu.Lock()
		m.removeWaiter(prepared.path, prepared.waiter)
		m.waitersMu.Unlock()
	}
	lateWatch := false
	if prepared.wait && ctx.Err() == nil && prepared.sync.token.confirmable {
		lateWatch = m.watchLateDiagnostics(prepared.path, prepared.sync.serverVersions, prepared.watchGenerations, nil, prepared.afterSeq, writerAgentID, prepared.sync.token)
	}
	status := prepared.skipStatus
	if prepared.wait {
		status = unavailableStatus(ctx, FileWriteStatusTimeout)
	}
	m.logAfterWriteBreakdown(prepared, status, false, lateWatch, 0)
}

// coldWriteBudgetResult reports a write whose cold-start total budget ran out
// before its diagnostics wait could start (or finish). The cancelled case
// stays untouched; a timed-out one reports the wait in its diagnostics
// section, same as a wait that ran its course.
func (m *Manager) coldWriteBudgetResult(ctx context.Context, path, base string, includeOtherFiles bool, displayBaseDir string) FileWriteResult {
	if ctx.Err() != nil {
		return FileWriteResult{Output: base, Status: FileWriteStatusCancelled}
	}
	result := timeoutFileWrite(base, coldStartWriteBudget)
	states := map[string]FileWriteDiagnosticsState{path: {Status: result.Status, Detail: result.Detail}}
	result.Output = m.AppendLSPDiagnosticsToToolOutputForPaths(base, []string{path}, includeOtherFiles, nil, nil, nil, states, displayBaseDir)
	return result
}
