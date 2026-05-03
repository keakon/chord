// Package power provides runtime power management for preventing system sleep
// during agent activity. It is only effective on macOS (darwin) in local mode.
package power

import (
	"sync"
	"time"

	"github.com/keakon/golog/log"
)

// ActivityType mirrors agent.ActivityType to avoid a direct import cycle.
type ActivityType string

const (
	ActivityIdle          ActivityType = "idle"
	ActivityConnecting    ActivityType = "connecting"
	ActivityWaitingHeader ActivityType = "waiting_headers"
	ActivityWaitingToken  ActivityType = "waiting_token"
	ActivityStreaming     ActivityType = "streaming"
	ActivityExecuting     ActivityType = "executing"
	ActivityCompacting    ActivityType = "compacting"
	ActivityRetrying      ActivityType = "retrying"
	ActivityRetryingKey   ActivityType = "retrying_key"
	ActivityCooling       ActivityType = "cooling"
)

// IsSleepPreventing reports whether the activity type should prevent idle sleep.
// Compact activity is intentionally excluded to match TUI "non-busy" perception.
func IsSleepPreventing(t ActivityType) bool {
	switch t {
	case ActivityConnecting, ActivityWaitingHeader, ActivityWaitingToken,
		ActivityStreaming, ActivityExecuting,
		ActivityRetrying, ActivityRetryingKey, ActivityCooling:
		return true
	default:
		return false
	}
}

// Backend defines the platform-specific power management interface.
type Backend interface {
	// Acquire prevents the system from idle sleep.
	Acquire() error
	// Release allows the system to idle sleep again.
	Release() error
	// Close releases any held resources and waits for backend cleanup.
	Close() error
}

// Manager tracks agent activity states and manages power assertions.
// It aggregates activity across main agent and all subagents.
type Manager struct {
	mu sync.Mutex

	// activities maps agentID to its current activity type.
	activities map[string]ActivityType

	// held indicates whether the backend currently holds a sleep prevention assertion.
	held bool

	// releaseTimer is the delayed-release timer; fires when all agents have been idle.
	releaseTimer *time.Timer

	// releaseDelay is the duration to wait after all agents become idle before releasing.
	releaseDelay time.Duration

	// backend is the platform-specific implementation.
	backend Backend

	// closed indicates the manager has been shut down.
	closed bool
}

const defaultReleaseDelay = 1 * time.Second

// NewManager creates a new power manager with the given backend.
func NewManager(backend Backend) *Manager {
	return &Manager{
		activities:   make(map[string]ActivityType),
		releaseDelay: defaultReleaseDelay,
		backend:      backend,
	}
}

// UpdateActivity updates the activity state for the given agent.
// It triggers acquire/release based on the aggregated state.
func (m *Manager) UpdateActivity(agentID string, activity ActivityType) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		return
	}

	// Update the activity map.
	prev, existed := m.activities[agentID]
	m.activities[agentID] = activity

	// Check if any agent is now in a sleep-preventing state.
	anyActive := m.anyActiveLocked()

	// If any agent is active, we should hold the assertion immediately.
	if anyActive && !m.held {
		// Cancel any pending release timer.
		if m.releaseTimer != nil {
			m.releaseTimer.Stop()
			m.releaseTimer = nil
		}
		// Acquire immediately.
		if err := m.backend.Acquire(); err != nil {
			log.Warnf("power: failed to acquire sleep prevention error=%v", err)
		} else {
			log.Debugf("power: acquired sleep prevention trigger_agent=%v activity=%v", agentID, activity)
			m.held = true
		}
		return
	}

	// If we were holding and now all agents are idle, start delayed release.
	if m.held && !anyActive {
		// Cancel previous timer if any.
		if m.releaseTimer != nil {
			m.releaseTimer.Stop()
		}
		m.releaseTimer = time.AfterFunc(m.releaseDelay, func() {
			m.mu.Lock()
			defer m.mu.Unlock()
			if m.closed {
				return
			}
			// Re-check: maybe an agent became active during the delay.
			if m.anyActiveLocked() {
				// Still active, don't release. The assertion remains held.
				return
			}
			if !m.held {
				return
			}
			if err := m.backend.Release(); err != nil {
				log.Warnf("power: failed to release sleep prevention error=%v", err)
			} else {
				log.Debug("power: released sleep prevention")
				m.held = false
			}
		})
	}

	// Log activity transitions for debugging.
	if existed && prev != activity {
		log.Debugf("power: activity transition agent=%v from=%v to=%v held=%v", agentID, prev, activity, m.held)
	}
}

// anyActiveLocked reports whether any agent is in a sleep-preventing state.
// Caller must hold m.mu.
func (m *Manager) anyActiveLocked() bool {
	for _, activity := range m.activities {
		if IsSleepPreventing(activity) {
			return true
		}
	}
	return false
}

// Close releases any held assertion and shuts down the backend.
// After Close, UpdateActivity calls are no-ops.
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		return nil
	}
	m.closed = true

	if m.releaseTimer != nil {
		m.releaseTimer.Stop()
		m.releaseTimer = nil
	}

	if m.held {
		if err := m.backend.Release(); err != nil {
			log.Warnf("power: failed to release on close error=%v", err)
		}
		m.held = false
	}

	return m.backend.Close()
}

// IsHeld reports whether the manager currently holds a sleep prevention assertion.
// Primarily for testing.
func (m *Manager) IsHeld() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.held
}
