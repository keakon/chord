package agent

import (
	"errors"
	"strings"
	"sync"
	"time"
)

type PersistenceHealthState string

const (
	PersistenceHealthy    PersistenceHealthState = "healthy"
	PersistenceDegraded   PersistenceHealthState = "degraded"
	PersistenceRecovering PersistenceHealthState = "recovering"
)

var (
	errPersistenceQueueUnavailable = errors.New("ordered persistence queue unavailable")
	errPersistenceStopping         = errors.New("persistence barrier interrupted by shutdown")
)

// PersistenceHealth is a read-only snapshot of an agent's durable transcript
// health. Missing state in older sessions is interpreted as healthy.
type PersistenceHealth struct {
	State       PersistenceHealthState `json:"state"`
	LastError   string                 `json:"last_error,omitempty"`
	FailedAt    time.Time              `json:"failed_at"`
	RecoveredAt time.Time              `json:"recovered_at"`
}

type agentPersistenceHealth struct {
	mu          sync.RWMutex
	state       PersistenceHealthState
	lastError   string
	failedAt    time.Time
	recoveredAt time.Time
}

func normalizePersistenceHealthState(state PersistenceHealthState) PersistenceHealthState {
	switch state {
	case PersistenceDegraded, PersistenceRecovering:
		return state
	default:
		return PersistenceHealthy
	}
}

func (h *agentPersistenceHealth) snapshot() PersistenceHealth {
	if h == nil {
		return PersistenceHealth{State: PersistenceHealthy}
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return PersistenceHealth{
		State:       normalizePersistenceHealthState(h.state),
		LastError:   h.lastError,
		FailedAt:    h.failedAt,
		RecoveredAt: h.recoveredAt,
	}
}

func (h *agentPersistenceHealth) restore(snapshot PersistenceHealth) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.state = normalizePersistenceHealthState(snapshot.State)
	// A process cannot resume an in-flight recovery operation. Retry it from the
	// durable degraded state when the restored runtime next checkpoints.
	if h.state == PersistenceRecovering {
		h.state = PersistenceDegraded
	}
	h.lastError = strings.TrimSpace(snapshot.LastError)
	h.failedAt = snapshot.FailedAt
	h.recoveredAt = snapshot.RecoveredAt
	h.mu.Unlock()
}

func (h *agentPersistenceHealth) markDegraded(err error) bool {
	if h == nil || err == nil {
		return false
	}
	now := time.Now()
	h.mu.Lock()
	changed := normalizePersistenceHealthState(h.state) != PersistenceDegraded || h.lastError != err.Error()
	h.state = PersistenceDegraded
	h.lastError = err.Error()
	h.failedAt = now
	h.mu.Unlock()
	return changed
}

func (h *agentPersistenceHealth) beginRecovery() bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if normalizePersistenceHealthState(h.state) != PersistenceDegraded {
		return false
	}
	h.state = PersistenceRecovering
	return true
}

func (h *agentPersistenceHealth) markRecovered() {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.state = PersistenceHealthy
	h.lastError = ""
	h.recoveredAt = time.Now()
	h.mu.Unlock()
}

func (h *agentPersistenceHealth) reset() {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.state = PersistenceHealthy
	h.lastError = ""
	h.failedAt = time.Time{}
	h.recoveredAt = time.Time{}
	h.mu.Unlock()
}
