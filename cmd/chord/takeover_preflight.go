package main

import (
	"context"
	"fmt"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/takeover"
)

// A preflight reply proves that this coordinator has not dispatched start. It
// also supplies Chord's workspace identity independently of the caller's cwd.
func preflightTakeoverTarget(ctx context.Context, loc takeoverLocation, sid, transferID, targetKind, targetInstance string) (agent.SessionPauseStatus, error) {
	_, status, err := inspectTakeoverTarget(ctx, loc, sid, transferID, targetKind, targetInstance)
	return status, err
}

func inspectTakeoverTarget(ctx context.Context, loc takeoverLocation, sid, transferID, targetKind, targetInstance string) (takeover.Record, agent.SessionPauseStatus, error) {
	id := recovery.TransferIdentity{TransferID: transferID, SessionID: sid, Workspace: loc.workspace, TargetKind: targetKind, TargetInstanceID: targetInstance}
	status := agent.SessionPauseStatus{TransferIdentity: id, State: agent.SessionPausePreflight}
	if err := validateTakeoverCLIArguments(takeover.ActionPreflight, sid, transferID, targetKind, targetInstance); err != nil {
		return takeover.Record{}, status, err
	}
	if err := ctx.Err(); err != nil {
		return takeover.Record{}, status, err
	}
	if err := preflightTakeover(loc); err != nil {
		return takeover.Record{}, status, err
	}
	record, err := takeover.Find(loc.stateDir, sid)
	if err != nil {
		return takeover.Record{}, status, fmt.Errorf("locate source endpoint: %w", err)
	}
	if record.Workspace != loc.workspace {
		return record, status, &takeover.Error{Code: takeover.CodeWrongWorkspace, Message: "source belongs to another workspace"}
	}
	status.SourceRuntimeID = record.RuntimeID
	status.SourceSessionEpoch = record.SessionEpoch
	if record.OwnerKind == targetKind || (record.OwnerKind != takeover.OwnerTUI && record.OwnerKind != takeover.OwnerGateway) {
		return record, status, &takeover.Error{Code: takeover.CodeWrongOwnerKind, Message: "only TUI and gateway-managed headless can transfer to each other"}
	}
	return record, status, nil
}
