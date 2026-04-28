package tui

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/keakon/chord/internal/tools"
)

const (
	atMentionMaxDepth = 8
	atMentionMaxFiles = 10000
)

type atMentionFilesLoadedMsg struct {
	files []string
}

func loadAtMentionFiles() tea.Cmd {
	return func() tea.Msg {
		var files []string
		ignore := tools.NewGitIgnoreMatcher(".")
		_ = filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if path != "." && strings.HasPrefix(d.Name(), ".") {
					return filepath.SkipDir
				}
				if tools.IsSkippedDirName(d.Name()) {
					return filepath.SkipDir
				}
				switch d.Name() {
				case "node_modules", ".idea", ".vscode", "vendor", "dist", "build":
					return filepath.SkipDir
				}
				if strings.Count(path, string(os.PathSeparator)) >= atMentionMaxDepth {
					return filepath.SkipDir
				}
				// Honor .gitignore at the walk root so project-specific
				// generated / cached directories (e.g. `out/`, `target/`,
				// `coverage/`) don't pollute @-mention suggestions.
				if ignore != nil && path != "." {
					rel := filepath.ToSlash(strings.TrimPrefix(path, "./"))
					if ignore.Match(rel, true) {
						return filepath.SkipDir
					}
				}
				return nil
			}
			// Exclude binary files from @-mention suggestions. The list is
			// extension-only (zero IO) and shared with Grep's binary fast-path
			// so the two filters stay in sync.
			if tools.IsBinaryExtension(d.Name()) {
				return nil
			}
			rel := filepath.ToSlash(strings.TrimPrefix(path, "./"))
			if ignore != nil && ignore.Match(rel, false) {
				return nil
			}
			files = append(files, rel)
			if len(files) >= atMentionMaxFiles {
				return filepath.SkipAll
			}
			return nil
		})
		slices.Sort(files)
		return atMentionFilesLoadedMsg{files: files}
	}
}

func (m *Model) startAtMentionFileLoad() tea.Cmd {
	if m.atMentionLoaded || m.atMentionLoading {
		return nil
	}
	m.atMentionLoading = true
	return loadAtMentionFiles()
}
