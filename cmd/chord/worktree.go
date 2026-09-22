package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/pathutil"
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

	// flagWorktreeResetBranch lets --worktree reset a leftover branch that no
	// worktree has checked out. Without it Create refuses to recreate the
	// branch of a removed worktree, because that branch may hold the only
	// copy of its commits.
	flagWorktreeResetBranch bool
	// flagHeadlessResetBranch is the headless counterpart of
	// flagWorktreeResetBranch.
	flagHeadlessResetBranch bool
	// flagWorktreeResumeNotice carries a resume problem detected before the
	// agent exists (the recorded worktree is gone) so initApp can surface it
	// as a startup toast once the TUI is attached.
	flagWorktreeResumeNotice string
	// flagWorktreeStartupReason records how the startup checkout was chosen
	// (created or resumed). It is written to the session's worktree timeline
	// so a resumed session can tell the two apart.
	flagWorktreeStartupReason string
	// flagStartupPlan is the session startup plan resolved before initApp ran
	// (--continue resolves its session early so it can enter the checkout that
	// session recorded). initApp uses it instead of planning a second time, so
	// the session it opens is the one whose checkout is already active.
	flagStartupPlan *sessionStartupPlan
)

// newWorktreeCmd builds the `chord worktree …` parent command and its
// list/remove/finish subcommands. In addition to management subcommands,
// `chord worktree <name>` creates or enters that chord-managed worktree
// and starts a session there; combine with `--continue` / `--resume` to
// act on the worktree's own session history.
func newWorktreeCmd() *cobra.Command {
	var continueLatest bool
	var resumeID string
	var resetBranch bool

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
			return runWorktreeSessionEntry(cmd, args[0], continueLatest, resumeID, resetBranch, runRoot)
		},
	}
	cmd.Flags().BoolVarP(&continueLatest, "continue", "c", false, "Continue the latest non-empty session in this worktree")
	cmd.Flags().StringVarP(&resumeID, "resume", "r", "", "Resume a specific session ID in this worktree")
	cmd.Flags().BoolVar(&resetBranch, "reset-branch", false, "Reset an existing branch that no worktree has checked out to HEAD instead of refusing to recreate it")
	cmd.AddCommand(newWorktreeListCmd(), newWorktreeRemoveCmd(), newWorktreeFinishCmd())
	return cmd
}

func runWorktreeSessionEntry(cmd *cobra.Command, name string, continueLatest bool, resumeID string, resetBranch bool, runner func(*cobra.Command, []string) error) error {
	ctx := context.Background()
	if cmd != nil && cmd.Context() != nil {
		ctx = cmd.Context()
	}
	info, err := prepareStartupWorktree(ctx, name, resetBranch)
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
			repoID := worktree.ResolveRepoID(ctx, cwd, mainRoot)
			idx, _ := worktree.LoadRepoIndex(pl.StateDir, repoID)
			rows := buildWorktreeListRows(ctx, infos, idx)
			sort.SliceStable(rows, func(i, j int) bool {
				return rows[i].LastUsedAt.After(rows[j].LastUsedAt)
			})
			if len(rows) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No chord-managed worktrees in this repository.")
				return nil
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tBRANCH\tPATH\tSTATUS\tOWNER\tLAST_USED")
			for _, r := range rows {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", r.Name, r.Branch, r.Path, r.Status, r.Owner, worktree.FormatRelativeTime(r.LastUsedAt))
			}
			return tw.Flush()
		},
	}
}

// worktreeListRow is the per-worktree view rendered by `worktree list`,
// merging porcelain Info with index LastUsedAt and a clean/dirty probe.
type worktreeListRow struct {
	worktree.Info
	Owner      string
	Status     string
	LastUsedAt time.Time
}

