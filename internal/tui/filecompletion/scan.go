package filecompletion

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/keakon/chord/internal/filefilter"
	"github.com/keakon/chord/internal/gitinspect"
)

const (
	scanMaxDepth = 8
	scanTimeout  = 2 * time.Second
)

// Scan gathers sorted file-reference candidates within a bounded discovery window.
// It prefers Git tracked/untracked listings and falls back to a filesystem walk
// using the same deadline. Nonpositive limits produce no candidates.
func Scan(workDir string, limit int) []string {
	if limit <= 0 {
		return nil
	}
	if strings.TrimSpace(workDir) == "" {
		workDir = "."
	}
	ctx, cancel := context.WithTimeout(context.Background(), scanTimeout)
	defer cancel()
	if files, ok := scanGit(ctx, workDir, limit); ok {
		return files
	}
	return scanWalk(ctx, workDir, limit)
}

func scanGit(ctx context.Context, workDir string, limit int) ([]string, bool) {
	tracked, err := gitinspect.Files(ctx, workDir, true)
	if err != nil {
		return nil, false
	}
	files := make([]string, 0, min(limit, 1024))
	seen := make(map[string]bool)
	add := func(out string) {
		for line := range strings.SplitSeq(out, "\n") {
			if len(files) >= limit {
				return
			}
			path := normalizeGitPath(line)
			if path == "" || seen[path] || skipIndexedPath(path) || !indexedPathExists(workDir, path) {
				continue
			}
			seen[path] = true
			files = append(files, path)
		}
	}
	add(tracked)
	if len(files) < limit {
		if untracked, err := gitinspect.Files(ctx, workDir, false); err == nil {
			add(untracked)
		}
	}
	slices.Sort(files)
	return files, true
}

func normalizeGitPath(path string) string {
	path = strings.TrimSpace(path)
	path = strings.TrimSuffix(path, "\r")
	path = filepath.ToSlash(path)
	path = strings.TrimPrefix(path, "./")
	if path == "." || strings.HasPrefix(path, "../") || filepath.IsAbs(path) {
		return ""
	}
	return path
}

func indexedPathExists(workDir, path string) bool {
	if workDir == "" {
		workDir = "."
	}
	info, err := os.Stat(filepath.Join(workDir, filepath.FromSlash(path)))
	return err == nil && !info.IsDir()
}

func scanWalk(ctx context.Context, workDir string, limit int) []string {
	var files []string
	if strings.TrimSpace(workDir) == "" {
		workDir = "."
	}
	root := workDir
	if abs, err := filepath.Abs(workDir); err == nil {
		root = abs
	}
	ignore := filefilter.NewGitIgnoreMatcher(workDir)
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return filepath.SkipAll
		}
		if err != nil {
			return nil
		}
		if d.IsDir() {
			// Rules that exclude generated or hidden directories apply below
			// the walk root only: an explicitly opened project may itself be
			// named `build`, `vendor`, or `dist`.
			if path != root {
				if strings.HasPrefix(d.Name(), ".") {
					return filepath.SkipDir
				}
				if filefilter.IsSkippedDirName(d.Name()) {
					return filepath.SkipDir
				}
				switch d.Name() {
				case "node_modules", ".idea", ".vscode", "vendor", "dist", "build":
					return filepath.SkipDir
				}
			}
			if strings.Count(path, string(os.PathSeparator))-strings.Count(root, string(os.PathSeparator)) >= scanMaxDepth {
				return filepath.SkipDir
			}
			// Honor .gitignore at the walk root so project-specific
			// generated / cached directories (e.g. `out/`, `target/`,
			// `coverage/`) don't pollute @-mention suggestions.
			if ignore != nil && path != root {
				rel, relErr := filepath.Rel(root, path)
				if relErr != nil {
					return nil
				}
				rel = filepath.ToSlash(rel)
				if ignore.Match(rel, true) {
					return filepath.SkipDir
				}
			}
			return nil
		}
		// Exclude non-attachable binary files from @-mention suggestions.
		if skipIndexedPath(d.Name()) {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if ignore != nil && ignore.Match(rel, false) {
			return nil
		}
		files = append(files, rel)
		if len(files) >= limit {
			return filepath.SkipAll
		}
		return nil
	})
	slices.Sort(files)
	return files
}
