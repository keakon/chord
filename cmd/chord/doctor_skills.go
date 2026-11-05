package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/skill"
	"github.com/keakon/chord/internal/toolname"
	"github.com/keakon/chord/internal/tooloutput"
	"github.com/keakon/chord/internal/tools"
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

	doctorSkillVisible = "visible"
	doctorSkillDenied  = "denied"
	// doctorSkillNotChecked marks a row whose visibility could not be
	// decided because the configured baseline was unavailable.
	doctorSkillNotChecked = "not_checked"
	// doctorSkillNotApplicable marks a row whose role does not use Chord's
	// local skill catalog at all, such as an external backend role.
	doctorSkillNotApplicable = "not_applicable"
)

const (
	doctorSkillReasonParseError        = "parse_error"
	doctorSkillReasonInvalidPaths      = "invalid_paths"
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

// Startup listing states. They describe what the model actually sees in the
// Available Skills block, not whether the skill can be loaded by name.
const (
	// doctorSkillStartupListed means the entry carries its description.
	doctorSkillStartupListed = "listed"
	// doctorSkillStartupListedTruncated means the entry carries a shortened
	// description because it did not fit the per-entry budget.
	doctorSkillStartupListedTruncated = "listed_truncated"
	// doctorSkillStartupNameOnly means the entry is only named in the
	// trailing list, without a description.
	doctorSkillStartupNameOnly = "name_only"
	// doctorSkillStartupNotListed means the entry is absent from the block.
	doctorSkillStartupNotListed = "not_listed"
)

// Reasons an entry is absent from, or reduced inside, the startup listing.
const (
	doctorSkillStartupReasonEntryLimit           = tools.SkillListingOmitEntryLimit
	doctorSkillStartupReasonByteBudget           = tools.SkillListingOmitByteBudget
	doctorSkillStartupReasonDescriptionTruncated = "description_truncated"
	doctorSkillStartupReasonPaths                = "paths_conditional"
	doctorSkillStartupReasonPathsInvalid         = "paths_invalid"
	doctorSkillStartupReasonManualOnly           = "model_invocation_disabled"
	doctorSkillStartupReasonDenied               = "denied_by_baseline"
	doctorSkillStartupReasonBaselineUnavailable  = "baseline_unavailable"
	doctorSkillStartupReasonNotApplicable        = "not_applicable"
	doctorSkillStartupReasonShadowed             = "shadowed"
	doctorSkillStartupReasonInvalid              = "invalid_metadata"
	doctorSkillStartupReasonUnknown              = "unknown"
)

// Path activation states.
const (
	doctorSkillPathsNone        = "none"
	doctorSkillPathsConditional = "conditional"
	doctorSkillPathsInvalid     = "invalid"
)

// Model invocation states.
const (
	doctorSkillModelInvocationAllowed    = "allowed"
	doctorSkillModelInvocationManualOnly = "manual_only"
)

// Per-name invocation actions under the configured baseline.
const (
	doctorSkillInvocationAllow = "allow"
	doctorSkillInvocationAsk   = "ask"
	doctorSkillInvocationDeny  = "deny"
	// doctorSkillInvocationUnknown marks a row whose invocation action could
	// not be decided because the baseline was unavailable or does not apply.
	doctorSkillInvocationUnknown = "unknown"
)

// Role scopes. The scope decides how far the report's conclusions may go.
const (
	// doctorSkillScopeMain is a main role: its baseline is the role's own
	// permission block.
	doctorSkillScopeMain = "main"
	// doctorSkillScopeWorker is a subagent role: the report only describes
	// the role's own baseline, never the parent's rules.
	doctorSkillScopeWorker = "worker"
	// doctorSkillScopeExternalBackend is a role that runs on an external
	// coding-agent process and does not use Chord's local skill catalog.
	doctorSkillScopeExternalBackend = "external_backend"
	// doctorSkillScopeUnavailable means no baseline could be resolved.
	doctorSkillScopeUnavailable = "unavailable"
)

// Where a role's configuration came from.
const (
	doctorSkillAgentSourceBuiltin     = "builtin"
	doctorSkillAgentSourceGlobal      = "global"
	doctorSkillAgentSourceProject     = "project"
	doctorSkillAgentSourceUnavailable = "unavailable"
)

// Action kinds. The thresholds behind them only order the list; they never
// change the exit code.
const (
	doctorSkillActionOverInlineBudget = "over_inline_budget"
	doctorSkillActionNameOnly         = "name_only"
	doctorSkillActionDescTruncated    = "description_truncated"
	doctorSkillActionShadowed         = "shadowed"
	doctorSkillActionNotVisible       = "not_visible"
)

// doctorSkillsMaxActions caps the number of evidence-backed items the report
// highlights. A long list of suggestions is not a checklist.
const doctorSkillsMaxActions = 5

// doctorSkillsMaxLargest caps how many of the largest skills the text report
// lists.
const doctorSkillsMaxLargest = 5

// doctorSkillsDefaultAgent is the role the report uses when --agent is not
// given.
const doctorSkillsDefaultAgent = "builder"

// errDoctorSkillUnknownRole marks a role that is not configured at all. It is
// an explicit failure rather than an unchecked row: reporting "no rules"
// for a role that does not exist would answer a different question.
var errDoctorSkillUnknownRole = errors.New("unknown agent role")

type doctorSkillsOptions struct {
	Out    io.Writer
	JSON   bool
	Strict bool
	Agent  string
}

type doctorSkillResourceEntry struct {
	Declared    string `json:"declared"`
	Path        string `json:"path,omitempty"`
	Status      string `json:"status"`
	Reason      string `json:"reason,omitempty"`
	Detail      string `json:"detail,omitempty"`
	Placeholder bool   `json:"placeholder,omitempty"`
}

// doctorSkillDescription reports the description in its original form and in
// the form the startup listing actually writes.
type doctorSkillDescription struct {
	RawBytes int `json:"raw_bytes"`
	RawChars int `json:"raw_chars"`
	// RenderedBytes and RenderedChars count the exact JSON string in the
	// listing, including quotes and escapes. Zero means no description was
	// written; nil means the listing could not be computed or does not apply.
	RenderedBytes *int `json:"rendered_bytes"`
	RenderedChars *int `json:"rendered_chars"`
	// Truncated is true only when a shortened description was written.
	Truncated *bool `json:"truncated"`
}

// doctorSkillSizes separates the three sizes the report must not mix up: the
// source file, the parsed body, and the rendered result the model would
// receive.
type doctorSkillSizes struct {
	// SourceBytes and SourceChars measure the SKILL.md file as it is on
	// disk, frontmatter included.
	SourceBytes int `json:"source_bytes"`
	SourceChars int `json:"source_chars"`
	// BodyBytes and BodyChars measure the parsed body: the frontmatter
	// removed and CRLF normalized.
	BodyBytes int `json:"body_bytes"`
	BodyChars int `json:"body_chars"`
	// RenderBytes and RenderChars measure the rendered result before any
	// truncation: the envelope, the resource warning and the expanded body.
	RenderBytes int `json:"render_bytes"`
	RenderChars int `json:"render_chars"`
	// EnvelopeBytes measures the envelope alone; WarningBytes measures the
	// resource warning block inside it.
	EnvelopeBytes int `json:"envelope_bytes"`
	WarningBytes  int `json:"warning_bytes"`
	// OverInlineBudget reports that the rendered result exceeds the inline
	// budget of a model tool result, so the runtime would truncate it.
	OverInlineBudget bool `json:"over_inline_budget"`
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

	// ShadowedBy names the SKILL.md that owns the name.
	ShadowedBy string `json:"shadowed_by,omitempty"`
	// Scope is the priority tier of the directory the skill came from.
	Scope string `json:"scope,omitempty"`
	// Priority is the 1-based position of that directory in the search path.
	Priority int `json:"priority,omitempty"`
	// Sidecar names the sidecar file that supplied the effective metadata.
	Sidecar string `json:"sidecar,omitempty"`
	// Paths holds the conditional activation patterns.
	Paths []string `json:"paths,omitempty"`
	// PathsStatus is none, conditional or invalid.
	PathsStatus string `json:"paths_status,omitempty"`
	// ModelInvocation is allowed or manual_only.
	ModelInvocation string `json:"model_invocation,omitempty"`
	// Invocation is the per-name action under the configured baseline.
	Invocation string `json:"invocation,omitempty"`
	// Startup describes what the model sees in the Available Skills block.
	Startup string `json:"startup,omitempty"`
	// StartupReason explains a reduced or missing startup entry.
	StartupReason string `json:"startup_reason,omitempty"`
	// Description reports the description sizes.
	Description doctorSkillDescription `json:"description"`
	// Sizes reports the source, body and render sizes.
	Sizes doctorSkillSizes `json:"sizes"`
}

// doctorSkillDir is one directory of the skill search path, in priority
// order.
type doctorSkillDir struct {
	Path     string `json:"path"`
	Origin   string `json:"origin"`
	Scope    string `json:"scope"`
	Priority int    `json:"priority"`
	Exists   bool   `json:"exists"`
}

// doctorSkillsContext describes the configuration baseline the report was
// produced under.
type doctorSkillsContext struct {
	ContentRoot  string `json:"content_root"`
	CheckoutRoot string `json:"checkout_root,omitempty"`
	WorkDir      string `json:"workdir"`
	ConfigHome   string `json:"config_home,omitempty"`
	// Agent is the role whose baseline the report uses.
	Agent string `json:"agent"`
	// AgentSource is builtin, global or project.
	AgentSource string `json:"agent_source"`
	// AgentScope is main, worker, external_backend or unavailable.
	AgentScope string `json:"agent_scope"`
	// AgentMode is the role's declared mode.
	AgentMode string `json:"agent_mode,omitempty"`
	// AgentNote states what the baseline does and does not include.
	AgentNote string `json:"agent_note,omitempty"`
	// AgentError carries the failure that made the baseline unavailable.
	AgentError string `json:"agent_error,omitempty"`
	// ExtraPaths lists the configured skills.paths entries.
	ExtraPaths []string `json:"extra_paths,omitempty"`
	// ConfigNote records a fallback the global config took, such as a missing
	// config.yaml being replaced by built-in defaults.
	ConfigNote string `json:"config_note,omitempty"`
	// RenderArgs is the argument set the diagnostic render used. It is empty
	// in this version: the report measures the render without arguments.
	RenderArgs string `json:"render_args"`
}

// doctorSkillsStartup describes the Available Skills block the runtime would
// write under this baseline.
type doctorSkillsStartup struct {
	// Available is false when the block could not be computed for this
	// baseline.
	Available bool `json:"available"`
	// Reason explains an unavailable block.
	Reason string `json:"reason,omitempty"`
	// HeaderBytes is the size of the block preamble.
	HeaderBytes int `json:"header_bytes"`
	// TotalBytes and TotalChars measure the rendered block.
	TotalBytes int `json:"total_bytes"`
	TotalChars int `json:"total_chars"`
	// DescriptionBudgetBytes includes the header and omission notice, but
	// excludes the additional names list. It does not bound TotalBytes.
	DescriptionBudgetBytes int `json:"description_budget_bytes"`
	// EntryLimit is the maximum number of described entries.
	EntryLimit int `json:"entry_limit"`
	// DescCharLimit is the per-entry description budget in characters.
	DescCharLimit int `json:"desc_char_limit"`
	// Shown counts entries that carry a description.
	Shown int `json:"shown"`
	// NameOnly counts entries reduced to a bare name.
	NameOnly int `json:"name_only"`
}

// doctorSkillAction is one evidence-backed item to check.
type doctorSkillAction struct {
	Kind   string `json:"kind"`
	Skill  string `json:"skill,omitempty"`
	Detail string `json:"detail"`
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
	// ScanComplete reports whether every directory was scanned without a
	// problem. It is false when the scan could not read a path.
	ScanComplete bool `json:"scan_complete"`
}

type doctorSkillsReport struct {
	Ruleset    string              `json:"ruleset"`
	ScanIssues []string            `json:"scan_issues,omitempty"`
	Entries    []doctorSkillEntry  `json:"entries"`
	Summary    doctorSkillsSummary `json:"summary"`

	Context doctorSkillsContext `json:"context"`
	Dirs    []doctorSkillDir    `json:"dirs"`
	Startup doctorSkillsStartup `json:"startup"`
	Actions []doctorSkillAction `json:"actions,omitempty"`
	// ActionTotal counts the items found, including the ones the report left
	// out to stay short.
	ActionTotal int `json:"action_total,omitempty"`
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
reading a skill says nothing about whether the model will pick it.

The report also measures what the model would receive: the startup listing
block, the description each entry keeps, and the size of a rendered load.
Those sizes are diagnostics, not a failure: a large or truncated skill does
not change the exit code.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts.Out = cmd.OutOrStdout()
			return runDoctorSkills(opts)
		},
	}
	cmd.Flags().BoolVar(&opts.JSON, "json", false, "Write a JSON report")
	cmd.Flags().BoolVar(&opts.Strict, "strict", false, "Fail when the ruleset is unavailable or a check did not run")
	cmd.Flags().StringVar(&opts.Agent, "agent", doctorSkillsDefaultAgent, "Configured agent role whose permission baseline the report uses")
	return cmd
}

