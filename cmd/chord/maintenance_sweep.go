package main

import (
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/bytefmt"
	"github.com/keakon/chord/internal/maintenance"
)

// startMaintenanceSweep launches the one-shot startup cleanup when any
// retention policy is enabled. It runs on the app context after the target
// session is locked and the log system is initialized, so the sweep can never
// touch the session this process just opened and its deletions land in real
// logs. Shutdown cancels the sweep between candidates; nothing waits for it,
// and an interrupted sweep simply leaves work for the next startup.
func startMaintenanceSweep(ac *AppContext) {
	cleanupCfg := ac.GlobalCfg.Maintenance.Cleanup
	if !cleanupCfg.Sessions.Enabled && !cleanupCfg.LLMDumps.Enabled {
		return
	}
	locator := ac.PathLocator
	ctx := ac.Ctx
	go func() {
		res, err := maintenance.RunAutoCleanup(ctx, locator, cleanupCfg, time.Now())
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Warnf("startup maintenance cleanup failed error=%v", err)
			return
		}
		if res.Skipped != "" {
			log.Debugf("startup maintenance cleanup skipped: %s", res.Skipped)
			return
		}
		logStartupCleanupResult(res)
	}()
}

func logStartupCleanupResult(res *maintenance.AutoCleanupResult) {
	sessions, dumpFiles := 0, 0
	if res.Sessions != nil {
		sessions = len(res.Sessions.Deleted)
		for _, cand := range res.Sessions.Deleted {
			log.Infof("startup cleanup removed session %s (%s)", cand.Path, bytefmt.Short(cand.Bytes))
		}
	}
	if res.LLMDumps != nil {
		dumpFiles = len(res.LLMDumps.Deleted)
		for _, cand := range res.LLMDumps.Deleted {
			log.Debugf("startup cleanup removed LLM dump %s (%s)", cand.Path, bytefmt.Short(cand.Bytes))
		}
	}
	if sessions > 0 || dumpFiles > 0 {
		log.Infof("startup maintenance cleanup done sessions=%d dump_files=%d", sessions, dumpFiles)
	}
}
