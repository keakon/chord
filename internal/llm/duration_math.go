package llm

import "time"

const maxTimeDuration = time.Duration(1<<63 - 1)
const maxSafeDurationSeconds = int64((1<<63 - 1) / int64(time.Second))

func durationFromPositiveSecondsClamped(seconds int64, cap time.Duration) time.Duration {
	if seconds <= 0 {
		return 0
	}
	if cap > 0 {
		capSeconds := int64(cap / time.Second)
		if capSeconds > 0 && seconds > capSeconds {
			return cap
		}
	}
	if seconds > maxSafeDurationSeconds {
		if cap > 0 {
			return cap
		}
		return maxTimeDuration
	}
	return time.Duration(seconds) * time.Second
}

func saturatingDoublingDuration(base, cap time.Duration, doublings int) time.Duration {
	if base <= 0 {
		return 0
	}
	if cap > 0 && base >= cap {
		return cap
	}
	if doublings <= 0 {
		if cap > 0 && base > cap {
			return cap
		}
		return base
	}
	delay := base
	for i := 0; i < doublings; i++ {
		if cap > 0 && delay >= cap {
			return cap
		}
		if delay > maxTimeDuration/2 {
			if cap > 0 {
				return cap
			}
			return maxTimeDuration
		}
		delay *= 2
		if cap > 0 && delay >= cap {
			return cap
		}
	}
	if cap > 0 && delay > cap {
		return cap
	}
	return delay
}
