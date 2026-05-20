package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

type RootStartupPlan struct {
	ResumeID        string
	RunSetupWizard  bool
	PrepareWorktree bool
	WorktreeName    string
	PprofListenAddr string
	SessionOptions  sessionStartupOptions
}

func planRootStartup(cmd *cobra.Command, continueSession bool, resumeSession string, worktreeName string) (RootStartupPlan, error) {
	resumeID := strings.TrimSpace(resumeSession)
	if continueSession && resumeID != "" {
		return RootStartupPlan{}, fmt.Errorf("--continue and --resume are mutually exclusive")
	}

	addr, err := resolvePprofListenAddr()
	if err != nil {
		return RootStartupPlan{}, err
	}

	plan := RootStartupPlan{
		ResumeID:        resumeID,
		RunSetupWizard:  cmd != nil && cmd.Parent() == nil,
		PprofListenAddr: addr,
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
	return plan, nil
}
