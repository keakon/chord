package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/keakon/golog/log"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/pathutil"
	"github.com/keakon/chord/internal/skill"
	"github.com/keakon/chord/internal/trust"
)

// The project trust gate implements the startup invariant: static discovery
// of every project-level configuration source, an authorization decision,
// and only then the application of the project layer and the initialization
// of the components that consume it. Discovery reads and hashes files; it
// never connects MCP servers, starts LSP clients, runs hooks, or sends
// requests to project-declared endpoints.

// projectTrustPolicy decides what happens when project inputs are present
// but not covered by an authorization.
type projectTrustPolicy int

const (
	// projectTrustPolicyInteractive prompts on the terminal (TUI startup).
	projectTrustPolicyInteractive projectTrustPolicy = iota
	// projectTrustPolicyFailClosed refuses to start with an actionable
	// diagnostic (headless and ACP startups).
	projectTrustPolicyFailClosed
	// projectTrustPolicySkipProject applies no project layer at all
	// (explicit --skip-project-config).
	projectTrustPolicySkipProject
	// projectTrustPolicyTrustOnce applies the current project layer for this
	// run without persisting an authorization (explicit
	// --trust-project-config).
	projectTrustPolicyTrustOnce
)

// projectTrustPolicyForStartup derives the policy from the startup mode and
// the CLI flags. An explicit flag wins over the mode default.
func projectTrustPolicyForStartup(mode string) projectTrustPolicy {
	switch {
	case flagSkipProjectConfig:
		return projectTrustPolicySkipProject
	case flagTrustProjectConfig:
		return projectTrustPolicyTrustOnce
	case mode == "local":
		return projectTrustPolicyInteractive
	default:
		return projectTrustPolicyFailClosed
	}
}

// projectTrustFlagOverridesError rejects contradictory one-run overrides:
// ignoring the project layer and trusting it for one run cannot both apply.
func projectTrustFlagOverridesError() error {
	if flagSkipProjectConfig && flagTrustProjectConfig {
		return fmt.Errorf("--skip-project-config and --trust-project-config are mutually exclusive")
	}
	return nil
}

// registerProjectTrustOverrideFlags adds the one-run project trust overrides
// to a parent command so every subcommand that reads project configuration
// accepts them.
func registerProjectTrustOverrideFlags(cmd *cobra.Command) {
	cmd.PersistentFlags().BoolVar(&flagSkipProjectConfig, "skip-project-config", false, "Ignore this directory's project-level configuration (.chord) for this run")
	cmd.PersistentFlags().BoolVar(&flagTrustProjectConfig, "trust-project-config", false, "Apply this directory's project-level configuration for this run without saving an authorization")
}

// projectTrustSource is one disclosed source file and the capabilities it
// would contribute.
type projectTrustSource struct {
	// File is the display path, relative to the content root when the file
	// lives under it.
	File string
	// Items are the redacted capability lines.
	Items []string
	// Effects describes automatic or external behavior contributed by this source.
	Effects []string
}

// projectTrustDiscovery is the static snapshot of project-level sources for
// one launch. The same read feeds the fingerprint, the disclosure, and (for
// the config file) the eventual merge.
type projectTrustDiscovery struct {
	// Inputs is the fingerprint input set over every declaration file.
	Inputs trust.InputSet
	// ConfigData holds the project config.yaml bytes as read during
	// discovery; nil when the file is absent.
	ConfigData []byte
	// Sources are the disclosed capability sources.
	Sources []projectTrustSource
	// Risk is the high-risk delta over the no-project baseline. Set by the
	// gate before disclosure; nil until assessed.
	Risk *projectTrustRisk
}

// projectTrustPlan is the gate decision handed to startup.
type projectTrustPlan struct {
	// ApplyProject allows the project layer (config merge, project agents,
	// project commands, project skills) to be applied.
	ApplyProject bool
	// Authorized is true when an explicit authorization (stored or granted
	// in this startup) covers Inputs. Trust-once and low-risk runs apply
	// without it.
	Authorized bool
	// LowRisk is true when the project layer was applied without a prompt
	// because its delta over the global baseline contained no hazard.
	LowRisk bool
	// Inputs is the declaration input set covered by this run's trust decision;
	// nil when the project layer is not applied.
	Inputs trust.InputSet
	// Notice explains a skipped project layer; empty when nothing was
	// skipped.
	Notice string
}

type projectTrustDecision struct {
	ContentRoot string
	Plan        *projectTrustPlan
	ConfigData  []byte
}

