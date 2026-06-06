package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
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
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(res)
			}

			if dryRun {
				fmt.Fprintf(cmd.OutOrStdout(), "Dry-run import (%s)\nProject: %s\nMessages: %d\n", source, res.ProjectRoot, res.Messages)
				printImportSummary(cmd.OutOrStdout(), source, res, "")
				printImportWarnings(cmd.OutOrStdout(), res.Report.Warnings, "")
				return nil
			}

			reportPath := filepath.Join(res.SessionDir, "import-report.json")
			fmt.Fprintf(cmd.OutOrStdout(), "Imported %s session\nPWD:  %s\nSID:  %s\nPath: %s\nResume: chord resume %s\nMessages: %d\n", source, res.ProjectRoot, res.SessionID, res.SessionDir, res.SessionID, res.Messages)
			printImportSummary(cmd.OutOrStdout(), source, res, reportPath)
			printImportWarnings(cmd.OutOrStdout(), res.Report.Warnings, reportPath)
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

const maxImportWarningsShown = 5

func printImportSummary(w io.Writer, source string, res *sessionimport.ImportResult, reportPath string) {
	r := res.Report
	structuredToolCalls := r.StructuredToolCalls
	structuredToolResults := r.StructuredToolResults
	if source == "claude" && r.Claude != nil {
		if r.Claude.StructuredToolCalls > structuredToolCalls {
			structuredToolCalls = r.Claude.StructuredToolCalls
		}
		if r.Claude.StructuredToolResults > structuredToolResults {
			structuredToolResults = r.Claude.StructuredToolResults
		}
	}
	fmt.Fprintf(w, "Tools: structured %d calls / %d results, downgraded %d calls / %d results\n",
		structuredToolCalls, structuredToolResults, r.UnsupportedToolCalls, r.UnsupportedToolResults)
	fmt.Fprintf(w, "Skipped: %d entries, %d status events, %d duplicates\n",
		r.SkippedEntries, r.SkippedStatusEvents, r.SkippedDuplicates+r.DuplicateSourceConflicts)
	if source == "claude" && r.Claude != nil {
		printClaudeImportSummary(w, r.Claude)
	}
	if reportPath != "" {
		fmt.Fprintf(w, "Report: %s\n", reportPath)
	}
}

func printClaudeImportSummary(w io.Writer, report *sessionimport.ClaudeImportReport) {
	fmt.Fprintf(w, "Claude: selected %d/%d main messages, skipped %d sidechain messages, %d compact boundaries, %d tombstones\n",
		report.SelectedSpanLength, report.NonSidechainMessages, report.SidechainMessagesSkipped, report.CompactBoundaries, report.Tombstones)
	fmt.Fprintf(w, "Claude selection: %s", report.SelectionReason)
	if report.TerminalCandidates > 0 {
		fmt.Fprintf(w, ", candidates=%d", report.TerminalCandidates)
	}
	if report.DowngradedVisibleEntries > 0 {
		fmt.Fprintf(w, ", downgraded_visible=%d", report.DowngradedVisibleEntries)
	}
	fmt.Fprintln(w)
	if report.SidechainMessagesSkipped > 0 {
		fmt.Fprintf(w, "Detected %d sub-agent messages (not imported in current version).\n", report.SidechainMessagesSkipped)
	}
}

func printImportWarnings(w io.Writer, warnings []string, reportPath string) {
	if len(warnings) == 0 {
		return
	}
	fmt.Fprintln(w, "Warnings:")
	shown := min(len(warnings), maxImportWarningsShown)
	for _, warning := range warnings[:shown] {
		fmt.Fprintf(w, "- %s\n", warning)
	}
	if remaining := len(warnings) - shown; remaining > 0 {
		if strings.TrimSpace(reportPath) != "" {
			fmt.Fprintf(w, "- ... %d more warnings omitted; see %s\n", remaining, reportPath)
		} else {
			fmt.Fprintf(w, "- ... %d more warnings omitted; run without --dry-run to write the full import-report.json\n", remaining)
		}
	}
}
