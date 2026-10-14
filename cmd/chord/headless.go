// Package main provides the chord headless subcommand for stdio JSON protocol mode.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/keakon/golog/log"
	"github.com/spf13/cobra"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/tools"
)

// newHeadlessCmd creates and returns the Cobra command for headless stdio mode.
func newHeadlessCmd() *cobra.Command {
	var (
		flagHeadlessDir      string
		flagHeadlessContinue bool
		flagHeadlessResume   string
		flagHeadlessWorktree string
	)

	cmd := &cobra.Command{
		Use:   "headless",
		Short: "Run chord without TUI (stdio JSON protocol)",
		Long: `Run chord in headless mode with stdio JSON protocol.

In headless mode, chord communicates via JSON lines over stdin/stdout,
suitable for integration with external tools or gateways.

After startup, chord emits a "ready" event. The gateway can then send a
"subscribe" command to select which event types it wants to receive.
If no subscribe command is sent, all event types are forwarded.

Examples:
  chord headless -d /path/to/project
  chord headless --continue
  chord headless --resume <session-id>
  chord headless -d /path/to/project --worktree feat-auth

Model pool control commands:
  {"type":"models","action":"status"}
  {"type":"models","action":"set_current_model_pool","pool":"thinking"}

Main role control commands:
  {"type":"role","action":"list"}
  {"type":"role","action":"set","role":"planner"}`,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if (flagGatewayInstance != "" || flagGatewayTransferID != "") && !flagGatewayManaged || flagGatewayManaged && flagGatewayInstance == "" {
				return fmt.Errorf("invalid_request: gateway-managed requires --gateway-instance")
			}
			if flagGatewayTransferID != "" && (flagHeadlessContinue || cmd.Flags().Changed("worktree")) {
				return fmt.Errorf("invalid_request: incoming transfer cannot use --continue or --worktree")
			}
			if flagGatewayTransferID != "" && flagHeadlessResume == "" {
				return fmt.Errorf("invalid_request: gateway-transfer-id requires --resume")
			}
			if flagHeadlessDir != "" {
				if err := os.Chdir(flagHeadlessDir); err != nil {
					return fmt.Errorf("change to session-dir %q: %w", flagHeadlessDir, err)
				}
			}
			if cmd.Flags().Changed("worktree") {
				wtCtx := cmd.Context()
				if wtCtx == nil {
					wtCtx = context.Background()
				}
				info, err := prepareStartupWorktree(wtCtx, flagHeadlessWorktree, flagHeadlessResetBranch)
				if err != nil {
					return err
				}
				flagWorktreeStartupInfo = info
				flagWorktreeStartupMeta = worktreeMetaForInfo(info)
				flagWorktreeStartupReason = recovery.WorktreeSwitchCreate
			}
			if flagHeadlessContinue {
				flagContinueSession = true
			}
			if flagHeadlessResume != "" {
				flagResumeSession = flagHeadlessResume
			}
			if flagContinueSession && flagResumeSession != "" {
				return fmt.Errorf("--continue and --resume are mutually exclusive")
			}
			if flagGatewayTransferID != "" {
				startup, identity, workDir, err := claimIncomingGatewayStartup(cmd.Context(), flagResumeSession, flagGatewayTransferID, flagGatewayInstance)
				if err != nil {
					return err
				}
				defer startup.SessionLock.Release()
				flagStartupPlan = &startup
				flagTakeoverExpectedIdentity = identity
				if err := os.Chdir(workDir); err != nil {
					return fmt.Errorf("restore_failed: enter saved working directory: %w", err)
				}
			}
			// A resumed session remembers the checkout it was working in.
			// Enter it before initApp so tools, the LSP root and git status
			// are anchored there; an explicit --worktree stays authoritative.
			if flagGatewayTransferID == "" && flagWorktreeStartupInfo == nil && flagResumeSession != "" {
				wtCtx := cmd.Context()
				if wtCtx == nil {
					wtCtx = context.Background()
				}
				startup, workDir, paused, err := claimPausedResumeStartup(wtCtx, flagResumeSession)
				if err != nil {
					return err
				}
				if paused {
					defer startup.SessionLock.Release()
					flagStartupPlan = &startup
					if err := os.Chdir(workDir); err != nil {
						return fmt.Errorf("restore_failed: enter saved working directory: %w", err)
					}
				} else if info := resumeSessionWorktree(wtCtx, flagResumeSession); info != nil {
					if err := os.Chdir(info.Path); err != nil {
						return fmt.Errorf("chdir to worktree %q: %w", info.Name, err)
					}
					flagWorktreeStartupInfo = info
					flagWorktreeStartupMeta = worktreeMetaForInfo(info)
					flagWorktreeStartupReason = recovery.WorktreeSwitchResume
				}
			}
			if flagWorktreeStartupInfo == nil && flagContinueSession && flagResumeSession == "" {
				// --continue opens an existing session, so the checkout to work
				// in is the one that session recorded. Resolve the plan (and the
				// session lock with it) before initApp anchors tools and the LSP
				// root at the launch directory.
				wtCtx := cmd.Context()
				if wtCtx == nil {
					wtCtx = context.Background()
				}
				startup, info, err := resolveContinueStartup(wtCtx)
				if err != nil {
					return err
				}
				flagStartupPlan = &startup
				switch {
				case startup.SessionDir == "":
					// No session could be claimed, so a fresh one is created: bind
					// it to the launch checkout like a plain startup does.
					if cwdInfo := startupWorktreeFromCwd(wtCtx); cwdInfo != nil {
						flagWorktreeStartupInfo = cwdInfo
						flagWorktreeStartupMeta = worktreeMetaForInfo(cwdInfo)
						flagWorktreeStartupReason = recovery.WorktreeSwitchStartup
					}
				case info != nil:
					if err := os.Chdir(info.Path); err != nil {
						return fmt.Errorf("chdir to worktree %q: %w", info.Name, err)
					}
					flagWorktreeStartupInfo = info
					flagWorktreeStartupMeta = worktreeMetaForInfo(info)
					flagWorktreeStartupReason = recovery.WorktreeSwitchResume
				}
			}
			if flagWorktreeStartupInfo == nil && flagResumeSession == "" && !flagContinueSession {
				// Started inside a checkout: see startupWorktreeFromCwd. The
				// session's meta is stamped from flagWorktreeStartupMeta below.
				wtCtx := cmd.Context()
				if wtCtx == nil {
					wtCtx = context.Background()
				}
				if info := startupWorktreeFromCwd(wtCtx); info != nil {
					flagWorktreeStartupInfo = info
					flagWorktreeStartupMeta = worktreeMetaForInfo(info)
					flagWorktreeStartupReason = recovery.WorktreeSwitchStartup
				}
			}
			return runHeadless(cmd, nil)
		},
	}

	cmd.Flags().BoolVar(&flagGatewayManaged, "gateway-managed", false, "Enable session takeover for a gateway-owned runtime")
	cmd.Flags().StringVar(&flagGatewayInstance, "gateway-instance", "", "Gateway binding instance")
	cmd.Flags().StringVar(&flagGatewayTransferID, "gateway-transfer-id", "", "Expected incoming transfer identity")
	cmd.Flags().StringVarP(&flagHeadlessDir, "session-dir", "d", "", "Project directory (session directory)")
	cmd.Flags().BoolVarP(&flagHeadlessContinue, "continue", "c", false, "Continue the latest session")
	cmd.Flags().StringVarP(&flagHeadlessResume, "resume", "r", "", "Resume a specific session ID")
	cmd.Flags().StringVarP(&flagHeadlessWorktree, "worktree", "w", "", "Create or enter a chord-managed git worktree by name (auto-named when empty); sessions are shared by every checkout of the repository")
	cmd.Flags().Lookup("worktree").NoOptDefVal = ""
	cmd.Flags().BoolVar(&flagHeadlessResetBranch, "reset-branch", false, "Reset an existing branch that no worktree has checked out to HEAD instead of refusing to recreate it (only with --worktree)")

	return cmd
}

