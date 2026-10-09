package main

import (
	"github.com/spf13/cobra"
)

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage model configuration",
	}
	cmd.AddCommand(newConfigAddCmd(), newConfigAdviseCmd(), newConfigRefreshCatalogCmd())
	return cmd
}