// projectTrustGate carries the startup policy and the authorization prompt.
type projectTrustGate struct {
	policy                        projectTrustPolicy
	preflight                     *projectTrustDecision
	allowTrustOncePreflightPrompt bool
	// prompt asks the user to authorize the disclosed sources. Returning an
	// error aborts startup; returning false skips the project layer.
	prompt func(discovery *projectTrustDiscovery, contentRoot string) (bool, error)
}

func newProjectTrustGate(policy projectTrustPolicy) *projectTrustGate {
	return &projectTrustGate{policy: policy, prompt: promptProjectTrustAuthorization}
}

// resolve runs the gate: discovery, fingerprint evaluation against the trust
// store, and the policy decision. It returns the plan and the project
// config bytes to merge (nil when no project config applies or the layer is
// skipped).
func (g *projectTrustGate) resolve(globalConfigPath, contentRoot, workDir string) (*projectTrustPlan, []byte, error) {
	discovery, err := discoverProjectTrust(contentRoot, workDir)
	if err != nil {
		return nil, nil, err
	}
	// A low-risk decision is recomputed from the baseline on every start, so a
	// worktree switch must not reuse or lock it like a pending authorization.
	if decision := g.preflight; decision != nil && decision.Plan != nil &&
		!decision.Plan.Authorized && !decision.Plan.LowRisk && !decision.Plan.Inputs.Empty() {
		sameRoot := canonicalProjectTrustRoot(decision.ContentRoot) == canonicalProjectTrustRoot(contentRoot)
		if sameRoot && decision.Plan.Inputs.Fingerprint() == discovery.Inputs.Fingerprint() {
			if decision.Plan.ApplyProject {
				if verifyErr := verifyProjectTrustInputs(decision.Plan.Inputs, contentRoot, workDir); verifyErr != nil {
					return nil, nil, verifyErr
				}
			}
			return decision.Plan, decision.ConfigData, nil
		}
		if decision.Plan.ApplyProject {
			if g.policy == projectTrustPolicyTrustOnce && g.allowTrustOncePreflightPrompt {
				authorize, promptErr := g.prompt(discovery, contentRoot)
				if promptErr != nil {
					return nil, nil, promptErr
				}
				if !authorize {
					return &projectTrustPlan{
						Inputs: discovery.Inputs,
						Notice: fmt.Sprintf("project configuration in %s is not authorized; starting without the project layer", contentRoot),
					}, nil, nil
				}
				return &projectTrustPlan{
					ApplyProject: true,
					Inputs:       discovery.Inputs,
				}, discovery.ConfigData, nil
			}
			return nil, nil, fmt.Errorf("project configuration changed after worktree setup; restart chord to review it")
		}
	}
	if discovery.Inputs.Empty() {
		// No project-declared input exists: nothing to gate, no
		// authorization demanded.
		return &projectTrustPlan{}, nil, nil
	}
	switch g.policy {
	case projectTrustPolicySkipProject:
		return &projectTrustPlan{
			Notice: fmt.Sprintf("project configuration in %s is ignored (--skip-project-config)", contentRoot),
		}, nil, nil
	case projectTrustPolicyTrustOnce:
		return &projectTrustPlan{
			ApplyProject: true,
			Inputs:       discovery.Inputs,
		}, discovery.ConfigData, nil
	}

	storePath, hasRecord, record, err := loadProjectTrustRecord(globalConfigPath, contentRoot)
	if err != nil {
		return nil, nil, err
	}
	fingerprint := discovery.Inputs.Fingerprint()
	if hasRecord && record.Fingerprint == fingerprint {
		return &projectTrustPlan{
			ApplyProject: true,
			Authorized:   true,
			Inputs:       discovery.Inputs,
		}, discovery.ConfigData, nil
	}

	// A stored authorization is only needed when the project layer changes
	// something that can start a process, redirect traffic, or rewrite a
	// control. Compare against the effective no-project baseline; if the delta
	// is hazard-free, apply it without a prompt and without persisting trust.
	// Recomputing on every start means a later high-risk edit, or a baseline
	// change that promotes the delta, is still gated.
	globalData, readErr := os.ReadFile(globalConfigPath)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return nil, nil, fmt.Errorf("read global config for risk assessment: %w", readErr)
	}
	if readErr != nil {
		globalData = nil
	}
	risk, err := assessProjectTrustRisk(globalConfigPath, globalData, contentRoot, workDir, discovery)
	if err != nil {
		return nil, nil, err
	}
	discovery.Risk = risk
	if !risk.requiresTrust() {
		// No notice: a hazard-free layer is the normal case, so surfacing it
		// on every start would recreate the confirmation fatigue this removes.
		// doctor config reports the low-risk state for anyone who wants it.
		return &projectTrustPlan{
			ApplyProject: true,
			LowRisk:      true,
			Inputs:       discovery.Inputs,
		}, discovery.ConfigData, nil
	}

	switch g.policy {
	case projectTrustPolicyFailClosed:
		return nil, nil, projectTrustRequiredError(contentRoot, hasRecord)
	case projectTrustPolicyInteractive:
		authorize, err := g.prompt(discovery, contentRoot)
		if err != nil {
			return nil, nil, err
		}
		if !authorize {
			return &projectTrustPlan{
				Inputs: discovery.Inputs,
				Notice: fmt.Sprintf("project configuration in %s is not authorized; starting without the project layer", contentRoot),
			}, nil, nil
		}
		if err := verifyProjectTrustInputs(discovery.Inputs, contentRoot, workDir); err != nil {
			return nil, nil, err
		}
		if err := trust.Authorize(storePath, canonicalProjectTrustRoot(contentRoot), discovery.Inputs, time.Now()); err != nil {
			// A failed save must never land as authorized: run without the
			// project layer and say why.
			log.Warnf("trust store write failed error=%v", err)
			return &projectTrustPlan{
				Notice: fmt.Sprintf("project configuration could not be saved as authorized (%v); starting without the project layer", err),
			}, nil, nil
		}
		return &projectTrustPlan{
			ApplyProject: true,
			Authorized:   true,
			Inputs:       discovery.Inputs,
		}, discovery.ConfigData, nil
	default:
		return nil, nil, fmt.Errorf("unknown project trust policy %d", g.policy)
	}
}

