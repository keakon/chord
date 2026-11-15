package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/command"
	"github.com/keakon/chord/internal/skill"
)

func loadCustomCommands(ac *AppContext) {
	if ac == nil || ac.MainAgent == nil {
		return
	}
	projectLayerApplied := ac.projectLayerApplied()
	var projectCfgCommands map[string]string
	if projectLayerApplied && ac.ProjectCfg != nil {
		projectCfgCommands = ac.ProjectCfg.Commands
	}
	var globalCfgCommands map[string]string
	if ac.GlobalCfg != nil {
		globalCfgCommands = ac.GlobalCfg.Commands
	}
	projectCfgPath := filepath.Join(ac.ContentRoot, ".chord", "config.yaml")
	globalCfgPath := filepath.Join(ac.ConfigHome, "config.yaml")
	defs, warnings := command.Load(command.LoadOptions{
		ContentRoot:      ac.ContentRoot,
		ConfigHome:       ac.ConfigHome,
		ProjectCfg:       projectCfgCommands,
		ProjectCfgPath:   projectCfgPath,
		GlobalCfg:        globalCfgCommands,
		GlobalCfgPath:    globalCfgPath,
		SkipProjectFiles: !projectLayerApplied,
	})
	for _, w := range warnings {
		log.Warnf("custom command warning=%v", w)
	}
	if len(defs) > 0 {
		ac.LoadedCommands = defs
		ac.MainAgent.SetCustomCommands(defs)
		log.Debugf("custom commands loaded count=%v", len(defs))
	}
}

func skillLoadDirs(ac *AppContext) []string {
	if ac == nil {
		return nil
	}
	return skillLoadDirsForWorkDir(ac, ac.WorkDir)
}

func skillLoadDirsForWorkDir(ac *AppContext, workDir string) []string {
	dirs, _ := trustedSkillDirsForWorkDir(ac, workDir)
	return SkillDirPaths(dirs)
}

func trustedSkillDirsForWorkDir(ac *AppContext, workDir string) ([]SkillDir, []string) {
	if ac == nil {
		return nil, nil
	}
	var extra []string
	if ac.Cfg != nil {
		extra = ac.Cfg.Skills.Paths
	}
	dirs := ResolveSkillDirs(SkillDirsInput{
		ContentRoot: ac.ContentRoot,
		WorkDir:     workDir,
		ConfigHome:  ac.ConfigHome,
		Extra:       extra,
	})
	if !ac.projectLayerApplied() {
		return globalScopeSkillDirs(dirs), nil
	}
	if ac.TrustPlan == nil {
		return dirs, nil
	}
	allowed, blocked := filterSkillDirsByTrust(dirs, ac.TrustPlan.Inputs)
	return allowed, blocked
}

// globalScopeSkillDirs keeps only the directories that are not
// project-controlled: the config-home skills directory and the configured
// extra paths.
func globalScopeSkillDirs(dirs []SkillDir) []SkillDir {
	out := make([]SkillDir, 0, len(dirs))
	for _, dir := range dirs {
		if dir.Scope == skillDirScopeGlobal || dir.Scope == skillDirScopeExtra {
			out = append(out, dir)
		}
	}
	return out
}

// WorkDirSkillChain walks from projectRoot to cwd, collecting
// .agents/skills directories at each intermediate level.
// Deeper directories (closer to cwd) come first for priority.
// The projectRoot level is excluded (already added separately).
func WorkDirSkillChain(projectRoot, cwd string) []string {
	projectRoot = filepath.Clean(projectRoot)
	cwd = filepath.Clean(cwd)
	// Make cwd relative to projectRoot if it's under it.
	rel, err := filepath.Rel(projectRoot, cwd)
	if err != nil {
		return nil
	}
	if rel == "." || rel == "" || strings.HasPrefix(rel, "..") {
		return nil
	}
	// Walk intermediate path segments from deepest to shallowest.
	parts := strings.Split(rel, string(filepath.Separator))
	var result []string
	for i := len(parts); i >= 1; i-- {
		subdir := filepath.Join(append([]string{projectRoot}, parts[:i]...)...)
		result = append(result, filepath.Join(subdir, ".agents", "skills"))
	}
	return result
}

