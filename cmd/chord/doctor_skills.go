package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/skill"
)

// Skill diagnostics dimensions. Integrity reports whether the runtime keeps
// the file; load reports whether the body reads back; visibility reports the
// target ruleset outcome; resources reports declared resource health.
// A successful load says nothing about whether the model will pick the skill.
const (
	doctorSkillIntegrityPassed = "passed"
	doctorSkillIntegrityFailed = "failed"

	doctorSkillLoadPassed = "passed"
	doctorSkillLoadFailed = "failed"
	doctorSkillLoadNotRun = "not_run"

	doctorSkillVisible    = "visible"
	doctorSkillDenied     = "denied"
	doctorSkillNotChecked = "not_checked"
)

const (
	doctorSkillReasonParseError        = "parse_error"
	doctorSkillReasonMissingField      = "missing_field"
	doctorSkillReasonLoadFailed        = "load_failed"
	doctorSkillReasonDeniedByRuleset   = "denied_by_ruleset"
	doctorSkillReasonShadowed          = "shadowed"
	doctorSkillReasonRulesetUnavail    = "ruleset_unavailable"
	doctorSkillReasonResourceMissing   = "resource_missing"
	doctorSkillReasonResourceEmpty     = "resource_empty"
	doctorSkillReasonResourceOutOfRoot = "resource_out_of_root"
	doctorSkillReasonResourceNotReg    = "resource_not_regular"
	doctorSkillReasonResourceInvalid   = "resource_invalid"
)

type doctorSkillsOptions struct {
	Out    io.Writer
	JSON   bool
	Strict bool
}

type doctorSkillResourceEntry struct {
	Declared    string `json:"declared"`
	Path        string `json:"path,omitempty"`
	Status      string `json:"status"`
	Reason      string `json:"reason,omitempty"`
	Detail      string `json:"detail,omitempty"`
	Placeholder bool   `json:"placeholder,omitempty"`
}

type doctorSkillEntry struct {
	Name          string                     `json:"name"`
	Path          string                     `json:"path"`
	Root          string                     `json:"root"`
	Source        string                     `json:"source"`
	Integrity     string                     `json:"integrity"`
	Load          string                     `json:"load"`
	Visibility    string                     `json:"visibility"`
	Shadowed      bool                       `json:"shadowed"`
	Resources     string                     `json:"resources"`
	Reason        string                     `json:"reason,omitempty"`
	Detail        string                     `json:"detail,omitempty"`
	ResourceItems []doctorSkillResourceEntry `json:"resource_entries,omitempty"`
}

type doctorSkillsSummary struct {
	Configured       bool `json:"configured"`
	Dirs             int  `json:"dirs"`
	Total            int  `json:"total"`
	IntegrityFailed  int  `json:"integrity_failed"`
	LoadFailed       int  `json:"load_failed"`
	NotRun           int  `json:"not_run"`
	Denied           int  `json:"denied"`
	Shadowed         int  `json:"shadowed"`
	ResourcesFailed  int  `json:"resources_failed"`
	ResourcesWarning int  `json:"resources_warning"`
}

type doctorSkillsReport struct {
	Ruleset    string              `json:"ruleset"`
	ScanIssues []string            `json:"scan_issues,omitempty"`
	Entries    []doctorSkillEntry  `json:"entries"`
	Summary    doctorSkillsSummary `json:"summary"`
}

