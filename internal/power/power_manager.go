package power

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/keakon/golog/log"
)

const (
	defaultReleaseDelay = time.Second
	acquireTimeout      = 5 * time.Second
	stablePeriod        = 30 * time.Second
)

// Manager aggregates activity; its worker alone owns platform operations.
// Observer calls never wait for acquisition or cleanup.
type Manager struct {
	mu         sync.Mutex
	activities map[string]ActivityType
	idleSince  time.Time
	closed     bool
	generation uint64
	cancel     context.CancelFunc
	lease      Lease
	closeErr   error
	backend    Backend
	wake       chan struct{}
	done       chan struct{}
}

func NewManager(backend Backend) *Manager {
	m := &Manager{activities: make(map[string]ActivityType), backend: backend,
		wake: make(chan struct{}, 1), done: make(chan struct{})}
	go m.run()
	return m
}

func (m *Manager) signal() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func (m *Manager) UpdateActivity(agentID string, activity ActivityType) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	wasActive := len(m.activities) != 0
	if IsSleepPreventing(activity) {
		m.activities[agentID] = activity
	} else {
		delete(m.activities, agentID)
	}
	active := len(m.activities) != 0
	var cancel context.CancelFunc
	if active != wasActive {
		if active {
			m.idleSince = time.Time{}
		} else {
			m.idleSince = time.Now()
			m.generation++
			cancel = m.cancel
		}
	}
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	m.signal()
}

// Close waits for the same cleanup completion on every call.
func (m *Manager) Close() error {
	m.mu.Lock()
	m.closed = true
	cancel := m.cancel
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	m.signal()
	<-m.done
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.closeErr
}

// IsHeld reports a live API lease, not proof of effective system policy.
func (m *Manager) IsHeld() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.lease == nil {
		return false
	}
	select {
	case <-m.lease.Done():
		return false
	default:
		return true
	}
}

func retryDelay(failures int) time.Duration {
	delays := [...]time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second, 60 * time.Second}
	if failures > len(delays) {
		return delays[len(delays)-1]
	}
	if failures < 1 {
		return delays[0]
	}
	return delays[failures-1]
}

func (m *Manager) run() {
	defer close(m.done)
	var lease Lease
	var retryAt, stableAt time.Time
	failures := 0
	disabled := false
	diagnostics := errorReporter{}
	cleanup := func(l Lease) {
		if err := l.Close(); err != nil {
			// Any failed lease cleanup is terminal, even for a faulty backend which
			// omitted the sentinel: never stack resources of uncertain ownership.
			err = errors.Join(ErrCleanupFailed, err)
			m.mu.Lock()
			m.closeErr = errors.Join(m.closeErr, err)
			m.mu.Unlock()
			disabled = true
			log.Warnf("power: lease cleanup failed error=%v", err)
		}
	}
	fail := func(err error) {
		diagnostics.report("manager", "acquire", err)
		if errors.Is(err, ErrCleanupFailed) {
			m.mu.Lock()
			m.closeErr = errors.Join(m.closeErr, err)
			m.mu.Unlock()
			disabled = true
		} else if errors.Is(err, ErrUnsupported) {
			disabled = true
		} else {
			if failures < 7 {
				failures++
			}
			retryAt = time.Now().Add(retryDelay(failures))
		}
	}
	clearLease := func() {
		m.mu.Lock()
		m.lease = nil
		m.mu.Unlock()
		cleanup(lease)
		lease = nil
		stableAt = time.Time{}
	}
	for {
		m.mu.Lock()
		closed, active, idleSince := m.closed, len(m.activities) != 0, m.idleSince
		m.mu.Unlock()
		if closed {
			if lease != nil {
				clearLease()
			}
			return
		}
		if lease != nil {
			select {
			case <-lease.Done():
				err := lease.Err()
				if err == nil {
					err = errors.New("sleep prevention lease ended unexpectedly")
				}
				clearLease()
				if !disabled {
					fail(err)
				}
				continue
			default:
			}
			if !stableAt.IsZero() && !time.Now().Before(stableAt) {
				failures = 0
				retryAt = time.Time{}
				stableAt = time.Time{}
				diagnostics.recovered("manager")
			}
			if !active && !time.Now().Before(idleSince.Add(defaultReleaseDelay)) {
				// Recheck under the observer mutex at the release commit point.
				m.mu.Lock()
				release := !m.closed && len(m.activities) == 0 && !time.Now().Before(m.idleSince.Add(defaultReleaseDelay))
				if release {
					m.lease = nil
				}
				m.mu.Unlock()
				if release {
					clearLease()
					continue
				}
				continue
			}
		} else if active && !disabled && !time.Now().Before(retryAt) {
			// Installation and the active/closed check share the observer's mutex.
			m.mu.Lock()
			if m.closed || len(m.activities) == 0 {
				m.mu.Unlock()
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), acquireTimeout)
			m.cancel = cancel
			generation := m.generation
			m.mu.Unlock()
			result, err := m.backend.Acquire(ctx)
			m.mu.Lock()
			stale := ctx.Err() != nil || m.closed || len(m.activities) == 0 || generation != m.generation
			ctxErr := ctx.Err()
			m.cancel = nil
			if err == nil && result != nil && !stale {
				m.lease = result
				lease = result
			}
			m.mu.Unlock()
			cancel() // The delivered lease has an independent lifetime.
			if result != nil && (err != nil || stale) {
				cleanup(result)
			}
			if err != nil {
				// Idle/Close cancellation is not an acquisition failure. A deadline is.
				if errors.Is(err, ErrCleanupFailed) || !stale || errors.Is(ctxErr, context.DeadlineExceeded) {
					fail(err)
				}
			} else if errors.Is(ctxErr, context.DeadlineExceeded) {
				fail(ctxErr)
			} else if result == nil {
				fail(errors.New("backend returned a nil lease"))
			} else if !stale {
				stableAt = time.Now().Add(stablePeriod)
				log.Debug("power: acquired API lease")
			}
			continue
		}
		var deadline time.Time
		var lost <-chan struct{}
		if lease != nil {
			lost = lease.Done()
			deadline = stableAt
			if !active {
				idleDeadline := idleSince.Add(defaultReleaseDelay)
				if deadline.IsZero() || idleDeadline.Before(deadline) {
					deadline = idleDeadline
				}
			}
		} else if active && !disabled {
			deadline = retryAt
		}
		var timer *time.Timer
		var tick <-chan time.Time
		if !deadline.IsZero() {
			timer = time.NewTimer(time.Until(deadline))
			tick = timer.C
		}
		select {
		case <-m.wake:
		case <-lost:
		case <-tick:
		}
		if timer != nil {
			timer.Stop()
		}
	}
}
