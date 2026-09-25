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
	// Warnings are computed on the effective config, with the project layer
	// merged over the global one, so they are not attributed to a single file.
	Warnings []string `json:"warnings,omitempty"`
	OK       bool     `json:"ok"`
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
	globalReport, err := checkDoctorConfigFile(globalPath, true)
	if err != nil {
		return cliExitError{code: 2, err: err}
	}
	report.Files = append(report.Files, globalReport)

	projectPath := ""
	if cwd, cwdErr := os.Getwd(); cwdErr == nil {
		projectPath = config.ProjectConfigPath(cwd)
		if _, statErr := os.Stat(projectPath); statErr == nil {
			projectReport, err := checkDoctorConfigFile(projectPath, false)
			if err != nil {
				return cliExitError{code: 2, err: err}
			}
			report.Files = append(report.Files, projectReport)
		}
	}
	report.Warnings = doctorConfigAdvisories(globalPath, projectPath)

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

// checkDoctorConfigFile collects the problems for one config file. The global config is checked with defaults; a project config is
// checked on its own, including the top-level keys the project layer rejects.
func checkDoctorConfigFile(path string, global bool) (doctorConfigFileReport, error) {
	var issues []string
	var err error
	if global {
		issues, err = config.CollectConfigFileIssues(path, true)
	} else {
		issues, err = config.CollectProjectConfigIssues(path)
	}
	if err != nil {
		return doctorConfigFileReport{}, err
	}
	return doctorConfigFileReport{Path: path, Issues: issues, OK: len(issues) == 0}, nil
}

// doctorConfigAdvisories returns the advisories for the effective config the
// runtime would start with. A config that does not load has its problems
// reported as issues already, so it yields no advisories.
func doctorConfigAdvisories(globalPath, projectPath string) []string {
	cfg, err := config.LoadConfigFromPath(globalPath)
	if err != nil {
		return nil
	}
	if _, merged, err := config.MergeProjectConfig(cfg, projectPath); err == nil {
		cfg = merged
	}
	return config.Advisories(cfg)
}
