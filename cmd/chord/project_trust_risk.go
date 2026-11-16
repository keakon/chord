package main

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"gopkg.in/yaml.v3"

	"github.com/keakon/chord/internal/command"
	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/skill"
)

// projectTrustRisk classifies what the project layer actually changes relative
// to the configuration that would be in effect without it. Only changes that
// can start a process, redirect traffic, or rewrite another control require an
// explicit trust decision. Everything else - new agents, new commands, new
// skills, prose-only changes - is applied without a prompt.
//
// The assessment is recomputed on every discovery: a low-risk layer is applied
// without writing an authorization, so a later mutation that introduces a
// high-risk change is seen and gated on the next start. Likewise a change to
// the global baseline that turns a previously low-risk delta high-risk is
// caught, because no stored authorization short-circuits the assessment.
type projectTrustRisk struct {
	// Hazards are high-risk deltas found in the project layer. Non-empty means
	// an explicit trust decision is required.
	Hazards []string
	// Unreadable is set when a declaration could not be parsed strictly. An
	// unreadable declaration can hide a high-risk delta, so it always forces a
	// trust decision instead of reading as low-risk.
	Unreadable bool
}

func (r *projectTrustRisk) requiresTrust() bool {
	return r == nil || r.Unreadable || len(r.Hazards) > 0
}

// riskBaseline is the effective configuration with the project layer absent,
// assembled from the same global bytes the real startup reads.
type riskBaseline struct {
	config     *config.Config
	agents     map[string]*config.AgentConfig
	commands   map[string]bool
	skills     map[string]bool
	configHome string
}

// assessProjectTrustRisk compares the discovered project layer against the
// effective baseline. globalPath/globalData are the exact bytes startup read.
func assessProjectTrustRisk(globalPath string, globalData []byte, contentRoot, workDir string, discovery *projectTrustDiscovery) (*projectTrustRisk, error) {
	risk := &projectTrustRisk{}
	if discovery == nil {
		risk.Unreadable = true
		return risk, nil
	}

	base, err := buildRiskBaseline(globalPath, globalData, contentRoot, workDir)
	if err != nil {
		// A baseline that cannot be resolved must never read as "low risk".
		risk.Unreadable = true
		return risk, nil
	}

	if len(discovery.ConfigData) > 0 {
		var parsed projectDisclosureYAML
		if err := yaml.Unmarshal(discovery.ConfigData, &parsed); err != nil {
			risk.Unreadable = true
		} else {
			applied, loadErr := config.LoadResolvedConfigData(globalPath, globalData,
				config.ProjectConfigPath(contentRoot), discovery.ConfigData)
			if loadErr != nil {
				risk.Unreadable = true
			} else {
				risk.Hazards = append(risk.Hazards, compareConfigHazards(base.config, applied.Config)...)
			}
		}
	}

	agentHazards, agentUnreadable := compareAgentHazards(base, contentRoot)
	risk.Hazards = append(risk.Hazards, agentHazards...)
	risk.Unreadable = risk.Unreadable || agentUnreadable
	risk.Hazards = append(risk.Hazards, compareCommandHazards(base, contentRoot, discovery.ConfigData)...)
	skillHazards, skillUnreadable := compareSkillHazards(base, contentRoot, workDir, discovery.ConfigData)
	risk.Hazards = append(risk.Hazards, skillHazards...)
	risk.Unreadable = risk.Unreadable || skillUnreadable

	return risk, nil
}

