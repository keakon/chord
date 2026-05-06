package main

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// newResumeCmd resolves a session id back to the worktree (or main repo)
// it belongs to, chdirs into the right project, and runs the TUI with
// the resume flag set. Cross-worktree complement to `chord -r <sid>`,
// which only works when the cwd already matches the session's project.
func newResumeCmd() *cobra.Command {
	return &cobra.Command{
		Use:           "resume <session-id>",
		Short:         "Resume a session by ID, auto-locating the chord-managed worktree it belongs to",
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(c *cobra.Command, args []string) error {
			sid := args[0]
			loc, err := resolveSessionWorktree(context.Background(), sid)
			if err != nil {
				return err
			}
			switch {
			case loc.Worktree != nil:
				if err := os.Chdir(loc.Worktree.Path); err != nil {
					return fmt.Errorf("chdir to worktree %q: %w", loc.Worktree.Name, err)
				}
				flagWorktreeStartupInfo = loc.Worktree
				flagWorktreeStartupMeta = worktreeMetaForInfo(loc.Worktree)
				fmt.Fprintf(os.Stderr, "Resuming session %s in worktree %s (%s)\n", sid, loc.Worktree.Name, loc.Worktree.Branch)
			case loc.MainRepoRoot != "":
				if err := os.Chdir(loc.MainRepoRoot); err != nil {
					return fmt.Errorf("chdir to main repo %q: %w", loc.MainRepoRoot, err)
				}
				fmt.Fprintf(os.Stderr, "Resuming session %s in main repository (%s)\n", sid, loc.MainRepoRoot)
			default:
				return fmt.Errorf("session %q location could not be determined", sid)
			}
			flagResumeSession = sid
			flagContinueSession = false
			return runRoot(c, nil)
		},
	}
}
