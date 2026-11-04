package lsp

import (
	"context"
	"path/filepath"
	"time"

	"github.com/keakon/chord/internal/config"
)

const maxPrewarmTasks = 4
const prewarmStartTimeout = 30 * time.Second

// Prewarm queues a best-effort Go server start. authorize runs off the stream
// callback, before filesystem discovery or subprocess creation. It must use
// immutable call inputs and must not wait for user approval.
func (m *Manager) Prewarm(ctx context.Context, path string, authorize func() bool) {
	if m == nil || m.cfg == nil || len(m.cfg.LSP) == 0 || !config.DiagnosticsEnabled(m.cfg) || ctx == nil || ctx.Err() != nil || filepath.Ext(path) != ".go" {
		return
	}
	if !m.clientsMu.TryLock() {
		return
	}
	if m.prewarmTasks >= maxPrewarmTasks {
		m.clientsMu.Unlock()
		return
	}
	taskCtx, _, finish := m.newSyncTaskLocked(ctx)
	era := m.startEra
	m.prewarmTasks++
	m.clientsMu.Unlock()
	go func() {
		defer finish()
		defer func() {
			m.clientsMu.Lock()
			m.prewarmTasks--
			m.clientsMu.Unlock()
		}()
		if taskCtx.Err() != nil || authorize == nil || !authorize() || taskCtx.Err() != nil {
			return
		}
		m.start(taskCtx, path, era)
	}()
}
