package llm

import "time"

const maxTimeDuration = time.Duration(1<<63 - 1)
const maxSafeDurationSeconds = int64((1<<63 - 1) / int64(time.Second))
const maxSafeDurationMilliseconds = int64((1<<63 - 1) / int64(time.Millisecond))

func durationFromPositiveSecondsClamped(seconds int64, cap time.Duration) time.Duration {
	return durationFromPositiveUnitsClamped(seconds, time.Second, maxSafeDurationSeconds, cap)
}

func durationFromPositiveMillisecondsClamped(milliseconds int64, cap time.Duration) time.Duration {
	return durationFromPositiveUnitsClamped(milliseconds, time.Millisecond, maxSafeDurationMilliseconds, cap)
}

func durationFromPositiveUnitsClamped(value int64, unit time.Duration, maxSafeValue int64, cap time.Duration) time.Duration {
	if value <= 0 {
		return 0
	}
	if cap > 0 {
		capUnits := int64(cap / unit)
		if capUnits > 0 && value > capUnits {
			return cap
		}
	}
	if value > maxSafeValue {
		if cap > 0 {
			return cap
		}
		return maxTimeDuration
	}
	return time.Duration(value) * unit
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
	for range doublings {
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
