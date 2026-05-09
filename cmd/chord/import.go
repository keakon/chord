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
	var sourceID string
	var sourceRoot string
	var toolMode string
	var reasoningMode string
	var dryRun bool
	var jsonOut bool
	var force bool

	cmd := &cobra.Command{
		Use:           "import <source> [file]",
		Short:         "Import an external session into Chord's session storage",
		Args:          cobra.RangeArgs(1, 2),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			source := strings.TrimSpace(args[0])
			input := ""
			if len(args) > 1 {
				input = strings.TrimSpace(args[1])
			}
			if source == "" {
				return fmt.Errorf("source is required")
			}
			if input == "" && strings.TrimSpace(sourceID) == "" {
				return fmt.Errorf("file is required unless --id is provided")
			}

			res, err := sessionimport.Import(
				ctx,
				sessionimport.ImportOptions{
					Source:        source,
					InputPath:     input,
					SourceID:      sourceID,
					SourceRoot:    sourceRoot,
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
	cmd.Flags().StringVar(&sourceID, "id", "", "import by source session id (codex/claude); requires --root or default roots")
	cmd.Flags().StringVar(&sourceRoot, "root", "", "root directory for --id lookup (codex: ~/.codex/sessions, claude: ~/.claude/projects)")
	cmd.Flags().StringVar(&toolMode, "tool-mode", "", "tool import mode: auto|text|structured")
	cmd.Flags().StringVar(&reasoningMode, "reasoning", "", "reasoning import mode: off|visible|strict")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "parse and report only; do not write session")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "output machine-readable JSON summary")
	cmd.Flags().BoolVar(&force, "force", false, "allow overwriting an existing session id")

	return cmd
}