func verifyProjectTrustInputs(inputs trust.InputSet, contentRoot, workDir string) error {
	current, err := discoverProjectTrust(contentRoot, workDir)
	if err != nil {
		return fmt.Errorf("recheck project configuration: %w", err)
	}
	if inputs.Fingerprint() != current.Inputs.Fingerprint() {
		return fmt.Errorf("project configuration changed since discovery; restart chord to review it")
	}
	if err := inputs.Verify(); err != nil {
		return fmt.Errorf("project configuration changed since discovery: %w (restart chord to review it)", err)
	}
	return nil
}

// loadProjectTrustRecord resolves the trust store from the global config
// and returns the record for the canonical project root. A store that
// cannot be read yields no record: damaged state must never read as
// authorized.
func loadProjectTrustRecord(globalConfigPath, contentRoot string) (storePath string, hasRecord bool, record trust.ProjectTrust, err error) {
	globalCfg, err := config.LoadConfigFromPath(globalConfigPath)
	if err != nil {
		return "", false, trust.ProjectTrust{}, fmt.Errorf("load config: %w", err)
	}
	pathLocator, err := config.ResolvePathLocator(globalCfg, config.PathOptions{})
	if err != nil {
		return "", false, trust.ProjectTrust{}, fmt.Errorf("resolve storage paths: %w", err)
	}
	storePath = trust.Path(pathLocator.StateDir)
	store, err := trust.Load(storePath)
	if err != nil {
		log.Warnf("trust store unavailable; treating the project layer as unauthorized error=%v", err)
		return storePath, false, trust.ProjectTrust{}, nil
	}
	record, hasRecord = store.Lookup(canonicalProjectTrustRoot(contentRoot))
	return storePath, hasRecord, record, nil
}

func canonicalProjectTrustRoot(contentRoot string) string {
	canonical, err := config.CanonicalProjectRoot(contentRoot)
	if err != nil {
		return filepath.Clean(contentRoot)
	}
	return canonical
}

// projectTrustRequiredError is the fail-closed diagnostic for non-interactive
// startups facing unauthorized project inputs.
func projectTrustRequiredError(contentRoot string, hasRecord bool) error {
	reason := "no authorization is recorded for this directory"
	if hasRecord {
		reason = "the recorded authorization does not match the current project configuration files"
	}
	return fmt.Errorf("project configuration in %s is not authorized: %s\n"+
		"Run chord interactively in this directory to review and authorize it, or pass "+
		"--skip-project-config to ignore the project layer for this run, or "+
		"--trust-project-config to trust the current project configuration for this run only",
		contentRoot, reason)
}

