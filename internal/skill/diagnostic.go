package skill

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/bmatcuk/doublestar/v4"
)

// DiagnosticItem is one row of the diagnostic scan. Unlike ScanMeta it keeps
// parse failures and shadowed skills so the doctor report can show why a
// SKILL.md never reaches the model.
type DiagnosticItem struct {
	Name       string // parsed skill name, empty when parsing failed
	Path       string // absolute path to the SKILL.md file
	Root       string // absolute path to the skill directory
	Dir        string // scan directory that produced this row
	Meta       *Meta  // nil when the file failed to parse
	Err        error  // raw parse/read error, nil for valid rows
	Shadowed   bool   // true when an earlier valid skill already owns the name
	ShadowedBy string // winning SKILL.md path for shadowed rows
}

// ScanProblem is one path the diagnostic scan cannot read, or a scan root the
// runtime glob would silently drop. The glob reports success in both cases, so
// the doctor report carries them next to the rows instead of hiding them.
type ScanProblem struct {
	Path string // scan root or tree entry the glob cannot traverse
	Err  error  // underlying filesystem error
}

// String renders the problem for JSON and text reports.
func (p ScanProblem) String() string {
	return fmt.Sprintf("%s: %v", p.Path, p.Err)
}

// walkSkillTree collects the unreadable parts of dir, following symlinks the
// way the runtime glob does. filepath.WalkDir does not follow them, so an
// audit built on it would call a symlinked skill tree complete while the glob
// silently skips an unreadable directory behind the link. visited holds
// resolved paths so symlink cycles and repeated trees stay finite.
func walkSkillTree(dir string, problems *[]ScanProblem, visited map[string]struct{}) {
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		*problems = append(*problems, ScanProblem{Path: dir, Err: err})
		return
	}
	if _, seen := visited[resolved]; seen {
		return
	}
	visited[resolved] = struct{}{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		*problems = append(*problems, ScanProblem{Path: dir, Err: err})
		return
	}
	for _, entry := range entries {
		child := filepath.Join(dir, entry.Name())
		if entry.IsDir() {
			walkSkillTree(child, problems, visited)
			continue
		}
		if entry.Type()&fs.ModeSymlink == 0 {
			continue
		}
		info, err := os.Stat(child) // follows the link
		if err != nil {
			*problems = append(*problems, ScanProblem{Path: child, Err: err})
			continue
		}
		if info.IsDir() {
			walkSkillTree(child, problems, visited)
		}
	}
}

// ScanMetaDiagnostic discovers skills with the same directory order, glob,
// and LoadMeta parser as the runtime ScanMeta. The difference is reporting:
// invalid files become their own rows instead of being silently skipped, and
// later same-name skills become shadowed rows. Deduplication applies only
// between valid rows, so an invalid file never masks a valid same-name skill
// from a lower-priority directory.
//
// Rows and problems come back together. The glob skips unreadable
// directories, broken symlinks, and non-directory roots while reporting
// success, so those paths are returned as problems for the caller to report
// instead of making the roots look empty.
func (l *Loader) ScanMetaDiagnostic() ([]DiagnosticItem, []ScanProblem) {
	if l == nil {
		return nil, nil
	}
	seen := make(map[string]string)
	var items []DiagnosticItem
	var problems []ScanProblem
	for _, dir := range l.dirs {
		info, statErr := os.Stat(dir)
		if statErr != nil {
			if !errors.Is(statErr, fs.ErrNotExist) {
				problems = append(problems, ScanProblem{Path: dir, Err: statErr})
				continue
			}
			// A missing directory is just an unconfigured one, but Stat
			// follows symlinks, so a dangling link looks missing too.
			if _, lstatErr := os.Lstat(dir); lstatErr == nil {
				problems = append(problems, ScanProblem{Path: dir, Err: errors.New("dangling symlink")})
			}
			continue
		}
		if !info.IsDir() {
			problems = append(problems, ScanProblem{Path: dir, Err: errors.New("scan path is not a directory")})
			continue
		}
		walkSkillTree(dir, &problems, make(map[string]struct{}))
		matches, err := doublestar.Glob(os.DirFS(dir), "**/SKILL.md")
		if err != nil {
			problems = append(problems, ScanProblem{Path: dir, Err: fmt.Errorf("glob skills: %w", err)})
			continue
		}
		for _, match := range matches {
			fullPath := filepath.Join(dir, match)
			meta, loadErr := LoadMeta(fullPath)
			absPath, absErr := filepath.Abs(fullPath)
			if absErr != nil {
				absPath = fullPath
			}
			root := filepath.Dir(absPath)
			if loadErr != nil {
				items = append(items, DiagnosticItem{
					Path: absPath,
					Root: root,
					Dir:  dir,
					Err:  loadErr,
				})
				continue
			}
			if winner, exists := seen[meta.Name]; exists {
				items = append(items, DiagnosticItem{
					Name:       meta.Name,
					Path:       meta.Location,
					Root:       meta.RootDir,
					Dir:        dir,
					Meta:       meta,
					Shadowed:   true,
					ShadowedBy: winner,
				})
				continue
			}
			seen[meta.Name] = meta.Location
			items = append(items, DiagnosticItem{
				Name: meta.Name,
				Path: meta.Location,
				Root: meta.RootDir,
				Dir:  dir,
				Meta: meta,
			})
		}
	}
	return items, problems
}
