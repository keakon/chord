package llm

import "time"

// HostedAdmissionError rejects a local wire attempt before any provider call.
// It must not cool credentials or be mistaken for an unknown execution result.
type HostedAdmissionError struct {
	RetryAt     time.Time
	RetryBudget bool
}

func (e *HostedAdmissionError) Error() string {
	if e.RetryBudget {
		return "hosted retry budget exhausted"
	}
	return "hosted request rate limit reached"
}
