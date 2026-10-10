package httpheader

import (
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/http/httpguts"
)

// ParseRetryAfter accepts HTTP delay-seconds or an HTTP date. A valid zero or
// past date is distinct from invalid advice when choosing a fallback hint.
func ParseRetryAfter(value string) (time.Duration, bool) {
	if !httpguts.ValidHeaderFieldValue(value) {
		return 0, false
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	digits := true
	for _, c := range value {
		if c < '0' || c > '9' {
			digits = false
			break
		}
	}
	if digits {
		seconds, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return 0, false
		}
		if seconds > math.MaxInt64/int64(time.Second) {
			return time.Duration(math.MaxInt64), true
		}
		return time.Duration(seconds) * time.Second, true
	}
	if at, err := http.ParseTime(value); err == nil {
		return max(time.Until(at), 0), true
	}
	return 0, false
}
