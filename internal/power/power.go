// Package power provides runtime power management for preventing system sleep
// during agent activity on the execution host.
package power

import "github.com/keakon/chord/internal/message"

// ActivityType mirrors agent.ActivityType, which cannot be imported here
// without a cycle. Values that name an LLM request phase are shared with the
// agent through message.StatusDelta*, so the two sides cannot drift apart.
type ActivityType string

const (
	ActivityIdle          ActivityType = "idle"
	ActivityPreparing     ActivityType = "preparing"
	ActivityConnecting    ActivityType = message.StatusDeltaConnecting
	ActivityWaitingHeader ActivityType = message.StatusDeltaWaitingHeaders
	ActivityWaitingToken  ActivityType = message.StatusDeltaWaitingToken
	ActivityStreaming     ActivityType = message.StatusDeltaStreaming
	ActivityExecuting     ActivityType = "executing"
	ActivityCompacting    ActivityType = message.StatusDeltaCompacting
	ActivityRetrying      ActivityType = message.StatusDeltaRetrying
	ActivityRetryingKey   ActivityType = message.StatusDeltaRetryingKey
	ActivityCooling       ActivityType = message.StatusDeltaCooling
)

// IsSleepPreventing reports whether the activity type should prevent idle sleep.
// Compact activity is intentionally excluded to match TUI "non-busy" perception.
func IsSleepPreventing(t ActivityType) bool {
	switch t {
	case ActivityPreparing, ActivityConnecting, ActivityWaitingHeader, ActivityWaitingToken,
		ActivityStreaming, ActivityExecuting,
		ActivityRetrying, ActivityRetryingKey, ActivityCooling:
		return true
	default:
		return false
	}
}