func resolveProjectConfigForUse(ctx context.Context, workDir, mode, globalPath string, globalData []byte, extraRefs ...string) (*config.ResolvedConfig, *projectTrustDecision, error) {
	if err := projectTrustFlagOverridesError(); err != nil {
		return nil, nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	contentRoot := resolveContentRoot(ctx, workDir)
	gate := newProjectTrustGate(projectTrustPolicyForStartup(mode))
	plan, projectData, err := gate.resolve(globalPath, contentRoot, workDir)
	if err != nil {
		return nil, nil, err
	}
	if plan != nil && plan.ApplyProject {
		if verifyErr := verifyProjectTrustInputs(plan.Inputs, contentRoot, workDir); verifyErr != nil {
			return nil, nil, verifyErr
		}
	}
	resolved, err := config.LoadResolvedConfigData(globalPath, globalData, config.ProjectConfigPath(contentRoot), projectData, extraRefs...)
	if err != nil {
		return nil, nil, fmt.Errorf("load config: %w", err)
	}
	return resolved, &projectTrustDecision{ContentRoot: contentRoot, Plan: plan, ConfigData: projectData}, nil
}

func newProjectSkillLoader(inputs trust.InputSet, trustState string) agent.ProjectSkillLoader {
	return func(meta *skill.Meta) (*skill.Skill, error) {
		declaration := meta.DeclarationPath
		if declaration == "" {
			declaration = meta.Location
		}
		var matched *trust.Input
		matchedLen := -1
		for _, input := range inputs {
			_, ok := projectTrustRelativePath(input.Root, declaration)
			if !ok {
				continue
			}
			// Compare canonical roots, not the raw strings. A canonical trust
			// root may spell the same ancestor differently from a lexical
			// declaration path (macOS /var vs /private/var), and then the
			// shorter-spelled but less specific root could outwin the truly
			// most specific one by raw string length alone.
			length := len(input.Root)
			if canonical, err := config.CanonicalProjectRoot(input.Root); err == nil {
				length = len(canonical)
			}
			if matched == nil || length > matchedLen {
				matched = &input
				matchedLen = length
			}
		}
		if matched != nil {
			snapshot, err := skill.ReadSnapshot(declaration)
			if err != nil {
				return nil, err
			}
			if err := verifySkillSnapshot(*matched, snapshot); err != nil {
				return nil, fmt.Errorf("skill %q: declaration no longer matches the project configuration %s: %w; restart chord to review it", meta.Name, trustState, err)
			}
			return snapshot.Load()
		}
		return skill.LoadSkill(meta.Location)
	}
}

// A canonical trust root and a lexical declaration may spell an ancestor
// differently (for example, macOS /var and /private/var). Match that ancestor
// without resolving symlinks below the project root and losing provenance.
func projectTrustRelativePath(root, path string) (string, bool) {
	if rel, ok := pathutil.RelToBase(path, root); ok {
		return filepath.ToSlash(rel), true
	}
	rootInfo, err := os.Stat(root)
	if err != nil {
		return "", false
	}
	for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
		if info, err := os.Stat(dir); err == nil && os.SameFile(info, rootInfo) {
			rel, err := filepath.Rel(dir, path)
			return filepath.ToSlash(rel), err == nil
		}
		if filepath.Dir(dir) == dir {
			return "", false
		}
	}
}

func verifySkillSnapshot(input trust.Input, snapshot *skill.Snapshot) error {
	authorized := make(map[string]trust.FileHash, len(input.Files))
	for _, f := range input.Files {
		authorized[f.Path] = f
	}
	for _, file := range snapshot.Files {
		hash, err := skillDeclarationHash(input.Root, file)
		if err != nil {
			return err
		}
		expected, exists := authorized[hash.Path]
		if file.Data == nil && !exists {
			continue
		}
		if !exists || expected != hash {
			return fmt.Errorf("declaration %s was added, removed or changed", hash.Path)
		}
	}
	return nil
}

func skillDeclarationHash(root string, file skill.Declaration) (trust.FileHash, error) {
	rel, ok := projectTrustRelativePath(root, file.Path)
	var err error
	if !ok {
		rel, err = filepath.Rel(root, file.Path)
	}
	if err != nil {
		return trust.FileHash{}, fmt.Errorf("relativize skill declaration: %w", err)
	}
	hash := trust.FileHash{Path: filepath.ToSlash(rel), Target: file.Target}
	if file.Data != nil {
		hash.SHA256 = fmt.Sprintf("%x", sha256.Sum256(file.Data))
	}
	return hash, nil
}

// filterSkillDirsByTrust keeps only the skill directories whose current
// declaration files are all covered by the authorized input set, and
// returns the paths of the directories that had to be skipped.
func filterSkillDirsByTrust(dirs []SkillDir, inputs trust.InputSet) (allowed []SkillDir, blocked []string) {
	for _, dir := range dirs {
		if dir.Root == "" {
			allowed = append(allowed, dir)
			continue
		}
		files, _, err := discoverSkillDeclarations(dir)
		if err != nil {
			blocked = append(blocked, dir.Path)
			continue
		}
		root := skillTrustRoot(dir)
		if !inputs.Covers(root, files) {
			blocked = append(blocked, dir.Path)
			continue
		}
		allowed = append(allowed, dir)
	}
	return allowed, blocked
}