func buildRiskBaseline(globalPath string, globalData []byte, contentRoot, workDir string) (*riskBaseline, error) {
	resolved, err := config.LoadResolvedConfigData(globalPath, globalData, config.ProjectConfigPath(contentRoot), nil)
	if err != nil {
		return nil, err
	}
	b := &riskBaseline{config: resolved.Config}

	globalCfg, err := config.LoadConfigFromPath(globalPath)
	if err != nil {
		return nil, err
	}
	locator, err := config.ResolvePathLocator(globalCfg, config.PathOptions{})
	if err != nil {
		return nil, err
	}
	b.configHome = locator.ConfigHome

	b.agents, err = config.ResolveAgentConfigs("", filepath.Join(locator.ConfigHome, "agents"))
	if err != nil {
		return nil, err
	}

	baseDefs, _ := command.Load(command.LoadOptions{
		ConfigHome:    locator.ConfigHome,
		GlobalCfg:     b.config.Commands,
		GlobalCfgPath: globalPath,
	})
	b.commands = make(map[string]bool, len(baseDefs))
	for _, def := range baseDefs {
		b.commands[def.Name] = true
	}

	// Global skill declarations: config-home skills plus global skills.paths.
	// Project-declared extra paths are deliberately excluded (they are part of
	// the delta being assessed).
	b.skills = make(map[string]bool)
	dirs := ResolveSkillDirs(SkillDirsInput{
		ContentRoot: contentRoot,
		WorkDir:     workDir,
		ConfigHome:  locator.ConfigHome,
		Extra:       b.config.Skills.Paths,
	})
	for _, dir := range globalScopeSkillDirs(dirs) {
		// Global skill dirs carry no Root, so discoverSkillDeclarations cannot
		// relativize against it. Read names straight from the declarations.
		paths, err := listSkillDeclarationPaths(dir.Path)
		if err != nil {
			return nil, err
		}
		for _, path := range paths {
			meta, err := readSkillMeta(path)
			if err != nil {
				continue
			}
			b.skills[meta.Name] = true
		}
	}
	return b, nil
}

// compareConfigHazards reports only capability-bearing sections. Cosmetic or
// model-behaviour fields in the project layer do not require trust.
func compareConfigHazards(base, applied *config.Config) []string {
	if base == nil || applied == nil {
		return []string{"config declaration could not be compared"}
	}
	var out []string
	if len(base.MCP) == 0 && len(applied.MCP) > 0 {
		out = append(out, "declares MCP server(s) that start local processes or connect to remote services")
	} else if !reflect.DeepEqual(base.MCP, applied.MCP) {
		out = append(out, "changes MCP server configuration")
	}
	if !reflect.DeepEqual(base.Hooks, applied.Hooks) {
		out = append(out, "adds or changes hooks that run commands automatically")
	}
	if !reflect.DeepEqual(base.LSP, applied.LSP) {
		out = append(out, "adds or changes LSP server(s) that start local processes")
	}
	if !reflect.DeepEqual(base.Providers, applied.Providers) || base.Proxy != applied.Proxy ||
		!reflect.DeepEqual(base.WebFetch, applied.WebFetch) {
		out = append(out, "redirects model or web-fetch requests to configured endpoints")
	}
	return out
}

// compareAgentHazards flags agents that widen an existing role or introduce an
// auto-starting MCP server. A new agent with ordinary permission is guidance.
func compareAgentHazards(b *riskBaseline, contentRoot string) ([]string, bool) {
	paths, err := listProjectAgentFiles(filepath.Join(contentRoot, ".chord", "agents"))
	if err != nil {
		return []string{"project agent declarations could not be read"}, true
	}
	var out []string
	for _, path := range paths {
		cfg, err := config.LoadAgentConfig(path)
		if err != nil || cfg == nil {
			return append(out, "project agent declaration could not be parsed"), true
		}
		if len(cfg.MCP) > 0 {
			out = append(out, fmt.Sprintf("agent %q declares MCP server(s) that start automatically", cfg.Name))
		}
		baseCfg, exists := b.agents[cfg.Name]
		if !exists {
			continue
		}
		if widensPermission(permission.ParsePermission(&baseCfg.Permission), permission.ParsePermission(&cfg.Permission)) {
			out = append(out, fmt.Sprintf("agent %q widens an existing role's permission", cfg.Name))
		}
	}
	return out, false
}