// headlessRuntime is the small runtime surface required by headless stdio mode.
type headlessRuntime interface {
	Close()
	Events() <-chan agent.AgentEvent
	Backend() headlessBackend
}

type runtimeHeadlessAdapter struct {
	rt *Runtime
}

func (a runtimeHeadlessAdapter) Close() {
	if a.rt != nil {
		a.rt.Close()
	}
}

func (a runtimeHeadlessAdapter) Events() <-chan agent.AgentEvent {
	if a.rt == nil || a.rt.Agent == nil {
		ch := make(chan agent.AgentEvent)
		close(ch)
		return ch
	}
	return a.rt.Agent.Events()
}

func (a runtimeHeadlessAdapter) Backend() headlessBackend {
	if a.rt == nil {
		return nil
	}
	return a.rt.Agent
}

type headlessRunDeps struct {
	initApp             func(asyncMCP bool, mode string, sessionOpts sessionStartupOptions) (*AppContext, error)
	createRuntime       func(*AppContext) (headlessRuntime, error)
	stdin               io.Reader
	stdout              io.Writer
	watchParent         bool
	parentCheckInterval time.Duration
	getppid             func() int
}

func defaultHeadlessRunDeps() headlessRunDeps {
	return headlessRunDeps{
		initApp: initApp,
		createRuntime: func(ac *AppContext) (headlessRuntime, error) {
			rt, err := createRuntime(ac)
			if err != nil {
				return nil, err
			}
			return runtimeHeadlessAdapter{rt: rt}, nil
		},
		stdin:               os.Stdin,
		stdout:              os.Stdout,
		watchParent:         true,
		parentCheckInterval: time.Second,
		getppid:             os.Getppid,
	}
}

