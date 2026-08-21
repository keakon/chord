package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/keakon/chord/internal/config"
)

type doctorConfigOptions struct {
	Out  io.Writer
	JSON bool
}

type doctorConfigFileReport struct {
	Path   string   `json:"path"`
	Issues []string `json:"issues,omitempty"`
	OK     bool     `json:"ok"`
}

type doctorConfigReport struct {
	Files []doctorConfigFileReport `json:"files"`
	OK    bool                     `json:"ok"`
}

func newDoctorConfigCmd() *cobra.Command {
	opts := doctorConfigOptions{}
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Validate configuration files",
		Long: `Check the global and project config.yaml files for unrecognized keys,
wrongly typed values, malformed YAML, and invalid setting values.

The running application logs such problems and starts anyway, treating the
offending value as not configured; this command surfaces them explicitly.
Any problem makes the command exit with status 2.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts.Out = cmd.OutOrStdout()
			return runDoctorConfig(opts)
		},
	}
	cmd.Flags().BoolVar(&opts.JSON, "json", false, "Write a JSON report")
	return cmd
}

func runDoctorConfig(opts doctorConfigOptions) error {
	out := opts.Out
	if out == nil {
		out = os.Stdout
	}
	report := doctorConfigReport{Files: []doctorConfigFileReport{}}

	globalPath, err := config.ConfigPath()
	if err != nil {
		return cliExitError{code: 2, err: fmt.Errorf("resolve config path: %w", err)}
	}
	if _, statErr := os.Stat(globalPath); os.IsNotExist(statErr) {
		return cliExitError{code: 2, err: initialSetupRequiredError()}
	}
	issues, err := config.CollectConfigFileIssues(globalPath, true)
	if err != nil {
		return cliExitError{code: 2, err: err}
	}
	report.Files = append(report.Files, doctorConfigFileReport{Path: globalPath, Issues: issues, OK: len(issues) == 0})

	if cwd, cwdErr := os.Getwd(); cwdErr == nil {
		projectPath := config.ProjectConfigPath(cwd)
		if _, statErr := os.Stat(projectPath); statErr == nil {
			projectIssues, err := config.CollectProjectConfigIssues(projectPath)
			if err != nil {
				return cliExitError{code: 2, err: err}
			}
			report.Files = append(report.Files, doctorConfigFileReport{Path: projectPath, Issues: projectIssues, OK: len(projectIssues) == 0})
		}
	}

	report.OK = true
	totalIssues := 0
	for _, f := range report.Files {
		if !f.OK {
			report.OK = false
			totalIssues += len(f.Issues)
		}
	}

	if opts.JSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			return cliExitError{code: 2, err: fmt.Errorf("write JSON report: %w", err)}
		}
	} else {
		for _, f := range report.Files {
			if f.OK {
				fmt.Fprintf(out, "%s: OK\n", f.Path)
			} else {
				fmt.Fprintf(out, "%s: %d problem(s):\n", f.Path, len(f.Issues))
				for _, issue := range f.Issues {
					fmt.Fprintf(out, "  - %s\n", issue)
				}
			}
		}
		if report.OK {
			fmt.Fprintln(out, "config OK")
		}
	}

	if !report.OK {
		plural := "problem"
		if totalIssues != 1 {
			plural = "problems"
		}
		return cliExitError{code: 2, err: fmt.Errorf("config has %d %s", totalIssues, plural)}
	}
	return nil
}