// widensPermission reports whether the project ruleset grants allow where the
// baseline grants something weaker. Rules are keyed by (permission, pattern) so
// a pure reordering of identical rules is not a false positive.
func widensPermission(base, project permission.Ruleset) bool {
	baseByKey := make(map[[2]string]permission.Action, len(base))
	for _, rule := range base {
		baseByKey[[2]string{rule.Permission, rule.Pattern}] = rule.Action
	}
	for _, rule := range project {
		if rule.Action != permission.ActionAllow {
			continue
		}
		key := [2]string{rule.Permission, rule.Pattern}
		before, found := baseByKey[key]
		if !found || before != permission.ActionAllow {
			return true
		}
	}
	return false
}

// compareCommandHazards flags a project command that shadows a global one: the
// user believes they invoke their own command but run project-supplied text.
func compareCommandHazards(b *riskBaseline, contentRoot string, configData []byte) []string {
	projectCfg := parseProjectCommands(configData)
	defs, _ := command.Load(command.LoadOptions{
		ContentRoot: contentRoot,
		ConfigHome:  b.configHome,
		ProjectCfg:  projectCfg,
		GlobalCfg:   b.config.Commands,
	})
	var out []string
	for _, def := range defs {
		if def.Source != "project-md" && def.Source != "project-yaml" {
			continue
		}
		if b.commands[def.Name] {
			out = append(out, fmt.Sprintf("command %q overrides an existing command", def.Name))
		}
	}
	return out
}

func parseProjectCommands(data []byte) map[string]string {
	if len(data) == 0 {
		return nil
	}
	var parsed struct {
		Commands map[string]string `yaml:"commands"`
	}
	if yaml.Unmarshal(data, &parsed) != nil {
		return nil
	}
	return parsed.Commands
}

// compareSkillHazards flags project skills that shadow a global skill or carry
// execution-affecting frontmatter (fork context, model override, tool grants).
func compareSkillHazards(b *riskBaseline, contentRoot, workDir string, configData []byte) ([]string, bool) {
	var extras []string
	unreadable := false
	if len(configData) > 0 {
		var parsed projectDisclosureYAML
		if yaml.Unmarshal(configData, &parsed) == nil {
			extras = parsed.Skills.Paths
		}
	}
	var out []string
	seen := make(map[string]bool)
	for _, dir := range projectScopeSkillDirs(contentRoot, workDir, extras) {
		matches, err := listSkillDeclarationPaths(dir.Path)
		if err != nil {
			out = append(out, "project skill declarations could not be read")
			unreadable = true
			continue
		}
		for _, path := range matches {
			meta, err := readSkillMeta(path)
			if err != nil {
				out = append(out, "project skill declaration could not be parsed")
				unreadable = true
				continue
			}
			if seen[meta.Name] {
				continue
			}
			seen[meta.Name] = true
			if b.skills[meta.Name] {
				out = append(out, fmt.Sprintf("skill %q overrides an existing skill", meta.Name))
			}
			if meta.AllowedTools != nil || strings.TrimSpace(meta.Model) != "" ||
				strings.TrimSpace(meta.Effort) != "" || meta.Context == "fork" {
				out = append(out, fmt.Sprintf("skill %q changes execution context or tool access", meta.Name))
			}
		}
	}
	return out, unreadable
}

func readSkillMeta(path string) (*skill.Meta, error) {
	snapshot, err := skill.ReadSnapshot(path)
	if err != nil {
		return nil, err
	}
	loaded, err := snapshot.Load()
	if err != nil {
		return nil, err
	}
	meta := loaded.Meta
	return &meta, nil
}

func listSkillDeclarationPaths(dir string) ([]string, error) {
	if dir == "" {
		return nil, nil
	}
	matches, err := doublestar.Glob(os.DirFS(dir), "**/SKILL.md")
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	sort.Strings(matches)
	out := make([]string, 0, len(matches))
	for _, rel := range matches {
		out = append(out, filepath.Join(dir, filepath.FromSlash(rel)))
	}
	return out, nil
}
