package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/keakon/chord/internal/identity"
)

// newResumeCmd resolves a session id back to the worktree (or main repo)
// it belongs to, chdirs into the right project, and runs the TUI with
// the resume flag set. Cross-worktree complement to `chord --resume <sid>`,
// which only works when the cwd already matches the session's project.
//
// With --fork-history[=N] it first forks the session at compaction boundary N
// (default: the latest applied boundary) into a brand-new session and resumes
// that fork instead. Forking only reads the source session's archive files and
// writes a new session directory, so it also works while the source session is
// still open in another Chord process (unlike plain resume).
func newResumeCmd() *cobra.Command {
	var forkHistoryValue string
	cmd := &cobra.Command{
		Use:           "resume <session-id>",
		Short:         "Resume a session by ID, auto-locating the chord-managed worktree it belongs to",
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(c *cobra.Command, args []string) error {
			sid := args[0]
			ctx := c.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			loc, err := resolveSessionWorktree(ctx, sid)
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
			case loc.MainRepoRoot != "":
				if err := os.Chdir(loc.MainRepoRoot); err != nil {
					return fmt.Errorf("chdir to main repo %q: %w", loc.MainRepoRoot, err)
				}
			case loc.ProjectRoot != "":
				if err := os.Chdir(loc.ProjectRoot); err != nil {
					return fmt.Errorf("chdir to project %q: %w", loc.ProjectRoot, err)
				}
			default:
				return fmt.Errorf("session %q location could not be determined", sid)
			}

			if c.Flags().Changed("fork-history") {
				target, err := forkBoundaryFromFlag(forkHistoryValue)
				if err != nil {
					return err
				}
				srcDir, err := sessionDirForLocation(loc, sid)
				if err != nil {
					return err
				}
				projectSessionsDir := filepath.Dir(srcDir)
				newDir, chosen, seeded, err := forkSessionAtHistory(srcDir, projectSessionsDir, target)
				if err != nil {
					return err
				}
				sid = filepath.Base(newDir)
				fmt.Fprintf(os.Stderr, "Forked session %s at compaction boundary history-%d into new session %s (%d messages)\n", args[0], chosen, sid, seeded)
			}

			switch {
			case loc.Worktree != nil:
				fmt.Fprintf(os.Stderr, "Resuming session %s in worktree %s (%s)\n", sid, loc.Worktree.Name, loc.Worktree.Branch)
			case loc.MainRepoRoot != "":
				fmt.Fprintf(os.Stderr, "Resuming session %s in main repository (%s)\n", sid, loc.MainRepoRoot)
			case loc.ProjectRoot != "":
				fmt.Fprintf(os.Stderr, "Resuming session %s in project (%s)\n", sid, loc.ProjectRoot)
			}
			flagResumeSession = sid
			flagContinueSession = false
			return runRoot(c, nil)
		},
	}
	cmd.Flags().StringVar(&forkHistoryValue, "fork-history", "", forkHistoryFlagHelp)
	cmd.Flags().Lookup("fork-history").NoOptDefVal = "latest"
	return cmd
}

// sessionDirForLocation resolves the session directory for a sid that
// resolveSessionWorktree already located: <state>/sessions/<projectKey>/<sid>.
func sessionDirForLocation(loc *SessionLocation, sid string) (string, error) {
	if loc == nil || loc.ProjectKey == "" {
		return "", fmt.Errorf("session %q project could not be determined", sid)
	}
	pl, err := startupPathLocator()
	if err != nil {
		return "", fmt.Errorf("resolve session path: %w", err)
	}
	dir := filepath.Join(pl.SessionsRoot, loc.ProjectKey, sid)
	info, err := os.Stat(filepath.Join(dir, identity.MainSessionLogFilename))
	if err != nil || info.Size() == 0 {
		return "", fmt.Errorf("session %s not found or has no messages in project %s", sid, loc.ProjectKey)
	}
	return dir, nil
}
