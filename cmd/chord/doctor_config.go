package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/modelcatalog"
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

type doctorConfigCatalogReport struct {
	Version          string                      `json:"version"`
	Source           *modelcatalog.CatalogSource `json:"source,omitempty"`
	Commit           string                      `json:"commit,omitempty"`
	FromRefreshCache bool                        `json:"from_refresh_cache"`
	CacheDetail      string                      `json:"cache_detail,omitempty"`
}

type doctorConfigReport struct {
	Files   []doctorConfigFileReport  `json:"files"`
	Catalog doctorConfigCatalogReport `json:"catalog"`
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
	report.Warnings = append(report.Warnings, config.ResolvedAdvisories(rc)...)

	origin := modelcatalog.OriginInfo()
	report.Catalog = doctorConfigCatalogReport{
		Version:          origin.Version,
		Source:           origin.Source,
		Commit:           origin.Commit,
		FromRefreshCache: origin.Cached,
	}
	if status := modelcatalog.CurrentCacheStatus(); status.Detail != "" {
		report.Catalog.CacheDetail = status.Detail
	}

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
		fmt.Fprintln(out, describeDoctorCatalog(report.Catalog))
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

// describeDoctorCatalog names the snapshot the runtime resolves against and,
// when a refresh cache is present but not in effect, why it was rejected.
func describeDoctorCatalog(catalog doctorConfigCatalogReport) string {
	kind := "embedded snapshot"
	if catalog.FromRefreshCache {
		kind = "refresh cache"
	}
	description := fmt.Sprintf("model catalog: %s (%s", catalog.Version, kind)
	if catalog.Source != nil {
		description += " of " + catalog.Source.Repository
		if catalog.Source.Revision != "" {
			description += " @ " + catalog.Source.Revision
		}
	}
	if catalog.Commit != "" {
		description += ", commit " + catalog.Commit
	}
	description += ")"
	if catalog.CacheDetail != "" {
		description += "; local cache not in effect: " + catalog.CacheDetail
	}
	return description
}
