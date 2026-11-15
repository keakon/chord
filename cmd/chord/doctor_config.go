package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/jsonx"
	"github.com/keakon/chord/internal/modelcatalog"
	"github.com/keakon/chord/internal/trust"
)

type doctorConfigOptions struct {
	Out            io.Writer
	JSON           bool
	Model          string
	Field          string
	QueryRequested bool
	RevokeTrust    bool
}

type doctorConfigFileReport struct {
	Path   string   `json:"path"`
	Issues []string `json:"issues,omitempty"`
	OK     bool     `json:"ok"`
}

type doctorConfigCatalogReport struct {
	Version          string                      `json:"version"`
	Source           *modelcatalog.CatalogSource `json:"source,omitzero"`
	Commit           string                      `json:"commit,omitempty"`
	FromRefreshCache bool                        `json:"from_refresh_cache"`
	CacheDetail      string                      `json:"cache_detail,omitempty"`
}

type doctorConfigTrustSource struct {
	File  string   `json:"file"`
	Items []string `json:"items,omitempty"`
}

type doctorConfigTrustReport struct {
	ContentRoot string                    `json:"content_root"`
	State       string                    `json:"state"`
	Fingerprint string                    `json:"fingerprint,omitempty"`
	GrantedAt   string                    `json:"granted_at,omitempty"`
	StorePath   string                    `json:"store_path,omitempty"`
	Sources     []doctorConfigTrustSource `json:"sources,omitempty"`
}

type doctorConfigReport struct {
	Query   *doctorConfigQuery        `json:"query,omitzero"`
	Files   []doctorConfigFileReport  `json:"files"`
	Catalog doctorConfigCatalogReport `json:"catalog"`
	// ProjectTrust describes whether this directory's project-level
	// configuration is authorized, and the sources the gate discovered.
	ProjectTrust *doctorConfigTrustReport `json:"project_trust,omitzero"`
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
receives); they are listed without changing the exit status.

The report also shows whether this directory's project-level configuration
(.chord) is authorized to apply, with the sources the trust gate discovered.
Pass --revoke-project-trust to drop the recorded authorization for the
current project.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts.Out = cmd.OutOrStdout()
			opts.QueryRequested = cmd.Flags().Changed("model") || cmd.Flags().Changed("field")
			return runDoctorConfig(cmd.Context(), opts)
		},
	}
	cmd.Flags().BoolVar(&opts.JSON, "json", false, "Write a JSON report")
	cmd.Flags().StringVar(&opts.Model, "model", "", "Explain a base model (provider/model; requires --field)")
	cmd.Flags().StringVar(&opts.Field, "field", "", "Explain reasoning.effort, thinking.type, limit.context, limit.input, or limit.output (requires --model)")
	cmd.Flags().BoolVar(&opts.RevokeTrust, "revoke-project-trust", false, "Revoke the recorded project configuration authorization for the current project and exit")
	return cmd
}

