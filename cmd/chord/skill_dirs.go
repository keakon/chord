package main

import (
	"path/filepath"
	"strings"

	"github.com/keakon/chord/internal/pathutil"
)

// Skill directory origins. The origin says which kind of directory a skill
// was found in; the scope says which root it belongs to, and together they
// explain why one directory outranks another.
const (
	// skillDirOriginProject marks a <root>/.chord/skills directory.
	skillDirOriginProject = "project"
	// skillDirOriginAgents marks a <root>/.agents/skills directory.
	skillDirOriginAgents = "agents"
	// skillDirOriginGlobal marks the <config-home>/skills directory.
	skillDirOriginGlobal = "global"
	// skillDirOriginExtra marks a directory from the skills.paths config.
	skillDirOriginExtra = "extra"
)

// Skill directory scopes. The scope is the priority tier: earlier scopes
// outrank later ones when two directories declare the same skill name.
const (
	// skillDirScopeCheckout marks the checkout root's own directories.
	skillDirScopeCheckout = "checkout"
	// skillDirScopeWorkdirChain marks the intermediate .agents/skills
	// directories between the checkout root and the working directory.
	skillDirScopeWorkdirChain = "workdir_chain"
	// skillDirScopeContentRoot marks the content root's fallback
	// directories, used for project skills a checkout does not contain.
	skillDirScopeContentRoot = "content_root_fallback"
	// skillDirScopeGlobal marks the config home's skills directory.
	skillDirScopeGlobal = "global"
	// skillDirScopeExtra marks a configured skills.paths entry.
	skillDirScopeExtra = "extra"
)

// SkillDir is one directory of the skill search path.
type SkillDir struct {
	// Path is the directory to scan.
	Path string
	// Origin is the kind of directory: chord, agents, global or extra.
	Origin string
	// Scope is the priority tier the directory belongs to.
	Scope string
	// Root is the checkout or content root the directory belongs to. It is
	// empty for the global and extra directories, which belong to no root.
	Root string
}

// SkillDirsInput carries the explicit inputs of the skill search path. The
// inputs are explicit so the doctor command can resolve the same path as the
// runtime without starting an AppContext.
type SkillDirsInput struct {
	// ContentRoot anchors project configuration, agent definitions and the
	// fallback skill directories.
	ContentRoot string
	// WorkDir is the directory the session runs in. It decides the checkout
	// root and the intermediate .agents/skills chain.
	WorkDir string
	// ConfigHome holds the global skills directory.
	ConfigHome string
	// Extra holds the configured skills.paths entries.
	Extra []string
}

// SkillContentRoots returns the roots project skills are read from, in
// priority order: the checkout workDir sits in first (a branch may add or
// override skills), then the content root as the fallback for gitignored
// project skills the checkout does not contain. The skill loader
// de-duplicates by skill name, so listing both implements "checkout wins,
// otherwise content root".
func SkillContentRoots(contentRoot, workDir string) []string {
	if strings.TrimSpace(contentRoot) == "" {
		return nil
	}
	checkoutRoot := pathutil.CheckoutRoot(workDir, contentRoot)
	if checkoutRoot == "" || checkoutRoot == contentRoot {
		return []string{contentRoot}
	}
	return []string{checkoutRoot, contentRoot}
}

// ResolveSkillDirs returns the skill search path in runtime priority order:
// the checkout's own directories, the intermediate .agents/skills chain from
// the checkout root down to the working directory, the content root's
// fallback directories, the global directory, and finally the configured
// extra paths. The doctor diagnostics reuse it so a report describes the
// directories the runtime actually scans.
func ResolveSkillDirs(in SkillDirsInput) []SkillDir {
	roots := SkillContentRoots(in.ContentRoot, in.WorkDir)
	dirs := make([]SkillDir, 0, 2*len(roots)+4)
	if len(roots) > 0 {
		checkout := roots[0]
		dirs = append(dirs,
			SkillDir{Path: filepath.Join(checkout, ".chord", "skills"), Origin: skillDirOriginProject, Scope: skillDirScopeCheckout, Root: checkout},
			SkillDir{Path: filepath.Join(checkout, ".agents", "skills"), Origin: skillDirOriginAgents, Scope: skillDirScopeCheckout, Root: checkout},
		)
		// Walk from the checkout root down to workDir, collecting
		// .agents/skills at each level. Deeper dirs come first (higher priority
		// in first-wins deduplication), and the checkout's own chain outranks
		// the content-root fallback below.
		for _, chain := range WorkDirSkillChain(checkout, in.WorkDir) {
			dirs = append(dirs, SkillDir{Path: chain, Origin: skillDirOriginAgents, Scope: skillDirScopeWorkdirChain, Root: checkout})
		}
		for _, fallback := range roots[1:] {
			dirs = append(dirs,
				SkillDir{Path: filepath.Join(fallback, ".chord", "skills"), Origin: skillDirOriginProject, Scope: skillDirScopeContentRoot, Root: fallback},
				SkillDir{Path: filepath.Join(fallback, ".agents", "skills"), Origin: skillDirOriginAgents, Scope: skillDirScopeContentRoot, Root: fallback},
			)
		}
	}
	if strings.TrimSpace(in.ConfigHome) != "" {
		dirs = append(dirs, SkillDir{Path: filepath.Join(in.ConfigHome, "skills"), Origin: skillDirOriginGlobal, Scope: skillDirScopeGlobal})
	}
	for _, p := range in.Extra {
		if strings.TrimSpace(p) == "" {
			continue
		}
		dirs = append(dirs, SkillDir{Path: p, Origin: skillDirOriginExtra, Scope: skillDirScopeExtra})
	}
	return dirs
}

// SkillDirPaths returns only the paths of a search path, in the same order.
func SkillDirPaths(dirs []SkillDir) []string {
	paths := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		paths = append(paths, dir.Path)
	}
	return paths
}
