package main

import (
	"context"
	"fmt"
	"time"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/mcp"
)

func configureRuntimeMCPAuth(ac *AppContext) {
	if ac == nil || ac.MainAgent == nil {
		return
	}
	ac.MainAgent.SetMCPRuntime(ac.MCPTargets, ac.MCPMgr, ac.MCPAuth, ac.ConfigHome)
	ac.MainAgent.SetMCPPreparationHook(func(ctx context.Context) error { return reconcileRuntimeMCPCredentials(ctx, ac) })
	if ac.MCPMgr != nil {
		ac.MainAgent.SetMCPStatusSnapshotFunc(ac.MCPMgr.Snapshot)
		ac.MCPMgr.SetOnChange(func(mcp.RuntimeSnapshot) { ac.MainAgent.RefreshMCPStatusSnapshot() })
	}
	ac.MainAgent.SetMCPAuthControlFunc(func(ctx context.Context, req agent.MCPAuthRequest, progress func(mcp.LoginProgress)) error {
		target, mgr, ok := ac.MainAgent.MCPRuntimeTarget(req.Ref)
		if !ok || ac.MCPAuth == nil {
			return fmt.Errorf("MCP target is unavailable")
		}
		// Read committed credential facts even when validation or cancellation
		// ends the operation before connection reconciliation can run.
		defer func() {
			snapshotCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
			defer cancel()
			mgr.RefreshCredentialSnapshot(snapshotCtx)
		}()
		switch req.Action {
		case agent.MCPAuthLogin, agent.MCPAuthReauthorize:
			result, err := ac.MCPAuth.Login(ctx, target, mcp.LoginOptions{Reauth: req.Action == agent.MCPAuthReauthorize}, func(p mcp.LoginProgress) {
				progress(p)
				if p.AuthorizationURL != "" {
					_ = openBrowser(p.AuthorizationURL)
				}
			})
			if result.CredentialSaved && mgr != nil {
				mgr.RetireConnection(target.Config.Name, mcp.RetireReconnect)
			}
			if err != nil {
				return err
			}
		case agent.MCPAuthLogout:
			if mgr != nil {
				mgr.RetireConnection(target.Config.Name, mcp.RetireLogout)
			}
			if _, err := ac.MCPAuth.Logout(ctx, target); err != nil {
				return err
			}
		case agent.MCPAuthReconnect:
			view, err := ac.MCPAuth.Inspect(ctx, target)
			if err != nil {
				return err
			}
			if view.Blocked != nil {
				if _, err := ac.MCPAuth.RetryBlockedRefresh(ctx, target, *view.Blocked); err != nil {
					return err
				}
			}
		default:
			return fmt.Errorf("unknown MCP authentication action")
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if ac.MainAgent.SessionDir() != req.SessionDir {
			return context.Canceled
		}
		if mgr != nil && req.Action != agent.MCPAuthLogout {
			enabled := false
			for _, server := range mgr.Snapshot().Servers {
				if server.Ref == target.Ref {
					enabled = server.DesiredEnabled
					break
				}
			}
			if enabled {
				if err := mgr.ConnectOne(ctx, target); err != nil {
					return err
				}
			}
		}
		return reconcileRuntimeMCPCredentials(ctx, ac)
	})
}
