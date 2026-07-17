package agent

import (
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	orchestrationTrackedMailboxLimit = 4096
	orchestrationTrackedParkedLimit  = 1024
)

type mailboxMetricState struct {
	createdAt  time.Time
	delivered  bool
	generation uint64
}

type parkedMetricState struct {
	parkedAt   time.Time
	generation uint64
}

type metricQueueEntry struct {
	id         string
	generation uint64
}

type orchestrationRuntimeMetrics struct {
	admissionWaitCount atomic.Uint64
	admissionWaitNanos atomic.Uint64
	scopeConflicts     atomic.Uint64
	rehydrates         atomic.Uint64
	parks              atomic.Uint64
	parkedSamples      atomic.Uint64
	parkedNanos        atomic.Uint64
	mailboxDeliveries  atomic.Uint64
	mailboxDeliveryNs  atomic.Uint64
	mailboxAcks        atomic.Uint64
	mailboxAckNs       atomic.Uint64
	mailboxEvictions   atomic.Uint64
	parkedEvictions    atomic.Uint64

	mailboxMu sync.Mutex
	mailboxes map[string]mailboxMetricState
	mailboxQ  []metricQueueEntry
	mailboxAt int
	mailboxID uint64
	parkedMu  sync.Mutex
	parkedAt  map[string]parkedMetricState
	parkedQ   []metricQueueEntry
	parkedPos int
	parkedID  uint64
}

// OrchestrationStats is a process-local, read-only snapshot of multi-agent
// coordination health. Counters reset when a MainAgent is reconstructed.
type OrchestrationStats struct {
	EventQueue EventQueueStats

	SemaphoreCapacity    int
	SemaphoreInUse       int
	SemaphoreUtilization float64

	TasksTotal      uint64
	TasksByState    map[string]uint64
	TerminalReasons map[string]uint64

	AdmissionWaitCount    uint64
	AdmissionWaitTotal    time.Duration
	AdmissionWaitAverage  time.Duration
	ScopeConflicts        uint64
	Rehydrates            uint64
	Parks                 uint64
	ParkedDurationCount   uint64
	ParkedDurationTotal   time.Duration
	ParkedDurationAverage time.Duration

	MailboxDeliveries             uint64
	MailboxDeliveryLatencyTotal   time.Duration
	MailboxDeliveryLatencyAverage time.Duration
	MailboxAcks                   uint64
	MailboxAckLatencyTotal        time.Duration
	MailboxAckLatencyAverage      time.Duration
	MailboxTrackingEvictions      uint64
	ParkedTrackingEvictions       uint64
}

func averageDuration(total time.Duration, count uint64) time.Duration {
	if count == 0 {
		return 0
	}
	return time.Duration(uint64(total) / count)
}

func addDuration(total *atomic.Uint64, duration time.Duration) {
	if duration > 0 {
		total.Add(uint64(duration))
	}
}

func (m *orchestrationRuntimeMetrics) recordAdmissionWait(duration time.Duration) {
	if m == nil {
		return
	}
	m.admissionWaitCount.Add(1)
	addDuration(&m.admissionWaitNanos, duration)
}

func (m *orchestrationRuntimeMetrics) recordPark(taskID string, parkedAt time.Time) {
	if m == nil {
		return
	}
	m.parks.Add(1)
	m.trackParked(taskID, parkedAt)
}

func (m *orchestrationRuntimeMetrics) trackParked(taskID string, parkedAt time.Time) {
	if m == nil {
		return
	}
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return
	}
	if parkedAt.IsZero() {
		parkedAt = time.Now()
	}
	m.parkedMu.Lock()
	if m.parkedAt == nil {
		m.parkedAt = make(map[string]parkedMetricState)
	}
	if _, exists := m.parkedAt[taskID]; !exists && len(m.parkedAt) >= orchestrationTrackedParkedLimit {
		m.evictTrackedParkedLocked()
	}
	if _, exists := m.parkedAt[taskID]; !exists {
		m.compactParkedQueueLocked()
		m.parkedID++
		m.parkedQ = append(m.parkedQ, metricQueueEntry{id: taskID, generation: m.parkedID})
		m.parkedAt[taskID] = parkedMetricState{parkedAt: parkedAt, generation: m.parkedID}
	} else {
		state := m.parkedAt[taskID]
		state.parkedAt = parkedAt
		m.parkedAt[taskID] = state
	}
	m.parkedMu.Unlock()
}

