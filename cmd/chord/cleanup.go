package main

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/maintenance"
)

func newCleanupCmd() *cobra.Command {
	var olderThan time.Duration
	var yes bool
	cmd := &cobra.Command{Use: "cleanup", Short: "Inspect or clean Chord state/cache/logs managed by the path locator"}
	statusCmd := &cobra.Command{Use: "status", Short: "Show state/cache/log sizes", RunE: func(*cobra.Command, []string) error {
		locator, err := config.DefaultPathLocator()
		if err != nil {
			return err
		}
		st, err := maintenance.BuildStatus(locator)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stdout, "state_dir: %s (%d bytes)\ncache_dir: %s (%d bytes)\nlogs_dir: %s (%d bytes)\nsessions: %d across %d projects\n", st.StateDir, st.StateBytes, st.CacheDir, st.CacheBytes, st.LogsDir, st.LogsBytes, st.SessionCount, st.ProjectCount)
		for _, w := range st.Warnings {
			fmt.Fprintf(os.Stdout, "warning: %s\n", w)
		}
		return nil
	}}
	runCleanup := func(kind string) error {
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
		for _, c := range res.Candidates {
			if c.Skip != "" {
				fmt.Fprintf(os.Stdout, "skip %s: %s\n", c.Path, c.Skip)
				continue
			}
			fmt.Fprintf(os.Stdout, "%s %s (%d bytes)\n", verb, c.Path, c.Bytes)
		}
		if !yes {
			fmt.Fprintln(os.Stdout, "dry-run: pass --yes to delete")
		}
		return nil
	}
	for _, kind := range []string{"sessions", "cache", "logs", "project"} {
		k := kind
		sub := &cobra.Command{Use: k, Short: "Clean " + k + " under managed Chord paths", RunE: func(*cobra.Command, []string) error { return runCleanup(k) }}
		sub.Flags().DurationVar(&olderThan, "older-than", 0, "only clean entries older than this duration (for example 720h)")
		sub.Flags().BoolVar(&yes, "yes", false, "actually delete; default is dry-run")
		cmd.AddCommand(sub)
	}
	cmd.AddCommand(statusCmd)
	return cmd
}