// loadDoctorSkillsGlobalConfig loads the global config the skills report runs
// under. The report only consumes skills.paths from it, so a missing global
// config.yaml falls back to the built-in defaults — the same baseline a
// present-but-empty file would decode to — instead of failing the whole check.
// Any other load problem (a malformed file, an unreadable path) still fails.
func loadDoctorSkillsGlobalConfig() (*config.Config, string, error) {
	cfg, err := config.LoadConfig()
	if err == nil {
		return cfg, "", nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return config.DefaultConfig(), "global config.yaml not found; using built-in defaults", nil
	}
	return nil, "", cliExitError{code: 2, err: wrapConfigLoadError("load config", err)}
}

// doctorSkillBaseline is the permission baseline the report is evaluated
// against: one configured role's own permission rules, without session
// overlays, YOLO, or a worker parent's rules.
type doctorSkillBaseline struct {
	Role    string
	Source  string
	Mode    string
	Scope   string
	Ruleset permission.Ruleset
	// Rules is the number of permission rules the role declares.
	Rules int
	Err   error
	Note  string
}

// Available reports whether the baseline could be resolved.
func (b doctorSkillBaseline) Available() bool {
	return b.Err == nil && b.Scope != doctorSkillScopeUnavailable && b.Scope != doctorSkillScopeExternalBackend
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
	// Project configuration, agent definitions and the fallback skill
	// directories all live under the content root: inside a linked worktree
	// that is the main worktree, not the directory the command runs in.
	contentRoot := resolveContentRoot(context.Background(), cwd)
	configHome, _ := config.ConfigHomeDir()

	cfg, configNote, err := loadDoctorSkillsGlobalConfig()
	if err != nil {
		return err
	}
	projectConfigPath := config.ProjectConfigPath(contentRoot)
	if _, mergedCfg, mergeErr := config.MergeProjectConfig(cfg, projectConfigPath); mergeErr != nil {
		return cliExitError{code: 2, err: fmt.Errorf("load project config: %w", mergeErr)}
	} else if mergedCfg != nil {
		cfg = mergedCfg
	}

	role := strings.TrimSpace(opts.Agent)
	if role == "" {
		role = doctorSkillsDefaultAgent
	}
	baseline := resolveDoctorSkillBaseline(contentRoot, configHome, role)
	if errors.Is(baseline.Err, errDoctorSkillUnknownRole) {
		return cliExitError{code: 2, err: baseline.Err}
	}

	dirs := ResolveSkillDirs(SkillDirsInput{
		ContentRoot: contentRoot,
		WorkDir:     cwd,
		ConfigHome:  configHome,
		Extra:       cfg.Skills.Paths,
	})
	dirReport := make([]doctorSkillDir, 0, len(dirs))
	configured := false
	for i, dir := range dirs {
		exists := false
		if info, statErr := os.Stat(dir.Path); statErr == nil && info.IsDir() {
			exists = true
			configured = true
		}
		dirReport = append(dirReport, doctorSkillDir{
			Path:     dir.Path,
			Origin:   dir.Origin,
			Scope:    dir.Scope,
			Priority: i + 1,
			Exists:   exists,
		})
	}
	loader := skill.NewLoader(SkillDirPaths(dirs))
	items, scanProblems := loader.ScanMetaDiagnostic()

	// The catalog keeps the loader's order: the renderer budgets the first
	// entries differently from the last ones, so reordering here would
	// describe a startup block the runtime never writes.
	var catalog []*skill.Meta
	for _, item := range items {
		if item.Err != nil || item.Shadowed || item.Meta == nil {
			continue
		}
		catalog = append(catalog, item.Meta)
	}

	entries := make([]doctorSkillEntry, 0, len(items))
	for _, item := range items {
		entries = append(entries, buildDoctorSkillEntry(item, baseline, dirReport[item.DirIndex]))
	}

	startup := doctorSkillsStartup{
		HeaderBytes:            len(tools.StartupSkillListingHeader),
		DescriptionBudgetBytes: tools.SkillListingMaxTotalBytes,
		EntryLimit:             tools.SkillListingMaxEntries,
		DescCharLimit:          tools.SkillListingMaxDescCharsPerEntry,
	}
	var listing tools.SkillListing
	switch {
	case baseline.Scope == doctorSkillScopeExternalBackend:
		startup.Reason = doctorSkillStartupReasonNotApplicable
	case !baseline.Available():
		startup.Reason = doctorSkillStartupReasonBaselineUnavailable
	default:
		startup.Available = true
		listing = tools.BuildStartupSkillListingDetailed(doctorSkillListingEntries(catalog, baseline.Ruleset), tools.StartupSkillListingHeader)
		startup.TotalBytes = listing.TotalBytes
		startup.TotalChars = utf8.RuneCountInString(listing.Text)
		startup.Shown = listing.Shown
		startup.NameOnly = listing.NameOnly
	}
	applyDoctorSkillStartup(entries, startup, listing, baseline)
	// The report rows are sorted for readability, but the renderer already
	// received the loader's order, so the sort cannot change which entries
	// kept their description.
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Path == entries[j].Path {
			return entries[i].Name < entries[j].Name
		}
		return entries[i].Path < entries[j].Path
	})

	report := doctorSkillsReport{
		Ruleset: baseline.Role,
		Entries: entries,
		Context: doctorSkillsContext{
			ContentRoot:  contentRoot,
			CheckoutRoot: doctorSkillCheckoutRoot(contentRoot, cwd),
			WorkDir:      cwd,
			ConfigHome:   configHome,
			Agent:        baseline.Role,
			AgentSource:  baseline.Source,
			AgentScope:   baseline.Scope,
			AgentMode:    baseline.Mode,
			AgentNote:    baseline.Note,
			ConfigNote:   configNote,
			RenderArgs:   "",
		},
		Dirs:    dirReport,
		Startup: startup,
	}
	if baseline.Err != nil {
		// The ruleset could not be resolved, so the report does not name one.
		report.Ruleset = doctorSkillScopeUnavailable
		report.Context.AgentError = baseline.Err.Error()
	}
	if len(cfg.Skills.Paths) > 0 {
		report.Context.ExtraPaths = slices.Clone(cfg.Skills.Paths)
	}
	for _, problem := range scanProblems {
		report.ScanIssues = append(report.ScanIssues, problem.String())
	}
	report.Summary = summarizeDoctorSkills(len(dirs), configured, entries, len(report.ScanIssues))
	report.Actions, report.ActionTotal = doctorSkillActions(entries)

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

