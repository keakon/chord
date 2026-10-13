package main

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/takeover"
)

var flagTakeoverSession string
var flagTakeoverTimeout time.Duration

func newTakeoverCmd() *cobra.Command {
	parent := &cobra.Command{Use: "takeover", Short: "Transfer a local session after its current work finishes"}
	for _, action := range []string{takeover.ActionPreflight, takeover.ActionStart, takeover.ActionStatus, "retry-save"} {
		var sid, transferID, targetKind, targetInstance, dir string
		var jsonOutput bool
		var timeout time.Duration
		var blockerOffset int
		cmd := &cobra.Command{Use: action, SilenceUsage: true, Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
			err := validateTakeoverCLIArguments(action, sid, transferID, targetKind, targetInstance)
			if err == nil && blockerOffset < 0 {
				err = &takeover.Error{Code: takeover.CodeInvalidRequest, Message: "blocker offset must be nonnegative"}
			}
			var loc takeoverLocation
			if err == nil {
				loc, err = locateTakeover(c.Context(), dir, sid)
			}
			var status agent.SessionPauseStatus
			var page *takeover.Status
			if err == nil {
				switch action {
				case "start":
					var wait *time.Duration
					if c.Flags().Changed("timeout") {
						wait = &timeout
					}
					status, err = startTakeover(c.Context(), loc, sid, transferID, targetKind, targetInstance, wait)
				case takeover.ActionPreflight:
					status, err = preflightTakeoverTarget(c.Context(), loc, sid, transferID, targetKind, targetInstance)
				case "status":
					if c.Flags().Changed("blocker-offset") {
						var value takeover.Status
						value, err = queryTakeoverPage(c.Context(), loc, sid, transferID, blockerOffset)
						status = value.AgentStatus()
						page = &value
					} else {
						status, err = queryTakeover(c.Context(), loc, sid, transferID)
					}
				case "retry-save":
					var rec takeover.Record
					rec, err = takeover.Find(loc.stateDir, sid)
					if err == nil && rec.Workspace != loc.workspace {
						err = fmt.Errorf("wrong_workspace: source belongs to another workspace")
					}
					if err == nil {
						status, err = (takeover.Client{Record: rec, StateDir: loc.stateDir}).RetrySave(c.Context(), transferID)
					}
				}
			}
			if status.TransferID == "" {
				status.TransferID = boundedTakeoverCLIIdentity(transferID)
				status.SessionID = boundedTakeoverCLIIdentity(sid)
				status.Workspace = loc.workspace
				if targetKind == takeover.OwnerTUI || targetKind == takeover.OwnerGateway {
					status.TargetKind = targetKind
				}
				status.TargetInstanceID = boundedTakeoverCLIIdentity(targetInstance)
				status.State = takeover.CodeStatusUnknown
				if action == takeover.ActionPreflight || action == takeover.ActionStart {
					status.State = agent.SessionPausePreflight
				}
			}
			if err != nil {
				status.Error = err.Error()
				status.ErrorCode = takeoverCLIErrorCode(err)
			}
			if jsonOutput {
				var encodeErr error
				if page != nil && err == nil {
					encodeErr = writeTakeoverCLIPublicStatus(c.OutOrStdout(), *page)
				} else {
					encodeErr = writeTakeoverCLIStatus(c.OutOrStdout(), status)
				}
				if encodeErr != nil {
					return encodeErr
				}
			} else {
				fmt.Fprintf(c.OutOrStdout(), "%s %s: %s (%d pending inputs, %d blockers)\n", status.SessionID, status.TransferID, status.State, status.PendingInputCount, status.BlockerCount)
			}
			return err
		}}
		cmd.Flags().StringVar(&sid, "session", "", "Session ID")
		cmd.Flags().StringVar(&transferID, "transfer-id", "", "Transfer identity")
		cmd.Flags().StringVar(&targetKind, "target-kind", "", "Target kind: tui or gateway")
		cmd.Flags().StringVar(&targetInstance, "target-instance", "", "Target runtime/binding instance")
		cmd.Flags().StringVarP(&dir, "session-dir", "d", "", "Canonical project workspace")
		cmd.Flags().DurationVar(&timeout, "timeout", 0, "Observation budget; 0 returns after admission")
		cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit one bounded JSON status")
		if action == "status" {
			cmd.Flags().IntVar(&blockerOffset, "blocker-offset", 0, "Blocker page offset from a previous status response")
		}
		parent.AddCommand(cmd)
	}
	return parent
}

func prepareRootTakeover(c *cobra.Command) error {
	if flagTakeoverSession == "" {
		return nil
	}
	if flagResumeSession != "" || flagContinueSession || flagForkHistory != "" || c.Flags().Changed("worktree") {
		return fmt.Errorf("invalid_request: --takeover cannot be combined with session or worktree startup flags")
	}
	loc, err := locateTakeover(c.Context(), "", flagTakeoverSession)
	if err != nil {
		return err
	}
	var timeout *time.Duration
	if c.Flags().Changed("takeover-timeout") {
		timeout = &flagTakeoverTimeout
	}
	transferID := newTakeoverIdentity()
	status, err := startTakeover(c.Context(), loc, flagTakeoverSession, transferID, takeover.OwnerTUI, newTakeoverIdentity(), timeout)
	if err != nil {
		fmt.Fprintf(c.ErrOrStderr(), "Transfer %s can be queried with chord takeover status --session %s --transfer-id %s --json\n", transferID, flagTakeoverSession, transferID)
		return err
	}
	if status.State != agent.SessionPauseReleased {
		fmt.Fprintf(c.ErrOrStderr(), "Transfer %s remains %s; query with chord takeover status --session %s --transfer-id %s --json\n", status.TransferID, status.State, status.SessionID, status.TransferID)
		return cliExitError{code: 0}
	}
	// Ordinary startup owns the lock and installs the pause checkpoint before
	// runtime workers start. A failed restore never falls back to a new session.
	flagResumeSession = flagTakeoverSession
	flagTakeoverExpectedIdentity = status.TransferIdentity
	return nil
}
