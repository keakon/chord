package main

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/keakon/chord/internal/externalsupervisor"
)

// newExternalSupervisorCmd is the entrypoint Chord re-executes itself with to
// run the lifecycle supervisor of one external worker. It is hidden: it is an
// internal process contract, not a command a user invokes.
//
// The supervisor talks to Chord over inherited descriptors and to nobody else:
// the managed process owns the business pipes, and this process deliberately
// has no terminal, no configuration and no output of its own.
func newExternalSupervisorCmd() *cobra.Command {
	return &cobra.Command{
		Use:          externalsupervisor.CommandName,
		Short:        "Supervise the lifecycle of one managed external agent process",
		Hidden:       true,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		// Skip the root's startup work. A supervisor needs no configuration,
		// no model catalog and no session, and running any of that would tax
		// the launch of every external worker.
		PersistentPreRunE: func(*cobra.Command, []string) error { return nil },
		Run: func(_ *cobra.Command, args []string) {
			// The exit code is the supervisor's own: zero means it proved the
			// managed process group is gone.
			os.Exit(externalsupervisor.Run(args))
		},
	}
}
