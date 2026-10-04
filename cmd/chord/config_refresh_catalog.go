package main

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/modelcatalog/refresh"
)

func newConfigRefreshCatalogCmd() *cobra.Command {
	var repo string
	cmd := &cobra.Command{
		Use:   "refresh-catalog",
		Short: "Pull the latest tagged model catalog snapshot from the upstream data repository",
		Long: `Fetch the newest version tag of the upstream model catalog repository and
store it as a local cache. A refreshed snapshot supersedes the built-in
catalog as a whole — by catalog version, never merged entry by entry — and
takes effect immediately for this command and on the next start of every
chord command.

Refresh is an explicit network operation: it never runs in the background,
and a failure anywhere (network, corrupt snapshot, incompatible schema)
leaves the previously effective catalog and the previous cache untouched.
The built-in catalog keeps working offline either way.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runConfigRefreshCatalog(cmd.Context(), cmd.OutOrStdout(), strings.TrimSpace(repo))
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "",
		"upstream model catalog repository to pull from (default "+refresh.DefaultRepository+")")
	return cmd
}

func runConfigRefreshCatalog(ctx context.Context, out io.Writer, repo string) error {
	cachePath, err := config.ModelCatalogCachePath()
	if err != nil {
		return fmt.Errorf("resolve catalog cache path: %w", err)
	}
	result, err := refresh.Run(ctx, repo, cachePath)
	if err != nil {
		return err
	}
	printCatalogRefreshResult(out, result)
	if result.Updated {
		fmt.Fprintln(out, "The refreshed catalog is in effect for this command and on the next start of every chord command.")
	}
	return nil
}

// refreshCatalogForCommand runs the explicit catalog refresh for commands
// that opt in with --refresh-catalog. Unlike the dedicated refresh-catalog
// command, a failure here does not stop the command: the caller reports it
// and continues with the catalog already in effect.
func refreshCatalogForCommand(ctx context.Context, out io.Writer) error {
	cachePath, err := config.ModelCatalogCachePath()
	if err != nil {
		return fmt.Errorf("resolve catalog cache path: %w", err)
	}
	result, err := refresh.Run(ctx, "", cachePath)
	if err != nil {
		return err
	}
	printCatalogRefreshResult(out, result)
	return nil
}

func printCatalogRefreshResult(out io.Writer, result refresh.Result) {
	if !result.Updated {
		fmt.Fprintf(out, "Catalog already up to date: version %s is in effect; upstream tag %s carries no newer snapshot.\n",
			result.FromVersion, result.Revision)
		return
	}
	fmt.Fprintf(out, "Catalog installed: %s -> %s (tag %s).\n", result.FromVersion, result.ToVersion, result.Revision)
	if result.CandidateCount > 0 {
		fmt.Fprintf(out, "  %d candidate entries came with the snapshot; they appear in discovery lists with their scope and sources, never as defaults.\n", result.CandidateCount)
	}
}
