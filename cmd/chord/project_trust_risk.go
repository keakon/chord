package main

import (
	"fmt"
	"maps"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/keakon/chord/internal/command"
	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/skill"
)

// projectTrustRisk classifies what the project layer actually changes relative
// to the configuration that would be in effect without it. Only changes that
// can start a process, redirect traffic, or rewrite another control require an
// explicit trust decision. New guidance-only agents, commands, and ordinary
// skills are applied without a prompt.
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

	agentHazards, agentUnreadable := compareAgentHazards(base, discovery.AgentFiles)
	risk.Hazards = append(risk.Hazards, agentHazards...)
	risk.Unreadable = risk.Unreadable || agentUnreadable
	risk.Hazards = append(risk.Hazards, compareCommandHazards(base, contentRoot, discovery)...)
	skillHazards, skillUnreadable := compareSkillHazards(base, discovery.SkillSnapshots)
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

	locator, err := config.ResolvePathLocator(resolved.Global, config.PathOptions{})
	if err != nil {
		return nil, err
	}
	b.configHome = locator.ConfigHome

	b.agents, err = config.ResolveAgentConfigs("", filepath.Join(locator.ConfigHome, "agents"))
	if err != nil {
		return nil, err
	}

	// SkipProjectFiles is essential: with an empty ContentRoot the loader's
	// project command directory is the relative ".chord/commands", which
	// resolves against the process cwd - normally the project root at startup.
	// Without this, the baseline would scan the very commands being assessed
	// and every project command would look like it overrides a global one.
	baseDefs, _ := command.Load(command.LoadOptions{
		ConfigHome:       locator.ConfigHome,
		GlobalCfg:        b.config.Commands,
		GlobalCfgPath:    globalPath,
		SkipProjectFiles: true,
	})
	b.commands = make(map[string]bool, len(baseDefs))
	for _, def := range baseDefs {
		b.commands[def.Name] = true
	}

	// Global skill declarations: config-home skills plus global skills.paths.
	// Project-declared extra paths are deliberately excluded (they are part of
	// the delta being assessed).
	dirs := ResolveSkillDirs(SkillDirsInput{
		ContentRoot: contentRoot,
		WorkDir:     workDir,
		ConfigHome:  locator.ConfigHome,
		Extra:       b.config.Skills.Paths,
	})
	globalSkills, err := skill.NewLoader(SkillDirPaths(globalScopeSkillDirs(dirs))).ScanMeta()
	if err != nil {
		return nil, err
	}
	b.skills = make(map[string]bool, len(globalSkills))
	for _, meta := range globalSkills {
		b.skills[meta.Name] = true
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
	} else if !maps.EqualFunc(base.MCP, applied.MCP, func(a, b config.MCPServerConfig) bool {
		return reflect.DeepEqual(a, b)
	}) {
		out = append(out, "changes MCP server configuration")
	}
	if !reflect.DeepEqual(base.Hooks, applied.Hooks) {
		out = append(out, "adds or changes hooks that run commands automatically")
	}
	if !maps.EqualFunc(base.LSP, applied.LSP, func(a, b config.LSPServerConfig) bool {
		return reflect.DeepEqual(a, b)
	}) {
		out = append(out, "adds or changes LSP server(s) that start local processes")
	}
	if !maps.EqualFunc(base.Providers, applied.Providers, func(a, b config.ProviderConfig) bool {
		return reflect.DeepEqual(applyRuntimeAPIBaseOverride(a), applyRuntimeAPIBaseOverride(b))
	}) || base.Proxy != applied.Proxy ||
		!reflect.DeepEqual(base.WebFetch, applied.WebFetch) {
		out = append(out, "redirects model or web-fetch requests to configured endpoints")
	}
	return out
}

// compareAgentHazards flags agents that widen an existing role or introduce an
// MCP server or an external process. A new guidance-only agent is low-risk.
func compareAgentHazards(b *riskBaseline, files map[string][]byte) ([]string, bool) {
	agents, err := config.LoadAgentConfigsData(files)
	if err != nil {
		return []string{"project agent declaration could not be parsed"}, true
	}
	var out []string
	for _, name := range sortedMapKeys(agents) {
		cfg := agents[name]
		if len(cfg.MCP) > 0 {
			out = append(out, fmt.Sprintf("agent %q declares MCP server(s) that start automatically", cfg.Name))
		}
		baseCfg, exists := b.agents[cfg.Name]
		if cfg.IsExternalBackend() && (!exists || !sameBackendLaunch(baseCfg.Backend, cfg.Backend)) {
			out = append(out, fmt.Sprintf("agent %q adds or changes an external backend process", cfg.Name))
		}
		if !exists {
			continue
		}
		if permission.ParsePermission(&cfg.Permission).MayWiden(permission.ParsePermission(&baseCfg.Permission)) {
			out = append(out, fmt.Sprintf("agent %q widens an existing role's permission or cannot be proven more restrictive", cfg.Name))
		}
	}
	return out, false
}

func sameBackendLaunch(base, project *config.BackendConfig) bool {
	return base != nil && strings.TrimSpace(base.Kind) == strings.TrimSpace(project.Kind) &&
		base.EffectiveCommand() == project.EffectiveCommand() &&
		slices.Equal(base.LaunchArgs, project.LaunchArgs) && maps.Equal(base.Env, project.Env)
}

// compareCommandHazards flags a project command that shadows a global one: the
// user believes they invoke their own command but run project-supplied text.
func compareCommandHazards(b *riskBaseline, contentRoot string, discovery *projectTrustDiscovery) []string {
	projectCfg := parseProjectCommands(discovery.ConfigData)
	defs, _ := command.Load(command.LoadOptions{
		ContentRoot:  contentRoot,
		ConfigHome:   b.configHome,
		ProjectCfg:   projectCfg,
		GlobalCfg:    b.config.Commands,
		ProjectFiles: discovery.CommandFiles,
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
func compareSkillHazards(b *riskBaseline, snapshots []*skill.Snapshot) ([]string, bool) {
	unreadable := false
	var out []string
	seen := make(map[string]bool)
	for _, snapshot := range snapshots {
		loaded, err := snapshot.Load()
		if err != nil {
			out = append(out, "project skill declaration could not be parsed")
			unreadable = true
			continue
		}
		meta := loaded.Meta
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
	return out, unreadable
}