func startAsyncSkillLoad(ac *AppContext) {
	if ac == nil || ac.MainAgent == nil || ac.Cfg == nil {
		return
	}
	ac.skillsLoadOnce.Do(func() {
		refreshSkills(ac)
	})
}

func refreshSkills(ac *AppContext) {
	if ac == nil || ac.MainAgent == nil || ac.Cfg == nil {
		return
	}
	dirs, blocked := trustedSkillDirsForWorkDir(ac, ac.WorkDir)
	notifyBlockedSkillDirs(ac, blocked, false)
	refreshSkillsFromDirs(ac, SkillDirPaths(dirs))
}

// refreshSkillsForWorkDir reloads project skills for an explicit working
// directory after a worktree switch. It mirrors refreshSkills but anchors the
// checkout-first lookup at workDir instead of the startup ac.WorkDir, so a
// branch that adds .chord/skills becomes visible without restarting.
//
// The trust gate stays closed across switches: when the project layer
// applies — under a stored authorization or a one-shot trust alike — only
// directories whose declaration files were part of the startup-discovered
// input set are scanned; a new checkout's skill directories are skipped and
// reported instead of silently widening what this run trusts.
func refreshSkillsForWorkDir(ac *AppContext, workDir string) {
	if ac == nil || ac.MainAgent == nil || ac.Cfg == nil {
		return
	}
	dirs, blocked := trustedSkillDirsForWorkDir(ac, workDir)
	notifyBlockedSkillDirs(ac, blocked, true)
	skillDirs := SkillDirPaths(dirs)
	refreshSkillsFromDirs(ac, skillDirs)
}

func notifyBlockedSkillDirs(ac *AppContext, blocked []string, worktreeSwitch bool) {
	if len(blocked) == 0 {
		return
	}
	for _, dir := range blocked {
		log.Warnf("skill directory not covered by the project inputs trusted at startup; skipped path=%v", dir)
	}
	if ac == nil || ac.MainAgent == nil {
		return
	}
	coverage := "the project configuration trusted at startup"
	if ac.TrustPlan != nil && ac.TrustPlan.Authorized {
		coverage = "the project authorization"
	}
	message := fmt.Sprintf("%d skill director%s not covered by %s were skipped",
		len(blocked), pluralEnding(len(blocked), "y", "ies"), coverage)
	if worktreeSwitch {
		message += "; restart chord there to review them"
	}
	ac.MainAgent.NotifyRuntimeToast(message, "warn")
}

func pluralEnding(n int, singular, plural string) string {
	if n == 1 {
		return singular
	}
	return plural
}

func refreshSkillsFromDirs(ac *AppContext, skillDirs []string) {
	gen := ac.skillsRefreshGen.Add(1)
	go func() {
		loadedSkills, skillErr := skill.NewLoader(skillDirs).ScanMeta()
		ac.installScannedSkills(gen, loadedSkills, skillErr)
	}()
}

// installScannedSkills publishes one project-skill scan unless a newer scan has
// started meanwhile. A worktree switch starts a scan that runs off the main
// path; without the generation check a slow scan of the checkout the session
// has already left would overwrite the catalog the later switch installed.
func (ac *AppContext) installScannedSkills(gen uint64, loadedSkills []*skill.Meta, skillErr error) {
	if ac == nil || ac.skillsRefreshGen.Load() != gen {
		return
	}
	if skillErr != nil {
		log.Warnf("skill loading failed error=%v", skillErr)
		ac.MainAgent.MarkSkillsReady()
		return
	}
	ac.LoadedSkills = loadedSkills
	ac.MainAgent.SetSkills(loadedSkills)
	if len(loadedSkills) > 0 {
		log.Debugf("skills discovered count=%v", len(loadedSkills))
	}
}
