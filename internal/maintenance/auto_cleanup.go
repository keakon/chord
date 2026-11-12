package maintenance

import (
	"context"
	"fmt"
	"time"

	"github.com/keakon/chord/internal/config"
)

// AutoCleanupResult reports the outcome of one startup sweep.
type AutoCleanupResult struct {
	// Skipped is non-empty when the whole sweep was skipped, with the reason.
	Skipped  string
	Sessions *CleanupResult
	LLMDumps *CleanupResult
}

// RunAutoCleanup runs one startup sweep: expired session directories and LLM
// dump files, per the enabled policies. Only the global config reaches here —
// project configs must never enable cross-project deletion.
//
// A non-blocking exclusive lock on <state-dir>/maintenance.lock de-duplicates
// sweeps among Chord processes sharing the same state dir; when another
// process holds it, this sweep is skipped and the next startup retries. The
// lock is an optimization, not a correctness requirement: every deletion is
// idempotent and protected by the session lock protocol on its own.
//
// Cancellation is checked between candidates. The sweep never blocks shutdown;
// an interrupted sweep leaves work for the next one.
func RunAutoCleanup(ctx context.Context, locator *config.PathLocator, cfg config.MaintenanceCleanupConfig, now time.Time) (*AutoCleanupResult, error) {
	if locator == nil {
		return nil, fmt.Errorf("path locator is nil")
	}
	out := &AutoCleanupResult{}
	release, skipped, err := acquireCleanupLock(locator.StateDir)
	if err != nil {
		return nil, err
	}
	if skipped != "" {
		out.Skipped = skipped
		return out, nil
	}
	defer release()

	if cfg.Sessions.Enabled {
		sessions, err := CleanupSessions(locator, retainCleanupOptions(ctx, now, cfg.Sessions.RetainDays))
		if err != nil {
			return out, err
		}
		out.Sessions = sessions
	}
	if err := ctxErr(ctx); err != nil {
		return out, err
	}
	if cfg.LLMDumps.Enabled {
		dumps, err := CleanupLLMDumps(locator, retainCleanupOptions(ctx, now, cfg.LLMDumps.RetainDays))
		if err != nil {
			return out, err
		}
		out.LLMDumps = dumps
	}
	return out, nil
}

// retainCleanupOptions builds sweep options for one retention policy. Days are
// clamped to the validated range so an unvalidated caller cannot overflow the
// date arithmetic or reverse the cutoff's direction.
func retainCleanupOptions(ctx context.Context, now time.Time, days int) CleanupOptions {
	if days < 1 {
		days = 1
	}
	if days > config.MaxMaintenanceRetainDays {
		days = config.MaxMaintenanceRetainDays
	}
	cutoff := now.AddDate(0, 0, -days)
	return CleanupOptions{Yes: true, Now: now, Cutoff: &cutoff, Ctx: ctx}
}
