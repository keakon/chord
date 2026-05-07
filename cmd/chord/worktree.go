package main

import (
	"context"
	"fmt"
	"os"
	"sort"
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
// list/remove subcommands. Runs against the cwd's git repo and the same
// PathLocator chord uses elsewhere. Creating/entering a worktree uses
// `chord --worktree [name]` (combinable with --continue/--resume), not
// a subcommand, since it is part of chord's session-startup flow.
func newWorktreeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "worktree",
		Short: "Manage chord-owned git worktrees (create/enter via `chord --worktree`)",
	}
	cmd.AddCommand(newWorktreeListCmd(), newWorktreeRemoveCmd(), newWorktreeFinishCmd())
	return cmd
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
		RunE: func(_ *cobra.Command, _ []string) error {
			ctx := context.Background()
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
		RunE: func(_ *cobra.Command, args []string) error {
			ctx := context.Background()
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

// newWorktreeFinishCmd rebases a worktree branch onto the main line,
// fast-forwards the main line to include it, then reclaims the worktree
// and deletes its branch.
func newWorktreeFinishCmd() *cobra.Command {
	var onto string
	var force bool
	cmd := &cobra.Command{
		Use:           "finish <name>",
		Short:         "Rebase a worktree back onto the main line, then remove the worktree and its branch",
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(_ *cobra.Command, args []string) error {
			ctx := context.Background()
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
			if err := worktree.Finish(ctx, cwd, name, worktree.FinishOptions{Onto: onto, Force: force, BranchPrefix: branchPrefix}, pl); err != nil {
				return err
			}
			if ontoUsed != "" {
				fmt.Fprintf(os.Stdout, "Finished worktree %s into %s\n", name, ontoUsed)
			} else {
				fmt.Fprintf(os.Stdout, "Finished worktree %s\n", name)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&onto, "onto", "", "target main branch to rebase onto and fast-forward (default: main worktree's current branch)")
	cmd.Flags().BoolVar(&force, "force", false, "relax clean-tree checks; use git rebase --autostash; force-delete branch when reclaiming")
	return cmd
}
