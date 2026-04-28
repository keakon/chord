package agent

import (
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
)

// agentSeq is a process-wide counter used to generate unique agent instance IDs.
var agentSeq atomic.Uint64

// NextInstanceID returns a unique identifier for an agent instance.
// The name is derived from the agent type (e.g. "builder-1", "explorer-2").
// It is safe to call from any goroutine.
func NextInstanceID(agentType string) string {
	return fmt.Sprintf("%s-%d", agentType, agentSeq.Add(1))
}

// AdvancePastID parses the numeric suffix from an instance ID (e.g. "builder-3")
// and advances the global agentSeq so that future calls to NextInstanceID never
// collide with restored IDs.
func AdvancePastID(id string) {
	idx := strings.LastIndex(id, "-")
	if idx < 0 {
		return
	}
	n, err := strconv.ParseUint(id[idx+1:], 10, 64)
	if err != nil {
		return
	}
	for {
		cur := agentSeq.Load()
		if cur >= n {
			return
		}
		if agentSeq.CompareAndSwap(cur, n) {
			return
		}
	}
}
