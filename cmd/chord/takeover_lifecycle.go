package main

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/takeover"
)

const takeoverExitCode = takeover.SourceTransferExitCode

type runtimeTakeover struct {
	mu       sync.Mutex
	server   *takeover.Server
	record   takeover.Record
	main     *agent.MainAgent
	stateDir string
}

func startRuntimeTakeover(ac *AppContext, kind string) (*runtimeTakeover, error) {
	if ac == nil || ac.MainAgent == nil || ac.PathLocator == nil || ac.ProjectLocator == nil {
		return nil, fmt.Errorf("takeover runtime unavailable")
	}
	rt := &runtimeTakeover{main: ac.MainAgent, stateDir: ac.PathLocator.StateDir, record: takeover.Record{Protocol: takeover.ProtocolVersion, RuntimeID: ac.MainAgent.InputDeliveryTarget().RuntimeID, SessionID: filepath.Base(ac.MainAgent.SessionDir()), SessionEpoch: ac.MainAgent.SessionEpoch(), Workspace: ac.ProjectLocator.CanonicalRoot, OwnerKind: kind}}
	server, err := takeover.StartServer(takeover.ServerOptions{StateDir: rt.stateDir, Record: rt.record, Start: func(ctx context.Context, id recovery.TransferIdentity) (recovery.SessionPauseStatus, error) {
		rt.mu.Lock()
		rec := rt.record
		rt.mu.Unlock()
		if id.SourceRuntimeID != rec.RuntimeID || id.SourceSessionEpoch != rec.SessionEpoch || id.SessionID != rec.SessionID || rec.SessionID != filepath.Base(rt.main.SessionDir()) || rec.SessionEpoch != rt.main.SessionEpoch() {
			return recovery.SessionPauseStatus{}, &takeover.Error{Code: takeover.CodeStaleSource, Message: "source session changed"}
		}
		return rt.main.BeginSessionPause(ctx, agent.SessionPauseRequest{Identity: id})
	}, Status: rt.main.SessionPauseStatus, RetrySave: rt.main.RetrySessionPauseSave})
	if err != nil {
		return nil, err
	}
	rt.server = server
	rt.record = server.Record()
	ac.takeoverRuntime.Store(rt)
	return rt, nil
}

func (rt *runtimeTakeover) close() {
	if rt != nil && rt.server != nil {
		if err := rt.server.Close(); err != nil {
			log.Warnf("takeover endpoint close failed error=%v", err)
		}
	}
}
func (rt *runtimeTakeover) refresh() takeover.Record {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	sid := filepath.Base(rt.main.SessionDir())
	desired := rt.record
	if sid != desired.SessionID {
		desired.SessionID = sid
	}
	desired.SessionEpoch = rt.main.SessionEpoch()
	if desired != rt.server.Record() || desired != rt.record {
		if err := rt.server.UpdateRecord(desired); err != nil {
			log.Warnf("takeover discovery update failed error=%v", err)
			return rt.record
		}
		rt.record = rt.server.Record()
	}

	return rt.record
}
func (rt *runtimeTakeover) identity() takeover.Record {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.record
}

func pausedStartupStatus(ac *AppContext) (recovery.SessionPauseStatus, bool) {
	if ac == nil || ac.MainAgent == nil {
		return recovery.SessionPauseStatus{}, false
	}
	manager := recovery.NewRecoveryManager(ac.MainAgent.SessionDir())
	snap, err := manager.Recover()
	if err != nil || snap.Pause == nil {
		return recovery.SessionPauseStatus{}, false
	}
	status, err := ac.MainAgent.SessionPauseStatus(snap.Pause.Identity.TransferID)
	return status, err == nil && status.State == recovery.SessionPauseReady
}

func savedTakeoverStatus(ac *AppContext) (recovery.SessionPauseStatus, bool) {
	if ac == nil || ac.MainAgent == nil {
		return recovery.SessionPauseStatus{}, false
	}
	manager := recovery.NewRecoveryManager(ac.MainAgent.SessionDir())
	snap, err := manager.Recover()
	if err != nil || snap.Pause == nil {
		return recovery.SessionPauseStatus{}, false
	}
	status, err := ac.MainAgent.SessionPauseStatus(snap.Pause.Identity.TransferID)
	return status, err == nil && status.State == recovery.SessionPauseSaved
}