// resolveDoctorSkillBaseline resolves one configured role's own permission
// rules. It never starts an agent runtime, so the result is a static
// baseline: no session overlays, no YOLO, and no parent rules for a worker.
func resolveDoctorSkillBaseline(contentRoot, configHome, role string) doctorSkillBaseline {
	baseline := doctorSkillBaseline{Role: role, Scope: doctorSkillScopeUnavailable, Source: doctorSkillAgentSourceUnavailable}
	agentConfigs, err := config.ResolveAgentConfigs(
		filepath.Join(contentRoot, ".chord", "agents"),
		filepath.Join(configHome, "agents"),
	)
	if err != nil {
		baseline.Err = fmt.Errorf("load agent configs: %w", err)
		baseline.Note = "the role's permission rules could not be read, so no visibility conclusion was drawn"
		return baseline
	}
	agentCfg, ok := agentConfigs[role]
	if !ok || agentCfg == nil {
		baseline.Err = fmt.Errorf("%w %q; configured roles: %s", errDoctorSkillUnknownRole, role, strings.Join(doctorSkillKnownRoles(agentConfigs), ", "))
		return baseline
	}
	baseline.Source = doctorSkillAgentSource(contentRoot, configHome, role)
	baseline.Mode = strings.TrimSpace(agentCfg.Mode)
	baseline.Ruleset = permission.ParsePermission(&agentCfg.Permission)
	baseline.Rules = len(baseline.Ruleset)
	switch {
	case agentCfg.IsExternalBackend():
		baseline.Scope = doctorSkillScopeExternalBackend
		baseline.Note = "the role runs on an external backend and does not use Chord's local skill catalog"
	case agentCfg.IsSubAgent():
		baseline.Scope = doctorSkillScopeWorker
		baseline.Note = "the role's own permission rules only; the parent agent's rules and the session overlays are not included, so this is not the worker's effective runtime permission"
	default:
		baseline.Scope = doctorSkillScopeMain
		baseline.Note = "the role's own permission rules only; the session permission overlays and YOLO are not included"
	}
	// An external backend never evaluates Chord's permission rules, so the
	// "no rules means every call denies" hint would misdescribe it.
	if baseline.Rules == 0 && baseline.Scope != doctorSkillScopeExternalBackend {
		baseline.Note += "; the role declares no permission rules, so every skill call evaluates to deny while the catalog still lists the skill"
	}
	return baseline
}

