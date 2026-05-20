package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

type RootStartupPlan struct {
	RunSetupWizard  bool
	PrepareWorktree bool
	WorktreeName    string
	PprofListenAddr string
	SessionOptions  sessionStartupOptions
}

// ErrInvalidPprofPort signals that CHORD_PPROF_PORT could not be parsed. The
// returned plan is still complete; pprof is just disabled. Callers use
// errors.Is to log this case as a warning and proceed.
var ErrInvalidPprofPort = errors.New("invalid CHORD_PPROF_PORT")

func planRootStartup(cmd *cobra.Command, continueSession bool, resumeSession string, worktreeName string) (RootStartupPlan, error) {
	resumeID := strings.TrimSpace(resumeSession)
	if continueSession && resumeID != "" {
		return RootStartupPlan{}, fmt.Errorf("--continue and --resume are mutually exclusive")
	}

	plan := RootStartupPlan{
		RunSetupWizard: cmd != nil && cmd.Parent() == nil,
		SessionOptions: sessionStartupOptions{
			ContinueLatest: continueSession,
			ResumeID:       resumeID,
			NewSessionMeta: flagWorktreeStartupMeta,
		},
	}
	if cmd != nil && cmd.Flags().Changed("worktree") && flagWorktreeStartupInfo == nil {
		plan.PrepareWorktree = true
		plan.WorktreeName = worktreeName
	}

	addr, pprofErr := resolvePprofListenAddr()
	plan.PprofListenAddr = addr
	if pprofErr != nil {
		return plan, fmt.Errorf("%w: %v", ErrInvalidPprofPort, pprofErr)
	}
	return plan, nil
}
