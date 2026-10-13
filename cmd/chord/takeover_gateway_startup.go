package main

import (
	"context"
	"fmt"

	"github.com/keakon/chord/internal/recovery"
)

// claimIncomingGatewayStartup validates the binding before app initialization,
// then takes the session lock and rechecks the complete checkpoint identity.
func claimIncomingGatewayStartup(ctx context.Context, sid, transferID, binding string) (sessionStartupPlan, recovery.TransferIdentity, string, error) {
	loc, err := locateTakeover(ctx, "", sid)
	if err != nil {
		return sessionStartupPlan{}, recovery.TransferIdentity{}, "", err
	}
	snapshot, err := recovery.NewRecoveryManager(loc.sessionDir).Recover()
	if err != nil {
		return sessionStartupPlan{}, recovery.TransferIdentity{}, "", fmt.Errorf("restore_failed: read incoming transfer: %w", err)
	}
	pause := snapshot.Pause
	if pause == nil || pause.Identity.TransferID != transferID || pause.Identity.TargetKind != "gateway" || pause.Identity.TargetInstanceID != binding {
		return sessionStartupPlan{}, recovery.TransferIdentity{}, "", fmt.Errorf("restore_failed: incoming paused transfer identity mismatch")
	}
	plan, workDir, err := claimTakeoverStartup(ctx, sessionStartupOptions{ResumeID: sid, ExpectedTransfer: pause.Identity})
	return plan, pause.Identity, workDir, err
}