// doctorSkillKnownRoles lists the configured role names in a stable order.
func doctorSkillKnownRoles(agentConfigs map[string]*config.AgentConfig) []string {
	roles := make([]string, 0, len(agentConfigs))
	for name := range agentConfigs {
		roles = append(roles, name)
	}
	sort.Strings(roles)
	return roles
}

// doctorSkillAgentSource reports where a role's configuration came from.
func doctorSkillAgentSource(contentRoot, configHome, role string) string {
	for _, ext := range []string{".yaml", ".yml", ".md"} {
		if _, err := os.Stat(filepath.Join(contentRoot, ".chord", "agents", role+ext)); err == nil {
			return doctorSkillAgentSourceProject
		}
	}
	for _, ext := range []string{".yaml", ".yml", ".md"} {
		if _, err := os.Stat(filepath.Join(configHome, "agents", role+ext)); err == nil {
			return doctorSkillAgentSourceGlobal
		}
	}
	return doctorSkillAgentSourceBuiltin
}

// doctorSkillCheckoutRoot reports the checkout the working directory belongs
// to, or empty when the content root is the checkout.
func doctorSkillCheckoutRoot(contentRoot, workDir string) string {
	roots := SkillContentRoots(contentRoot, workDir)
	if len(roots) > 1 {
		return roots[0]
	}
	return ""
}

