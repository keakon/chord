package main

import (
	"github.com/keakon/golog/log"
	"os"
	"path/filepath"
	"strings"

	"github.com/keakon/chord/internal/command"
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
	if ac.Cfg != nil {
		globalCfgCommands = ac.Cfg.Commands
	}
	projectCfgPath := filepath.Join(ac.ProjectRoot, ".chord", "config.yaml")
	globalCfgPath := filepath.Join(ac.ConfigHome, "config.yaml")
	defs, warnings := command.Load(command.LoadOptions{
		ProjectRoot:    ac.ProjectRoot,
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
		log.Infof("custom commands loaded count=%v", len(defs))
	}
}

func skillLoadDirs(ac *AppContext) []string {
	cwd, _ := os.Getwd()
	return skillLoadDirsForWorkDir(ac, cwd)
}

func skillLoadDirsForWorkDir(ac *AppContext, cwd string) []string {
	if ac == nil {
		return nil
	}
	skillDirs := []string{
		filepath.Join(ac.ProjectRoot, ".chord", "skills"),
		filepath.Join(ac.ProjectRoot, ".agents", "skills"),
		filepath.Join(ac.ConfigHome, "skills"),
	}
	// Phase 2: workDir chain discovery — walk from projectRoot to cwd,
	// collecting .agents/skills at each level. Deeper dirs come first
	// (higher priority in first-wins deduplication).
	if chain := WorkDirSkillChain(ac.ProjectRoot, cwd); len(chain) > 0 {
		// Insert after project-local directories but before global/home skills.
		merged := make([]string, 0, len(skillDirs)+len(chain))
		merged = append(merged, skillDirs[:2]...)
		merged = append(merged, chain...)
		merged = append(merged, skillDirs[2:]...)
		skillDirs = merged
	}
	if ac.Cfg != nil && len(ac.Cfg.Skills.Paths) > 0 {
		skillDirs = append(skillDirs, ac.Cfg.Skills.Paths...)
	}
	if ac.ProjectCfg != nil && len(ac.ProjectCfg.Skills.Paths) > 0 {
		skillDirs = append(skillDirs, ac.ProjectCfg.Skills.Paths...)
	}
	return skillDirs
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
	go func() {
		skillLoader := skill.NewLoader(skillDirs)
		loadedSkills, skillErr := skillLoader.ScanMeta()
		if skillErr != nil {
			log.Warnf("skill loading failed error=%v", skillErr)
			ac.MainAgent.MarkSkillsReady()
			return
		}
		ac.LoadedSkills = loadedSkills
		ac.MainAgent.SetSkills(loadedSkills)
		if len(loadedSkills) > 0 {
			log.Infof("skills discovered count=%v", len(loadedSkills))
		}
	}()
}