func (m *orchestrationRuntimeMetrics) compactParkedQueueLocked() {
	if len(m.parkedQ) < 2*orchestrationTrackedParkedLimit {
		return
	}
	queue := make([]metricQueueEntry, 0, len(m.parkedAt))
	for taskID, state := range m.parkedAt {
		queue = append(queue, metricQueueEntry{id: taskID, generation: state.generation})
	}
	m.parkedQ = queue
	m.parkedPos = 0
}

func (m *orchestrationRuntimeMetrics) evictTrackedParkedLocked() {
	for m.parkedPos < len(m.parkedQ) {
		entry := m.parkedQ[m.parkedPos]
		m.parkedPos++
		state, exists := m.parkedAt[entry.id]
		if !exists || state.generation != entry.generation {
			continue
		}
		delete(m.parkedAt, entry.id)
		m.parkedEvictions.Add(1)
		return
	}
}

func (m *orchestrationRuntimeMetrics) recordRehydrate(taskID string, rehydratedAt time.Time) {
	if m == nil {
		return
	}
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return
	}
	if rehydratedAt.IsZero() {
		rehydratedAt = time.Now()
	}
	m.rehydrates.Add(1)
	m.parkedMu.Lock()
	parkedState, exists := m.parkedAt[taskID]
	if exists {
		delete(m.parkedAt, taskID)
	}
	m.parkedMu.Unlock()
	if exists && rehydratedAt.After(parkedState.parkedAt) {
		m.parkedSamples.Add(1)
		addDuration(&m.parkedNanos, rehydratedAt.Sub(parkedState.parkedAt))
	}
}

func (m *orchestrationRuntimeMetrics) recordMailboxCreated(messageID string, createdAt time.Time) {
	if m == nil {
		return
	}
	messageID = strings.TrimSpace(messageID)
	if messageID == "" || createdAt.IsZero() {
		return
	}
	m.mailboxMu.Lock()
	if m.mailboxes == nil {
		m.mailboxes = make(map[string]mailboxMetricState)
	}
	if _, exists := m.mailboxes[messageID]; !exists {
		m.ensureMailboxCapacityLocked()
		m.compactMailboxQueueLocked()
		m.mailboxID++
		m.mailboxes[messageID] = mailboxMetricState{createdAt: createdAt, generation: m.mailboxID}
		m.mailboxQ = append(m.mailboxQ, metricQueueEntry{id: messageID, generation: m.mailboxID})
	}
	m.mailboxMu.Unlock()
}

func (m *orchestrationRuntimeMetrics) recordMailboxDelivery(messageID string, fallbackCreatedAt time.Time) {
	if m == nil {
		return
	}
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return
	}
	now := time.Now()
	m.mailboxMu.Lock()
	if m.mailboxes == nil {
		m.mailboxes = make(map[string]mailboxMetricState)
	}
	state, exists := m.mailboxes[messageID]
	if !exists {
		m.ensureMailboxCapacityLocked()
		m.compactMailboxQueueLocked()
		m.mailboxID++
		state.createdAt = fallbackCreatedAt
		state.generation = m.mailboxID
		m.mailboxQ = append(m.mailboxQ, metricQueueEntry{id: messageID, generation: m.mailboxID})
	}
	if state.delivered {
		m.mailboxMu.Unlock()
		return
	}
	state.delivered = true
	m.mailboxes[messageID] = state
	m.mailboxMu.Unlock()

	m.mailboxDeliveries.Add(1)
	if !state.createdAt.IsZero() {
		addDuration(&m.mailboxDeliveryNs, now.Sub(state.createdAt))
	}
}

func (m *orchestrationRuntimeMetrics) compactMailboxQueueLocked() {
	if len(m.mailboxQ) < 2*orchestrationTrackedMailboxLimit {
		return
	}
	queue := make([]metricQueueEntry, 0, len(m.mailboxes))
	for messageID, state := range m.mailboxes {
		queue = append(queue, metricQueueEntry{id: messageID, generation: state.generation})
	}
	m.mailboxQ = queue
	m.mailboxAt = 0
}