func displayDoctorSkillName(entry doctorSkillEntry) string {
	if strings.TrimSpace(entry.Name) != "" {
		return entry.Name
	}
	return entry.Path
}

// buildDoctorSkillEntry builds one report row and the measurements behind it.
func buildDoctorSkillEntry(item skill.DiagnosticItem, baseline doctorSkillBaseline, dir doctorSkillDir) doctorSkillEntry {
	entry := doctorSkillEntry{}
	entry.Path = item.Path
	entry.Root = item.Root
	entry.Shadowed = item.Shadowed
	entry.ShadowedBy = item.ShadowedBy
	entry.Source = dir.Origin
	entry.Scope = dir.Scope
	entry.Priority = dir.Priority

	if item.Err != nil {
		entry.Integrity = doctorSkillIntegrityFailed
		entry.Load = doctorSkillLoadNotRun
		entry.Visibility = doctorSkillNotChecked
		entry.Resources = "none"
		entry.Invocation = doctorSkillInvocationUnknown
		entry.Startup = doctorSkillStartupNotListed
		entry.StartupReason = doctorSkillStartupReasonBaselineUnavailable
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
	entry.Sidecar = item.Meta.SidecarSource
	entry.Paths = slices.Clone(item.Meta.Paths)

	descriptor := skill.BuildPathDescriptor(item.Meta)
	switch {
	case len(item.Meta.Paths) == 0:
		entry.PathsStatus = doctorSkillPathsNone
	case descriptor.Diagnostic != "":
		entry.PathsStatus = doctorSkillPathsInvalid
		entry.Reason = doctorSkillReasonInvalidPaths
		entry.Detail = descriptor.Diagnostic
	default:
		entry.PathsStatus = doctorSkillPathsConditional
	}
	if item.Meta.DisableModelInvocation {
		entry.ModelInvocation = doctorSkillModelInvocationManualOnly
	} else {
		entry.ModelInvocation = doctorSkillModelInvocationAllowed
	}

	entry.Description = doctorSkillDescription{
		RawBytes: len(item.Meta.Description),
		RawChars: utf8.RuneCountInString(item.Meta.Description),
	}

	loaded, source, loadErr := skill.LoadSkillWithSource(item.Path)
	var body string
	if loadErr == nil && loaded != nil {
		body = loaded.Content
	}
	// Resource problems are reported for every row, including the shadowed
	// ones: a shadowed skill still has a body whose declared resources can
	// be missing.
	resourceStatus, resourceItems := checkDoctorSkillResources(item.Meta, body, loadErr == nil)
	entry.Resources = resourceStatus
	entry.ResourceItems = resourceItems

	if loadErr != nil {
		entry.Load = doctorSkillLoadFailed
		entry.Reason = doctorSkillReasonLoadFailed
		entry.Detail = loadErr.Error()
	} else {
		entry.Load = doctorSkillLoadPassed
		entry.Sizes = doctorSkillSizeMetrics(loaded, source)
	}

	if item.Shadowed {
		// Shadowed rows never reach the ruleset filter; keep the reason on the
		// shadowing fact so --strict can exempt them explicitly.
		entry.Visibility = doctorSkillNotChecked
		entry.Invocation = doctorSkillInvocationUnknown
		entry.Startup = doctorSkillStartupNotListed
		entry.StartupReason = doctorSkillStartupReasonShadowed
		if entry.Reason == "" {
			entry.Reason = doctorSkillReasonShadowed
			entry.Detail = "shadowed by " + item.ShadowedBy
		}
		if entry.Resources == "" {
			entry.Resources = "none"
		}
		return entry
	}

	switch {
	case baseline.Scope == doctorSkillScopeExternalBackend:
		entry.Visibility = doctorSkillNotApplicable
		entry.Invocation = doctorSkillInvocationUnknown
	case !baseline.Available():
		entry.Visibility = doctorSkillNotChecked
		entry.Invocation = doctorSkillInvocationUnknown
		if entry.Reason == "" {
			entry.Reason = doctorSkillReasonRulesetUnavail
			if baseline.Err != nil {
				entry.Detail = baseline.Err.Error()
			} else {
				entry.Detail = "ruleset unavailable for " + baseline.Role
			}
		}
	case !skill.VisibleUnderRuleset(item.Meta, baseline.Ruleset):
		entry.Visibility = doctorSkillDenied
		entry.Invocation = doctorSkillInvocationDeny
		if entry.Reason == "" {
			entry.Reason = doctorSkillReasonDeniedByRuleset
			entry.Detail = fmt.Sprintf("skill %q denied by %s ruleset", item.Meta.Name, baseline.Role)
		}
	default:
		entry.Visibility = doctorSkillVisible
		entry.Invocation = doctorSkillInvocationAction(baseline.Ruleset, item.Meta.Name)
	}

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

// doctorSkillSizeMetrics measures the three sizes the report keeps apart: the
// source file, the parsed body, and the rendered result.
func doctorSkillSizeMetrics(loaded *skill.Skill, source []byte) doctorSkillSizes {
	metrics := doctorSkillSizes{}
	if len(source) > 0 {
		metrics.SourceBytes = len(source)
		metrics.SourceChars = utf8.RuneCount(source)
	}
	if loaded == nil {
		return metrics
	}
	metrics.BodyBytes = len(loaded.Content)
	metrics.BodyChars = utf8.RuneCountInString(loaded.Content)
	parts := tools.FormatSkillInvocationParts(loaded, "")
	metrics.RenderBytes = len(parts.Result)
	metrics.RenderChars = utf8.RuneCountInString(parts.Result)
	metrics.EnvelopeBytes = parts.EnvelopeBytes
	metrics.WarningBytes = len(parts.Warning)
	metrics.OverInlineBudget = metrics.RenderBytes > tooloutput.MaxOutputBytes
	return metrics
}

// doctorSkillInvocationAction evaluates the per-name action of a skill call
// under the baseline. The skill tool matches the permission pattern against
// the skill name, so the name is the argument the ruleset is asked about.
func doctorSkillInvocationAction(ruleset permission.Ruleset, name string) string {
	switch ruleset.Evaluate(toolname.Skill, name) {
	case permission.ActionAllow:
		return doctorSkillInvocationAllow
	case permission.ActionAsk:
		return doctorSkillInvocationAsk
	default:
		return doctorSkillInvocationDeny
	}
}

// doctorSkillListingEntries builds the startup listing input from the
// catalog, in the catalog's order.
func doctorSkillListingEntries(catalog []*skill.Meta, ruleset permission.Ruleset) []tools.SkillListingEntry {
	startup := skill.StartupForRuleset(catalog, ruleset)
	entries := make([]tools.SkillListingEntry, 0, len(startup))
	for _, meta := range startup {
		if meta == nil {
			continue
		}
		entries = append(entries, tools.SkillListingEntry{Name: meta.Name, Desc: meta.Description})
	}
	return entries
}

// applyDoctorSkillStartup records, per skill, whether the startup listing
// keeps its description, reduces it to a name, or omits it.
func applyDoctorSkillStartup(entries []doctorSkillEntry, startup doctorSkillsStartup, listing tools.SkillListing, baseline doctorSkillBaseline) {
	// The listing holds one record per input entry in input order, so the
	// records line up with the startup catalog the renderer received.
	listed := make(map[string]tools.SkillListingRenderEntry, len(listing.Entries))
	if startup.Available {
		for _, record := range listing.Entries {
			listed[record.Name] = record
		}
	}
	for i := range entries {
		entry := &entries[i]
		if startup.Available || entry.Shadowed || entry.Integrity == doctorSkillIntegrityFailed {
			entry.Description.RenderedBytes = new(0)
			entry.Description.RenderedChars = new(0)
			entry.Description.Truncated = new(false)
		}
		if entry.Integrity == doctorSkillIntegrityFailed {
			entry.Startup = doctorSkillStartupNotListed
			entry.StartupReason = doctorSkillStartupReasonInvalid
			continue
		}
		if entry.Shadowed {
			continue
		}
		switch {
		case baseline.Scope == doctorSkillScopeExternalBackend:
			entry.Startup = doctorSkillStartupNotListed
			entry.StartupReason = doctorSkillStartupReasonNotApplicable
		case !baseline.Available():
			entry.Startup = doctorSkillStartupNotListed
			entry.StartupReason = doctorSkillStartupReasonBaselineUnavailable
		default:
			record, ok := listed[entry.Name]
			if ok && record.Listed {
				entry.Description.RenderedBytes = new(len(record.DescriptionJSON))
				entry.Description.RenderedChars = new(utf8.RuneCountInString(record.DescriptionJSON))
				entry.Description.Truncated = new(record.DescriptionTruncated)
			}
			switch {
			case !ok:
				entry.Startup = doctorSkillStartupNotListed
				entry.StartupReason = doctorSkillStartupReason(entry)
			case !record.Listed:
				entry.Startup = doctorSkillStartupNameOnly
				entry.StartupReason = record.OmittedReason
			case record.DescriptionTruncated:
				entry.Startup = doctorSkillStartupListedTruncated
				entry.StartupReason = doctorSkillStartupReasonDescriptionTruncated
			default:
				entry.Startup = doctorSkillStartupListed
			}
		}
	}
}

// doctorSkillStartupReason explains why a skill is absent from the startup
// listing.
func doctorSkillStartupReason(entry *doctorSkillEntry) string {
	switch {
	case entry.Visibility == doctorSkillDenied:
		return doctorSkillStartupReasonDenied
	case entry.ModelInvocation == doctorSkillModelInvocationManualOnly:
		return doctorSkillStartupReasonManualOnly
	case entry.PathsStatus == doctorSkillPathsInvalid:
		return doctorSkillStartupReasonPathsInvalid
	case entry.PathsStatus == doctorSkillPathsConditional:
		return doctorSkillStartupReasonPaths
	default:
		return doctorSkillStartupReasonUnknown
	}
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

func summarizeDoctorSkills(dirs int, configured bool, entries []doctorSkillEntry, scanIssues int) doctorSkillsSummary {
	summary := doctorSkillsSummary{Configured: configured, Dirs: dirs, Total: len(entries), ScanComplete: scanIssues == 0}
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

// doctorSkillActions collects the evidence-backed items worth checking. The
// thresholds only order the list: they never turn a healthy skill into a
// failure. It returns the items to show and the total number found, so the
// report can say how many were left out.
func doctorSkillActions(entries []doctorSkillEntry) ([]doctorSkillAction, int) {
	var actions []doctorSkillAction
	for _, entry := range entries {
		switch {
		case !entry.Shadowed && entry.Sizes.OverInlineBudget:
			actions = append(actions, doctorSkillAction{
				Kind:   doctorSkillActionOverInlineBudget,
				Skill:  entry.Name,
				Detail: fmt.Sprintf("the rendered result is %d bytes, over the %d byte inline budget of a model tool result, so the runtime truncates it", entry.Sizes.RenderBytes, tooloutput.MaxOutputBytes),
			})
		}
		if entry.Startup == doctorSkillStartupNameOnly {
			actions = append(actions, doctorSkillAction{
				Kind:   doctorSkillActionNameOnly,
				Skill:  entry.Name,
				Detail: fmt.Sprintf("the startup listing keeps only the name of this skill (%s)", entry.StartupReason),
			})
		}
		if entry.Startup == doctorSkillStartupListedTruncated {
			actions = append(actions, doctorSkillAction{
				Kind:   doctorSkillActionDescTruncated,
				Skill:  entry.Name,
				Detail: fmt.Sprintf("the description is %d characters and was cut to the %d character per-entry budget", entry.Description.RawChars, tools.SkillListingMaxDescCharsPerEntry),
			})
		}
		if entry.Shadowed {
			actions = append(actions, doctorSkillAction{
				Kind:   doctorSkillActionShadowed,
				Skill:  entry.Name,
				Detail: "shadowed by " + entry.ShadowedBy,
			})
		}
		if !entry.Shadowed && entry.Visibility == doctorSkillDenied {
			actions = append(actions, doctorSkillAction{
				Kind:   doctorSkillActionNotVisible,
				Skill:  entry.Name,
				Detail: entry.Detail,
			})
		}
	}
	sort.SliceStable(actions, func(i, j int) bool {
		return doctorSkillActionRank(actions[i].Kind) < doctorSkillActionRank(actions[j].Kind)
	})
	total := len(actions)
	if len(actions) > doctorSkillsMaxActions {
		actions = actions[:doctorSkillsMaxActions]
	}
	return actions, total
}

// doctorSkillActionRank orders the action kinds: the ones that cost the
// model context come first.
func doctorSkillActionRank(kind string) int {
	switch kind {
	case doctorSkillActionOverInlineBudget:
		return 0
	case doctorSkillActionNameOnly:
		return 1
	case doctorSkillActionDescTruncated:
		return 2
	case doctorSkillActionShadowed:
		return 3
	case doctorSkillActionNotVisible:
		return 4
	default:
		return 5
	}
}

func renderDoctorSkillsText(out io.Writer, report doctorSkillsReport) {
	fmt.Fprintln(out, "Configuration:")
	fmt.Fprintf(out, "  content root: %s\n", report.Context.ContentRoot)
	if report.Context.CheckoutRoot != "" {
		fmt.Fprintf(out, "  checkout root: %s\n", report.Context.CheckoutRoot)
	}
	fmt.Fprintf(out, "  workdir: %s\n", report.Context.WorkDir)
	if report.Context.ConfigHome != "" {
		fmt.Fprintf(out, "  config home: %s\n", report.Context.ConfigHome)
	}
	if report.Context.ConfigNote != "" {
		fmt.Fprintf(out, "  config note: %s\n", report.Context.ConfigNote)
	}
	fmt.Fprintf(out, "  role: %s (source: %s, scope: %s)\n", report.Context.Agent, report.Context.AgentSource, report.Context.AgentScope)
	if report.Context.AgentNote != "" {
		fmt.Fprintf(out, "  baseline: %s\n", report.Context.AgentNote)
	}
	if report.Context.AgentError != "" {
		fmt.Fprintf(out, "  baseline error: %s\n", report.Context.AgentError)
	}
	if len(report.Context.ExtraPaths) > 0 {
		fmt.Fprintf(out, "  extra skill paths: %s\n", strings.Join(report.Context.ExtraPaths, ", "))
	}
	fmt.Fprintf(out, "  render arguments: %q\n", report.Context.RenderArgs)

	if len(report.Dirs) > 0 {
		fmt.Fprintf(out, "\nSkill directories (%d, priority order):\n", len(report.Dirs))
		for _, dir := range report.Dirs {
			state := "missing"
			if dir.Exists {
				state = "present"
			}
			fmt.Fprintf(out, "  %d. %s (%s, %s) [%s]\n", dir.Priority, dir.Path, dir.Origin, dir.Scope, state)
		}
	}

	if len(report.ScanIssues) > 0 {
		fmt.Fprintf(out, "\nScan issues (%d):\n", len(report.ScanIssues))
		for _, issue := range report.ScanIssues {
			fmt.Fprintf(out, "  - %s\n", issue)
		}
		fmt.Fprintln(out, "The rows below may be incomplete: the scan could not read every directory.")
	}

	if len(report.Entries) == 0 {
		if !report.Summary.Configured {
			fmt.Fprintln(out, "\nNo skills configured.")
		} else {
			fmt.Fprintln(out, "\nNo skills found.")
		}
		fmt.Fprintf(out, "Summary: %d skills (ruleset: %s)\n", report.Summary.Total, report.Ruleset)
		return
	}

	fmt.Fprintln(out)
	for _, entry := range report.Entries {
		title := displayDoctorSkillName(entry)
		fmt.Fprintf(out, "- %s\n", title)
		fmt.Fprintf(out, "    path: %s\n", entry.Path)
		fmt.Fprintf(out, "    source: %s\n", entry.Source)
		if entry.Scope != "" {
			fmt.Fprintf(out, "    scope: %s\n", entry.Scope)
		}
		fmt.Fprintf(out, "    integrity: %s, load: %s, visibility: %s, resources: %s",
			entry.Integrity, entry.Load, entry.Visibility, entry.Resources)
		if entry.Shadowed {
			fmt.Fprint(out, ", shadowed")
		}
		fmt.Fprintln(out)
		if entry.Invocation != "" {
			fmt.Fprintf(out, "    invocation by name: %s\n", entry.Invocation)
		}
		if entry.ModelInvocation != "" {
			fmt.Fprintf(out, "    model invocation: %s\n", entry.ModelInvocation)
		}
		if entry.PathsStatus != "" {
			switch entry.PathsStatus {
			case doctorSkillPathsNone:
				fmt.Fprintln(out, "    paths: none")
			case doctorSkillPathsConditional:
				fmt.Fprintf(out, "    paths: conditional (%s), waiting for a matching path\n", strings.Join(entry.Paths, ", "))
			case doctorSkillPathsInvalid:
				fmt.Fprintf(out, "    paths: invalid (%s)\n", strings.Join(entry.Paths, ", "))
			}
		}
		if entry.Startup != "" {
			fmt.Fprintf(out, "    startup: %s", entry.Startup)
			if entry.StartupReason != "" {
				fmt.Fprintf(out, " (%s)", entry.StartupReason)
			}
			fmt.Fprintln(out)
		}
		if entry.Description.RawBytes > 0 || entry.Description.RawChars > 0 {
			fmt.Fprintf(out, "    description: %d bytes / %d chars raw",
				entry.Description.RawBytes, entry.Description.RawChars)
			if entry.Description.RenderedBytes == nil {
				fmt.Fprintln(out, ", rendered size unavailable")
			} else {
				fmt.Fprintf(out, ", %d bytes / %d chars rendered (JSON string)%s\n",
					*entry.Description.RenderedBytes, *entry.Description.RenderedChars,
					doctorSkillTruncatedNote(*entry.Description.Truncated))
			}
		}
		if entry.Sizes.SourceBytes > 0 || entry.Sizes.RenderBytes > 0 {
			fmt.Fprintf(out, "    sizes: source %d bytes / %d chars, body %d bytes / %d chars, render %d bytes / %d chars (envelope %d, warning %d)%s\n",
				entry.Sizes.SourceBytes, entry.Sizes.SourceChars,
				entry.Sizes.BodyBytes, entry.Sizes.BodyChars,
				entry.Sizes.RenderBytes, entry.Sizes.RenderChars,
				entry.Sizes.EnvelopeBytes, entry.Sizes.WarningBytes,
				doctorSkillInlineNote(entry.Sizes.OverInlineBudget))
		}
		if entry.Sidecar != "" {
			fmt.Fprintf(out, "    sidecar: %s\n", entry.Sidecar)
		}
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

	renderDoctorSkillsStartup(out, report)
	renderDoctorSkillsSizes(out, report)

	if len(report.Actions) > 0 {
		fmt.Fprintf(out, "\nTo check (%d of %d shown):\n", len(report.Actions), report.ActionTotal)
		for _, action := range report.Actions {
			if action.Skill != "" {
				fmt.Fprintf(out, "  - %s: %s (%s)\n", action.Kind, action.Skill, action.Detail)
				continue
			}
			fmt.Fprintf(out, "  - %s: %s\n", action.Kind, action.Detail)
		}
	}

	fmt.Fprintf(out, "Summary: %d skills, %d integrity failures, %d load failures, %d denied, %d shadowed, %d resource failures, %d resource warnings (ruleset: %s)\n",
		report.Summary.Total, report.Summary.IntegrityFailed, report.Summary.LoadFailed,
		report.Summary.Denied, report.Summary.Shadowed,
		report.Summary.ResourcesFailed, report.Summary.ResourcesWarning, report.Ruleset)
	fmt.Fprintln(out, "Note: loading a skill says nothing about whether the model will pick it.")
}

// renderDoctorSkillsStartup reports the startup listing block the runtime
// would write under this baseline.
func renderDoctorSkillsStartup(out io.Writer, report doctorSkillsReport) {
	if !report.Startup.Available {
		fmt.Fprintf(out, "\nStartup listing: not computed (%s)\n", report.Startup.Reason)
		return
	}
	fmt.Fprintf(out, "\nStartup listing: %d bytes / %d chars total, %d described, %d by name\n",
		report.Startup.TotalBytes, report.Startup.TotalChars,
		report.Startup.Shown, report.Startup.NameOnly)
	fmt.Fprintf(out, "Description list budget: %d bytes (includes header and omission notice; additional names excluded).\n",
		report.Startup.DescriptionBudgetBytes)
}

// renderDoctorSkillsSizes lists the largest skills first, because the size of
// a body is what decides whether it survives the inline budget.
func renderDoctorSkillsSizes(out io.Writer, report doctorSkillsReport) {
	sized := make([]doctorSkillEntry, 0, len(report.Entries))
	for _, entry := range report.Entries {
		if entry.Sizes.RenderBytes == 0 && entry.Sizes.SourceBytes == 0 {
			continue
		}
		sized = append(sized, entry)
	}
	if len(sized) == 0 {
		return
	}
	sort.SliceStable(sized, func(i, j int) bool {
		if sized[i].Sizes.RenderBytes == sized[j].Sizes.RenderBytes {
			return sized[i].Name < sized[j].Name
		}
		return sized[i].Sizes.RenderBytes > sized[j].Sizes.RenderBytes
	})
	fmt.Fprintln(out, "\nLargest skills:")
	limit := min(len(sized), doctorSkillsMaxLargest)
	for _, entry := range sized[:limit] {
		fmt.Fprintf(out, "  - %s: source %d bytes, body %d bytes, render %d bytes%s\n",
			displayDoctorSkillName(entry), entry.Sizes.SourceBytes, entry.Sizes.BodyBytes, entry.Sizes.RenderBytes,
			doctorSkillInlineNote(entry.Sizes.OverInlineBudget))
	}
	if len(sized) > limit {
		fmt.Fprintf(out, "  ... and %d more\n", len(sized)-limit)
	}
}

func doctorSkillTruncatedNote(truncated bool) string {
	if !truncated {
		return ""
	}
	return ", truncated"
}

func doctorSkillInlineNote(overBudget bool) string {
	if !overBudget {
		return ""
	}
	return fmt.Sprintf(", over the %d byte inline budget", tooloutput.MaxOutputBytes)
}