func newDoctorSkillsCmd() *cobra.Command {
	opts := doctorSkillsOptions{}
	cmd := &cobra.Command{
		Use:   "skills",
		Short: "Diagnose skill discovery, loading, and visibility",
		Long: `Check that configured skills are discoverable, parse cleanly, load, and stay visible under the active ruleset.

The scan reuses the runtime discovery order and parser, but keeps invalid and
shadowed files as their own rows instead of skipping them silently. It also
audits the directories the runtime glob traverses, so unreadable directories
and broken symlinks become scan issues instead of looking empty. Loading and
reading a skill says nothing about whether the model will pick it.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts.Out = cmd.OutOrStdout()
			return runDoctorSkills(opts)
		},
	}
	cmd.Flags().BoolVar(&opts.JSON, "json", false, "Write a JSON report")
	cmd.Flags().BoolVar(&opts.Strict, "strict", false, "Fail when the ruleset is unavailable or a check did not run")
	return cmd
}

func runDoctorSkills(opts doctorSkillsOptions) error {
	out := opts.Out
	if out == nil {
		out = os.Stdout
	}
	cwd, err := os.Getwd()
	if err != nil {
		return cliExitError{code: 2, err: fmt.Errorf("get working directory: %w", err)}
	}
	cfg, err := config.LoadConfig()
	if err != nil {
		return cliExitError{code: 2, err: wrapConfigLoadError("load config", err)}
	}
	projectConfigPath := config.ProjectConfigPath(cwd)
	if _, mergedCfg, mergeErr := config.MergeProjectConfig(cfg, projectConfigPath); mergeErr != nil {
		return cliExitError{code: 2, err: fmt.Errorf("load project config: %w", mergeErr)}
	} else if mergedCfg != nil {
		cfg = mergedCfg
	}
	configHome, _ := config.ConfigHomeDir()
	dirs := doctorSkillDirs(cwd, configHome, cfg.Skills.Paths)
	configured := false
	for _, dir := range dirs {
		if info, statErr := os.Stat(dir); statErr == nil && info.IsDir() {
			configured = true
			break
		}
	}
	ruleset, rulesetSource, rulesetErr := buildDoctorSkillsRuleset(cwd, configHome)
	rulesetOK := rulesetErr == nil

	loader := skill.NewLoader(dirs)
	items, scanProblems := loader.ScanMetaDiagnostic()
	entries := make([]doctorSkillEntry, 0, len(items))
	for _, item := range items {
		entries = append(entries, buildDoctorSkillEntry(item, cwd, configHome, ruleset, rulesetOK, rulesetSource, rulesetErr))
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Path == entries[j].Path {
			return entries[i].Name < entries[j].Name
		}
		return entries[i].Path < entries[j].Path
	})
	report := doctorSkillsReport{Ruleset: rulesetSource, Entries: entries}
	for _, problem := range scanProblems {
		report.ScanIssues = append(report.ScanIssues, problem.String())
	}
	report.Summary = summarizeDoctorSkills(dirs, configured, entries)

	if opts.JSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			return cliExitError{code: 2, err: fmt.Errorf("write JSON report: %w", err)}
		}
	} else {
		renderDoctorSkillsText(out, report)
	}

	// Scan problems outrank integrity/load failures: an unreadable directory or
	// a dropped root means the rows above are incomplete, not merely unhappy.
	if len(report.ScanIssues) > 0 {
		return cliExitError{code: 2, err: fmt.Errorf("skill scan reported %d issue(s)", len(report.ScanIssues))}
	}
	if report.Summary.IntegrityFailed > 0 || report.Summary.LoadFailed > 0 {
		return cliExitError{code: 1, err: fmt.Errorf("%d skill(s) failed integrity or load checks", report.Summary.IntegrityFailed+report.Summary.LoadFailed)}
	}
	if opts.Strict {
		for _, entry := range entries {
			if entry.Shadowed {
				continue
			}
			if entry.Visibility == doctorSkillNotChecked || entry.Load == doctorSkillLoadNotRun {
				return cliExitError{code: 1, err: fmt.Errorf("skill %q was not fully checked (strict mode)", displayDoctorSkillName(entry))}
			}
		}
	}
	return nil
}

func displayDoctorSkillName(entry doctorSkillEntry) string {
	if strings.TrimSpace(entry.Name) != "" {
		return entry.Name
	}
	return entry.Path
}

func doctorSkillDirs(projectRoot, configHome string, extra []string) []string {
	dirs := []string{
		filepath.Join(projectRoot, ".chord", "skills"),
		filepath.Join(projectRoot, ".agents", "skills"),
	}
	if strings.TrimSpace(configHome) != "" {
		dirs = append(dirs, filepath.Join(configHome, "skills"))
	}
	for _, p := range extra {
		if strings.TrimSpace(p) == "" {
			continue
		}
		dirs = append(dirs, p)
	}
	return dirs
}

func doctorSkillSource(dir, projectRoot, configHome string) string {
	switch filepath.Clean(dir) {
	case filepath.Clean(filepath.Join(projectRoot, ".chord", "skills")):
		return "project"
	case filepath.Clean(filepath.Join(projectRoot, ".agents", "skills")):
		return "agents"
	}
	if strings.TrimSpace(configHome) != "" && filepath.Clean(dir) == filepath.Clean(filepath.Join(configHome, "skills")) {
		return "global"
	}
	return "extra"
}

// buildDoctorSkillsRuleset resolves the MainAgent (builder) ruleset without
// starting an agent runtime. Any failure degrades to visibility not_checked
// instead of failing the whole command.
func buildDoctorSkillsRuleset(projectRoot, configHome string) (permission.Ruleset, string, error) {
	agentConfigs, err := config.ResolveAgentConfigs(
		filepath.Join(projectRoot, ".chord", "agents"),
		filepath.Join(configHome, "agents"),
	)
	if err != nil {
		return nil, "unavailable", fmt.Errorf("load agent configs: %w", err)
	}
	builderCfg, ok := agentConfigs["builder"]
	if !ok || builderCfg == nil {
		return nil, "unavailable", fmt.Errorf("builder agent config not found")
	}
	return permission.ParsePermission(&builderCfg.Permission), "builder", nil
}

func buildDoctorSkillEntry(item skill.DiagnosticItem, projectRoot, configHome string, ruleset permission.Ruleset, rulesetOK bool, rulesetSource string, rulesetErr error) doctorSkillEntry {
	entry := doctorSkillEntry{
		Path:   item.Path,
		Root:   item.Root,
		Source: doctorSkillSource(item.Dir, projectRoot, configHome),
	}
	if item.Err != nil {
		entry.Integrity = doctorSkillIntegrityFailed
		entry.Load = doctorSkillLoadNotRun
		entry.Visibility = doctorSkillNotChecked
		entry.Resources = "none"
		if isDoctorSkillMissingField(item.Err) {
			entry.Reason = doctorSkillReasonMissingField
		} else {
			entry.Reason = doctorSkillReasonParseError
		}
		entry.Detail = item.Err.Error()
		return entry
	}
	entry.Name = item.Meta.Name
	entry.Integrity = doctorSkillIntegrityPassed
	entry.Shadowed = item.Shadowed
	if item.Shadowed {
		entry.Load = doctorSkillLoadPassed
		loaded, loadErr := skill.LoadSkill(item.Path)
		if loadErr != nil {
			entry.Load = doctorSkillLoadFailed
			entry.Reason = doctorSkillReasonLoadFailed
			entry.Detail = loadErr.Error()
		} else {
			entry.Resources, entry.ResourceItems = doctorSkillResourceReport(loaded.Meta.RootDir, loaded.Meta.Resources, loaded.Content)
		}
		entry.Visibility = doctorSkillNotChecked
		// Shadowed rows never reach the ruleset filter; keep the reason on the
		// shadowing fact so --strict can exempt them explicitly.
		if entry.Reason == "" {
			entry.Reason = doctorSkillReasonShadowed
			entry.Detail = "shadowed by " + item.ShadowedBy
		}
		if entry.Resources == "" {
			entry.Resources = "none"
		}
		return entry
	}
	loaded, loadErr := skill.LoadSkill(item.Path)
	if loadErr != nil {
		entry.Load = doctorSkillLoadFailed
		entry.Reason = doctorSkillReasonLoadFailed
		entry.Detail = loadErr.Error()
	} else {
		entry.Load = doctorSkillLoadPassed
	}
	if !rulesetOK {
		entry.Visibility = doctorSkillNotChecked
		if entry.Reason == "" {
			entry.Reason = doctorSkillReasonRulesetUnavail
			if rulesetErr != nil {
				entry.Detail = rulesetErr.Error()
			} else {
				entry.Detail = "ruleset unavailable for " + rulesetSource
			}
		}
	} else if !skill.VisibleUnderRuleset(item.Meta, ruleset) {
		entry.Visibility = doctorSkillDenied
		if entry.Reason == "" {
			entry.Reason = doctorSkillReasonDeniedByRuleset
			entry.Detail = fmt.Sprintf("skill %q denied by %s ruleset", item.Meta.Name, rulesetSource)
		}
	} else {
		entry.Visibility = doctorSkillVisible
	}
	var body string
	if loadErr == nil && loaded != nil {
		body = loaded.Content
	}
	resourceStatus, resourceItems := checkDoctorSkillResources(item.Meta, body, loadErr == nil)
	entry.Resources = resourceStatus
	entry.ResourceItems = resourceItems
	if entry.Reason == "" && (resourceStatus == skill.ResourceStatusFailed || resourceStatus == skill.ResourceStatusWarning) {
		for _, resource := range resourceItems {
			if (resourceStatus == skill.ResourceStatusFailed && resource.Status == skill.ResourceStatusFailed) ||
				(resourceStatus == skill.ResourceStatusWarning && resource.Status == skill.ResourceStatusWarning) {
				entry.Reason = mapSkillResourceReason(resource.Reason)
				entry.Detail = resource.Detail
				break
			}
		}
	}
	return entry
}

func isDoctorSkillMissingField(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "missing name") || strings.Contains(msg, "missing description")
}

func checkDoctorSkillResources(meta *skill.Meta, body string, bodyOK bool) (string, []doctorSkillResourceEntry) {
	if meta == nil {
		return "none", nil
	}
	declared := skill.CheckDeclaredResources(meta.RootDir, meta.Resources)
	var placeholders []skill.ResourceEntry
	if bodyOK {
		placeholders = skill.CheckPlaceholderResources(meta.RootDir, body)
	}
	combined := append(append([]skill.ResourceEntry(nil), declared...), placeholders...)
	if len(combined) == 0 {
		return "none", nil
	}
	items := make([]doctorSkillResourceEntry, 0, len(combined))
	for _, resource := range combined {
		items = append(items, doctorSkillResourceEntry{
			Declared:    resource.Declared,
			Path:        resource.Path,
			Status:      resource.Status,
			Reason:      mapSkillResourceReason(resource.Reason),
			Detail:      resource.Detail,
			Placeholder: resource.Placeholder,
		})
	}
	return skill.SummarizeResourceStatus(combined), items
}

func doctorSkillResourceReport(rootDir string, resources []string, body string) (string, []doctorSkillResourceEntry) {
	return checkDoctorSkillResources(&skill.Meta{RootDir: rootDir, Resources: resources}, body, true)
}

func mapSkillResourceReason(reason string) string {
	switch reason {
	case skill.ResourceReasonMissing:
		return doctorSkillReasonResourceMissing
	case skill.ResourceReasonEmpty:
		return doctorSkillReasonResourceEmpty
	case skill.ResourceReasonOutOfRoot:
		return doctorSkillReasonResourceOutOfRoot
	case skill.ResourceReasonNotReg:
		return doctorSkillReasonResourceNotReg
	case skill.ResourceReasonInvalid:
		return doctorSkillReasonResourceInvalid
	default:
		return reason
	}
}

func summarizeDoctorSkills(dirs []string, configured bool, entries []doctorSkillEntry) doctorSkillsSummary {
	summary := doctorSkillsSummary{Configured: configured, Dirs: len(dirs), Total: len(entries)}
	for _, entry := range entries {
		if entry.Integrity == doctorSkillIntegrityFailed {
			summary.IntegrityFailed++
		}
		if entry.Load == doctorSkillLoadFailed {
			summary.LoadFailed++
		}
		if entry.Load == doctorSkillLoadNotRun {
			summary.NotRun++
		}
		if !entry.Shadowed && entry.Visibility == doctorSkillDenied {
			summary.Denied++
		}
		if entry.Shadowed {
			summary.Shadowed++
		}
		switch entry.Resources {
		case skill.ResourceStatusFailed:
			summary.ResourcesFailed++
		case skill.ResourceStatusWarning:
			summary.ResourcesWarning++
		}
	}
	return summary
}

func renderDoctorSkillsText(out io.Writer, report doctorSkillsReport) {
	if len(report.ScanIssues) > 0 {
		fmt.Fprintf(out, "Scan issues (%d):\n", len(report.ScanIssues))
		for _, issue := range report.ScanIssues {
			fmt.Fprintf(out, "  - %s\n", issue)
		}
		fmt.Fprintln(out, "The rows below may be incomplete: the scan could not read every directory.")
	}
	if len(report.Entries) == 0 {
		if !report.Summary.Configured {
			fmt.Fprintln(out, "No skills configured.")
		} else {
			fmt.Fprintln(out, "No skills found.")
		}
		fmt.Fprintf(out, "Summary: %d skills (ruleset: %s)\n", report.Summary.Total, report.Ruleset)
		return
	}
	for _, entry := range report.Entries {
		title := displayDoctorSkillName(entry)
		fmt.Fprintf(out, "- %s\n", title)
		fmt.Fprintf(out, "    path: %s\n", entry.Path)
		fmt.Fprintf(out, "    source: %s\n", entry.Source)
		fmt.Fprintf(out, "    integrity: %s, load: %s, visibility: %s, resources: %s",
			entry.Integrity, entry.Load, entry.Visibility, entry.Resources)
		if entry.Shadowed {
			fmt.Fprint(out, ", shadowed")
		}
		fmt.Fprintln(out)
		if entry.Reason != "" {
			if entry.Detail != "" {
				fmt.Fprintf(out, "    reason: %s: %s\n", entry.Reason, entry.Detail)
			} else {
				fmt.Fprintf(out, "    reason: %s\n", entry.Reason)
			}
		}
		for _, resource := range entry.ResourceItems {
			marker := "declared"
			if resource.Placeholder {
				marker = "placeholder"
			}
			fmt.Fprintf(out, "    resource [%s] %s: %s (%s)\n", marker, resource.Declared, resource.Status, resource.Reason)
		}
	}
	fmt.Fprintf(out, "Summary: %d skills, %d integrity failures, %d load failures, %d denied, %d shadowed, %d resource failures, %d resource warnings (ruleset: %s)\n",
		report.Summary.Total, report.Summary.IntegrityFailed, report.Summary.LoadFailed,
		report.Summary.Denied, report.Summary.Shadowed,
		report.Summary.ResourcesFailed, report.Summary.ResourcesWarning, report.Ruleset)
	fmt.Fprintln(out, "Note: loading a skill says nothing about whether the model will pick it.")
}