func (m *orchestrationRuntimeMetrics) ensureMailboxCapacityLocked() {
	if len(m.mailboxes) < orchestrationTrackedMailboxLimit {
		return
	}
	for m.mailboxAt < len(m.mailboxQ) {
		entry := m.mailboxQ[m.mailboxAt]
		m.mailboxAt++
		state, exists := m.mailboxes[entry.id]
		if !exists || state.generation != entry.generation {
			continue
		}
		delete(m.mailboxes, entry.id)
		m.mailboxEvictions.Add(1)
		return
	}
}

func (m *orchestrationRuntimeMetrics) resetCorrelations() {
	if m == nil {
		return
	}
	m.mailboxMu.Lock()
	m.mailboxes = nil
	m.mailboxQ = nil
	m.mailboxAt = 0
	m.mailboxID = 0
	m.mailboxMu.Unlock()
	m.parkedMu.Lock()
	m.parkedAt = nil
	m.parkedQ = nil
	m.parkedPos = 0
	m.parkedID = 0
	m.parkedMu.Unlock()
}

func (m *orchestrationRuntimeMetrics) recordMailboxAck(messageID string) {
	if m == nil {
		return
	}
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return
	}
	now := time.Now()
	m.mailboxMu.Lock()
	state, exists := m.mailboxes[messageID]
	if exists {
		delete(m.mailboxes, messageID)
	}
	m.mailboxMu.Unlock()
	if !exists {
		return
	}
	m.mailboxAcks.Add(1)
	if !state.createdAt.IsZero() {
		addDuration(&m.mailboxAckNs, now.Sub(state.createdAt))
	}
}

func (a *MainAgent) OrchestrationStats() OrchestrationStats {
	if a == nil {
		return OrchestrationStats{}
	}
	metrics := &a.orchestrationMetrics
	admissionCount := metrics.admissionWaitCount.Load()
	admissionTotal := time.Duration(metrics.admissionWaitNanos.Load())
	parkedCount := metrics.parkedSamples.Load()
	parkedTotal := time.Duration(metrics.parkedNanos.Load())
	deliveryCount := metrics.mailboxDeliveries.Load()
	deliveryTotal := time.Duration(metrics.mailboxDeliveryNs.Load())
	ackCount := metrics.mailboxAcks.Load()
	ackTotal := time.Duration(metrics.mailboxAckNs.Load())

	stats := OrchestrationStats{
		EventQueue:                    a.EventQueueStats(),
		SemaphoreCapacity:             cap(a.sem),
		SemaphoreInUse:                len(a.sem),
		AdmissionWaitCount:            admissionCount,
		AdmissionWaitTotal:            admissionTotal,
		AdmissionWaitAverage:          averageDuration(admissionTotal, admissionCount),
		ScopeConflicts:                metrics.scopeConflicts.Load(),
		Rehydrates:                    metrics.rehydrates.Load(),
		Parks:                         metrics.parks.Load(),
		ParkedDurationCount:           parkedCount,
		ParkedDurationTotal:           parkedTotal,
		ParkedDurationAverage:         averageDuration(parkedTotal, parkedCount),
		MailboxDeliveries:             deliveryCount,
		MailboxDeliveryLatencyTotal:   deliveryTotal,
		MailboxDeliveryLatencyAverage: averageDuration(deliveryTotal, deliveryCount),
		MailboxAcks:                   ackCount,
		MailboxAckLatencyTotal:        ackTotal,
		MailboxAckLatencyAverage:      averageDuration(ackTotal, ackCount),
		MailboxTrackingEvictions:      metrics.mailboxEvictions.Load(),
		ParkedTrackingEvictions:       metrics.parkedEvictions.Load(),
		TasksByState:                  make(map[string]uint64),
		TerminalReasons:               make(map[string]uint64),
	}
	if stats.SemaphoreCapacity > 0 {
		stats.SemaphoreUtilization = float64(stats.SemaphoreInUse) / float64(stats.SemaphoreCapacity)
	}

	a.subs.mu.RLock()
	for _, rec := range a.subs.taskRecords {
		if rec == nil {
			continue
		}
		stats.TasksTotal++
		state := strings.TrimSpace(rec.State)
		if state == "" {
			state = "unknown"
		}
		stats.TasksByState[state]++
		if isNonTerminalTaskState(state) {
			continue
		}
		reason := strings.TrimSpace(rec.ClosedReason)
		if reason == "" {
			reason = state
		}
		stats.TerminalReasons[reason]++
	}
	a.subs.mu.RUnlock()
	return stats
}
