package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/worktree"
)

// flagWorktreeStartupInfo and flagWorktreeStartupMeta are populated by
// runRoot/runHeadless after --worktree is processed (or by the worktree
// resume subcommand). They feed the headless ready event payload and
// the new-session metadata stamping inside initApp's session startup.
// Held as package-level vars to match the existing flagContinueSession /
// flagResumeSession pattern.
var (
	flagWorktreeStartupInfo *worktree.Info
	flagWorktreeStartupMeta *recovery.SessionMeta
)

// newWorktreeCmd builds the `chord worktree …` parent command and its
// list/remove/finish subcommands. In addition to management subcommands,
// `chord worktree <name>` creates or enters that chord-managed worktree
// and starts a session there; combine with `--continue` / `--resume` to
// act on the worktree's own session history.
func newWorktreeCmd() *cobra.Command {
	var continueLatest bool
	var resumeID string

	cmd := &cobra.Command{
		Use:           "worktree [name]",
		Short:         "Manage chord-owned git worktrees, or enter one and start/resume a session",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return cmd.Help()
			}
			resumeID = strings.TrimSpace(resumeID)
			if continueLatest && resumeID != "" {
				return fmt.Errorf("--continue and --resume are mutually exclusive")
			}
			return runWorktreeSessionEntry(cmd, args[0], continueLatest, resumeID, runRoot)
		},
	}
	cmd.Flags().BoolVarP(&continueLatest, "continue", "c", false, "Continue the latest non-empty session in this worktree")
	cmd.Flags().StringVarP(&resumeID, "resume", "r", "", "Resume a specific session ID in this worktree")
	cmd.AddCommand(newWorktreeListCmd(), newWorktreeRemoveCmd(), newWorktreeFinishCmd())
	return cmd
}

func runWorktreeSessionEntry(cmd *cobra.Command, name string, continueLatest bool, resumeID string, runner func(*cobra.Command, []string) error) error {
	ctx := context.Background()
	if cmd != nil && cmd.Context() != nil {
		ctx = cmd.Context()
	}
	info, err := prepareStartupWorktree(ctx, name)
	if err != nil {
		return err
	}

	prevContinue := flagContinueSession
	prevResume := flagResumeSession
	prevInfo := flagWorktreeStartupInfo
	prevMeta := flagWorktreeStartupMeta
	flagContinueSession = continueLatest
	flagResumeSession = resumeID
	flagWorktreeStartupInfo = info
	flagWorktreeStartupMeta = worktreeMetaForInfo(info)
	defer func() {
		flagContinueSession = prevContinue
		flagResumeSession = prevResume
		flagWorktreeStartupInfo = prevInfo
		flagWorktreeStartupMeta = prevMeta
	}()

	if runner == nil {
		runner = runRoot
	}
	return runner(cmd, nil)
}

// newWorktreeListCmd lists chord-managed worktrees of the current repo,
// merging on-disk git state (the source of truth) with the repo index
// (for last-used metadata).
func newWorktreeListCmd() *cobra.Command {
	return &cobra.Command{
		Use:           "list",
		Short:         "List chord-managed worktrees of the current repository",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("get working directory: %w", err)
			}
			pl, err := startupPathLocator()
			if err != nil {
				return err
			}
			branchPrefix, err := startupBranchPrefix()
			if err != nil {
				return fmt.Errorf("resolve worktree branch_prefix: %w", err)
			}
			mainRoot, err := worktree.GitMainRoot(ctx, cwd)
			if err != nil {
				return err
			}
			infos, err := worktree.List(ctx, mainRoot, branchPrefix)
			if err != nil {
				return err
			}
			repoID := worktree.RepoIDFor(mainRoot)
			idx, _ := worktree.LoadRepoIndex(pl.StateDir, repoID)
			rows := buildWorktreeListRows(ctx, infos, idx)
			sort.SliceStable(rows, func(i, j int) bool {
				return rows[i].LastUsedAt.After(rows[j].LastUsedAt)
			})
			if len(rows) == 0 {
				fmt.Fprintln(os.Stdout, "No chord-managed worktrees in this repository.")
				return nil
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tBRANCH\tPATH\tSTATUS\tLAST_USED")
			for _, r := range rows {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", r.Name, r.Branch, r.Path, r.Status, worktree.FormatRelativeTime(r.LastUsedAt))
			}
			return tw.Flush()
		},
	}
}

// worktreeListRow is the per-worktree view rendered by `worktree list`,
// merging porcelain Info with index LastUsedAt and a clean/dirty probe.
type worktreeListRow struct {
	worktree.Info
	Status     string
	LastUsedAt time.Time
}

