package tui

import (
	"time"

	"github.com/keakon/chord/internal/agent"
)

const statusBarConnectingCadence = 500 * time.Millisecond

// Samples belong to the footer, not to the request's authoritative progress.
// State transitions and card boundaries bypass the numeric refresh interval.
type statusBarActivitySampleKey struct {
	Activity   agent.AgentActivityEvent
	StartedAt  time.Time
	BaseBytes  int64
	BaseEvents int64
	Done       bool
}

type statusBarCadenceState struct {
	activityKey        statusBarActivitySampleKey
	activitySecond     int64
	activityDisplay    statusBarActivityDisplay
	activityValid      bool
	compactionIdentity compactionBackgroundStatus
	compactionSecond   int64
	compactionDisplay  compactionBackgroundStatus
	compactionValid    bool
}

func (m Model) statusBarConnectingFrameAt(now time.Time) string {
	if !m.animRunning || m.currentCadence().visualAnimDelay <= 0 {
		return statusBarConnectingFrames[0]
	}
	return statusBarConnectingFrames[now.UnixMilli()/statusBarConnectingCadence.Milliseconds()%int64(len(statusBarConnectingFrames))]
}

func (m *Model) statusBarSampledActivityAt(a agent.AgentActivityEvent, now time.Time) statusBarActivityDisplay {
	if a.AgentID == "" {
		a.AgentID = "main"
	}
	startedAt := m.activityStartTime[a.AgentID]
	if startedAt.IsZero() {
		startedAt, _ = m.latestStatusStartWall(a.AgentID)
	}
	progress := m.requestProgress[a.AgentID]
	key := statusBarActivitySampleKey{
		Activity: a, StartedAt: startedAt,
		BaseBytes: progress.BaseBytes, BaseEvents: progress.BaseEvents, Done: progress.Done,
	}
	sample := &m.statusBarCadence
	if !sample.activityValid || sample.activityKey != key || sample.activitySecond != now.Unix() {
		sample.activityValid = true
		sample.activityKey = key
		sample.activitySecond = now.Unix()
		sample.activityDisplay = m.buildStatusBarActivityDisplayAt(a, now)
	}
	display := sample.activityDisplay
	if a.Type == agent.ActivityConnecting {
		display.Icon = m.statusBarConnectingFrameAt(now)
	}
	return display
}

func (m *Model) statusBarSampledCompactionAt(now time.Time) compactionBackgroundStatus {
	status := m.compactionBgStatus
	identity := status
	identity.Bytes = 0
	identity.Events = 0
	sample := &m.statusBarCadence
	if !sample.compactionValid || sample.compactionIdentity != identity || sample.compactionSecond != now.Unix() {
		sample.compactionValid = true
		sample.compactionIdentity = identity
		sample.compactionSecond = now.Unix()
		sample.compactionDisplay = status
	}
	return sample.compactionDisplay
}
