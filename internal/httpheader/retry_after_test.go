package httpheader

import (
	"math"
	"net/http"
	"testing"
	"time"
)

func TestParseRetryAfter(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  time.Duration
		valid bool
	}{
		{"120", 2 * time.Minute, true},
		{" 2 \t", 2 * time.Second, true},
		{"0", 0, true},
		{"9223372036854775807", time.Duration(math.MaxInt64), true},
		{"9223372036854775808", 0, false},
		{"-1", 0, false},
		{"+2", 0, false},
		{"1.5", 0, false},
		{"", 0, false},
		{"invalid", 0, false},
		{"2\r\n", 0, false},
	} {
		t.Run(tc.value, func(t *testing.T) {
			got, valid := ParseRetryAfter(tc.value)
			if got != tc.want || valid != tc.valid {
				t.Fatalf("ParseRetryAfter(%q) = (%v, %v), want (%v, %v)", tc.value, got, valid, tc.want, tc.valid)
			}
		})
	}
	deadline := time.Now().Add(2 * time.Minute).UTC().Truncate(time.Second)
	got, valid := ParseRetryAfter(deadline.Format(http.TimeFormat))
	if !valid || got <= 0 || got > 2*time.Minute {
		t.Fatalf("future HTTP date = (%v, %v)", got, valid)
	}
	if delay, valid := ParseRetryAfter(time.Now().Add(-time.Minute).UTC().Format(http.TimeFormat)); !valid || delay != 0 {
		t.Fatalf("past HTTP date = (%v, %v), want (0, true)", delay, valid)
	}
}