// runHeadless executes the headless mode main loop.
func runHeadless(_ *cobra.Command, _ []string) error {
	return runHeadlessWithDeps(defaultHeadlessRunDeps())
}

func runHeadlessWithDeps(deps headlessRunDeps) (runErr error) {
	if deps.initApp == nil {
		deps.initApp = defaultHeadlessRunDeps().initApp
	}
	if deps.createRuntime == nil {
		deps.createRuntime = defaultHeadlessRunDeps().createRuntime
	}
	if deps.stdin == nil {
		deps.stdin = os.Stdin
	}
	if deps.stdout == nil {
		deps.stdout = os.Stdout
	}
	if deps.getppid == nil {
		deps.getppid = os.Getppid
	}
	if deps.parentCheckInterval <= 0 {
		deps.parentCheckInterval = time.Second
	}

	ac, err := deps.initApp(false, "headless", sessionStartupOptions{
		ContinueLatest:   flagContinueSession,
		ResumeID:         flagResumeSession,
		ExpectedTransfer: flagTakeoverExpectedIdentity,
		NewSessionMeta:   flagWorktreeStartupMeta,
		Plan:             flagStartupPlan,
	})
	if err != nil {
		return err
	}
	if flagGatewayTransferID != "" {
		paused, ok := pausedStartupStatus(ac)
		manager := recovery.NewRecoveryManager(ac.SessionDir)
		snapshot, readErr := manager.Recover()
		if !ok || readErr != nil || snapshot.Pause == nil || !snapshot.Pause.CheckpointComplete || paused.TransferID != flagGatewayTransferID || paused.TargetKind != "gateway" || paused.TargetInstanceID != flagGatewayInstance {
			ac.Close()
			return fmt.Errorf("restore_failed: incoming paused transfer identity mismatch")
		}
	}
	if deps.watchParent {
		startHeadlessParentWatcher(ac, deps.getppid(), deps.parentCheckInterval, deps.getppid)
	}

	rt, err := deps.createRuntime(ac)
	if err != nil {
		ac.Close()
		return err
	}
	// Keep the writer alive through runtime shutdown so the event forwarder can
	// drain already-published events after the application context is cancelled.
	out := newStdoutWriter(context.Background(), deps.stdout)
	// Command replies must release the stdin loop when the application stops.
	// Event forwarding uses the writer context to drain accepted runtime events.
	out.commandCtx = ac.Ctx
	stopShutdownDeadline := context.AfterFunc(ac.Ctx, func() { out.shutdownOutputDeadline() })
	defer stopShutdownDeadline()
	go out.run()
	// Release command admission on transport failure without consuming the
	// error. Cleanup reads it after draining, including failures during close.
	disableHeadlessMCPAuthorization(ac.MainAgent)
	stopOutputCancellation := context.AfterFunc(out.ctx, ac.Cancel)
	defer stopOutputCancellation()

	var endpoint *runtimeTakeover
	var transferAdapter *headlessTransferAdapter
	transferReady := make(chan struct{})
	transferDone := make(chan error, 1)
	if flagGatewayManaged && ac.MainAgent != nil {
		transferAdapter = &headlessTransferAdapter{out: out, binding: flagGatewayInstance, ready: transferReady}
		ac.MainAgent.SetSessionPausePreparer(transferAdapter.prepare)
		var endpointErr error
		endpoint, endpointErr = startRuntimeTakeover(ac, "gateway")
		if endpointErr != nil {
			log.Warnf("session takeover unavailable error=%v", endpointErr)
		} else {
			defer endpoint.close()
		}
	}
	sessionID := filepath.Base(ac.SessionDir)
	state := &headlessState{mcp: headlessMCPSnapshot(rt.Backend()), sessionID: sessionID, workspace: ac.ContentRoot, workDir: headlessWorkDirSnapshot(rt.Backend()), updatedAt: time.Now()}
	state.mcpSession = sessionID
	if ac.MainAgent != nil {
		state.mcpRole = ac.MainAgent.CurrentRole()
	}
	if ac.ProjectLocator != nil {
		state.workspace = ac.ProjectLocator.CanonicalRoot
	}

	if endpoint != nil {
		rec := endpoint.identity()
		state.runtimeID = rec.RuntimeID
		state.sessionEpoch = rec.SessionEpoch
	}
	if queue, ok := rt.Backend().(headlessInputQueueBackend); ok {
		target := queue.InputDeliveryTarget()
		state.runtimeID, state.sessionEpoch = target.RuntimeID, target.SessionEpoch
		state.inputQueue = headlessInputQueueSnapshot(rt.Backend())
	}
	// Emit a one-time ready marker so gateways can detect successful init.
	readyPayload := map[string]any{
		"session_id":    sessionID,
		"workspace":     state.workspace,
		"workdir":       headlessWorkDirPayload(state.workDir),
		"paused_inputs": headlessPausedInputProjection(ac.MainAgent),
		"input_queue":   state.inputQueue,
		"runtime_id":    state.runtimeID, "session_epoch": state.sessionEpoch,
	}
	if endpoint != nil {
		rec := endpoint.identity()
		readyPayload["runtime_id"] = rec.RuntimeID
		readyPayload["session_epoch"] = rec.SessionEpoch
	}
	if paused, ok := pausedStartupStatus(ac); ok {
		readyPayload["state"] = recovery.SessionPauseReady
		readyPayload["transfer_id"] = paused.TransferID
		readyPayload["binding_instance_id"] = flagGatewayInstance
	}
	if len(ac.StartupSkippedLockedSessions) > 0 {
		readyPayload["skipped_locked_sessions"] = append([]string(nil), ac.StartupSkippedLockedSessions...)
	}
	if flagWorktreeStartupInfo != nil {
		readyPayload["worktree"] = map[string]any{
			"name":      flagWorktreeStartupInfo.Name,
			"branch":    flagWorktreeStartupInfo.Branch,
			"path":      flagWorktreeStartupInfo.Path,
			"repo_root": flagWorktreeStartupInfo.RepoRoot,
		}
	}
	out.emit(headlessEnvelope{
		Type:    "ready",
		Payload: readyPayload,
	})
	close(transferReady)

	// Event loop: forward filtered events to stdout. Seq allocation and the
	// enqueue share one ordered section so event pushes keep wire order
	// aligned with seq order against command-path pushes.
	backend := rt.Backend()
	events := rt.Events()
	eventDone := make(chan struct{})
	go func() {
		defer close(eventDone)
		for ev := range events {
			if _, ok := ev.(agent.SessionRestoredEvent); ok && endpoint != nil {
				rec := endpoint.refresh()
				state.mu.Lock()
				state.runtimeID = rec.RuntimeID
				state.sessionEpoch = rec.SessionEpoch
				state.mu.Unlock()
			}
			if e, ok := ev.(agent.SessionTransferEvent); ok {
				env := transferEnvelope(e.Status, flagGatewayInstance)
				if !out.emitWithContext(out.ctx, env) {
					continue
				}
				if e.Status.State == recovery.SessionPauseSaved {
					if !out.flush(out.ctx) {
						continue
					}
					err := ac.MainAgent.ReleasePausedSession(e.Status.TransferID)
					if err == nil {
						status, _ := ac.MainAgent.SessionPauseStatus(e.Status.TransferID)
						if !out.emitWithContext(out.ctx, transferEnvelope(status, flagGatewayInstance)) || !out.flush(out.ctx) {
							err = fmt.Errorf("released transfer output unavailable")
						}
					}
					transferDone <- err
				}
				continue
			}
			out.orderedWithContext(out.ctx, func() {
				envs := filterHeadlessEvent(ev, state, backend)
				for _, env := range envs {
					if !out.emitWithContext(out.ctx, env) {
						return
					}
				}
				if e, ok := ev.(agent.ToolResultEvent); ok && e.Name == tools.NameGenerateImage && state.isSubscribed(headlessImageResult) {
					emitHeadlessImageOriginals(out.ctx, e, func(env *headlessEnvelope) bool { return out.emitWithContext(out.ctx, env) })
				}
			})
		}
	}()
	defer func() {
		deadline := out.shutdownOutputDeadline()
		rt.Close()
		ac.Close()
		// Drain already-published events before closing stdout, but never hang
		// on a wedged event loop. These output waits share the remaining budget
		// after runtime cleanup rather than each adding a fresh shutdown wait.
		select {
		case <-eventDone:
		case <-time.After(max(time.Until(deadline), 0)):
			log.Warnf("headless event drain timed out")
		}
		if !out.closeUntil(time.After(max(time.Until(deadline), 0))) {
			err := errors.New("headless output writer did not stop within shutdown budget")
			log.Warnf("%v", err)
			runErr = errors.Join(runErr, err)
		}
		select {
		case err := <-out.errors():
			if err != nil {
				log.Warnf("headless output transport failed error=%v", err)
				runErr = errors.Join(runErr, err)
			}
		default:
		}
	}()

	// Command loop: read stdin JSON lines.
	stdinLines := make(chan headlessStdinLine, 16)
	go readHeadlessStdinLines(ac.Ctx, deps.stdin, stdinLines)

	for {
		select {
		case err := <-transferDone:
			if err != nil {
				return err
			}
			return cliExitError{code: takeoverExitCode}
		case <-ac.Ctx.Done():
			return nil
		case item, ok := <-stdinLines:
			if !ok {
				// stdin closed (parent/gateway exited) → exit.
				ac.Cancel()
				return nil
			}
			if item.err != nil {
				payload := map[string]string{
					"message": "stdin read error: " + item.err.Error(),
				}
				if item.code != "" {
					payload["code"] = item.code
				}
				out.emit(headlessEnvelope{Type: "error", Payload: payload})
				if item.fatal {
					ac.Cancel()
					return nil
				}
				continue
			}

			line := item.line
			if len(line) == 0 {
				continue
			}

			var hcmd headlessCommand
			if err := json.Unmarshal(line, &hcmd); err != nil {
				out.emit(headlessEnvelope{
					Type: "error",
					Payload: map[string]string{
						"message": "invalid JSON command",
					},
				})
				continue
			}
			if hcmd.Type == headlessTransferPrepared {
				var ack transferPreparedAck
				err := json.Unmarshal(line, &ack)
				if err == nil && transferAdapter != nil {
					err = transferAdapter.ack(ack)
				} else if err == nil {
					err = fmt.Errorf("wrong_owner_kind: runtime is not gateway-managed")
				}
				if err != nil {
					out.emit(headlessEnvelope{Type: "error", Payload: map[string]string{"message": err.Error()}})
				}
				continue
			}
			handleHeadlessCommand(hcmd, backend, state, out)
		}
	}
}

func startHeadlessParentWatcher(ac *AppContext, ppid0 int, interval time.Duration, getppid func() int) {
	if ac == nil || getppid == nil {
		return
	}
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ac.Ctx.Done():
				return
			case <-t.C:
				ppid := getppid()
				if ppid == 1 || (ppid0 != 0 && ppid != ppid0) {
					log.Warnf("parent process disappeared, exiting ppid0=%v ppid=%v", ppid0, ppid)
					ac.Cancel()
					return
				}
			}
		}
	}()
}
