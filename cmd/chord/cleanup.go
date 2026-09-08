package main

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/keakon/chord/internal/bytefmt"
	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/maintenance"
)

func newCleanupCmd() *cobra.Command {
	var olderThan time.Duration
	var yes bool
	cmd := &cobra.Command{Use: "cleanup", Short: "Inspect or clean Chord state/cache/logs managed by the path locator"}
	statusCmd := &cobra.Command{Use: "status", Short: "Show state/cache/log sizes", RunE: func(cmd *cobra.Command, _ []string) error {
		locator, err := config.DefaultPathLocator()
		if err != nil {
			return err
		}
		st, err := maintenance.BuildStatus(locator)
		if err != nil {
			return err
		}
		writeCleanupStatus(cmd.OutOrStdout(), st)
		return nil
	}}
	runCleanup := func(cmd *cobra.Command, kind string) error {
		locator, err := config.DefaultPathLocator()
		if err != nil {
			return err
		}
		opts := maintenance.CleanupOptions{ProjectRoot: ".", OlderThan: olderThan, Yes: yes}
		var res *maintenance.CleanupResult
		switch kind {
		case "sessions":
			res, err = maintenance.CleanupSessions(locator, opts)
		case "cache":
			res, err = maintenance.CleanupCache(locator, opts)
		case "logs":
			res, err = maintenance.CleanupLogs(locator, opts)
		case "project":
			res, err = maintenance.CleanupProject(locator, opts)
		default:
			err = fmt.Errorf("unknown cleanup kind %s", kind)
		}
		if err != nil {
			return err
		}
		verb := "would remove"
		if yes {
			verb = "removed"
		}
		out := cmd.OutOrStdout()
		for _, c := range res.Candidates {
			writeCleanupCandidate(out, verb, c)
		}
		removed := res.Deleted
		if !yes {
			removed = nil
			for _, c := range res.Candidates {
				if c.Skip == "" {
					removed = append(removed, c)
				}
			}
		}
		writeCleanupSummary(out, verb, kind, removed)
		if !yes {
			fmt.Fprintln(out, "dry-run: pass --yes to delete")
		}
		return nil
	}
	for _, kind := range []string{"sessions", "cache", "logs", "project"} {
		k := kind
		sub := &cobra.Command{Use: k, Short: "Clean " + k + " under managed Chord paths", RunE: func(cmd *cobra.Command, _ []string) error { return runCleanup(cmd, k) }}
		sub.Flags().DurationVar(&olderThan, "older-than", 0, "only clean entries older than this duration (for example 720h)")
		sub.Flags().BoolVar(&yes, "yes", false, "actually delete; default is dry-run")
		cmd.AddCommand(sub)
	}
	cmd.AddCommand(statusCmd)
	return cmd
}

func writeCleanupStatus(w io.Writer, st *maintenance.Status) {
	fmt.Fprintf(w, "state_dir: %s (%s)\ncache_dir: %s (%s)\nlogs_dir: %s (%s)\nsessions: %d across %d projects\n", st.StateDir, bytefmt.Short(st.StateBytes), st.CacheDir, bytefmt.Short(st.CacheBytes), st.LogsDir, bytefmt.Short(st.LogsBytes), st.SessionCount, st.ProjectCount)
	for _, warning := range st.Warnings {
		fmt.Fprintf(w, "warning: %s\n", warning)
	}
}

func writeCleanupCandidate(w io.Writer, verb string, candidate maintenance.CleanupCandidate) {
	if candidate.Skip != "" {
		fmt.Fprintf(w, "skip %s: %s\n", candidate.Path, candidate.Skip)
		return
	}
	fmt.Fprintf(w, "%s %s (%s)\n", verb, candidate.Path, bytefmt.Short(candidate.Bytes))
}

// writeCleanupSummary prints one aggregate line after the per-entry lines. Empty
// project dirs hold only a leftover project.json, so sessions are counted
// separately from them and the byte total is not read as belonging to the dirs.
func writeCleanupSummary(w io.Writer, verb, kind string, removed []maintenance.CleanupCandidate) {
	if len(removed) == 0 {
		return
	}
	var total int64
	for _, c := range removed {
		total += c.Bytes
	}
	what := fmt.Sprintf("%d items", len(removed))
	if kind == "sessions" {
		var sessions, emptyDirs int
		for _, c := range removed {
			switch c.Kind {
			case "session":
				sessions++
			case "empty project sessions":
				emptyDirs++
			}
		}
		parts := make([]string, 0, 2)
		if sessions > 0 {
			parts = append(parts, fmt.Sprintf("%d sessions", sessions))
		}
		if emptyDirs > 0 {
			parts = append(parts, fmt.Sprintf("%d empty project dirs", emptyDirs))
		}
		what = strings.Join(parts, ", ")
	}
	fmt.Fprintf(w, "%s %s, total %s\n", verb, what, bytefmt.Short(total))
}