// buildWorktreeListRows merges porcelain-derived Info with the repo
// index and computes a coarse clean/dirty status. Failures during status
// probing become "?" instead of failing the listing.
func buildWorktreeListRows(ctx context.Context, infos []worktree.Info, idx *worktree.RepoIndex) []worktreeListRow {
	rows := make([]worktreeListRow, 0, len(infos))
	for _, info := range infos {
		row := worktreeListRow{Info: info, Status: "?"}
		if dirty, ok := worktree.IsDirty(ctx, info.Path); ok {
			if dirty {
				row.Status = "dirty"
			} else {
				row.Status = "clean"
			}
		}
		var entry *worktree.RepoIndexWorktree
		if idx != nil {
			if e := idx.FindWorktree(info.Name); e != nil {
				row.LastUsedAt = e.LastUsedAt
				entry = e
			}
		}
		row.Owner = worktreeOwnerLabel(ctx, info.Path, entry)
		rows = append(rows, row)
	}
	return rows
}

// worktreeOwnerLabel renders the OWNER column. The worktree's own git
// directory is authoritative; the index only caches the creator for display,
// so a missing or rebuilt index still reports who made the worktree.
func worktreeOwnerLabel(ctx context.Context, path string, entry *worktree.RepoIndexWorktree) string {
	if entry != nil && (entry.OwnerSessionID != "" || entry.OwnerKind != "") {
		return formatOwnerLabel(entry.OwnerKind, entry.OwnerSessionID, entry.OwnerAgentID)
	}
	owner, err := worktree.ReadOwner(ctx, path)
	if err != nil {
		return "?"
	}
	return formatOwnerLabel(string(owner.Kind), owner.SessionID, owner.AgentID)
}

func formatOwnerLabel(kind, sessionID, agentID string) string {
	if strings.TrimSpace(kind) == "" && strings.TrimSpace(sessionID) == "" {
		return "?"
	}
	label := strings.TrimSpace(kind)
	if sessionID != "" {
		label += ":" + shortOwnerID(sessionID)
	}
	if agentID != "" {
		label += "/" + shortOwnerID(agentID)
	}
	return label
}

func shortOwnerID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

// newWorktreeSessionHolders returns the resolver the command line uses to find
// chord sessions still working in a checkout before it deletes one.
//
// A live session is recognized by its process lock, which is exclusive and
// released on exit: session metadata alone is durable and would let a crashed
// session block removal forever. For every live session two records are read,
// because either can point at the checkout: the session's own active checkout,
// and the checkouts its workers hold (a worker can enter a checkout the session
// itself never named).
//
// Anything unverifiable is reported as a holder rather than as "free",
// matching the agent-side guard: a checkout whose users cannot be determined
// is not deleted.
func newWorktreeSessionHolders(pl *config.PathLocator, contentRoot string) worktree.HoldersResolver {
	sessionsDir := ""
	unresolved := "the repository's session store could not be resolved"
	if pl != nil && strings.TrimSpace(contentRoot) != "" {
		if pj, err := pl.LocateProject(contentRoot); err == nil {
			sessionsDir = pj.ProjectSessionsDir
			unresolved = ""
		} else {
			unresolved = fmt.Sprintf("the repository's session store could not be resolved (%v)", err)
		}
	}
	return func(info *worktree.Info) []string {
		if info == nil || strings.TrimSpace(info.Path) == "" {
			return []string{"the worktree path is unknown"}
		}
		if unresolved != "" {
			return []string{fmt.Sprintf("another chord session may be open in it, and that could not be checked (%s)", unresolved)}
		}
		target, err := config.CanonicalProjectRoot(info.Path)
		if err != nil {
			target = filepath.Clean(info.Path)
		}
		entries, err := os.ReadDir(sessionsDir)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				// No sessions have ever been stored for this repository.
				return nil
			}
			return []string{fmt.Sprintf("another chord session may be open in it, and that could not be checked (%v)", err)}
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			sessionDir := filepath.Join(sessionsDir, entry.Name())
			active, err := recovery.SessionLockActive(sessionDir)
			if err != nil {
				return []string{fmt.Sprintf("session %s is recorded in this repository and it could not be verified whether it is still running (%v)", entry.Name(), err)}
			}
			if !active {
				continue
			}
			if holder := liveSessionHolders(sessionDir, entry.Name(), target); holder != "" {
				return []string{holder}
			}
		}
		return nil
	}
}

