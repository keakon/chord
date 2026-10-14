package llm

import (
	"errors"
	"strings"
	"time"
	"unicode"

	"github.com/keakon/chord/internal/message"
)

// maxCoolingWait bounds the sleep between rounds when the pool holds more than
// one target: the wait is only a re-probe interval there, because a sibling
// target, a newly added credential or a refreshed rate-limit snapshot can make
// progress possible long before the target that reported the longest cooldown
// recovers. A single-target pool has nothing to re-probe — key selection is a
// local check that returns the same AllKeysCoolingError until the key's
// recovery instant — so capping the wait there only wakes up to sleep again.
const maxCoolingWait = 1 * time.Minute

// coolingWaitCap reports the upper bound for a cooling wait, or 0 when the wait
// must run to the key's real recovery instant. A reported window needs no cap of
// its own: the cooldown it comes from is already the instant the key becomes
// usable — a Retry-After hint bounded by the provider's retry_after_max_s, or a
// confirmed quota reset instant that the provider alone defines.
func coolingWaitCap(fallbackEnabled bool, fallbackModels []FallbackModel) time.Duration {
	if !fallbackEnabled || len(fallbackModels) == 0 {
		return 0
	}
	return maxCoolingWait
}

func isAllKeysCoolingError(err error) bool {
	_, ok := errors.AsType[*AllKeysCoolingError](err)
	return ok
}

// upstreamStreamFailureRetryRounds bounds how many full retry rounds a
// provider-stream upstream failure (upstream_connection_error / "upstream
// stream was interrupted") is retried when the caller did not configure an
// explicit retry cap: brief upstream blips self-heal, but the failure is
// deterministic, so retrying forever would only burn time.
const upstreamStreamFailureRetryRounds = 2

// preservedInterruptionCooldownBase seeds the cooldown applied to the
// credential that just streamed a preserved partial before the interruption is
// escalated for continuation. ProviderConfig.MarkTransportCooldown grows the
// wait for that credential's consecutive failures, so a transport that keeps
// truncating mid-stream backs off over time instead of firing back-to-back,
// while a single blip recovers after the base wait. This throttle replaces a
// round cap: resumption itself is uncapped and only ends on completion, no new
// visible text, cancellation, staleness, or a non-resumable error.
//
// preservedInterruptionCooldownMax saturates that growth well below
// maxProviderRetryDelay on purpose. The cooldown exists to pace one caller's
// restarts, but it sits on a ProviderConfig shared with compaction, sub-agents
// and thinking translation; letting it reach a minute would starve exactly the
// compaction that a long continuation loop needs.
// These are vars so tests can shrink the seeded cooldown instead of waiting it
// out; production keeps the values described above.
var (
	preservedInterruptionCooldownBase = time.Second
	preservedInterruptionCooldownMax  = 8 * time.Second
)

func shouldContinueRetryMode(retryCount, maxAttempts int, lastErr error, hardCap bool) bool {
	if maxAttempts <= 0 {
		return true
	}
	if retryCount < maxAttempts {
		return true
	}
	if hardCap {
		return false
	}
	return isAllKeysCoolingError(lastErr) || isConcurrentRequestLimit429(lastErr)
}

// clampCoolingWait bounds one reported cooling window to cap. cap <= 0 keeps
// the reported window as-is; a zero or negative window still becomes the 1s
// floor so a round never spins without pausing.
func clampCoolingWait(wait, cap time.Duration) time.Duration {
	if wait <= 0 {
		return time.Second
	}
	if cap > 0 && wait > cap {
		return cap
	}
	return wait
}

// roundCoolingWait pairs the two durations a cooling round must keep apart.
// sleep is how long the round actually pauses before probing the pool again,
// bounded by the re-probe cap; recovery is the earliest instant a request can
// really go out, taken from the reported key cooldowns without that cap. They
// are equal for an uncapped (single-target) pool and diverge once the cap
// shortens the sleep, which is why a user-facing countdown must read recovery:
// waking up to re-probe is not the same as being able to send.
type roundCoolingWait struct {
	sleep    time.Duration
	recovery time.Duration
}

func (w roundCoolingWait) waiting() bool { return w.sleep > 0 }

// minPositiveDuration keeps the smaller non-zero duration. A zero candidate
// means "this target reported no cooling at all" and must not pull the window
// down to zero, which would turn the wait into an immediate re-probe.
func minPositiveDuration(current, candidate time.Duration) time.Duration {
	if candidate > 0 && (current == 0 || candidate < current) {
		return candidate
	}
	return current
}

// mergeCoolingWindow folds one reported cooling window into the round's running
// minimum. Both fields track their own minimum: the first target to re-probe
// decides when the next round starts, and the first target to actually recover
// decides what the countdown shows, and those can be different targets.
func mergeCoolingWindow(current roundCoolingWait, candidate, cap time.Duration) roundCoolingWait {
	current.sleep = minPositiveDuration(current.sleep, clampCoolingWait(candidate, cap))
	current.recovery = minPositiveDuration(current.recovery, clampCoolingWait(candidate, 0))
	return current
}

// mergePendingCoolingWait folds one target's accumulated window into the
// round's, ignoring targets that reported no cooling at all.
func mergePendingCoolingWait(current, candidate roundCoolingWait) roundCoolingWait {
	current.sleep = minPositiveDuration(current.sleep, candidate.sleep)
	current.recovery = minPositiveDuration(current.recovery, candidate.recovery)
	return current
}

func roundRetryDelay(backoffDelay, pendingRoundWait time.Duration) time.Duration {
	if backoffDelay <= 0 {
		return pendingRoundWait
	}
	if pendingRoundWait > 0 && pendingRoundWait < backoffDelay {
		return pendingRoundWait
	}
	return backoffDelay
}

func nextRetryCount(current int, roundHadRequestAttempt, roundHadUsableReply bool, lastErr error, hardCap bool) int {
	if roundHadUsableReply {
		return 0
	}
	if !hardCap && !roundHadRequestAttempt && isAllKeysCoolingError(lastErr) {
		return current
	}
	return current + 1
}

// hasVisibleContent reports whether content contains any user-visible
// printable rune. Zero-width format characters (e.g. \u200b) that some gateways
// emit as a placeholder before a connection timeout are not visible output and
// must not make an empty interrupted response look usable.
func hasVisibleContent(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool {
		return unicode.IsPrint(r) && !unicode.IsSpace(r)
	})
}

func hasRecoverableTruncatedThinking(resp *message.Response) bool {
	if resp == nil || (resp.StopReason != "length" && resp.StopReason != "max_tokens") {
		return false
	}
	return hasVisibleContent(resp.ReasoningContent)
}

func responseHasUsableOutput(resp *message.Response) bool {
	if resp == nil {
		return false
	}
	if resp.NativeTools != nil {
		return true
	}
	if resp.Hosted != nil && (len(resp.Hosted.Calls) > 0 || len(resp.Hosted.Items) > 0 || resp.Hosted.RequiresApproval) {
		// Hosted output must reach the backend for validation and continuation.
		return true
	}
	if hasVisibleContent(resp.Content) || len(resp.ToolCalls) > 0 {
		return true
	}
	// A reasoning-only length response must reach the agent's bounded recovery
	// path. It is not a normal successful answer, but discarding it here loses
	// the only reasoning prefix that can help a compatible gateway recover.
	if hasRecoverableTruncatedThinking(resp) {
		return true
	}
	return message.HasReplayableThinkingBlocks(resp.ThinkingBlocks)
}