// discoverProjectTrust enumerates and hashes every project-level source:
// the project config, agent definitions, custom command files, and the
// project-scope skill declarations. Only files under project-controlled
// roots count; global config-home sources are the user's own.
func discoverProjectTrust(contentRoot, workDir string) (*projectTrustDiscovery, error) {
	discovery := &projectTrustDiscovery{}
	filesByRoot := map[string][]trust.FileHash{}

	// Project config: this one read is the disclosure and the merge snapshot.
	configPath := config.ProjectConfigPath(contentRoot)
	configData, err := os.ReadFile(configPath)
	switch {
	case err == nil:
		discovery.ConfigData = configData
		if err := addProjectTrustData(filesByRoot, contentRoot, configPath, configData); err != nil {
			return nil, err
		}
	case errors.Is(err, os.ErrNotExist):
		// No project config file.
	default:
		return nil, fmt.Errorf("read project config %s: %w", configPath, err)
	}

	parsed, parseErr := parseProjectDisclosureConfig(configData)
	discovery.Sources = append(discovery.Sources, configDisclosureSources(configPath, configData, parsed, parseErr, contentRoot)...)

	// Extra skill directories declared by the project config are themselves
	// project-controlled inputs.
	var projectSkillExtras []string
	if parseErr == nil {
		projectSkillExtras = parsed.Skills.Paths
	}

	// Agent definitions under .chord/agents.
	agentFiles, err := listProjectAgentFiles(filepath.Join(contentRoot, ".chord", "agents"))
	if err != nil {
		return nil, err
	}
	for _, path := range agentFiles {
		if err := addProjectTrustFile(filesByRoot, contentRoot, path); err != nil {
			return nil, err
		}
	}
	discovery.Sources = append(discovery.Sources, agentDisclosureSources(contentRoot, agentFiles)...)

	// Custom command Markdown under .chord/commands.
	commandFiles, err := listProjectCommandFiles(filepath.Join(contentRoot, ".chord", "commands"))
	if err != nil {
		return nil, err
	}
	for _, path := range commandFiles {
		if err := addProjectTrustFile(filesByRoot, contentRoot, path); err != nil {
			return nil, err
		}
	}
	discovery.Sources = append(discovery.Sources, commandDisclosureSources(contentRoot, commandFiles)...)

	// Project-scope skill declarations (checkout, workdir chain, content
	// root fallback, project-declared extra paths).
	skillDirs := projectScopeSkillDirs(contentRoot, workDir, projectSkillExtras)
	var skillItems []skillDisclosureEntry
	for _, dir := range skillDirs {
		files, names, err := discoverSkillDeclarations(dir)
		if err != nil {
			return nil, err
		}
		root := skillTrustRoot(dir)
		filesByRoot[root] = append(filesByRoot[root], files...)
		for _, name := range names {
			skillItems = append(skillItems, skillDisclosureEntry{label: dir.Path, name: name})
		}
	}
	discovery.Sources = append(discovery.Sources, skillDisclosureSources(skillItems)...)

	discovery.Inputs = buildProjectTrustInputs(filesByRoot)
	return discovery, nil
}

// addProjectTrustFile hashes one declaration file into its root's group.
func addProjectTrustFile(filesByRoot map[string][]trust.FileHash, root, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	return addProjectTrustData(filesByRoot, root, path, data)
}

func addProjectTrustData(filesByRoot map[string][]trust.FileHash, root, path string, data []byte) error {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return fmt.Errorf("relativize %s against %s: %w", path, root, err)
	}
	sum := fmt.Sprintf("%x", sha256.Sum256(data))
	key := canonicalProjectTrustRoot(root)
	filesByRoot[key] = append(filesByRoot[key], trust.FileHash{Path: filepath.ToSlash(rel), SHA256: sum})
	return nil
}

func buildProjectTrustInputs(filesByRoot map[string][]trust.FileHash) trust.InputSet {
	inputs := make(trust.InputSet, 0, len(filesByRoot))
	for root, files := range filesByRoot {
		inputs = append(inputs, trust.Input{Root: root, Files: files})
	}
	return inputs.Normalize()
}

