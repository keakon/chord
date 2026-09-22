package main

import (
	"path/filepath"
	"strings"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/command"
	"github.com/keakon/chord/internal/pathutil"
	"github.com/keakon/chord/internal/skill"
)

func loadCustomCommands(ac *AppContext) {
	if ac == nil || ac.MainAgent == nil {
		return
	}
	var projectCfgCommands map[string]string
	if ac.ProjectCfg != nil {
		projectCfgCommands = ac.ProjectCfg.Commands
	}
	var globalCfgCommands map[string]string
	if ac.GlobalCfg != nil {
		globalCfgCommands = ac.GlobalCfg.Commands
	}
	projectCfgPath := filepath.Join(ac.ContentRoot, ".chord", "config.yaml")
	globalCfgPath := filepath.Join(ac.ConfigHome, "config.yaml")
	defs, warnings := command.Load(command.LoadOptions{
		ContentRoot:    ac.ContentRoot,
		ConfigHome:     ac.ConfigHome,
		ProjectCfg:     projectCfgCommands,
		ProjectCfgPath: projectCfgPath,
		GlobalCfg:      globalCfgCommands,
		GlobalCfgPath:  globalCfgPath,
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
	if ac == nil {
		return nil
	}
	roots := ac.projectSkillRoots(workDir)
	skillDirs := make([]string, 0, 2*len(roots)+4)
	if len(roots) > 0 {
		checkout := roots[0]
		skillDirs = append(skillDirs,
			filepath.Join(checkout, ".chord", "skills"),
			filepath.Join(checkout, ".agents", "skills"))
		// Walk from the checkout root down to workDir, collecting
		// .agents/skills at each level. Deeper dirs come first (higher priority
		// in first-wins deduplication), and the checkout's own chain outranks
		// the content-root fallback below.
		skillDirs = append(skillDirs, WorkDirSkillChain(checkout, workDir)...)
		for _, fallback := range roots[1:] {
			skillDirs = append(skillDirs,
				filepath.Join(fallback, ".chord", "skills"),
				filepath.Join(fallback, ".agents", "skills"))
		}
	}
	skillDirs = append(skillDirs, filepath.Join(ac.ConfigHome, "skills"))
	if ac.Cfg != nil && len(ac.Cfg.Skills.Paths) > 0 {
		skillDirs = append(skillDirs, ac.Cfg.Skills.Paths...)
	}
	return skillDirs
}

// projectSkillRoots returns the roots project skills are read from, in priority
// order: the checkout workDir sits in first (a branch may add or override
// skills), then the content root as the fallback for gitignored project skills
// the checkout does not contain. The skill loader de-duplicates by skill name,
// so listing both implements "checkout wins, otherwise content root".
func (ac *AppContext) projectSkillRoots(workDir string) []string {
	if ac == nil || strings.TrimSpace(ac.ContentRoot) == "" {
		return nil
	}
	checkoutRoot := pathutil.CheckoutRoot(workDir, ac.ContentRoot)
	if checkoutRoot == "" || checkoutRoot == ac.ContentRoot {
		return []string{ac.ContentRoot}
	}
	return []string{checkoutRoot, ac.ContentRoot}
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
	skillDirs := skillLoadDirs(ac)
	refreshSkillsFromDirs(ac, skillDirs)
}

// refreshSkillsForWorkDir reloads project skills for an explicit working
// directory after a worktree switch. It mirrors refreshSkills but anchors the
// checkout-first lookup at workDir instead of the startup ac.WorkDir, so a
// branch that adds .chord/skills becomes visible without restarting.
func refreshSkillsForWorkDir(ac *AppContext, workDir string) {
	if ac == nil || ac.MainAgent == nil || ac.Cfg == nil {
		return
	}
	skillDirs := skillLoadDirsForWorkDir(ac, workDir)
	refreshSkillsFromDirs(ac, skillDirs)
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