// buildWorktreeListRows merges porcelain-derived Info with the repo
// index and computes a coarse clean/dirty status. Failures during status
// probing become "?" instead of failing the listing.
func buildWorktreeListRows(ctx context.Context, infos []worktree.Info, idx *worktree.RepoIndex) []worktreeListRow {
	rows := make([]worktreeListRow, 0, len(infos))
	for i := range infos {
		info := infos[i]
		row := worktreeListRow{Info: info, Status: "?"}
		if dirty, ok := worktree.IsDirty(ctx, info.Path); ok {
			if dirty {
				row.Status = "dirty"
			} else {
				row.Status = "clean"
			}
		}
		if idx != nil {
			if entry := idx.FindWorktree(info.Name); entry != nil {
				row.LastUsedAt = entry.LastUsedAt
			}
		}
		rows = append(rows, row)
	}
	return rows
}

// newWorktreeRemoveCmd removes a chord-managed worktree, preserving its
// branch by default to avoid losing commits that exist only there.
func newWorktreeRemoveCmd() *cobra.Command {
	var force bool
	var deleteBranch bool
	cmd := &cobra.Command{
		Use:           "remove <name>",
		Short:         "Remove a chord-managed worktree (branch is preserved by default)",
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			name := args[0]
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("get working directory: %w", err)
			}
			pl, err := startupPathLocator()
			if err != nil {
				return err
			}
			branchPrefix, err := startupBranchPrefix()
			if err != nil {
				return fmt.Errorf("resolve worktree branch_prefix: %w", err)
			}
			if err := worktree.Remove(ctx, cwd, name, worktree.RemoveOptions{Force: force, DeleteBranch: deleteBranch, BranchPrefix: branchPrefix}, pl); err != nil {
				return err
			}
			fmt.Fprintf(os.Stdout, "Removed worktree %s\n", name)
			if !force && !deleteBranch {
				fmt.Fprintln(os.Stdout, "Note: branch was kept. Pass --delete-branch (only if merged) or --force (always) to remove the branch.")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "remove even when the worktree is dirty; force-delete the branch")
	cmd.Flags().BoolVar(&deleteBranch, "delete-branch", false, "delete the worktree's branch (only if merged; pass --force to override)")
	return cmd
}

// newWorktreeFinishCmd merges the target branch into the worktree, squashes the
// finished worktree back onto the target branch as one commit, then reclaims the
// worktree and deletes its branch.
func newWorktreeFinishCmd() *cobra.Command {
	var onto string
	var check bool
	var message string
	cmd := &cobra.Command{
		Use:           "finish <name>",
		Short:         "Merge the target branch into the real worktree, squash the result back as one commit, then remove the worktree and its branch",
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			name := args[0]
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("get working directory: %w", err)
			}
			pl, err := startupPathLocator()
			if err != nil {
				return err
			}
			branchPrefix, err := startupBranchPrefix()
			if err != nil {
				return fmt.Errorf("resolve worktree branch_prefix: %w", err)
			}
			var ontoUsed string
			if onto != "" {
				ontoUsed = onto
			} else {
				// Best-effort: discover the default onto branch so the success
				// message shows where the worktree was finished into.
				if mainRoot, rerr := worktree.GitMainRoot(ctx, cwd); rerr == nil {
					if br, berr := worktree.CurrentBranch(ctx, mainRoot); berr == nil {
						ontoUsed = br
					}
				}
			}
			if err := worktree.Finish(ctx, cwd, name, worktree.FinishOptions{Onto: onto, Check: check, Message: message, BranchPrefix: branchPrefix}, pl); err != nil {
				return err
			}
			if check {
				if ontoUsed != "" {
					fmt.Fprintf(os.Stdout, "Worktree %s can merge %s cleanly and finish cleanly\n", name, ontoUsed)
				} else {
					fmt.Fprintf(os.Stdout, "Worktree %s can merge its target branch cleanly and finish cleanly\n", name)
				}
				return nil
			}
			if ontoUsed != "" {
				fmt.Fprintf(os.Stdout, "Finished worktree %s into %s\n", name, ontoUsed)
			} else {
				fmt.Fprintf(os.Stdout, "Finished worktree %s\n", name)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&onto, "onto", "", "target branch to merge into the worktree and squash back onto (default: main worktree's current branch)")
	cmd.Flags().BoolVar(&check, "check", false, "preview whether the target branch can merge cleanly into the worktree in a temporary worktree; a real finish may leave the real worktree in a merge state if conflicts must be resolved")
	cmd.Flags().StringVarP(&message, "message", "m", "", "override the generated squash commit message")
	return cmd
}
