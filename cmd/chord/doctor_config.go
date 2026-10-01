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
	// Errors are problems of the effective config the runtime would start
	// with, such as model pool references that do not resolve. They fail the
	// command exactly like file issues.
	Errors   []string `json:"errors,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
	OK       bool     `json:"ok"`
}

func newDoctorConfigCmd() *cobra.Command {
	opts := doctorConfigOptions{}
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Validate configuration files",
		Long: `Check the global and project config.yaml files for unrecognized keys,
wrongly typed values, malformed YAML, and invalid setting values, plus
model pool references that do not resolve against the configured
providers.

The running application ignores invalid optional values and reports them at
startup. Structural errors block the action using the affected model or pool.
Any problem makes the command exit with status 2.

Warnings flag settings that load as written but are unlikely to behave as
intended (for example a thinking block a chat-completions gateway never
receives); they are listed without changing the exit status.`,
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
	projectPath := ""
	if cwd, cwdErr := os.Getwd(); cwdErr == nil {
		projectPath = config.ProjectConfigPath(cwd)
	}
	rc, err := config.LoadResolvedConfig(globalPath, projectPath)
	if err != nil {
		return cliExitError{code: 2, err: err}
	}
	report.Files = append(report.Files, doctorConfigFileReport{Path: globalPath, OK: true})
	if rc.Project != nil {
		report.Files = append(report.Files, doctorConfigFileReport{Path: projectPath, OK: true})
	}
	for _, diagnostic := range rc.Diagnostics {
		if diagnostic.Fallback != "" && diagnostic.File != "" {
			for i := range report.Files {
				if report.Files[i].Path == diagnostic.File {
					report.Files[i].Issues = append(report.Files[i].Issues, diagnostic.String())
					report.Files[i].OK = false
				}
			}
		} else if diagnostic.Severity == config.DiagnosticSeverityError {
			report.Errors = append(report.Errors, diagnostic.String())
		} else {
			report.Warnings = append(report.Warnings, diagnostic.String())
		}
	}
	report.Warnings = append(report.Warnings, config.Advisories(rc.Config)...)

	report.OK = true
	totalIssues := 0
	for _, f := range report.Files {
		if !f.OK {
			report.OK = false
			totalIssues += len(f.Issues)
		}
	}
	if len(report.Errors) > 0 {
		report.OK = false
		totalIssues += len(report.Errors)
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
		for _, problem := range report.Errors {
			fmt.Fprintf(out, "problem: %s\n", problem)
		}
		for _, warning := range report.Warnings {
			fmt.Fprintf(out, "warning: %s\n", warning)
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