// projectScopeSkillDirs returns the skill search path restricted to
// project-controlled scopes, plus the project-declared extra paths.
func projectScopeSkillDirs(contentRoot, workDir string, projectExtras []string) []SkillDir {
	dirs := ResolveSkillDirs(SkillDirsInput{
		ContentRoot: contentRoot,
		WorkDir:     workDir,
		// The global config home and the global skills.paths entries are not
		// project-controlled and stay out of the gate.
	})
	var out []SkillDir
	for _, dir := range dirs {
		switch dir.Scope {
		case skillDirScopeCheckout, skillDirScopeWorkdirChain, skillDirScopeContentRoot:
			out = append(out, dir)
		}
	}
	seen := map[string]bool{}
	for _, dir := range out {
		seen[filepath.Clean(dir.Path)] = true
	}
	for _, extra := range projectExtras {
		trimmed := strings.TrimSpace(extra)
		if trimmed == "" || seen[filepath.Clean(trimmed)] {
			continue
		}
		seen[filepath.Clean(trimmed)] = true
		out = append(out, SkillDir{
			Path:   trimmed,
			Origin: skillDirOriginExtra,
			Scope:  skillDirScopeExtra,
			Root:   trimmed,
		})
	}
	return out
}

func discoverSkillDeclarations(dir SkillDir) ([]trust.FileHash, []string, error) {
	matches, err := doublestar.Glob(os.DirFS(dir.Path), "**/SKILL.md")
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("scan skill directory %s: %w", dir.Path, err)
	}
	sort.Strings(matches)
	var files []trust.FileHash
	var names []string
	seen := map[string]bool{}
	for _, rel := range matches {
		snapshot, err := skill.ReadSnapshot(filepath.Join(dir.Path, filepath.FromSlash(rel)))
		if err != nil {
			return nil, nil, err
		}
		for _, file := range snapshot.Files {
			if file.Data == nil {
				continue
			}
			hash, err := skillDeclarationHash(skillTrustRoot(dir), file)
			if err != nil {
				return nil, nil, err
			}
			if !seen[hash.Path] {
				files = append(files, hash)
				seen[hash.Path] = true
			}
		}
		name := filepath.Base(filepath.Dir(rel))
		if loaded, err := snapshot.Load(); err == nil {
			name = loaded.Name
		}
		names = append(names, name)
	}
	return files, names, nil
}

func skillTrustRoot(dir SkillDir) string {
	if dir.Scope == skillDirScopeExtra {
		// Keep the configured path identity when the directory itself is a
		// symlink. Retargeting it must not turn a project source into a global one.
		if root, err := filepath.Abs(dir.Root); err == nil {
			return root
		}
	}
	return canonicalProjectTrustRoot(dir.Root)
}

// listProjectAgentFiles enumerates the flat agent definition files the
// loader would read, in stable order.
func listProjectAgentFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read agent config directory %s: %w", dir, err)
	}
	var out []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		switch strings.ToLower(filepath.Ext(entry.Name())) {
		case ".yaml", ".yml", ".md":
			out = append(out, filepath.Join(dir, entry.Name()))
		}
	}
	sort.Strings(out)
	return out, nil
}

// listProjectCommandFiles enumerates the custom command Markdown files the
// loader would scan, in stable order.
func listProjectCommandFiles(dir string) ([]string, error) {
	matches, err := doublestar.Glob(os.DirFS(dir), "**/*.md")
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("scan command directory %s: %w", dir, err)
	}
	sort.Strings(matches)
	out := make([]string, 0, len(matches))
	for _, rel := range matches {
		out = append(out, filepath.Join(dir, filepath.FromSlash(rel)))
	}
	return out, nil
}

// projectDisclosureYAML extracts the capability-bearing sections of the
// project config for the disclosure list. Unrelated fields are ignored.
type projectDisclosureYAML struct {
	MCP        config.MCPConfig                 `yaml:"mcp"`
	Hooks      config.HookConfig                `yaml:"hooks"`
	LSP        config.LSPConfig                 `yaml:"lsp"`
	Providers  map[string]config.ProviderConfig `yaml:"providers"`
	ModelPools map[string][]string              `yaml:"model_pools"`
	Proxy      string                           `yaml:"proxy"`
	WebFetch   *config.WebFetchConfig           `yaml:"web_fetch"`
	Skills     config.SkillsConfig              `yaml:"skills"`
	Commands   map[string]string                `yaml:"commands"`
}

func parseProjectDisclosureConfig(data []byte) (projectDisclosureYAML, error) {
	var parsed projectDisclosureYAML
	if len(data) == 0 {
		return parsed, nil
	}
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		return parsed, err
	}
	return parsed, nil
}

// projectTrustDisplayPath renders a source path relative to the content
// root when possible.
func projectTrustDisplayPath(path, contentRoot string) string {
	if rel, err := filepath.Rel(contentRoot, path); err == nil && !strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(rel)
	}
	return path
}