// liveSessionHolders reports why a live session holds the checkout rooted at
// target, or "" when it does not.
func liveSessionHolders(sessionDir, sessionID, target string) string {
	meta, err := recovery.LoadSessionMeta(sessionDir)
	if err != nil {
		return fmt.Sprintf("chord session %s is running and its checkout could not be read (%v)", sessionID, err)
	}
	if meta != nil && strings.TrimSpace(meta.WorktreePath) != "" {
		if checkoutInDir(meta.WorktreePath, target) {
			return fmt.Sprintf("chord session %s is still open in this checkout", sessionID)
		}
	}
	workerDirs, err := agent.NonTerminalSubAgentWorkDirs(sessionDir)
	if err != nil {
		return fmt.Sprintf("chord session %s is running and its workers' checkouts could not be read (%v)", sessionID, err)
	}
	for _, dir := range workerDirs {
		if checkoutInDir(dir, target) {
			return fmt.Sprintf("chord session %s has a worker still working in this checkout", sessionID)
		}
	}
	return ""
}

// checkoutInDir reports whether dir is the checkout root or lies below it.
// dir is canonicalized first so a recorded path and the target compare as the
// same directory when they differ only through symlinks; target is expected to
// be canonical already.
func checkoutInDir(dir, target string) bool {
	canonical, err := config.CanonicalProjectRoot(dir)
	if err != nil {
		canonical = filepath.Clean(dir)
	}
	_, ok := pathutil.RelToBase(canonical, target)
	return ok
}

// newWorktreeRemoveCmd removes a chord-managed worktree, preserving its
// branch by default to avoid losing commits that exist only there.
func newWorktreeRemoveCmd() *cobra.Command {
	var force bool
	var deleteBranch bool
	cmd := &cobra.Command{
		Use:           "remove <name>",
		Short:         "Remove a chord-managed worktree (branch and sessions are preserved)",
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
			opts := worktree.RemoveOptions{
				Force: force, DeleteBranch: deleteBranch, BranchPrefix: branchPrefix,
				// Sessions live under the content root's project key, shared by
				// every checkout, so that is where the sessions using this
				// checkout are recorded.
				Holders: newWorktreeSessionHolders(pl, resolveContentRoot(ctx, cwd)),
			}
			if err := worktree.Remove(ctx, cwd, name, opts, pl); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Removed worktree %s\n", name)
			fmt.Fprintln(cmd.OutOrStdout(), "Note: sessions and exports live under the repository's content root, shared by every checkout, so removing a worktree does not delete them.")
			if !force && !deleteBranch {
				fmt.Fprintln(cmd.OutOrStdout(), "Note: branch was kept. Pass --delete-branch (only if merged) or --force (always) to remove the branch.")
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
			if !check {
				// A real finish fast-forwards the target branch in the
				// repository's main checkout, updating its working tree (and
				// switching its branch back afterwards when it is on another
				// branch), so that checkout must not be in use meanwhile.
				target := ontoUsed
				if target == "" {
					target = "the target branch"
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "Note: a real finish fast-forwards %s in the repository's main checkout, updating its working tree (and switching its branch back when it is on another branch); don't run another session or tool there while finish runs.\n", target)
			}
			finishOpts := worktree.FinishOptions{
				Onto: onto, Check: check, Message: message, BranchPrefix: branchPrefix,
				// A real finish reclaims the checkout at the end, so it needs
				// the same in-use guard a plain removal has.
				Holders: newWorktreeSessionHolders(pl, resolveContentRoot(ctx, cwd)),
			}
			if err := worktree.Finish(ctx, cwd, name, finishOpts, pl); err != nil {
				return err
			}
			if check {
				if ontoUsed != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "Worktree %s can merge %s cleanly and finish cleanly\n", name, ontoUsed)
				} else {
					fmt.Fprintf(cmd.OutOrStdout(), "Worktree %s can merge its target branch cleanly and finish cleanly\n", name)
				}
				return nil
			}
			if ontoUsed != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "Finished worktree %s into %s\n", name, ontoUsed)
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "Finished worktree %s\n", name)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&onto, "onto", "", "target branch to merge into the worktree and squash back onto (default: main worktree's current branch)")
	cmd.Flags().BoolVar(&check, "check", false, "preview whether the target branch can merge cleanly into the worktree in a temporary worktree; a real finish may leave the real worktree in a merge state if conflicts must be resolved")
	cmd.Flags().StringVarP(&message, "message", "m", "", "override the generated squash commit message")
	return cmd
}
