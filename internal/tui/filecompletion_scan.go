package tui

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/tools"
)

const (
	atMentionMaxDepth    = 8
	atMentionMaxFiles    = 10000
	atMentionRefreshTTL  = 5 * time.Second
	atMentionLoadTimeout = 2 * time.Second
)

type atMentionFilesLoadedMsg struct {
	files      []string
	workDir    string
	generation uint64
}

func loadAtMentionFilesForWorkDir(workDir string, generation uint64, limit int) tea.Cmd {
	return func() tea.Msg {
		return atMentionFilesLoadedMsg{
			files:      loadAtMentionFileListInDir(workDir, limit),
			workDir:    workDir,
			generation: generation,
		}
	}
}

func loadAtMentionFileListInDir(workDir string, limit int) []string {
	if limit <= 0 {
		return nil
	}
	if strings.TrimSpace(workDir) == "" {
		workDir = "."
	}
	ctx, cancel := context.WithTimeout(context.Background(), atMentionLoadTimeout)
	defer cancel()
	if files, ok := loadAtMentionGitFiles(ctx, workDir, limit); ok {
		return files
	}
	return loadAtMentionWalkFilesInDir(ctx, workDir, limit)
}

func loadAtMentionGitFiles(ctx context.Context, workDir string, limit int) ([]string, bool) {
	tracked, err := gitCommand(ctx, workDir, "ls-files", "--recurse-submodules")
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
			path := normalizeAtMentionGitPath(line)
			if path == "" || seen[path] || skipAtMentionIndexedPath(path) || !atMentionIndexedPathExists(workDir, path) {
				continue
			}
			seen[path] = true
			files = append(files, path)
		}
	}
	add(tracked)
	if len(files) < limit {
		if untracked, err := gitCommand(ctx, workDir, "ls-files", "--others", "--exclude-standard"); err == nil {
			add(untracked)
		}
	}
	slices.Sort(files)
	return files, true
}

func normalizeAtMentionGitPath(path string) string {
	path = strings.TrimSpace(path)
	path = strings.TrimSuffix(path, "\r")
	path = filepath.ToSlash(path)
	path = strings.TrimPrefix(path, "./")
	if path == "." || strings.HasPrefix(path, "../") || filepath.IsAbs(path) {
		return ""
	}
	return path
}

func atMentionIndexedPathExists(workDir, path string) bool {
	if workDir == "" {
		workDir = "."
	}
	info, err := os.Stat(filepath.Join(workDir, filepath.FromSlash(path)))
	return err == nil && !info.IsDir()
}

func skipAtMentionIndexedPath(path string) bool {
	if path == "" {
		return true
	}
	for part := range strings.SplitSeq(path, "/") {
		if part == "" || strings.HasPrefix(part, ".") || tools.IsSkippedDirName(part) {
			return true
		}
		switch part {
		case "node_modules", ".idea", ".vscode", "vendor", "dist", "build":
			return true
		}
	}
	return tools.IsBinaryExtension(path) && attachmentKindForPath(path) == ""
}

func loadAtMentionWalkFilesInDir(ctx context.Context, workDir string, limit int) []string {
	var files []string
	if strings.TrimSpace(workDir) == "" {
		workDir = "."
	}
	root := workDir
	if abs, err := filepath.Abs(workDir); err == nil {
		root = abs
	}
	ignore := tools.NewGitIgnoreMatcher(workDir)
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
				if tools.IsSkippedDirName(d.Name()) {
					return filepath.SkipDir
				}
				switch d.Name() {
				case "node_modules", ".idea", ".vscode", "vendor", "dist", "build":
					return filepath.SkipDir
				}
			}
			if strings.Count(path, string(os.PathSeparator))-strings.Count(root, string(os.PathSeparator)) >= atMentionMaxDepth {
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
		if skipAtMentionIndexedPath(d.Name()) {
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

func (m *Model) startAtMentionFileLoad() tea.Cmd {
	return m.startAtMentionFileLoadIfStale(time.Now())
}

func (m *Model) startAtMentionFileLoadIfStale(now time.Time) tea.Cmd {
	if m.atMentionLoading {
		return nil
	}
	if m.atMentionLoaded && now.Sub(m.atMentionLoadedAt) < atMentionRefreshTTL {
		return nil
	}
	m.atMentionLoading = true
	return loadAtMentionFilesForWorkDir(m.workingDir, m.workingDirGeneration, atMentionMaxFiles)
}