func configDisclosureSources(configPath string, data []byte, parsed projectDisclosureYAML, parseErr error, contentRoot string) []projectTrustSource {
	if len(data) == 0 {
		return nil
	}
	display := projectTrustDisplayPath(configPath, contentRoot)
	if parseErr != nil {
		return []projectTrustSource{{File: display, Items: []string{"config declaration present (could not be read for disclosure)"}}}
	}
	var items []string
	var effects []string
	for _, name := range sortedMapKeys(parsed.MCP) {
		server := parsed.MCP[name]
		items = append(items, mcpServerDisclosure(name, server))
	}
	if len(parsed.MCP) > 0 {
		effects = append(effects, "MCP servers may start local processes or connect to remote services.")
	}
	for _, event := range projectHookEvents() {
		entries := event.entries(parsed.Hooks)
		if len(entries) == 0 {
			continue
		}
		var commands []string
		for _, entry := range entries {
			commands = append(commands, truncateDisclosureTarget(hookCommandDisplay(entry.Command)))
		}
		items = append(items, fmt.Sprintf("hook %s -> %s", event.label, strings.Join(commands, "; ")))
		effects = append(effects, "Hooks can run commands automatically during configured events.")
	}
	hasLSP := false
	for _, name := range sortedMapKeys(parsed.LSP) {
		server := parsed.LSP[name]
		if server.Disabled {
			continue
		}
		hasLSP = true
		items = append(items, fmt.Sprintf("lsp server %q -> %s", name, truncateDisclosureTarget(server.Command)))
	}
	if hasLSP {
		effects = append(effects, "LSP servers may start local processes.")
	}
	for _, name := range sortedMapKeys(parsed.Providers) {
		provider := parsed.Providers[name]
		endpoint := provider.APIURL
		if endpoint == "" {
			endpoint = provider.Preset
		}
		if endpoint == "" {
			endpoint = "(endpoint inherited)"
		}
		items = append(items, fmt.Sprintf("provider %q -> %s", name, disclosureEndpoint(endpoint)))
	}
	if len(parsed.Providers) > 0 || parsed.Proxy != "" {
		effects = append(effects, "Model requests may be sent to the configured provider or proxy endpoints.")
	}
	if len(parsed.ModelPools) > 0 {
		items = append(items, fmt.Sprintf("model pools: %d pool(s)", len(parsed.ModelPools)))
	}
	if parsed.Proxy != "" {
		items = append(items, fmt.Sprintf("proxy -> %s", disclosureEndpoint(parsed.Proxy)))
	}
	if parsed.WebFetch != nil {
		if parsed.WebFetch.Proxy != nil && *parsed.WebFetch.Proxy != "" {
			items = append(items, fmt.Sprintf("web_fetch proxy -> %s", disclosureEndpoint(*parsed.WebFetch.Proxy)))
			effects = append(effects, "Web fetch requests may be sent through the configured proxy.")
		} else {
			items = append(items, "web_fetch override")
		}
	}
	for _, path := range parsed.Skills.Paths {
		if strings.TrimSpace(path) != "" {
			items = append(items, fmt.Sprintf("skills path -> %s", truncateDisclosureTarget(path)))
		}
	}
	if len(parsed.Commands) > 0 {
		items = append(items, fmt.Sprintf("commands: %d yaml command(s)", len(parsed.Commands)))
	}
	if len(items) == 0 {
		items = []string{"config declaration present (no capability-bearing section)"}
	}
	return []projectTrustSource{{File: display, Items: items, Effects: effects}}
}

func mcpServerDisclosure(name string, server config.MCPServerConfig) string {
	target := disclosureEndpoint(server.URL)
	kind := "http"
	if target == "" {
		kind = "stdio"
		target = strings.TrimSpace(server.Command)
		if len(server.Args) > 0 {
			target += " [arguments hidden]"
		}
	}
	if target == "" {
		target = "(no target)"
	}
	item := fmt.Sprintf("mcp server %q -> %s (%s)", name, truncateDisclosureTarget(target), kind)
	if server.Manual {
		item += " [manual]"
	}
	return item
}

type projectHookEvent struct {
	label   string
	entries func(cfg config.HookConfig) []config.HookEntry
}

