package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/keakon/chord/internal/sessionimport"
)

func newImportCmd() *cobra.Command {
	var projectRoot string
	var sid string
	var toolMode string
	var reasoningMode string
	var dryRun bool
	var jsonOut bool
	var force bool

	cmd := &cobra.Command{
		Use:           "import <source> <file>",
		Short:         "Import an external session into Chord's session storage",
		Args:          cobra.ExactArgs(2),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(_ *cobra.Command, args []string) error {
			source := strings.TrimSpace(args[0])
			input := strings.TrimSpace(args[1])
			if source == "" || input == "" {
				return fmt.Errorf("source and file are required")
			}

			res, err := sessionimport.Import(
				context.Background(),
				sessionimport.ImportOptions{
					Source:        source,
					InputPath:     input,
					ProjectRoot:   projectRoot,
					SessionID:     sid,
					ToolMode:      toolMode,
					ReasoningMode: reasoningMode,
					DryRun:        dryRun,
					JSONOutput:    jsonOut,
					Force:         force,
				},
			)
			if err != nil {
				return err
			}

			if jsonOut {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(res)
			}

			if dryRun {
				fmt.Fprintf(os.Stdout, "Dry-run import (%s)\nProject: %s\nMessages: %d\n", source, res.ProjectRoot, res.Messages)
				if len(res.Report.Warnings) > 0 {
					fmt.Fprintln(os.Stdout, "Warnings:")
					for _, w := range res.Report.Warnings {
						fmt.Fprintf(os.Stdout, "- %s\n", w)
					}
				}
				return nil
			}

			fmt.Fprintf(os.Stdout, "Imported %s session\nPWD:  %s\nSID:  %s\nPath: %s\nResume: chord resume %s\nMessages: %d\n", source, res.ProjectRoot, res.SessionID, res.SessionDir, res.SessionID, res.Messages)
			if len(res.Report.Warnings) > 0 {
				fmt.Fprintln(os.Stdout, "Warnings:")
				for _, w := range res.Report.Warnings {
					fmt.Fprintf(os.Stdout, "- %s\n", w)
				}
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&projectRoot, "project", ".", "write into which Chord project (default: current directory)")
	cmd.Flags().StringVar(&sid, "sid", "", "specify the Chord session id (default: auto-generated)")
	cmd.Flags().StringVar(&toolMode, "tool-mode", "", "tool import mode: auto|text|structured")
	cmd.Flags().StringVar(&reasoningMode, "reasoning", "", "reasoning import mode: off|visible|strict")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "parse and report only; do not write session")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "output machine-readable JSON summary")
	cmd.Flags().BoolVar(&force, "force", false, "allow overwriting an existing session id")

	return cmd
}
