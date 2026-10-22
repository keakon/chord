package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/keakon/chord/internal/recovery"
)

// sessionSyncBarrierBudget bounds the directory durability barrier that pause
// and takeover run before another process may read the session. The barrier
// covers only the recovery read set and normally completes in milliseconds;
// the budget turns a stalled filesystem into a retryable failure instead of an
// unbounded handoff stall.
const sessionSyncBarrierBudget = 5 * time.Second

func syncSessionDurabilityBarrier(manager *recovery.RecoveryManager) error {
	ctx, cancel := context.WithTimeout(context.Background(), sessionSyncBarrierBudget)
	defer cancel()
	if err := manager.Sync(ctx); err != nil {
		return fmt.Errorf("session durability barrier (budget %v): %w", sessionSyncBarrierBudget, err)
	}
	return nil
}