func projectHookEvents() []projectHookEvent {
	return []projectHookEvent{
		{"on_session_start", func(c config.HookConfig) []config.HookEntry { return c.OnSessionStart }},
		{"on_session_end", func(c config.HookConfig) []config.HookEntry { return c.OnSessionEnd }},
		{"on_tool_call", func(c config.HookConfig) []config.HookEntry { return c.OnToolCall }},
		{"on_tool_result", func(c config.HookConfig) []config.HookEntry { return c.OnToolResult }},
		{"on_before_tool_result_append", func(c config.HookConfig) []config.HookEntry { return c.OnBeforeToolResultAppend }},
		{"on_before_llm_call", func(c config.HookConfig) []config.HookEntry { return c.OnBeforeLLMCall }},
		{"on_after_llm_call", func(c config.HookConfig) []config.HookEntry { return c.OnAfterLLMCall }},
		{"on_before_compress", func(c config.HookConfig) []config.HookEntry { return c.OnBeforeCompress }},
		{"on_after_compress", func(c config.HookConfig) []config.HookEntry { return c.OnAfterCompress }},
		{"on_idle", func(c config.HookConfig) []config.HookEntry { return c.OnIdle }},
		{"on_wait_confirm", func(c config.HookConfig) []config.HookEntry { return c.OnWaitConfirm }},
		{"on_wait_question", func(c config.HookConfig) []config.HookEntry { return c.OnWaitQuestion }},
		{"on_agent_error", func(c config.HookConfig) []config.HookEntry { return c.OnAgentError }},
		{"on_tool_batch_complete", func(c config.HookConfig) []config.HookEntry { return c.OnToolBatchComplete }},
	}
}

func hookCommandDisplay(command config.HookCommand) string {
	if len(command.Args) > 0 {
		return command.Args[0] + " [arguments hidden]"
	}
	return "(shell script; contents hidden)"
}

func agentDisclosureSources(contentRoot string, paths []string) []projectTrustSource {
	if len(paths) == 0 {
		return nil
	}
	byFile := map[string][]string{}
	effectsByFile := map[string][]string{}
	order := make([]string, 0, len(paths))
	for _, path := range paths {
		display := projectTrustDisplayPath(path, contentRoot)
		if _, seen := byFile[display]; !seen {
			order = append(order, display)
		}
		item := fmt.Sprintf("agent definition %q", filepath.Base(path))
		if agentCfg, err := config.LoadAgentConfig(path); err == nil && agentCfg != nil {
			item = fmt.Sprintf("agent %q", agentCfg.Name)
			if len(agentCfg.MCP) > 0 {
				item += fmt.Sprintf(" with %d mcp server(s)", len(agentCfg.MCP))
				effectsByFile[display] = append(effectsByFile[display], "Agent MCP servers may start local processes or connect to remote services.")
				for _, name := range sortedMapKeys(agentCfg.MCP) {
					byFile[display] = append(byFile[display], mcpServerDisclosure(name, agentCfg.MCP[name]))
				}
			}
		}
		byFile[display] = append(byFile[display], item)
	}
	sources := make([]projectTrustSource, 0, len(order))
	for _, display := range order {
		sources = append(sources, projectTrustSource{File: display, Items: byFile[display], Effects: effectsByFile[display]})
	}
	return sources
}

func commandDisclosureSources(contentRoot string, paths []string) []projectTrustSource {
	if len(paths) == 0 {
		return nil
	}
	byFile := map[string][]string{}
	order := make([]string, 0, len(paths))
	for _, path := range paths {
		display := projectTrustDisplayPath(path, contentRoot)
		if _, seen := byFile[display]; !seen {
			order = append(order, display)
		}
		name := commandDisplayName(path)
		byFile[display] = append(byFile[display], fmt.Sprintf("command %q", name))
	}
	sources := make([]projectTrustSource, 0, len(order))
	for _, display := range order {
		sources = append(sources, projectTrustSource{File: display, Items: byFile[display]})
	}
	return sources
}

func commandDisplayName(path string) string {
	base := filepath.Base(path)
	return strings.TrimSuffix(base, ".md")
}

// skillDisclosureEntry pairs one skill declaration with the directory that
// contributed it, for the disclosure list.
type skillDisclosureEntry struct {
	label string
	name  string
}

func skillDisclosureSources(entries []skillDisclosureEntry) []projectTrustSource {
	if len(entries) == 0 {
		return nil
	}
	byDir := map[string][]string{}
	order := make([]string, 0, len(entries))
	for _, entry := range entries {
		if _, seen := byDir[entry.label]; !seen {
			order = append(order, entry.label)
		}
		byDir[entry.label] = append(byDir[entry.label], fmt.Sprintf("skill %q", entry.name))
	}
	sources := make([]projectTrustSource, 0, len(order))
	for _, label := range order {
		sources = append(sources, projectTrustSource{File: label, Items: byDir[label]})
	}
	return sources
}

func sortedMapKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// truncateDisclosureTarget keeps the disclosed target readable without
// echoing arbitrary file content.
func truncateDisclosureTarget(target string) string {
	target = strings.TrimSpace(target)
	const maxLen = 120
	if len(target) <= maxLen {
		return target
	}
	return target[:maxLen-3] + "..."
}
