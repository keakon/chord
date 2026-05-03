package tui

import (
	"context"
	"strings"
	"time"

	"github.com/keakon/golog/log"

	tea "charm.land/bubbletea/v2"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/runtimecache"
)

type runtimeCacheManager interface {
	OpenSession(projectRoot, sessionID string) (runtimeCacheSessionHandle, error)
	CleanupStaleSessions(ctx context.Context) error
}

type runtimeCacheSessionHandle interface {
	SessionID() string
	Dir() string
	ViewportSpillPath() string
	ImageOpenDir() string
	Close() error
	Remove() error
}

type runtimeCacheManagerAdapter struct {
	*runtimecache.Manager
}

func newRuntimeCacheManager() runtimeCacheManager {
	locator, err := config.DefaultPathLocator()
	if err != nil || locator == nil || strings.TrimSpace(locator.CacheDir) == "" {
		return nil
	}
	mgr, err := runtimecache.NewManager(locator.CacheDir)
	if err != nil {
		return nil
	}
	return runtimeCacheManagerAdapter{Manager: mgr}
}

func (a runtimeCacheManagerAdapter) OpenSession(projectRoot, sessionID string) (runtimeCacheSessionHandle, error) {
	if a.Manager == nil {
		return nil, nil
	}
	return a.Manager.OpenSession(projectRoot, sessionID)
}

func (m *Model) currentSessionID() string {
	if m == nil || m.agent == nil {
		return ""
	}
	summary := m.agent.GetSessionSummary()
	if summary == nil {
		return ""
	}
	return strings.TrimSpace(summary.ID)
}

func (m *Model) startRuntimeCacheCleanup() tea.Cmd {
	if m == nil || m.runtimeCacheMgr == nil || m.agent == nil {
		return nil
	}
	if !m.startupRestorePending && m.currentSessionID() == "" {
		return nil
	}
	return func() tea.Msg {
		go func(mgr runtimeCacheManager) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := mgr.CleanupStaleSessions(ctx); err != nil && ctx.Err() == nil {
				log.Warnf("tui runtime cache stale cleanup failed error=%v", err)
			}
		}(m.runtimeCacheMgr)
		return nil
	}
}

func (m *Model) prepareRuntimeCacheSession(reset bool) (*ViewportSpillStore, runtimeCacheSessionHandle, error) {
	if m == nil || m.runtimeCacheMgr == nil || strings.TrimSpace(m.workingDir) == "" {
		return nil, nil, nil
	}
	sessionID := m.currentSessionID()
	if sessionID == "" {
		return nil, nil, nil
	}
	if !reset && m.runtimeCacheHandle != nil && m.runtimeCacheSession == sessionID {
		return nil, nil, nil
	}

	handle, err := m.runtimeCacheMgr.OpenSession(m.workingDir, sessionID)
	if err != nil {
		return nil, nil, err
	}
	spill, err := newViewportSpillStoreAt(handle.ViewportSpillPath(), "")
	if err != nil {
		_ = handle.Remove()
		return nil, nil, err
	}

	var oldStore *ViewportSpillStore
	if m.viewport != nil {
		oldStore = m.viewport.SwapSpillStore(spill)
	}
	oldHandle := m.runtimeCacheHandle
	m.runtimeCacheHandle = handle
	m.runtimeCacheSession = sessionID
	return oldStore, oldHandle, nil
}

func (m *Model) finishRuntimeCacheSessionSwap(oldStore *ViewportSpillStore, oldHandle runtimeCacheSessionHandle) {
	if oldStore != nil {
		if err := oldStore.Close(); err != nil {
			log.Warnf("close previous viewport spill store failed error=%v", err)
		}
	}
	if oldHandle != nil {
		if err := oldHandle.Remove(); err != nil {
			log.Warnf("remove previous session runtime cache failed error=%v", err)
		}
	}
}

func (m *Model) ensureRuntimeCacheSession(reset bool) error {
	oldStore, oldHandle, err := m.prepareRuntimeCacheSession(reset)
	if err != nil {
		return err
	}
	m.finishRuntimeCacheSessionSwap(oldStore, oldHandle)
	return nil
}

func (m *Model) runtimeImageOpenDir() string {
	if m == nil || m.runtimeCacheHandle == nil {
		return ""
	}
	return strings.TrimSpace(m.runtimeCacheHandle.ImageOpenDir())
}

func (m *Model) Close() error {
	if m == nil {
		return nil
	}
	var firstErr error
	if m.viewport != nil {
		if err := m.viewport.Close(); err != nil {
			firstErr = err
		}
	}
	if m.runtimeCacheHandle != nil {
		if err := m.runtimeCacheHandle.Remove(); err != nil && firstErr == nil {
			firstErr = err
		}
		m.runtimeCacheHandle = nil
		m.runtimeCacheSession = ""
	}
	return firstErr
}