func runDoctorConfig(cmdCtx context.Context, opts doctorConfigOptions) error {
	out := opts.Out
	if out == nil {
		out = os.Stdout
	}
	if opts.RevokeTrust {
		return runDoctorConfigRevokeTrust(cmdCtx, out, opts.JSON)
	}
	report := doctorConfigReport{Files: []doctorConfigFileReport{}}
	var canonicalRef string
	if opts.QueryRequested {
		report.Query = &doctorConfigQuery{View: "disk"}
		var queryErr *doctorConfigQueryError
		canonicalRef, queryErr = validateDoctorConfigQuery(opts)
		if queryErr != nil {
			return finishDoctorConfigQueryError(out, opts.JSON, report, queryErr)
		}
	}
	failLoad := func(err error) error {
		if !opts.QueryRequested {
			return cliExitError{code: 2, err: err}
		}
		return finishDoctorConfigQueryError(out, opts.JSON, report, &doctorConfigQueryError{Code: doctorQueryLoadFailed, Message: err.Error()})
	}

	globalPath, err := config.ConfigPath()
	if err != nil {
		return failLoad(fmt.Errorf("resolve config path: %w", err))
	}
	if report.Query != nil {
		report.Query.GlobalPath = globalPath
	}
	projectPath := ""
	if cwd, cwdErr := os.Getwd(); cwdErr == nil {
		projectPath = config.ProjectConfigPath(cwd)
	}
	if report.Query != nil {
		report.Query.ProjectPath = projectPath
		if _, err := os.Stat(projectPath); err == nil {
			report.Query.ProjectExists = true
		}
		report.Files = append(report.Files, doctorConfigFileReport{Path: globalPath})
		if report.Query.ProjectExists {
			report.Files = append(report.Files, doctorConfigFileReport{Path: projectPath})
		}
	}
	if _, statErr := os.Stat(globalPath); os.IsNotExist(statErr) {
		return failLoad(initialSetupRequiredError())
	}
	var extraRefs []string
	if opts.QueryRequested {
		extraRefs = []string{canonicalRef}
	}
	rc, err := config.LoadResolvedConfig(globalPath, projectPath, extraRefs...)
	if err != nil {
		return failLoad(err)
	}
	report.Files = nil
	report.Files = append(report.Files, doctorConfigFileReport{Path: globalPath, OK: true})
	if rc.Project != nil {
		report.Files = append(report.Files, doctorConfigFileReport{Path: projectPath, OK: true})
	}
	for _, diagnostic := range rc.Diagnostics {
		if opts.QueryRequested {
			diagnostic = redactConfigDiagnostic(diagnostic)
		}
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

	// Project trust status: whether this directory's project-level
	// configuration is authorized to apply. Discovery is read-only; it never
	// applies the layer it inspects.
	if status, err := inspectProjectTrustForDoctor(cmdCtx); err != nil {
		report.Warnings = append(report.Warnings, fmt.Sprintf("project trust status unavailable: %v", err))
	} else {
		report.ProjectTrust = &doctorConfigTrustReport{
			ContentRoot: status.ContentRoot,
			State:       status.State,
			Fingerprint: status.Fingerprint,
			GrantedAt:   status.GrantedAt,
			StorePath:   status.StorePath,
		}
		for _, source := range status.Sources {
			report.ProjectTrust.Sources = append(report.ProjectTrust.Sources, doctorConfigTrustSource{File: source.File, Items: source.Items})
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
	if len(report.Errors) > 0 {
		report.OK = false
		totalIssues += len(report.Errors)
	}

	if report.Query != nil {
		provider, model := config.SplitProviderModelRef(canonicalRef)
		result, queryErr := config.ExplainModelField(rc, provider, model, config.ExplainField(opts.Field))
		if queryErr != nil {
			report.Query.Status = doctorQueryError
			report.Query.Error = &doctorConfigQueryError{Code: doctorQueryUnknownModel, Message: redactConfigDiagnostic(config.Diagnostic{Message: queryErr.Error()}).Message}
			report.OK = false
		} else {
			redactModelFieldExplanation(&result)
			report.Query.Status = doctorQueryResolved
			report.Query.Result = &result
		}
	}
	if err := writeDoctorConfigReport(out, opts.JSON, report); err != nil {
		return err
	}
	if report.Query != nil && report.Query.Error != nil {
		return cliExitError{code: 2, err: fmt.Errorf("%s", report.Query.Error.Message)}
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

func writeDoctorConfigReport(out io.Writer, asJSON bool, report doctorConfigReport) error {

	if asJSON {
		if err := jsonx.EncodeIndent(out, report, "", "  "); err != nil {
			return cliExitError{code: 2, err: fmt.Errorf("write JSON report: %w", err)}
		}
	} else {
		if report.Query != nil {
			renderDoctorConfigQuery(out, report.Query)
		}
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
		if report.ProjectTrust != nil {
			fmt.Fprintf(out, "project configuration trust: %s\n", report.ProjectTrust.State)
			if report.ProjectTrust.Fingerprint != "" {
				fmt.Fprintf(out, "  fingerprint: %s", report.ProjectTrust.Fingerprint)
				if report.ProjectTrust.GrantedAt != "" {
					fmt.Fprintf(out, " (granted %s)", report.ProjectTrust.GrantedAt)
				}
				fmt.Fprintln(out)
			}
			for _, source := range report.ProjectTrust.Sources {
				fmt.Fprintf(out, "  %s\n", source.File)
				for _, item := range source.Items {
					fmt.Fprintf(out, "    - %s\n", item)
				}
			}
		}
		if report.OK {
			fmt.Fprintln(out, "config OK")
		}
	}

	return nil
}

// inspectProjectTrustForDoctor anchors the trust inspection at the content
// root the runtime would resolve from the current directory.
func inspectProjectTrustForDoctor(cmdCtx context.Context) (*projectTrustStatus, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("resolve working directory: %w", err)
	}
	contentRoot := resolveContentRoot(cmdCtx, cwd)
	globalPath, err := config.ConfigPath()
	if err != nil {
		return nil, fmt.Errorf("resolve config path: %w", err)
	}
	return inspectProjectTrustStatus(contentRoot, cwd, globalPath)
}

// runDoctorConfigRevokeTrust drops the recorded project configuration
// authorization for the current project. It is a management action, not a
// diagnostic: it exits 0 whether or not a record existed.
func runDoctorConfigRevokeTrust(cmdCtx context.Context, out io.Writer, asJSON bool) error {
	cwd, err := os.Getwd()
	if err != nil {
		return cliExitError{code: 2, err: fmt.Errorf("resolve working directory: %w", err)}
	}
	contentRoot := resolveContentRoot(cmdCtx, cwd)
	globalPath, err := config.ConfigPath()
	if err != nil {
		return cliExitError{code: 2, err: fmt.Errorf("resolve config path: %w", err)}
	}
	storePath, _, _, err := loadProjectTrustRecord(globalPath, contentRoot)
	if err != nil {
		return cliExitError{code: 2, err: err}
	}
	removed, err := trust.Revoke(storePath, canonicalProjectTrustRoot(contentRoot))
	if err != nil {
		return cliExitError{code: 2, err: fmt.Errorf("revoke project trust: %w", err)}
	}
	if asJSON {
		result := struct {
			ContentRoot string `json:"content_root"`
			Revoked     bool   `json:"revoked"`
		}{ContentRoot: contentRoot, Revoked: removed}
		if err := jsonx.Encode(out, result); err != nil {
			return cliExitError{code: 2, err: fmt.Errorf("write JSON report: %w", err)}
		}
		return nil
	}
	if removed {
		fmt.Fprintf(out, "Revoked the project configuration authorization for %s.\n", contentRoot)
	} else {
		fmt.Fprintf(out, "No project configuration authorization is recorded for %s.\n", contentRoot)
	}
	return nil
}
